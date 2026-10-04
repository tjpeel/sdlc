# Unattended Docker delivery

Status: partially implemented on 4 October 2026. Native GitHub profiles, the
trusted Docker publisher and mandatory 1Password-backed signing are implemented.
The host controller remains a foreground process. Detached supervision and
restart reconciliation are proposed, and connected end-to-end delivery remains
unvalidated.

The intended result is a local single-user ticket job that implements, checks,
signs, publishes and reviews after one-time provisioning without desktop
1Password approval. It must eventually survive closure of the launching terminal.
Human questions, revoked credentials, provider refusals and exhausted limits
must still stop work and appear in the dashboard.

## Implemented boundaries

| Component | Authority and inputs |
| --- | --- |
| Host controller | Owns the frozen plan, private journal, run lock and Docker operations; remains attached to the launching terminal. |
| Provider worker | Selected source, ticket and its own native provider cache; no GitHub cache, signing key, vault bootstrap or host Docker socket. |
| Check worker and isolated test daemon | Committed candidate, exact check inputs and checks; no provider or publication credentials. Each test daemon remains separate. |
| Trusted Docker publisher | Candidate bundle, fixed publication request, narrow state, selected read-only GitHub cache and temporary signing key; no project checkout, provider cache or Docker socket. |
| Official 1Password CLI container | The approved signing-key reference and bootstrap supplied over stdin; independent 35-second timeout, managed labels and automatic removal. |

The publisher binary is built from a bounded public source closure and baked into
the recorded runtime image. It imports Git objects into controlled metadata
without running project code, hooks, filters or repository-supplied credential
and signer programs. It requires check evidence for the exact candidate head and
tree, signs every delivered commit, verifies the approved public key and confirms
the signed tree matches the checked tree.

Before provider work, the controller freezes effective project Git name/email,
GitHub profile and numeric account ID, repository numeric ID and canonical name,
signing public key/fingerprint and runtime image. Publication rechecks the
selected account/repository, push permission, base and expected remote branch
head. An exact push lease rejects competing changes. CI and review remain tied
to the recorded published revision. Identity changes stop delivery; there is no
host credential fallback or automatic account switch.

## Profiles and signing

GitHub authentication uses the unmodified official `gh` browser/device login
flow with HTTPS. Each named profile has an independent native configuration
volume and lease while sharing the runtime image:

```sh
sdlc auth login --service github --profile personal
sdlc auth status --service github --profile personal
sdlc auth status --service github --profile personal --verify
sdlc signing configure --profile personal --file /PATH/TO/YOUR_PRIVATE_SIGNING_PROFILE.json
sdlc signing verify --profile personal
sdlc run --reference YOUR_WORK_REFERENCE --ticket 01-ticket.md --github-profile personal
```

Omitting the name selects `default`. A separate `work` profile can coexist with
`personal`; each uses its matching named signing configuration. Native GitHub
credentials persist as plaintext in private managed Docker volumes. Offline
status checks storage presence; explicit `--verify` makes a native connected
status request and prints the selected login and numeric account ID, without a
token. Logout removes the local native login, without revoking remote
authority. Codex and Claude still each use one account cache per installation.

This route does not require a GitHub App installation. An App is an optional
alternative for an environment with the required administrative access; App
installation-token resolution is not implemented by this native login route.
See the [credential guide](../github-credentials.md).

Signing requires a dedicated Ed25519 key retrieved by the official 1Password CLI
with a Service Account and read-only access to its dedicated automation vault.
The configured profile stores public metadata and references. The private key
stays in the vault until resolution; it is passed over stdin into publisher
private tmpfs, verified against the frozen public key and removed before push.
The runtime uses its own signer and trust file. No desktop approval or forwarded
personal SSH agent is part of this route. See
[CLI machine authentication](https://www.1password.dev/service-accounts/use-with-1password-cli).

`sdlc signing verify --profile NAME` resolves the approved key through the
official CLI and verifies a disposable signed Git commit in a separate
network-disabled container. It makes no GitHub or model request and does not
prove GitHub Verified attribution.

The resolver's bootstrap is an explicit mode-`0600` plaintext bearer-token file
outside repositories. It must remain outside provider caches, Docker build
contexts and committed source. OS credential-store integration is not
implemented. SDLC does not encrypt this file or Docker volumes; host disk/swap
protection and Docker-administrator trust remain necessary. Register the public
key with GitHub for the intended signing identity and verify attribution in the
disposable connected trial.

## Validation so far

Offline tests use disposable keys and fake GitHub responses. They exercise the
native cache and logout with networking disabled, signing and signature checks,
account/repository identity checks, tested revision gates, exact push leases,
restricted mounts and cleanup. They do not establish live vault access, GitHub
Verified attribution, provider entitlement or a completed repair cycle.
Use the [Docker GitHub test guide](../github-docker-test.md) for the staged trial.

## Remaining supervision and recovery work

The controller must become a trusted owned Docker process that continues after
the host launcher disconnects. Add explicit launch, attach, stop and restart
ownership around the existing private journal, run lock and activity registry.
Do not restart an attention state into further provider work automatically.

Paths and volumes must be resolved from the Docker daemon's perspective. A
controller's internal path is not automatically a valid bind source for sibling
workers. Host dashboard attachment and resumed runs must use the same journal
and registry without path or run-identity drift. Persist checkpoints separately
from temporary secrets and reconcile surviving containers and leases after
interruption.

Before retrying a push or PR request with a lost response, reconcile the exact
remote head and existing PR. Revocation, vault failure or provider denial must
produce a bounded attention state without input prompts or identity fallback.
These recovery cases need further tests beyond successful foreground delivery.

## Acceptance for full unattended delivery

After one-time provisioning, a disposable connected trial must:

- Complete implementation, a signed draft PR, current-head CI, independent review
  and an actual repair without desktop secret approval prompts.
- Keep working after the launching terminal closes and remain visible in the
  host dashboard.
- Restart at a safe checkpoint with the same recorded identities, paths and
  publication state, without duplicate publication.
- Stop on revoked credentials, provider denials and exhausted limits without
  switching accounts, and retain no secrets in output or journals.

The current foreground controller does not satisfy the terminal-closure or
restart requirements. The [dedicated-vault test](../1password-test.md) and Docker
GitHub trial validate narrower provisioning and publication steps first.

## Remaining risk

The publisher and resolver possess usable signing and publication authority.
Compromise of either component, the controller or Docker host can misuse it.
Restrict vault contents, repository permissions and key scope. Network
destination restrictions and encryption at rest are not implemented.

Tmpfs can spill into host swap, and file removal is not guaranteed secure
erasure. See [Docker tmpfs limitations](https://docs.docker.com/engine/storage/tmpfs/).
Provider workers retain their own native account caches and remain subject to
the [provider usage rules](../provider-usage.md).
