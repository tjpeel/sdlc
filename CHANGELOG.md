# Changelog

The application version is declared in `internal/buildinfo/version.go`.
Each delivered beta iteration advances its numeric beta identifier. Source,
running/installed executable and Docker runtime identities are reported separately.
See the [versioning plan](docs/proposals/beta-versioning.md).

## Unreleased

## 0.1.0-beta.30 - 2026-10-13

### Fixed

- Detect pinned CSharpier 1.x in local .NET tool manifests and place tool restore and root formatting checks before other inferred checks. Preserve documented formatter options, service cleanup and saved settings; reject malformed or ambiguous manifests without overwriting usable configuration.
- Recognise analyzer error S8969 in bounded repair feedback, returning its committed source location and fixed guidance without exposing private log messages.

## 0.1.0-beta.29 - 2026-10-13

### Fixed

- Include bounded CSharpier Expected/Actual line locations in repair feedback for approved committed files, keeping source excerpts and other check output private.
- Run pending isolated verification requests after configured check failures within the repair bound. Failed commands invalidate shared source and HOME reuse; configured failures still prevent publication.
- Let an explicit resume after the repository-check repair limit begin another finite repair window, preserving cumulative repair history, native sessions and the implementation-turn limit. Other stops retain their existing recovery rules.

## 0.1.0-beta.28 - 2026-10-13

### Fixed

- Include CSharpier formatting diagnostics in repair feedback, resolving Docker prefixes and relative paths to unambiguous committed files while keeping raw check output private.
- Let implementation workers request companion source through the controller using the run's selected GitHub profile and official CLI. Export tmpfs source through tar while the reader stays running, retain Git history, recover validated staging after interruption, and mount source read-only without exposing GitHub credentials. Provider stops remain stops even when they include source requests.

## 0.1.0-beta.27 - 2026-10-12

### Fixed

- Recover feature previews and retries from older empty-check preparation failures that recorded run IDs before creating run directories. When no run data exists, use the corrected project checks and Docker-test setting while preserving selected providers, models and account pairing. Preview leaves the saved checkpoint unchanged; execution freezes the corrected preparation under the feature lock. Prepared runs keep their frozen settings.

## 0.1.0-beta.26 - 2026-10-12

### Fixed

- Ignore the whole local `.sdlc` directory during initialization, including `project.json`, so new local settings do not appear as untracked source. Repeated initialization expands the previous work-only rule while preserving project settings and other excludes. Already tracked settings stay tracked; portable configuration can still be added explicitly after review.

## 0.1.0-beta.25 - 2026-10-12

### Added

- Prepare .NET restore, build and runner-specific test checks from required-check guidance, simple CI sequences or solution/project manifests. Documented Compose checks enable Docker tests and always tear services down after failures. Report incomplete or ambiguous inference for explicit configuration.
- Add `sdlc init --force` to refresh inferred checks and Docker-test settings while preserving inputs, branch format, NuGet cache preferences and saved runs. Retain the previous configuration in a private ignored backup when settings change.

### Fixed

- Reject feature runs without verification checks before freezing settings, saving ticket checkpoints or contacting Docker and providers.

## 0.1.0-beta.24 - 2026-10-12

### Added

- Capture Claude subscription percentage remaining before and after native work through the official client's experimental usage control request. Show percentage points used alongside Codex, retaining unknown results for unsupported clients, missing data or changed quota windows.
- Verify both pinned clients' quota protocols without authentication, network access or model prompts. Keep Claude's percentage-valued usage snapshots separate from native quota-event ratios and retain only selected numeric fields.

## 0.1.0-beta.23 - 2026-10-12

### Added

- Capture Codex subscription percentage remaining before and after native work through the official App Server client, without model requests. Retain the baseline during execution and show account allowance changes in usage, dashboard details and checkpoint reports.
- Keep percentage used unknown when snapshots are missing, quota windows reset or allowance increases. Quota probes do not contribute to native event reception timing.

## 0.1.0-beta.22 - 2026-10-12

### Added

- Record command, source-copy, cache, fingerprint and cleanup durations alongside the complete candidate checking phase. CI wall time includes queries and controller polling; polling sleep remains separate.
- Capture runtime source, skill and agent catalogue revisions for new runs from their frozen runtime evidence. Usage, dashboard JSON and checkpoint exports show this provenance; older runs retain unknown provenance, and resumed older runs mark timing history as partial.

## 0.1.0-beta.21 - 2026-10-12

### Changed

- Disable workload update notifications and the first-run workload integrity check in credential-free .NET check workers.
- Document runner-specific focused tests, fully qualified xUnit/MTP method filters and minimum expected test counts.

## 0.1.0-beta.20 - 2026-10-12

### Changed

- Reuse the candidate workspace and HOME across primary checks and additional verification within one controller phase. Baselines keep separate source and HOME.
- Fall back to fresh check storage when a command changes tracked source or frozen inputs; retain separate request logs and clean up the shared candidate phase before publication.

## 0.1.0-beta.19 - 2026-10-12

### Changed

- Seed NuGet caches from committed lockfiles, preserving exact direct and transitive versions alongside unlocked projects.
- Let projects configure bounded host import budgets. New runs default to 512 packages and 256 MiB of archive and expanded payload bytes; older saved runs retain their previous limits.

## 0.1.0-beta.18 - 2026-10-12

### Fixed

- Validate new runs' PR titles, reference fields and complete description headings before publication. Metadata corrections resume the original implementation session and reuse passing checks for unchanged source.
- Freeze the default PR structure or a repository's default Markdown PR template at capture. Existing saved runs retain their original publication contract.

## 0.1.0-beta.17 - 2026-10-12

### Changed

- Name new publication branches from the work reference and ticket description, preserving valid reference case and omitting ticket sequence numbers and run IDs. Project `branch_format` supports `{reference}` and `{ticket}`, defaulting to `{reference}-{ticket}`. Individual and feature previews show resolved names, duplicate feature branch names stop before execution, and resumed runs retain their recorded branches.

## 0.1.0-beta.16 - 2026-10-10

### Fixed

- Publish the drafted PR description without appending internal verification hashes or orchestration details. Work references remain in PR titles, and publication integrity checks still apply.
- Refresh PR drafting guidance to favour completed behaviour, compact visual explanations and relevant evidence beyond routine CI. Retain the current runtime tool versions in source pins.

## 0.1.0-beta.15 - 2026-10-10

### Added

- Allow `runtime build --force` to replace the runtime while retaining stopped work, its checkpoints and the previous image. Saved work remains tied to its recorded runtime and cannot resume against the replacement. Active controllers, dependent containers and invalid checkpoint data still block replacement.

## 0.1.0-beta.14 - 2026-10-10

### Added

- Reuse validated NuGet package archives across Docker check sessions, seed them from the host package cache, and give each session its own writable package and scratch directories. Nested SDK containers receive the same session cache.
- Record native event reception intervals in private attempt metrics and usage JSON. Include agent-tool start and completion in progress logs without their arguments or results.

### Fixed

- Preserve the check worker's private home between commands, so tool restore and package restore remain available to later formatting and build commands. Candidate and baseline phases keep independent homes and build outputs.
- Describe the check worker's source, Git, package and tool boundaries in implementation prompts to avoid repeated verification setup repairs.

## 0.1.0-beta.13 - 2026-10-09

### Added

- Share one persistent test daemon across new Docker-enabled runs, retaining image layers and build cache. Scoped Docker API sockets allow parallel check sessions to use identical Compose projects, container and network names, and published TCP or UDP ports.
- Reuse required host-cached images by immutable identity and committed source templates by Git tree. Read source blobs through a single Git batch process and keep frozen inputs and writable build state separate from cached templates.

### Changed

- Clean up each session's resources and recover orphaned checks after controller crashes while retaining cached images and live sessions. Saved runs keep their original disposable-daemon mode and frozen image pin.
- Shared checks use the classic Engine build API and controller cleanup for Testcontainers. Rebuild the runtime after upgrading; the Docker check cache guide describes supported operations and cache lifetime.
- Validate Docker request field casing, canonical bind paths, image provenance and build networks; override inherited controller labels. Shared builds reject deferred instructions, `ADD` and archive links, and keep private intermediate images out of session API access.

## 0.1.0-beta.12 - 2026-10-09

### Added

- Execute structured requests for additional verification in isolated check workers. Baseline comparisons retain candidate tests, restore selected original source files and require the expected assertion failures before returning evidence to the implementation session.

### Fixed

- Keep additional verification requests outstanding until satisfied, cache evidence by request and exact candidate, and stop repeated requests that make no progress. Temporary runtime failures remain retryable; passing project checks cannot substitute for newly requested work.

## 0.1.0-beta.11 - 2026-10-09

### Fixed

- Align dashboard provider and last-output columns. Label output age explicitly for live controllers and show fixed timestamps for stopped or stale controllers, so refreshes do not imply new job activity.
- Display dashboard durations in whole seconds and retain a fixed elapsed duration for stopped runs without a recorded stop timestamp. Label absent native context telemetry as not reported; cumulative token totals remain separate.

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
