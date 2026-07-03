package url

import (
	"path/filepath"
	"testing"
)

func TestParseRepositoryURL(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantHost  string
		wantOwner string
		wantRepo  string
		wantFull  string
		wantErr   bool
	}{
		{
			name:      "standard github https",
			input:     "https://github.com/user/repo",
			wantHost:  "github.com",
			wantOwner: "user",
			wantRepo:  "repo",
			wantFull:  "github.com/user/repo",
		},
		{
			name:      "github https with .git suffix",
			input:     "https://github.com/user/repo.git",
			wantHost:  "github.com",
			wantOwner: "user",
			wantRepo:  "repo",
			wantFull:  "github.com/user/repo",
		},
		{
			name:      "github ssh format",
			input:     "git@github.com:user/repo.git",
			wantHost:  "github.com",
			wantOwner: "user",
			wantRepo:  "repo",
			wantFull:  "github.com/user/repo",
		},
		{
			name:      "gitlab nested group - 3 levels",
			input:     "https://gitlab.com/org/team/repo",
			wantHost:  "gitlab.com",
			wantOwner: "org",
			wantRepo:  "repo",
			wantFull:  "gitlab.com/org/team/repo",
		},
		{
			name:      "gitlab nested group - 4 levels",
			input:     "https://gitlab.com/org/team/suborg/repo",
			wantHost:  "gitlab.com",
			wantOwner: "org",
			wantRepo:  "repo",
			wantFull:  "gitlab.com/org/team/suborg/repo",
		},
		{
			name:      "gitlab nested group with .git suffix",
			input:     "https://gitlab.com/org/team/suborg/repo.git",
			wantHost:  "gitlab.com",
			wantOwner: "org",
			wantRepo:  "repo",
			wantFull:  "gitlab.com/org/team/suborg/repo",
		},
		{
			name:      "gitlab nested group ssh format",
			input:     "git@gitlab.com:org/team/suborg/repo.git",
			wantHost:  "gitlab.com",
			wantOwner: "org",
			wantRepo:  "repo",
			wantFull:  "gitlab.com/org/team/suborg/repo",
		},
		{
			name:      "SSH config alias",
			input:     "workgit:myorg/myrepo.git",
			wantHost:  "workgit",
			wantOwner: "myorg",
			wantRepo:  "myrepo",
			wantFull:  "workgit/myorg/myrepo",
		},
		{
			name:      "SSH config alias without .git",
			input:     "myalias:owner/repo",
			wantHost:  "myalias",
			wantOwner: "owner",
			wantRepo:  "repo",
			wantFull:  "myalias/owner/repo",
		},
		{
			name:      "SSH config alias with nested path",
			input:     "myhost:org/team/repo.git",
			wantHost:  "myhost",
			wantOwner: "org",
			wantRepo:  "repo",
			wantFull:  "myhost/org/team/repo",
		},
		{
			name:      "git@ with SSH config alias",
			input:     "git@workgit:org/repo.git",
			wantHost:  "workgit",
			wantOwner: "org",
			wantRepo:  "repo",
			wantFull:  "workgit/org/repo",
		},
		{
			name:      "URL with port number",
			input:     "localhost:8080/user/repo",
			wantHost:  "localhost:8080",
			wantOwner: "user",
			wantRepo:  "repo",
			wantFull:  "localhost:8080/user/repo",
		},
		{
			name:    "single path component is invalid",
			input:   "https://github.com/user",
			wantErr: true,
		},
		{
			name:    "no host",
			input:   "/user/repo",
			wantErr: true,
		},
		{
			name:    "empty path component is invalid",
			input:   "https://github.com/user//repo",
			wantErr: true,
		},
		{
			name:    "dot path component is invalid",
			input:   "https://github.com/user/./repo",
			wantErr: true,
		},
		{
			name:    "dotdot path component is invalid",
			input:   "https://github.com/user/../repo",
			wantErr: true,
		},
		{
			name:    "escaped dotdot path component is invalid",
			input:   "https://github.com/user/%2e%2e/repo",
			wantErr: true,
		},
		{
			name:    "path component with platform separator is invalid",
			input:   "https://github.com/user/team\\repo.git",
			wantErr: true,
		},
		{
			name:    "dotdot host is invalid",
			input:   "https://../user/repo",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := ParseRepositoryURL(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("ParseRepositoryURL(%s) expected error, got nil", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseRepositoryURL(%s) unexpected error: %v", tt.input, err)
			}
			if info.Host != tt.wantHost {
				t.Errorf("Host = %q, want %q", info.Host, tt.wantHost)
			}
			if info.Owner != tt.wantOwner {
				t.Errorf("Owner = %q, want %q", info.Owner, tt.wantOwner)
			}
			if info.Repository != tt.wantRepo {
				t.Errorf("Repository = %q, want %q", info.Repository, tt.wantRepo)
			}
			if info.FullPath != tt.wantFull {
				t.Errorf("FullPath = %q, want %q", info.FullPath, tt.wantFull)
			}
		})
	}
}

func TestIsSCPLikeURL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "basic SSH config alias",
			input:    "workgit:myorg/myrepo.git",
			expected: true,
		},
		{
			name:     "alias without .git",
			input:    "myalias:owner/repo",
			expected: true,
		},
		{
			name:     "port number URL",
			input:    "localhost:8080/user/repo",
			expected: false,
		},
		{
			name:     "port only without path",
			input:    "localhost:8080",
			expected: false,
		},
		{
			name:     "URL with scheme",
			input:    "https://github.com/user/repo",
			expected: false,
		},
		{
			name:     "git@ prefix",
			input:    "git@github.com:user/repo.git",
			expected: false,
		},
		{
			name:     "empty path after colon",
			input:    "host:",
			expected: false,
		},
		{
			name:     "no colon",
			input:    "github.com/user/repo",
			expected: false,
		},
		{
			name:     "colon followed by slash",
			input:    "host:/user/repo",
			expected: false,
		},
		{
			name:     "bracketed IPv6 address",
			input:    "[::1]:8080/user/repo",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isSCPLikeURL(tt.input)
			if result != tt.expected {
				t.Errorf("isSCPLikeURL(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestNormalizeURL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "git@ format",
			input:    "git@github.com:user/repo.git",
			expected: "https://github.com/user/repo.git",
		},
		{
			name:     "ssh://git@ format",
			input:    "ssh://git@github.com:user/repo.git",
			expected: "https://github.com/user/repo.git",
		},
		{
			name:     "https format unchanged",
			input:    "https://github.com/user/repo.git",
			expected: "https://github.com/user/repo.git",
		},
		{
			name:     "http format unchanged",
			input:    "http://github.com/user/repo.git",
			expected: "http://github.com/user/repo.git",
		},
		{
			name:     "plain url gets https prefix",
			input:    "github.com/user/repo.git",
			expected: "https://github.com/user/repo.git",
		},
		{
			name:     "SSH config alias SCP format",
			input:    "workgit:myorg/myrepo.git",
			expected: "https://workgit/myorg/myrepo.git",
		},
		{
			name:     "SSH config alias without .git",
			input:    "myalias:owner/repo",
			expected: "https://myalias/owner/repo",
		},
		{
			name:     "SSH config alias with nested path",
			input:    "myhost:org/team/repo.git",
			expected: "https://myhost/org/team/repo.git",
		},
		{
			name:     "URL with port number is not SCP",
			input:    "localhost:8080/user/repo",
			expected: "https://localhost:8080/user/repo",
		},
		{
			name:     "git@ with SSH config alias",
			input:    "git@workgit:org/repo.git",
			expected: "https://workgit/org/repo.git",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeURL(tt.input)
			if result != tt.expected {
				t.Errorf("normalizeURL(%s) = %s, want %s", tt.input, result, tt.expected)
			}
		})
	}
}

func TestGenerateParseWorktreePathPreservesNestedNamespace(t *testing.T) {
	baseDir := t.TempDir()
	repoInfo := &RepositoryInfo{
		Host:       "gitlab.com",
		Owner:      "org",
		Repository: "service",
		FullPath:   "gitlab.com/org/team/service",
	}

	worktreePath := GenerateWorktreePath(baseDir, repoInfo, "feature/read-api")
	gotInfo, gotBranch, err := ParseWorktreePath(worktreePath, baseDir)
	if err != nil {
		t.Fatalf("ParseWorktreePath(%q, %q) unexpected error: %v", worktreePath, baseDir, err)
	}

	if gotInfo.Host != repoInfo.Host {
		t.Errorf("Host = %q, want %q", gotInfo.Host, repoInfo.Host)
	}
	if gotInfo.Owner != repoInfo.Owner {
		t.Errorf("Owner = %q, want %q", gotInfo.Owner, repoInfo.Owner)
	}
	if gotInfo.Repository != repoInfo.Repository {
		t.Errorf("Repository = %q, want %q", gotInfo.Repository, repoInfo.Repository)
	}
	if gotInfo.FullPath != repoInfo.FullPath {
		t.Errorf("FullPath = %q, want %q", gotInfo.FullPath, repoInfo.FullPath)
	}
	if gotBranch != "feature-read-api" {
		t.Errorf("branch = %q, want %q", gotBranch, "feature-read-api")
	}
	if worktreePath != filepath.Join(baseDir, "gitlab.com", "org", "team", "service", "feature-read-api") {
		t.Errorf("worktreePath = %q, want nested namespace under base", worktreePath)
	}
}
