# Changelog

The application version is declared in `internal/buildinfo/version.go`.
Each delivered beta iteration advances its numeric beta identifier. Source,
running/installed executable and Docker runtime identities are reported separately.
See the [versioning plan](docs/proposals/beta-versioning.md).

## Unreleased

## 0.1.0-beta.10 - 2026-10-09

### Added

- Set `"docker_tests": true` in project settings to enable the isolated privileged test daemon by default for new ticket and feature runs. Explicit `--docker-tests` or `--docker-tests=false` overrides the project default; saved runs keep their recorded setting.

### Fixed

- Stop with a Docker setup diagnostic when repository checks cannot reach their test daemon, instead of spending a provider repair turn and requesting an answer about missing logs. Retain private check evidence and explain when a new run with Docker tests is needed. Compiler and formatting errors remain available for repair when Docker cleanup also fails.

## 0.1.0-beta.9 - 2026-10-09

### Fixed

- Preserve the originating shell's PATH when starting a background run controller, so custom terminal commands can find Docker and other installed tools. Capture only PATH in the private launch receipt; older receipts retain their existing behaviour.

## 0.1.0-beta.8 - 2026-10-09

### Fixed

- Explain missing iTerm2 background terminal setup during shell run review, before Start. Show the Python API prerequisite, setup command and manual fallback.
- Display readable launch status and manual handoff instructions in the shell. An unavailable terminal reports that no run started; an uncertain launch keeps monitoring its receipt.

## 0.1.0-beta.7 - 2026-10-07

This release records the completed changes since the beta.6 progress delivery,
including changes previously installed under beta.6 with newer source revisions.

### Added

- Unique run ID prefixes and terminal-launch UUID prefixes of at least three characters. Dashboard, answers, resume, inputs, usage, progress, history actions and run inspection resolve prefixes; ambiguous matches report candidate IDs.
- `sdlc references` and `sdlc tickets REFERENCE` list local work references and their tickets, with matching shell commands.
- `sdlc inputs --run ID` inspects captured requirements and attaches missing files to eligible paused runs without restarting them. Questions and run details identify the recovery command.
- Read-only `sdlc storage` reports retained file sizes, categories and run ages. `--older-than DAYS` filters run rows; `--json` provides numeric results.
- Named runtime builds from a committed local skills checkout, selected with `--runtime NAME` for single-ticket runs. Each run retains its selected image; variant builds preserve the shared runtime.
- Bulk dashboard clearing with `dashboard remove --all`, previewed by default and applied with `--yes`. `forget` remains an alias; both retain saved work and its runtime update protection.
- `storage purge --run ID|--all` previews permanent deletion of stopped saved runs and affected feature checkpoints; `--yes` applies it. Tickets, specifications, archives and account settings remain.
- `work archive --reference REF` moves the complete stopped reference into `.sdlc/work/.archive`, preserving its files and freeing the original reference name for fresh scoping. Archived work no longer blocks runtime replacement or resumes from its original paths.
- `sdlc update --agent-tools` refreshes Codex, Claude, skills and agents while retaining other dependency pins. Add `--update-dockerfile` to write those four selected pins into the source Dockerfile after a successful runtime update; review and commit that change to share it with CI.

### Changed

- Rename host-only installation/update mode to `--sdlc-only`, keeping `--cli-only` as a compatibility alias. This mode retains the complete runtime, including provider CLIs and instruction catalogues.
- Present onboarding, setup problems and next actions in separate readable sections. Run and usage views distinguish missing evidence from failed checks.
- Pin the published skills catalogue with evidence-based test selection guidance.

### Fixed

- Scroll long shell help and command output with arrows, wheel and Page Up/Page Down. Clicking pauses output for terminal text selection; Escape restores scrolling and live updates.
- Keep dashboard footers and copy viewports steady during refreshes, and highlight questions, failures and active work while dimming completed rows.
- Capture native shell command output without the terminal flash, retain the command and its result, and keep runtime update failures visible without duplicate installer headings.
- Resolve committed links to requirements before snapshotting them. Preserve native sessions and captured inputs when recovering missing files, and reject incomplete source history before connected work begins.
- Use the active project's GitHub account for runtime status and explain account-pairing waits.
- Give repair agents bounded, actionable check failure details, preserving late compiler diagnostics. Isolated checks remove inherited proxy and publication credential variables.
- Preserve source dependency defaults when building local skills variants.
- Explain dashboard entries already removed while their saved work remains. Cleanup accepts ordinary owned scoping folders and ignores empty preparation directories while retaining checkpoint permissions, ownership and controller-lock checks.

## 0.1.0-beta.6 - 2026-10-06

### Added

- `sdlc progress --run ID` tails incremental private output; `--launch-id` follows all tickets from a terminal launch. JSON batches include a cursor for clients. Labels distinguish SDLC steps, checks, and agent provider and role.
- Rich and plain shells follow progress after starting or resuming work. `/progress --run ID` and `/dashboard --run ID --logs` use the CLI feed. Page Up preserves a paused viewport; End returns to the tail. Questions include the answer command.
- Readable native messages and tool output alongside existing raw diagnostic logs. Bounded batches retain unread records, and progress display failures retain provider session checkpoints.

## 0.1.0-beta.5 - 2026-10-06

### Added

- Local Homebrew installation through `python3 scripts/install_homebrew.py`, using a checksum-pinned committed snapshot and a host-only `local/sdlc` tap. The one-command flow prepares the runtime; `--cli-only` migrates a receipt-verified native CLI while retaining Docker state and backing up the executable.
- Homebrew-aware `sdlc update` and `/update` refresh the snapshot and formula before upgrading, including after signing or amending a commit. Homebrew owns CLI replacement; the source installer rejects package-owned destinations.
- Bundled runtime source discovery and verified package identity in version details. Archive builds retain their source revision; runtime commands continue after cleanup removes an older keg.

### Fixed

- Verify committed Homebrew source by its contents, so unchanged files with different timestamps do not block an update. Preview avoids Git content filters and filesystem-monitor helpers.

## 0.1.0-beta.4 - 2026-10-06

### Added

- One-command installation builds the CLI and runtime together using the newly built candidate. `sdlc update` and `/update` reuse saved installation locations; `--pull` fast-forwards clean source, `--cli-only` preserves the runtime and `--dependencies` refreshes public dependencies.
- Private installation receipts record source, destination and executable identity. Version details find the saved source from other projects and verify installed identity against its recorded hash.
- `runtime build --source-pins` applies the checkout's pins, including pipeline changes previously masked by private overrides. Default one-command updates use this mode; ordinary runtime rebuilds retain installed pins.

### Fixed

- Block runtime replacement when known saved runs or incomplete features need its image. Errors identify the work and offer CLI-only installation.
- Include Claude Code releases in scheduled runtime pin updates.

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
