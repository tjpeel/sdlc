package workrun

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxSourcePatchDiff = 32 * 1024
const maxSourcePatchJournal = 128 * 1024

type SourcePatchRequest struct {
	Paths []string `json:"paths"`
}
type SourcePatchFile struct {
	Path   string `json:"path"`
	Before string `json:"before_sha256"`
	After  string `json:"after_sha256"`
}
type SourcePatchResult struct {
	Head       string            `json:"head"`
	Tree       string            `json:"tree"`
	Files      []SourcePatchFile `json:"files"`
	Diff       string            `json:"diff"`
	DiffSHA256 string            `json:"diff_sha256"`
}
type SourcePatchChecker interface {
	VerifySourcePatch(context.Context, string, VerificationRequest, string, string, io.Writer) (*SourcePatchResult, error)
}
type sourcePatchCapture struct {
	request    VerificationRequest
	head, tree string
	result     *SourcePatchResult
}

type sourcePatchDiffBudgetError struct{}

func (*sourcePatchDiffBudgetError) Error() string {
	return "source correction diff exceeds the 32 KiB request budget"
}
func (*sourcePatchDiffBudgetError) Unwrap() error { return ErrCheckFailed }

// Only the fixed collector's single-member failure envelope may request refinement.
// Do not infer semantic failures from stderr, extra members or duplicate keys.
func sourcePatchCollectionFailure(encoded []byte) (bool, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return false, nil
	}
	key, err := decoder.Token()
	if err != nil || key != "failure" {
		return false, nil
	}
	code, err := decoder.Token()
	if err != nil || code != "source_patch_diff_budget_exceeded" || decoder.More() {
		return true, errors.New("invalid source correction failure response")
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return true, errors.New("invalid source correction failure response")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return true, errors.New("invalid source correction failure response")
	}
	return true, &sourcePatchDiffBudgetError{}
}

func safeSourcePatchPath(p string) bool {
	if len(p) > 240 || !utf8.ValidString(p) || !sourceRelative(p) || excluded(p) || strings.IndexFunc(p, unicode.IsControl) >= 0 {
		return false
	}
	for _, part := range strings.Split(strings.ToLower(p), "/") {
		if strings.HasPrefix(part, "appsettings.") || strings.HasPrefix(part, "settings.local.") {
			return false
		}
		switch part {
		case ".sdlc", ".codex", ".config", ".docker", ".auth", ".npmrc", ".netrc", ".pypirc", "auth", "auth.json", "auth.toml", "settings.json", "config.json", "config.toml", "nuget.config", "appsettings.json", "profiles.json":
			return false
		}
	}
	return true
}
func validateSourcePatchRequest(r VerificationRequest, plan *Plan) error {
	if r.SourcePatch == nil {
		return nil
	}
	if r.Baseline != nil || len(r.SourcePatch.Paths) == 0 || len(r.SourcePatch.Paths) > 16 {
		return errors.New("invalid source correction selection")
	}
	seen := map[string]bool{}
	for _, p := range r.SourcePatch.Paths {
		if !safeSourcePatchPath(p) || seen[p] {
			return errors.New("unsafe or duplicate source correction path")
		}
		seen[p] = true
		if plan != nil {
			for _, input := range append(append([]Input(nil), plan.Inputs...), plan.CheckInputs...) {
				if p == input.Path || strings.HasPrefix(p, input.Path+"/") || strings.HasPrefix(input.Path, p+"/") {
					return errors.New("source correction overlaps frozen input")
				}
			}
		}
	}
	return nil
}
func validateSourcePatchResult(result *SourcePatchResult, request VerificationRequest, head, tree string) error {
	if result == nil || request.SourcePatch == nil || result.Head != head || result.Tree != tree || !objectID.MatchString(head) || !objectID.MatchString(tree) || len(result.Diff) > maxSourcePatchDiff || !utf8.ValidString(result.Diff) || strings.ContainsRune(result.Diff, 0) || len(result.Files) != len(request.SourcePatch.Paths) {
		return errors.New("invalid source correction artifact")
	}
	digest := sha256.Sum256([]byte(result.Diff))
	if result.DiffSHA256 != hex.EncodeToString(digest[:]) {
		return errors.New("source correction diff digest differs")
	}
	changed := false
	for i, file := range result.Files {
		if file.Path != request.SourcePatch.Paths[i] || !safeSourcePatchPath(file.Path) || len(file.Before) != 64 || len(file.After) != 64 || strings.Trim(file.Before+file.After, "0123456789abcdef") != "" {
			return errors.New("invalid source correction file metadata")
		}
		changed = changed || file.Before != file.After
	}
	if changed != (result.Diff != "") {
		return errors.New("source correction artifact lacks matching change evidence")
	}
	return nil
}
func validateSourcePatchJournal(journal *Journal) error {
	total := 0
	for _, evidence := range journal.Verification {
		if evidence.SourcePatch == nil {
			continue
		}
		if err := validateSourcePatchRequest(evidence.Request, &journal.Plan); err != nil {
			return err
		}
		if err := validateSourcePatchResult(evidence.SourcePatch, evidence.Request, evidence.Head, evidence.Tree); err != nil {
			return err
		}
		if evidence.Passed && evidence.SourcePatch.Diff != "" {
			return errors.New("unapplied source correction cannot pass verification")
		}
		data, err := json.Marshal(evidence.SourcePatch)
		if err != nil {
			return err
		}
		total += len(data)
	}
	if total > maxSourcePatchJournal {
		return errors.New("source correction artifacts exceed journal bound")
	}
	return nil
}

func discardSourcePatchFeedback(journal *Journal, evidence *VerificationResult) {
	if evidence.SourcePatch == nil {
		return
	}
	encoded, _ := json.Marshal(evidence.SourcePatch)
	journal.Feedback = strings.ReplaceAll(journal.Feedback, `"source_patch":`+string(encoded), `"source_patch":null`)
	evidence.SourcePatch = nil
}

// Interrupted repair turns may resume after the candidate changes. Remove only
// stale artifact data from feedback, preserving review findings and human input.
func (runner Runner) validateSourcePatchResume(ctx context.Context, directory string, journal *Journal) error {
	pending := false
	for _, evidence := range journal.Verification {
		pending = pending || evidence.SourcePatch != nil
	}
	if !pending {
		return nil
	}
	revision, err := runner.Repository.Inspect(ctx, journal.Workspace)
	if err != nil {
		return runner.stop(directory, journal, "blocked", "cannot establish the source correction candidate before resuming")
	}
	stale := false
	for i := range journal.Verification {
		evidence := &journal.Verification[i]
		if evidence.SourcePatch != nil && (!revision.Clean || evidence.Head != revision.Head || evidence.Tree != revision.Tree) {
			discardSourcePatchFeedback(journal, evidence)
			evidence.Key = ""
			stale = true
		}
	}
	if stale {
		journal.Feedback += "\nPreviously supplied source correction artifacts are stale for this workspace. Do not apply them. Inspect the current source, preserve intended work, commit the candidate and request fresh full controller verification."
	}
	return nil
}
func sourcePatchIdentity(ctx context.Context, workspace, head, tree string) error {
	data, err := SafeGit(ctx, workspace, "rev-parse", "HEAD", "HEAD^{tree}")
	if err != nil || strings.TrimSpace(data) != head+"\n"+tree {
		return errors.New("source correction candidate revision changed")
	}
	return nil
}
func (checker DockerChecker) VerifySourcePatch(ctx context.Context, workspace string, request VerificationRequest, head, tree string, output io.Writer) (*SourcePatchResult, error) {
	if err := validateVerificationRequests([]VerificationRequest{request}, nil); err != nil {
		return nil, err
	}
	if request.SourcePatch == nil || !objectID.MatchString(head) || !objectID.MatchString(tree) {
		return nil, errors.New("source correction requires a selected committed candidate")
	}
	if err := sourcePatchIdentity(ctx, workspace, head, tree); err != nil {
		return nil, err
	}
	checker.InputDirectory = ""
	checker.DockerTests = false
	checker.DaemonMode = ""
	checker.DaemonImage = ""
	checker.UseDefaultDaemonImage = true
	checker.NugetCache = nil
	checker.nugetSeedDirectory = ""
	checker.workerSequence = nil
	checker.sourcePatch = &sourcePatchCapture{request: request, head: head, tree: tree}
	if err := checker.check(ctx, workspace, request.Commands, output, nil); err != nil {
		return nil, err
	}
	if err := sourcePatchIdentity(ctx, workspace, head, tree); err != nil {
		return nil, err
	}
	if err := validateSourcePatchResult(checker.sourcePatch.result, request, head, tree); err != nil {
		return nil, err
	}
	return checker.sourcePatch.result, nil
}
func (checker DockerChecker) collectSourcePatch(ctx context.Context, workspace, mount, name, image string, capture bool) error {
	selection := checker.sourcePatch
	data, _ := json.Marshal(struct {
		Paths   []string `json:"paths"`
		Head    string   `json:"head"`
		Tree    string   `json:"tree"`
		Capture bool     `json:"capture"`
	}{selection.request.SourcePatch.Paths, selection.head, selection.tree, capture})
	suffix := "validate"
	if capture {
		suffix = "capture"
	}
	args := checkContainerEnvironment([]string{"run", "--name", name + "-source-patch-" + suffix, "--pull", "never", "--network", "none", "--workdir", "/", "--user", "0:0", "--read-only", "--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--security-opt", "no-new-privileges", "--mount", "type=bind,src=" + workspace + ",dst=/source,readonly", "--mount", mount + ",readonly"})
	args = append(args, "--entrypoint", "python3", image, "-I", "-c", collectSourcePatchPython, string(data))
	// Collection output is decoded privately, never forwarded to a log or stream.
	encoded, err := checker.Runtime.Docker.Output(ctx, args...)
	if err != nil {
		return fmt.Errorf("source correction collection could not complete: %w", err)
	}
	if len(encoded) > 128*1024 {
		return errors.New("source correction collection exceeds bound")
	}
	if failure, failureErr := sourcePatchCollectionFailure(encoded); failure {
		if !capture {
			return errors.New("invalid source correction validation response")
		}
		return failureErr
	}
	var result SourcePatchResult
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("invalid source correction collection")
	}
	if err := validateSourcePatchResult(&result, selection.request, selection.head, selection.tree); err != nil {
		return err
	}
	if capture {
		selection.result = &result
	}
	return nil
}

const collectSourcePatchPython = `import difflib,hashlib,json,os,pathlib,stat,subprocess,sys
selection=json.loads(sys.argv[1])
env={'PATH':os.environ['PATH'],'HOME':'/tmp','GIT_CONFIG_NOSYSTEM':'1','GIT_CONFIG_GLOBAL':'/dev/null'}
git=['git','--no-replace-objects','-c','safe.directory=/source','-c','core.hooksPath=/dev/null','-c','core.fsmonitor=false','-C','/source']
def identity():
    values=subprocess.check_output(git+['rev-parse','HEAD','HEAD^{tree}'],env=env).decode().splitlines()
    if values!=[selection['head'],selection['tree']]: raise ValueError('candidate identity changed')
identity()
requested=selection['paths']; selected=set(requested); files={}; patches=[]; total=0; overflow=False
def source_lines(data):
    parts=data.decode('utf-8').split('\n')
    return [part+'\n' for part in parts[:-1]]+([parts[-1]] if parts[-1] else [])
entries=subprocess.check_output(git+['ls-tree','-r','-z','HEAD'],env=env).split(b'\0')
for entry in entries:
    if not entry: continue
    metadata,raw=entry.split(b'\t',1); mode,kind,oid=metadata.split(b' ')
    name=raw.decode('utf-8'); relative=pathlib.PurePosixPath(name)
    if relative.is_absolute() or any(p in ('..','.git') for p in relative.parts) or mode not in (b'100644',b'100755') or kind!=b'blob': raise ValueError('unsafe committed source')
    current=pathlib.Path('/workspace')/relative
    for parent in [current,*current.parents]:
        if parent.is_symlink(): raise ValueError('linked source')
    info=current.stat()
    if not stat.S_ISREG(info.st_mode) or (info.st_mode&0o777)!=(0o755 if mode==b'100755' else 0o644): raise ValueError('source mode changed')
    digest=(hashlib.sha1() if len(selection['head'])==40 else hashlib.sha256())
    digest.update(('blob '+str(info.st_size)+'\0').encode())
    with current.open('rb') as stream:
        for chunk in iter(lambda:stream.read(65536),b''): digest.update(chunk)
    changed=digest.hexdigest()!=oid.decode()
    if name not in selected:
        if changed: raise ValueError('unselected source changed')
        continue
    size=int(subprocess.check_output(git+['cat-file','-s',oid.decode()],env=env))
    if size>262144 or info.st_size>262144: raise ValueError('selected source exceeds bound')
    before=subprocess.check_output(git+['cat-file','blob',oid.decode()],env=env); after=current.read_bytes()
    if len(before)!=size or len(after)!=info.st_size: raise ValueError('source size changed')
    for data in (before,after):
        text=data.decode('utf-8')
        if any(ord(c)<32 and c not in '\t\r\n' or ord(c)==127 for c in text): raise ValueError('selected source is not text')
    files[name]={'path':name,'before_sha256':hashlib.sha256(before).hexdigest(),'after_sha256':hashlib.sha256(after).hexdigest()}
    if changed and selection['capture'] and not overflow:
        lines=difflib.unified_diff(source_lines(before),source_lines(after),fromfile='a/'+name,tofile='b/'+name,n=3)
        patch=''
        for line in lines:
            patch+=line if line.endswith('\n') else line+'\n\\ No newline at end of file\n'
            if len(patch.encode())+total>32768:
                overflow=True; patch=''; break
        if not overflow: patches.append(patch); total+=len(patch.encode())
    elif changed and not selection['capture']: raise ValueError('source changed before commands')
if set(files)!=selected: raise ValueError('selected source is not committed regular text')
identity()
if overflow:
    print(json.dumps({'failure':'source_patch_diff_budget_exceeded'}))
    sys.exit(0)
diff=''.join(patches)
print(json.dumps({'head':selection['head'],'tree':selection['tree'],'files':[files[p] for p in requested],'diff':diff,'diff_sha256':hashlib.sha256(diff.encode()).hexdigest()},ensure_ascii=False))
`
