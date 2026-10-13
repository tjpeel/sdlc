package project

import (
	"reflect"
	"testing"
)

func TestDiscoverFormattersUsesPinnedPolicyWithoutCheckSelection(t *testing.T) {
	formatters, err := DiscoverFormatters([]byte(`{"version":1,"isRoot":true,"tools":{"CSharpier":{"version":"1.3.0","commands":["csharpier"],"rollForward":false}}}`))
	want := []Formatter{{Name: "csharpier", Extensions: []string{".cs"}, Setup: [][]string{{"dotnet", "tool", "restore"}}, Command: []string{"dotnet", "csharpier", "format"}}}
	if err != nil || !reflect.DeepEqual(formatters, want) {
		t.Fatalf("unexpected policy: %+v %v", formatters, err)
	}
	for _, metadata := range []string{"", `{"version":1,"tools":{"other":{"version":"1.0.0","commands":["other"]}}}`} {
		if policies, err := DiscoverFormatters([]byte(metadata)); err != nil || len(policies) != 0 {
			t.Fatalf("invented policy: %+v %v", policies, err)
		}
	}
	for _, metadata := range []string{
		`{"version":1,"tools":{"csharpier":{"version":"1.3.0-beta.1","commands":["csharpier"]}}}`,
		`{"version":1,"tools":{"csharpier":{"version":"1.3.0","commands":["csharpier","shell"]}}}`,
		`{"version":1,"tools":{"csharpier":{"version":"1.3.0","commands":["csharpier"]},"CSharpier":{"version":"1.2.0","commands":["csharpier"]}}}`,
		`{"version":1,"tools":{"csharpier":{"version":"2.0.0","version":"1.3.0","commands":["csharpier"]}}}`,
	} {
		if _, err := DiscoverFormatters([]byte(metadata)); err == nil {
			t.Fatalf("ambiguous or unsupported metadata admitted: %s", metadata)
		}
	}
}

func TestFormatterForChecksPreservesRootConfigurationAndSafeEnvironment(t *testing.T) {
	policies, err := DiscoverFormatters([]byte(`{"version":1,"tools":{"csharpier":{"version":"1.3.0","commands":["csharpier"]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	checks := [][]string{
		{"env", "DOTNET_CLI_TELEMETRY_OPTOUT=1", "dotnet", "tool", "restore", "--verbosity", "quiet"},
		{"env", "DOTNET_CLI_TELEMETRY_OPTOUT=1", "dotnet", "csharpier", "check", ".", "--config-path", ".config/csharpier.json", "--ignore-path=.config/csharpierignore", "--no-cache"},
		{"sh", "-c", "arbitrary formatter fix"},
	}
	formatter, err := FormatterForChecks(policies[0], checks)
	expected := []string{"env", "DOTNET_CLI_TELEMETRY_OPTOUT=1", "dotnet", "csharpier", "format", "--config-path", ".config/csharpier.json", "--ignore-path", ".config/csharpierignore"}
	if err != nil || !reflect.DeepEqual(formatter.Command, expected) || !reflect.DeepEqual(formatter.Setup, checks[:1]) {
		t.Fatalf("root policy changed: %+v %v", formatter, err)
	}
	for _, command := range [][]string{
		{"dotnet", "csharpier", "check", ".", "--skip-validation"},
		{"dotnet", "csharpier", "check", ".", "--config-path", "../private.json"},
		{"env", "LD_PRELOAD=private", "dotnet", "csharpier", "check", "."},
	} {
		if _, err := FormatterForChecks(policies[0], [][]string{command}); err == nil {
			t.Fatalf("unsafe policy inferred: %v", command)
		}
	}
	conflict := append(checks, []string{"dotnet", "csharpier", "check", ".", "--config-path", "another.json"})
	if _, err := FormatterForChecks(policies[0], conflict); err == nil {
		t.Fatal("ambiguous root config inferred")
	}
}

func TestFormatterForChecksRetainsSupportedSharedFlagsAndRootManifest(t *testing.T) {
	policies, _ := DiscoverFormatters([]byte(`{"version":1,"tools":{"csharpier":{"version":"1.3.0","commands":["csharpier"]}}}`))
	checks := [][]string{{"dotnet", "tool", "restore", "--tool-manifest", ".config/dotnet-tools.json"}, {"dotnet", "csharpier", "check", ".", "--include-generated", "--no-msbuild-check"}}
	formatter, err := FormatterForChecks(policies[0], checks)
	if err != nil || !reflect.DeepEqual(formatter.Command, []string{"dotnet", "csharpier", "format", "--include-generated", "--no-msbuild-check"}) || !reflect.DeepEqual(formatter.Setup, checks[:1]) {
		t.Fatalf("documented shared policy dropped: %+v %v", formatter, err)
	}
}
