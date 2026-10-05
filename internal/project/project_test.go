package project

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func run(t *testing.T, root string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = root
	c.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	b, e := c.CombinedOutput()
	if e != nil {
		t.Fatalf("git %v: %s", args, b)
	}
	return string(b)
}
func repo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	run(t, root, "init", "-b", "main")
	run(t, root, "config", "user.name", "Example")
	run(t, root, "config", "user.email", "example@example.invalid")
	write(t, root, "README.md", "example\n")
	run(t, root, "add", "README.md")
	run(t, root, "commit", "-m", "Initial example")
	return root
}
func write(t *testing.T, root, path, data string) {
	t.Helper()
	dest := filepath.Join(root, path)
	if e := os.MkdirAll(filepath.Dir(dest), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(dest, []byte(data), 0600); e != nil {
		t.Fatal(e)
	}
}
func initAt(t *testing.T, root string) Result {
	t.Helper()
	r, e := Initialize(context.Background(), root)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func TestInitializeNestedAndRepeat(t *testing.T) {
	root := repo(t)
	write(t, root, "go.mod", "module example.invalid/project\n\ngo 1.24\n")
	write(t, root, "nested/.keep", "")
	t.Setenv("GIT_DIR", "/missing")
	t.Setenv("GIT_INDEX_FILE", "/missing")
	r := initAt(t, filepath.Join(root, "nested"))
	if r.Branch != "main" || r.Head == "" || !r.Dirty || !r.ConfigCreated || len(r.Config.Checks) != 2 {
		t.Fatalf("unexpected result: %+v", r)
	}
	config := `{"version":1,"checks":[["custom","test"]],"input_files":["README.md"]}`
	write(t, root, ConfigPath, config)
	write(t, root, ".sdlc/work/example/tickets/01-example.md", "private contents never read")
	before, _ := os.ReadFile(filepath.Join(root, ".git/info/exclude"))
	r = initAt(t, root)
	after, _ := os.ReadFile(filepath.Join(root, ".git/info/exclude"))
	settings, _ := os.ReadFile(filepath.Join(root, ConfigPath))
	if r.ConfigCreated || string(settings) != config || string(before) != string(after) || len(r.Tickets) != 1 || r.Config.Checks[0][0] != "custom" {
		t.Fatalf("preservation failed: %+v", r)
	}
}
func TestDetachedUnbornAndNoRepository(t *testing.T) {
	root := repo(t)
	run(t, root, "checkout", "--detach")
	if !initAt(t, root).Detached {
		t.Fatal("expected detached HEAD")
	}
	unborn := t.TempDir()
	run(t, unborn, "init", "-b", "main")
	r := initAt(t, unborn)
	if r.Head != "" || r.Branch != "main" || len(r.Warnings) == 0 {
		t.Fatalf("unexpected unborn result: %+v", r)
	}
	if _, e := Initialize(context.Background(), t.TempDir()); e == nil {
		t.Fatal("expected no repo failure")
	}
}
func TestRemoteSanitization(t *testing.T) {
	root := repo(t)
	credential := "https://" + "example-user" + ":" + "disposable-value" + "@example.invalid/team/project.git?private=example#fragment"
	run(t, root, "remote", "add", "origin", credential)
	run(t, root, "remote", "add", "other", "git@example.invalid:team/other.git")
	run(t, root, "remote", "add", "local", "../other")
	r := initAt(t, root)
	identities := map[string]string{}
	for _, remote := range r.Remotes {
		identities[remote.Name] = remote.Identity
	}
	if !reflect.DeepEqual(identities, map[string]string{"origin": "example.invalid/team/project.git", "other": "example.invalid/team/other.git", "local": "local path (omitted)"}) {
		t.Fatalf("unexpected sanitized remotes: %+v", identities)
	}
}
func TestPreflightFailuresDoNotWrite(t *testing.T) {
	for _, scenario := range []string{"invalid config", "tracked work", "state symlink", "work symlink", "ticket symlink", "exclude symlink", "ignore override"} {
		t.Run(scenario, func(t *testing.T) {
			root := repo(t)
			exclude := filepath.Join(root, ".git/info/exclude")
			before, _ := os.ReadFile(exclude)
			switch scenario {
			case "invalid config":
				write(t, root, ConfigPath, `{"version":1,"input_files":["../private"]}`)
			case "tracked work":
				write(t, root, ".sdlc/work/example/input", "example")
				run(t, root, "add", ".sdlc/work")
			case "state symlink":
				if e := os.Symlink(t.TempDir(), filepath.Join(root, ".sdlc")); e != nil {
					t.Fatal(e)
				}
			case "work symlink":
				write(t, root, ".sdlc/.keep", "")
				if e := os.Symlink(t.TempDir(), filepath.Join(root, ".sdlc/work")); e != nil {
					t.Fatal(e)
				}
			case "ticket symlink":
				write(t, root, ".sdlc/work/example/tickets/.keep", "")
				if e := os.Symlink(filepath.Join(root, "README.md"), filepath.Join(root, ".sdlc/work/example/tickets/01-example.md")); e != nil {
					t.Fatal(e)
				}
			case "exclude symlink":
				os.Remove(exclude)
				if e := os.Symlink(filepath.Join(root, "README.md"), exclude); e != nil {
					t.Fatal(e)
				}
			case "ignore override":
				write(t, root, ".gitignore", "!.sdlc/work/\n")
			}
			if _, e := Initialize(context.Background(), root); e == nil {
				t.Fatal("expected preflight failure")
			}
			if scenario != "exclude symlink" {
				after, _ := os.ReadFile(exclude)
				if string(after) != string(before) {
					t.Fatal("exclude changed before failed preflight")
				}
			}
			if scenario == "ignore override" {
				if _, e := os.Lstat(filepath.Join(root, ".sdlc")); !os.IsNotExist(e) {
					t.Fatal("state created before ignore failure")
				}
			}
		})
	}
}
func TestLinkedWorktreeAndDiscovery(t *testing.T) {
	root := repo(t)
	linked := filepath.Join(t.TempDir(), "linked")
	run(t, root, "worktree", "add", "-b", "linked", linked)
	write(t, linked, "package.json", `{"scripts":{"test":"touch SHOULD_NOT_EXIST","lint":"example","build":"example","other":"example"}}`)
	write(t, linked, "pnpm-lock.yaml", "")
	write(t, linked, "pyproject.toml", "")
	write(t, linked, "example.csproj", "")
	write(t, linked, "compose.yaml", "")
	r := initAt(t, linked)
	if len(r.Tools) != 5 || !reflect.DeepEqual(r.Config.Checks, [][]string{{"pnpm", "run", "test"}, {"pnpm", "run", "lint"}, {"pnpm", "run", "build"}}) {
		t.Fatalf("unexpected discovery: %+v", r)
	}
	if _, e := os.Stat(filepath.Join(linked, "SHOULD_NOT_EXIST")); !os.IsNotExist(e) {
		t.Fatal("project command executed")
	}
	data, _ := os.ReadFile(filepath.Join(root, ".git/info/exclude"))
	if !strings.Contains(string(data), "/.sdlc/work/\n") {
		t.Fatal("linked worktree exclude missing")
	}
}
func TestConfigValidation(t *testing.T) {
	if portablePath(string([]byte{'Z', ':', '\\'}) + "example") {
		t.Fatal("accepted Windows absolute path")
	}
	for _, data := range []string{
		`{"version":2,"checks":[],"input_files":[]}`,
		`{"version":1}`,
		`{"version":1,"checks":null,"input_files":[]}`,
		`{"version":1,"checks":[],"input_files":null}`,
		`{"version":1,"checks":[[]],"input_files":[]}`,
		`{"version":1,"checks":[[""]],"input_files":[]}`,
		`{"version":1,"checks":[],"input_files":["/example/input"]}`,
		`{"version":1,"checks":[],"input_files":["example:input"]}`,
		`{"version":1,"checks":[],"input_files":[]} {}`,
		`{"version":1,"checks":[],"input_files":[],"unknown":true}`,
	} {
		c := Config{Version: 1, Checks: [][]string{}, InputFiles: []string{}}
		if decodeConfig([]byte(data), &c) == nil {
			t.Fatalf("accepted %s", data)
		}
	}
	var c Config
	if err := decodeConfig([]byte(`{"version":1,"checks":[],"input_files":[]}`), &c); err != nil {
		t.Fatal(err)
	}
}

func TestInitializeColonWorkReference(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows directory names cannot contain a colon")
	}
	root := repo(t)
	write(t, root, ".sdlc/work/Example: work/tickets/01-example.md", "disposable ticket contents\n")
	result := initAt(t, root)
	if !reflect.DeepEqual(result.Tickets, []string{".sdlc/work/Example: work/tickets/01-example.md"}) {
		t.Fatalf("initialization changed the literal work reference: %+v", result.Tickets)
	}
}

func TestNoFSMonitorExecution(t *testing.T) {
	root := repo(t)
	marker := filepath.Join(root, "fsmonitor-ran")
	hook := filepath.Join(t.TempDir(), "fsmonitor")
	if e := os.WriteFile(hook, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0700); e != nil {
		t.Fatal(e)
	}
	run(t, root, "config", "core.fsmonitor", hook)
	initAt(t, root)
	if _, e := os.Stat(marker); !os.IsNotExist(e) {
		t.Fatal("Git fsmonitor hook executed")
	}
}
func TestConfigInputSymlinkRejected(t *testing.T) {
	root := repo(t)
	write(t, root, ConfigPath, `{"version":1,"checks":[],"input_files":["input/data"]}`)
	if e := os.Symlink(t.TempDir(), filepath.Join(root, "input")); e != nil {
		t.Fatal(e)
	}
	if _, e := Initialize(context.Background(), root); e == nil {
		t.Fatal("accepted symlink input path")
	}
	if _, e := os.Stat(filepath.Join(root, ".sdlc/work")); !os.IsNotExist(e) {
		t.Fatal("work directory created after rejected config")
	}
}

func TestCaseVariantTrackedWorkRejected(t *testing.T) {
	root := repo(t)
	run(t, root, "config", "core.ignorecase", "true")
	write(t, root, ".SDLC/work/example/tickets/01-example.md", "disposable example")
	run(t, root, "add", ".SDLC/work")
	exclude := filepath.Join(root, ".git/info/exclude")
	before, _ := os.ReadFile(exclude)
	if _, err := Initialize(context.Background(), root); err == nil {
		t.Fatal("accepted case-variant tracked work")
	}
	after, _ := os.ReadFile(exclude)
	if string(after) != string(before) {
		t.Fatal("exclude changed on rejected private path")
	}
	if _, err := os.Lstat(filepath.Join(root, ConfigPath)); !os.IsNotExist(err) {
		t.Fatal("settings created on rejected private path")
	}
}
func TestDirtyIndexAndStatInspection(t *testing.T) {
	root := repo(t)
	dirty, e := dirty(context.Background(), root, strings.TrimSpace(run(t, root, "rev-parse", "HEAD")))
	if e != nil || dirty {
		t.Fatalf("clean checkout dirty=%v error=%v", dirty, e)
	}
	// A staged change is detected even after the worktree matches the index.
	write(t, root, "README.md", "staged change\n")
	run(t, root, "add", "README.md")
	r := initAt(t, root)
	if !r.Dirty {
		t.Fatal("staged change not detected")
	}
}

func TestNoGitFilterExecution(t *testing.T) {
	for _, filter := range []string{"clean", "process"} {
		for _, invalid := range []bool{false, true} {
			t.Run(filter+map[bool]string{false: " initialized", true: " rejected config"}[invalid], func(t *testing.T) {
				root := repo(t)
				write(t, root, ".gitattributes", "subject.txt filter=marker\n")
				write(t, root, "subject.txt", "before\n")
				run(t, root, "add", ".gitattributes", "subject.txt")
				run(t, root, "commit", "-m", "Add example subject")
				marker := filepath.Join(root, "filter-ran")
				script := filepath.Join(t.TempDir(), "filter")
				body := "#!/bin/sh\ntouch '" + marker + "'\ncat\n"
				if filter == "process" {
					body = "#!/bin/sh\ntouch '" + marker + "'\nexit 1\n"
				}
				if err := os.WriteFile(script, []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
				run(t, root, "config", "filter.marker."+filter, script)
				subject := filepath.Join(root, "subject.txt")
				info, err := os.Stat(subject)
				if err != nil {
					t.Fatal(err)
				}
				write(t, root, "subject.txt", "after!\n")
				if err = os.Chtimes(subject, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
				for _, args := range [][]string{{"ls-files", "--cached", "--debug", "-z"}, {"ls-files", "--others", "--exclude-standard", "-z"}, {"diff-index", "--cached", "--name-only", "--no-ext-diff", "--no-textconv", "-z", "HEAD", "--"}} {
					if _, err = git(context.Background(), root, args...); err != nil {
						t.Fatal(err)
					}
					if _, err = os.Stat(marker); !os.IsNotExist(err) {
						t.Fatalf("inspection %v executed filter", args)
					}
				}
				if invalid {
					write(t, root, ConfigPath, `{"version":2,"checks":[],"input_files":[]}`)
				}
				result, initErr := Initialize(context.Background(), root)
				err = initErr
				if !invalid && !result.Dirty {
					t.Fatal("same-size change with restored mtime reported clean")
				}
				if invalid && err == nil {
					t.Fatal("invalid config accepted")
				}
				if !invalid && err != nil {
					t.Fatal(err)
				}
				if _, err = os.Stat(marker); !os.IsNotExist(err) {
					t.Fatal("initialization executed Git filter")
				}
			})
		}
	}
}
func TestExcludeConcurrentEditPreserved(t *testing.T) {
	root := repo(t)
	path := filepath.Join(root, ".git/info/exclude")
	expected, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	candidate := append(append([]byte{}, expected...), []byte("/.sdlc/work/\n")...)
	edited := append(append([]byte{}, expected...), []byte("/user-local-rule/\n")...)
	if err = os.WriteFile(path, edited, 0640); err != nil {
		t.Fatal(err)
	}
	if err = writeRegular(path, candidate, false, expected); err == nil {
		t.Fatal("stale exclude candidate accepted")
	}
	actual, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(actual) != string(edited) {
		t.Fatal("concurrent exclude rule lost")
	}
}

func TestGitHubRepositoryUsesOnlyUniqueSanitizedRemoteIdentity(t *testing.T) {
	for _, test := range []struct {
		remotes []Remote
		want    string
	}{
		{[]Remote{{"origin", remoteIdentity("ssh://git" + "@" + "github.com/example/project.git")}}, "example/project"},
		{[]Remote{{"origin", remoteIdentity("git" + "@" + "github.com:example/project.git")}}, "example/project"},
		{[]Remote{{"origin", "github.com/example/project.git"}, {"origin", "github.com/EXAMPLE/project.git"}}, "EXAMPLE/project"},
		{[]Remote{{"origin", "github.com/example/project.git"}, {"origin", "github.com/other/project.git"}}, ""},
		{[]Remote{{"origin", "github-alias/example/project.git"}}, ""},
		{[]Remote{{"origin", "github.com/example/project.git/extra"}}, ""},
		{[]Remote{{"origin", "github.com/example/project%20name.git"}}, ""},
		{[]Remote{{"origin", "github.com/example/project.git"}, {"upstream", "github.com/other/project.git"}}, "example/project"},
		{[]Remote{{"origin", "example.invalid/example/project.git"}, {"upstream", "github.com/other/project.git"}}, ""},
		{nil, ""},
	} {
		if got := GitHubRepository(test.remotes); got != test.want {
			t.Fatalf("got %q, want %q", got, test.want)
		}
	}
}
