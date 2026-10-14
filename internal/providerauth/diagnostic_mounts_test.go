package providerauth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHeadlessDiagnosticMountsAreImplementationOnlyAndSeparate(t *testing.T) {
	request := headlessFixture(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	request.DiagnosticHelper = filepath.Join(root, "helper.py")
	request.DiagnosticRequests = filepath.Join(root, "requests")
	request.DiagnosticResponses = filepath.Join(root, "responses")
	os.WriteFile(request.DiagnosticHelper, []byte("fixed helper"), 0444)
	os.Mkdir(request.DiagnosticRequests, 0777)
	os.Mkdir(request.DiagnosticResponses, 0755)
	if err := validateHeadless(request); err != nil {
		t.Fatal(err)
	}
	args := strings.Join(headlessArgs("image", "worker", "volume", request), " ")
	for _, want := range []string{"dst=/sdlc-diagnostics.py,readonly", "dst=/sdlc-diagnostic-requests", "dst=/sdlc-diagnostic-responses,readonly"} {
		if !strings.Contains(args, want) {
			t.Fatal("missing protected diagnostic mount", args)
		}
	}
	for _, kind := range []string{"review", "missing", "linked", "nested", "writable helper", "session"} {
		t.Run(kind, func(t *testing.T) {
			bad := request
			switch kind {
			case "review":
				bad.ReadOnly = true
			case "missing":
				bad.DiagnosticResponses = ""
			case "nested":
				bad.DiagnosticResponses = bad.DiagnosticRequests
			case "session":
				bad.DiagnosticRequests = request.SessionDirectory
			case "writable helper":
				bad.DiagnosticHelper = filepath.Join(root, "writable.py")
				os.WriteFile(bad.DiagnosticHelper, []byte("untrusted"), 0644)
			case "linked":
				bad.DiagnosticHelper = filepath.Join(root, "linked.py")
				os.Symlink(request.DiagnosticHelper, bad.DiagnosticHelper)
			}
			if validateHeadless(bad) == nil {
				t.Fatal("unsafe diagnostic mounts accepted")
			}
		})
	}
	request.DiagnosticHelper, request.DiagnosticRequests, request.DiagnosticResponses = "", "", ""
	request.ReadOnly = true
	if err := validateHeadless(request); err != nil {
		t.Fatal("ordinary reviewer mounts changed", err)
	}
}
