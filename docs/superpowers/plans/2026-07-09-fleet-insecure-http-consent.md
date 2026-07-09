# Fleet Insecure HTTP Consent Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace inferred Tailscale safety for outbound fleet bearer requests with a default-deny `fleet.allow_insecure` consent flag for non-loopback HTTP.

**Architecture:** The trusted global fleet config owns the consent bit. Every client construction path copies it into `fleet.ClientOptions`, and URL validation checks that explicit policy before an Authorization header can reach the HTTP transport. The server listener keeps its existing verified-tailnet policy; only outbound client policy changes.

**Tech Stack:** Go 1.26, Cobra, Viper/mapstructure, `net/http`, `httptest`, Testify, Markdown documentation.

---

## File Map

- `pkg/models/models.go`: add the global fleet consent field.
- `internal/config/config_test.go`: prove global loading and local-config isolation.
- `internal/fleet/client.go`: carry consent through client construction and enforce it in bearer URL validation.
- `internal/fleet/client_test.go`: exercise default denial, explicit consent, environment-proxy behavior, and best-effort propagation.
- `internal/fleet/tailnet.go`: remove client-only peer verification that becomes obsolete; preserve self-address verification for listeners.
- `internal/fleet/server_test.go`: update the shared tailnet-status fixture after client peer verification is removed.
- `internal/cmd/fleet.go`: pass trusted config consent to validation and the runtime client.
- `internal/cmd/fleet_test.go`: exercise explicit command client construction with the opt-in.
- `README.md`, `docs/multi-machine-sync.md`, `docs/design/multi-machine-sync.md`, `docs/reference/configuration.md`: document the contract and plaintext/proxy warning.

### Task 1: Add the Trusted Configuration Field

**Files:**
- Modify: `internal/config/config_test.go`
- Modify: `pkg/models/models.go`

- [ ] **Step 1: Write failing global and local configuration tests**

In `TestLoadFleetConfigExpandsPaths`, add `allow_insecure = true` under `[fleet]` and assert:

```go
assert.True(t, cfg.Fleet.AllowInsecure)
```

In `TestLoadFleetDefaultsDisabled`, assert the default is false:

```go
assert.False(t, cfg.Fleet.AllowInsecure)
```

In `TestMergeLocalConfig/IgnoresFleetSettings`, put `allow_insecure = true` in the repository-local `[fleet]` table, set the global value false before merging, and assert it remains false:

```go
viper.Set("fleet.allow_insecure", false)
// ... merge local config ...
assert.False(t, viper.GetBool("fleet.allow_insecure"))
```

- [ ] **Step 2: Run the focused tests and verify RED**

Run:

```bash
go test ./internal/config -run 'TestLoadFleetDefaultsDisabled|TestLoadFleetConfigExpandsPaths|TestMergeLocalConfig' -count=1
```

Expected: compile failure because `models.FleetConfig` has no `AllowInsecure` field.

- [ ] **Step 3: Add the minimal model field**

Add to `FleetConfig` in `pkg/models/models.go`:

```go
AllowInsecure bool `mapstructure:"allow_insecure" toml:"allow_insecure"` // Consent to bearer-authenticated non-loopback HTTP
```

No merge exception is needed: `mergeLocalConfig` already rejects every `fleet.*` key.

- [ ] **Step 4: Run the focused tests and verify GREEN**

Run the command from Step 2. Expected: PASS.

### Task 2: Enforce Explicit Client Consent Before Transport

**Files:**
- Modify: `internal/fleet/client_test.go`
- Modify: `internal/fleet/client.go`
- Modify: `internal/fleet/tailnet.go`
- Modify: `internal/fleet/server_test.go`

- [ ] **Step 1: Replace the inferred-tailnet client expectations with consent-policy tests**

Update the plaintext URL cases so default policy rejects public, private,
MagicDNS, and tailnet-range non-loopback hosts. The error assertion must include
`fleet.allow_insecure`.

Replace `TestClientAllowsPlaintextHubURLForVerifiedTailnetPeers` with a table
that constructs clients using:

```go
ClientOptions{
    HubURL:       tt.hubURL,
    Token:        "secret",
    AllowInsecure: true,
}
```

Cover a Tailscale IPv4 literal, a Tailscale IPv6 literal, a MagicDNS hostname,
and a private LAN address. Assert `newRequest` succeeds and carries
`Authorization: Bearer secret`.

Delete the client test that varies Tailscale CLI/backend status. Those states
will no longer participate in outbound client authorization.

- [ ] **Step 2: Add a real environment-proxy subprocess regression test**

Add `TestClientPlaintextProxyRequiresExplicitConsent`. Use `httptest.NewServer`
as an HTTP proxy in the parent test and launch the current test binary as a
helper subprocess so Go's process-cached `http.ProxyFromEnvironment` reads the
test's fresh `HTTP_PROXY` value.

The helper branch should:

```go
client := NewClient(ClientOptions{
    HubURL:        "http://100.64.1.2:8787",
    Token:         "secret",
    AllowInsecure: os.Getenv("KWT_TEST_ALLOW_INSECURE") == "1",
})
_, _, _, err := client.State(context.Background(), "")
```

Run two child processes with `HTTP_PROXY=<proxy URL>` and empty `NO_PROXY`:

1. Without consent, require an error mentioning `fleet.allow_insecure` and
   assert the proxy received zero requests.
2. With `KWT_TEST_ALLOW_INSECURE=1`, have the proxy return `{}` as JSON, require
   success, and assert it received `Authorization: Bearer secret`.

This test exercises the real default transport and documents that explicit
consent includes environment-proxy transport.

- [ ] **Step 3: Update best-effort publishing tests for propagation**

Keep the existing test that skips the expensive manifest build for a rejected
non-loopback HTTP URL. Add a consent-enabled counterpart that calls the
synchronous `publishBestEffort` helper with an already-canceled context:

```go
Fleet: models.FleetConfig{
    Enabled:       true,
    HubURL:        "http://192.0.2.10:8787",
    AllowInsecure: true,
    TokenEnv:      "KWT_FLEET_TOKEN",
}
```

Assert the builder runs once and the returned error is `context.Canceled`, not
the consent-policy rejection. The synchronous helper always builds before it
attempts the request, avoiding the goroutine/select race in the best-effort
wrapper while proving both pre-build validation and the client created after
the build receive the same consent bit.

- [ ] **Step 4: Run the fleet tests and verify RED**

Run:

```bash
go test ./internal/fleet -run 'TestClient|TestPublishBestEffort' -count=1
```

Expected: compile failure for the missing `AllowInsecure` client option and/or
behavior failures because current validation still infers safety from daemon
membership.

- [ ] **Step 5: Implement the minimal consent-aware policy**

In `internal/fleet/client.go`:

1. Add `AllowInsecure bool` to `ClientOptions`.
2. Add `allowInsecure bool` to `Client` and copy the option in `NewClient`.
3. Change validation to accept an explicit boolean:

```go
func ValidateHubURL(raw string, allowInsecure bool) error {
    return validateBearerHubURL(raw, allowInsecure)
}

func validateBearerHubURL(raw string, allowInsecure bool) error {
    parsed, err := url.Parse(raw)
    if err != nil {
        return fmt.Errorf("parse sync hub URL: %w", err)
    }
    if parsed.Scheme != "http" || isLoopbackHost(parsed.Hostname()) || allowInsecure {
        return nil
    }
    return fmt.Errorf(
        "plaintext sync hub URL %q requires explicit consent; use https or set fleet.allow_insecure = true",
        raw,
    )
}
```

4. Pass `c.allowInsecure` from `newRequest`.
5. Pass `cfg.Fleet.AllowInsecure` from both best-effort prevalidation and its
   `ClientOptions`.

- [ ] **Step 6: Remove obsolete client peer verification as a green refactor**

Delete `verifyTailnetPeerAddress` and simplify `verifyTailnetAddress` to the
self-address-only behavior used by `verifyTailnetSelfAddress`. Remove the
unused peer map from `tailnetStatus`. Update `runningTailnetStatus` and the
listener test fixture so the server still proves a peer address is rejected
because it is absent from `Self`.

Do not change `ParseHubEndpoint`, `requireLoopbackOrTailnet`, or listener
semantics.

- [ ] **Step 7: Run focused fleet tests and verify GREEN**

Run:

```bash
go test ./internal/fleet -count=1
```

Expected: PASS, including the subprocess proxy regression and listener tests.

### Task 3: Propagate Consent Through Explicit Sync Commands

**Files:**
- Modify: `internal/cmd/fleet_test.go`
- Modify: `internal/cmd/fleet.go`

- [ ] **Step 1: Write the failing explicit-client test**

Extend `TestDefaultNewFleetClientFromConfigRejectsPlaintextNonLoopbackHub` with
an `allows explicit consent` subtest. Build config with:

```go
models.FleetConfig{
    Enabled:       true,
    HubURL:        "http://192.0.2.10:8787",
    AllowInsecure: true,
    TokenEnv:      "KWT_FLEET_TOKEN",
}
```

Require client construction succeeds. Call `State` with an already-canceled
context and assert its error is not the `fleet.allow_insecure` policy error;
this catches a constructor that validates with consent but forgets to store it
in `ClientOptions`.

- [ ] **Step 2: Run the focused command test and verify RED**

Run:

```bash
go test ./internal/cmd -run TestDefaultNewFleetClientFromConfig -count=1
```

Expected: FAIL because `defaultNewFleetClientFromConfig` does not propagate the
new setting.

- [ ] **Step 3: Pass consent through validation and construction**

Change `defaultNewFleetClientFromConfig` to call:

```go
fleet.ValidateHubURL(hubURL, cfg.Fleet.AllowInsecure)
```

and construct:

```go
fleet.NewClient(fleet.ClientOptions{
    HubURL:        hubURL,
    Token:         token,
    AllowInsecure: cfg.Fleet.AllowInsecure,
})
```

- [ ] **Step 4: Run focused command tests and verify GREEN**

Run the command from Step 2. Expected: PASS.

### Task 4: Update the User Contract Documentation

**Files:**
- Modify: `README.md`
- Modify: `docs/multi-machine-sync.md`
- Modify: `docs/design/multi-machine-sync.md`
- Modify: `docs/reference/configuration.md`

- [ ] **Step 1: Replace inferred Tailscale client language**

In all four documents, state:

- Loopback HTTP needs no opt-in.
- Every non-loopback HTTP hub URL needs `allow_insecure = true` under `[fleet]`.
- The flag permits plaintext bearer transport without proving Tailscale
  membership or routing.
- Go environment proxies remain honored, so the token may cross a configured
  proxy in plaintext.
- Operators should enable it only when they trust the complete transport, such
  as a controlled tailnet.
- HTTPS remains the recommended default.

Keep the existing verified local-tailnet listener documentation unchanged.

- [ ] **Step 2: Format and inspect the documentation diff**

Run:

```bash
git diff --check
git diff -- README.md docs/multi-machine-sync.md docs/design/multi-machine-sync.md docs/reference/configuration.md
```

Expected: no whitespace errors; examples and prose consistently use
`allow_insecure`.

### Task 5: Full Quality Gate, Single Review-Fix Commit, and Ledger Closure

**Files:** all files above.

- [ ] **Step 1: Format modified Go files**

Run:

```bash
gofmt -w pkg/models/models.go internal/config/config_test.go internal/fleet/client.go internal/fleet/client_test.go internal/fleet/tailnet.go internal/fleet/server_test.go internal/cmd/fleet.go internal/cmd/fleet_test.go
```

- [ ] **Step 2: Run the repository quality gate**

Run fresh, in order:

```bash
go test ./internal/config ./internal/fleet ./internal/cmd -count=1
make build
make test
go test -race ./internal/fleet ./internal/cmd
git diff --check
```

Expected: every command exits 0 with no test failures or race reports.

- [ ] **Step 3: Review scope and history**

Run:

```bash
git status --short
git diff --stat
git diff HEAD
git log --format='%s%n%b%n---' -10
```

Confirm only the consent policy, tests, cleanup, and corresponding docs are
included.

- [ ] **Step 4: Commit all review fixes together**

Use the mandatory `kenn:commit` workflow. Create one implementation commit so
the combined-review triage remains atomic. Include this triage summary in the
body:

```text
VALID (fixed): #1, #2 -- explicit fleet.allow_insecure consent replaces inferred route safety and covers proxy transport
INVALID (dismissed): none
PEDANTIC (skipped): none
```

Do not amend or squash the earlier design or plan commits.

- [ ] **Step 5: Close kata issue `qype` with evidence**

After the commit exists, run:

```bash
kata close qype --done \
  --message "Added explicit fleet.allow_insecure consent for non-loopback bearer HTTP; verified proxy policy, config trust boundaries, full tests, build, and race checks." \
  --commit <implementation-sha> \
  --evidence "tests:make test" \
  --evidence "build:make build" \
  --evidence "race:go test -race ./internal/fleet ./internal/cmd"
```

- [ ] **Step 6: Verify the final repository state**

Run:

```bash
git status --short
git log --oneline -3
kata show qype --agent
```

Expected: clean working tree, the implementation commit follows the design and
plan commits, and `qype` is closed with the implementation SHA and evidence.
