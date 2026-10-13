package project

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	return pinnedDotnetFormatter(data)
}

func pinnedDotnetFormatter(data []byte) ([][]string, string, error) {
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

// Formatter describes a controller-supported preparation policy. Commands are
// fixed by this package, never inferred from scripts or check failure output.
type Formatter struct {
	Name       string
	Extensions []string
	Setup      [][]string
	Command    []string
}

// DiscoverFormatters reads committed metadata supplied by the controller. Check
// selection is independent: explicit and legacy suites receive the same policy.
func DiscoverFormatters(manifest []byte) ([]Formatter, error) {
	if len(manifest) == 0 {
		return nil, nil
	}
	commands, warning, err := pinnedDotnetFormatter(manifest)
	if err != nil {
		return nil, err
	}
	if warning != "" {
		return nil, fmt.Errorf("%s", warning)
	}
	if len(commands) == 0 {
		return nil, nil
	}
	return []Formatter{{Name: "csharpier", Extensions: []string{".cs"}, Setup: commands[:1], Command: []string{"dotnet", "csharpier", "format"}}}, nil
}

// FormatterForChecks retains documented root configuration from direct check
// argv. Scripts and arbitrary fix commands are never interpreted as policies.
func FormatterForChecks(formatter Formatter, checks [][]string) (Formatter, error) {
	if formatter.Name != "csharpier" {
		return formatter, nil
	}
	var selected []string
	var selectedEnv []string
	var setup []string
	for _, command := range checks {
		invocation := dotnetInvocation(command)
		restore := len(invocation) >= 3 && slices.Equal(invocation[:3], []string{"dotnet", "tool", "restore"})
		rootCheck := len(invocation) >= 4 && slices.Equal(invocation[:4], []string{"dotnet", "csharpier", "check", "."})
		if !restore && !rootCheck {
			continue
		}
		environment, err := formatterEnvironment(command, invocation)
		if err != nil {
			return Formatter{}, err
		}
		if restore {
			for i := 3; i < len(invocation); i++ {
				switch invocation[i] {
				case "--disable-parallel", "--ignore-failed-sources", "--no-cache":
				case "--verbosity", "-v":
					i++
					if i >= len(invocation) || !slices.Contains([]string{"q", "quiet", "m", "minimal", "n", "normal", "d", "detailed", "diag", "diagnostic"}, invocation[i]) {
						return Formatter{}, fmt.Errorf("unsupported formatter tool restore options")
					}
				case "--tool-manifest":
					i++
					if i >= len(invocation) || invocation[i] != dotnetToolsManifest {
						return Formatter{}, fmt.Errorf("unsupported formatter tool manifest")
					}
				case "--configfile":
					i++
					if i >= len(invocation) || !formatterConfigPath(invocation[i]) {
						return Formatter{}, fmt.Errorf("unsafe formatter tool configuration")
					}
				default:
					return Formatter{}, fmt.Errorf("unsupported formatter tool restore options")
				}
			}
			if setup != nil && !slices.Equal(setup, command) {
				return Formatter{}, fmt.Errorf("ambiguous formatter tool restore policy")
			}
			setup = append([]string(nil), command...)
			continue
		}
		options := []string{}
		for i := 4; i < len(invocation); i++ {
			argument := invocation[i]
			name, value, equals := strings.Cut(argument, "=")
			switch name {
			case "--config-path", "--ignore-path":
				if !equals {
					i++
					if i >= len(invocation) {
						return Formatter{}, fmt.Errorf("missing formatter configuration path")
					}
					value = invocation[i]
				}
				if !formatterConfigPath(value) {
					return Formatter{}, fmt.Errorf("unsafe formatter configuration path")
				}
				if slices.Contains(options, name) {
					return Formatter{}, fmt.Errorf("ambiguous formatter configuration options")
				}
				options = append(options, name, value)
			case "--include-generated", "--no-msbuild-check":
				if equals {
					return Formatter{}, fmt.Errorf("unsupported formatter check option")
				}
				options = append(options, name)
			case "--no-cache":
				if equals {
					return Formatter{}, fmt.Errorf("unsupported formatter check option")
				}
			default:
				return Formatter{}, fmt.Errorf("unsupported formatter check option; preparation cannot infer its formatting policy")
			}
		}
		if selected != nil && (!slices.Equal(selected, options) || !slices.Equal(selectedEnv, environment)) {
			return Formatter{}, fmt.Errorf("ambiguous root formatter policy")
		}
		selected, selectedEnv = options, environment
	}
	if setup != nil {
		formatter.Setup = [][]string{setup}
	}
	formatter.Command = append(append([]string(nil), formatter.Command...), selected...)
	if len(selectedEnv) > 0 {
		formatter.Command = append(append([]string{"env"}, selectedEnv...), formatter.Command...)
	}
	return formatter, nil
}

func formatterConfigPath(value string) bool {
	if value == "" || filepath.IsAbs(value) || filepath.Clean(value) != value || strings.Contains(value, "\\") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." || part == ".git" {
			return false
		}
	}
	return !strings.ContainsAny(value, "\x00\r\n")
}
func formatterEnvironment(command, invocation []string) ([]string, error) {
	if len(command) == len(invocation) {
		return nil, nil
	}
	environment := command[1 : len(command)-len(invocation)]
	for _, setting := range environment {
		name, value, ok := strings.Cut(setting, "=")
		if !ok || !slices.Contains([]string{"DOTNET_CLI_TELEMETRY_OPTOUT", "DOTNET_NOLOGO", "DOTNET_SKIP_FIRST_TIME_EXPERIENCE"}, name) || !slices.Contains([]string{"0", "1", "true", "false"}, value) {
			return nil, fmt.Errorf("unsupported formatter environment setting")
		}
	}
	return append([]string(nil), environment...), nil
}
