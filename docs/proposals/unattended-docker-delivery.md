# Unattended Docker delivery

Status: proposed on 4 October 2026. This records a required capability, not a
completed implementation or a connected validation result.

After one-time provisioning, a local single-user ticket job must implement,
check, sign, publish and review within Docker without desktop 1Password approval.
Closing the launching terminal must not stop the job. Human questions, revoked
credentials, provider refusals and exhausted limits must still stop work and
appear in the dashboard. Unattended operation does not override those gates.

## Current gap

Provider sessions, source inspection and checks already use Docker. GitHub
publication and signing still run in the foreground host controller. Signing is
optional, identity is read again on publication, and there is no detached
controller supervisor. A host `op run` wrapper or remembered desktop signing
approval does not meet the requirement.

## Required boundaries

| Component | Authority and inputs |
| --- | --- |
| Host CLI | Select a ticket, start or attach to a local job, stream private output and show attention state. Provisioning is separate from each run. |
| Trusted Docker controller | Own the frozen plan, private journal and run lock; launch workers and the publisher; reconcile interrupted operations. Engine access makes it a trusted component. It must never run repository commands itself. |
| Provider worker | Selected source, ticket and its own native provider cache. No publication token, signing key, secret-store bootstrap token or host Docker socket. |
| Check worker and isolated test daemon | Committed candidate, selected check-only inputs and checks. No provider cache or publication credentials. Each test daemon remains separate. |
| Trusted Docker publisher | Validated bundle, fixed publication manifest, narrow publication state and machine credentials. No test scripts, provider cache, worker Git configuration or host Docker socket. |

The publisher must accept requests only from the trusted controller. It is not a
general command, signing or arbitrary-repository endpoint for workers. Import Git
objects into controlled metadata without checking out or executing project code;
disable hooks, filesystem monitors and project-supplied credential/signer programs.
Preserve the existing equality check between the tested tree and the published
tree. Restrict publication to the approved repository, branch and base.

Do not mount the entire private run directory into the publisher: it also contains
provider sessions and diagnostic output. Give each component only its required
state. Network access should be limited where practical to the selected vault
service and GitHub for the publisher; enforce this in the runtime rather than
relying on prompts.

## Machine credentials and signing

The first secret-store integration should use the official 1Password CLI with a
Service Account, scoped to read-only access in a dedicated automation vault.
This route supports unattended secret retrieval without the desktop app. It does
not expose a service-account SSH signing API. See
[CLI machine authentication](https://www.1password.dev/service-accounts/use-with-1password-cli).

Store a repository-scoped GitHub credential and a dedicated SDLC SSH signing key
in that vault. A small initial deployment can use a fine-grained PAT with only
the required repository permissions; organisation policy and the actual `gh`
operations need validation. GitHub App installation tokens are the longer-term
option: restrict installation and token permissions, then refresh their one-hour
tokens through the documented flow. Neither route grants commit signing merely
by authenticating a push. See the
[credential guide](../github-credentials.md) and
[installation token flow](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-an-installation-access-token-for-a-github-app).

The trusted resolver reads the bootstrap token from a runtime secret and supplies
`OP_SERVICE_ACCOUNT_TOKEN` only to the official CLI child process. Resolve the
GitHub token and signing key inside the publisher boundary, never through Docker
arguments, image build variables, logs or journals. Clear conflicting
authentication configuration. Do not fall back to desktop login or another
identity when resolution fails.

The bootstrap token needs a durable protected store that can supply it after a
restart without a desktop prompt. A token saved only in the vault it opens is
insufficient. Selecting and testing that store is part of implementation and
one-time onboarding, not an assumption that Docker supplies encrypted storage.
Keep it outside source, provider caches and build contexts. Ordinary Compose
file secrets are file mounts; protection at rest depends on the backing store.
See [Compose secrets](https://docs.docker.com/compose/how-tos/use-secrets/).

Retrieve only a dedicated machine signing key into a private temporary file using
the official CLI's output-file support. Require mode `0600`, restricted tmpfs,
noninteractive key loading and removal at completion. If the key is encrypted,
its unlock secret must also use the machine route; a passphrase prompt would
reintroduce the same failure. A dedicated automated signer is an alternative,
but forwarding the personal desktop agent is not the default. See
[SSH-key retrieval](https://www.1password.dev/cli/reference/commands/read).

Freeze the approved effective project Git name/email, public-key fingerprint,
required signing policy, repository, account or App installation, credential
profile and immutable publisher image identity before provider work. Journal
only those nonsecret values in private state. Do not copy host Git configuration
or signer paths into Linux unchanged. Configure the controlled publisher with
its own signer and public trust file. Sign every published commit and verify the
exact approved key before push; never silently publish unsigned commits.

Register the dedicated public key with GitHub as a signing key for the intended
identity and validate Verified attribution in the disposable trial. A signature
from this automated key means the machine had authority to sign; it does not
mean the owner approved each change at a screen.

## Supervision and recovery

Package the Linux controller and publisher binaries and required credential
client in recorded runtime images. The host CLI becomes a launcher and viewer;
an owned controller container continues after terminal closure. Reuse the
existing private journal, run lock and activity registry, with explicit launch,
attach, stop and restart ownership. Do not automatically restart an attention
state into further provider work.

Define paths and volumes from the Docker daemon's perspective. A controller's
internal path is not automatically a valid host bind source for sibling workers.
Persist run checkpoints separately from temporary secrets. Reconcile containers,
leases and publication state after interruption; retain same-provider cache
serialization and per-check test daemons.

Define how the host dashboard and container controller locate the same journal
and activity registry. Current state stores absolute workspace/project/run paths
and validates them during loading and resume. Use explicit host/container path
translation or a consistent mapping; container-only paths must not make host
attachment or resumption fail.

Before retrying a push or PR request whose response was lost, inspect the exact
remote branch/head and existing PR. Reject unexpected movement and avoid duplicate
publication. Refresh credentials only through their supported machine flow.
Expiry, revocation, vault failure or provider denial must produce a clear attention
state without hanging for input or switching accounts.

The controller's engine authority can expose other containers and mounted
secrets. It belongs to the trusted control plane, never a model or test worker.
A read-only Docker socket mount does not restrict the Docker API's power. Host or
Docker-administrator compromise remains outside this isolation boundary.

## Implementation order

1. Add the separate Docker publisher and frozen publication preflight. Keep the
   existing `Publisher` interface, but exchange a fixed request, bundle and
   publication-only state rather than host filesystem paths. Require signing and
   verify identity/tree equality. Test with disposable keys and fake GitHub replies.
2. Add private machine-credential profiles and official 1Password Service Account
   resolution, including bootstrap delivery, expiry, redaction and cleanup.
   Inventory the credential client. Test resolution offline with fake responses;
   real vault setup and minimal-scope GitHub access are one-time joint tasks.
3. Add the Docker controller launcher, supervision and restart reconciliation.
   Keep dashboard attachment independent of job lifetime, and prove that no
   recurring desktop approval is needed across publication, repair and restart.

These are required before treating the solution as unattended Docker delivery.
The existing onboarding trial can still diagnose provider, test and host
publication behavior while these iterations are built.

## Acceptance

Offline tests must use disposable credentials and keys, fake GitHub responses
and network-disabled fixtures:

- Sign and verify the expected identity; missing credentials, wrong signer,
  changed account/image/base or changed tested tree stop before push.
- Prove secrets are absent from model/check mounts, container configuration,
  process arguments, journals and logs; assert narrow publisher input/state mounts.
- Handle lost push/PR responses and competing controllers without duplicate work.
- Continue after the launcher disconnects; stop and restart with correct owned
  resource cleanup and persisted attention states.
- Load the same journal and registry from the host dashboard after a controller
  restart, attach to its output and resume without path or run-identity drift.

After one-time provisioning, the joint disposable-repository trial must:

- Run with the 1Password desktop app locked or closed and no forwarded desktop
  agent. Close the launching terminal and observe continued dashboard activity.
- Complete implementation, signed draft PR, current-head CI, opposite-provider
  review and a real repair cycle without secret approval prompts. A clean review
  does not prove the repair path.
- Restart at a safe checkpoint and prove machine credential retrieval and
  signature verification still work, without changing provider identities.
- Revoke or expire a disposable credential and observe a bounded attention state,
  without interactive fallback. Confirm no secrets remain in retained output.

## Remaining risk

An unattended publisher must possess usable signing and publication authority.
Compromise of it, its resolver, the controller or Docker host can misuse that
authority. Restrict vault contents, repository permissions, signing keys and
network access; use bounded credential lifetimes and rotate after compromise.
Isolation reduces the reach of compromised test dependencies, not host trust.

Tmpfs can spill into host swap, and deleting a temporary file is not guaranteed
secure erasure. Account for host disk/swap protection and avoid durable key
copies. See [Docker tmpfs limitations](https://docs.docker.com/engine/storage/tmpfs/).
Provider workers still have their own native account caches; machine GitHub
credentials do not change the [provider usage rules](../provider-usage.md).
