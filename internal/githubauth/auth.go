package githubauth

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/tjpeel/sdlc/internal/filelock"
	"github.com/tjpeel/sdlc/internal/providerauth"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
)

//go:embed container.py
var helper string

type Docker = providerauth.Docker
type LocalDocker = providerauth.LocalDocker

type Manager struct {
	// Profile selects an independent native GitHub CLI cache; blank means default.
	Profile    string
	Runtime    runtimeimage.Manager
	Docker     Docker
	Terminal   func() bool
	OnWait     func(provider, reason string)
	OnAcquired func(provider string)
}

func New(runtime runtimeimage.Manager) Manager {
	return Manager{Runtime: runtime, Docker: LocalDocker{}, Terminal: terminal}
}

var profileName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,47}$`)

// NormalizeProfile preserves case-sensitive profile names and maps an omitted
// name to the default profile, whose existing storage paths remain unchanged.
func NormalizeProfile(profile string) (string, error) {
	if profile == "" {
		profile = "default"
	}
	if !profileName.MatchString(profile) {
		return "", fmt.Errorf("GitHub profile must use 1 to 48 lowercase letters, digits, underscores or hyphens and start with a letter or digit")
	}
	return profile, nil
}
func ValidateProfile(profile string) error { _, err := NormalizeProfile(profile); return err }

func (manager Manager) profilePath(base, extension string) (string, error) {
	profile, err := NormalizeProfile(manager.Profile)
	if err != nil {
		return "", err
	}
	if profile != "default" {
		base += "-profile-" + profile
	}
	return filepath.Join(manager.Runtime.Directory, base+extension), nil
}
func (manager Manager) authHint(action string) string {
	hint := "sdlc auth " + action + " --service github"
	if profile, err := NormalizeProfile(manager.Profile); err == nil && profile != "default" {
		hint += " --profile " + profile
	}
	return hint
}

func terminal() bool {
	for _, file := range []*os.File{os.Stdin, os.Stdout, os.Stderr} {
		info, err := file.Stat()
		if err != nil || info.Mode()&os.ModeCharDevice == 0 {
			return false
		}
	}
	return true
}

func randomID() (string, error) {
	var token [16]byte
	_, err := rand.Read(token[:])
	return hex.EncodeToString(token[:]), err
}

var installationID = regexp.MustCompile(`^[0-9a-f]{32}$`)

func (manager Manager) identity(create bool) (string, error) {
	return manager.identityContext(context.Background(), create)
}

func (manager Manager) identityContext(ctx context.Context, create bool) (string, error) {
	lockPath, err := manager.profilePath("github-installation", ".lock")
	if err != nil {
		return "", err
	}
	path, err := manager.profilePath("github-installation", ".json")
	if err != nil {
		return "", err
	}
	lock, err := filelock.AcquireContext(ctx, lockPath, filelock.Exclusive, nil)
	if err != nil {
		return "", fmt.Errorf("cannot lock authentication installation state: %w", err)
	}
	defer lock.Close()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && !create {
		return "", nil
	}
	if err == nil {
		var record struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(data, &record) != nil || !installationID.MatchString(record.ID) {
			return "", fmt.Errorf("authentication installation state is invalid")
		}
		return record.ID, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("cannot read authentication installation state")
	}
	id, err := randomID()
	if err != nil {
		return "", fmt.Errorf("cannot create authentication installation identity")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", fmt.Errorf("cannot save authentication installation identity")
	}
	if err = json.NewEncoder(file).Encode(struct {
		ID string `json:"id"`
	}{id}); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return "", fmt.Errorf("cannot save authentication installation identity")
	}
	return id, nil
}

func labels(id, provider string) map[string]string {
	return map[string]string{"io.sdlc.managed": "true", "io.sdlc.kind": "github-auth",
		"io.sdlc.installation": id, "io.sdlc.provider": "github"}
}

func (manager Manager) volume(ctx context.Context, id, provider string, create bool) (string, error) {
	name := "sdlc-github-auth-" + id + "-" + provider
	output, err := manager.Docker.Output(ctx, "volume", "ls", "--filter", "name="+name, "--format", "{{.Name}}")
	if err != nil {
		return "", fmt.Errorf("cannot list authentication storage")
	}
	found := false
	for _, candidate := range strings.Fields(string(output)) {
		if candidate == name {
			found = true
		}
	}
	if !found {
		if !create {
			return "", nil
		}
		args := []string{"volume", "create", "--driver", "local"}
		for _, key := range []string{"io.sdlc.managed", "io.sdlc.kind", "io.sdlc.installation", "io.sdlc.provider"} {
			args = append(args, "--label", key+"="+labels(id, provider)[key])
		}
		if _, err := manager.Docker.Output(ctx, append(args, name)...); err != nil {
			return "", fmt.Errorf("cannot create authentication storage")
		}
	}
	output, err = manager.Docker.Output(ctx, "volume", "inspect", name)
	if err != nil {
		return "", fmt.Errorf("cannot inspect authentication storage")
	}
	var volumes []struct {
		Name, Driver string
		Options      map[string]string
		Labels       map[string]string
	}
	if json.Unmarshal(output, &volumes) != nil || len(volumes) != 1 || volumes[0].Name != name ||
		volumes[0].Driver != "local" || len(volumes[0].Options) != 0 {
		return "", fmt.Errorf("authentication storage must be an SDLC-managed local volume without driver options")
	}
	for key, value := range labels(id, provider) {
		if volumes[0].Labels[key] != value {
			return "", fmt.Errorf("authentication storage ownership labels do not match this installation")
		}
	}
	if err := manager.guardVolume(ctx, name, id, provider); err != nil {
		return "", err
	}
	return name, nil
}

// guardVolume refuses to reuse a cache while any surviving container holds it.
// Even a stopped container can be restarted outside this process's lease.
func (manager Manager) guardVolume(ctx context.Context, volume, id, provider string) error {
	output, err := manager.Docker.Output(ctx, "ps", "--all", "--filter", "volume="+volume, "--format", "{{.ID}}")
	if err != nil {
		return fmt.Errorf("cannot check authentication storage container ownership")
	}
	for _, container := range strings.Fields(string(output)) {
		data, err := manager.Docker.Output(ctx, "container", "inspect", container)
		if err != nil {
			return fmt.Errorf("cannot inspect authentication storage container ownership")
		}
		var records []struct {
			Config struct{ Labels map[string]string }
			State  struct {
				Status                      string
				Running, Paused, Restarting bool
			}
			Mounts []struct{ Type, Name string }
		}
		if json.Unmarshal(data, &records) != nil || len(records) != 1 || records[0].State.Status == "" {
			return fmt.Errorf("cannot verify authentication storage container ownership")
		}
		for _, mount := range records[0].Mounts {
			if mount.Type != "volume" || mount.Name != volume {
				continue
			}
			expected := labels(id, provider)
			for _, key := range []string{"io.sdlc.managed", "io.sdlc.kind", "io.sdlc.installation", "io.sdlc.provider"} {
				if records[0].Config.Labels[key] != expected[key] {
					return fmt.Errorf("authentication storage is held by an unmanaged container; inspect Docker before retrying")
				}
			}
			return fmt.Errorf("authentication storage is held by a surviving provider container; inspect Docker before retrying")
		}
		return fmt.Errorf("cannot verify authentication storage container mounts")
	}
	return nil
}

func containerArgs(image, name, volume, provider, action string) []string {
	args := []string{"run", "--rm", "--name", name, "--pull", "never", "--log-driver", "none",
		"--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--pids-limit", "128", "--memory", "1g", "--cpus", "2",
		"--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=64m,mode=1777",
		"--tmpfs", "/home/node/:rw,nosuid,nodev,noexec,size=64m,mode=0700,uid=1000,gid=1000"}
	installation := strings.TrimSuffix(strings.TrimPrefix(volume, "sdlc-github-auth-"), "-"+provider)
	for _, key := range []string{"io.sdlc.managed", "io.sdlc.kind", "io.sdlc.installation", "io.sdlc.provider"} {
		args = append(args, "--label", key+"="+labels(installation, provider)[key])
	}
	// Docker's client can inject proxies from its host config. Empty overrides prevent that.
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "FTP_PROXY", "NO_PROXY", "ALL_PROXY",
		"http_proxy", "https_proxy", "ftp_proxy", "no_proxy", "all_proxy"} {
		args = append(args, "--env", key+"=")
	}
	mount := "type=volume,src=" + volume + ",dst=/github-auth,volume-nocopy"
	if action == "init" {
		args = append(args, "--network", "none", "--user", "0:0", "--cap-add", "CHOWN", "--cap-add", "FOWNER")
	} else {
		args = append(args, "--user", "1000:1000")
		if action == "status" || action == "verify" || action == "identity" || action == "repository" {
			network := "none"
			if action == "verify" || action == "identity" || action == "repository" {
				network = "bridge"
			}
			args = append(args, "--network", network)
			mount += ",readonly"
		}
		if action == "login" {
			args = append(args, "--interactive", "--tty", "--network", "bridge")
		}
		if action == "logout" {
			args = append(args, "--network", "none")
		}
	}
	return append(args, "--mount", mount, "--entrypoint", "/usr/bin/python3", image, "-c", helper, action)
}

func (manager Manager) container(ctx context.Context, image, volume, provider, action string, extra ...string) (output []byte, err error) {
	id, err := randomID()
	if err != nil {
		return nil, fmt.Errorf("cannot name authentication container")
	}
	name := "sdlc-github-auth-" + id
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		// --rm normally removes it; listing also distinguishes that from a failed cleanup.
		remaining, cleanupErr := manager.Docker.Output(cleanup, "ps", "--all", "--filter", "name=^/"+name+"$", "--format", "{{.ID}}")
		if cleanupErr == nil && len(strings.TrimSpace(string(remaining))) > 0 {
			_, cleanupErr = manager.Docker.Output(cleanup, "rm", "--force", name)
		}
		if cleanupErr != nil {
			err = fmt.Errorf("authentication container cleanup failed; check Docker before retrying")
		}
	}()
	args := append(containerArgs(image, name, volume, provider, action), extra...)
	if action == "login" {
		err = manager.Docker.Interactive(ctx, args...)
	} else {
		output, err = manager.Docker.Output(ctx, args...)
	}
	if err != nil {
		return nil, fmt.Errorf("authentication %s failed or was cancelled; check %s before retrying", action, manager.authHint("status"))
	}
	return output, nil
}

type operationLease struct{ provider, runtime *os.File }

func (lease *operationLease) Close() error {
	return errors.Join(lease.runtime.Close(), lease.provider.Close())
}

func (manager Manager) begin(ctx context.Context, provider string) (runtimeimage.State, *operationLease, error) {
	leasePath, err := manager.profilePath("github-auth", ".lock")
	if err != nil {
		return runtimeimage.State{}, nil, err
	}
	if err := os.MkdirAll(manager.Runtime.Directory, 0700); err != nil {
		return runtimeimage.State{}, nil, fmt.Errorf("cannot open SDLC state directory")
	}
	wait := func(reason string) func() {
		return func() {
			if manager.OnWait != nil {
				manager.OnWait(provider, reason)
			}
		}
	}
	providerLock, err := filelock.AcquireContext(ctx, leasePath, filelock.Exclusive, wait("provider_busy"))
	if err != nil {
		return runtimeimage.State{}, nil, fmt.Errorf("provider lease unavailable: %w", err)
	}
	runtimeLock, err := filelock.AcquireContext(ctx, filepath.Join(manager.Runtime.Directory, "runtime-build.lock"), filelock.Shared, wait("runtime_busy"))
	if err != nil {
		providerLock.Close()
		return runtimeimage.State{}, nil, fmt.Errorf("runtime lease unavailable: %w", err)
	}
	lease := &operationLease{providerLock, runtimeLock}
	state, err := manager.Runtime.Status(ctx)
	if err != nil {
		lease.Close()
		return runtimeimage.State{}, nil, fmt.Errorf("shared local runtime is unavailable or changed; run sdlc runtime status and rebuild if needed")
	}
	if manager.OnAcquired != nil {
		manager.OnAcquired(provider)
	}
	return state, lease, nil
}

func (manager Manager) Login(ctx context.Context) error {
	if err := ValidateProfile(manager.Profile); err != nil {
		return err
	}
	provider := "github"
	if !manager.Terminal() {
		return fmt.Errorf("login needs an interactive terminal for stdin, stdout and stderr; do not redirect login output")
	}
	state, lock, err := manager.begin(ctx, provider)
	if err != nil {
		return err
	}
	defer lock.Close()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	id, err := manager.identityContext(ctx, true)
	if err != nil {
		return err
	}
	volume, err := manager.volume(ctx, id, provider, true)
	if err != nil {
		return err
	}
	if _, err := manager.container(ctx, state.ImageID, volume, provider, "init"); err != nil {
		return err
	}
	stored, err := manager.status(ctx, state.ImageID, volume, provider)
	if err != nil {
		return err
	}
	if stored == "stored" {
		return fmt.Errorf("GitHub profile already contains a login; use %s --verify, run %s before reauthorizing or changing accounts, or select a new profile", manager.authHint("status"), manager.authHint("logout"))
	}
	if stored != "missing" {
		return fmt.Errorf("GitHub profile storage is invalid; inspect %s before logging in", manager.authHint("status"))
	}
	if _, err := manager.container(ctx, state.ImageID, volume, provider, "login"); err != nil {
		return err
	}
	// Prove the persisted credential can be loaded by a separate, offline container.
	result, err := manager.status(ctx, state.ImageID, volume, provider)
	if err != nil {
		return err
	}
	if result != "stored" {
		return fmt.Errorf("login completed but a fresh container could not load stored account credentials")
	}
	return nil
}

func (manager Manager) status(ctx context.Context, image, volume, provider string, connected ...bool) (string, error) {
	action := "status"
	if len(connected) > 0 && connected[0] {
		action = "verify"
	}
	output, err := manager.container(ctx, image, volume, provider, action)
	if err != nil {
		return "", err
	}
	var result struct {
		State string `json:"state"`
	}
	if json.Unmarshal(output, &result) != nil {
		return "", fmt.Errorf("authentication status returned an invalid response")
	}
	allowed := result.State == "missing" || result.State == "invalid"
	if action == "verify" {
		allowed = allowed || result.State == "verified" || result.State == "failed"
	} else {
		allowed = allowed || result.State == "stored"
	}
	if !allowed {
		return "", fmt.Errorf("authentication status returned an invalid response")
	}
	return result.State, nil
}

func (manager Manager) Status(ctx context.Context, connected bool) (string, error) {
	provider := "github"
	state, lock, err := manager.begin(ctx, provider)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	id, err := manager.identityContext(ctx, false)
	if err != nil || id == "" {
		return "missing", err
	}
	volume, err := manager.volume(ctx, id, provider, false)
	if err != nil || volume == "" {
		return "missing", err
	}
	return manager.status(ctx, state.ImageID, volume, provider, connected)
}

// Session holds the authentication and runtime leases until publication finishes.
// Consumers must use only Volume at /github-auth with a clean gh environment.
type Session struct {
	Profile string
	ImageID string
	Volume  string
	lease   *operationLease
	manager Manager
}

func (session *Session) Close() error { return session.lease.Close() }

func (manager Manager) Acquire(ctx context.Context) (*Session, error) {
	state, lease, err := manager.begin(ctx, "github")
	if err != nil {
		return nil, err
	}
	id, err := manager.identityContext(ctx, false)
	if err != nil {
		lease.Close()
		return nil, err
	}
	if id == "" {
		lease.Close()
		return nil, fmt.Errorf("GitHub login is missing; run %s", manager.authHint("login"))
	}
	volume, err := manager.volume(ctx, id, "github", false)
	if err != nil {
		lease.Close()
		return nil, err
	}
	if volume == "" {
		lease.Close()
		return nil, fmt.Errorf("GitHub login is missing; run %s", manager.authHint("login"))
	}
	result, err := manager.status(ctx, state.ImageID, volume, "github")
	if err != nil || result != "stored" {
		lease.Close()
		return nil, fmt.Errorf("GitHub stored login is unavailable; run %s", manager.authHint("status"))
	}
	profile, _ := NormalizeProfile(manager.Profile)
	return &Session{Profile: profile, ImageID: state.ImageID, Volume: volume, lease: lease, manager: manager}, nil
}

func (manager Manager) Logout(ctx context.Context) error {
	state, lease, err := manager.begin(ctx, "github")
	if err != nil {
		return err
	}
	defer lease.Close()
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	id, err := manager.identityContext(ctx, false)
	if err != nil || id == "" {
		return err
	}
	volume, err := manager.volume(ctx, id, "github", false)
	if err != nil || volume == "" {
		return err
	}
	_, err = manager.container(ctx, state.ImageID, volume, "github", "logout")
	return err
}

// Identity is the selected account reported by GitHub's authenticated user API.
type Identity struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}

func (session *Session) Identity(ctx context.Context) (Identity, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	output, err := session.manager.container(ctx, session.ImageID, session.Volume, "github", "identity")
	if err != nil {
		return Identity{}, fmt.Errorf("cannot verify selected GitHub identity")
	}
	var identity Identity
	if len(output) > 1024 || json.Unmarshal(output, &identity) != nil || identity.ID <= 0 || len(identity.Login) == 0 || len(identity.Login) > 100 {
		return Identity{}, fmt.Errorf("GitHub identity returned an invalid response")
	}
	for _, char := range identity.Login {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-') {
			return Identity{}, fmt.Errorf("GitHub identity returned an invalid response")
		}
	}
	return identity, nil
}

// RepositoryIdentity is GitHub's immutable repository ID and canonical name.
type RepositoryIdentity struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Push bool   `json:"push"`
}

var repositoryOwner = regexp.MustCompile(`^[A-Za-z0-9-]{1,39}$`)
var repositoryName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)

func validRepository(value string) bool {
	parts := strings.Split(value, "/")
	return len(parts) == 2 && repositoryOwner.MatchString(parts[0]) && !strings.HasPrefix(parts[0], "-") && !strings.HasSuffix(parts[0], "-") && !strings.Contains(parts[0], "--") && repositoryName.MatchString(parts[1]) && parts[1] != "." && parts[1] != ".."
}
func (session *Session) Repository(ctx context.Context, repository string) (RepositoryIdentity, error) {
	if !validRepository(repository) {
		return RepositoryIdentity{}, fmt.Errorf("GitHub repository must be OWNER/REPO")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	output, err := session.manager.container(ctx, session.ImageID, session.Volume, "github", "repository", repository)
	if err != nil {
		return RepositoryIdentity{}, fmt.Errorf("cannot verify selected GitHub repository")
	}
	var identity RepositoryIdentity
	if len(output) > 1024 || json.Unmarshal(output, &identity) != nil || identity.ID <= 0 || !identity.Push || !validRepository(identity.Name) || !strings.EqualFold(identity.Name, repository) {
		return RepositoryIdentity{}, fmt.Errorf("GitHub repository identity or push permission does not match the selected repository")
	}
	return identity, nil
}
