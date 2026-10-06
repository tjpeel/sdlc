// Package shell is an opt-in presentation adapter. Account, run and filesystem
// operations belong to the existing CLI callbacks, never to this package.
package shell

import (
	"context"
	"io"
	"os"
	"os/exec"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"
)

type Flag struct {
	Name    string `json:"name"`
	Summary string `json:"summary"`
}
type Command struct {
	Name    string `json:"name"`
	Group   string `json:"group"`
	Summary string `json:"summary"`
	Usage   string `json:"usage"`
	Flags   []Flag `json:"flags,omitempty"`
	Native  bool   `json:"native"`
}
type Suggestion struct{ Label, Insert, Description string }
type RunAction struct {
	ID, Root, Reference, Ticket, State, Checkpoint string
	Questions                                      []string
}

type Config struct {
	ResolveRun            func(context.Context, string, string) (RunAction, error)
	RespondRun            func(context.Context, RunAction, string) (string, error)
	ResumeRun             func(context.Context, RunAction) (string, error)
	Root, Version, Branch string
	Dirty                 bool
	Commands              []Command
	Input                 io.Reader
	Output                io.Writer
	Complete              func(context.Context, string, string) ([]Suggestion, error)
	Execute               func(context.Context, string, []string) (*exec.Cmd, error)
	Read                  func(context.Context, string, []string) (string, error)
	Launch                func(context.Context, string, []string) (string, error)
	SelectProject         func(context.Context, string, string) (string, error)
	Prompt                func(context.Context, string) (string, bool)
}

func IsTerminal(file *os.File) bool { return file != nil && term.IsTerminal(int(file.Fd())) }
func Run(ctx context.Context, c Config) error {
	options := []tea.ProgramOption{tea.WithAltScreen(), tea.WithContext(ctx)}
	if c.Input != nil {
		options = append(options, tea.WithInput(c.Input))
	}
	if c.Output != nil {
		options = append(options, tea.WithOutput(c.Output))
	}
	_, err := tea.NewProgram(NewModel(ctx, c), options...).Run()
	return err
}
