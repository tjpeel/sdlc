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

func acceptRecommendationsCommand(ctx context.Context, id string, dry bool, timeout time.Duration, automatic *bool, output io.Writer) error {
	action, err := inspectRunAction(ctx, id, false, true)
	if err != nil {
		return err
	}
	waiting := action.View.State == "waiting_for_human"
	var text string
	if waiting {
		text, err = workrun.RecommendationDecisionAnswer(*action.View.Journal)
	} else {
		text, err = workrun.RecommendationFeedback(*action.View.Journal)
	}
	if err != nil {
		return err
	}
	fmt.Fprintln(output, "Recommendations selected for acceptance:")
	for _, line := range strings.Split(text, "\n") {
		fmt.Fprintln(output, dashboard.SafeText(line))
	}
	current, err := inspectRunAction(ctx, action.View.ID, false, true)
	if err != nil {
		return err
	}
	if current.Checkpoint != action.Checkpoint {
		return fmt.Errorf("review checkpoint changed; inspect the latest recommendations before accepting")
	}
	if dry {
		fmt.Fprintln(output, "Preview only: no acceptance saved, files written, remote requests or provider work started.")
		if waiting {
			return printResumePlan(ctx, output, *current.View.Journal, text, false)
		}
		return printPRFeedbackResumePlan(output, *current.View.Journal, false)
	}
	if waiting {
		saved, err := saveInlineAnswer(current, text)
		if err != nil {
			return err
		}
		argv := runActionArguments(current, saved)
		if automatic != nil {
			argv = append(argv, fmt.Sprintf("--auto-accept-recommendations=%t", *automatic))
		}
		if timeout != 0 {
			argv = append(argv, "--timeout", timeout.String())
		}
		return runCommand(ctx, argv, output)
	}
	saved, err := workrun.StoreLocalPRFeedback(current.View.Directory, text)
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "Accepted recommendations saved privately: %s\n", saved)
	argv := append(runActionArguments(current, ""), "--pr-feedback-file", saved)
	if automatic != nil {
		argv = append(argv, fmt.Sprintf("--auto-accept-recommendations=%t", *automatic))
	}
	if timeout != 0 {
		argv = append(argv, "--timeout", timeout.String())
	}
	return runCommand(ctx, argv, output)
}
