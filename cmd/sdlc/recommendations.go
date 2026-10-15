package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/tjpeel/sdlc/internal/dashboard"
	"github.com/tjpeel/sdlc/internal/workrun"
)

func acceptRecommendationsCommand(ctx context.Context, id string, dry bool, timeout time.Duration, output io.Writer) error {
	action, err := resolveRunAction(ctx, id, false, true)
	if err != nil {
		return err
	}
	text, err := workrun.RecommendationFeedback(*action.View.Journal)
	if err != nil {
		return err
	}
	fmt.Fprintln(output, "Recommendations selected for acceptance:")
	for _, line := range strings.Split(text, "\n") {
		fmt.Fprintln(output, dashboard.SafeText(line))
	}
	current, err := resolveRunAction(ctx, action.View.ID, false, true)
	if err != nil {
		return err
	}
	if current.Checkpoint != action.Checkpoint {
		return fmt.Errorf("review checkpoint changed; inspect the latest recommendations before accepting")
	}
	if dry {
		fmt.Fprintln(output, "Preview only: no acceptance saved, files written, remote requests or provider work started.")
		return printPRFeedbackResumePlan(output, *current.View.Journal, false)
	}
	saved, err := workrun.StoreLocalPRFeedback(current.View.Directory, text)
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "Accepted recommendations saved privately: %s\n", saved)
	argv := append(runActionArguments(current, ""), "--pr-feedback-file", saved)
	if timeout != 0 {
		argv = append(argv, "--timeout", timeout.String())
	}
	return runCommand(ctx, argv, output)
}
