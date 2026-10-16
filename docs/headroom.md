# Headroom proxy

New ticket, feature and PR-adoption runs require a saved external Headroom proxy
address. Configure it once, independently of a run:

```sh
sdlc runtime headroom set --proxy-url http://127.0.0.1:8787
sdlc runtime headroom status
sdlc run --reference EXAMPLE --ticket 01-example.md --dry-run
```

Use the proxy origin, without `/dashboard` or `/v1`. HTTP and HTTPS are supported;
credentials, paths, query strings and fragments are rejected. Settings live in
`headroom.json` in SDLC's private state directory (`SDLC_STATE_DIR`, or the OS
configuration directory followed by `sdlc`). No project-file or per-run flag is
needed. A missing or invalid address stops new plans and runs with setup guidance.
Setting or inspecting the address makes no network request and does not verify
connectivity, proxy version, compression settings or account access.

New runs freeze the address in their private plan. Changing the saved address
applies to later runs; resumed tickets and features retain their recorded route.
This includes existing direct and managed-proxy runs. New runs cannot use
`--headroom off|passthrough|optimize` to override the saved setting. The flag and
`sdlc runtime headroom build` remain available for legacy saved runs only.

## Docker connectivity

Provider workers run in Docker. SDLC translates `localhost`, `127.0.0.1` and other
loopback IP addresses to `host.docker.internal`, preserving the port. Docker
Desktop supplies this hostname; on Linux SDLC adds the Docker host-gateway
mapping. A proxy bound only to the host's loopback interface may need a listen
address reachable from Docker. On Linux, the host-gateway mapping does not expose
a loopback-only listener. Use a reachable host interface and restrict access to
trusted local workers. Remote Docker engines require an address reachable from
that engine; a loopback address refers to the Docker host, not the CLI machine.

SDLC does not start, configure, stop, restart or collect aggregate statistics from
your external proxy. You control its availability, logging, compression, routing,
retry policy and telemetry. Native provider token and context observations remain
available; per-run Headroom savings are unknown because shared totals may include
other clients. There is no automatic fallback to direct inference or a managed
proxy when the external proxy fails.

## Native routing and trust

The unmodified official clients retain authentication and account handling. Codex
uses its built-in inference URL override with `/v1`; Claude uses
`ANTHROPIC_BASE_URL`. Login and status remain direct. Selecting a proxy trusts it
with native inference headers and request bodies, including account authentication
in transit. Configure only a proxy you control. SDLC neither extracts credentials
nor supplies replacement authentication.

Review [provider usage](provider-usage.md) before connected work. The external
proxy must preserve native protocols, authentication headers and provider limits;
setting an address does not establish that its configuration meets these rules.
Interactive provider sessions, login, checks and publication workers do not use
this inference route.

## Legacy managed proxy

Existing saved `passthrough` and `optimize` plans retain their pinned Headroom
0.39.1 image and `lossless-v1` policy. They still start an invocation-scoped proxy,
collect its bounded numeric statistics and remove it after worker cleanup. The
image can be rebuilt with `sdlc runtime headroom build`; new runs use the required
external address instead.

## Validation

Offline boundary tests check mode validation, frozen images, preview changes,
official-client arguments, cleanup and private numeric records. The opt-in Docker
probe uses a fixed fake upstream and disables external networking. It checks
Responses HTTP/SSE and WebSockets, Anthropic HTTP/SSE, structured response schema,
OAuth capability-header forwarding, 429 forwarding, passthrough preservation and
round-trip expansion of compressed timestamped tool logs. Run it locally:

```sh
SDLC_HEADROOM_DOCKER_TESTS=1 go test -count=1 -timeout 5m ./internal/headroom -run Docker
```

The probe needs the separately built image. It does not contact provider services.
The pinned unmodified Codex client also has a route-only probe: disposable native
account data must produce a WebSocket inference handshake at the local override
with native authentication headers. It uses the existing local SDLC runtime,
verifies Codex `0.160.0` and disables external networking:

```sh
SDLC_HEADROOM_NATIVE_DOCKER_TESTS=1 go test -count=1 -timeout 2m ./internal/providerauth -run NativeHeadroomRoute
```

Set `SDLC_HEADROOM_NATIVE_IMAGE` to the recorded local image ID to choose a runtime
explicitly. This checks endpoint selection and native headers; the separate proxy
transport probe checks the forwarding protocols. It does not simulate a completed
engineering job or prove real subscription access.

Supervised private trials have exercised native account forwarding with both
official clients, resumed implementation, isolated checks, signed draft
publication, CI and independent review. These results establish the tested local
workflow, not typical savings or permission for other accounts and execution
modes. Connected trials still require explicit authorisation. Use equivalent
tickets and retain repair history and metric coverage; passthrough comparisons
are needed to isolate forwarding overhead from optimization and model variation.
