# Signing and publication spike

From the repository root, run `python3 -m unittest discover -s research/spikes/security -p 'test_publisher_spike.py' -v`. It uses Python's standard library, Git and
`ssh-keygen`. Every fixture creates a disposable Ed25519 key, synthetic commits
and local bare repositories, then removes them. It never accesses host GitHub
authentication, invokes a model or container, or publishes to GitHub.

The worker exports a versioned JSON object manifest. The coordinator admits a
fixed job with repository, base branch and SHA, head branch, author, candidate
SHA-256 and every reviewed tree SHA. Publication accepts only a request matching
that job. Candidate names must be single spool filenames; the reader opens the
spool and file without following symlinks, rejects hardlinks and nonregular
files, limits input size, and reads one byte snapshot before validation.

The helper fetches the fixed base from its approved local remote into a fresh
bare Git repository. It validates object hashes and linear parents, imports
objects through `hash-object` without filters, checks them with `fsck`, and signs
each increment through fixed `commit-tree -S` operations. Each resulting tree
must match the review. The helper verifies each SSH signature before a fixed
non-force push. No worktree is created. Worker configuration, hooks, filters,
attributes, SSH commands, credential helpers and project scripts are not
imported or executed. Tracked symlink contents remain Git data.

Git subprocesses receive an environment allowlist, empty global/system config,
an empty template, disabled hooks/attributes/fsmonitor/replacement objects and
file-only transport. Every Git subprocess runs from the protected helper home,
including remote-head checks without an explicit repository. This prevents a
caller checkout's local config from rewriting the approved remote. Git discovery
also stops at the home's resolved physical parent, preventing config discovery
in ancestor directories even when the supplied home path traverses a symlink.
Git ignores a ceiling equal to the current directory, so the parent is required.
The home is checked for `.git` entries and bare repository markers before any
Git invocation and at helper construction. Fixed local
upload/receive command paths are quoted for Git's shell command interpretation;
the wrappers set the same neutral working directory and disable remote hooks.
The only signing operation constructs a
commit for a fixed reviewed job; there is no arbitrary command or data-signing
endpoint. Candidate parsing happens before signing. The signing key path is
supplied only to fixed helper operations.

Signing changes commit IDs. The receipt records every candidate/signed/tree
mapping and the canonical signed head. A stacked job must base its candidate
and PR on the previous job's published signed head and branch. Receipts are
saved before push; repeat publication and a simulated crash after push reuse
the signed SHA and deterministic mock PR identity.

## Proof and limits

The private proof record, retained outside the tracked source tree, records the acceptance count and a synthetic two-ticket stack with
two signed increments per ticket. The tests verify signatures with OpenSSH,
tree IDs, parents and local remote branch IDs. PR creation is a deterministic
mock. GitHub permissions, Verified status, API failures and duplicate live PRs
remain untested.

This spike proves the Git/configuration boundary, not OS isolation. Worker and
helper fixtures run under one host UID; a malicious process under that UID
could read helper key files directly. A real worker must run under a separate
identity or container with no helper storage, secret mounts or shared writable
helper state. Trusted helper storage, policies, executable paths, job admission,
remote selection and their ancestors are assumed protected.

This prototype resolves `GIT` from the startup process's `PATH` at import and
then fixes the absolute path. That startup selection is trusted here; it is not
a production executable boundary. A deployed helper needs a pinned, root-owned
Git binary and interpreter/executable chain, including its Git subprocesses,
Python, OpenSSH, transport wrappers and shared libraries. A user's Homebrew
installation or attacker-controlled startup `PATH` does not meet that condition.
Protecting the service's credentials also does not protect an interactive user's
ambient `gh` or signing clients from an unrestricted host agent under that user's
UID. The worker must be restricted independently of the publisher service.

The helper currently has an in-memory admitted-job registry. Restoring the
trusted registry, repository leases, fsync/crash durability, cancellation,
timeouts, object-count limits, remote base/ref races, key rotation and real
HTTPS/GitHub API publication need implementation before unattended use. No
force capability exists, and stacked jobs cannot target the repository root
base branch. The spike conservatively rejects observed base drift at admission,
before signing, and immediately before a new push. This is an experimental
policy, not an accepted requirement to freeze a branch indefinitely. Plain Git
push protects against non-fast-forward updates, but the base check and push
still have a remote time-of-check/time-of-use race and no atomic condition on
the PR base branch.

Candidate manifests can preserve arbitrary reviewed Git data and messages.
There are no project acceptance checks here; those belong in credential-free
worker validation and must bind their evidence to the same candidate digest.

After review, only `publisher_spike.py`, `test_publisher_spike.py` and a limits
document are suitable to adapt into the public repository. Keep `run_spike.py`
and `proof.json` outside the tracked tree: the private proof script writes its
evidence beside itself and records the selected local Git path. Generated
fixture keys, repositories and Python bytecode also stay outside tracked source.
