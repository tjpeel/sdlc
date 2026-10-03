# Ticket workflow

The confirmed direction is an installed Go CLI coordinating work from a local
Git repository through one shared SDLC Docker image. macOS and Linux are the
primary hosts; Windows should remain possible with a Linux-container engine.

The [CLI setup commands, provider login, instruction settings and interactive sessions](cli.md)
are implemented today. Interactive sessions currently use an empty disposable
workspace. Ticket execution, secret retrieval, update prompts and recovery remain
to be built.

## Set up the installation

Clone the SDLC repository, install the CLI on PATH and build the shared image
locally. Every project uses that image. Project names, source, tickets and
credentials do not become image build inputs.

Authenticate Codex and Claude with `sdlc auth login --provider codex` and
`sdlc auth login --provider claude`. Their login state lives in installation-wide
storage, separate from disposable work containers. Claude manages its own native
cache, following the [provider usage rules](provider-usage.md). The image contains pinned
skills and agents from the public GitHub catalogues. Startup should report
available updates to both provider CLIs and the skills/agents, and prompt before
updating the shared runtime.

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

The ticket layout comes from the public
[engineering skills on GitHub](https://github.com/tjpeel/skills/tree/e5071a81c703f36014ea60446204b2434d6b1579/engineering):

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
