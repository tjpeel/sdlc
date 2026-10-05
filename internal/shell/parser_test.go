package shell

import (
	"reflect"
	"testing"
)

func TestParseLiteralArguments(t *testing.T) {
	args, err := Parse(`/run --input 'a file.md' --repo "$(ignored)" --ticket @DEMO-42/01-count.md --input ''`)
	want := []string{"run", "--input", "a file.md", "--repo", "$(ignored)", "--ticket", "@DEMO-42/01-count.md", "--input", ""}
	if err != nil || !reflect.DeepEqual(args, want) {
		t.Fatalf("got %#v, %v", args, err)
	}
}
func TestParseRejectsIncompleteInput(t *testing.T) {
	for _, input := range []string{`run`, `/run "unfinished`, `/run trailing\`, "/run \x1b[2J", "/"} {
		if _, err := Parse(input); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
}
func TestDryRunLiteralFlagValueIsNotOption(t *testing.T) {
	if hasOption([]string{"run", "--input", "--dry-run"}, "dry-run") {
		t.Fatal("literal input path became a dry-run option")
	}
	if !hasOption([]string{"run", "--input", "file", "--dry-run"}, "dry-run") {
		t.Fatal("dry-run flag not recognised")
	}
}
