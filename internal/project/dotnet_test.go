package project

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func dotnetFixture(t *testing.T) string {
	t.Helper()
	root := repo(t)
	write(t, root, "example.slnx", `<Solution><Project Path="src/Example/Example.csproj"/><Project Path="tests/Example.Tests/Example.Tests.csproj"/><Project Path="tests/Example.IntegrationTests/Example.IntegrationTests.csproj"/></Solution>`)
	write(t, root, "src/Example/Example.csproj", `<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net10.0</TargetFramework></PropertyGroup></Project>`)
	for _, name := range []string{"Example.Tests", "Example.IntegrationTests"} {
		write(t, root, "tests/"+name+"/"+name+".csproj", `<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net10.0</TargetFramework><OutputType>Exe</OutputType><UseMicrosoftTestingPlatformRunner>true</UseMicrosoftTestingPlatformRunner></PropertyGroup><ItemGroup><PackageReference Include="xunit.v3" Version="4.0.1"/></ItemGroup></Project>`)
	}
	write(t, root, "global.json", `{"test":{"runner":"Microsoft.Testing.Platform"}}`)
	write(t, root, "compose.yml", "services: {}\n")
	write(t, root, "CONTRIBUTING.md", "# Contributing\n\n## Required checks\n\n```bash\ndocker compose up --build -d --wait\nDOTNET_CLI_WORKLOAD_UPDATE_NOTIFY_DISABLE=1 dotnet build example.slnx --no-restore -m:1 -nodeReuse:false --disable-build-servers -v:minimal\nDOTNET_CLI_WORKLOAD_UPDATE_NOTIFY_DISABLE=1 dotnet test --test-modules tests/Example.Tests/bin/Debug/net10.0/Example.Tests.dll --no-build -v:minimal\nDOTNET_CLI_WORKLOAD_UPDATE_NOTIFY_DISABLE=1 dotnet test --test-modules tests/Example.IntegrationTests/bin/Debug/net10.0/Example.IntegrationTests.dll --no-build -v:minimal\ndocker compose down -v --remove-orphans\n```\n")
	write(t, root, ".github/workflows/check.yml", "jobs:\n  checks:\n    steps:\n      - run: dotnet restore example.slnx -p:NuGetAudit=true -p:NuGetAuditMode=all\n")
	return root
}

func TestInitializeDotnetUsesRequiredChecksAndAlwaysTearsDown(t *testing.T) {
	root := dotnetFixture(t)
	result := initAt(t, root)
	if !result.Config.DockerTests || len(result.Config.Checks) == 0 {
		t.Fatalf("documented .NET and Compose checks missing: %+v", result.Config)
	}
	data, err := os.ReadFile(filepath.Join(root, ConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseConfig(data); err != nil {
		t.Fatalf("generated configuration cannot be used by a run: %v", err)
	}
	text := string(data)
	for _, required := range []string{"dotnet", "restore", "NuGetAuditMode=all", "--test-modules", "Example.Tests.dll", "Example.IntegrationTests.dll", "--disable-build-servers"} {
		if !strings.Contains(text, required) {
			t.Fatalf("missing repository requirement %q", required)
		}
	}
	// Execute only disposable local stand-ins to establish cleanup on test failure.
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "commands")
	for name, body := range map[string]string{
		"dotnet": "printf 'dotnet %s\\n' \"$*\" >> \"$CHECK_LOG\"; [ \"$1\" != test ]",
		"docker": "printf 'docker %s\\n' \"$*\" >> \"$CHECK_LOG\"",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	command := result.Config.Checks[0]
	child := exec.Command(command[0], command[1:]...)
	child.Dir = root
	child.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "CHECK_LOG="+log)
	if err := child.Run(); err == nil {
		t.Fatal("failed test was reported successful")
	}
	observed, err := os.ReadFile(log)
	if err != nil || !strings.HasSuffix(string(observed), "docker compose down -v --remove-orphans\n") {
		t.Fatalf("Compose was not torn down after test failure: %q %v", observed, err)
	}
}

func TestInitializeDotnetManifestFallbackMatchesRunner(t *testing.T) {
	for _, runner := range []string{"Microsoft.Testing.Platform", "VSTest"} {
		t.Run(runner, func(t *testing.T) {
			root := dotnetFixture(t)
			os.Remove(filepath.Join(root, "CONTRIBUTING.md"))
			os.RemoveAll(filepath.Join(root, ".github"))
			os.Remove(filepath.Join(root, "compose.yml"))
			if runner == "VSTest" {
				write(t, root, "global.json", `{"test":{"runner":"VSTest"}}`)
				for _, name := range []string{"Example.Tests", "Example.IntegrationTests"} {
					write(t, root, "tests/"+name+"/"+name+".csproj", `<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net10.0</TargetFramework><IsTestProject>true</IsTestProject></PropertyGroup><ItemGroup><PackageReference Include="Microsoft.NET.Test.Sdk" Version="18.0.0"/></ItemGroup></Project>`)
				}
			}
			result, err := Initialize(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(result.Config)
			if len(result.Config.Checks) < 3 || strings.Contains(string(encoded), "--test-modules") != (runner == "Microsoft.Testing.Platform") {
				t.Fatalf("runner-specific checks missing: %s", encoded)
			}
		})
	}
}

func TestForceInitRefreshesChecksAndPreservesPreferencesAndSavedWork(t *testing.T) {
	root := dotnetFixture(t)
	initAt(t, root)
	original := `{"version":1,"checks":[["custom","test"]],"input_files":["README.md"],"branch_format":"{reference}-custom-{ticket}","nuget_cache":{"max_packages":128,"max_bytes":10485760}}`
	write(t, root, ConfigPath, original)
	checkpoint := ".sdlc/work/Example/series/journal.json"
	write(t, root, checkpoint, "Saved execution state is outside initialization scope")
	if result := initAt(t, root); result.Config.Checks[0][0] != "custom" || result.ConfigUpdated {
		t.Fatal("ordinary init replaced custom checks")
	}
	result, err := InitializeWithOptions(context.Background(), root, InitOptions{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.ConfigUpdated || !result.Config.DockerTests || result.Config.BranchFormat != "{reference}-custom-{ticket}" || len(result.Config.InputFiles) != 1 || result.Config.InputFiles[0] != "README.md" || result.Config.NugetCache == nil || result.Config.NugetCache.MaxPackages != 128 {
		t.Fatalf("forced refresh lost preferences or did not refresh checks: %+v", result)
	}
	backup, err := os.ReadFile(result.ConfigBackup)
	if err != nil || string(backup) != original {
		t.Fatalf("previous settings were not preserved exactly: %v", err)
	}
	info, err := os.Stat(result.ConfigBackup)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("backup is not private")
	}
	relative, _ := filepath.Rel(result.Root, result.ConfigBackup)
	if run(t, root, "check-ignore", "--no-index", relative) == "" {
		t.Fatal("backup is not ignored")
	}
	state, _ := os.ReadFile(filepath.Join(root, checkpoint))
	if string(state) != "Saved execution state is outside initialization scope" {
		t.Fatal("forced init changed saved work")
	}
	repeated, err := InitializeWithOptions(context.Background(), root, InitOptions{Force: true})
	if err != nil || repeated.ConfigUpdated || repeated.ConfigBackup != "" {
		t.Fatalf("identical refresh created another backup: %+v %v", repeated, err)
	}
}

func TestForceInitCannotEraseCustomChecksWhenInspectionIsAmbiguous(t *testing.T) {
	root := dotnetFixture(t)
	initAt(t, root)
	original := `{"version":1,"checks":[["custom","test"]],"input_files":[]}`
	write(t, root, ConfigPath, original)
	write(t, root, "another.slnx", `<Solution/>`)
	if _, err := InitializeWithOptions(context.Background(), root, InitOptions{Force: true}); err == nil {
		t.Fatal("ambiguous inspection replaced existing checks")
	}
	data, _ := os.ReadFile(filepath.Join(root, ConfigPath))
	if string(data) != original {
		t.Fatal("ambiguous force changed configuration")
	}
}

func TestForceInitRepairsInvalidGeneratedFieldsWithoutResettingPreferences(t *testing.T) {
	root := dotnetFixture(t)
	initAt(t, root)
	original := `{"version":1,"checks":[["sh","-c","line one\nline two"]],"docker_tests":"invalid","input_files":["README.md"],"branch_format":"{reference}-custom-{ticket}"}`
	write(t, root, ConfigPath, original)
	result, err := InitializeWithOptions(context.Background(), root, InitOptions{Force: true})
	if err != nil || !result.ConfigUpdated || !result.Config.DockerTests || result.Config.BranchFormat != "{reference}-custom-{ticket}" || len(result.Config.InputFiles) != 1 {
		t.Fatalf("generated fields were not repaired: %+v %v", result, err)
	}
	backup, _ := os.ReadFile(result.ConfigBackup)
	if string(backup) != original {
		t.Fatal("invalid previous settings were not retained exactly")
	}
	write(t, root, ConfigPath, `{"version":1,"checks":[],"input_files":["../outside"]}`)
	if _, err := InitializeWithOptions(context.Background(), root, InitOptions{Force: true}); err == nil {
		t.Fatal("forced init accepted unsafe input preferences")
	}
}

func TestInitializeDotnetUsesCIWhenNoRequiredCheckGuideExists(t *testing.T) {
	root := dotnetFixture(t)
	os.Remove(filepath.Join(root, "CONTRIBUTING.md"))
	write(t, root, ".github/workflows/check.yml", "jobs:\n  checks:\n    steps:\n      - run: dotnet restore example.slnx -p:NuGetAuditMode=all\n      - run: |\n          dotnet build example.slnx --no-restore -c Release\n          dotnet test --test-modules tests/Example.Tests/bin/Release/net10.0/Example.Tests.dll --no-build\n      - run: |\n          docker compose up --build -d --wait\n          dotnet test --test-modules tests/Example.IntegrationTests/bin/Release/net10.0/Example.IntegrationTests.dll --no-build\n      - run: docker compose down -v --remove-orphans\n")
	result := initAt(t, root)
	data, _ := json.Marshal(result.Config)
	if !result.Config.DockerTests || !strings.Contains(string(data), "bin/Release/net10.0") || strings.Contains(string(data), "bin/Debug/net10.0") || !strings.Contains(string(data), "NuGetAuditMode=all") {
		t.Fatalf("CI commands were not used: %s", data)
	}
	if _, err := ParseConfig(data); err != nil {
		t.Fatal(err)
	}
}

func TestInitializeDotnetDeclinesGuidanceWithUnsupportedShellOrRunner(t *testing.T) {
	for _, scenario := range []string{"shell", "runner", "linked project"} {
		t.Run(scenario, func(t *testing.T) {
			root := dotnetFixture(t)
			if scenario == "shell" {
				write(t, root, "CONTRIBUTING.md", "## Required checks\n```sh\ndotnet build example.slnx && echo prepared\ndotnet test --test-modules tests/Example.Tests/bin/Debug/net10.0/Example.Tests.dll\n```\n")
			}
			if scenario == "runner" {
				write(t, root, "global.json", `{"test":{"runner":"VSTest"}}`)
			}
			if scenario == "linked project" {
				path := filepath.Join(root, "tests/Example.Tests/Example.Tests.csproj")
				os.Remove(path)
				if err := os.Symlink(filepath.Join(t.TempDir(), "outside.csproj"), path); err != nil {
					t.Fatal(err)
				}
			}
			result, err := Initialize(context.Background(), root)
			if scenario == "linked project" {
				if err == nil {
					t.Fatal("followed linked project outside the checkout")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Config.Checks) != 0 || len(result.Warnings) == 0 {
				t.Fatalf("ambiguous guidance accepted: %+v", result)
			}
		})
	}
}

func TestInitializeDotnetCollectsAllRequiredCheckBlocks(t *testing.T) {
	root := dotnetFixture(t)
	write(t, root, "CONTRIBUTING.md", "## Required checks\n### Unit checks\n```sh\ndocker compose up --build -d --wait\ndotnet build example.slnx --no-restore\ndotnet test --test-modules tests/Example.Tests/bin/Debug/net10.0/Example.Tests.dll --no-build\n```\n### Integration checks\n```sh\ndotnet test --test-modules tests/Example.IntegrationTests/bin/Debug/net10.0/Example.IntegrationTests.dll --no-build\ndocker compose down -v --remove-orphans\n```\n")
	result := initAt(t, root)
	data, _ := json.Marshal(result.Config)
	if !strings.Contains(string(data), "Example.Tests.dll") || !strings.Contains(string(data), "Example.IntegrationTests.dll") {
		t.Fatalf("required check blocks were omitted: %s", data)
	}
}

func TestInitializeDotnetPreservesDocumentedServiceRestarts(t *testing.T) {
	root := dotnetFixture(t)
	write(t, root, "CONTRIBUTING.md", "## Required checks\n```sh\ndocker compose up --build -d --wait\ndotnet build example.slnx --no-restore\ndotnet test --test-modules tests/Example.Tests/bin/Debug/net10.0/Example.Tests.dll --no-build\ndocker compose down -v --remove-orphans\ndocker compose up --build -d --wait\ndotnet build example.slnx --no-restore\ndotnet test --test-modules tests/Example.IntegrationTests/bin/Debug/net10.0/Example.IntegrationTests.dll --no-build\ndocker compose down -v --remove-orphans\n```\n")
	result := initAt(t, root)
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "docker-operations")
	for name, body := range map[string]string{"dotnet": "exit 0", "docker": "printf '%s\\n' \"$*\" >> \"$CHECK_LOG\""} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	command := result.Config.Checks[0]
	child := exec.Command(command[0], command[1:]...)
	child.Dir = root
	child.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "CHECK_LOG="+log)
	if err := child.Run(); err != nil {
		t.Fatal(err)
	}
	observed, err := os.ReadFile(log)
	want := "compose up --build -d --wait\ncompose down -v --remove-orphans\ncompose up --build -d --wait\ncompose down -v --remove-orphans\n"
	if err != nil || string(observed) != want {
		t.Fatalf("documented service reset was lost: %q %v", observed, err)
	}
}

func TestInitializeDotnetDeclinesCISetupAndWorkingDirectory(t *testing.T) {
	for _, setup := range []string{"          cd src/Example\n", "      - run: python3 scripts/seed.py\n      - run: |\n", "        working-directory: src/Example\n      - run: |\n"} {
		root := dotnetFixture(t)
		os.Remove(filepath.Join(root, "CONTRIBUTING.md"))
		write(t, root, ".github/workflows/check.yml", "jobs:\n  checks:\n    steps:\n      - run: |\n"+setup+"          dotnet build example.slnx\n          dotnet test --test-modules tests/Example.Tests/bin/Debug/net10.0/Example.Tests.dll\n")
		result := initAt(t, root)
		if len(result.Config.Checks) != 0 || len(result.Warnings) == 0 {
			t.Fatalf("CI context was discarded: %+v", result)
		}
	}
}

func TestInitializeDotnetDeclinesCustomOrConditionalModulePaths(t *testing.T) {
	for _, properties := range []string{
		`<PropertyGroup><OutputPath>artifacts/tests</OutputPath></PropertyGroup>`,
		`<PropertyGroup><AppendTargetFrameworkToOutputPath>false</AppendTargetFrameworkToOutputPath></PropertyGroup>`,
		`<PropertyGroup><AppendTargetFrameworkToOutputPath>False</AppendTargetFrameworkToOutputPath></PropertyGroup>`,
		`<PropertyGroup Condition="'$(Configuration)' == 'Release'"><TargetFramework>net9.0</TargetFramework></PropertyGroup>`,
		`<PropertyGroup><TargetFramework Condition="'$(Configuration)' == 'Debug'">net10.0</TargetFramework><TargetFramework Condition="'$(Configuration)' == 'Release'">net9.0</TargetFramework></PropertyGroup>`,
	} {
		root := dotnetFixture(t)
		os.Remove(filepath.Join(root, "CONTRIBUTING.md"))
		os.RemoveAll(filepath.Join(root, ".github"))
		write(t, root, "Directory.Build.props", "<Project>"+properties+"</Project>")
		result := initAt(t, root)
		if len(result.Config.Checks) != 0 || len(result.Warnings) == 0 {
			t.Fatalf("custom module path was guessed: %+v", result)
		}
	}
}
