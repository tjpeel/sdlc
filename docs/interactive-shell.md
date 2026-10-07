# Interactive shell

The first beta offers an opt-in frontend over the existing CLI:

```sh
sdlc shell
```

Start it in the project you want to work on. Bare `sdlc` still prints usage.
The shell requires terminal input and output. For a basic terminal, use
`sdlc shell --plain`; explicit CLI commands remain suitable for scripts and
redirected output.

The rich view uses the full terminal window and redraws when it is resized.
It inherits the terminal's background, font and ANSI palette. Its prompt follows
Robby Russell: green arrow, cyan current directory, blue `git:(...)`, red branch,
and a yellow dirty marker. There is no theme selector.

## Commands and help

`/onboard` starts with what needs attention and the next useful action, then
shows the full eight-step walkthrough. Revisit it after changing local setup.
It validates local project settings and counts numbered tickets, but it does
not check provider or GitHub access, signing, execute checks or query Docker.
Local configuration and recorded runtime evidence remain distinct from verified
readiness. A missing terminal bridge is optional because jobs can use an
external controller terminal.

Onboarding, dashboard, version details and usage use the same section
headings and status labels. Problems and human actions come before details;
unverified evidence and missing measurements stay visible without relying on
colour. Page through longer views using the controls below.

Type `/` to see commands, then keep typing to narrow the list. Tab or Enter
inserts the selected completion; another Enter submits it. Arrow keys move
through suggestions, Escape dismisses them, and the mouse wheel or Page Up/Down
scroll the output while the command line stays visible. `/help` opens at the
beginning of the matching command catalogue.
On Mac keyboards, Fn+↑ and Fn+↓ send Page Up and Page Down; plain arrow keys
continue to edit the command or move through suggestions.

To copy text, left-click the displayed output, then drag to select and use your
terminal's copy shortcut: Cmd+C on macOS or Ctrl+Shift+C in many Linux terminals.
The first click enters selection mode, freezes the visible frame and releases
mouse capture. Escape returns to shell scrolling; your command or answer draft
is retained. Jobs and monitoring continue collecting updates while the frame is
frozen. Page or scroll to the text you need before entering selection mode.
F2 remains a keyboard shortcut (Fn+F2 if your Mac uses F2 for brightness); F2 or
Ctrl+C can also resume controls without cancelling work. Resizing the terminal reflows the frozen output without resuming updates. Selection and copying are handled by the terminal.

Quotes and backslashes group literal arguments. Shell substitutions, pipes
and environment expansion are not evaluated.

Useful commands:

| Command | Purpose |
| --- | --- |
| `/help` or `/help run` | Discover commands, usage and flags. |
| `/onboard` | Show the current project's setup steps, their purpose and repair commands. |
| `/version` | Separate running CLI, PATH installation, SDLC source and recorded runtime evidence. |
| `/reference` | List local work references without reading tickets. |
| `/references [--json]` | List local work references without changing the selected reference. |
| `/tickets "Example stream" [--json]` | List ordered ticket filenames for the named reference without reading bodies. |
| `/reference "Example stream"` | Select a reference for subsequent work/run commands. |
| `/work` | List exact ticket filenames for the selected reference. |
| `/inspect "@Example stream/01-first.md"` | Explicitly read a bounded local ticket. |
| `/dashboard` | Follow project-scoped registered jobs and recorded activity. |
| `/progress --run RUN_ID` | Tail labelled SDLC steps, checks, and provider agent output. |
| `/attention` | Follow pending questions, problems and work ready for human review. |
| `/answer RUN_ID` | Read the questions, type a reply, then Ctrl+S to answer and resume. |
| `/inputs --run RUN_ID [--add RELATIVE_PATH] [--dry-run]` | Inspect captured requirements or attach missing files to an unpublished paused implementation without starting it. |
| `/resume RUN_ID` | Inspect a stopped run, then Ctrl+S to resume its saved stage. |
| `/usage` | Read recorded provider tokens, Headroom estimates and completion outcomes. |
| `/update` | Install SDLC and its runtime; `--agent-tools` refreshes Codex, Claude, skills and agents; add `--update-dockerfile` to write those source pins; `--sdlc-only` keeps the entire runtime. |
| `/scope all` | Browse installation-wide history from multiple projects. |
| `/scope project` | Return to the selected project's history. |
| `/clear` | Clear the displayed view. |
| `/exit` | Close this frontend. |

`/update --agent-tools --update-dockerfile` writes only `CODEX_VERSION`,
`CLAUDE_VERSION`, `SKILLS_REVISION` and `AGENTS_REVISION` in the selected source
Dockerfile after successful runtime selection, including an already-current
result. It retains other pins and text and makes no Git commit or push. A failed
source write after runtime replacement reports a partial result. Dry-run stays
offline and writes nothing. Without writeback, refresh selections stay private.
Default `/update` rebuilds the source baseline; `--dependencies` refreshes the
full dependency set. `runtime status` is read-only. `--cli-only` remains an alias
for `--sdlc-only`.

Use `@REFERENCE/NUMBERED_FILE` in `/run --ticket` for local ticket completion.
Suggestions use reference names and filenames; they do not load ticket bodies.
An explicit `--reference` must agree with the locator. Existing selection,
ignored/untracked work, numeric ordering and filesystem validation still apply.

Ordinary CLI commands run inside the shell, with their command, stdout and stderr
retained in the output view. Longer commands stream their
output as they work. Ctrl+C interrupts the current command and keeps the shell
open. Captured output is held in memory and bounded; older output may be omitted
when the limit is reached.

Native command outcomes stay visible above the command prompt while you scroll
through output. Failures are bold red, successful exits green, and cancellations
remain labelled. The displayed command and its output stay in the scrollable
area; editing a draft keeps the result visible until the next command or view.
Copy mode freezes this result along with the output. `NO_COLOR=1` disables outcome
colors while retaining the result text.

Provider sessions, account login and signing setup hand the terminal to the
existing interactive command. Its prompts and provider slash commands belong
to that command until it exits; SDLC then redraws. Other signing actions,
runtime operations and status checks keep their results in the shell.

## Projects and history

The shell starts at the current Git checkout's root. It keeps an explicit
project context without changing its process working directory. Register
additional checkouts with:

```text
/project add /PATH/TO/OTHER_CHECKOUT --name example
/project list
/project select example
```

The catalogue is private installation state. Removing a locator with
`/project remove example` retains jobs, history and work files. Project selection
clears the selected reference and launch review. Jobs keep their original project.

History is filtered before ordering and pagination. `/scope all` broadens only
browsing; it does not change where the next job will run. The external
`sdlc dashboard` retains its installation-wide default. Use
`sdlc dashboard --scope project --json` for a project snapshot.

Inspect a run with `/dashboard --run RUN_ID` or `/inspect @run:RUN_ID`.
Run selectors accept a full ID or a unique lowercase hexadecimal prefix of at
least three characters. The same rule applies to answer, resume, inputs, usage
and progress. Ambiguous prefixes list the matching full IDs; use more characters
to select one. Commands retain the full ID after selection.
Use `/progress --run RUN_ID` or `/dashboard --run RUN_ID --logs` to follow its
output.

`dashboard remove` is the recommended spelling; `dashboard forget` remains an alias.
Both hide registry entries and retain saved files.

`/dashboard remove --run RUN_ID` hides the registry entry, retains saved
work and refuses active controllers. Saved work can still block an update.
`/dashboard remove --all` previews clearing stopped registrations across the
installation; add `--scope project` to narrow it and `--yes` to clear them.
Use `--dry-run` for an explicit preview.

`/storage purge --run RUN_ID` or `/storage purge --all` previews permanent
deletion of stopped saved runs in the current repository, including those absent
from the dashboard. Add `--yes` to delete or `--dry-run` to preview. The preview
also reports abandonment of affected feature series checkpoints, which are
removed with the runs. Tickets and specs remain. Live runs or series refuse
removal; `--all` excludes archived references and credentials.

`/work archive --reference TASK-123 [--dry-run]` moves the full reference tree
into a unique directory under `.sdlc/work/.archive/`, removes its run registrations
and frees the name for fresh scoping. It refuses live runs or series and needs
no `--yes`. The tree and evidence remain, but archived work is excluded from
active discovery, resume and runtime guards. An update may replace its image,
so archiving does not guarantee later resumability and is not a portable backup.

## Follow job output

After Start or an answer/resume submission, the shell follows the new launch
receipt and then its registered runs. Each new output record appends to the
view. Labels identify the run and producer: `[RUN_ID sdlc]`, `[RUN_ID checks]`,
`[RUN_ID agent:codex implementation]`, or `[RUN_ID agent:claude review]`.
Native messages, commands, tool results and diagnostics are rendered as text;
the original provider events remain in private run logs.

The rich view follows the newest output. Scrolling up with the mouse wheel or
Page Up pauses scrolling while the feed continues; scrolling down to the end or
pressing End returns to the tail. New output preserves a typed command.
Escape stops following, and `/exit` closes the shell. The
controller continues in its independent terminal. A stopped controller's
questions and answer/resume commands appear in the feed. `/answer` can use the
sole run being followed; a feature with several runs requires `/answer RUN_ID`.

Plain mode prints increments while waiting for a command. `/cancel` stops the
view. Terminal echo can share a line with arriving output; typed input remains
intact. Native interactive commands retain the terminal's input and output.

The initial view starts with a recent bounded tail, then reads new records
without repeating them. The rich shell retains up to 1,000 lines or 256 KiB of
scrollback. Long messages are clipped; use private raw logs for complete
diagnostics. These display limits do not truncate raw logs or change usage
measurements. `/usage --run RUN_ID` shows the recorded metrics.

Explicit progress selection uses the shell's current history scope. Resumed
work follows its recorded project, even when another project is selected.
`/progress --run RUN_ID --json` displays one CLI batch as a snapshot.

`/progress --launch-id ID` and `/launch status --id ID` accept a full launch UUID
or a unique prefix of at least three characters. Progress cursors bind to the
resolved full ID; internal launch execution requires the full UUID.

## Answer a stopped run

The dashboard reads saved questions directly from the run checkpoint and shows
them in both the list and selected-run details. `/attention` narrows the view to
runs needing human action. Questions, stop reasons and next commands remain
visible without opening their private files.

For a missing requirement file, use `/inputs --run RUN_ID` to see captured paths
and missing linked documents. Preview `/inputs --run RUN_ID --add RELATIVE_PATH
--dry-run`, then repeat without `--dry-run` to attach it. The run stays paused;
use `/answer RUN_ID` after the file is available. Existing input hashes are
preserved. Dashboard, progress and the answer editor show this file-repair route
for eligible unpublished implementation questions. In the answer editor, Escape
returns to commands before attaching a file.

Use `/answer RUN_ID` to open the answer editor. After
`/dashboard --run RUN_ID`, `/answer` uses
that selected run. The editor shows its project, ticket and questions. Type free
text; Enter inserts a newline, Ctrl+S submits the answer and requests a resumed
controller in an independent terminal, and Escape or Ctrl+C discards the editor.
The mouse wheel or Page Up/Down scroll the questions while the reply and submit
controls stay visible.
Quotes, slash commands and punctuation remain literal answer text. The limit is
64 KiB of nonempty UTF-8 text.

Plain mode displays the same questions. Enter a line containing `.` to submit,
or `/cancel` to discard. Use `..` or `//cancel` for those literal lines. End of
input before the submit line cancels the answer.

For other stopped runs, resolve the displayed problem, then `/resume RUN_ID` and
Ctrl+S (`/start` in plain mode). The recorded run supplies its project and frozen
settings, even when another project is selected. The shell then monitors that run.
If the checkpoint changes while an answer is being written, inspect it again
before submitting. An active controller cannot be resumed a second time.

Answers are saved in private run history and sent as provider input. They are
never placed in the terminal launch command. A failed submission keeps the
editor text unless the terminal launch may already have dispatched the controller.

## Review and start a job

```text
/run --ticket "@Example stream/01-first.md" --repo example/project
/run --reference "Example stream" --all --parallel 2
```

Submitting `/run` displays the existing offline plan. It does not check provider
credentials, create a run or start a model. Review the project, inputs, repository,
provider roles, checks and options. Ctrl+S requests Start; Escape cancels. Plain
mode uses `/start` and `/cancel`. An explicit `--dry-run` remains preview only.
Changed plan evidence requires another review.

Start requests an independent native iTerm2 tab through a one-use private launch
receipt. The existing `sdlc run` controller owns execution in that terminal;
the shell observes its receipt, registered run IDs and output. The controller
must acknowledge startup before monitoring can show an actual run.

The iTerm2 adapter requires explicit terminal setup. In iTerm2, enable its
Python API in Settings, then run `/terminal setup` (or `sdlc terminal setup`).
This harmless handshake creates no tab or job and can request the initial macOS
Automation permission. `/terminal status` reads local setup evidence. Start
rechecks Automation permission without requesting it; if permission is missing,
return to explicit setup. SDLC uses iTerm's own `it2run` utility and unmodified
Python API without copying API cookies into the shell or saved state.
The adapter verifies the app's code signature, bundle identifier and publisher
against the [official release publisher](https://github.com/gnachman/iTerm2/blob/master/tools/release_beta.sh).

Its documented
`select=False` tab creation preserves keyboard focus. Permission, availability
or dispatch failures remain visible with a manual command for a separate
terminal. That command consumes the same one-use launch receipt, so a delayed
native handoff and manual handoff cannot both start the job. SDLC does not
activate another terminal and then restore focus.
Visual flicker and survival on closing the originating terminal require a
native acceptance check for the installed iTerm2 version.

Closing the SDLC shell does not signal an independent run controller. Keep the
controller's own terminal open while it works: closing that terminal can stop
the foreground job, as with the existing CLI. No daemon, supervisor or remote
stop protocol is introduced.

## Versions and delivery

`sdlc --version` retains the short executable identity. `sdlc version --details
--json` supplies the overview without Docker or provider calls. Pass
`--source /PATH/TO/SDLC_SOURCE` to select the application source independently
of the current work project.

Source version is a declaration, not proof of a built or installed binary.
Running identity stays attached to the open shell. An older PATH executable's
application version may be unknown when its build metadata does not record it.
The last separately built artifact is reported as “not recorded”; recorded
runtime source/image evidence is labelled unverified until explicit status
checks. Installing the CLI does not rebuild Docker.

`internal/buildinfo.Version` is the version source. The first baseline is
`0.1.0-beta.1`; each delivered application iteration advances the beta number
and adds a dated [changelog](../CHANGELOG.md) entry. No release tag is created
by setting the version.

The [proposal](proposals/interactive-cli.md) and
[CLI contracts](proposals/interactive-cli/cli-contract.md) retain later work:
richer onboarding forms, complete conversation inspection, reviewed bulk clear,
broader terminal/platform validation and eventual bare-command shell launch.

### Live terminal dashboard

`sdlc dashboard` uses a separate terminal screen and updates only changed rows.
Its output stays within the terminal dimensions; wheel, arrows and Page Up/Down
scroll the current page, and `n`/`p` change pages without Enter. `q` or Ctrl+C closes
it while jobs continue. `--once`, JSON and redirected output remain plain snapshots.

Click the output (or press F2/Space) to pause the displayed dashboard and release
mouse capture. Then drag to select and use your terminal's copy shortcut. Escape
or F2 resumes with the latest collected status. A resize reflows the paused text.
The first click switches modes; selection starts with the subsequent drag.

Dashboard sections use cyan/blue headings and restrained green accents for active
work. Recorded runs are grey; questions and review actions remain prominent.
Set `NO_COLOR=1` to disable dashboard colors.
