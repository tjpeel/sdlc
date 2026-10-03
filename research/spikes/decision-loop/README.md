# CLI decision-loop spike

> Earlier experiment. This is separate from the
> [current Go CLI](../../../docs/cli.md).

**SDLC DECISION LOOP SIMULATION.** This runnable spike demonstrates repository
admission and a worker stopping for a decision. It performs no provider calls,
Docker or VM operations, repository edits, checks, commits, pushes or pull
requests. Runtime and provider names describe an admitted policy.

It runs on Python 3 and Git on Linux or macOS, including a Linux guest on another
machine. It uses POSIX file locks; native Windows needs a Linux environment such
as WSL. The executable `sdlc` entry point requires no AI harness. An installer in
this directory can stage that command under a new external prefix; it does not
change an existing system installation.

To install the standalone simulation, choose a new absolute prefix outside any
repository, with an existing parent directory:

```sh
python3 -B research/spikes/decision-loop/install.py --prefix /PRIVATE/NEW/sdlc-spike
/PRIVATE/NEW/sdlc-spike/bin/sdlc --help
```

The installer copies two allowlisted public source files and creates a command
launcher and hash manifest. It refuses existing prefixes and symbolic-link paths.
Use that command as `spike` in the generated demo below to run independently of
this checkout. It installs no Docker runtime, provider, credentials or global
system command. The [options guide](../../notes/runner-options-and-feedback.md)
describes the proposed production CLI and runtime integration.

## Command contract

Every invocation supplies an absolute `--state-dir` outside all source
repositories. The parent directory must already exist. State directories belong
to the caller and have mode `0700`; files have mode `0600`. Paths through symbolic
links, Git metadata and the source checkout are rejected. Resolve system aliases
such as `/tmp` before supplying paths on macOS.

```text
sdlc --state-dir STATE repo add --id ID --path REPO_ROOT --remote OWNER/REPO \
  --runtime docker|colima|remote-vm|ubuntu-box \
  --implementer codex|claude --reviewer codex|claude --check LABEL [--check LABEL]
sdlc --state-dir STATE repo list
sdlc --state-dir STATE job start --repo ID --ticket .sdlc/work/tickets/NUMBER/ticket-X.md
sdlc --state-dir STATE job status JOB
sdlc --state-dir STATE job question JOB
sdlc --state-dir STATE job answer JOB --request REQUEST --checkpoint SHA256 \
  --choice keep-api|change-api
sdlc --state-dir STATE job continue JOB
sdlc --state-dir STATE job events JOB
```

`repo add` admits an explicit Git repository root, descriptive `OWNER/REPO`,
runtime, different implementation and review providers, and one or more check
labels. It reads the local Git root and committed HEAD using a restricted Git
environment. It does not discover other repositories, fetch, invoke hooks,
authenticate or inspect credential stores. Check labels never become shell
commands. Admission is stored outside the repository; it does not install files
into that repository.

`job start` snapshots the ticket and bounded local Markdown context using the
existing `runtime/bin/ticket_input.py` capture rules. It records local HEAD and an
immutable policy copy, then generates a fixed example question and enters
`NEEDS_INPUT`. No running worker is left waiting on stdin. `job answer` admits
one recorded enum choice against the current job, request ID and checkpoint
hash. It changes the status to `READY_TO_CONTINUE`; `job continue` explicitly
starts logical attempt two and records `SIMULATED_COMPLETE`.

Duplicate, replayed, stale or different-job answers are rejected. Continuation
requires an admitted answer and rejects changes to the stored input snapshot,
manifest, checkpoint, policy, question, admitted answer or local Git HEAD. Edits
to the original host ticket after snapshot admission have no effect. Other
source-worktree changes are outside this simulation: no implementation clone or
workspace is frozen, changed or restored.

Each response is one JSON object with `simulation: true`. Completed results also
state that checks and publication did not happen. Ticket contents and arbitrary
invalid input are not printed. Each job retains a private `events.jsonl` journal
which this CLI appends to, a ticket snapshot, checkpoint, question, answer and
result. These artifacts may contain private work; keep the state directory out
of source control.

## Generated local demo

Run this from the repository root. The example creates a disposable, unconnected
Git repository and generated ticket outside the checkout. It uses no real
credentials or work material.

```sh
umask 077
spike="$PWD/research/spikes/decision-loop/sdlc"
demo="$(python3 -c 'from pathlib import Path; import tempfile; print(Path(tempfile.mkdtemp(prefix="sdlc-decision-demo-")).resolve())')"
mkdir "$demo/repo"
mkdir -p "$demo/repo/.sdlc/work/tickets/1"
printf 'example\n' > "$demo/repo/example.txt"
printf '.sdlc/work/\n' > "$demo/repo/.gitignore"
printf '# Generated example\n\nChoose the API behaviour. [Context](context.md)\n' \
  > "$demo/repo/.sdlc/work/tickets/1/ticket-1.md"
printf 'Generated context only.\n' > "$demo/repo/.sdlc/work/tickets/1/context.md"
env -i PATH="$PATH" GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null git -c core.hooksPath=/dev/null -C "$demo/repo" init -q
env -i PATH="$PATH" GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null git -c core.hooksPath=/dev/null -C "$demo/repo" add example.txt .gitignore
env -i PATH="$PATH" GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null git -c core.hooksPath=/dev/null -C "$demo/repo" \
  -c user.name=Example -c user.email=example@example.invalid -c commit.gpgsign=false \
  commit -qm 'Generated example'

"$spike" --state-dir "$demo/state" repo add --id demo --path "$demo/repo" \
  --remote example/demo --runtime colima --implementer codex --reviewer claude --check unit
job="$("$spike" --state-dir "$demo/state" job start --repo demo \
  --ticket .sdlc/work/tickets/1/ticket-1.md | python3 -c 'import json,sys; print(json.load(sys.stdin)["job_id"])')"
"$spike" --state-dir "$demo/state" job status "$job"
"$spike" --state-dir "$demo/state" job question "$job" > "$demo/question.json"
cat "$demo/question.json"
request="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["request_id"])' "$demo/question.json")"
checkpoint="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["checkpoint"])' "$demo/question.json")"

# Deliberately supply one recorded choice, then start continuation separately.
"$spike" --state-dir "$demo/state" job answer "$job" \
  --request "$request" --checkpoint "$checkpoint" --choice keep-api
"$spike" --state-dir "$demo/state" job continue "$job"
"$spike" --state-dir "$demo/state" job events "$job"
```

The expected states are `NEEDS_INPUT`, `READY_TO_CONTINUE` and
`SIMULATED_COMPLETE`. Repeat the answer or continuation to see rejection. Start
a new job and change its stored snapshot to see the integrity guard reject an
answer. The original generated ticket can instead be edited after admission
without altering the admitted snapshot. Remove only your generated demo
directory after inspection.

## Boundary and recovery limits

Admission validation does not prove a human supplied an answer. A process with
the same OS identity can invoke this CLI or edit the state and its checksum
anchors. `submitted_by: local-cli-account` is descriptive. Separate manager
ownership or an authenticated manager service remains necessary to prevent an
initiating harness from submitting guidance. The JSONL journal is append-only
through this program, not protected from its account owner.

A whole-command advisory lock serialises answers and continuations. Individual
JSON files are written atomically, but this spike does not recover interrupted
multi-file transitions. A crash between writing an answer or result and updating
job status can require starting a new simulated job. It retains private artifacts
for inspection and performs no publication that could be duplicated.

Production integration still needs a retained worker workspace, provider event
capture and optional native session resume, fresh review after continuation,
decision authentication, durable transition recovery, credential lifecycle and
publication reconciliation. A decision response should remain data admitted by
the manager, rather than a new shell command or direct terminal injection.

## Validation

```sh
python3 -B -m unittest discover -s research/spikes/decision-loop -p 'test_*.py' -v
```

The tests generate local repositories and cover lifecycle, replay and stale
admission, snapshot/checkpoint/question/answer mutation, changed HEAD, external
state permissions, path/link rejection, safe output, ambient Git redirection and
concurrent duplicate admission and continuation.

All 27 lifecycle and installation tests passed. An independent installed-package
demo also completed the simulated lifecycle with provider roles reversed.
