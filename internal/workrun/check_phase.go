package workrun

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// CandidatePhaseChecker keeps candidate build state for one controller phase.
// Calls within the callback are sequential; baselines remain independent.
type CandidatePhaseChecker interface {
	WithCandidate(context.Context, string, io.Writer, func(Checker) error) error
}

type candidateExecute func([][]string, io.Writer) error
type candidatePhase func(candidateExecute) error

func (checker DockerChecker) WithCandidate(ctx context.Context, workspace string, output io.Writer, callback func(Checker) error) error {
	checker.workerSequence = new(int)
	return checker.checkPhase(ctx, workspace, nil, output, nil, func(execute candidateExecute) error {
		return callback(&candidateChecks{base: checker, workspace: workspace, execute: execute})
	})
}

type candidateChecks struct {
	base      DockerChecker
	workspace string
	execute   candidateExecute
}

func (checks *candidateChecks) Check(ctx context.Context, workspace string, commands [][]string, output io.Writer) error {
	if workspace != checks.workspace {
		return errors.New("candidate check workspace differs from its phase")
	}
	return checks.execute(commands, output)
}

func (checks *candidateChecks) Verify(ctx context.Context, workspace string, request VerificationRequest, output io.Writer) error {
	if err := validateVerificationRequests([]VerificationRequest{request}, nil); err != nil {
		return err
	}
	if request.SourcePatch != nil {
		return errors.New("source correction export requires the dedicated verification interface")
	}
	if _, err := io.WriteString(output, "Additional verification candidate phase\n"); err != nil {
		return err
	}
	if err := checks.Check(ctx, workspace, request.Commands, output); err != nil {
		return &VerificationFailure{Phase: "candidate", Err: err}
	}
	if request.Baseline != nil {
		if _, err := io.WriteString(output, "Additional verification baseline phase\n"); err != nil {
			return err
		}
		// Preserve a different source copy and HOME even while the candidate lives.
		if err := checks.base.check(ctx, workspace, request.Commands, output, request.Baseline); err != nil {
			return &VerificationFailure{Phase: "baseline", Err: err}
		}
	}
	return nil
}

func (checks *candidateChecks) VerifySourcePatch(ctx context.Context, workspace string, request VerificationRequest, head, tree string, output io.Writer) (*SourcePatchResult, error) {
	if workspace != checks.workspace {
		return nil, errors.New("candidate check workspace differs from its phase")
	}
	return checks.base.VerifySourcePatch(ctx, workspace, request, head, tree, output)
}

func (checker DockerChecker) runCandidatePhase(ctx context.Context, workspace, inputs, mount, workPath, name, image string, execute candidateExecute, phase candidatePhase) error {
	fingerprint := func() (string, error) {
		started := time.Now()
		defer checker.recordTiming("candidate_fingerprint", nil, 0, started)
		args := checkContainerEnvironment([]string{"run", "--rm", "--name", name + "-fingerprint", "--label", testOwnerLabel + "=" + name, "--env", "SDLC_CHECK_WORKSPACE=" + workPath, "--pull", "never", "--network", "none", "--user", "0:0", "--read-only", "--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--security-opt", "no-new-privileges", "--mount", mount + ",readonly", "--mount", "type=bind,src=" + workspace + ",dst=/source,readonly"})
		if inputs != "" {
			args = append(args, "--mount", "type=bind,src="+inputs+",dst=/inputs,readonly")
		}
		if checker.ExpectedTree != "" {
			args = append(args, "--env", "SDLC_CHECK_TREE="+checker.ExpectedTree)
		}
		args = append(args, "--entrypoint", "python3", image, "-c", candidateFingerprint)
		data, err := checker.Runtime.Docker.Output(ctx, args...)
		if err != nil {
			return "", fmt.Errorf("inspect candidate check source: %w", err)
		}
		digest := strings.TrimSpace(string(data))
		if len(digest) != 64 || strings.Trim(digest, "0123456789abcdef") != "" {
			return "", errors.New("candidate check source fingerprint is unavailable")
		}
		return digest, nil
	}
	initial, err := fingerprint()
	if err != nil {
		return err
	}
	reusable := true
	return phase(func(commands [][]string, output io.Writer) error {
		if !reusable {
			if _, err := io.WriteString(output, "Candidate source or frozen inputs changed during checks; using an independent workspace and HOME\n"); err != nil {
				return err
			}
			return checker.Check(ctx, workspace, commands, output)
		}
		if len(commands) == 0 {
			return errors.New("repository checks are required")
		}
		for _, command := range commands {
			if len(command) == 0 || command[0] == "" {
				return errors.New("repository check has no executable")
			}
			for _, argument := range command {
				if strings.ContainsRune(argument, 0) {
					return errors.New("repository check argument contains NUL")
				}
			}
		}
		if err := execute(commands, output); err != nil {
			// Failed commands may have changed source or HOME before exiting.
			// Their state cannot be trusted by subsequent diagnostic requests.
			reusable = false
			return err
		}
		current, err := fingerprint()
		if err != nil {
			return err
		}
		reusable = current == initial
		return nil
	})
}

// Ignore untracked build outputs, but include every committed source path and
// frozen input. A changed, missing or linked path invalidates further reuse.
const candidateFingerprint = `import hashlib,os,pathlib,stat,subprocess
env={'PATH':os.environ['PATH'],'HOME':'/tmp','GIT_CONFIG_NOSYSTEM':'1','GIT_CONFIG_GLOBAL':'/dev/null'}
git=['git','--no-replace-objects','-c','safe.directory=/source','-c','core.hooksPath=/dev/null','-c','core.fsmonitor=false','-C','/source']
paths=set(subprocess.check_output(git+['ls-tree','-r','--name-only','-z',os.environ.get('SDLC_CHECK_TREE','HEAD')],env=env).split(b'\0'))-{b''}
if os.path.isdir('/inputs'):
    for root,dirs,files in os.walk('/inputs',followlinks=False):
        for name in files:
            paths.add(os.fsencode(str((pathlib.Path(root)/name).relative_to('/inputs'))))
digest=hashlib.sha256()
base=pathlib.Path(os.environ.get('SDLC_CHECK_WORKSPACE','/workspace'))
for raw in sorted(paths):
    path=base/os.fsdecode(raw)
    digest.update(raw+b'\0')
    if any(p.is_symlink() for p in [path,*path.parents]):
        digest.update(b'linked');continue
    try:
        mode=path.stat().st_mode
        if not stat.S_ISREG(mode):
            digest.update(b'nonregular');continue
        digest.update(str(mode&0o111).encode()+b'\0')
        with path.open('rb') as stream:
            for chunk in iter(lambda:stream.read(65536),b''):
                digest.update(chunk)
    except FileNotFoundError:
        digest.update(b'missing')
    digest.update(b'\0')
print(digest.hexdigest())
`

// WithDiagnosticRevision pins every candidate copy and fallback to its accepted tree.
func (checker DockerChecker) WithDiagnosticRevision(revision Revision) Checker {
	checker.ExpectedTree = revision.Tree
	return checker
}
