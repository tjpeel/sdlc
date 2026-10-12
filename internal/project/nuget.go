package project

import "fmt"

// NugetCacheBudget bounds package-only host import for a new check session.
type NugetCacheBudget struct {
	MaxPackages int   `json:"max_packages,omitempty"`
	MaxBytes    int64 `json:"max_bytes,omitempty"`
}

func ResolveNugetCacheBudget(config *NugetCacheBudget) (NugetCacheBudget, error) {
	budget := NugetCacheBudget{MaxPackages: 512, MaxBytes: 256 * 1024 * 1024}
	if config != nil {
		if config.MaxPackages < 0 || config.MaxPackages > 4096 || config.MaxBytes < 0 || config.MaxBytes > 1024*1024*1024 {
			return budget, fmt.Errorf("nuget_cache limits must be positive, at most 4096 packages and 1 GiB; omitted limits use defaults")
		}
		if config.MaxPackages != 0 {
			budget.MaxPackages = config.MaxPackages
		}
		if config.MaxBytes != 0 {
			budget.MaxBytes = config.MaxBytes
		}
	}
	return budget, nil
}
