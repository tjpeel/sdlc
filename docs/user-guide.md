# SDLC user guide

SDLC runs prepared engineering tickets through implementation, isolated checks, signed draft PR
publication, CI and independent review. Start from the repository where you want the work done. The host CLI
coordinates disposable Docker workers; your original checkout remains intact.

This guide covers installation, account setup and daily use. Its example profile names are `personal` for
GitHub and `personal-key` for signing. Replace uppercase placeholders locally. Keep real account settings,
tickets, credentials and logs out of the public SDLC source checkout.

Follow sections 1–6 once per installation/account setup. For each project, follow
section 7 once and review its settings when the project changes. For daily work,
start at sections 8–9; use sections 10–12 to monitor, resume and retain evidence.
After a mistake, return to the failed step using section 13. Successful logins
and signing setup do not need repeating for every ticket.

## 1. Prepare the host and accounts

Use macOS or Linux for the complete signing and publication workflow. Windows can install the CLI and use some
commands with a Linux-container engine, but signing, private account pairing and history removal/export
currently fail closed until equivalent ownership checks exist. The shell examples below use POSIX syntax. The
current runtime supports amd64 and arm64 Linux containers.

On the host, you need:

- Git and Go 1.25 or later to install from source. The installed executable does
  not need Go to run, but reinstalling from source does.
- A local Docker engine with Linux containers and its Docker CLI. Integration
  checks using `--docker-tests` also require permission to start a privileged
  disposable Docker-in-Docker daemon.
- A private terminal and browser for the official account login flows.
- Your own supported Codex and Claude Code accounts, with access to the models
  you intend to request. Review [provider usage rules](provider-usage.md).
- A GitHub account authorised for the target repository and its organisation
  SSO/OAuth policy, where applicable.
- 1Password with custom vaults, Service Accounts and an Ed25519 SSH Key item.
  The desktop app is used for initial key provisioning; later signing uses the
  official 1Password CLI in a separate container.

Host `gh`, an SSH agent and project SDKs can help with your existing clone, fetch, baseline and CI setup. They
do not supply SDLC's publication credentials or its workers' tools. The shared image already includes the
official Codex, Claude, GitHub and Docker clients, Compose/Buildx, Git, OpenSSH, Python, Node/npm, .NET 10
SDK, and pinned skills and agent definitions. Do not install host copies of the model clients or `op` solely
for this route.

Use trusted repositories and inputs. Provider workers can access their own native login caches and the
network. Check workers have no provider/GitHub/vault credentials, but dependency scripts and tests can read
the source and check inputs they receive. Docker administrators can read credential volumes; SDLC does not
encrypt them. Account login does not grant permission for every use: this is the account owner's local
single-user CLI workflow, not a service that shares subscription access. Account-authenticated ticket runs
reject CI.

If prerequisites are missing, follow the official [Git installation](https://git-scm.com/install/),
[Go installation](https://go.dev/doc/install), and [Docker Desktop for macOS](https://docs.docker.com/desktop/setup/install/mac-install/)
or [Docker Engine for Linux](https://docs.docker.com/engine/install/) instructions.
Start the Docker engine, open a new terminal after PATH changes, and confirm:

```sh
git --version
go version
docker version
```

`docker version` must show both client and server. If Go is installed but not found,
put its installation's `bin` directory on PATH; SDLC's installer does not install
Go. Choose the installer matching your host architecture.

## 2. Install or reinstall the host CLI

Clone the SDLC source into a local directory. Select an existing writable installation directory that is
already on your shell's PATH:

```sh
cd /PATH/TO/SDLC_SOURCE
go run ./cmd/sdlc-install --bin-dir /PATH/TO/YOUR_BIN_DIRECTORY
sdlc --version
sdlc --help
```

Add your chosen bin directory to PATH before running the installer if necessary. It refuses a destination
outside PATH or an earlier conflicting `sdlc` command. Clear cross-compilation `GOOS`/`GOARCH` overrides: this
installer builds for the current host. On Windows, close running SDLC processes before replacing the
executable. Release archives and package-manager installation are not implemented.

After updating the source, repeat the install command. It builds the replacement before switching executables,
leaves the old executable intact on build failure, and preserves runtime records, login volumes and private
settings. Keep the same installation state directory and Docker engine to reuse saved logins.

Private installation state normally uses the OS user configuration directory followed by `sdlc`:
`~/Library/Application Support/sdlc` on macOS or `~/.config/sdlc` on Linux, respecting XDG configuration.
`SDLC_STATE_DIR` can select a different private directory. Keep it outside every Git checkout, with owned
mode-`0700` directories and mode-`0600` metadata files, without symlink paths or extra access grants. Do not
copy actual account state into the source tree or Docker context.

## 3. Build and inspect the shared image

From the source checkout:

```sh
sdlc runtime build --source .
sdlc runtime status --offline
sdlc runtime status
sdlc runtime status --all
```

The image is tagged `sdlc:local`. SDLC records its immutable image ID and Docker engine identity; a moved tag
alone cannot substitute another image for a run. The first build downloads public dependencies. Its bounded
build context excludes host credentials, local account profiles, private work and Git metadata.

Offline status checks the recorded local runtime and inventory. Ordinary status also checks public upstream
update metadata; it does not update anything or prove provider, GitHub or vault access. `--all` includes
supporting dependencies. An unpaired GitHub account can report signing attention while the runtime itself
passes. Use the dedicated account/signing commands below to check their readiness.

When no run or session holds the runtime lease, preview and apply updates:

```sh
sdlc runtime update --dry-run
sdlc runtime update
```

This dry-run contacts public release sources. The executing update resolves exact pins, rebuilds and selects
the candidate only after validation. Failed updates retain the previous selected runtime. Pins stay in private
installation state; source pins and account caches remain available. Later `runtime build` commands can use
the saved source location; use `--source /PATH/TO/SDLC_SOURCE` if it moves. Reinstalling the host CLI and
rebuilding the image are separate operations.

For an initial source build, prefetch the current default signing resolver image:

```sh
docker pull 1password/op:2.39.0@sha256:3cd5a1febc662c93d46b944b983b301e710da5c016ef63be9d436cf2b1ed30d5
```

This public pull does not access a vault. Signing uses the selected pinned image with `--pull never`; after
runtime updates, use that runtime's selected resolver pin when restoring a missing image. See [runtime
commands](cli.md#inspect-the-runtime) for inventory and update details.

## 4. Sign into the model providers

Use a private interactive terminal, with the Docker engine running:

```sh
sdlc auth login
sdlc auth login --provider claude
sdlc auth status --all
```

The first command defaults to Codex. Follow its official device/browser flow; device authentication must be
enabled for the account. Claude uses its official browser login. If the browser cannot reach the container
callback, follow the native terminal instructions for the displayed code. Keep login URLs, codes and terminal
recordings private. Do not copy provider tokens into SDLC configuration.

Status uses a fresh network-disabled container and a read-only cache. It proves that the client can load
stored account login, not that a token is currently valid remotely or that your account can use a particular
model. A missing login returns nonzero. Check one provider with `sdlc auth status --provider codex` or
`--provider claude`. Login state survives normal container removal, image rebuild and CLI reinstallation;
official clients manage their own authentication state.

Optionally validate each native interface with a harmless prompt:

```sh
sdlc interactive --approval on-request
sdlc interactive --provider claude --permission-mode manual
```

These sessions open an empty disposable workspace, without the current checkout. Ask only a harmless question
and exit through the client; Claude accepts `/exit`. Workspace files and interactive progress are discarded.
This is not the place for a ticket you need to retain, and a response can consume account usage.

Claude can still show first-launch setup or another sign-in prompt after saved login succeeds. Complete its
native prompts with the same account, exit, then open it again to check persistence. First-launch completion
and offline saved login are separate checks. Default interactive modes permit full access inside the
container; the examples above request more interactive approval handling. Managed provider policy can reduce
or refuse requested access.

Optional shared instructions apply across projects; new runs freeze their current body. Inspect them with
`sdlc instructions show`, add private Markdown with `instructions set --file /PATH/TO/PRIVATE_INSTRUCTIONS.md`,
or remove additions with `instructions reset`. The stop-for-human-questions rule remains.

## 5. Provision a dedicated signing authority

### Create the vault, key and Service Account

Create a **custom** 1Password vault for automation, with access limited to its intended owner or
administrators. Keep unrelated secrets out. Service Accounts cannot access built-in Personal, Private,
Employee or default Shared vaults.

In the unlocked desktop app, choose **New Item → SSH Key → Add Private Key → Generate a New Key**, select
**Ed25519**, and save the item in that custom vault. Generate a new automation key. Copy only its public key
and note its SHA256 fingerprint. Leave the private key in 1Password; unattended signing requires a key that
the CLI can use without an interactive passphrase prompt.

On 1Password.com, create a Service Account under **Developer**. Select only the automation vault and grant
**Read Items** only. Disable write/share access, vault creation, unrelated vaults and Environments. Save the
one-time token securely in a separate private recovery vault, outside the automation vault.

Read Items grants access to the entire selected vault. A field reference does not narrow that authority.
Review the grant yourself: SDLC cannot attest its scope. Incorrect grants require a replacement Service
Account; token rotation retains permissions. Detailed official links and provisioning screens are in
[1Password signing setup](1password-signing-setup.md).

### Save the signing profile without exposing secrets

Run the setup wizard in a private interactive terminal:

```sh
sdlc signing setup --profile personal-key --provider 1password
```

Enter vault and SSH Key item names or IDs at their separate prompts, without shell quotes. IDs avoid ambiguity
after renames. Enter the **public** Ed25519 key and expected fingerprint; a blank fingerprint is calculated
from that public key. The wizard constructs the OpenSSH field reference:

```text
op://YOUR_VAULT_ID/YOUR_ITEM_ID/private key?ssh-format=openssh
```

The locator is not a credential, but real vault/item names and paths are private metadata. Setup asks for the
Service Account token with terminal echo disabled. Enter it only at that hidden terminal prompt. Never put a
token or private key in an argument, shell command, AI prompt, ticket, screenshot, source file or chat. Do not
print a token file or run a secret-reading command to inspect its contents. Review the local configuration and
confirm the save.

The wizard saves private profile JSON and a plaintext bootstrap token file outside checkouts. The JSON
contains provider, locator, public key/fingerprint and bootstrap path, with no token or private key. 1Password
advises against plaintext token storage; this bootstrap is a current SDLC limitation. Use encrypted host
storage, owned `0600` files and least-privilege vault access. Permissions are not encryption; avoid
shared/synchronised storage.

If you already have a safe external token file, use:

```sh
sdlc signing setup --profile personal-key --provider 1password \
  --bootstrap-file /PATH/TO/YOUR_PRIVATE_BOOTSTRAP
```

It must contain only the token, be owned mode `0600`, be outside all Git checkouts, and have no symlink or
hard link. Setup makes no network request and creates no 1Password resources. `1password` is currently the
only signing-secret provider.

### Check access and signing separately

```sh
sdlc signing status --profile personal-key
sdlc signing status --profile personal-key --verify
```

Plain status checks local configuration, public identity and bootstrap safety. Connected `--verify` uses
official `op` to retrieve the key, checks its public key and fingerprint, and signs/verifies a disposable
commit in a separate network-disabled container. `sdlc signing verify --profile personal-key` is an alias.
This makes no GitHub or model request and does not prove GitHub attribution or the Service Account's grant
scope. Success describes this check only.

Normal output omits locators and storage paths. Use `signing status --show-config` with the named profile only
for deliberate private metadata inspection; it never prints the token or private key. Keep even that metadata
output private.

For changes, rerun the wizard with the actual replacement flag:

```sh
sdlc signing setup --profile personal-key --provider 1password --replace
```

Enter keeps existing vault/item/public-key values. A blank fingerprint is recalculated. Keep the current token
or enter a new one at the hidden prompt; `--bootstrap-file` can select a different safe file. New tokens use
separate files. Setup never overwrites/deletes an existing bootstrap, which another profile might share.
Saving switches the profile atomically; cancellation keeps it intact. Recheck with `--verify` afterwards.

### Understand the unattended credential boundary

The host controller reads the bootstrap for the short-lived official `op` resolver. The token and locator
enter that container through stdin; only the native CLI child receives the token in its process environment.
The token grants vault access so signing can proceed while the desktop app is locked or closed. It never goes
to provider/check workers or the GitHub publisher.

The resolver returns the private key to the trusted publisher through stdin and tmpfs. The publisher verifies
the approved public identity, signs, and removes its key file before push. No desktop SSH agent or approval
prompt is involved. These steps limit key lifetime; they do not guarantee erasure from every buffer, host swap
or administrator access. The bootstrap remains plaintext on the host. An unattended OS secret store and
managed bootstrap recovery are future work.

## 6. Sign into GitHub and pair the account with the key

An SSH **authentication** key authorises clone/fetch access. An SSH **signing** key signs commits. SDLC
publishes over HTTPS through a separate native `gh` login; neither your host SSH key nor a signing key selects
that GitHub account.

In the intended GitHub account, add the automation **public** key under **Settings → SSH and GPG keys → New
SSH key**, choosing **Signing key**. Use the account's verified email or GitHub noreply email for the project
Git identity. Local signature verification alone does not establish GitHub's Verified badge.

Establish organisation SSO in your browser when required, then run:

```sh
sdlc auth login --service github --profile personal
sdlc auth status --service github --profile personal
sdlc auth status --service github --profile personal --verify
sdlc github pair --profile personal --signing-profile personal-key
sdlc github status --profile personal --verify
```

Complete official `gh` login using the intended account. Offline auth status checks cache presence; its
`--verify` contacts GitHub and displays the selected login and numeric ID. Confirm those values. Organisation
OAuth/SSO approval is still required; working host SSH access does not prove this new session's access.

Pairing checks the native account and that its configured public key is registered as a signing key. It
fetches no 1Password secret and makes no GitHub writes. Names may differ; `--signing-profile` defaults to the
account profile name when omitted. `github status --profile personal` inspects the global pair; `--verify`
rechecks native account/key registration without proving vault access.

After pairing, `sdlc runtime status --offline --github-profile personal` includes
that profile's local signing readiness. Without this flag runtime status checks
the `default` profile, which may still be unconfigured.

Each GitHub account has a separate private plaintext Docker cache, with its own lease, on the same shared
image. Host `GH_TOKEN`, host `gh` configuration and SSH agents cannot override it. Use another named profile
for another account. Names use 1–48 lowercase letters, digits, underscores or hyphens, starting with a letter
or digit. Auth commands without a profile use `default`; fresh ticket runs resolve repository selection
instead. Existing login/signing setup remains, but pair each account once before launching new runs.

A populated native profile refuses another login. Reauthorise with its own `auth logout --service github
--profile personal` then login, or provision another profile. Local logout does not revoke remote authority. A
deliberate changed account/key pair requires `github pair --profile personal --signing-profile personal-key
--replace`, followed by `github use` in affected repositories. Existing frozen runs will not follow a
replacement account or key.

## 7. Prepare the project once

Clone/fetch the target repository on the host through your existing approved route. SDLC captures a local
checkout; authenticated URL cloning is not implemented. Use an ordinary checkout with an existing commit and
local base branch. Run capture currently rejects linked worktrees, submodules, source symlinks and unsafe Git
metadata. The local base must be included in source HEAD and match GitHub at publication; fetch/update it
deliberately before launch.

From the project repository:

```sh
cd /PATH/TO/YOUR_PROJECT
git config --local user.name 'YOUR_GIT_NAME'
git config --local user.email 'YOUR_VERIFIED_EMAIL'
sdlc init
sdlc github use --profile personal
sdlc github status --verify
```

`init` takes no arguments. It discovers local Git state, manifests and work paths, creates `.sdlc/work/`, and
protects it with Git's local `info/exclude` rule. It also creates or validates `.sdlc/project.json`. It needs
no Docker/account login, runs no checks and makes no account selection. Private work must remain ignored and
untracked; tracked/staged private paths cause failure. Adding an ignore rule does not remove earlier private
content from Git history.

`github list` shows all configured native profiles and paired/unpaired account/signing metadata locally;
login validity remains unverified. `github use` checks the selected native account, signing key and repository
push access before saving this checkout's selection outside Git, without writing
account settings into `.git` or project files. Repository-mode `github status --verify` checks selected
account identity, repository push permission and public-key registration. Pair/selection records hold public
IDs/login/key/fingerprint, without vault locators or tokens.

Fresh runs use saved selection, a unique owner pair or the sole configured pair. `github use` can omit
`--profile` for a saved selection or sole configured profile; multiple profiles require an explicit choice. A run's `--github-profile` can choose a
registered pair for an unbound checkout but cannot override conflicting saved selection. `init` remembers a unique GitHub origin identity locally, including SSH remote metadata, without
using host SSH credentials. Saved identity/selection takes precedence over later origin edits or removal.
Changed pair metadata requires deliberate reselection. Only GitHub.com is supported. Supply `--repo OWNER/REPO`
once to `github use` when no identity can be inferred; subsequent commands reuse the saved selection.

### Review checks without replacing the project's own configuration

`.sdlc/project.json` is portable project configuration and can be committed after review. Keep account data,
vault references and private host paths out. Existing `package.json`, lockfiles, SDK selections and CI remain
the project's authority; `init` may suggest checks from them but does not replace them. Repeated `init`
preserves valid custom settings. Dependency installation must be an explicit check when required; the
controller does not guess commands at run time.

For npm, configure the repository's own commands, for example `["npm", "ci"]`, `["npm", "run", "test"]` and
`["npm", "run", "build"]` inside the `checks` array. Use scripts that actually exist. Each check is an
argument array, not a host shell string; checks run in order in a separate worker. There is no standalone
`sdlc check`.

.NET initialization does not infer checks. For a .NET 10 `.slnx` project, configure its actual solution/test
paths and VSTest or Microsoft Testing Platform commands. The runtime's SDK must satisfy `global.json`; inspect
the installed image inventory rather than assuming that the host SDK is supplied to Docker. The public smoke
fixture's settings illustrate one supported topology:

```json
{
  "version": 1,
  "checks": [
    ["dotnet", "build", "Smoke.slnx"],
    ["dotnet", "test", "tests/Smoke.UnitTests/Smoke.UnitTests.csproj"],
    ["python3", "scripts/integration.py"]
  ],
  "input_files": [".env"]
}
```

For ignored test `.env` or similar files, list exact relative paths in `input_files`. SDLC freezes their
contents and copies them only into the check workspace; later host changes do not alter that run. Do not
select them with `--input`, which exposes requirements to providers. Files already tracked in source remain
visible to providers. Use disposable test configuration, never production credentials.

Compose/service checks need `run --docker-tests`. Each run gets its own test daemon, network and volumes;
matching internal service ports can coexist without publishing host ports. Configure required services,
readiness and relative mounts inside that topology. External host files are not supplied automatically. This
privileged mode is for trusted tests. The [public .NET fixture](../examples/dotnet-smoke/README.md) shows
API/Mongo, Compose interpolation and check-only `.env` capture; it does not establish every real repository's
consumer/queue integration.

Configure PR-triggered CI before launching: SDLC requires reported checks at the current draft PR head. A
green baseline is a useful starting point; missing CI reports pause delivery after the bounded startup wait.

## 8. Prepare and discover one ticket

Keep the work stream private inside the project:

```text
.sdlc/work/YOUR_WORK_REFERENCE/
  specification.md
  decisions.md
  tickets/
    01-add-feature.md
    02-add-tests.md
```

Use your editor to supply real requirements and acceptance criteria. The reference is the exact folder name,
including case, without path separators or control characters. Tickets must be numbered Markdown files
directly in `tickets/`. List them with:

```sh
sdlc work --reference YOUR_WORK_REFERENCE
```

Discovery reads filenames and local Git metadata, not ticket bodies. It orders numeric prefixes, rejects
duplicates, and ignores unnumbered notes. It does not judge eligibility, dependencies, blockers or
completeness. Selecting one ticket never launches the next. The implementation skill evaluates those gates
when encountered. Specification/decision files are optional explicit inputs; SDLC does not discover or supply
them automatically.

## 9. Preview, then launch

From the initialized project:

```sh
sdlc run --reference YOUR_WORK_REFERENCE --ticket 01-add-feature.md \
  --input .sdlc/work/YOUR_WORK_REFERENCE/specification.md \
  --input .sdlc/work/YOUR_WORK_REFERENCE/decisions.md --dry-run
```

Omit inputs that do not exist or are not needed. Review provider roles, selected paths, checks, repository and
pairing. This dry-run is offline: it makes no model, GitHub or vault request and does not capture source or
register a dashboard run. Missing pairing produces a warning, while malformed/changed metadata and ambiguous
selection fail. Dry-run cannot prove account/model access or check success.

When ready, run the same command without `--dry-run`. Add `--docker-tests` to both preview and launch when the
configured checks require Docker integration:

```sh
sdlc run --reference YOUR_WORK_REFERENCE --ticket 01-add-feature.md \
  --input .sdlc/work/YOUR_WORK_REFERENCE/specification.md \
  --input .sdlc/work/YOUR_WORK_REFERENCE/decisions.md --docker-tests --timeout 2h
```

Preserve the inputs approved in your preview. The default implementation provider is Codex; Claude reviews
independently. `--provider claude` reverses those roles. Defaults come from the installed selection; dry-run
shows the requested models and efforts. `--model`, `--effort`, `--review-model` and `--review-effort` override
new lead settings. They do not establish account entitlement or replace the image's subagent policies.
Provider-reported model mismatches stop the run; missing model reports remain unverified.

SDLC captures source, local changes and selected inputs into a separate workspace and creates a unique
`work/...` publication branch. Use `--base` only for an approved different local base. Let it generate
branches unless you have a reason to supply `--branch`. It freezes the image, models, input hashes, Git
name/email, account/repository IDs and signing identity before provider work.

The implementation makes candidate commits. The controller checks the committed candidate in an isolated
worker, then returns evidence to the same implementation session for whole-ticket review. The trusted
publisher recreates delivered commits with the frozen Git identity and approved SSH key, preserving the tested
tree, and publishes a **draft** PR. Current-head CI must pass before fresh independent review. Actionable
findings return to the original implementation session; repairs repeat checks, signed publication, CI and a
new review. A complete clean review and passing current CI produce `ready` for human review. SDLC never
merges. Inspect the actual PR, author, signatures/Verified attribution, CI and changes.

The implementation login is required. Missing opposite-reviewer login allows checks, draft publication and CI,
then pauses at `awaiting_reviewer`. Log in that provider and resume. Questions, denials, usage limits and
inconsistent identity stop rather than silently switching accounts or restarting to evade limits.

`run` is a real headless provider job, but its controller is a **foreground host process**. Keep its terminal
open while it runs. Closing a separate dashboard terminal does not stop it. Detached daemon/supervisor
execution is not implemented.

Two independent calls can use separate captured workspaces and generated branches from the same unchanged
baseline. Start them in separate terminals and avoid conflicting or dependent changes. Same-provider native
operations queue on their cache leases; Codex and Claude can overlap. Integration daemons remain separate.
Host capacity, account limits and external movement of the base still apply.

To schedule a whole feature, start from a clean checkout at the published integration revision:

```sh
sdlc run --reference YOUR_WORK_REFERENCE --all --parallel 2 --dry-run
sdlc run --reference YOUR_WORK_REFERENCE --all --parallel 2 --watch
```

`--parallel` defaults to `1` and accepts `1` through `8`. Add `--alternate-providers` to alternate
implementers in planned ticket order; the opposite provider reviews each ticket. Same-provider cache
leases can still queue. Supply common requirements with the same repeatable `--input` flags.

An optional private `.sdlc/work/YOUR_WORK_REFERENCE/plan.json` maps exact ticket filenames to lower-first
`priority`, `depends_on` filenames and `touches` path prefixes. Overlapping prefixes serialize work.
Without it, tickets are independent and use numeric filename order. A child with one dependency can
start from its parent's signed, tested and reviewed draft PR, targeting the parent branch/head. Multiple
parents must all merge into the integration branch before their child starts. See the
[plan format](cli.md#private-feature-plan).

`--watch` keeps the foreground controller observing human merges. Without it, SDLC finishes currently
unblocked work and exits. Rerun the same feature command to continue its checkpoint. Compatible existing
runs are adopted rather than duplicated; the plan, models, accounts, runtime, inputs and checks stay frozen.
After a squash merge or base movement, SDLC restacks only each ticket's commits, checks the owned PR head
and push lease, and repeats signing, checks, CI and independent review. Conflicts return to the original
implementation session; product decisions stop for a human answer. SDLC never merges PRs.

A human-attention stop ends feature execution. Resolve it with the individual ticket's recorded resume
command, then rerun `--all`. The dashboard continues to list individual ticket runs. Feature scheduling and
reconciliation have offline test coverage; connected feature execution still needs a supervised trial.

## 10. Watch runs and optional local notifications

Use another terminal:

```sh
sdlc dashboard
sdlc dashboard --page 2
sdlc dashboard --run RUN_ID_OR_UNIQUE_PREFIX
sdlc dashboard --run RUN_ID_OR_UNIQUE_PREFIX --logs
sdlc dashboard --once
sdlc dashboard --json
```

The live terminal refreshes every two seconds and shows ten runs per page across repositories.
Questions/problems come first, then missing reviewer login, queued/running work, and completed work. Within
groups, newer updates come first. Type `n` then Enter for next page, `p` then Enter for previous, or `q` then
Enter to close. Prefixes must be unique and at least six hexadecimal characters. Redirected output defaults to
one snapshot; `--watch` appends snapshots. `--interval` accepts `250ms` through `1m`.

Details include questions, findings, check evidence, PR state and the resume command. `--logs` adds a bounded
private tail; it requires `--run` and cannot combine with JSON. Keep output private. A heartbeat indicates
liveness, not percentage completion. Native context/usage reports can be missing; the display does not invent
a context window or turn aggregate counters into current usage.

Notifications default off. For macOS desktop alerts:

```sh
sdlc dashboard --notify desktop --sound
```

A watching dashboard observes attention and new ready transitions across pages. Existing historical ready runs
stay quiet at startup. `--notify bell` requires a terminal; `--sound` requires desktop mode. macOS
Focus/notification settings can suppress delivery. Alerts contain fixed labels and a run-ID prefix, with no
work, repository, question, path or log text. Delivery failures warn once and do not stop runs. There is no
email, webhook or remote notification service.

Add `--notify desktop --sound` to the `run` command for alerts without a dashboard, including on resume. A
sudden controller death needs a separate watcher to detect stale heartbeat. Keep both terminals open if using
that arrangement. Starting another watcher can repeat current attention; enabling both watchers and run alerts
can duplicate notifications.

## 11. Resolve a stop and resume the recorded run

Use the full original run ID and the same work folder/ticket:

```sh
sdlc run --reference YOUR_WORK_REFERENCE --ticket 01-add-feature.md \
  --resume RECORDED_RUN_ID --timeout 2h
```

For `waiting_for_human`, save a nonempty UTF-8 answer in an owned private external file, then use:

```sh
sdlc run --reference YOUR_WORK_REFERENCE --ticket 01-add-feature.md \
  --resume RECORDED_RUN_ID --answer-file /PATH/TO/PRIVATE_ANSWER.txt
```

The answer becomes provider input and private run history; include only what the question needs. For
`awaiting_reviewer`, authenticate the missing reviewer first. For `blocked`/`failed`, inspect the stop reason,
repair that failure, then resume. For an interrupted/stale controller, confirm its process and container state
before resuming; stale display alone does not prove the controller has stopped.

Resume preserves the frozen repository, account/key, image, models, instructions, inputs, checks and native
implementation session. It accepts only reference, ticket, resume ID, answer file, timeout, dry-run and
notification options. Do not supply new provider/profile/model/input flags to change the run. Existing older
frozen journals retain their legacy same-name signer route. Changed identities, missing pinned images, moved
bases or external PR changes need deliberate reconciliation; SDLC does not fall back to host credentials.

The timeout is per controller invocation: default `2h`, accepted range `1m`–`24h`. Cancellation or timeout
retains a checkpoint. Fix the cause and resume the same ID; starting a new run is not a way to bypass a
provider's denial or usage limit.

## 12. Keep useful history and remove dashboard entries

Run artifacts have no automatic expiry. `ready` neither deletes files nor merges the PR. Before hiding an old
stopped run, export a private checkpoint report:

```sh
install -d -m 700 /PATH/TO/PRIVATE_REPORT_DIRECTORY
sdlc dashboard export --run RECORDED_RUN_ID \
  --to /PATH/TO/PRIVATE_REPORT_DIRECTORY
sdlc dashboard forget --run RECORDED_RUN_ID
```

The report directory must already exist outside Git checkouts, be owned mode `0700`, and have no symlink path.
Reports are mode `0600`; existing reports are never overwritten. Reports contain checkpoint state, models,
hashes, check commands/evidence, PR/CI details and latest outcome. They omit raw logs, transcripts,
explicit credential fields, source and private input contents. Still inspect them before sharing: work details and
command/model summaries can be sensitive.

`forget` (`remove` is an alias) deletes only that run's registry JSON. All journals, logs, inputs, captured
workspaces, native sessions and lock files remain. Active controllers cannot be forgotten, even with stale
heartbeat. Missing directories or unsafe metadata require repair first. Resuming registers the retained run
again. A checkpoint report cannot resume it.

For investigation or restoration, back up the **entire stopped run directory**:

```text
.sdlc/work/<reference>/runs/<ticket-stem>/<run-id>/
```

Use encrypted private storage, limited access and adequate disk space. Logs, transcripts and test output can
contain private code or secrets. A full backup still needs the recorded paths, runtime image and separately
configured authorised accounts to resume. Do not treat credential Docker volumes or signing bootstrap files as
ticket-history backups. Deleting the checkout deletes its local history unless backed up; the remote PR
remains. The [history guide](run-history.md) lists artifacts and report limits.

## 13. Repair one failed step at a time

| Symptom | Next step |
| --- | --- |
| `sdlc` missing or an older executable runs | Check PATH ordering, then reinstall into the intended existing directory. |
| Runtime missing/mismatched | Check the Docker engine and `runtime status --offline`; rebuild from the saved or explicit source when needed. Do not replace a frozen run's image silently. |
| Provider offline status passes but a job is denied | Check account/model access and current provider policy. Offline cache loading does not establish entitlement; stop on denials or limits. |
| Claude repeats first-launch prompts | Complete native setup with the same account in an interactive session, exit, and reopen to check persistence. |
| Signing verification fails | Inspect the named profile locally and repair image availability, bootstrap safety, vault access or key mismatch. Use `setup --replace` for configuration changes; never print the token/key. |
| GitHub account/key mismatch | Confirm the native profile's verified identity and public signing-key registration; change pairing deliberately, then reselect affected repositories. |
| Organisation or ambiguous profile cannot be inferred | Run `github use --profile NAME`; use explicit `--repo OWNER/REPO` for unsupported origin aliases. |
| Checks fail in Docker despite passing on the host | Verify image SDK/dependencies, check argument arrays, check-only inputs, service readiness and mounts. Host tools/files are not automatically supplied. |
| CI missing or reviewer unavailable | Configure PR-triggered CI or authenticate the opposite reviewer, then resume the original ID. |
| Queued operation appears idle | Inspect cache-wait state and the owning operation. Finish it and its cleanup; do not delete volumes or locks to bypass the lease. |
| History entry cannot be removed/exported | Restore a real private directory or repair unsafe metadata; stop any controller first. Windows ownership checks remain unsupported. |

Do not restart all setup after one failure. Reinstallation, runtime rebuild, provider login, signing setup and
repository selection each have separate state. Preserve working stages, inspect the reported cause privately,
and rerun only the needed command. If cleanup failed, inspect retained containers before retrying.

Single-ticket execution, account/key pairing, local notifications and retained history are implemented. Full
live provider/GitHub/SSO/signing attribution and repair trials still require validation in your authorised
disposable repository. Detached supervision, broader recovery,
managed encrypted bootstrap storage and additional signing backends remain planned. For a connected first
trial, [the onboarding runbook](onboarding.md) provides a disposable .NET baseline; [the CLI
reference](cli.md) records every option and [the credential guide](github-credentials.md) explains component
access.

## Optional: drive SDLC from a host harness

The [proposed `sdlc-drive` skill](skills/sdlc-drive/SKILL.md) defines a host-harness
workflow for selecting authorised work, starting the current CLI, monitoring
compact checkpoints, answering human questions and resuming through success.
It is Markdown with skill frontmatter, ready for review or opt-in installation
using your harness's supported skill mechanism. Keep it in the outer host
harness; do not load it into SDLC workers or create recursive SDLC jobs.

The skill does not add a daemon, series command or new permission to publish.
The harness must preserve the foreground controller session and existing account
safety rules. Read the [unified onboarding proposal](proposals/unified-onboarding.md)
for the proposed Back/Next/Resume setup flow and the remaining manual vault steps.
No `sdlc setup` command is available yet.
