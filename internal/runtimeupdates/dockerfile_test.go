package runtimeupdates

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func dockerfileFixture(t *testing.T) (string, []byte) {
	t.Helper()
	source := t.TempDir()
	if err := os.Mkdir(filepath.Join(source, "runtime"), 0755); err != nil {
		t.Fatal(err)
	}
	original := append(planRecipe(), []byte("# retain unrelated text and spacing\n")...)
	if err := os.WriteFile(filepath.Join(source, "runtime", "Dockerfile"), original, 0640); err != nil {
		t.Fatal(err)
	}
	return source, original
}

func TestDockerfileUpdateChangesOnlyFourArgumentsAndRetainsMode(t *testing.T) {
	source, original := dockerfileFixture(t)
	pins := agentToolsState(t).Pins
	pins.Arguments["AGENTS_REVISION"] = strings.Repeat("d", 40)
	update, err := PrepareAgentToolDockerfile(source, original, pins)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(source, "runtime", "Dockerfile"))
	if !bytes.Equal(before, original) {
		t.Fatal("preparation wrote source")
	}
	if err := update.Apply(); err != nil {
		t.Fatal(err)
	}
	expected := string(original)
	for key, old := range map[string]string{"CODEX_VERSION": "0.159.3", "CLAUDE_VERSION": "2.1.287", "SKILLS_REVISION": strings.Repeat("a", 40), "AGENTS_REVISION": strings.Repeat("c", 40)} {
		expected = strings.Replace(expected, "ARG "+key+"="+old, "ARG "+key+"="+pins.Arguments[key], 1)
	}
	after, err := os.ReadFile(filepath.Join(source, "runtime", "Dockerfile"))
	if err != nil || string(after) != expected {
		t.Fatal("unexpected source changes", err, string(after))
	}
	info, _ := os.Stat(filepath.Join(source, "runtime", "Dockerfile"))
	if info.Mode().Perm() != 0640 {
		t.Fatal("source mode changed", info.Mode())
	}
	// The complete private plan includes different Node/daemon pins and inherited
	// tool versions; none may leak into the public source file.
	if !bytes.Contains(after, []byte("FROM node:24-bookworm@")) || !bytes.Contains(after, []byte("ARG NPM_VERSION=\n")) {
		t.Fatal("private baseline completion leaked into source")
	}
}

func TestDockerfileUpdateRejectsUnsafeOrChangedTargets(t *testing.T) {
	for _, mode := range []string{"symlink", "directory", "duplicate", "missing", "source-symlink", "changed-bytes", "replaced-file"} {
		t.Run(mode, func(t *testing.T) {
			source, original := dockerfileFixture(t)
			target := filepath.Join(source, "runtime", "Dockerfile")
			pins := agentToolsState(t).Pins
			if mode == "symlink" {
				os.Remove(target)
				os.Symlink(filepath.Join(source, "other"), target)
			}
			if mode == "directory" {
				os.Remove(target)
				os.Mkdir(target, 0700)
			}
			if mode == "duplicate" {
				original = append(original, []byte("ARG CODEX_VERSION=0.159.3\n")...)
				os.WriteFile(target, original, 0640)
			}
			if mode == "missing" {
				original = []byte(strings.Replace(string(original), "ARG CLAUDE_VERSION=2.1.287\n", "", 1))
				os.WriteFile(target, original, 0640)
			}
			if mode == "source-symlink" {
				alias := filepath.Join(t.TempDir(), "checkout")
				os.Symlink(source, alias)
				source = alias
			}
			update, err := PrepareAgentToolDockerfile(source, original, pins)
			if strings.HasPrefix(mode, "changed-") || mode == "replaced-file" {
				if err != nil {
					t.Fatal(err)
				}
				edited := append(append([]byte(nil), original...), []byte("# local edit\n")...)
				if mode == "replaced-file" {
					os.Remove(target)
				}
				os.WriteFile(target, edited, 0640)
				if err := update.Apply(); err == nil {
					t.Fatal("concurrent source edit overwritten")
				}
				data, _ := os.ReadFile(target)
				if !bytes.Equal(data, edited) {
					t.Fatal("local edit changed")
				}
			} else if err == nil {
				t.Fatal("unsafe target accepted", mode)
			}
		})
	}
}
