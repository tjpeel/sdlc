// Package runtimepins validates public dependency pins and applies them to a
// private runtime build recipe.
package runtimepins

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const DefaultSigningImage = "1password/op:2.39.0@sha256:3cd5a1febc662c93d46b944b983b301e710da5c016ef63be9d436cf2b1ed30d5"

// DefaultDaemonImage is the legacy recipe used before daemon pins were recorded.
const DefaultDaemonImage = "docker:29.8.2-dind"

type Pins struct {
	Arguments    map[string]string `json:"arguments"`
	Images       map[string]string `json:"images"`
	SigningImage string            `json:"signing_image"`
	DaemonImage  string            `json:"daemon_image"`
}

var argumentNames = []string{"CODEX_VERSION", "CLAUDE_VERSION", "GH_VERSION", "DOTNET_VERSION", "SKILLS_REVISION", "AGENTS_REVISION", "NPM_VERSION", "YARN_VERSION", "COMPOSE_VERSION", "BUILDX_VERSION"}
var stable = `(?:0|[1-9][0-9]{0,19})\.(?:0|[1-9][0-9]{0,19})\.(?:0|[1-9][0-9]{0,19})`
var stableVersion = regexp.MustCompile(`^` + stable + `$`)
var revision = regexp.MustCompile(`^[0-9a-f]{40}$`)
var digest = `@sha256:[0-9a-f]{64}`
var imagePatterns = map[string]*regexp.Regexp{
	"node":   regexp.MustCompile(`^node:` + stable + `-bookworm` + digest + `$`),
	"golang": regexp.MustCompile(`^golang:` + stable + `-bookworm` + digest + `$`),
	"docker": regexp.MustCompile(`^docker:` + stable + `-cli` + digest + `$`),
}
var signingPattern = regexp.MustCompile(`^1password/op:2\.(?:0|[1-9][0-9]{0,19})\.(?:0|[1-9][0-9]{0,19})` + digest + `$`)
var daemonPattern = regexp.MustCompile(`^docker:` + stable + `-dind` + digest + `$`)

func validVersion(value string) bool {
	if !stableVersion.MatchString(value) {
		return false
	}
	for _, part := range strings.Split(value, ".") {
		if _, err := strconv.ParseUint(part, 10, 64); err != nil {
			return false
		}
	}
	return true
}

func validImageVersion(reference string) bool {
	tag := strings.SplitN(strings.SplitN(reference, ":", 2)[1], "@", 2)[0]
	tag = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(tag, "-bookworm"), "-cli"), "-dind")
	return validVersion(tag)
}

func ValidateSigningImage(reference string) error {
	if !signingPattern.MatchString(reference) || !validImageVersion(reference) {
		return fmt.Errorf("signing image must be an exact public 1Password v2 digest reference")
	}
	return nil
}

func ValidateDaemonImage(reference string) error {
	if !daemonPattern.MatchString(reference) || !validImageVersion(reference) {
		return fmt.Errorf("Docker daemon image must be an exact public dind digest reference")
	}
	return nil
}

func (pins Pins) Validate() error {
	if len(pins.Arguments) != len(argumentNames) || len(pins.Images) != len(imagePatterns) {
		return fmt.Errorf("runtime pins must cover every managed argument and image")
	}
	for _, name := range argumentNames {
		value, ok := pins.Arguments[name]
		valid := validVersion(value)
		if name == "SKILLS_REVISION" || name == "AGENTS_REVISION" {
			valid = revision.MatchString(value)
		}
		if !ok || !valid {
			return fmt.Errorf("runtime pin %s is invalid", name)
		}
	}
	for name, pattern := range imagePatterns {
		if !pattern.MatchString(pins.Images[name]) || !validImageVersion(pins.Images[name]) {
			return fmt.Errorf("runtime image pin %s is invalid", name)
		}
	}
	if err := ValidateSigningImage(pins.SigningImage); err != nil {
		return err
	}
	if err := ValidateDaemonImage(pins.DaemonImage); err != nil {
		return err
	}
	return nil
}

var fromLine = regexp.MustCompile(`^FROM[ \t]+([^ \t]+)(.*)$`)
var argLine = regexp.MustCompile(`^ARG[ \t]+([A-Z_]+)(?:=(.*))?$`)
var recipeImages = map[string]*regexp.Regexp{
	"node":   regexp.MustCompile(`^node:(?:[1-9][0-9]{0,2}|` + stable + `)-bookworm` + digest + `$`),
	"golang": imagePatterns["golang"],
	"docker": imagePatterns["docker"],
}

// ReadDockerfile reads the three approved stages and managed ARG defaults. Empty
// or legacy missing defaults for inherited tools describe their base-image
// provenance; an update plan resolves them before producing validated Pins.
func ReadDockerfile(original []byte) (Pins, error) {
	pins := Pins{Arguments: map[string]string{}, Images: map[string]string{}, SigningImage: DefaultSigningImage, DaemonImage: DefaultDaemonImage}
	if err := visitRecipe(original, true, func(index int, name, value, suffix string, image bool) error {
		if image {
			pins.Images[name] = value
		} else {
			pins.Arguments[name] = value
		}
		return nil
	}); err != nil {
		return Pins{}, err
	}
	return pins, nil
}

// ApplyDockerfile replaces exactly one occurrence of each managed ARG and FROM.
// It returns bytes for an isolated build context and never changes the source.
func ApplyDockerfile(original []byte, pins Pins) ([]byte, error) {
	if err := pins.Validate(); err != nil {
		return nil, err
	}
	lines := strings.Split(string(original), "\n")
	err := visitRecipe(original, false, func(index int, name, value, suffix string, image bool) error {
		if image {
			lines[index] = "FROM " + pins.Images[name] + suffix
		} else {
			lines[index] = "ARG " + name + "=" + pins.Arguments[name]
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return []byte(strings.Join(lines, "\n")), nil
}

func optionalArgument(name string) bool {
	return name == "NPM_VERSION" || name == "YARN_VERSION" || name == "COMPOSE_VERSION" || name == "BUILDX_VERSION"
}

func visitRecipe(original []byte, legacy bool, visit func(int, string, string, string, bool) error) error {
	if len(original) == 0 || len(original) > 1<<20 || strings.ContainsRune(string(original), '\r') || strings.ContainsRune(string(original), 0) {
		return fmt.Errorf("runtime Dockerfile is invalid")
	}
	arguments, images := map[string]int{}, map[string]int{}
	wanted := map[string]bool{}
	for _, name := range argumentNames {
		wanted[name] = true
	}
	for index, line := range strings.Split(string(original), "\n") {
		if fields := fromLine.FindStringSubmatch(line); fields != nil {
			name := strings.SplitN(fields[1], ":", 2)[0]
			pattern, ok := recipeImages[name]
			if !ok || !pattern.MatchString(fields[1]) {
				return fmt.Errorf("runtime Dockerfile has an unsupported FROM reference")
			}
			expectedSuffix := map[string]string{"node": "", "golang": " AS publisher-build", "docker": " AS docker-tools"}[name]
			if fields[2] != expectedSuffix {
				return fmt.Errorf("runtime Dockerfile has an unexpected %s stage", name)
			}
			images[name]++
			if err := visit(index, name, fields[1], fields[2], true); err != nil {
				return err
			}
		} else if fields := argLine.FindStringSubmatch(line); fields != nil && wanted[fields[1]] {
			value := fields[2]
			valid := validVersion(value)
			if fields[1] == "SKILLS_REVISION" || fields[1] == "AGENTS_REVISION" {
				valid = revision.MatchString(value)
			}
			optional := optionalArgument(fields[1])
			if !strings.Contains(line, "=") || (!valid && !(optional && value == "")) {
				return fmt.Errorf("runtime Dockerfile argument %s is invalid", fields[1])
			}
			arguments[fields[1]]++
			if err := visit(index, fields[1], value, "", false); err != nil {
				return err
			}
		} else if strings.HasPrefix(strings.TrimSpace(strings.ToUpper(line)), "FROM ") || strings.HasPrefix(strings.TrimSpace(strings.ToUpper(line)), "FROM\t") {
			return fmt.Errorf("runtime Dockerfile has an unsupported FROM instruction")
		} else {
			// Refuse malformed/indented duplicate declarations instead of silently
			// leaving an alternative build argument in the recipe.
			parts := strings.Fields(line)
			if len(parts) > 1 && strings.EqualFold(parts[0], "ARG") && wanted[strings.SplitN(parts[1], "=", 2)[0]] {
				return fmt.Errorf("runtime Dockerfile has an invalid managed ARG instruction")
			}
		}
	}
	for _, name := range argumentNames {
		if legacy && optionalArgument(name) && arguments[name] == 0 {
			if err := visit(-1, name, "", "", false); err != nil {
				return err
			}
			continue
		}
		if arguments[name] != 1 {
			return fmt.Errorf("runtime Dockerfile must declare %s exactly once", name)
		}
	}
	for name := range recipeImages {
		if images[name] != 1 {
			return fmt.Errorf("runtime Dockerfile must declare %s FROM exactly once", name)
		}
	}
	return nil
}
