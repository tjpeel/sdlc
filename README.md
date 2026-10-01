# SDLC container spike

Run Codex inside a container with an explicit GitHub account, SSH commit signing, and a separate command to publish a draft PR. This is an initial runtime experiment for an unattended development workflow, researched on 1 October 2026.

**Start with the disposable CLI worker.** It exercises the account, signing and ticket workflow before adding a desktop connection or a ticket queue. [The options note](docs/options.md) compares the alternatives and links the research.

This is a public repository. Examples contain placeholders; actual account settings and credentials belong in ignored local files or an external secret store. Keep real work tickets and job output outside the tracked source tree. The runtime stores authentication and results in Docker volumes.

“Full access” means Codex's approval and sandbox settings. It is independent of model choice. Here Codex runs as the `node` user with `--dangerously-bypass-approvals-and-sandbox`; Docker still controls its mounts, capabilities and network. The worker can read every credential supplied to it. This example has normal outbound network access.

## What is included

- A Debian/Node image with Codex CLI and GitHub CLI versions pinned in [`runtime/Dockerfile`](runtime/Dockerfile), plus a pinned multi-architecture Node 24/Bookworm base image. Debian apt packages are resolved during the build.
- Skills and custom agents from [tjpeel/skills](https://github.com/tjpeel/skills) and [tjpeel/agents](https://github.com/tjpeel/agents), pinned to source commits in the image.
- Model selection for each launch, with an optional profile default.
- Explicit account/repository profiles. Each pair gets separate workspace, Codex and T3 state volumes.
- Dedicated SSH commit signing, separate from HTTPS Git and GitHub API authentication.
- One ticket file per run; streamed JSON events and a saved final response.
- Signature and account checks before an explicit draft-PR command.
- An optional SSH service for Codex desktop or T3 Code.

The scripts have no scheduler, Jira integration, automatic merge, application deployment or retry policy. Repository language tools must be added to the image as needed.

## 1. Configure one account and test repository

Requirements on the host: Docker with Compose, Python 3 and Git. Use a disposable GitHub repository for the first connected test.

```sh
cp examples/profiles.json profiles.local.json
mkdir -p .secrets/personal .secrets/work
chmod 700 .secrets .secrets/personal .secrets/work
```

Edit `profiles.local.json`: replace the login, name, verified GitHub email and repository allowlist. Paths are relative to that configuration file. The launcher requires `--profile` and `--repo`, so folder-root inference and global `gh auth switch` cannot accidentally choose an account.

Provide **two dedicated automation credentials** per account:

1. An unencrypted OpenSSH private key at `.secrets/personal/signing-key`, registered on the matching GitHub account as a **Signing key**. It need not be a GitHub SSH authentication key. Use a dedicated key rather than your interactive 1Password key.
2. A fine-grained PAT at `.secrets/personal/github-token`, restricted to the test repository, with **Contents: read/write** and **Pull requests: read/write**. Check organisation approval requirements. This example supports github.com user tokens, not GitHub Enterprise or GitHub App actors.

```sh
ssh-keygen -t ed25519 -N '' -C 'sdlc automation signing' -f .secrets/personal/signing-key
chmod 600 .secrets/personal/signing-key .secrets/personal/github-token
```

Create the token file yourself; the spike does not read your existing `gh` login or 1Password vault. The GitHub UI can register `.secrets/personal/signing-key.pub`. Registering the public key and using a verified account email are needed for GitHub's verification display. [GitHub signing setup](https://docs.github.com/en/authentication/managing-commit-signature-verification/telling-git-about-your-signing-key), [PAT permissions](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens).

The launcher checks private-file permissions and passes their contents to Compose's environment-backed **secrets**, rather than the service environment or command arguments. Inside the container, the token is loaded into the tool environment and signing uses the private file with `SSH_AUTH_SOCK` cleared. Access to the Docker daemon and the worker still permits access to these secrets; Compose secrets are not a vault.

## 2. Build, authenticate Codex and clone

The build and Codex login commands do not supply GitHub credentials. Codex authentication is saved in a dedicated Docker volume for this profile/repository.

```sh
python3 scripts/sdlc.py build --profile personal --repo YOUR_PERSONAL_LOGIN/example-repo
python3 scripts/sdlc.py login --profile personal --repo YOUR_PERSONAL_LOGIN/example-repo
python3 scripts/sdlc.py init --profile personal --repo YOUR_PERSONAL_LOGIN/example-repo
```

The build runs each catalogue's Codex installer with the `tjpeel` prefix. Skills go in `/home/node/.agents/skills`; custom agents go in `/etc/codex/agents`. Their supporting files remain in `/opt/sdlc/catalogues`. These paths sit outside the mounted Codex state volume, so rebuilding also updates the catalogue for existing workspaces. The paths are supported by the pinned CLI's [skill discovery](https://github.com/openai/codex/blob/rust-v0.159.2/codex-rs/ext/skills/src/host_roots.rs) and [agent discovery](https://github.com/openai/codex/blob/rust-v0.159.2/codex-rs/agent-roles/src/loader.rs).

The default source commits are recorded in `runtime/Dockerfile`. To override either catalogue for one build, set `SDLC_SKILLS_REVISION` or `SDLC_AGENTS_REVISION` when running the launcher’s `build` command. Each value must be a full, lowercase 40-character commit SHA. The launcher passes overrides as Docker build arguments; Compose uses the Dockerfile defaults. Build downloads use the public repositories and need no GitHub credentials.

Login uses `codex login --device-auth`; your ChatGPT workspace must allow device-code login. After bootstrap, unattended jobs use the stored authentication, subject to token validity and usage limits. API-key authentication is another option, documented in [Codex authentication](https://learn.chatgpt.com/docs/auth). Do not mount your whole host `.codex` directory.

Choose a model when launching an interactive worker or ticket job:

```sh
python3 scripts/sdlc.py cli --profile personal --repo YOUR_PERSONAL_LOGIN/example-repo \
  --model YOUR_CODEX_MODEL

python3 scripts/sdlc.py exec --profile personal --repo YOUR_PERSONAL_LOGIN/example-repo \
  --model YOUR_CODEX_MODEL --branch spike/example-ticket --ticket examples/ticket.md
```

Replace `YOUR_CODEX_MODEL` with a model available to the container's Codex account. You can also add an optional `"model": "YOUR_CODEX_MODEL"` field to a profile in `profiles.local.json`. `--model` takes precedence; omitting both leaves Codex to choose from its own configuration. Changing the model reuses the same workspace and authentication volumes.

For `cli` and `exec`, the launcher passes the selected model explicitly to Codex. Each container also sets it as the machine default in `/etc/codex/config.toml`, including containers started with `ssh-up --model YOUR_CODEX_MODEL`. Persisted user configuration, trusted project configuration, and desktop or T3 session choices can override that machine default. See [Codex configuration precedence](https://learn.chatgpt.com/docs/config-file/config-basic). Custom agents retain the model and reasoning effort declared in their own definitions.

## 3. Run one supplied ticket

Edit a copy of `examples/ticket.md`, then inspect the command before running it:

```sh
python3 scripts/sdlc.py exec --profile personal --repo YOUR_PERSONAL_LOGIN/example-repo \
  --branch spike/example-ticket --ticket examples/ticket.md --dry-run

python3 scripts/sdlc.py exec --profile personal --repo YOUR_PERSONAL_LOGIN/example-repo \
  --branch spike/example-ticket --ticket examples/ticket.md
```

This requires a clean checkout, fetches the configured base, creates a new branch, and runs `codex exec` with the ticket on stdin. Branch names are explicit and are not prefixed with `codex/`. JSON events stream to the terminal. `/workspace/results/<UTC timestamp>/` contains `events.jsonl`, `stderr.log`, `summary.md` when Codex writes it, and `exit-code.txt`.

The prompt asks Codex to implement, test and create signed commits, leaving publication to the next step. **That is workflow guidance, not an enforced boundary:** the full-access worker already has a GitHub token and signing key. Exit code zero only establishes that the Codex process completed; inspect the result and checks before publishing.

Ticket text can invoke a bundled skill by name, for example `$tjpeel-engineering-implement` or `$tjpeel-pr-review`. Ask for an agent by its declared name, such as `read_low` or `write_medium`; the `tjpeel-` prefix applies to its installed filename. Skills that use external tools still need those tools configured in the container. See [skill invocation](https://learn.chatgpt.com/docs/build-skills) and [custom agents](https://learn.chatgpt.com/docs/agent-configuration/subagents).

Read the latest saved response after a container exits, without supplying GitHub credentials:

```sh
python3 scripts/sdlc.py results --profile personal --repo YOUR_PERSONAL_LOGIN/example-repo
```

For interactive work:

```sh
python3 scripts/sdlc.py cli --profile personal --repo YOUR_PERSONAL_LOGIN/example-repo
```

The launcher rejects overlapping commands for one workspace and refuses to start a job while its SSH container is running. Different profile/repository pairs have separate volumes. Direct Docker commands and multiple SSH clients can bypass that guard; use one writer per checkout.

## 4. Verify and publish a draft PR

```sh
python3 scripts/sdlc.py verify --profile personal --repo YOUR_PERSONAL_LOGIN/example-repo \
  --branch spike/example-ticket

python3 scripts/sdlc.py publish --profile personal --repo YOUR_PERSONAL_LOGIN/example-repo \
  --branch spike/example-ticket --title 'Implement example ticket' --body examples/pr-body.md
```

`publish` **prints only** until you add `--execute`. The print-only path does not read credentials, start containers, push, or call `gh pr create --dry-run` (that command can itself push). Replace the PR-body example with actual changes and validation first.

With `--execute`, the helper checks the token's user, repository URL, clean checkout, branch and every proposed commit's author, committer and signature against the selected key. It fetches the base, pushes an explicit branch ref without force, then creates a draft PR with explicit repository/base/head. These checks catch account or signing mistakes; the worker can alter files in its own container, so they do not provide an independent security gate.

A push can succeed before PR creation fails. Inspect GitHub before retrying; this spike does not deduplicate existing PRs or roll back published branches.

### Test the complete flow

The connected smoke test finishes the marker ticket in `examples/smoke-ticket.md` by checking its acceptance criteria, verifying its signed commit, pushing the branch and creating a draft PR. All checks, GitHub authentication, push, PR creation and remote verification happen in the container using the selected profile's credentials. The test does not use the host's `gh` login.

Rebuild the image to install the publication checks, then preview the test:

```sh
python3 scripts/sdlc.py build --profile personal --repo YOUR_PERSONAL_LOGIN/example-repo
python3 tests/github_smoke.py \
  --profile personal --repo YOUR_PERSONAL_LOGIN/example-repo \
  --branch spike/example-ticket \
  --title 'Test Codex execution in Docker' --body examples/pr-body.md
```

Replace the PR body template with the actual change and validation results. Add `--execute` to run the connected test and make the GitHub writes. Supply `--profiles /path/to/profiles.local.json` for a custom configuration file and `--model YOUR_CODEX_MODEL` to override the profile model. Without `--ticket`, the test reuses the signed branch already in the container and makes no model request. With `--ticket examples/smoke-ticket.md` and a fresh branch, it runs Codex first and automatically invokes the publish stage only after execution and local verification succeed.

The connected test enables `--smoke-checks` on the publisher. Before any push, the helper independently requires Docker, user 1000, exactly one proposed commit, only `docs/codex-docker-smoke.md` changed, and its exact marker contents including the newline. A successful Codex exit alone does not satisfy these checks. This fixture-specific test is not a general acceptance runner for arbitrary tickets.

`--verify-published` on the underlying `publish` command checks that the remote branch SHA matches the local commit and exactly one open draft PR has the expected head, base, commit and author. The connected test enables this check and prints the verified PR URL and commit hash. A failed execution or local verification stops publication. A failure during publication can leave a pushed branch or PR; inspect GitHub before retrying.

## 5. Try a desktop connection

Generate a **separate control key** on the host. The container receives only its public half:

```sh
ssh-keygen -t ed25519 -N '' -f .secrets/container-control
python3 scripts/sdlc.py ssh-up --profile personal --repo YOUR_PERSONAL_LOGIN/example-repo
```

Run `init` before `ssh-up`, then put an alias in your host SSH config using your absolute SDLC path:

```sshconfig
Host sdlc-personal
  HostName 127.0.0.1
  Port 2222
  User node
  IdentityFile /ABSOLUTE/PATH/TO/sdlc/.secrets/container-control
  IdentitiesOnly yes
  IdentityAgent none
  ForwardAgent no
```

Check the server host-key fingerprint through the Docker container before accepting the first SSH connection. Then test both login-shell and ordinary remote commands:

```sh
ssh sdlc-personal 'codex --version; gh --version; git -C /workspace/repo status --short'
ssh sdlc-personal 'bash -lc "test -n \"\$GH_TOKEN\" && sdlc-job init"'
```

Do not print the token. In **Codex desktop**, add the SSH host under Settings → Connections and choose `/workspace/repo`. In **T3 Code**, add an SSH environment and choose that folder. Both products document remote SSH hosts; treating this container as such a host is an integration proposal that still needs a connected smoke test. Select Full access in the client and confirm the resulting thread permissions.

Each client creates and owns its conversations. It does not automatically display a separately launched `codex exec` session. Avoid having desktop, T3 and CLI edit the same checkout at once.

```sh
python3 scripts/sdlc.py ssh-down --profile personal --repo YOUR_PERSONAL_LOGIN/example-repo
```

Stopping preserves named volumes, including code, Codex authentication, T3 state and SSH host keys. This example assigns one SSH port per profile, so run one SSH container per profile at a time. Containers and logs remain local to this Docker host. The Mac must stay awake; use an always-on host for jobs that must survive laptop sleep.

## Validation

Run the offline checks without credentials or Docker:

```sh
python3 -m unittest discover -s tests -v
```

The original image build and generated-credential Docker/SSH smoke test passed on this Mac. The catalogue and model additions pass the host checks; their updated Docker/SSH smoke test still needs Docker access. [The validation record](docs/validation.md) records the checks and remaining tests. To reproduce the container checks after building:

```sh
python3 tests/docker_smoke.py
```

See [the research and smoke-test checklist](docs/options.md) for what remains to establish before using real work tickets.

## Dependency updates

Once this configuration reaches GitHub's default branch, updates are checked weekly:

| Dependency | Update route |
| --- | --- |
| Node 24/Bookworm image digest | Dependabot Docker updates; Node major upgrades remain a deliberate choice |
| Workflow actions | Dependabot GitHub Actions updates |
| Codex CLI | The runtime updater reads the latest stable `@openai/codex` npm release |
| GitHub CLI | The runtime updater reads the latest stable `cli/cli` release |
| Skills and agents | The runtime updater reads each repository’s `main` commit |
| Debian tools installed with apt | Resolved during uncached builds; weekly validation builds the image without cache |

[`dependabot.yml`](.github/dependabot.yml) configures the native updates. [`Update runtime pins`](.github/workflows/update-runtime-pins.yml) handles the four Dockerfile arguments that Dependabot cannot parse. It opens one PR after offline tests, an uncached image build, and the Docker/SSH smoke test pass. While a runtime update PR is open, further runtime PRs wait for its review. Updates take effect in local containers after merging and rebuilding; the local image tag is `sdlc-codex-spike:local`.

[`Validate container`](.github/workflows/validate.yml) runs on pushes to `main`, pull requests, manual dispatch, and weekly. The weekly uncached build checks current Debian packages even when none of the tracked pins changes. Python uses only the standard library, so there is no Python dependency manifest to update.

The runtime updater uses GitHub's built-in token. Enable **Settings → Actions → General → Workflow permissions → Allow GitHub Actions to create and approve pull requests** for it to open PRs. The workflow grants only contents and pull-request write permissions, and does not approve or merge PRs. Dependabot version updates activate when its configuration is on the default branch. See [Dependabot setup](https://docs.github.com/en/code-security/how-tos/secure-your-supply-chain/secure-your-dependencies/configure-version-updates) and [GitHub Actions repository settings](https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/enabling-features-for-your-repository/managing-github-actions-settings-for-a-repository).

PRs created with `GITHUB_TOKEN` can require a user to approve their separate PR workflow runs. The updater therefore validates candidates before opening the PR. See [GitHub token event behavior](https://docs.github.com/en/actions/concepts/security/github_token). Run the updater locally with an authenticated `gh` CLI when needed:

```sh
python3 scripts/update_runtime_pins.py --check  # Validate local pins without network access.
python3 scripts/update_runtime_pins.py --write  # Fetch upstream metadata and update the Dockerfile.
```
