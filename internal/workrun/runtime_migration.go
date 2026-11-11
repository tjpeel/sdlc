package workrun

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tjpeel/sdlc/internal/runtimeimage"
)

// RuntimeMigration records the old execution boundary; native transcripts and
// the previous checkpoint remain private beside the run workspace.
type RuntimeMigration struct {
	At                  time.Time          `json:"at"`
	PreviousImageID     string             `json:"previous_image_id"`
	PreviousRuntimeName string             `json:"previous_runtime_name"`
	PreviousSessionID   string             `json:"previous_session_id"`
	PreviousAttempt     int                `json:"previous_attempt"`
	PreviousProvenance  *RuntimeProvenance `json:"previous_provenance,omitempty"`
	SessionDirectory    string             `json:"session_directory"`
}

// PrepareRuntimeMigration is deliberately limited to stopped, unpublished
// implementation holds. The caller must own the controller lock, verify both
// runtime and frozen inputs, preserve the original journal, and save atomically.
func PrepareRuntimeMigration(original Journal, name string, target runtimeimage.State) (Journal, error) {
	if err := runtimeimage.ValidateName(name); err != nil {
		return Journal{}, err
	}
	if original.State != "blocked" || original.ResumeState != "implementing" || original.Publication.Number != 0 || original.SessionID == "" || original.RunLimitOverride != nil || original.SessionTurnExtension != nil || original.PendingInputs != nil || original.CandidatePhasePending || original.PendingRole != "" || original.Reconciliation != nil || original.Plan.DependencyContext != nil || original.Plan.Derived != nil || original.Plan.Restack != nil || original.Plan.Adoption != nil || original.PRFeedback != nil || len(original.CIFeedbackRecoveries) != 0 || len(original.RuntimeMigrations) != 0 {
		return Journal{}, fmt.Errorf("runtime migration requires a stopped, unpublished implementation hold without pending recovery or session grants")
	}
	if target.Name != name || target.ImageID == "" || target.ImageID == original.ImageID {
		return Journal{}, fmt.Errorf("target runtime must have a different image")
	}
	data, err := json.Marshal(original)
	if err != nil {
		return Journal{}, err
	}
	var next Journal
	if err := json.Unmarshal(data, &next); err != nil {
		return Journal{}, err
	}
	next.RuntimeMigrations = append(next.RuntimeMigrations, RuntimeMigration{
		At: time.Now().UTC(), PreviousImageID: original.ImageID, PreviousRuntimeName: original.Plan.RuntimeName,
		PreviousSessionID: original.SessionID, PreviousAttempt: original.Attempt, PreviousProvenance: original.Plan.RuntimeProvenance,
		SessionDirectory: "native-implementation-migrated-1",
	})
	next.ImageID = target.ImageID
	next.Plan.RuntimeName = name
	next.Plan.RuntimeProvenance = CaptureRuntimeProvenance(target)
	next.SessionID = ""
	next.SelfReview = nil
	next.ReviewReuse = nil
	invalidateCommandEvidence(&next)
	next.Feedback = "The runtime was explicitly migrated after a corroborated formatter SDK hold. Continue the selected ticket using the existing workspace and frozen inputs. The previous implementation session is retained privately; inspect the current candidate, repeat self-review and rerun required formatting and checks. Prior runtime evidence is not current. Previous stop reason (untrusted data): " + strconv.Quote(original.StopReason)
	return next, nil
}

// RuntimeRecheck binds post-migration checks to the unchanged candidate and PR.
type RuntimeRecheck struct {
	PreviousCIStatus      string      `json:"previous_ci_status,omitempty"`
	PreviousCIIdentities  string      `json:"previous_ci_identities,omitempty"`
	ImplementationAttempt int         `json:"implementation_attempt,omitempty"`
	Publication           Publication `json:"publication"`
	CandidateHead         string      `json:"candidate_head"`
	CandidateTree         string      `json:"candidate_tree"`
	HoldReason            string      `json:"hold_reason"`
	Pending               bool        `json:"pending"`
	AbandonedAt           *time.Time  `json:"abandoned_at,omitempty"`
}

// PreparePublishedFeatureRuntimeMigration builds, but does not write, a new
// checkpoint for the narrow published CI-hold case. The owning feature must
// migrate every started ticket and its frozen runtime settings transactionally.
func PreparePublishedFeatureRuntimeMigration(original Journal, name string, target runtimeimage.State) (Journal, error) {
	if err := runtimeimage.ValidateName(name); err != nil {
		return Journal{}, err
	}
	if original.Plan.CheckSelection == "runtime" || len(original.Plan.Checks) == 0 || original.State != "blocked" || original.ResumeState != "ci" || original.Publication.Number < 1 || original.Publication.URL != fmt.Sprintf("https://github.com/%s/pull/%d", original.Plan.Repository, original.Publication.Number) || !objectID.MatchString(original.Publication.HeadSHA) || !objectID.MatchString(original.Publication.BaseSHA) || original.SessionID == "" || !original.Evidence.Passed || !objectID.MatchString(original.Evidence.Head) || !objectID.MatchString(original.Evidence.Tree) || original.Outcome.Status != "implemented" || len(original.Outcome.Questions) > 0 || len(original.Outcome.Findings) > 0 || len(original.Outcome.Limitations) > 0 || len(original.Outcome.RepositoryRequests) > 0 || len(original.Outcome.VerificationRequests) > 0 || len(original.Outcome.DiagnosticRequests) > 0 || len(original.Outcome.PRFeedbackAssessments) > 0 || original.RunLimitOverride != nil || original.SessionTurnExtension != nil || original.PendingInputs != nil || original.CandidatePhasePending || original.PendingRole != "" || original.Reconciliation != nil || original.DependencyInvalid || original.ResumeRebase || original.Plan.DependencyContext != nil || original.Plan.Derived != nil || original.Plan.Restack != nil || original.Plan.Adoption != nil || original.PRFeedback != nil || original.PushRepair != nil || original.ReviewContinuation != nil || original.ReviewInterruption != nil || original.ReviewAssessment != nil || original.ReviewReuse != nil || original.ReviewSkip != nil || original.CIHeldReview != nil || len(original.CIHeldReviewHistory) > 0 || len(original.ReviewReports) > 0 || original.RebaseMonitoring != nil || len(original.Verification) > 0 || len(original.CIFeedbackRecoveries) > 0 || len(original.RuntimeMigrations) > 0 {
		return Journal{}, fmt.Errorf("feature runtime migration supports only a stopped published CI hold without pending review or recovery")
	}
	if target.Name != name || !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(target.ImageID) || target.ImageID == original.ImageID {
		return Journal{}, fmt.Errorf("target runtime must have a different verified image")
	}
	data, err := json.Marshal(original)
	if err != nil {
		return Journal{}, err
	}
	var next Journal
	if err := json.Unmarshal(data, &next); err != nil {
		return Journal{}, err
	}
	next.RuntimeMigrations = append(next.RuntimeMigrations, RuntimeMigration{At: time.Now().UTC(), PreviousImageID: original.ImageID, PreviousRuntimeName: original.Plan.RuntimeName, PreviousSessionID: original.SessionID, PreviousAttempt: original.Attempt, PreviousProvenance: original.Plan.RuntimeProvenance, SessionDirectory: "native-implementation-migrated-1"})
	next.ImageID = target.ImageID
	next.Plan.RuntimeName = name
	next.Plan.RuntimeProvenance = CaptureRuntimeProvenance(target)
	next.SessionID = ""
	next.SelfReview = nil
	next.ReviewReuse = nil
	invalidateCommandEvidence(&next)
	next.CI = CIResult{}
	next.State = "blocked"
	next.ResumeState = "runtime_recheck"
	previousStatus, previousIdentities := captureCIIdentity(&original)
	next.RuntimeRecheck = &RuntimeRecheck{Publication: original.Publication, CandidateHead: original.Evidence.Head, CandidateTree: original.Evidence.Tree, HoldReason: original.StopReason, Pending: true, PreviousCIStatus: previousStatus, PreviousCIIdentities: previousIdentities}
	next.StopReason = "Runtime migrated; resume the original run to repeat isolated checks without publishing"
	next.Feedback = "The feature runtime was explicitly migrated. The previous session and CI hold are archived. Isolated checks must be repeated against the unchanged candidate before returning to CI. No publication is performed by runtime recheck. Previous CI stop reason (untrusted data): " + strconv.Quote(original.StopReason)
	return next, nil
}

// ValidateRuntimeMigrations prevents persisted session paths from escaping the run.
func ValidateRuntimeMigrations(j *Journal) error {
	if len(j.RuntimeMigrations) == 0 {
		return nil
	}
	if len(j.RuntimeMigrations) != 1 {
		return fmt.Errorf("unsupported runtime migration history")
	}
	m := j.RuntimeMigrations[0]
	if m.At.IsZero() || m.PreviousAttempt < 0 || j.Attempt < m.PreviousAttempt || !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(m.PreviousImageID) || !sessionIdentifier.MatchString(m.PreviousSessionID) || m.SessionDirectory != "native-implementation-migrated-1" || m.PreviousImageID == j.ImageID {
		return fmt.Errorf("invalid runtime migration checkpoint")
	}
	return runtimeimage.ValidateName(m.PreviousRuntimeName)
}

// VerifyRuntimeMigrationInputs checks the frozen provider and check inputs.
func VerifyRuntimeMigrationInputs(directory string, j Journal) error {
	if err := verifyInputs(j.Workspace, j.Plan.Inputs); err != nil {
		return err
	}
	return verifyInputs(filepath.Join(directory, "check-inputs"), j.Plan.CheckInputs)
}

// SaveRuntimeMigration preserves the original checkpoint before selecting the
// target. A retry after interruption can reuse the identical private backup.
func SaveRuntimeMigration(directory string, original Journal, next *Journal) error {
	current, err := Load(directory)
	if err != nil {
		return err
	}
	want, err := json.Marshal(original)
	if err != nil {
		return err
	}
	actual, err := json.Marshal(current)
	if err != nil || !bytes.Equal(want, actual) {
		return fmt.Errorf("runtime migration checkpoint changed")
	}
	journalPath := filepath.Join(directory, "journal.json")
	info, err := os.Lstat(journalPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1024*1024 {
		return fmt.Errorf("unsafe runtime migration checkpoint")
	}
	file, err := os.Open(journalPath)
	if err != nil {
		return err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		file.Close()
		return fmt.Errorf("runtime migration checkpoint changed")
	}
	data, err := io.ReadAll(io.LimitReader(file, 1024*1024+1))
	closeErr := file.Close()
	if err != nil || closeErr != nil || len(data) > 1024*1024 {
		return fmt.Errorf("cannot preserve runtime migration checkpoint")
	}
	path := filepath.Join(directory, "runtime-migration-1.before.json")
	tmp, err := os.CreateTemp(directory, ".runtime-migration-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Link(tmp.Name(), path); errors.Is(err, os.ErrExist) {
		info, statErr := os.Lstat(path)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 1024*1024 {
			return fmt.Errorf("unsafe runtime migration backup")
		}
		file, openErr := os.Open(path)
		if openErr != nil {
			return openErr
		}
		opened, statErr := file.Stat()
		if statErr != nil || !os.SameFile(info, opened) {
			file.Close()
			return fmt.Errorf("runtime migration backup changed")
		}
		saved, readErr := io.ReadAll(io.LimitReader(file, 1024*1024+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || !bytes.Equal(saved, data) {
			return fmt.Errorf("runtime migration backup differs from current checkpoint")
		}
	} else if err != nil {
		return err
	}
	if err := syncDirectory(directory); err != nil {
		return err
	}
	return Save(directory, next)
}

var formatterSDKVersion = regexp.MustCompile(`(?m)^Requested SDK version: ([0-9]+\.[0-9]+\.[0-9]+)\s*$`)

// VerifyFormatterSDKHold only permits the specific corroborated SDK hold. It
// must not turn provider usage, access or policy stops into restart paths.
func VerifyFormatterSDKHold(directory string, j Journal, target runtimeimage.State) error {
	reason := strings.ToLower(j.StopReason)
	allContext := reason + " " + strings.ToLower(j.Outcome.Summary) + " " + strings.ToLower(strings.Join(j.Outcome.Limitations, " "))
	if j.Outcome.Status != "blocked" || len(j.Outcome.Questions) != 0 || !strings.Contains(reason, "formatter worker") || !strings.Contains(reason, "restore .net sdk") {
		return fmt.Errorf("runtime migration is limited to a corroborated formatter SDK hold")
	}
	for _, forbidden := range []string{"rate", "usage", "quota", "access", "policy", "refusal", "authentication", "credential", "suspend", "exhaust", "provider"} {
		if strings.Contains(allContext, forbidden) {
			return fmt.Errorf("provider or policy holds cannot be migrated")
		}
	}
	configPath := filepath.Join(j.Workspace, "global.json")
	info, err := os.Lstat(configPath)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 65536 {
		return fmt.Errorf("SDK configuration is unavailable or unsafe")
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	var config struct {
		SDK struct {
			Version string `json:"version"`
		} `json:"sdk"`
	}
	if json.Unmarshal(data, &config) != nil || config.SDK.Version == "" {
		return fmt.Errorf("SDK configuration is invalid")
	}
	foundTarget := false
	if target.Inventory != nil {
		for _, dep := range target.Inventory.Dependencies {
			if dep.Kind == "dotnet-sdk" && dep.Version == config.SDK.Version {
				foundTarget = true
			}
		}
	}
	if !foundTarget {
		return fmt.Errorf("target runtime does not provide the required SDK %s", config.SDK.Version)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	if len(entries) > 4096 {
		return fmt.Errorf("too many run artifacts to verify the formatter hold")
	}
	foundLog := false
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "precommit-format-") || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 1024*1024 {
			return fmt.Errorf("formatter diagnostic is unsafe")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		match := formatterSDKVersion.FindSubmatch(data)
		if len(match) == 2 && string(match[1]) == config.SDK.Version && strings.Contains(strings.ToLower(string(data)), "compatible .net sdk was not found") {
			foundLog = true
		}
	}
	if !foundLog {
		return fmt.Errorf("formatter SDK mismatch is not corroborated by retained diagnostics")
	}
	return nil
}

// ReferencesRuntimeImage retains previous migration images until the run is
// complete so recovery history is not silently orphaned by runtime updates.
func ReferencesRuntimeImage(j Journal, image string) bool {
	if image == "" {
		return false
	}
	if j.ImageID == image {
		return true
	}
	for _, migration := range j.RuntimeMigrations {
		if migration.PreviousImageID == image {
			return true
		}
	}
	return false
}
