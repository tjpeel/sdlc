package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/tjpeel/sdlc/internal/headroom"
	"github.com/tjpeel/sdlc/internal/providerauth"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
)

func headroomCommand(ctx context.Context, args []string, output, diagnostics io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: sdlc runtime headroom set --proxy-url URL | status | build")
	}
	manager, err := runtimeimage.New(io.Discard, io.Discard)
	if err != nil {
		return err
	}
	var config headroom.Config
	switch args[0] {
	case "set":
		flags := flag.NewFlagSet("runtime headroom set", flag.ContinueOnError)
		flags.SetOutput(diagnostics)
		address := flags.String("proxy-url", "", "Headroom proxy origin reachable from local Docker workers (omit /dashboard)")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *address == "" {
			return fmt.Errorf("usage: sdlc runtime headroom set --proxy-url URL")
		}
		config, err = headroom.Save(manager.Directory, *address)
	case "status":
		if len(args) != 1 {
			return fmt.Errorf("usage: sdlc runtime headroom status")
		}
		config, err = headroom.Load(manager.Directory)
	case "build":
		if len(args) != 1 {
			return fmt.Errorf("usage: sdlc runtime headroom build")
		}
		config, err = headroom.Build(ctx, providerauth.LocalDocker{})
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(output, "Legacy Headroom image: %s\nImage ID: %s\nPolicy: %s\nNew runs use the saved external proxy.\n", headroom.Image, config.ImageID, config.PolicyVersion)
		return err
	default:
		return fmt.Errorf("usage: sdlc runtime headroom set --proxy-url URL | status | build")
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "Headroom proxy: %s\nWorker proxy: %s\nRequired for all new ticket and feature runs. Existing runs retain their saved route.\nConnectivity and proxy policy have not been checked.\n", config.ProxyURL, config.WorkerURL())
	return err
}

func selectRunHeadroom(directory string, options runOptions) (headroom.Config, error) {
	if options.frozenHeadroom != nil {
		return *options.frozenHeadroom, options.frozenHeadroom.Validate()
	}
	if options.supplied["headroom"] {
		return headroom.Config{}, fmt.Errorf("new runs use the required saved Headroom proxy; configure sdlc runtime headroom set --proxy-url URL instead of --headroom")
	}
	return headroom.Load(directory)
}
