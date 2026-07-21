package pullrequest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/google/go-github/v89/github"
)

type commandOutput func(context.Context, string, ...string) ([]byte, error)

func ResolveGitHubToken(ctx context.Context, getenv func(string) string, run commandOutput) (string, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	if token := strings.TrimSpace(getenv("KWT_GITHUB_TOKEN")); token != "" {
		return token, nil
	}
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).Output()
		}
	}
	output, err := run(ctx, "gh", "auth", "token")
	if err != nil {
		return "", NewError(CodeAuthentication,
			"GitHub authentication is unavailable; set KWT_GITHUB_TOKEN or run gh auth login", false, err)
	}
	token := strings.TrimSpace(string(output))
	if token == "" {
		return "", NewError(CodeAuthentication,
			"GitHub authentication returned an empty token; set KWT_GITHUB_TOKEN or run gh auth login", false, nil)
	}
	return token, nil
}

func NewAuthenticatedGitHubProvider(ctx context.Context) (*GitHubProvider, error) {
	token, err := ResolveGitHubToken(ctx, nil, nil)
	if err != nil {
		return nil, err
	}
	client, err := github.NewClient(github.WithAuthToken(token))
	if err != nil {
		return nil, NewError(CodeAuthentication, "failed to configure GitHub authentication", false, err)
	}
	return NewGitHubProvider(client), nil
}

type GitHubProvider struct {
	client *github.Client
}

func NewGitHubProvider(client *github.Client) *GitHubProvider {
	return &GitHubProvider{client: client}
}

func (p *GitHubProvider) List(ctx context.Context, repository Repository, state string) ([]PullRequest, error) {
	if p == nil || p.client == nil {
		return nil, NewError(CodeNetwork, "GitHub client is not configured", false, nil)
	}
	if state == "" {
		state = "open"
	}
	options := &github.PullRequestListOptions{State: state, ListOptions: github.ListOptions{PerPage: 100}}
	var result []PullRequest
	for {
		prs, response, err := p.client.PullRequests.List(ctx, repository.Owner, repository.Name, options)
		if err != nil {
			return nil, classifyGitHubError(err, "list pull requests")
		}
		for _, githubPR := range prs {
			pr, mapErr := mapGitHubPullRequest(githubPR)
			if mapErr != nil {
				return nil, mapErr
			}
			result = append(result, pr)
		}
		if response == nil || response.NextPage == 0 {
			break
		}
		options.Page = response.NextPage
	}
	return result, nil
}

func (p *GitHubProvider) Get(ctx context.Context, repository Repository, number int) (PullRequest, error) {
	if p == nil || p.client == nil {
		return PullRequest{}, NewError(CodeNetwork, "GitHub client is not configured", false, nil)
	}
	githubPR, _, err := p.client.PullRequests.Get(ctx, repository.Owner, repository.Name, number)
	if err != nil {
		return PullRequest{}, classifyGitHubError(err, "get pull request")
	}
	return mapGitHubPullRequest(githubPR)
}

func mapGitHubPullRequest(value *github.PullRequest) (PullRequest, error) {
	if value == nil || value.Number == nil || value.HTMLURL == nil || value.Title == nil ||
		value.User == nil || value.User.Login == nil || value.State == nil || value.Head == nil ||
		value.Base == nil || value.Base.Repo == nil {
		return PullRequest{}, NewError(CodeMalformedResponse,
			"GitHub returned a pull request with missing required fields", false, nil)
	}
	if value.Head.Repo == nil {
		return PullRequest{}, NewError(CodeInaccessibleHead,
			fmt.Sprintf("pull request #%d has no accessible head repository", value.GetNumber()), false, nil)
	}
	base, err := mapGitHubRepository(value.Base.Repo)
	if err != nil {
		return PullRequest{}, err
	}
	head, err := mapGitHubRepository(value.Head.Repo)
	if err != nil {
		return PullRequest{}, err
	}
	if value.Head.Ref == nil || value.Head.SHA == nil || value.Base.Ref == nil {
		return PullRequest{}, NewError(CodeMalformedResponse,
			"GitHub returned a pull request with incomplete branch information", false, nil)
	}
	return PullRequest{
		ID: OpaqueID(base.Identity, value.GetNumber()), Provider: "github", Repository: base,
		Number: value.GetNumber(), URL: value.GetHTMLURL(), Title: value.GetTitle(),
		Author: value.User.GetLogin(), Source: Branch{Name: value.Head.GetRef(), Repository: head},
		Target: Branch{Name: value.Base.GetRef(), Repository: base}, Draft: value.GetDraft(),
		State: value.GetState(), HeadSHA: value.Head.GetSHA(),
	}, nil
}

func mapGitHubRepository(repository *github.Repository) (Repository, error) {
	if repository == nil || repository.FullName == nil || repository.Name == nil || repository.CloneURL == nil {
		return Repository{}, NewError(CodeMalformedResponse,
			"GitHub returned a repository with missing required fields", false, nil)
	}
	owner, name, ok := strings.Cut(repository.GetFullName(), "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return Repository{}, NewError(CodeMalformedResponse,
			"GitHub returned a malformed repository identity", false, nil)
	}
	return Repository{
		Provider: "github", Host: "github.com", Owner: owner, Name: name,
		Identity: NormalizeRepositoryIdentity("github.com/" + owner + "/" + name), CloneURL: repository.GetCloneURL(),
	}, nil
}

func classifyGitHubError(err error, operation string) error {
	var responseError *github.ErrorResponse
	if errors.As(err, &responseError) && responseError.Response != nil {
		switch responseError.Response.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return NewError(CodeAuthentication, "GitHub authentication failed", false, err)
		case http.StatusNotFound:
			return NewError(CodeNotFound, "GitHub pull request or repository was not found", false, err)
		default:
			if responseError.Response.StatusCode >= 500 {
				return NewError(CodeNetwork, "GitHub is temporarily unavailable", true, err)
			}
		}
	}
	var syntaxError *json.SyntaxError
	if errors.As(err, &syntaxError) {
		return NewError(CodeMalformedResponse, "GitHub returned malformed JSON", false, err)
	}
	var urlError *url.Error
	var netError net.Error
	if errors.As(err, &urlError) || errors.As(err, &netError) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return NewError(CodeNetwork, fmt.Sprintf("GitHub network failure while attempting to %s", operation), true, err)
	}
	return NewError(CodeNetwork, fmt.Sprintf("GitHub request failed while attempting to %s", operation), false, err)
}
