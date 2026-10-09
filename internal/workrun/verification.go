package workrun

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

// VerificationRequest asks the controller for machine verification in its existing isolated worker.
type VerificationRequest struct {
	ID       string                `json:"id"`
	Purpose  string                `json:"purpose"`
	Commands [][]string            `json:"commands"`
	Baseline *VerificationBaseline `json:"baseline"`
}
type VerificationBaseline struct {
	Revision         string   `json:"revision"`
	Paths            []string `json:"paths"`
	ExpectedExitCode int      `json:"expected_exit_code"`
	FailureContains  []string `json:"failure_contains"`
}
type VerificationResult struct {
	Request    VerificationRequest `json:"request"`
	Key        string              `json:"key,omitempty"`
	Head       string              `json:"head,omitempty"`
	Tree       string              `json:"tree,omitempty"`
	Passed     bool                `json:"passed"`
	Log        string              `json:"log,omitempty"`
	Diagnostic string              `json:"diagnostic,omitempty"`
}
type VerificationChecker interface {
	Verify(context.Context, string, VerificationRequest, io.Writer) error
}

var verificationID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

func validateVerificationRequests(requests []VerificationRequest, plan *Plan) error {
	if len(requests) > 16 {
		return fmt.Errorf("too many verification requests")
	}
	ids := map[string]bool{}
	for _, r := range requests {
		encoded, err := json.Marshal(r)
		if err != nil || len(encoded) > 8192 {
			return fmt.Errorf("verification request exceeds 8 KiB")
		}
		if !verificationID.MatchString(r.ID) || ids[r.ID] || len(r.Purpose) == 0 || len(r.Purpose) > 1024 || unsafeVerificationText(r.Purpose) || len(r.Commands) == 0 || len(r.Commands) > 16 {
			return fmt.Errorf("invalid verification request")
		}
		ids[r.ID] = true
		total := 0
		for _, command := range r.Commands {
			if len(command) == 0 || len(command) > 64 || command[0] == "" {
				return fmt.Errorf("invalid verification command")
			}
			for _, arg := range command {
				total += len(arg)
				if len(arg) > 4096 || strings.ContainsRune(arg, 0) {
					return fmt.Errorf("invalid verification argument")
				}
			}
		}
		if total > 8192 {
			return fmt.Errorf("verification commands exceed bound")
		}
		b := r.Baseline
		if b == nil {
			continue
		}
		if !objectID.MatchString(b.Revision) || (plan != nil && b.Revision != plan.StartingSHA && b.Revision != plan.SourceSHA) || b.ExpectedExitCode < 1 || b.ExpectedExitCode > 255 || len(b.Paths) == 0 || len(b.Paths) > 32 || len(b.FailureContains) == 0 || len(b.FailureContains) > 16 {
			return fmt.Errorf("invalid verification baseline")
		}
		for _, p := range b.Paths {
			if len(p) > 256 || unsafeVerificationText(p) || p == "." || path.IsAbs(p) || path.Clean(p) != p || strings.Contains(p, "\\") || strings.ContainsRune(p, 0) {
				return fmt.Errorf("unsafe verification baseline path")
			}
			for _, part := range strings.Split(p, "/") {
				if part == ".." || part == ".git" {
					return fmt.Errorf("unsafe verification baseline path")
				}
			}

			if plan != nil {
				for _, input := range append(append([]Input(nil), plan.Inputs...), plan.CheckInputs...) {
					if p == input.Path || strings.HasPrefix(p, input.Path+"/") || strings.HasPrefix(input.Path, p+"/") {
						return fmt.Errorf("baseline path overlaps frozen input")
					}
				}
			}
		}
		for _, marker := range b.FailureContains {
			if len(marker) == 0 || len(marker) > 256 || unsafeVerificationText(marker) {
				return fmt.Errorf("invalid verification failure marker")
			}
		}
	}
	return nil
}
func mergeVerification(j *Journal, requests []VerificationRequest) error {
	if err := validateVerificationRequests(requests, &j.Plan); err != nil {
		return err
	}
	for _, request := range requests {
		found := false
		for i := range j.Verification {
			if j.Verification[i].Request.ID == request.ID {
				found = true
				if j.Verification[i].Request.Baseline != nil && request.Baseline == nil {
					return fmt.Errorf("baseline verification %s cannot be downgraded to a generic check", request.ID)
				}
				old, _ := json.Marshal(j.Verification[i].Request)
				next, _ := json.Marshal(request)
				if string(old) != string(next) {
					j.Verification[i] = VerificationResult{Request: request}
				}
				break
			}
		}
		if !found {
			if len(j.Verification) >= 16 {
				return fmt.Errorf("outstanding verification request bound exceeded")
			}
			j.Verification = append(j.Verification, VerificationResult{Request: request})
		}
	}
	return nil
}
func verificationKey(j *Journal, request VerificationRequest, head, tree string) string {
	data, _ := json.Marshal(struct {
		Request           VerificationRequest
		Head, Tree, Image string
		Inputs            []Input
		Commands          [][]string
		Docker            bool
		Daemon            string
		DaemonMode        string `json:",omitempty"`
	}{request, head, tree, j.ImageID, j.Plan.CheckInputs, j.Plan.Checks, j.Plan.DockerTests, j.Plan.DaemonImage, j.Plan.DaemonMode})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func verificationCurrent(j *Journal, head, tree string) bool {
	for _, r := range j.Verification {
		if !r.Passed || r.Key != verificationKey(j, r.Request, head, tree) {
			return false
		}
	}
	return true
}

// failureMatcher retains only the suffix needed to recognise bounded literal markers.
// It sees command stdout/stderr only, never the command announcement.
type failureMatcher struct {
	markers []string
	matched []bool
	tail    string
	longest int
}

func newFailureMatcher(b *VerificationBaseline) *failureMatcher {
	m := &failureMatcher{}
	if b != nil {
		m.markers = b.FailureContains
		m.matched = make([]bool, len(m.markers))
		for _, s := range m.markers {
			if len(s) > m.longest {
				m.longest = len(s)
			}
		}
	}
	return m
}
func (m *failureMatcher) Write(p []byte) (int, error) {
	if m.longest == 0 {
		return len(p), nil
	}
	text := m.tail + string(p)
	for i, s := range m.markers {
		if strings.Contains(text, s) {
			m.matched[i] = true
		}
	}
	n := m.longest - 1
	if len(text) > n {
		text = text[len(text)-n:]
	}
	m.tail = text
	return len(p), nil
}
func (m *failureMatcher) complete() bool {
	for _, v := range m.matched {
		if !v {
			return false
		}
	}
	return len(m.matched) > 0
}

func verificationFeedback(j *Journal) string {
	data, _ := json.Marshal(struct {
		Suite      CheckEvidence        `json:"suite"`
		Additional []VerificationResult `json:"additional"`
	}{j.Evidence, j.Verification})
	return "Controller verification evidence: " + string(data)
}
func verificationFeedbackKey(j *Journal, head, tree string) string {
	data, _ := json.Marshal(struct {
		Head, Tree string
		Results    []VerificationResult
	}{head, tree, j.Verification})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// A missing field remains compatible with old provider handoffs and journals;
// an explicitly null field is a malformed new handoff.
func (o *Outcome) UnmarshalJSON(data []byte) error {
	type plain Outcome
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if raw, ok := fields["verification_requests"]; ok && bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("verification_requests must be an array")
	}
	var value plain
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if err := validateVerificationRequests(value.VerificationRequests, nil); err != nil {
		return err
	}
	*o = Outcome(value)
	return nil
}
func (r *VerificationRequest) UnmarshalJSON(data []byte) error {
	type plain VerificationRequest
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, key := range []string{"id", "purpose", "commands"} {
		raw, ok := fields[key]
		if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("verification request missing %s", key)
		}
	}

	var value plain
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	*r = VerificationRequest(value)
	return nil
}

func unsafeVerificationText(s string) bool {
	for _, r := range s {
		if r < 32 || r == 127 {
			return true
		}
	}
	return false
}

// VerificationFailure preserves check classification while keeping the phase explicit.
type VerificationFailure struct {
	Phase string
	Err   error
}

func (f *VerificationFailure) Error() string {
	return "additional verification " + f.Phase + ": " + f.Err.Error()
}
func (f *VerificationFailure) Unwrap() error { return f.Err }

type baselineMismatch struct {
	Reason string
	Err    error
}

func (f *baselineMismatch) Error() string { return f.Reason }
func (f *baselineMismatch) Unwrap() error { return f.Err }
func verificationDiagnostic(request VerificationRequest, tree string, err error) string {
	phase := "candidate"
	var failure *VerificationFailure
	if errors.As(err, &failure) {
		phase = failure.Phase
	}
	prefix := "Additional verification " + request.ID + " " + phase + " phase failed. "
	var mismatch *baselineMismatch
	if errors.As(err, &mismatch) {
		prefix += mismatch.Reason + ". "
	}
	var check *CheckFailure
	if errors.As(err, &check) {
		feedback := checkRepairFeedback(tree, check, request.Commands)
		feedback = strings.Replace(feedback, "Configured checks failed", "Requested commands failed", 1)
		feedback = strings.Replace(feedback, "Ask the human only if the evidence needed to repair is unavailable.", "Submit a structured verification request for additional machine work; stop only for genuine human questions.", 1)
		return boundedVerificationDiagnostic(prefix + feedback)
	}
	return prefix + "Candidate commands must pass; the final baseline command must exit as specified with all literal failure markers. Detailed output remains in the private log."
}

func boundedVerificationDiagnostic(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	if len(s) > 4096 {
		s = s[:4096]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}
