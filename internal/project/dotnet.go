package project

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type dotnetDiscovery struct {
	checks            [][]string
	formatting        [][]string
	docker            bool
	sources, warnings []string
}

type dotnetProperties struct {
	Framework          string `xml:"TargetFramework"`
	Frameworks         string `xml:"TargetFrameworks"`
	Assembly           string `xml:"AssemblyName"`
	Test               string `xml:"IsTestProject"`
	NativeRunner       string `xml:"UseMicrosoftTestingPlatformRunner"`
	Condition          string `xml:"Condition,attr"`
	OutputPath         string `xml:"OutputPath"`
	BaseOutputPath     string `xml:"BaseOutputPath"`
	OutDir             string `xml:"OutDir"`
	ArtifactsPath      string `xml:"ArtifactsPath"`
	UseArtifactsOutput string `xml:"UseArtifactsOutput"`
	RuntimeIdentifier  string `xml:"RuntimeIdentifier"`
	AppendFramework    string `xml:"AppendTargetFrameworkToOutputPath"`
	Configuration      string `xml:"Configuration"`
	UncertainOutput    bool   `xml:"-"`
	ConditionalTest    bool   `xml:"-"`
}

func (properties *dotnetProperties) UnmarshalXML(decoder *xml.Decoder, start xml.StartElement) error {
	var raw struct {
		Condition string `xml:"Condition,attr"`
		Values    []struct {
			XMLName   xml.Name
			Condition string `xml:"Condition,attr"`
			Text      string `xml:",chardata"`
		} `xml:",any"`
	}
	if err := decoder.DecodeElement(&raw, &start); err != nil {
		return err
	}
	properties.Condition = raw.Condition
	fields := map[string]*string{
		"TargetFramework": &properties.Framework, "TargetFrameworks": &properties.Frameworks,
		"AssemblyName": &properties.Assembly, "IsTestProject": &properties.Test,
		"UseMicrosoftTestingPlatformRunner": &properties.NativeRunner,
		"OutputPath":                        &properties.OutputPath, "BaseOutputPath": &properties.BaseOutputPath,
		"OutDir": &properties.OutDir, "ArtifactsPath": &properties.ArtifactsPath,
		"UseArtifactsOutput": &properties.UseArtifactsOutput, "RuntimeIdentifier": &properties.RuntimeIdentifier,
		"AppendTargetFrameworkToOutputPath": &properties.AppendFramework, "Configuration": &properties.Configuration,
	}
	for _, value := range raw.Values {
		if target := fields[value.XMLName.Local]; target != nil {
			*target = strings.TrimSpace(value.Text)
			if value.Condition != "" {
				properties.UncertainOutput = true
			}
			if (value.XMLName.Local == "IsTestProject" || value.XMLName.Local == "UseMicrosoftTestingPlatformRunner") && (value.Condition != "" || raw.Condition != "") {
				properties.ConditionalTest = true
			}
		}
	}
	return nil
}

type dotnetProject struct {
	Groups []dotnetProperties `xml:"PropertyGroup"`
	Items  []struct {
		Packages []struct {
			Name string `xml:"Include,attr"`
		} `xml:"PackageReference"`
	} `xml:"ItemGroup"`
}

func discoverDotnet(root string, tools []string) (dotnetDiscovery, error) {
	var found dotnetDiscovery
	targets := []string{}
	for _, name := range tools {
		switch strings.ToLower(filepath.Ext(name)) {
		case ".sln", ".slnx", ".csproj", ".fsproj":
			targets = append(targets, name)
		}
	}
	if len(targets) == 0 {
		return found, nil
	}
	// Solutions define the selected project set; unrelated examples are not checks.
	solutions := []string{}
	for _, name := range targets {
		if strings.HasSuffix(name, ".sln") || strings.HasSuffix(name, ".slnx") {
			solutions = append(solutions, name)
		}
	}
	if len(solutions) != 0 {
		targets = solutions
	}
	if len(targets) != 1 {
		found.warnings = append(found.warnings, "Multiple .NET build targets found; configure checks for the intended solution.")
		return found, nil
	}
	formatter, warning, err := dotnetFormatter(root)
	if err != nil {
		return found, err
	}
	if warning != "" {
		found.warnings = append(found.warnings, warning)
		return found, nil
	}
	target := targets[0]
	projects, err := dotnetProjects(root, target)
	if err != nil {
		return found, err
	}
	if len(projects) == 0 {
		found.warnings = append(found.warnings, "The .NET manifest does not identify projects; configure checks explicitly.")
		return found, nil
	}
	global, err := readOptional(filepath.Join(root, "global.json"))
	if err != nil {
		return found, err
	}
	var settings struct {
		Test struct {
			Runner string `json:"runner"`
		} `json:"test"`
	}
	if global != nil && json.Unmarshal(global, &settings) != nil {
		return found, fmt.Errorf("global.json must contain valid JSON to discover .NET checks")
	}
	runner := settings.Test.Runner
	if runner != "" && runner != "VSTest" && runner != "Microsoft.Testing.Platform" {
		found.warnings = append(found.warnings, "Unknown global.json test runner; configure .NET checks explicitly.")
		return found, nil
	}
	compose := false
	for _, name := range tools {
		if name == "compose.yml" || name == "compose.yaml" || name == "docker-compose.yml" || name == "docker-compose.yaml" {
			compose = true
		}
	}
	ci, ciSources, ciWarnings, err := dotnetCICommands(root)
	if err != nil {
		return found, err
	}
	for _, name := range []string{"CONTRIBUTING.md", "README.md"} {
		data, err := readOptional(filepath.Join(root, name))
		if err != nil {
			return found, err
		}
		blocks := dotnetDocumentBlocks(string(data))
		commands := [][]string{}
		for _, block := range blocks {
			parsed := dotnetCommandLines(block)
			if parsed == nil && strings.TrimSpace(block) != "" {
				found.warnings = append(found.warnings, "Required checks in "+name+" contain unsupported shell syntax; configure their complete sequence explicitly.")
				return found, nil
			}
			commands = append(commands, parsed...)
		}
		if dotnetComplete(commands) {
			if !dotnetRunnerMatches(commands, runner) {
				found.warnings = append(found.warnings, "Documented .NET test commands do not match global.json; configure the matching runner before starting work.")
				return found, nil
			}
			found.checks, found.docker, found.formatting = dotnetPrepare(commands, target, ci, formatter)
			if found.docker && !compose {
				found.warnings = append(found.warnings, "Documented checks need Compose but no root Compose file was found; configure checks explicitly.")
				found.checks = nil
				return found, nil
			}
			found.sources = []string{name, target}
			if len(formatter) != 0 {
				found.sources = append(found.sources, dotnetToolsManifest)
			}
			if !dotnetHas(commands, "restore") && dotnetHas(ci, "restore") {
				found.sources = append(found.sources, ciSources...)
			}
			if global != nil {
				found.sources = append(found.sources, "global.json")
			}
			return found, nil
		}
	}
	if len(ciWarnings) != 0 {
		found.warnings = append(found.warnings, ciWarnings...)
		return found, nil
	}
	if len(ciSources) == 1 && dotnetComplete(ci) && dotnetRunnerMatches(ci, runner) {
		found.checks, found.docker, found.formatting = dotnetPrepare(ci, target, nil, formatter)
		if found.docker && !compose {
			found.checks = nil
			found.warnings = append(found.warnings, "CI checks need Compose but no root Compose file was found; configure checks explicitly.")
			return found, nil
		}
		found.sources = append([]string{target}, ciSources...)
		if len(formatter) != 0 {
			found.sources = append(found.sources, dotnetToolsManifest)
		}
		if global != nil {
			found.sources = append(found.sources, "global.json")
		}
		return found, nil
	}
	// Without a complete documented sequence, derive SDK commands from manifests.
	fallback := [][]string{{"dotnet", "restore", target}, {"dotnet", "build", target, "--no-restore", "-m:1", "-nodeReuse:false", "--disable-build-servers", "-v:minimal"}}
	found.sources = []string{target}
	if global != nil {
		found.sources = append(found.sources, "global.json")
	}
	var modules []string
	var hasTests bool
	for _, name := range projects {
		data, err := readOptional(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			return found, err
		}
		var project dotnetProject
		if xml.Unmarshal(data, &project) != nil {
			return found, fmt.Errorf("cannot inspect .NET project %s", name)
		}
		properties := dotnetProperties{}
		// The nearest Directory.Build.props supplies common properties.
		for directory := filepath.Dir(filepath.FromSlash(name)); ; directory = filepath.Dir(directory) {
			path := filepath.Join(directory, "Directory.Build.props")
			inherited, err := readOptional(filepath.Join(root, path))
			if err != nil {
				return found, err
			}
			if inherited != nil {
				var shared dotnetProject
				if xml.Unmarshal(inherited, &shared) != nil {
					return found, fmt.Errorf("cannot inspect Directory.Build.props")
				}
				for _, group := range shared.Groups {
					mergeDotnetProperties(&properties, group)
				}
				found.sources = append(found.sources, filepath.ToSlash(path))
				break
			}
			if directory == "." {
				break
			}
		}
		for _, group := range project.Groups {
			mergeDotnetProperties(&properties, group)
		}
		test := strings.EqualFold(properties.Test, "true") || strings.EqualFold(properties.NativeRunner, "true") || properties.ConditionalTest
		for _, group := range project.Items {
			for _, packageRef := range group.Packages {
				if packageRef.Name == "Microsoft.NET.Test.Sdk" || packageRef.Name == "xunit.v3" {
					test = true
				}
			}
		}
		if !test {
			continue
		}
		hasTests = true
		found.sources = append(found.sources, name)
		if strings.EqualFold(properties.NativeRunner, "true") && runner != "Microsoft.Testing.Platform" {
			found.warnings = append(found.warnings, "Native Microsoft Testing Platform projects need an explicit matching global.json runner or documented check sequence.")
			return found, nil
		}
		if runner != "Microsoft.Testing.Platform" {
			continue
		}
		if properties.UncertainOutput {
			found.warnings = append(found.warnings, "Test output paths have conditional or custom MSBuild settings; configure exact .NET test commands.")
			return found, nil
		}
		frameworks := properties.Framework
		if properties.Frameworks != "" {
			frameworks = properties.Frameworks
		}
		assembly := properties.Assembly
		if assembly == "" {
			assembly = strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
		}
		for _, framework := range strings.Split(frameworks, ";") {
			if !dotnetLiteral.MatchString(framework) || !dotnetLiteral.MatchString(assembly) {
				found.warnings = append(found.warnings, "Test output paths depend on unresolved MSBuild properties; configure exact .NET test commands.")
				return found, nil
			}
			modules = append(modules, filepath.ToSlash(filepath.Join(filepath.Dir(name), "bin", "Debug", framework, assembly+".dll")))
		}
	}
	if hasTests && runner != "Microsoft.Testing.Platform" {
		fallback = append(fallback, []string{"dotnet", "test", target, "--no-build", "--no-restore", "-v:minimal"})
	}
	for _, module := range modules {
		fallback = append(fallback, []string{"dotnet", "test", "--test-modules", module, "--no-build", "-v:minimal"})
	}
	needsCompose := false
	for _, command := range ci {
		if len(command) >= 3 && command[0] == "docker" && command[1] == "compose" && command[2] == "up" {
			needsCompose = true
		}
	}
	if needsCompose && compose {
		fallback = append(fallback[:1], append([][]string{{"docker", "compose", "up", "--build", "-d", "--wait"}}, fallback[1:]...)...)
		fallback = append(fallback, []string{"docker", "compose", "down", "-v", "--remove-orphans"})
	} else if compose && hasTests {
		found.warnings = append(found.warnings, "Compose is present but test service startup is not documented; review whether the generated checks need Docker services.")
	}
	found.checks, found.docker, found.formatting = dotnetPrepare(fallback, target, nil, formatter)
	if len(formatter) != 0 {
		found.sources = append(found.sources, dotnetToolsManifest)
	}
	if !hasTests {
		found.warnings = append(found.warnings, "No .NET test projects found; generated checks restore and build the selected target.")
	}
	return found, nil
}

var dotnetLiteral = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

func mergeDotnetProperties(target *dotnetProperties, source dotnetProperties) {
	target.UncertainOutput = target.UncertainOutput || source.UncertainOutput
	target.ConditionalTest = target.ConditionalTest || source.ConditionalTest
	if source.Condition != "" && (source.Framework != "" || source.Frameworks != "" || source.Assembly != "" || source.Test != "" || source.NativeRunner != "") || source.OutputPath != "" || source.BaseOutputPath != "" || source.OutDir != "" || source.ArtifactsPath != "" || strings.EqualFold(source.UseArtifactsOutput, "true") || source.RuntimeIdentifier != "" || strings.EqualFold(source.AppendFramework, "false") || source.Configuration != "" && source.Configuration != "Debug" {
		target.UncertainOutput = true
	}
	if source.Framework != "" {
		target.Framework = source.Framework
	}
	if source.Frameworks != "" {
		target.Frameworks = source.Frameworks
	}
	if source.Assembly != "" {
		target.Assembly = source.Assembly
	}
	if source.Test != "" {
		target.Test = source.Test
	}
	if source.NativeRunner != "" {
		target.NativeRunner = source.NativeRunner
	}
}

func dotnetProjects(root, target string) ([]string, error) {
	data, err := readOptional(filepath.Join(root, target))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	projects := []string{}
	if strings.HasSuffix(target, ".slnx") {
		decoder := xml.NewDecoder(strings.NewReader(string(data)))
		for {
			token, err := decoder.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("cannot inspect .slnx solution")
			}
			if element, ok := token.(xml.StartElement); ok && element.Name.Local == "Project" {
				for _, attr := range element.Attr {
					if attr.Name.Local == "Path" {
						projects = append(projects, strings.ReplaceAll(attr.Value, "\\", "/"))
					}
				}
			}
		}
	} else if strings.HasSuffix(target, ".sln") {
		pattern := regexp.MustCompile(`(?m)^Project\([^\n]+\) = "[^\n]+", "([^"\n]+\.(?:csproj|fsproj))",`)
		for _, match := range pattern.FindAllStringSubmatch(string(data), 257) {
			projects = append(projects, strings.ReplaceAll(match[1], "\\", "/"))
		}
	} else {
		projects = append(projects, target)
	}
	if len(projects) > 256 {
		return nil, fmt.Errorf("too many .NET projects to inspect automatically")
	}
	for _, path := range projects {
		if !portablePath(path) || strings.HasPrefix(path, ".git/") || strings.HasPrefix(path, ".sdlc/") {
			return nil, fmt.Errorf("solution project paths must stay inside the checkout")
		}
		if err := directoryOrMissing(filepath.Dir(filepath.Join(root, filepath.FromSlash(path)))); err != nil {
			return nil, err
		}
		data, err := readOptional(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			return nil, err
		}
		if data == nil {
			return nil, fmt.Errorf("solution project %s is missing", path)
		}
	}
	sort.Strings(projects)
	return projects, nil
}

func dotnetDocumentBlocks(text string) []string {
	var blocks []string
	var lines []string
	section, fence := false, false
	depth := 0
	required := regexp.MustCompile(`(?mi)^#+\s+required checks\s*$`).MatchString(text)
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") && !fence {
			heading := strings.ToLower(strings.TrimSpace(strings.TrimLeft(trimmed, "#")))
			level := len(trimmed) - len(strings.TrimLeft(trimmed, "#"))
			if !section || level <= depth {
				section = heading == "required checks" || !required && (heading == "validation" || heading == "testing" || heading == "build and test" || heading == "tests")
				if section {
					depth = level
				}
			}
		}
		if strings.HasPrefix(trimmed, "```") && section {
			if fence {
				blocks = append(blocks, strings.Join(lines, "\n"))
				lines = nil
				fence = false
			} else {
				language := strings.TrimPrefix(trimmed, "```")
				fence = language == "bash" || language == "sh" || language == "shell" || language == ""
			}
			continue
		}
		if fence {
			lines = append(lines, line)
		}
	}
	return blocks
}

func dotnetCICommands(root string) ([][]string, []string, []string, error) {
	directory := filepath.Join(root, ".github", "workflows")
	if err := directoryOrMissing(directory); err != nil {
		return nil, nil, nil, err
	}
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return nil, nil, nil, nil
	}
	if err != nil {
		return nil, nil, nil, fmt.Errorf("cannot inspect workflow check commands")
	}
	if len(entries) > 64 {
		return nil, nil, nil, fmt.Errorf("too many workflows to inspect automatically")
	}
	var commands [][]string
	var sources []string
	var warnings []string
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".yml") && !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		data, err := readOptional(filepath.Join(directory, entry.Name()))
		if err != nil {
			return nil, nil, nil, err
		}
		var body []string
		indent := -1
		unsupported := false
		relevant := false
		flush := func() {
			text := strings.Join(body, "\n")
			if strings.Contains(text, "dotnet restore") || strings.Contains(text, "dotnet build") || strings.Contains(text, "dotnet test") || strings.Contains(text, "dotnet tool restore") || strings.Contains(text, "dotnet csharpier") || strings.Contains(text, "docker compose") {
				relevant = true
			}
			if parsed := dotnetCommandLines(text); parsed == nil && strings.TrimSpace(text) != "" {
				unsupported = true
			} else {
				commands = append(commands, parsed...)
			}
			body = nil
		}
		before := len(commands)
		for _, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			padding := len(line) - len(strings.TrimLeft(line, " "))
			if indent >= 0 && (trimmed == "" || padding > indent) {
				body = append(body, line)
				continue
			}
			flush()
			indent = -1
			value := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
			if strings.HasPrefix(value, "run:") {
				value = strings.TrimSpace(strings.TrimPrefix(value, "run:"))
				if value == "|" || value == "|-" || value == ">" || value == ">-" {
					indent = padding
					if strings.HasPrefix(trimmed, "- ") {
						indent += 2
					}
				} else {
					body = append(body, value)
					flush()
				}
			}
		}
		flush()
		if len(commands) != before && strings.Contains(string(data), "working-directory:") {
			unsupported = true
		}
		if unsupported && relevant {
			commands = commands[:before]
			warnings = append(warnings, "Check steps in .github/workflows/"+entry.Name()+" need unsupported setup or execution context; configure their complete local sequence explicitly.")
		} else if len(commands) != before {
			sources = append(sources, ".github/workflows/"+entry.Name())
		}
	}
	return commands, sources, warnings, nil
}

func dotnetCommandLines(text string) [][]string {
	var commands [][]string
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		command := dotnetCommandLine(line)
		if command == nil {
			return nil
		}
		commands = append(commands, command)
	}
	return commands
}

func dotnetCommandLine(line string) []string {
	words := strings.Fields(strings.TrimSpace(line))
	if len(words) < 2 || strings.ContainsAny(line, "$`\\;|&<>\"'(){}") {
		return nil
	}
	var environment []string
	for len(words) != 0 && strings.HasPrefix(words[0], "DOTNET_") && strings.Contains(words[0], "=") {
		if !regexp.MustCompile(`^DOTNET_[A-Z0-9_]+=(0|1|true|false)$`).MatchString(words[0]) {
			return nil
		}
		environment = append(environment, words[0])
		words = words[1:]
	}
	if len(words) < 2 {
		return nil
	}
	if words[0] == "dotnet" {
		toolRestore := len(words) >= 3 && words[1] == "tool" && words[2] == "restore"
		formatterCheck := len(words) >= 4 && words[1] == "csharpier" && words[2] == "check" && !strings.HasPrefix(words[3], "-")
		if words[1] != "restore" && words[1] != "build" && words[1] != "test" && !toolRestore && !formatterCheck {
			return nil
		}
		if toolRestore {
			for i, word := range words {
				if strings.HasPrefix(word, "--tool-manifest") && (word != "--tool-manifest" || i+1 >= len(words) || words[i+1] != dotnetToolsManifest) {
					return nil
				}
			}
		}
	} else if len(words) >= 3 && words[0] == "docker" && words[1] == "compose" {
		if words[2] != "up" && words[2] != "down" {
			return nil
		}
	} else {
		return nil
	}
	for _, word := range words {
		if strings.HasPrefix(word, "/") || strings.Contains(word, "../") || hasControl(word) {
			return nil
		}
	}
	if len(environment) != 0 {
		return append(append([]string{"env"}, environment...), words...)
	}
	return words
}

func dotnetHas(commands [][]string, verb string) bool {
	for _, command := range commands {
		for i := 0; i+1 < len(command); i++ {
			if command[i] == "dotnet" && command[i+1] == verb {
				return true
			}
		}
	}
	return false
}

func dotnetComplete(commands [][]string) bool {
	return dotnetHas(commands, "build") && dotnetHas(commands, "test")
}

func dotnetRunnerMatches(commands [][]string, runner string) bool {
	for _, command := range commands {
		for i := 0; i+1 < len(command); i++ {
			if command[i] != "dotnet" || command[i+1] != "test" {
				continue
			}
			platformArguments := false
			for _, arg := range command[i+2:] {
				if arg == "--test-modules" || arg == "--project" || arg == "--solution" {
					platformArguments = true
				}
			}
			if platformArguments != (runner == "Microsoft.Testing.Platform") {
				return false
			}
		}
	}
	return true
}

func dotnetQuote(command []string) string {
	words := make([]string, len(command))
	for i, word := range command {
		words[i] = "'" + strings.ReplaceAll(word, "'", "'\\''") + "'"
	}
	return strings.Join(words, " ")
}

func dotnetPrepare(commands [][]string, target string, ci, formatter [][]string) ([][]string, bool, [][]string) {
	prefix, commands := dotnetFormatterFirst(commands, formatter)
	if !dotnetHas(commands, "restore") {
		restore := []string{"dotnet", "restore", target}
		for _, command := range ci {
			if len(command) >= 3 && command[0] == "dotnet" && command[1] == "restore" && command[2] == target {
				restore = command
				break
			}
		}
		commands = append([][]string{restore}, commands...)
	}
	docker := false
	down := []string{"docker", "compose", "down", "-v", "--remove-orphans"}
	var work [][]string
	for index, command := range commands {
		if len(command) >= 3 && command[0] == "docker" && command[1] == "compose" {
			docker = true
			if command[2] == "down" {
				down = command
				if index == len(commands)-1 {
					continue
				}
			}
		}
		work = append(work, command)
	}
	if !docker {
		return work, false, prefix
	}
	parts := []string{"trap " + dotnetQuote([]string{dotnetQuote(down)}) + " EXIT"}
	for _, command := range work {
		parts = append(parts, dotnetQuote(command))
	}
	return [][]string{{"sh", "-eu", "-c", strings.Join(parts, "; ")}}, true, prefix
}
