package workrun

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// CompanionRepositoryRequest asks the controller to make another repository
// available for read-only inspection. The provider never receives GitHub auth.
type CompanionRepositoryRequest struct {
	ID         string `json:"id"`
	Repository string `json:"repository"`
	Purpose    string `json:"purpose"`
}

// CompanionRepository records the private run-local source staged by the
// controller for a provider session.
type CompanionRepository struct {
	Request  CompanionRepositoryRequest `json:"request"`
	Path     string                     `json:"path"`
	Revision string                     `json:"revision"`
}

type CompanionRepositoryCloner interface {
	CloneCompanionRepository(context.Context, Plan, string, string) (string, error)
}

type companionSourceAccessError struct{ repository string }

// Fixed controller diagnostics can be reported without forwarding gh output.
type companionSourceStageError string

func (e companionSourceStageError) Error() string { return string(e) }

func (e *companionSourceAccessError) Error() string {
	return "configured GitHub profile could not clone companion repository " + e.repository
}

func companionCloneRequiresHumanAccess(err error) bool {
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"repository not found",
		"could not resolve to a repository",
		"http 401",
		"http 403",
		"not logged in",
		"authentication required",
		"authentication token",
		"could not read username",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

var companionRepositoryName = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func companionRepositoryPath(repository string) (string, bool) {
	if !companionRepositoryName.MatchString(repository) || strings.Count(repository, "/") != 1 {
		return "", false
	}
	owner, name, _ := strings.Cut(repository, "/")
	if owner == "." || owner == ".." || name == "." || name == ".." {
		return "", false
	}
	return filepath.ToSlash(filepath.Join(".sdlc", "repositories", owner, name)), true
}

func ValidateCompanionRepositoryName(selectedRepository, requested string) error {
	path, ok := companionRepositoryPath(requested)
	if !ok || path == "" {
		return fmt.Errorf("invalid companion repository name")
	}
	if _, ok := companionRepositoryPath(selectedRepository); !ok {
		return fmt.Errorf("invalid selected repository name")
	}
	selectedOwner, _, validSelected := strings.Cut(selectedRepository, "/")
	requestedOwner, _, _ := strings.Cut(requested, "/")
	if !validSelected || !strings.EqualFold(requestedOwner, selectedOwner) {
		return fmt.Errorf("companion repositories must belong to the selected repository owner")
	}
	return nil
}

func validateCompanionRepositoryRequests(requests []CompanionRepositoryRequest, plan *Plan) error {
	if len(requests) > 8 {
		return fmt.Errorf("too many companion repository requests")
	}
	ids := make(map[string]bool, len(requests))
	for _, request := range requests {
		encoded, err := json.Marshal(request)
		if err != nil || len(encoded) > 4096 || !verificationID.MatchString(request.ID) || ids[request.ID] || len(request.Purpose) == 0 || len(request.Purpose) > 1024 || unsafeVerificationText(request.Purpose) {
			return fmt.Errorf("invalid companion repository request")
		}
		if plan != nil {
			if err := ValidateCompanionRepositoryName(plan.Repository, request.Repository); err != nil {
				return err
			}
		} else if _, ok := companionRepositoryPath(request.Repository); !ok {
			return fmt.Errorf("invalid companion repository name")
		}
		ids[request.ID] = true
	}
	return nil
}

func validateCompanionRepositories(repositories []CompanionRepository, plan Plan) error {
	if len(repositories) > 16 {
		return fmt.Errorf("too many staged companion repositories")
	}
	seen := make(map[string]bool, len(repositories))
	for _, repository := range repositories {
		if err := validateCompanionRepositoryRequests([]CompanionRepositoryRequest{repository.Request}, &plan); err != nil {
			return err
		}
		path, _ := companionRepositoryPath(repository.Request.Repository)
		if repository.Path != path || !objectID.MatchString(repository.Revision) || seen[strings.ToLower(repository.Request.Repository)] {
			return fmt.Errorf("invalid staged companion repository")
		}
		seen[strings.ToLower(repository.Request.Repository)] = true
	}
	return nil
}

func (runner Runner) stageCompanionRepositories(ctx context.Context, directory string, journal *Journal, requests []CompanionRepositoryRequest) (string, error) {
	if err := validateCompanionRepositoryRequests(requests, &journal.Plan); err != nil {
		return "", err
	}
	if runner.CompanionCloner == nil {
		return "", fmt.Errorf("companion repository access is unavailable")
	}
	staged := make([]CompanionRepository, 0, len(requests))
	for _, request := range requests {
		path, _ := companionRepositoryPath(request.Repository)
		var existing *CompanionRepository
		for i := range journal.CompanionRepositories {
			stored := &journal.CompanionRepositories[i]
			if stored.Request.ID == request.ID && !strings.EqualFold(stored.Request.Repository, request.Repository) {
				return "", fmt.Errorf("companion request id was reused for another repository")
			}
			if strings.EqualFold(stored.Request.Repository, request.Repository) {
				existing = stored
				break
			}
		}
		if existing != nil {
			revision, err := runner.CompanionCloner.CloneCompanionRepository(ctx, journal.Plan, journal.Workspace, request.Repository)
			if err != nil {
				return "", err
			}
			if revision != existing.Revision {
				return "", fmt.Errorf("staged companion source revision changed")
			}
			staged = append(staged, *existing)
			continue
		}
		if len(journal.CompanionRepositories) >= 16 {
			return "", fmt.Errorf("staged companion repository limit reached")
		}
		revision, err := runner.CompanionCloner.CloneCompanionRepository(ctx, journal.Plan, journal.Workspace, request.Repository)
		if err != nil {
			return "", err
		}
		if !objectID.MatchString(revision) {
			return "", fmt.Errorf("companion checkout returned an invalid revision")
		}
		result := CompanionRepository{Request: request, Path: path, Revision: revision}
		journal.CompanionRepositories = append(journal.CompanionRepositories, result)
		if err := runner.save(directory, journal); err != nil {
			return "", err
		}
		staged = append(staged, result)
	}
	sort.Slice(staged, func(i, j int) bool {
		return strings.ToLower(staged[i].Request.Repository) < strings.ToLower(staged[j].Request.Repository)
	})
	var feedback strings.Builder
	feedback.WriteString("Controller staged companion source using the run's configured GitHub profile. Inspect these default-branch checkouts for context only; do not modify or publish them:")
	for _, repository := range staged {
		fmt.Fprintf(&feedback, "\n- %s at %s (revision %s)", repository.Request.Repository, repository.Path, repository.Revision)
	}
	return feedback.String(), nil
}
