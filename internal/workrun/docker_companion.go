package workrun

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/tjpeel/sdlc/internal/githubauth"
)

func (publisher DockerPublisher) CloneCompanionRepository(ctx context.Context, plan Plan, workspace, repository string) (revision string, resultErr error) {
	defer func() {
		var access *companionSourceAccessError
		if resultErr != nil && !errors.As(resultErr, &access) {
			// Every error returned here is a fixed controller message; raw
			// client stderr stays outside the provider prompt and journal.
			resultErr = companionSourceStageError(resultErr.Error())
		}
	}()
	if err := ValidateCompanionRepositoryName(plan.Repository, repository); err != nil {
		return "", err
	}
	if publisher.Auth.Profile != plan.GitHubProfile || plan.PublicationIdentity == nil || plan.PublicationIdentity.GitHubVolume == "" {
		return "", &companionSourceAccessError{repository: repository}
	}
	if publisher.ValidatePair != nil {
		if err := publisher.ValidatePair(); err != nil {
			return "", fmt.Errorf("configured GitHub identity changed")
		}
	}
	if publisher.Runtime.Docker == nil || publisher.ImageID == "" {
		return "", fmt.Errorf("pinned GitHub runtime is unavailable")
	}
	state, err := publisher.Runtime.Status(ctx)
	if err != nil || state.ImageID != publisher.ImageID {
		return "", fmt.Errorf("pinned GitHub runtime changed")
	}
	session, err := publisher.Auth.Acquire(ctx)
	if err != nil {
		return "", &companionSourceAccessError{repository: repository}
	}
	defer session.Close()
	if publisher.ValidatePair != nil {
		if err := publisher.ValidatePair(); err != nil {
			return "", fmt.Errorf("configured GitHub identity changed")
		}
	}
	identity, err := session.Identity(ctx)
	frozen := plan.PublicationIdentity
	frozenProfile, profileErr := githubauth.NormalizeProfile(frozen.GitHubProfile)
	planProfile, planErr := githubauth.NormalizeProfile(plan.GitHubProfile)
	if err != nil || profileErr != nil || planErr != nil || frozenProfile != planProfile || session.Profile != frozenProfile || session.ImageID != publisher.ImageID || session.Volume != frozen.GitHubVolume || identity.ID != frozen.GitHubID || identity.Login != frozen.GitHubLogin {
		return "", &companionSourceAccessError{repository: repository}
	}

	workspace, err = filepath.EvalSymlinks(workspace)
	if err != nil || !filepath.IsAbs(workspace) {
		return "", fmt.Errorf("run workspace is unavailable")
	}
	relative, _ := companionRepositoryPath(repository)
	destination := filepath.Join(workspace, filepath.FromSlash(relative))
	if err := createCompanionDirectories(workspace, filepath.Dir(filepath.FromSlash(relative))); err != nil {
		return "", fmt.Errorf("cannot prepare private companion source directory")
	}
	if err := excludeCompanionSource(workspace); err != nil {
		return "", fmt.Errorf("cannot exclude companion source from the candidate repository")
	}
	if info, err := os.Lstat(destination); err == nil {
		// A completed copy may outlive a controller that stopped before saving
		// its journal. Validate and adopt it without replacing local material.
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("existing companion source is not a regular checkout")
		}
		revision, err := validateCompanionCheckout(ctx, destination, repository)
		if err != nil {
			return "", err
		}
		if err := makeCompanionReadOnly(filepath.Join(workspace, ".sdlc", "repositories")); err != nil {
			return "", fmt.Errorf("cannot protect companion source from provider edits")
		}
		return revision, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("cannot inspect companion source destination")
	}
	staging, err := os.MkdirTemp(filepath.Dir(destination), ".companion-")
	if err != nil {
		return "", fmt.Errorf("cannot prepare companion source staging")
	}
	defer removeCompanionStaging(staging)
	nameID, err := NewID()
	if err != nil {
		return "", fmt.Errorf("cannot allocate companion source container")
	}
	name := "sdlc-companion-" + nameID
	args := companionCloneArguments(publisher.ImageID, name, session.Volume, repository)
	// Keep tmpfs mounted through export. docker cp cannot read tmpfs, so use
	// Docker's documented exec/tar route while the container is running.
	cloneCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	defer publisher.cleanupCompanionContainer(name)
	if _, err := publisher.Runtime.Docker.Output(cloneCtx, args...); err != nil {
		return "", fmt.Errorf("cannot start the companion source reader")
	}
	_, cloneErr := publisher.Runtime.Docker.Output(cloneCtx, companionCloneCommand(name, repository)...)
	if cloneErr != nil {
		if companionCloneRequiresHumanAccess(cloneErr) {
			return "", &companionSourceAccessError{repository: repository}
		}
		return "", fmt.Errorf("official GitHub client failed during companion clone")
	}
	archive, copyErr := publisher.Runtime.Docker.Output(cloneCtx, companionCopyArguments(name)...)
	cleanupErr := publisher.cleanupCompanionContainer(name)
	if copyErr != nil || cleanupErr != nil {
		return "", fmt.Errorf("controller could not copy the companion checkout into the private run workspace")
	}
	if err := extractCompanionArchive(archive, staging); err != nil {
		return "", fmt.Errorf("companion source archive failed local validation")
	}
	revision, err = validateCompanionCheckout(ctx, staging, repository)
	if err != nil {
		return "", err
	}
	if err := makeCompanionReadOnly(staging); err != nil {
		return "", fmt.Errorf("cannot protect staged companion source")
	}
	if err := os.Rename(staging, destination); err != nil {
		return "", fmt.Errorf("cannot retain validated companion source")
	}
	if err := makeCompanionReadOnly(filepath.Join(workspace, ".sdlc", "repositories")); err != nil {
		return "", fmt.Errorf("cannot protect companion source from provider edits")
	}
	return revision, nil
}

func validateCompanionCheckout(ctx context.Context, directory, repository string) (string, error) {
	if err := validateSourceMetadata(directory); err != nil {
		return "", fmt.Errorf("companion source failed local metadata validation")
	}
	if err := validateCompanionSourceLinks(directory); err != nil {
		return "", fmt.Errorf("companion source contains an unresolved or escaping link")
	}
	// Orphan recovery must not execute hooks, filters or includes installed by
	// a previous worker. Read only this config before any working-tree command.
	config, err := isolatedGit(ctx, directory, "config", "--no-includes", "--file", filepath.Join(directory, ".git", "config"), "--null", "--list")
	if err != nil {
		return "", fmt.Errorf("companion source has invalid Git configuration")
	}
	allowed := map[string]bool{"core.repositoryformatversion": true, "core.filemode": true, "core.bare": true, "core.logallrefupdates": true, "core.ignorecase": true, "core.precomposeunicode": true, "extensions.objectformat": true, "remote.origin.url": true, "remote.origin.fetch": true}
	for _, entry := range strings.Split(config, "\x00") {
		if entry == "" {
			continue
		}
		key, value, _ := strings.Cut(entry, "\n")
		if key == "remote.origin.gh-resolved" && value == "base" {
			continue
		}
		branch := strings.HasPrefix(key, "branch.") && (strings.HasSuffix(key, ".remote") || strings.HasSuffix(key, ".merge"))
		if !allowed[key] && !branch {
			return "", fmt.Errorf("companion source has unsupported Git configuration")
		}
	}
	revision, err := isolatedGit(ctx, directory, "rev-parse", "--verify", "HEAD")
	revision = strings.TrimSpace(revision)
	if err != nil || !objectID.MatchString(revision) {
		return "", fmt.Errorf("companion source returned an invalid revision")
	}
	shallow, err := isolatedGit(ctx, directory, "rev-parse", "--is-shallow-repository")
	if err != nil || strings.TrimSpace(shallow) != "false" {
		return "", fmt.Errorf("companion source does not retain full Git history")
	}
	status, err := isolatedGit(ctx, directory, "status", "--porcelain", "--untracked-files=all")
	if err != nil || status != "" {
		return "", fmt.Errorf("companion source is not a clean checkout")
	}
	remote, err := isolatedGit(ctx, directory, "remote", "get-url", "origin")
	if err != nil || !companionRemoteMatches(strings.TrimSpace(remote), repository) {
		return "", fmt.Errorf("companion source has an unexpected origin")
	}
	return revision, nil
}

func validateCompanionSourceLinks(directory string) error {
	root, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return err
	}
	return filepath.Walk(directory, func(file string, info os.FileInfo, err error) error {
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			return err
		}
		resolved, err := filepath.EvalSymlinks(file)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, resolved)
		if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("companion link leaves checkout")
		}
		return nil
	})
}

func removeCompanionStaging(root string) {
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err == nil && info.IsDir() {
			return os.Chmod(path, 0700)
		}
		return err
	})
	_ = os.RemoveAll(root)
}

func companionRemoteMatches(remote, repository string) bool {
	remote = strings.TrimSuffix(remote, ".git")
	sshRemote := "git" + "@" + "github.com:" + repository
	sshURL := "ssh://" + "git" + "@" + "github.com/" + repository
	return strings.EqualFold(remote, "https://github.com/"+repository) || strings.EqualFold(remote, sshRemote) || strings.EqualFold(remote, sshURL)
}

func companionCloneArguments(image, name, volume, repository string) []string {
	installation := strings.TrimSuffix(strings.TrimPrefix(volume, "sdlc-github-auth-"), "-github")
	args := []string{"run", "--detach", "--name", name,
		"--label", "io.sdlc.managed=true", "--label", "io.sdlc.kind=companion-source", "--label", "io.sdlc.provider=github", "--label", "io.sdlc.installation=" + installation,
		"--pull", "never", "--network", "bridge", "--user", "1000:1000", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--pids-limit", "64", "--memory", "1g", "--cpus", "1", "--log-driver", "none",
		"--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=128m,mode=1777",
		"--mount", "type=volume,src=" + volume + ",dst=/github-auth,readonly,volume-nocopy",
		"--workdir", "/tmp",
		"--env", "HOME=/tmp", "--env", "GH_CONFIG_DIR=/github-auth", "--env", "GH_HOST=github.com", "--env", "GH_PROMPT_DISABLED=1",
		"--entrypoint", "/bin/sleep"}
	args = checkContainerEnvironment(args)
	return append(args, image, "infinity")
}

func companionCloneCommand(name, repository string) []string {
	// Preserve history for ticket references to earlier commits. Do not add a
	// fork's upstream remote or recursively clone unrelated submodules.
	return []string{"exec", name, "/usr/local/bin/gh", "repo", "clone", "https://github.com/" + repository, "/tmp/companion", "--no-upstream"}
}

func companionCopyArguments(name string) []string {
	return []string{"exec", name, "/bin/tar", "-C", "/tmp/companion", "-czf", "-", "."}
}

func extractCompanionArchive(data []byte, destination string) error {
	const maximumArchive = 256 * 1024 * 1024
	const maximumSource = 128 * 1024 * 1024 // The source reader's tmpfs budget.
	if len(data) > maximumArchive {
		return fmt.Errorf("companion archive exceeds limit")
	}
	compressed, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer compressed.Close()
	bounded := &io.LimitedReader{R: compressed, N: maximumArchive + 1}
	archive := tar.NewReader(bounded)
	var total int64
	for entries := 0; ; entries++ {
		header, err := archive.Next()
		if err == io.EOF {
			if _, err := io.Copy(io.Discard, bounded); err != nil || bounded.N <= 0 {
				return fmt.Errorf("invalid companion archive stream")
			}
			return nil
		}
		if err != nil || entries >= 100000 {
			return fmt.Errorf("invalid companion archive")
		}
		name := strings.TrimPrefix(header.Name, "./")
		if (name == "." || name == "") && header.Typeflag == tar.TypeDir {
			continue
		}
		name = strings.TrimSuffix(name, "/")
		if len(name) > 4096 || path.IsAbs(name) || filepath.VolumeName(filepath.FromSlash(name)) != "" || strings.ContainsAny(name, "\\\x00\r\n") {
			return fmt.Errorf("unsafe companion archive path")
		}
		for _, part := range strings.Split(name, "/") {
			if part == "" || part == "." || part == ".." {
				return fmt.Errorf("unsafe companion archive path")
			}
		}
		file := filepath.Join(destination, filepath.FromSlash(name))
		if parent := filepath.Dir(filepath.FromSlash(name)); parent != "." {
			if err := createCompanionDirectories(destination, parent); err != nil {
				return err
			}
		}
		if err := realDirectory(filepath.Dir(file)); err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.Mkdir(file, 0755); err != nil && !os.IsExist(err) {
				return err
			}
			if err := realDirectory(file); err != nil {
				return err
			}
		case tar.TypeReg:
			if header.Size < 0 || header.Size > maximumSource-total {
				return fmt.Errorf("companion source exceeds limit")
			}
			mode := os.FileMode(0644)
			if header.Mode&0111 != 0 {
				mode = 0755
			}
			output, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
			if err != nil {
				return err
			}
			n, copyErr := io.CopyN(output, archive, header.Size)
			closeErr := output.Close()
			if copyErr != nil || closeErr != nil {
				return fmt.Errorf("cannot extract companion source")
			}
			total += n
		case tar.TypeSymlink:
			target := path.Clean(path.Join(path.Dir(name), header.Linkname))
			if header.Linkname == "" || path.IsAbs(header.Linkname) || filepath.VolumeName(filepath.FromSlash(header.Linkname)) != "" || strings.ContainsAny(header.Linkname, "\\\x00\r\n") || target == ".." || strings.HasPrefix(target, "../") {
				return fmt.Errorf("unsafe companion source link")
			}
			if err := os.Symlink(header.Linkname, file); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported companion archive entry")
		}
	}
}

func (publisher DockerPublisher) cleanupCompanionContainer(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	listed, err := publisher.Runtime.Docker.Output(ctx, "ps", "--all", "--filter", "name=^/"+name+"$", "--format", "{{.Names}}")
	if err != nil || strings.TrimSpace(string(listed)) != name {
		return err
	}
	_, err = publisher.Runtime.Docker.Output(ctx, "rm", "--force", name)
	return err
}

func createCompanionDirectories(workspace, relative string) error {
	root, err := filepath.Abs(workspace)
	if err != nil {
		return err
	}
	current := root
	for _, part := range strings.Split(filepath.Clean(relative), string(filepath.Separator)) {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("unsafe companion directory")
		}
		current = filepath.Join(current, part)
		if err := os.Mkdir(current, 0700); err != nil && !os.IsExist(err) {
			return err
		}
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe companion directory")
		}
		mode := os.FileMode(0755)
		if current == filepath.Join(root, ".sdlc") {
			// Preserve existing workspace access and let UID 1000 traverse an
			// ancestor created by a different host UID.
			mode = info.Mode().Perm() | 0111
		}
		if err := os.Chmod(current, mode); err != nil {
			return err
		}
	}
	return nil
}

func makeCompanionReadOnly(root string) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if info.IsDir() {
			return os.Chmod(path, 0555)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported companion source file")
		}
		mode := os.FileMode(0444)
		if info.Mode()&0111 != 0 {
			mode = 0555
		}
		return os.Chmod(path, mode)
	})
}

func excludeCompanionSource(workspace string) error {
	gitDir := filepath.Join(workspace, ".git")
	gitInfo, err := os.Lstat(gitDir)
	if err != nil || !gitInfo.IsDir() || gitInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("missing isolated Git directory")
	}
	infoDir := filepath.Join(gitDir, "info")
	info, err := os.Lstat(infoDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("missing isolated Git info directory")
	}
	path := filepath.Join(infoDir, "exclude")
	before, err := os.Lstat(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND|os.O_CREATE, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !after.Mode().IsRegular() || after.Size() > 1024*1024 || (before != nil && !os.SameFile(before, after)) {
		return fmt.Errorf("unsafe Git exclude file")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(file, 1024*1024+1))
	if err != nil || len(data) > 1024*1024 {
		return fmt.Errorf("Git exclude file exceeds limit")
	}
	if strings.Contains(string(data), "/.sdlc/repositories/") {
		return nil
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		if _, err := file.Write([]byte("\n")); err != nil {
			return err
		}
	}
	_, err = file.Write([]byte("/.sdlc/repositories/\n"))
	return err
}
