# Onboard the Docker worker

> Research archive. See the [current CLI](../../docs/cli.md) and
> [agreed workflow](../../docs/workflow.md) for current status.

Use this sequence for a new account/repository pair or to refresh provider login.
The worker image includes Codex, Claude Code, GitHub CLI, .NET 10 and Docker
tools. The host needs Docker with Compose, Python 3 and Git; application SDKs
run inside the container.

## 1. Keep configuration outside repositories

In a Bash or Zsh terminal, set the checkout and private configuration paths:

```sh
SDLC_ROOT=/ABSOLUTE/PATH/TO/sdlc
SDLC_PRIVATE="$HOME/.config/sdlc"
umask 077
mkdir -p "$SDLC_PRIVATE/.secrets/work"
test -e "$SDLC_PRIVATE/profiles.local.json" || \
  cp "$SDLC_ROOT/research/container-spike/examples/profiles.json" "$SDLC_PRIVATE/profiles.local.json"
chmod 600 "$SDLC_PRIVATE/profiles.local.json"
```

Copy the example once. On a refresh, edit the existing private file instead of
overwriting it. Replace the selected profile's login, Git name, verified GitHub
email and repository allowlist. Secret paths are relative to that file. Leave
`base_branch` as the repository's actual default branch. The SSH-control fields
are only needed for the separate desktop/SSH route.

For a complete trial, add `implementation`, `review`, `checks` and `cleanup`
using [the ticket job guide](docker-ticket-jobs.md). The profile-level `model`
is the older Codex default. Unattended jobs use the optional model inside each
provider role.

Define the selected profile and repository, then reuse them throughout:

```sh
SDLC_PROFILE=work
SDLC_REPO=YOUR_ORG/example-repo
sdlc() {
  python3 "$SDLC_ROOT/research/container-spike/sdlc.py" "$@" \
    --profiles "$SDLC_PRIVATE/profiles.local.json" \
    --profile "$SDLC_PROFILE" --repo "$SDLC_REPO"
}
```

## 2. Prepare GitHub and signing access

The connected worker needs a dedicated **unencrypted SSH signing key** and a
GitHub token restricted to the selected repository. Create a key only when the
configured path does not already exist:

```sh
ssh-keygen -t ed25519 -N '' -C 'container automation signing' \
  -f "$SDLC_PRIVATE/.secrets/work/signing-key"
chmod 600 "$SDLC_PRIVATE/.secrets/work/signing-key"
```

Register its `.pub` file on the selected GitHub account as a **Signing key**.
Use an email verified on that account in the profile. Git transport uses HTTPS;
the signing key does not need SSH authentication access.

Create a fine-grained GitHub token with **Contents: read/write** and **Pull
requests: read/write** for this repository. Complete any organisation approval
or SSO requirements. Save the token using your editor or secret store at the
configured `github_token_file`, then set mode `0600`. Do not put token values in
commands, tickets, screenshots or chat. Build and provider-login commands work
before these files exist; repository execution needs both.

This remains a compatibility spike: the worker can read the supplied
credentials, and `--docker-tests` starts a privileged per-job daemon. Use
dedicated trial credentials. The protected-service work is separate.

## 3. Build and authenticate the providers

Start Docker, then check the host tools and build:

```sh
docker info >/dev/null
docker compose version
python3 --version
git --version
sdlc build
```

The build uses only the runtime's allowlisted source context. It installs
pinned CLIs and public skill/agent catalogues; it does not mount the target
checkout or copy host authentication.

Authenticate the provider you want to use. Authenticate both for review by a
different provider:

```sh
sdlc login --provider codex
sdlc auth-status --provider codex
sdlc login --provider claude
sdlc auth-status --provider claude
```

Codex prints a device-login URL and code. Enable device login for the chosen
ChatGPT account/workspace, then complete it in the host browser. See
[official OpenAI authentication guidance](https://learn.chatgpt.com/docs/auth).

Claude prints a browser login URL. Open it on the host; if the browser returns
a code, paste it into the waiting container terminal. Use the intended Claude
subscription/team account. See [Claude container login
guidance](https://code.claude.com/docs/en/authentication).

`auth-status` starts no model session and receives no GitHub token or signing
key. Exit code zero means the CLI reports stored authentication; it does not
prove remaining usage or access to a particular model. Provider login state
persists in separate Docker volumes for the profile/repository. Re-run login
to refresh expired authentication. Keep the same profile name, repository,
GitHub login and Git email when reusing those volumes. Do not use `down -v` to
refresh login; that would remove stored state.

## 4. Open either interactive CLI

These commands initialise a separate persistent checkout in Docker and open
the selected CLI there:

```sh
sdlc cli --provider codex
sdlc cli --provider claude
```

Both start a fresh conversation with their container full-access flags. An
interactive CLI can show its available skills and agents; it can also spend
provider usage when you send a prompt. The catalogues are installed at:

| Catalogue | Container path |
| --- | --- |
| Codex skills | `/home/node/.agents/skills` |
| Codex agents | `/etc/codex/agents` |
| Claude skills | `/home/node/.claude/skills` |
| Claude agents | `/home/node/.claude/agents` |

For an explicit interactive model, append `--model YOUR_PROVIDER_MODEL`.
Claude ignores the legacy profile-level Codex model. Interactive work retains
its current branch and edits; opening it again does not reset the checkout.
`init`/`cli` initialise Git but do not establish that the token belongs to the
profile account; unattended `run` checks that account before model work.

This interactive route does not start the nested Docker daemon or orchestrate
checks, review and publication. Use `run --docker-tests` for the complete
integration-test ticket flow. The older `exec` action remains Codex-only.

## 5. Run the guided ticket trial

Put one authorised ticket in the target checkout at
`.sdlc/work/tickets/<number>/ticket-X.md`. Keep local work inputs untracked.
The launcher captures that ticket and linked work Markdown, then starts from
the configured remote base in a fresh Docker volume. It does not bring local
source edits or the interactive workspace into the job.

First inspect the launch:

```sh
sdlc run --repo-root /ABSOLUTE/PATH/TO/TARGET \
  --ticket .sdlc/work/tickets/123/ticket-1.md \
  --branch 123-short-description \
  --implementer codex --reviewer claude --docker-tests
```

This is print-only. With a complete profile and ticket, append `--execute` to
authorise implementation, signed push and one draft PR. Reverse the provider
roles with `--implementer claude --reviewer codex`. Never run a completed ticket
again merely to test the opposite role order; use another authorised change
and a new branch.

Save the printed job ID and inspection command. After the run:

```sh
sdlc results --job-id JOB_ID
```

Pass means configured checks succeeded, review passed without findings, the
reviewed signed commits were pushed unchanged, and the saved result identifies
one draft PR. Check GitHub's author, base, diff and Verified signature display.
Application CI can include additional checks beyond the local profile.

On failure, inspect private stage logs and the remote branch/PR before retrying.
Workspace/results remain available; the job's nested daemon and its data/socket
volumes are removed on ordinary completion or handled cancellation. Follow
[the ticket guide](docker-ticket-jobs.md#results-and-verification) for limits
and cleanup details.
