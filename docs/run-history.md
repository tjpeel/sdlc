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

New runs require complete Git history so their saved workspace can be reviewed
and published. Shallow checkouts or missing reachable objects fail before
provider preparation; restore the history or use a complete checkout. An offline
`--dry-run` remains available to inspect the plan.

When an isolated check fails, repair feedback identifies the command number and
exit status. Recognised Prettier warnings also identify committed source files
that need formatting, including when checks share one shell command. Raw check
logs stay in `checks-N.log`; arbitrary output and private inputs are withheld
from provider prompts.

Check processes receive no inherited proxy or publication credential variables.
SDLC first overrides Docker's automatic proxy injection, then removes those
variables before executing the original command. This also supports libraries
that interpret an empty proxy value as a configured URL. Docker integration
checks retain their dedicated daemon connection.

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

## Hide dashboard entries, remove saved runs or archive work

```sh
sdlc dashboard remove --run RECORDED_RUN_ID
sdlc dashboard remove --all
sdlc dashboard remove --all --scope project --yes
sdlc storage purge --run RECORDED_RUN_ID
sdlc storage purge --all --yes
sdlc work archive --reference TASK-123 --dry-run
sdlc work archive --reference TASK-123
```

`dashboard remove` is the recommended spelling; `dashboard forget` remains an alias.
Both hide registry entries and retain saved files. Both accept `--run ID` or
`--all`, `--scope project|installation`, and `--yes|--dry-run`. The default scope
is the installation. Single-run removal remains immediate unless `--dry-run` is
selected; bulk removal previews unless `--yes` is selected.

`dashboard remove --run` (also `forget --run`) hides one stopped run by deleting only its registry JSON. It
retains saved work and lock files, so that work can still block an update.
Resuming the retained run registers it again. An active controller cannot be
forgotten, even if its heartbeat is stale. Export a report first if you want a
snapshot that remains accessible without the registration.

`dashboard remove --all` (also `forget --all`) previews clearing stopped registrations by default. Use `--yes`
to clear them or `--dry-run` for an explicit preview. Its default scope is the
installation; `--scope project` limits it to the current repository. It leaves
all saved run files in place.

`storage purge` permanently deletes stopped saved runs
from the current Git repository, including runs absent from the dashboard.
Choose `--run ID` or `--all`. Both preview by default; `--yes` authorises deletion
and `--dry-run` explicitly previews it. Affected feature series checkpoint
directories are also removed, and the preview reports that the series will be
abandoned. Ticket and specification files remain. Live runs or series refuse
removal. `--all` excludes archived references and credentials.

`work archive --reference REF` moves the complete `.sdlc/work/REF` tree to a
unique directory under `.sdlc/work/.archive/` and removes dashboard registrations
for its runs. `--dry-run` previews the move. It retains the tickets, specs, saved
runs and evidence; the reversible local move needs no `--yes`. The source
reference becomes available for fresh scoping. Live runs or series refuse
archiving.

Archived references are excluded from active discovery, resume and runtime
guards. A later update can replace their runtime image, so archiving does not
guarantee that a restored run can resume. It is not a portable backup.

These operations check controller locks and private filesystem metadata.
Unsafe links, ownership or metadata must be repaired first. Report export and
history changes fail closed on Windows until equivalent ownership checks are
implemented. There is no automatic expiry or credential deletion.

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
