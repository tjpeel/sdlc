package runtimeimage

import (
	"context"
	"errors"
	"os"
	"testing"
)

type removalDocker struct {
	*fakeDocker
	removeErr  error
	removedTag string
}

func (d *removalDocker) Run(ctx context.Context, args ...string) error {
	if len(args) == 3 && args[0] == "image" && args[1] == "rm" {
		d.removedTag = args[2]
		return d.removeErr
	}
	return d.fakeDocker.Run(ctx, args...)
}
func TestRemoveRuntimeChecksIdentityAndContainers(t *testing.T) {
	for _, scenario := range []string{"removed", "active", "changed", "docker error"} {
		t.Run(scenario, func(t *testing.T) {
			manager, fake, _ := fixture(t)
			manager.Name = "updated"
			docker := &removalDocker{fakeDocker: fake}
			manager.Docker = docker
			state := State{Version: 1, Name: "updated", Engine: "test-engine", ImageID: newImage}
			if err := manager.save(state); err != nil {
				t.Fatal(err)
			}
			fake.current = newImage
			switch scenario {
			case "active":
				fake.active = true
			case "changed":
				fake.current = oldImage
			case "docker error":
				docker.removeErr = errors.New("failure")
			}
			err := manager.Remove(context.Background(), state)
			if (err == nil) != (scenario == "removed") {
				t.Fatalf("unexpected removal result: %v", err)
			}
			path, _ := manager.statePath()
			_, statErr := os.Stat(path)
			if scenario == "removed" {
				if !os.IsNotExist(statErr) || docker.removedTag != "sdlc:updated" {
					t.Fatal("runtime not removed")
				}
			} else if statErr != nil {
				t.Fatal("failed removal lost record")
			}
			if (scenario == "active" || scenario == "changed") && docker.removedTag != "" {
				t.Fatal("unsafe tag removal")
			}
		})
	}
}
