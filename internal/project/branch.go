package project

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"
)

const DefaultBranchFormat = "{reference}-{ticket}"

var errInvalidBranchName = errors.New("branch name must be a literal valid Git branch with bounded path components")

func validateBranchFormat(format string) error {
	if format == "" {
		return nil
	}
	if len(format) > 256 || !strings.HasPrefix(format, "{reference}") || strings.Count(format, "{reference}") != 1 || strings.Count(format, "{ticket}") != 1 {
		return errors.New("branch_format must start with {reference} and contain {reference} and {ticket} exactly once")
	}
	literal := strings.NewReplacer("{reference}", "", "{ticket}", "").Replace(format)
	if strings.IndexFunc(literal, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._/-", r))
	}) >= 0 {
		return errors.New("branch_format literals must use letters, numbers, dots, underscores, slashes or hyphens; only {reference} and {ticket} are supported")
	}
	return nil
}

// BranchName resolves the configured format without reading ticket contents or
// changing Git refs. Valid references stay exact; invalid ones are rendered.
func BranchName(ctx context.Context, root, reference, ticket, format string) (string, error) {
	if !validWorkReference(reference) || !ticketName.MatchString(filepath.Base(ticket)) {
		return "", errors.New("branch planning requires a work reference and numbered ticket filename")
	}
	if err := validateBranchFormat(format); err != nil {
		return "", err
	}
	if format == "" {
		format = DefaultBranchFormat
	}
	_, description, _ := strings.Cut(strings.TrimSuffix(filepath.Base(ticket), ".md"), "-")
	description = branchSlug(strings.ToLower(description))
	if description == "" {
		return "", errors.New("ticket filename must contain a branch description")
	}
	render := func(ref string) string {
		return strings.NewReplacer("{reference}", ref, "{ticket}", description).Replace(format)
	}
	branch := render(reference)
	err := ValidateBranchName(ctx, root, branch)
	if err == nil {
		return branch, nil
	}
	if !errors.Is(err, errInvalidBranchName) {
		return "", err
	}
	ref := branchSlug(reference)
	if ref == "" {
		return "", errors.New("work reference cannot be rendered as a branch prefix")
	}
	branch = render(ref)
	if err := ValidateBranchName(ctx, root, branch); err != nil {
		return "", fmt.Errorf("branch_format does not produce a usable branch: %w", err)
	}
	return branch, nil
}

func branchSlug(value string) string {
	var result strings.Builder
	separator := false
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			if separator && result.Len() != 0 {
				result.WriteByte('-')
			}
			result.WriteRune(r)
			separator = false
		} else {
			separator = true
		}
	}
	return result.String()
}

// ValidateBranchName rejects shorthand expansion as well as invalid Git names.
func ValidateBranchName(ctx context.Context, root, branch string) error {
	if branch == "" || hasControl(branch) || strings.HasPrefix(branch, "-") || strings.Contains(branch, "@{") {
		return errInvalidBranchName
	}
	for _, component := range strings.Split(branch, "/") {
		if len(component) > 255 {
			return errInvalidBranchName
		}
	}
	command := exec.CommandContext(ctx, "git", "-c", "core.fsmonitor=false", "check-ref-format", "--branch", branch)
	command.Dir = root
	output, err := command.Output()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return errInvalidBranchName
		}
		return fmt.Errorf("cannot validate branch name: %w", err)
	}
	if strings.TrimSpace(string(output)) != branch {
		return errInvalidBranchName
	}
	return nil
}
