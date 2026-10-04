package runtimeupdates

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/runtimepins"
)

type Change struct{ Dependency, Installed, Candidate, Action, Kind string }
type Plan struct {
	Pins             runtimepins.Pins
	Report           Report
	Changes          []Change
	Remaining        []Result
	ExpectedRuntimes map[string]string
}

var managedArguments = map[string]string{
	"npm/@openai/codex": "CODEX_VERSION", "npm/@anthropic-ai/claude-code": "CLAUDE_VERSION",
	"npm/npm": "NPM_VERSION", "npm/yarn": "YARN_VERSION", "github-release/cli/cli": "GH_VERSION",
	"github-release/docker/compose": "COMPOSE_VERSION", "github-release/docker/buildx": "BUILDX_VERSION",
	"dotnet-sdk/dotnet": "DOTNET_VERSION", "git/tjpeel/skills": "SKILLS_REVISION", "git/tjpeel/agents": "AGENTS_REVISION",
}

// ResolveDefaultDaemonImage freezes the legacy default tag for a new isolated
// check. Resumed legacy jobs retain their original daemon reference.
func ResolveDefaultDaemonImage(ctx context.Context) (string, error) {
	return New().ResolveDefaultDaemonImage(ctx)
}

func (checker Checker) ResolveDefaultDaemonImage(ctx context.Context) (string, error) {
	number, _, err := referenceParts(runtimepins.DefaultDaemonImage, "docker", "-dind")
	if err != nil {
		return "", err
	}
	tag := number + "-dind"
	digest, err := checker.fetcher().image(ctx, runtimeimage.Dependency{Source: "library/docker", Track: tag})
	if err != nil {
		return "", fmt.Errorf("default Docker daemon digest unavailable: %w", err)
	}
	reference := "docker:" + tag + "@" + digest
	if err := runtimepins.ValidateDaemonImage(reference); err != nil {
		return "", err
	}
	return reference, nil
}

func managedArgument(dependency runtimeimage.Dependency) string {
	if dependency.Kind == "npm" && dependency.Track != "" {
		return ""
	}
	return managedArguments[dependency.Kind+"/"+dependency.Source]
}

func recipePins(state runtimeimage.State, recipe []byte) (runtimepins.Pins, error) {
	pins, err := runtimepins.ReadDockerfile(recipe)
	if err != nil {
		return runtimepins.Pins{}, err
	}
	if state.BuildRecipe != "" {
		pins, err = runtimepins.ReadDockerfile([]byte(state.BuildRecipe))
		if err != nil {
			return runtimepins.Pins{}, fmt.Errorf("recorded runtime build recipe is invalid: %w", err)
		}
	}
	if state.DependencyPins != nil {
		if err := state.DependencyPins.Validate(); err != nil {
			return runtimepins.Pins{}, err
		}
		pins = *state.DependencyPins
	}
	return pins, nil
}

// CheckState adds dependencies that live outside the final image's inventory:
// its Go builder, Docker CLI image, isolated daemon and signing resolver.
func (checker Checker) CheckState(ctx context.Context, state runtimeimage.State, recipe []byte, offline bool,
	packages func(context.Context) ([]runtimeimage.PackageUpdate, error)) (Report, error) {
	if state.Inventory == nil {
		return Report{}, fmt.Errorf("runtime has no dependency inventory; rebuild before checking updates")
	}
	var previous runtimepins.Pins
	var err error
	missingRecipe := false
	if state.DependencyPins != nil {
		if err := state.DependencyPins.Validate(); err != nil {
			return Report{}, err
		}
		previous = *state.DependencyPins
	} else if state.BuildRecipe != "" || len(recipe) != 0 {
		actual := recipe
		if state.BuildRecipe != "" {
			actual = []byte(state.BuildRecipe)
		}
		previous, err = runtimepins.ReadDockerfile(actual)
		if err != nil {
			return Report{}, err
		}
	} else {
		missingRecipe = true
	}
	fetch := checker.fetcher()
	report, err := checker.check(ctx, *state.Inventory, offline, packages, fetch)
	if err != nil {
		return report, err
	}
	if missingRecipe {
		for _, component := range []struct{ name, source string }{{"Node exact base image", "library/node"}, {"Go publisher builder", "library/golang"}, {"Docker CLI image", "library/docker"}, {"Docker check daemon", "library/docker"}, {"1Password resolver", "1password/op"}} {
			report.Results = append(report.Results, Result{Name: component.name, Kind: "image", Source: component.source, Status: Unavailable, Detail: "recorded build recipe unavailable; rebuild to capture source provenance"})
		}
		return report, nil
	}
	_, results, err := fetch.sourcePins(ctx, previous, *state.Inventory, offline)
	labelAssumedSources(state, results)
	report.Results = append(report.Results, results...)
	if err != nil && len(results) == 0 {
		return report, err
	}
	return report, nil
}

// Plan resolves every managed pin before returning an actionable build plan.
// Transitive npm packages remain governed by their published parent packages.
func (checker Checker) Plan(ctx context.Context, state runtimeimage.State, recipe []byte,
	packages func(context.Context) ([]runtimeimage.PackageUpdate, error)) (plan Plan, err error) {
	defer func() {
		if err != nil {
			plan.Pins = runtimepins.Pins{}
			plan.Changes = nil
			plan.ExpectedRuntimes = nil
		}
	}()
	if state.Inventory == nil {
		return plan, fmt.Errorf("runtime has no dependency inventory; rebuild before updating")
	}
	previous, err := recipePins(state, recipe)
	if err != nil {
		return plan, err
	}
	fetch := checker.fetcher()
	plan.Report, err = checker.check(ctx, *state.Inventory, false, packages, fetch)
	if err != nil {
		return plan, err
	}
	if plan.Report.PackageError != "" {
		return plan, fmt.Errorf("update planning refused: %s", plan.Report.PackageError)
	}
	for _, item := range plan.Report.Packages {
		if item.Candidate == "" {
			return plan, fmt.Errorf("update planning refused: Debian candidate unavailable for %s:%s", item.Name, item.Architecture)
		}
	}
	pins := runtimepins.Pins{Arguments: map[string]string{}, Images: map[string]string{}}
	plan.ExpectedRuntimes = map[string]string{}
	direct := map[string]bool{}
	for i, dependency := range state.Inventory.Dependencies {
		result := plan.Report.Results[i]
		argument := managedArgument(dependency)
		if argument != "" {
			if direct[argument] {
				return plan, fmt.Errorf("update planning refused: ambiguous installed %s dependency", argument)
			}
			direct[argument] = true
			if result.Status == Unavailable {
				return plan, fmt.Errorf("update planning refused: %s metadata unavailable: %s", dependency.Source, result.Detail)
			}
			if dependency.Kind == "git" && result.Status != Current && result.Status != Update {
				return plan, fmt.Errorf("update planning refused: %s catalogue is behind or diverged", dependency.Source)
			}
			candidate := result.Candidate
			if result.Status == Ahead {
				candidate = dependency.Version
			}
			pins.Arguments[argument] = candidate
			if candidate != dependency.Version {
				plan.Changes = append(plan.Changes, Change{Dependency: dependency.Name, Installed: dependency.Version, Candidate: candidate, Action: "pin in private build recipe", Kind: dependency.Kind})
			}
			continue
		}
		if dependency.Kind == "dotnet-runtime" {
			if result.Status == Unavailable {
				return plan, fmt.Errorf("update planning refused: %s metadata unavailable: %s", dependency.Source, result.Detail)
			}
			if _, found := plan.ExpectedRuntimes[dependency.Source]; found {
				return plan, fmt.Errorf("update planning refused: ambiguous installed .NET runtime")
			}
			candidate := result.Candidate
			if result.Status == Ahead {
				candidate = dependency.Version
			}
			plan.ExpectedRuntimes[dependency.Source] = candidate
			if candidate != dependency.Version {
				plan.Changes = append(plan.Changes, Change{Dependency: dependency.Name, Installed: dependency.Version, Candidate: candidate, Action: "verify runtime supplied by the published SDK", Kind: dependency.Kind})
			}
			continue
		}
		if dependency.Kind == "node" || dependency.Kind == "image" || dependency.Kind == "github-release" && dependency.Source == "docker/cli" {
			if result.Status == Unavailable {
				return plan, fmt.Errorf("update planning refused: %s metadata unavailable", dependency.Source)
			}
			continue
		}
		if result.Status != Current {
			result.Detail = "resolved by published parent packages; no transitive version overrides"
			plan.Remaining = append(plan.Remaining, result)
		}
	}
	for _, argument := range managedArguments {
		if !direct[argument] {
			return plan, fmt.Errorf("update planning refused: installed %s dependency missing; rebuild to capture it", argument)
		}
	}
	sources, results, sourceErr := fetch.sourcePins(ctx, previous, *state.Inventory, false)
	labelAssumedSources(state, results)
	plan.Report.Results = append(plan.Report.Results, results...)
	if sourceErr != nil {
		return plan, fmt.Errorf("update planning refused: %w", sourceErr)
	}
	pins.Images, pins.SigningImage, pins.DaemonImage = sources.Images, sources.SigningImage, sources.DaemonImage
	for _, result := range results {
		if result.Candidate != result.Installed {
			plan.Changes = append(plan.Changes, Change{Dependency: result.Name, Installed: result.Installed, Candidate: result.Candidate, Action: "pin exact public image digest", Kind: "image"})
		}
	}
	for _, item := range plan.Report.Packages {
		if item.Update {
			plan.Changes = append(plan.Changes, Change{Dependency: "Debian " + item.Name + ":" + item.Architecture, Installed: item.Installed, Candidate: item.Candidate, Action: "refresh Debian packages during rebuild", Kind: "debian"})
		}
	}
	if err := pins.Validate(); err != nil {
		return plan, fmt.Errorf("resolved update pins invalid: %w", err)
	}
	if _, err := runtimepins.ApplyDockerfile(recipe, pins); err != nil {
		return plan, err
	}
	plan.Pins = pins
	return plan, nil
}

func (plan Plan) Print(output io.Writer) error {
	if err := plan.Report.PrintSummary(output); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(output, "Planned build actions: %d.\n", len(plan.Changes)); err != nil {
		return err
	}
	debian := 0
	for _, change := range plan.Changes {
		if change.Kind == "debian" {
			debian++
			continue
		}
		if _, err := fmt.Fprintf(output, "%s: %s -> %s (%s).\n", change.Dependency, summaryIdentity(change.Installed), summaryIdentity(change.Candidate), change.Action); err != nil {
			return err
		}
	}
	if debian > 0 {
		if _, err := fmt.Fprintf(output, "Debian rebuild: refresh %d packages.\n", debian); err != nil {
			return err
		}
	}
	if len(plan.Remaining) > 0 {
		_, err := fmt.Fprintf(output, "Remaining parent-managed npm differences: %d; keep versions selected by their published parents. Use sdlc runtime status --all for package details.\n", len(plan.Remaining))
		return err
	}
	return nil
}

func labelAssumedSources(state runtimeimage.State, results []Result) {
	if state.DependencyPins != nil || state.BuildRecipe != "" {
		return
	}
	for i := range results {
		if results[i].Detail != "" {
			results[i].Detail += "; "
		}
		results[i].Detail += "source recipe assumed; rebuild to record image provenance"
	}
}

func referenceParts(reference, repository, suffix string) (string, string, error) {
	prefix := repository + ":"
	if !strings.HasPrefix(reference, prefix) {
		return "", "", fmt.Errorf("unexpected public image repository")
	}
	parts := strings.Split(strings.TrimPrefix(reference, prefix), "@")
	if len(parts) > 2 || !strings.HasSuffix(parts[0], suffix) {
		return "", "", fmt.Errorf("invalid public image reference")
	}
	number := strings.TrimSuffix(parts[0], suffix)
	if _, valid := version(number); !valid {
		return "", "", fmt.Errorf("image tag is not an exact stable version")
	}
	digest := ""
	if len(parts) == 2 {
		digest = parts[1]
	}
	return number, digest, nil
}

func installedDependency(inventory runtimeimage.Inventory, kind, source string) (runtimeimage.Dependency, error) {
	var selected runtimeimage.Dependency
	found := false
	for _, entry := range inventory.Dependencies {
		if entry.Kind == kind && entry.Source == source && (kind != "npm" || entry.Track == "") {
			if found {
				return selected, fmt.Errorf("ambiguous installed %s dependency", source)
			}
			selected, found = entry, true
		}
	}
	if !found {
		return selected, fmt.Errorf("installed %s dependency missing", source)
	}
	return selected, nil
}

func (fetch *fetcher) sourcePins(ctx context.Context, previous runtimepins.Pins, inventory runtimeimage.Inventory, offline bool) (runtimepins.Pins, []Result, error) {
	pins := runtimepins.Pins{Images: map[string]string{}}
	goInstalled, _, err := referenceParts(previous.Images["golang"], "golang", "-bookworm")
	if err != nil {
		return pins, nil, err
	}
	dockerInstalled, _, err := referenceParts(previous.Images["docker"], "docker", "-cli")
	if err != nil {
		return pins, nil, err
	}
	opInstalled, _, err := referenceParts(previous.SigningImage, "1password/op", "")
	if err != nil {
		return pins, nil, err
	}
	daemonInstalled, _, err := referenceParts(previous.DaemonImage, "docker", "-dind")
	if err != nil {
		return pins, nil, err
	}
	node, err := installedDependency(inventory, "node", "nodejs")
	if err != nil {
		return pins, nil, err
	}
	nodeParts, validNode := version(node.Version)
	if !validNode || strconv.FormatUint(nodeParts[0], 10) != node.Track {
		return pins, nil, fmt.Errorf("installed Node release track is invalid")
	}
	nodeRef := strings.TrimPrefix(previous.Images["node"], "node:")
	nodeTag := strings.SplitN(nodeRef, "@", 2)[0]
	nodeTag = strings.TrimSuffix(nodeTag, "-bookworm")
	if nodeTag != node.Track {
		parts, valid := version(nodeTag)
		if !valid || parts[0] != nodeParts[0] {
			return pins, nil, fmt.Errorf("Node base image differs from the installed release track")
		}
		if compare(nodeTag, node.Version) > 0 {
			node.Version = nodeTag
		}
	}
	docker, err := installedDependency(inventory, "github-release", "docker/cli")
	if err != nil {
		return pins, nil, err
	}
	if _, valid := version(docker.Version); !valid {
		return pins, nil, fmt.Errorf("installed Docker CLI version is invalid")
	}
	// CLI and daemon must remain on one version. The immutable CLI inventory
	// is authoritative even when a legacy source recipe has changed since build.
	dockerFloor := docker.Version
	for _, existing := range []string{dockerInstalled, daemonInstalled} {
		if compare(existing, dockerFloor) > 0 {
			dockerFloor = existing
		}
	}
	goParts, _ := version(goInstalled)
	results := []Result{
		{Name: "Node exact base image", Kind: "image", Source: "library/node", Installed: previous.Images["node"], Track: node.Track + " / bookworm", Status: Offline},
		{Name: "Go publisher builder", Kind: "image", Source: "library/golang", Installed: previous.Images["golang"], Track: fmt.Sprintf("%d.%d / bookworm", goParts[0], goParts[1]), Status: Offline},
		{Name: "Docker CLI image", Kind: "image", Source: "library/docker", Installed: previous.Images["docker"], Track: "latest stable / cli", Status: Offline},
		{Name: "Docker check daemon", Kind: "image", Source: "library/docker", Installed: previous.DaemonImage, Track: "latest stable / dind", Status: Offline},
		{Name: "1Password resolver", Kind: "image", Source: "1password/op", Installed: previous.SigningImage, Track: "2 / stable", Status: Offline},
	}
	if offline {
		return pins, results, nil
	}
	nodeResult := fetch.check(ctx, node)
	dockerResult := fetch.check(ctx, docker)
	goCandidate, goErr := fetch.golang(ctx, goInstalled)
	opCandidate, opErr := fetch.onePassword(ctx)
	versions := []string{nodeResult.Candidate, goCandidate, dockerResult.Candidate, dockerResult.Candidate, opCandidate}
	installed := []string{node.Version, goInstalled, dockerFloor, dockerFloor, opInstalled}
	sources := []string{"library/node", "library/golang", "library/docker", "library/docker", "1password/op"}
	suffixes := []string{"-bookworm", "-bookworm", "-cli", "-dind", ""}
	errors := []error{nil, goErr, nil, nil, opErr}
	if nodeResult.Status == Unavailable {
		errors[0] = fmt.Errorf("Node release metadata unavailable")
	}
	if dockerResult.Status == Unavailable {
		errors[2], errors[3] = fmt.Errorf("Docker release metadata unavailable"), fmt.Errorf("Docker release metadata unavailable")
	}
	var firstErr error
	for i := range results {
		results[i].Status = Unavailable
		if errors[i] == nil {
			if _, valid := version(versions[i]); !valid {
				errors[i] = fmt.Errorf("no stable version in public metadata")
			}
		}
		if errors[i] == nil && compare(versions[i], installed[i]) < 0 {
			versions[i] = installed[i]
		}
		if errors[i] == nil {
			tag := versions[i] + suffixes[i]
			digest, imageErr := fetch.image(ctx, runtimeimage.Dependency{Source: sources[i], Track: tag})
			if imageErr != nil {
				errors[i] = imageErr
			} else {
				ref := strings.TrimPrefix(sources[i], "library/") + ":" + tag + "@" + digest
				results[i].Candidate, results[i].Status = ref, Current
				if ref != results[i].Installed {
					results[i].Status = Update
				}
				switch i {
				case 0:
					pins.Images["node"] = ref
				case 1:
					pins.Images["golang"] = ref
				case 2:
					pins.Images["docker"] = ref
				case 3:
					pins.DaemonImage = ref
				case 4:
					pins.SigningImage = ref
				}
			}
		}
		if errors[i] != nil {
			results[i].Detail = errors[i].Error()
			if firstErr == nil {
				firstErr = fmt.Errorf("%s metadata unavailable: %w", results[i].Name, errors[i])
			}
		}
	}
	return pins, results, firstErr
}

func (fetch *fetcher) golang(ctx context.Context, installed string) (string, error) {
	var releases []struct {
		Version string
		Stable  *bool
	}
	if err := fetch.json(ctx, "https://go.dev/dl/?mode=json&include=all", &releases); err != nil {
		return "", err
	}
	if releases == nil {
		return "", fmt.Errorf("invalid Go release metadata")
	}
	track, valid := version(installed)
	if !valid {
		return "", fmt.Errorf("invalid Go builder track")
	}
	candidate := ""
	for _, release := range releases {
		if release.Stable == nil {
			return "", fmt.Errorf("invalid Go stable release metadata")
		}
		number := strings.TrimPrefix(release.Version, "go")
		parts, valid := version(number)
		if *release.Stable && valid && parts[0] == track[0] && parts[1] == track[1] && (candidate == "" || compare(number, candidate) > 0) {
			candidate = number
		}
	}
	if candidate == "" {
		return "", fmt.Errorf("no stable Go release in builder track")
	}
	return candidate, nil
}

func (fetch *fetcher) onePassword(ctx context.Context) (string, error) {
	const endpoint = "https://hub.docker.com/v2/repositories/1password/op/tags"
	address, candidate := endpoint+"?page_size=100", ""
	visited := map[string]bool{}
	for page := 0; page < 20; page++ {
		if visited[address] {
			return "", fmt.Errorf("invalid 1Password tag pagination")
		}
		visited[address] = true
		var metadata struct {
			Next    json.RawMessage
			Results []struct{ Name string }
		}
		if err := fetch.json(ctx, address, &metadata); err != nil {
			return "", err
		}
		var next *string
		if metadata.Results == nil || len(metadata.Results) > 100 || len(metadata.Next) == 0 || json.Unmarshal(metadata.Next, &next) != nil {
			return "", fmt.Errorf("invalid 1Password tag metadata")
		}
		for _, tag := range metadata.Results {
			parts, valid := version(tag.Name)
			if valid && parts[0] == 2 && !strings.HasPrefix(tag.Name, "v") && (candidate == "" || compare(tag.Name, candidate) > 0) {
				candidate = tag.Name
			}
		}
		if next == nil || *next == "" {
			if candidate == "" {
				return "", fmt.Errorf("no stable 1Password v2 image tag")
			}
			return candidate, nil
		}
		parsed, err := url.Parse(*next)
		if err != nil || parsed.Scheme != "https" || parsed.Host != "hub.docker.com" || strings.TrimSuffix(parsed.Path, "/") != "/v2/repositories/1password/op/tags" || parsed.User != nil || parsed.Fragment != "" || parsed.Query().Get("page_size") != "100" || len(parsed.Query()) != 2 || len(parsed.Query()["page_size"]) != 1 || len(parsed.Query()["page"]) != 1 {
			return "", fmt.Errorf("invalid 1Password tag pagination")
		}
		pageNumber, err := strconv.Atoi(parsed.Query().Get("page"))
		if err != nil || pageNumber != page+2 {
			return "", fmt.Errorf("invalid 1Password tag pagination")
		}
		address = endpoint + "?" + parsed.Query().Encode()
	}
	return "", fmt.Errorf("1Password tag pagination exceeded the check limit")
}
