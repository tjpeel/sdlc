# SDLC container spike

Run Codex inside a container with an explicit GitHub account, SSH commit signing, and a separate command to publish a draft PR. This is an initial runtime experiment for an unattended development workflow, researched on 1 October 2026.

**Start with the disposable CLI worker.** It exercises the account, signing and ticket workflow before adding a desktop connection or a ticket queue. [The options note](docs/options.md) compares the alternatives and links the research.

This is a public repository. Examples contain placeholders; actual account settings and credentials belong in ignored local files or an external secret store. Keep real work tickets and job output outside the tracked source tree. The runtime stores authentication and results in Docker volumes.

“Full access” means Codex's approval and sandbox settings. It is independent of model choice. Here Codex runs as the `node` user with `--dangerously-bypass-approvals-and-sandbox`; Docker still controls its mounts, capabilities and network. The worker can read every credential supplied to it. This example has normal outbound network access.

## What is included

- A Debian/Node image with Codex CLI **0.159.2** and GitHub CLI **2.81.0**, pinned for repeatable CLI behaviour. Base images and apt packages are not pinned by digest.
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

Login uses `codex login --device-auth`; your ChatGPT workspace must allow device-code login. After bootstrap, unattended jobs use the stored authentication, subject to token validity and usage limits. API-key authentication is another option, documented in [Codex authentication](https://learn.chatgpt.com/docs/auth). Do not mount your whole host `.codex` directory.

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

The image build and generated-credential Docker/SSH smoke test passed on this Mac. [The validation record](docs/validation.md) separates those checks from the remaining real-account and desktop tests. To reproduce the container checks after building:

```sh
python3 tests/docker_smoke.py
```

See [the research and smoke-test checklist](docs/options.md) for what remains to establish before using real work tickets.
