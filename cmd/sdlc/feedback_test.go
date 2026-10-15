package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFeedbackPastePreservesMultilineAndBracketedPaste(t *testing.T) {
	text := "First finding\n" + strings.Repeat("long line ", 1000) + "\nSecond finding\n"
	got, err := readFeedbackPaste(context.Background(), strings.NewReader("\x1b[200~"+text+"\x1b[201~\x04"))
	if err != nil || got != text {
		t.Fatal("paste changed or truncated", err)
	}
	for _, input := range []string{"\x04", "hello\x03", "not submitted", strings.Repeat("x", 64*1024+1) + "\x04", "\xff\x04", "\x1b[200~unfinished\x04"} {
		if _, err := readFeedbackPaste(context.Background(), strings.NewReader(input)); err == nil {
			t.Fatal("invalid or cancelled input accepted")
		}
	}
}

func TestFeedbackPreviewIsPrivateAndDoesNotMutateOrDispatch(t *testing.T) {
	j, marker := readyPRFeedbackFixture(t)
	dir := filepath.Dir(j.Workspace)
	before, err := os.ReadFile(filepath.Join(dir, "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	text := "Private bulk feedback with file.go:42\n"
	source := filepath.Join(t.TempDir(), "feedback.md")
	if err := os.WriteFile(source, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--run", j.ID, "--dry-run"}, {"--run", j.ID, "--stdin", "--dry-run"}, {"--run", j.ID, "--file", source, "--dry-run"}} {
		var output bytes.Buffer
		if err := feedbackCommand(context.Background(), args, strings.NewReader(text), &output); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(output.String(), text) {
			t.Fatal("preview echoed private feedback")
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "feedback")); !os.IsNotExist(err) {
		t.Fatal("preview saved feedback", err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "journal.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("preview mutated journal")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("preview invoked runtime")
	}
}

func TestLocalFeedbackRunOptionsRequireIsolatedResume(t *testing.T) {
	base := runArgs("--resume", "saved", "--pr-feedback-file", "feedback.txt", "--dry-run")
	options, err := parseRunOptions(base)
	if err != nil || !options.processPRFeedback || options.prFeedbackFile != "feedback.txt" {
		t.Fatal(options, err)
	}
	for _, extra := range [][]string{{"--pr-feedback"}, {"--answer-file", "answer.txt"}, {"--ci-feedback-file", "ci.txt"}, {"--override-run-limits"}, {"--extend-session-turn-budget", "1"}, {"--review-repair-limit", "3"}, {"--initialize-ci-repair-budget"}, {"--initialize-review-repair-budget", "1"}} {
		if _, err := parseRunOptions(append(append([]string{}, base...), extra...)); err == nil {
			t.Fatal("combined recovery accepted", extra)
		}
	}
	if _, err := parseRunOptions(runArgs("--pr-feedback-file", "feedback.txt")); err == nil {
		t.Fatal("new run accepted feedback")
	}
}
