package main

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/tjpeel/sdlc/internal/shell"
)

func (a *shellAdapter) resolveRun(ctx context.Context, _ string, id string) (shell.RunAction, error) {
	action, err := inspectRunAction(ctx, id)
	if err != nil {
		return shell.RunAction{}, err
	}
	v := action.View
	var questions []string
	if v.State == "waiting_for_human" {
		questions = append([]string(nil), v.Questions...)
	}
	return shell.RunAction{ID: v.ID, Root: v.Root, Reference: v.Reference, Ticket: filepath.Base(v.Ticket), State: v.State, Checkpoint: action.Checkpoint, Questions: questions}, nil
}

func currentShellAction(ctx context.Context, selected shell.RunAction, answer bool) (runAction, error) {
	action, err := resolveRunAction(ctx, selected.ID, answer)
	if err != nil {
		return runAction{}, err
	}
	v := action.View
	if action.Checkpoint != selected.Checkpoint || v.Root != selected.Root || v.Reference != selected.Reference || filepath.Base(v.Ticket) != selected.Ticket || v.State != selected.State {
		return runAction{}, fmt.Errorf("run changed while you were responding; inspect it again before submitting")
	}
	return action, nil
}

func (a *shellAdapter) prepareRunResponse(ctx context.Context, selected shell.RunAction, text *string) ([]string, error) {
	action, err := currentShellAction(ctx, selected, text != nil)
	if err != nil {
		return nil, err
	}
	path := ""
	if text != nil {
		path, err = saveInlineAnswer(action, *text)
		if err != nil {
			return nil, err
		}
	}
	args := append([]string{"run"}, runActionArguments(action, path)...)
	if _, err := a.read(ctx, action.View.Root, args); err != nil {
		return nil, err
	}
	return args, nil
}

func (a *shellAdapter) respondRun(ctx context.Context, action shell.RunAction, text string) (string, error) {
	args, err := a.prepareRunResponse(ctx, action, &text)
	if err != nil {
		return "", err
	}
	return a.launch(ctx, action.Root, args)
}

func (a *shellAdapter) resumeRun(ctx context.Context, action shell.RunAction) (string, error) {
	args, err := a.prepareRunResponse(ctx, action, nil)
	if err != nil {
		return "", err
	}
	return a.launch(ctx, action.Root, args)
}
