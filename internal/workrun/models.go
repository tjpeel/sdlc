package workrun

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

type ModelDefaults struct {
	Version int   `json:"version"`
	Codex   Roles `json:"codex"`
	Claude  Roles `json:"claude"`
}

func DefaultModels() ModelDefaults {
	return ModelDefaults{Version: 1,
		Codex:  Roles{Model{"codex", "gpt-6.1-sol", "medium"}, Model{"claude", "claude-opus-5-5", "high"}},
		Claude: Roles{Model{"claude", "claude-sonnet-5-5", "medium"}, Model{"codex", "gpt-6.1-sol", "high"}},
	}
}

func LoadModels(directory string) (ModelDefaults, error) {
	defaults := DefaultModels()
	path := filepath.Join(directory, "models.json")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return defaults, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64*1024 {
		return defaults, fmt.Errorf("model defaults must be a regular JSON file of at most 64 KiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return defaults, fmt.Errorf("cannot open private model defaults")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return defaults, fmt.Errorf("model defaults changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, 64*1024+1))
	if err != nil || len(data) > 64*1024 {
		return defaults, fmt.Errorf("cannot read bounded model defaults")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&defaults) != nil || decoder.Decode(new(any)) != io.EOF || defaults.Version != 1 {
		return defaults, fmt.Errorf("model defaults require version 1 JSON with codex and claude role settings")
	}
	for provider, roles := range map[string]Roles{"codex": defaults.Codex, "claude": defaults.Claude} {
		if roles.Implementation.Provider != provider || roles.Review.Provider == provider {
			return defaults, fmt.Errorf("model defaults must use the selected implementer and the opposite reviewer")
		}
		if err := ValidateModel(roles.Implementation); err != nil {
			return defaults, err
		}
		if err := ValidateModel(roles.Review); err != nil {
			return defaults, err
		}
	}
	return defaults, nil
}

func ValidateModel(model Model) error {
	if model.Provider != "codex" && model.Provider != "claude" {
		return fmt.Errorf("provider must be codex or claude")
	}
	if model.Name == "" || len(model.Name) > 256 || strings.HasPrefix(model.Name, "-") || strings.IndexFunc(model.Name, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return fmt.Errorf("model must be a nonempty identifier without whitespace or control characters")
	}
	valid := map[string]bool{"low": true, "medium": true, "high": true, "xhigh": true, "max": true}
	if model.Provider == "codex" {
		valid["minimal"] = true
		valid["none"] = true
		valid["ultra"] = true
	}
	if !valid[model.Effort] {
		return fmt.Errorf("unsupported %s reasoning effort %q", model.Provider, model.Effort)
	}
	return nil
}

func ResolveModels(defaults ModelDefaults, provider, model, effort, reviewModel, reviewEffort string) (Roles, error) {
	if provider == "" {
		provider = "codex"
	}
	roles := defaults.Codex
	if provider == "claude" {
		roles = defaults.Claude
	} else if provider != "codex" {
		return Roles{}, fmt.Errorf("provider must be codex or claude")
	}
	if model != "" {
		roles.Implementation.Name = model
	}
	if effort != "" {
		roles.Implementation.Effort = effort
	}
	if reviewModel != "" {
		roles.Review.Name = reviewModel
	}
	if reviewEffort != "" {
		roles.Review.Effort = reviewEffort
	}
	if roles.Implementation.Provider != provider || roles.Review.Provider == provider {
		return Roles{}, fmt.Errorf("implementation and review must use different providers")
	}
	if err := ValidateModel(roles.Implementation); err != nil {
		return Roles{}, err
	}
	if err := ValidateModel(roles.Review); err != nil {
		return Roles{}, err
	}
	return roles, nil
}
