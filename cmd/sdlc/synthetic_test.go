package main

import "testing"

func TestSyntheticPublicSelection(t *testing.T) {
	for _, action := range []string{"login", "status"} {
		got, err := parseAuthOptions([]string{action, "--provider", "synthetic"})
		if err != nil || len(got.providers) != 1 || got.providers[0] != "synthetic" {
			t.Fatalf("%s selection: %+v %v", action, got, err)
		}
	}
	options, err := parseInteractiveOptions([]string{"--provider", "synthetic", "--permission-mode", "ask"})
	if err != nil || options.mode != "ask" {
		t.Fatalf("native mode: %+v %v", options, err)
	}
	config, err := selectRunHeadroom(t.TempDir(), runOptions{provider: "synthetic"})
	if err != nil || config.Enabled() {
		t.Fatalf("Synthetic required foreign proxy: %+v %v", config, err)
	}
}
