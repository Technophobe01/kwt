# Fleet Insecure HTTP Consent Design

## Context

The fleet client sends a bearer token with every hub request. It currently
allows plaintext HTTP for loopback and for tailnet IP literals confirmed by
the local Tailscale daemon. That confirmation proves only that an address is a
tailnet member; it does not prove that this process's connection uses a
Tailscale-owned route. The default Go HTTP transport may also honor
`HTTP_PROXY`, including for tailnet IPs.

Operators still need a deliberate way to use plaintext HTTP for trusted
deployments, including Tailscale setups that use hostnames, userspace
networking, or an explicit proxy. The safety boundary should therefore be an
explicit configuration choice instead of inferred Tailscale membership.

## Configuration Contract

Add a boolean `allow_insecure` key to the global `[fleet]` configuration:

```toml
[fleet]
enabled = true
hub_url = "http://hub.example.internal:8787"
allow_insecure = true
token_file = "~/.config/kwt/fleet.token"
```

The default is `false`. Repository-local `.kwt.toml` files must not be able to
set or override this key, consistent with the other fleet credentials and
endpoint settings.

## Client Policy

Bearer-authenticated hub URLs follow these rules:

- HTTPS is always accepted.
- Plaintext HTTP to loopback is accepted without additional configuration.
- Plaintext HTTP to any non-loopback host is rejected unless
  `[fleet].allow_insecure = true`.
- When `allow_insecure` is true, the client does not require a Tailscale IP,
  daemon status, interface, or route check.
- The existing Go transport behavior remains intact, including environment
  proxy support. Explicit consent therefore covers plaintext bearer transport
  through a configured proxy as well as a direct connection.

The rejection message must name `fleet.allow_insecure` and recommend HTTPS so
the opt-in is discoverable but unmistakable.

`allow_insecure` must be carried through every fleet client construction path,
including explicit sync commands and best-effort publishing after mutations.

## Hub Listener Policy

This change applies only to outbound bearer-token transport. Hub listener
validation remains unchanged: the hub may listen on loopback or on one of the
local machine's tailnet addresses verified through the Tailscale daemon.

## Documentation

The README, multi-machine sync guide, architecture document, and configuration
reference must describe the new opt-in. They must warn that enabling it can
send the bearer token in plaintext over the network or an environment-configured
proxy and should be used only when the operator trusts that transport, such as
an appropriately controlled tailnet.

## Testing

Regression coverage must demonstrate:

1. A non-loopback plaintext URL is rejected by default even when it is a
   Tailscale-range address and `HTTP_PROXY` is set; no request reaches the
   proxy.
2. The same URL, plus plaintext hostnames and private addresses, is accepted
   when `allow_insecure` is true.
3. Loopback HTTP and HTTPS continue to work without the opt-in.
4. Explicit commands and best-effort publishing both propagate the setting.
5. Global config loads `allow_insecure`, while repository-local config cannot
   enable or override it.

## Non-goals

- Proving that the operating system selected a Tailscale-owned route.
- Adding a Tailscale LocalAPI or userspace-networking integration.
- Disabling proxy support for explicitly permitted plaintext requests.
- Relaxing the hub listener policy.
