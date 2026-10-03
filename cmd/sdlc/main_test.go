package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestInitRejectsArgumentsBeforeWritingProjectSettings(t *testing.T) {
	directory := t.TempDir()
	t.Chdir(directory)
	for _, args := range [][]string{{"extra"}, {"--source", directory}, {"--provider", "codex"}} {
		var output bytes.Buffer
		if err := initCommand(context.Background(), args, &output); err == nil {
			t.Fatalf("invalid arguments accepted: %v", args)
		}
		if output.Len() != 0 {
			t.Fatal("invalid arguments printed a setup summary")
		}
	}
	if _, err := os.Stat(filepath.Join(directory, ".sdlc")); !os.IsNotExist(err) {
		t.Fatal("invalid arguments changed the project")
	}
	if err := initCommand(context.Background(), []string{"--help"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}

func TestInitFromNestedDirectoryReportsProjectAndPreservesSettings(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	command := exec.Command("git", "init", "--initial-branch=main", directory)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create test repository: %v: %s", err, output)
	}
	if err := os.WriteFile(filepath.Join(directory, "go.mod"), []byte("module example.invalid/project\n\ngo 1.24.0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(directory, "source", "nested")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(nested)
	var output bytes.Buffer
	if err := initCommand(context.Background(), nil, &output); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"HEAD: no commits yet", "Project file: \"go.mod\"", "Tickets found: 0", "Project settings created: .sdlc/project.json", "Local project setup complete"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing %q in summary: %s", want, output.String())
		}
	}
	settings := filepath.Join(directory, ".sdlc", "project.json")
	before, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := initCommand(context.Background(), nil, &output); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(settings)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("repeat initialization replaced project settings", err)
	}
	if !strings.Contains(output.String(), "Project settings preserved") {
		t.Fatal("repeat initialization did not report existing settings")
	}
}

func TestUnknownProviderIsRejectedBeforeAccessingDocker(t *testing.T) {
	for _, command := range []string{"login", "status"} {
		err := auth(context.Background(), []string{command, "--provider", "untrusted"})
		if err == nil || !strings.Contains(err.Error(), "provider must be") {
			t.Fatal("unknown provider was enabled")
		}
	}
}

func TestInvalidInteractiveArgumentsDoNotCreateState(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	t.Setenv("SDLC_STATE_DIR", directory)
	for _, args := range [][]string{{"--provider="}, {"--provider", "untrusted"}, {"--provider", "codex", "prompt"}, {"--provider", "claude", "--dangerously-skip-permissions"},
		{"--provider", "codex", "--approval", "untrusted"}, {"--provider", "codex", "--permission-mode", "plan"},
		{"--provider", "claude", "--approval", "never"}, {"--provider", "claude", "--permission-mode", "unsupported"},
		{"--provider", "codex", "--approval="}, {"--provider", "codex", "--permission-mode="},
		{"--provider", "claude", "--approval="}, {"--provider", "claude", "--permission-mode="},
		{"--permission-mode", "plan"}, {"--permission-mode="}, {"--approval="}, {"--approval", "untrusted"}} {
		var output bytes.Buffer
		if err := interactive(context.Background(), args, &output); err == nil {
			t.Fatalf("invalid arguments were accepted: %v", args)
		}
		if output.Len() != 0 {
			t.Fatal("invalid arguments reached session startup")
		}
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatal("invalid interactive command changed installation state")
	}
}

func TestInteractiveProviderSelection(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want interactiveOptions
	}{
		{name: "default", want: interactiveOptions{provider: "codex", mode: "never"}},
		{name: "implicit Codex approvals", args: []string{"--approval", "on-request"}, want: interactiveOptions{provider: "codex", mode: "on-request"}},
		{name: "explicit Codex", args: []string{"--provider", "codex"}, want: interactiveOptions{provider: "codex", mode: "never"}},
		{name: "explicit Claude", args: []string{"--provider", "claude"}, want: interactiveOptions{provider: "claude", mode: "bypassPermissions"}},
		{name: "Claude plan", args: []string{"--provider", "claude", "--permission-mode", "plan"}, want: interactiveOptions{provider: "claude", mode: "plan"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseInteractiveOptions(test.args)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("selected %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestAuthProviderSelection(t *testing.T) {
	for _, test := range []struct {
		name      string
		args      []string
		providers []string
	}{
		{name: "default login", args: []string{"login"}, providers: []string{"codex"}},
		{name: "default status", args: []string{"status"}, providers: []string{"codex"}},
		{name: "Claude login", args: []string{"login", "--provider", "claude"}, providers: []string{"claude"}},
		{name: "Claude status", args: []string{"status", "--provider", "claude"}, providers: []string{"claude"}},
		{name: "Codex status", args: []string{"status", "--provider", "codex"}, providers: []string{"codex"}},
		{name: "all status", args: []string{"status", "--all"}, providers: []string{"codex", "claude"}},
		{name: "false all status", args: []string{"status", "--all=false"}, providers: []string{"codex"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseAuthOptions(test.args)
			if err != nil {
				t.Fatal(err)
			}
			if got.action != test.args[0] || !reflect.DeepEqual(got.providers, test.providers) {
				t.Fatalf("selected %+v, want action %s and providers %v", got, test.args[0], test.providers)
			}
		})
	}
}

func TestInvalidAuthArgumentsDoNotCreateState(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	t.Setenv("SDLC_STATE_DIR", directory)
	for _, args := range [][]string{nil, {"unknown"}, {"login", "--all"}, {"login", "--all=false"},
		{"login", "--provider="}, {"status", "--provider="}, {"status", "--provider=", "--all"},
		{"status", "--provider", "codex", "--all"}, {"status", "--all", "--provider", "claude"},
		{"status", "--all=false", "--provider", "codex"}, {"status", "unexpected"}} {
		if err := auth(context.Background(), args); err == nil {
			t.Fatalf("invalid arguments were accepted: %v", args)
		}
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatal("invalid authentication command changed installation state")
	}
}

func TestInstructionsCanBeConfiguredWithoutDockerOrRuntime(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("SDLC_STATE_DIR", filepath.Join(directory, "state"))
	var output bytes.Buffer
	if err := instructionsCommand([]string{"show"}, &output); err != nil {
		t.Fatal(err)
	}
	defaultContent := output.String()
	if !strings.Contains(defaultContent, "stop implementation immediately") || !strings.Contains(defaultContent, "until a human has answered") {
		t.Fatal("default instructions do not require a human answer")
	}
	file := filepath.Join(directory, "additional.md")
	custom := "Run the repository checks before declaring work complete.\n"
	if err := os.WriteFile(file, []byte(custom), 0600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := instructionsCommand([]string{"set", "--file", file}, &output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), custom) {
		t.Fatal("set unexpectedly printed private instruction contents")
	}
	output.Reset()
	if err := instructionsCommand([]string{"show"}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(output.String(), defaultContent) || !strings.Contains(output.String(), custom) {
		t.Fatal("shared instructions lost the human-answer rule or custom content")
	}
	if err := instructionsCommand([]string{"reset"}, &output); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := instructionsCommand([]string{"show"}, &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != defaultContent {
		t.Fatal("reset did not restore the default instructions")
	}
}

func TestInvalidInstructionsArgumentsDoNotCreateState(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "state")
	t.Setenv("SDLC_STATE_DIR", directory)
	for _, args := range [][]string{nil, {"unknown"}, {"show", "extra"}, {"reset", "--file", "extra"}, {"set"}, {"set", "--file", "unused", "extra"}} {
		if err := instructionsCommand(args, &bytes.Buffer{}); err == nil {
			t.Fatalf("invalid arguments were accepted: %v", args)
		}
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatal("invalid commands changed installation state")
	}
}
