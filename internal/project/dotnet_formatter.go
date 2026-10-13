package project

import (
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const dotnetToolsManifest = ".config/dotnet-tools.json"

// Tool manifests are static discovery inputs; initialization never restores or
// invokes their tools. Only the documented stable CSharpier 1.x CLI is inferred.
func dotnetFormatter(root string) ([][]string, string, error) {
	if err := directoryOrMissing(filepath.Join(root, ".config")); err != nil {
		return nil, "", err
	}
	data, err := readOptional(filepath.Join(root, dotnetToolsManifest))
	if err != nil || data == nil {
		return nil, "", err
	}
	var manifest struct {
		Version int                        `json:"version"`
		IsRoot  json.RawMessage            `json:"isRoot"`
		Tools   map[string]json.RawMessage `json:"tools"`
	}
	warning := "The .NET tool manifest does not identify a supported pinned CSharpier 1.x command; configure its formatting checks explicitly."
	if !validDotnetToolJSON(data) || json.Unmarshal(data, &manifest) != nil || manifest.Version != 1 || manifest.Tools == nil || !optionalDotnetBoolean(manifest.IsRoot) {
		return nil, warning, nil
	}
	var selected json.RawMessage
	for name, raw := range manifest.Tools {
		if strings.EqualFold(name, "csharpier") {
			if selected != nil {
				return nil, warning, nil
			}
			selected = raw
		}
	}
	if selected == nil {
		return nil, "", nil
	}
	var tool struct {
		Version     string          `json:"version"`
		Commands    []string        `json:"commands"`
		RollForward json.RawMessage `json:"rollForward"`
	}
	if json.Unmarshal(selected, &tool) != nil || !csharpierVersion.MatchString(tool.Version) || !slices.Equal(tool.Commands, []string{"csharpier"}) || !optionalDotnetBoolean(tool.RollForward) {
		return nil, warning, nil
	}
	return [][]string{{"dotnet", "tool", "restore"}, {"dotnet", "csharpier", "check", "."}}, "", nil
}

func optionalDotnetBoolean(raw json.RawMessage) bool {
	value := strings.TrimSpace(string(raw))
	return len(raw) == 0 || value == "true" || value == "false"
}

// The bounded manifest must not rely on JSON's last-member-wins behaviour.
// Limit nesting before decoding its selected fields; unknown fields remain valid.
func validDotnetToolJSON(data []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var value func(int) bool
	value = func(depth int) bool {
		if depth > 16 {
			return false
		}
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		delimiter, nested := token.(json.Delim)
		if !nested {
			return true
		}
		if delimiter != '{' && delimiter != '[' {
			return false
		}
		seen := map[string]bool{}
		for decoder.More() {
			if delimiter == '{' {
				key, err := decoder.Token()
				name, ok := key.(string)
				if err != nil || !ok || seen[name] {
					return false
				}
				seen[name] = true
			}
			if !value(depth + 1) {
				return false
			}
		}
		closing, err := decoder.Token()
		return err == nil && (delimiter == '{' && closing == json.Delim('}') || delimiter == '[' && closing == json.Delim(']'))
	}
	if !value(0) {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}

var csharpierVersion = regexp.MustCompile(`^1\.(0|[1-9][0-9]{0,5})\.(0|[1-9][0-9]{0,5})$`)

func dotnetInvocation(command []string) []string {
	if len(command) > 0 && command[0] == "env" {
		for i, argument := range command[1:] {
			if !strings.Contains(argument, "=") {
				return command[i+1:]
			}
		}
	}
	return command
}

// Preserve documented root options and environment settings. Only exact
// duplicates are removed; other required commands retain their order.
func dotnetFormatterFirst(commands, defaults [][]string) ([][]string, [][]string) {
	if len(defaults) == 0 {
		return nil, commands
	}
	var setup, lint, remaining [][]string
	for _, command := range commands {
		invocation := dotnetInvocation(command)
		switch {
		case len(invocation) >= 3 && slices.Equal(invocation[:3], []string{"dotnet", "tool", "restore"}):
			if !slices.ContainsFunc(setup, func(seen []string) bool { return slices.Equal(seen, command) }) {
				setup = append(setup, command)
			}
		case len(invocation) >= 4 && slices.Equal(invocation[:4], []string{"dotnet", "csharpier", "check", "."}):
			if !slices.ContainsFunc(lint, func(seen []string) bool { return slices.Equal(seen, command) }) {
				lint = append(lint, command)
			}
		default:
			remaining = append(remaining, command)
		}
	}
	if len(setup) == 0 {
		setup = append(setup, defaults[0])
	}
	if len(lint) == 0 {
		lint = append(lint, defaults[1])
	}
	return append(setup, lint...), remaining
}
