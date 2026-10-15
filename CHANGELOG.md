# Changelog

The application version is declared in `internal/buildinfo/version.go`.
Each delivered beta iteration advances its numeric beta identifier. Source,
running/installed executable and Docker runtime identities are reported separately.
See the [versioning plan](docs/proposals/beta-versioning.md).

## Unreleased

## 0.1.0-beta.80 - 2026-10-15

### Changed

- Choose Claude review effort from the exact published PR by default: low for small documentation changes, medium for ordinary or uncertain changes, and high for large or sensitive changes. Retain the selected model, record the decision and reasons, and respect explicit effort overrides and previously saved settings.
- Shorten completion usage reports to model/role, requested effort and speed, elapsed time, measured tokens, observed subagents and available subscription percentages. Omit repeated missing fields and duplicate session totals; full details remain available through usage and JSON.

## 0.1.0-beta.79 - 2026-10-15

### Changed

- Treat coverage limitations in a completed independent review as visible warnings at the human-review checkpoint, retaining the reviewer’s account in private run state. Findings, human questions and operational failures keep their existing handling. Diagnostics distinguish reviews with limitations; later PR feedback passes remain available.
- Ask reviewers to explain incomplete coverage and why they stopped, without treating controller verification as their own review.

## 0.1.0-beta.78 - 2026-10-15

### Changed

- Quote the original top-level PR comment or review summary, with a direct link, before the response. Inline replies keep their original thread; retries recognise earlier ID-only acknowledgements without posting duplicates.

## 0.1.0-beta.77 - 2026-10-15

### Fixed

- Isolate shared Docker check caches by frozen daemon recipe, allowing old and new daemon versions to coexist without resetting another run's cache. Legacy caches remain untouched; the first check under the new controller starts a separate compatible cache.

## 0.1.0-beta.76 - 2026-10-15

### Fixed

- Include the validated local agents archive in Docker's runtime build context, allowing builds that select local skills and agents to reach catalogue installation. Add an offline Docker regression check for prepared-file completeness and excluded private files.

## 0.1.0-beta.75 - 2026-10-15

### Added

- Print recorded usage when a run stops or finishes. Usage and dashboard details now show provider sessions, resumed invocations, requested model/reasoning/speed and available subagent lifecycle, model and usage observations. Missing native settings remain unknown; child counters are kept separate from session totals.

### Fixed

- Keep different reasoning and speed requests separate in usage groups, while preserving cumulative-session reconciliation across resumes.

## 0.1.0-beta.74 - 2026-10-15

### Fixed

- Dependency build planning now selects the newest published Docker CLI and daemon image pair when source tags precede image publication. Preserve the installed version floor and report registry failures without silently falling back.

## 0.1.0-beta.73 - 2026-10-15

### Added

- Refresh supported dependencies when building a named runtime with `--dependencies`, while retaining exact local skills and agents commits. Preview resolved versions and image digests with `--dry-run`; fresh builds do not require an installed runtime, and candidate validation precedes selection.

## 0.1.0-beta.72 - 2026-10-15

### Added

- Adopt an existing open same-repository PR with `sdlc adopt --pr URL`, optionally supplying a local ticket. Start a fresh comments-repair session, preserve existing history and PR metadata, sign correction commits, and require verification, CI and independent review. Retain the frozen base for PRs behind their target branch; reject fork PRs and changed publication identities.

## 0.1.0-beta.71 - 2026-10-15

### Added

- Build named runtimes from clean, committed local agents catalogues with `--agents-source`, independently or alongside `--skills-source`. Record both commits and preserve the default runtime and saved runs.

## 0.1.0-beta.70 - 2026-10-14

### Changed

- Default new runs to Codex Sol 6.1 and Claude Sonnet 5.5 at low effort. Select a shared reasoning band, override each lead's model or effort, and request normal or fast service separately for implementation and review. Saved runs retain their recorded settings.
- Record requested speed separately from unknown effective service speed. Reject unsupported Claude fast-mode models before dispatch; speed controls use the official native clients.

### Fixed

- Recognize Claude's synthetic usage-limit error metadata while preserving the selected model and genuine model-change holds. A corroborated saved review stop can record its skipped review from retained events without another provider attempt.

## 0.1.0-beta.69 - 2026-10-14

### Changed

- Finish at the human review checkpoint with an explicit warning when the selected independent reviewer exhausts its provider allowance. Record the skipped review against the exact published revision and keep local verification, signing and CI requirements.
- Give independent reviewers a smaller initial packet and output contract, retaining the selected requirements, current feedback and verification evidence, with the full checkout, history and companion source available for inspection.

### Fixed

- Retain native reviewer usage-limit evidence separately from authentication, protocol and cleanup failures. Corroborated saved review usage stops can complete without another provider attempt.
- Keep existing comment assessments and replies valid when GitHub moves an inline comment's current path or line after a rename, while retaining checks for actual comment edits and changed original context.

## 0.1.0-beta.68 - 2026-10-14

### Changed

- Give the original implementer all pending PR comments together so it can choose the order, group related fixes and request full checks for a coherent candidate. Retain a separate assessment, commit mapping and reply for every comment.

### Fixed

- Allow comment assessments alongside development diagnostic requests, preserving them while the original session investigates and completes its changes.

## 0.1.0-beta.67 - 2026-10-14

### Fixed

- Disable automatic Git maintenance during publisher operations so detached collectors cannot rewrite retained packs during import, strict integrity checks and signing.
- Keep offline aggregate fixtures free of detached Git maintenance that can race temporary checkout cleanup. Regression checks observe real Git subprocesses under low maintenance thresholds.

## 0.1.0-beta.66 - 2026-10-14

### Fixed

- Include validated frozen companion source and independent Git history in each published review snapshot.
- Accept source-only companion requests from independent reviewers, stage new source through the existing controller reader and start a fresh review after exact PR and CI checks. Source requests retain the implementation session and cumulative accounting without spending repair grants.
- Keep companion access questions and their recorded answers with independent review.

### Changed

- Remind implementation agents to follow the selected repository's commit subject policy and check capitalization during focused self-review before requesting checks. Signing continues to preserve exact commit messages.

## 0.1.0-beta.65 - 2026-10-14

### Fixed

- Count configured-check failures separately from CI and independent review, retaining cumulative history and opening a new failure window only after a passing configured suite.
- Keep active and queued live diagnostic requests owned until their controller operation completes, without a whole-tool deadline.

### Changed

- Remove the default lifetime cap on native agent dispatches while retaining all recorded attempts and historical continuation grants.
- Give new CI, configured-check and review repair allowances twelve unsuccessful cycles or handoffs; saved review limits can be increased through an audited resume option.
- Allow thirty minutes for CI checks to appear after publication. Reported pending checks continue to wait without an implicit deadline.

## 0.1.0-beta.64 - 2026-10-14

### Fixed

- Remove the implicit two-hour deadline from run, resume and feature controllers so progressing workflows can finish. Explicit invocation timeouts, parent cancellation and bounded unsuccessful cycles still apply.

### Changed

- Report whether an ordinary resume preview has an execution deadline.

## 0.1.0-beta.63 - 2026-10-14

### Fixed

- Retain analyzer descriptions even when annotations omit rule IDs, and collect verified current-job Trivy reports so CI repairs receive vulnerability IDs, affected packages and fixed versions. Exclude secret matches and source snippets, preserve feedback limits, and prioritise findings over repeated log output.
- Recover a corroborated saved CI repair that asks for fuller controller evidence through ordinary resume, reusing its original session and repair grant.

### Changed

- Clarify that context and token efficiency must preserve enough detail for diagnosis and repair, with focused diagnostics available when the first feedback packet is insufficient.

## 0.1.0-beta.62 - 2026-10-14

### Fixed

- Read verified Actions job logs with a separate 16 MiB intake limit instead of the 512 KiB metadata limit. Mask the full log before selecting failure excerpts; retain the existing agent-feedback limits, identity checks and operational holds. Oversized-log stops now name the intake limit.

### Changed

- Make agent context headroom and token consumption explicit workflow design goals, assessed alongside time, repair rounds and the delivered outcome.

## 0.1.0-beta.61 - 2026-10-14

### Fixed

- Retain a bounded window around the first standard failure marker in unsuccessful check output, including failures in the middle of long logs. Keep original byte offsets and truncation counts, preserve the existing output limits, and prioritise that context when combining command results.

## 0.1.0-beta.60 - 2026-10-14

### Fixed

- Return formatting outside the selected source paths as a live diagnostic request failure, retaining the other batch results so the agent can narrow its command and continue. Export no artifact for that request; keep source integrity, frozen inputs and cleanup failures as operational holds.

## 0.1.0-beta.59 - 2026-10-14

### Added

- Let the implementation agent call an isolated diagnostic worker during its running native CLI invocation on macOS and Linux. Return focused check results and bounded source correction artifacts immediately, without another model turn or permanent verification gate. Keep the structured diagnostic handoff as the fallback.

### Changed

- Use implementer self-review followed by the controller's required opposite-provider review after CI. Remove the extra mandatory delegated local review while retaining full checks, signing, CI, and revision-bound repairs.
- Return recoverable dirty-checkpoint and stale-candidate tool errors to the running agent so it can commit or retry. Withhold provisional evidence and keep infrastructure, frozen-input and cleanup failures as operational holds.

### Fixed

- Pin source copying, fingerprints and source correction collection to the accepted tree during live diagnostics, so concurrent edits cannot mix candidate identities.

## 0.1.0-beta.58 - 2026-10-14

### Added

- Let the original implementer request focused diagnostic batches before the configured suite. Return bounded results from the existing credential-free worker without making exploratory commands permanent publication gates. Preserve required verification and all final checks, signing, CI and independent review.

### Changed

- Wait for pending CI checks before returning a completed failure packet, so the implementer can assess related failures together. Keep each command chain fail-fast and encourage coherent repair batches.

### Fixed

- Distinguish an unavailable GitHub identity lookup from a confirmed identity mismatch, with a relevant verification command before resuming. Both conditions still block CI inspection.

## 0.1.0-beta.57 - 2026-10-14

### Fixed

- Read coloured GitHub Actions logs using the official client's documented option. Remove terminal controls and mask private values in the bounded collector before retaining failure evidence; keep metadata requests and all identity, cleanup and size checks unchanged.

## 0.1.0-beta.56 - 2026-10-14

### Added

- Return exact-revision CI check output, annotations and failed GitHub Actions job context automatically to the original implementer. Keep native Sonar findings alongside generic failure evidence so the agent can assess unfamiliar errors.
- Resume an eligible saved missing-CI-context stop with ordinary `sdlc resume`, reusing its existing repair grant and original session. Mask collected credentials and hash-verified check-input values before retaining bounded private excerpts; keep collection, identity, cleanup and transport failures as operational holds.

### Fixed

- Preserve the same blocked summary and limitations in CI recovery admission, audit validation and dispatch. A missing-context reason supplied only in limitations no longer passes preview and then fails before the worker starts.

## 0.1.0-beta.55 - 2026-10-13

### Added

- Supply bounded supplemental CI context to an original implementation session that stopped for missing failure evidence with `sdlc resume --run RUN_ID --ci-feedback-file FILE`. Bind the context to the unchanged candidate and failed published head, retain a private recovery audit, and require normal checks, signing, CI and independent review afterward.
- Preserve the existing CI grant and all cumulative accounting. Refuse unrelated holds, repeated supplementation of the same published head, stale evidence and ambiguous interrupted dispatch.

## 0.1.0-beta.54 - 2026-10-13

### Added

- Amend only the dependencies of an unstarted ticket in a saved feature with `sdlc dependencies`. Preview the change offline, preserve parent jobs and frozen execution settings, and retain an audit record with interrupted-update recovery.
- Reject amendments while a feature controller is active, after child preparation or execution has started, or when the saved plan has changed. Existing parent readiness, combined-base checks, signing, CI and review gates still control child execution.

### Fixed

- Treat failed GitHub Actions workflow checks that mention Sonar as pipeline failures rather than native Sonar analysis. Return them through the existing CI repair cycle while preserving exact-head checks and trusted app identity for analyzer annotations.

## 0.1.0-beta.53 - 2026-10-13

### Fixed

- Replace the publisher's private candidate and rejected-audit import refs when verified unpublished history is rewritten. Keep bundle integrity, exact candidate identity, published history and remote lease checks enforced before another protected push.

## 0.1.0-beta.52 - 2026-10-13

### Fixed

- Accept trailing space padding in GitHub's push-protection diagnostics before validating the exact rejection grammar. Padded commit, file and line locations now reach the original implementation session through the existing verified recovery path.

## 0.1.0-beta.51 - 2026-10-13

### Added

- Recognise a verified GitHub push-protection rejection at the actual push operation and return bounded original commit, file and line locations to the saved implementation session. Keep raw diagnostics, detected values and exemption URLs private.
- Repair the unpublished implementation history before another protected push, retaining rejected source evidence and published history. Require a meaningful historical correction and fresh checks, signing, CI and independent review; unrelated publication failures retain their existing holds.

### Changed

- Run journals containing push-protection repair evidence require beta.51 or newer. Older saved runs remain readable; an old generic publication diagnostic requires a fresh trusted push result before it can authorise a repair cycle.

## 0.1.0-beta.50 - 2026-10-13

### Added

- Start tickets with multiple dependencies from a checked combination of current integration and exact ready parent revisions. Publish an immutable signed derived base and target the child PR there, keeping its review diff limited to its own work.
- Record combined-base generations, parent provenance, verification context and the child's own source boundary. Reuse unchanged contexts, invalidate affected descendants when parents change, and reconcile through the original saved jobs and sessions. After all parents merge, replay only the child's changes onto integration and retarget its existing PR.

### Changed

- Restacks retain linear ticket history and repeat repository checks, signing, CI and independent review. Parent PRs still require human merge decisions; dependent PRs should wait for retargeting to integration before merging. Recorded derived branches remain available for recovery and audit.
- Saved checkpoints and run journals containing the new dependency or restack recovery fields require beta.50 or newer. Existing saved work without those fields remains readable.

## 0.1.0-beta.49 - 2026-10-13

### Fixed

- Keep PR-comment assessments provisional while the implementation session requests checks, companion source or human clarification. Accept the final assessment for the same current comment revision, retain assessment history, and post the reply only after completion. Recover the matching saved feedback hold without another provider turn or budget reset.
- Retain bounded publication failure diagnostics in private host files. Deliver a verified publisher helper with the CLI so saved runs can use current diagnostics while preserving their frozen runtime, signing identity, remote guards and credential isolation. Stage verified read-only package assets as executable private copies before credential acquisition.

### Added

- Show a concrete next action when a run stops. Inspect allowlisted state, counters, evidence identities and validated publication diagnostic metadata with `sdlc diagnose --run RUN_ID --json`; raw logs, comments, commands and credentials stay outside the report.

### Changed

- Journals containing provisional PR-feedback assessments or their retained history require beta.49 or newer. Existing feedback journals remain readable and retain their history.
- CLI-only installation also delivers the matching publisher helper assets; it leaves the runtime image unchanged.

## 0.1.0-beta.48 - 2026-10-13

### Added

- Enable the durable run-limit override at a verified ready checkpoint before another PR-feedback cycle begins. The command records permission without starting work, preserves cumulative accounting and the original session, and carries the override into later feedback cycles. Provider, input, verification, signing and review gates remain enforced.

## 0.1.0-beta.47 - 2026-10-13

### Fixed

- Allow check-only input capture for a saved feature run after the project's current check configuration changes. Use the run's frozen settings and retain every requirement and check-file hash; continue checking the configuration hash when that file is itself a recorded input. Provider input supplements and feature dispatch retain their existing configuration guard.

## 0.1.0-beta.46 - 2026-10-13

### Fixed

- Honour a saved run-limit override during formatter preparation, so a pending correction can reach the original implementation session without granting or resetting another allowance. Recognise corroborated formatter repair holds when explicitly requesting an override; ordinary runs retain their repair ceiling.

### Added

- Add explicit check-only input recovery for stopped, unpublished implementation runs with `sdlc inputs --run RUN_ID --add-check-input RELATIVE_PATH`. Snapshot selected files into the private check-input directory, audit their hashes and invalidate verification while preserving the original state, sessions, counters and limits. Keep those bytes out of provider workspaces, review snapshots and publication; retain immutable interrupted recovery and input resource bounds.

### Changed

- Journals with check-only recovery records require beta.46 or newer. Existing provider-input recoveries remain readable.

## 0.1.0-beta.45 - 2026-10-13

### Added

- Resume one saved run at a corroborated local numeric hold with `--override-run-limits`. Persist permission to continue past native-call, configured-check repair, CI repair, review-handoff, repeated-feedback, retained-verification-ID and PR metadata correction ceilings, preserving the original session, accounts, models, history, evidence and cumulative accounting.
- Remove the implicit two-hour controller deadline for overridden runs and later ordinary resumes. Explicit timeouts, parent deadlines, cancellation, provider holds, verification, signing, exact-head CI, fresh review, human merge and same-head guards remain enforced, along with per-response and journal resource bounds.

### Fixed

- Admit verification batches atomically, including replacements and baseline-downgrade failures. Recover a saved outcome stopped only by the retained request limit without another model call, preserving already admitted stable IDs and ordinary companion-source and PR feedback handling.

### Changed

- Journals with a persisted run-limit override require beta.45 or newer. Saved runs without an override remain readable and keep their existing defaults; unknown prior configured ceilings remain unknown in the audit.

## 0.1.0-beta.44 - 2026-10-13

### Added

- Prepare formatting before full repository checks. Discover supported formatters from committed candidate metadata, starting with pinned CSharpier 1.x, and format changed C# files from the repository root in an isolated source copy. Return bounded corrections and hashes to the original implementation session for review and commitment before running the full suite; reuse preparation for an unchanged candidate and preserve frozen checks and repair limits.

### Changed

- Journals containing formatter preparation evidence require beta.44 or newer. Existing saved runs remain readable and receive preparation without changing their frozen check selection.

## 0.1.0-beta.43 - 2026-10-13

### Added

- Resume a ready saved PR with `--pr-feedback` to assess new or changed human comments in the original implementation session. Retain comment context, correction commits, signed commit mappings and reply acknowledgements; show unresolved feedback in the handoff. Corrections pass repository checks, signing, publication, CI and a fresh independent review before returning for human review, with saved accounts, models and budgets preserved.

### Changed

- Feedback-cycle journals require beta.43 or newer. Existing journals remain readable; keep the newer CLI when resuming a run that has begun PR feedback processing.

## 0.1.0-beta.42 - 2026-10-13

### Added

- Allow one explicit extension of 1–24 native turns for a saved run stopped at its original 24-turn ceiling. Resume keeps the same implementation session and cumulative counters, with a persisted audit and a shared ceiling for implementation and independent review. Input integrity, cleanup, provider holds and all check, CI and review allowances remain enforced.

## 0.1.0-beta.41 - 2026-10-13

### Changed

- Allow independent ticket sessions to call the same provider concurrently across folders and work references. Remove provider execution reservations while retaining runtime protection and ownership checks for live versus orphaned containers.
- Give native clients private writable authentication snapshots. Protect shared snapshots and opaque refresh persistence with brief transactions; preserve recovery material on conflicting refreshes, failed transfers or later login changes.
- Refill feature ticket capacity as work finishes, preserving priority, dependency and path-overlap rules. Keep ready-ticket repairs coordinated with active work so reconciliation cannot exceed the feature limit or start children from stale parent evidence.

## 0.1.0-beta.40 - 2026-10-13

### Fixed

- Give independent review findings a separate persisted allowance of three handoffs to the original implementation session, preserving configured-check and CI accounting and the overall native-turn ceiling. The implementor assesses each finding, fixes justified defects, explains evidence-backed disagreement, or asks a genuinely unresolved human question; finding priority alone does not demand human input.
- Preserve the reviewed publication boundary and findings through diagnostic turns, and send a bounded final assessment to a fresh independent reviewer without raw command output. Repeated findings on an already assessed published head cannot grant another implementation handoff.
- Add an explicit one-time legacy completed-review recovery with an allowance of one to three handoffs. Validate the current candidate, published tree, CI and inputs before forwarding the stored finding, without repeating the completed review or resetting history.

## 0.1.0-beta.39 - 2026-10-13

### Changed

- Return bounded actual command output from isolated checks to the original implementation session, including unfamiliar failures and successful diagnostics. Include command exit status, tested revision and explicit head/tail truncation instead of prescribing repairs for recognised errors. Preserve operational holds and keep command output out of independent-review packets and checkpoint exports.
- Discover checks from committed project metadata before each checking phase for projects using runtime selection. New initialization records that selection without saving an inferred command snapshot. Preserve explicit and legacy suites, invalidate evidence when effective commands change, and stop when discovery is ambiguous or requires Docker access absent from the saved run.
- Return newly completed verification to the original session for assessment before publication, while retaining the existing session-turn and repair bounds.
- Require current provider-documentation review for sign-in attempts and provider-related implementation changes, rather than routine SDLC CLI commands.

## 0.1.0-beta.38 - 2026-10-13

### Fixed

- Identify Verify snapshot comparisons, including wrapped test-platform exceptions without source frames, and return fixed byte-comparison guidance while keeping assertion data private. Explain that each check command receives a fresh temporary directory and that reports must be inspected in the same command or retained in the request's private HOME or workspace.
- Run fresh diagnostics against unchanged candidates without repeating a completed failed configured suite. Preserve its safe repair feedback and failed evidence, invalidate reuse when the verification boundary changes or cleanup fails, and charge each diagnostic cycle to the existing repair bound.
- Close the official native quota-probe input stream and allow a bounded natural exit before termination. Retain fixed shutdown modes and bounded exit codes in private usage records so natural exit, termination and forced termination can be distinguished without retaining provider messages or account data.

## 0.1.0-beta.37 - 2026-10-13

### Fixed

- Start status reporting before saving an explicitly initialized CI repair budget, so legacy CI recovery can reach the original worker without a startup panic. Preserve a budget already saved by an interrupted initialization and continue through ordinary resume.

## 0.1.0-beta.36 - 2026-10-13

### Fixed

- Read failed Sonar check annotations through the trusted GitHub CLI controller and return bounded diagnostics tied to the exact published revision and committed source locations. Keep raw annotation messages private and require the original implementation session to repair, verify and publish the change.
- Give CI repairs a finite allowance separate from earlier repository-check repairs. Preserve cumulative history and the implementation-turn ceiling, stop unchanged published revisions, and provide one explicit recovery for legacy runs stopped before their first CI repair.

## 0.1.0-beta.35 - 2026-10-13

### Fixed

- Include approved committed C# source locations from .NET stack frames in check feedback, so workers can investigate test failures without receiving method arguments or private assertion messages. Preserve existing diagnostic precedence and operational failure handling.

## 0.1.0-beta.34 - 2026-10-13

### Fixed

- Include bounded Sonar analyzer identifiers and committed source locations in C# check feedback, so repair workers can identify analyzer failures without receiving private diagnostic text. Preserve the existing first-error limit and source-path checks.

## 0.1.0-beta.33 - 2026-10-13

### Fixed

- Return fixed request-refinement guidance when a source correction exceeds the existing diff limit, so the original worker can split its named file selection. Keep oversized corrections unpublished and retain unexpected collection errors only in private diagnostic logs.

## 0.1.0-beta.32 - 2026-10-13

### Fixed

- Execute new additional verification requests even when earlier requests have failed on the same candidate. Keep those failures as publication blockers, avoid rerunning cached failures and retain the hold when no new machine work is pending.

## 0.1.0-beta.31 - 2026-10-13

### Fixed

- Return bounded source corrections from explicitly requested isolated formatter work to the original implementation session. Compare selected files with their committed originals, keep raw output and frozen check inputs private, and require review, a new commit and passing checks before publication.

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
