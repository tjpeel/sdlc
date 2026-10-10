package runtimeimage

import (
	"archive/tar"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

type namedDocker struct {
	*fakeDocker
	tags map[string]string
}

func (d *namedDocker) Output(ctx context.Context, args ...string) ([]byte, error) {
	if len(args) > 2 && args[0] == "image" && args[1] == "inspect" {
		name := args[len(args)-1]
		if name != Image && !strings.HasPrefix(name, "sdlc:build-") {
			d.calls = append(d.calls, append([]string(nil), args...))
			if id := d.tags[name]; id != "" {
				return []byte(id), nil
			}
			return nil, errors.New("image missing")
		}
	}
	return d.fakeDocker.Output(ctx, args...)
}
func (d *namedDocker) Run(ctx context.Context, args ...string) error {
	if len(args) == 4 && args[0] == "image" && args[1] == "tag" && args[3] != Image {
		d.calls = append(d.calls, append([]string(nil), args...))
		d.tags[args[3]] = args[2]
		return nil
	}
	return d.fakeDocker.Run(ctx, args...)
}
func skillGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture git %v: %v %s", args, err, data)
	} else {
		return strings.TrimSpace(string(data))
	}
	return ""
}
func skillsFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	skillGit(t, root, "init", "--initial-branch=main")
	sourceFile(t, root, "scripts/install-skills", "#!/bin/sh\nexit 0\n")
	sourceFile(t, root, "skills/example/SKILL.md", "Public synthetic catalogue")
	sourceFile(t, root, ".gitignore", ".secrets/\n")
	skillGit(t, root, "add", ".")
	skillGit(t, root, "-c", "user.name=Example", "-c", "user.email=example@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "Add synthetic catalogue")
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return canonical, skillGit(t, root, "rev-parse", "HEAD")
}
func TestNamedRuntimeDoesNotReplaceDefaultStateOrImage(t *testing.T) {
	manager, base, source, pins, recipe := pinsFixture(t)
	recordPrevious(t, manager, base, source, nil)
	defaultState, err := os.ReadFile(filepath.Join(manager.Directory, "runtime.json"))
	if err != nil {
		t.Fatal(err)
	}
	skills, revision := skillsFixture(t)
	sourceFile(t, skills, ".secrets/ignored-token", "PRIVATE-IGNORED-SENTINEL")
	sourceFile(t, source, "runtime/Dockerfile", string(recipe)+"ARG LOCAL_SKILLS=0\n# /tmp/sdlc-local-catalogues/skills.tar\n")
	for i := range base.inventoryOverride.Dependencies {
		if base.inventoryOverride.Dependencies[i].Source == "tjpeel/skills" {
			base.inventoryOverride.Dependencies[i].Version = revision
		}
	}
	docker := &namedDocker{fakeDocker: base, tags: map[string]string{}}
	named := manager
	named.Name = "skills-test"
	named.Docker = docker
	archiveChecked := false
	base.buildHook = func() {
		file, err := os.Open(filepath.Join(base.context, "local-catalogues", "skills.tar"))
		if err != nil {
			t.Error(err)
			return
		}
		defer file.Close()
		archive := tar.NewReader(file)
		for {
			header, err := archive.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Error(err)
				return
			}
			if strings.Contains(header.Name, ".secrets") || strings.Contains(header.Name, ".git/") {
				t.Error("private checkout state entered archive")
			}
		}
		archiveChecked = true
	}
	state, err := named.BuildWithOptions(context.Background(), source, BuildOptions{SkillsSource: skills, Pins: &pins})
	if err != nil {
		t.Fatal(err)
	}
	if !archiveChecked || state.Name != "skills-test" || state.SkillsRevision != revision || state.SkillsSource != skills || !strings.Contains(state.BuildRecipe, "ARG LOCAL_SKILLS=1") || !strings.Contains(state.BuildRecipe, "ARG SKILLS_REVISION="+revision) {
		t.Fatalf("local provenance missing: %+v", state)
	}
	after, err := os.ReadFile(filepath.Join(manager.Directory, "runtime.json"))
	if err != nil || string(after) != string(defaultState) || base.current != oldImage {
		t.Fatal("default runtime changed")
	}
	if docker.tags[named.ImageName()] != newImage {
		t.Fatal("named image not selected")
	}
	if _, err := os.Stat(filepath.Join(manager.Directory, "runtime.skills-test.json")); err != nil {
		t.Fatal("named state missing")
	}
	if _, err := named.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, removed := range base.removed {
		if removed == oldImage || removed == Image {
			t.Fatal("default image cleanup attempted")
		}
	}
	if _, err := named.BuildWithOptions(context.Background(), source, BuildOptions{}); err == nil {
		t.Fatal("local catalogue silently switched to remote")
	}
}
func TestInvalidNamesAndLocalSourceFailuresPrecedeDocker(t *testing.T) {
	for _, name := range []string{"local", "build-example", "../other", "Upper", "name/child", "-start", strings.Repeat("a", 49)} {
		if _, err := NewNamed(io.Discard, io.Discard, name); err == nil {
			t.Fatalf("invalid name accepted %q", name)
		}
	}
	t.Setenv("SDLC_STATE_DIR", t.TempDir())
	normal, err := New(io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	named, err := NewNamed(io.Discard, io.Discard, "skills-test")
	if err != nil || normal.Directory != named.Directory || normal.ImageName() != Image {
		t.Fatal("named runtime moved account storage")
	}
	for _, kind := range []string{"dirty", "subdirectory", "no-installer", "default"} {
		t.Run(kind, func(t *testing.T) {
			manager, docker, source := fixture(t)
			manager.Name = "skills-test"
			skills, _ := skillsFixture(t)
			switch kind {
			case "dirty":
				sourceFile(t, skills, "skills/example/SKILL.md", "changed")
			case "subdirectory":
				skills = filepath.Join(skills, "skills")
			case "no-installer":
				skillGit(t, skills, "rm", "scripts/install-skills")
				skillGit(t, skills, "-c", "user.name=Example", "-c", "user.email=example@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "Remove installer")
			case "default":
				manager.Name = ""
			}
			if _, err := manager.BuildWithOptions(context.Background(), source, BuildOptions{SkillsSource: skills}); err == nil {
				t.Fatal("unsafe local source accepted")
			}
			if len(docker.calls) != 0 {
				t.Fatal("Docker contacted before local source validation")
			}
		})
	}
}

func TestCommittedSymlinkAndAuthenticationPathsRejectedBeforeDocker(t *testing.T) {
	for _, kind := range []string{"symlink", "authentication"} {
		t.Run(kind, func(t *testing.T) {
			manager, docker, source := fixture(t)
			manager.Name = "skills-test"
			skills, _ := skillsFixture(t)
			if kind == "symlink" {
				if err := os.Symlink("../../outside-private", filepath.Join(skills, "skills", "linked")); err != nil {
					t.Fatal(err)
				}
				skillGit(t, skills, "add", "skills/linked")
			} else {
				sourceFile(t, skills, "credentials/example.json", "synthetic credential placeholder")
				skillGit(t, skills, "add", "credentials/example.json")
			}
			skillGit(t, skills, "-c", "user.name=Example", "-c", "user.email=example@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "Add unsafe synthetic entry")
			if _, err := manager.BuildWithOptions(context.Background(), source, BuildOptions{SkillsSource: skills}); err == nil {
				t.Fatal("unsafe catalogue archive accepted")
			}
			if len(docker.calls) != 0 {
				t.Fatal("unsafe archive contacted Docker")
			}
		})
	}
}

func TestLocalInventoryMismatchDoesNotSelectCandidate(t *testing.T) {
	manager, base, source, pins, recipe := pinsFixture(t)
	before := recordPrevious(t, manager, base, source, nil)
	skills, _ := skillsFixture(t)
	sourceFile(t, source, "runtime/Dockerfile", string(recipe)+"ARG LOCAL_SKILLS=0\n# /tmp/sdlc-local-catalogues/skills.tar\n")
	named := manager
	named.Name = "skills-test"
	docker := &namedDocker{fakeDocker: base, tags: map[string]string{}}
	named.Docker = docker
	if _, err := named.BuildWithOptions(context.Background(), source, BuildOptions{SkillsSource: skills, Pins: &pins}); err == nil {
		t.Fatal("incorrect catalogue inventory selected")
	}
	after, err := os.ReadFile(filepath.Join(manager.Directory, "runtime.json"))
	if err != nil || string(after) != string(before) || base.current != oldImage || docker.tags[named.ImageName()] != "" {
		t.Fatal("inventory failure changed runtime selection")
	}
	if _, err := os.Stat(filepath.Join(manager.Directory, "runtime.skills-test.json")); !os.IsNotExist(err) {
		t.Fatal("unverified named runtime state saved")
	}
}

func TestLocalCatalogueArchiveBounds(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "archive")
	if err != nil {
		t.Fatal(err)
	}
	writer := &boundedArchive{file: file, bytes: maximumSkillsArchive}
	if n, err := writer.Write([]byte("x")); n != 0 || err == nil {
		t.Fatal("archive overflow reached filesystem")
	}
	archive := tar.NewWriter(file)
	if err := archive.WriteHeader(&tar.Header{Name: "oversized", Mode: 0600, Size: maximumBuildFile + 1, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validateSkillsArchive(file.Name()); err == nil || !strings.Contains(err.Error(), "2 MiB") {
		t.Fatal("oversized catalogue entry accepted")
	}
}

func TestCommittedInternalReferenceLinksMaterializeOnlyArchivedBytes(t *testing.T) {
	skills, _ := skillsFixture(t)
	sourceFile(t, skills, "shared/ownership-and-delegation.md", "Committed shared ownership guidance")
	if err := os.MkdirAll(filepath.Join(skills, "skills", "example", "references"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../../shared/ownership-and-delegation.md", filepath.Join(skills, "skills", "example", "references", "ownership-and-delegation.md")); err != nil {
		t.Fatal(err)
	}
	skillGit(t, skills, "add", "shared", "skills")
	skillGit(t, skills, "-c", "user.name=Example", "-c", "user.email=example@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "Add shared synthetic reference")
	contextDirectory := t.TempDir()
	if err := os.Mkdir(filepath.Join(contextDirectory, "local-catalogues"), 0700); err != nil {
		t.Fatal(err)
	}
	_, revision, err := snapshotSkills(context.Background(), skills, contextDirectory)
	if err != nil || revision != skillGit(t, skills, "rev-parse", "HEAD") {
		t.Fatal("committed internal references rejected", err)
	}
	file, err := os.Open(filepath.Join(contextDirectory, "local-catalogues", "skills.tar"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader := tar.NewReader(file)
	found := false
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeSymlink {
			t.Fatal("symlink remained in build archive")
		}
		if header.Name == "skills/example/references/ownership-and-delegation.md" {
			data, err := io.ReadAll(reader)
			if err != nil || header.Typeflag != tar.TypeReg || string(data) != "Committed shared ownership guidance" {
				t.Fatal("reference did not use committed regular bytes")
			}
			found = true
		}
	}
	if !found {
		t.Fatal("materialized reference missing")
	}
}

func TestCatalogueLinksRejectCyclesMissingTargetsAndParentLinks(t *testing.T) {
	for _, kind := range []string{"cycle", "missing", "parent-link", "authentication-target"} {
		t.Run(kind, func(t *testing.T) {
			skills, _ := skillsFixture(t)
			sourceFile(t, skills, "shared/regular.md", "shared")
			var links map[string]string
			switch kind {
			case "cycle":
				links = map[string]string{"skills/first": "second", "skills/second": "first"}
			case "missing":
				links = map[string]string{"skills/first": "../ignored/private.md"}
				sourceFile(t, skills, ".gitignore", ".secrets/\nignored/\n")
				sourceFile(t, skills, "ignored/private.md", "PRIVATE-UNTRACKED-SENTINEL")
			case "parent-link":
				links = map[string]string{"skills/directory": "../shared", "skills/first": "directory/regular.md"}
			case "authentication-target":
				links = map[string]string{"skills/first": "../.secrets/private.md"}
				sourceFile(t, skills, ".secrets/private.md", "PRIVATE-IGNORED-SENTINEL")
			}
			for name, target := range links {
				if err := os.Symlink(target, filepath.Join(skills, name)); err != nil {
					t.Fatal(err)
				}
			}
			skillGit(t, skills, "add", ".")
			skillGit(t, skills, "-c", "user.name=Example", "-c", "user.email=example@example.invalid", "-c", "commit.gpgsign=false", "commit", "-m", "Add unsafe synthetic reference")
			contextDirectory := t.TempDir()
			if err := os.Mkdir(filepath.Join(contextDirectory, "local-catalogues"), 0700); err != nil {
				t.Fatal(err)
			}
			if _, _, err := snapshotSkills(context.Background(), skills, contextDirectory); err == nil {
				t.Fatal("unsafe committed reference accepted")
			}
		})
	}
}

func TestLocalSkillsSourceBuildPreservesBlankOptionalDockerfileDefaults(t *testing.T) {
	manager, base, source := fixture(t)
	recipe, err := os.ReadFile(filepath.Join("..", "..", "runtime", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	recipe = regexp.MustCompile(`(?m)^ARG (NPM_VERSION|YARN_VERSION|COMPOSE_VERSION|BUILDX_VERSION)=.*$`).ReplaceAll(recipe, []byte("ARG ${1}="))
	sourceFile(t, source, "runtime/Dockerfile", string(recipe))
	skills, revision := skillsFixture(t)
	inventory := testInventory()
	for i := range inventory.Dependencies {
		if inventory.Dependencies[i].Source == "tjpeel/skills" {
			inventory.Dependencies[i].Version = revision
		}
	}
	base.inventoryOverride = &inventory
	manager.Name = "skills-test"
	docker := &namedDocker{fakeDocker: base, tags: map[string]string{}}
	manager.Docker = docker
	state, err := manager.BuildWithOptions(context.Background(), source, BuildOptions{SkillsSource: skills})
	if err != nil {
		t.Fatal(err)
	}
	if state.DependencyPins != nil {
		t.Fatal("source defaults promoted to full dependency overrides")
	}
	for _, arg := range []string{"NPM_VERSION", "YARN_VERSION", "COMPOSE_VERSION", "BUILDX_VERSION"} {
		blank := "ARG " + arg + "=\n"
		if !strings.Contains(string(recipe), blank) || !strings.Contains(state.BuildRecipe, blank) {
			t.Fatalf("optional default changed: %s", arg)
		}
	}
	expected, err := localSkillsRecipe(recipe, revision)
	if err != nil {
		t.Fatal(err)
	}
	if state.BuildRecipe != string(expected) || string(base.buildRecipe) != string(expected) || state.SkillsRevision != revision || docker.tags[manager.ImageName()] != newImage {
		t.Fatal("actual local recipe or provenance differs")
	}
}
