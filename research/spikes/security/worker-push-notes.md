# Direct worker Git push proof

This disposable local proof runs real `git commit -S` and `git push` in the
worker process. The worker's signed commit reaches a local bare repository with
the same object ID. It uses generated SSH keys, fake tokens and synthetic data.
It does not contact GitHub, start Docker, provision a VM or use host credentials.

Run it with Python 3, Git supporting SSH signatures, and `ssh-keygen` installed:

```sh
python3 -m unittest discover -s research/spikes/security -p 'test_push_spike.py' -v
```

The tests generate their fixtures in temporary directories and remove them.
There are no installation or network steps. The source currently expects
`ssh-keygen` at `/usr/bin/ssh-keygen`, as on the tested host.

## What runs

`worker_push_fixtures.PushFixture` registers one job, repository, branch, public signing
identity and finite job deadline with `push_spike.Provider`. The controller
holds a generated issuer HMAC key. The worker receives a bearer token and an
opaque capability for renewing that registered job. The renewal request has
exactly `job` and `capability` fields. It cannot choose another repository,
permissions or lifetime, omit repository scope, or request a broad fallback.

`worker_git()` runs Git in the worker checkout. `worker_push()` invokes
`git push fixture::target.git HEAD:refs/heads/job/synthetic-a`. Git invokes the
`git-remote-fixture` helper, which connects over stdio to a modeled provider
service. The service authenticates the bearer token before invoking real
`git receive-pack`. It checks signed repository scope, permission, expiry,
revocation and registered job membership even if the worker changes its URL.
The client helper's URL handling is not the enforcement boundary. A changed
URL and forged token still fail the server check.

Local TCP binding was denied by the execution sandbox, so this proof uses Git's
remote-helper `connect` protocol. It does not test HTTP, TLS, credential helpers,
GitHub authentication or a deployed service boundary. The provider process is
launched locally under the same UID and its directory remains readable by that
UID. This is an adapter for testing the protocol and policy, not isolation.

The receive policy is separate from credential scope. Each authenticated job
selects its registered policy through server-controlled state. The policy
allows one branch update per push, rejects main, tags, other jobs' branches,
deletions and non-fast-forward updates, and verifies new commits plus the tip
against the registered public key. The receiver checks incoming objects in
Git's quarantine store before accepting the ref. The server rebuilds its
environment and the hook preserves only validated receive-pack quarantine
paths. The worker supplies no issuer key, policy path, hooks or Git configuration
to the backend. Two jobs can enroll distinct branches in the same repository,
and the second signed commit can retain the first commit as its parent.

The standalone tests put a disposable signing key in the worker checkout only
to make signed test commits. The setup accepts `public_key=` for a separate
signer experiment: configure that experiment's Git signing shim in
`fixture.worker`, obtain `fixture.token()`, and call `fixture.push(token)`.
`fixture.remote_head()` returns the pushed OID for comparison. No commit is
rebuilt or re-signed by the push service.

## Measured checks

The suite verifies worker signing and unchanged pushed OIDs; a two-job signed
stack; repository, signature and path tampering; missing, expired and revoked
credentials; renewal scope and finite job expiry; overlapping token lifetime;
protected refs, excess refs, force pushes and deletion; unsigned incoming and
already-present tips; and removal of worker-controlled backend environment.
Errors and token payloads do not disclose the fake issuer key. These are bounded
protocol tests, not a proof against every possible cryptographic attack.

A positive control reads the fake issuer key from the provider directory in a
worker subprocess. It succeeds because the two processes share a UID. Neither
file modes nor this adapter protect issuer authority from full access to that
host. A deployed issuer must run behind a separately enforced service boundary,
with its storage, implementation, executable chain and credentials inaccessible
to the worker. Direct access to the bare repository must also be excluded;
otherwise a worker could bypass the modeled provider endpoint.

## GitHub production requirements

The fake HMAC token format is not a GitHub token implementation. GitHub App
installation tokens can authenticate Git over HTTPS with the Contents
permission. A production issuer must retain the App private key and App JWT
outside the worker. For every renewal it must resolve the registered job's
installation and immutable repository ID itself, then explicitly request
`repository_ids: [registered_repository_id]` and the minimum required
permissions, normally `contents: write` for Git pushes. Give the worker only
the resulting installation token. Request additional permissions only for a
concrete operation that needs them. [GitHub installation authentication](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/authenticating-as-a-github-app-installation)

The issuer must fail closed when job enrollment, repository scope or permission
validation is missing. GitHub defaults an issuance request without repository
selection to all repositories available to the installation, and without a
permissions selection to all granted App permissions. Tokens expire after
about one hour. [GitHub token issuance](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-an-installation-access-token-for-a-github-app)

This fake provider supports subminute tokens and knows the job deadline. It
clips token expiry to that deadline and denies all issued tokens immediately
when the controller ends a job. These are extra modeled service properties.
GitHub does not know the job deadline. In production, stopping renewal prevents
new tokens but does not invalidate existing ones: track and explicitly revoke
outstanding installation tokens at job end. A controller crash or failed revoke
can leave them usable until expiry. Minting a replacement token must not be
assumed to revoke the previous token; this proof permits overlap and tests
revocation separately. [GitHub installation token revocation](https://docs.github.com/en/rest/apps/installations#revoke-an-installation-access-token)

A repository-scoped Contents token is not restricted to one branch or one
commit. The receive hook here models an additional policy that GitHub tokens do
not inherently provide. Production branch and tag rules must be configured and
tested separately, including App bypass privileges, protected main, force
updates, deletions, and the intended job branches. [GitHub rulesets](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/about-rulesets)

## Remaining limits

This proof has no isolated worker, durable transactions, locking, concurrent
renewal safety, rate limit, HTTP transport or production GitHub API adapter.
Token validity is checked when a Git connection begins; expiry or revocation
during an already-authenticated push is not rechecked before refs change.
The hook verifies signature identity, not a human review or exact signing
authorization. The separate signer experiment supplies that policy. Provider
state, hooks, repository configuration, paths and executable startup remain
trusted inputs. The same-UID fixture permits tampering with them and is not a
protected secret store. No live GitHub protection or workflow-file behavior has
been verified.

The [Git remote helper protocol](https://git-scm.com/docs/gitremote-helpers) and
[receive-pack quarantine documentation](https://git-scm.com/docs/git-receive-pack)
describe the Git mechanisms exercised by this proof.
