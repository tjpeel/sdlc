package workrun

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tjpeel/sdlc/internal/filelock"
)

// OperatorDirection records an explicit human instruction without rewriting feedback or native history.
type OperatorDirection struct {
	At           time.Time `json:"at"`
	Checkpoint   time.Time `json:"checkpoint"`
	AfterAttempt int       `json:"after_attempt"`
	FirstAttempt int       `json:"first_attempt,omitempty"`
	Text         string    `json:"text"`
}

func validateOperatorDirections(j *Journal) error {
	if len(j.OperatorDirections) > 20 {
		return fmt.Errorf("too many operator directions")
	}
	for _, d := range j.OperatorDirections {
		if d.At.IsZero() || d.Checkpoint.IsZero() || d.At.Before(d.Checkpoint) || d.AfterAttempt < 1 || d.AfterAttempt > j.Attempt || d.FirstAttempt < 0 || d.FirstAttempt > j.Attempt || d.FirstAttempt != 0 && d.FirstAttempt <= d.AfterAttempt || validateTakeoverReason(d.Text) != nil {
			return fmt.Errorf("invalid operator direction audit")
		}
	}
	return nil
}

// ValidateOperatorDirection only admits a controller-cancelled implementation,
// never a usage, access, policy, or other provider hold.
func ValidateOperatorDirection(j Journal, text string) error {
	if err := validateTakeoverReason(text); err != nil {
		return err
	}
	if len(j.OperatorDirections) >= 20 {
		return fmt.Errorf("too many operator directions")
	}
	return validateCancelledImplementation(j)
}

func validateCancelledImplementation(j Journal) error {
	if j.State != "failed" || j.ResumeState != "implementing" || j.SessionID == "" || !sessionIdentifier.MatchString(j.SessionID) || j.Attempt < 1 || j.UpdatedAt.IsZero() || (j.PendingRole != "" && j.PendingRole != "implementation") || len(j.Outcome.Questions) != 0 || j.DeferredAnswer != "" || j.PendingInputs != nil {
		return fmt.Errorf("operator direction requires a stopped implementation session without a pending question")
	}
	if j.Outcome.Status == "blocked" || j.Outcome.Status == "failed" || j.Outcome.Status == "waiting_for_human" {
		return fmt.Errorf("operator direction cannot bypass a retained provider or human hold")
	}
	lines := strings.Split(strings.TrimSpace(j.StopReason), "\n")
	if len(lines) < 1 || len(lines) > 4 {
		return fmt.Errorf("operator direction requires a cancellation-only stop")
	}
	cancelled := false
	for _, line := range lines {
		switch strings.TrimSpace(line) {
		case "context canceled":
			cancelled = true
		case "live diagnostic worker could not complete; controller checkpoint retained":
		default:
			return fmt.Errorf("operator direction cannot bypass a provider or operational hold")
		}
	}
	if !cancelled {
		return fmt.Errorf("operator direction requires a cancellation-only stop")
	}
	return nil
}

func RecordOperatorDirection(dir string, checkpoint time.Time, text string) error {
	path := filepath.Join(dir, "run.lock")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return fmt.Errorf("run controller lock is unsafe")
	}
	lock, err := filelock.Acquire(path)
	if err != nil {
		return err
	}
	defer lock.Close()
	j, err := Load(dir)
	if err != nil {
		return err
	}
	if !j.UpdatedAt.Equal(checkpoint) {
		return fmt.Errorf("run checkpoint changed; preview direction again")
	}
	if err := ValidateOperatorDirection(j, text); err != nil {
		return err
	}
	j.OperatorDirections = append(j.OperatorDirections, OperatorDirection{At: time.Now().UTC(), Checkpoint: j.UpdatedAt, AfterAttempt: j.Attempt, Text: text})
	return Save(dir, &j)
}

func operatorDirectionContext(j Journal, role string) string {
	if len(j.OperatorDirections) == 0 {
		return ""
	}
	data, _ := json.Marshal(j.OperatorDirections)
	if role == "review" {
		return "\nRecorded operator directions are requirements context for independent assessment, not review approval. Independently review the current publication and report conflicts: " + string(data) + "\n"
	}
	return "\nExplicit operator directions for this run (human-authorized requirements, not untrusted PR feedback): implement them while preserving the existing session and work. Surface any conflict or blocker; do not bypass checks, security, or independent review: " + string(data) + "\n"
}
