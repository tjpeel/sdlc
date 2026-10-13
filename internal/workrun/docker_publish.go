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

	"github.com/tjpeel/sdlc/internal/buildinfo"
	"github.com/tjpeel/sdlc/internal/controllerasset"
	"github.com/tjpeel/sdlc/internal/githubauth"
	"github.com/tjpeel/sdlc/internal/headroom"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
)

// PublisherRequest contains public frozen settings only; key material uses stdin.
type PublisherRequest struct {
	PushRepair     *PushProtectionRepair `json:"push_repair,omitempty"`
	Action         string                `json:"action"`
	Plan           Plan                  `json:"plan"`
	Previous       Publication           `json:"previous"`
	ExpectedHead   string                `json:"expected_head"`
	ExpectedTree   string                `json:"expected_tree"`
	SnapshotBranch string                `json:"snapshot_branch,omitempty"`
}
type PublisherResponse struct {
	HistoryIncomplete *HistoricalCleanupIncomplete `json:"history_incomplete,omitempty"`
	PushProtection    *PushProtectionRejection     `json:"push_protection,omitempty"`
	Diagnostic        *PublicationDiagnostic       `json:"diagnostic,omitempty"`
	Publication       Publication                  `json:"publication"`
	Checks            CIResult                     `json:"checks"`
	Snapshot          BranchSnapshot               `json:"snapshot"`
	RemotePR          RemotePR                     `json:"remote_pr"`
	Error             string                       `json:"error,omitempty"`
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

func (runner publisherCommandRunner) Run(ctx context.Context, input io.Reader, output io.Writer, args ...string) error {
	return runner.RunDiagnostic(ctx, input, output, io.Discard, args...)
}
func (publisherCommandRunner) RunDiagnostic(ctx context.Context, input io.Reader, output, diagnostic io.Writer, args ...string) error {
	command := exec.CommandContext(ctx, "docker", args...)
	command.Stdin, command.Stdout = input, output
	command.Stderr = diagnostic
	if err := command.Run(); err != nil {
		return fmt.Errorf("isolated publisher failed: %w", err)
	}
	return nil
}

var stagePublisherController = controllerasset.Stage
var controllerIdentityRunner PublisherCommandRunner = publisherCommandRunner{}

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

// PublishDerived uses the same isolated linear signing and exact tree proof as
// normal publication, without creating or updating any pull request.
func (publisher DockerPublisher) PublishDerived(ctx context.Context, plan Plan, directory string, revision Revision, evidence CheckEvidence) (Publication, error) {
	if plan.Derived == nil || !evidence.Passed || evidence.Head != revision.Head || evidence.Tree != revision.Tree || !revision.Clean {
		return Publication{}, fmt.Errorf("derived publication requires passing source evidence")
	}
	response, err := publisher.invoke(ctx, directory, PublisherRequest{Action: "publish", Plan: plan, ExpectedHead: revision.Head, ExpectedTree: revision.Tree})
	if err == nil && (response.Publication.BaseSHA != plan.BaseSHA || !objectID.MatchString(response.Publication.HeadSHA)) {
		return Publication{}, fmt.Errorf("invalid derived publication revision")
	}
	return response.Publication, err
}

func (publisher DockerPublisher) Publish(ctx context.Context, plan Plan, workspace, directory string, previous Publication, output io.Writer) (Publication, error) {
	if err := ValidatePRMetadata(plan); err != nil {
		return Publication{}, err
	}
	journal, err := Load(directory)
	if err != nil || !journal.Evidence.Passed || !objectID.MatchString(journal.Evidence.Tree) || !objectID.MatchString(journal.Evidence.Head) {
		return Publication{}, fmt.Errorf("publication requires retained passing check evidence")
	}
	revision, err := (DockerRepository{Runtime: publisher.Runtime, ImageID: publisher.ImageID}).Inspect(ctx, workspace)
	if err != nil || !revision.Clean || revision.Tree != journal.Evidence.Tree || revision.Head != journal.Evidence.Head {
		return Publication{}, fmt.Errorf("source changed after verification")
	}
	if err := validateRejectedBundle(directory, journal.PushRepair); err != nil {
		return Publication{}, err
	}
	response, err := publisher.invoke(ctx, directory, PublisherRequest{PushRepair: journal.PushRepair, Action: "publish", Plan: plan, Previous: previous, ExpectedHead: revision.Head, ExpectedTree: revision.Tree})
	if err == nil && output != nil {
		fmt.Fprintln(output, "Draft PR:", response.Publication.URL)
	}
	return response.Publication, err
}

// Checks runs the current controller's collector with the frozen image's official
// gh client. Old publisher binaries do not determine diagnostic capabilities.
func (publisher DockerPublisher) Checks(ctx context.Context, plan Plan, previous Publication) (result CIResult, resultErr error) {
	frozen := plan.PublicationIdentity
	if frozen == nil {
		return CIResult{}, fmt.Errorf("CI inspection requires frozen publication identity")
	}
	if err := frozen.Validate(); err != nil {
		return CIResult{}, err
	}
	if publisher.ValidatePair != nil {
		if err := publisher.ValidatePair(); err != nil {
			return CIResult{}, err
		}
	}
	if publisher.Runtime.Docker == nil || publisher.ImageID == "" {
		return CIResult{}, fmt.Errorf("CI inspection requires pinned runtime")
	}
	state, err := publisher.Runtime.Status(ctx)
	if err != nil || state.ImageID != publisher.ImageID {
		return CIResult{}, fmt.Errorf("CI runtime differs from pinned image")
	}
	session, err := publisher.Auth.Acquire(ctx)
	if err != nil {
		return CIResult{}, fmt.Errorf("GitHub stored login unavailable")
	}
	defer func() {
		if session.Close() != nil {
			result = CIResult{}
			resultErr = errors.Join(resultErr, fmt.Errorf("CI credential lease cleanup failed"))
		}
	}()
	if publisher.ValidatePair != nil {
		if err := publisher.ValidatePair(); err != nil {
			return CIResult{}, err
		}
	}
	profile, profileErr := githubauth.NormalizeProfile(plan.GitHubProfile)
	frozenProfile, frozenErr := githubauth.NormalizeProfile(frozen.GitHubProfile)
	identity, err := session.Identity(ctx)
	if err != nil || profileErr != nil || frozenErr != nil || profile != frozenProfile || session.Profile != frozenProfile || session.ImageID != publisher.ImageID || session.Volume != frozen.GitHubVolume || identity.ID != frozen.GitHubID || identity.Login != frozen.GitHubLogin {
		return CIResult{}, fmt.Errorf("CI GitHub session differs from frozen publication identity")
	}
	collector := GitHubPublisher{Frozen: frozen, Command: func(ctx context.Context, name string, argv ...string) ([]byte, error) {
		if name != "gh" || len(argv) < 1 || argv[0] != "api" && argv[0] != "pr" {
			return nil, fmt.Errorf("unsupported CI controller command")
		}
		return publisher.ciControllerCommand(ctx, session.Volume, argv)
	}}
	result, err = collector.Checks(ctx, plan, previous)
	if err != nil {
		return CIResult{}, err
	}
	if err := validateCIResult(result, previous); err != nil {
		return CIResult{}, err
	}
	return result, nil
}

func (publisher DockerPublisher) ciControllerCommand(ctx context.Context, volume string, argv []string) (data []byte, commandErr error) {
	id, err := NewID()
	if err != nil {
		return nil, fmt.Errorf("cannot allocate CI controller")
	}
	name := "sdlc-ci-" + id
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		listed, err := publisher.Runtime.Docker.Output(cleanup, "ps", "--all", "--filter", "name=^/"+name+"$", "--format", "{{.Names}}")
		if err != nil {
			data = nil
			commandErr = errors.Join(commandErr, fmt.Errorf("CI controller cleanup inspection failed"))
			return
		}
		if strings.TrimSpace(string(listed)) == name {
			if _, err := publisher.Runtime.Docker.Output(cleanup, "rm", "--force", name); err != nil {
				data = nil
				commandErr = errors.Join(commandErr, fmt.Errorf("CI controller cleanup failed"))
			}
		}
	}()
	args := ciControllerArguments(publisher.ImageID, name, volume)
	args = append(args, argv...)
	runner := publisher.Runner
	if runner == nil {
		runner = publisherCommandRunner{}
	}
	var output ciAPIOutput
	err = runner.Run(ctx, nil, &output, args...)
	if output.exceeded {
		return nil, fmt.Errorf("CI controller response exceeds bound")
	}
	return output.buffer.Bytes(), err
}

func ciControllerArguments(image, name, volume string) []string {
	installation := strings.TrimSuffix(strings.TrimPrefix(volume, "sdlc-github-auth-"), "-github")
	args := checkContainerEnvironment([]string{"run", "--name", name, "--label", "io.sdlc.managed=true", "--label", "io.sdlc.kind=github-auth", "--label", "io.sdlc.provider=github", "--label", "io.sdlc.installation=" + installation, "--pull", "never", "--network", "bridge", "--user", "1000:1000", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "128", "--memory", "2g", "--cpus", "2", "--log-driver", "none", "--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=64m,mode=1777", "--mount", "type=volume,src=" + volume + ",dst=/github-auth,readonly,volume-nocopy", "--env", "HOME=/tmp", "--env", "GH_CONFIG_DIR=/github-auth", "--env", "GH_HOST=github.com", "--env", "GH_NO_UPDATE_NOTIFIER=1", "--env", "GH_NO_EXTENSION_UPDATE_NOTIFIER=1", "--env", "GH_PROMPT_DISABLED=1"})
	return append(args, "--entrypoint", "/usr/local/bin/gh", image)
}

type boundedPublisherOutput struct {
	buffer   bytes.Buffer
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

func (output *boundedPublisherOutput) Len() int { return output.buffer.Len() }

func (output *boundedPublisherOutput) Write(data []byte) (int, error) {
	if output.buffer.Len()+len(data) > 1024*1024 {
		output.exceeded = true
		return len(data), nil
	}
	return output.buffer.Write(data)
}
func (publisher DockerPublisher) invoke(ctx context.Context, directory string, request PublisherRequest) (result PublisherResponse, resultErr error) {
	// This runs after credential/container/request/controller cleanup. A receipt
	// is durable only when the typed rejection is the sole remaining error.
	defer func() { result, resultErr = finishPushAuthority(ctx, directory, result, resultErr) }()
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
	// A new attempt must never expose a retained diagnostic from an earlier refusal.
	directory, canonicalErr := filepath.Abs(directory)
	if canonicalErr != nil || realDirectory(directory) != nil || privatePublisherParent(directory) != nil {
		return fail("invalid private publisher directory")
	}
	if request.Action == "publish" {
		var pendingReceipt *PushProtectionRejection
		if request.PushRepair != nil {
			pendingReceipt = request.PushRepair.Pending
		}
		if _, err := loadPushReceipt(directory, request.Plan, request.Previous, request.ExpectedHead, request.ExpectedTree, pendingReceipt); err != nil {
			return fail(err.Error())
		}
	}

	previousDiagnostic := filepath.Join(directory, "publisher")
	if _, err := os.Lstat(previousDiagnostic); err == nil {
		if realDirectory(previousDiagnostic) != nil {
			return fail("invalid publisher state")
		}
		if err = os.Remove(filepath.Join(previousDiagnostic, "push-rejection-pending.json")); err != nil && !os.IsNotExist(err) {
			return fail("cannot invalidate previous provisional push receipt")
		}
		if err = os.Remove(filepath.Join(previousDiagnostic, "push-rejection.json")); err != nil && !os.IsNotExist(err) {
			return fail("cannot invalidate previous push receipt")
		}
		if err = os.Remove(filepath.Join(previousDiagnostic, "failure.json")); err != nil && !os.IsNotExist(err) {
			return fail("cannot invalidate previous publisher diagnostic")
		}
	}

	if publisher.Runtime.Docker == nil || publisher.ImageID == "" {
		return fail("publication requires pinned Docker runtime and GitHub credential volume")
	}
	state, err := publisher.Runtime.Status(ctx)
	if err != nil || state.ImageID != publisher.ImageID {
		return fail("publisher runtime differs from pinned image")
	}
	platformBytes, platformErr := publisher.Runtime.Docker.Output(ctx, "image", "inspect", "--format", "{{.Os}}/{{.Architecture}}", publisher.ImageID)
	platform := strings.TrimSpace(string(platformBytes))
	if platformErr != nil || platform != "linux/amd64" && platform != "linux/arm64" {
		return fail("publisher runtime platform unavailable or unsupported")
	}
	helperPath, cleanupHelper, err := stagePublisherController(platform)
	if err != nil {
		return fail("trusted current publisher controller unavailable; reinstall SDLC CLI with matching Linux assets")
	}
	defer func() {
		if cleanupHelper() != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("trusted publisher controller cleanup failed"))
		}
	}()
	if err := publisher.verifyController(ctx, helperPath, platform); err != nil {
		return fail("trusted publisher controller provenance or frozen runtime execution mismatch; reinstall SDLC CLI")
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
		defer clear(secret)
		if err != nil || len(secret) == 0 || len(secret) > 65536 {
			return fail("publication signing key unavailable")
		}

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
		if request.PushRepair != nil && request.PushRepair.Pending != nil {
			if err := validateRejectedBundle(directory, request.PushRepair); err != nil {
				return fail("original rejected bundle unavailable or changed")
			}
			rejectedPath := filepath.Join(requestDirectory, "rejected.bundle")
			if err := stagePublisherBundle(filepath.Join(private, request.PushRepair.Pending.KnownAuditBundle), rejectedPath); err != nil {
				return fail("cannot stage original rejected bundle")
			}
			digest, err := rejectedBundleHash(rejectedPath)
			if err != nil || digest != request.PushRepair.Pending.KnownAuditSHA256 {
				return fail("staged rejected bundle hash mismatch")
			}
			args = append(args, "--mount", mount(rejectedPath, "/rejected.bundle", true))
		}

	}
	args = append(args, "--mount", mount(helperPath, "/trusted/sdlc-publisher", true), "--entrypoint", "/trusted/sdlc-publisher", publisher.ImageID)
	runner := publisher.Runner
	if runner == nil {
		runner = publisherCommandRunner{}
	}
	var output boundedPublisherOutput
	if realDirectory(directory) != nil || privatePublisherParent(directory) != nil || realDirectory(private) != nil {
		return fail("publisher directory boundary changed")
	}
	var diagnostic boundedPublisherDiagnostic
	var runErr error
	if diagnosticRunner, ok := runner.(interface {
		RunDiagnostic(context.Context, io.Reader, io.Writer, io.Writer, ...string) error
	}); ok {
		runErr = diagnosticRunner.RunDiagnostic(ctx, clearingKeyReader{bytes.NewReader(secret), secret}, &output, &diagnostic, args...)
	} else {
		runErr = runner.Run(ctx, clearingKeyReader{bytes.NewReader(secret), secret}, &output, args...)
	}
	defer clear(diagnostic.buffer.Bytes())
	if runErr != nil {
		return fail("isolated publisher failed")
	}
	var response PublisherResponse
	decoder := json.NewDecoder(bytes.NewReader(output.buffer.Bytes()))
	decoder.DisallowUnknownFields()
	if output.exceeded || decoder.Decode(&response) != nil {
		return fail("invalid isolated publisher response")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return fail("invalid isolated publisher response")
	}
	if response.Error == "" && (response.HistoryIncomplete != nil || response.PushProtection != nil || response.Diagnostic != nil || diagnostic.buffer.Len() != 0 || diagnostic.exceeded) {
		return fail("unexpected isolated publisher diagnostic response")
	}
	if response.HistoryIncomplete != nil {
		var prior *PushProtectionRejection
		if request.PushRepair != nil {
			prior = request.PushRepair.Pending
		}
		if request.Action != "publish" || response.Error != "historical cleanup incomplete" || response.PushProtection != nil || response.Diagnostic != nil || diagnostic.buffer.Len() != 0 || diagnostic.exceeded || validateHistoryPacket(response.HistoryIncomplete, prior, request.Plan, request.Previous, request.ExpectedHead, request.ExpectedTree) != nil {
			return fail("invalid history cleanup response")
		}
		return PublisherResponse{}, response.HistoryIncomplete
	}

	if response.Error != "" {
		if diagnostic.exceeded || response.Diagnostic == nil {
			return fail("isolated publication refused; diagnostic unavailable")
		}
		if response.PushProtection != nil {
			if request.Action != "publish" || validatePushPacket(response.PushProtection, request.Plan, request.ExpectedHead, request.ExpectedTree) != nil || validatePushPublication(response.PushProtection, request.Previous) != nil || response.Diagnostic == nil || response.Diagnostic.Stage != "command" || response.Diagnostic.Code != "command_failed" || retainProvisionalPushReceipt(directory, response.PushProtection, response.Diagnostic.SHA256) != nil {
				return fail("invalid provisional push protection response")
			}
		}
		if response.Error != "publisher refused request" || response.Diagnostic == nil || retainPublicationDiagnostic(directory, *response.Diagnostic, diagnostic.buffer.Bytes()) != nil {
			return fail("isolated publication refused; diagnostic unavailable; inspect frozen identity, source, signing and remote state")
		}
		if response.PushProtection != nil {
			if request.Action != "publish" || response.Diagnostic.Stage != "command" || response.Diagnostic.Code != "command_failed" || validatePushPacket(response.PushProtection, request.Plan, request.ExpectedHead, request.ExpectedTree) != nil || validatePushPublication(response.PushProtection, request.Previous) != nil {
				return fail("invalid push protection response")
			}
			return PublisherResponse{}, response.PushProtection
		}

		return fail(fmt.Sprintf("isolated publication refused at %s (%s); inspect private diagnostic %s", response.Diagnostic.Stage, response.Diagnostic.Code, filepath.Join(private, "failure.stderr")))
	}
	if request.Action == "publish" && request.Plan.Derived == nil && (response.Publication.Number < 1 || response.Publication.BaseSHA != request.Plan.BaseSHA || !objectID.MatchString(response.Publication.HeadSHA) || !strings.HasPrefix(response.Publication.URL, "https://github.com/"+request.Plan.Repository+"/pull/")) {
		return fail("invalid isolated publication result")
	}
	if request.Action == "checks" {
		if err := validateCIResult(response.Checks, request.Previous); err != nil {
			return fail("invalid isolated CI diagnostics")
		}
	}

	return response, nil
}

// Sidecar pins belong to the host controller. Keep the publisher wire format
// compatible with frozen images, and exclude paths and inputs it never needs.
func publisherRequestForContainer(request PublisherRequest) PublisherRequest {
	request.Plan.Headroom = headroom.Config{}
	request.Plan.Root = ""
	request.Plan.Ticket = ""
	request.Plan.Inputs = nil
	request.Plan.CheckInputs = nil
	request.Plan.Checks = nil
	request.Plan.SigningImage = ""
	request.Plan.DaemonImage = ""
	request.Plan.DaemonMode = ""
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

type boundedPublisherDiagnostic struct {
	buffer   bytes.Buffer
	exceeded bool
}

func (output *boundedPublisherDiagnostic) Write(data []byte) (int, error) {
	n := len(data)
	remaining := MaximumPublicationDiagnostic - output.buffer.Len()
	if len(data) > remaining {
		data = data[:remaining]
		output.exceeded = true
	}
	_, _ = output.buffer.Write(data)
	return n, nil
}

func (publisher DockerPublisher) verifyController(ctx context.Context, path, platform string) (resultErr error) {
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	id, err := NewID()
	if err != nil {
		return fmt.Errorf("cannot allocate trusted controller identity probe")
	}
	name := "sdlc-controller-proof-" + id
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		listed, err := publisher.Runtime.Docker.Output(cleanup, "ps", "--all", "--filter", "name=^/"+name+"$", "--format", "{{.Names}}")
		if err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("controller identity probe cleanup inspection failed"))
			return
		}
		if strings.TrimSpace(string(listed)) == name {
			if _, err := publisher.Runtime.Docker.Output(cleanup, "rm", "--force", name); err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("controller identity probe cleanup failed"))
			}
		}
	}()
	args := []string{"run", "--name", name, "--rm", "--pull", "never", "--network", "none", "--user", "1000:1000", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--pids-limit", "16", "--memory", "128m", "--log-driver", "none", "--mount", mount(path, "/trusted/sdlc-publisher", true), "--entrypoint", "/trusted/sdlc-publisher", publisher.ImageID, "--identity"}
	var output controllerIdentityOutput
	if err := controllerIdentityRunner.Run(bounded, nil, &output, args...); err != nil || output.exceeded {
		return fmt.Errorf("trusted controller identity probe failed")
	}
	var identity buildinfo.Identity
	decoder := json.NewDecoder(bytes.NewReader(output.buffer.Bytes()))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&identity) != nil || decoder.Decode(new(any)) != io.EOF {
		return fmt.Errorf("invalid trusted controller identity")
	}
	expected := buildinfo.Current()
	if identity.Version != expected.Version || identity.Revision != expected.Revision || identity.Dirty != expected.Dirty || identity.OS != "linux" || "linux/"+identity.Arch != platform {
		return fmt.Errorf("trusted controller identity mismatch")
	}
	return nil
}

type controllerIdentityOutput struct {
	buffer   bytes.Buffer
	exceeded bool
}

func (output *controllerIdentityOutput) Write(data []byte) (int, error) {
	n := len(data)
	remaining := 4096 - output.buffer.Len()
	if len(data) > remaining {
		data = data[:remaining]
		output.exceeded = true
	}
	_, _ = output.buffer.Write(data)
	return n, nil
}
