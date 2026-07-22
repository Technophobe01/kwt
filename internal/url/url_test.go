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
			name:      "at sign in SCP path",
			input:     "mirror:path@tenant/org/repo.git",
			wantHost:  "mirror",
			wantOwner: "path@tenant",
			wantRepo:  "repo",
			wantFull:  "mirror/path@tenant/org/repo",
		},
		{
			name:      "numeric first path component is SCP syntax",
			input:     "localhost:8080/user/repo",
			wantHost:  "localhost",
			wantOwner: "8080",
			wantRepo:  "repo",
			wantFull:  "localhost/8080/user/repo",
		},
		{
			name:      "SCP absolute path",
			input:     "host:/srv/repo.git",
			wantHost:  "host",
			wantOwner: "srv",
			wantRepo:  "repo",
			wantFull:  "host/srv/repo",
		},
		{
			name:      "bracketed IPv6 SSH URL with port",
			input:     "ssh://git@[2001:db8::1]:2222/org/repo.git",
			wantHost:  "[2001:db8::1]:2222",
			wantOwner: "org",
			wantRepo:  "repo",
			wantFull:  "[2001:db8::1]:2222/org/repo",
		},
		{
			name:      "bracketed IPv6 SSH URL without port",
			input:     "ssh://git@[2001:db8::1]/org/repo.git",
			wantHost:  "[2001:db8::1]",
			wantOwner: "org",
			wantRepo:  "repo",
			wantFull:  "[2001:db8::1]/org/repo",
		},
		{
			name:      "https with token userinfo drops the token",
			input:     "https://ghp_token@github.com/org/repo.git",
			wantHost:  "github.com",
			wantOwner: "org",
			wantRepo:  "repo",
			wantFull:  "github.com/org/repo",
		},
		{
			name:      "https with user:token userinfo drops the credentials",
			input:     "https://user:token@github.com/org/repo.git",
			wantHost:  "github.com",
			wantOwner: "org",
			wantRepo:  "repo",
			wantFull:  "github.com/org/repo",
		},
		{
			name:      "scp-style with user prefix drops the user",
			input:     "myuser@github.com:org/repo.git",
			wantHost:  "github.com",
			wantOwner: "org",
			wantRepo:  "repo",
			wantFull:  "github.com/org/repo",
		},
		{
			name:      "git scheme",
			input:     "git://github.com/org/repo.git",
			wantHost:  "github.com",
			wantOwner: "org",
			wantRepo:  "repo",
			wantFull:  "github.com/org/repo",
		},
		{
			name:      "git+ssh scheme with git@ user",
			input:     "git+ssh://git@github.com/org/repo.git",
			wantHost:  "github.com",
			wantOwner: "org",
			wantRepo:  "repo",
			wantFull:  "github.com/org/repo",
		},
		{
			name:      "ssh scheme without user",
			input:     "ssh://github.com/org/repo",
			wantHost:  "github.com",
			wantOwner: "org",
			wantRepo:  "repo",
			wantFull:  "github.com/org/repo",
		},
		{
			name:      "ssh scheme with numeric port keeps the port",
			input:     "ssh://git@github.com:2222/org/repo.git",
			wantHost:  "github.com:2222",
			wantOwner: "org",
			wantRepo:  "repo",
			wantFull:  "github.com:2222/org/repo",
		},
		{
			name:    "file scheme with rooted path is rejected",
			input:   "file:/tmp/org/repo",
			wantErr: true,
		},
		{
			name:    "file scheme with authority form is rejected",
			input:   "file:///home/user/repo",
			wantErr: true,
		},
		{
			name:    "unrecognized scheme is rejected",
			input:   "svn://github.com/org/repo",
			wantErr: true,
		},
		{
			name:    "scp-style with user:token userinfo is rejected",
			input:   "user:token@github.com:org/repo.git",
			wantErr: true,
		},
		{
			name:    "remote-helper ext command line is rejected",
			input:   "ext::/usr/bin/sshpass -p p@ss ssh user@example.com git-upload-pack /org/repo.git",
			wantErr: true,
		},
		{
			name:    "remote-helper custom transport is rejected",
			input:   "myhelper::github.com/org/repo.git",
			wantErr: true,
		},
		{
			name:    "remote-helper transport::address URL form is rejected",
			input:   "https::http://example.com/org/repo.git",
			wantErr: true,
		},
		{
			name:    "remote-helper empty transport is rejected",
			input:   "::--token=secret",
			wantErr: true,
		},
		{
			name:    "ssh scheme with user:token userinfo is rejected",
			input:   "ssh://user:token@github.com/org/repo.git",
			wantErr: true,
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

func TestSplitSCPLikeURL(t *testing.T) {
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
			name:     "numeric first path component is SCP",
			input:    "localhost:8080/user/repo",
			expected: true,
		},
		{
			name:     "port only without path",
			input:    "localhost:8080",
			expected: true,
		},
		{
			name:     "URL with scheme",
			input:    "https://github.com/user/repo",
			expected: false,
		},
		{
			name:     "git@ prefix",
			input:    "git@github.com:user/repo.git",
			expected: true,
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
			expected: true,
		},
		{
			name:     "bracketed IPv6 address",
			input:    "[::1]:8080/user/repo",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, result := splitSCPLikeURL(tt.input)
			if result != tt.expected {
				t.Errorf("splitSCPLikeURL(%q) ok = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestNormalizeURL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
		wantErr  bool
	}{
		{
			name:     "git@ format",
			input:    "git@github.com:user/repo.git",
			expected: "https://github.com/user/repo.git",
		},
		{
			name:    "ssh URL with nonnumeric port is rejected",
			input:   "ssh://git@github.com:user/repo.git",
			wantErr: true,
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
			name:     "numeric first path component is SCP",
			input:    "localhost:8080/user/repo",
			expected: "https://localhost/8080/user/repo",
		},
		{
			name:     "SCP absolute path",
			input:    "host:/srv/repo.git",
			expected: "https://host/srv/repo.git",
		},
		{
			name:     "git@ with SSH config alias",
			input:    "git@workgit:org/repo.git",
			expected: "https://workgit/org/repo.git",
		},
		{
			name:     "at sign in SCP path",
			input:    "mirror:path@tenant/org/repo.git",
			expected: "https://mirror/path@tenant/org/repo.git",
		},
		{
			name:     "git scheme",
			input:    "git://github.com/org/repo.git",
			expected: "https://github.com/org/repo.git",
		},
		{
			name:     "git+ssh scheme drops git@ user",
			input:    "git+ssh://git@github.com/org/repo.git",
			expected: "https://github.com/org/repo.git",
		},
		{
			name:     "ssh scheme without user",
			input:    "ssh://github.com/org/repo",
			expected: "https://github.com/org/repo",
		},
		{
			name:     "ssh scheme with numeric port keeps the port",
			input:    "ssh://git@github.com:2222/org/repo.git",
			expected: "https://github.com:2222/org/repo.git",
		},
		{
			name:     "bracketed IPv6 SSH URL keeps brackets and port",
			input:    "ssh://git@[2001:db8::1]:2222/org/repo.git",
			expected: "https://[2001:db8::1]:2222/org/repo.git",
		},
		{
			name:    "file scheme is rejected",
			input:   "file:///home/user/repo",
			wantErr: true,
		},
		{
			name:    "unrecognized scheme is rejected",
			input:   "svn://github.com/org/repo",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := normalizeURL(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("normalizeURL(%s) expected error, got %q", tt.input, result)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeURL(%s) unexpected error: %v", tt.input, err)
			}
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

func TestRepositoryInfoForFilesystemEncodesAuthority(t *testing.T) {
	tests := []struct {
		name     string
		info     *RepositoryInfo
		wantHost string
		wantFull string
	}{
		{
			name: "port",
			info: &RepositoryInfo{
				Host:       "github.com:2222",
				Owner:      "org",
				Repository: "repo",
				FullPath:   "github.com:2222/org/repo",
			},
			wantHost: "github.com%3A2222",
			wantFull: "github.com%3A2222/org/repo",
		},
		{
			name: "bracketed IPv6 with port",
			info: &RepositoryInfo{
				Host:       "[2001:db8::1]:2222",
				Owner:      "org",
				Repository: "repo",
				FullPath:   "[2001:db8::1]:2222/org/repo",
			},
			wantHost: "%5B2001%3Adb8%3A%3A1%5D%3A2222",
			wantFull: "%5B2001%3Adb8%3A%3A1%5D%3A2222/org/repo",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			originalHost := tt.info.Host
			originalFullPath := tt.info.FullPath
			got := RepositoryInfoForFilesystem(tt.info)
			if got.Host != tt.wantHost {
				t.Errorf("Host = %q, want %q", got.Host, tt.wantHost)
			}
			if got.FullPath != tt.wantFull {
				t.Errorf("FullPath = %q, want %q", got.FullPath, tt.wantFull)
			}
			if tt.info.Host != originalHost || tt.info.FullPath != originalFullPath {
				t.Fatal("RepositoryInfoForFilesystem mutated its canonical input")
			}
		})
	}
}

func TestGenerateWorktreePathEncodesAuthority(t *testing.T) {
	repoInfo := &RepositoryInfo{
		Host:       "github.com:2222",
		Owner:      "org",
		Repository: "repo",
		FullPath:   "github.com:2222/org/repo",
	}

	got := GenerateWorktreePath("/tmp/worktrees", repoInfo, "feature/read-api")
	want := filepath.Join("/tmp/worktrees", "github.com%3A2222", "org", "repo", "feature-read-api")
	if got != want {
		t.Fatalf("GenerateWorktreePath() = %q, want %q", got, want)
	}
}
