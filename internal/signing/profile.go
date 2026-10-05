// Package signing resolves one explicitly configured automation key through the
// official 1Password CLI. It never stores a private key in installation state.
package signing

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/tjpeel/sdlc/internal/runtimepins"
)

const DefaultProvider = "1password"

const Image = runtimepins.DefaultSigningImage

type Profile struct {
	Provider      string `json:"provider,omitempty"`
	Version       int    `json:"version"`
	ID            string `json:"id"`
	Reference     string `json:"reference"`
	PublicKey     string `json:"public_key"`
	Fingerprint   string `json:"fingerprint"`
	BootstrapFile string `json:"bootstrap_file"`
}

// EffectiveProvider preserves version-1 profiles written before provider selection.
func (profile Profile) EffectiveProvider() string {
	if profile.Provider == "" {
		return DefaultProvider
	}
	return profile.Provider
}

// ValidateProvider rejects unsupported routes before reading credentials or
// invoking a connected client. Empty values use the historical default.
func ValidateProvider(provider string) error {
	if provider != "" && provider != DefaultProvider {
		return fmt.Errorf("signing secret provider must be 1password; other providers are not implemented")
	}
	return nil
}

func (profile Profile) Validate() error {
	if err := ValidateProvider(profile.Provider); err != nil {
		return err
	}
	if profile.Version != 1 || !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`).MatchString(profile.ID) {
		return fmt.Errorf("invalid signing profile identity")
	}
	if !strings.HasPrefix(profile.Reference, "op://") || !strings.HasSuffix(profile.Reference, "/private key?ssh-format=openssh") || strings.ContainsAny(profile.Reference, "\x00\r\n\"") || len(profile.Reference) > 1024 {
		return fmt.Errorf("signing reference must identify an OpenSSH private-key field")
	}
	segments := strings.Split(strings.TrimSuffix(strings.TrimPrefix(profile.Reference, "op://"), "/private key?ssh-format=openssh"), "/")
	if len(segments) != 2 || !referenceSegment(segments[0]) || !referenceSegment(segments[1]) {
		return fmt.Errorf("signing reference must identify exactly one nonempty unambiguous vault and item")
	}

	if err := ValidatePublicIdentity(profile.PublicKey, profile.Fingerprint); err != nil {
		return err
	}
	if !filepath.IsAbs(profile.BootstrapFile) {
		return fmt.Errorf("bootstrap file must be an absolute path outside source repositories")
	}
	return outsideRepository(profile.BootstrapFile)
}

// ValidatePublicIdentity checks public metadata without inspecting any secret.
func ValidatePublicIdentity(publicKey, fingerprint string) error {
	if !regexp.MustCompile(`^ssh-ed25519 [A-Za-z0-9+/]+={0,2}$`).MatchString(publicKey) || len(publicKey) > 256 || !regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`).MatchString(fingerprint) {
		return fmt.Errorf("signing profile requires an Ed25519 public key and its SHA256 fingerprint")
	}
	encoded := strings.TrimPrefix(publicKey, "ssh-ed25519 ")
	public, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(public) != 51 || !bytes.Equal(public[:19], []byte("\x00\x00\x00\x0bssh-ed25519\x00\x00\x00\x20")) {
		return fmt.Errorf("invalid Ed25519 public key")
	}
	hash := sha256.Sum256(public)
	if fingerprint != "SHA256:"+base64.RawStdEncoding.EncodeToString(hash[:]) {
		return fmt.Errorf("signing public key and fingerprint differ")
	}
	return nil
}

func outsideRepository(path string) error {
	for directory := filepath.Dir(path); ; directory = filepath.Dir(directory) {
		if _, err := os.Lstat(filepath.Join(directory, ".git")); err == nil || !os.IsNotExist(err) {
			return fmt.Errorf("signing profile and bootstrap must stay outside source repositories")
		}
		if filepath.Dir(directory) == directory {
			return nil
		}
	}
}

func privateRead(path string, maximum int64) ([]byte, error) {
	if !filepath.IsAbs(path) || outsideRepository(path) != nil {
		return nil, fmt.Errorf("credential input must be an absolute path outside source repositories")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != filepath.Clean(path) {
		return nil, fmt.Errorf("credential input must not follow symlinks")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() <= 0 || info.Size() > maximum {
		return nil, fmt.Errorf("credential input must be a nonempty bounded private file with mode 0600")
	}
	if err := privateOwner(info); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot open private credential input")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("credential input changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(data)) > maximum {
		return nil, fmt.Errorf("cannot read bounded credential input")
	}
	return data, nil
}

func Load(path string) (Profile, error) {
	data, err := privateRead(path, 8192)
	if err != nil {
		return Profile{}, err
	}
	var profile Profile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&profile) != nil {
		return Profile{}, fmt.Errorf("invalid signing profile")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return Profile{}, fmt.Errorf("invalid signing profile")
	}
	return profile, profile.Validate()
}

// Store writes references and public metadata only. It does not read the bootstrap.
func ProfilePath(directory, name string) (string, error) {
	if name == "" || name == "default" {
		return filepath.Join(directory, "profiles.local.json"), nil
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,47}$`).MatchString(name) {
		return "", fmt.Errorf("invalid credential profile name")
	}
	return filepath.Join(directory, "profiles."+name+".local.json"), nil
}

func Store(directory string, profile Profile, names ...string) error {
	if err := profile.Validate(); err != nil {
		return err
	}
	name := ""
	if len(names) > 1 {
		return fmt.Errorf("select one credential profile")
	}
	if len(names) == 1 {
		name = names[0]
	}
	path, err := ProfilePath(directory, name)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(directory) || outsideRepository(path) != nil {
		return fmt.Errorf("signing state must be an absolute path outside source repositories")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return fmt.Errorf("cannot prepare signing state")
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil || resolved != filepath.Clean(directory) || privateDirectory(directory) != nil {
		return fmt.Errorf("signing state must be a private owned directory without symlinks")
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || privateOwner(info) != nil {
			return fmt.Errorf("signing profile storage is unsafe")
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("cannot inspect signing profile storage")
	}
	data, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return fmt.Errorf("cannot encode signing profile")
	}
	temporary, err := os.CreateTemp(directory, ".signing-profile-")
	if err != nil {
		return fmt.Errorf("cannot save signing profile")
	}
	defer os.Remove(temporary.Name())
	_, writeErr := temporary.Write(append(data, '\n'))
	syncErr := temporary.Sync()
	closeErr := temporary.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil || os.Rename(temporary.Name(), path) != nil {
		return fmt.Errorf("cannot save signing profile")
	}
	return nil
}

type Command func(context.Context, io.Reader, io.Writer, ...string) error
type Resolver struct {
	Profile Profile
	Image   string
	Run     Command
}

// The bearer token is supplied over stdin, never Docker arguments or Config.Env.
// Only the official CLI child receives OP_SERVICE_ACCOUNT_TOKEN. Both its cache
// and temporary key live in tmpfs; errors are suppressed rather than redacted.
const helper = `set -eu
umask 077
mkdir -p /tmp/sdlc-op
export HOME=/tmp/sdlc-op OP_CONFIG_DIR=/tmp/sdlc-op/.op
unset OP_CONNECT_HOST OP_CONNECT_TOKEN
IFS= read -r OP_SERVICE_ACCOUNT_TOKEN
IFS= read -r reference
export OP_SERVICE_ACCOUNT_TOKEN
trap 'rm -f /tmp/sdlc-op/key' EXIT
op read --out-file /tmp/sdlc-op/key --file-mode 0600 "$reference" >/dev/null 2>/dev/null
unset OP_SERVICE_ACCOUNT_TOKEN
cat /tmp/sdlc-op/key
`

func localCommand(ctx context.Context, input io.Reader, output io.Writer, args ...string) (resultErr error) {
	name := ""
	for i, arg := range args {
		if arg == "--name" && i+1 < len(args) {
			name = args[i+1]
		}
	}
	if name == "" {
		return fmt.Errorf("signing credential container requires a fixed identity")
	}
	profile := ""
	for _, arg := range args {
		if strings.HasPrefix(arg, "io.sdlc.profile=") {
			profile = arg
		}
	}
	if profile == "" {
		return fmt.Errorf("signing credential container requires an ownership profile")
	}
	prior, err := exec.CommandContext(ctx, "docker", "ps", "--all", "--filter", "label=io.sdlc.kind=signing", "--filter", "label="+profile, "--format", "{{.Names}}").Output()
	if err != nil || len(bytes.TrimSpace(prior)) != 0 {
		return fmt.Errorf("an existing signing resolver requires inspection before retry")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		listed, err := exec.CommandContext(cleanup, "docker", "ps", "--all", "--filter", "name=^/"+name+"$", "--format", "{{.Names}}").Output()
		if err != nil {
			resultErr = fmt.Errorf("signing credential container cleanup could not be confirmed")
			return
		}
		if strings.TrimSpace(string(listed)) == name && exec.CommandContext(cleanup, "docker", "rm", "--force", name).Run() != nil {
			resultErr = fmt.Errorf("signing credential container cleanup failed")
		}
	}()
	command := exec.CommandContext(ctx, "docker", args...)
	command.Cancel = func() error {
		cleanup, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		return exec.CommandContext(cleanup, "docker", "stop", "--time", "3", name).Run()
	}
	command.WaitDelay = 10 * time.Second
	command.Stdin, command.Stdout, command.Stderr = input, output, io.Discard
	if command.Run() != nil {
		return fmt.Errorf("signing credential retrieval failed")
	}
	return nil
}

type boundedOutput struct {
	bytes.Buffer
	invalid bool
}

func (output *boundedOutput) Write(data []byte) (int, error) {
	if output.Len()+len(data) > 65536 {
		output.invalid = true
		return len(data), nil
	}
	return output.Buffer.Write(data)
}

func (resolver Resolver) Resolve(ctx context.Context) ([]byte, error) {
	image := resolver.Image
	if image == "" {
		image = Image
	} else if err := runtimepins.ValidateSigningImage(image); err != nil {
		return nil, err
	}
	if err := resolver.Profile.Validate(); err != nil {
		return nil, err
	}
	data, err := privateRead(resolver.Profile.BootstrapFile, 16384)
	if err != nil {
		return nil, err
	}
	defer clear(data)
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.ContainsAny(data, "\x00\r\n") {
		return nil, fmt.Errorf("bootstrap file must contain one service-account token")
	}
	input := make([]byte, 0, len(data)+len(resolver.Profile.Reference)+2)
	input = append(input, data...)
	input = append(input, '\n')
	input = append(input, resolver.Profile.Reference...)
	input = append(input, '\n')
	defer clear(input)
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var identifier [12]byte
	if _, err := rand.Read(identifier[:]); err != nil {
		return nil, fmt.Errorf("cannot identify signing credential container")
	}
	profile := sha256.Sum256([]byte(resolver.Profile.ID + "\n" + resolver.Profile.PublicKey))
	// The official image's named non-root account must match the CLI's user
	// lookup. A foreign numeric UID makes its private-directory checks fail.
	args := []string{"run", "--rm", "--name", "sdlc-signing-" + hex.EncodeToString(identifier[:]), "--label", "io.sdlc.managed=true", "--label", "io.sdlc.kind=signing", "--label", "io.sdlc.profile=" + hex.EncodeToString(profile[:]), "--interactive", "--pull", "never", "--network", "bridge", "--user", "opuser", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "64", "--memory", "256m", "--cpus", "1", "--log-driver", "none", "--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=64m,mode=1777"}
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "FTP_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "ftp_proxy", "no_proxy"} {
		args = append(args, "--env", name+"=")
	}
	// The timeout is PID 1 inside the container, independent of host cleanup.
	args = append(args, "--entrypoint", "/usr/bin/timeout", image, "--kill-after=5s", "35s", "/bin/sh", "-c", helper)
	run := resolver.Run
	if run == nil {
		run = localCommand
	}
	var output boundedOutput
	if run(ctx, bytes.NewReader(input), &output, args...) != nil || output.invalid || output.Len() == 0 {
		clear(output.Bytes())
		return nil, fmt.Errorf("signing credential retrieval failed; check vault access and bootstrap without printing credentials")
	}
	key := append([]byte(nil), output.Bytes()...)
	clear(output.Bytes())
	return key, nil
}
