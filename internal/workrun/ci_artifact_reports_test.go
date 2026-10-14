package workrun

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

func syntheticCIReportZIP(t *testing.T, names []string, contents []string) []byte {
	t.Helper()
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	for i, name := range names {
		member, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = member.Write([]byte(contents[i])); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

const syntheticTrivyReport = `{"SchemaVersion":2,"Results":[{"Target":"runtime/example.dll","Vulnerabilities":[{"VulnerabilityID":"CVE-2026-12345","PkgName":"Example.Package","InstalledVersion":"1.0","FixedVersion":"1.1","Severity":"HIGH","Description":"DO-NOT-RETAIN-description"}],"Secrets":[{"RuleID":"example-rule","StartLine":3,"EndLine":3,"Severity":"HIGH","Match":"DO-NOT-RETAIN-secret","Code":{"Lines":["DO-NOT-RETAIN-source"]}}]}]}`

func ciArtifactFailureFixture(t *testing.T) *ciFailureFixture {
	t.Helper()
	f := actionsCIFailureFixture()
	job := f.responses["repos/example/project/actions/jobs/99"].(map[string]any)
	job["steps"] = append(job["steps"].([]any), map[string]any{"name": "Upload reports", "number": 3, "status": "completed", "conclusion": "success", "started_at": "2026-01-01T12:03:00Z", "completed_at": "2026-01-01T12:04:00Z"})
	f.responses["repos/example/project/actions/jobs/99/logs"] = []byte("2026-01-01T12:01:00Z scan failed without details\n2026-01-01T12:03:30Z Artifact arbitrary-output successfully finalized. Artifact ID 123\n2026-01-01T12:03:31Z Artifact arbitrary-output has been successfully uploaded! Final size is 200 bytes. Artifact ID is 123\n")
	data := syntheticCIReportZIP(t, []string{"test.trx", "reports/trivy.json"}, []string{"DO-NOT-RETAIN-trx", syntheticTrivyReport})
	digest := sha256.Sum256(data)
	f.responses["repos/example/project/actions/artifacts/123"] = map[string]any{"id": 123, "name": "arbitrary-output", "size_in_bytes": len(data), "expired": false, "digest": "sha256:" + hex.EncodeToString(digest[:]), "created_at": "2026-01-01T12:03:10Z", "updated_at": "2026-01-01T12:03:31Z", "workflow_run": map[string]any{"id": 88, "repository_id": 7, "head_repository_id": 7, "head_sha": testHead}}
	f.responses["repos/example/project/actions/artifacts/123/zip"] = data
	return f
}

func TestActionsArtifactReportFactsArePrivateAndBounded(t *testing.T) {
	f := ciArtifactFailureFixture(t)
	result, err := (GitHubPublisher{Command: f.command, CIPrivateValues: []string{"Example.Package"}}).Checks(context.Background(), Plan{Repository: "example/project"}, annotationPublication())
	if err != nil || result.FailureContext == nil {
		t.Fatal("verified report withheld", err)
	}
	entry := result.FailureContext.Checks[0]
	for _, want := range []string{"CVE-2026-12345", "1.1", "example-rule", "runtime/example.dll"} {
		if !strings.Contains(entry.Text, want) {
			t.Fatal("report finding absent", want)
		}
	}
	if strings.Contains(entry.Text, "DO-NOT-RETAIN") || strings.Contains(entry.Text, "Example.Package") || !entry.Redacted || len(entry.Text) > maxCIFailureText {
		t.Fatal("report privacy or retention failed", entry)
	}
}

func TestNativeSonarPrivateTitlesDoNotRequireRuleIDs(t *testing.T) {
	f := genericCIFailureFixture()
	run := f.responses["repos/example/project/check-runs/41"].(map[string]any)
	run["app"] = map[string]any{"id": 90, "slug": "sonarcloud"}
	run["output"] = map[string]any{"annotations_count": 1}
	f.responses["repos/example/project/check-runs/41/annotations?per_page=100&page=1"] = []any{map[string]any{"path": "src/Example.cs", "start_line": 42, "annotation_level": "failure", "title": "Avoid command argument injection disposable-title-private", "message": "DO-NOT-RETAIN-message", "raw_details": "DO-NOT-RETAIN-source"}}
	packet, err := (GitHubPublisher{Command: f.command, CIPrivateValues: []string{"disposable-title-private"}}).collectCIFailureContext(context.Background(), Plan{Repository: "example/project"}, annotationPublication(), f.checks, []CIAnnotation{{Head: testHead, Path: "src/Example.cs", Line: 42, EndLine: 42, Level: "failure", Kind: "generic"}})
	if err != nil || packet == nil || !strings.Contains(packet.Checks[0].Text, "Avoid command argument injection") || !strings.Contains(packet.Checks[0].Text, "src/Example.cs:42") {
		t.Fatal("actionable native title lost", err)
	}
	if strings.Contains(packet.Checks[0].Text, "DO-NOT-RETAIN") || strings.Contains(packet.Checks[0].Text, "disposable-title-private") || !packet.Checks[0].Redacted {
		t.Fatal("native title privacy failed")
	}
}

func TestActionsArtifactIdentityIntegrityAndOptionalIntake(t *testing.T) {
	for _, kind := range []string{"digest", "run", "repository", "head", "expired", "timestamp", "changed", "transport", "zip traversal", "zip duplicate", "zip crc", "report oversized", "optional oversized", "junit report oversized", "dotnet reports oversized"} {
		t.Run(kind, func(t *testing.T) {
			f := ciArtifactFailureFixture(t)
			meta := f.responses["repos/example/project/actions/artifacts/123"].(map[string]any)
			workflow := meta["workflow_run"].(map[string]any)
			switch kind {
			case "digest":
				meta["digest"] = "sha256:" + strings.Repeat("0", 64)
			case "run":
				workflow["id"] = 89
			case "repository":
				workflow["repository_id"] = 8
			case "head":
				workflow["head_sha"] = testBase
			case "expired":
				meta["expired"] = true
			case "timestamp":
				meta["created_at"] = "2025-01-01T12:03:10Z"
			case "changed":
				calls := 0
				f.before = func(endpoint string) {
					if endpoint == "repos/example/project/actions/artifacts/123" {
						calls++
						if calls == 2 {
							meta["expired"] = true
						}
					}
				}
			case "transport":
				f.fail = "repos/example/project/actions/artifacts/123/zip"
			case "report oversized":
				meta["name"] = "scanner-reports"
				meta["size_in_bytes"] = maxCIArtifactZIPBytes + 1
			case "optional oversized", "junit report oversized", "dotnet reports oversized":
				meta["name"] = "container-build"
				if kind == "junit report oversized" {
					meta["name"] = "junit-report"
				}
				if kind == "dotnet reports oversized" {
					meta["name"] = "dotnet-test-reports"
				}
				meta["size_in_bytes"] = maxCIArtifactZIPBytes + 1
			default:
				names := []string{"../trivy.json"}
				contents := []string{syntheticTrivyReport}
				if kind == "zip duplicate" {
					names = []string{"trivy.json", "trivy.json"}
					contents = append(contents, syntheticTrivyReport)
				}
				data := syntheticCIReportZIP(t, names, contents)
				if kind == "zip crc" {
					data = syntheticCIReportZIP(t, []string{"trivy.json"}, contents)
					archive, _ := zip.NewReader(bytes.NewReader(data), int64(len(data)))
					offset, _ := archive.File[0].DataOffset()
					data[offset] ^= 0xff
				}
				digest := sha256.Sum256(data)
				meta["size_in_bytes"] = len(data)
				meta["digest"] = "sha256:" + hex.EncodeToString(digest[:])
				f.responses["repos/example/project/actions/artifacts/123/zip"] = data
			}
			result, err := (GitHubPublisher{Command: f.command}).Checks(context.Background(), Plan{Repository: "example/project"}, annotationPublication())
			if strings.HasSuffix(kind, "oversized") {
				if err != nil || result.FailureContext == nil || !strings.Contains(result.FailureContext.Checks[0].Text, "report contents unknown") {
					t.Fatal("optional artifact became a gate or false absence", err)
				}
				for _, call := range f.calls {
					if strings.HasSuffix(call, "artifacts/123/zip") {
						t.Fatal("oversized optional artifact downloaded")
					}
				}
			} else if err == nil || result.FailureContext != nil {
				t.Fatal("unsafe artifact escaped hold", kind)
			}
		})
	}
}

func TestCITrivyZIPRejectsUnsafeMembersAndSkipsOtherReports(t *testing.T) {
	for _, name := range []string{"/trivy.json", "../trivy.json", "a/../trivy.json", "a\\trivy.json", "C:trivy.json"} {
		if _, err := ciTrivyZIPFacts(syntheticCIReportZIP(t, []string{name}, []string{syntheticTrivyReport})); err == nil {
			t.Fatal("unsafe member admitted", name)
		}
	}
	var data bytes.Buffer
	w := zip.NewWriter(&data)
	header := &zip.FileHeader{Name: "trivy.json"}
	header.SetMode(os.ModeSymlink | 0777)
	r, _ := w.CreateHeader(header)
	fmt.Fprint(r, "target")
	w.Close()
	if _, err := ciTrivyZIPFacts(data.Bytes()); err == nil {
		t.Fatal("symlink admitted")
	}
	for _, contents := range []string{`{"SchemaVersion":1}`, `{"SchemaVersion":2,"Results":[]}`} {
		facts, err := ciTrivyZIPFacts(syntheticCIReportZIP(t, []string{"trivy.json"}, []string{contents}))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(contents, `"SchemaVersion":1`) {
			if !strings.Contains(facts, "unsupported Trivy schema; report contents unknown") {
				t.Fatal("unsupported schema was silently omitted")
			}
		} else if facts != "" {
			t.Fatal("supported empty report added facts")
		}
	}
}

func TestActionsOptionalArtifactDiscoveryIsBoundedWithoutFalseGate(t *testing.T) {
	f := ciArtifactFailureFixture(t)
	log := string(f.responses["repos/example/project/actions/jobs/99/logs"].([]byte))
	base := f.responses["repos/example/project/actions/artifacts/123"].(map[string]any)
	for id := int64(124); id < 130; id++ {
		log += fmt.Sprintf("2026-01-01T12:03:31Z Artifact build successfully finalized. Artifact ID %d\n", id)
		copyMeta := map[string]any{}
		for k, v := range base {
			copyMeta[k] = v
		}
		copyMeta["id"] = id
		copyMeta["name"] = "container-build"
		copyMeta["size_in_bytes"] = maxCIArtifactZIPBytes + 1
		f.responses[fmt.Sprintf("repos/example/project/actions/artifacts/%d", id)] = copyMeta
	}
	f.responses["repos/example/project/actions/jobs/99/logs"] = []byte(log)
	result, err := (GitHubPublisher{Command: f.command}).Checks(context.Background(), Plan{Repository: "example/project"}, annotationPublication())
	if err != nil || result.FailureContext == nil {
		t.Fatal("ordinary optional artifact volume became a gate", err)
	}
	entry := result.FailureContext.Checks[0]
	if !entry.Truncated || !strings.Contains(entry.Text, "discovery count bound") || !strings.Contains(entry.Text, "CVE-2026-12345") {
		t.Fatal("omission hidden or supported findings starved")
	}
	for _, call := range f.calls {
		if strings.Contains(call, "artifacts/127") {
			t.Fatal("discovery cap bypassed")
		}
	}
}

func TestActionsUnrelatedArchiveDoesNotHideSupportedReport(t *testing.T) {
	f := ciArtifactFailureFixture(t)
	log := string(f.responses["repos/example/project/actions/jobs/99/logs"].([]byte)) + "2026-01-01T12:03:32Z Artifact build successfully finalized. Artifact ID 124\n"
	f.responses["repos/example/project/actions/jobs/99/logs"] = []byte(log)
	data := []byte("synthetic unrelated non-ZIP build output")
	digest := sha256.Sum256(data)
	base := f.responses["repos/example/project/actions/artifacts/123"].(map[string]any)
	meta := map[string]any{}
	for k, v := range base {
		meta[k] = v
	}
	meta["id"] = 124
	meta["name"] = "container-build"
	meta["size_in_bytes"] = len(data)
	meta["digest"] = "sha256:" + hex.EncodeToString(digest[:])
	f.responses["repos/example/project/actions/artifacts/124"] = meta
	f.responses["repos/example/project/actions/artifacts/124/zip"] = data
	result, err := (GitHubPublisher{Command: f.command}).Checks(context.Background(), Plan{Repository: "example/project"}, annotationPublication())
	if err != nil || result.FailureContext == nil || !result.FailureContext.Checks[0].Truncated || !strings.Contains(result.FailureContext.Checks[0].Text, "unsupported archive") || !strings.Contains(result.FailureContext.Checks[0].Text, "CVE-2026-12345") {
		t.Fatal("unrelated archive became a gate or hid supported facts", err)
	}
}

func TestActionsReportFindingsPrecedeRepeatedLogOutput(t *testing.T) {
	f := ciArtifactFailureFixture(t)
	contents := `{"SchemaVersion":2,"Results":[{"Target":"runtime/example.dll","Vulnerabilities":[`
	for i := 0; i < 12; i++ {
		if i > 0 {
			contents += ","
		}
		contents += fmt.Sprintf(`{"VulnerabilityID":"CVE-2026-%05d","PkgName":"Example.Package","InstalledVersion":"1.0","FixedVersion":"1.1","Severity":"HIGH"}`, 12345+i)
	}
	contents += "]}]}"
	data := syntheticCIReportZIP(t, []string{"trivy.json"}, []string{contents})
	digest := sha256.Sum256(data)
	meta := f.responses["repos/example/project/actions/artifacts/123"].(map[string]any)
	meta["size_in_bytes"] = len(data)
	meta["digest"] = "sha256:" + hex.EncodeToString(digest[:])
	f.responses["repos/example/project/actions/artifacts/123/zip"] = data
	log := string(f.responses["repos/example/project/actions/jobs/99/logs"].([]byte)) + strings.Repeat("2026-01-01T12:01:00Z ERROR repeated scan failure with no finding detail\n", 500)
	f.responses["repos/example/project/actions/jobs/99/logs"] = []byte(log)
	result, err := (GitHubPublisher{Command: f.command}).Checks(context.Background(), Plan{Repository: "example/project"}, annotationPublication())
	if err != nil || result.FailureContext == nil {
		t.Fatal(err)
	}
	entry := result.FailureContext.Checks[0]
	if strings.Count(entry.Text, "CVE-2026-") != 12 || len(entry.Text) > maxCIFailureText {
		t.Fatal("actionable report detail was starved by repeated logs", entry.Text)
	}
}

func TestCITrivyZIPEnforcesExpansionAndMemberBounds(t *testing.T) {
	contents := strings.Repeat("x", maxCIArtifactExpandedBytes+1)
	if _, err := ciTrivyZIPFacts(syntheticCIReportZIP(t, []string{"other.txt"}, []string{contents})); err == nil {
		t.Fatal("expansion bound bypassed")
	}
	var names, data []string
	for i := 0; i <= maxCIArtifactMembers; i++ {
		names = append(names, fmt.Sprintf("file-%d.txt", i))
		data = append(data, "x")
	}
	if _, err := ciTrivyZIPFacts(syntheticCIReportZIP(t, names, data)); err == nil {
		t.Fatal("member bound bypassed")
	}
}

func TestActionsTrivyFieldsMaskPrivateValuesBeforeJSONEncoding(t *testing.T) {
	for _, private := range []string{`disposable\private\path`, `disposable "private" value`, "disposable\tprivate\nvalue", "disposable<private>&value"} {
		t.Run(private, func(t *testing.T) {
			f := ciArtifactFailureFixture(t)
			report := map[string]any{"SchemaVersion": 2, "Results": []any{map[string]any{"Target": private, "Vulnerabilities": []any{map[string]any{"VulnerabilityID": "CVE-2026-12345", "PkgName": private, "InstalledVersion": "1.0", "FixedVersion": "1.1", "Severity": "HIGH"}}, "Secrets": []any{map[string]any{"RuleID": private, "StartLine": 3, "EndLine": 3, "Severity": "HIGH", "Match": "DO-NOT-RETAIN-secret"}}}}}
			content, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			data := syntheticCIReportZIP(t, []string{"trivy.json"}, []string{string(content)})
			digest := sha256.Sum256(data)
			meta := f.responses["repos/example/project/actions/artifacts/123"].(map[string]any)
			meta["size_in_bytes"] = len(data)
			meta["digest"] = "sha256:" + hex.EncodeToString(digest[:])
			f.responses["repos/example/project/actions/artifacts/123/zip"] = data
			result, err := (GitHubPublisher{Command: f.command, CIPrivateValues: []string{private}}).Checks(context.Background(), Plan{Repository: "example/project"}, annotationPublication())
			if err != nil || result.FailureContext == nil {
				t.Fatal(err)
			}
			escaped, _ := json.Marshal(private)
			entry := result.FailureContext.Checks[0]
			if strings.Contains(entry.Text, private) || strings.Contains(entry.Text, string(escaped[1:len(escaped)-1])) || !entry.Redacted || !strings.Contains(entry.Text, "CVE-2026-12345") {
				t.Fatal("escaped private report field bypassed masking", entry)
			}
		})
	}
}

func TestActionsUnsupportedTrivySchemaReportsIncompleteIntake(t *testing.T) {
	f := ciArtifactFailureFixture(t)
	data := syntheticCIReportZIP(t, []string{"trivy.json"}, []string{`{"SchemaVersion":3,"Results":[]}`})
	digest := sha256.Sum256(data)
	meta := f.responses["repos/example/project/actions/artifacts/123"].(map[string]any)
	meta["size_in_bytes"] = len(data)
	meta["digest"] = "sha256:" + hex.EncodeToString(digest[:])
	f.responses["repos/example/project/actions/artifacts/123/zip"] = data
	result, err := (GitHubPublisher{Command: f.command}).Checks(context.Background(), Plan{Repository: "example/project"}, annotationPublication())
	if err != nil || result.FailureContext == nil || !result.FailureContext.Checks[0].Truncated || !strings.Contains(result.FailureContext.Checks[0].Text, "unsupported Trivy schema; report contents unknown") {
		t.Fatal("unsupported schema looked like confirmed empty findings", err)
	}
}

func TestActionsArtifactWithoutSupportedReportMarksIntakeUnknown(t *testing.T) {
	f := ciArtifactFailureFixture(t)
	data := syntheticCIReportZIP(t, []string{"results.trx"}, []string{"DO-NOT-RETAIN-unrelated-test-source"})
	digest := sha256.Sum256(data)
	meta := f.responses["repos/example/project/actions/artifacts/123"].(map[string]any)
	meta["size_in_bytes"] = len(data)
	meta["digest"] = "sha256:" + hex.EncodeToString(digest[:])
	f.responses["repos/example/project/actions/artifacts/123/zip"] = data
	result, err := (GitHubPublisher{Command: f.command}).Checks(context.Background(), Plan{Repository: "example/project"}, annotationPublication())
	if err != nil || result.FailureContext == nil {
		t.Fatal(err)
	}
	entry := result.FailureContext.Checks[0]
	if !entry.Truncated || !strings.Contains(entry.Text, "no supported report members; report contents unknown") || strings.Contains(entry.Text, "DO-NOT-RETAIN") {
		t.Fatal("unrecognized optional report looked like confirmed empty findings", entry)
	}
}
