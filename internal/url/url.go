// Package url provides utilities for handling repository URLs and generating directory paths.
package url

import (
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"strings"

	"go.kenn.io/kwt/internal/utils"
)

// RepositoryInfo contains parsed repository information.
type RepositoryInfo struct {
	Host       string // e.g., "github.com"
	Owner      string // e.g., "user1"
	Repository string // e.g., "myapp"
	FullPath   string // e.g., "github.com/user1/myapp"
}

// ParseRepositoryURL parses a git repository URL and extracts host, owner, and repository name.
func ParseRepositoryURL(repoURL string) (*RepositoryInfo, error) {
	// Handle different URL formats
	repoURL, err := normalizeURL(repoURL)
	if err != nil {
		return nil, err
	}

	parsedURL, err := url.Parse(repoURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse repository URL: %w", err)
	}

	host := parsedURL.Host
	if host == "" {
		return nil, fmt.Errorf("no host found in URL: %s", repoURL)
	}
	if strings.HasSuffix(host, ":") {
		// An empty port means the pre-normalization input smuggled a scheme
		// or rooted colon path into the authority (e.g. "file:/tmp/repo" or
		// "host:/path"), which must not become a repository identity.
		return nil, fmt.Errorf("invalid host %q in URL %s: empty port", host, repoURL)
	}
	if err := validateRepositoryPathComponent("host", host); err != nil {
		return nil, err
	}

	return repositoryInfoFromParts(host, strings.Split(strings.Trim(parsedURL.Path, "/"), "/"))
}

// repositoryInfoFromParts assembles a RepositoryInfo from a validated
// authority and slash-split path components, trimming the ".git" suffix from
// the final component. It is the single component pipeline behind both URL
// parsing and canonical authority-slug parsing.
func repositoryInfoFromParts(authority string, parts []string) (*RepositoryInfo, error) {
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid repository path: %s", strings.Join(parts, "/"))
	}
	parts[len(parts)-1] = strings.TrimSuffix(parts[len(parts)-1], ".git")
	for _, part := range parts {
		if err := validateRepositoryPathComponent("repository path component", part); err != nil {
			return nil, err
		}
	}
	return &RepositoryInfo{
		Host:       authority,
		Owner:      parts[0],
		Repository: parts[len(parts)-1],
		FullPath:   authority + "/" + strings.Join(parts, "/"),
	}, nil
}

func validateRepositoryPathComponent(label, component string) error {
	switch {
	case component == "":
		return fmt.Errorf("invalid %s: empty component", label)
	case component == "." || component == "..":
		return fmt.Errorf("invalid %s %q", label, component)
	case strings.ContainsAny(component, `/\`):
		return fmt.Errorf("invalid %s %q: contains path separator", label, component)
	default:
		return nil
	}
}

// GenerateWorktreePath creates a worktree path based on repository info and branch name.
func GenerateWorktreePath(baseDir string, repoInfo *RepositoryInfo, branch string) string {
	// Sanitize branch name for filesystem
	safeBranch := sanitizeBranchName(branch)
	filesystemInfo := RepositoryInfoForFilesystem(repoInfo)
	return filepath.Join(baseDir, filesystemInfo.FullPath, safeBranch)
}

// RepositoryInfoForFilesystem returns a copy whose authority is safe to use
// as a path component on every supported platform. Canonical identities keep
// ports and bracketed IPv6 verbatim; percent-encoding the authority only at
// the filesystem boundary preserves that identity while avoiding Windows'
// reserved path characters and collisions with literal escape sequences.
func RepositoryInfoForFilesystem(repoInfo *RepositoryInfo) *RepositoryInfo {
	if repoInfo == nil {
		return nil
	}

	filesystemInfo := *repoInfo
	filesystemInfo.Host = encodeFilesystemComponent(repoInfo.Host)
	authority, remainder, ok := strings.Cut(repoInfo.FullPath, "/")
	if ok {
		filesystemInfo.FullPath = encodeFilesystemComponent(authority) + "/" + remainder
	} else {
		filesystemInfo.FullPath = encodeFilesystemComponent(repoInfo.FullPath)
	}
	return &filesystemInfo
}

func encodeFilesystemComponent(component string) string {
	const hex = "0123456789ABCDEF"
	var encoded strings.Builder
	for i := 0; i < len(component); i++ {
		char := component[i]
		if (char >= 'a' && char <= 'z') ||
			(char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') ||
			strings.ContainsRune("-._~", rune(char)) {
			encoded.WriteByte(char)
			continue
		}
		encoded.WriteByte('%')
		encoded.WriteByte(hex[char>>4])
		encoded.WriteByte(hex[char&0x0f])
	}
	return encoded.String()
}

// normalizeURL converts supported git remote formats to a standard HTTP(S)
// format for parsing. Explicit URI schemes are handled before SCP-style
// syntax: the network git schemes (https, http, ssh, git, git+ssh) are
// accepted, and any other scheme (file:, etc.) is rejected so non-network
// remotes fall through to the callers' local-path fallback handling instead
// of minting bogus host/owner identities.
func normalizeURL(repoURL string) (string, error) {
	if IsRemoteHelperURL(repoURL) {
		return "", fmt.Errorf(
			"unsupported remote-helper syntax in repository URL %s (git reads <transport>:: as a helper invocation, not a host)",
			repoURL)
	}
	if strings.Contains(repoURL, "://") {
		return normalizeExplicitNetworkURL(repoURL)
	}
	if strings.HasPrefix(strings.ToLower(repoURL), "file:") {
		return "", fmt.Errorf("unsupported scheme %q in repository URL %s", "file", repoURL)
	}

	if host, remotePath, user, ok := splitSCPLikeURL(repoURL); ok {
		if strings.Contains(user, ":") {
			return "", fmt.Errorf("invalid SCP-style userinfo in repository URL %s", repoURL)
		}
		if looksLikeCredentialBearingSCPPath(remotePath) {
			return "", fmt.Errorf("invalid SCP-style userinfo in repository URL %s", repoURL)
		}
		return fmt.Sprintf("https://%s/%s", host, strings.TrimLeft(remotePath, "/")), nil
	}

	// Ensure https:// prefix
	if !strings.HasPrefix(repoURL, "http://") && !strings.HasPrefix(repoURL, "https://") {
		repoURL = "https://" + repoURL
	}

	return repoURL, nil
}

// normalizeExplicitNetworkURL parses scheme-bearing remotes with net/url so
// userinfo, numeric ports, and bracketed IPv6 authorities remain distinct
// from path colons. Only explicit URLs may carry a port; scheme-less
// colon-before-slash values are Git's SCP syntax and take the other path.
func normalizeExplicitNetworkURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("failed to parse repository URL: %w", err)
	}
	if !isNetworkGitURLScheme(parsed.Scheme) {
		return "", fmt.Errorf(
			"unsupported scheme %q in repository URL %s (supported: https, http, ssh, git, git+ssh)",
			parsed.Scheme, raw)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("no host found in URL: %s", raw)
	}

	lowered := strings.ToLower(parsed.Scheme)
	if lowered == "http" || lowered == "https" {
		return raw, nil
	}
	if parsed.User != nil {
		if _, hasPassword := parsed.User.Password(); hasPassword {
			return "", fmt.Errorf("invalid userinfo in repository URL %s", raw)
		}
		parsed.User = nil
	}
	parsed.Scheme = "https"
	return parsed.String(), nil
}

// IsRemoteHelperURL reports whether repoURL uses git's remote-helper syntax
// <transport>::<address> (gitremote-helpers(7)): URL-scheme characters from
// the start of the string followed by "::", matching git's own
// is_urlschemechar check in transport.c. Git dispatches these to a helper
// command and the address is an arbitrary command line (for example a
// git-remote-ext invocation embedding credentials or key paths), never
// SCP host:path syntax, so it must not be parsed into a repository identity.
// Bracketed IPv6 forms never match: their "::" is preceded by non-scheme
// characters ("[", ":"). A leading "::" (empty transport) also matches and
// fails closed, as git reads it as a helper invocation too.
func IsRemoteHelperURL(repoURL string) bool {
	prefix, _, found := strings.Cut(repoURL, "::")
	if !found {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		if !isURLSchemeChar(i == 0, prefix[i]) {
			return false
		}
	}
	return true
}

func isURLSchemeChar(first bool, c byte) bool {
	isAlpha := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
	if first || isAlpha {
		return isAlpha
	}
	return (c >= '0' && c <= '9') || c == '+' || c == '-' || c == '.'
}

// splitSCPLikeURL separates [user@]host:path while ignoring colons inside a
// bracketed IPv6 host. ok is false when the delimiter is absent, after the
// first slash, or has an empty host/path.
func splitSCPLikeURL(raw string) (host, remotePath, user string, ok bool) {
	if raw == "" || strings.Contains(raw, "://") {
		return "", "", "", false
	}

	inBrackets := false
	delimiter := -1
	for i := 0; i < len(raw); i++ {
		switch raw[i] {
		case '[':
			inBrackets = true
		case ']':
			inBrackets = false
		case ':':
			if !inBrackets {
				delimiter = i
				i = len(raw)
			}
		case '/':
			i = len(raw)
		}
	}
	if delimiter < 0 {
		return "", "", "", false
	}
	authority := raw[:delimiter]
	host = authority
	if at := strings.LastIndex(authority, "@"); at >= 0 {
		user = authority[:at]
		host = authority[at+1:]
	}
	remotePath = raw[delimiter+1:]
	if host == "" || remotePath == "" {
		return "", "", "", false
	}
	return host, remotePath, user, true
}

// looksLikeCredentialBearingSCPPath preserves the existing no-credentials
// contract for ambiguous user:token@host:path input. Git reads the first
// colon as the SCP delimiter, but publishing the resulting path would expose
// a token-shaped value. A plain @ in a path component remains valid.
func looksLikeCredentialBearingSCPPath(remotePath string) bool {
	firstComponent, _, _ := strings.Cut(remotePath, "/")
	at := strings.IndexByte(firstComponent, '@')
	return at >= 0 && strings.Contains(firstComponent[at+1:], ":")
}

// sanitizeBranchName converts branch names to filesystem-safe names.
func sanitizeBranchName(branch string) string {
	return utils.SanitizeForFilesystem(branch)
}

// ParseWorktreePath extracts repository info and branch from a worktree path.
func ParseWorktreePath(worktreePath, baseDir string) (*RepositoryInfo, string, error) {
	// Remove base directory from path
	relPath, err := filepath.Rel(baseDir, worktreePath)
	if err != nil {
		return nil, "", fmt.Errorf("failed to get relative path: %w", err)
	}

	// Split into components: host/owner[/namespace...]/repo/branch
	parts := strings.Split(filepath.ToSlash(relPath), "/")
	if len(parts) < 4 {
		return nil, "", fmt.Errorf("invalid worktree path structure: %s", relPath)
	}

	fullPathParts := parts[:len(parts)-1]
	branch := parts[len(parts)-1]
	host := fullPathParts[0]
	owner := fullPathParts[1]
	repository := fullPathParts[len(fullPathParts)-1]

	repoInfo := &RepositoryInfo{
		Host:       host,
		Owner:      owner,
		Repository: repository,
		FullPath:   path.Join(fullPathParts...),
	}

	return repoInfo, branch, nil
}
