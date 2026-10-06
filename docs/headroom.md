# Headroom variant

Headroom is optional. Direct native execution remains the default. The variant
uses a separate local Docker image and one proxy per provider invocation, so the
shared SDLC runtime does not need another Python dependency or a rebuild.

```sh
sdlc runtime headroom build
sdlc runtime headroom status
sdlc run --reference EXAMPLE --ticket 01-example.md --headroom passthrough
sdlc run --reference EXAMPLE --ticket 01-example.md --headroom optimize
sdlc run --reference EXAMPLE --all --headroom optimize --dry-run
```

Use separate equivalent tickets for comparisons; do not launch the same owned
ticket twice concurrently. Review [provider usage](provider-usage.md) and
[usage metrics](usage-metrics.md) before a connected trial.

## Modes

| Selection | Behaviour |
| --- | --- |
| `off` | Existing direct native route; no proxy |
| `passthrough` | Native requests pass through Headroom with optimization disabled |
| `optimize` | Conservative cache-aware, lossless tool-output compression |

The CLI builds Headroom `0.39.1` from the fixed embedded recipe, labels the
`lossless-v1` policy and records the resulting Docker image ID. Building uses the
Dockerfile over stdin, with no checkout or credential build context. Public image
and package downloads are needed at build time; tokenizers are baked into the
image for runtime use. Runs use `--pull never` with the recorded image ID.

The image and policy are frozen before execution. Resume uses the saved selection
and refuses a mode change or missing image. Feature previews include the mode,
models, checks and input selections; an executing feature resolves one image and
uses it for each ticket. A tag update cannot silently change a saved run.

## Native routing

The official Codex or Claude client performs authentication and sends requests.
Codex receives the built-in inference URL override; Claude receives only its
documented base URL. Native login/status still use the direct route. This supports
the documented account-auth forwarding route; it does not convert subscriptions
into API credentials or approve other execution modes.

The proxy listens on loopback in its own Docker network namespace. The provider
worker joins that namespace and reaches it at `127.0.0.1:8787`. No host port is
published, and no credentials or workspace are mounted into the proxy. Authentication
headers necessarily pass through its memory on the way to the provider. Container
logs are disabled, writable state is temporary, and the proxy is removed after
worker cleanup and numeric stats collection. Cleanup failures stop the invocation.

The policy disables CCR retrieval, semantic response cache, learned compression
and its fallback, user-message compression, tool search/deduplication, output
shaping, model routing and read-lifecycle transformations. Headroom retries are
limited to one attempt. Beacon/update calls and external telemetry are disabled;
local aggregate statistics remain enabled. Optimization can leave an output
unchanged when it cannot classify it safely. Engineering quality still needs
comparison with the direct run.

Interactive provider sessions, login, checks and publication workers do not use
this variant. It applies to native headless ticket implementation, repairs and
independent review, including those scheduled by a feature controller.

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
