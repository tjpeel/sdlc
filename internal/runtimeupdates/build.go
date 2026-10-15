package runtimeupdates

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/runtimepins"
)

// PlanBuild resolves a fresh source recipe, without requiring an installed image.
// Local catalogues are immutable selections, not remote branches to compare.
func (checker Checker) PlanBuild(ctx context.Context, recipe []byte, catalogues map[string]string) (plan Plan, err error) {
	defer func() {
		if err != nil {
			plan = Plan{}
		}
	}()
	previous, err := runtimepins.ReadDockerfile(recipe)
	if err != nil {
		return plan, err
	}
	for key, revision := range catalogues {
		if key != "SKILLS_REVISION" && key != "AGENTS_REVISION" || !validSHA(revision) {
			return plan, fmt.Errorf("invalid fixed catalogue selection")
		}
	}
	fetch := checker.fetcher()
	pins := runtimepins.Pins{Arguments: map[string]string{}}
	keys := make([]string, 0, len(managedArguments))
	for key := range managedArguments {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		argument := managedArguments[key]
		if revision, ok := catalogues[argument]; ok {
			pins.Arguments[argument] = revision
			continue
		}
		kind, source, _ := strings.Cut(key, "/")
		if kind == "git" {
			var commit struct{ SHA string }
			if err = fetch.json(ctx, "https://api.github.com/repos/"+source+"/commits/main", &commit); err != nil {
				return plan, fmt.Errorf("resolve %s: %w", source, err)
			}
			if !validSHA(commit.SHA) {
				return plan, fmt.Errorf("invalid %s catalogue revision", source)
			}
			pins.Arguments[argument] = commit.SHA
			continue
		}
		baseline := previous.Arguments[argument]
		// Optional source defaults have no installed version to compare. This lower
		// bound is internal only; build previews report selected pins, not inventory.
		if baseline == "" {
			baseline = "0.0.0"
		}
		dependency := runtimeimage.Dependency{Name: source, Kind: kind, Source: source, Version: baseline}
		if kind == "dotnet-sdk" {
			parts := strings.Split(baseline, ".")
			dependency.Track = strings.Join(parts[:2], ".")
		}
		result := fetch.check(ctx, dependency)
		if result.Status == Unavailable || result.Candidate == "" {
			return plan, fmt.Errorf("cannot resolve %s: %s", source, result.Detail)
		}
		candidate := result.Candidate
		if result.Status == Ahead {
			candidate = baseline
		}
		pins.Arguments[argument] = candidate
	}
	nodeTag := strings.TrimSuffix(strings.SplitN(strings.TrimPrefix(previous.Images["node"], "node:"), "@", 2)[0], "-bookworm")
	nodeVersion := nodeTag
	if !strings.Contains(nodeTag, ".") {
		nodeVersion += ".0.0"
	}
	nodeTrack := strings.Split(nodeVersion, ".")[0]
	dockerVersion, _, err := referenceParts(previous.Images["docker"], "docker", "-cli")
	if err != nil {
		return plan, err
	}
	inventory := runtimeimage.Inventory{Dependencies: []runtimeimage.Dependency{
		{Name: "Node", Kind: "node", Source: "nodejs", Version: nodeVersion, Track: nodeTrack},
		{Name: "Docker CLI", Kind: "github-release", Source: "docker/cli", Version: dockerVersion},
	}}
	images, _, err := fetch.sourcePins(ctx, previous, inventory, false)
	if err != nil {
		return plan, err
	}
	pins.Images, pins.SigningImage, pins.DaemonImage = images.Images, images.SigningImage, images.DaemonImage
	sdk := strings.Split(pins.Arguments["DOTNET_VERSION"], ".")
	track := strings.Join(sdk[:2], ".")
	plan.ExpectedRuntimes = map[string]string{}
	for _, source := range []string{"Microsoft.NETCore.App", "Microsoft.AspNetCore.App"} {
		candidate, err := fetch.dotnet(ctx, runtimeimage.Dependency{Kind: "dotnet-runtime", Source: source, Track: track})
		if err != nil {
			return plan, err
		}
		if _, valid := version(candidate); !valid {
			return plan, fmt.Errorf("no stable %s runtime in release metadata", source)
		}
		plan.ExpectedRuntimes[source] = candidate
	}
	if err = pins.Validate(); err != nil {
		return plan, err
	}
	if _, err = runtimepins.ApplyDockerfile(recipe, pins); err != nil {
		return plan, err
	}
	plan.Pins = pins
	return plan, nil
}

func (plan Plan) PrintBuild(output io.Writer) error {
	if _, err := fmt.Fprintln(output, "Resolved build dependencies (supported release tracks):"); err != nil {
		return err
	}
	keys := make([]string, 0, len(plan.Pins.Arguments))
	for key := range plan.Pins.Arguments {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, err := fmt.Fprintf(output, "  %s=%s\n", key, plan.Pins.Arguments[key]); err != nil {
			return err
		}
	}
	for _, key := range []string{"node", "golang", "docker"} {
		if _, err := fmt.Fprintf(output, "  %s=%s\n", key, plan.Pins.Images[key]); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(output, "  signing=%s\n  daemon=%s\nDebian packages will be refreshed during the build; transitive packages remain controlled by their parent tools.\n", plan.Pins.SigningImage, plan.Pins.DaemonImage)
	return err
}
