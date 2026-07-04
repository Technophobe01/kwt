# kwt Fleet Sync Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build opt-in advisory fleet sync so kwt can publish local worktree manifests to a token-authenticated hub and render the fleet union without syncing file contents.

**Architecture:** Add a focused `internal/fleet` package for fleet models, identity normalization, manifest collection, hub storage, HTTP server, HTTP client, and table rendering. Keep Cobra wiring in `internal/cmd`, with fleet disabled by default and best-effort publish hooks on successful local mutations. The hub stores raw manifests, computes grouped `/api/v1/fleet/state` responses, and never mutates local worktrees on other machines.

**Tech Stack:** Go 1.26, Cobra, Viper/mapstructure, `net/http`, `go.kenn.io/kit/daemon`, existing `internal/table`, existing Git/status/discovery packages, `github.com/stretchr/testify` for tests.

---

## File Structure

- `pkg/models/models.go`: add `FleetConfig` and `FleetHubConfig` fields to global config.
- `internal/config/config.go`: expand fleet path fields and keep fleet disabled by default.
- `internal/config/config_test.go`: config parsing/default/path expansion coverage.
- `internal/fleet/types.go`: manifest, state, warning, and status-count data structs plus schema constants.
- `internal/fleet/types_test.go`: JSON wire-shape tests for manifest and state payloads.
- `internal/fleet/identity.go`: host ID validation, host ID defaulting, and repository identity normalization.
- `internal/fleet/identity_test.go`: table tests for host and repository identity behavior.
- `internal/fleet/manifest.go`: local manifest builder over configured projects and discovered worktrees.
- `internal/fleet/manifest_test.go`: temp-git tests for branch/detached worktree manifest rows and status fields.
- `internal/fleet/store.go`: atomic JSON hub store, latest manifest per host, host delete, grouped state build, state hash.
- `internal/fleet/store_test.go`: store, grouping, warning, ETag/state-version tests.
- `internal/fleet/server.go`: HTTP API, bearer auth, request size cap, route handling, ETag/304.
- `internal/fleet/server_test.go`: `httptest` coverage for all routes and validation failures.
- `internal/fleet/client.go`: token loading, timeout-bound HTTP client, publish/state/forget calls, best-effort publish helper.
- `internal/fleet/client_test.go`: token source and client behavior tests.
- `internal/fleet/render.go`: fleet status table rows and current-host advisory interpretation.
- `internal/fleet/render_test.go`: materialized/missing/differs/dirty/freshness rendering tests.
- `internal/cmd/fleet.go`: `kwt fleet serve|publish|status|forget` command surface.
- `internal/cmd/fleet_test.go`: Cobra command wiring and mutation hook tests with stubbed fleet publisher/client.
- `internal/cmd/add.go`, `internal/cmd/remove.go`, `internal/cmd/prune.go`: call best-effort publish after successful non-dry-run mutations.
- `README.md`: document opt-in fleet config and commands.
- `go.mod`, `go.sum`: add `go.kenn.io/kit` dependency when server code imports `kit/daemon`.

Do not add `prek` or repo infrastructure in this PR. The repo already has `mise`, `lefthook`, `golangci-lint`, and `testify`; fleet sync does not need additional project tooling.

---

### Task 1: Fleet Config And Identity

**Files:**

- Modify: `pkg/models/models.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Create: `internal/fleet/types.go`
- Create: `internal/fleet/types_test.go`
- Create: `internal/fleet/identity.go`
- Create: `internal/fleet/identity_test.go`

- [ ] **Step 1: Write failing config tests**

Add tests to `internal/config/config_test.go`:

```go
func TestLoadFleetDefaultsDisabled(t *testing.T) {
    viper.Reset()
    t.Cleanup(func() { viper.Reset() })
    viper.SetConfigType("toml")
    require.NoError(t, viper.ReadConfig(strings.NewReader(`
[worktree]
basedir = "/tmp/worktrees"
auto_mkdir = true
`)))

    cfg, err := Load()
    require.NoError(t, err)
    assert.False(t, cfg.Fleet.Enabled)
    assert.Empty(t, cfg.Fleet.HostID)
    assert.Empty(t, cfg.Fleet.HubURL)
    assert.Empty(t, cfg.Fleet.TokenFile)
    assert.Empty(t, cfg.Fleet.TokenEnv)
    assert.Empty(t, cfg.Fleet.Hub.ListenAddr)
    assert.Empty(t, cfg.Fleet.Hub.StorePath)
}

func TestLoadFleetConfigExpandsPaths(t *testing.T) {
    viper.Reset()
    t.Cleanup(func() { viper.Reset() })
    home := t.TempDir()
    t.Setenv("HOME", home)
    viper.SetConfigType("toml")
    require.NoError(t, viper.ReadConfig(strings.NewReader(`
[worktree]
basedir = "/tmp/worktrees"
auto_mkdir = true

[fleet]
enabled = true
host_id = "Host-A"
hub_url = "http://100.64.1.2:8787"
token_file = "~/kwt/fleet.token"
token_env = "KWT_FLEET_TOKEN"

[fleet.hub]
listen_addr = "100.64.1.2:8787"
store_path = "~/kwt/fleet/state.json"
`)))

    cfg, err := Load()
    require.NoError(t, err)
    assert.True(t, cfg.Fleet.Enabled)
    assert.Equal(t, "Host-A", cfg.Fleet.HostID)
    assert.Equal(t, filepath.Join(home, "kwt", "fleet.token"), cfg.Fleet.TokenFile)
    assert.Equal(t, filepath.Join(home, "kwt", "fleet", "state.json"), cfg.Fleet.Hub.StorePath)
}
```

- [ ] **Step 2: Run config tests and verify they fail**

Run: `go test ./internal/config -run 'TestLoadFleet' -count=1`

Expected: FAIL because `models.Config` does not yet have `Fleet`.

- [ ] **Step 3: Write failing identity tests**

Create `internal/fleet/identity_test.go`:

```go
package fleet

import (
    "testing"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestNormalizeHostID(t *testing.T) {
    tests := []struct {
        name    string
        input   string
        want    string
        wantErr bool
    }{
        {"lowercase simple", "host-a", "host-a", false},
        {"trims and lowercases", " Host.A_01 ", "host.a_01", false},
        {"rejects spaces", "host a", "", true},
        {"rejects slash", "host/a", "", true},
        {"rejects empty", "   ", "", true},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got, err := NormalizeHostID(tt.input)
            if tt.wantErr {
                require.Error(t, err)
                return
            }
            require.NoError(t, err)
            assert.Equal(t, tt.want, got)
        })
    }
}

func TestNormalizeRepositoryIdentity(t *testing.T) {
    tests := map[string]string{
        "git@github.com:kenn-io/kwt.git":      "github.com/kenn-io/kwt",
        "https://github.com/kenn-io/kwt":      "github.com/kenn-io/kwt",
        "https://github.com/kenn-io/kwt.git":  "github.com/kenn-io/kwt",
        "https://github.com/fork/kwt.git":     "github.com/fork/kwt",
    }
    for input, want := range tests {
        got, err := NormalizeRepositoryIdentity(input)
        require.NoError(t, err)
        assert.Equal(t, want, got)
    }
}

func TestDefaultHostIDUsesHostname(t *testing.T) {
    got, err := DefaultHostID(func() (string, error) { return "Host.A", nil })
    require.NoError(t, err)
    assert.Equal(t, "host.a", got)
}
```

- [ ] **Step 4: Write failing JSON wire-shape tests**

Create `internal/fleet/types_test.go`:

```go
package fleet

import (
    "encoding/json"
    "testing"
    "time"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/require"
)

func TestManifestJSONWireShape(t *testing.T) {
    manifest := Manifest{
        SchemaVersion: 1,
        HostID: "host-a",
        Host: HostInfo{Hostname: "Host-A", Platform: "darwin/arm64"},
        ObservedAt: time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC),
        Projects: []ProjectManifest{{Identity: "github.com/kenn-io/kwt", Name: "kwt", LocalRoot: "/home/user-a/code/kwt", RemoteURL: "git@github.com:kenn-io/kwt.git"}},
        Worktrees: []WorktreeManifest{{
            ProjectIdentity: "github.com/kenn-io/kwt",
            Kind: "branch",
            Ref: "feature/fleet",
            Branch: "feature/fleet",
            Path: "/tmp/feature",
            Head: "abc",
            Status: ChangeStatus{Modified: 1, Untracked: 2},
        }},
    }

    body, err := json.Marshal(manifest)
    require.NoError(t, err)
    text := string(body)
    assert.Contains(t, text, `"schema_version":1`)
    assert.Contains(t, text, `"host_id":"host-a"`)
    assert.Contains(t, text, `"project_identity":"github.com/kenn-io/kwt"`)
    assert.Contains(t, text, `"ahead":0`)
    assert.Contains(t, text, `"modified":1`)
    assert.NotContains(t, text, "SchemaVersion")

    var decoded map[string]any
    require.NoError(t, json.Unmarshal(body, &decoded))
    worktrees := decoded["worktrees"].([]any)
    first := worktrees[0].(map[string]any)
    status := first["status"].(map[string]any)
    assert.NotContains(t, status, "ahead", "ahead belongs beside status, not inside status")
    assert.NotContains(t, status, "behind", "behind belongs beside status, not inside status")
}

func TestFleetStateJSONWireShape(t *testing.T) {
    state := FleetState{
        SchemaVersion: 1,
        StateVersion: "sha256:abc123",
        Hosts: []HostState{{HostID: "host-a", Hostname: "Host-A", Platform: "darwin/arm64"}},
        Rows: []FleetRow{{
            ProjectIdentity: "github.com/kenn-io/kwt",
            ProjectName: "kwt",
            Kind: "branch",
            Ref: "feature/fleet",
            Branch: "feature/fleet",
            Observations: []Observation{{HostID: "host-a", Head: "abc", Status: ChangeStatus{Staged: 1}}},
        }},
    }

    body, err := json.Marshal(state)
    require.NoError(t, err)
    text := string(body)
    assert.Contains(t, text, `"state_version":"sha256:abc123"`)
    assert.Contains(t, text, `"observations"`)
    assert.Contains(t, text, `"staged":1`)
    assert.NotContains(t, text, "StateVersion")
}
```

- [ ] **Step 5: Run identity and wire-shape tests and verify they fail**

Run: `go test ./internal/fleet -run 'TestNormalize' -count=1`

Run: `go test ./internal/fleet -run 'TestManifestJSONWireShape|TestFleetStateJSONWireShape' -count=1`

Expected: FAIL because `internal/fleet` and wire structs do not exist.

- [ ] **Step 6: Implement config structs and path expansion**

In `pkg/models/models.go`, add:

```go
type FleetConfig struct {
    Enabled   bool           `mapstructure:"enabled" toml:"enabled"`
    HostID    string         `mapstructure:"host_id" toml:"host_id"`
    HubURL    string         `mapstructure:"hub_url" toml:"hub_url"`
    TokenFile string         `mapstructure:"token_file" toml:"token_file"`
    TokenEnv  string         `mapstructure:"token_env" toml:"token_env"`
    Hub       FleetHubConfig `mapstructure:"hub" toml:"hub"`
}

type FleetHubConfig struct {
    ListenAddr string `mapstructure:"listen_addr" toml:"listen_addr"`
    StorePath  string `mapstructure:"store_path" toml:"store_path"`
}
```

Add `Fleet FleetConfig` to `Config`.

In `internal/config/config.go`, expand `cfg.Fleet.TokenFile` and `cfg.Fleet.Hub.StorePath` with `utils.ExpandPath` when non-empty. Do not add fleet defaults to `defaultConfigTOML`; zero values keep fleet inert.

- [ ] **Step 7: Implement fleet identity/types package**

Create `internal/fleet/types.go` with `ManifestSchemaVersion = 1`, `StateSchemaVersion = 1`, and structs matching the spec:

```go
type Manifest struct { SchemaVersion int `json:"schema_version"`; HostID string `json:"host_id"`; Host HostInfo `json:"host"`; ObservedAt time.Time `json:"observed_at"`; Projects []ProjectManifest `json:"projects"`; Worktrees []WorktreeManifest `json:"worktrees"` }
type HostInfo struct { Hostname string `json:"hostname"`; Platform string `json:"platform"` }
type ProjectManifest struct { Identity string `json:"identity"`; Name string `json:"name"`; LocalRoot string `json:"local_root"`; RemoteURL string `json:"remote_url"` }
type ChangeStatus struct { Modified int `json:"modified"`; Added int `json:"added"`; Deleted int `json:"deleted"`; Untracked int `json:"untracked"`; Staged int `json:"staged"`; Conflicts int `json:"conflicts"` }
type WorktreeManifest struct { ProjectIdentity string `json:"project_identity"`; Kind string `json:"kind"`; Ref string `json:"ref"`; Branch string `json:"branch,omitempty"`; Path string `json:"path"`; Head string `json:"head"`; HeadTime time.Time `json:"head_time"`; Upstream string `json:"upstream,omitempty"`; Ahead int `json:"ahead"`; Behind int `json:"behind"`; Status ChangeStatus `json:"status"`; LastActivity time.Time `json:"last_activity"`; IsMain bool `json:"is_main"` }
type FleetState struct { SchemaVersion int `json:"schema_version"`; StateVersion string `json:"state_version"`; Hosts []HostState `json:"hosts"`; Rows []FleetRow `json:"rows"`; Warnings []Warning `json:"warnings,omitempty"` }
type HostState struct { HostID string `json:"host_id"`; Hostname string `json:"hostname"`; Platform string `json:"platform"`; ObservedAt time.Time `json:"observed_at"` }
type FleetRow struct { ProjectIdentity string `json:"project_identity"`; ProjectName string `json:"project_name"`; Kind string `json:"kind"`; Ref string `json:"ref"`; Branch string `json:"branch,omitempty"`; Observations []Observation `json:"observations"` }
type Observation struct { HostID string `json:"host_id"`; Path string `json:"path"`; Head string `json:"head"`; HeadTime time.Time `json:"head_time"`; Upstream string `json:"upstream,omitempty"`; Ahead int `json:"ahead"`; Behind int `json:"behind"`; Status ChangeStatus `json:"status"`; LastActivity time.Time `json:"last_activity"`; ObservedAt time.Time `json:"observed_at"`; IsMain bool `json:"is_main"` }
type Warning struct { Code string `json:"code"`; HostID string `json:"host_id,omitempty"`; Message string `json:"message"` }
```

Create `internal/fleet/identity.go`:

- `NormalizeHostID(raw string) (string, error)`: `strings.TrimSpace`, `strings.ToLower`, validate `^[a-z0-9._-]+$`.
- `DefaultHostID(hostname func() (string, error)) (string, error)`: calls injected hostname function and normalizes.
- `NormalizeRepositoryIdentity(raw string) (string, error)`: call existing `internal/url.ParseRepositoryURL`; return `RepositoryInfo.FullPath`; reject empty identity.

- [ ] **Step 8: Run focused tests**

Run:

```bash
go test ./internal/config -run 'TestLoadFleet' -count=1
go test ./internal/fleet -run 'TestNormalize|TestDefaultHostID|TestManifestJSONWireShape|TestFleetStateJSONWireShape' -count=1
```

Expected: PASS.

- [ ] **Step 9: Commit Task 1**

Run:

```bash
git add pkg/models/models.go internal/config/config.go internal/config/config_test.go internal/fleet/types.go internal/fleet/types_test.go internal/fleet/identity.go internal/fleet/identity_test.go
git commit -m "Add fleet config and identity"
```

---

### Task 2: Local Manifest Builder

**Files:**

- Create: `internal/fleet/manifest.go`
- Create: `internal/fleet/manifest_test.go`

- [ ] **Step 1: Write failing manifest builder tests**

Create tests that use temporary Git repos and real `git worktree` commands:

```go
func TestBuildManifestIncludesConfiguredProjectWorktrees(t *testing.T) {
    repo := initFleetTestRepo(t, "https://github.com/kenn-io/kwt.git")
    wtPath := filepath.Join(t.TempDir(), "feature-fleet")
    runGit(t, repo, "worktree", "add", "-b", "feature/fleet", wtPath)

    cfg := &models.Config{Fleet: models.FleetConfig{HostID: "host-a"}, Projects: []models.Project{{
        Repository: "github.com/kenn-io/kwt",
        Name: "kwt",
        Path: repo,
    }}}
    manifest, err := NewManifestBuilder(ManifestBuilderOptions{
        Now: func() time.Time { return fixedTime },
        Hostname: func() (string, error) { return "Host-A", nil },
    }).Build(context.Background(), cfg)

    require.NoError(t, err)
    assert.Equal(t, 1, manifest.SchemaVersion)
    assert.Equal(t, "host-a", manifest.HostID)
    assert.Contains(t, worktreeRefs(manifest), "branch:feature/fleet")
    assert.Equal(t, "github.com/kenn-io/kwt", manifest.Projects[0].Identity)
}

func TestBuildManifestKeysDetachedWorktreeByHead(t *testing.T) {
    repo := initFleetTestRepo(t, "https://github.com/kenn-io/kwt.git")
    head := strings.TrimSpace(runGitOutput(t, repo, "rev-parse", "HEAD"))
    wtPath := filepath.Join(t.TempDir(), "detached")
    runGit(t, repo, "worktree", "add", "--detach", wtPath, head)

    manifest, err := buildTestManifestForRepo(t, repo)
    require.NoError(t, err)
    detached := findKind(t, manifest, "detached")
    assert.Equal(t, head, detached.Ref)
    assert.Equal(t, head, detached.Head)
}
```

Also add tests for dirty counts and `LastActivity` being populated using a modified/untracked file.

Add a project identity override test where the repo remote is `https://github.com/fork/kwt.git` but `cfg.Projects[0].Repository` is `github.com/kenn-io/kwt`; the resulting manifest must use the configured canonical identity for project and worktree rows.

- [ ] **Step 2: Run manifest tests and verify they fail**

Run: `go test ./internal/fleet -run 'TestBuildManifest' -count=1`

Expected: FAIL because manifest builder does not exist.

- [ ] **Step 3: Implement `ManifestBuilder`**

Create `ManifestBuilderOptions` with injectable `Now`, `Hostname`, and optional `DiscoverGlobalWorktrees`/`ListProjectWorktrees` hooks for tests.

Implementation requirements:

- Resolve host ID from config or hostname using Task 1 helpers.
- Include configured `cfg.Projects` with normalized identity, display name, local root, and remote URL when available.
- List worktrees for each configured project with `git.New(project.Path).ListWorktrees()`.
- Include global base-dir worktrees from `discovery.DiscoverGlobalWorktrees(cfg.Worktree.BaseDir)` and de-duplicate by path.
- For each worktree:
  - `kind = "branch"` and `ref = branch` unless branch is empty or `HEAD`.
  - `kind = "detached"` and `ref = head` for detached worktrees.
  - `head = git rev-parse HEAD`.
  - `head_time = git show -s --format=%cI HEAD`.
  - `upstream = git rev-parse --abbrev-ref <branch>@{upstream}` when branch worktree has upstream.
  - `ahead` and `behind` are local rev-list counts relative to upstream when available.
  - `status` uses existing `status.StatusCollector` logic or equivalent porcelain parsing without network fetch, converted into `fleet.ChangeStatus` without `ahead`/`behind`.
  - `last_activity` uses existing status collector behavior.
- Ignore missing project paths and broken worktrees; do not fail the whole manifest unless host ID/config is invalid.

- [ ] **Step 4: Run focused manifest tests**

Run: `go test ./internal/fleet -run 'TestBuildManifest' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit Task 2**

Run:

```bash
git add internal/fleet/manifest.go internal/fleet/manifest_test.go
git commit -m "Build fleet manifests"
```

---

### Task 3: Hub Store And Grouped State

**Files:**

- Create: `internal/fleet/store.go`
- Create: `internal/fleet/store_test.go`

- [ ] **Step 1: Write failing store tests**

Create tests for latest-per-host storage, deletion, atomic persistence, grouped rows, warnings, and deterministic state version:

```go
func TestStorePutLatestAndBuildStateGroupsRows(t *testing.T) {
    store := NewFileStore(filepath.Join(t.TempDir(), "state.json"))
    first := testManifest("host-a", "Host-A", "darwin/arm64", "github.com/kenn-io/kwt", "branch", "feature/fleet", "aaa")
    second := testManifest("host-b", "Host-B", "darwin/arm64", "github.com/kenn-io/kwt", "branch", "feature/fleet", "bbb")

    require.NoError(t, store.Put(context.Background(), first))
    require.NoError(t, store.Put(context.Background(), second))
    state, err := store.State(context.Background())

    require.NoError(t, err)
    require.Len(t, state.Rows, 1)
    assert.Equal(t, "github.com/kenn-io/kwt", state.Rows[0].ProjectIdentity)
    assert.Equal(t, "feature/fleet", state.Rows[0].Ref)
    assert.ElementsMatch(t, []string{"host-a", "host-b"}, observationHosts(state.Rows[0]))
    assert.NotEmpty(t, state.StateVersion)
}

func TestStoreHostCollisionWarning(t *testing.T) {
    store := NewFileStore(filepath.Join(t.TempDir(), "state.json"))
    require.NoError(t, store.Put(ctx, testManifestWithHost("same", "one", "darwin/arm64")))
    require.NoError(t, store.Put(ctx, testManifestWithHost("same", "two", "linux/amd64")))
    state, err := store.State(ctx)
    require.NoError(t, err)
    require.Len(t, state.Warnings, 1)
    assert.Equal(t, "host_id_collision", state.Warnings[0].Code)
}
```

- [ ] **Step 2: Run store tests and verify they fail**

Run: `go test ./internal/fleet -run 'TestStore' -count=1`

Expected: FAIL because store does not exist.

- [ ] **Step 3: Implement file store**

Implement:

- `type FileStore struct { path string; mu sync.Mutex }`.
- `NewFileStore(path string) *FileStore`.
- `Put(ctx context.Context, manifest Manifest) error`.
- `Delete(ctx context.Context, hostID string) error`.
- `State(ctx context.Context) (FleetState, error)`.
- Persistent shape stores raw manifests and warnings:

```go
type storeFile struct {
    Hosts map[string]Manifest `json:"hosts"`
    Warnings []Warning `json:"warnings,omitempty"`
}
```

Validation:

- Manifest schema version must be 1.
- Host ID must pass `NormalizeHostID` and match manifest host ID.
- Project identities and worktree project identities must be non-empty.

Persistence:

- Create parent dir with `0700`.
- Write JSON to temp file, close, then `os.Rename` to `store_path`.
- Read missing store as empty.

State grouping:

- Sort hosts, rows, observations, and warnings deterministically.
- Group rows by `(project_identity, kind, ref)`.
- Compute `state_version` as `sha256:` plus the SHA-256 of canonical JSON of hosts/rows/warnings with `StateVersion` empty.

- [ ] **Step 4: Run focused store tests**

Run: `go test ./internal/fleet -run 'TestStore' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit Task 3**

Run:

```bash
git add internal/fleet/store.go internal/fleet/store_test.go
git commit -m "Store fleet manifests"
```

---

### Task 4: Hub HTTP Server

**Files:**

- Create: `internal/fleet/server.go`
- Create: `internal/fleet/server_test.go`
- Modify: `go.mod`
- Modify: `go.sum`

- [ ] **Step 1: Add kit dependency**

Run: `go get go.kenn.io/kit@latest`

Expected: `go.mod` and `go.sum` include `go.kenn.io/kit`.

- [ ] **Step 2: Write failing server tests**

Create `internal/fleet/server_test.go` with `httptest`:

```go
func TestServerRejectsMissingBearerToken(t *testing.T) {
    srv := NewServer(ServerOptions{Store: NewMemoryStoreForTest(), Token: "secret"})
    req := httptest.NewRequest(http.MethodGet, "/api/v1/fleet/state", nil)
    rec := httptest.NewRecorder()
    srv.ServeHTTP(rec, req)
    assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestServerStoresManifestAndReturnsGroupedStateWithETag(t *testing.T) {
    srv := NewServer(ServerOptions{Store: NewFileStore(filepath.Join(t.TempDir(), "state.json")), Token: "secret"})
    body := encodeJSON(t, testManifest("host-a", "Host-A", "darwin/arm64", "github.com/kenn-io/kwt", "branch", "feature/fleet", "aaa"))
    post := httptest.NewRequest(http.MethodPost, "/api/v1/fleet/hosts/host-a/manifest", bytes.NewReader(body))
    post.Header.Set("Authorization", "Bearer secret")
    rec := httptest.NewRecorder()
    srv.ServeHTTP(rec, post)
    require.Equal(t, http.StatusNoContent, rec.Code)

    get := httptest.NewRequest(http.MethodGet, "/api/v1/fleet/state", nil)
    get.Header.Set("Authorization", "Bearer secret")
    rec = httptest.NewRecorder()
    srv.ServeHTTP(rec, get)
    require.Equal(t, http.StatusOK, rec.Code)
    assert.NotEmpty(t, rec.Header().Get("ETag"))

    get304 := httptest.NewRequest(http.MethodGet, "/api/v1/fleet/state", nil)
    get304.Header.Set("Authorization", "Bearer secret")
    get304.Header.Set("If-None-Match", rec.Header().Get("ETag"))
    rec304 := httptest.NewRecorder()
    srv.ServeHTTP(rec304, get304)
    assert.Equal(t, http.StatusNotModified, rec304.Code)
}
```

Also test:

- `GET /api/v1/ping`.
- invalid path/body host mismatch.
- invalid host ID path.
- unknown schema version.
- body over 1 MiB returns `413`.
- unsupported methods return `405`.

- [ ] **Step 3: Run server tests and verify they fail**

Run: `go test ./internal/fleet -run 'TestServer' -count=1`

Expected: FAIL because server does not exist.

- [ ] **Step 4: Implement server**

Implement `NewServer(ServerOptions)` returning `http.Handler`:

```go
type ServerOptions struct {
    Store Store
    Token string
    Service string
    Version string
    PID int
    MaxManifestBytes int64
}
```

Use `Authorization: Bearer <token>` for every `/api/v1/fleet/*` route. `GET /api/v1/ping` can be unauthenticated.

Routes:

- `GET /api/v1/ping`: JSON `{ "ok": true, "service": "kwt-fleet", "version": "...", "pid": ... }`.
- `POST /api/v1/fleet/hosts/{host_id}/manifest`: `http.MaxBytesReader`, decode, validate path/body host ID, `Store.Put`, return `204`.
- `GET /api/v1/fleet/state`: `Store.State`, set `ETag` to quoted `state_version`, honor `If-None-Match`.
- `DELETE /api/v1/fleet/hosts/{host_id}`: validate host ID, `Store.Delete`, return `204`.

- [ ] **Step 5: Add endpoint parsing helper tests**

Add tests for `ParseHubEndpoint` or equivalent helper:

- `127.0.0.1:8787` accepted.
- `100.64.1.2:8787` accepted.
- `0.0.0.0:8787` rejected.
- `8.8.8.8:8787` rejected.

- [ ] **Step 6: Implement endpoint helper using kit**

Use:

```go
daemon.ParseEndpoint(raw, daemon.ParseEndpointOptions{
    DefaultTCPAddress: "",
    TCPPolicy: daemon.RequireNonPublic,
})
```

Do not add autostart.

- [ ] **Step 7: Run focused server tests**

Run: `go test ./internal/fleet -run 'TestServer|TestParseHubEndpoint' -count=1`

Expected: PASS.

- [ ] **Step 8: Commit Task 4**

Run:

```bash
git add internal/fleet/server.go internal/fleet/server_test.go go.mod go.sum
git commit -m "Serve fleet hub API"
```

---

### Task 5: Fleet Client, Tokens, And Best-Effort Publish

**Files:**

- Create: `internal/fleet/client.go`
- Create: `internal/fleet/client_test.go`

- [ ] **Step 1: Write failing token/client tests**

Create tests:

```go
func TestLoadTokenFromFileAndEnv(t *testing.T) {
    tokenFile := filepath.Join(t.TempDir(), "token")
    require.NoError(t, os.WriteFile(tokenFile, []byte("file-secret\n"), 0o600))
    got, err := LoadToken(models.FleetConfig{TokenFile: tokenFile})
    require.NoError(t, err)
    assert.Equal(t, "file-secret", got)

    t.Setenv("KWT_FLEET_TOKEN", "env-secret")
    got, err = LoadToken(models.FleetConfig{TokenEnv: "KWT_FLEET_TOKEN"})
    require.NoError(t, err)
    assert.Equal(t, "env-secret", got)
}

func TestClientUsesBearerTokenAndTimeout(t *testing.T) {
    var gotAuth string
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        gotAuth = r.Header.Get("Authorization")
        w.WriteHeader(http.StatusNoContent)
    }))
    defer server.Close()

    client := NewClient(ClientOptions{HubURL: server.URL, Token: "secret", Timeout: 2 * time.Second})
    manifest := testManifest("host-a", "Host-A", "darwin/arm64", "github.com/kenn-io/kwt", "branch", "feature/fleet", "aaa")
    err := client.Publish(context.Background(), manifest)
    require.NoError(t, err)
    assert.Equal(t, "Bearer secret", gotAuth)
}
```

Also test `State`, `Forget`, and `PublishBestEffort` returning nil/no fatal error on unreachable hub.

- [ ] **Step 2: Run client tests and verify they fail**

Run: `go test ./internal/fleet -run 'TestLoadToken|TestClient|TestPublishBestEffort' -count=1`

Expected: FAIL because client code does not exist.

- [ ] **Step 3: Implement client**

Implement:

- `LoadToken(cfg models.FleetConfig) (string, error)`: prefer `TokenFile` when set, otherwise `TokenEnv`; trim whitespace; reject empty.
- `NewClient(ClientOptions)` with default timeout 2 seconds.
- `Publish(ctx, manifest) error`.
- `State(ctx, etag string) (FleetState, string, bool, error)` returning state, response ETag, not-modified bool.
- `Forget(ctx, hostID string) error`.
- `PublishBestEffort(ctx, cfg, builder, warn io.Writer)`: no-op when fleet disabled, short timeout, warning only on failure.
- `EffectiveHubURL(cfg)`: if `HubURL` empty and `Hub.ListenAddr` set, return `http://` + listen address.

- [ ] **Step 4: Run focused client tests**

Run: `go test ./internal/fleet -run 'TestLoadToken|TestClient|TestPublishBestEffort' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit Task 5**

Run:

```bash
git add internal/fleet/client.go internal/fleet/client_test.go
git commit -m "Add fleet hub client"
```

---

### Task 6: Fleet CLI Surface And Status Rendering

**Files:**

- Create: `internal/fleet/render.go`
- Create: `internal/fleet/render_test.go`
- Create: `internal/cmd/fleet.go`
- Create: `internal/cmd/fleet_test.go`

- [ ] **Step 1: Write failing render tests**

Create `internal/fleet/render_test.go`:

```go
func TestRenderRowsMarksMissingMaterializedAndDiffering(t *testing.T) {
    state := FleetState{Rows: []FleetRow{{
        ProjectIdentity: "github.com/kenn-io/kwt",
        ProjectName: "kwt",
        Kind: "branch",
        Ref: "feature/fleet",
        Branch: "feature/fleet",
        Observations: []Observation{
            {HostID: "host-a", Head: "aaa", ObservedAt: now.Add(-time.Minute)},
            {HostID: "host-b", Head: "bbb", Status: ChangeStatus{Modified: 1}, ObservedAt: now.Add(-2 * time.Minute)},
        },
    }}}

    rows := BuildStatusRows(state, "host-a", now)
    require.Len(t, rows, 1)
    assert.Equal(t, "materialized", rows[0].Local)
    assert.Contains(t, rows[0].Sync, "differs from host-b")
    assert.Contains(t, rows[0].Dirty, "host-b")

    rows = BuildStatusRows(state, "air", now)
    assert.Equal(t, "missing", rows[0].Local)
}
```

- [ ] **Step 2: Run render tests and verify they fail**

Run: `go test ./internal/fleet -run 'TestRenderRows' -count=1`

Expected: FAIL because render code does not exist.

- [ ] **Step 3: Implement render helpers**

Implement:

- `type StatusRow struct { Project, Ref, Local, Hosts, Sync, Dirty, Freshness string }`.
- `BuildStatusRows(state FleetState, currentHost string, now time.Time) []StatusRow`.
- `RenderStatusTable(w io.Writer, rows []StatusRow) error` using `internal/table`.

Rules:

- Local is `materialized` if any observation host matches current host, otherwise `missing`.
- Sync says `same` when all heads equal or only one observation, otherwise `differs from <host> (<age>)`.
- Dirty lists hosts with modified/staged/untracked/conflict counts, otherwise `clean`.
- Freshness uses observed age from newest observation.

- [ ] **Step 4: Write failing CLI command tests**

Create `internal/cmd/fleet_test.go` with package-level stubs for command dependencies:

- `kwt fleet` command exists and has subcommands `serve`, `publish`, `status`, `forget`.
- `runFleetStatus` publishes first but still renders state if publish returns an error.
- `runFleetForget` calls client delete for host ID.
- `runFleetPublish` errors when fleet disabled or token missing.

Use package-level variables in `fleet.go` to inject:

```go
var (
    newFleetManifestBuilder = fleet.NewManifestBuilder
    newFleetClient = fleet.NewClientFromConfig
    runFleetServer = runFleetServe
)
```

- [ ] **Step 5: Run CLI tests and verify they fail**

Run: `go test ./internal/cmd -run 'TestFleet' -count=1`

Expected: FAIL because command does not exist.

- [ ] **Step 6: Implement `internal/cmd/fleet.go`**

Add:

- `fleetCmd` root command.
- `fleetServeCmd`.
- `fleetPublishCmd`.
- `fleetStatusCmd`.
- `fleetForgetCmd`.

Behavior:

- Commands load config with `config.Load()`.
- If fleet disabled, `publish/status/forget/serve` return a clear error.
- `serve` requires `[fleet.hub].listen_addr`, token, and store path. It parses endpoint with fleet helper, creates `fleet.NewServer`, calls `daemon.Listen`, writes a runtime record with service `kwt-fleet`, then serves foreground HTTP.
- `publish` builds manifest and posts to hub.
- `status` best-effort publishes, fetches state, renders table, and prints publish warning to stderr on publish failure.
- `forget` sends `DELETE`.

- [ ] **Step 7: Run focused CLI/render tests**

Run:

```bash
go test ./internal/fleet -run 'TestRenderRows' -count=1
go test ./internal/cmd -run 'TestFleet' -count=1
```

Expected: PASS.

- [ ] **Step 8: Commit Task 6**

Run:

```bash
git add internal/fleet/render.go internal/fleet/render_test.go internal/cmd/fleet.go internal/cmd/fleet_test.go
git commit -m "Add fleet CLI commands"
```

---

### Task 7: Publish Hooks And Documentation

**Files:**

- Modify: `internal/cmd/add.go`
- Modify: `internal/cmd/add_test.go`
- Modify: `internal/cmd/remove.go`
- Create: `internal/cmd/remove_test.go`
- Modify: `internal/cmd/prune.go`
- Create: `internal/cmd/prune_test.go`
- Modify: `README.md`

- [ ] **Step 1: Write failing mutation hook tests**

Add tests using a package-level hook function:

```go
func TestAddPublishesFleetManifestAfterSuccessfulCreate(t *testing.T) {
    var calls int
    old := publishFleetBestEffort
    publishFleetBestEffort = func(context.Context, *models.Config, io.Writer) { calls++ }
    t.Cleanup(func() { publishFleetBestEffort = old })
    // Use existing add command test setup to create a worktree successfully with cfg.Fleet.Enabled = true.
    // Assert calls == 1.
}

func TestAddDoesNotPublishFleetManifestOnFailure(t *testing.T) {
    // Force worktree creation failure.
    // Assert calls == 0.
}
```

Add equivalent tests for:

- `remove` successful non-dry-run removal publishes once after command.
- `remove --dry-run` does not publish.
- `prune` successful prune publishes once.
- `prune --expired --dry-run` does not publish.

- [ ] **Step 2: Run hook tests and verify they fail**

Run: `go test ./internal/cmd -run 'Test(Add|Remove|Prune).*Fleet' -count=1`

Expected: FAIL because hook is not wired.

- [ ] **Step 3: Implement best-effort publish hook**

In `internal/cmd/fleet.go`, expose package-level:

```go
var publishFleetBestEffort = func(ctx context.Context, cfg *models.Config, warn io.Writer) {
    fleet.PublishBestEffort(ctx, cfg, fleet.NewManifestBuilder(fleet.ManifestBuilderOptions{}), warn)
}
```

In mutation commands:

- After `ctx.WorktreeManager.Add` succeeds and after expiration registry work succeeds, call `publishFleetBestEffort(context.Background(), ctx.Config, os.Stderr)` before optional tmux attach.
- In local/global remove, track whether any worktree was removed; call once after the loop when not dry-run.
- In `prune`, call after successful normal prune.
- In `prune --expired`, call when at least one worktree was actually removed/unregistered and not dry-run.

The hook must no-op when fleet disabled and never return an error to the mutation command.

- [ ] **Step 4: Update README**

Add a short opt-in Fleet Sync section:

- Config example with `[fleet]` and `[fleet.hub]`.
- Mention token from `token_file` or `token_env`.
- Commands: `kwt fleet serve`, `publish`, `status`, `forget`.
- State v1 is advisory and does not sync dirty files or auto-create/remove worktrees.

- [ ] **Step 5: Run focused hook tests**

Run: `go test ./internal/cmd -run 'Test(Add|Remove|Prune).*Fleet' -count=1`

Expected: PASS.

- [ ] **Step 6: Commit Task 7**

Run:

```bash
git add internal/cmd/add.go internal/cmd/add_test.go internal/cmd/remove.go internal/cmd/remove_test.go internal/cmd/prune.go internal/cmd/prune_test.go README.md
git commit -m "Publish fleet manifests after mutations"
```

---

### Task 8: Full Verification And PR Prep

**Files:**

- No new files expected unless earlier verification reveals focused fixes.

- [ ] **Step 1: Run full test suite**

Run: `go test ./...`

Expected: PASS.

- [ ] **Step 2: Run build**

Run: `go build ./cmd/kwt`

Expected: PASS.

- [ ] **Step 3: Run focused CLI smoke checks**

Run:

```bash
tmp=$(mktemp -d)
export KWT_HOME="$tmp/kwt-home"
go run ./cmd/kwt fleet status
```

Expected: exits non-zero with a clear message that fleet is disabled.

Then create a minimal fleet config with invalid public listen:

```bash
mkdir -p "$KWT_HOME"
cat > "$KWT_HOME/config.toml" <<'TOML'
[worktree]
basedir = "~/worktrees"
auto_mkdir = true

[fleet]
enabled = true
host_id = "test-host"
hub_url = "http://127.0.0.1:1"
token_env = "KWT_FLEET_TOKEN"

[fleet.hub]
listen_addr = "0.0.0.0:8787"
store_path = "./state.json"
TOML
export KWT_FLEET_TOKEN=secret
go run ./cmd/kwt fleet serve
```

Expected: exits non-zero rejecting the unspecified listen address.

- [ ] **Step 4: Inspect git status**

Run: `git status --short --branch`

Expected: clean working tree on `feature/fleet-sync`.

- [ ] **Step 5: Push and open PR**

Run:

```bash
git push -u origin feature/fleet-sync
gh pr create --fill
```

Expected: PR opened against `main`.
