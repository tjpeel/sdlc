package runtimeimage

import (
	"context"
	"fmt"
	"os"
)

// Remove removes the selected tag and its state only when it still identifies
// the recorded image. Callers must hold the runtime writer lease.
func (manager Manager) Remove(ctx context.Context, state State) error {
	current, err := manager.Status(ctx)
	if err != nil {
		return err
	}
	if current.ImageID != state.ImageID {
		return fmt.Errorf("runtime changed while preparing removal")
	}
	if err := manager.EnsureNoDependentContainers(ctx, state.ImageID); err != nil {
		return err
	}
	path, err := manager.statePath()
	if err != nil {
		return err
	}
	if err := manager.Docker.Run(ctx, "image", "rm", manager.ImageName()); err != nil {
		return fmt.Errorf("remove runtime image tag: %w", err)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("image tag removed but runtime record could not be removed: %w", err)
	}
	return nil
}
