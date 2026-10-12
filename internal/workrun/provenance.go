package workrun

import (
	"strings"

	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/runtimepins"
)

// RuntimeProvenance is captured with the run's image. Absent fields stay unknown;
// displaying a saved run must not substitute today's installation or host skills.
type RuntimeProvenance struct {
	SourceRevision string            `json:"source_revision,omitempty"`
	SkillsRevision string            `json:"skills_revision,omitempty"`
	AgentsRevision string            `json:"agents_revision,omitempty"`
	Sources        map[string]string `json:"sources,omitempty"`
}

func CaptureRuntimeProvenance(state runtimeimage.State) *RuntimeProvenance {
	provenance := &RuntimeProvenance{Sources: map[string]string{}}
	if objectID.MatchString(strings.TrimSuffix(state.Revision, "-dirty")) {
		provenance.SourceRevision = state.Revision
		provenance.Sources["source"] = "runtime_descriptor"
	}
	record := func(name, revision, source string) {
		if !objectID.MatchString(revision) {
			return
		}
		target := &provenance.SkillsRevision
		if name == "agents" {
			target = &provenance.AgentsRevision
		}
		if *target == "" {
			*target = revision
			provenance.Sources[name] = source
		}
	}
	record("skills", state.SkillsRevision, "runtime_descriptor")
	if state.Inventory != nil {
		for _, dependency := range state.Inventory.Dependencies {
			if dependency.Kind != "git" {
				continue
			}
			switch dependency.Source {
			case "tjpeel/skills":
				record("skills", dependency.Version, "runtime_inventory")
			case "tjpeel/agents":
				record("agents", dependency.Version, "runtime_inventory")
			}
		}
	}
	if state.DependencyPins != nil {
		record("skills", state.DependencyPins.Arguments["SKILLS_REVISION"], "runtime_dependency_pins")
		record("agents", state.DependencyPins.Arguments["AGENTS_REVISION"], "runtime_dependency_pins")
	}
	if pins, err := runtimepins.ReadDockerfile([]byte(state.BuildRecipe)); err == nil {
		record("skills", pins.Arguments["SKILLS_REVISION"], "runtime_build_recipe")
		record("agents", pins.Arguments["AGENTS_REVISION"], "runtime_build_recipe")
	}
	return provenance
}
