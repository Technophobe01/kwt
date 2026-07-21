package pullrequest

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-github/v89/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveGitHubTokenPrefersEnvironment(t *testing.T) {
	called := false
	token, err := ResolveGitHubToken(context.Background(),
		func(name string) string {
			assert.Equal(t, "KWT_GITHUB_TOKEN", name)
			return " env-token\n"
		},
		func(context.Context, string, ...string) ([]byte, error) {
			called = true
			return nil, errors.New("must not run")
		},
	)
	require.NoError(t, err)
	assert.Equal(t, "env-token", token)
	assert.False(t, called)
}

func TestResolveGitHubTokenFallsBackToGHWithoutPrompt(t *testing.T) {
	token, err := ResolveGitHubToken(context.Background(), func(string) string { return "" },
		func(_ context.Context, name string, args ...string) ([]byte, error) {
			assert.Equal(t, "gh", name)
			assert.Equal(t, []string{"auth", "token"}, args)
			return []byte("gh-token\n"), nil
		})
	require.NoError(t, err)
	assert.Equal(t, "gh-token", token)
}

func TestResolveGitHubTokenReportsAuthenticationFailureWithoutLeakingOutput(t *testing.T) {
	_, err := ResolveGitHubToken(context.Background(), func(string) string { return "" },
		func(context.Context, string, ...string) ([]byte, error) {
			return []byte("sensitive-test-output"), errors.New("exit 1")
		})

	assertErrorCode(t, err, CodeAuthentication)
	assert.NotContains(t, err.Error(), "sensitive-test-output")
}

func TestGitHubProviderMapsPullRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/acme/widget/pulls", r.URL.Path)
		assert.Equal(t, "all", r.URL.Query().Get("state"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[{"number":17,"html_url":"https://github.com/acme/widget/pull/17","title":"A draft",`+
			`"user":{"login":"octocat"},"draft":true,"state":"open",`+
			`"head":{"ref":"feature/widgets","sha":"0123456789abcdef0123456789abcdef01234567",`+
			`"repo":{"name":"widget","full_name":"octocat/widget","html_url":"https://github.com/octocat/widget","clone_url":"https://github.com/octocat/widget.git"}},`+
			`"base":{"ref":"main","repo":{"name":"widget","full_name":"acme/widget","html_url":"https://github.com/acme/widget","clone_url":"https://github.com/acme/widget.git"}}}]`)
	}))
	defer server.Close()

	baseURL := server.URL + "/"
	client, err := github.NewClient(github.WithURLs(&baseURL, nil))
	require.NoError(t, err)
	provider := NewGitHubProvider(client)

	prs, err := provider.List(context.Background(), Repository{Provider: "github", Identity: "github.com/acme/widget", Host: "github.com", Owner: "acme", Name: "widget"}, "all")

	require.NoError(t, err)
	require.Len(t, prs, 1)
	pr := prs[0]
	assert.Equal(t, OpaqueID("github.com/acme/widget", 17), pr.ID)
	assert.Equal(t, "octocat", pr.Author)
	assert.True(t, pr.Draft)
	assert.Equal(t, "feature/widgets", pr.Source.Name)
	assert.Equal(t, "github.com/octocat/widget", pr.Source.Repository.Identity)
	assert.Equal(t, "main", pr.Target.Name)
	assert.Equal(t, "github.com/acme/widget", pr.Repository.Identity)
}

func TestGitHubProviderClassifiesFailures(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		want      ErrorCode
		retryable bool
	}{
		{name: "authentication", status: http.StatusUnauthorized, body: `{"message":"Bad credentials"}`, want: CodeAuthentication},
		{name: "missing", status: http.StatusNotFound, body: `{"message":"Not Found"}`, want: CodeNotFound},
		{name: "network", status: http.StatusServiceUnavailable, body: `{"message":"try later"}`, want: CodeNetwork, retryable: true},
		{name: "malformed", status: http.StatusOK, body: `{broken`, want: CodeMalformedResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			baseURL := server.URL + "/"
			client, err := github.NewClient(github.WithURLs(&baseURL, nil))
			require.NoError(t, err)
			provider := NewGitHubProvider(client)

			_, err = provider.List(context.Background(), Repository{Owner: "acme", Name: "widget", Identity: "github.com/acme/widget"}, "open")

			var typed *Error
			require.ErrorAs(t, err, &typed)
			assert.Equal(t, tc.want, typed.Code)
			assert.Equal(t, tc.retryable, typed.Retryable)
		})
	}
}

func TestGitHubProviderReportsDeletedHeadRepository(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[{"number":1,"html_url":"https://github.com/acme/widget/pull/1","title":"gone",`+
			`"user":{"login":"octocat"},"state":"open","head":{"ref":"gone","sha":"abc","repo":null},`+
			`"base":{"ref":"main","repo":{"name":"widget","full_name":"acme/widget","clone_url":"https://github.com/acme/widget.git"}}}]`)
	}))
	defer server.Close()
	baseURL := server.URL + "/"
	client, err := github.NewClient(github.WithURLs(&baseURL, nil))
	require.NoError(t, err)
	provider := NewGitHubProvider(client)

	_, err = provider.List(context.Background(), Repository{Owner: "acme", Name: "widget", Identity: "github.com/acme/widget"}, "open")

	assertErrorCode(t, err, CodeInaccessibleHead)
}

func TestGitHubProviderRejectsMalformedSuccessfulResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[{"number":1,"head":{},"base":null}]`)
	}))
	defer server.Close()
	baseURL := server.URL + "/"
	client, err := github.NewClient(github.WithURLs(&baseURL, nil))
	require.NoError(t, err)
	provider := NewGitHubProvider(client)

	_, err = provider.List(context.Background(), Repository{Owner: "acme", Name: "widget", Identity: "github.com/acme/widget"}, "open")

	assertErrorCode(t, err, CodeMalformedResponse)
	assert.True(t, strings.Contains(err.Error(), "missing") || strings.Contains(err.Error(), "malformed"))
}
