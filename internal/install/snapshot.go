package install

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ValidateCommittedSource compares tracked bytes directly with committed blobs.
// Git's index stat cache can report unchanged bytes as dirty after a metadata
// change; reading blobs avoids both that false positive and configured filters.
func ValidateCommittedSource(ctx context.Context, source string) (string, error) {
	root, err := ValidateSource(source)
	if err != nil {
		return "", err
	}
	top, err := snapshotGit(ctx, root, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	canonicalTop, err := filepath.EvalSymlinks(strings.TrimSpace(top))
	if err != nil || canonicalTop != root {
		return "", fmt.Errorf("Homebrew source must be the root of a committed Git checkout")
	}
	head, err := snapshotGit(ctx, root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", err
	}
	head = strings.TrimSpace(head)
	if decoded, err := hex.DecodeString(head); err != nil || len(decoded) != sha1.Size {
		return "", fmt.Errorf("Homebrew source requires a committed SHA-1 Git checkout")
	}
	if err := cleanSnapshotIndex(ctx, root, head); err != nil {
		return "", err
	}
	untracked, err := snapshotGit(ctx, root, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return "", err
	}
	if untracked != "" {
		return "", fmt.Errorf("Homebrew source has untracked files; commit or ignore them before updating")
	}
	tree, err := snapshotGit(ctx, root, "ls-tree", "-r", "-z", head)
	if err != nil {
		return "", err
	}
	for _, entry := range strings.Split(tree, "\x00") {
		if entry == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		header, name, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(header)
		if !ok || len(fields) != 3 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
			return "", fmt.Errorf("Homebrew source contains an unsupported tracked file %q", name)
		}
		if err := verifySnapshotFile(root, name, fields[0], fields[2]); err != nil {
			return "", err
		}
	}
	current, err := snapshotGit(ctx, root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(current) != head {
		return "", fmt.Errorf("Homebrew source commit changed while inspecting it")
	}
	if err := cleanSnapshotIndex(ctx, root, head); err != nil {
		return "", err
	}
	return root, nil
}

func cleanSnapshotIndex(ctx context.Context, root, head string) error {
	staged, err := snapshotGit(ctx, root, "diff-index", "--cached", "--name-only", "--no-ext-diff", "--no-textconv", head, "--")
	if err != nil {
		return err
	}
	if staged != "" {
		return fmt.Errorf("Homebrew source has staged changes; commit them before updating")
	}
	return nil
}

func verifySnapshotFile(root, name, mode, object string) error {
	if name == "" || name == "." || filepath.IsAbs(name) || filepath.Clean(name) != name || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
		return fmt.Errorf("Homebrew source contains an invalid tracked path")
	}
	path := filepath.Join(root, name)
	for parent := filepath.Dir(path); parent != root; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("Homebrew source tracked file %q has a non-directory parent", name)
		}
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64*1024*1024 {
		return fmt.Errorf("Homebrew source tracked file %q must be a regular file under 64 MiB", name)
	}
	if (info.Mode().Perm()&0111 != 0) != (mode == "100755") {
		return fmt.Errorf("Homebrew source tracked file %q has uncommitted executable permissions", name)
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("cannot read Homebrew source tracked file %q", name)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) || opened.Size() != info.Size() {
		return fmt.Errorf("Homebrew source tracked file %q changed while reading", name)
	}
	hash := sha1.New()
	fmt.Fprintf(hash, "blob %d\x00", info.Size())
	count, err := io.Copy(hash, io.LimitReader(file, info.Size()+1))
	after, statErr := file.Stat()
	if err != nil || statErr != nil || count != info.Size() || after.Size() != info.Size() || after.Mode() != info.Mode() || !after.ModTime().Equal(info.ModTime()) {
		return fmt.Errorf("Homebrew source tracked file %q changed while reading", name)
	}
	if hex.EncodeToString(hash.Sum(nil)) != object {
		return fmt.Errorf("Homebrew source tracked file %q has uncommitted content; commit it before updating", name)
	}
	return nil
}

func snapshotGit(ctx context.Context, root string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-c", "core.fsmonitor=false"}, args...)...)
	command.Dir = root
	for _, entry := range os.Environ() {
		key := strings.SplitN(entry, "=", 2)[0]
		if !strings.HasPrefix(key, "GIT_") || key == "GIT_CONFIG_GLOBAL" || key == "GIT_CONFIG_SYSTEM" || key == "GIT_CONFIG_NOSYSTEM" {
			command.Env = append(command.Env, entry)
		}
	}
	command.Env = append(command.Env, "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	output, err := command.Output()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", fmt.Errorf("cannot inspect committed Homebrew source; check the checkout and Git installation")
	}
	return string(output), nil
}
