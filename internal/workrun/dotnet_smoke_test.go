package workrun

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/project"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
)

var dotnetSmokePublicFiles = []string{
	".dockerignore", ".gitignore", "Directory.Build.props", "Dockerfile", "README.md", "Smoke.slnx", "global.json", "compose.yml", "test-environment.example",
	"docs/specification.md", "docs/tickets/01-count-items.md", "scripts/integration.py",
	"src/Smoke.Api/ItemName.cs", "src/Smoke.Api/Program.cs", "src/Smoke.Api/Smoke.Api.csproj",
	"tests/Smoke.IntegrationTests/ApiTests.cs", "tests/Smoke.IntegrationTests/Smoke.IntegrationTests.csproj",
	"tests/Smoke.UnitTests/ItemNameTests.cs", "tests/Smoke.UnitTests/Smoke.UnitTests.csproj",
}

// Read the public allowlist before writing anything. Ignored local material is
// never enumerated, copied, added to the temporary Git index or sent to a worker.
func copyDotnetSmokeFixture(source, destination string) error {
	if err := realDirectory(source); err != nil {
		return err
	}
	if err := realDirectory(destination); err != nil {
		return err
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return fmt.Errorf("fixture destination must be empty")
	}
	type publicFile struct {
		name string
		data []byte
	}
	files := make([]publicFile, 0, len(dotnetSmokePublicFiles))
	const maximumFixtureFile = 1024 * 1024
	for _, relative := range dotnetSmokePublicFiles {
		path := filepath.Join(source, filepath.FromSlash(relative))
		if err := realDirectory(filepath.Dir(path)); err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > maximumFixtureFile {
			return fmt.Errorf("public fixture input must be a bounded regular file: %s", relative)
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		opened, err := file.Stat()
		if err != nil || !os.SameFile(info, opened) {
			file.Close()
			return fmt.Errorf("public fixture input changed while opening: %s", relative)
		}
		data, readErr := io.ReadAll(io.LimitReader(file, maximumFixtureFile+1))
		closeErr := file.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		if len(data) > maximumFixtureFile {
			return fmt.Errorf("public fixture input exceeds 1 MiB: %s", relative)
		}
		files = append(files, publicFile{relative, data})
	}
	for _, file := range files {
		path := filepath.Join(destination, filepath.FromSlash(file.name))
		if err := os.MkdirAll(filepath.Dir(path), 0777); err != nil {
			return err
		}
		if err := os.WriteFile(path, file.data, 0644); err != nil {
			return err
		}
	}
	return nil
}

// Public registry access restores NuGet packages and pulls test images. This
// probe never mounts account caches or invokes a model or GitHub client.
func TestOfflineDotnetSmokeExample(t *testing.T) {
	if os.Getenv("SDLC_OFFLINE_DOTNET_TESTS") != "1" {
		t.Skip("set SDLC_OFFLINE_DOTNET_TESTS=1 for credential-free .NET Docker checks")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	runtime, err := runtimeimage.New(io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	state, err := runtime.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	source, err := filepath.Abs("../../examples/dotnet-smoke")
	if err != nil {
		t.Fatal(err)
	}
	hostRepository := filepath.Join(realTemp(t), "host-repository")
	if err := os.Mkdir(hostRepository, 0700); err != nil {
		t.Fatal(err)
	}
	if err := copyDotnetSmokeFixture(source, hostRepository); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "--initial-branch=main", "--template="}, {"add", "."}, {"-c", "user.name=Example User", "-c", "user.email=example@example.invalid", "commit", "-m", "Create disposable .NET fixture"}} {
		if _, err := isolatedGit(ctx, hostRepository, args...); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := project.Initialize(ctx, hostRepository); err != nil {
		t.Fatal(err)
	}
	config := project.Config{Version: 1, Checks: [][]string{{"dotnet", "build", "Smoke.slnx"}, {"dotnet", "test", "tests/Smoke.UnitTests/Smoke.UnitTests.csproj"}, {"python3", "scripts/integration.py"}}, InputFiles: []string{".env"}}
	settings, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	sourceWrite(t, hostRepository, project.ConfigPath, string(settings))
	id, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	marker := "fake-dotenv-" + id
	environment := "SMOKE_MONGO_CONNECTION_STRING=mongodb://mongo:27017/?serverSelectionTimeoutMS=2000\nSMOKE_COMPOSE_MARKER=" + marker + "\nSMOKE_ENV_FILE_MARKER=" + marker + "\n"
	sourceWrite(t, hostRepository, ".env", environment)
	if _, err := isolatedGit(ctx, hostRepository, "check-ignore", ".env"); err != nil {
		t.Fatal("host .env must be ignored")
	}
	tracked, err := isolatedGit(ctx, hostRepository, "ls-files", "--", ".env")
	if err != nil || tracked != "" {
		t.Fatal("host .env must remain untracked")
	}
	ticket := ".sdlc/work/dotnet-smoke/tickets/01-count-items.md"
	sourceWrite(t, hostRepository, ticket, "# Disposable root environment check\n")
	launch, err := project.Launch(ctx, hostRepository, "dotnet-smoke", "01-count-items.md", nil)
	if err != nil {
		t.Fatal(err)
	}
	directory := realTemp(t)
	workspace := filepath.Join(directory, "workspace")
	plan, err := Capture(ctx, launch, workspace, "work/dotnet-env", "main", "example/project", Roles{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.CheckInputs) != 1 || plan.CheckInputs[0].Path != ".env" {
		t.Fatal("configured .env was not captured as a check input")
	}
	if _, err := os.Lstat(filepath.Join(workspace, ".env")); !os.IsNotExist(err) {
		t.Fatal("host .env reached the provider workspace")
	}
	checkInputs := filepath.Join(directory, "check-inputs")
	captured, err := os.ReadFile(filepath.Join(checkInputs, ".env"))
	if err != nil || string(captured) != environment {
		t.Fatal("captured .env differs from the host input")
	}
	// Execution must use the frozen input, rather than rebinding the live host
	// file. A live bind would now lack the required Compose variables.
	sourceWrite(t, hostRepository, ".env", "DISPOSABLE_CHANGED_AFTER_CAPTURE=true\n")
	// One worker keeps the restore cache while exercising all three gates.
	command := `import hashlib,os,pathlib,subprocess
assert os.environ['DOCKER_HOST']=='unix:///run/sdlc/docker.sock'
assert not pathlib.Path('/provider-auth').exists()
assert not pathlib.Path('/var/run/docker.sock').exists()
assert not pathlib.Path(os.environ['CODEX_HOME']).exists()
assert not pathlib.Path(os.environ['CLAUDE_CONFIG_DIR']).exists()
assert 'SMOKE_COMPOSE_MARKER' not in os.environ
assert 'SMOKE_ENV_FILE_MARKER' not in os.environ
assert hashlib.sha256(pathlib.Path('.env').read_bytes()).hexdigest()==` + fmt.Sprintf("%q", plan.CheckInputs[0].SHA256) + `
subprocess.run(['dotnet','build','Smoke.slnx','--disable-build-servers','-m:1','-p:UseSharedCompilation=false'],check=True)
subprocess.run(['dotnet','test','tests/Smoke.UnitTests/Smoke.UnitTests.csproj','--no-build','--no-restore'],check=True)
subprocess.run(['python3','scripts/integration.py'],check=True)
print('Captured root .env, Compose interpolation/env_file, bridge and localhost integration passed')`
	var output strings.Builder
	checker := DockerChecker{Runtime: runtime, ImageID: state.ImageID, DockerTests: true, InputDirectory: checkInputs}
	if err := checker.Check(ctx, workspace, [][]string{{"python3", "-c", command}}, io.MultiWriter(os.Stdout, &output)); err != nil {
		t.Fatalf(".NET Docker checks: %v", err)
	}
	if !strings.Contains(output.String(), "bridge and localhost integration passed") {
		t.Fatal("missing final fixture evidence")
	}
}

func publicDotnetCopyFixture(t *testing.T) (string, string) {
	t.Helper()
	root := realTemp(t)
	source, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	for _, relative := range dotnetSmokePublicFiles {
		path := filepath.Join(source, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("public fixture: "+relative), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return source, destination
}

func TestDotnetFixtureCopyExcludesPrivateAndGeneratedLocalMaterial(t *testing.T) {
	source, destination := publicDotnetCopyFixture(t)
	for _, relative := range []string{".env", "profiles.local.json", ".secrets/disposable-marker", ".sdlc/work/example/tickets/01-local.md", ".git/config", "src/Smoke.Api/obj/generated.json", "bin/generated.bin"} {
		path := filepath.Join(source, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("excluded disposable data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := copyDotnetSmokeFixture(source, destination); err != nil {
		t.Fatal(err)
	}
	allowed := make(map[string]bool)
	for _, name := range dotnetSmokePublicFiles {
		allowed[filepath.FromSlash(name)] = true
	}
	count := 0
	err := filepath.Walk(destination, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(destination, path)
		if err != nil {
			return err
		}
		if !allowed[relative] {
			return fmt.Errorf("copied local material: %s", relative)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if string(data) != "public fixture: "+filepath.ToSlash(relative) {
			return fmt.Errorf("public fixture bytes changed: %s", relative)
		}
		count++
		return nil
	})
	if err != nil || count != len(dotnetSmokePublicFiles) {
		t.Fatalf("copy boundary: %d public files, error %v", count, err)
	}
}

func TestDotnetFixtureCopyRejectsFileAndAncestorSymlinksBeforeWriting(t *testing.T) {
	for _, kind := range []string{"file", "ancestor", "source"} {
		t.Run(kind, func(t *testing.T) {
			source, destination := publicDotnetCopyFixture(t)
			path := filepath.Join(source, "src", "Smoke.Api", "Program.cs")
			if kind == "ancestor" {
				path = filepath.Join(source, "src")
			}
			if kind == "source" {
				path = source
			}
			if err := os.Rename(path, path+".actual"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(path+".actual", path); err != nil {
				t.Fatal(err)
			}
			if err := copyDotnetSmokeFixture(source, destination); err == nil {
				t.Fatal("symlink fixture input accepted")
			}
			entries, err := os.ReadDir(destination)
			if err != nil || len(entries) != 0 {
				t.Fatalf("invalid input caused a partial copy: %v %v", entries, err)
			}
		})
	}
}

func TestDotnetFixtureCopyMissingPublicInputPreservesDestination(t *testing.T) {
	source, destination := publicDotnetCopyFixture(t)
	if err := os.Remove(filepath.Join(source, filepath.FromSlash(dotnetSmokePublicFiles[len(dotnetSmokePublicFiles)-1]))); err != nil {
		t.Fatal(err)
	}
	if err := copyDotnetSmokeFixture(source, destination); err == nil {
		t.Fatal("missing public input accepted")
	}
	entries, err := os.ReadDir(destination)
	if err != nil || len(entries) != 0 {
		t.Fatalf("missing input caused a partial copy: %v %v", entries, err)
	}
}
