// Package githubprofile stores public account/key bindings in private host state.
// It never reads credentials, vault locators, or native GitHub cache contents.
package githubprofile

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/tjpeel/sdlc/internal/filelock"
	"github.com/tjpeel/sdlc/internal/githubauth"
	"github.com/tjpeel/sdlc/internal/signing"
)

var ErrSelectionRequired = errors.New("repository account selection is missing; pair the account with sdlc github pair, then select it here with sdlc github use --profile NAME")

type Pair struct {
	Version        int    `json:"version"`
	GitHubProfile  string `json:"github_profile"`
	AccountID      int64  `json:"account_id"`
	Login          string `json:"login"`
	SigningProfile string `json:"signing_profile"`
	SigningID      string `json:"signing_id"`
	PublicKey      string `json:"public_key"`
	Fingerprint    string `json:"fingerprint"`
}

func (pair Pair) Validate() error {
	if pair.Version != 1 || pair.AccountID <= 0 || pair.GitHubProfile == "" || pair.SigningProfile == "" ||
		githubauth.ValidateProfile(pair.GitHubProfile) != nil || githubauth.ValidateProfile(pair.SigningProfile) != nil ||
		!regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`).MatchString(pair.Login) || strings.HasSuffix(pair.Login, "-") || strings.Contains(pair.Login, "--") ||
		!regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`).MatchString(pair.SigningID) {
		return fmt.Errorf("invalid GitHub account/signing pairing")
	}
	return signing.ValidatePublicIdentity(pair.PublicKey, pair.Fingerprint)
}

func (pair Pair) CheckSigning(profile signing.Profile) error {
	if pair.SigningID != profile.ID || pair.PublicKey != profile.PublicKey || pair.Fingerprint != profile.Fingerprint {
		return fmt.Errorf("signing key differs from the paired identity; deliberately pair it again with sdlc github pair --replace")
	}
	return nil
}

func pairFile(name string) (string, error) {
	if name == "" || githubauth.ValidateProfile(name) != nil {
		return "", fmt.Errorf("select a valid named GitHub profile")
	}
	return "github-pair." + name + ".local.json", nil
}

func Load(directory, name string) (Pair, error) {
	file, err := pairFile(name)
	if err != nil {
		return Pair{}, err
	}
	var pair Pair
	if err := read(directory, file, &pair); err != nil {
		return Pair{}, err
	}
	if err := pair.Validate(); err != nil {
		return Pair{}, err
	}
	if pair.GitHubProfile != name {
		return Pair{}, fmt.Errorf("GitHub pairing profile does not match its file")
	}
	return pair, nil
}

func Store(directory string, pair Pair, replace bool) error {
	if err := pair.Validate(); err != nil {
		return err
	}
	file, _ := pairFile(pair.GitHubProfile)
	return lockedWrite(directory, file, pair, func() error {
		previous, err := Load(directory, pair.GitHubProfile)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if previous != pair && !replace {
			return fmt.Errorf("GitHub profile is already paired; use github pair --replace deliberately, then github use in affected repositories")
		}
		return nil
	})
}

func List(directory string) ([]Pair, error) {
	root, err := state(directory, false)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer root.Close()
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("cannot list GitHub pairings")
	}
	var pairs []Pair
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "github-pair.") || !strings.HasSuffix(entry.Name(), ".local.json") {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(entry.Name(), "github-pair."), ".local.json")
		pair, err := Load(directory, name)
		if err != nil {
			return nil, err
		}
		pairs = append(pairs, pair)
	}
	return pairs, nil
}

type Selection struct {
	Version    int    `json:"version"`
	Root       string `json:"root"`
	Repository string `json:"repository"`
	Pair       Pair   `json:"pair"`
}

func selectionFile(root string) (string, error) {
	resolved, err := filepath.EvalSymlinks(root)
	info, statErr := os.Stat(root)
	if !filepath.IsAbs(root) || err != nil || resolved != filepath.Clean(root) || statErr != nil || !info.IsDir() {
		return "", fmt.Errorf("repository selection requires a canonical checkout directory")
	}
	return fmt.Sprintf("repository-%x.local.json", sha256.Sum256([]byte(root))), nil
}

func validRepository(repository string) bool {
	parts := strings.Split(repository, "/")
	return len(parts) == 2 && regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`).MatchString(parts[0]) && !strings.HasSuffix(parts[0], "-") && !strings.Contains(parts[0], "--") && regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`).MatchString(parts[1])
}

func LoadSelection(directory, root string) (Selection, error) {
	file, err := selectionFile(root)
	if err != nil {
		return Selection{}, err
	}
	var selection Selection
	if err := read(directory, file, &selection); err != nil {
		return Selection{}, err
	}
	if selection.Version != 1 || selection.Root != root || !validRepository(selection.Repository) || selection.Pair.Validate() != nil {
		return Selection{}, fmt.Errorf("invalid repository account selection")
	}
	return selection, nil
}

func SaveSelection(directory, root, repository string, pair Pair) error {
	file, err := selectionFile(root)
	if err != nil {
		return err
	}
	if !validRepository(repository) {
		return fmt.Errorf("repository must be a GitHub OWNER/REPO")
	}
	if err := pair.Validate(); err != nil {
		return err
	}
	return lockedWrite(directory, file, Selection{1, root, repository, pair}, func() error {
		current, err := Load(directory, pair.GitHubProfile)
		if err != nil {
			return err
		}
		if current != pair {
			return fmt.Errorf("GitHub pairing changed during repository selection")
		}
		_, err = LoadSelection(directory, root)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	})
}

// Select makes no network requests and refuses ambiguous account choices.
func Select(directory, root, repository, explicit string) (Pair, error) {
	if !validRepository(repository) {
		return Pair{}, fmt.Errorf("repository must be a GitHub OWNER/REPO")
	}
	selection, err := LoadSelection(directory, root)
	if err == nil {
		if !strings.EqualFold(selection.Repository, repository) {
			return Pair{}, fmt.Errorf("repository differs from its saved account selection; run sdlc github use --profile NAME deliberately")
		}
		if explicit != "" && explicit != selection.Pair.GitHubProfile {
			return Pair{}, fmt.Errorf("--github-profile conflicts with the saved repository selection; change it with sdlc github use --profile NAME")
		}
		pair, err := Load(directory, selection.Pair.GitHubProfile)
		if err != nil {
			return Pair{}, fmt.Errorf("saved repository pairing is unavailable or unsafe; restore it or run sdlc github use deliberately")
		}
		if pair != selection.Pair {
			return Pair{}, fmt.Errorf("paired identity differs from the repository selection; run sdlc github use --profile NAME deliberately")
		}
		return pair, nil
	}
	if !os.IsNotExist(err) {
		return Pair{}, err
	}
	if explicit != "" {
		return Load(directory, explicit)
	}
	pairs, err := List(directory)
	if err != nil {
		return Pair{}, err
	}
	var matching []Pair
	for _, pair := range pairs {
		if strings.EqualFold(pair.Login, strings.Split(repository, "/")[0]) {
			matching = append(matching, pair)
		}
	}
	if len(matching) == 1 {
		return matching[0], nil
	}
	if len(matching) == 0 && len(pairs) == 1 {
		profiles, err := ListConfigured(directory)
		if err != nil {
			return Pair{}, err
		}
		if len(profiles) == 1 {
			return pairs[0], nil
		}
	}
	if len(matching) == 0 {
		return Pair{}, ErrSelectionRequired
	}
	return Pair{}, fmt.Errorf("repository account selection is ambiguous; select it here with sdlc github use --profile NAME")
}

// state refuses repository-owned and redirected metadata. Missing state is not
// created by reads. os.Root confines all file operations to the checked directory.
func state(directory string, create bool) (*os.Root, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return nil, fmt.Errorf("account metadata must use an absolute private host directory")
	}
	for current := directory; ; current = filepath.Dir(current) {
		if _, err := os.Lstat(filepath.Join(current, ".git")); err == nil || !os.IsNotExist(err) {
			return nil, fmt.Errorf("account metadata must stay outside repositories")
		}
		if info, err := os.Lstat(current); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("account metadata must not follow symlinks")
		} else if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("cannot inspect account metadata directory")
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	if create {
		if err := os.MkdirAll(directory, 0700); err != nil {
			return nil, fmt.Errorf("cannot prepare private account metadata")
		}
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 || owned(info, false) != nil {
		return nil, fmt.Errorf("account metadata directory must be private and owned with mode 0700")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, fmt.Errorf("cannot open private account metadata")
	}
	return root, nil
}

func checked(root *os.Root, file string) (os.FileInfo, error) {
	info, err := root.Lstat(file)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 16384 || owned(info, true) != nil {
		return nil, fmt.Errorf("account metadata file must be private, owned, regular and bounded")
	}
	return info, nil
}

func read(directory, file string, target any) error {
	root, err := state(directory, false)
	if err != nil {
		return err
	}
	defer root.Close()
	info, err := checked(root, file)
	if err != nil {
		return err
	}
	stream, err := root.Open(file)
	if err != nil {
		return fmt.Errorf("cannot read account metadata")
	}
	defer stream.Close()
	opened, err := stream.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return fmt.Errorf("account metadata changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(stream, 16385))
	if err != nil || len(data) > 16384 {
		return fmt.Errorf("cannot read bounded account metadata")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		return fmt.Errorf("invalid account metadata")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return fmt.Errorf("invalid account metadata")
	}
	return nil
}

func lockedWrite(directory, file string, value any, preflight func() error) error {
	root, err := state(directory, true)
	if err != nil {
		return err
	}
	defer root.Close()
	const lockName = "github-pairing.lock"
	lockFile, err := root.OpenFile(lockName, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err == nil {
		lockFile.Close()
	} else if !os.IsExist(err) {
		return fmt.Errorf("cannot prepare account metadata lock")
	}
	info, err := checked(root, lockName)
	if err != nil {
		return err
	}
	lock, err := filelock.Acquire(filepath.Join(directory, lockName))
	if err != nil {
		return fmt.Errorf("account metadata is busy or unsafe")
	}
	defer lock.Close()
	opened, err := lock.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return fmt.Errorf("account metadata lock changed")
	}
	if _, err := checked(root, file); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := preflight(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("cannot encode account metadata")
	}
	temporary, err := os.CreateTemp(directory, ".github-pairing-")
	if err != nil {
		return fmt.Errorf("cannot save account metadata")
	}
	defer os.Remove(temporary.Name())
	_, writeErr := temporary.Write(append(data, '\n'))
	syncErr := temporary.Sync()
	closeErr := temporary.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return fmt.Errorf("cannot save account metadata")
	}
	if err := root.Rename(filepath.Base(temporary.Name()), file); err != nil {
		return fmt.Errorf("cannot save account metadata")
	}
	return nil
}

// Repository stores only the checkout hash and a public GitHub repository name.
type Repository struct {
	Version      int    `json:"version"`
	CheckoutHash string `json:"checkout_hash"`
	Name         string `json:"repository"`
}

func repositoryFile(root string) (string, string, error) {
	_, err := selectionFile(root)
	if err != nil {
		return "", "", err
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(root)))
	return "repository-identity-" + hash + ".local.json", hash, nil
}

func LoadRepository(directory, root string) (string, error) {
	file, hash, err := repositoryFile(root)
	if err != nil {
		return "", err
	}
	var record Repository
	if err := read(directory, file, &record); err != nil {
		return "", err
	}
	if record.Version != 1 || record.CheckoutHash != hash || !validRepository(record.Name) {
		return "", fmt.Errorf("invalid initialized repository identity")
	}
	return record.Name, nil
}

// SaveRepository never replaces an existing identity or deliberately bound selection.
func SaveRepository(directory, root, repository string) error {
	file, hash, err := repositoryFile(root)
	if err != nil {
		return err
	}
	if !validRepository(repository) {
		return fmt.Errorf("repository must be a GitHub OWNER/REPO")
	}
	// Validate both records before deciding to preserve either one.
	_, selectionErr := LoadSelection(directory, root)
	if selectionErr != nil && !os.IsNotExist(selectionErr) {
		return selectionErr
	}
	_, previousErr := LoadRepository(directory, root)
	if previousErr != nil && !os.IsNotExist(previousErr) {
		return previousErr
	}
	if selectionErr == nil || previousErr == nil {
		return nil
	}
	return lockedWrite(directory, file, Repository{1, hash, repository}, func() error {
		_, err := LoadSelection(directory, root)
		if err == nil {
			return fmt.Errorf("repository selection changed during initialization; retry init")
		}
		if !os.IsNotExist(err) {
			return err
		}
		_, err = LoadRepository(directory, root)
		if err == nil {
			return fmt.Errorf("repository identity changed during initialization; retry init")
		}
		if os.IsNotExist(err) {
			return nil
		}
		return err
	})
}

type ConfiguredProfile struct {
	Name   string
	Native bool
	Pair   *Pair
}

// ListConfigured reads installation metadata and public pairs, never native caches.
func ListConfigured(directory string) ([]ConfiguredProfile, error) {
	pairs, err := List(directory)
	if err != nil {
		return nil, err
	}
	profiles := map[string]ConfiguredProfile{}
	for _, pair := range pairs {
		profiles[pair.GitHubProfile] = ConfiguredProfile{Name: pair.GitHubProfile, Pair: &pair}
	}
	root, err := state(directory, false)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer root.Close()
	stream, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	entries, err := stream.ReadDir(-1)
	if err != nil {
		return nil, fmt.Errorf("cannot list GitHub profiles")
	}
	for _, entry := range entries {
		name := ""
		if entry.Name() == "github-installation.json" {
			name = "default"
		} else if strings.HasPrefix(entry.Name(), "github-installation-profile-") && strings.HasSuffix(entry.Name(), ".json") {
			name = strings.TrimSuffix(strings.TrimPrefix(entry.Name(), "github-installation-profile-"), ".json")
			if name == "default" || name == "" || githubauth.ValidateProfile(name) != nil {
				return nil, fmt.Errorf("invalid native GitHub profile metadata filename")
			}
		} else {
			continue
		}
		var record struct {
			ID string `json:"id"`
		}
		if err := read(directory, entry.Name(), &record); err != nil {
			return nil, err
		}
		if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(record.ID) {
			return nil, fmt.Errorf("invalid native GitHub installation metadata")
		}
		profile := profiles[name]
		profile.Name, profile.Native = name, true
		profiles[name] = profile
	}
	result := make([]ConfiguredProfile, 0, len(profiles))
	for _, profile := range profiles {
		result = append(result, profile)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}
