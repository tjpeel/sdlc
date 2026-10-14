package workrun

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const maxCIArtifactZIPBytes = 8 * 1024 * 1024
const maxCIArtifactExpandedBytes = 16 * 1024 * 1024
const maxCIArtifactMembers = 64
const maxCIArtifactIDs = 4

// IDs come only from official upload confirmations inside the current job's
// successful step windows. URLs in log or report data never authorize reads.
var ciArtifactUpload = regexp.MustCompile(`^([^ ]+) .*successfully (?:finalized|uploaded)!?\..*Artifact ID (?:is |: ?)?([1-9][0-9]*)\s*$`)

type ciArtifactWindow struct{ start, end time.Time }
type ciArtifactMetadata struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Size     int64  `json:"size_in_bytes"`
	Expired  bool   `json:"expired"`
	Digest   string `json:"digest"`
	Created  string `json:"created_at"`
	Updated  string `json:"updated_at"`
	Workflow struct {
		ID               int64  `json:"id"`
		RepositoryID     int64  `json:"repository_id"`
		HeadRepositoryID int64  `json:"head_repository_id"`
		Head             string `json:"head_sha"`
	} `json:"workflow_run"`
}

func ciUploadedArtifacts(log string, steps []ciActionStep) (map[int64]ciArtifactWindow, bool, error) {
	ids := map[int64]ciArtifactWindow{}
	incomplete := false
	for _, line := range strings.Split(log, "\n") {
		m := ciArtifactUpload.FindStringSubmatch(line)
		if len(m) == 0 {
			continue
		}
		timestamp, err := time.Parse(time.RFC3339Nano, m[1])
		if err != nil {
			return nil, false, fmt.Errorf("invalid artifact upload timestamp")
		}
		id, err := strconv.ParseInt(m[2], 10, 64)
		if err != nil {
			return nil, false, fmt.Errorf("invalid artifact upload identity")
		}
		for _, step := range steps {
			if step.Status != "completed" || step.Conclusion != "success" {
				continue
			}
			start, e1 := time.Parse(time.RFC3339Nano, step.StartedAt)
			end, e2 := time.Parse(time.RFC3339Nano, step.CompletedAt)
			if e1 != nil || e2 != nil || end.Before(start) || timestamp.Before(start) || timestamp.After(end) {
				continue
			}
			window := ciArtifactWindow{start, end}
			if previous, ok := ids[id]; ok && previous != window {
				return nil, false, fmt.Errorf("ambiguous artifact upload identity")
			}
			ids[id] = window
			if len(ids) > maxCIArtifactIDs {
				var largest int64
				for selected := range ids {
					if selected > largest {
						largest = selected
					}
				}
				delete(ids, largest)
				incomplete = true
			}
		}
	}
	return ids, incomplete, nil
}

func validCIArtifact(a ciArtifactMetadata, id int64, run ciActionRun, window ciArtifactWindow) bool {
	created, e1 := time.Parse(time.RFC3339Nano, a.Created)
	updated, e2 := time.Parse(time.RFC3339Nano, a.Updated)
	return a.ID == id && a.Size > 0 && !a.Expired && a.Workflow.ID == run.ID && a.Workflow.RepositoryID == run.Repository.ID && a.Workflow.HeadRepositoryID == run.Repository.ID && a.Workflow.Head == run.Head && e1 == nil && e2 == nil && !created.Before(window.start.Truncate(time.Second)) && !created.After(window.end) && !updated.Before(created) && !updated.After(window.end) && len(a.Digest) == 71 && strings.HasPrefix(a.Digest, "sha256:") && strings.Trim(a.Digest[7:], "0123456789abcdef") == ""
}

func (p GitHubPublisher) collectCIArtifactReports(ctx context.Context, repo string, run ciActionRun, job ciActionJob, log string) (string, bool, bool, error) {
	ids, incomplete, err := ciUploadedArtifacts(log, job.Steps)
	if err != nil {
		return "", false, false, err
	}
	// Stable order keeps saved automatic recovery payloads reproducible.
	ordered := make([]int64, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	slices.Sort(ordered)
	var facts strings.Builder
	reportRedacted := false
	if incomplete {
		facts.WriteString("Optional artifact intake omitted: discovery count bound; report contents unknown.\n")
	}
	for _, id := range ordered {
		endpoint := fmt.Sprintf("repos/%s/actions/artifacts/%d", repo, id)
		var before ciArtifactMetadata
		if err := p.ciAPI(ctx, endpoint, &before); err != nil {
			return "", false, false, err
		}
		if !validCIArtifact(before, id, run, ids[id]) {
			return "", false, false, fmt.Errorf("Actions artifact identity or intake bound changed")
		}
		if before.Size > maxCIArtifactZIPBytes {
			incomplete = true
			facts.WriteString("Optional artifact intake omitted: compressed-size bound; report contents unknown.\n")
			var after ciArtifactMetadata
			if err := p.ciAPI(ctx, endpoint, &after); err != nil {
				return "", false, false, err
			}
			if before != after {
				return "", false, false, fmt.Errorf("Actions artifact changed during collection")
			}
			continue
		}
		data, err := p.command(ctx, "gh", "api", "--hostname", "github.com", "--allow-escape-sequences", endpoint+"/zip")
		if err != nil || len(data) > maxCIArtifactZIPBytes || int64(len(data)) != before.Size {
			return "", false, false, fmt.Errorf("Actions artifact ZIP could not be read within its bound")
		}
		digest := sha256.Sum256(data)
		if "sha256:"+hex.EncodeToString(digest[:]) != before.Digest {
			return "", false, false, fmt.Errorf("Actions artifact digest changed")
		}
		projected, err := ciTrivyZIPProjection(data, p.CIPrivateValues)
		if err != nil {
			archive, zipErr := zip.NewReader(bytes.NewReader(data), int64(len(data)))
			recognized := false
			if zipErr == nil {
				for _, member := range archive.File {
					recognized = recognized || path.Base(member.Name) == "trivy.json"
				}
			}
			if recognized {
				return "", false, false, err
			}
			incomplete = true
			projected = ciReportProjection{Text: "Optional artifact intake omitted: unsupported archive; report contents unknown.\n", Incomplete: true}
		}
		var after ciArtifactMetadata
		if err := p.ciAPI(ctx, endpoint, &after); err != nil {
			return "", false, false, err
		}
		if before != after {
			return "", false, false, fmt.Errorf("Actions artifact changed during collection")
		}
		reportRedacted = reportRedacted || projected.Redacted
		incomplete = incomplete || projected.Incomplete
		facts.WriteString(projected.Text)
		if facts.Len() > maxCIArtifactExpandedBytes {
			return "", false, false, fmt.Errorf("Actions report facts exceed bound")
		}
	}
	clean, redacted, err := sanitizeCITextWithinBound(facts.String(), p.CIPrivateValues, maxCIArtifactExpandedBytes)
	if err != nil {
		return "", false, false, err
	}
	selected, truncated := ciTextExcerpt(clean, maxCIJobExcerpt)
	return selected, redacted || reportRedacted, truncated || incomplete, nil
}

// Validate every ZIP member, including ignored reports, without host extraction.
type ciReportProjection struct {
	Text                 string
	Redacted, Incomplete bool
}

func ciTrivyZIPFacts(data []byte) (string, error) {
	projected, err := ciTrivyZIPProjection(data, nil)
	return projected.Text, err
}
func ciTrivyZIPProjection(data []byte, privateValues []string) (ciReportProjection, error) {
	if len(data) > maxCIArtifactZIPBytes {
		return ciReportProjection{}, fmt.Errorf("Actions artifact ZIP exceeds bound")
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(archive.File) > maxCIArtifactMembers {
		return ciReportProjection{}, fmt.Errorf("invalid Actions artifact ZIP")
	}
	seen := map[string]bool{}
	var expanded uint64
	var facts strings.Builder
	redacted, incomplete, recognized := false, false, false
	for _, member := range archive.File {
		name := member.Name
		if name == "" || name == "." || !utf8.ValidString(name) || strings.ContainsAny(name, "\\\x00") || strings.HasPrefix(name, "/") || name == ".." || strings.HasPrefix(name, "../") || strings.Contains(name, ":") || path.Clean(name) != strings.TrimSuffix(name, "/") || member.Mode().Type() != 0 && member.Mode().Type() != os.ModeDir || seen[strings.TrimSuffix(name, "/")] {
			return ciReportProjection{}, fmt.Errorf("unsafe Actions artifact ZIP member")
		}
		seen[strings.TrimSuffix(name, "/")] = true
		if member.UncompressedSize64 > maxCIArtifactExpandedBytes || expanded > maxCIArtifactExpandedBytes-member.UncompressedSize64 {
			return ciReportProjection{}, fmt.Errorf("Actions artifact ZIP expansion exceeds bound")
		}
		expanded += member.UncompressedSize64
		reader, err := member.Open()
		if err != nil {
			return ciReportProjection{}, fmt.Errorf("invalid Actions artifact ZIP member")
		}
		content, readErr := io.ReadAll(io.LimitReader(reader, maxCIArtifactExpandedBytes+1))
		closeErr := reader.Close()
		if readErr != nil || closeErr != nil || uint64(len(content)) != member.UncompressedSize64 {
			return ciReportProjection{}, fmt.Errorf("Actions artifact ZIP member integrity failed")
		}
		if member.FileInfo().IsDir() || path.Base(name) != "trivy.json" {
			continue
		}
		recognized = true
		projected, err := ciTrivyReportFacts(content, privateValues)
		if err != nil {
			return ciReportProjection{}, err
		}
		redacted = redacted || projected.Redacted
		incomplete = incomplete || projected.Incomplete
		facts.WriteString(projected.Text)
		if facts.Len() > maxCIArtifactExpandedBytes {
			return ciReportProjection{}, fmt.Errorf("Actions report facts exceed bound")
		}
	}
	if !recognized {
		incomplete = true
		facts.WriteString("Optional artifact intake omitted: no supported report members; report contents unknown.\n")
	}
	return ciReportProjection{Text: facts.String(), Redacted: redacted, Incomplete: incomplete}, nil
}

type ciTrivyReport struct {
	SchemaVersion int
	Results       []struct {
		Target          string
		Vulnerabilities []struct{ VulnerabilityID, PkgName, InstalledVersion, FixedVersion, Severity string }
		Secrets         []struct {
			RuleID, Severity   string
			StartLine, EndLine int
		}
	}
}

func ciTrivyReportFacts(data []byte, privateValues []string) (ciReportProjection, error) {
	var header struct{ SchemaVersion int }
	if json.Unmarshal(data, &header) != nil {
		return ciReportProjection{}, fmt.Errorf("invalid Trivy report")
	}
	if header.SchemaVersion != 2 {
		return ciReportProjection{Text: "Optional artifact intake omitted: unsupported Trivy schema; report contents unknown.\n", Incomplete: true}, nil
	}
	var report ciTrivyReport
	if json.Unmarshal(data, &report) != nil || len(report.Results) > 1000 {
		return ciReportProjection{}, fmt.Errorf("invalid Trivy report facts")
	}
	redacted := false
	mask := func(fields ...*string) error {
		for _, field := range fields {
			clean, changed, err := sanitizeCIText(*field, privateValues)
			if err != nil {
				return err
			}
			*field = clean
			redacted = redacted || changed
		}
		return nil
	}
	var facts strings.Builder
	count := 0
	for _, result := range report.Results {
		for _, v := range result.Vulnerabilities {
			if v.Severity != "HIGH" && v.Severity != "CRITICAL" {
				continue
			}
			count++
			if count > 1000 {
				return ciReportProjection{}, fmt.Errorf("Trivy fact count exceeds bound")
			}
			if err := mask(&result.Target, &v.VulnerabilityID, &v.PkgName, &v.InstalledVersion, &v.FixedVersion, &v.Severity); err != nil {
				return ciReportProjection{}, err
			}
			// JSON quoting prevents fields from impersonating controller delimiters.
			fact, _ := json.Marshal(struct{ Kind, Target, CVE, Package, Installed, Fixed, Severity string }{"vulnerability", result.Target, v.VulnerabilityID, v.PkgName, v.InstalledVersion, v.FixedVersion, v.Severity})
			facts.Write(fact)
			facts.WriteByte('\n')
		}
		for _, s := range result.Secrets {
			count++
			if count > 1000 || s.StartLine < 1 || s.EndLine < s.StartLine {
				return ciReportProjection{}, fmt.Errorf("invalid Trivy secret fact")
			}
			if err := mask(&result.Target, &s.RuleID, &s.Severity); err != nil {
				return ciReportProjection{}, err
			}
			fact, _ := json.Marshal(struct {
				Kind, Path, RuleID, Severity string
				StartLine, EndLine           int
			}{"secret", result.Target, s.RuleID, s.Severity, s.StartLine, s.EndLine})
			facts.Write(fact)
			facts.WriteByte('\n')
		}
	}
	if facts.Len() > maxCIArtifactExpandedBytes {
		return ciReportProjection{}, fmt.Errorf("Trivy facts exceed bound")
	}
	return ciReportProjection{Text: facts.String(), Redacted: redacted}, nil
}
