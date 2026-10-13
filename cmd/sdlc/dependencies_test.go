package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/tjpeel/sdlc/internal/workrun"
	"github.com/tjpeel/sdlc/internal/workseries"
)

func TestDependenciesCLIIsOfflinePreservesFrozenFeatureAndPreviewsNormally(t *testing.T) {
	driver, parent := featureAdoptionFixture(t, "ready")
	root := driver.plan.Root
	third := "03-c.md"
	path := ".sdlc/work/TASK-1/tickets/" + third
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), []byte("Disposable offline third requirement\n"), 0600); err != nil {
		t.Fatal(err)
	}
	plan, err := workseries.Discover(context.Background(), root, "TASK-1", "main")
	if err != nil {
		t.Fatal(err)
	}
	driver.settings.Roles[third] = workrun.DefaultModels().Codex
	hash, err := seriesInputHash(root, path)
	if err != nil {
		t.Fatal(err)
	}
	driver.settings.InputHashes[path] = hash
	settings, err := json.Marshal(driver.settings)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := workseries.Directory(root, "TASK-1", true)
	if err != nil {
		t.Fatal(err)
	}
	state := workseries.State{Version: 1, Plan: plan, Settings: settings, Results: map[string]workseries.Result{"01-selected.md": parent}}
	if err := workseries.Save(dir, &state); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "series.lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	marker := forbidConnectedRunCommands(t, root)
	args := []string{"--run-root", root, "--reference", "TASK-1", "--ticket", third, "--depends-on", "01-selected.md", "--depends-on", "02-other.md", "--json"}
	before, _ := os.ReadFile(filepath.Join(dir, "journal.json"))
	var output bytes.Buffer
	if err := dependenciesCommand(context.Background(), append(args, "--dry-run"), &output); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "journal.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("CLI preview changed checkpoint")
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(dir), "plan.json")); !os.IsNotExist(err) {
		t.Fatal("CLI preview created sidecar")
	}
	output.Reset()
	if err := dependenciesCommand(context.Background(), args, &output); err != nil {
		t.Fatal(err)
	}
	next, err := workseries.Load(dir)
	if err != nil || !bytes.Equal(next.Settings, state.Settings) || !reflect.DeepEqual(next.Results, state.Results) {
		t.Fatal("CLI changed frozen feature", err)
	}
	options, err := parseRunOptions([]string{"--reference", "TASK-1", "--all", "--dry-run", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	options.root = root
	output.Reset()
	if err := runSeriesCommand(context.Background(), options, &output); err != nil {
		t.Fatal("normal feature preview refused amended saved plan", err)
	}
	if !bytes.Contains(output.Bytes(), []byte(third)) || !bytes.Contains(output.Bytes(), []byte("01-selected.md")) {
		t.Fatal("normal preview lost amended tickets")
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatal("offline command reached auth, Docker or provider")
	}
	output.Reset()
	clear := []string{"--run-root", root, "--reference", "TASK-1", "--ticket", third, "--depends-on", "", "--dry-run", "--json"}
	if err := dependenciesCommand(context.Background(), clear, &output); err != nil {
		t.Fatal("empty dependency replacement refused", err)
	}
}

func TestDependenciesCLIRefusesCapturedDefinitionAndAliasBeforeWriting(t *testing.T) {
	for _, alias := range []bool{false, true} {
		t.Run(map[bool]string{false: "selected sidecar", true: "hardlink alias"}[alias], func(t *testing.T) {
			driver, parent := featureAdoptionFixture(t, "ready")
			root := driver.plan.Root
			definition := filepath.Join(root, ".sdlc/work/TASK-1/plan.json")
			data := []byte(`{"version":1,"tickets":{}}`)
			if err := os.WriteFile(definition, data, 0600); err != nil {
				t.Fatal(err)
			}
			selected := ".sdlc/work/TASK-1/plan.json"
			if alias {
				selected = "metadata-input.json"
				if err := os.Link(definition, filepath.Join(root, selected)); err != nil {
					t.Fatal(err)
				}
			}
			driver.settings.Inputs = append(driver.settings.Inputs, selected)
			hash, err := seriesInputHash(root, selected)
			if err != nil {
				t.Fatal(err)
			}
			driver.settings.InputHashes[selected] = hash
			settings, err := json.Marshal(driver.settings)
			if err != nil {
				t.Fatal(err)
			}
			dir, err := workseries.Directory(root, "TASK-1", true)
			if err != nil {
				t.Fatal(err)
			}
			state := workseries.State{Version: 1, Plan: driver.plan, Settings: settings, Results: map[string]workseries.Result{"01-selected.md": parent}}
			if err := workseries.Save(dir, &state); err != nil {
				t.Fatal(err)
			}
			os.WriteFile(filepath.Join(dir, "series.lock"), nil, 0600)
			before, _ := os.ReadFile(filepath.Join(dir, "journal.json"))
			marker := forbidConnectedRunCommands(t, root)
			for _, dry := range []bool{true, false} {
				args := []string{"--run-root", root, "--reference", "TASK-1", "--ticket", "02-other.md", "--depends-on", "01-selected.md"}
				if dry {
					args = append(args, "--dry-run")
				}
				var output bytes.Buffer
				if err := dependenciesCommand(context.Background(), args, &output); err == nil {
					t.Fatal("captured definition amendment accepted")
				}
				after, _ := os.ReadFile(filepath.Join(dir, "journal.json"))
				current, _ := os.ReadFile(definition)
				if !bytes.Equal(before, after) || !bytes.Equal(current, data) {
					t.Fatal("refusal changed frozen files")
				}
				if _, err := workseries.Load(dir); err != nil {
					t.Fatal("refusal left pending transaction", err)
				}
			}
			if _, err := os.Lstat(marker); !os.IsNotExist(err) {
				t.Fatal("refusal connected to provider/runtime")
			}
		})
	}
}
