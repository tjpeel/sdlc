package runtimeimage

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/tjpeel/sdlc/internal/runtimepins"
)

func copyPins(pins *runtimepins.Pins) *runtimepins.Pins {
	if pins == nil {
		return nil
	}
	result := *pins
	result.Arguments = make(map[string]string, len(pins.Arguments))
	result.Images = make(map[string]string, len(pins.Images))
	for key, value := range pins.Arguments {
		result.Arguments[key] = value
	}
	for key, value := range pins.Images {
		result.Images[key] = value
	}
	return &result
}

func (manager Manager) noDependentContainers(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	containers, err := manager.Docker.Output(ctx, "ps", "--all", "--filter", "ancestor="+id, "--format", "{{.ID}}")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(containers)) != "" {
		return errors.New("containers still depend on the shared runtime; finish or remove them before rebuilding")
	}
	return nil
}

var auxiliaryVersion = regexp.MustCompile(`\b(?:v)?([0-9]+\.[0-9]+\.[0-9]+)\b`)

// Auxiliary probes do not mount account state or start the privileged daemon.
// The digest remains available for jobs that were frozen against older pins.
func (manager Manager) probeAuxiliaryImages(ctx context.Context, pins runtimepins.Pins) error {
	for _, item := range []struct{ reference, entrypoint, label string }{
		{pins.SigningImage, "op", "signing CLI"},
		{pins.DaemonImage, "docker", "check daemon CLI"},
	} {
		pullContext, cancelPull := context.WithTimeout(ctx, 5*time.Minute)
		err := manager.PullPublic(pullContext, item.reference)
		cancelPull()
		if err != nil {
			return fmt.Errorf("planned %s image could not be pulled; candidate was not selected", item.label)
		}
		probeContext, cancelProbe := context.WithTimeout(ctx, 20*time.Second)
		args := isolatedRun("none", "1000:1000", item.entrypoint, item.reference)
		output, err := manager.Docker.Output(probeContext, append(args, "--version")...)
		cancelProbe()
		if err != nil {
			return fmt.Errorf("planned %s version probe failed; candidate was not selected", item.label)
		}
		match := auxiliaryVersion.FindSubmatch(output)
		if len(output) > 4096 || len(match) != 2 || string(match[1]) != referenceVersion(item.reference) {
			return fmt.Errorf("planned %s version probe did not match its pin; candidate was not selected", item.label)
		}
	}
	return nil
}

func referenceVersion(reference string) string {
	tagged := strings.SplitN(reference, "@", 2)[0]
	tag := tagged[strings.LastIndex(tagged, ":")+1:]
	match := auxiliaryVersion.FindStringSubmatch(tag)
	if len(match) != 2 {
		return ""
	}
	return match[1]
}

// The inventory must reflect the managed versions actually installed in the
// candidate. A successful download/build alone does not establish that match.
func validatePinnedInventory(inventory Inventory, pins runtimepins.Pins) error {
	checks := []struct{ argument, kind, source string }{
		{"CODEX_VERSION", "npm", "@openai/codex"},
		{"CLAUDE_VERSION", "npm", "@anthropic-ai/claude-code"},
		{"GH_VERSION", "github-release", "cli/cli"},
		{"NPM_VERSION", "npm", "npm"},
		{"YARN_VERSION", "npm", "yarn"},
		{"DOTNET_VERSION", "dotnet-sdk", "dotnet"},
		{"COMPOSE_VERSION", "github-release", "docker/compose"},
		{"BUILDX_VERSION", "github-release", "docker/buildx"},
		{"SKILLS_REVISION", "git", "tjpeel/skills"},
		{"AGENTS_REVISION", "git", "tjpeel/agents"},
	}
	for _, check := range checks {
		if err := inventoryVersionMatches(inventory, check.kind, check.source, pins.Arguments[check.argument]); err != nil {
			return err
		}
	}
	if err := inventoryVersionMatches(inventory, "node", "nodejs", referenceVersion(pins.Images["node"])); err != nil {
		return err
	}
	if err := inventoryVersionMatches(inventory, "github-release", "docker/cli", referenceVersion(pins.Images["docker"])); err != nil {
		return err
	}
	parts := strings.SplitN(pins.Images["node"], "@", 2)
	if len(parts) != 2 {
		return errors.New("node base image pin is invalid")
	}
	tag := strings.TrimPrefix(parts[0], "node:")
	found := false
	for _, dependency := range inventory.Dependencies {
		if dependency.Kind == "image" && dependency.Source == "library/node" {
			if dependency.Version != parts[1] || dependency.Track != tag {
				return errors.New("built runtime node base image does not match its pin; candidate was not selected")
			}
			found = true
		}
	}
	if !found {
		return errors.New("built runtime node base image is absent; candidate was not selected")
	}
	return nil
}

func inventoryVersionMatches(inventory Inventory, kind, source, expected string) error {
	found := false
	for _, dependency := range inventory.Dependencies {
		if dependency.Kind != kind || dependency.Source != source || (kind == "npm" && dependency.Track != "") {
			continue
		}
		if dependency.Version != expected {
			return fmt.Errorf("built runtime %s version does not match its pin; candidate was not selected", source)
		}
		found = true
	}
	if !found {
		return fmt.Errorf("built runtime %s inventory is absent; candidate was not selected", source)
	}
	return nil
}
