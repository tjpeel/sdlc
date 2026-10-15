package workrun

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/runtimepins"
)

func TestRuntimeProvenanceUsesFrozenInventoryAndLocalOverride(t *testing.T) {
	skills, agents := strings.Repeat("c", 40), strings.Repeat("d", 40)
	state := runtimeimage.State{Revision: testHead, Inventory: &runtimeimage.Inventory{Dependencies: []runtimeimage.Dependency{
		{Kind: "git", Source: "tjpeel/skills", Version: skills},
		{Kind: "git", Source: "tjpeel/agents", Version: agents},
	}}, DependencyPins: &runtimepins.Pins{Arguments: map[string]string{"SKILLS_REVISION": testBase}}}
	provenance := CaptureRuntimeProvenance(state)
	if provenance.SourceRevision != testHead || provenance.SkillsRevision != skills || provenance.AgentsRevision != agents || provenance.Sources["skills"] != "runtime_inventory" {
		t.Fatal("frozen runtime inventory lost", provenance)
	}
	state.SkillsRevision = testTree
	state.AgentsRevision = testTree
	provenance = CaptureRuntimeProvenance(state)
	if provenance.SkillsRevision != testTree || provenance.Sources["skills"] != "runtime_descriptor" || provenance.Sources["agents"] != "runtime_descriptor" || provenance.AgentsRevision != testTree {
		t.Fatal("local runtime skills override lost", provenance)
	}
	state.Inventory.Dependencies[1].Version = testBase
	if provenance.AgentsRevision != testTree {
		t.Fatal("captured provenance changed with installation state")
	}
}

func TestSourcePinRuntimeProvenanceUsesRecordedRecipe(t *testing.T) {
	recipe, err := os.ReadFile("../../runtime/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	pins, err := runtimepins.ReadDockerfile(recipe)
	if err != nil {
		t.Fatal(err)
	}
	provenance := CaptureRuntimeProvenance(runtimeimage.State{BuildRecipe: string(recipe)})
	if provenance.SkillsRevision != pins.Arguments["SKILLS_REVISION"] || provenance.AgentsRevision != pins.Arguments["AGENTS_REVISION"] || provenance.Sources["skills"] != "runtime_build_recipe" {
		t.Fatal("source-pin recipe provenance lost", provenance)
	}
	unknown := CaptureRuntimeProvenance(runtimeimage.State{Revision: "unknown"})
	if unknown.SourceRevision != "" || unknown.SkillsRevision != "" || unknown.AgentsRevision != "" {
		t.Fatal("missing runtime evidence was inferred", unknown)
	}
}

func TestSavedRunKeepsCapturedRuntimeProvenance(t *testing.T) {
	directory, journal := testRun(t)
	if journal.Plan.RuntimeProvenance != nil {
		t.Fatal("legacy plan inferred current runtime provenance")
	}
	journal.Plan.RuntimeProvenance = CaptureRuntimeProvenance(runtimeimage.State{Revision: testHead, SkillsRevision: testTree})
	if err := Save(directory, journal); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(directory)
	if err != nil || !reflect.DeepEqual(loaded.Plan.RuntimeProvenance, journal.Plan.RuntimeProvenance) {
		t.Fatal("frozen provenance did not survive checkpoint", err)
	}
}
