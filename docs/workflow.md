# Ticket workflow

The confirmed direction is an installed Go CLI coordinating work from a local
Git repository through one shared SDLC Docker image. macOS and Linux are the
primary hosts; Windows should remain possible with a Linux-container engine.

The [CLI setup commands and provider login](cli.md) are implemented today. Ticket
execution, secret retrieval, update prompts and recovery remain to be built.

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

## Stop and continue

Unattended work should either complete with recorded checks and results or stop
with an explicit problem and the information needed to intervene. The CLI should
show the failure, accept the required decision and continue the same logical job.

Logs alone cannot restore uncommitted work or a provider session. A recovery
checkpoint must preserve the relevant source changes, input identity and session
state outside ephemeral storage. Recovery and cleanup rules are still to be
implemented.

The [research archive](../research/README.md) preserves the investigations and
prototypes behind this direction. Their commands and experiments are separate
from the current Go CLI.
