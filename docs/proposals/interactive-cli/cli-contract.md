# CLI contracts supporting the interactive shell

Status: proposed on 5 October 2026. These additions are not implemented. The shell is another frontend to the CLI's command operations; the existing argv interface remains available.

## Shared behaviour

Keep parsing, validation, local state and execution in the existing command/service paths. Add structured results and metadata where presentation currently depends on printed text. CLI renderers and shell views consume those same results. The shell need not spawn a subprocess for each read operation, but every operation it depends on must also be accessible through the core CLI. Neither frontend scrapes the other's text output.

Preserve current commands, defaults, text output and exit behaviour unless an explicitly new flag or subcommand is selected. New structured outputs carry a schema version, send diagnostics to stderr and contain no terminal escape codes. One-shot JSON and streaming newline-delimited JSON have distinct contracts. Private paths and selected conversation content are local output; do not include credentials, ticket bodies or raw provider events in metadata responses.

The project is the command's working directory, resolved through existing discovery. The shell adapter passes its explicitly selected root as command context; scripts can use their normal working-directory control. A new global project flag is unnecessary for the first release. `/scope`, `/reference`, `/clear` and `/exit` are shell view/input controls; they do not introduce alternative execution policy. The sole prompt style is Robby Russell, matching zsh and inheriting iTerm2 font/palette; there is no theme command.

Read the external CLI's working directory at its entry boundary; pass that root explicitly into shared operations. Do not call process-global `chdir` when the shell selects another project. Subprocess adapters set their own working directory, so asynchronous reads or launches for two projects cannot change one another's target.

## Small additive surface

| Proposed CLI contract | Use and existing behaviour to retain |
| --- | --- |
| `sdlc shell [--plain]` | Opt-in terminal frontend before changing bare TTY launch. Explicit existing commands remain available. |
| `sdlc help [COMMAND_PATH] --json` | Shared command/flag metadata, descriptions, examples, conflicts and completion types. Existing human help remains unchanged. |
| `sdlc work --references --json` | Safe local reference metadata; mutually exclusive with `--reference`. No ticket bodies or filesystem crawling. |
| `sdlc work --reference REF --json` | Structured output from today's numeric ticket discovery. Keep current text mode and validation. |
| `sdlc inspect TARGET [--json]` | Explicit bounded local content/detail inspection using the same safe resolver as completion. Metadata completion never calls this automatically. |
| `sdlc project list/add/remove … [--json]` | Manage the private project locator list. Removing a locator preserves jobs and history. Project selection remains explicit shell context. |
| `sdlc onboard status --json` | Local project overview: each step's purpose, instructions, configuration evidence and remaining work. Reuse current configuration readers; unknown login/access remains unchecked until the user runs an existing verification command. |
| `sdlc version --details --json` | Current running/installed/source/runtime evidence and absent build records. Keep bare version text compatible. No install, rebuild or upstream request. |
| `sdlc run RUN_FLAGS --dry-run --json` | Structured result from today's offline single-ticket or feature plan, with provenance and unknown checks labelled. No new preparation engine or run ID allocation. |
| `sdlc run RUN_FLAGS --terminal background --json [--launch-id UUID]` | Start the existing controller in a separate terminal without activating it. New terminal flags select the launch adapter; bare `run` retains today's foreground behaviour. |
| `sdlc launch status --id UUID --json` | Read the private launch receipt, acceptance uncertainty and correlated existing run/feature identities. No process control or relaunch. |
| `sdlc launch execute --id UUID` | Internal terminal handoff helper: consume one owner-only request once and invoke the existing run operation. No arbitrary executable, arguments or caller-selected run ID. Normal output stays in the secondary terminal. |
| `sdlc dashboard --scope project\|installation …` | Add an explicit presentation filter before existing ordering/paging. Preserve the external command's current installation-wide default; the shell always requests project scope initially. Reuse existing `--json`, `--run` and `--page`. |
| `sdlc dashboard follow --run ID [--logs] [--json] [--cursor CURSOR]` | Follow existing status and bounded, sanitised output in text or NDJSON. Preserve one-shot `dashboard --json` and its current flag restrictions. Closing the reader never stops a job. |
| `sdlc dashboard conversation --run ID [--json] [--cursor CURSOR] [--follow]` | Read the bounded projection of existing native messages/tool events. Explain missing material and preparation-only records; never synthesise a transcript. |
| `sdlc dashboard clear --all [--scope project\|installation] --review --json` | Return a fixed list of eligible stopped registrations, active/skipped entries and retained-artifact effects. No removal. |
| `sdlc dashboard clear --run ID … [--scope project\|installation] --apply [--json]` | Apply only explicitly supplied full IDs from a review, using existing Forget checks per entry. Do not rediscover an all-scope set during Apply. Preserve artifacts and private conversation locators, report partial outcomes. |

`RUN_FLAGS` means the existing run flags, including `--all`, `--parallel`, `--watch` and explicit `--resume`. All current conflicts and frozen-setting rules still apply. `--dry-run` and `--terminal` are mutually exclusive. `run --json` is accepted only with the new plan or terminal-launch mode initially; foreground execution keeps its current terminal output. No public `--run-id` override is added.

For Clear, `--all` selects a review set and never combines with `--apply`. Apply requires repeated explicit full run IDs and validates the recorded scope again. The shell keeps that reviewed candidate set in its model; CLI callers pass the returned IDs deliberately. There is no second discovery pass or new clear-review state store.

Reference/ticket discovery and dashboard snapshots already have service implementations. Structured planning, terminal launch/receipt, project/onboarding views, conversation projection and bulk clear are the small missing adapters. Do not add a parallel `status` command, scheduler, generic event bus or supervisor API.

## Background terminal launch

After Start, keep the shell's terminal buffer, rendering loop and keyboard focus intact. Switch to a monitor view labelled Launching, retaining the command prompt. Request a separate native terminal session without activation. Do not hand over the shell's TTY, open a foreground window and restore focus, minimise a briefly focused window, or display the terminal's output in the shell through an attached subprocess. These approaches can cause the flicker or focus change the user wants to avoid.

The secondary terminal runs the same executable's existing `run` operation in its own TTY, with the exact validated project root and argv. It displays today's controller output. The shell independently follows existing records and bounded log output, so either location can be used to watch work. The secondary terminal remains available until the user closes it; use its existing Ctrl+C behaviour to stop the controller. Shell exit leaves that terminal alive. Closing the controller terminal, logout or reboot retain current interruption/recovery rules.

A terminal adapter is supported only after its specific terminal/version/window-mode combination passes focus and lifecycle tests. Target iTerm2 first. Prefer a separate background window where proven; its documented unselected tab is also a separate TTY and must be identified as a tab in the launch result. Detect and explain required terminal automation access during onboarding. Do not change terminal security preferences automatically or trigger an Automation permission prompt after Start without earlier setup.

If a no-activation adapter is unavailable, remain in the shell with the draft and offer the exact escaped command for an explicitly opened separate terminal. Do not silently fall back to a foreground launch or a hidden process. This is a capability limitation, not a reason to add a supervisor.

Official terminal facilities are candidates, not proof of SDLC integration:

- [iTerm2's window API](https://iterm2.com/python-api/window.html) provides `async_create_tab(..., select=False)` for a background tab. New-window focus behaviour still needs verification. Its [API security documentation](https://iterm2.com/python-api-auth.html) describes enabling access; SDLC must explain this prerequisite.
- [kitty remote control](https://sw.kovidgoyal.net/kitty/remote-control/#kitten-launch) documents `launch --type=os-window --dont-take-focus`, an explicit working directory and a returned window ID. Verify the configured remote-control boundary and desktop/window-manager behaviour.
- [Apple's Terminal automation guide](https://support.apple.com/guide/terminal/trml1003/mac) directs developers to the scripting dictionary; it does not establish a no-activation command-window contract. Omitting `activate` or using `open -g` alone is insufficient evidence.
- [Windows Terminal command arguments](https://learn.microsoft.com/en-us/windows/terminal/command-line-arguments) document window/command launch, but not a no-activation switch. Its `--focus` flag selects a display mode. Treat focus-preserving launch as unproven until the platform spike passes.

## Launch acknowledgement and correlation

Generate a launch UUID when Start is accepted, separately from existing run IDs. Create an owner-only, bounded, one-use launch envelope in private host state containing the validated root/argv, executable identity and reviewed input/configuration fingerprint. Store no secrets or answer contents. The terminal adapter executes a fixed SDLC helper entry point with that opaque UUID; it never interpolates work arguments into shell/AppleScript code. The helper validates ownership and consumes the envelope once, then invokes the existing command path. A minimal internal helper mode can implement this without exposing arbitrary executable/path execution.

Keep a small private receipt with `schema_version`, `launch_id`, explicit target/mode, terminal identity, phase, correlated run IDs or feature identity, and a safe error. This is a one-use handoff acknowledgement, not a new job journal or control service. Existing preparation records, run journals, series journals and registry entries remain authoritative.

Distinguish `requested`, `terminal_opened`, `controller_started`, `registered`, `launch_failed`, `preparation_failed` and `acceptance_unknown`. Opening a terminal does not mean a job is running. Single-ticket launches bind the generated run ID after existing preparation/registration; failed preparation binds its existing failure record. Feature launches bind the existing root/reference/series identity first, then show the ticket run IDs as they appear. Do not pick the newest registry row to infer identity or expose an arbitrary run-ID override.

The launch UUID is an idempotency key: repeating the same request returns its receipt; changed arguments are rejected; an uncertain acceptance never launches again automatically. A one-use consumption lock prevents repeated `launch execute` callbacks from executing another controller. Check receipt and existing evidence after timeout, shell crash or cancellation. Cancelling a shell query after submission stops observation, not the accepted controller. Publish the generated identities through small callbacks in the current run/feature path; do not redirect native output into JSON or replace controller ownership.

Revalidate reviewed inputs and configuration before execution. If the fingerprint changed, report that a fresh review is needed without connected preflight or execution. The shell returns to the draft/review. Terminal startup, helper startup, preparation and controller errors remain separately visible.

## Verification gates

- Existing CLI text, JSON, exit codes and foreground cancellation pass unchanged when new flags are absent.
- Both frontends produce the same plan and execute the same validated operation, including feature and frozen-resume cases.
- Background launch never changes the foreground application, active shell tab/window or keyboard recipient, including transient changes. Test terminal already running, terminal not running, multiple windows and supported permission states. Screen recording plus OS focus-event observation establish absence of flicker; the HTML prototype cannot prove this.
- The monitor appears immediately as Launching; late/error/unknown acknowledgement cannot be presented as an active run. Two simultaneous jobs correlate correctly, and duplicate requests produce one controller.
- Shell exit/crash and closure of its terminal leave the separate run terminal/controller alive. Closing that controller's terminal or Ctrl+C there retains today's behaviour.
- Ticket/reference/root values containing spaces and punctuation remain literal across terminal automation. Changed source/configuration requires fresh review; no terminal launch is constructed from untrusted command text.
- Both terminal output and shell following show existing recorded activity; reader cancellation, resize and reconnect never restart work. Missing log bytes or native messages remain explicit gaps.
- Clear Apply acts only on reviewed IDs, retains artifacts/locators and reports races or partial failure using existing Forget semantics.

Use offline fake controllers and disposable private fixtures. No native terminal focus test or connected provider trial has been performed by this proposal.
