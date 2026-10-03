# Runtime and credential options

> Research archive. See the [current CLI](../../docs/cli.md) and
> [agreed workflow](../../docs/workflow.md) for current status.

Research snapshot: **1 October 2026**. The repository starts with a CLI experiment; the desktop routes are proposals to test against the same image.

This is an early options snapshot. The later requirement is that the worker
creates its own signed commits and pushes them unchanged. Helper-created
publication below is historical research, and Sandcastle remains inspiration
with no adoption planned. Use [the protected runner handoff](protected-runner-handoff.md)
for the current candidate and active plan.

## Proposed order

| Option | Control and visibility | Credentials | Fit |
| --- | --- | --- | --- |
| **A. Disposable CLI worker** | `docker compose run`, `codex exec --json`, persisted results and Codex state | One selected account's token and signing key | Start here; smallest route to one ticket → signed branch → draft PR |
| **B. Persistent SSH container** | Codex desktop or T3 Code remote environment; UI commands, changes and conversations | Same selected profile; auth state persists | Add when interactive supervision is useful |
| **C. Remote Codex app-server** | Host Codex CLI connects to a container's authenticated WebSocket server | Same runtime profile | CLI-native control; experimental transport adds integration work |
| **D. Worker plus signing/publishing helper** | Ticket runner records implementation, validation and publication separately | Worker gets no publishing token or signing key | Stronger unattended design after the workflow is proven |

### A. Disposable CLI worker

The launcher passes one reviewed ticket and an account/repository profile to a container. Codex runs inside it:

```sh
codex exec --dangerously-bypass-approvals-and-sandbox --json \
  --output-last-message /workspace/results/job/summary.md \
  -C /workspace/repo - < /input/ticket.md
```

The CLI supports noninteractive execution, streamed JSON events and saved final responses. Keep `CODEX_HOME` in a dedicated volume if you want stored authentication and session continuation; do not use `--ephemeral` for that workflow. `codex exec resume THREAD_ID 'Address the remaining findings'` is a follow-up mechanism, not a job queue. [Noninteractive mode](https://learn.chatgpt.com/docs/non-interactive-mode), [CLI commands](https://learn.chatgpt.com/docs/developer-commands?surface=cli).

Full access disables Codex's inner approval/sandbox mechanism. It does not require `docker --privileged`, the host Docker socket, or your home directory. This scaffold mounts only named worker-state volumes and individual supplied input files. The worker is unprivileged after a root bootstrap that prepares volumes and optional SSH. It has outbound network access, so credentials remain exposed to repository code and the agent. [Codex sandboxing](https://learn.chatgpt.com/docs/sandboxing).

### B. SSH host for Codex desktop and T3 Code

Codex desktop documents SSH remote connections: it runs Codex on the remote host while the app stays local. T3 Code similarly starts or reuses a remote T3 server through SSH and runs Git/provider tools there. A Linux container with `sshd`, Codex, Git, `gh` and persistent state fits that host shape. **Container support is an inference from these host workflows, not a documented Docker attach feature.** [Codex remote connections](https://learn.chatgpt.com/docs/remote-connections), [T3 remote access](https://github.com/pingdotgg/t3code/blob/main/docs/user/remote-access.md).

T3 launches its own Codex app-server and can select a provider-specific `CODEX_HOME`. Its source-control integration requires `gh` 2.81.0 or later, which is why the example downloads a pinned GitHub release instead of Debian's older package. Git's `commit.gpgsign` should cover T3's ordinary commit path; test a UI-created commit and GitHub's verification display. [T3 providers](https://github.com/pingdotgg/t3code/blob/main/docs/user/providers-codex.md), [T3 source control](https://github.com/pingdotgg/t3code/blob/main/docs/user/source-control.md), [permission modes](https://github.com/pingdotgg/t3code/blob/main/docs/user/permission-modes.md).

Neither UI should be assumed to attach to a conversation launched independently with `docker exec codex` or `codex exec`. Choose a controlling client for each job. A UI connection is for supervision; terminal JSON logs remain the durable evidence for CLI jobs.

The SSH overlay publishes **127.0.0.1 only**, uses a public control key, disables passwords and agent forwarding, and persists host keys. It enables local TCP forwarding for remote-client tunnels. Keep the control key distinct from the Git signing key. Direct remote commands need the account environment too; the entrypoint prepares both login-shell and Bash SSH-command environments.

### C. Host CLI to container app-server

The installed CLI exposes authenticated remote app-server transport. An illustrative alternative, not wired into the launcher:

```sh
# Inside the container, with a high-entropy capability token file:
codex app-server --listen ws://0.0.0.0:4500 \
  --ws-auth capability-token --ws-token-file /run/secrets/app-server-token

# On the host, with CODEX_REMOTE_TOKEN set securely:
codex --remote ws://127.0.0.1:4500 --remote-auth-token-env CODEX_REMOTE_TOKEN
```

Publish the Docker port only on `127.0.0.1`, pin client and server together, and protect transport with SSH/TLS for a different machine. The WebSocket transport is experimental. Test approvals, events, reconnection and in-flight jobs on disconnect; persistence alone does not guarantee an interrupted job resumes. This does not establish that the desktop app or T3 accepts an arbitrary app-server URL. [App-server documentation](https://learn.chatgpt.com/docs/app-server), [CLI developer commands](https://learn.chatgpt.com/docs/developer-commands?surface=cli).

### D. Separate implementation from signing and publishing

For valuable work repositories, this is the next design to explore:

```text
Ticket + explicit account/repository policy
  → worker with read access, isolated checkout, implementation and tests
  → immutable candidate changes + recorded validation
  → trusted helper with a fresh clone, fixed Git/sign/push/PR operations
  → draft PR + result record
```

The helper should not import the worker's `.git`, Git configuration, hooks or executable commands. It should check the repository/base/branch, apply reviewed changes in a fresh checkout and avoid running project code with credentials present. Disabling hooks alone is insufficient. The helper can make the signed commit itself, or sign the worker's exact commit object with an explicitly designed protocol. The former is simpler but changes who creates the final commit.

This is **not implemented** here. In options A–C, the worker can push or use the API even though the prompt asks it to leave publication to the helper command. Giving it a read-only token while implementing and a write token only for a later container reduces time of exposure, but shared writable state still needs validation.

## GitHub accounts, signing and 1Password

Treat three identities separately: Git author/committer, commit signing key, and GitHub API/transport actor. An SSH signing key does not authenticate `gh`. HTTPS transport uses the selected token; `GH_TOKEN` overrides `gh`'s saved active account. The container has a fresh `GH_CONFIG_DIR`, explicit `--repo`, no inherited host credentials and no global account switching. [gh environment](https://cli.github.com/manual/gh_help_environment), [SSH signing](https://git-scm.com/docs/git-config).

For v1, use a repository-scoped user PAT plus a dedicated signing key registered to that user. Fine-grained tokens have organisation approval and account/repository limitations; confirm availability for your work organisation. Contents write covers branch publication, while Pull requests write covers draft PR creation. Avoid labels, projects and assignees until their permission requirements are intentional. [Token management](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens), [PR API permissions](https://docs.github.com/en/rest/pulls/pulls#create-a-pull-request), [Git reference permissions](https://docs.github.com/en/rest/git/refs#create-a-reference).

A later ticket runner can mint GitHub App installation tokens, which expire after one hour and have selected repository permissions. Keep the App private key in the launcher/helper. The API actor becomes the App; commit author and signing identity require a separate decision. The supplied publisher deliberately expects a user token and checks `gh api user`; it is not App-token compatible. [Installation-token authentication](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/authenticating-as-a-github-app-installation).

Forwarding the 1Password SSH agent into Docker retains its approval/lock dependency. If you want 1Password as the store without a desktop unlock, use a service account with access to a dedicated automation vault, and let a trusted host launcher read only the selected profile's items. Service accounts cannot access several built-in vaults, including Personal/Private/Employee. The worker should never receive the service-account token. [Service accounts](https://www.1password.dev/service-accounts/get-started), [CLI service-account usage](https://www.1password.dev/service-accounts/use-with-1password-cli).

Illustrative host-side provisioning after you configure service-account authentication:

```sh
op read --out-file .secrets/work/signing-key \
  'op://YOUR_AUTOMATION_VAULT/YOUR_SIGNING_ITEM/private key?ssh-format=openssh'
op read --out-file .secrets/work/github-token \
  'op://YOUR_AUTOMATION_VAULT/YOUR_GITHUB_TOKEN_ITEM/token'
chmod 600 .secrets/work/signing-key .secrets/work/github-token
```

Use an unencrypted dedicated automation key for this example; encrypted keys fail immediately rather than prompt unattended. `ssh-keygen` may query an agent even when given a private file, so the signing wrapper explicitly removes `SSH_AUTH_SOCK`. Deleting a supplied key later does not revoke a copy; revocation means removing its trust at GitHub and rotating exposed tokens. [1Password read command](https://www.1password.dev/cli/reference/commands/read), [OpenSSH signing implementation](https://github.com/openssh/openssh-portable/blob/master/ssh-keygen.c).

## What to take from Sandcastle

[Sandcastle](https://github.com/mattpocock/sandcastle) supplies provider abstraction, Docker lifecycle, hooks, branches and event streams. Its Codex provider uses noninteractive JSON/full-access execution, which supports the CLI-first direction. Authentication, signing and account policy still need your own design. Its writable checkout mounts also require care with concurrent runs. [Codex provider](https://github.com/mattpocock/sandcastle/blob/main/src/AgentProvider.ts), [Docker sandbox](https://github.com/mattpocock/sandcastle/blob/main/src/sandboxes/docker.ts).

Borrow the job lifecycle and durable events first. A small launcher plus Compose is enough to test these decisions; adopting the orchestration library can wait until there are several providers or many jobs to manage.

## Next experiments and ticket runner

Before a real work-ticket batch:

1. Build the image; authenticate Codex into its own volume; complete one synthetic ticket in a test repository.
2. Verify every new commit locally and confirm **Verified** on GitHub after an explicitly executed draft-PR publication. Repeat with a second account/repository profile.
3. Connect Codex desktop and T3 separately through SSH. Confirm full-access permissions, a UI-created signed commit, API account, event visibility and behaviour on client disconnect.
4. Restart containers, test expired credentials and an interrupted job, and recover without publishing twice. Test repository build/test tooling in the image.
5. Choose direct worker credentials or the independent helper before granting unattended access to work repositories.

Then add a small **serial** ticket manifest: ticket ID/source, selected profile, allowed repository, base, branch, acceptance criteria, limits and result paths. Model the durable states as queued → implementing → validating → ready to publish → draft PR → complete/needs attention. Ticket text supplies the task; it must not choose the credential profile or expand the repository allowlist.

Add a per-repository lease, failure budget, resumable job IDs, cancellation, duplicate-PR handling and meaningful notifications before parallel queues. Keep draft PRs as the initial unattended endpoint. Mac-hosted Docker cannot keep running through host sleep; an always-on Linux host makes the same SSH/client arrangement more useful for unattended work.
