package project

import "testing"

func TestNugetCacheBudgetIsBoundedAndDefaultsAreIndependent(t *testing.T) {
	defaults, err := ResolveNugetCacheBudget(nil)
	if err != nil || defaults.MaxPackages != 512 || defaults.MaxBytes != 256*1024*1024 {
		t.Fatal("unexpected default budget", defaults, err)
	}
	configured, err := ResolveNugetCacheBudget(&NugetCacheBudget{MaxPackages: 96})
	if err != nil || configured.MaxPackages != 96 || configured.MaxBytes != defaults.MaxBytes {
		t.Fatal("partial budget did not preserve the byte bound", configured, err)
	}
	for _, input := range []*NugetCacheBudget{{MaxPackages: -1}, {MaxPackages: 4097}, {MaxBytes: -1}, {MaxBytes: 1024*1024*1024 + 1}} {
		if _, err := ResolveNugetCacheBudget(input); err == nil {
			t.Fatal("unbounded import budget accepted", input)
		}
	}
}

func TestProjectConfigAcceptsOnlyBoundedNugetSettings(t *testing.T) {
	var config Config
	if err := decodeConfig([]byte(`{"version":1,"checks":[],"input_files":[],"nuget_cache":{"max_packages":128,"max_bytes":10485760}}`), &config); err != nil {
		t.Fatal(err)
	}
	if config.NugetCache == nil || config.NugetCache.MaxPackages != 128 || config.NugetCache.MaxBytes != 10485760 {
		t.Fatal("configured cache budget was lost")
	}
	for _, value := range []string{`{"max_packages":4097}`, `{"max_bytes":-1}`, `{"unknown":true}`} {
		if err := decodeConfig([]byte(`{"version":1,"checks":[],"input_files":[],"nuget_cache":`+value+`}`), &config); err == nil {
			t.Fatal("invalid cache configuration accepted", value)
		}
	}
}
