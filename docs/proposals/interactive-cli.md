# Interactive CLI refactoring proposal

Status: the opt-in first pass is implemented; later stages remain proposals. Research checked on 5 October 2026. See the [interactive shell guide](../interactive-shell.md) for the current command surface and validation limits.

Make `sdlc` open a persistent terminal application where every SDLC action is available through a slash command. The prompt should suggest commands, subcommands, flags, work references and tickets while the user types. Help, onboarding, inspection and run monitoring should share that prompt and its current project context.

Make the shell another frontend to today's CLI and services. Both interfaces must use the same parsers, validators, settings, execution, credential isolation, signing, publication, independent review and checkpoint rules. Extract only the seams needed to present their results interactively. New execution policy, scheduling, retention, automatic recovery and a general supervisor service are outside this UI refactor.

The deliverables in this directory are a design and implementation plan, plus a [visual prototype](interactive-cli/preview.html). The prototype uses invented local work and simulated statuses. It is not the refactored executable. See [research and sources](interactive-cli/research.md) for the product comparisons and [screenshot generation](interactive-cli/render.mjs) for reproducible PNG captures.

Agreed requirements: history defaults to the launch project; an explicit all-projects mode supports browsing and launching work across registered projects; closing the shell leaves accepted jobs running; onboarding explains current-project setup and remaining configuration. Starting work opens a separate run terminal in the background while the focused shell switches to monitoring, with no activation or flicker. Use Robby Russell throughout, matching zsh in iTerm2 and inheriting its font and colours. Version reporting must distinguish source, executable and runtime state. The beta baseline and increment policy are specified in the [versioning proposal](beta-versioning.md). The first pass implements the source baseline; native background-launch acceptance remains pending.

### What is presentation and what requires an extension

| Requirement | Smallest implementation boundary |
| --- | --- |
| Slash commands, completion, help, onboarding and Robby Russell prompt | Shell state and presentation over existing command parsers/configuration operations. |
| Project/default history and all-projects views | Filter existing registry views by canonical project root before ordering/paging; select an explicit root for the existing command adapter. |
| Attention, concurrency and recovery | Display current states, queue reasons, provider leases, `--parallel` and resume actions. Preserve their current semantics. |
| Conversation inspection | A bounded, read-only parser over existing private native event files. Show available messages and gaps; more complete future capture is optional separate work. |
| Clear all | Review a fixed set, invoke existing `Registry.Forget` per eligible entry, and retain private locators for later inspection. Introduce no automatic purge or new retention policy. |
| Jobs launched inside the shell survive its exit | Open the existing run controller in a separate terminal without activating it; keep the shell focused and monitor existing records. Prove focus and lifetime per adapter; invisible background execution is separate backend work. |
| Version clarity and beta changelog | Present current build/runtime evidence; a small separate version-source/changelog iteration benefits both interfaces. No new artifact registry or upgrade service is required for the UI. |

The smallest release preserves the existing backend contracts and adds these requested adapters. A full local supervisor, control protocol, reboot recovery service, global scheduler and state migration framework are deferred to their separate proposals.

## Current behaviour and refactoring scope

The current [dispatcher](../../cmd/sdlc/main.go) prints usage when called without arguments. It then routes separate commands to handlers that mix flag parsing, service construction, terminal I/O and execution. There is no command registry, persistent SDLC prompt or completion engine. `sdlc interactive` opens an official provider session in an empty disposable workspace; it is a different capability from the proposed SDLC application.

Useful services already exist:

| Responsibility | Existing implementation | Refactoring use |
| --- | --- | --- |
| Project and ticket validation | `internal/project`, `cmd/sdlc/work.go` | Discover the project, resolve exact references and tickets, validate launch inputs. |
| Single ticket and feature execution | `internal/workrun`, `internal/workseries`, `cmd/sdlc/run.go`, `cmd/sdlc/series.go` | Retain runner locks, frozen settings, dependency scheduling and journals. |
| Monitoring and history | `internal/runstatus`, `internal/dashboard`, `cmd/sdlc/dashboard.go` | Feed typed snapshots into the terminal views. |
| Provider sessions | `internal/providerauth` | Hand the terminal to the unmodified official client for login or interactive use. |
| GitHub and signing | `internal/githubauth`, `internal/githubprofile`, `internal/signing` | Retain profile storage, pairing and isolated verification. |
| Runtime and instructions | `internal/runtimeimage`, `internal/runtimeupdates`, `internal/instructions` | Reuse build, update, status and instruction operations. |

The [unified onboarding proposal](unified-onboarding.md) remains the authority for provisioning and storage. `/onboard` supplies its proposed unified navigation inside this application. The [work reference proposal](pr-manage-work-reference.md) remains the authority for preserving ticket and reference identity through publication. Neither proposal should be presented as already implemented.

This design includes the three recent `main` commits through `95cf565`:

| Commit | Behaviour the new interface must preserve |
| --- | --- |
| `169eb16` | Local GitHub profile listing, connected profile selection, privately remembered repository identity and explicit selection provenance. |
| `ac1702b` | Committed `.env.example` templates survive source capture when allowed by parent-directory rules; private and untracked environment files remain excluded. |
| `95cf565` | Preparation failures appear in dashboard history before a journal exists, with safe feature-owner retry and new-run recovery. |

## Research informing the design

Codex documents a slash popup filtered by typing, `@` file search, prompt history and saved-session pickers. Its launch screen identifies the model and directory and offers commands to get started. These are useful precedents for discoverable actions and visible context. See the [official command reference](https://learn.chatgpt.com/docs/developer-commands?surface=cli) and [CLI overview](https://learn.chatgpt.com/docs/codex/cli).

Claude Code documents filtered slash commands, file-path completion through `@`, and skill argument hints. Gemini CLI documents nested slash commands, searchable saved sessions and suggestion acceptance with Tab or Enter. These support completion and progressive help. Ticket lookup, SDLC context defaults and the launch review below are proposed SDLC behaviour, inferred from those patterns. See [Claude interactive mode](https://code.claude.com/docs/en/interactive-mode), [skill argument hints](https://code.claude.com/docs/en/skills#frontmatter-reference), [Gemini commands](https://geminicli.com/docs/reference/commands/) and [Gemini keyboard shortcuts](https://geminicli.com/docs/reference/keyboard-shortcuts/).

Use the whole active terminal window, with an application transcript, stable command area, brief context and a completion panel. Reserve large detail views for explicit inspection, help and monitoring. Redraw and reflow when the terminal is resized. The rendering spike must prove terminal ownership, scrolling and restoration; the HTML prototype demonstrates layout and browser resizing, not native terminal rendering performance.

## Launch and application context

The final default is:

```text
$ sdlc

SDLC
sample-api · main · reference DEMO-42
Provider codex · reviewer claude · stored login, not verified

/help       Browse commands and examples
/onboard    Continue setup
/reference  Choose local work
/dashboard  Inspect runs and checkpoints

› /_
```

Resolve project context from the launch directory using the current safe project discovery. Launching must not initialise the repository, edit its exclude rules, fetch source, start Docker, read ticket bodies, contact a provider, retrieve a signing key or check upstream releases. Show unknown or unchecked values explicitly. Outside a Git repository, help and installation setup remain available; project actions explain how to open a project.

Context includes the canonical project root, selected opaque work reference, selected ticket, provider default and privately configured GitHub selection. The provider default remains `codex`. Explicit command flags take precedence. Repository identity follows the current resolver: explicit `--repo`, saved checkout selection, saved repository identity, then Git origin. Show the source of that selection and report unsafe, changed or ambiguous metadata instead of silently falling back. Changing the reference clears the selected ticket. Inspecting a ticket in another reference does not silently change the selected reference. Completion displays the full target for any cross-reference operation.

Store non-secret application preferences and onboarding progress in versioned private host state, outside the source checkout. A remembered reference is a suggestion that must be revalidated. Never resume a controller or a login automatically at launch. Show last activity and recovery actions instead.

### Project context and all-projects mode

Separate the active project, which supplies defaults for new actions, from the visible history scope. Every launch starts with project scope when safe discovery identifies a project, even if a previous shell used all-projects mode. Scope applies to dashboard history, archived conversations, resume suggestions and command-history search. The prompt and every view show the active project and scope. Outside a project, offer the project picker; all-projects browsing is an explicit choice.

`/projects` lists the launch project and privately registered project roots with local readiness and recorded activity. `/project add PATH` reviews and registers a validated root; `/project select NAME` switches active context; `/project remove NAME` removes only the private locator. Do not crawl the user's filesystem or infer roots from remote repository names. Canonical checkout roots distinguish separate clones and worktrees, with a private stable key and a friendly display name. Revalidate roots, ownership and saved identity before use; a missing or changed root is unavailable until explicitly resolved. Removing a locator neither stops jobs nor deletes their history.

`/scope all` shows jobs and history across projects known to this installation; `/scope project` restores the active project's view. Scope changes do not change the active project, expand completion reads into every checkout, or change mutation scope. Ticket/reference completion remains within the active project. Run-ID completion in all-projects mode includes project/root labels; ambiguous prefixes must be expanded to a unique run identity across the selected scope.

Several projects can have jobs running simultaneously. Select another project explicitly to prepare its next job; an all-projects row's Run here action first reviews that context switch, then prepares a draft. New launch reviews always show the target project/root and repository selection provenance. Switching context clears selected ticket/reference and project-specific drafts after offering to retain or discard the draft; saved draft state is partitioned by project. Existing jobs retain their frozen project, inputs, repository/profile, runtime and source. Inspection or answering a job in another project uses that job's identity without changing browsing context. A project switch while a launch review is open invalidates the review and requires new preparation.

Bulk clear still defaults to the active project even in an all-projects view. Installation-wide removal requires the explicit destructive scope in its own review. Command-history entries retain originating project identity; choosing an entry from all-projects search shows its source and requires explicit context selection before a project action can execute.

Introduce `sdlc shell` first as an opt-in entry point during migration. After parity checks pass, bare `sdlc` enters it when stdin and stdout are terminals and the terminal is supported. Bare `sdlc` with redirected I/O or `TERM=dumb` keeps the current usage output and exit behaviour. `sdlc shell` without terminal I/O reports that an interactive terminal is required and exits with code 2. For an unsupported rich terminal, offer `sdlc shell --plain`; it still requires terminal I/O but accepts `TERM=dumb`. `sdlc --help`, `sdlc help`, `sdlc --version`, `sdlc version` and every existing explicit command keep their current contracts.

## Command vocabulary and parity

Mirror the existing verbs, subcommands and named flags. Do not create a competing shorthand for each service. All action families are available from `/`, including actions unavailable in the current context; these show their requirement. Installation of the executable remains the existing external installer because an application cannot be its own first-run installer.

The [supporting CLI contracts](interactive-cli/cli-contract.md) specify the small additions needed for this layer: structured offline plans and metadata, background terminal launch/acknowledgement, scoped following, local inspection and the requested conversation/clear adapters. These operations remain usable from the core CLI. Both frontends call the same command operations; subprocesses are needed for the independent controller, not every local read. Draft clearing and history scope stay in the shell.

| Slash command | Existing equivalent or proposed behaviour |
| --- | --- |
| `/help [command path]` | New searchable command catalogue, examples, keybindings and contextual guidance. `/?` is its alias. |
| `/onboard [resume]` | New unified setup and repair flow using existing setup operations. |
| `/projects` | New private project picker, readiness and recorded activity overview. |
| `/project add PATH`, `/project select NAME`, `/project remove NAME` | New reviewed registration and active-context selection; removing a locator preserves work/history. |
| `/scope project\|all` | New history/view scope; defaults to the launch project and never widens action scope. |
| `/reference [NAME]` | New picker or exact selection of a local work folder. Without a name, opens the picker. |
| `/inspect TARGET` | New local preview of an explicitly selected ticket, reference, approved file or run. |
| `/init` | `sdlc init`; review its intended project settings and ignore changes before applying. |
| `/work [--reference NAME]` | `sdlc work`; omitted reference uses selected context or opens its picker. Lists filename order only. |
| `/run FLAGS` | Plan through `sdlc run --dry-run --json`; after Start, use the proposed `--terminal background --json` adapter with the same run flags. Keep the shell focused and switch to its monitor. |
| `/dashboard FLAGS` | `sdlc dashboard`; opens a view without taking over the application prompt. |
| `/dashboard forget FLAGS` | Same as current `forget`; retain current `remove` alias. Review local deletion. |
| `/dashboard export FLAGS` | Same private export, with a destination review. |
| `/dashboard conversation --run RUN_ID` | New local viewer for recorded agent messages and tool events, grouped by attempt and role. |
| `/dashboard clear --all [--scope project\|installation]` | New reviewed bulk removal of stopped dashboard registrations; defaults to current project and retains saved conversations and work. |
| `/interactive FLAGS` | Existing official provider session; temporarily hands over terminal ownership. |
| `/auth login FLAGS` | Existing provider or GitHub login, through the official client. |
| `/auth status FLAGS` | Existing local or explicitly connected status checks. |
| `/auth logout --service github FLAGS` | Existing GitHub logout only. Provider logout is not currently supported. |
| `/github pair`, `/github list`, `/github use`, `/github status` | Existing pairing/selection rules; list is local, use verifies connected access before saving selection. |
| `/signing setup`, `/signing configure`, `/signing status`, `/signing verify` | Existing signing operations and private storage. |
| `/runtime build`, `/runtime status`, `/runtime update` | Existing build/status/update behaviour, including connected metadata checks. |
| `/instructions show`, `/instructions set`, `/instructions reset` | Existing instruction operations. |
| `/version` | New local version overview: running/installed executable, selected source/build and runtime identities. Existing external version text remains compatible. |
| `/clear` | Clear the visible application transcript; preserve run journals. |
| `/exit` | Close the shell view; independently launched controllers continue. `/quit` is its alias. Stopping uses the controller's existing mechanism. |

The registry must represent every existing flag, not only commonly used flags:

| Family | Required flag coverage |
| --- | --- |
| Run | `--reference`, `--ticket`, `--all`, `--parallel`, `--watch`, `--alternate-providers`, `--provider`, `--github-profile`, repeatable `--input`, `--base`, `--branch`, `--repo`, `--model`, `--effort`, `--review-model`, `--review-effort`, `--resume`, `--answer-file`, `--docker-tests`, `--dry-run`, `--timeout`, `--notify`, `--sound`. |
| Dashboard | `--once`, `--watch`, `--json`, `--logs`, `--run`, `--page`, `--interval`, `--notify`, `--sound`; forget/remove uses `--run`; export uses `--run`, `--to`. |
| Provider interactive | `--provider`, Codex `--approval`, Claude `--permission-mode`; keep provider-specific validation and current defaults. |
| Auth | `--provider`, status-only `--all`, GitHub `--service github`, `--profile`, status-only `--verify`. |
| GitHub | `--profile`, `--signing-profile`, `--repo`, `--replace`, `--verify`, scoped to their current subcommands. List accepts no flags; use can omit profile when the existing resolver establishes an unambiguous selection. |
| Signing | `--profile`, `--provider`, `--replace`, `--bootstrap-file`, `--file`, `--verify`, `--show-config`, scoped to their current subcommands. |
| Runtime | Build/update `--source`; update `--dry-run`; status `--offline`, `--all`, `--github-profile`. |
| Instructions and work | Set `--file`; work `--reference`; init has no arguments. |

`/help run` includes both single-ticket and feature examples and the current constraints: `--all` cannot combine with ticket, branch, resume or answer-file; parallelism is 1 through 8; a resume retains frozen settings. Context must not inject a provider, profile or selected ticket into `--resume`, or inject a selected ticket into `--all`.

## Completion and input rules

The command line is deterministic. Ordinary text displays guidance to use `/help`, `/run` or `/interactive`; it does not make a model call. There is no `!` shell escape in the SDLC prompt. Slash notation resembles skills, but built-in operations are typed application commands, not prompts interpreted by a model. Custom skill execution is a separate future feature.

Use one incremental parser for validation and suggestions. Parse command paths, named arguments, booleans, repeatable values and quoted strings. Accept single or double quotes for names with spaces and escaped quote/backslash characters; preserve argument bytes after unquoting. Do not expand environment variables, tildes, glob patterns, shell substitutions, pipes or redirects. Distinguish unfinished input from invalid submitted input. Dispatch typed requests directly; never construct shell command strings from the prompt.

| Input state | Behaviour |
| --- | --- |
| `/` | Show grouped commands with one-line descriptions. |
| `/ru` | Filter to `/run` and `/runtime`; highlight the best match without executing. |
| `/auth ` | Suggest `login`, `status`, `logout`; explain GitHub-only logout. |
| `/run --` | Suggest legal flags, suppress exhausted non-repeatable flags and explain incompatible choices. |
| `/run --provider ` | Offer only `codex` and `claude`. |
| `/reference D` | Match available local references by name; retain exact case and identity on insertion. |
| `/run --ticket @DEMO-42/0` | Offer numbered tickets from the explicitly named reference. |
| `/run --ticket 0` | Complete filenames within the selected reference. |
| `/run --input @file:` | Complete bounded project files; require explicit inclusion before reading or sending contents. |
| `/run --resume ` | Offer saved run IDs with project, reference, ticket, status and last activity. |
| Invalid completed input | Keep the draft, underline the argument and offer an insertion or help action. |

`@DEMO-42/01-count-items.md` is a typed local ticket locator. Resolve it to the exact reference plus basename before the existing launch validator runs. `@"DEMO 42/01-count-items.md"` handles a reference containing spaces. `/inspect @ref:DEMO-42`, `/inspect @run:RUN_ID` and `/inspect @file:docs/specification.md` disambiguate other targets. Bare ticket filenames remain valid with selected context. To address a literal filename beginning with `@`, use its quoted relative path under `--input`; explicit `@file:` locators are recommended for interactive file selection.

An explicit `--reference` must agree with a ticket locator's reference. A mismatch produces an error before reads or writes. Matching can ignore case for search ranking, but must insert and validate the actual case. Never choose the first ambiguous match, normalise two distinct references into one ID, or reduce a ticket to its numeric prefix.

Keyboard precedence is part of the contract:

| State | Tab | Enter | Escape | Ctrl+C |
| --- | --- | --- | --- | --- |
| Completion open | Insert highlighted suggestion. | Insert suggestion and close the menu; never dispatch in this state. | Dismiss; retain draft. | Clear draft/menu. |
| Draft without menu | Insert available completion or leave draft. | Validate and dispatch a complete command. | Clear ghost suggestion. | Clear draft. |
| Picker | Move focus forward. | Select and return to reviewable input or explicit inspection. | Return without selection. | Cancel picker. |
| Form or launch review | Move focus. | Activate focused control; only an explicit Apply/Start initiates effects. | Return to draft without effects. | Cancel review. |
| Observed job active | Complete commands. | Dispatch commands through normal preparation, including independent jobs. | Dismiss overlays without stopping work. | Clear draft or cancel the shell's local query; never stop a job. |
| Idle empty prompt | No action. | No action. | No action. | Detach and exit; jobs continue. |

Arrow keys navigate an open menu. With no menu, Up/Down recall command history; Ctrl+R searches allowed history. Right accepts only the visible ghost suffix at the end of the draft. Multi-line paste stays a draft, displays a paste summary and never executes. Do not persist commands containing private paths, inline answers or secret-entry data. Secret fields are excluded from command history and transcripts entirely.

## Ticket and reference IntelliSense

Discover only `.sdlc/work/<reference>/tickets/` using the current ignored/untracked and filesystem-safety rules. The new reference index is metadata-only and must reuse the project validators rather than bypassing them. A suggestion shows exact identity, filename-derived label, numeric order, source and the age of the local snapshot. For example, `01-count-items.md` may display “Count items · label from filename”. That label is not a parsed ticket title.

Numeric order does not establish readiness, dependencies, requirements or completion. Show “not inspected” for content and “not assessed” for readiness. The existing feature planner can supply validated dependency metadata after an explicit `/work --reference NAME` detail action or feature preview. Label its evidence as `plan.json`, and distinguish recorded run status from ticket eligibility.

The completion index must not read ticket Markdown, specifications, secret configuration, vaults or remote issue trackers. Search local filenames and available non-secret run metadata only. First browse computes a bounded snapshot asynchronously. Reuse it within the application; refresh on explicit navigation, command completion and a short expiry. Directory watching is optional after measurement. Failed refreshes show stale data with its timestamp and error, then revalidate any selected object at dispatch.

Proposed limits are 50 visible suggestions, paged results for larger collections, cancellable indexing, and initial limits of 1,000 references and 10,000 ticket filenames. Exceeding a limit shows the count boundary and a request to narrow the reference; do not imply a complete catalogue. Target cached completion latency is under 50 ms at p95 on this fixture size. This is an acceptance target, not a measured result.

`/inspect` is the explicit content-read boundary. Read only the selected safe local ticket or file, with a size cap of 256 KiB initially, and show truncation. Display the real path identity and escaped Markdown as terminal text. Local plan links may be shown when validated. Linked documents are not recursively opened; the user selects an allowed local file explicitly. URLs are displayed as source text and never fetched by hovering or completing. `/inspect` does not send contents to a provider or launch a run.

Rerun project, ignore, tracking and link checks before opening content and again before launch. Refuse symlinks, traversal, unsafe reference directories, stale targets, tracked private work and malformed or duplicate ticket numbers. Use safe file opens and compare identity after opening to address filesystem races. Apply existing dashboard terminal sanitisation to filenames, Markdown, error messages, provider output and run metadata; strip ANSI/OSC control sequences supplied by content, escape controls and bound line lengths.

## Help and onboarding

`/help` opens a searchable catalogue within the application. Search by command, action or ordinary synonyms such as “login”, “tickets”, “resume” and “sign”. The detail pane shows syntax, scope, arguments, defaults, side effects, prerequisites and runnable examples. Selecting an example inserts it into the prompt. Help reads registry metadata and makes no connected requests. `/help auth login` and `COMMAND --help` resolve to the same entry.

Show a short first-use guide, then keep hints beside the prompt: `/ commands`, `Tab complete`, `@ references`, `Ctrl+R history`. Readiness failures link to the exact repair step; do not reopen the entire wizard for every missing prerequisite.

`/onboard` follows the existing unified onboarding design:

Open an overview for the active project before entering a step. Separate shared installation setup from project configuration and execution readiness. Show the project/root/repository, purpose of each step, current evidence, what remains to configure, next action and why it is needed. Built-in explanations come from the command registry; the overview may read explicitly selected safe project documentation with source labels when opened. Startup/completion must not read documentation or invoke a model to invent a project description.

Use `unchecked`, `needs configuration`, `configured`, `verified`, `blocked` and `not required` with evidence and verification age. A total such as “3 configuration steps complete; 1 connected check pending” must not imply execution eligibility. Make requirements specific to the intended action: missing reviewer login may permit initial implementation and later pause at independent review, as the current service allows. Offer Overview, Step details, Configure, Verify, Repair and Skip; each detail states purpose, instructions, current selection, scope, effects and completion evidence.

1. Check local prerequisites and runtime configuration. Building or checking upstream releases is an explicit action.
2. Select implementation provider and explain the opposite reviewer. Native account login is optional until needed and starts only when selected.
3. Configure or reuse GitHub login, signing profile and pairing through the existing native and isolated operations. List configured profiles locally; connected validity remains a separate result.
4. Review `/github use` to select or reuse the project's profile, verify repository access and save its private repository binding. Initialise the project after reviewing the proposed local changes.
5. Choose a work reference and ticket, inspect inputs, and reach the launch preview.

For shell-launched jobs, explain the iTerm2 background-session capability and any required automation setup during local prerequisites. Show its purpose, current access evidence and manual-launch fallback; do not change iTerm2 security settings or open a permission prompt during Start. The Robby Russell prompt requires no theme installation or shell configuration change.

Add a Versions panel to the overview with executable and runtime identities, source/build state and suggested repair. Explain installing the CLI, building the shared runtime and configuring a project as separate operations. A newer source checkout does not mean the installed command or Docker runtime has changed. Reuse the existing setup/configuration operations; the first release does not require rebuilding every provisioning mechanism in the separate unified-onboarding proposal.

Support Back, Next, Resume, Cancel, Skip and targeted Repair. Recheck persisted state rather than trusting a completed-step marker. A completed login is reusable; retrying a later step must not repeat it. Store only non-secret progress and atomically validate changes before replacing private profiles. Preserve prior configurations and shared bootstrap paths on cancellation or failure.

Use separate status text for local configuration, stored login, connected verification and execution readiness. Include a verification timestamp when one exists. “Stored” never becomes “authenticated” merely because a cache exists. The wizard does not collect provider tokens or implement refresh. Sensitive signing entry uses the current protected terminal flow and its existing storage explanation. See [provider usage](../provider-usage.md) and [unified onboarding](unified-onboarding.md) for account and bootstrap constraints.

## Versions and beta delivery

`/version` opens a local overview with evidence and an observation timestamp for each component:

| Component | Identity and meaning | Repair action |
| --- | --- | --- |
| Running CLI | Embedded application version, source revision/dirty state, executable identity, OS/architecture. This is what this shell is using. | Reopen the shell after installation; keep existing jobs pinned. |
| Installed CLI | The managed executable resolving first on PATH, its version/build identity and path. It can differ from the already running shell. | Review a CLI build/install action; never silently overwrite a different PATH executable. |
| Selected SDLC source | Canonical SDLC source checkout, declared version, commit and dirty state; a last-built artifact is separate evidence. This is not the active work project's version. | Select source, review changes, build/install deliberately. |
| Built CLI artifact | Recorded version/revision/hash and build time for an actual successful build, when available. Source declarations alone are not a build. | Build through the supported installer; keep a failed replacement from damaging the installed executable. |
| Local runtime | Application/source version recorded at image build, immutable image ID, build time, engine identity and tool inventory. `sdlc:local` is a mutable tag, not a version. | Explicitly build/update runtime and validate its manifest. |
| Active jobs | Runtime/source identity already captured in the journal; controller version only when recorded. Unknown fields stay unknown. | Show the existing resume and runtime guidance; never replace a running job's captured state. |

The three headline groups are Source and build, CLI installed and running, and Runtime built and in use. Report `unknown`, `not built`, `not installed`, `mixed` or `compatible` with concrete evidence; never collapse them into an unqualified “up to date”. Compare application version and revision, not timestamps or tag names alone. Distinguish a dirty source tree from a newer released version. Last local refresh and connected release-check time are separate.

Local refresh reads safe current build/installation/runtime evidence without network, credential retrieval or building Docker images. Use the embedded build identity, selected source revision/dirty state and existing `runtime.json` image/source records. Show an actual last-built artifact only if a reliable record is available; otherwise show not recorded. Never execute an arbitrary PATH candidate to discover a version. An optional later build-manifest extension can improve missing fields without blocking the UI. `/runtime status --offline` remains available; its existing connected counterpart is explicit. Explain mismatches and offer the existing repair commands through reviewed terminal handoff.

Start the release policy from the current `0.1.0-dev` baseline with proposed `0.1.0-beta.1`. Increment to `beta.2`, `beta.3` and so on for each validated, delivered application change or fix. Multiple preparation commits can belong to one iteration; rebuilding identical source does not consume another version. Keep a root changelog with Unreleased entries and dated, immutable delivered-version sections. Do not list this proposed shell as shipped. The [beta versioning plan](beta-versioning.md) separates the small version-source/changelog change from optional later metadata improvements.

## Launch review and active work

Submitting a valid `/run` produces a review of the resolved operation: project, source selection, reference and exact ticket or feature scope, provider/reviewer, requested models and efforts, profile names, checks, explicitly included inputs, base/branch, timeout, notification mode and intended draft PR publication. Separate locally known values from checks that will occur after Start. Show current native permission defaults for provider interactive sessions when those are launched.

The review is a local plan and must not retrieve credentials, make provider calls or publish. Expose a structured result from the existing offline planning path, adding only the local provenance/fingerprint fields needed for review. Do not create a new preparation engine. `/run --dry-run` retains its current offline planning semantics and displays its result without a Start action. Feature planning retains its existing metadata and dependency checks.

On Start, revalidate source, selected inputs and configuration generations. If they changed since review, rebuild the review and require another Start. Freeze the actual source and settings through the existing capture path, then show the captured identity. Preflight or eligibility failures remain explicit service errors; filename discovery alone cannot approve execution. Native authentication, account eligibility and service-limit failures remain stop conditions.

Retain the source capture rules introduced by commit `ac1702b`: preserve committed `.env.example` templates when their parent directories are not excluded; exclude untracked environment files and templates beneath excluded parents. Snapshot materialisation must use the same tracked-file exclusion rule and restore the captured index. The shell's preview describes these rules; it does not invent a new snapshot filter or read private environment values for completion.

Use the current `sdlc run` controller for execution. The first release opens it in a separate native terminal without activation, with an explicit project root and validated argument array. After Start, the shell retains keyboard focus and its alternate-screen buffer and immediately shows a Launching monitor; it does not hand over its terminal, flicker or bring the run terminal forward and then restore focus. Show the job as active only after its existing records acknowledge it. The secondary terminal displays today's controller output, while the shell can follow the same recorded activity. Keep the run terminal open while work is active. Closing the shell leaves it open; closing the run terminal can stop its controller. The existing command still owns preparation, capture, journals, locks, worker creation and delivery. Use the one-use launch receipt described in the [CLI contract](interactive-cli/cli-contract.md) to correlate preparation/run or feature identity, report failure or uncertain acceptance, and prevent duplicate submission. Feature parallelism, provider leases and queue behaviour remain unchanged.

Preparation failures are a distinct state, already recorded through `preparation.json`. Show the failing preflight/capture step and the existing legal retry route: the owning feature controller may retry, while single-ticket work starts a new run when required. Do not treat a preparation record as a provider session, invent a resumable journal or overwrite an existing run directory. Frozen repository, profile, signer and captured settings remain the authority after preparation succeeds.

Dashboard registrations can exist before a run journal is available. Use the existing preparation projection in `internal/runstatus/registry.go` and distinguish a preparation-only record from a missing or corrupt journal. A preparation-only row has no agent conversation yet; its detail view shows the recorded preparation evidence and legal recovery actions.

Show journal stages, checks, CI/review evidence, queue reasons and current human questions. Do not infer success from a finished spinner or invent token usage when native events omit it. A `/dashboard` view can follow logs, page runs, inspect a selected run and insert its legal resume command. `/dashboard --json` renders a bounded JSON result inside the transcript; the external command preserves machine-readable stdout.

For `waiting_for_human`, the detail view offers an answer form. On explicit submission, save the answer privately using the existing answer-file format, then prepare `/run --reference REFERENCE --ticket NUMBERED_FILE --resume RUN_ID --answer-file PRIVATE_FILE` through the same validator. Resolve project root, reference and ticket from the selected journal, not current context. A different project root must be shown explicitly in the resume review; execution uses that validated root without changing the browsing context. The form is a proposed adapter, not a new checkpoint format. Answers do not enter command history. Feature continuation repeats the feature command and respects its existing reconciliation semantics.

`/exit`, idle Ctrl+C, closure of the shell's terminal and SIGTERM close the shell view; a separate run terminal stays open and its controller continues there. Print continuing job IDs and how to reopen `/dashboard`; do not prompt to stop them. Ctrl+C with a draft clears it; a shell query or unaccepted launch preparation can be cancelled locally. A launch race must be reconciled against the existing preparation/run evidence before reporting cancellation. Native `/interactive` remains a directly attached provider session with its existing terminal lifetime.

Remote Stop is not an existing dashboard capability and is outside the first UI release. Display the recorded controller identity and the existing stop/recovery instructions rather than inventing control authority or a new IPC service. If direct shell stop is selected later, it is a separate backend change with authenticated ownership and acknowledgement requirements. Exiting a dashboard already leaves other controllers running; the existing `sdlc run` process itself is foreground. Preserve both contracts.

Prove each terminal/version adapter with offline tests: no transient focus change or flicker; immediate shell monitoring; UI exit/crash/closure of its terminal leaves the run terminal/controller alive; prompts and Ctrl+C in that terminal retain their existing behaviour; errors remain inspectable; existing journals and leases stay authoritative. Target iTerm2 first. Its documented unselected-tab API is a candidate; background-window behaviour still needs verification. Where support or permission is missing, keep the draft and offer a manual command rather than silently opening a foreground terminal. Preserve current notification validation, including rejecting `--notify bell` without a TTY. No native launch/focus test or connected provider trial has established this adapter; preserve current official-client/account boundaries and require an explicitly authorised trial before declaring connected support.

Invisible background controllers are optional later work. Redirecting their I/O removes the original terminal's stop mechanism, so that mode also needs verified process identity and a deliberate stop route. The registry's logical controller ID is not an OS process handle. Do not add a daemon/control protocol just to deliver the first shell view.

Sleep, logout, Docker restart and reboot retain current interruption/recovery semantics. Do not add automatic restart or resume, a new scheduler, active-controller replacement during installation, or a new retention policy. The broader trusted supervisor and remote-worker lifecycle remain in [unattended Docker delivery](unattended-docker-delivery.md) and [remote execution](remote-vm-execution.md), outside the scope of this view refactor.

## Dashboard controls and session conversations

`/dashboard` opens an in-app monitoring view with the command prompt still available. Its default history is scoped to the active project; `/scope all` explicitly exposes the existing installation-wide registry and attention ordering. Apply presentation filters before existing ordering/paging so totals remain accurate. Show active project, history scope, current filter and result count. Closing this view or the shell leaves independently owned jobs running. Attention states and notification settings use today's services; there is no new alert service.

Selecting a row opens the equivalent of `/dashboard --run 4b91de`. Retain full 24-character lowercase hexadecimal IDs and the current unique-prefix rule of at least six characters. The detail view shows Overview, Conversation, Checks and Publication sections. The UI uses “run” for the ticket controller and “native session” for each provider conversation; one run can contain implementation, repair, review and nested agent activity. A row's Open conversation action inserts or executes the explicit read-only command `/dashboard conversation --run 4b91de`.

The conversation view shows the messages and tool activity that played out, rather than only the current bounded log tail. Provide attempt/round navigation, implementation versus independent review filters, provider/session labels, message search, collapsed tool calls/results and follow/pause for active recorded events. Group repairs under the original implementation native session; independent review attempts start fresh. Include nested agent messages only when the recorded native events identify them. Preserve source order by attempt and event offset; do not invent timestamps or a global causal order when events lack them.

Existing `events-N.jsonl` files retain native output, but the current parser extracts outcome/session/telemetry fields rather than a conversation. Add a read-only `internal/conversation` adapter with provider-specific allowlists for visible assistant/user messages and tool events. Handle fragmented lines, duplicates, malformed entries, unknown event types, oversized records and a truncated final line. Keep original events private and untouched. Paginate from offsets rather than loading whole logs, cap each record initially at 1 MiB and each display page at 200 messages, and explain skipped/truncated material. Search operates on the selected conversation's bounded local index and reports its indexed extent.

The first conversation view reads only existing event/journal evidence. Derive roles and attempt boundaries from recorded fields when reliable; otherwise label them unknown. Do not add new prompt/feedback capture or change review inputs for this UI release. A later optional per-invocation manifest at `Runner.session` could improve completeness, but it is a separate recording change with private storage and credential-boundary review. Existing missing controller prompts remain explicitly unavailable.

Older runs may lack submitted prompts, role mappings or provider event fields. Display “Recorded events only; some messages unavailable” and the precise missing source. A cancelled run can have a partial conversation. This view shows available visible messages and tool activity, never reconstructs undisclosed reasoning or promises a complete transcript. Do not read undocumented native credential/configuration stores to fill gaps. Provider event schemas must be confirmed from official documentation and exercised with offline fixtures before implementation.

Apply safe local-file ownership, root anchoring, link checks, bounded parsing and terminal control sanitisation before display. Tool output may contain credentials or private code: hide recognised secret fields by default and mark redactions, without claiming pattern matching removes every secret. Conversation inspection is local and must not send content to providers or change the independent review's inputs. Keep `dashboard export` as its current bounded summary export, which excludes prompts, native IDs and raw logs; conversation export is outside this iteration.

“Clear all” opens `/dashboard clear --all` as a review. Its default scope is stopped runs in the current project, across every page and independently of temporary UI filters. Outside a project, require an explicit scope. `--scope installation` selects stopped registrations across all projects. Show project scope, eligible count, full selected IDs, unavailable/unsafe entries and active entries that will be skipped. Take a stable candidate snapshot; new entries after the review are not swept into it.

On Apply, reuse `Registry.Forget` for each selected registration and recheck each run's actual ownership lock and registration identity. A stale heartbeat alone never proves it is stopped. Preserve the current lock ordering; do not hold a global registry lock while waiting for a run lock. Report removed, skipped, changed and failed entries separately. A partial failure does not roll back successful removals or claim that everything cleared. No controller is stopped by Clear all.

The review states: “Remove these sessions from the dashboard. Saved conversations, checkpoints, inputs and workspaces remain.” Offer Cancel and Clear dashboard entries. `/clear` still clears only visible application output. This iteration does not erase run directories or native transcripts. To keep retained conversations discoverable after clearing, add a separately indexed Archived view backed by a private locator record, not directory crawling. Persist the locator safely before removing a registration; a failure to retain the locator blocks that removal. Archive entries are read-only pointers, do not re-register jobs on inspection, and are revalidated before reading. A later explicit resume may register a retained run again. Archive creation, interrupted bulk operations, expired/missing paths and duplicate live/archive identities need offline recovery tests.

## Rendering and terminal ownership

### Robby Russell continuity

Use Robby Russell as the sole prompt style, matching the zsh prompt in iTerm2: the same arrow and spacing, current directory, `git:(branch)` wrapper and dirty marker. Use the public [Robby Russell theme](https://github.com/ohmyzsh/ohmyzsh/blob/master/themes/robbyrussell.zsh-theme) as the implementation reference. Preserve its ANSI colour choices so iTerm2's configured palette gives the same visible colours. Status reflects the shell's last command result and current project/Git state; entering the shell must not introduce a different prompt shape.

Inherit iTerm2's current font, foreground/background and ANSI palette. Do not paint a fixed dark background or introduce coloured prompt segments. The browser prototype's font/palette are illustrative; native visual comparison against the existing zsh prompt is the acceptance check. Honour `NO_COLOR` and terminal capability fallbacks without adding a second theme.

Keep SDLC reference/provider/job status and keyboard hints in a separate status row, leaving the prompt's Robby Russell appearance intact. Completion, help and monitoring use restrained terminal text, the inherited palette and clear focus markers. There is no theme picker, `/theme` command, Powerline variant or alternate SDLC prompt. Matching the public theme requires no sourcing of `.zshrc`, prompt substitution execution or shell/profile changes. The style screenshot now compares zsh and SDLC with the same prompt.

### Renderer and terminal handoff

Recommend Bubble Tea with Bubbles and Lip Gloss for the Go terminal layer. Bubble Tea's documented model/update/view architecture fits asynchronous input and service events, and it supports inline and full-window operation. Confirm compatible current module versions and licences during the rendering spike, then pin them in `go.mod` and `go.sum`. No Go UI dependencies have been added by this proposal. See the [maintainer documentation](https://github.com/charmbracelet/bubbletea).

The rich application uses the terminal's full current width and height in an alternate screen buffer. It owns the transcript viewport, optional detail panes, completion panel, command input and status row. There is no fixed canvas or 80-column outer frame. Reserve the prompt/status rows first and give the remaining space to content. Long content scrolls inside its pane; it must never push input off screen. Avoid unmanaged service writes while the renderer owns the terminal. On exit or native-client handoff, restore the main buffer, terminal modes and original scrollback. Print a concise safe exit summary; saved run evidence remains available through `/dashboard` and explicit private exports rather than automatically dumping conversations into shell scrollback.

Read the terminal's actual cell dimensions on startup. Process framework window-size events, including Unix `SIGWINCH` and the supported Windows console path, through the UI update loop. Recompute pane widths, row budgets and wrapped lines, then redraw from the existing model. Coalesce bursts of resize events to the next render frame; never rerun a command, repeat a connected check, restart a controller or rebuild context because the window changed. After native-client handoff, obtain current dimensions before the first redraw.

Preserve the command draft and logical caret position, selected completion identity, selected run/attempt, search/filter, expanded message state and focused control. Anchor a paused conversation by message/event identity and offset rather than wrapped screen-line number; a following conversation stays attached to the tail. Clamp viewport offsets after reflow. A pane removed at a narrower breakpoint keeps its selection and is reachable through keyboard detail navigation; expanding restores the layout without discarding state. The spike is a release gate for full-window rendering, rapid resize, scrolling, paste and terminal restoration.

At 100 or more columns, show suggestions alongside details when the height budget allows. Between 80 and 99, stack a short selected detail beneath the list. Below 80, use a single list and explicit detail navigation. Cap completion height to the available rows and scroll its selection into view; collapse optional headers and hints in short windows. Below 60 columns or 16 rows, show compact size guidance with enlarge and plain-mode choices, preserving the draft and active work until the window grows. Switching to plain mode preserves controller ownership and requires deliberate selection. Plain interactive line mode uses the same registry, `/help` and explicit text selection. Honour `NO_COLOR`, provide ASCII borders, expose status in words and keep all actions accessible by keyboard.

Provider login, existing signing secret entry and `/interactive` need exclusive terminal ownership. Stop SDLC input/rendering, flush output, restore cooked mode and hand stdin/stdout/stderr to the existing official client or protected terminal adapter. Suspend incompatible foreground actions first. On child exit, cancellation, launch failure or panic, restore the selected renderer's terminal mode (raw mode for the rich UI, its existing line-input mode for plain rendering), redraw SDLC and refresh only local status. SDLC slash commands do not intercept the provider's own slash commands while that client owns the terminal. Do not proxy OAuth or replace native permission prompts.

## Application architecture

Keep the existing services and CLI command handlers as the behaviour owners. Add a shell with command/view adapters and extract only the small parsing/result seams required by a second frontend. Avoid a new general application framework or one package per screen.

```text
cmd/sdlc
  existing argv parsers/handlers ──────────> existing services
  shell command/result adapters ──────────> same parsers/handlers/services
                  ^
                  |
internal/shell    prompt, command metadata, local catalogue, project preferences,
                  dashboard/help/onboarding/version views, terminal handoff
                  background run-terminal launch adapter + one-use receipt
                  |
                  v
internal/dashboard + internal/runstatus   existing registry/order/page/detail/forget
                  bounded read-only conversation projection over existing events
```

Keep a small command definition for completion/help and callbacks into the existing operations. Suggested design pseudocode:

```go
type CommandSpec struct {
    Path []string
    Summary string
    Arguments []ArgumentSpec // types, completion source, repetition, conflicts
    Effects []Effect         // local read/write, network, provider, publication
    Examples []string
}

type ShellOperations interface {
    Preview(context.Context, ProjectRoot, CommandArgs) (LaunchSummary, error)
    Invoke(context.Context, ProjectRoot, CommandArgs) (CommandResult, error)
    LaunchController(context.Context, ProjectRoot, CommandArgs) (LaunchResult, error)
}
```

Preview exposes the existing offline plan with safe local provenance and fingerprints. Invocation/launch runs the existing parser and service checks again. A stale preview returns to review. The command additions in the [CLI contract](interactive-cli/cli-contract.md) expose these same operations for scripts and future frontends. Help/completion stay metadata-only, inspection reads selected local content, and connected checks occur only through an explicit existing command action.

Refresh the dashboard through current authoritative registry snapshots; use a bounded UI update queue without inventing a job event protocol. Logs and conversation readers keep local byte/event offsets and report gaps or stale data. Opening, changing or closing a view never controls a run. Existing journals, preparation records and provider events remain the data authority.

Extract structured status values only where the UI needs them; preserve existing stdout/JSON renderers and exit behaviour. Operations needing native/protected terminal input use handoff. Avoid moving every command into a generic Prepare/Execute/event-bus abstraction. Keep source, runner, publication and credential packages free of UI dependencies. Shared command metadata/flag builders should reduce duplicate help and completion rules without reimplementing the service validators.

## Implementation sequence and acceptance

Each stage is a separate reviewable iteration on `main`, validated and committed before the next one. The visible interface becomes the default only after all parity and lifecycle gates pass.

| Stage | Work and likely files | Completion evidence |
| --- | --- | --- |
| 1 Shared command seams | Add structured metadata/results and the supporting CLI flags/subcommands around current parsers/handlers and offline plan. | Both interfaces use the same operation; existing text/JSON/exit behaviour passes; preview has no credential, network or publication effects. |
| 2 Terminal shell | Add `internal/shell`, opt-in entry point, full-window layout, command metadata, help, Robby Russell prompt and handoff. | Native PTY resize/state, scrolling, paste, panic recovery and platform probes pass; prompt matches zsh in iTerm2. |
| 3 Scoped views and catalogue | Reuse registry/order/page/detail; project picker/preferences, metadata completion and explicit inspection. | Filter-before-page totals, two-project identity, safe discovery, stale selection and bounded latency pass. |
| 4 Guided onboarding and version clarity | Show purpose/how-to steps, existing readiness/configuration and current build/runtime evidence. | Missing configuration and version drift are explained without automatic login/install/update; unknown records stay unknown. |
| 5 Conversation and bulk clear adapters | Add read-only native-event projection and reviewed calls to existing Forget, plus private retained locators. | Recorded messages and gaps are accurate; existing locks/errors/retention are preserved and partial clear results match review. |
| 6 Background run terminal | Add terminal launch/receipt CLI adapter; invoke the existing controller with explicit root/arguments in its own TTY. | No transient focus change/flicker; shell immediately monitors; fake controller survives UI exit/crash/closure of the shell terminal. Its own terminal and Ctrl+C work as before. Acknowledgement/unknown acceptance and feature identities are accurate; repeated requests never duplicate execution. Required before shell `/run` ships. |
| 7 Parity and default launch | Complete action/flag parity, plain mode and guides; switch bare TTY launch after gates. | Existing argv/redirected behaviours, separate-terminal lifetime and platform checks pass. |

The small beta constant/changelog iteration can be delivered separately. Optional richer artifact manifests, remote Stop, supervisor/reboot recovery and a new scheduler must not become prerequisites for the UI.

High-value verification scenarios are:

- Launch in a nested project, outside Git, a detached checkout and a new repository without HEAD. Launch itself changes no project or credential state.
- Register two clones/projects, switch context with a draft, browse all-projects history and inspect/answer another project's run. Keep exact target roots and frozen job identities; opening a new shell resets history scope to its launch project. A view filter never widens Clear all or launch scope.
- Type `/ru`, accept with Enter, then press Enter again; the first Enter only inserts. No selection, completion or pasted command starts a model or run.
- Compare native zsh and SDLC Robby Russell prompts in the same iTerm2 profile, including success/failure, clean/dirty Git state, nested directories and light/dark ANSI palettes. Check `NO_COLOR`, glyph fallback and readable completion focus. Preserve the same prompt before/after a draft, resize or job launch.
- Resize repeatedly across wide, stacked, compact and minimum-size layouts with completion open, a draft caret in the middle, a paused conversation and a live fake-provider run. Fill the current terminal, keep input reachable, preserve logical selections/scroll anchors and perform no extra service calls. Repeat after child handoff and restore terminal modes after panic.
- Select references containing spaces, case-distinct names and literal punctuation. A cross-reference locator and conflicting `--reference` stop before effects.
- Discover duplicate numbers, malformed ticket filenames, symlinked/tracked private work, missing references and files removed after completion. Errors remain visible and actionable.
- Read a ticket only through explicit inspection. Escape unsafe terminal controls; show truncation and avoid recursive or remote reads.
- Resume with a different current provider/reference selection. Frozen settings win; feature mode never inherits a selected single ticket.
- Select a saved repository binding or sole configured GitHub profile; show its provenance, keep list local and require explicit connected execution for use. Reject corrupt selection metadata. Preserve the committed `.env.example` capture exception and excluded-parent rules, and restore the snapshot index. Expose preparation-only dashboard records and their legal retry/new-run route without overwriting state.
- Change source or configuration between preview and Start. Execution returns to an updated review instead of using stale approval.
- Reuse a native login, interrupt onboarding/signing, restore terminal echo and preserve old/shared configuration. Test with disposable fake data and no provider network access.
- Start a noisy fake controller in a background terminal with focus-event observation and screen recording. The shell immediately shows Launching and then acknowledges the exact run/feature, without flicker or transient focus change. Test terminal already running/not running, multiple windows, missing permissions, two concurrent launches, duplicate callbacks and unknown acknowledgement. Receive a question, close/kill the shell and reopen from another project; the separate run terminal continues without restart. Closing that terminal retains current interruption/recovery behaviour. Check existing parallelism, leases, notifications and journals remain authoritative.
- Open onboarding for two projects with different repository/profile/check configuration. Explain each missing step, configuration versus verification and source/build/install/runtime drift. Upgrade the CLI without rebuilding the runtime, then rebuild runtime while existing jobs use the old image; show all identities accurately without automatic job migration.
- Inspect one run across implementation, repairs and fresh reviews, including interleaved feature workers. Compare visible messages/tools to existing event fixtures, preserve ordering and display missing/partial sources. Inspect archived conversations without restarting work. Preparation-only records show that no native conversation is available.
- Clear all while another controller resumes and a new registration appears. Remove only reviewed stopped registrations, retain conversation locators and work, skip unsafe/active entries and report any partial failure.
- Compare every external command family, status-only read path, JSON output, exit code and notification restriction against the existing offline suite.

Run the repository's ordinary offline Go tests, `go vet ./...`, CLI/installer target builds and Python probes as required by the touched code. Use isolated temporary caches where needed. Add focused PTY and behavioural tests for the new contracts; avoid screenshot-only tests as proof of execution safety. Connected provider/account trials require explicit authorisation and current official account/execution guidance. None has been performed for this proposal.

## Visual review

The [prototype gallery](interactive-cli/preview.html) shows sixteen interactions:

| Scene | What to review |
| --- | --- |
| Welcome | Visible project context and first useful commands. |
| Onboarding | Current-project overview, purpose/instructions, remaining configuration, verification and version guidance. |
| Command lookahead | Filtered `/ru` matches, argument guidance and insertion before execution. |
| Reference picker | Existing local work and exact reference identity. |
| Ticket lookahead | Typed ticket arguments, filename-derived labels and unassessed readiness. |
| Inspection | An explicitly opened ticket and local supporting references. |
| Launch review | Resolved scope and effects before Start. |
| Running | Background terminal request, focused shell monitoring and existing controller lifetime guidance. |
| Help | Searchable actions, syntax and examples. |
| Narrow recovery | Responsive missing-input guidance. |
| Dashboard | In-app run selection and history controls. |
| Session conversation | Visible agent messages and tool activity with provenance and completeness limits. |
| Clear all | Review stopped-session removal, active exclusions and retained conversations. |
| Prompt continuity | The same Robby Russell treatment in zsh and SDLC, inheriting iTerm2 font/palette. |
| Projects and scope | Current-project defaults, registered roots and explicit all-projects job browsing. |
| Version overview | Source/build, installed/running CLI and built/pinned runtime differences. |

PNG screenshots are local review artifacts in ignored `results/interactive-cli-design/`. They are generated from the HTML source by `render.mjs`; see its usage comment. The publication set contains the source and generator, not binary captures. Inspect the images as well as the source before sharing them. Their data is invented and carries no real work tickets, host paths, accounts or credentials.

The gallery's Terminal only link opens the viewport-filling prototype. Resize that window to see the live reflow. The screenshot generator captures all sixteen scenes in this view, an all-projects dashboard, ticket lookahead at 960 × 640, 720 × 520 and 1600 × 1100, and a filtered conversation at 960 × 640. The resize captures use the same loaded view without navigation, preserving its draft or filter. Browser smoke checks also cover 640 × 400; native cell sizing, operating-system resize events and terminal restoration remain stage 2 acceptance work.

Prototype validation on 5 October 2026 passed 61 browser assertions covering insertion before execution, reference/ticket selection, launch review and stop guidance, prompt focus after simulated Start, Robby Russell continuity with no theme picker, conversation filtering, retained history, live resize state, project scope, reviewed bulk-clear scope and simulated job lifetime. All 21 screenshots filled their viewport with a visible prompt and no horizontal overflow; the updated continuity and running screens were visually inspected alongside the project, onboarding and version screens. The browser blocked external network requests and reported no JavaScript errors. These checks validate the design prototype, not native iTerm2 focus, terminal launching or controller lifetime; the local screenshot manifest records capture dimensions and layout checks.

## Decisions carried into implementation

Proceed with a deterministic slash-command application over the existing CLI command operations, local metadata completion, explicit content inspection and guided launch reviews. Add the structured CLI contracts needed by those views. Use Robby Russell throughout and keep provider terminal sessions intact. Deliver through opt-in shell stages before changing the default.

The rendering spike must prove full-window redraw, resize-state preservation and terminal support, starting with iTerm2. Background terminal launch without focus change/flicker, independent controller lifetime and project isolation are required release gates for shell execution. Catalogue measurements must settle whether directory watching is necessary. Initial releases should not add conversational command inference, remote issue fetching during completion, arbitrary plugins, account switching to recover from limits, remote execution or new credential storage.

Recovery, parallelism, queueing, notifications and retention use today's functionality; they are not new product decisions required by the UI. Show those rules and their recorded evidence clearly. Broader supervision, automatic recovery, scheduling, remote Stop, permanent deletion and upgrade services belong in separate backend proposals. The beta baseline remains a small cross-cutting release change.
