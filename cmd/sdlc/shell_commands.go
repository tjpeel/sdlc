package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/tjpeel/sdlc/internal/shell"
)

// This catalogue is shared by CLI help and the shell. Execution continues to
// use each existing command's parser and validators.
func shellCommands() []shell.Command {
	command := func(name, group, summary, usage string, native bool, flags string) shell.Command {
		entry := shell.Command{Name: name, Group: group, Summary: summary, Usage: usage, Native: native}
		entry.Interactive = name == "interactive" || name == "auth login" || name == "signing setup"
		for _, name := range strings.Fields(flags) {
			entry.Flags = append(entry.Flags, shell.Flag{Name: "--" + name, Summary: "See command help for values and constraints"})
		}
		return entry
	}
	return []shell.Command{
		command("help", "Guide", "Browse commands and examples", "/help [command path]", false, "json"),
		command("onboard", "Guide", "Explain current-project setup and remaining configuration", "/onboard", false, "json"),
		command("version", "Guide", "Distinguish running, installed, source and runtime identities", "/version", false, "details json source"),
		command("projects", "Context", "List current and registered project roots", "/projects", false, "json"),
		command("project", "Context", "Select an explicit project or manage its private locator", "/project select NAME | add PATH [--name NAME] | remove NAME", false, "name json"),
		command("scope", "Context", "Choose project or all-project dashboard history", "/scope project|all", false, ""),
		command("reference", "Context", "Select a local work reference without reading ticket bodies", "/reference [NAME]", false, ""),
		command("work", "Work", "List local references or ordered ticket metadata", "/work --reference REF | --references", false, "reference references json"),
		command("work archive", "Work", "Move a stopped reference and all its files into .sdlc/work/.archive", "/work archive --reference REF [--dry-run]", true, "reference dry-run"),
		command("references", "Work", "List local work references without reading ticket bodies", "/references [--json]", false, "json"),
		command("tickets", "Work", "List ordered ticket filenames for a local reference", "/tickets REFERENCE [--json]", false, "json"),
		command("inspect", "Work", "Read one explicitly selected safe local ticket or run", "/inspect @REF/NUMBERED_FILE | @ref:REF | @run:ID", false, "reference ticket json"),
		command("run", "Work", "Review the existing run plan, then start an independent terminal", "/run --reference REF --ticket FILE | --all [--parallel 2]", false, "reference ticket all parallel watch alternate-providers provider github-profile input base branch repo model effort review-model review-effort resume answer-file docker-tests dry-run timeout notify sound json terminal launch-id headroom runtime"),
		command("answer", "Work", "Read pending questions, type an answer and resume the recorded run; CLI: sdlc answer --run ID", "/answer [RUN_ID]", false, ""),
		command("inputs", "Work", "Inspect captured requirements or attach missing files to an unpublished paused run without starting it", "/inputs --run ID [--add RELATIVE_PATH] [--dry-run]", true, "run add dry-run json"),
		command("resume", "Work", "Resume a stopped run using its saved project and settings; CLI: sdlc resume --run ID", "/resume [RUN_ID]", false, ""),
		command("storage", "Monitor", "Inspect retained project storage without deleting files", "/storage [status] [--older-than DAYS] [--json]", false, "older-than json"),
		command("storage purge", "Monitor", "Preview permanent deletion of stopped saved runs in this project", "/storage purge --run ID | --all [--yes | --dry-run]", true, "run all yes dry-run"),
		command("usage", "Monitor", "Read recorded provider usage and completion outcomes", "/usage [--since 7d] [--scope project|installation] [--run ID]", false, "since scope run json"),
		command("attention", "Monitor", "Follow questions, problems and work ready for human review", "/attention [--scope project|installation]", false, "scope page"),
		command("dashboard", "Monitor", "Follow current-project history or inspect an exact run", "/dashboard [--attention] [--run ID --logs] [--page N]", false, "once watch json logs run page interval scope notify sound attention"),
		command("dashboard remove", "Monitor", "Hide stopped dashboard entries; saved files and runtime protection remain", "/dashboard remove --run ID | --all [--yes | --dry-run] [--scope project|installation]", true, "run all yes dry-run scope"),
		command("dashboard forget", "Monitor", "Alias of dashboard remove; hides entries without deleting saved files", "/dashboard forget --run ID | --all [--yes | --dry-run] [--scope project|installation]", true, "run all yes dry-run scope"),
		command("dashboard export", "Monitor", "Export the current bounded run summary privately", "/dashboard export --run ID --to PRIVATE_DIRECTORY", true, "run to"),
		command("launch status", "Monitor", "Read one launch receipt without controlling its job", "/launch status --id UUID", false, "id json"),
		command("terminal setup", "Setup", "Explicitly configure the official iTerm2 background-tab bridge", "/terminal setup", true, "json"),
		command("terminal status", "Setup", "Read local background-terminal setup evidence", "/terminal status", false, "json"),
		command("init", "Setup", "Review and save local project settings using the existing CLI", "/init", true, ""),
		command("update", "Setup", "Install SDLC; optionally refresh provider CLIs and catalogues and write their source pins", "/update [--pull] [--sdlc-only | --agent-tools [--update-dockerfile] | --dependencies] [--dry-run]", true, "source bin-dir pull sdlc-only cli-only agent-tools update-dockerfile dependencies dry-run"),
		command("interactive", "Provider", "Temporarily hand the terminal to an official provider session", "/interactive [--provider codex|claude]", true, "provider approval permission-mode"),
		command("auth login", "Setup", "Run the existing official login flow", "/auth login [--provider codex|claude] | --service github [--profile NAME]", true, "provider service profile"),
		command("auth status", "Setup", "Run the existing explicit login status check", "/auth status [--provider codex|claude | --all] | --service github [--verify]", true, "provider all service profile verify"),
		command("auth logout", "Setup", "Use the existing GitHub logout operation", "/auth logout --service github [--profile NAME]", true, "service profile"),
		command("github list", "Setup", "List configured GitHub profile metadata locally", "/github list", false, ""),
		command("github pair", "Setup", "Use existing account and signing-key verification", "/github pair --profile NAME [--signing-profile NAME]", true, "profile signing-profile replace"),
		command("github use", "Setup", "Verify and save the project's repository/profile selection", "/github use [--profile NAME] [--repo OWNER/REPO]", true, "profile repo"),
		command("github status", "Setup", "Inspect selection or deliberately verify connected access", "/github status [--profile NAME] [--verify]", true, "profile verify"),
		command("signing setup", "Setup", "Use the existing protected signing provisioning flow", "/signing setup [--profile NAME]", true, "profile provider replace"),
		command("signing configure", "Setup", "Import an explicitly selected private signing profile", "/signing configure --file PRIVATE_PROFILE [--profile NAME]", true, "profile file replace"),
		command("signing status", "Setup", "Inspect signing readiness using the existing CLI", "/signing status [--profile NAME] [--verify]", true, "profile verify show-config"),
		command("signing verify", "Setup", "Explicitly verify configured signing authority", "/signing verify [--profile NAME]", true, "profile"),
		command("runtime build", "Setup", "Rebuild using installed pins, or select checkout pins with --source-pins", "/runtime build [--name NAME] [--source SDLC_SOURCE] [--source-pins] [--skills-source SKILLS_SOURCE]", true, "name source source-pins skills-source"),
		command("runtime headroom build", "Setup", "Build the separate pinned compression proxy", "/runtime headroom build", true, ""),
		command("runtime headroom status", "Setup", "Inspect the local Headroom image", "/runtime headroom status", true, ""),
		command("runtime status", "Setup", "Explicit runtime check; --offline avoids upstream updates", "/runtime status [--name NAME] [--offline] [--all]", true, "name offline all github-profile"),
		command("runtime update", "Setup", "Update runtime dependencies; optionally write the four agent-tool pins to source", "/runtime update [--agent-tools [--update-dockerfile --source SDLC_SOURCE]] [--dry-run]", true, "agent-tools update-dockerfile dry-run source"),
		command("instructions show", "Setup", "Show shared instructions locally", "/instructions show", false, ""),
		command("instructions set", "Setup", "Use the existing explicit instruction update", "/instructions set --file FILE", true, "file"),
		command("instructions reset", "Setup", "Restore shared instruction defaults", "/instructions reset", true, ""),
		command("progress", "Observe", "Tail SDLC steps, checks and provider agent output", "/progress --run ID [--once]", false, "run launch-id scope once follow json cursor interval"),
		command("clear", "View", "Clear this view while preserving jobs and history", "/clear", false, ""),
		command("exit", "View", "Close the shell while independent job terminals continue", "/exit", false, ""),
	}
}

func helpCommand(args []string, output io.Writer) error {
	structured := false
	var path []string
	for _, arg := range args {
		if arg == "--json" {
			structured = true
		} else if strings.HasPrefix(arg, "-") {
			return fmt.Errorf("help accepts a command path and --json")
		} else {
			path = append(path, arg)
		}
	}
	query := strings.Join(path, " ")
	var matches []shell.Command
	for _, command := range shellCommands() {
		if query == "" || command.Name == query || strings.HasPrefix(command.Name, query+" ") {
			matches = append(matches, command)
		}
	}
	if len(matches) == 0 {
		return fmt.Errorf("unknown help path; run sdlc help --json for the command catalogue")
	}
	if structured {
		return json.NewEncoder(output).Encode(struct {
			Version  int             `json:"version"`
			Commands []shell.Command `json:"commands"`
		}{1, matches})
	}
	for _, command := range matches {
		fmt.Fprintf(output, "%s\n  %s\n", command.Usage, command.Summary)
		for _, flag := range command.Flags {
			fmt.Fprintf(output, "  %s\n", flag.Name)
		}
	}
	return nil
}
