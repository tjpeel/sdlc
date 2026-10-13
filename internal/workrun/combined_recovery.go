package workrun

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

var combinedWorkerName = regexp.MustCompile(`^sdlc-check-[0-9a-f]{24}$`)

// RecoverCombinedCheck stops only resources in the frozen generation namespace.
// A new check cannot begin until every interrupted resource is confirmed gone.
func (checker DockerChecker) RecoverCombinedCheck(ctx context.Context) error {
	name := checker.WorkerName
	if !combinedWorkerName.MatchString(name) {
		return fmt.Errorf("invalid combined recovery worker identity")
	}
	for _, kind := range []string{"container", "volume", "network"} {
		args := []string{kind, "ls", "--filter", "name=^" + name, "--format", "{{.Name}}"}
		if kind == "container" {
			args = []string{"ps", "--all", "--filter", "name=^/" + name, "--format", "{{.Names}}"}
		}
		output, err := checker.Runtime.Docker.Output(ctx, args...)
		if err != nil {
			return fmt.Errorf("cannot inspect interrupted combined check resources: %w", err)
		}
		for _, resource := range strings.Fields(string(output)) {
			if resource != name && !strings.HasPrefix(resource, name+"-") {
				return fmt.Errorf("combined recovery returned an unrelated resource")
			}
			removal := []string{kind, "rm", resource}
			if kind == "container" {
				removal = []string{"rm", "--force", resource}
			}
			if _, err := checker.Runtime.Docker.Output(ctx, removal...); err != nil {
				return fmt.Errorf("cannot clean interrupted combined check resource: %w", err)
			}
		}
		output, err = checker.Runtime.Docker.Output(ctx, args...)
		if err != nil || len(strings.TrimSpace(string(output))) != 0 {
			return fmt.Errorf("interrupted combined check resources remain; child has not started")
		}
	}
	return nil
}
