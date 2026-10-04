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

Use a dedicated automation vault, a Service Account with read access only to
that vault, and a dedicated unencrypted Ed25519 signing key. The account grant
is vault-wide, not restricted to a single item: put no unrelated secrets there.
Register the public key as a **signing key** in the intended GitHub account.
Separate keys and references per GitHub profile make revocation clearer.
The [dedicated-vault test](1password-test.md) covers provisioning and outside-vault
denial. Prefer vault/item IDs in references to reduce name-based lookup requests.

For this iteration, SDLC reads the Service Account token from an explicit
private host file. This supports a locked desktop but is **persistent plaintext**,
not an OS credential-store integration. A process running as your host user,
host administrator or Docker administrator can compromise this arrangement.
Use host disk encryption, ordinary local filesystems and private directories
without extra ACL grants. Do not use a shared/synchronised directory or broad
vault grants. The token is sent only to the short-lived official `op` resolver,
never to the provider, test worker or GitHub publisher.

Run this locally once per profile. It uses a hidden prompt for the token and
refuses to overwrite existing files; the token never appears in shell history:

```sh
python3 - <<'PY'
import getpass, json, os, pathlib, re, sys
sys.stdin = open('/dev/tty', 'r')
name = input('Profile name (personal or work): ').strip()
if not re.fullmatch(r'[a-z0-9][a-z0-9_-]{0,47}', name):
    raise SystemExit('Invalid profile name')
directory = pathlib.Path.home() / '.config' / 'sdlc-private'
directory.mkdir(mode=0o700, parents=True, exist_ok=True)
if directory.is_symlink():
    raise SystemExit('Use a directory without symlinks')
directory = directory.resolve()
if any((parent / '.git').exists() for parent in [directory, *directory.parents]):
    raise SystemExit('Use storage outside all repositories')
os.chmod(directory, 0o700)
bootstrap = directory / (name + '-bootstrap')
profile = directory / (name + '-profile.json')
if bootstrap.exists() or profile.exists():
    raise SystemExit('Files already exist; inspect privately before changing them')
reference = input('OpenSSH private-key reference, without outer quotes: ').strip()
public = input('Public Ed25519 key: ').split()
fingerprint = input('Expected SHA256 fingerprint: ').strip()
if len(public) < 2:
    raise SystemExit('Public key is incomplete')
token = getpass.getpass('Service Account token (hidden): ').strip()
if not token or any(c in token for c in '\r\n\x00'):
    raise SystemExit('Token must be one line')
old_mask = os.umask(0o077)
try:
    with bootstrap.open('x') as file:
        file.write(token + '\n')
    del token
    with profile.open('x') as file:
        json.dump({'version': 1, 'id': name + '-signing',
                   'reference': reference, 'public_key': ' '.join(public[:2]),
                   'fingerprint': fingerprint, 'bootstrap_file': str(bootstrap)},
                  file, indent=2)
        file.write('\n')
finally:
    os.umask(old_mask)
print('Private files prepared; no credential contents printed.')
PY
```

An example reference is
`op://YOUR_VAULT/YOUR_SIGNING_KEY/private key?ssh-format=openssh`.
The profile contains references and public metadata only. Its bootstrap file
contains the actual bearer token. Both files must be owned by your user with
mode `0600`, without symlinks or hard links, outside all Git repositories.
SDLC rejects unsafe files. Keep the terminal and clipboard private during setup.

## 5 Configure and test each signing profile

```sh
sdlc signing configure --profile personal --file "$HOME/.config/sdlc-private/personal-profile.json"
sdlc signing verify --profile personal
sdlc signing configure --profile work --file "$HOME/.config/sdlc-private/work-profile.json"
sdlc signing verify --profile work
```

`configure` saves only public metadata and references in private installation
state. `verify` retrieves the configured key through official `op`, then signs
and verifies a disposable local commit in a separate container with networking
disabled. It checks the expected public key and fingerprint; it performs no
GitHub or model request and prints no key material.

Lock or close the 1Password desktop app and repeat `signing verify`. Restart
Docker after all checks finish and repeat. Expected: no desktop prompt. Stop on
any mismatch, missing image, vault denial or timeout; inspect configuration
privately rather than pasting secret output into chat.

Each retrieval uses a fresh resolver. The token and private reference travel
through stdin, not Docker configuration or environment arguments. The official
CLI child receives the token in its environment inside that container. It has
a 35-second independent deadline, removal on exit and cleanup checks. A surviving
resolver prevents a retry. The retrieved key enters the publisher through stdin
and tmpfs; the key file is removed after signing and verification, before push.
Tmpfs and process memory are not guaranteed secure erasure and may spill into
host swap. See [tmpfs limitations](https://docs.docker.com/engine/storage/tmpfs/).

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
