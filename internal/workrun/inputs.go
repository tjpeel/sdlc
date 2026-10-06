package workrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode"
)

type InputRecovery struct {
	PreviousCheckpoint time.Time `json:"previous_checkpoint"`
	AddedAt            time.Time `json:"added_at"`
	Inputs             []Input   `json:"inputs"`
}

type PendingInputRecovery struct {
	InputRecovery
	Stage string `json:"stage"`
}

// CanAttachInputs restricts supplements to an unpublished implementation question.
// An unfinished supplement may be completed through the same operation.
func CanAttachInputs(j Journal) error {
	if j.State != "waiting_for_human" || j.PendingRole != "implementation" || len(j.Outcome.Questions) == 0 || (j.ResumeState != "" && j.ResumeState != "implementing" && j.ResumeState != "repairing") {
		return errors.New("inputs can only be added to a waiting implementation question")
	}
	if j.Publication != (Publication{}) || j.Reconciliation != nil || j.Plan.Restack != nil || len(j.RestackAudit) > 0 {
		return errors.New("inputs cannot be added after publication or during reconciliation")
	}
	return nil
}

func safeRecoveryPath(path, reference string) bool {
	if !sourceRelative(path) || len(path) > 4096 || strings.IndexFunc(path, unicode.IsControl) >= 0 || codexPath(path) {
		return false
	}
	if strings.HasPrefix(path, ".sdlc/") {
		if !strings.HasPrefix(path, ".sdlc/work/"+reference+"/") {
			return false
		}
		for _, part := range strings.Split(strings.ToLower(path), "/") {
			if part == "runs" || part == "series" || part == "logs" || part == "answers" {
				return false
			}
		}
		return !forbiddenCheckInput(strings.TrimPrefix(path, ".sdlc/work/"))
	}
	return !excluded(path) && !forbiddenCheckInput(path)
}

func readStableInput(root, path string, limit int64) ([]byte, error) {
	if !sourceRelative(path) {
		return nil, errors.New("unsafe input path")
	}
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := realDirectory(filepath.Dir(full)); err != nil {
		return nil, err
	}
	before, err := os.Lstat(full)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Size() > limit {
		return nil, errors.New("input must be a regular file within the combined 16 MiB limit")
	}
	f, err := os.Open(full)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, errors.New("input changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	current, e := os.Lstat(full)
	if err != nil || e != nil || !os.SameFile(opened, current) || opened.Size() != after.Size() || !opened.ModTime().Equal(after.ModTime()) || int64(len(data)) > limit {
		return nil, errors.New("input changed while reading or exceeds 16 MiB")
	}
	return data, nil
}
func inputHash(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }

func inputBudget(directory string, j Journal) (int64, error) {
	var total int64
	for _, group := range []struct {
		root   string
		inputs []Input
	}{{j.Workspace, j.Plan.Inputs}, {filepath.Join(directory, "check-inputs"), j.Plan.CheckInputs}} {
		for _, input := range group.inputs {
			for _, root := range []string{group.root, j.Plan.Root} {
				limit := int64(maximumInputBytes)
				if root == group.root {
					limit -= total
				}
				data, err := readStableInput(root, input.Path, limit)
				if err != nil {
					return 0, fmt.Errorf("recorded input %q: %w", input.Path, err)
				}
				if inputHash(data) != input.SHA256 {
					return 0, fmt.Errorf("recorded input %q changed after capture", input.Path)
				}
				if root == group.root {
					total += int64(len(data))
				}
			}
		}
	}
	return maximumInputBytes - total, nil
}

// AttachInputs requires the caller to hold feature ownership (if applicable),
// then run ownership. dry only reads. A saved pending transaction owns its
// immutable bytes, so recovery never recaptures a subsequently changed source.
func AttachInputs(ctx context.Context, directory string, j *Journal, paths []string, dry bool) ([]Input, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if filepath.Clean(j.Workspace) != filepath.Join(directory, "workspace") {
		return nil, errors.New("input recovery workspace identity changed")
	}
	for _, root := range []string{directory, j.Workspace, j.Plan.Root} {
		if err := realDirectory(root); err != nil {
			return nil, err
		}
	}
	if err := CanAttachInputs(*j); err != nil {
		return nil, err
	}
	if err := validateInputRecoveries(*j); err != nil {
		return nil, err
	}
	budget, err := inputBudget(directory, *j)
	if err != nil {
		return nil, err
	}
	if j.PendingInputs != nil {
		for _, path := range paths {
			found := false
			for _, in := range j.PendingInputs.Inputs {
				if path == in.Path {
					found = true
				}
			}
			if !found {
				return nil, errors.New("finish the recorded input transaction before selecting other files")
			}
		}
		return completeInputs(ctx, directory, j, budget, dry)
	}
	if len(j.InputRecoveries) >= 128 {
		return nil, errors.New("input recovery history is full")
	}
	known := map[string]string{}
	for _, in := range j.Plan.Inputs {
		known[in.Path] = in.SHA256
	}
	var additions []Input
	var contents [][]byte
	for _, path := range paths {
		if !safeRecoveryPath(path, j.Plan.Reference) {
			return nil, fmt.Errorf("unsafe supplementary input %q", path)
		}
		limit := budget
		if _, ok := known[path]; ok {
			limit = maximumInputBytes
		}
		data, err := readStableInput(j.Plan.Root, path, limit)
		if err != nil {
			return nil, err
		}
		hash := inputHash(data)
		if original, ok := known[path]; ok {
			if original != hash {
				return nil, errors.New("a recorded input cannot be replaced")
			}
			continue
		}
		tracked, err := SafeGit(ctx, j.Workspace, "ls-files", "--cached", "--", path)
		if err != nil {
			return nil, err
		}
		if tracked != "" {
			return nil, errors.New("supplementary input conflicts with tracked worker output")
		}
		if err := checkInputDestination(j.Workspace, path, nil, false); err != nil {
			return nil, err
		}
		budget -= int64(len(data))
		additions = append(additions, Input{path, hash})
		contents = append(contents, data)
		known[path] = hash
	}
	if len(j.Plan.Inputs)+len(additions) > 256 {
		return nil, errors.New("too many recorded inputs")
	}
	if dry || len(additions) == 0 {
		return additions, nil
	}
	stage, err := os.MkdirTemp(directory, "input-recovery-")
	if err != nil {
		return nil, err
	}
	for i, data := range contents {
		if err := writeSynced(filepath.Join(stage, fmt.Sprint(i)), data, 0400); err != nil {
			return nil, err
		}
	}
	if err := syncDirectory(stage); err != nil {
		return nil, err
	}
	j.PendingInputs = &PendingInputRecovery{InputRecovery: InputRecovery{PreviousCheckpoint: j.UpdatedAt, AddedAt: time.Now().UTC(), Inputs: additions}, Stage: filepath.Base(stage)}
	if err := Save(directory, j); err != nil {
		return nil, err
	}
	return completeInputs(ctx, directory, j, budget+sumBytes(contents), false)
}
func sumBytes(data [][]byte) int64 {
	var total int64
	for _, d := range data {
		total += int64(len(d))
	}
	return total
}
func writeSynced(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}
func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if runtime.GOOS == "windows" {
		return nil
	}
	return f.Sync()
}

func checkInputDestination(root, path string, data []byte, recovering bool) error {
	root = filepath.Clean(root)
	full := filepath.Join(root, filepath.FromSlash(path))
	for parent := filepath.Dir(full); parent != root; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("input destination parent is unsafe")
		}
	}
	info, err := os.Lstat(full)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || !recovering {
		return fmt.Errorf("input destination %q already exists; refusing to overwrite worker output", path)
	}
	existing, err := readStableInput(root, path, maximumInputBytes)
	if err != nil {
		return err
	}
	if inputHash(existing) != inputHash(data) {
		return errors.New("input recovery destination conflicts with staged bytes")
	}
	return nil
}
func completeInputs(ctx context.Context, directory string, j *Journal, budget int64, dry bool) ([]Input, error) {
	pending := j.PendingInputs
	stage := filepath.Join(directory, pending.Stage)
	if err := realDirectory(stage); err != nil {
		return nil, err
	}
	var contents [][]byte
	for i, in := range pending.Inputs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, err := readStableInput(stage, fmt.Sprint(i), budget)
		if err != nil {
			return nil, err
		}
		if inputHash(data) != in.SHA256 {
			return nil, errors.New("staged recovery input hash changed")
		}
		budget -= int64(len(data))
		contents = append(contents, data)
		tracked, err := SafeGit(ctx, j.Workspace, "ls-files", "--cached", "--", in.Path)
		if err != nil {
			return nil, err
		}
		if tracked != "" {
			return nil, errors.New("input recovery conflicts with tracked worker output")
		}
		if err := checkInputDestination(j.Workspace, in.Path, data, true); err != nil {
			return nil, err
		}
	}
	if dry {
		return pending.Inputs, nil
	}
	for i, in := range pending.Inputs {
		full := filepath.Join(j.Workspace, filepath.FromSlash(in.Path))
		if err := os.MkdirAll(filepath.Dir(full), 0777); err != nil {
			return nil, err
		}
		if err := realDirectory(filepath.Dir(full)); err != nil {
			return nil, err
		}
		// A synced temporary inode is linked without replacement; interrupted copies
		// cannot leave a partial final file that is mistaken for a completed copy.
		temp, err := os.CreateTemp(stage, ".materialize-")
		if err != nil {
			return nil, err
		}
		name := temp.Name()
		_, err = temp.Write(contents[i])
		if err == nil {
			err = temp.Chmod(0666)
		}
		if err == nil {
			err = temp.Sync()
		}
		err = errors.Join(err, temp.Close())
		if err == nil {
			err = os.Link(name, full)
			if os.IsExist(err) {
				err = checkInputDestination(j.Workspace, in.Path, contents[i], true)
			}
		}
		os.Remove(name)
		if err != nil {
			return nil, err
		}
		if err := syncDirectory(filepath.Dir(full)); err != nil {
			return nil, err
		}
	}
	exclude := filepath.Join(j.Workspace, ".git", "info", "exclude")
	info, e := os.Lstat(exclude)
	if e != nil || !info.Mode().IsRegular() || info.Size() > maximumInputBytes {
		return nil, errors.New("worker excludes are unsafe")
	}
	data, err := os.ReadFile(exclude)
	if err != nil {
		return nil, err
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	var paths []string
	for _, in := range pending.Inputs {
		paths = append(paths, in.Path)
	}
	for _, line := range strings.Split(inputExcludes(paths), "\n") {
		if line != "" && !strings.Contains("\n"+string(data), "\n"+line+"\n") {
			data = append(data, []byte(line+"\n")...)
		}
	}
	temp, err := os.CreateTemp(filepath.Dir(exclude), ".exclude-")
	if err != nil {
		return nil, err
	}
	name := temp.Name()
	defer os.Remove(name)
	_, err = temp.Write(data)
	if err == nil {
		err = temp.Chmod(0666)
	}
	if err == nil {
		err = temp.Sync()
	}
	err = errors.Join(err, temp.Close())
	if err != nil {
		return nil, err
	}
	if err := os.Rename(name, exclude); err != nil {
		return nil, err
	}
	if err := syncDirectory(filepath.Dir(exclude)); err != nil {
		return nil, err
	}
	before := *j
	j.Plan.Inputs = append(append([]Input{}, j.Plan.Inputs...), pending.Inputs...)
	j.InputRecoveries = append(append([]InputRecovery{}, j.InputRecoveries...), pending.InputRecovery)
	j.PendingInputs = nil
	j.Evidence = CheckEvidence{}
	if err := Save(directory, j); err != nil {
		*j = before
		return nil, err
	}
	return pending.Inputs, nil
}

func validateInputRecoveries(j Journal) error {
	known := map[string]string{}
	for _, in := range j.Plan.Inputs {
		if !sourceRelative(in.Path) || !validInputHash(in.SHA256) || known[in.Path] != "" {
			return errors.New("invalid input manifest")
		}
		known[in.Path] = in.SHA256
	}
	audited := map[string]bool{}
	validate := func(r InputRecovery, pending bool) error {
		if r.PreviousCheckpoint.IsZero() || r.AddedAt.IsZero() || len(r.Inputs) == 0 || len(r.Inputs) > 256 {
			return errors.New("invalid input recovery audit")
		}
		for _, in := range r.Inputs {
			if !safeRecoveryPath(in.Path, j.Plan.Reference) || !validInputHash(in.SHA256) || audited[in.Path] {
				return errors.New("invalid supplementary input audit")
			}
			if pending {
				if known[in.Path] != "" {
					return errors.New("pending input already exists in manifest")
				}
			} else if known[in.Path] != in.SHA256 {
				return errors.New("input audit does not match manifest")
			}
			audited[in.Path] = true
		}
		return nil
	}
	if len(j.InputRecoveries) > 128 {
		return errors.New("input recovery history exceeds limit")
	}
	for _, r := range j.InputRecoveries {
		if err := validate(r, false); err != nil {
			return err
		}
	}
	if p := j.PendingInputs; p != nil {
		if filepath.Base(p.Stage) != p.Stage || !strings.HasPrefix(p.Stage, "input-recovery-") {
			return errors.New("invalid input recovery staging directory")
		}
		return validate(p.InputRecovery, true)
	}
	return nil
}

// VerifyCapturedInputs checks recorded hashes without expanding any document links.
func VerifyCapturedInputs(root string, inputs []Input) error {
	var total int64
	for _, in := range inputs {
		data, err := readStableInput(root, in.Path, maximumInputBytes-total)
		if err != nil {
			return err
		}
		total += int64(len(data))
		if inputHash(data) != in.SHA256 {
			return fmt.Errorf("captured input %q changed", in.Path)
		}
	}
	return nil
}

func validInputHash(value string) bool {
	data, err := hex.DecodeString(value)
	return err == nil && len(data) == 32 && strings.ToLower(value) == value
}
