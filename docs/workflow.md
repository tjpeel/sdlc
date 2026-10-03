# Ticket workflow

The confirmed direction is an installed Go CLI coordinating work from a local
Git repository through one shared SDLC Docker image. macOS and Linux are the
primary hosts; Windows should remain possible with a Linux-container engine.

The [CLI setup commands, provider login, instruction settings, project initialization and interactive sessions](cli.md)
are implemented today. Interactive sessions currently use an empty disposable
workspace. Ticket execution, secret retrieval, update prompts and recovery remain
to be built.

## Set up the installation

Clone the SDLC repository, install the CLI on PATH and build the shared image
locally. Every project uses that image. Project names, source, tickets and
credentials do not become image build inputs.

Authenticate Codex and Claude with `sdlc auth login` and
`sdlc auth login --provider claude`. Their login state lives in installation-wide
storage, separate from disposable work containers. Claude manages its own native
cache, following the [provider usage rules](provider-usage.md). The image contains pinned
skills and agents from the public GitHub catalogues. Startup should report
available updates to both provider CLIs and the skills/agents, and prompt before
updating the shared runtime.

Codex is the default provider when none is specified. Use `--provider claude` to
select Claude. `sdlc auth status` checks Codex; `sdlc auth status --all` checks both
providers.

Configure common agent instructions with `sdlc instructions set --file FILE`,
inspect them with `sdlc instructions show`, or remove custom additions with
`sdlc instructions reset`. The default shared body contains only the rule that
implementation stops whenever an agent has a question for a human, until a human
answers. Additional instructions retain that rule. These settings are private
installation state and do not require an image rebuild.

## Start work from a project

Run the CLI in the repository containing the work. It should infer the Git root,
remote, current source state and local engineering inputs, then show the selected
work and execution settings before starting.

`sdlc init` now discovers local Git state, project manifests and ticket paths,
protects `.sdlc/work/` with Git's local exclude file, and saves portable check and
input settings in `.sdlc/project.json`. Existing settings are preserved on repeat
runs. It does not select tickets, capture source or start a worker; those steps
belong to the future work command.

The ticket layout comes from the public
[engineering skills on GitHub](https://github.com/tjpeel/skills/tree/56e38979baf389b058ae91c6812abae8d8dbcafa/engineering),
reviewed at published commit `56e38979baf389b058ae91c6812abae8d8dbcafa`:

```text
.sdlc/work/<reference>/
  decisions.md
  specification.md
  tickets/
    01-ticket-title.md
    02-ticket-title.md
```

Treat `<reference>` as an opaque work identifier. Ticket eligibility and order
follow the engineering process, including `ready-for-agent` and explicit
lower-numbered dependencies. The GitHub process is the authority; locally
installed skills do not define the ticket format. Ticket selection and execution
are still to be implemented.

At launch, the CLI will obtain the signing key and GitHub token from an authorised
secret store. The first integration is 1Password scoped to a named vault. Secret
references belong in private configuration. Open-source alternatives remain part
of the [detailed design research](../research/notes/cli-workflow-design.md).

Create a fresh checkout in disposable job storage. Carry over the selected local
source state, ticket inputs and required project configuration, including approved
environment files for integration tests. Keep these inputs out of the image and
public source. The exact capture and secret-handling rules remain implementation
work.

## Implement and review a ticket stream

A selected stream uses two provider roles: an implementer and an independent
reviewer. Codex is the initial implementer default; Claude is the initial reviewer
default. Either role can select another supported provider, but the two providers
must differ. Keep the original implementer responsible for the stream and its
repairs. If the selected reviewer is unavailable, stop rather than falling back
to the implementation provider. These roles and stream execution remain to be
implemented; current interactive sessions do not coordinate tickets or PRs.

For the requested linear stream, create the first ticket branch from the agreed
`main` revision. Create each subsequent ticket branch from its predecessor's
verified delivered revision, with one branch and draft PR per ticket:

```text
main
  ticket-one    PR base: main
    ticket-two  PR base: ticket-one
      ticket-three  PR base: ticket-two
```

The SDLC repository itself continues to be developed on `main`. These ticket
branches belong to the repository in which work is requested. A stream's delivery
order does not rewrite ticket blockers or treat an unresolved dependency as
complete. Each launch names one ticket, its exact selected inputs, starting SHA,
destination branch, review boundary and PR base. Keep branch state in the launch
context, not in maintained ticket or specification files. The published
[implementation contract](https://github.com/tjpeel/skills/blob/56e38979baf389b058ae91c6812abae8d8dbcafa/engineering/implement/SKILL.md)
already defines prepared launches, predecessor revisions and stale descendants.

The skill roles are:

| Published skill | Responsibility |
| --- | --- |
| [`engineering-implement`](https://github.com/tjpeel/skills/blob/56e38979baf389b058ae91c6812abae8d8dbcafa/engineering/implement/SKILL.md) | Implement one selected ticket in verified, committed increments. |
| [`engineering-testing`](https://github.com/tjpeel/skills/blob/56e38979baf389b058ae91c6812abae8d8dbcafa/engineering/testing/SKILL.md) | Choose checks for required behaviour and credible coverage gaps. |
| [`engineering-verification`](https://github.com/tjpeel/skills/blob/56e38979baf389b058ae91c6812abae8d8dbcafa/engineering/verification/SKILL.md) | Bind completion evidence to the actual checked revision and outcomes. |
| [`engineering-code-review`](https://github.com/tjpeel/skills/blob/56e38979baf389b058ae91c6812abae8d8dbcafa/engineering/code-review/SKILL.md) | Review the complete local ticket change against technical behaviour and selected requirements. |
| [`pr-draft`](https://github.com/tjpeel/skills/blob/56e38979baf389b058ae91c6812abae8d8dbcafa/pr/draft/SKILL.md) | Draft each PR's description, distinguishing its slice from inherited work. |
| [`pr-manage`](https://github.com/tjpeel/skills/blob/56e38979baf389b058ae91c6812abae8d8dbcafa/pr/manage/SKILL.md) | Publish authorised branches and draft PRs with the recorded predecessor bases. |
| [`pr-monitor`](https://github.com/tjpeel/skills/blob/56e38979baf389b058ae91c6812abae8d8dbcafa/pr/monitor/SKILL.md) | Wait for the required checks on current published revisions and return supported CI repairs to the implementation owner. |
| [`pr-review`](https://github.com/tjpeel/skills/blob/56e38979baf389b058ae91c6812abae8d8dbcafa/pr/review/SKILL.md) | Review published PRs against fixed base and head revisions. |

These are source skill names; use their installed names from the image's
catalogue. The image pins published skills commit
`56e38979baf389b058ae91c6812abae8d8dbcafa` and agents commit
`347f58e598515c51c42fab4a7f699380db725fd3`. A local rebuild installs them.
The earlier decision, specification and ticket-creation skills supply approved
inputs; a work run consumes those inputs rather than regenerating them.

The immediate step is a [ticket-stream prompt](prompts/implement-ticket-stream.md)
that coordinates the existing skills, with one implementation invocation per
ticket. It does not require a new skill or changes to the skills repository.
Propose any incompatible skill requirement before deciding whether to change
or defer it. The current CLI does not yet prepare a project checkout or submit
this prompt; it is a template for a harness with the selected repository and
inputs available.

All process inputs and private run outputs use the initiating repository's
`.sdlc/` root. The skills catalogue keeps its existing structure. A future single
`sdlc` skill would let an outer AI harness use the CLI; the CLI itself will not
invoke that skill. Its design is deferred.

Implementation already applies testing and verification guidance and invokes a
local code review at the end of each ticket. Preserve that per-ticket check.
The different-provider review requested here is an additional whole-stream gate
after publication and CI, coordinated by SDLC:

1. Implement and verify each selected ticket on its own branch. Publish its draft
   PR, then launch the next ticket from the verified predecessor.
2. Wait until every stream PR is published and its configured required CI checks
   have passed for the recorded current revisions. Missing, pending, failed,
   cancelled or unexpectedly skipped checks do not satisfy the gate.
3. Start a fresh session with the selected review provider. Supply approved
   tickets and specifications, immutable source and diffs, PR base/head SHAs and
   check evidence. Review each incremental PR and the complete stack against the
   stream's recorded `main` starting point, including interactions across tickets.
4. Record actionable findings against the reviewed revisions and return them to
   the original implementer. An incomplete or interrupted review is not approval.
   A finding that needs a new product decision stops for a human answer.
5. Apply repairs within the affected tickets. Carry an earlier ticket's repair
   through all affected descendants, publish the updated branches and invalidate
   their old CI and review evidence. Preserve original ticket boundaries and
   record their mapping to the updated bases.
6. Repeat the CI gate and fresh independent review for the updated stack. Finish
   as ready for human review and merge only when required checks pass and the
   independent review completes without actionable findings.

CI evidence and reviews belong to exact content, not just PR numbers. A changed
head, base or relevant input invalidates affected evidence. Reconcile remote
state before resume and before declaring the stack ready. Questions, unexpected
external changes, policy refusals and exhausted usage stop the whole stream.
Do not automatically merge PRs.

Keep publication credentials and signing with the controller where possible.
The reviewer receives only its own provider login and selected review inputs,
with no implementer transcript, publication credentials or implementation secrets.
It cannot publish or change the implementation branches; any verification scratch
changes are disposable and must not be mistaken for the reviewed revision.
Store the stream journal, findings and source checkpoints outside disposable
container storage, alongside the private run outputs. Verify the supported
unattended account route under the [provider rules](provider-usage.md) before
connecting either role; current interactive login does not establish that route.

The published [pr-manage reference rules](https://github.com/tjpeel/skills/blob/56e38979baf389b058ae91c6812abae8d8dbcafa/pr/manage/SKILL.md#L61-L121)
now accept a confirmed ticket key or the exact selected work reference. Every
PR carries that identifier in its title and body; branch names can use a
separate Git-safe rendering. Preserve both identifiers in the body when a ticket
key and associated work reference are supplied. The [isolated proposal](proposals/pr-manage-work-reference.md)
records the change that was adopted upstream. SDLC uses the published skill.
Rebase and force-push restacking also need explicit authority. Publication authority
alone does not authorise rewriting someone else's work.

## Execute and report progress

Each job gets a disposable worker and its own Docker test daemon. Nested test
containers, daemon data and job storage should be ephemeral. The worker implements
tickets, runs the repository's checks and uses the engineering review process.

The initiating repository retains progress and diagnostic logs at a path tied to
the requested ticket:

```text
.sdlc/work/<reference>/runs/<ticket-stem>/<job-id>/attempts/<attempt-id>/
```

This run-log layout is the proposed SDLC convention; it is not defined by the
upstream engineering skills. Bind the selected run directory into the worker so
the CLI can tail files on the host. Logs then survive container removal and a
disconnected terminal. Keep them private and ignored.

At job launch, capture the shared instructions in a read-only per-run snapshot and
make the same body available to Codex and Claude through their native global
instruction files. Preserve the checkout's own instruction files. The SDLC
repository's development instructions are not a worker default. Interactive
sessions already receive this snapshot; ticket-worker injection remains to be
implemented with the work launcher.

## Stop and continue

Unattended work should either complete with recorded checks and results or stop
with an explicit problem and the information needed to intervene. The CLI should
show the failure, accept the required decision and continue the same logical job.

Any agent question for a human must stop implementation and put the job into a
waiting-for-human state with the question recorded in its progress log. Do not
guess an answer, continue to another ticket or restart automatically. Resume only
after a human has answered the pending question. Enforce this in the runner's
status handling as well as the shared instructions; a Markdown rule alone is not
a process control. Question capture and answer handling remain implementation
work.

Logs alone cannot restore uncommitted work or a provider session. A recovery
checkpoint must preserve the relevant source changes, input identity and session
state outside ephemeral storage. Recovery and cleanup rules are still to be
implemented.

The [research archive](../research/README.md) preserves the investigations and
prototypes behind this direction. Their commands and experiments are separate
from the current Go CLI.
