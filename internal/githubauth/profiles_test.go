package githubauth

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestProfileValidationAndDefaultCompatibility(t *testing.T) {
	for _, value := range []string{"", "default", "work", "personal-account", "account_2", strings.Repeat("a", 48)} {
		normalized, err := NormalizeProfile(value)
		if err != nil || ValidateProfile(value) != nil {
			t.Fatal("valid profile rejected", value, err)
		}
		if value == "" && normalized != "default" {
			t.Fatal("omitted profile lost default")
		}
	}
	for _, value := range []string{"../work", "/work", "Work", " work", "work ", "work/name", "work\n", "-work", "_work", strings.Repeat("a", 49)} {
		if ValidateProfile(value) == nil {
			t.Fatal("invalid profile accepted", value)
		}
	}
	manager, docker := fixture(t)
	if err := manager.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	original, err := manager.identity(false)
	if err != nil {
		t.Fatal(err)
	}
	manager.Profile = "default"
	if err := manager.Login(context.Background()); err == nil {
		t.Fatal("explicit default bypassed existing login")
	}
	if state, err := manager.Status(context.Background(), false); err != nil || state != "stored" {
		t.Fatal(state, err)
	}
	repeated, err := manager.identity(false)
	if err != nil || repeated != original || len(docker.volumes) != 1 {
		t.Fatal("explicit default changed legacy cache", err)
	}
	for _, name := range []string{"github-installation.json", "github-installation.lock", "github-auth.lock"} {
		if _, err := os.Stat(filepath.Join(manager.Runtime.Directory, name)); err != nil {
			t.Fatal("legacy default state path changed", name, err)
		}
	}
}

func TestNamedProfilesKeepIndependentCachesAndLeases(t *testing.T) {
	manager, docker := fixture(t)
	work := manager
	work.Profile = "work"
	personal := manager
	personal.Profile = "personal"
	for _, selected := range []Manager{work, personal} {
		if err := selected.Login(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(docker.volumes) != 2 {
		t.Fatal("named profiles shared a native cache")
	}
	workID, err := work.identity(false)
	if err != nil {
		t.Fatal(err)
	}
	personalID, err := personal.identity(false)
	if err != nil || personalID == workID {
		t.Fatal("named profiles shared an installation identity", err)
	}
	for _, selected := range []Manager{work, personal} {
		path, err := selected.profilePath("github-installation", ".json")
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("profile metadata is not private", err)
		}
	}
	if _, err := os.Stat(filepath.Join(manager.Runtime.Directory, "github-installation.json")); !os.IsNotExist(err) {
		t.Fatal("named login touched default metadata")
	}
	workSession, err := work.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer workSession.Close()
	// A distinct profile can use the same immutable runtime while work is leased.
	personalSession, err := personal.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer personalSession.Close()
	if workSession.Profile != "work" || personalSession.Profile != "personal" || workSession.Volume == personalSession.Volume || workSession.ImageID != personalSession.ImageID {
		t.Fatal("profile selection or shared runtime identity changed")
	}
	for _, operation := range []func(context.Context) error{
		work.Login, work.Logout,
		func(ctx context.Context) error { _, err := work.Status(ctx, false); return err },
		func(ctx context.Context) error {
			session, err := work.Acquire(ctx)
			if err == nil {
				session.Close()
			}
			return err
		},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
		err := operation(ctx)
		cancel()
		if err == nil {
			t.Fatal("same profile authentication or publication bypassed its lease")
		}
	}
}

func TestInvalidProfileRejectsBeforeStorageOrDocker(t *testing.T) {
	manager, docker := fixture(t)
	manager.Profile = "../another-profile"
	before, err := os.ReadDir(manager.Runtime.Directory)
	if err != nil {
		t.Fatal(err)
	}
	names := func(entries []os.DirEntry) []string {
		var out []string
		for _, entry := range entries {
			out = append(out, entry.Name())
		}
		return out
	}
	for _, operation := range []func() error{
		func() error { return manager.Login(context.Background()) },
		func() error { _, err := manager.Status(context.Background(), false); return err },
		func() error { return manager.Logout(context.Background()) },
		func() error {
			session, err := manager.Acquire(context.Background())
			if err == nil {
				session.Close()
			}
			return err
		},
	} {
		if err := operation(); err == nil {
			t.Fatal("invalid profile reached operation")
		}
	}
	after, err := os.ReadDir(manager.Runtime.Directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(docker.calls) != 0 || !reflect.DeepEqual(names(before), names(after)) {
		t.Fatal("invalid profile touched Docker or storage")
	}
}

func TestMissingNamedProfileKeepsSelectionInLoginHint(t *testing.T) {
	manager, _ := fixture(t)
	manager.Profile = "work"
	_, err := manager.Acquire(context.Background())
	if err == nil || !strings.Contains(err.Error(), "sdlc auth login --service github --profile work") {
		t.Fatal("missing named cache lost selected profile", err)
	}
	state, err := manager.Status(context.Background(), false)
	if err != nil || state != "missing" {
		t.Fatal(state, err)
	}
	path, _ := manager.profilePath("github-installation", ".json")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("named status created login metadata")
	}
}

func TestConfiguredProfileRefusesNativeReloginAndAllowsLogoutFirst(t *testing.T) {
	manager, docker := fixture(t)
	manager.Profile = "work"
	if err := manager.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	nativeLogins := func() int {
		count := 0
		for _, args := range docker.calls {
			if args[0] == "run" && args[len(args)-1] == "login" {
				count++
			}
		}
		return count
	}
	if nativeLogins() != 1 {
		t.Fatal("initial login did not invoke native flow")
	}
	err := manager.Login(context.Background())
	if err == nil || !strings.Contains(err.Error(), "status --service github --profile work --verify") || !strings.Contains(err.Error(), "logout --service github --profile work") || !strings.Contains(err.Error(), "new profile") {
		t.Fatal("configured profile lacked actionable refusal", err)
	}
	if nativeLogins() != 1 {
		t.Fatal("configured profile started another native login")
	}
	if err := manager.Logout(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := manager.Login(context.Background()); err != nil {
		t.Fatal("empty cache could not reauthorize", err)
	}
	if nativeLogins() != 2 || len(docker.volumes) != 1 {
		t.Fatal("logout/login did not reuse the empty profile volume")
	}
}
