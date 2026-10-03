package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnknownProviderIsRejectedBeforeAccessingDocker(t *testing.T) {
	for _, command := range []string{"login", "status"} {
		err := auth(context.Background(), []string{command, "--provider", "untrusted"})
		if err == nil || !strings.Contains(err.Error(), "provider must be") {
			t.Fatal("unknown provider was enabled")
		}
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
