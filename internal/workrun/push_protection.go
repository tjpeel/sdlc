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
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// PublisherCommandFailure is produced only by the trusted command runner. Raw
// process output stays private and is never used as implementation guidance.
type PublisherCommandFailure struct {
	Name   string
	Args   []string
	Stderr []byte
	Exit   int
	Cause  error
}

func (e *PublisherCommandFailure) Error() string { return "publisher command failed" }
func (e *PublisherCommandFailure) Unwrap() error { return e.Cause }

type PushProtectionLocation struct {
	Commit string `json:"commit"`
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Blob   string `json:"blob"`
}
type PushProtectionRejection struct {
	KnownHead        string                   `json:"known_head,omitempty"`
	KnownTree        string                   `json:"known_tree,omitempty"`
	KnownAuditBundle string                   `json:"known_audit_bundle,omitempty"`
	KnownAuditSHA256 string                   `json:"known_audit_sha256,omitempty"`
	Kind             string                   `json:"kind,omitempty"`
	Known            []PushProtectionLocation `json:"known,omitempty"`
	Previous         Publication              `json:"previous"`
	RemoteHead       string                   `json:"remote_head"`
	AuditBundle      string                   `json:"audit_bundle,omitempty"`
	AuditSHA256      string                   `json:"audit_sha256,omitempty"`
	Diagnostic       []byte                   `json:"-"`
	Cutoff           string                   `json:"cutoff"`
	Identity         PublicationIdentity      `json:"identity"`
	Head             string                   `json:"head"`
	Tree             string                   `json:"tree"`
	SignedHead       string                   `json:"signed_head"`
	Source           string                   `json:"source"`
	Repository       string                   `json:"repository"`
	Branch           string                   `json:"branch"`
	Locations        []PushProtectionLocation `json:"locations"`
}
type HistoricalCleanupIncomplete struct {
	OriginalHead string                  `json:"original_head"`
	Candidate    PushProtectionRejection `json:"candidate"`
}

func (*HistoricalCleanupIncomplete) Error() string { return "historical cleanup incomplete" }

func (e *PushProtectionRejection) Error() string {
	return "GitHub push protection rejected unpublished content"
}

type PushProtectionRepair struct {
	Audit      []PushProtectionAudit    `json:"audit"`
	SessionID  string                   `json:"session_id"`
	Candidates []string                 `json:"candidates"`
	Pending    *PushProtectionRejection `json:"pending,omitempty"`
}

const pushProtectionRepairBound = "push protection history repairs reached the repair bound"

type PushProtectionAudit struct {
	Head   string `json:"head"`
	Bundle string `json:"bundle"`
	SHA256 string `json:"sha256"`
}
type pushProtectionReceipt struct {
	DiagnosticSHA256 string                  `json:"diagnostic_sha256"`
	Rejection        PushProtectionRejection `json:"rejection"`
}

func finishPushAuthority(ctx context.Context, directory string, result PublisherResponse, resultErr error) (PublisherResponse, error) {
	rejection, pure := resultErr.(*PushProtectionRejection)
	var nested *PushProtectionRejection
	var history *HistoricalCleanupIncomplete
	if errors.As(resultErr, &history) {
		if _, pure := resultErr.(*HistoricalCleanupIncomplete); !pure || ctx.Err() != nil {
			return PublisherResponse{}, fmt.Errorf("history repair authority withheld after publisher cleanup or cancellation failure")
		}
		return result, resultErr
	}
	if !pure && errors.As(resultErr, &nested) || pure && ctx.Err() != nil {
		return PublisherResponse{}, fmt.Errorf("push protection authority withheld after publisher cleanup or cancellation failure")
	}
	if pure && retainPushReceipt(directory, rejection) != nil {
		return PublisherResponse{}, fmt.Errorf("cannot retain verified push protection receipt")
	}
	return result, resultErr
}

func retainPushReceipt(directory string, rejection *PushProtectionRejection) error {
	diagnostic, err := LoadPublicationDiagnostic(directory)
	if err != nil {
		return err
	}
	data, err := json.Marshal(pushProtectionReceipt{DiagnosticSHA256: diagnostic.SHA256, Rejection: *rejection})
	if err != nil || len(data) > MaximumPublicationDiagnostic {
		return fmt.Errorf("push receipt exceeds bound")
	}
	return atomicDiagnosticWrite(filepath.Join(directory, "publisher", "push-rejection.json"), data)
}
func retainProvisionalPushReceipt(directory string, rejection *PushProtectionRejection, diagnosticSHA string) error {
	data, err := json.Marshal(pushProtectionReceipt{DiagnosticSHA256: diagnosticSHA, Rejection: *rejection})
	if err != nil || len(data) > MaximumPublicationDiagnostic || !validInputHash(diagnosticSHA) {
		return fmt.Errorf("invalid provisional push receipt")
	}
	return atomicDiagnosticWrite(filepath.Join(directory, "publisher", "push-rejection-pending.json"), data)
}
func loadPushReceipt(directory string, plan Plan, previous Publication, head, tree string, granted ...*PushProtectionRejection) (*PushProtectionRejection, error) {
	path := filepath.Join(directory, "publisher", "push-rejection.json")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if _, pendingErr := os.Lstat(filepath.Join(directory, "publisher", "push-rejection-pending.json")); !os.IsNotExist(pendingErr) {
			return nil, fmt.Errorf("push rejection cleanup was interrupted; publication remains held")
		}
		return nil, nil
	}
	if err != nil || !info.Mode().IsRegular() || !diagnosticOwned(info) || info.Mode().Perm() != 0600 || info.Size() < 1 || info.Size() > MaximumPublicationDiagnostic {
		return nil, fmt.Errorf("invalid retained push receipt")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("push receipt changed")
	}
	var receipt pushProtectionReceipt
	decoder := json.NewDecoder(io.LimitReader(file, MaximumPublicationDiagnostic+1))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&receipt) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("invalid retained push receipt")
	}
	diagnostic, err := LoadPublicationDiagnostic(directory)
	if err != nil || diagnostic.SHA256 != receipt.DiagnosticSHA256 || diagnostic.Stage != "command" || diagnostic.Code != "command_failed" {
		return nil, fmt.Errorf("retained push receipt lacks diagnostic proof")
	}
	if err := validatePushPublication(&receipt.Rejection, previous); err != nil {
		return nil, err
	}
	if err := validatePushPacket(&receipt.Rejection, plan, head, tree); err != nil {
		if len(granted) != 1 || granted[0] == nil || !samePushBoundary(&receipt.Rejection, granted[0]) {
			return nil, err
		}
		if err := validatePushPacket(&receipt.Rejection, plan, granted[0].Head, granted[0].Tree); err != nil {
			return nil, err
		}
	}
	return &receipt.Rejection, nil
}
func samePushBoundary(a, b *PushProtectionRejection) bool {
	left, right := *a, *b
	left.Diagnostic, right.Diagnostic = nil, nil
	left.AuditBundle, right.AuditBundle, left.AuditSHA256, right.AuditSHA256 = "", "", "", ""
	left.KnownHead, right.KnownHead, left.KnownTree, right.KnownTree = "", "", "", ""
	left.KnownAuditBundle, right.KnownAuditBundle, left.KnownAuditSHA256, right.KnownAuditSHA256 = "", "", "", ""
	return reflect.DeepEqual(left, right)
}

var pushCommitLine = regexp.MustCompile(`^remote: +\- commit: ([0-9a-f]{40}(?:[0-9a-f]{24})?)$`)
var pushPathLine = regexp.MustCompile(`^remote: +path: (.+):([0-9]+)$`)

// Parse only the bounded secret-only GH013 shape for the expected destination.
func parsePushProtection(data []byte, branch string) ([]PushProtectionLocation, error) {
	if len(data) == 0 || len(data) > MaximumPublicationDiagnostic || !utf8.Valid(data) || bytes.Contains(data, []byte{0}) {
		return nil, fmt.Errorf("invalid push rejection")
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	for _, r := range text {
		if unicode.IsControl(r) && r != '\n' {
			return nil, fmt.Errorf("invalid push diagnostic control character")
		}
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "remote:") {
			lines[i] = strings.TrimRight(line, " ")
		}
	}
	text = strings.Join(lines, "\n")
	header := "remote: error: GH013: Repository rule violations found for refs/heads/" + branch + "."
	if strings.Count(text, "GH013:") != 1 || !strings.Contains(text, header+"\n") || strings.Count(text, "remote: - GITHUB PUSH PROTECTION\n") != 1 || !strings.Contains(text, "remote:     - Push cannot contain secrets\n") {
		return nil, fmt.Errorf("not a secret push rejection")
	}
	var locations []PushProtectionLocation
	pending := ""
	sections, completed := 0, 0
	sectionOpen, sectionLocations := false, 0
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(strings.TrimPrefix(line, "remote:"))
		if trimmed == "locations:" {
			if sectionOpen {
				return nil, fmt.Errorf("incomplete location block")
			}
			sectionOpen, sectionLocations = true, 0
			sections++
		}
		if trimmed == "(?) To push, remove secret from commit(s) or follow this URL to allow the secret." || trimmed == "(?) To push, remove secret from commit(s) or follow this URL to request an exemption." {
			if !sectionOpen || sectionLocations == 0 || pending != "" {
				return nil, fmt.Errorf("incomplete secret block")
			}
			sectionOpen = false
			completed++
		}
		if strings.HasPrefix(trimmed, "- ") && trimmed != "- GITHUB PUSH PROTECTION" && trimmed != "- Push cannot contain secrets" && !strings.HasPrefix(trimmed, "- commit: ") {
			return nil, fmt.Errorf("additional repository rule")
		}
		if strings.HasPrefix(line, "fatal:") || (strings.HasPrefix(line, "remote: error:") && line != header) {
			return nil, fmt.Errorf("operational push failure")
		}
		if strings.HasPrefix(line, "remote: - ") && line != "remote: - GITHUB PUSH PROTECTION" {
			return nil, fmt.Errorf("additional repository rule")
		}
		if strings.Contains(line, "commit:") {
			m := pushCommitLine.FindStringSubmatch(line)
			if m == nil || pending != "" || !sectionOpen {
				return nil, fmt.Errorf("invalid rejection location")
			}
			pending = m[1]
		} else if strings.Contains(line, "path:") {
			m := pushPathLine.FindStringSubmatch(line)
			if m == nil || pending == "" || !safePushPath(m[1]) {
				return nil, fmt.Errorf("invalid rejection path")
			}
			n, err := strconv.Atoi(m[2])
			if err != nil || n < 1 || len(locations) >= 20 {
				return nil, fmt.Errorf("invalid rejection line")
			}
			locations = append(locations, PushProtectionLocation{Commit: pending, Path: m[1], Line: n})
			sectionLocations++
			pending = ""
		}
	}
	if pending != "" || sectionOpen || len(locations) == 0 || sections == 0 || sections != completed {
		return nil, fmt.Errorf("incomplete push rejection")
	}
	return locations, nil
}
func safePushPath(path string) bool {
	return len(path) <= 240 && utf8.ValidString(path) && sourceRelative(path) && strings.IndexFunc(path, unicode.IsControl) < 0
}

func validatePushPublication(r *PushProtectionRejection, previous Publication) error {
	if r == nil || r.Previous != previous || r.RemoteHead != "" && (!objectID.MatchString(r.RemoteHead) || r.RemoteHead != previous.HeadSHA) {
		return fmt.Errorf("push rejection publication boundary changed")
	}
	return nil
}

func validatePushPacket(r *PushProtectionRejection, plan Plan, head, tree string) error {
	if r == nil || plan.PublicationIdentity == nil || !reflect.DeepEqual(r.Identity, *plan.PublicationIdentity) || r.Repository != plan.Repository || r.Branch != plan.Branch || r.Source != plan.SourceSHA || r.Head != head || r.Tree != tree || !objectID.MatchString(r.Head) || !objectID.MatchString(r.Tree) || !objectID.MatchString(r.Cutoff) || !objectID.MatchString(r.SignedHead) || len(r.Locations) == 0 || len(r.Locations) > 20 || plan.Derived != nil || plan.Restack != nil {
		return fmt.Errorf("invalid push protection boundary")
	}
	for _, location := range r.Locations {
		if !objectID.MatchString(location.Commit) || !objectID.MatchString(location.Blob) || !safePushPath(location.Path) || location.Line < 1 || location.Line > 1024*1024 {
			return fmt.Errorf("invalid push protection location")
		}
	}
	if r.Kind != "" && r.Kind != "historical_cleanup_incomplete" {
		return fmt.Errorf("invalid history repair kind")
	}
	if len(r.Known) > 20 {
		return fmt.Errorf("history repair provenance exceeds bound")
	}
	for _, location := range r.Known {
		if !objectID.MatchString(location.Commit) || !objectID.MatchString(location.Blob) || !safePushPath(location.Path) || location.Line < 1 || location.Line > 1024*1024 {
			return fmt.Errorf("invalid known history location")
		}
	}
	return nil
}
func validateHistoryPacket(h *HistoricalCleanupIncomplete, prior *PushProtectionRejection, plan Plan, previous Publication, head, tree string) error {
	if h == nil || prior == nil || h.OriginalHead != prior.Head || h.Candidate.Head == prior.Head || h.Candidate.Kind != "historical_cleanup_incomplete" || h.Candidate.Cutoff != prior.Cutoff || h.Candidate.SignedHead != prior.SignedHead {
		return fmt.Errorf("invalid history cleanup provenance")
	}
	known := prior.Known
	if len(known) == 0 {
		known = prior.Locations
	}
	if !reflect.DeepEqual(known, h.Candidate.Known) {
		return fmt.Errorf("history cleanup provenance changed")
	}
	if h.Candidate.KnownHead != prior.KnownHead || h.Candidate.KnownTree != prior.KnownTree || h.Candidate.KnownAuditBundle != prior.KnownAuditBundle || h.Candidate.KnownAuditSHA256 != prior.KnownAuditSHA256 {
		return fmt.Errorf("history cleanup audit changed")
	}
	if err := validatePushPacket(&h.Candidate, plan, head, tree); err != nil {
		return err
	}
	return validatePushPublication(&h.Candidate, previous)
}
func validatePushRepairJournal(j *Journal) error {
	r := j.PushRepair
	if r == nil {
		return nil
	}
	if r.SessionID == "" || r.SessionID != j.SessionID || len(r.Candidates) == 0 || len(r.Candidates) > 256 {
		return fmt.Errorf("invalid push protection repair session")
	}
	seen := map[string]bool{}
	for _, head := range r.Candidates {
		if !objectID.MatchString(head) || seen[head] {
			return fmt.Errorf("invalid push protection repair grant")
		}
		seen[head] = true
	}
	if len(r.Audit) != len(r.Candidates) {
		return fmt.Errorf("incomplete push protection audit")
	}
	for i, audit := range r.Audit {
		if audit.Head != r.Candidates[i] || audit.Bundle != "rejected-"+audit.Head+".bundle" || !validInputHash(audit.SHA256) {
			return fmt.Errorf("invalid push protection audit")
		}
	}
	if r.Pending != nil {
		if !objectID.MatchString(r.Pending.KnownHead) || !objectID.MatchString(r.Pending.KnownTree) || r.Pending.KnownAuditBundle != "rejected-"+r.Pending.KnownHead+".bundle" || !validInputHash(r.Pending.KnownAuditSHA256) {
			return fmt.Errorf("push rejection lacks original known-content audit")
		}
		if r.Pending.AuditBundle != "rejected-"+r.Pending.Head+".bundle" || !validInputHash(r.Pending.AuditSHA256) {
			return fmt.Errorf("push rejection lacks immutable audit bundle")
		}
		if !seen[r.Pending.Head] {
			return fmt.Errorf("push rejection lacks repair grant")
		}
		if err := validatePushPublication(r.Pending, j.Publication); err != nil {
			return err
		}
		return validatePushPacket(r.Pending, j.Plan, r.Pending.Head, r.Pending.Tree)
	}
	return nil
}
func (runner Runner) grantPushRepair(directory string, j *Journal, r *PushProtectionRejection) error {
	if err := validatePushPacket(r, j.Plan, j.Evidence.Head, j.Evidence.Tree); err != nil {
		return runner.stop(directory, j, "blocked", err.Error())
	}
	if err := validatePushPublication(r, j.Publication); err != nil {
		return runner.stop(directory, j, "blocked", err.Error())
	}
	if j.SessionID == "" {
		return runner.stop(directory, j, "blocked", "push protection recovery requires the original implementation session")
	}
	repair := j.PushRepair
	if repair == nil {
		repair = &PushProtectionRepair{SessionID: j.SessionID}
	}
	for _, candidate := range repair.Candidates {
		if candidate == r.Head {
			return runner.stop(directory, j, "blocked", "this rejected candidate already received a push protection repair grant")
		}
	}
	if len(repair.Candidates) >= 256 || (!runLimitsOverridden(j) && len(repair.Candidates) >= 3) {
		return runner.stop(directory, j, "blocked", pushProtectionRepairBound)
	}
	if err := retainRejectedBundle(directory, r); err != nil {
		return runner.stop(directory, j, "blocked", "cannot preserve rejected source bundle")
	}
	if r.Kind != "historical_cleanup_incomplete" {
		r.KnownHead, r.KnownTree = r.Head, r.Tree
		r.KnownAuditBundle, r.KnownAuditSHA256 = r.AuditBundle, r.AuditSHA256
	}
	j.PushRepair = repair
	repair.Candidates = append(repair.Candidates, r.Head)
	repair.Audit = append(repair.Audit, PushProtectionAudit{Head: r.Head, Bundle: r.AuditBundle, SHA256: r.AuditSHA256})
	r.Diagnostic = nil
	j.PushRepair.Pending = r
	j.Evidence = CheckEvidence{}
	j.ConfiguredFeedbackKey = ""
	j.CI = CIResult{}
	j.MissingChecksSince = time.Time{}
	for i := range j.Verification {
		j.Verification[i].Key = ""
		j.Verification[i].Output = nil
		discardSourcePatchFeedback(j, &j.Verification[i])
	}
	var guidance strings.Builder
	if r.Kind == "historical_cleanup_incomplete" {
		guidance.WriteString("Historical cleanup is incomplete in the new candidate; the controller has not pushed it. The verified known offending lines still occur in these unpublished source locations.\n")
	} else {
		guidance.WriteString("GitHub push protection rejected the exact candidate. ")
	}
	fmt.Fprintf(&guidance, "Correct the unpublished history after source commit %s in this original session. Preserve that commit and all earlier history. Remove offending content from every affected commit; deleting it only at HEAD or changing commit IDs is insufficient. Do not bypass protection, request exemptions, or change authentication. Recommit meaningful corrected history, then request checks and repeat the complete local review and PR metadata gates. Verified original locations:\n", r.Cutoff)
	for _, location := range r.Locations {
		fmt.Fprintf(&guidance, "commit %s, path %s, line %d\n", location.Commit, location.Path, location.Line)
	}
	j.Feedback = guidance.String()
	return runner.transition(directory, j, "repairing")
}

func retainRejectedBundle(directory string, r *PushProtectionRejection) error {
	name := "rejected-" + r.Head + ".bundle"
	path := filepath.Join(directory, "publisher", name)
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		if err := stagePublisherBundle(filepath.Join(directory, "source.bundle"), path); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	sourceHash, err := rejectedBundleHash(filepath.Join(directory, "source.bundle"))
	if err != nil {
		return err
	}
	auditHash, err := rejectedBundleHash(path)
	if err != nil || sourceHash != auditHash {
		return fmt.Errorf("rejected audit bundle changed")
	}
	if err := os.Chmod(path, 0600); err != nil {
		return err
	}
	r.AuditBundle, r.AuditSHA256 = name, auditHash
	return nil
}
func rejectedBundleHash(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || !diagnosticOwned(info) || info.Size() < 1 || info.Size() > maximumBundleBytes {
		return "", fmt.Errorf("invalid rejected bundle")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", fmt.Errorf("rejected bundle changed")
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, maximumBundleBytes+1))
	if err != nil || n != info.Size() {
		return "", fmt.Errorf("rejected bundle changed")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
func validateRejectedBundle(directory string, repair *PushProtectionRepair) error {
	if repair == nil || repair.Pending == nil {
		return nil
	}
	r := repair.Pending
	if r.AuditBundle != "rejected-"+r.Head+".bundle" {
		return fmt.Errorf("invalid rejected bundle name")
	}
	info, err := os.Lstat(filepath.Join(directory, "publisher", r.AuditBundle))
	if err != nil || info.Mode().Perm() != 0600 {
		return fmt.Errorf("rejected audit bundle must remain private")
	}
	digest, err := rejectedBundleHash(filepath.Join(directory, "publisher", r.AuditBundle))
	if err != nil || digest != r.AuditSHA256 {
		return fmt.Errorf("rejected audit bundle changed")
	}
	if r.KnownAuditBundle != "rejected-"+r.KnownHead+".bundle" {
		return fmt.Errorf("invalid known-content audit bundle")
	}
	knownPath := filepath.Join(directory, "publisher", r.KnownAuditBundle)
	info, err = os.Lstat(knownPath)
	if err != nil || info.Mode().Perm() != 0600 {
		return fmt.Errorf("known-content audit bundle must remain private")
	}
	digest, err = rejectedBundleHash(knownPath)
	if err != nil || digest != r.KnownAuditSHA256 {
		return fmt.Errorf("known-content audit bundle changed")
	}
	return nil
}

func (p GitHubPublisher) rejectedPush(ctx context.Context, plan Plan, previous Publication, local, candidate, signed, remoteHead string, mapping map[string]string, commits []string, args []string, cause error) error {
	var failure *PublisherCommandFailure
	if p.Frozen == nil || !errors.As(cause, &failure) || failure.Name != "git" || failure.Exit <= 0 || !reflect.DeepEqual(failure.Args, args) {
		return cause
	}
	locations, err := parsePushProtection(failure.Stderr, plan.Branch)
	if err != nil {
		return cause
	}
	inverse := map[string]string{}
	for _, original := range commits {
		inverse[mapping[original]] = original
	}
	cutoff := plan.SourceSHA
	if previous.Number > 0 {
		cutoff = inverse[previous.HeadSHA]
		if cutoff == "" {
			return cause
		}
	}
	allowed := map[string]bool{}
	after := cutoff == plan.SourceSHA
	for _, original := range commits {
		if after {
			allowed[original] = true
		}
		if original == cutoff {
			after = true
		}
	}
	if plan.Derived != nil || plan.Restack != nil {
		return cause
	}
	for i := range locations {
		original := inverse[locations[i].Commit]
		if original == "" || !allowed[original] {
			return cause
		}
		locations[i].Commit = original
		blob, data, err := p.pushBlob(ctx, local, original, locations[i].Path)
		if err != nil || !utf8.Valid(data) || bytes.Contains(data, []byte{0}) {
			return cause
		}
		lines := bytes.Split(data, []byte{'\n'})
		if locations[i].Line > len(lines) || len(bytes.TrimSpace(lines[locations[i].Line-1])) == 0 {
			return cause
		}
		locations[i].Blob = blob
	}
	return &PushProtectionRejection{Previous: previous, RemoteHead: remoteHead, Diagnostic: append([]byte(nil), failure.Stderr...), Cutoff: cutoff, Identity: *p.Frozen, Head: candidate, Tree: p.ExpectedTree, SignedHead: signed, Source: plan.SourceSHA, Repository: plan.Repository, Branch: plan.Branch, Locations: locations}
}

func (p GitHubPublisher) pushBlob(ctx context.Context, local, commit, path string) (string, []byte, error) {
	entry, err := p.git(ctx, local, "ls-tree", "-z", commit, "--", path)
	if err != nil {
		return "", nil, err
	}
	parts := strings.SplitN(entry, "\t", 2)
	if len(parts) != 2 || strings.TrimSuffix(parts[1], "\x00") != path {
		return "", nil, fmt.Errorf("invalid detected file")
	}
	fields := strings.Fields(parts[0])
	if len(fields) != 3 || (fields[0] != "100644" && fields[0] != "100755") || fields[1] != "blob" || !objectID.MatchString(fields[2]) {
		return "", nil, fmt.Errorf("detected file is not regular")
	}
	data, err := p.boundedPushBlob(ctx, local, fields[2])
	return fields[2], data, err
}
func (p GitHubPublisher) boundedPushBlob(ctx context.Context, local, blob string) ([]byte, error) {
	size, err := p.git(ctx, local, "cat-file", "-s", blob)
	n, parseErr := strconv.Atoi(size)
	if err != nil || parseErr != nil || n < 0 || n > 1024*1024 {
		return nil, fmt.Errorf("push repair blob exceeds bound")
	}
	data, err := p.command(ctx, "git", p.gitArgs(local, "cat-file", "blob", blob)...)
	if err != nil || len(data) != n {
		return nil, fmt.Errorf("push repair requires regular text")
	}
	return data, nil
}

// Every new implementation tree must be free of the rejected line, including
// intermediate commits. Reading the old blob never exposes its content to the worker.
func (p GitHubPublisher) validatePushRepair(ctx context.Context, plan Plan, local, candidate string, commits []string) error {
	r := p.PushRepair
	if r == nil || r.Pending == nil {
		return nil
	}
	rejected := r.Pending
	if p.RejectedBundlePath != "" {
		if _, err := p.git(ctx, local, "bundle", "verify", p.RejectedBundlePath); err != nil {
			return fmt.Errorf("original rejected bundle verification failed")
		}
		listing, err := p.git(ctx, local, "bundle", "list-heads", p.RejectedBundlePath)
		if err != nil || listing != rejected.KnownHead+" HEAD" {
			return fmt.Errorf("original rejected bundle boundary changed")
		}
		if _, err := p.git(ctx, local, "-c", "protocol.file.allow=always", "fetch", "--no-tags", p.RejectedBundlePath, "HEAD:refs/sdlc/rejected"); err != nil {
			return fmt.Errorf("cannot import original rejected bundle")
		}
		head, err := p.git(ctx, local, "rev-parse", "refs/sdlc/rejected")
		tree, treeErr := p.git(ctx, local, "rev-parse", "refs/sdlc/rejected^{tree}")
		if err != nil || treeErr != nil || head != rejected.KnownHead || tree != rejected.KnownTree {
			return fmt.Errorf("original rejected source revision changed")
		}
	}
	if p.Frozen == nil || !reflect.DeepEqual(rejected.Identity, *p.Frozen) {
		return fmt.Errorf("push repair identity changed")
	}
	if rejected.Source != plan.SourceSHA || rejected.Repository != plan.Repository || rejected.Branch != plan.Branch || candidate == rejected.Head || len(commits) > 256 {
		return fmt.Errorf("push protection repair requires corrected unpublished history")
	}
	if _, err := p.git(ctx, local, "merge-base", "--is-ancestor", rejected.Cutoff, candidate); err != nil {
		return fmt.Errorf("published source prefix changed during push repair")
	}
	originalParent := plan.SourceSHA
	for _, commit := range commits {
		parents, err := p.git(ctx, local, "show", "-s", "--format=%P", commit)
		if err != nil || parents != originalParent {
			return fmt.Errorf("history repair requires linear own source history")
		}
		originalParent = commit
	}
	if rejected.Cutoff != plan.SourceSHA {
		index := -1
		for i, commit := range commits {
			if commit == rejected.Cutoff {
				index = i
			}
		}
		if index < 0 {
			return fmt.Errorf("published cutoff missing")
		}
		commits = commits[index+1:]
	}
	var forbidden [][]byte
	known := rejected.Known
	if len(known) == 0 {
		known = rejected.Locations
	}
	for _, location := range known {
		if p.RejectedBundlePath != "" {
			if _, err := p.git(ctx, local, "merge-base", "--is-ancestor", location.Commit, rejected.KnownHead); err != nil {
				return fmt.Errorf("known location is outside rejected history")
			}
			blob, _, err := p.pushBlob(ctx, local, location.Commit, location.Path)
			if err != nil || blob != location.Blob {
				return fmt.Errorf("known rejected blob differs from audit")
			}
		}
		data, err := p.boundedPushBlob(ctx, local, location.Blob)
		if err != nil {
			return err
		}
		lines := bytes.Split(data, []byte{'\n'})
		if location.Line < 1 || location.Line > len(lines) {
			return fmt.Errorf("invalid retained rejection")
		}
		forbidden = append(forbidden, bytes.TrimSpace(lines[location.Line-1]))
	}
	seen := map[string]bool{}
	total := 0
	remaining := []PushProtectionLocation{}
	for _, commit := range commits {
		entries, err := p.git(ctx, local, "ls-tree", "-r", "-z", commit)
		if err != nil {
			return err
		}
		for _, entry := range strings.Split(entries, "\x00") {
			if entry == "" {
				continue
			}
			fields := strings.Fields(strings.SplitN(entry, "\t", 2)[0])
			if len(fields) != 3 {
				return fmt.Errorf("invalid repair tree")
			}
			if fields[1] != "blob" || seen[fields[2]] {
				continue
			}
			seen[fields[2]] = true
			if len(seen) > 4096 {
				return fmt.Errorf("repair history exceeds file bound")
			}
			// Non-text files cannot carry the exact rejected text line. Their sizes
			// still remain bounded; unusual content holds for human inspection.
			data, err := p.boundedPushBlob(ctx, local, fields[2])
			if err != nil {
				return err
			}
			total += len(data)
			if total > 16*1024*1024 {
				return fmt.Errorf("repair history exceeds content bound")
			}
			for _, line := range forbidden {
				if bytes.Contains(data, line) {
					parts := strings.SplitN(entry, "\t", 2)
					if len(parts) != 2 || !safePushPath(parts[1]) || (fields[0] != "100644" && fields[0] != "100755") || !utf8.Valid(data) || bytes.Contains(data, []byte{0}) {
						return fmt.Errorf("known content remains in unsupported history file")
					}
					for index, current := range bytes.Split(data, []byte{'\n'}) {
						if bytes.Contains(current, line) {
							if len(remaining) >= 20 {
								return fmt.Errorf("history cleanup locations exceed bound")
							}
							remaining = append(remaining, PushProtectionLocation{Commit: commit, Path: parts[1], Line: index + 1, Blob: fields[2]})
						}
					}
				}
			}
		}
	}
	if len(remaining) > 0 {
		correction := *rejected
		correction.Diagnostic = nil
		correction.AuditBundle, correction.AuditSHA256 = "", ""
		correction.Kind = "historical_cleanup_incomplete"
		correction.Head, correction.Tree = candidate, p.ExpectedTree
		correction.Locations, correction.Known = remaining, known
		return &HistoricalCleanupIncomplete{OriginalHead: rejected.Head, Candidate: correction}
	}
	return nil
}
