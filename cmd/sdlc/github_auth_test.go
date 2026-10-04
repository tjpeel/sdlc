package main

import (
	"testing"
)

func TestGitHubAuthRequiresExplicitServiceAndVerificationChoice(t *testing.T) {
	for _, args := range [][]string{{"login", "--service", "github", "--profile", "personal"}, {"logout", "--service", "github"}, {"status", "--service", "github", "--verify"}, {"status", "--service", "github"}} {
		options, err := parseAuthOptions(args)
		if err != nil || options.service != "github" || len(options.providers) != 0 {
			t.Fatalf("GitHub selection failed: %v %v", args, err)
		}
	}
	for _, args := range [][]string{{"logout"}, {"status", "--profile", "work"}, {"login", "--service", "github", "--profile", "../work"}, {"status", "--service", "github", "--profile="}, {"status", "--verify"}, {"status", "--service="}, {"status", "--service", "other"}, {"login", "--service", "github", "--verify"}, {"status", "--service", "github", "--all"}, {"status", "--service", "github", "--provider", "codex"}} {
		if _, err := parseAuthOptions(args); err == nil {
			t.Fatalf("ambiguous credentials accepted: %v", args)
		}
	}
}

func TestRunProfileChoiceIsExplicitAndRetainedOnResume(t *testing.T) {
	options, err := parseRunOptions([]string{"--reference", "example", "--ticket", "01-test.md", "--github-profile", "work", "--dry-run"})
	if err != nil || options.githubProfile != "work" {
		t.Fatalf("profile choice lost: %v", err)
	}
	for _, args := range [][]string{
		{"--reference", "example", "--ticket", "01-test.md", "--github-profile", ""},
		{"--reference", "example", "--ticket", "01-test.md", "--github-profile", "../work"},
		{"--reference", "example", "--ticket", "01-test.md", "--resume", "recorded", "--github-profile", "personal"},
	} {
		if _, err := parseRunOptions(args); err == nil {
			t.Fatalf("invalid or changed resume profile accepted: %v", args)
		}
	}
}
