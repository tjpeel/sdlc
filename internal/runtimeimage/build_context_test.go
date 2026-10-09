package runtimeimage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sourceFile(t *testing.T, root, name, body string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestBuildContextContainsOnlyRuntimeAndPublisherClosure(t *testing.T) {
	_, _, root := fixture(t)
	sourceFile(t, root, "cmd/sdlc-publisher/main.go", `package main
import "github.com/tjpeel/sdlc/internal/instructions"
func main() { instructions.F() }
`)
	sourceFile(t, root, "internal/instructions/instructions.go", `package instructions
import _ "embed"
//go:embed default.md
var text string
func F() {}
`)
	sourceFile(t, root, "internal/instructions/default.md", "Public instructions")
	for _, name := range []string{"profiles.local.json", ".secrets/token", ".git/config", "logs/session.jsonl", "runtime/.env", "runtime/private.py", "runtime/bin/private", "cmd/sdlc-publisher/main_test.go", "internal/instructions/private.json", "internal/unused/secret.go"} {
		sourceFile(t, root, name, "PRIVATE-SENTINEL")
	}
	directory, cleanup, err := buildContext(root)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if strings.HasPrefix(directory, root+string(filepath.Separator)) {
		t.Fatal("context is inside checkout")
	}
	count := 0
	err = filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		count++
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.Contains(string(data), "PRIVATE-SENTINEL") {
			t.Errorf("private sentinel entered build context: %s", entry.Name())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != len(runtimeAssets)+6 {
		t.Fatal("unexpected context files", count)
	}
	cleanup()
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatal("context cleanup failed")
	}
}

func TestBuildContextRejectsUnsafeOrIncompleteSource(t *testing.T) {
	for _, cause := range []string{"runtime symlink", "source symlink", "package symlink", "embed symlink", "large", "special", "too much data", "missing publisher", "unsupported embed", "external module"} {
		t.Run(cause, func(t *testing.T) {
			_, _, root := fixture(t)
			tempBase := t.TempDir()
			t.Setenv("TMPDIR", tempBase)
			sourceFile(t, root, "internal/instructions/instructions.go", "package instructions\nimport _ \"embed\"\n//go:embed default.md\nvar data string\n")
			sourceFile(t, root, "internal/instructions/default.md", "public fixture")
			sourceFile(t, root, "cmd/sdlc-publisher/main.go", "package main\nimport _ \"github.com/tjpeel/sdlc/internal/instructions\"\nfunc main() {}\n")
			target := ""
			switch cause {
			case "runtime symlink":
				target = "runtime/entrypoint.py"
			case "source symlink":
				target = "cmd/sdlc-publisher/main.go"
			case "package symlink":
				target = "internal/instructions"
			case "embed symlink":
				target = "internal/instructions/default.md"
			case "large":
				sourceFile(t, root, "runtime/entrypoint.py", strings.Repeat("x", maximumBuildFile+1))
			case "special":
				os.Remove(filepath.Join(root, "runtime/entrypoint.py"))
				os.Mkdir(filepath.Join(root, "runtime/entrypoint.py"), 0700)
			case "too much data":
				for _, asset := range runtimeAssets {
					sourceFile(t, root, filepath.Join("runtime", asset), strings.Repeat("x", maximumBuildFile))
				}
			case "missing publisher":
				os.RemoveAll(filepath.Join(root, "cmd/sdlc-publisher"))
			case "unsupported embed":
				sourceFile(t, root, "internal/instructions/instructions.go", "package instructions\nimport _ \"embed\"\n//go:embed ../../profiles.local.json\nvar data string\n")
			case "external module":
				sourceFile(t, root, "cmd/sdlc-publisher/main.go", "package main\nimport _ \"example.invalid/private\"\n")
			}
			if target != "" {
				path := filepath.Join(root, target)
				if err := os.RemoveAll(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), path); err != nil {
					t.Fatal(err)
				}
			}
			directory, cleanup, err := buildContext(root)
			if err == nil {
				cleanup()
				t.Fatal("unsafe build context accepted", directory)
			}
			entries, err := os.ReadDir(tempBase)
			if err != nil || len(entries) != 0 {
				t.Fatal("failed context leaked temporary sources", err)
			}
		})
	}
}
