package workrun

import (
	"fmt"
	"strings"
)

type workflowInputDeclaration struct {
	Required bool
	Default  string
}

// Read only the expanded mapping form of GitHub's event/input declarations.
// This is not a general YAML parser: aliases, flow mappings and multiline
// declaration keys are refused, so unsupported syntax cannot authorize a POST.
// Workflow validity remains GitHub/actionlint's responsibility. Keeping this
// bounded reader standard-library-only preserves the offline publisher build.
func readWorkflowDispatchDeclaration(data []byte) (map[string]workflowInputDeclaration, error) {
	if len(data) > 256*1024 {
		return nil, fmt.Errorf("configured workflow is too large")
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	onFound, prFound, dispatchFound := false, false, false
	eventIndent, inputIndent, nameIndent, propertyIndent := -1, -1, -1, -1
	inDispatch, inInputs := false, false
	inputs := map[string]workflowInputDeclaration{}
	events := map[string]bool{}
	name := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.ContainsRune(line, '\t') {
			return nil, fmt.Errorf("workflow declarations require space indentation")
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if indent == 0 {
			key, value, ok := strings.Cut(trimmed, ":")
			key = strings.Trim(key, "\"'")
			if key == "on" && ok {
				if onFound {
					return nil, fmt.Errorf("duplicate workflow event declaration")
				}
				if strings.TrimSpace(strings.SplitN(value, "#", 2)[0]) != "" {
					return nil, fmt.Errorf("workflow dispatch requires expanded event mappings")
				}
				onFound = true
				continue
			}
			if onFound {
				break
			}
			continue
		}
		if !onFound {
			continue
		}
		if eventIndent >= 0 && indent > eventIndent && !inDispatch {
			continue
		}
		key, value, ok := strings.Cut(trimmed, ":")
		if !ok {
			if inInputs && propertyIndent >= 0 && indent > propertyIndent {
				continue
			}
			return nil, fmt.Errorf("unsupported workflow event declaration")
		}
		key = strings.Trim(strings.TrimSpace(key), "\"'")
		value = strings.TrimSpace(strings.SplitN(value, "#", 2)[0])
		if eventIndent < 0 {
			eventIndent = indent
		}
		if indent == eventIndent {
			if events[key] {
				return nil, fmt.Errorf("duplicate workflow event")
			}
			events[key] = true
			if key == "<<" || strings.HasPrefix(value, "&") || strings.HasPrefix(value, "*") {
				return nil, fmt.Errorf("workflow event aliases are unsupported")
			}
			prFound = prFound || key == "pull_request"
			inDispatch = key == "workflow_dispatch"
			dispatchFound = dispatchFound || inDispatch
			if inDispatch && value != "" && value != "{}" && value != "null" {
				return nil, fmt.Errorf("workflow dispatch requires expanded input mappings")
			}
			inInputs = false
			continue
		}
		if !inDispatch {
			continue
		}
		if inputIndent < 0 {
			inputIndent = indent
		}
		if indent == inputIndent {
			inInputs = key == "inputs"
			if inInputs && value != "" && value != "{}" {
				return nil, fmt.Errorf("workflow inputs require an expanded mapping")
			}
			continue
		}
		if !inInputs {
			continue
		}
		if nameIndent < 0 {
			nameIndent = indent
		}
		if indent == nameIndent {
			if key == "" || key == "<<" || value != "" {
				return nil, fmt.Errorf("unsupported workflow input declaration")
			}
			if _, exists := inputs[key]; exists {
				return nil, fmt.Errorf("duplicate workflow input")
			}
			for _, r := range key {
				if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
					return nil, fmt.Errorf("unsupported workflow input name")
				}
			}
			name = key
			inputs[name] = workflowInputDeclaration{}
			propertyIndent = -1
			continue
		}
		if name == "" {
			return nil, fmt.Errorf("workflow input property lacks a name")
		}
		if propertyIndent < 0 {
			propertyIndent = indent
		}
		if indent != propertyIndent {
			continue
		}
		input := inputs[name]
		switch key {
		case "required":
			if value != "true" && value != "false" {
				return nil, fmt.Errorf("workflow required flag must be a boolean")
			}
			input.Required = value == "true"
		case "default":
			if strings.HasPrefix(value, "&") || strings.HasPrefix(value, "*") || strings.HasPrefix(value, "|") || strings.HasPrefix(value, ">") {
				return nil, fmt.Errorf("workflow input aliases are unsupported")
			}
			input.Default = strings.Trim(value, "\"'")
		}
		inputs[name] = input
	}
	if !prFound || !dispatchFound {
		return nil, fmt.Errorf("configured workflow must declare pull_request and workflow_dispatch")
	}
	return inputs, nil
}
