# GitHub credentials and commit signing

Unattended SDLC delivery must not depend on desktop 1Password unlock or approval
prompts. The planned route uses machine credentials and a dedicated signing key
inside a separate Docker publisher. It is not implemented yet. The current host
publisher is useful for a supervised diagnostic trial, but does not meet this
requirement. Initial GitHub cloning currently happens on the host; SDLC captures
that local checkout. Research below was checked against official documentation
on 4 October 2026; proposed changes are identified separately from current behavior.

## Current credential flow

| Operation | Current behavior |
| --- | --- |
| Initial clone or fetch | Prepare the checkout on the host. `sdlc run` does not clone a GitHub URL or refresh the original checkout. |
| Worker source | SDLC exports a local Git bundle, clones it into private run storage and removes `origin`. Worker commits use a generic unsigned SDLC identity. |
| Published name and email | The host publisher reads effective `user.name` and `user.email` in the original project context and recreates linear worker commits using those values. |
| Commit signing | The publisher reads host signing configuration. `commit.gpgsign=true` enables signing and local verification before push. Signing is currently optional. |
| GitHub push and PR/CI requests | Host Git uses HTTPS with `gh auth git-credential`; host `gh` creates/updates the draft PR and reads its checks. |
| Credential exposure | Provider workers get their native provider cache but no publication token, signing key or SSH agent. Separate check workers get none of those caches or publication credentials. |

The implementation is in [source capture](../internal/workrun/source.go) and
[host publication](../internal/workrun/publish.go). The host publisher strips
inherited `GIT_*` overrides but retains `GH_TOKEN`, `GITHUB_TOKEN`, `GH_CONFIG_DIR`
and `SSH_AUTH_SOCK`. It reads signing/identity settings again on publication and
resume; it does not freeze an expected GitHub account or signing identity at
launch. Keep them stable for the initial trial.

Use absolute host paths for signing executables, public-key files and the SSH
trust file, or an inline public signing key. The publisher copies selected raw
settings into a different Git repository, so a source-repository-relative path
can pass a baseline test and still fail during publication.

There are no current `sdlc` token, signing-key, Git-name/email, account-profile or
secret-store flags. The credential Compose and Python entrypoint in the runtime
tree belong to an [archived prototype](../research/README.md); do not use that
private-key/PAT injection route as the onboarding path for the Go CLI.

## GitHub authentication on the host

This section describes the current supervised trial, not the required unattended
Docker publication path.

Existing native `gh` login works with the current publisher. When login is
required, use `gh auth login --hostname github.com --git-protocol https` and
complete its supported flow. The CLI uses the system credential store when
available and falls back to plaintext storage if it cannot use that store. Check
the storage indication without printing the token. `GH_TOKEN` takes precedence
over `GITHUB_TOKEN` and saved login, so a vault-supplied token can select a
different effective account. See [GitHub CLI login](https://cli.github.com/manual/gh_auth_login)
and [environment precedence](https://cli.github.com/manual/gh_help_environment).

For a dedicated SDLC credential, prefer a fine-grained PAT restricted to the
chosen repository, with a short planned expiry. Start with:

| Repository permission | Needed operation |
| --- | --- |
| Contents read and write | Read base/source and push the ticket branch. Read-only suffices for a separate clone-only credential. |
| Pull requests read and write | Create/update the draft PR and inspect it. |
| Checks read | Read check runs. |
| Commit statuses read | Read legacy commit statuses. |

Metadata read is implicit. Actual organisation policy/approval and the endpoints
used by the installed `gh` client must be validated in the disposable trial.
Fine-grained PATs have contributor/resource-owner limitations. Do not grant
Workflows write unless the credential must create or modify Actions workflows.
See [PAT scope and limitations](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens),
[PR creation permissions](https://docs.github.com/en/rest/pulls/pulls#create-a-pull-request),
[check-read permissions](https://docs.github.com/en/rest/checks/runs#list-check-runs-for-a-git-reference)
and [status-read permissions](https://docs.github.com/en/rest/commits/statuses#get-the-combined-status-for-a-specific-reference).
GitHub's general PAT limitations page and endpoint-specific Checks documentation
differ; the read endpoint documents fine-grained PAT support. Verify the actual
read path rather than assuming the token supports writing Checks.

The installed `gh pr checks` uses GraphQL check/status rollup information, so
these REST permission references are a starting point, not proof of complete CLI
coverage. On an existing disposable PR, test the exact read shape:

```sh
gh pr checks PR_NUMBER --repo OWNER/REPO --json name,bucket,state,link
```

Use the selected host credential route. Pending or failed checks can produce a
nonzero exit without an authentication failure; distinguish those states from a
permission error. See the [official CLI implementation](https://github.com/cli/cli/blob/trunk/pkg/cmd/pr/checks/checks.go).

### External 1Password wrapper

This works through current host environment support; it is not native SDLC
secret-store integration. Desktop-authenticated `op run` can still prompt for
approval or unlock. Wrapping a command does not make its authentication
unattended. Install/configure the official 1Password CLI and create
a private file **outside all source repositories**, readable only by your user,
containing a secret reference rather than the token itself:

```dotenv
GH_TOKEN=op://YOUR_VAULT/YOUR_ITEM/YOUR_FIELD
```

Resolve it only for the required host command:

```sh
op run --env-file=/PATH/TO/PRIVATE_GITHUB_ENV -- \
  gh auth status --active --hostname github.com
op run --env-file=/PATH/TO/PRIVATE_GITHUB_ENV -- \
  gh api --hostname github.com user --jq .login
op run --env-file=/PATH/TO/PRIVATE_GITHUB_ENV -- \
  sdlc run --reference WORK_REFERENCE --ticket NUMBERED_FILE --docker-tests
```

Use the same wrapper for resumes and required GitHub preparation commands.
`op run` resolves references into its child environment and masks secrets in
terminal output by default. The token remains readable to the host process and
other sufficiently authorised host processes; masking does not prevent code
from stealing it. SDLC's explicit container environment does not forward this
token. See [op run](https://www.1password.dev/cli/reference/commands/run) and
[environment security](https://www.1password.dev/cli/secrets-environment-variables).

GitHub CLI recommends `GH_TOKEN` for fine-grained PATs. Its `auth login
--with-token` route expects classic PAT scopes and can behave confusingly with
fine-grained scoping. Do not run `gh auth token`, enable shell tracing, put a token
in a remote URL, or store it as a literal Git config value. A revoked or expired
external PAT needs replacement in the vault; `gh auth refresh` does not renew it.

### Clone and push preparation

The wrapper alone does not make bare Git use `GH_TOKEN`. For HTTPS Git, explicitly
use the same host `gh` credential helper that SDLC's publisher uses:

```sh
op run --env-file=/PATH/TO/PRIVATE_GITHUB_ENV -- \
  git -c credential.helper= \
  -c 'credential.https://github.com.helper=!gh auth git-credential' \
  clone https://github.com/OWNER/REPO.git /PATH/TO/NEW_CHECKOUT
```

With saved host `gh` login, omit only the `op run` prefix. Use the same Git
`-c` options for a host fetch or push when a configured helper is otherwise
absent. Remotes stay credential-free. SDLC's publisher already supplies these
helper options, so its run command needs no token argument.

Git supports helpers/askpass for credential retrieval. For a future dedicated
helper, validate the HTTPS host and exact repository and consider
`credential.useHttpPath=true`; by default, HTTP credential matching ignores the
repository path. A helper that returns a token inside a worker still exposes
that token there. Keep Git network operations within the trusted publication
boundary: currently the host, and the separate publisher in the planned design.
See [Git credentials](https://git-scm.com/docs/gitcredentials).

## Git identity and signed commits

Git name/email, GitHub authentication and signing authority are separate
settings. Read identity in the original project directory so repository-local
settings and conditional includes are applied:

```sh
git -C /PATH/TO/PROJECT config --get user.name
git -C /PATH/TO/PROJECT config --get user.email
git -C /PATH/TO/PROJECT config --type=bool --get commit.gpgsign
git -C /PATH/TO/PROJECT config --get gpg.format
git -C /PATH/TO/PROJECT config --get user.signingkey
```

Use the approved email, including the account's actual GitHub noreply address
when appropriate; do not construct one from a username. The current publisher
transfers the selected values to separate Git metadata rather than copying
`.gitconfig` or trusting the worker's Git configuration. It does not preserve
the worker's generic author identity in the published commits. `GIT_AUTHOR_*`
and `GIT_COMMITTER_*` environment overrides are stripped; use the approved Git
config values for this route. See
[Git configuration](https://git-scm.com/docs/git-config) and
[GitHub email forms](https://docs.github.com/en/account-and-profile/reference/email-addresses-reference).

### Host 1Password SSH signing

Use this route only for supervised diagnostics. It does not establish that
signing will continue after the desktop app locks or the machine restarts.

Keep the private key in 1Password. Configure host Git for SSH signing and provide
its **public** key as `user.signingkey`. 1Password's supported macOS signing
program is `/Applications/1Password.app/Contents/MacOS/op-ssh-sign`. Prefer the
existing working host configuration, with repository-local identity overrides
where needed. See [1Password Git signing](https://www.1password.dev/ssh/git-commit-signing).

Register the public key with GitHub as a signing key. Registering a key only for
SSH authentication does not establish commit-signing verification. No GitHub
token is involved in generating the local signature. See
[GitHub SSH signing](https://docs.github.com/en/authentication/managing-commit-signature-verification/telling-git-about-your-signing-key#telling-git-about-your-ssh-key).

SDLC also runs `git verify-commit`. SSH verification requires
`gpg.ssh.allowedSignersFile`, pointing to a trusted host file containing the
approved principal/public key. An illustrative entry is:

```text
YOUR_EMAIL_PLACEHOLDER namespaces="git" ssh-ed25519 PUBLIC_KEY_BASE64_PLACEHOLDER
```

Set the path locally if it is not inherited:

```sh
git config --local gpg.ssh.allowedSignersFile /PATH/TO/TRUSTED_ALLOWED_SIGNERS
git config --local commit.gpgsign true
```

The trust file contains public material, but keep real identity settings outside
this public repository. GitHub verification alone does not replace local trust
configuration. Prove the route with a signed baseline commit and
`git verify-commit HEAD` before requesting a provider implementation. See
[Git SSH trust settings](https://git-scm.com/docs/git-config#Documentation/git-config.txt-gpgsshallowedSignersFile).

If the existing host uses GPG rather than SSH, retain that supported signer and
verify a test commit on the host. Current SDLC transfers `gpg.program`, but not
repository-local or conditional `gpg.openpgp.program`. If that modern setting
selects a custom signer, explicitly configure its equivalent absolute host path
as `gpg.program` for the trial. The configured GPG key identity, committer
email and verified GitHub email must agree for GitHub verification; do not force
a new SSH route just for SDLC. See
[GPG email verification](https://docs.github.com/en/authentication/troubleshooting-commit-signature-verification/using-a-verified-email-address-in-your-gpg-key).

## Machine credentials for unattended Docker delivery

Use a 1Password Service Account with the official CLI, scoped to `read_items` in
a dedicated automation vault. `OP_SERVICE_ACCOUNT_TOKEN` supports `op read`,
`op run` and `op inject` without a desktop approval session. Built-in Personal,
Private, Employee and default Shared vaults are excluded; vault grants and
permissions are fixed at creation. Remove conflicting Connect configuration so
the selected authentication route is explicit. See
[service-account CLI authentication](https://www.1password.dev/service-accounts/use-with-1password-cli)
and [service-account provisioning](https://www.1password.dev/service-accounts/get-started).

Keep only the intended SDLC credentials in that vault; vault-wide read access
must not expose unrelated personal or production secrets.

The service-account token is itself a secret. One-time provisioning must supply
it through a protected runtime secret mechanism that works without desktop
approval. Keeping it only in a vault that requires that same token to open does
not solve bootstrap. Do not put it in an image, source checkout, Docker command
line, recorded container environment or run journal. The supported CLI receives
it in its own child environment after the trusted resolver reads the runtime
secret. The durable store and recovery procedure must be selected and tested
before claiming unattended restart support.

A service account retrieves secret fields; it is not the desktop SSH signing
agent. Use a dedicated SDLC signing key, never an exported personal signing key.
The official `op read` command supports retrieving an SSH private key in OpenSSH
format. A separate publisher can write it directly to a private temporary file
and use Git SSH signing. This gives that container access to the private key.
See [SSH-key retrieval and private output files](https://www.1password.dev/cli/reference/commands/read)
and [Git signing configuration](https://git-scm.com/docs/git-config#Documentation/git-config.txt-usersigningKey).

Keep key files in restricted tmpfs storage, remove them at completion and give
neither signing capability nor GitHub credentials to provider or check workers.
Tmpfs avoids the container's writable layer but can reach host swap; it is not
an unconditional guarantee of memory-only storage. Docker Compose file secrets
are mounted files, not an encrypted secret store. See
[tmpfs limitations](https://docs.docker.com/engine/storage/tmpfs/)
and [Compose secret delivery](https://docs.docker.com/compose/how-tos/use-secrets/).

Register the dedicated public key as a GitHub signing key for the approved
identity, and require local verification of that exact signer before every push.
A machine signature proves control of the key; it does not prove a human approved
each change. Freeze the approved name/email from the project's effective Git
configuration at launch, rather than mounting the host's entire Git configuration.
The [unattended Docker delivery plan](proposals/unattended-docker-delivery.md)
defines the publisher boundary, supervision and acceptance checks.

1Password Connect is another supported machine-access route, with a server
credential, API token and local cache to operate. It adds infrastructure and
still needs secure bootstrap; it does not supply desktop-agent signing. Start
with a Service Account unless caching or deployment needs justify Connect. See
[Connect deployment](https://www.1password.dev/connect/get-started).

### Why desktop approval and forwarding are insufficient

1Password can authorise an application and subprocesses for a session; approval
rules and lock state determine whether later signing needs interaction. Extending
the authority window cannot guarantee unattended signing after lock or restart.
Do not use it as the production automation path. See
[1Password agent security](https://www.1password.dev/ssh/agent/security)
and [key selection](https://www.1password.dev/ssh/agent/config).

Docker Desktop documents an SSH-agent bridge at
`/run/host-services/ssh-auth.sock` on Mac/Linux, and 1Password documents SSH
forwarding. Those documents do not establish that the ordinary Docker bridge
selects an arbitrary custom 1Password socket. A raw macOS socket mount or macOS
`op-ssh-sign` path is not a verified Linux-container signing setup. A bridge also
does not remove the desktop agent's approval requirement. See
[Docker agent forwarding](https://docs.docker.com/desktop/features/networking/networking-how-tos/#ssh-agent-forwarding)
and [1Password forwarding](https://www.1password.dev/ssh/agent/forwarding).

Security implication: a forwarded agent keeps private key bytes on the host but
lets any process with socket access request authentication/signatures. A read-only
private-key mount prevents changes, not copying. Do not provide either to coding
or dependency/test workers. If a future separate publisher container needs an
agent, restrict the keys and its requests and validate the bridge first.

## Proposed improvements

These changes are not implemented by this documentation iteration:

1. Add a separate Docker publisher with machine credential resolution. It must
   sign and publish without desktop 1Password, forwarded personal agents or
   interactive fallback. Keep secrets outside provider/check containers.
2. Freeze approved identity/public signing settings for a run, reject unexpected
   changes on resume, and verify the same account/token scope at publication.
   Normalize source-relative signing paths and preserve supported format-specific
   signer settings. Do not save a token or private key in the run journal.
3. Add publication preflight and private secret-store profiles. Record the
   intended repository, GitHub account or App installation, approved Git identity
   and required signer before provider work. Keep public templates free of real
   vault references, emails and credential paths. Constrain the repository/ref
   and disable Git hooks/config execution from the project. Signing errors must
   stop before push, without changing lightweight ticket discovery.
4. For sustained automation, consider a GitHub App installed only on selected
   repositories. Installation tokens expire after one hour and can be restricted
   to repository/permission subsets. Keep the App private key within the trusted
   credential resolver/publisher, refresh tokens through the documented flow and
   configure commit signing separately.
   This is a future implementation choice, not an existing SDLC flag. See
   [installation authentication](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/authenticating-as-a-github-app-installation).

An App push credential does not turn a local human commit into a verified bot
signature. GitHub's automatic bot verification has separate requirements; use
the approved dedicated signing key and identity. See
[bot verification](https://docs.github.com/en/authentication/managing-commit-signature-verification/about-commit-signature-verification#signature-verification-for-bots).

Separating publication credentials from workers reduces what compromised project
dependencies can reach. It does not protect against a compromised host,
Docker administrator, controller or publisher. An authenticated provider can
still reach its own native account cache. Read the
[remaining security risks](../README.md#security-boundary-and-risks) before
granting unattended publication authority.
