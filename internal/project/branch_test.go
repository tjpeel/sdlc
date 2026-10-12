package project

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestBranchNameUsesWorkReferenceAndTicketDescription(t *testing.T) {
	root := repo(t)
	for _, tc := range []struct {
		name, reference, ticket, format, want string
	}{
		{"default", "TASK-123", "01-Add_Cache.md", "", "TASK-123-add-cache"},
		{"explicit default", "TASK-123", "01-add-cache.md", DefaultBranchFormat, "TASK-123-add-cache"},
		{"project format", "TASK-123", "02-add-cache.md", "{reference}/{ticket}", "TASK-123/add-cache"},
		{"unsafe reference rendering", "Example stream", "03-add-cache.md", "", "Example-stream-add-cache"},
		{"leading dash rendering", "-draft", "03-add-cache.md", "", "draft-add-cache"},
		{"literal reference", "TASK$(false)", "04-add-cache.md", "", "TASK$(false)-add-cache"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := BranchName(context.Background(), root, tc.reference, tc.ticket, tc.format)
			if err != nil || got != tc.want {
				t.Fatalf("branch=%q error=%v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestBranchNameRejectsUnusableNamesBeforeWrites(t *testing.T) {
	root := repo(t)
	for _, format := range []string{"{reference}-{ticket}.lock", "{reference}..{ticket}", "{reference}/."} {
		if _, err := BranchName(context.Background(), root, "TASK-1", "01-add-cache.md", format); err == nil {
			t.Fatalf("accepted unusable format %q", format)
		}
	}
	if _, err := BranchName(context.Background(), root, strings.Repeat("A", 250), "01-add-cache.md", ""); err == nil {
		t.Fatal("accepted an oversized branch component")
	}
	if err := ValidateBranchName(context.Background(), root, "@{-1}"); err == nil {
		t.Fatal("accepted branch shorthand")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := BranchName(ctx, root, "TASK-1", "01-add-cache.md", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestProjectConfigValidatesBranchFormat(t *testing.T) {
	for _, format := range []string{"{reference}", "{ticket}-{reference}", "work/{reference}-{ticket}", "{reference}-{ticket}-{ticket}", "{reference}-{unknown}", "{reference} {ticket}"} {
		data := `{"version":1,"checks":[],"input_files":[],"branch_format":"` + format + `"}`
		if _, err := ParseConfig([]byte(data)); err == nil {
			t.Fatalf("accepted invalid branch format %q", format)
		}
	}
	c, err := ParseConfig([]byte(`{"version":1,"checks":[],"input_files":[],"branch_format":"{reference}/{ticket}"}`))
	if err != nil || c.BranchFormat != "{reference}/{ticket}" {
		t.Fatalf("project format lost: %+v %v", c, err)
	}
}
