# Research and prototypes

This archive preserves the investigations and experiments that led to the Go CLI
and shared local runtime. Start with the [current CLI](../docs/cli.md) and
[agreed workflow](../docs/workflow.md) for the supported commands and direction.
Archive material may describe earlier layouts, alternatives or unimplemented
proposals.

## Design and evidence

- [CLI language and container workflow](notes/cli-workflow-design.md)
- [Execution options](notes/options.md)
- [Runner options and human feedback](notes/runner-options-and-feedback.md)
- [Worker execution handoff](notes/worker-execution-handoff.md)
- [Protected runner handoff](notes/protected-runner-handoff.md)
- [Host control handoff](notes/host-control-handoff.md)
- [Colima trial](notes/colima-trial.md)
- [Guided host control test](notes/host-control-test.md)
- [Herdr remote assessment](notes/herdr-remote-assessment.md)
- [Historical runtime validation](notes/historical-runtime-validation.md)

## Earlier container runner

The [Python container runner](container-spike/README.md), its
[example inputs](container-spike/examples/) and the
[launcher](container-spike/sdlc.py) remain for reference and regression checks.
Its profile, login, ticket and publication commands are not Go CLI features.

- [Container onboarding](notes/container-onboarding.md)
- [Unattended Docker ticket jobs](notes/docker-ticket-jobs.md)

## Experiments

- [Security and signing](spikes/security/README.md)
- [Colima worker trials](spikes/colima/README.md)
- [Host control](spikes/host-control/README.md)
- [Offline decision loop](spikes/decision-loop/README.md)

These experiments have their own reproduction steps and test suites. They are
not installation paths for the current CLI. Keep generated state, credentials
and output outside tracked source.
