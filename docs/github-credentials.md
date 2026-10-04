# GitHub credentials and commit signing

The current route uses the unmodified official GitHub CLI's browser login in
Docker and a dedicated signing key retrieved through the official 1Password
CLI. Each named GitHub profile has separate login storage and signing settings,
while all profiles share the runtime image. No GitHub App installation, host
OAuth-token export, subscription-token replay or desktop SSH-agent forwarding
is involved.

Follow the [step-by-step profile and signing test](github-docker-test.md), then
[connected onboarding](onboarding.md). This is an implemented boundary with
offline validation, including actual Docker signature verification. Live
GitHub/SSO, Verified attribution and complete provider delivery remain to be
validated. Official references were checked on 4 October 2026.

## Current credential flow

| Operation | Current behaviour |
| --- | --- |
| Initial clone/fetch | Prepare the checkout on the host using existing approved access. SDLC captures a local checkout; authenticated URL cloning is not implemented. |
| Account selection | `auth ... --service github --profile NAME` provisions one separate native login; `run --github-profile NAME` selects it and its matching signing configuration. Omission selects `default`. |
| Worker source | A captured local bundle creates a disposable checkout with no origin. Worker commits have a generic unsigned identity. |
| Publication identity | The controller freezes effective project `user.name`/`user.email`, numeric GitHub account/repository IDs, canonical repository name, credential volume, signing profile/public key/fingerprint and runtime image before provider work. |
| Signing | A separate official `op` container retrieves one dedicated Ed25519 key. The Docker publisher recreates linear commits, requires SSH signatures, verifies the approved key and preserves the exact tested tree. |
| Push/PR/CI | Only the trusted publisher gets the selected native `gh` volume, read-only. HTTPS Git uses the native credential helper. Account/repository/base checks and exact branch leases reject unexpected movement. |
| Provider/check credentials | Providers get their own native AI cache. Checks get no provider cache. Neither gets GitHub credentials, vault bootstrap, signing key, host SSH agent or host Docker socket. |

The host's GitHub login, `GH_TOKEN`, `GITHUB_TOKEN` and `SSH_AUTH_SOCK` do not
select the Docker publisher's account. It never switches accounts or falls back
to those credentials after denial. Resume keeps the recorded profile and key;
changing flags cannot move an existing run to another account.

## Native GitHub login and organisation access

`sdlc auth login --service github --profile work` runs native `gh auth login`
with HTTPS, browser/device flow and explicit `--insecure-storage`. The
noninteractive publisher uses the saved login. Offline status checks stored
configuration only; `--verify` uses official `gh` and displays the effective
account identity. No raw token or native credential-file contents are returned
to the host by status. See [login](https://cli.github.com/manual/gh_auth_login)
and [auth status](https://cli.github.com/manual/gh_auth_status).

Separate profiles are separate Docker volumes, not several accounts switched
inside one native cache. Each profile's operations are serialized through
cleanup. Native Codex/Claude caches still support one account each; their
[provider rules](provider-usage.md) and account limits apply independently.

An existing GitHub CLI authorisation can be compatible with an organisation
that prohibits installing new Apps. Establish organisation SSO when authorising
that account, and respect any OAuth-app approval policy. Successful host SSH
access does not prove the new OAuth session can read or push those repositories.
SDLC checks selected-repository push access at launch and actual PR/CI access
when used. See [SSO app authorisation](https://docs.github.com/en/enterprise-cloud@latest/authentication/authenticating-with-single-sign-on/authorizing-an-app-for-single-sign-on).

This OAuth grant is not automatically scoped to one repository or short-lived.
Its lifetime and access depend on GitHub and organisation policy. Per-run
repository binding limits the controller's intended requests, not a stolen
credential's authority. Expiry, revocation or SSO denial stops the run for
attention; supported interactive reauthorisation may then be needed. SDLC does
not promise automatic refresh or copy/replay authentication tokens. See
[token expiration and revocation](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/token-expiration-and-revocation).

Local `auth logout` removes only that profile's saved login. Remote revocation is
separate and may affect other GitHub CLI sessions on the same account. See
[native logout](https://cli.github.com/manual/gh_auth_logout).

## Machine credentials for unattended Docker delivery

Use a read-only 1Password Service Account for a dedicated automation vault.
The vault grant covers its contents, so keep it small and exclude unrelated
credentials. Configure an external private profile containing one OpenSSH key
reference, Ed25519 public key/fingerprint and bootstrap-file path. Configure each
GitHub profile separately with `sdlc signing configure --profile NAME --file FILE`.
`signing verify --profile NAME` proves retrieval, key identity and local signed
commit verification without a GitHub or model request.

The bootstrap is currently a plaintext bearer-token file outside all repositories,
owned by the user with mode `0600`, without symlinks or hard links. The saved
profile contains no token or private key. SDLC passes bootstrap and reference
through stdin to a disposable official `op` container, not Docker metadata or
host environment variables. Only the native CLI child receives the token in its
process environment. Retrieval has an independent container deadline, managed
labels, auto-removal and checked cleanup; a surviving resolver blocks retry.
The native CLI's config and temporary key stay in tmpfs.

The publisher receives the key through stdin, verifies the public identity and
uses a private tmpfs file for signing. It clears explicit buffers and removes
the file before remote mutation. These measures reduce lifetime; they do not
provide guaranteed erasure of every process/Docker buffer or protect against
host swap, debugging or administrator access. See [Service Account use](https://www.1password.dev/service-accounts/use-with-1password-cli),
[OpenSSH key retrieval](https://www.1password.dev/cli/reference/commands/read)
and [tmpfs](https://docs.docker.com/engine/storage/tmpfs/).

No desktop unlock is required after provisioning. The trust root is the host
file and Docker engine, not a hidden or encrypted bootstrap. A supported
unattended OS store/workload identity and detached controller remain future work.
GitHub App installation tokens are an optional alternative where an owning
account permits installation; they are not required for the current OAuth route.

## Compromise and response

| Compromised component | Reach and response |
| --- | --- |
| Check dependency/test | Selected source and check-only inputs, network access and its isolated test daemon. Keep production credentials out of test inputs; privileged Docker-in-Docker is not hostile-code isolation. |
| Provider process or prompt | Source, supplied requirements and its AI login cache. It can exfiltrate these through unrestricted network access; GitHub/vault/signing credentials are absent from its mounts. |
| GitHub publisher/runtime | Saved OAuth credential and the current signing key during signing. It could misuse that account's full grant; revoke GitHub authorisation and remove the automation signing key after compromise. |
| Credential resolver | Vault-wide read authority while its bootstrap is in memory. Revoke the Service Account; assume any readable vault items may have been copied and rotate them. |
| Same host user, Docker administrator or controller | Bootstrap file, caches, keys in use and engine authority. Rotate/revoke all affected credentials and repair/rebuild the trusted host before reuse. |

The publisher never checks out or runs repository code. It imports objects using
fixed trusted Git/OpenSSH programs, disables hooks/project configuration and
uses a build context excluding credential files. Malformed Git objects and
runtime/client vulnerabilities remain risks. A signature means the automation
possessed the approved key; it does not mean a human approved that change.

Private publisher state has a writable child for the container UID inside an
unmounted, owned `0700` run parent. This depends on local filesystem permission
enforcement without extra ACL grants. The GitHub cache remains read-only and
separate. Docker administrators and sufficiently privileged host processes can
read these volumes; SDLC does not encrypt them. Network destination restrictions
and managed encrypted bootstrap storage remain unimplemented.

Revoke the Service Account to stop future reads. That does not invalidate a
copied signing key or GitHub token; remove/revoke those separately. Use dedicated
keys per profile to make recovery bounded. See [Service Account management](https://www.1password.dev/service-accounts/manage-service-accounts).
The archived Python/Compose prototype does not define the current credential
flow; use the commands above.

Register the dedicated public key as a GitHub signing key and confirm a
published disposable commit's Verified badge. Local verification alone does not
prove GitHub attribution. GitHub keeps prior verification records after a key is
removed; revocation prevents future use, not historical verification. See
[GitHub SSH verification](https://docs.github.com/en/authentication/managing-commit-signature-verification/about-commit-signature-verification#ssh-commit-verification).
