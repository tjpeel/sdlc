// Package runtimeupdates compares an immutable runtime inventory with public metadata.
package runtimeupdates

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tjpeel/sdlc/internal/runtimeimage"
)

const (
	Current         = "current"
	Update          = "update available"
	Ahead           = "installed ahead"
	Changed         = "revision differs"
	Unavailable     = "unavailable"
	Offline         = "not checked (offline)"
	maximumResponse = 8 << 20
)

type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}
type Checker struct{ Client HTTPClient }
type Result struct {
	Name, Installed, Candidate, Track, Status, Detail string
	Kind, Source                                      string
}
type Report struct {
	Results      []Result
	Packages     []runtimeimage.PackageUpdate
	PackageError string
	PackageCount int
	Offline      bool
}

func New() Checker {
	return Checker{Client: &http.Client{
		Timeout: 12 * time.Second,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) > 2 || request.URL.Scheme != "https" || request.URL.Host != via[0].URL.Host {
				return fmt.Errorf("metadata redirect refused")
			}
			return nil
		},
	}}
}

// Check never writes pins, installs packages or loads account credentials.
// Callers bound the whole check with their context; each HTTP request is also bounded.
func (checker Checker) Check(ctx context.Context, inventory runtimeimage.Inventory, offline bool,
	packages func(context.Context) ([]runtimeimage.PackageUpdate, error)) (Report, error) {
	return checker.check(ctx, inventory, offline, packages, checker.fetcher())
}

func (checker Checker) fetcher() *fetcher {
	client := checker.Client
	if client == nil {
		client = New().Client
	}
	return &fetcher{client: client, responses: make(map[string]*response)}
}

func (checker Checker) check(ctx context.Context, inventory runtimeimage.Inventory, offline bool,
	packages func(context.Context) ([]runtimeimage.PackageUpdate, error), fetch *fetcher) (Report, error) {
	report := Report{Results: make([]Result, len(inventory.Dependencies)), PackageCount: len(inventory.Packages), Offline: offline}
	if err := runtimeimage.ValidateInventory(inventory); err != nil {
		return report, err
	}
	for i, dependency := range inventory.Dependencies {
		report.Results[i] = Result{Name: dependency.Name, Kind: dependency.Kind, Source: dependency.Source, Installed: dependency.Version, Track: dependency.Track, Status: Offline}
	}
	if offline {
		return report, nil
	}
	jobs := make(chan int, len(inventory.Dependencies))
	for i := range inventory.Dependencies {
		jobs <- i
	}
	close(jobs)
	var workers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range jobs {
				report.Results[i] = fetch.check(ctx, inventory.Dependencies[i])
			}
		}()
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		if packages == nil {
			report.PackageError = "package metadata checker unavailable"
			return
		}
		var err error
		report.Packages, err = packages(ctx)
		if err != nil {
			report.PackageError = "Debian metadata refresh failed or timed out"
			return
		}
		// Results must describe every package in the immutable inventory exactly once.
		expected := make(map[string]string, len(inventory.Packages))
		for _, p := range inventory.Packages {
			expected[p.Name+":"+p.Architecture] = p.Version
		}
		for _, p := range report.Packages {
			key := p.Name + ":" + p.Architecture
			version, found := expected[key]
			if !found || version != p.Installed {
				report.PackageError = "Debian package inventory mismatch"
				break
			}
			delete(expected, key)
		}
		if len(expected) != 0 {
			report.PackageError = "Debian package check incomplete"
		}
		if report.PackageError != "" {
			report.Packages = nil
		}
	}()
	workers.Wait()
	return report, nil
}

func (report Report) Incomplete() bool {
	if report.Offline {
		return false
	}
	if report.PackageError != "" {
		return true
	}
	for _, result := range report.Results {
		if result.Status == Unavailable {
			return true
		}
	}
	for _, p := range report.Packages {
		if p.Candidate == "" {
			return true
		}
	}
	return false
}

type response struct {
	data    []byte
	headers http.Header
	err     error
	done    chan struct{}
}
type fetcher struct {
	client    HTTPClient
	mutex     sync.Mutex
	responses map[string]*response
}

func (fetch *fetcher) get(ctx context.Context, address, accept, token string) ([]byte, http.Header, error) {
	key := address + "|" + accept + "|" + token
	fetch.mutex.Lock()
	if cached, ok := fetch.responses[key]; ok {
		fetch.mutex.Unlock()
		select {
		case <-ctx.Done():
			return nil, nil, fmt.Errorf("metadata request cancelled")
		case <-cached.done:
		}
		return cached.data, cached.headers, cached.err
	}
	cached := &response{done: make(chan struct{})}
	fetch.responses[key] = cached
	fetch.mutex.Unlock()
	defer close(cached.done)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		cached.err = fmt.Errorf("invalid metadata endpoint")
		return nil, nil, cached.err
	}
	request.Header.Set("User-Agent", "sdlc-runtime-status")
	request.Header.Set("Accept", accept)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if request.URL.Host == "api.github.com" {
		request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	result, err := fetch.client.Do(request)
	if err != nil {
		cached.err = fmt.Errorf("public metadata request failed or timed out")
		return nil, nil, cached.err
	}
	defer result.Body.Close()
	if result.StatusCode != http.StatusOK {
		cached.err = fmt.Errorf("metadata returned HTTP %d", result.StatusCode)
		return nil, nil, cached.err
	}
	data, err := io.ReadAll(io.LimitReader(result.Body, maximumResponse+1))
	if err != nil || len(data) > maximumResponse {
		cached.err = fmt.Errorf("metadata response invalid or too large")
		return nil, nil, cached.err
	}
	cached.data, cached.headers = data, result.Header.Clone()
	return cached.data, cached.headers, nil
}

func (fetch *fetcher) json(ctx context.Context, address string, target any) error {
	data, _, err := fetch.get(ctx, address, "application/json", "")
	if err != nil {
		return err
	}
	if json.Unmarshal(data, target) != nil {
		return fmt.Errorf("invalid metadata JSON")
	}
	return nil
}

func (fetch *fetcher) check(ctx context.Context, dependency runtimeimage.Dependency) Result {
	result := Result{Name: dependency.Name, Kind: dependency.Kind, Source: dependency.Source, Installed: dependency.Version, Track: dependency.Track, Status: Unavailable}
	var candidate string
	var err error
	switch dependency.Kind {
	case "npm":
		var metadata struct{ Name, Version string }
		tag := dependency.Track
		if tag == "" {
			tag = "latest"
		}
		err = fetch.json(ctx, "https://registry.npmjs.org/"+url.PathEscape(dependency.Source)+"/"+url.PathEscape(tag), &metadata)
		if err == nil && metadata.Name != dependency.Source {
			err = fmt.Errorf("unexpected npm package metadata")
		}
		candidate = metadata.Version
	case "github-release":
		if dependency.Source == "docker/cli" {
			// Docker publishes CLI version tags; it does not consistently publish releases.
			candidate, err = fetch.dockerCLI(ctx)
		} else {
			var release struct {
				Tag               string `json:"tag_name"`
				Draft, Prerelease *bool
			}
			err = fetch.json(ctx, "https://api.github.com/repos/"+dependency.Source+"/releases/latest", &release)
			if err == nil && (release.Draft == nil || release.Prerelease == nil || *release.Draft || *release.Prerelease) {
				err = fmt.Errorf("metadata does not describe a stable release")
			}
			candidate = release.Tag
		}
	case "node":
		var releases []struct{ Version string }
		err = fetch.json(ctx, "https://nodejs.org/dist/index.json", &releases)
		if err == nil {
			for _, release := range releases {
				parsed, valid := version(release.Version)
				if valid && strconv.FormatUint(parsed[0], 10) == dependency.Track && (candidate == "" || compare(release.Version, candidate) > 0) {
					candidate = release.Version
				}
			}
		}
	case "dotnet-sdk", "dotnet-runtime":
		candidate, err = fetch.dotnet(ctx, dependency)
	case "git":
		var commit struct {
			SHA string `json:"sha"`
		}
		err = fetch.json(ctx, "https://api.github.com/repos/"+dependency.Source+"/commits/"+dependency.Track, &commit)
		candidate = commit.SHA
		if err == nil && !validSHA(candidate) {
			err = fmt.Errorf("invalid catalogue commit metadata")
		}
		if err == nil {
			result.Candidate = candidate
			if candidate == dependency.Version {
				result.Status = Current
				return result
			}
			var comparison struct{ Status string }
			err = fetch.json(ctx, "https://api.github.com/repos/"+dependency.Source+"/compare/"+dependency.Version+"..."+candidate, &comparison)
			if err == nil {
				switch comparison.Status {
				case "ahead":
					result.Status = Update
				case "behind":
					result.Status = Ahead
				case "diverged":
					result.Status = Changed
				default:
					err = fmt.Errorf("invalid catalogue comparison metadata")
				}
				if err == nil {
					return result
				}
			}
		}
	case "image":
		candidate, err = fetch.image(ctx, dependency)
		if err == nil {
			result.Candidate = candidate
			result.Status = Current
			if candidate != dependency.Version {
				result.Status = "tag changed"
			}
			return result
		}
	default:
		err = fmt.Errorf("unsupported dependency source")
	}
	installedVersion, candidateVersion := dependency.Version, candidate
	if err == nil && dependency.Kind == "npm" && dependency.Track != "" {
		suffix := "-" + dependency.Track
		if !strings.HasSuffix(installedVersion, suffix) || !strings.HasSuffix(candidateVersion, suffix) {
			err = fmt.Errorf("npm platform version does not match its release tag")
		} else {
			installedVersion = strings.TrimSuffix(installedVersion, suffix)
			candidateVersion = strings.TrimSuffix(candidateVersion, suffix)
		}
	}
	if err == nil {
		if _, valid := version(candidateVersion); !valid {
			err = fmt.Errorf("no valid stable version in metadata")
		}
		if _, valid := version(installedVersion); !valid {
			err = fmt.Errorf("installed version cannot be compared")
		}
	}
	if err != nil {
		result.Detail = err.Error()
		return result
	}
	result.Candidate = strings.TrimPrefix(candidate, "v")
	result.Status = Current
	switch order := compare(candidateVersion, installedVersion); {
	case order > 0:
		result.Status = Update
	case order < 0:
		result.Status = Ahead
	}
	return result
}

func (fetch *fetcher) dockerCLI(ctx context.Context) (string, error) {
	const endpoint = "https://api.github.com/repos/docker/cli/tags"
	address, candidate := endpoint+"?per_page=100", ""
	visited := map[string]bool{}
	for page := 0; page < 20; page++ {
		if visited[address] {
			return "", fmt.Errorf("invalid Docker tag pagination")
		}
		visited[address] = true
		data, headers, err := fetch.get(ctx, address, "application/json", "")
		if err != nil {
			return "", err
		}
		var tags []struct{ Name string }
		if json.Unmarshal(data, &tags) != nil || tags == nil || len(tags) > 100 {
			return "", fmt.Errorf("invalid Docker tag metadata")
		}
		for _, tag := range tags {
			if _, valid := version(tag.Name); valid && (candidate == "" || compare(tag.Name, candidate) > 0) {
				candidate = tag.Name
			}
		}
		next := ""
		for _, link := range strings.Split(headers.Get("Link"), ",") {
			parts := strings.Split(strings.TrimSpace(link), ";")
			if len(parts) < 2 {
				continue
			}
			for _, relation := range parts[1:] {
				if strings.TrimSpace(relation) == `rel="next"` {
					next = strings.TrimSpace(parts[0])
				}
			}
		}
		if next == "" {
			return candidate, nil
		}
		parsed, err := url.Parse(strings.TrimSuffix(strings.TrimPrefix(next, "<"), ">"))
		if err != nil {
			return "", fmt.Errorf("invalid Docker tag pagination")
		}
		validPath := parsed.Path == "/repos/docker/cli/tags"
		// GitHub emits canonical numeric repository URLs in Link headers.
		if strings.HasPrefix(parsed.Path, "/repositories/") && strings.HasSuffix(parsed.Path, "/tags") {
			id := strings.TrimSuffix(strings.TrimPrefix(parsed.Path, "/repositories/"), "/tags")
			if number, err := strconv.ParseUint(id, 10, 64); err == nil && number > 0 {
				validPath = true
			}
		}
		if parsed.Scheme != "https" || parsed.Host != "api.github.com" || !validPath || parsed.User != nil || parsed.Fragment != "" || parsed.Query().Get("per_page") != "100" || len(parsed.Query()["per_page"]) != 1 || len(parsed.Query()["page"]) != 1 {
			return "", fmt.Errorf("invalid Docker tag pagination")
		}
		pageNumber, err := strconv.Atoi(parsed.Query().Get("page"))
		if err != nil || pageNumber != page+2 || len(parsed.Query()) != 2 {
			return "", fmt.Errorf("invalid Docker tag pagination")
		}
		// Use only the validated page parameters, retaining the known repository.
		address = endpoint + "?" + parsed.Query().Encode()
	}
	return "", fmt.Errorf("Docker tag pagination exceeded the check limit")
}

func version(value string) ([3]uint64, bool) {
	var parsed [3]uint64
	parts := strings.Split(strings.TrimPrefix(value, "v"), ".")
	if len(parts) != 3 {
		return parsed, false
	}
	for i, part := range parts {
		if part == "" || len(part) > 1 && part[0] == '0' {
			return parsed, false
		}
		for _, digit := range part {
			if digit < '0' || digit > '9' {
				return parsed, false
			}
		}
		var err error
		parsed[i], err = strconv.ParseUint(part, 10, 64)
		if err != nil {
			return parsed, false
		}
	}
	return parsed, true
}

func compare(left, right string) int {
	a, _ := version(left)
	b, _ := version(right)
	for i := range a {
		if a[i] > b[i] {
			return 1
		}
		if a[i] < b[i] {
			return -1
		}
	}
	return 0
}

func validSHA(value string) bool {
	if len(value) != 40 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func (fetch *fetcher) dotnet(ctx context.Context, dependency runtimeimage.Dependency) (string, error) {
	type component struct{ Version string }
	var metadata struct {
		Channel  string `json:"channel-version"`
		Releases []struct {
			SDK     component
			SDKs    []component
			Runtime component
			ASP     component `json:"aspnetcore-runtime"`
		} `json:"releases"`
	}
	if err := fetch.json(ctx, "https://builds.dotnet.microsoft.com/dotnet/release-metadata/"+dependency.Track+"/releases.json", &metadata); err != nil {
		return "", err
	}
	if metadata.Channel != dependency.Track {
		return "", fmt.Errorf("unexpected .NET release channel")
	}
	latest := ""
	for _, release := range metadata.Releases {
		components := []component{release.Runtime}
		if dependency.Kind == "dotnet-sdk" {
			components = append([]component{release.SDK}, release.SDKs...)
		} else if dependency.Source == "Microsoft.AspNetCore.App" {
			components = []component{release.ASP}
		}
		for _, component := range components {
			if _, valid := version(component.Version); valid && strings.HasPrefix(component.Version, dependency.Track+".") && (latest == "" || compare(component.Version, latest) > 0) {
				latest = component.Version
			}
		}
	}
	return latest, nil
}

func (fetch *fetcher) image(ctx context.Context, dependency runtimeimage.Dependency) (string, error) {
	// This anonymous, read-only registry token grants access to public image metadata.
	// It is unrelated to provider account login and is never persisted or printed.
	var authorization struct{ Token string }
	address := "https://auth.docker.io/token?service=registry.docker.io&scope=" + url.QueryEscape("repository:"+dependency.Source+":pull")
	if err := fetch.json(ctx, address, &authorization); err != nil {
		return "", err
	}
	if authorization.Token == "" || len(authorization.Token) > 16384 || strings.ContainsAny(authorization.Token, "\r\n") {
		return "", fmt.Errorf("invalid public registry authorization")
	}
	accept := "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"
	data, headers, err := fetch.get(ctx, "https://registry-1.docker.io/v2/"+dependency.Source+"/manifests/"+dependency.Track, accept, authorization.Token)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	var manifest struct {
		Schema int `json:"schemaVersion"`
	}
	if headers.Get("Docker-Content-Digest") != digest || json.Unmarshal(data, &manifest) != nil || manifest.Schema != 2 {
		return "", fmt.Errorf("invalid public image manifest")
	}
	return digest, nil
}
