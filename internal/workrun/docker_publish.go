package workrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/tjpeel/sdlc/internal/githubauth"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
)

// PublisherRequest contains public frozen settings only; key material uses stdin.
type PublisherRequest struct {
	Action         string      `json:"action"`
	Plan           Plan        `json:"plan"`
	Previous       Publication `json:"previous"`
	ExpectedHead   string      `json:"expected_head"`
	ExpectedTree   string      `json:"expected_tree"`
	SnapshotBranch string      `json:"snapshot_branch,omitempty"`
}
type PublisherResponse struct {
	Publication Publication    `json:"publication"`
	Checks      CIResult       `json:"checks"`
	Snapshot    BranchSnapshot `json:"snapshot"`
	RemotePR    RemotePR       `json:"remote_pr"`
	Error       string         `json:"error,omitempty"`
}

// Snapshot exports one verified remote branch into caller-private state.
func (publisher DockerPublisher) Snapshot(ctx context.Context, plan Plan, branch, expectedSHA, directory string) (BranchSnapshot, error) {
	if _, err := isolatedGit(ctx, directory, "check-ref-format", "--branch", branch); err != nil {
		return BranchSnapshot{}, fmt.Errorf("invalid snapshot branch")
	}
	response, err := publisher.invoke(ctx, directory, PublisherRequest{Action: "snapshot", Plan: plan, ExpectedHead: expectedSHA, SnapshotBranch: branch})
	if err != nil {
		return BranchSnapshot{}, err
	}
	path := filepath.Join(directory, "publisher", "snapshot.bundle")
	if err := validateSnapshotBundle(path); err != nil {
		return BranchSnapshot{}, err
	}
	if response.Snapshot.Branch != branch || !objectID.MatchString(response.Snapshot.SHA) || (expectedSHA != "" && response.Snapshot.SHA != expectedSHA) {
		return BranchSnapshot{}, fmt.Errorf("invalid isolated snapshot result")
	}
	return BranchSnapshot{Branch: branch, SHA: response.Snapshot.SHA, Bundle: path}, nil
}

// Branch observes a remote branch without creating a bundle. Schedulers use
// it for inexpensive base polling; callers take a Snapshot only before work.
func (publisher DockerPublisher) Branch(ctx context.Context, plan Plan, branch string) (BranchSnapshot, error) {
	if _, err := isolatedGit(ctx, os.TempDir(), "check-ref-format", "--branch", branch); err != nil {
		return BranchSnapshot{}, fmt.Errorf("invalid snapshot branch")
	}
	directory, err := os.MkdirTemp("", "sdlc-publisher-branch-")
	if err != nil {
		return BranchSnapshot{}, fmt.Errorf("cannot prepare publisher request")
	}
	defer os.RemoveAll(directory)
	directory, err = filepath.EvalSymlinks(directory)
	if err != nil {
		return BranchSnapshot{}, fmt.Errorf("cannot resolve publisher request directory")
	}
	response, err := publisher.invoke(ctx, directory, PublisherRequest{Action: "branch", Plan: plan, SnapshotBranch: branch})
	if err != nil {
		return BranchSnapshot{}, err
	}
	if response.Snapshot.Branch != branch || !objectID.MatchString(response.Snapshot.SHA) || response.Snapshot.Bundle != "" {
		return BranchSnapshot{}, fmt.Errorf("invalid isolated branch result")
	}
	return BranchSnapshot{Branch: branch, SHA: response.Snapshot.SHA}, nil
}

func (publisher DockerPublisher) Observe(ctx context.Context, plan Plan, publication Publication) (RemotePR, error) {
	if publication.Number < 1 {
		return RemotePR{}, fmt.Errorf("publication number is required")
	}
	directory, err := os.MkdirTemp("", "sdlc-publisher-observe-")
	if err != nil {
		return RemotePR{}, fmt.Errorf("cannot prepare publisher request")
	}
	defer os.RemoveAll(directory)
	directory, err = filepath.EvalSymlinks(directory)
	if err != nil {
		return RemotePR{}, fmt.Errorf("cannot resolve publisher request directory")
	}
	response, err := publisher.invoke(ctx, directory, PublisherRequest{Action: "observe", Plan: plan, Previous: publication})
	if err != nil {
		return RemotePR{}, err
	}
	remote := response.RemotePR
	if remote.State != "OPEN" && remote.State != "MERGED" && remote.State != "CLOSED" || remote.Base == "" || !objectID.MatchString(remote.HeadSHA) || (remote.BaseSHA != "" && !objectID.MatchString(remote.BaseSHA)) || (remote.MergeSHA != "" && !objectID.MatchString(remote.MergeSHA)) {
		return RemotePR{}, fmt.Errorf("invalid isolated observation result")
	}
	return remote, nil
}

func validateSnapshotBundle(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maximumBundleBytes {
		return fmt.Errorf("invalid snapshot bundle")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || opened.Size() != info.Size() {
		return fmt.Errorf("snapshot bundle changed")
	}
	return nil
}

type PublisherCommandRunner interface {
	Run(context.Context, io.Reader, io.Writer, ...string) error
}
type publisherCommandRunner struct{}

func (publisherCommandRunner) Run(ctx context.Context, input io.Reader, output io.Writer, args ...string) error {
	command := exec.CommandContext(ctx, "docker", args...)
	command.Stdin, command.Stdout = input, output
	command.Stderr = io.Discard
	if command.Run() != nil {
		return fmt.Errorf("isolated publisher failed")
	}
	return nil
}

type DockerPublisher struct {
	Runtime          runtimeimage.Manager
	ImageID          string
	Auth             githubauth.Manager
	CredentialVolume string
	SigningKey       func(context.Context) ([]byte, error)
	// ValidatePair rechecks host pairing metadata before any publication operation.
	ValidatePair func() error
	Runner       PublisherCommandRunner
}

func (publisher DockerPublisher) Publish(ctx context.Context, plan Plan, workspace, directory string, previous Publication, output io.Writer) (Publication, error) {
	journal, err := Load(directory)
	if err != nil || !journal.Evidence.Passed || !objectID.MatchString(journal.Evidence.Tree) || !objectID.MatchString(journal.Evidence.Head) {
		return Publication{}, fmt.Errorf("publication requires retained passing check evidence")
	}
	revision, err := (DockerRepository{Runtime: publisher.Runtime, ImageID: publisher.ImageID}).Inspect(ctx, workspace)
	if err != nil || !revision.Clean || revision.Tree != journal.Evidence.Tree || revision.Head != journal.Evidence.Head {
		return Publication{}, fmt.Errorf("source changed after verification")
	}
	response, err := publisher.invoke(ctx, directory, PublisherRequest{Action: "publish", Plan: plan, Previous: previous, ExpectedHead: revision.Head, ExpectedTree: revision.Tree})
	if err == nil && output != nil {
		fmt.Fprintln(output, "Draft PR:", response.Publication.URL)
	}
	return response.Publication, err
}

// Directory is the run's private directory, never a worker workspace.
func (publisher DockerPublisher) Checks(ctx context.Context, plan Plan, previous Publication) (CIResult, error) {
	directory, err := os.MkdirTemp("", "sdlc-publisher-checks-")
	if err != nil {
		return CIResult{}, fmt.Errorf("cannot prepare publisher request")
	}
	defer os.RemoveAll(directory)
	directory, err = filepath.EvalSymlinks(directory)
	if err != nil {
		return CIResult{}, fmt.Errorf("cannot resolve publisher request directory")
	}
	response, err := publisher.invoke(ctx, directory, PublisherRequest{Action: "checks", Plan: plan, Previous: previous})
	return response.Checks, err
}

type boundedPublisherOutput struct {
	bytes.Buffer
	exceeded bool
}

type clearingKeyReader struct {
	*bytes.Reader
	key []byte
}

func (reader clearingKeyReader) Read(data []byte) (int, error) {
	n, err := reader.Reader.Read(data)
	if reader.Reader.Len() == 0 {
		clear(reader.key)
	}
	return n, err
}

func (output *boundedPublisherOutput) Write(data []byte) (int, error) {
	if output.Len()+len(data) > 1024*1024 {
		output.exceeded = true
		return len(data), nil
	}
	return output.Buffer.Write(data)
}
func (publisher DockerPublisher) invoke(ctx context.Context, directory string, request PublisherRequest) (result PublisherResponse, resultErr error) {
	fail := func(message string) (PublisherResponse, error) { return PublisherResponse{}, fmt.Errorf("%s", message) }
	if request.Plan.PublicationIdentity == nil {
		return fail("run lacks frozen publication identity; create a new run")
	}
	if err := request.Plan.PublicationIdentity.Validate(); err != nil {
		return PublisherResponse{}, err
	}
	if publisher.ValidatePair != nil {
		if err := publisher.ValidatePair(); err != nil {
			return PublisherResponse{}, err
		}
	}
	if publisher.Runtime.Docker == nil || publisher.ImageID == "" {
		return fail("publication requires pinned Docker runtime and GitHub credential volume")
	}
	state, err := publisher.Runtime.Status(ctx)
	if err != nil || state.ImageID != publisher.ImageID {
		return fail("publisher runtime differs from pinned image")
	}
	session, err := publisher.Auth.Acquire(ctx)
	if err != nil {
		return fail("GitHub stored login unavailable")
	}
	defer func() {
		if session.Close() != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("GitHub publisher lease cleanup failed"))
		}
	}()
	// Pairing can be replaced while acquisition waits. The native auth lease
	// now prevents the pairing wizard from changing it during publication.
	if publisher.ValidatePair != nil {
		if err := publisher.ValidatePair(); err != nil {
			return PublisherResponse{}, err
		}
	}
	identity, err := session.Identity(ctx)
	frozen := request.Plan.PublicationIdentity
	frozenProfile, profileErr := githubauth.NormalizeProfile(frozen.GitHubProfile)
	planProfile, planErr := githubauth.NormalizeProfile(request.Plan.GitHubProfile)
	if err != nil || profileErr != nil || planErr != nil || planProfile != frozenProfile || session.Profile != frozenProfile || session.ImageID != publisher.ImageID || session.Volume != frozen.GitHubVolume || identity.ID != frozen.GitHubID || identity.Login != frozen.GitHubLogin {
		return fail("GitHub session differs from frozen publication identity")
	}
	publisher.CredentialVolume = session.Volume
	directory, err = filepath.Abs(directory)
	if err != nil || realDirectory(directory) != nil {
		return fail("invalid publisher directory")
	}
	if err := privatePublisherParent(directory); err != nil {
		return fail("publisher parent must be owned by this user with mode 0700")
	}
	request = publisherRequestForContainer(request)
	requestDirectory, err := os.MkdirTemp(directory, "publisher-request-")
	if err != nil {
		return fail("cannot prepare publisher request")
	}
	defer func() {
		if os.RemoveAll(requestDirectory) != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("publisher request cleanup failed"))
		}
	}()
	requestPath := filepath.Join(requestDirectory, "request.json")
	if err := saveJSON(requestPath, request); err != nil {
		return fail("cannot write publisher request")
	}
	if err := os.Chmod(requestPath, 0644); err != nil {
		return fail("cannot expose public publisher request")
	}
	private := filepath.Join(directory, "publisher")
	if err := os.MkdirAll(private, 0700); err != nil || realDirectory(private) != nil {
		return fail("invalid publisher state")
	}
	if err := os.Chmod(private, 0777); err != nil {
		return fail("cannot prepare isolated publisher state")
	}
	var secret []byte
	if request.Action == "publish" {
		info, err := os.Lstat(filepath.Join(directory, "source.bundle"))
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 2*1024*1024*1024 {
			return fail("publication requires an exported bundle")
		}
		if publisher.SigningKey == nil {
			return fail("publication requires external signing key resolver")
		}
		secret, err = publisher.SigningKey(ctx)
		if err != nil || len(secret) == 0 || len(secret) > 65536 {
			return fail("publication signing key unavailable")
		}
		defer func() {
			for i := range secret {
				secret[i] = 0
			}
		}()
	}
	nameID, err := NewID()
	if err != nil {
		return fail("cannot allocate publisher")
	}
	name := "sdlc-publisher-" + nameID
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		listed, err := publisher.Runtime.Docker.Output(cleanup, "ps", "--all", "--filter", "name=^/"+name+"$", "--format", "{{.Names}}")
		if err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("publisher cleanup inspection failed"))
			return
		}
		if strings.TrimSpace(string(listed)) == name {
			if _, err := publisher.Runtime.Docker.Output(cleanup, "rm", "--force", name); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("publisher container cleanup failed"))
			}
		}
	}()
	args := publisherArguments(publisher.ImageID, name, publisher.CredentialVolume, requestPath, private)
	if request.Action == "publish" {
		bundlePath := filepath.Join(requestDirectory, "source.bundle")
		if err := stagePublisherBundle(filepath.Join(directory, "source.bundle"), bundlePath); err != nil {
			return fail("cannot stage exported bundle")
		}
		args = append(args, "--mount", mount(bundlePath, "/source.bundle", true))
	}
	args = append(args, "--entrypoint", "/usr/local/bin/sdlc-publisher", publisher.ImageID)
	runner := publisher.Runner
	if runner == nil {
		runner = publisherCommandRunner{}
	}
	var output boundedPublisherOutput
	if realDirectory(directory) != nil || privatePublisherParent(directory) != nil || realDirectory(private) != nil {
		return fail("publisher directory boundary changed")
	}
	if err := runner.Run(ctx, clearingKeyReader{bytes.NewReader(secret), secret}, &output, args...); err != nil {
		return fail("isolated publisher failed")
	}
	var response PublisherResponse
	decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
	decoder.DisallowUnknownFields()
	if output.exceeded || decoder.Decode(&response) != nil {
		return fail("invalid isolated publisher response")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return fail("invalid isolated publisher response")
	}
	if response.Error != "" {
		return fail("isolated publication refused; check frozen identity, source, signing and remote state")
	}
	if request.Action == "publish" && (response.Publication.Number < 1 || response.Publication.BaseSHA != request.Plan.BaseSHA || !objectID.MatchString(response.Publication.HeadSHA) || !strings.HasPrefix(response.Publication.URL, "https://github.com/"+request.Plan.Repository+"/pull/")) {
		return fail("invalid isolated publication result")
	}
	return response, nil
}

// Sidecar pins belong to the host controller. Keep the publisher wire format
// compatible with frozen images, and exclude paths and inputs it never needs.
func publisherRequestForContainer(request PublisherRequest) PublisherRequest {
	request.Plan.Root = ""
	request.Plan.Ticket = ""
	request.Plan.Inputs = nil
	request.Plan.CheckInputs = nil
	request.Plan.Checks = nil
	request.Plan.SigningImage = ""
	request.Plan.DaemonImage = ""
	request.Plan.SigningProfile = ""
	return request
}

func stagePublisherBundle(path, destination string) error {
	const maximum = int64(2 * 1024 * 1024 * 1024)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maximum {
		return fmt.Errorf("invalid exported bundle")
	}
	source, err := os.Open(path)
	if err != nil {
		return err
	}
	defer source.Close()
	opened, err := source.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return fmt.Errorf("exported bundle changed while opening")
	}
	dest, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(dest, io.LimitReader(source, maximum+1))
	after, statErr := source.Stat()
	if copyErr != nil || statErr != nil || n != info.Size() || after.Size() != info.Size() || after.ModTime() != info.ModTime() {
		dest.Close()
		return fmt.Errorf("exported bundle changed during staging")
	}
	// The private request directory controls host traversal. The container's
	// distinct UID needs read access to the mounted public source bundle even
	// when the controller inherits a restrictive umask.
	modeErr := dest.Chmod(0644)
	closeErr := dest.Close()
	return errors.Join(modeErr, closeErr)
}

func publisherArguments(image, name, volume, requestPath, statePath string) []string {
	installation := strings.TrimPrefix(volume, "sdlc-github-auth-")
	installation = strings.TrimSuffix(installation, "-github")
	args := checkContainerEnvironment([]string{"run", "--name", name, "--label", "io.sdlc.managed=true", "--label", "io.sdlc.kind=github-auth", "--label", "io.sdlc.provider=github", "--label", "io.sdlc.installation=" + installation, "--interactive", "--pull", "never", "--network", "bridge", "--user", "1000:1000", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "128", "--memory", "2g", "--cpus", "2", "--log-driver", "none", "--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=256m,mode=1777", "--mount", mount(requestPath, "/request.json", true), "--mount", mount(statePath, "/publisher", false), "--mount", "type=volume,src=" + volume + ",dst=/github-auth,readonly,volume-nocopy"})
	return args
}
