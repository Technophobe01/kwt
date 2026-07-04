package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"go.kenn.io/kwt/pkg/models"
)

const defaultFleetClientTimeout = 2 * time.Second

// ClientOptions configures a fleet hub HTTP client.
type ClientOptions struct {
	HubURL  string
	Token   string
	Timeout time.Duration
}

// Client publishes manifests to and reads state from a fleet hub.
type Client struct {
	hubURL     string
	token      string
	httpClient *http.Client
}

// ManifestBuildProvider builds the local manifest used by best-effort publish.
type ManifestBuildProvider interface {
	Build(context.Context, *models.Config) (*Manifest, error)
}

// LoadToken loads the fleet bearer token from the configured source.
func LoadToken(cfg models.FleetConfig) (string, error) {
	if tokenFile := strings.TrimSpace(cfg.TokenFile); tokenFile != "" {
		body, err := os.ReadFile(tokenFile)
		if err != nil {
			return "", fmt.Errorf("read fleet token file: %w", err)
		}
		return nonEmptyToken(string(body), "fleet token file")
	}

	if tokenEnv := strings.TrimSpace(cfg.TokenEnv); tokenEnv != "" {
		value, ok := os.LookupEnv(tokenEnv)
		if !ok {
			return "", fmt.Errorf("fleet token environment variable %q is not set", tokenEnv)
		}
		return nonEmptyToken(value, "fleet token environment variable "+tokenEnv)
	}

	return "", errors.New("fleet token is not configured")
}

// EffectiveHubURL returns the configured hub URL, falling back to the local hub listener.
func EffectiveHubURL(cfg models.FleetConfig) string {
	if hubURL := strings.TrimSpace(cfg.HubURL); hubURL != "" {
		return hubURL
	}
	if listenAddr := strings.TrimSpace(cfg.Hub.ListenAddr); listenAddr != "" {
		if strings.HasPrefix(listenAddr, "http://") || strings.HasPrefix(listenAddr, "https://") {
			return strings.TrimRight(listenAddr, "/")
		}
		return "http://" + listenAddr
	}
	return ""
}

// NewClient creates a fleet hub client.
func NewClient(opts ClientOptions) *Client {
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = defaultFleetClientTimeout
	}
	return &Client{
		hubURL: strings.TrimRight(strings.TrimSpace(opts.HubURL), "/"),
		token:  strings.TrimSpace(opts.Token),
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// Publish sends one manifest to the fleet hub.
func (c *Client) Publish(ctx context.Context, manifest Manifest) error {
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(manifest); err != nil {
		return fmt.Errorf("encode fleet manifest: %w", err)
	}

	req, err := c.newRequest(
		ctx,
		http.MethodPost,
		"/api/v1/fleet/hosts/"+url.PathEscape(manifest.HostID)+"/manifest",
		&body,
	)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("publish fleet manifest: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if !successStatus(resp.StatusCode) {
		return responseError("publish fleet manifest", resp)
	}
	return nil
}

// State reads the current fleet state from the hub.
func (c *Client) State(ctx context.Context, etag string) (FleetState, string, bool, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/api/v1/fleet/state", nil)
	if err != nil {
		return FleetState{}, "", false, err
	}
	if etag = strings.TrimSpace(etag); etag != "" {
		req.Header.Set("If-None-Match", etag)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return FleetState{}, "", false, fmt.Errorf("read fleet state: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	responseETag := resp.Header.Get("ETag")
	if resp.StatusCode == http.StatusNotModified {
		return FleetState{}, responseETag, true, nil
	}
	if !successStatus(resp.StatusCode) {
		return FleetState{}, responseETag, false, responseError("read fleet state", resp)
	}

	var state FleetState
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		return FleetState{}, responseETag, false, fmt.Errorf("decode fleet state: %w", err)
	}
	return state, responseETag, false, nil
}

// Forget removes one host from the fleet hub state.
func (c *Client) Forget(ctx context.Context, hostID string) error {
	req, err := c.newRequest(ctx, http.MethodDelete, "/api/v1/fleet/hosts/"+url.PathEscape(hostID), nil)
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("forget fleet host: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if !successStatus(resp.StatusCode) {
		return responseError("forget fleet host", resp)
	}
	return nil
}

// PublishBestEffort attempts to publish local fleet state without failing callers.
func PublishBestEffort(ctx context.Context, cfg *models.Config, builder ManifestBuildProvider, warn io.Writer) error {
	if cfg == nil || !cfg.Fleet.Enabled {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	ctx, cancel := context.WithTimeout(ctx, defaultFleetClientTimeout)
	defer cancel()

	result := make(chan error, 1)
	go func() {
		result <- publishBestEffort(ctx, cfg, builder)
	}()

	var err error
	select {
	case err = <-result:
	case <-ctx.Done():
		err = ctx.Err()
	}
	if err != nil {
		writeFleetWarning(warn, err)
	}
	return nil
}

func publishBestEffort(ctx context.Context, cfg *models.Config, builder ManifestBuildProvider) error {
	hubURL := EffectiveHubURL(cfg.Fleet)
	if hubURL == "" {
		return errors.New("fleet hub URL is not configured")
	}
	token, err := LoadToken(cfg.Fleet)
	if err != nil {
		return err
	}
	if builder == nil {
		return errors.New("fleet manifest builder is not configured")
	}
	manifest, err := builder.Build(ctx, cfg)
	if err != nil {
		return err
	}
	if manifest == nil {
		return errors.New("fleet manifest builder returned nil manifest")
	}
	return NewClient(ClientOptions{
		HubURL:  hubURL,
		Token:   token,
		Timeout: defaultFleetClientTimeout,
	}).Publish(ctx, *manifest)
}

func (c *Client) newRequest(ctx context.Context, method string, path string, body io.Reader) (*http.Request, error) {
	if c == nil {
		return nil, errors.New("fleet client is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if c.hubURL == "" {
		return nil, errors.New("fleet hub URL is not configured")
	}

	req, err := http.NewRequestWithContext(ctx, method, c.hubURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("create fleet request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	return req, nil
}

func nonEmptyToken(raw string, source string) (string, error) {
	token := strings.TrimSpace(raw)
	if token == "" {
		return "", fmt.Errorf("%s is empty", source)
	}
	return token, nil
}

func successStatus(status int) bool {
	return status >= http.StatusOK && status <= 299
}

func responseError(action string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	message := strings.TrimSpace(string(body))
	if message == "" {
		return fmt.Errorf("%s: hub returned %s", action, resp.Status)
	}
	return fmt.Errorf("%s: hub returned %s: %s", action, resp.Status, message)
}

func writeFleetWarning(warn io.Writer, err error) {
	if warn == nil || err == nil {
		return
	}
	_, _ = fmt.Fprintf(warn, "warning: fleet publish failed: %v\n", err)
}
