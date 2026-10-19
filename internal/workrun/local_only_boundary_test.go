package workrun

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProductDiffKeepsRunnerReferencesLocal(t *testing.T) {
	for _, patch := range []string{"+run-name: ${{ inputs.sdlc_dispatch_id }}", "+// Created for SDLC automation", "+++ b/tests/SdlcDispatchTests.cs"} {
		if validateLocalOnlyPatch(patch) == nil {
			t.Fatal("runner reference was accepted", patch)
		}
	}
	for _, patch := range []string{"-run-name: ${{ inputs.sdlc_dispatch_id }}", "+name: Check Pull Request", "+// Confirm cancellation publication"} {
		if err := validateLocalOnlyPatch(patch); err != nil {
			t.Fatal("blocked product code or removal", err)
		}
	}
}
func TestProductPublicationBoundaryReadsActualCommittedDiff(t *testing.T) {
	root := t.TempDir()
	repo := feedbackGitRepository{t: t}
	repo.git(root, "init")
	repo.git(root, "config", "user.name", "Example Developer")
	repo.git(root, "config", "user.email", "developer@example.invalid")
	file := filepath.Join(root, "workflow.yml")
	if err := os.WriteFile(file, []byte("name: Product checks\n"), 0600); err != nil {
		t.Fatal(err)
	}
	repo.git(root, "add", ".")
	repo.git(root, "commit", "-m", "Product baseline")
	base := repo.git(root, "rev-parse", "HEAD")
	if err := os.WriteFile(file, []byte("name: Product checks\nrun-name: ${{ inputs.sdlc_dispatch_id }}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	repo.git(root, "add", ".")
	repo.git(root, "commit", "-m", "Add runner coupling")
	if err := validateLocalOnlySource(context.Background(), Plan{Repository: "example/notifications", SourceSHA: base}, root); err == nil || !strings.Contains(err.Error(), "keep runner integration local") {
		t.Fatal("committed runner footprint could be published", err)
	}
	if err := validateLocalOnlySource(context.Background(), Plan{Repository: "example/sdlc", SourceSHA: base}, root); err != nil {
		t.Fatal("blocked the controller's own product development", err)
	}
}
