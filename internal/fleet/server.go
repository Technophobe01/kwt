package fleet

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"go.kenn.io/kit/daemon"
)

const (
	defaultFleetService     = "kwt-sync"
	defaultMaxManifestBytes = 1 << 20
)

// ServerOptions configures the fleet hub HTTP server.
type ServerOptions struct {
	Store            Store
	Token            string
	Service          string
	Version          string
	PID              int
	MaxManifestBytes int64
}

type server struct {
	store            Store
	token            string
	service          string
	version          string
	pid              int
	maxManifestBytes int64
}

// NewServer returns an HTTP handler for the fleet hub API.
func NewServer(opts ServerOptions) http.Handler {
	srv := &server{
		store:            opts.Store,
		token:            opts.Token,
		service:          opts.Service,
		version:          opts.Version,
		pid:              opts.PID,
		maxManifestBytes: opts.MaxManifestBytes,
	}
	if srv.service == "" {
		srv.service = defaultFleetService
	}
	if srv.pid == 0 {
		srv.pid = os.Getpid()
	}
	if srv.maxManifestBytes == 0 {
		srv.maxManifestBytes = defaultMaxManifestBytes
	}
	return srv
}

// ParseHubEndpoint parses and validates a fleet hub listen endpoint.
func ParseHubEndpoint(raw string) (daemon.Endpoint, error) {
	return daemon.ParseEndpoint(raw, daemon.ParseEndpointOptions{
		DefaultTCPAddress: "",
		TCPPolicy:         daemon.RequireNonPublic,
	})
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.EscapedPath()
	if path == "/api/v1/ping" {
		s.handlePing(w, r)
		return
	}
	if strings.HasPrefix(path, "/api/v1/fleet/") {
		if !s.authorized(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		s.handleFleet(w, r, path)
		return
	}
	http.NotFound(w, r)
}

func (s *server) authorized(r *http.Request) bool {
	if s.token == "" {
		return false
	}
	return r.Header.Get("Authorization") == "Bearer "+s.token
}

func (s *server) handlePing(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"service": s.service,
		"version": s.version,
		"pid":     s.pid,
	})
}

func (s *server) handleFleet(w http.ResponseWriter, r *http.Request, path string) {
	switch {
	case path == "/api/v1/fleet/state":
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleState(w, r)
	case strings.HasPrefix(path, "/api/v1/fleet/hosts/"):
		s.handleHost(w, r, path)
	default:
		http.NotFound(w, r)
	}
}

func (s *server) handleState(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		http.Error(w, "fleet store is not configured", http.StatusInternalServerError)
		return
	}
	state, err := s.store.State(r.Context())
	if err != nil {
		http.Error(w, "read fleet state", http.StatusInternalServerError)
		return
	}
	etag := strconv.Quote(state.StateVersion)
	w.Header().Set("ETag", etag)
	if strings.TrimSpace(r.Header.Get("If-None-Match")) == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (s *server) handleHost(w http.ResponseWriter, r *http.Request, path string) {
	switch {
	case strings.HasSuffix(path, "/manifest"):
		hostID, ok, escapedSlash := hostManifestPathHostID(path)
		if escapedSlash {
			http.Error(w, "invalid host ID", http.StatusBadRequest)
			return
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleManifest(w, r, hostID)
	default:
		hostID, ok, escapedSlash := hostPathHostID(path)
		if escapedSlash {
			http.Error(w, "invalid host ID", http.StatusBadRequest)
			return
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodDelete {
			w.Header().Set("Allow", http.MethodDelete)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleDeleteHost(w, r, hostID)
	}
}

func (s *server) handleManifest(w http.ResponseWriter, r *http.Request, hostID string) {
	normalizedHostID, err := NormalizeHostID(hostID)
	if err != nil || normalizedHostID != hostID {
		http.Error(w, "invalid host ID", http.StatusBadRequest)
		return
	}
	if s.store == nil {
		http.Error(w, "fleet store is not configured", http.StatusInternalServerError)
		return
	}
	if r.ContentLength > s.maxManifestBytes {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, s.maxManifestBytes)
	var manifest Manifest
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&manifest); err != nil {
		writeDecodeError(w, err)
		return
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		if err != nil {
			writeDecodeError(w, err)
			return
		}
		http.Error(w, "request body must contain one JSON value", http.StatusBadRequest)
		return
	}
	if manifest.HostID != hostID {
		http.Error(w, "manifest host ID does not match path", http.StatusBadRequest)
		return
	}
	if err := validateStoreManifest(manifest); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.store.Put(r.Context(), manifest); err != nil {
		http.Error(w, "store fleet manifest", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) handleDeleteHost(w http.ResponseWriter, r *http.Request, hostID string) {
	normalizedHostID, err := NormalizeHostID(hostID)
	if err != nil || normalizedHostID != hostID {
		http.Error(w, "invalid host ID", http.StatusBadRequest)
		return
	}
	if s.store == nil {
		http.Error(w, "fleet store is not configured", http.StatusInternalServerError)
		return
	}
	if err := s.store.Delete(r.Context(), hostID); err != nil {
		http.Error(w, "delete fleet host", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func hostManifestPathHostID(path string) (string, bool, bool) {
	const prefix = "/api/v1/fleet/hosts/"
	const suffix = "/manifest"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return "", false, false
	}
	hostID := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if containsEscapedSlash(hostID) {
		return "", false, true
	}
	if hostID == "" || strings.Contains(hostID, "/") {
		return "", false, false
	}
	return hostID, true, false
}

func hostPathHostID(path string) (string, bool, bool) {
	const prefix = "/api/v1/fleet/hosts/"
	if !strings.HasPrefix(path, prefix) {
		return "", false, false
	}
	hostID := strings.TrimPrefix(path, prefix)
	if containsEscapedSlash(hostID) {
		return "", false, true
	}
	if hostID == "" || strings.Contains(hostID, "/") {
		return "", false, false
	}
	return hostID, true, false
}

func containsEscapedSlash(segment string) bool {
	return strings.Contains(strings.ToLower(segment), "%2f")
}

func writeDecodeError(w http.ResponseWriter, err error) {
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return
	}
	http.Error(w, "decode manifest", http.StatusBadRequest)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
