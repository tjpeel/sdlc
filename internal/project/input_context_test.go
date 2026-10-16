package project

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestBlockersUseOnlyPlanningMetadata(t *testing.T) {
	cases := []struct {
		name, text         string
		present, completed bool
		parents            []string
		invalid            bool
	}{
		{name: "commented metadata", text: "<!--\n**Blocked by:** [example](99-example.md)\n-->\n# Current\n**Blocked by:** None", present: true, parents: []string{}},
		{name: "comment only", text: "<!--\n**Blocked by:** [example](99-example.md)\n-->\n# Current", parents: []string{}},
		{name: "none", text: "# Work\n**Blocked by:** None (can start immediately)\n\n[boundary](02-boundary.md)", present: true, parents: []string{}},
		{name: "prerequisite", text: "# Work\n**Blocked by:** [first](01-first.md) and [second](02-second.md#contract)", present: true, parents: []string{"01-first.md", "02-second.md"}},
		{name: "multiline", text: "# Work\n**Blocked by:**\n- [first](01-first.md)\n- [second](02-second.md)\n\nBody", present: true, parents: []string{"01-first.md", "02-second.md"}},
		{name: "ordinary reference", text: "# Work\n## Context\n[reference](01-first.md)", parents: []string{}},
		{name: "example", text: "# Work\n## Example\n**Blocked by:** [sample](01-first.md)", parents: []string{}},
		{name: "completed", text: "# Work\n**Status:** completed\n**Blocked by:** None (completed; merged PR: https://github.com/example/project/pull/1)", present: true, completed: true, parents: []string{}},
		{name: "ambiguous", text: "**Blocked by:** None ([first](01-first.md))", invalid: true},
		{name: "external", text: "**Blocked by:** [first](https://example.test/01-first.md)", invalid: true},
		{name: "traversal", text: "**Blocked by:** [first](../01-first.md)", invalid: true},
		{name: "unresolved", text: "**Blocked by:** To be determined", invalid: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d, err := ParseTicketBlockers([]byte(c.text))
			if (err != nil) != c.invalid {
				t.Fatal(d, err)
			}
			if err == nil && (d.Present != c.present || d.Completed != c.completed || !reflect.DeepEqual(d.Parents, c.parents)) {
				t.Fatal(d)
			}
		})
	}
}
func TestCapturedContextDistinguishesScopeAndLinkPurpose(t *testing.T) {
	root := t.TempDir()
	base := ".sdlc/work/EXAMPLE/"
	selected := base + "tickets/02-current.md"
	docs := map[string]string{
		selected:                       "# Current\n**Blocked by:** [earlier](01-earlier.md)\n\n[spec](../specification.md#shared-contract)\n[future boundary](03-later.md)",
		base + "specification.md":      "# Shared specification\n[another reference](tickets/03-later.md)",
		base + "tickets/01-earlier.md": "# Earlier\n**Blocked by:** None\n",
		base + "tickets/03-later.md":   "# Later\n**Blocked by:** [current](02-current.md)\n",
	}
	for path, text := range docs {
		full := filepath.Join(root, path)
		os.MkdirAll(filepath.Dir(full), 0700)
		if err := os.WriteFile(full, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	inputs, err := ResolveRequirements(context.Background(), root, "EXAMPLE", []string{selected})
	if err != nil {
		t.Fatal(err)
	}
	roles, err := DescribeRequirementContext(root, selected, inputs)
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]string{selected: "selected_ticket", base + "specification.md": "supporting_context", base + "tickets/01-earlier.md": "prerequisite_context", base + "tickets/03-later.md": "boundary_context"}
	if len(inputs) != len(expected) || len(roles) != len(expected) {
		t.Fatal(inputs, roles)
	}
	for _, item := range roles {
		if item.Role != expected[item.Path] {
			t.Fatal(item)
		}
		if item.Path == base+"tickets/03-later.md" && len(item.ReferencedBy) != 2 {
			t.Fatal("lost provenance", item)
		}
	}
	for path, text := range docs {
		actual, err := os.ReadFile(filepath.Join(root, path))
		if err != nil || string(actual) != text {
			t.Fatal("context file was modified", path, err)
		}
	}
}
