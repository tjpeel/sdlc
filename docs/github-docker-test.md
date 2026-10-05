# Test GitHub profiles and unattended signing

For full onboarding, follow the [user guide](user-guide.md). Use this focused
validation guide with a private disposable repository. It
uses `personal` and `work` as example profile names; replace paths, identities,
vault references and repository names locally. Never put account configuration,
tokens, private keys or transcripts in this public clone.

The shared runtime image supports several GitHub profiles. Each profile has its
own native `gh` login volume and lease, plus an explicitly paired signing
profile whose name can differ. A run selects one
profile and retains its numeric account/repository IDs, canonical repository
name, Git identity, approved public key and image. Codex and Claude still have
one saved account per provider.

The publisher and credential resolver run in Docker without desktop approval
after provisioning. The controller remains a foreground host process: **keep
its terminal open**. Terminal-independent execution and restart recovery remain
separate work. Supervised private trials have passed end-to-end delivery with
both implementation providers. New account/key pairing and later dashboard
changes still need their own validation; this guide does not certify a new setup.

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
sdlc signing setup --profile personal-key --provider 1password
sdlc signing setup --profile work-key --provider 1password
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
sdlc signing status --profile personal-key
sdlc signing status --profile personal-key --verify
sdlc signing status --profile work-key
sdlc signing status --profile work-key --verify
sdlc github pair --profile personal --signing-profile personal-key
sdlc github pair --profile work --signing-profile work-key
sdlc github status --profile personal --verify
sdlc runtime status --offline --github-profile personal
```

Pairing checks the selected native account and its public signing-key
registration through official `gh`; it fetches no 1Password secret and makes no
GitHub write. Omit `--signing-profile` to use the account profile name. Use
`pair --replace` to change a pair deliberately. Pair each existing account once;
native login and signing configuration remain available. `github status
--profile NAME` inspects the global pair; its `--verify` rechecks the native
account and public-key registration, without proving secret-store access or
Verified attribution.

Runtime status follows the account pair to the signing profile; unpaired
accounts report attention. It includes an informative offline summary;
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
sdlc github use --profile personal
sdlc github status --verify
sdlc auth status --all
```

Authenticate missing providers through `sdlc auth login` and
`sdlc auth login --provider claude`. Provider accounts remain separate from
GitHub profiles. See [provider rules](provider-usage.md) for supported local jobs.

Follow [connected onboarding](onboarding.md) to export the .NET fixture, commit
and push its baseline, configure CI and prepare a real bounded ticket. Review
`.sdlc/project.json`; disposable Compose inputs belong in `input_files`, not
provider-visible requirements. Baseline/local base must match GitHub.

`init` takes no arguments. `github use` checks the pair offline and saves
selection outside the checkout in private mode-`0700`/`0600` state, without Git
configuration writes. Records hold public IDs/login/key/fingerprint, with no
vault reference, bootstrap path or token. Selection binds the canonical checkout
root and origin repository; each worktree has its own selection. Changed remotes
or pair metadata require `use` again.

## 7 Select the profile and run one ticket

From that repository:

```sh
sdlc run --reference YOUR_WORK_REFERENCE --ticket 01-count-items.md \
  --github-profile personal --docker-tests --dry-run
sdlc run --reference YOUR_WORK_REFERENCE --ticket 01-count-items.md \
  --github-profile personal --docker-tests
```

A fresh run can omit `--github-profile` to use saved repository selection or a
unique registered pair whose login equals the repository owner. An unbound
organisation repository or ambiguous owner pairs require `github use`. Use
`github use --profile work` to change this checkout deliberately; a conflicting
run flag fails. An explicit run profile selects a registered pair when the
checkout is unbound.

`--dry-run` is offline selection only. A missing pair prints the requested or
unresolved profile with a warning; malformed/changed metadata and ambiguous
selection fail. The real run checks native account, repository push access and
public signing-key registration before provider execution. For an SSH alias or
custom origin host, supply `--repo OWNER/REPO` to `run`, `github use` and
repository-mode `github status`. Only GitHub.com is supported. Omitted-profile
`github status` resolves the repository; `--profile NAME` inspects the global pair.

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

- Validate the new account/key pairing in connected work and organisation
  OAuth/SSO access; earlier supervised trials used a private personal repository.
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
