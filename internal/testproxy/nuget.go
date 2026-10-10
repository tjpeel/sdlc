package testproxy

import (
	"errors"
	"strings"
)

// Only controller-supplied sources bypass workspace bind validation. API callers
// still cannot name a session HOME or cache, including another session's cache.
func (p *Proxy) nugetMounts(body, image map[string]any, mounts []any, occupied map[string]bool) ([]any, error) {
	if p.config.NugetCache == "" {
		return mounts, nil
	}
	env := array(body["Env"])
	for _, cache := range []struct{ key, directory string }{{"NUGET_PACKAGES", "packages"}, {"NUGET_SCRATCH", "scratch"}} {
		target := "/tmp/check-home/.nuget/" + cache.directory
		supplied := false
		for _, values := range [][]any{array(image["Env"]), env} {
			for _, raw := range values {
				key, value, ok := strings.Cut(stringValue(raw), "=")
				if ok && key == cache.key {
					target, supplied = value, true
				}
			}
		}
		source := p.config.NugetCache + "/" + cache.directory
		if supplied && target != "/tmp/check-home/.nuget/"+cache.directory {
			if target != "/workspace" && !strings.HasPrefix(target, "/workspace/") {
				return nil, errors.New("NuGet cache override must stay inside this check workspace")
			}
			var err error
			source, err = p.bindSource(target)
			if err != nil {
				return nil, err
			}
			// A workspace override is already accessible through an existing mount.
			covered := false
			for destination := range occupied {
				if target == destination || strings.HasPrefix(target, destination+"/") {
					covered = true
				}
			}
			if covered {
				continue
			}
		} else {
			// Prevent an unrelated user volume or ancestor mount hiding injected caches.
			for destination := range occupied {
				if target == destination || strings.HasPrefix(target, destination+"/") || strings.HasPrefix(destination, target+"/") {
					return nil, errors.New("mount overlaps controller NuGet cache")
				}
			}
		}
		mounts = append(mounts, map[string]any{"Type": "bind", "Source": source, "Target": target})
		occupied[target] = true
		if !supplied {
			env = append(env, cache.key+"="+target)
		}
	}
	body["Env"] = env
	return mounts, nil
}
