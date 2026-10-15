package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/runtimeupdates"
)

type dependencyBuildFixture struct {
	*updateFixture
	buildOptions runtimeimage.BuildOptions
}

func (f *dependencyBuildFixture) BuildWithOptions(ctx context.Context, source string, options runtimeimage.BuildOptions) (runtimeimage.State, error) {
	f.buildOptions = options
	// Existing update fixtures model inventory and candidate checks. Fresh named
	// builds deliberately have no dependency on a previous default image.
	options.ExpectedImageID = f.state.ImageID
	return f.updateFixture.BuildWithOptions(ctx, source, options)
}
func dependencyCatalogue(t *testing.T, kind string) (string, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "scripts"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "scripts", "install-"+kind), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	var revision string
	for _, args := range [][]string{{"init", "--initial-branch=main"}, {"add", "."}, {"commit", "-m", "Add public fixture"}, {"rev-parse", "HEAD"}} {
		command := exec.Command("git", append([]string{"-C", root, "-c", "user.name=Example", "-c", "user.email=example@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
		data, err := command.CombinedOutput()
		if err != nil {
			t.Fatal(string(data), err)
		}
		revision = strings.TrimSpace(string(data))
	}
	return root, revision
}
func TestNamedDependencyBuildPreviewsAndRetainsLocalCatalogues(t *testing.T) {
	for _, dry := range []bool{true, false} {
		t.Run(map[bool]string{true: "preview", false: "build"}[dry], func(t *testing.T) {
			fixture := &dependencyBuildFixture{updateFixture: newUpdateFixture(t)}
			skills, skillsRevision := dependencyCatalogue(t, "skills")
			agents, agentsRevision := dependencyCatalogue(t, "agents")
			var output bytes.Buffer
			options := dependencyBuildOptions{Name: "custom", Source: fixture.state.Source, SkillsSource: skills, AgentsSource: agents, DryRun: dry, Force: true}
			err := runtimeBuildDependencies(context.Background(), fixture, runtimeupdates.Checker{Client: updateHTTP(false)}, t.TempDir(), options, &output)
			if err != nil {
				t.Fatal(err, output.String())
			}
			if dry {
				if fixture.builds != 0 || !strings.Contains(output.String(), "Preview complete") || !strings.Contains(output.String(), skillsRevision) {
					t.Fatal("preview mutated runtime or lost fixed revision", output.String())
				}
				return
			}
			selected := fixture.buildOptions
			if fixture.builds != 1 || !selected.Refresh || !selected.KeepPreviousImage || selected.ExpectedImageID != "" || selected.SkillsSource != skills || selected.AgentsSource != agents || selected.ExpectedSkillsRevision != skillsRevision || selected.ExpectedAgentsRevision != agentsRevision || selected.Pins.Arguments["SKILLS_REVISION"] != skillsRevision || selected.Pins.Arguments["AGENTS_REVISION"] != agentsRevision {
				t.Fatalf("lost named build selection: %+v", selected)
			}
		})
	}
}
func TestNamedDependencyBuildRejectsDirtyCatalogueBeforePlanning(t *testing.T) {
	fixture := newUpdateFixture(t)
	skills, _ := dependencyCatalogue(t, "skills")
	if err := os.WriteFile(filepath.Join(skills, "uncommitted"), []byte("pending"), 0600); err != nil {
		t.Fatal(err)
	}
	err := runtimeBuildDependencies(context.Background(), fixture, runtimeupdates.Checker{Client: updateHTTP(false)}, t.TempDir(), dependencyBuildOptions{Name: "custom", Source: fixture.state.Source, SkillsSource: skills, DryRun: true}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "clean committed") || fixture.builds != 0 {
		t.Fatal("invalid catalogue reached dependency planning", err)
	}
}
func TestNamedDependencyBuildFlagConflictsDoNotCreateState(t *testing.T) {
	state := filepath.Join(t.TempDir(), "absent")
	t.Setenv("SDLC_STATE_DIR", state)
	for _, args := range [][]string{{"build", "--dependencies"}, {"build", "--name", "custom", "--dependencies", "--source-pins"}, {"build", "--name", "custom", "--dry-run"}} {
		if err := runtimeCommand(context.Background(), args, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
			t.Fatal("accepted incompatible flags", args)
		}
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("invalid flags wrote state")
	}
}
