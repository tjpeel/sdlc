# Test GitHub profiles and unattended signing

Use a private disposable repository for the first connected trial. This guide
uses `personal` and `work` as example profile names; replace paths, identities,
vault references and repository names locally. Never put account configuration,
tokens, private keys or transcripts in this public clone.

The shared runtime image supports several GitHub profiles. Each profile has its
own native `gh` login volume, lease and signing configuration. A run selects one
profile and retains its numeric account/repository IDs, canonical repository
name, Git identity, approved public key and image. Codex and Claude still have
one saved account per provider.

The publisher and credential resolver run in Docker without desktop approval
after provisioning. The controller remains a foreground host process: **keep
its terminal open**. Terminal-independent execution and restart recovery remain
separate work. No connected end-to-end ticket has been validated yet.

## 1 Reinstall and rebuild

From the SDLC clone, choose an existing directory on your PATH:

```sh
go run ./cmd/sdlc-install --bin-dir /PATH/TO/YOUR_BIN_DIRECTORY
sdlc --version
sdlc runtime build --source .
sdlc runtime status --offline
docker pull 1password/op:2.39.0@sha256:3cd5a1febc662c93d46b944b983b301e710da5c016ef63be9d436cf2b1ed30d5
```

The new publisher binary requires this rebuild. The resolver uses the pinned
official 1Password image with `--pull never`; pulling it does not read a vault.
Builds use a bounded allowlist of runtime and Go source files, excluding host
credentials, Git metadata and local profiles.

## 2 Log into each GitHub profile

Establish the relevant organisation SSO session in your browser, then run:

```sh
sdlc auth login --service github --profile personal
sdlc auth status --service github --profile personal
sdlc auth status --service github --profile personal --verify
```

Complete the official `gh` device/browser flow using the intended account.
Offline status reports stored configuration only. `--verify` contacts GitHub
through native `gh` and prints the verified account login and numeric ID. Confirm
these before continuing. Repository permissions are checked when a run starts.

Repeat for the other account:

```sh
sdlc auth login --service github --profile work
sdlc auth status --service github --profile work --verify
sdlc auth status --service github --profile personal --verify
```

Expected: each profile reports its own account and logging into `work` leaves
`personal` intact. Use lowercase profile names up to 48 characters containing
letters, digits, `_` or `-`. Omitting the flag selects `default`; existing default
storage is preserved. A populated profile refuses another login; log out first
to reauthorise it, or select a new profile for another account. No host `gh` cache or SSH agent is copied or forwarded.

Your existing organisation approval of GitHub CLI may allow this flow. A new
session still needs the organisation's SSO and OAuth policy to permit access;
SDLC cannot bypass those controls. An SDLC GitHub App installation is not needed
for this route. See [native login](https://cli.github.com/manual/gh_auth_login)
and [SSO app authorisation](https://docs.github.com/en/enterprise-cloud@latest/authentication/authenticating-with-single-sign-on/authorizing-an-app-for-single-sign-on).

## 3 Check login persistence

After the auth commands finish, restart your local Docker engine normally. Then:

```sh
sdlc auth status --service github --profile personal --verify
sdlc auth status --service github --profile work --verify
```

Expected: the same accounts, without another login. Reinstalling SDLC and
rebuilding the image also preserve the volumes. Do not delete Docker volumes or
installation metadata as part of this check.

GitHub credentials are **plaintext in Docker storage**. Native `gh` cannot use
your host credential store in these containers, so login explicitly uses its
documented plaintext-storage option. Read-only mounts prevent publisher cache
changes; they do not prevent theft by a compromised publisher or Docker
administrator. A normal job uses the saved login; expiry, revocation or SSO
denial stops it for attention. Interactive reauthorisation is not an unattended
token-renewal mechanism.

## 4 Provision the signing authority

Follow [1Password signing setup](1password-signing-setup.md) to create the dedicated
custom vault, Read Items Service Account and Ed25519 SSH Key item. Its grant is
vault-wide; a reference cannot narrow it to one item. The historical
[1Password test](1password-test.md) records earlier trials, not final provisioning.
Register the public key as a GitHub **signing key** in the intended account.

Then run the interactive wizard once per profile:

```sh
sdlc signing setup --profile personal --provider 1password
sdlc signing setup --profile work --provider 1password
```

It asks for vault/item names or IDs, the public key and optional expected SHA256
fingerprint, then reads the token at a hidden prompt. It saves private installation
configuration and a mode-`0600` bootstrap file outside Git. Use `--bootstrap-file`
to select an existing safe external token file instead. Setup makes no network
request and refuses to overwrite existing files; it does not create the
1Password resources or attest their grant scope.

The bootstrap is persistent plaintext host storage. Keep it in a private local
directory without extra ACL grants, outside shared/synchronised storage and all
repositories. A compromised host user or administrator can read it. Do not put
its contents in arguments, transcripts or chat. The token reaches only the
short-lived official `op` resolver, never providers, test workers or the publisher.

## 5 Inspect and test each signing profile

```sh
sdlc runtime status --offline --github-profile personal
sdlc signing status --profile personal
sdlc signing status --profile personal --verify
sdlc signing status --profile work
sdlc signing status --profile work --verify
```

Runtime status includes an informative offline signing summary for `personal`;
missing signing setup does not fail an otherwise successful runtime check. Plain
`signing status` checks saved settings, public identity and bootstrap safety offline
and returns its own readiness result.
`--verify` retrieves the configured key through official `op`, then checks the
expected public key/fingerprint and signs and verifies a disposable commit in a
separate network-disabled container. It performs no GitHub or model request.
Success applies to the current check; it is not a cached claim of continued access.
`signing verify --profile NAME` remains an alias.

Normal status prints no vault/item references, private paths or secrets. Use
`signing status --profile NAME --show-config` only for private local inspection
of references and storage locations; it never prints token or private-key contents.
For deliberate changes to an existing profile, `signing configure --profile NAME
--file /PATH/TO/YOUR_PRIVATE_SIGNING_PROFILE.json` remains available.

Lock or close the 1Password desktop app and repeat `signing status --verify`.
Restart Docker after the checks finish and repeat. Expected: no desktop prompt.
Stop on a mismatch, missing image, vault denial or timeout; inspect configuration
privately rather than pasting secret output into chat.

Each retrieval uses a fresh resolver. The token and reference travel through
stdin; only the native CLI child receives the token in its process environment.
The resolver has an independent deadline, auto-removal and checked cleanup; a
surviving resolver blocks retry. The publisher receives the key through stdin
and tmpfs and removes its key file before push. These measures do not guarantee
erasure from every buffer or host swap. See [tmpfs limitations](https://docs.docker.com/engine/storage/tmpfs/).

## 6 Prepare the disposable repository and providers

Keep initial clone/fetch on the host using your existing approved SSH/SSO route.
SDLC captures a local checkout; authenticated cloning a URL inside Docker is
not implemented. Set the effective identity in that repository:

```sh
git config --local user.name 'YOUR_GIT_NAME'
git config --local user.email 'YOUR_VERIFIED_EMAIL'
sdlc init
sdlc auth status --all
```

Authenticate missing providers through `sdlc auth login` and
`sdlc auth login --provider claude`. Provider accounts remain separate from
GitHub profiles. See [provider rules](provider-usage.md) for supported local jobs.

Follow [connected onboarding](onboarding.md) to export the .NET fixture, commit
and push its baseline, configure CI and prepare a real bounded ticket. Review
`.sdlc/project.json`; disposable Compose inputs belong in `input_files`, not
provider-visible requirements. Baseline/local base must match GitHub.

## 7 Select the profile and run one ticket

From that repository:

```sh
sdlc run --reference YOUR_WORK_REFERENCE --ticket 01-count-items.md \
  --github-profile personal --docker-tests --dry-run
sdlc run --reference YOUR_WORK_REFERENCE --ticket 01-count-items.md \
  --github-profile personal --docker-tests
```

Use `--github-profile work` for the other account. The matching signing
configuration is required. `--dry-run` is offline selection only; the real run
checks the saved account and repository push access before provider execution.
For an SSH alias that cannot be inferred from `origin`, supply `--repo OWNER/REPO`.

Watch `sdlc dashboard` in another terminal. Expected: streamed implementation,
passing isolated tests, a signed draft PR, current-head CI, opposite-provider
review, and `ready` after a complete clean review. Inspect the PR's author,
Verified badge, base and exact commit. A clean first review does not prove the
repair loop; a separate actionable-feedback trial is still needed.

Retain the run ID privately. Resume with the printed command; do not supply
another `--github-profile` on resume. Changing the recorded account, signer,
repository, base, tested HEAD/tree or image stops publication. The controller
never changes account or falls back to host credentials to recover.

## 8 Test logout and revocation separately

After all jobs finish:

```sh
sdlc auth logout --service github --profile work
sdlc auth status --service github --profile work
sdlc auth status --service github --profile personal --verify
```

Expected: `work` reports missing and returns nonzero, while `personal` still
works. Log `work` in again to restore it. Local logout does **not** revoke the
GitHub token; see [native logout](https://cli.github.com/manual/gh_auth_logout).

For a dedicated disposable account/credential, test remote revocation and confirm
`--verify` and publication fail without an interactive fallback. Revoking GitHub
CLI authorisation may invalidate other sessions of that app on the same account,
including host `gh`; plan this test deliberately. Revoke a disposable 1Password
Service Account and confirm `signing verify` fails within its deadline. Revoking
vault access stops future reads; it cannot recall an already copied signing key.
After compromise, remove the GitHub signing key and revoke publication credentials
as well. See [Service Account revocation](https://www.1password.dev/service-accounts/manage-service-accounts).

## Remaining gates

- Complete live GitHub/SSO, signing attribution and Codex/Claude delivery trials.
- Add a detached Docker controller, attachment and crash/restart reconciliation.
- Replace the explicit plaintext bootstrap file with a supported unattended
  host credential store or managed workload identity.
- Add destination-restricted networking and broader cleanup/recovery controls.
- Extend account profiles to Codex/Claude only when required; keep provider terms
  and account concurrency restrictions enforced.

Native `gh` login grants the access allowed to that OAuth session. Per-repository
checks bind SDLC's intended operations; they do not narrow a stolen token's
permissions. No claim of repository-scoped OAuth credentials, automatic token
minting or host-compromise protection is made. The [credential boundary](github-credentials.md)
describes what each compromised component could reach.
