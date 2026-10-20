package providerauth

import "testing"

func TestSyntheticResumeAndPermissionValidation(t *testing.T) {
	r := headlessFixture(t)
	r.Provider, r.Model, r.Effort, r.ResumeID = "synthetic", "hf:moonshotai/Kimi-K3", "default", "ses_example123"
	if err := validateHeadless(r); err != nil {
		t.Fatal(err)
	}
	r.Provider = "codex"
	if err := validateHeadless(r); err == nil {
		t.Fatal("OpenCode session admitted for Codex")
	}
	r.Provider, r.ResumeID = "synthetic", "ses_../another"
	if err := validateHeadless(r); err == nil {
		t.Fatal("unsafe session admitted")
	}
	if mode, err := InteractiveMode("synthetic", ""); err != nil || mode != "allow" {
		t.Fatalf("mode=%q err=%v", mode, err)
	}
	if _, err := InteractiveMode("synthetic", "bypassPermissions"); err == nil {
		t.Fatal("foreign client permission mode accepted")
	}
}
