package workrun

import (
	"context"
	"fmt"
	"path"
	"strings"
)

const localOnlyBoundaryInstruction = "Runner integration is local infrastructure, not ticket scope. Do not add SDLC names, runtime IDs, dispatch correlation identifiers, workflow inputs, tests, docs or configuration to the target repository to accommodate this runner. Do not change repository CI or product behavior solely to make manual dispatch, reporting or review automation work. Keep runner settings and state under ignored local .sdlc storage. Repair selected product requirements and independently actionable product defects only. If an existing workflow lacks the event context or evidence needed by the runner, report an operational block rather than a source-code repair. During review, flag runner-specific additions as out of scope."

type LocalOnlySourceRepository interface {
	CheckLocalOnlySource(context.Context, Plan, string) error
}

func (repository DockerRepository) CheckLocalOnlySource(ctx context.Context, plan Plan, workspace string) error {
	return validateLocalOnlySource(ctx, plan, workspace)
}
func validateLocalOnlySource(ctx context.Context, plan Plan, workspace string) error {
	// When the selected product is the controller itself, its name is product code.
	if strings.EqualFold(path.Base(plan.Repository), "sdlc") {
		return nil
	}
	if !objectID.MatchString(plan.SourceSHA) {
		return fmt.Errorf("local-only source verification requires the original source revision")
	}
	diff, err := SafeGit(ctx, workspace, "diff", "--no-ext-diff", "--no-textconv", "--no-color", "--unified=0", plan.SourceSHA, "HEAD", "--")
	if err != nil {
		return fmt.Errorf("cannot verify the local-only runner boundary: %w", err)
	}
	return validateLocalOnlyPatch(diff)
}
func validateLocalOnlyPatch(diff string) error {
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+") && strings.Contains(strings.ToLower(line), "sdlc") {
			return fmt.Errorf("product source contains added SDLC references; keep runner integration local and remove those source additions before publication")
		}
	}
	return nil
}
