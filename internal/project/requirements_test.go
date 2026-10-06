package project

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func linkedFixture(t *testing.T) (string, string) {
	t.Helper()
	root := workRepo(t)
	workTicket(t, root, "JOB", "1-first.md")
	write(t, root, ConfigPath, `{"version":1,"checks":[],"input_files":[]}`)
	return root, ".sdlc/work/JOB/tickets/1-first.md"
}
func TestLaunchIncludesLinkedSpecificationAndCycles(t *testing.T) {
	root, ticket := linkedFixture(t)
	write(t, root, ticket, "**Source specification:** [Example](../specification.md)\n[dependency][next]\n[next]: <2-sibling doc.md> \"Title\"\n")
	write(t, root, ".sdlc/work/JOB/specification.md", "[decisions](decisions%20record.md)\n[back](tickets/1-first.md)\n")
	write(t, root, ".sdlc/work/JOB/decisions record.md", "[spec](specification.md)\n")
	write(t, root, ".sdlc/work/JOB/tickets/2-sibling doc.md", "sibling")
	got, err := Launch(context.Background(), root, "JOB", "1-first.md", nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{ticket, ".sdlc/work/JOB/specification.md", ".sdlc/work/JOB/tickets/2-sibling doc.md", ".sdlc/work/JOB/decisions record.md"}
	if !reflect.DeepEqual(got.Inputs, want) {
		t.Fatalf("inputs %v, want %v", got.Inputs, want)
	}
	frozen, err := LaunchFrozen(context.Background(), root, "JOB", "1-first.md", []string{ticket})
	if err != nil || !reflect.DeepEqual(frozen.Inputs, []string{ticket}) {
		t.Fatalf("frozen inputs rediscovered: %+v %v", frozen, err)
	}
}
func TestRequirementsIgnoreImagesURLsAnchorsAndCode(t *testing.T) {
	root, ticket := linkedFixture(t)
	write(t, root, ticket, "[web](https://example.invalid/missing.md) [anchor](#missing.md) ![image](../missing.md) ![ref image][missing]\n[missing]: ../missing.md\n`[inline](../missing.md)`\n```md\n[fenced](../missing.md)\n```\n    [indented](../missing.md)\n")
	got, err := ResolveRequirements(context.Background(), root, "JOB", []string{ticket})
	if err != nil || !reflect.DeepEqual(got, []string{ticket}) {
		t.Fatalf("false link: %v %v", got, err)
	}
}
func TestRequirementsMissingUnsafeAndCrossReferenceLinks(t *testing.T) {
	for _, target := range []string{"../missing.md", "../../OTHER/spec.md", "../../../../.secrets/key.md", "/tmp/private.md"} {
		t.Run(target, func(t *testing.T) {
			root, ticket := linkedFixture(t)
			write(t, root, ticket, "[doc]("+target+")")
			_, err := Launch(context.Background(), root, "JOB", "1-first.md", nil)
			if err == nil || !strings.Contains(err.Error(), ticket) || !strings.Contains(err.Error(), target) {
				t.Fatalf("wanted referring path and exact target: %v", err)
			}
		})
	}
}
func TestRequirementsRejectLinkedFilesystemLinks(t *testing.T) {
	root, ticket := linkedFixture(t)
	write(t, root, ticket, "[doc](../linked.md)")
	if err := os.Symlink(filepath.Join(root, "README.md"), filepath.Join(root, ".sdlc/work/JOB/linked.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := Launch(context.Background(), root, "JOB", "1-first.md", nil); err == nil {
		t.Fatal("symlink accepted")
	}
}
func TestRequirementsBoundTotalBytes(t *testing.T) {
	root, ticket := linkedFixture(t)
	write(t, root, ticket, "[doc](../large.md)")
	write(t, root, ".sdlc/work/JOB/large.md", strings.Repeat("x", requirementBytes))
	if _, err := Launch(context.Background(), root, "JOB", "1-first.md", nil); err == nil || !strings.Contains(err.Error(), "16 MiB") {
		t.Fatalf("unbounded graph: %v", err)
	}
}

func TestRequirementsBoundFileCount(t *testing.T) {
	root, ticket := linkedFixture(t)
	var links strings.Builder
	for i := 0; i < requirementFiles; i++ {
		name := fmt.Sprintf("document-%03d.md", i)
		write(t, root, ".sdlc/work/JOB/"+name, "Document")
		fmt.Fprintf(&links, "[doc](../%s)\n", name)
	}
	write(t, root, ticket, links.String())
	if _, err := Launch(context.Background(), root, "JOB", "1-first.md", nil); err == nil || !strings.Contains(err.Error(), "256 files") {
		t.Fatalf("unbounded file count: %v", err)
	}
}

func TestRequirementsIgnoreNestedFenceExamples(t *testing.T) {
	for _, marker := range []string{"`", "~"} {
		t.Run(marker, func(t *testing.T) {
			root, ticket := linkedFixture(t)
			outer := strings.Repeat(marker, 4)
			inner := strings.Repeat(marker, 3)
			example := outer + "markdown\n" + inner + "md\n[example](../missing.md)\n" + inner + "\n[example after inner](../also-missing.md)\n" + outer + " trailing content\n[still example](../still-missing.md)\n" + outer + marker + "\n[real source](../specification.md)\n"
			write(t, root, ticket, example)
			write(t, root, ".sdlc/work/JOB/specification.md", "Actual specification")
			got, err := Launch(context.Background(), root, "JOB", "1-first.md", nil)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Inputs, []string{ticket, ".sdlc/work/JOB/specification.md"}) {
				t.Fatalf("fence examples changed requirements: %v", got.Inputs)
			}
		})
	}
}

func TestRequirementsIgnoreMalformedExternalCitation(t *testing.T) {
	root, ticket := linkedFixture(t)
	write(t, root, ticket, "[external citation](https://example.invalid/%not-an-escape.md) [external](//example.invalid/%zz.md)")
	got, err := Launch(context.Background(), root, "JOB", "1-first.md", nil)
	if err != nil || !reflect.DeepEqual(got.Inputs, []string{ticket}) {
		t.Fatalf("external citation became local requirement: %v %v", got.Inputs, err)
	}
}

func TestRequirementsIgnoreHiddenTemplateComments(t *testing.T) {
	root, ticket := linkedFixture(t)
	template := "<!-- [template example](../missing.md) -->\n<!--\n```markdown\n[hidden fenced example](../also-missing.md)\n[hidden ref][hidden]\n[hidden]: ../hidden.md\n-->\n```md\n<!-- comment marker in a code example\n```\n`<!-- inline marker`\nSome text <!-- [hidden inline](../hidden-inline.md) --> [visible source](../specification.md)\n"
	write(t, root, ticket, template)
	write(t, root, ".sdlc/work/JOB/specification.md", "Visible requirement")
	got, err := Launch(context.Background(), root, "JOB", "1-first.md", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Inputs, []string{ticket, ".sdlc/work/JOB/specification.md"}) {
		t.Fatalf("hidden template link changed requirements: %v", got.Inputs)
	}
}
