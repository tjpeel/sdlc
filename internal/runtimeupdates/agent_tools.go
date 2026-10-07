package runtimeupdates

import (
	"context"
	"fmt"
	"strings"

	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/runtimepins"
)

var agentToolArguments = map[string]string{
	"npm/@openai/codex":             "CODEX_VERSION",
	"npm/@anthropic-ai/claude-code": "CLAUDE_VERSION",
	"git/tjpeel/skills":             "SKILLS_REVISION",
	"git/tjpeel/agents":             "AGENTS_REVISION",
}

// PlanAgentTools checks only the four selected tools. All other build pins retain
// their installed recipe values; legacy inherited defaults come from inventory.
func (checker Checker) PlanAgentTools(ctx context.Context, state runtimeimage.State, recipe []byte) (Plan, error) {
	var plan Plan
	if state.Inventory == nil {
		return plan, fmt.Errorf("runtime has no dependency inventory; rebuild before updating")
	}
	// The incoming checkout is not evidence of an older installation's build
	// images. Inventory records direct tools and Node, but cannot recover the
	// Go publisher or auxiliary images. Refuse rather than substitute new pins.
	if state.DependencyPins == nil && state.BuildRecipe == "" {
		return plan, fmt.Errorf("cannot retain legacy dependency pins for --agent-tools: installed build recipe and image provenance are unavailable; use --dependencies to resolve a complete baseline")
	}
	previous, err := recipePins(state, recipe)
	if err != nil {
		return plan, err
	}
	pins := runtimepins.Pins{Arguments: map[string]string{}, Images: map[string]string{}, SigningImage: previous.SigningImage, DaemonImage: previous.DaemonImage}
	for key, value := range previous.Arguments {
		pins.Arguments[key] = value
	}
	for key, value := range previous.Images {
		pins.Images[key] = value
	}
	for identity, argument := range managedArguments {
		if agentToolArguments[identity] != "" || pins.Arguments[argument] != "" {
			continue
		}
		parts := strings.SplitN(identity, "/", 2)
		dependency, err := installedDependency(*state.Inventory, parts[0], parts[1])
		if err != nil {
			return plan, fmt.Errorf("cannot retain legacy %s pin: %w", argument, err)
		}
		pins.Arguments[argument] = dependency.Version
	}
	// An inherited major Node tag can be made exact with its installed version
	// while retaining the recorded digest. Never resolve another base image.
	nodeReference := strings.SplitN(pins.Images["node"], "@", 2)
	if len(nodeReference) == 2 {
		tag := strings.TrimSuffix(strings.TrimPrefix(nodeReference[0], "node:"), "-bookworm")
		if _, valid := version(tag); !valid {
			node, err := installedDependency(*state.Inventory, "node", "nodejs")
			if err != nil || node.Track != tag {
				return plan, fmt.Errorf("cannot retain legacy Node pin: installed version/track unavailable")
			}
			pins.Images["node"] = "node:" + node.Version + "-bookworm@" + nodeReference[1]
		}
	}
	// Select first, so missing or ambiguous inventory cannot produce partial plans.
	selected := make([]runtimeimage.Dependency, 0, 4)
	for _, identity := range []string{"npm/@openai/codex", "npm/@anthropic-ai/claude-code", "git/tjpeel/skills", "git/tjpeel/agents"} {
		parts := strings.SplitN(identity, "/", 2)
		dependency, err := installedDependency(*state.Inventory, parts[0], parts[1])
		if err != nil {
			return plan, err
		}
		selected = append(selected, dependency)
		pins.Arguments[agentToolArguments[identity]] = dependency.Version
	}
	fetch := checker.fetcher()
	// Older source-pin builds recorded an exact daemon version but no digest.
	// Freeze that same tag; this is baseline completion, never a release lookup.
	if !strings.Contains(pins.DaemonImage, "@") {
		number, _, err := referenceParts(pins.DaemonImage, "docker", "-dind")
		if err != nil {
			return plan, fmt.Errorf("cannot retain existing daemon version for --agent-tools: %w", err)
		}
		digest, err := fetch.image(ctx, runtimeimage.Dependency{Source: "library/docker", Track: number + "-dind"})
		if err != nil {
			return plan, fmt.Errorf("cannot freeze existing daemon tag for --agent-tools: %w", err)
		}
		pins.DaemonImage += "@" + digest
	}
	if err := pins.Validate(); err != nil {
		return plan, fmt.Errorf("cannot retain existing dependency pins for --agent-tools: %w; use --dependencies to resolve a complete baseline", err)
	}
	for _, dependency := range selected {
		result := fetch.check(ctx, dependency)
		plan.Report.Results = append(plan.Report.Results, result)
		if result.Status == Unavailable || (dependency.Kind == "git" && result.Status != Current && result.Status != Update) {
			return Plan{}, fmt.Errorf("agent-tool update refused: %s metadata unavailable or catalogue behind/diverged: %s", dependency.Source, result.Detail)
		}
		candidate := result.Candidate
		if result.Status == Ahead {
			candidate = dependency.Version
		}
		pins.Arguments[agentToolArguments[dependency.Kind+"/"+dependency.Source]] = candidate
		if candidate != dependency.Version {
			plan.Changes = append(plan.Changes, Change{Dependency: dependency.Name, Installed: dependency.Version, Candidate: candidate, Action: "pin agent tool in private build recipe", Kind: dependency.Kind})
		}
	}
	if err := pins.Validate(); err != nil {
		return Plan{}, err
	}
	if _, err := runtimepins.ApplyDockerfile(recipe, pins); err != nil {
		return Plan{}, err
	}
	plan.Pins = pins
	return plan, nil
}
