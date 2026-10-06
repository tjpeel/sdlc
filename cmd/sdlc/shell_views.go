package main

import (
	"context"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	identity "github.com/tjpeel/sdlc/internal/buildinfo"
	"github.com/tjpeel/sdlc/internal/dashboard"
	"github.com/tjpeel/sdlc/internal/filelock"
	"github.com/tjpeel/sdlc/internal/homebrew"
	"github.com/tjpeel/sdlc/internal/install"
	"github.com/tjpeel/sdlc/internal/project"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/terminallaunch"
	"github.com/tjpeel/sdlc/internal/textview"
)

type shellProject struct {
	Name    string `json:"name"`
	Root    string `json:"root"`
	Current bool   `json:"current,omitempty"`
}

func shellStateDirectory() (string, error) {
	m, err := runtimeimage.New(io.Discard, io.Discard)
	if err != nil {
		return "", err
	}
	return filepath.Abs(m.Directory)
}

func checkoutRoot(ctx context.Context, directory string) (string, error) {
	data, err := exec.CommandContext(ctx, "git", "-C", directory, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("select a non-bare Git checkout")
	}
	root, err := filepath.EvalSymlinks(strings.TrimSpace(string(data)))
	if err != nil {
		return "", err
	}
	if err := viewDirectory(root); err != nil {
		return "", err
	}
	return root, nil
}

func projectList(ctx context.Context) ([]shellProject, error) {
	dir, err := shellStateDirectory()
	if err != nil {
		return nil, err
	}
	entries := []shellProject{}
	data, err := viewReadFile(filepath.Join(dir, "shell-projects.json"), 256*1024, true)
	if err == nil {
		if err := json.Unmarshal(data, &entries); err != nil {
			return nil, fmt.Errorf("invalid project catalogue")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		if !viewName(entry.Name) || !filepath.IsAbs(entry.Root) || filepath.Clean(entry.Root) != entry.Root || seen[entry.Name] {
			return nil, fmt.Errorf("invalid project catalogue")
		}
		seen[entry.Name] = true
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	root, err := checkoutRoot(ctx, cwd)
	if err == nil {
		found := false
		for i := range entries {
			entries[i].Current = entries[i].Root == root
			found = found || entries[i].Current
		}
		if !found {
			entries = append(entries, shellProject{Name: filepath.Base(root), Root: root, Current: true})
		}
	}
	return entries, nil
}

func projectCommand(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: sdlc project list|add PATH [--name NAME]|remove NAME [--json]")
	}
	action := args[0]
	jsonOutput := false
	name := ""
	positionals := []string{}
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOutput = true
		case "--name":
			i++
			if i == len(args) {
				return fmt.Errorf("--name requires a value")
			}
			name = args[i]
		default:
			if strings.HasPrefix(args[i], "--") {
				return fmt.Errorf("unknown project flag")
			}
			positionals = append(positionals, args[i])
		}
	}
	if action == "list" {
		if len(positionals) != 0 || name != "" {
			return fmt.Errorf("project list accepts no project arguments")
		}
		entries, err := projectList(ctx)
		if err != nil {
			return err
		}
		if jsonOutput {
			return json.NewEncoder(out).Encode(entries)
		}
		for _, entry := range entries {
			suffix := ""
			if entry.Current {
				suffix = " (current)"
			}
			fmt.Fprintf(out, "%s%s  %s\n", dashboard.SafeText(entry.Name), suffix, dashboard.SafeText(entry.Root))
		}
		return nil
	}
	if (action != "add" && action != "remove") || len(positionals) != 1 || (action == "remove" && name != "") {
		return fmt.Errorf("project add requires PATH; remove requires NAME")
	}
	dir, err := shellStateDirectory()
	if err != nil {
		return err
	}
	if err := viewPrivateDirectory(dir); err != nil {
		return err
	}
	lockPath := filepath.Join(dir, "shell-projects.lock")
	if _, err := os.Lstat(lockPath); err == nil {
		if _, err := viewReadFile(lockPath, 1024, true); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	lock, err := filelock.AcquireContext(ctx, lockPath, filelock.Exclusive, nil)
	if err != nil {
		return err
	}
	defer lock.Close()
	entries := []shellProject{}
	path := filepath.Join(dir, "shell-projects.json")
	data, err := viewReadFile(path, 256*1024, true)
	if err == nil {
		if err := json.Unmarshal(data, &entries); err != nil {
			return fmt.Errorf("invalid project catalogue")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if action == "add" {
		root, err := checkoutRoot(ctx, positionals[0])
		if err != nil {
			return err
		}
		info, err := os.Stat(root)
		if err != nil || !viewOwned(info) {
			return fmt.Errorf("project checkout must be owned by the current user")
		}
		if name == "" {
			name = filepath.Base(root)
		}
		if !viewName(name) {
			return fmt.Errorf("project name must not contain control characters or path separators")
		}
		already := false
		for _, entry := range entries {
			if entry.Name == name {
				if entry.Root != root {
					return fmt.Errorf("project name already exists; supply --name")
				}
				already = true
			}
			if entry.Root == root && entry.Name != name {
				return fmt.Errorf("project is already registered as %s", dashboard.SafeText(entry.Name))
			}
		}
		if !already {
			entries = append(entries, shellProject{Name: name, Root: root})
		}
	} else {
		name = positionals[0]
		if !viewName(name) {
			return fmt.Errorf("invalid project name")
		}
		found := false
		kept := []shellProject{}
		for _, entry := range entries {
			if entry.Name == name {
				found = true
			} else {
				entry.Current = false
				kept = append(kept, entry)
			}
		}
		if !found {
			return fmt.Errorf("project locator not found")
		}
		entries = kept
	}
	for i := range entries {
		entries[i].Current = false
	}
	data, err = json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".shell-projects-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(append(data, '\n')); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	if jsonOutput {
		return json.NewEncoder(out).Encode(entries)
	}
	_, err = fmt.Fprintf(out, "Project locator %s: %s. Jobs and history are retained.\n", action, dashboard.SafeText(name))
	return err
}

type versionView struct {
	Running   identity.Identity `json:"running"`
	Installed struct {
		Path     string `json:"path"`
		Version  string `json:"version"`
		Revision string `json:"revision"`
		Dirty    bool   `json:"dirty"`
		OS       string `json:"os"`
		Arch     string `json:"arch"`
	} `json:"installed"`
	Source struct {
		Path     string `json:"path"`
		Version  string `json:"version"`
		Revision string `json:"revision"`
		Dirty    bool   `json:"dirty"`
	} `json:"source"`
	BuiltArtifact   string              `json:"built_artifact"`
	Runtime         *runtimeimage.State `json:"runtime,omitempty"`
	RuntimeStatus   string              `json:"runtime_status"`
	RuntimeCategory string              `json:"runtime_category"`
}

func versionDetailsCommand(ctx context.Context, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("version", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Bool("details", false, "version evidence")
	asJSON := flags.Bool("json", false, "JSON")
	source := flags.String("source", "", "SDLC source checkout")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("version accepts no positional arguments")
	}
	v := versionView{Running: identity.Current(), BuiltArtifact: "not recorded", RuntimeStatus: "not recorded", RuntimeCategory: "setup"}
	v.Installed.Version = "unknown"
	v.Installed.Revision = "unknown"
	v.Source.Version = "not recorded"
	v.Source.Revision = "unknown"
	if path, err := exec.LookPath("sdlc"); err == nil {
		path, err = filepath.EvalSymlinks(path)
		if err == nil {
			v.Installed.Path = path
			current, _ := os.Executable()
			current, _ = filepath.EvalSymlinks(current)
			if path == current {
				v.Installed.Version = v.Running.Version
				v.Installed.Revision = v.Running.Revision
				v.Installed.Dirty = v.Running.Dirty
				v.Installed.OS = v.Running.OS
				v.Installed.Arch = v.Running.Arch
			} else if info, err := buildinfo.ReadFile(path); err == nil && info.Path == "github.com/tjpeel/sdlc/cmd/sdlc" {
				for _, setting := range info.Settings {
					if setting.Key == "vcs.revision" {
						v.Installed.Revision = setting.Value
					}
					if setting.Key == "vcs.modified" {
						v.Installed.Dirty = setting.Value == "true"
					}
					if setting.Key == "GOOS" {
						v.Installed.OS = setting.Value
					}
					if setting.Key == "GOARCH" {
						v.Installed.Arch = setting.Value
					}
				}
			}
		}
	}
	selected := *source
	var packagedSource *homebrew.Package
	if executable, err := os.Executable(); err == nil {
		packagedSource, err = homebrew.Detect(executable)
		if err != nil {
			return err
		}
	}
	if v.Installed.Path != "" {
		pkg, err := homebrew.Detect(v.Installed.Path)
		if err != nil {
			return err
		}
		if pkg != nil {
			v.Installed.Version = pkg.Version
			v.Installed.Revision = pkg.Revision
			v.Installed.Dirty = false
			v.BuiltArtifact = "verified Homebrew package manifest"
			if packagedSource == nil {
				packagedSource = pkg
			}
		}
	}
	if selected == "" && packagedSource != nil {
		selected = packagedSource.Source
		v.Source.Path = packagedSource.Source
		v.Source.Version = packagedSource.Version
		v.Source.Revision = packagedSource.Revision
	}
	if dir, err := shellStateDirectory(); err == nil {
		if receipt, err := install.ReadReceipt(dir); err == nil {
			if selected == "" {
				selected = receipt.Source
			}
			if matches, err := receipt.MatchesExecutable(v.Installed.Path); err == nil && matches && packagedSource == nil {
				v.Installed.Version = receipt.Version
				v.Installed.Revision = receipt.Revision
				v.Installed.Dirty = receipt.Dirty
				if fields := strings.Fields(receipt.Version); len(fields) >= 2 && fields[0] == "sdlc" {
					v.Installed.Version = fields[1]
				}
				v.BuiltArtifact = "verified installation receipt"
			}
		} else if errors.Is(err, os.ErrNotExist) && selected == "" {
			if data, err := viewReadFile(filepath.Join(dir, "runtime.json"), 1<<20, true); err == nil {
				var state runtimeimage.State
				if json.Unmarshal(data, &state) == nil && state.Version == 1 && filepath.IsAbs(state.Source) {
					selected = state.Source
				}
			}
		}
	}
	if selected == "" {
		selected, _ = os.Getwd()
	}
	if *source == "" && packagedSource != nil {
		// Source identity comes from the verified archive manifest; no Git checkout is required.
	} else if root, err := checkoutRoot(ctx, selected); err == nil {
		module, err := viewReadFile(filepath.Join(root, "go.mod"), 64*1024, false)
		if err == nil && strings.Contains("\n"+string(module), "\nmodule github.com/tjpeel/sdlc\n") {
			v.Source.Path = root
			data, err := viewReadFile(filepath.Join(root, "internal/buildinfo/version.go"), 64*1024, false)
			if err == nil {
				if syntax, err := parser.ParseFile(token.NewFileSet(), "version.go", data, 0); err == nil {
					ast.Inspect(syntax, func(node ast.Node) bool {
						decl, ok := node.(*ast.ValueSpec)
						if ok && len(decl.Names) == 1 && decl.Names[0].Name == "Version" && len(decl.Values) == 1 {
							if literal, ok := decl.Values[0].(*ast.BasicLit); ok && literal.Kind == token.STRING {
								v.Source.Version, _ = strconv.Unquote(literal.Value)
							}
						}
						return true
					})
				}
			}
			if data, err := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "HEAD").Output(); err == nil {
				v.Source.Revision = strings.TrimSpace(string(data))
			}
			if source, err := project.InspectIdentity(ctx, root); err == nil {
				v.Source.Dirty = source.Dirty
				v.Source.Revision = source.Revision
			}
			if *source != "" && (v.Source.Version == "not recorded" || v.Source.Version == "") {
				return fmt.Errorf("SDLC source version is unavailable")
			}
		} else if *source != "" {
			return fmt.Errorf("--source must identify the SDLC source checkout")
		}
	} else if *source != "" {
		return err
	}
	if dir, err := shellStateDirectory(); err == nil {
		if data, err := viewReadFile(filepath.Join(dir, "runtime.json"), 256*1024, true); err == nil {
			var state runtimeimage.State
			if err := localRuntimeRecord(data, &state); err == nil {
				v.Runtime = &state
				v.RuntimeStatus = "recorded locally; Docker not queried"
				v.RuntimeCategory = "unverified"
			} else {
				v.RuntimeStatus = "invalid local record"
				v.RuntimeCategory = "problem"
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			v.RuntimeStatus = "local record unavailable"
			v.RuntimeCategory = "problem"
		}
	} else {
		v.RuntimeStatus = "local state unavailable: " + err.Error()
		v.RuntimeCategory = "problem"
	}
	if *asJSON {
		return json.NewEncoder(out).Encode(v)
	}
	return writeVersionDetails(out, v)
}

func knownIdentity(value string) bool {
	return value != "" && value != "unknown" && value != "not recorded"
}

// localRuntimeRecord checks the saved evidence only; it never queries Docker.
func localRuntimeRecord(data []byte, state *runtimeimage.State) error {
	if !utf8.Valid(data) || json.Unmarshal(data, state) != nil || state.Version != 1 || !strings.HasPrefix(state.ImageID, "sha256:") || len(state.ImageID) != 71 || strings.Trim(strings.TrimPrefix(state.ImageID, "sha256:"), "0123456789abcdef") != "" {
		return fmt.Errorf("runtime.json does not contain a valid version 1 image record")
	}
	if state.Inventory != nil {
		if err := runtimeimage.ValidateInventory(*state.Inventory); err != nil {
			return err
		}
	}
	if state.DependencyPins != nil {
		if err := state.DependencyPins.Validate(); err != nil {
			return err
		}
	}
	if state.BuildRecipe != "" && (len(state.BuildRecipe) > 1<<20 || !utf8.ValidString(state.BuildRecipe) || strings.ContainsRune(state.BuildRecipe, 0)) {
		return fmt.Errorf("runtime build recipe contains invalid text")
	}
	return nil
}

func writeVersionDetails(out io.Writer, v versionView) error {
	fmt.Fprintln(out, textview.Heading("Running CLI"))
	fmt.Fprintf(out, "Running SDLC: %s (%s; dirty=%t; %s/%s)\n", dashboard.SafeText(v.Running.Version), dashboard.SafeText(v.Running.Revision), v.Running.Dirty, v.Running.OS, v.Running.Arch)
	fmt.Fprintln(out, textview.Heading("Installed CLI on PATH"))
	fmt.Fprintf(out, "Installed on PATH: %s (%s; revision=%s; dirty=%t; %s/%s)\nLast built CLI artifact: %s\n", dashboard.SafeText(v.Installed.Version), dashboard.SafeText(v.Installed.Path), dashboard.SafeText(v.Installed.Revision), v.Installed.Dirty, v.Installed.OS, v.Installed.Arch, v.BuiltArtifact)
	fmt.Fprintln(out, textview.Heading("SDLC source"))
	fmt.Fprintf(out, "SDLC source: %s (%s; revision=%s; dirty=%t)\n", dashboard.SafeText(v.Source.Version), dashboard.SafeText(v.Source.Path), dashboard.SafeText(v.Source.Revision), v.Source.Dirty)
	fmt.Fprintln(out, textview.Heading("Identity comparison"))
	compared, mismatches := 0, 0
	for _, pair := range []struct{ name, a, b string }{
		{"Running/PATH version", v.Running.Version, v.Installed.Version},
		{"Running/PATH revision", v.Running.Revision, v.Installed.Revision},
		{"PATH/source version", v.Installed.Version, v.Source.Version},
		{"PATH/source revision", v.Installed.Revision, v.Source.Revision},
	} {
		if !knownIdentity(pair.a) || !knownIdentity(pair.b) {
			fmt.Fprintln(out, textview.Status("unverified", pair.name+": identity unavailable"))
			continue
		}
		compared++
		if pair.a != pair.b {
			mismatches++
			fmt.Fprintln(out, textview.Status("problem", pair.name+" differs: "+dashboard.SafeText(pair.a)+" / "+dashboard.SafeText(pair.b)))
		}
	}
	if compared > 0 && mismatches == 0 {
		fmt.Fprintln(out, textview.Status("configured", "Comparable version and revision values match; execution readiness is not checked here."))
	}
	fmt.Fprintln(out, textview.Heading("Runtime image record"))
	fmt.Fprintln(out, textview.Status(v.RuntimeCategory, "Runtime image: "+v.RuntimeStatus))
	if v.Runtime != nil {
		fmt.Fprintf(out, "Image ID: %s\nRuntime source revision: %s\n", dashboard.SafeText(v.Runtime.ImageID), dashboard.SafeText(v.Runtime.Revision))
	}

	return nil
}

type onboardingStep struct {
	Name        string `json:"name"`
	State       string `json:"state"`
	Purpose     string `json:"purpose"`
	Instruction string `json:"instruction"`
	Category    string `json:"category"`
	Reason      string `json:"reason"`
}

func onboardCommand(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "status" {
		return fmt.Errorf("usage: sdlc onboard status [--json]")
	}
	flags := flag.NewFlagSet("onboard", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	asJSON := flags.Bool("json", false, "JSON")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("onboard status accepts no positional arguments")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	root, err := checkoutRoot(ctx, cwd)
	if err != nil {
		return err
	}
	steps := []onboardingStep{
		{Name: "project", State: "needs configuration", Category: "setup", Reason: "No local project settings found.", Purpose: "Define project inputs and checks.", Instruction: "Run sdlc init; review .sdlc/project.json."},
		{Name: "checks", State: "needs review", Category: "review", Reason: "Project checks have not been inspected or executed.", Purpose: "Validate completed work with project checks.", Instruction: "Review checks in .sdlc/project.json; onboarding does not execute them."},
		{Name: "runtime", State: "not recorded", Category: "setup", Reason: "No local runtime image record found.", Purpose: "Pin the execution image used by jobs.", Instruction: "Use sdlc runtime status --offline to inspect local evidence; build explicitly if required."},
		{Name: "providers", State: "unchecked", Category: "unverified", Reason: "Provider clients and accounts are not checked here.", Purpose: "Allow official provider clients to perform work.", Instruction: "Use sdlc auth status and review provider setup; no connected checks run here."},
		{Name: "github", State: "unchecked", Category: "unverified", Reason: "GitHub access is not checked here.", Purpose: "Select the GitHub identity and repository for work.", Instruction: "Use sdlc github list; connected validation is explicit."},
		{Name: "signing", State: "unchecked", Category: "unverified", Reason: "Signing configuration is not checked here.", Purpose: "Sign commits using the configured identity.", Instruction: "Use sdlc signing status; verify setup before launching work."},
		{Name: "work", State: "not found", Category: "setup", Reason: "No numbered local tickets found.", Purpose: "Supply local references and numbered tickets.", Instruction: "Add ignored tickets under .sdlc/work/REFERENCE/tickets, then run sdlc work --references."},
		{Name: "background terminal", State: "bridge not recorded", Category: "optional", Reason: "An external controller terminal is supported without the bridge.", Purpose: "Keep job controllers independent of this shell.", Instruction: "Use the suggested command in another terminal; optionally run sdlc terminal setup explicitly, then sdlc terminal status."},
	}
	if data, err := viewReadFile(filepath.Join(root, project.ConfigPath), 64*1024, false); err == nil {
		if cfg, err := project.ParseConfig(data); err == nil {
			steps[0].State, steps[0].Category, steps[0].Reason = "configured locally", "configured", "Project settings passed local validation."
			steps[0].Instruction = "Review inputs and checks in .sdlc/project.json before launching work."
			if len(cfg.Checks) > 0 {
				steps[1].State, steps[1].Category, steps[1].Reason = "configured; not executed", "unverified", fmt.Sprintf("%d check commands configured; results are not checked here.", len(cfg.Checks))
			} else {
				steps[1].Reason = "No check commands configured."
			}
		} else {
			steps[0].State, steps[0].Category, steps[0].Reason = "invalid; needs review", "problem", err.Error()
			steps[0].Instruction = "Repair .sdlc/project.json, then run sdlc onboard status again."
			steps[1].Reason = "Checks cannot be assessed until project settings are valid."
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		steps[0].State, steps[0].Category, steps[0].Reason = "unavailable; needs review", "problem", err.Error()
		steps[0].Instruction = "Review ownership and access to .sdlc/project.json, then run sdlc onboard status again."
	}
	if dir, err := shellStateDirectory(); err == nil {
		if data, err := viewReadFile(filepath.Join(dir, "runtime.json"), 256*1024, true); err == nil {
			var state runtimeimage.State
			if err := localRuntimeRecord(data, &state); err == nil {
				steps[2].State, steps[2].Category, steps[2].Reason = "recorded locally; unverified", "unverified", "A local image record exists; Docker and image availability are not checked here."
			} else {
				steps[2].State, steps[2].Category, steps[2].Reason = "invalid local record", "problem", err.Error()
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			steps[2].State, steps[2].Category, steps[2].Reason = "local record unavailable", "problem", err.Error()
		}
		if _, err := viewReadFile(filepath.Join(dir, "terminal", "ready.json"), 16*1024, true); err == nil {
			status, err := terminallaunch.TerminalStatus(filepath.Join(dir, "terminal"))
			if err != nil {
				steps[7].State, steps[7].Category, steps[7].Reason = "bridge record unavailable", "problem", err.Error()
			} else if status.Ready {
				steps[7].State, steps[7].Category, steps[7].Reason = "configured bridge; native focus unverified", "unverified", status.Message
			} else {
				steps[7].State, steps[7].Category, steps[7].Reason = "bridge needs review", "problem", status.Message
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			steps[7].State, steps[7].Category, steps[7].Reason = "bridge record unavailable", "problem", err.Error()
		}
	} else {
		for _, i := range []int{2, 7} {
			steps[i].State, steps[i].Category, steps[i].Reason = "local state unavailable", "problem", err.Error()
		}
	}
	if refs, err := project.References(ctx, root); err == nil {
		tickets, problems := 0, []string{}
		for _, ref := range refs.References {
			tickets += len(ref.Tickets)
			if ref.Error != "" {
				problems = append(problems, ref.Name+": "+ref.Error)
			}
		}
		if len(problems) > 0 {
			steps[6].State, steps[6].Category, steps[6].Reason = "local work needs review", "problem", strings.Join(problems, "; ")
			steps[6].Instruction = "Run sdlc work --references and repair the reported local work problem."
		} else if tickets > 0 {
			steps[6].State, steps[6].Category, steps[6].Reason = "local tickets present; contents unverified", "unverified", fmt.Sprintf("%d numbered tickets found across %d references; ticket bodies are not read here.", tickets, len(refs.References))
			steps[6].Instruction = "Use sdlc work --references, then inspect the selected ticket before launching work."
		} else if len(refs.References) > 0 {
			steps[6].State, steps[6].Reason = "references present; no numbered tickets", "References alone do not supply work tickets."
		}
	} else {
		steps[6].State, steps[6].Category, steps[6].Reason = "local work unavailable", "problem", err.Error()
		steps[6].Instruction = "Run sdlc work --references and review the reported local work problem."
	}
	result := struct {
		Root  string           `json:"root"`
		Steps []onboardingStep `json:"steps"`
	}{root, steps}
	if *asJSON {
		return json.NewEncoder(out).Encode(result)
	}
	fmt.Fprintf(out, "Project onboarding: %s\n", dashboard.SafeText(root))
	fmt.Fprintln(out, textview.Heading("Needs attention"))
	attention := false
	for _, category := range []string{"problem", "setup", "review"} {
		for i, step := range steps {
			if step.Category == category {
				attention = true
				fmt.Fprintf(out, "%s (step %d): %s\n", textview.Status(category, step.Name), i+1, dashboard.SafeText(step.Reason))
			}
		}
	}
	if !attention {
		fmt.Fprintln(out, "No local setup problems observed. Execution readiness is unverified.")
	}
	for _, category := range []string{"problem", "setup", "review", "unverified"} {
		found := false
		for _, step := range steps {
			if step.Category == category {
				fmt.Fprintf(out, "Next: %s\n", dashboard.SafeText(step.Instruction))
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	fmt.Fprintln(out, "Offline summary: provider access, GitHub access, signing, checks and Docker availability are not checked here.")
	fmt.Fprintln(out, textview.Heading("Walkthrough"))
	for i, step := range steps {
		fmt.Fprintf(out, "\n%d. %s\n Status: %s\n Reason: %s\n Purpose: %s\n Next: %s\n", i+1, step.Name, textview.Status(step.Category, step.State), dashboard.SafeText(step.Reason), step.Purpose, step.Instruction)
	}
	return nil
}

func inspectCommand(ctx context.Context, args []string, out io.Writer) error {
	selector := ""
	remaining := []string{}
	for _, arg := range args {
		if strings.HasPrefix(arg, "@") {
			if selector != "" {
				return fmt.Errorf("inspect accepts one selector")
			}
			selector = arg
		} else {
			remaining = append(remaining, arg)
		}
	}
	flags := flag.NewFlagSet("inspect", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	reference := flags.String("reference", "", "reference")
	ticket := flags.String("ticket", "", "ticket")
	asJSON := flags.Bool("json", false, "JSON")
	if err := flags.Parse(remaining); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("inspect accepts a ticket selector or --reference and --ticket")
	}
	if selector != "" {
		if *reference != "" || *ticket != "" {
			return fmt.Errorf("do not mix selectors and flags")
		}
		if strings.HasPrefix(selector, "@ref:") {
			*reference = strings.TrimPrefix(selector, "@ref:")
		} else if strings.HasPrefix(selector, "@run:") {
			args := []string{"--run", strings.TrimPrefix(selector, "@run:")}
			if *asJSON {
				args = append(args, "--json")
			} else {
				args = append(args, "--once")
			}
			return dashboardCommand(ctx, args, out)
		} else {
			var found bool
			*reference, *ticket, found = strings.Cut(strings.TrimPrefix(selector, "@"), "/")
			if !found {
				return fmt.Errorf("ticket selector must be @REFERENCE/NUMBERED_FILE")
			}
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	work, err := project.InspectWork(ctx, cwd, *reference)
	if err != nil {
		return err
	}
	if *ticket == "" {
		if *asJSON {
			return json.NewEncoder(out).Encode(work)
		}
		fmt.Fprintf(out, "Reference: %s\n", dashboard.SafeText(work.Reference))
		for _, name := range work.Tickets {
			fmt.Fprintln(out, dashboard.SafeText(name))
		}
		return nil
	}
	if filepath.Base(*ticket) != *ticket {
		return fmt.Errorf("ticket must be a numbered filename")
	}
	selected := ""
	for _, name := range work.Tickets {
		if filepath.Base(name) == *ticket {
			selected = name
			break
		}
	}
	if selected == "" {
		return fmt.Errorf("ticket is not present in the selected reference")
	}
	data, err := viewReadFile(filepath.Join(work.Root, filepath.FromSlash(selected)), 256*1024, false)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	for i := range lines {
		lines[i] = dashboard.SafeText(lines[i])
	}
	content := strings.Join(lines, "\n")
	if *asJSON {
		return json.NewEncoder(out).Encode(struct {
			Root      string `json:"root"`
			Reference string `json:"reference"`
			Ticket    string `json:"ticket"`
			Content   string `json:"content"`
		}{work.Root, work.Reference, selected, content})
	}
	_, err = fmt.Fprintf(out, "%s\n%s\n", dashboard.SafeText(selected), content)
	return err
}

func viewName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\") && strings.IndexFunc(name, unicode.IsControl) < 0
}
func viewOwned(info os.FileInfo) bool {
	if info == nil {
		return false
	}
	value := reflect.ValueOf(info.Sys())
	if value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return false
	}
	uid := value.FieldByName("Uid")
	return uid.IsValid() && uid.CanUint() && uid.Uint() == uint64(os.Geteuid())
}
func viewDirectory(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("directory must be absolute and clean")
	}
	for p := path; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("directory must not contain symbolic links")
		}
		if p == filepath.Dir(p) {
			return nil
		}
	}
}
func viewPrivateDirectory(path string) error {
	ancestor := path
	for {
		_, err := os.Lstat(ancestor)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		ancestor = filepath.Dir(ancestor)
	}
	if err := viewDirectory(ancestor); err != nil {
		return err
	}
	info, err := os.Stat(ancestor)
	if err != nil || !viewOwned(info) || info.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("state directory parent must be owned and not writable by others")
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	if err := viewDirectory(path); err != nil {
		return err
	}
	info, err = os.Stat(path)
	if err != nil || !viewOwned(info) || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("state directory must be private and owned")
	}
	return nil
}
func viewReadFile(path string, limit int64, private bool) ([]byte, error) {
	if err := viewDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("file must be regular without links")
	}
	if private {
		stat := reflect.ValueOf(info.Sys())
		if stat.Kind() == reflect.Pointer {
			stat = stat.Elem()
		}
		links := stat.FieldByName("Nlink")
		if !viewOwned(info) || info.Mode().Perm()&0077 != 0 || (links.IsValid() && links.CanUint() && links.Uint() != 1) {
			return nil, fmt.Errorf("state file must be private, owned and without links")
		}
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("file changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file exceeds inspection limit")
	}
	return data, nil
}
