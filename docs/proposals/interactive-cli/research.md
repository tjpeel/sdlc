# Interactive CLI research

Official documentation checked on 5 October 2026. This research used documentation rather than authenticated product sessions. Product commands and availability can change. The mockups depict proposed SDLC behaviour with simulated data.

## Documented patterns and proposed use

| Product and source | Documented behaviour | Proposed SDLC use |
| --- | --- | --- |
| [Codex command reference](https://learn.chatgpt.com/docs/developer-commands?surface=cli) | Typing `/` opens a filtered command popup. `@` searches workspace files. Saved sessions can be selected with `/resume`; interactive shortcuts include prompt history. | Filtered command discovery, typed local targets and checkpoint selection. SDLC ticket eligibility still requires its own validators. |
| [Codex CLI overview](https://learn.chatgpt.com/docs/codex/cli) | Launch shows directory/model context and suggested actions. Interactive use and explicit scripted execution coexist. | Visible project context and persistent prompt alongside preserved argv commands. |
| [Claude interactive mode](https://code.claude.com/docs/en/interactive-mode) | Slash commands filter while typing; `@` completes paths. Commands include built-ins and extensions. | Predictable discovery; domain references can extend the path-completion pattern. Ticket IntelliSense is our proposal. |
| [Claude skill metadata](https://code.claude.com/docs/en/skills#frontmatter-reference) | Skills can supply argument hints during autocomplete. | Describe the next argument and its constraints beside suggestions. This source does not establish live ticket lookup. |
| [Claude keybindings](https://code.claude.com/docs/en/keybindings#autocomplete-actions) | Tab accepts completion, arrows navigate and Escape dismisses. | State-specific controls with explicit insertion before command dispatch. |
| [Claude quickstart](https://code.claude.com/docs/en/quickstart) | `claude` opens an interactive session; first use guides browser authentication. | Offer a guided first-use route while preserving SDLC's official native authentication flows. |
| [Claude fullscreen rendering](https://code.claude.com/docs/en/fullscreen) | Bottom input, application transcript scrolling and scrollback export are documented for fullscreen rendering. | Full-window layout, fixed command area and application-owned scrolling. Live resize and state preservation are explicit SDLC requirements, to verify in the native terminal spike. |
| [Gemini command reference](https://geminicli.com/docs/reference/commands/) | `/help` and nested slash commands expose session and configuration actions; `@` references include files. | Progressive subcommand help and explicit typed reference arguments. |
| [Gemini keyboard shortcuts](https://geminicli.com/docs/reference/keyboard-shortcuts/) | Tab and Enter can accept suggestions; Enter also submits prompts according to context. | Define input-state precedence instead of leaving Enter ambiguous. |
| [Gemini session management](https://geminicli.com/docs/cli/session-management/) | `/resume` provides a searchable saved-session browser with context. | Show project, reference, ticket and lifecycle state when selecting a resumable SDLC run. |
| [Bubble Tea maintainers](https://github.com/charmbracelet/bubbletea) | Go terminal framework with model/update/view structure and inline/full-window modes. | Recommended UI foundation, subject to a terminal spike and pinned compatible dependencies. |
| [Robby Russell theme source](https://github.com/ohmyzsh/ohmyzsh/blob/master/themes/robbyrussell.zsh-theme) | Arrow, current directory, Git wrapper/branch, dirty marker and ANSI choices define the zsh prompt; iTerm2 supplies font and visible palette. | Sole SDLC prompt style; match the existing zsh appearance and inherit iTerm2 settings without executing shell configuration. |
| [Semantic Versioning](https://semver.org/spec/v2.0.0.html), [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) | Prerelease identifiers, immutable released versions and human-readable dated release notes with Unreleased entries. | Proposed `0.1.0-beta.N` increments per delivered iteration, one canonical version source and an honest changelog that excludes unshipped features. |

The reference tools also accept conversational prompts. A slash-only SDLC action surface, local ticket catalogue, explicit launch review and structured results from existing CLI operations are proposed adaptations. Their documentation does not establish those SDLC features or authorise changes to SDLC account/execution rules.

## Repository evidence

The evidence below refers to the source at proposal preparation. The linked files remain the authority if implementation moves line numbers.

- [Dispatcher and auth/interactive handling](../../../cmd/sdlc/main.go): bare launch prints usage; provider sessions are separate operations; auth still prints through global output.
- [Run parsing and orchestration](../../../cmd/sdlc/run.go): exact selection, feature constraints, model/effort flags, frozen resumes and notification options.
- [Dashboard adapters](../../../cmd/sdlc/dashboard.go): TTY versus redirected output, paging, watch, JSON, forget/remove and private export.
- [Work discovery](../../../internal/project/work.go) and [launch validation](../../../internal/project/launch.go): opaque references, numeric filename order, ignore/tracking checks and safe inputs.
- [Feature discovery](../../../internal/workseries/discover.go): optional plan metadata and frozen definitions without ticket-body reads.
- [Runner lifecycle](../../../internal/workrun/runner.go): exclusive run ownership, persisted checkpoints, human answers and cancellation handling.
- [GitHub profile selection](../../../cmd/sdlc/github.go): local profile list, connected use, private repository identity and selection precedence. [Preparation records](../../../internal/runstatus/preparation.go) distinguish failed preparation from a resumable provider run.
- [Source capture](../../../internal/workrun/source.go) and [snapshot materialisation](../../../internal/workrun/stack.go): committed `.env.example` templates survive capture unless their parent directory is excluded; untracked environment files remain excluded and the captured index is restored. [Registry projections](../../../internal/runstatus/registry.go) expose preparation-only records without inventing journals or native conversations.
- [CLI version](../../../internal/buildinfo/version.go), [installer](../../../internal/install/install.go) and [runtime image](../../../internal/runtimeimage/runtime.go): executable version, PATH installation and immutable image/source records are separate identities. Current source is `0.1.0-dev`; the beta baseline remains proposed.
- [Current foreground lifecycle](../../cli.md) and [unattended controller plan](../unattended-docker-delivery.md): a dashboard can exit without cancelling jobs; the run command remains a foreground controller. The first shell proposal opens that controller in a separate terminal without activation and observes existing records. Invisible background execution and full supervision remain separate backend work.
- [Unified onboarding](../unified-onboarding.md): proposed progress/navigation and existing provisioning/storage boundaries.
- [Work identity](../pr-manage-work-reference.md): reference/ticket identity must survive publication.
- [Provider usage](../../provider-usage.md) and [publication safety](../../publication-safety.md): official-client execution, offline tests and public-content constraints.

No connected provider request, login, vault retrieval, ticket execution or GitHub publication was used to evaluate the proposed experience.

## Background terminal launch evidence

Official terminal documentation checked on 5 October 2026; no host launch/focus tests were performed. The [supporting CLI contract](cli-contract.md) requires capability-specific tests before promising no activation or flicker, starting with iTerm2.

| Source | Documented capability and limit |
| --- | --- |
| [iTerm2 window API](https://iterm2.com/python-api/window.html), [API security](https://iterm2.com/python-api-auth.html) | An unselected background tab has an explicit `select=False` option; new-window focus still needs verification. API access requires setup. |
| [kitty remote control](https://sw.kovidgoyal.net/kitty/remote-control/#kitten-launch) | `launch --type=os-window --dont-take-focus` supports a separate background window and returns its identity, subject to configured access and desktop tests. |
| [Apple Terminal automation](https://support.apple.com/guide/terminal/trml1003/mac) | Automation uses the scripting dictionary; this guide establishes no no-activation command-window contract. `open -g` alone cannot establish subsequent launch behaviour. |
| [Windows Terminal command arguments](https://learn.microsoft.com/en-us/windows/terminal/command-line-arguments) | Supports explicit window, working-directory and command launch; `--focus` is a display mode, not a no-activation guarantee. |

The proposed fallback retains the shell draft and offers an explicit manual command. It never opens a foreground terminal and restores focus to simulate background launch.
