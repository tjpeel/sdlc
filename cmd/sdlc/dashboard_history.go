package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tjpeel/sdlc/internal/dashboard"
	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
)

const dashboardForgetUsage = "Usage: sdlc dashboard forget --run RUN_ID\nRemoves one stopped run from the dashboard. Journals, logs, inputs, workspace and locks are retained.\nResuming the run registers it again. Active controllers cannot be forgotten."

const dashboardExportUsage = "Usage: sdlc dashboard export --run RUN_ID --to PRIVATE_DIRECTORY\nWrites a private checkpoint report into an existing owned 0700 directory.\nReports exclude raw logs, transcripts, authentication settings, input contents and source code.\nKeep reports private: check commands and model summaries can still contain sensitive material.\nThis is a point-in-time summary, not a full resumable backup. Existing reports are never overwritten."

func dashboardExportCommand(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("dashboard export", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	id := flags.String("run", "", "selected run ID or unique prefix")
	destination := flags.String("to", "", "existing private report directory")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		_, err = fmt.Fprintln(output, dashboardExportUsage)
		return err
	} else if err != nil {
		return err
	}
	if *id == "" || *destination == "" || flags.NArg() != 0 {
		return fmt.Errorf("export requires --run RUN_ID and --to PRIVATE_DIRECTORY")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	absolute, err := filepath.Abs(*destination)
	if err != nil {
		return err
	}
	runtime, err := runtimeimage.New(io.Discard, io.Discard)
	if err != nil {
		return err
	}
	views, err := runstatus.New(runtime.Directory).List(time.Now().UTC())
	if err != nil {
		return err
	}
	v, err := dashboard.Select(views, *id)
	if err != nil {
		return err
	}
	path, err := runstatus.ExportReport(v, absolute)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "Private checkpoint report saved at %q.\nRaw run artifacts remain at %q; this summary cannot resume the run.\n", dashboard.SafeText(path), dashboard.SafeText(v.Directory))
	return err
}

func dashboardForgetCommand(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("dashboard forget", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	id := flags.String("run", "", "stopped run ID or unique prefix")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		_, err = fmt.Fprintln(output, dashboardForgetUsage)
		return err
	} else if err != nil {
		return err
	}
	if *id == "" || flags.NArg() != 0 {
		return fmt.Errorf("forget requires --run RUN_ID")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	runtime, err := runtimeimage.New(io.Discard, io.Discard)
	if err != nil {
		return err
	}
	registry := runstatus.New(runtime.Directory)
	views, err := registry.List(time.Now().UTC())
	if err != nil {
		return err
	}
	v, err := dashboard.Select(views, *id)
	if err != nil {
		return err
	}
	if err := registry.Forget(v.ID); err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "Removed run %s from the dashboard. Saved work remains at %q.\nResume registers the run again; no run artifacts or credentials were removed.\n", v.ID, dashboard.SafeText(v.Directory))
	return err
}

func dashboardPageAction(page int, command string) (int, bool) {
	switch strings.TrimSpace(command) {
	case "n":
		return page + 1, false
	case "p":
		if page > 1 {
			return page - 1, false
		}
	case "q":
		return page, true
	}
	return page, false
}

// Own a separate descriptor so closing the dashboard cancels its input reader
// without closing the host process's standard input. Canonical mode is kept:
// commands require Enter and no terminal settings need restoring.
func openDashboardInput() (*os.File, error) {
	info, err := os.Stdin.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return nil, fmt.Errorf("dashboard paging requires terminal input")
	}
	return os.Open("/dev/tty")
}

func readDashboardInput(ctx context.Context, input io.Reader) <-chan string {
	commands := make(chan string)
	go func() {
		defer close(commands)
		scanner := bufio.NewScanner(input)
		scanner.Buffer(make([]byte, 128), 1024)
		for scanner.Scan() {
			command := strings.TrimSpace(scanner.Text())
			if command != "n" && command != "p" && command != "q" {
				continue
			}
			select {
			case commands <- command:
			case <-ctx.Done():
				return
			}
		}
	}()
	return commands
}
