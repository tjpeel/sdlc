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

Type `/` to see commands, then keep typing to narrow the list. Tab or Enter
inserts the selected completion; another Enter submits it. Arrow keys move
through suggestions, Escape dismisses them, and Page Up/Down scroll the view.
Quotes and backslashes group literal arguments. Shell substitutions, pipes
and environment expansion are not evaluated.

Useful commands:

| Command | Purpose |
| --- | --- |
| `/help` or `/help run` | Discover commands, usage and flags. |
| `/onboard` | Show the current project's setup steps, their purpose and repair commands. |
| `/version` | Separate running CLI, PATH installation, SDLC source and recorded runtime evidence. |
| `/reference` | List local work references without reading tickets. |
| `/reference "Example stream"` | Select a reference for subsequent work/run commands. |
| `/work` | List exact ticket filenames for the selected reference. |
| `/inspect "@Example stream/01-first.md"` | Explicitly read a bounded local ticket. |
| `/dashboard` | Follow project-scoped registered jobs and recorded activity. |
| `/attention` | Follow pending questions, problems and work ready for human review. |
| `/answer RUN_ID` | Read the questions, type a reply, then Ctrl+S to answer and resume. |
| `/resume RUN_ID` | Inspect a stopped run, then Ctrl+S to resume its saved stage. |
| `/usage` | Read recorded provider tokens, Headroom estimates and completion outcomes. |
| `/scope all` | Browse installation-wide history from multiple projects. |
| `/scope project` | Return to the selected project's history. |
| `/clear` | Clear the displayed view. |
| `/exit` | Close this frontend. |

Use `@REFERENCE/NUMBERED_FILE` in `/run --ticket` for local ticket completion.
Suggestions use reference names and filenames; they do not load ticket bodies.
An explicit `--reference` must agree with the locator. Existing selection,
ignored/untracked work, numeric ordering and filesystem validation still apply.

Account login, signing actions, runtime operations and native provider sessions
hand the terminal to the existing command. Its prompts and provider slash
commands belong to that command until it exits; SDLC then redraws.

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

Inspect a run with `/dashboard --run RUN_ID --logs` or `/inspect @run:RUN_ID`.
This first pass shows existing journal evidence and bounded recorded output.
It does not parse complete provider conversations. `/dashboard forget --run
RUN_ID` retains saved work and refuses active controllers under the existing
rules. Reviewed bulk history clearing remains a later iteration.

## Answer a stopped run

The dashboard reads saved questions directly from the run checkpoint and shows
them in both the list and selected-run details. `/attention` narrows the view to
runs needing human action. Questions, stop reasons and next commands remain
visible without opening their private files.

Use `/answer RUN_ID` to open the answer editor. A unique ID prefix of at least
six characters is sufficient. After `/dashboard --run RUN_ID`, `/answer` uses
that selected run. The editor shows its project, ticket and questions. Type free
text; Enter inserts a newline, Ctrl+S submits the answer and requests a resumed
controller in an independent terminal, and Escape or Ctrl+C discards the editor.
Page Up/Down scroll the questions while the reply and submit controls stay visible.
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
