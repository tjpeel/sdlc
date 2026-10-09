package testproxy

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"
)

var imageReferencePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]*$`)

func imageRepository(reference string) string {
	reference = strings.ToLower(reference)
	reference = strings.SplitN(reference, "@", 2)[0]
	if colon := strings.LastIndex(reference, ":"); colon > strings.LastIndex(reference, "/") {
		reference = reference[:colon]
	}
	for _, registry := range []string{"docker.io/", "index.docker.io/", "registry-1.docker.io/"} {
		reference = strings.TrimPrefix(reference, registry)
	}
	if strings.HasPrefix(reference, "library/") && strings.Count(reference, "/") == 1 {
		reference = strings.TrimPrefix(reference, "library/")
	}
	return strings.ToLower(reference)
}

// ReservedImageReference identifies controller and session repositories,
// including the qualified Docker Hub spellings of their local aliases.
func ReservedImageReference(reference string) bool {
	repository := imageRepository(reference)
	return repository == "sdlc-test" || strings.HasPrefix(repository, "sdlc-test/") || repository == "sdlc-test-cache" || repository == "sdlc-test-base"
}

func sharedBaseImage(metadata map[string]any) bool {
	id := stringValue(metadata["Id"])
	for _, raw := range array(metadata["RepoTags"]) {
		tag := stringValue(raw)
		if tag == "" || tag == "<none>:<none>" {
			continue
		}
		if !ReservedImageReference(tag) {
			// Pulls and controller seeds are the only writers of ordinary
			// global tags. Builds and user tag operations always use session
			// aliases; private classic intermediates have no ordinary tag.
			return true
		}
		if imageRepository(tag) == "sdlc-test-base" && strings.HasPrefix(id, "sha256:") && len(id) == 71 && tag[strings.LastIndex(tag, ":")+1:] == strings.TrimPrefix(id, "sha256:") {
			return true
		}
	}
	for _, raw := range array(metadata["RepoDigests"]) {
		if digest := stringValue(raw); strings.Contains(digest, "@sha256:") && !ReservedImageReference(digest) {
			return true
		}
	}
	return false
}

func (p *Proxy) imageForSession(ctx context.Context, reference string) (string, map[string]any, error) {
	if !imageReferencePattern.MatchString(reference) {
		return "", nil, errors.New("invalid check image reference")
	}
	p.mu.Lock()
	mapped := p.images[reference]
	p.mu.Unlock()
	if mapped == "" {
		mapped = reference
		if ReservedImageReference(reference) && imageRepository(reference) != "sdlc-test/"+p.config.Session {
			return "", nil, errors.New("image repository is reserved for controller resources")
		}
	}
	metadata, err := p.raw(ctx, "GET", "/images/"+url.PathEscape(mapped)+"/json", nil)
	if err != nil {
		return "", nil, err
	}
	owner := stringValue(object(object(metadata["Config"])["Labels"])[SessionLabel])
	if owner != "" && owner != p.config.Session {
		return "", nil, errors.New("image belongs to another check session")
	}
	if owner == "" && !sharedBaseImage(metadata) {
		return "", nil, errors.New("private build cache is unavailable to check sessions")
	}
	return mapped, metadata, nil
}
