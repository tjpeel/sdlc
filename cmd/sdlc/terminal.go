package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"

	"github.com/tjpeel/sdlc/internal/terminallaunch"
)

func terminalCommand(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 || (args[0] != "setup" && args[0] != "status") {
		return fmt.Errorf("usage: sdlc terminal setup|status [--json]")
	}
	asJSON := false
	for _, arg := range args[1:] {
		if arg != "--json" {
			return fmt.Errorf("unknown terminal argument")
		}
		asJSON = true
	}
	dir, err := shellStateDirectory()
	if err != nil {
		return err
	}
	var status terminallaunch.TerminalReadiness
	if args[0] == "setup" {
		status, err = terminallaunch.Setup(ctx, dir)
	} else {
		status, err = terminallaunch.TerminalStatus(filepath.Join(dir, "terminal"))
	}
	if err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(out).Encode(status)
	}
	_, err = fmt.Fprintf(out, "Background terminal ready: %t\n%s\n", status.Ready, status.Message)
	return err
}
