package workrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tjpeel/sdlc/internal/project"
)

const maximumInputBytes = 16 * 1024 * 1024

// ValidateSourceHistory checks that capture can export the complete authentic
// history. A missing shallow marker must not hide missing ancestry objects.
func ValidateSourceHistory(ctx context.Context, root string) error {
	shallow, err := isolatedGit(ctx, root, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return errors.New("cannot inspect source history; repair the repository before starting work")
	}
	if strings.TrimSpace(shallow) == "true" {
		return errors.New("source repository has shallow history; fetch the complete history before starting work")
	}
	if _, err := isolatedGit(ctx, root, "--no-replace-objects", "rev-list", "--objects", "--missing=error", "HEAD"); err != nil {
		return errors.New("source repository has incomplete history; restore missing Git objects or use a complete checkout before starting work")
	}
	return nil
}

// SafeGit disables inherited Git environment, global configuration and hooks.
// The workspace must have a real, locally generated .git directory.
func SafeGit(ctx context.Context, workspace string, args ...string) (string, error) {
	info, err := os.Lstat(filepath.Join(workspace, ".git"))
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("worker Git metadata must be a real directory")
	}
	// This helper is for controller-created metadata. Reject provider changes
	// that could execute commands or redirect object reads on the host.
	if err = validateMetadata(ctx, workspace); err != nil {
		return "", err
	}
	return isolatedGit(ctx, workspace, args...)
}

func isolatedGit(ctx context.Context, directory string, args ...string) (string, error) {
	prefix := []string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.untrackedCache=false", "-c", "maintenance.auto=false", "-c", "gc.auto=0", "-c", "commit.gpgSign=false", "-c", "tag.gpgSign=false", "-c", "core.attributesFile=/dev/null", "-c", "core.excludesFile=/dev/null", "-c", "protocol.file.allow=always"}
	cmd := exec.CommandContext(ctx, "git", append(prefix, args...)...)
	cmd.Dir = directory
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(key, "GIT_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_ATTR_NOSYSTEM=1")
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", fmt.Errorf("safe Git %s failed", args[0])
	}
	return string(out), nil
}

// Capture copies local source into a disposable checkout, preserving dirty
// files in a separate unsigned baseline commit. It never changes source files.
func Capture(ctx context.Context, launch project.LaunchResult, destination, branch, base, repository string, roles Roles) (plan Plan, err error) {
	for _, path := range launch.Config.InputFiles {
		if forbiddenCheckInput(path) {
			return plan, errors.New("configured check inputs must not use credential or account-state paths")
		}
	}
	plan = Plan{Root: launch.Root, SourceSHA: launch.Head, Reference: launch.Reference, Ticket: launch.Ticket, Branch: branch, Base: base, Repository: repository, Checks: launch.Config.Checks, Roles: roles}
	if _, err = isolatedGit(ctx, launch.Root, "check-ref-format", "--branch", branch); err != nil {
		return plan, err
	}
	if _, err = isolatedGit(ctx, launch.Root, "check-ref-format", "--branch", base); err != nil {
		return plan, err
	}
	baseHead, e := isolatedGit(ctx, launch.Root, "rev-parse", "--verify", "refs/heads/"+base+"^{commit}")
	if e != nil {
		return plan, errors.New("source base branch must exist locally")
	}
	plan.BaseSHA = strings.TrimSpace(baseHead)
	head, err := isolatedGit(ctx, launch.Root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil || strings.TrimSpace(head) != launch.Head {
		return plan, errors.New("source HEAD changed after launch")
	}
	if err = ValidateSourceHistory(ctx, launch.Root); err != nil {
		return plan, err
	}
	// The exported HEAD must contain the fixed base used by the independent
	// reviewer. A behind or diverged checkout cannot supply that review boundary.
	if _, e := isolatedGit(ctx, launch.Root, "merge-base", "--is-ancestor", plan.BaseSHA, launch.Head); e != nil {
		return plan, errors.New("source HEAD must include the selected base; update the checkout before launching work")
	}
	if err = realDirectory(launch.Root); err != nil {
		return plan, err
	}
	gitInfo, e := os.Lstat(filepath.Join(launch.Root, ".git"))
	if e != nil || !gitInfo.IsDir() || gitInfo.Mode()&os.ModeSymlink != 0 {
		return plan, errors.New("source requires a real .git directory")
	}
	if err = validateSourceMetadata(launch.Root); err != nil {
		return plan, err
	}
	if _, e = os.Lstat(destination); !errors.Is(e, os.ErrNotExist) {
		return plan, errors.New("capture destination must not exist")
	}
	if err = realDirectory(filepath.Dir(destination)); err != nil {
		return plan, err
	}
	parentInfo, e := os.Stat(filepath.Dir(destination))
	if e != nil || parentInfo.Mode().Perm()&0077 != 0 {
		return plan, errors.New("capture parent must be private to its owner")
	}
	// Parent permissions are managed by the private run directory owner.
	bundle, e := os.CreateTemp(filepath.Dir(destination), ".source-*.bundle")
	if e != nil {
		return plan, e
	}
	bundlePath := bundle.Name()
	bundle.Close()
	os.Remove(bundlePath)
	defer os.Remove(bundlePath)
	if _, err = isolatedGit(ctx, launch.Root, "bundle", "create", bundlePath, "HEAD"); err != nil {
		return plan, err
	}
	bundleHeads, e := isolatedGit(ctx, filepath.Dir(destination), "bundle", "list-heads", bundlePath)
	if e != nil || strings.TrimSpace(bundleHeads) != launch.Head+" HEAD" {
		return plan, errors.New("source HEAD changed during capture")
	}
	if _, err = isolatedGit(ctx, filepath.Dir(destination), "clone", "--no-local", "--no-hardlinks", "--no-checkout", "--template=", bundlePath, destination); err != nil {
		return plan, err
	}
	complete := false
	defer func() {
		if !complete {
			os.RemoveAll(destination)
		}
	}()
	if _, err = isolatedGit(ctx, destination, "remote", "remove", "origin"); err != nil {
		return plan, err
	}
	for _, pair := range [][2]string{{"user.name", "SDLC"}, {"user.email", "sdlc@example.invalid"}, {"core.hooksPath", "/dev/null"}, {"core.fsmonitor", "false"}, {"commit.gpgSign", "false"}, {"core.sharedRepository", "0666"}} {
		if _, err = SafeGit(ctx, destination, "config", pair[0], pair[1]); err != nil {
			return plan, err
		}
	}
	// Reject symlinks and submodules before checkout, which would materialize them.
	tree, e := SafeGit(ctx, destination, "ls-tree", "-r", "-z", launch.Head)
	if e != nil {
		return plan, e
	}
	for _, entry := range strings.Split(tree, "\x00") {
		_, treePath, _ := strings.Cut(entry, "\t")
		if trackedExcluded(treePath) {
			return plan, fmt.Errorf("source HEAD tracks excluded private material: %q", treePath)
		}
		if entry != "" && !strings.HasPrefix(entry, "100644 ") && !strings.HasPrefix(entry, "100755 ") {
			return plan, fmt.Errorf("source tree contains unsupported symlinks or submodules: %q", treePath)
		}
	}
	// Read the tree into the index only: raw source copies avoid checkout filters.
	if _, err = SafeGit(ctx, destination, "read-tree", launch.Head); err != nil {
		return plan, err
	}
	if _, err = SafeGit(ctx, destination, "update-ref", "refs/heads/"+branch, launch.Head); err != nil {
		return plan, err
	}
	if _, err = SafeGit(ctx, destination, "symbolic-ref", "HEAD", "refs/heads/"+branch); err != nil {
		return plan, err
	}
	tracked, e := isolatedGit(ctx, launch.Root, "ls-files", "--cached", "-z")
	if e != nil {
		return plan, e
	}
	for _, path := range strings.Split(tracked, "\x00") {
		if path != "" && trackedExcluded(path) {
			return plan, fmt.Errorf("source tracks excluded private material: %q", path)
		}
	}
	selected := map[string]bool{}
	for _, path := range launch.Inputs {
		selected[path] = true
	}
	configured := map[string]bool{}
	for _, path := range launch.Config.InputFiles {
		configured[path] = true
	}
	paths, e := isolatedGit(ctx, launch.Root, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
	if e != nil {
		return plan, e
	}
	for _, path := range strings.Split(paths, "\x00") {
		if path == "" {
			continue
		}
		isTracked := strings.Contains("\x00"+tracked, "\x00"+path+"\x00")
		if (excluded(path) && !(isTracked && !trackedExcluded(path))) || (codexPath(path) && !isTracked) || (selected[path] && !isTracked) || (configured[path] && !isTracked) {
			if _, err = SafeGit(ctx, destination, "update-index", "--force-remove", "--", path); err != nil {
				return plan, err
			}
			continue
		}
		if err = copySource(launch.Root, destination, path, -1); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return plan, err
		}
	}
	if _, err = SafeGit(ctx, destination, "add", "--all", "--", "."); err != nil {
		return plan, err
	}
	changed, e := SafeGit(ctx, destination, "diff-index", "--cached", "--name-only", launch.Head, "--")
	if e != nil {
		return plan, e
	}
	if changed != "" {
		if _, err = SafeGit(ctx, destination, "commit", "--no-verify", "-m", "Capture local source state"); err != nil {
			return plan, err
		}
	}
	plan.StartingSHA, err = SafeGit(ctx, destination, "rev-parse", "HEAD")
	if err != nil {
		return plan, err
	}
	plan.StartingSHA = strings.TrimSpace(plan.StartingSHA)
	var total int64
	for _, path := range launch.Inputs {
		if excluded(path) && !(path == ".sdlc/work" || strings.HasPrefix(path, ".sdlc/work/")) {
			return plan, errors.New("private paths cannot be selected as provider inputs")
		}
		if err = copySource(launch.Root, destination, path, maximumInputBytes-total); err != nil {
			return plan, err
		}
		data, e := os.ReadFile(filepath.Join(destination, filepath.FromSlash(path)))
		if e != nil {
			return plan, e
		}
		total += int64(len(data))
		if total > maximumInputBytes {
			return plan, errors.New("selected inputs exceed 16 MiB")
		}
		hash := sha256.Sum256(data)
		plan.Inputs = append(plan.Inputs, Input{Path: path, SHA256: hex.EncodeToString(hash[:])})
	}
	checkDir := filepath.Join(filepath.Dir(destination), "check-inputs")
	if len(launch.Config.InputFiles) > 0 {
		if _, e := os.Lstat(checkDir); !errors.Is(e, os.ErrNotExist) {
			return plan, errors.New("check input destination must not exist")
		}
		if err = os.Mkdir(checkDir, 0700); err != nil {
			return plan, err
		}
		defer func() {
			if !complete {
				os.RemoveAll(checkDir)
			}
		}()
		for _, path := range launch.Config.InputFiles {
			if err = copySource(launch.Root, checkDir, path, maximumInputBytes-total); err != nil {
				return plan, err
			}
			data, e := os.ReadFile(filepath.Join(checkDir, filepath.FromSlash(path)))
			if e != nil {
				return plan, e
			}
			total += int64(len(data))
			if total > maximumInputBytes {
				return plan, errors.New("selected inputs exceed 16 MiB")
			}
			hash := sha256.Sum256(data)
			plan.CheckInputs = append(plan.CheckInputs, Input{Path: path, SHA256: hex.EncodeToString(hash[:])})
		}
	}
	// Work material remains ignored even if source settings omitted this rule.
	if err = os.MkdirAll(filepath.Join(destination, ".git", "info"), 0777); err != nil {
		return plan, err
	}
	if err = os.WriteFile(filepath.Join(destination, ".git", "info", "exclude"), []byte(inputExcludes(launch.Inputs)), 0666); err != nil {
		return plan, err
	}
	err = filepath.Walk(destination, func(path string, info os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		mode := os.FileMode(0666)
		if info.IsDir() || info.Mode()&0111 != 0 {
			mode = 0777
		}
		return os.Chmod(path, mode)
	})
	if err != nil {
		return plan, err
	}
	complete = true
	return plan, nil
}

// Committed environment templates are public source only when none of their
// parent directories are excluded. Untracked files still use excluded.
func trackedExcluded(path string) bool {
	path = strings.ToLower(filepath.ToSlash(path))
	if filepath.Base(path) == ".env.example" {
		return excluded(filepath.Dir(path))
	}
	return excluded(path)
}

func excluded(path string) bool {
	for _, part := range strings.Split(strings.ToLower(filepath.ToSlash(path)), "/") {
		if strings.HasPrefix(part, ".env.") {
			return true
		}
		switch part {
		case ".git", ".secrets", ".ssh", ".aws", ".claude", ".t3", "profiles.local.json", "credentials", "credentials.json", ".env":
			return true
		}
	}
	normalized := strings.ToLower(filepath.ToSlash(path))
	if normalized == ".codex/auth.json" || strings.HasSuffix(normalized, "/.codex/auth.json") {
		return true
	}
	return normalized == ".sdlc/work" || strings.HasPrefix(normalized, ".sdlc/work/")
}
func sourceRelative(path string) bool {
	if path == "" || filepath.IsAbs(path) || strings.ContainsAny(path, "\\\x00\r\n") {
		return false
	}
	for _, p := range strings.Split(path, "/") {
		if p == "" || p == "." || p == ".." || strings.EqualFold(p, ".git") {
			return false
		}
	}
	return true
}
func realDirectory(path string) error {
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		info, e := os.Lstat(p)
		if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("capture directories must be real directories")
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	return nil
}
func copySource(source, destination, path string, limit int64) error {
	if !sourceRelative(path) {
		return errors.New("source paths must be exact relative paths")
	}
	from := filepath.Join(source, filepath.FromSlash(path))
	before, e := os.Lstat(from)
	if e != nil {
		return e
	}
	if err := realDirectory(filepath.Dir(from)); err != nil {
		return err
	}
	if !before.Mode().IsRegular() {
		return errors.New("source inputs must be regular files without symlinks")
	}
	if limit >= 0 && before.Size() > limit {
		return errors.New("selected inputs exceed 16 MiB")
	}
	f, e := os.Open(from)
	if e != nil {
		return e
	}
	defer f.Close()
	opened, e := f.Stat()
	if e != nil || !os.SameFile(before, opened) {
		return errors.New("source changed while opening")
	}
	to := filepath.Join(destination, filepath.FromSlash(path))
	if e = os.MkdirAll(filepath.Dir(to), 0777); e != nil {
		return e
	}
	if e = realDirectory(filepath.Dir(to)); e != nil {
		return e
	}
	if current, e := os.Lstat(to); e == nil && !current.Mode().IsRegular() {
		return errors.New("worker destination is not regular")
	}
	mode := os.FileMode(0666)
	if before.Mode()&0111 != 0 {
		mode = 0777
	}
	out, e := os.OpenFile(to, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if e != nil {
		return e
	}
	defer out.Close()
	var reader io.Reader = f
	if limit >= 0 {
		reader = io.LimitReader(f, limit+1)
	}
	n, e := io.Copy(out, reader)
	if e != nil {
		return e
	}
	if limit >= 0 && n > limit {
		return errors.New("selected inputs exceed 16 MiB")
	}
	return out.Close()
}

func inputExcludes(inputs []string) string {
	out := "/.sdlc/work/\n"
	for _, path := range inputs {
		replacer := strings.NewReplacer("\\", "\\\\", "*", "\\*", "?", "\\?", "[", "\\[")
		out += "/" + replacer.Replace(path) + "\n"
	}
	return out
}

func validateMetadata(ctx context.Context, workspace string) error {
	metadata := filepath.Join(workspace, ".git")
	if err := filepath.Walk(metadata, func(path string, info os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return errors.New("Git metadata must not contain symlinks or special files")
		}
		return nil
	}); err != nil {
		return err
	}
	for _, path := range []string{"objects/info/alternates", "objects/info/http-alternates", "commondir"} {
		if _, e := os.Lstat(filepath.Join(metadata, path)); !errors.Is(e, os.ErrNotExist) {
			return errors.New("redirected Git metadata is unsupported")
		}
	}
	output, e := isolatedGit(ctx, workspace, "config", "--no-includes", "--file", filepath.Join(metadata, "config"), "--null", "--list")
	if e != nil {
		return e
	}
	allowed := map[string]bool{"core.repositoryformatversion": true, "core.filemode": true, "core.bare": true, "core.logallrefupdates": true, "core.ignorecase": true, "core.precomposeunicode": true, "core.hookspath": true, "core.fsmonitor": true, "core.sharedrepository": true, "user.name": true, "user.email": true, "commit.gpgsign": true, "extensions.objectformat": true}
	for _, entry := range strings.Split(output, "\x00") {
		if entry == "" {
			continue
		}
		key, value, _ := strings.Cut(entry, "\n")
		if !allowed[key] {
			return errors.New("worker Git configuration contains an unsupported setting")
		}
		if key == "core.bare" && value != "false" || key == "core.repositoryformatversion" && value != "0" || key == "core.hookspath" && value != "/dev/null" || key == "core.fsmonitor" && value != "false" || key == "commit.gpgsign" && value != "false" {
			return errors.New("worker Git configuration changed a safety setting")
		}
	}
	return nil
}

func validateSourceMetadata(root string) error {
	metadata := filepath.Join(root, ".git")
	if err := filepath.Walk(metadata, func(path string, info os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return errors.New("source Git metadata contains symlinks or special files")
		}
		return nil
	}); err != nil {
		return err
	}
	for _, path := range []string{"objects/info/alternates", "objects/info/http-alternates", "commondir"} {
		if _, e := os.Lstat(filepath.Join(metadata, path)); !errors.Is(e, os.ErrNotExist) {
			return errors.New("source Git object redirects are unsupported")
		}
	}
	return nil
}

// Approved integration environment files can be used only by checks. Account
// state and credential directories remain forbidden even inside check inputs.
func forbiddenCheckInput(path string) bool {
	for _, part := range strings.Split(strings.ToLower(filepath.ToSlash(path)), "/") {
		switch part {
		case ".aws", ".ssh", ".codex", ".claude":
			return true
		}
	}
	if strings.EqualFold(filepath.Base(path), ".env") {
		return excluded(filepath.ToSlash(filepath.Dir(path)))
	}
	return excluded(path)
}

func codexPath(path string) bool {
	for _, part := range strings.Split(strings.ToLower(filepath.ToSlash(path)), "/") {
		if part == ".codex" {
			return true
		}
	}
	return false
}
