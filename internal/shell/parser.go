package shell

import (
	"fmt"
	"strings"
	"unicode"
)

// Parse returns literal argv. Quotes and backslash escaping group characters;
// shell expansion, substitutions, operators and environment interpolation never run.
func Parse(line string) ([]string, error) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "/") {
		return nil, fmt.Errorf("commands must start with /")
	}
	var args []string
	var word strings.Builder
	var quote rune
	escaped, started := false, false
	for _, r := range strings.TrimPrefix(line, "/") {
		if unicode.IsControl(r) {
			return nil, fmt.Errorf("control characters are not accepted")
		}
		if escaped {
			word.WriteRune(r)
			escaped = false
			started = true
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			started = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			started = true
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			started = true
			continue
		}
		if unicode.IsSpace(r) {
			if started {
				args = append(args, word.String())
				word.Reset()
				started = false
			}
			continue
		}
		word.WriteRune(r)
		started = true
	}
	if escaped || quote != 0 {
		return nil, fmt.Errorf("unfinished quote or escape")
	}
	if started {
		args = append(args, word.String())
	}
	if len(args) == 0 || args[0] == "" {
		return nil, fmt.Errorf("choose a command after /")
	}
	return args, nil
}

var valueFlags = map[string]bool{"run": true, "reference": true, "ticket": true, "parallel": true, "provider": true, "github-profile": true, "input": true, "base": true, "branch": true, "repo": true, "model": true, "effort": true, "review-model": true, "review-effort": true, "resume": true, "answer-file": true, "timeout": true, "notify": true, "terminal": true, "launch-id": true, "scope": true, "page": true, "interval": true, "profile": true, "file": true, "source": true}

func hasOption(args []string, name string) bool {
	// Known run value flags consume the following literal, even when it starts --.

	for i := 1; i < len(args); i++ {
		if args[i] == "--" {
			break
		}
		if !strings.HasPrefix(args[i], "--") {
			continue
		}
		key, value, assigned := strings.Cut(strings.TrimPrefix(args[i], "--"), "=")
		if key == name {
			return !assigned || value == "true"
		}
		if valueFlags[key] && !assigned {
			i++
		}
	}
	return false
}
