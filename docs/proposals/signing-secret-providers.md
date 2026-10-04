# Add one open source signing-secret provider

Status: planned follow-up. No alternative product has been selected or
implemented. The current signing-secret provider is `1password`; making its
name explicit in a profile does not provide a plugin system or another backend.

Implement one open source alternative for retrieving SDLC's dedicated signing
key. Keep GitHub publication, provider/check isolation and public-key verification
at the existing boundary. This work does not change Codex/Claude execution,
GitHub login or the requirement for signed publication.

## Current configuration

Signing profiles record `"provider": "1password"`. Earlier profiles that omit
`provider` continue to select 1Password. `sdlc signing setup --provider 1password`
is the supported setup choice and the default. Unsupported values must fail
before a token is read, setup files are written or a provider request is made.

Status displays the saved secret provider. `configure`, `status` and `verify`
use the provider saved in the profile; they do not accept an override that can
redirect an existing profile's credential. `--provider codex|claude` on execution
and provider-auth commands selects the engineering client, a separate concept.
See [current CLI commands](../cli.md#github-login-and-signing).

## Work to complete

1. Assess maintained open source products with an appropriate license, a
   documented unattended authentication route and a supported client. Record the
   candidate's trust root, bootstrap storage, permission model, operational cost
   and supported deployment before choosing it. Do not imply that open source
   removes the need to trust the host, server or administrators.
2. Select and implement one backend. Keep provider-specific authentication and
   retrieval in the credential resolver. Pass only the resolved key to the
   trusted publisher, which continues to check the frozen public identity and
   tested source before signing and publication.
3. Add explicit profile/setup validation and readable provider-specific status.
   Preserve existing 1Password profiles and require deliberate configuration to
   select the new provider. Document provisioning and recovery as separate work
   from disposable tests.

## Security and acceptance criteria

| Area | Required result |
| --- | --- |
| Least privilege | Document the bootstrap credential's actual authority and restrict it to the smallest supported signing-secret scope. A locator must not be presented as reducing credential permissions. Avoid access to unrelated secrets; reject a route that cannot meet the agreed authority boundary. |
| Unattended bootstrap | Work with the desktop locked and without interactive approval after provisioning. State exactly what persists, where, in what form and who can read it. If a plaintext bootstrap remains necessary, disclose that limitation; do not claim encryption or host-compromise protection. |
| Resolver isolation | Use a supported client in a pinned trusted container with a read-only root filesystem, non-root user, dropped capabilities, process/resource limits, independent deadline, disabled Docker logs and checked cleanup. Mount no project, host home, provider cache, GitHub cache or Docker socket. Surviving credential containers must block reuse until inspected. |
| Secret channels | Keep credentials and key material out of command arguments, Docker configuration/environment metadata, labels, journals, logs and source. Use a bounded private input/output channel; only the supported client child may receive authentication secrets in its process environment when its documented flow requires that. Keep temporary keys in private tmpfs and clear explicit buffers/files. |
| Repeated retrieval | Fetch afresh for each verification/publication. Require the resolved private key's normalized public key and SHA256 fingerprint to match the selected/frozen profile on every use. No remembered verification result may certify later access or identity. |
| Signing | Preserve real SSH signing and signature verification in the trusted publisher, with the exact tested tree retained. The new secret backend must not run project hooks/configuration or weaken signed-history reconciliation. |
| Failure | Stop on missing/unsupported credentials, access denial, timeout, identity mismatch or cleanup failure. Do not switch identities, retry to evade restrictions, use a host SSH agent/key, fall back to another provider or silently publish unsigned commits. |
| Rotation and revocation | Document token/key replacement, expiry and revocation separately. Test that revoked access fails on the next retrieval, that a changed signer requires deliberate profile configuration, and that a resumed run rejects a different frozen identity. Revocation cannot recall a key already copied. |
| User interface | Show the configured secret-provider name in setup/status without revealing locators, tokens or private keys in ordinary output. Keep offline configuration/bootstrap checks distinct from an explicitly connected verification. Private metadata inspection remains an explicit action; connected success describes only that invocation. |
| Compatibility | Existing omitted-provider profiles retain 1Password semantics. Unknown provider values fail before credential reads or requests. Keep the meaning of execution/auth `--provider codex|claude` separate from signing setup's secret-provider option. |
| Run identity | Bind the secret-provider selection into the frozen run plan and reject a provider change during resume. Dispatch only to the explicitly selected implementation; a matching public key does not authorize a different credential route. |

## Verification and completion

Add offline fake tests with parity against the current resolver: success,
wrong public identity, malformed/oversized secret output, access denial, timeout,
cleanup failure, repeated retrieval, provider selection and legacy-profile
compatibility. Assert that arguments, mounts, logs and retained state contain
neither authentication secrets nor private keys. Use disposable generated keys
for real local Docker signature tests with networking disabled.

A connected backend test needs separate explicit authorization and dedicated
disposable credentials. Do not connect a real vault/account merely because the
adapter or offline tests exist. That trial must check the granted scope,
locked-desktop operation, repeated fetches, rotation/revocation and cleanup
without exposing secret values. Record its limits alongside the results.

Completion requires one implemented backend, reviewed permission/bootstrap
boundaries, passing offline parity and real-signature tests, and a documented
provisioning guide. If connected validation has not been authorized or completed,
state that limitation. Product selection and implementation remain future work;
there is no claim that alternative providers are available today.
