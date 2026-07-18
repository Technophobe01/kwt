package url

import "testing"

// TestCanonicalRepositoryInfo pins the single bar for what counts as a
// stable, shareable repository identity: network remote URLs and
// host/owner/name slugs qualify; filesystem paths, local/... fallbacks, and
// non-network URL schemes do not.
func TestCanonicalRepositoryInfo(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		wantOK   bool
		wantFull string
	}{
		{name: "host owner name slug", raw: "github.com/org/repo", wantOK: true, wantFull: "github.com/org/repo"},
		{name: "https remote", raw: "https://github.com/org/repo.git", wantOK: true, wantFull: "github.com/org/repo"},
		{name: "scp-like ssh remote", raw: "git@github.com:org/repo.git", wantOK: true, wantFull: "github.com/org/repo"},
		{name: "git scheme remote", raw: "git://github.com/org/repo.git", wantOK: true, wantFull: "github.com/org/repo"},
		{name: "git+ssh scheme remote", raw: "git+ssh://git@github.com/org/repo.git", wantOK: true, wantFull: "github.com/org/repo"},
		{name: "ssh scheme remote", raw: "ssh://git@github.com/org/repo.git", wantOK: true, wantFull: "github.com/org/repo"},
		{name: "empty", raw: "", wantOK: false},
		{name: "whitespace only", raw: "   ", wantOK: false},
		{name: "absolute path", raw: "/home/user/repo", wantOK: false},
		{name: "relative path", raw: "./repo", wantOK: false},
		{name: "home path", raw: "~/repo", wantOK: false},
		{name: "local fallback identity", raw: "local/home/user/repo", wantOK: false},
		{name: "windows drive path", raw: "C:/repos/app", wantOK: false},
		{name: "backslash path", raw: `repos\app`, wantOK: false},
		{name: "non-network scheme", raw: "file:///home/user/repo", wantOK: false},
		{name: "non-network scheme with rooted path", raw: "file:/tmp/org/repo", wantOK: false},
		{name: "unrecognized scheme", raw: "svn://github.com/org/repo", wantOK: false},
		{name: "scp remote with absolute path", raw: "host:/user/repo", wantOK: true, wantFull: "host/user/repo"},
		{name: "ssh alias scp remote", raw: "workgit:org/repo.git", wantOK: true, wantFull: "workgit/org/repo"},
		{name: "ssh alias nested scp remote", raw: "workgit:org/team/repo.git", wantOK: true, wantFull: "workgit/org/team/repo"},
		{name: "ssh alias slug without dot", raw: "workgit/org/repo", wantOK: true, wantFull: "workgit/org/repo"},
		{name: "localhost ssh remote", raw: "ssh://localhost/user/repo", wantOK: true, wantFull: "localhost/user/repo"},
		{name: "localhost slug", raw: "localhost/user/repo", wantOK: true, wantFull: "localhost/user/repo"},
		{
			name:     "localhost with port slug",
			raw:      "localhost:3000/user/repo",
			wantOK:   true,
			wantFull: "localhost:3000/user/repo",
		},
		{name: "alias colliding with local namespace", raw: "local:org/repo", wantOK: false},
		{name: "local slug without dot stays rejected", raw: "local/org/repo", wantOK: false},
		{name: "repository name still ending in .git after trim", raw: "git@github.com:org/x.git.git", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, ok := CanonicalRepositoryInfo(tt.raw)
			if ok != tt.wantOK {
				t.Fatalf("CanonicalRepositoryInfo(%q) ok = %v, want %v", tt.raw, ok, tt.wantOK)
			}
			if !tt.wantOK {
				if info != nil {
					t.Fatalf("CanonicalRepositoryInfo(%q) info = %+v, want nil", tt.raw, info)
				}
				return
			}
			if info.FullPath != tt.wantFull {
				t.Errorf("CanonicalRepositoryInfo(%q).FullPath = %q, want %q", tt.raw, info.FullPath, tt.wantFull)
			}
		})
	}
}

// TestCanonicalRepositoryIdentityIsIdempotent pins the round-trip contract:
// every identity the canonicalizer emits must itself pass the canonical bar
// and re-canonicalize to the identical string. Without this, an identity
// registered after one normalization pass (e.g. an SSH config alias host with
// no dot, "workgit:org/repo" -> "workgit/org/repo") is silently rejected on
// the next pass, losing registered-identity precedence and downgrading
// surfaces to the local fallback.
func TestCanonicalRepositoryIdentityIsIdempotent(t *testing.T) {
	inputs := []string{
		"github.com/org/repo",
		"https://github.com/org/repo.git",
		"git@github.com:org/repo.git",
		"workgit:org/repo.git",
		"workgit:org/repo",
		"gitea.internal:team/service.git",
		"ssh://localhost/user/repo",
		"localhost/user/repo",
		"localhost:3000/user/repo",
		"ssh://git@github.com:22/user/repo",
		"ssh://git@[2001:db8::1]/org/repo.git",
		"git+ssh://git@github.com/org/repo.git",
		"github.com/org/group/subgroup/repo",
	}
	for _, raw := range inputs {
		identity, ok := CanonicalRepositoryIdentity(raw)
		if !ok {
			t.Fatalf("CanonicalRepositoryIdentity(%q) ok = false, want true", raw)
		}
		again, ok := CanonicalRepositoryIdentity(identity)
		if !ok {
			t.Fatalf("emitted identity %q (from %q) rejected on re-canonicalization", identity, raw)
		}
		if again != identity {
			t.Fatalf("re-canonicalizing %q (from %q) = %q, want no-op", identity, raw, again)
		}
	}
}

// TestCanonicalRepositoryIdentityFromRemote pins the remote-derived
// provenance of the canonical bar to git's own URL semantics (git-clone(1),
// GIT URLS): git treats a remote as a filesystem path unless it carries a
// URL scheme or an scp-style colon, and the scp-like form "is only
// recognized if there are no slashes before the first colon". A dotted
// first segment ("cache.example/team/repo.git") or a colon after a slash
// ("team/na:me/repo") is therefore a relative filesystem remote, not a
// shareable identity. Scheme-less slugs — dotted or dotless — remain
// acceptable only for explicitly configured identities
// (CanonicalRepositoryIdentity), where the user typed the slug deliberately.
func TestCanonicalRepositoryIdentityFromRemote(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		wantOK bool
		want   string
	}{
		{name: "relative dotless remote", raw: "cache/team/repo.git", wantOK: false},
		{name: "relative dotless remote without suffix", raw: "cache/team/repo", wantOK: false},
		{name: "deep relative dotless remote", raw: "cache/team/sub/repo.git", wantOK: false},
		{name: "relative dotted remote", raw: "cache.example/team/repo.git", wantOK: false},
		{name: "relative dotted remote without suffix", raw: "cache.example/team/repo", wantOK: false},
		{name: "colon after first slash", raw: "team/na:me/repo", wantOK: false},
		{name: "bare dotless localhost slug", raw: "localhost/user/repo", wantOK: false},
		{name: "scp alias remote", raw: "workgit:org/repo.git", wantOK: true, want: "workgit/org/repo"},
		{name: "scp alias remote without suffix", raw: "workgit:org/repo", wantOK: true, want: "workgit/org/repo"},
		{name: "scp remote with user", raw: "git@github.com:org/repo.git", wantOK: true, want: "github.com/org/repo"},
		{name: "https remote", raw: "https://github.com/org/repo.git", wantOK: true, want: "github.com/org/repo"},
		{name: "ssh scheme dotless host", raw: "ssh://localhost/user/repo", wantOK: true, want: "localhost/user/repo"},
		{name: "dotted slug without scheme is a relative path", raw: "github.com/org/repo", wantOK: false},
		{
			name:   "numeric first path component is SCP syntax",
			raw:    "localhost:3000/user/repo",
			wantOK: true,
			want:   "localhost/3000/user/repo",
		},
		{name: "non-network scheme", raw: "file:///tmp/org/repo", wantOK: false},
		{name: "absolute path", raw: "/home/user/repo", wantOK: false},
		{name: "relative dot path", raw: "./repo", wantOK: false},
		{name: "windows drive path", raw: "C:/repos/app", wantOK: false},
		{name: "empty", raw: "", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			identity, ok := CanonicalRepositoryIdentityFromRemote(tt.raw)
			if ok != tt.wantOK {
				t.Fatalf("CanonicalRepositoryIdentityFromRemote(%q) ok = %v, want %v", tt.raw, ok, tt.wantOK)
			}
			if !tt.wantOK {
				if identity != "" {
					t.Fatalf("CanonicalRepositoryIdentityFromRemote(%q) = %q, want empty", tt.raw, identity)
				}
				return
			}
			if identity != tt.want {
				t.Errorf("CanonicalRepositoryIdentityFromRemote(%q) = %q, want %q", tt.raw, identity, tt.want)
			}
		})
	}
}

// TestCanonicalRepositoryInfoFromRemote pins the parsed form of the
// remote-derived bar: callers that need the full RepositoryInfo (the shared
// worktree resolver, status, TUI launch discovery) must get the same
// accept/reject decisions as the string form, with the parsed identity on
// acceptance and nil on rejection.
func TestCanonicalRepositoryInfoFromRemote(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		wantOK   bool
		wantFull string
	}{
		{name: "relative dotless remote", raw: "cache/team/repo.git", wantOK: false},
		{name: "relative dotted remote", raw: "cache.example/team/repo.git", wantOK: false},
		{name: "colon after first slash", raw: "team/na:me/repo", wantOK: false},
		{name: "scp alias remote", raw: "workgit:org/repo.git", wantOK: true, wantFull: "workgit/org/repo"},
		{name: "https remote", raw: "https://github.com/org/repo.git", wantOK: true, wantFull: "github.com/org/repo"},
		{name: "absolute path", raw: "/home/user/repo", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, ok := CanonicalRepositoryInfoFromRemote(tt.raw)
			if ok != tt.wantOK {
				t.Fatalf("CanonicalRepositoryInfoFromRemote(%q) ok = %v, want %v", tt.raw, ok, tt.wantOK)
			}
			if !tt.wantOK {
				if info != nil {
					t.Fatalf("CanonicalRepositoryInfoFromRemote(%q) info = %+v, want nil", tt.raw, info)
				}
				return
			}
			if info.FullPath != tt.wantFull {
				t.Errorf("CanonicalRepositoryInfoFromRemote(%q).FullPath = %q, want %q", tt.raw, info.FullPath, tt.wantFull)
			}
		})
	}
}

// TestRemoteDerivedIdentitiesAreStableUnderConfiguredBar pins the closure
// contract for the remote provenance: every identity the remote-derived bar
// emits must itself pass the configured bar and re-canonicalize to the
// identical string, because emitted identities live in identity space (the
// registry, manifest joins, session-name derivation) where the configured bar
// governs. The scp alias round-trip depends on this: "workgit:org/repo" is
// remote-accepted and emits the dotless slug "workgit/org/repo", which the
// remote bar itself would reject as raw remote input (ambiguous with a
// relative path) but the configured bar must keep accepting.
func TestRemoteDerivedIdentitiesAreStableUnderConfiguredBar(t *testing.T) {
	inputs := []string{
		"workgit:org/repo.git",
		"git@github.com:org/repo.git",
		"https://github.com/org/repo.git",
		"ssh://localhost/user/repo",
		"ssh://localhost:3000/user/repo",
		"https://github.com/org/group/subgroup/repo",
	}
	for _, raw := range inputs {
		identity, ok := CanonicalRepositoryIdentityFromRemote(raw)
		if !ok {
			t.Fatalf("CanonicalRepositoryIdentityFromRemote(%q) ok = false, want true", raw)
		}
		again, ok := CanonicalRepositoryIdentity(identity)
		if !ok {
			t.Fatalf("remote-derived identity %q (from %q) rejected by the configured bar", identity, raw)
		}
		if again != identity {
			t.Fatalf("re-canonicalizing %q (from %q) = %q, want no-op", identity, raw, again)
		}
	}
}

// TestCanonicalRepositoryIdentityRejectsCredentialBearingRemotes pins that
// remotes carrying inline credentials never become identities (they would
// leak the credential into every surface that displays or publishes the
// identity). Moved alongside the bar itself from the fleet package.
// TestCanonicalIdentityRejectsRemoteHelperSyntax pins that git remote-helper
// forms (<transport>::<address>, gitremote-helpers(7)) never become identities
// on either bar. Git dispatches these to a helper command, never SCP parsing,
// and the address is an arbitrary command line that can embed credentials or
// local secret paths — publishing it to the fleet manifest would leak them.
func TestCanonicalIdentityRejectsRemoteHelperSyntax(t *testing.T) {
	for _, raw := range []string{
		"ext::/usr/bin/sshpass -p p@ss ssh user@example.com git-upload-pack /org/repo.git",
		"ext::ssh -i /home/user/.ssh/deploy_key user@example.com git-upload-pack /org/repo.git",
		"fd::17/org/repo",
		"myhelper::github.com/org/repo.git",
		"https::http://example.com/org/repo.git",
	} {
		if identity, ok := CanonicalRepositoryIdentity(raw); ok {
			t.Errorf("CanonicalRepositoryIdentity(%q) = %q, want rejection", raw, identity)
		}
		if identity, ok := CanonicalRepositoryIdentityFromRemote(raw); ok {
			t.Errorf("CanonicalRepositoryIdentityFromRemote(%q) = %q, want rejection", raw, identity)
		}
	}
}

func TestCanonicalRepositoryIdentityRejectsCredentialBearingRemotes(t *testing.T) {
	for _, raw := range []string{
		"user:token@github.com:org/repo.git",
		"user:ghp_SECRET@github.com/org/repo.git",
		"ssh://user:token@github.com/org/repo.git",
		"ghp_secret@github.com:2222/org/repo",
	} {
		identity, ok := CanonicalRepositoryIdentity(raw)
		if ok {
			t.Errorf("CanonicalRepositoryIdentity(%q) ok = true, want false", raw)
		}
		if identity != "" {
			t.Errorf("CanonicalRepositoryIdentity(%q) = %q, want empty", raw, identity)
		}
	}
}
