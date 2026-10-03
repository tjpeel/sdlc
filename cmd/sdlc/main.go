package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/tjpeel/sdlc/internal/buildinfo"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
)

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Println(buildinfo.String())
		return
	}
	if len(os.Args) == 1 || (len(os.Args) == 2 && (os.Args[1] == "--help" || os.Args[1] == "help")) {
		fmt.Println("Usage: sdlc --version | runtime build [--source SDLC_DIRECTORY] | runtime status")
		return
	}
	if len(os.Args) >= 3 && os.Args[1] == "runtime" {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()
		manager, err := runtimeimage.New(os.Stdout, os.Stderr)
		var state runtimeimage.State
		if err == nil {
			switch os.Args[2] {
			case "build":
				flags := flag.NewFlagSet("runtime build", flag.ContinueOnError)
				source := flags.String("source", "", "SDLC clone (uses saved source when omitted)")
				err = flags.Parse(os.Args[3:])
				if err == nil && flags.NArg() != 0 {
					err = fmt.Errorf("runtime build accepts only --source")
				}
				if err == nil {
					state, err = manager.Build(ctx, *source)
				}
			case "status":
				if len(os.Args) != 3 {
					err = fmt.Errorf("runtime status accepts no arguments")
				} else {
					state, err = manager.Status(ctx)
				}
			default:
				err = fmt.Errorf("unknown runtime command; run sdlc --help")
			}
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "sdlc:", err)
			os.Exit(1)
		}
		fmt.Printf("Shared image: %s\nImage ID: %s\nSource revision: %s\n%s\n", runtimeimage.Image, state.ImageID, state.Revision, state.Tools)
		return
	}
	fmt.Fprintln(os.Stderr, "sdlc: unknown command; run sdlc --help")
	os.Exit(2)
}
