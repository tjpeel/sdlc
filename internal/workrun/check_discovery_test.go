package workrun

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func committedDiscoveryRevision(t *testing.T, root string) Revision {
	t.Helper()
	ctx := context.Background()
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "Update disposable project metadata"}} {
		if _, err := SafeGit(ctx, root, args...); err != nil {
			t.Fatal(err)
		}
	}
	head, err := SafeGit(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	tree, err := SafeGit(ctx, root, "rev-parse", "HEAD^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	return Revision{Head: strings.TrimSpace(head), Tree: strings.TrimSpace(tree), Clean: true}
}
func TestRuntimeDiscoveryUsesCommittedMetadataAndInvalidatesIdentity(t *testing.T) {
	ctx := context.Background()
	root, _ := sourceFixture(t)
	sourceWrite(t, root, "package.json", `{"scripts":{"test":"test-original"}}`)
	sourceWrite(t, root, "package-lock.json", strings.Repeat("x", 256*1024))
	revision := committedDiscoveryRevision(t, root)
	j := &Journal{Workspace: root, Plan: Plan{CheckSelection: "runtime", Checks: [][]string{{"frozen", "preview"}}}}
	if err := resolveCandidateChecks(ctx, j, revision); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(j.EffectiveChecks, [][]string{{"npm", "run", "test"}}) || j.Plan.Checks[0][0] != "frozen" {
		t.Fatal("selection changed or discovery missing", j)
	}
	key := verificationKey(j, VerificationRequest{}, revision.Head, revision.Tree)
	sourceWrite(t, root, "package.json", `{"scripts":{"lint":"uncommitted script"}}`)
	sourceWrite(t, root, "go.mod", "module ignored.example/untracked\n")
	if err := resolveCandidateChecks(ctx, j, revision); err != nil || !reflect.DeepEqual(j.EffectiveChecks, [][]string{{"npm", "run", "test"}}) {
		t.Fatal("working files influenced committed discovery", err, j.EffectiveChecks)
	}
	// The effective command identity independently participates in cache keys.
	j.EffectiveChecks = [][]string{{"npm", "run", "lint"}}
	if key == verificationKey(j, VerificationRequest{}, revision.Head, revision.Tree) {
		t.Fatal("resolved command change reused verification identity")
	}
	revision = committedDiscoveryRevision(t, root)
	if err := resolveCandidateChecks(ctx, j, revision); err != nil || !reflect.DeepEqual(j.EffectiveChecks, [][]string{{"go", "test", "./..."}, {"go", "vet", "./..."}, {"npm", "run", "lint"}}) {
		t.Fatal("committed metadata change ignored", err, j.EffectiveChecks)
	}
}
func TestRuntimeDiscoveryBlocksMissingAmbiguousAndDockerEscalation(t *testing.T) {
	for _, kind := range []string{"missing", "ambiguous", "docker"} {
		t.Run(kind, func(t *testing.T) {
			root, _ := sourceFixture(t)
			switch kind {
			case "missing":
				sourceWrite(t, root, "package.json", `{"scripts":{}}`)
			case "ambiguous":
				sourceWrite(t, root, "package.json", `{"scripts":{"test":"test"}}`)
				sourceWrite(t, root, "package-lock.json", "{}")
				sourceWrite(t, root, "yarn.lock", "disposable")
			case "docker":
				sourceWrite(t, root, "example.csproj", `<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net10.0</TargetFramework><IsTestProject>true</IsTestProject></PropertyGroup><ItemGroup><PackageReference Include="Microsoft.NET.Test.Sdk" Version="18.0.0"/></ItemGroup></Project>`)
				sourceWrite(t, root, "compose.yml", "services: {}\n")
				sourceWrite(t, root, "CONTRIBUTING.md", "## Required checks\n\n```bash\ndocker compose up -d --wait\ndotnet build example.csproj --no-restore\ndotnet test example.csproj\ndocker compose down -v\n```\n")
			}
			revision := committedDiscoveryRevision(t, root)
			j := &Journal{Workspace: root, Plan: Plan{CheckSelection: "runtime"}, EffectiveChecks: [][]string{{"old", "pass"}}}
			err := resolveCandidateChecks(context.Background(), j, revision)
			if err == nil || len(j.EffectiveChecks) != 0 {
				t.Fatal("unsafe discovery reused old suite", err, j.EffectiveChecks)
			}
			if kind == "docker" && !strings.Contains(err.Error(), "Docker capability absent") {
				t.Fatal("capability guard not reached", err)
			}
		})
	}
}
func TestLegacyAndExplicitChecksRemainFrozen(t *testing.T) {
	for _, mode := range []string{"", "explicit"} {
		j := &Journal{Plan: Plan{CheckSelection: mode, Checks: [][]string{{"custom", "test"}}}}
		if err := resolveCandidateChecks(context.Background(), j, Revision{}); err != nil || !reflect.DeepEqual(effectiveChecks(j), j.Plan.Checks) {
			t.Fatal("legacy or explicit suite migrated", mode, err)
		}
	}
}

func TestGoDiscoveryIgnoresUnusedDotnetGuidance(t *testing.T) {
	root, _ := sourceFixture(t)
	sourceWrite(t, root, "go.mod", "module example.invalid/project\n\ngo 1.26\n")
	sourceWrite(t, root, "README.md", strings.Repeat("unrelated guidance\n", 10000))
	sourceWrite(t, root, "examples/unused.csproj", strings.Repeat("unused example\n", 10000))
	revision := committedDiscoveryRevision(t, root)
	j := &Journal{Workspace: root, Plan: Plan{CheckSelection: "runtime"}}
	if err := resolveCandidateChecks(context.Background(), j, revision); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(j.EffectiveChecks, [][]string{{"go", "test", "./..."}, {"go", "vet", "./..."}}) {
		t.Fatal("unused metadata changed the Go suite", j.EffectiveChecks)
	}
}
