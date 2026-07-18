package fleet

import (
	"fmt"
	"regexp"
	"strings"

	repositoryurl "go.kenn.io/kwt/internal/url"
)

var hostIDPattern = regexp.MustCompile(`^[a-z0-9._-]+$`)

// NormalizeHostID normalizes and validates a fleet host ID.
func NormalizeHostID(raw string) (string, error) {
	hostID := strings.ToLower(strings.TrimSpace(raw))
	if hostID == "" {
		return "", fmt.Errorf("host ID is required")
	}
	if !hostIDPattern.MatchString(hostID) {
		return "", fmt.Errorf("invalid host ID %q", hostID)
	}
	return hostID, nil
}

// DefaultHostID derives a fleet host ID from an injected hostname function.
func DefaultHostID(hostname func() (string, error)) (string, error) {
	name, err := hostname()
	if err != nil {
		return "", err
	}
	return NormalizeHostID(name)
}

// NormalizeRepositoryIdentity converts a Git remote URL into a stable project identity.
func NormalizeRepositoryIdentity(raw string) (string, error) {
	identity, ok := repositoryurl.CanonicalRepositoryIdentity(raw)
	if !ok {
		return "", fmt.Errorf("repository identity is required")
	}
	return identity, nil
}
