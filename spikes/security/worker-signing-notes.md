# Worker-created SSH commit signing spike

From the repository root, run `python3 -m unittest discover -s spikes/security -p test_worker_signer.py -v`. The tests use Python's standard
library, Git and OpenSSH. All keys, repositories and capabilities are disposable
synthetic fixtures. No model, Docker, GitHub authentication or network publication
is involved.

The worker runs real `git commit -S`. Git invokes an OpenSSH-compatible signing
shim, which sends its unsigned commit payload over a preconnected anonymous Unix
socket descriptor. The coordinator-side signer holds the private key outside the
worker workspace and returns an SSH signature. Git inserts that signature and
creates the final commit itself. The worker retains this exact signed OID; there
is no later commit reconstruction.

The normal worker environment and configuration contain the public key, an opaque
job capability, a client configuration path and a socket descriptor. They contain
no private key material or key path. Protocol responses contain only success and
the SSH signature, or a fixed rejection message. The signer calls a fixed
`ssh-keygen` operation with an environment allowlist, no SSH agent, a dedicated
working directory, a bounded temporary file and a subprocess timeout.

Each job fixes its repository scope, author/committer identity, capability and
expiry. The coordinator authorises each increment's tree, signed parent, message
and timestamps. The service accepts only a bounded unsigned single-parent Git
commit matching that exact approved payload. It rejects other data, modes,
identities, repository hints, capabilities, extra fields and unapproved changes.
An authorised replay returns the cached signature, but expiry and revocation are
checked before replay. Frames have a size limit and an absolute receive deadline
once their first byte arrives. Idle waiting permits the next ticket increment.

Approval scope determines what the worker can sign. A job supports several
incremental approvals; it is not limited to one commit for the whole ticket. The
tests create three successive signed commits and use each worker-created OID as
the next parent. A new unapproved increment fails, then succeeds when the
coordinator admits its payload under the same job capability. `Signer.authorize`
and `WorkerFixture.approve` are executable fixture admission methods, not a
production review or evidence-validation system. A real coordinator must bind
review and tests to the immutable objects and bytes it approves.

The shim accepts only Git's `-Y sign -n git -f PUBLIC_SELECTOR [ -U ] PAYLOAD`
shape. It reads regular bounded files from the registered temporary directory
without following leaf symlinks, verifies the public-key selector, and writes
the signature through a directory descriptor without following or overwriting
a planted output link. Worker hooks may still execute as worker code; the hook
probe demonstrates that changing the staged tree cannot expand the signing
approval. No repository scripts run inside the signer.

## Limits

This is a protocol fixture, not OS credential isolation. Both processes run as
the same UID. An explicit test proves that a worker process can read and write
the external disposable key directly; it exports no key bytes. It can also
tamper with same-UID code/state or attack the coordinator outside this protocol.
A real service needs protected ownership, a separate identity, client
authentication, immutable installed code and a worker sandbox. Running a worker
as root in Docker does not establish protection for a same-UID host socket or
client. VM provisioning is outside this spike.

The preconnected descriptor survives this local subprocess chain. This fixture
implements no channel across Docker `run` or `exec`. Deployment needs an
authenticated protected service channel or proxy; this fixture does not supply
a Docker launch transport. The signer and shim executable/interpreter chain
also need a trusted installed location. The fixture chooses Git and Python from
its startup environment; a user-writable Homebrew installation is not a service
security boundary.

Git SSH signatures use namespace `git` and do not encode repository identity.
An approved public signed commit can be copied to another repository. The job's
repository check limits signing requests, while a separate push credential and
provider policy must limit destination authority. The public signature itself
does not enforce repository scope.
Capability expiry and revocation stop new broker responses; they do not revoke
an already emitted public signature. The tests verify that distinction.

The fixture does not implement durable registration, key rotation, multi-client
leases, OS peer authentication, aggregate quotas or production review admission.
Its event and signing-environment instrumentation also has no retention cap;
production audit storage and request rates need explicit limits.
An idle client can retain its one session; the active-frame deadline prevents
trickled frames from extending one read indefinitely. Availability under hostile
workers still requires service resource limits. A protected service does not
shield an interactive user's ambient `gh` or signing clients from an unrestricted
agent under that user's UID.

For integration, `WorkerFixture(root)` is a context manager exposing `.worker`,
`.public_key`, `.head`, `.signer`, `.job`, and `stage`, `approve`, `commit`, `verify`
and `git` methods. Enrol its public key in a synthetic push provider and send its
actual signed objects unchanged. The private proof driver and generated OID records remain outside this public
source tree. The combined acceptance test is `test_worker_end_to_end.py`.
