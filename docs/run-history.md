# Run history and retention

SDLC keeps local run data indefinitely. Reaching `ready` means a draft PR has
passed the controller's checks and independent review and is awaiting human
review. It does not merge the PR or remove local files.

The dashboard shows ten runs per page. Questions and problems come first,
followed by missing reviewer login, queued/running work, then completed work.
Within a priority group, newer updates come first. Use `--page N`, or `n` / `p`
in a watching terminal, to page history. The page can change
when new runs arrive or their priority changes.

Run selectors accept a full ID or a unique lowercase hexadecimal prefix of at
least three characters. This applies to dashboard details, removal and export,
answers, resume, inputs, usage and progress, and `/inspect @run:ID`. Ambiguous
prefixes list the matching full IDs; use more characters to select one. Commands
retain the full ID after selection and keep their existing project or
installation scope. Explicit `usage --run` selection ignores the age filter.

## Keep a portable checkpoint report

Export before removing a dashboard registration:

```sh
install -d -m 700 "$HOME/.local/share/sdlc/run-reports"
sdlc dashboard export --run RECORDED_RUN_ID \
  --to "$HOME/.local/share/sdlc/run-reports"
```

The command writes `RUN_ID.report.json` with mode `0600`. The destination must be
an existing owned `0700` directory outside Git checkouts, with no symlink in its
path. Existing reports are never overwritten; use another private directory for
a later snapshot of the same run. This is an offline operation.

The versioned report contains the checkpoint stage and timestamps, repository
and ticket, branch/base/source commit IDs, runtime image, selected models and
efforts, input hashes, check commands and head/tree evidence, PR URL and published
commit, CI result, latest outcome summary, findings, limitations and repair/attempt
counts. Pending questions are included only when the checkpoint is waiting for a
human answer. This is a snapshot of the latest checkpoint, not a complete record
of every earlier review round.

It excludes raw logs, native transcripts, implementation instructions, feedback,
session IDs, account/signing configuration, private input contents and captured
source. Reports still contain private work details; check commands and model
summaries can themselves contain sensitive material. File permissions are not
encryption. Keep reports in private, backed-up storage and inspect them before
sharing. A report cannot resume a run.

## Remove a run from the dashboard

```sh
sdlc dashboard forget --run RECORDED_RUN_ID
```

`remove` is an alias for `forget`. It removes only that run's JSON registration
from the installation's private registry. It retains both registry and controller
lock files, and every run artifact. It prints where the saved work remains.

An active controller cannot be forgotten, even if its heartbeat is stale. The
command acquires the controller's real lock and checks private metadata, file
ownership and links before removing the registration. Missing directories or
unsafe registry metadata must be repaired first. Removal and export fail closed
on Windows until equivalent ownership checks are implemented.

The dashboard stops listing the run; resuming it registers it again. The legacy
`sdlc run --reference REFERENCE --ticket NUMBERED_FILE --resume RUN_ID` command
resolves prefixes within that ticket's retained checkpoint directory, including
runs forgotten from the dashboard.

Export a report first if you want it accessible independently of that registration.
There is no automatic expiry, disk purge or credential deletion in this command.

## What stays on disk

Each run is under the originating checkout:

```text
.sdlc/work/<reference>/runs/<ticket-stem>/<run-id>/
```

The run directory can retain:

| Artifact | Contents |
| --- | --- |
| `journal.json` | Latest stage, frozen plan and identities, models, requirements/check-input hashes, check evidence, native implementation session ID, latest provider outcome, review feedback, repair counts, PR/CI details and resume state. |
| `activity.json` | Latest heartbeat, controller state, output activity, cache-wait state and optional native usage measurements. |
| `events-N.jsonl`, `diagnostics-N.log` | Native provider event streams and diagnostic output for each attempt. Earlier review evidence may remain here after the latest journal outcome changes. |
| `checks-N.log` | Output from each local verification attempt. |
| `workspace/` | Captured Git repository, implementation changes and selected requirement inputs. |
| `check-inputs/` | Configured private verification files, including `.env` when selected. These are withheld from the provider and review workspace but remain on the host. |
| `native-implementation/`, `native-review-N/` | Native client session state and transcripts used for implementation resume or independent review. |
| `review-source-N-ID/` | Captured source at the published revision and selected requirements for each independent review. |
| `source.bundle`, `publisher/` | Latest publication bundle and retained signed publication repository/state. Older layouts may use `publication.git`. |
| `run.lock` | Controller ownership lock; retain it with the run. |

Only artifacts used by the run exist. The directory can be large: transcripts,
check logs and multiple source snapshots are retained. Journals, logs and model
summaries can contain private code or sensitive output from tests. A dependency
that prints environment variables can leak secrets into retained logs. Do not
commit these directories or copy them to shared storage without inspection.

Provider and GitHub login caches live in separate Docker volumes. The signing
profile and 1Password Service Account bootstrap are separate private host state.
Signing resolution/publication use disposable mounts for key material. These
credentials are not part of a checkpoint report or a planned run-directory
backup. Do not back up credential volumes or bootstrap files as ticket history.
Unexpected process termination and hostile code remain reasons to inspect
temporary/session artifacts before sharing any full backup.

## Keep a complete private record

For long-term evidence, keep the compact report in encrypted private storage and
rely on the draft PR for published code and discussion. For full investigation or
resume, also back up the **entire stopped run directory**, including native
session files and captured repository. Export only after the run has stopped if
you need a stable final checkpoint; an active export is a point-in-time summary.

A full backup contains private inputs and may contain secrets in logs. Use an
encrypted destination with access limited to the person running SDLC, a retention
policy appropriate for the project, and sufficient disk space. Backing up the
run directory does not make it portable to any host: resume also requires the
recorded source paths, runtime image and authorised provider/GitHub/signing
configuration. Credentials should be configured separately when restoring.

The GitHub PR remains after local dashboard removal. Local checks, full native
transcripts and `.env` contents are not automatically attached to that PR.
Deleting the checkout would delete its local run history unless backed up first.

## Inspect retained project storage

From a configured repository, use the CLI or the same shell command:

```sh
sdlc storage
sdlc storage status --older-than 30
sdlc storage --json
```

In `sdlc shell`, use `/storage [status] [--older-than DAYS] [--json]`.
The scan is read-only. It reports directory totals, storage categories, and each
retained run's size, file count, checkpoint state and last activity. Completed
runs awaiting review are distinguished from resumable runs. Controller status
uses saved heartbeat evidence; a recent heartbeat does not prove the controller
is running.

Sizes are logical bytes for regular-file paths, rather than allocated disk
blocks. Hard links count once per path. The scan excludes symbolic links, Git
metadata, known authentication directories/files and entries on other filesystem
devices. It lists these exclusions and reports unreadable files or malformed
checkpoint metadata as a partial measurement, with a failing exit status. It
reads bounded checkpoint/activity metadata and never displays artifact contents.
Filesystem boundary checks currently support Linux and macOS; other hosts fail
closed.

Age uses the newest measured artifact modification time or saved checkpoint,
heartbeat or activity timestamp. `--older-than DAYS` accepts 0–36500 and filters
run rows strictly older than that age. Exact-boundary and unknown-age runs are
excluded. Folder and category totals still cover the full scan; matching-run
totals are shown separately. Fresh artifacts keep a run out of an older-age view,
even when its checkpoint is old.

This command does not purge files, unregister runs, stop controllers or change
checkpoints. Age alone does not make a run safe to remove. Retention and cleanup
policy can be chosen after inspecting the retained artifacts.
