# Changelog

The application version is declared in `internal/buildinfo/version.go`.
Each delivered beta iteration advances its numeric beta identifier. Source,
running/installed executable and Docker runtime identities are reported separately.
See the [versioning plan](docs/proposals/beta-versioning.md).

## Unreleased

## 0.1.0-beta.3 - 2026-10-06

### Added

- `sdlc answer --run ID` displays pending questions and accepts a multiline reply, inline `--text`, or `--stdin`, then resumes the recorded run without repeating its project, reference or ticket. `sdlc resume --run ID` continues other stopped runs.
- `/answer ID` opens a free-text shell editor; Ctrl+S submits the answer and resumes. `/answer` uses the run selected by `/dashboard --run ID`. Plain mode accepts multiline replies with an explicit submit line.
- `sdlc attention`, `/attention`, and `dashboard --attention` show questions, problems and work ready for human review. Run lists show pending questions and the next command; dashboard JSON includes pending questions and the next action.
- Feature summaries identify each stopped run, its reason and the command to inspect or answer it.

### Fixed

- Bind resume previews to the checkpoint and answer content. Changed questions or answers require a fresh submission; busy controllers and ambiguous run IDs are rejected.
- Preserve space key events in typed shell commands and free-text replies.

## 0.1.0-beta.2 - 2026-10-06

### Added

- Private usage records for every provider attempt, retained across role changes, repairs and controller resumes. Native cumulative counters are reconciled without counting earlier work twice; absent or incomplete telemetry stays unknown.
- Read-only `sdlc usage` with project/installation scope, period selection, exact run selection and JSON output. Dashboard and run reports include durable totals, coverage and observed controller, check-worker and CI-polling time.
- Opt-in `--headroom passthrough|optimize` for ticket and feature runs, with a separately built Headroom 0.39.1 image. Runs freeze its image identity and compression policy; resumes retain those settings.
- Provider-native quota observations where emitted, and separate Headroom estimates for measuring the proxy variant.

### Fixed

- Include model roles, checks, input selections and Headroom mode in feature previews and their approval hash. Terminal launches use the saved preview settings and reject changed selections.

## 0.1.0-beta.1 - 2026-10-05

### Added

- List configured GitHub profiles locally and remember the selected GitHub profile and repository identity for a checkout.
- Record run preparation failures and show them in dashboard history before a provider run journal exists.
- Opt-in `sdlc shell` with slash commands, help, local reference/ticket completion, explicit content inspection, project selection and project-scoped history.
- Terminal-filling layout that redraws on resize, inherited terminal colours and font, a Robby Russell prompt, and a plain line mode.
- Offline run-plan review before explicit Start, private one-use terminal launch receipts and existing-controller monitoring. Native iTerm2 launch requires terminal setup; unsupported configurations show a manual command.
- Read-only project onboarding and separate running, PATH, selected-source and recorded-runtime version evidence.
- Structured CLI help, work discovery and run previews for the shell and other clients.

### Fixed

- Preserve committed `.env.example` templates in source snapshots when their parent directory is allowed, while excluding private and untracked environment files.
- Retry failed feature preparation through its owning controller without overwriting an existing run or inventing a provider resume checkpoint.

Earlier changes remain available in Git history. No retrospective release numbers are assigned here.

This establishes the beta source baseline. Release archives/tags and complete
conversation inspection, bulk history clearing and default bare-command shell
launch remain follow-up work.
