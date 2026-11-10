package runtimeimage

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// Remove removes the record and its tag if present and still identifying the
// recorded image. Callers must hold the runtime writer lease.
func (manager Manager) Remove(ctx context.Context, state State) error {
	current, err := manager.RecordedState()
	if err != nil {
		return err
	}
	if current.ImageID != state.ImageID {
		return fmt.Errorf("runtime changed while preparing removal")
	}
	engine, err := manager.engine(ctx)
	if err != nil {
		return err
	}
	if engine != state.Engine {
		return fmt.Errorf("selected Docker engine differs from the recorded runtime; build on this engine explicitly")
	}
	id, inspectErr := manager.inspect(ctx, manager.ImageName())
	missing := false
	if inspectErr != nil {
		// A failed inspect also occurs when Docker is unavailable. Only a
		// successful catalogue query can establish that the tag is absent.
		output, err := manager.Docker.Output(ctx, "image", "ls", "--no-trunc", "--filter", "reference="+manager.ImageName(), "--format", "{{.Repository}}:{{.Tag}}")
		if err != nil {
			return fmt.Errorf("cannot verify missing runtime tag: %w", err)
		}
		for _, tag := range strings.Split(strings.TrimSpace(string(output)), "\n") {
			if tag == manager.ImageName() {
				return inspectErr
			}
		}
		missing = true
	} else if id != state.ImageID {
		return fmt.Errorf("shared image was changed outside the CLI; rebuild to verify and register it")
	}
	if err := manager.EnsureNoDependentContainers(ctx, state.ImageID); err != nil {
		return err
	}
	path, err := manager.statePath()
	if err != nil {
		return err
	}
	if !missing {
		if err := manager.Docker.Run(ctx, "image", "rm", manager.ImageName()); err != nil {
			return fmt.Errorf("remove runtime image tag: %w", err)
		}
	}
	if err := os.Remove(path); err != nil {
		if missing {
			return fmt.Errorf("runtime record could not be removed: %w", err)
		}
		return fmt.Errorf("image tag removed but runtime record could not be removed: %w", err)
	}
	return nil
}
