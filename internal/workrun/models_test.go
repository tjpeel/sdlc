package workrun

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestModelRolesDefaultsAndOverrides(t *testing.T) {
	defaults := DefaultModels()
	for _, provider := range []string{"", "codex", "claude"} {
		roles, err := ResolveModels(defaults, provider, "", "", "", "")
		if err != nil {
			t.Fatal(err)
		}
		want := defaults.Codex
		if provider == "claude" {
			want = defaults.Claude
		}
		if roles != want {
			t.Fatalf("roles=%+v want=%+v", roles, want)
		}
		roles, err = ResolveModels(defaults, provider, "custom-implementation", "high", "custom-review", "max")
		if err != nil || roles.Implementation.Name != "custom-implementation" || roles.Review.Name != "custom-review" || roles.Implementation.Effort != "high" || roles.Review.Effort != "max" || roles.Implementation.Provider == roles.Review.Provider {
			t.Fatalf("overrides=%+v %v", roles, err)
		}
	}
}
func TestModelValidationRejectsUnsafeArguments(t *testing.T) {
	for _, m := range []Model{{"unknown", "model", "high"}, {"codex", "--flag", "high"}, {"codex", "model\ncommand", "high"}, {"codex", "", "high"}, {"claude", "model", "ultra"}, {"codex", "model", "arbitrary"}} {
		if err := ValidateModel(m); err == nil {
			t.Fatalf("accepted %+v", m)
		}
	}
}
func TestLoadModelsRejectsUnsafeFiles(t *testing.T) {
	for _, kind := range []string{"symlink", "directory", "oversize", "unknown field", "wrong version", "same provider", "trailing JSON"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "models.json")
			defaults := DefaultModels()
			data, _ := json.Marshal(defaults)
			switch kind {
			case "symlink":
				target := filepath.Join(dir, "target")
				os.WriteFile(target, data, 0600)
				os.Symlink(target, path)
			case "directory":
				os.Mkdir(path, 0700)
			case "oversize":
				os.WriteFile(path, make([]byte, 65537), 0600)
			case "unknown field":
				data = append(data[:len(data)-1], []byte(`,"extra":true}`)...)
				os.WriteFile(path, data, 0600)
			case "wrong version":
				defaults.Version = 2
				data, _ = json.Marshal(defaults)
				os.WriteFile(path, data, 0600)
			case "same provider":
				defaults.Codex.Review.Provider = "codex"
				data, _ = json.Marshal(defaults)
				os.WriteFile(path, data, 0600)
			case "trailing JSON":
				os.WriteFile(path, append(data, []byte(` {}`)...), 0600)
			}
			if _, err := LoadModels(dir); err == nil {
				t.Fatal("accepted unsafe model defaults")
			}
		})
	}
}
func TestLoadModelsMissingAndCustomDefaults(t *testing.T) {
	dir := t.TempDir()
	got, err := LoadModels(dir)
	if err != nil || got != DefaultModels() {
		t.Fatalf("missing defaults: %+v %v", got, err)
	}
	want := DefaultModels()
	want.Codex.Implementation.Name = "custom-model"
	data, _ := json.Marshal(want)
	if err := os.WriteFile(filepath.Join(dir, "models.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	got, err = LoadModels(dir)
	if err != nil || got != want {
		t.Fatalf("custom defaults: %+v %v", got, err)
	}
}
