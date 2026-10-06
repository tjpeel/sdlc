package install

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeRuntimeCandidate(t *testing.T, source string) {
	t.Helper()
	code := `package main
import("encoding/json";"fmt";"os";"os/exec")
func main(){
 if len(os.Args)>1 && os.Args[1]=="--version" { fmt.Println("new-version"); return }
 old,err:=exec.Command(os.Getenv("TEST_INSTALLED"),"--version").Output()
 if err!=nil { panic(err) }
 data,_:=json.Marshal(struct{Candidate string; Args []string; Old string; State string; ProjectRoot string}{os.Args[0],os.Args[1:],string(old),os.Getenv("SDLC_STATE_DIR"),os.Getenv("SDLC_UPDATE_PROJECT_ROOT")})
 file,err:=os.OpenFile(os.Getenv("TEST_RUNTIME_CALL"),os.O_CREATE|os.O_APPEND|os.O_WRONLY,0600);if err!=nil{panic(err)}
 file.Write(append(data,'\n'));file.Close()
 if os.Getenv("TEST_RUNTIME_FAIL")=="1" || (os.Getenv("TEST_RUNTIME_UPDATE_FAIL")=="1" && os.Args[2]=="update") {os.Exit(17)}
 if os.Getenv("TEST_RECEIPT_FAIL")=="1" {p:=os.Getenv("SDLC_STATE_DIR")+"/installation.json";os.Remove(p);os.Mkdir(p,0700)}
}`
	if err := os.WriteFile(filepath.Join(source, "cmd", "sdlc", "main.go"), []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeUsesCandidateBeforeReplacement(t *testing.T) {
	for _, policy := range []string{"source-pins", "dependencies", "bootstrap"} {
		t.Run(policy, func(t *testing.T) {
			source, bin := fixture(t)
			state, _ := canonicalStateDirectory(os.Getenv("SDLC_STATE_DIR"))
			writeCommand(t, source, `"old-version"`)
			installed, err := Build(context.Background(), source, bin, io.Discard, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("TEST_INSTALLED", installed)
			call := filepath.Join(t.TempDir(), "runtime-call.json")
			t.Setenv("TEST_RUNTIME_CALL", call)
			projectRoot, _ := filepath.EvalSymlinks(t.TempDir())
			t.Setenv("SDLC_UPDATE_PROJECT_ROOT", projectRoot)
			if policy == "source-pins" {
				t.Setenv("SDLC_UPDATE_PROJECT_ROOT", "")
				projectRoot, _ = os.Getwd()
				projectRoot, _ = filepath.EvalSymlinks(projectRoot)
			}
			options := Options{StateDirectory: state, Dependencies: policy != "source-pins"}
			if policy == "dependencies" {
				if err := os.WriteFile(filepath.Join(state, "runtime.json"), []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			writeRuntimeCandidate(t, source)
			if _, err := BuildWithOptions(context.Background(), source, bin, options, io.Discard, io.Discard); err != nil {
				t.Fatal(err)
			}
			var invocation struct {
				Candidate   string
				Args        []string
				Old         string
				State       string
				ProjectRoot string
			}
			data, err := os.ReadFile(call)
			if err != nil {
				t.Fatal(err)
			}
			root, _ := filepath.EvalSymlinks(source)
			canonicalBin, _ := filepath.EvalSymlinks(bin)
			wants := [][]string{{"runtime", "build", "--source", root, "--source-pins"}}
			if policy == "dependencies" {
				wants = nil
			}
			if policy != "source-pins" {
				wants = append(wants, []string{"runtime", "update", "--source", root})
			}
			lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
			if len(lines) != len(wants) {
				t.Fatalf("runtime call count: %s", data)
			}
			for index, line := range lines {
				if err := json.Unmarshal(line, &invocation); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(invocation.Args, wants[index]) || invocation.Old != "old-version\n" || invocation.Candidate == installed || filepath.Dir(invocation.Candidate) != canonicalBin || invocation.State != state || invocation.ProjectRoot != projectRoot {
					t.Fatalf("runtime did not prepare from candidate before replacement: %+v", invocation)
				}
			}
			output, err := exec.Command(installed, "--version").Output()
			if err != nil || string(output) != "new-version\n" {
				t.Fatalf("replacement: %s %v", output, err)
			}
			receipt, err := ReadReceipt(state)
			if match, err := receipt.MatchesExecutable(installed); err != nil || !match {
				t.Fatalf("receipt identity: %t %v", match, err)
			}
			if err != nil || receipt.Version != "new-version" {
				t.Fatalf("receipt: %+v %v", receipt, err)
			}
			info, _ := os.Stat(filepath.Join(state, ReceiptFilename))
			if info.Mode().Perm() != 0600 {
				t.Fatal("receipt is not private")
			}
			info, _ = os.Stat(state)
			if info.Mode().Perm() != 0700 {
				t.Fatal("receipt directory is not private")
			}
		})
	}
}

func TestRuntimeFailurePreservesBinaryAndReceipt(t *testing.T) {
	source, bin := fixture(t)
	state, _ := canonicalStateDirectory(os.Getenv("SDLC_STATE_DIR"))
	writeCommand(t, source, `"old-version"`)
	installed, err := Build(context.Background(), source, bin, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(installed)
	receiptBefore, _ := os.ReadFile(filepath.Join(state, ReceiptFilename))
	t.Setenv("TEST_INSTALLED", installed)
	t.Setenv("TEST_RUNTIME_CALL", filepath.Join(t.TempDir(), "call"))
	t.Setenv("TEST_RUNTIME_FAIL", "1")
	writeRuntimeCandidate(t, source)
	if _, err := BuildWithOptions(context.Background(), source, bin, Options{StateDirectory: state}, io.Discard, io.Discard); err == nil || !strings.Contains(err.Error(), "host executable unchanged") {
		t.Fatalf("runtime failure: %v", err)
	}
	after, _ := os.ReadFile(installed)
	receiptAfter, _ := os.ReadFile(filepath.Join(state, ReceiptFilename))
	if !bytes.Equal(before, after) || !bytes.Equal(receiptBefore, receiptAfter) {
		t.Fatal("runtime failure changed host or receipt")
	}
}

func TestDryRunDoesNotBuildOrCreateState(t *testing.T) {
	source, bin := fixture(t)
	writeCommand(t, source, "undefinedSymbol") // A build would fail.
	state := filepath.Join(t.TempDir(), "missing", "state")
	var out bytes.Buffer
	path, err := BuildWithOptions(context.Background(), source, bin, Options{StateDirectory: state, DryRun: true}, &out, io.Discard)
	if err != nil || filepath.Base(path) != executableName() {
		t.Fatalf("dry run: %s %v", path, err)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("dry run created state")
	}
	entries, _ := os.ReadDir(bin)
	if len(entries) != 0 {
		t.Fatal("dry run created build, executable, or lock")
	}
	if !strings.Contains(out.String(), "--source-pins") {
		t.Fatal("preview omitted pin policy")
	}
	if _, err := BuildWithOptions(context.Background(), source, bin, Options{CLIOnly: true, Dependencies: true, DryRun: true}, io.Discard, io.Discard); err == nil {
		t.Fatal("incompatible options accepted")
	}
}

func TestReceiptRejectsUnsafeFiles(t *testing.T) {
	source, bin := fixture(t)
	state, _ := canonicalStateDirectory(os.Getenv("SDLC_STATE_DIR"))
	writeCommand(t, source, `"first"`)
	if _, err := Build(context.Background(), source, bin, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(state, ReceiptFilename)
	saved, _ := os.ReadFile(path)
	cases := map[string]func(){
		"public permissions": func() { os.Chmod(path, 0644) },
		"unknown fields":     func() { os.WriteFile(path, []byte(`{"unexpected":true}`), 0600) },
		"oversized":          func() { os.WriteFile(path, bytes.Repeat([]byte("x"), 16385), 0600) },
		"symlink": func() {
			other := filepath.Join(t.TempDir(), "receipt")
			os.WriteFile(other, saved, 0600)
			os.Remove(path)
			os.Symlink(other, path)
		},
	}
	for name, damage := range cases {
		t.Run(name, func(t *testing.T) {
			os.Remove(path)
			os.WriteFile(path, saved, 0600)
			damage()
			if _, err := ReadReceipt(state); err == nil {
				t.Fatal("unsafe receipt accepted")
			}
		})
	}
	os.Remove(path)
	os.WriteFile(path, saved, 0600)
	if _, err := ValidateSource(t.TempDir()); err == nil {
		t.Fatal("non-source directory accepted")
	}
}

func TestReceiptDetectsReplacedExecutable(t *testing.T) {
	source, bin := fixture(t)
	state := os.Getenv("SDLC_STATE_DIR")
	writeCommand(t, source, `"first"`)
	installed, err := Build(context.Background(), source, bin, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := ReadReceipt(state)
	if err != nil {
		t.Fatal(err)
	}
	if match, err := receipt.MatchesExecutable(installed); err != nil || !match {
		t.Fatalf("initial identity: %t %v", match, err)
	}
	os.WriteFile(installed, []byte("changed"), 0755)
	if match, err := receipt.MatchesExecutable(installed); err != nil || match {
		t.Fatalf("changed identity: %t %v", match, err)
	}
}

func TestReceiptFailureReportsInstalledHost(t *testing.T) {
	source, bin := fixture(t)
	state := os.Getenv("SDLC_STATE_DIR")
	writeCommand(t, source, `"old-version"`)
	installed, err := Build(context.Background(), source, bin, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_INSTALLED", installed)
	t.Setenv("TEST_RUNTIME_CALL", filepath.Join(t.TempDir(), "call"))
	t.Setenv("TEST_RECEIPT_FAIL", "1")
	writeRuntimeCandidate(t, source)
	result, err := BuildWithOptions(context.Background(), source, bin, Options{StateDirectory: state}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "host executable installed (runtime prepared: true), but installation receipt failed") || result != installed {
		t.Fatalf("partial outcome was hidden: %s %v", result, err)
	}
	output, err := exec.Command(installed, "--version").Output()
	if err != nil || string(output) != "new-version\n" {
		t.Fatalf("partial installation: %s %v", output, err)
	}
}

func TestDependencyBootstrapUpdateFailurePreservesHost(t *testing.T) {
	source, bin := fixture(t)
	state := os.Getenv("SDLC_STATE_DIR")
	writeCommand(t, source, `"old-version"`)
	installed, err := Build(context.Background(), source, bin, io.Discard, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(installed)
	receiptBefore, _ := os.ReadFile(filepath.Join(state, ReceiptFilename))
	call := filepath.Join(t.TempDir(), "call")
	t.Setenv("TEST_INSTALLED", installed)
	t.Setenv("TEST_RUNTIME_CALL", call)
	t.Setenv("TEST_RUNTIME_UPDATE_FAIL", "1")
	writeRuntimeCandidate(t, source)
	_, err = BuildWithOptions(context.Background(), source, bin, Options{Dependencies: true, StateDirectory: state}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "host executable unchanged (runtime already prepared: true") {
		t.Fatalf("bootstrap partial runtime failure hidden: %v", err)
	}
	after, _ := os.ReadFile(installed)
	receiptAfter, _ := os.ReadFile(filepath.Join(state, ReceiptFilename))
	if !bytes.Equal(before, after) || !bytes.Equal(receiptBefore, receiptAfter) {
		t.Fatal("bootstrap update failure replaced host or receipt")
	}
	data, _ := os.ReadFile(call)
	if len(bytes.Split(bytes.TrimSpace(data), []byte("\n"))) != 2 {
		t.Fatalf("update was not attempted after bootstrap: %s", data)
	}
}

func TestDependencyBootstrapPreviewShowsBothSteps(t *testing.T) {
	source, bin := fixture(t)
	state := filepath.Join(t.TempDir(), "missing-state")
	var output bytes.Buffer
	_, err := BuildWithOptions(context.Background(), source, bin, Options{Dependencies: true, DryRun: true, StateDirectory: state}, &output, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	text := output.String()
	build := strings.Index(text, "candidate runtime build")
	update := strings.Index(text, "candidate runtime update")
	if build < 0 || update < build {
		t.Fatalf("bootstrap preview omitted update sequence: %s", text)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("bootstrap preview created state")
	}
}

func TestValidateSourceRejectsUnsafeModuleFiles(t *testing.T) {
	for _, kind := range []string{"symlink", "directory", "oversized", "invalid UTF-8"} {
		t.Run(kind, func(t *testing.T) {
			source, _ := fixture(t)
			path := filepath.Join(source, "go.mod")
			switch kind {
			case "symlink":
				other := filepath.Join(t.TempDir(), "module")
				os.WriteFile(other, []byte("module github.com/tjpeel/sdlc\n"), 0600)
				os.Remove(path)
				os.Symlink(other, path)
			case "directory":
				os.Remove(path)
				os.Mkdir(path, 0700)
			case "oversized":
				os.WriteFile(path, append([]byte("module github.com/tjpeel/sdlc\n"), bytes.Repeat([]byte("x"), 64*1024)...), 0600)
			case "invalid UTF-8":
				os.WriteFile(path, append([]byte("module github.com/tjpeel/sdlc\n"), 0xff), 0600)
			}
			if _, err := ValidateSource(source); err == nil {
				t.Fatalf("unsafe go.mod accepted: %s", kind)
			}
		})
	}
}
