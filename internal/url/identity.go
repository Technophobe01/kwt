package url

import (
	"net"
	"path/filepath"
	"strings"
)

// localIdentityNamespace is the first path segment worktree's
// RepositoryInfoFromLocalPath reserves for no-remote fallback identities
// ("local/..."). Slugs and hosts in that namespace can never be canonical:
// they would collide with (and shadow) local fallback identities.
const localIdentityNamespace = "local"

// CanonicalRepositoryInfo reports whether raw can serve as a stable,
// shareable repository identity and returns its parsed form. Network remote
// URLs and host/owner[/namespace...]/name slugs qualify — including dotless
// hosts such as SSH config aliases ("workgit/org/repo") and "localhost",
// which the normalizer itself emits for alias remotes. Filesystem paths
// (absolute, relative-dot, home-anchored, or Windows drive paths),
// "local/..." fallback identities, and non-network URL schemes do not.
//
// The bar is closed under normalization: an identity this function emits is
// itself accepted and re-canonicalizes to the identical string (enforced by
// the fixed-point check below), so registered identities survive repeated
// passes instead of silently degrading to the local fallback. This is the
// bar for explicitly CONFIGURED identities (registry entries, where the user
// typed the slug deliberately) and for identities the canonicalizer itself
// emitted; raw git remote output goes through
// CanonicalRepositoryIdentityFromRemote instead, which additionally rejects
// scheme-less slugs (dotted or not) because git reads them as relative
// filesystem paths, not remotes. A stored identity that passes this bar is
// authoritative as-is (an upstream identity deliberately pinned over a fork
// origin, for example); kwt does not revalidate it against the repository's
// current remote.
func CanonicalRepositoryInfo(raw string) (*RepositoryInfo, bool) {
	raw = strings.TrimSpace(raw)
	if !passesCanonicalIdentityPrechecks(raw) {
		return nil, false
	}
	if info, ok := parseCanonicalAuthoritySlug(raw); ok {
		return info, true
	}
	info, err := ParseRepositoryURL(raw)
	if err != nil {
		return nil, false
	}
	if strings.TrimSpace(info.FullPath) == "" {
		return nil, false
	}
	if info.FullPath != raw {
		// Fixed-point check: only accept inputs whose emitted identity is
		// itself canonical and stable, so canonicalization is idempotent by
		// construction. Terminates because each pass only shortens the
		// string (scp/URL forms flatten to slugs, ".git" suffixes trim).
		reparsed, ok := CanonicalRepositoryInfo(info.FullPath)
		if !ok || reparsed.FullPath != info.FullPath {
			return nil, false
		}
	}
	return info, true
}

// CanonicalRepositoryIdentity is the string form of CanonicalRepositoryInfo:
// it returns the normalized host/owner/name identity when raw qualifies.
func CanonicalRepositoryIdentity(raw string) (string, bool) {
	info, ok := CanonicalRepositoryInfo(raw)
	if !ok {
		return "", false
	}
	return info.FullPath, true
}

// CanonicalRepositoryIdentityFromRemote applies the canonical bar to raw git
// remote output (`git remote get-url`), which is genuinely ambiguous where a
// configured identity is not: git accepts a relative filesystem path with no
// leading "./" as a remote ("cache/team/repo.git"), byte-identical to an
// alias slug. A remote therefore only qualifies when it carries syntax git
// itself reads as a remote — a URL scheme or an scp-style colon before the
// first slash (git-clone(1)); scheme-less slugs, dotted
// ("cache.example/team/repo.git") or not, are filesystem paths to git and
// are rejected rather than published under a bogus host grouping.
// Identities this bar emits can themselves be scheme-less slugs
// ("workgit:org/repo" -> "workgit/org/repo"); they live in identity space,
// where CanonicalRepositoryIdentity governs and keeps accepting them (see
// the per-provenance closure test).
func CanonicalRepositoryIdentityFromRemote(raw string) (string, bool) {
	info, ok := CanonicalRepositoryInfoFromRemote(raw)
	if !ok {
		return "", false
	}
	return info.FullPath, true
}

// CanonicalRepositoryInfoFromRemote is the parsed form of
// CanonicalRepositoryIdentityFromRemote, for callers that need the full
// RepositoryInfo (host/owner/name components) rather than the identity
// string. Accept/reject decisions are identical to the string form.
func CanonicalRepositoryInfoFromRemote(raw string) (*RepositoryInfo, bool) {
	raw = strings.TrimSpace(raw)
	if !hasExplicitRemoteSyntax(raw) || !passesCanonicalIdentityPrechecks(raw) {
		return nil, false
	}
	info, err := ParseRepositoryURL(raw)
	if err != nil || strings.TrimSpace(info.FullPath) == "" {
		return nil, false
	}
	// Validate the emitted identity in configured-identity space. That parser
	// deliberately understands authority/path slugs with explicit ports,
	// while ParseRepositoryURL above applies Git's raw-remote rule that every
	// scheme-less colon before a slash is SCP syntax.
	stable, ok := CanonicalRepositoryInfo(info.FullPath)
	if !ok || stable.FullPath != info.FullPath {
		return nil, false
	}
	return info, true
}

// passesCanonicalIdentityPrechecks applies the shared precondition of both
// canonical bars: the value must be a plausible identity candidate and must
// not carry a credential-shaped authority or SCP path.
// caseInsensitiveRepositoryPathHosts are the hosts known to treat owner and
// repository names case-insensitively, so two spellings there name one
// repository. Hosts absent from this set are assumed case-sensitive, which is
// the safe default: mistakenly folding a case-sensitive host silently credits
// one repository's state to another, while mistakenly preserving a
// case-insensitive one merely shows a repository twice. Self-hosted forges
// cannot be recognized from a hostname and land on the safe side.
var caseInsensitiveRepositoryPathHosts = map[string]bool{
	"bitbucket.org": true,
	"codeberg.org":  true,
	"github.com":    true,
	"gitlab.com":    true,
}

// FoldRepositoryIdentity normalizes a canonical repository identity for
// comparison between spellings that name the same repository. The host is
// always folded because DNS is case-insensitive; the owner and repository path
// is folded only for hosts that resolve names case-insensitively, so a plain
// Git server on a case-sensitive filesystem keeps two repositories differing
// only in case distinct.
//
// Callers that compare identities must all fold them the same way, or one side
// stops recognizing the other's spelling.
func FoldRepositoryIdentity(identity string) string {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return ""
	}
	authority, remainder, ok := strings.Cut(identity, "/")
	authority = strings.ToLower(authority)
	if !ok {
		return authority
	}
	if caseInsensitiveRepositoryPathHosts[hostWithoutPort(authority)] {
		remainder = strings.ToLower(remainder)
	}
	return authority + "/" + remainder
}

func hostWithoutPort(authority string) string {
	if host, _, err := net.SplitHostPort(authority); err == nil {
		return host
	}
	return authority
}

func passesCanonicalIdentityPrechecks(raw string) bool {
	return isCanonicalIdentityCandidate(raw) &&
		!hasCredentialBearingAuthoritySlug(raw) &&
		!hasAmbiguousSCPCredentialPath(raw)
}

// parseCanonicalAuthoritySlug recognizes already-normalized identity slugs
// whose first path component is an explicit URL authority with a numeric port
// (including bracketed IPv6). These strings are identity data, not raw Git
// remotes, so preserving the authority makes normalization idempotent. Raw
// remotes never enter here; CanonicalRepositoryInfoFromRemote parses those
// with Git syntax first.
func parseCanonicalAuthoritySlug(raw string) (*RepositoryInfo, bool) {
	authority, remainder, ok := strings.Cut(raw, "/")
	if !ok || remainder == "" {
		return nil, false
	}
	host, port, err := net.SplitHostPort(authority)
	if err != nil || !isNumericPort(port) || !isCanonicalAuthorityHost(host) {
		return nil, false
	}
	info, err := repositoryInfoFromParts(authority, strings.Split(remainder, "/"))
	if err != nil {
		return nil, false
	}
	return info, true
}

func isCanonicalAuthorityHost(host string) bool {
	return strings.TrimSpace(host) != "" && !strings.ContainsAny(host, `@/\`)
}

func hasCredentialBearingAuthoritySlug(raw string) bool {
	authority, _, ok := strings.Cut(raw, "/")
	if !ok || !strings.Contains(authority, "@") {
		return false
	}
	_, port, err := net.SplitHostPort(authority)
	return err == nil && isNumericPort(port)
}

// hasAmbiguousSCPCredentialPath fails closed for authority:value@host/path.
// Git treats the first colon as the SCP delimiter, but the first path
// component is indistinguishable from an inline credential. ParseRepositoryURL
// remains syntax-faithful for callers that need raw Git parsing; canonical
// identity surfaces refuse to persist or publish this ambiguous shape.
func hasAmbiguousSCPCredentialPath(raw string) bool {
	_, remotePath, user, ok := splitSCPLikeURL(raw)
	if !ok || user != "" {
		return false
	}
	firstComponent, _, _ := strings.Cut(strings.TrimLeft(remotePath, "/"), "/")
	return strings.Contains(firstComponent, "@")
}

func isNumericPort(port string) bool {
	if port == "" {
		return false
	}
	for _, digit := range port {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

// hasExplicitRemoteSyntax reports whether raw carries syntax git itself
// reads as a remote rather than a filesystem path: a colon before the first
// slash, which covers URL schemes ("ssh://..."), scp-style [user@]host:path,
// and host:port forms (all validated downstream by the canonical bar).
// git-clone(1) recognizes the scp-like form "only ... if there are no
// slashes before the first colon"; everything else without a scheme —
// including a dotted first segment ("cache.example/team/repo.git") or a
// colon after a slash ("team/na:me/repo") — is a filesystem path to git.
func hasExplicitRemoteSyntax(raw string) bool {
	colon := strings.IndexByte(raw, ':')
	if colon < 0 {
		return false
	}
	slash := strings.IndexByte(raw, '/')
	return slash < 0 || colon < slash
}

// IsLocalFallbackIdentity reports whether value is the canonical "local/..."
// identity built by worktree.RepositoryInfoFromLocalPath for a repository
// without a usable remote. The namespace is owned here, next to the canonical
// bar that reserves it.
func IsLocalFallbackIdentity(value string) bool {
	value = strings.TrimSpace(value)
	return value == localIdentityNamespace ||
		strings.HasPrefix(value, localIdentityNamespace+"/")
}

// IsPathFallbackIdentity reports whether value is one of the path-derived
// fallback identities kwt synthesizes for a repository without a usable
// remote: the canonical "local/..." full path (the single form every surface
// now emits) or a bare absolute filesystem path (defensive, for identities
// that predate the canonical resolver). A stronger identity — a real remote
// or a configured publishable slug — may replace one of these.
func IsPathFallbackIdentity(value string) bool {
	return IsLocalFallbackIdentity(value) || isAbsoluteSlashPath(value)
}

func isAbsoluteSlashPath(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if filepath.IsAbs(filepath.FromSlash(value)) {
		return true
	}
	// Treat leading-slash paths as absolute fallbacks even when running on
	// Windows, where filepath.IsAbs("\\path") is false without a drive.
	slashValue := strings.ReplaceAll(value, `\`, "/")
	return strings.HasPrefix(slashValue, "/") || looksLikeWindowsDrivePath(slashValue)
}

func isCanonicalIdentityCandidate(raw string) bool {
	if raw == "" {
		return false
	}
	if filepath.IsAbs(raw) || strings.HasPrefix(raw, ".") || strings.HasPrefix(raw, "~") {
		return false
	}
	if strings.Contains(raw, `\`) || looksLikeWindowsDrivePath(raw) {
		return false
	}
	if strings.Contains(raw, "://") {
		scheme, _, _ := strings.Cut(raw, "://")
		return isNetworkGitURLScheme(scheme)
	}
	if strings.Contains(raw, ":") {
		return true
	}

	firstSegment, _, _ := strings.Cut(raw, "/")
	return firstSegment != localIdentityNamespace
}

func isNetworkGitURLScheme(scheme string) bool {
	switch strings.ToLower(scheme) {
	case "git", "git+ssh", "http", "https", "ssh":
		return true
	default:
		return false
	}
}

func looksLikeWindowsDrivePath(raw string) bool {
	if len(raw) < 3 || raw[1] != ':' {
		return false
	}
	drive := raw[0]
	return ((drive >= 'A' && drive <= 'Z') || (drive >= 'a' && drive <= 'z')) && (raw[2] == '/' || raw[2] == '\\')
}
