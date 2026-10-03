# Security and unattended execution spikes

Research date: 1 October 2026. These are experiments and a discovery handoff,
not an installed runner or an accepted architecture. They use disposable keys,
synthetic Git data and local publication. Supplied work tickets, credential
profiles and job output belong outside this public source tree.

For continuation in a fresh conversation, read the
[protected runner handoff](../../docs/protected-runner-handoff.md) first. It
distinguishes the active trial from older alternatives in this research record.

The initial workflow is a prompt from a local repository requesting a supplied
ticket set: start from fresh remote main, work through dependencies, implement,
test, obtain separate review, fix and recheck, then publish one signed draft PR
per ticket targeting its predecessor. A failure preserves work and requests
attention. The execution agent may use Full access inside the job boundary. The
worker must create signed commits and push them itself. Reusable signing and
token-issuing keys stay outside the worker; it receives job signing capabilities
and repository-scoped transport access. Other host agents must not acquire that
authority. Interactive vault approvals are unsuitable for unattended runs.

The latest spikes test this direct worker contract. VM provisioning is outside
their scope. The earlier helper publication and VM investigation below are
research evidence, not selected installation requirements.
[Direct worker findings and limits](direct-worker-notes.md).
[Protected host service trial runbook](host-service-test-runbook.md).

The evidence supports two separate protections: an execution boundary around
the job, and an authority boundary around the credential owner. Giving an
unrestricted host process the same identity and management access as the
credential owner defeats the second protection even if the job is in Docker.

| Spike | Evidence obtained | Limit |
| --- | --- | --- |
| Host identity | A second process with the same UID read and replaced an ignored `0600` marker in a `0700` directory. | Does not test a separate OS identity or a credential service. |
| Local input capture | Inline-linked Markdown is captured into immutable bytes with hashes. Symlinks, hardlinks, FIFOs, escapes, protected directories and oversized requests are rejected. | A protected enrolled root is assumed; source edits are not an atomic snapshot. |
| Signing and stacked publication | A fresh bare helper signs every synthetic increment, preserves reviewed trees, verifies signatures and returns the canonical head for the next ticket. Hostile configuration and modified candidates are probed. | Local bare Git and mock PR receipts only; no OS credential separation or live GitHub proof. |
| Worker-created signing | Real `git commit -S` uses an exact-candidate signing service; Git creates the final signed object and retains its ID. | Anonymous descriptor transport is local-only; no protected service or Docker connection is installed. |
| Worker push and renewal | Real `git push` sends signed objects to a provider-scoped local endpoint. Renewal retains the registered repository; separate receiver policy checks refs and signatures. | Fake tokens and stdio transport; extra job-aware policy is stronger than raw GitHub token scope. |
| Combined direct worker stack | Three broker-signed worker commits across two tickets reach two remote branches with unchanged IDs and the signed predecessor as parent. | Local Git only; no live PRs, GitHub verification display or host isolation. |
| Application runtime | The supplied repository's resolved Compose manifest and integration endpoint assumptions were inspected without starting services. | Full application checks inside a protected Docker job remain untested. |

Run the executable probes from the repository root:

```sh
python3 -m unittest discover -s spikes/security -p 'test_*.py' -v
python3 -m unittest discover -s tests -v
```

Python, Git and OpenSSH are required. Test fixtures remove their disposable
keys and repositories. Never substitute an actual signing key or token. The
tests intentionally demonstrate that same-UID file permissions fail to protect
a marker; that successful probe is evidence of a failed security boundary.

The earlier helper/publisher suite passed 24 probes in both the writer and independent
reviewer runs. The host/input suites passed 12 probes; the existing repository
suite passed 84. Independent review found and corrected inherited caller and
ancestor Git configuration, unquoted transport paths, stacked publication to
the root base branch, and a base check ending before signing. The input review
also found and corrected a descriptor leak on invalid requests. These counts
describe the local experiments, not live credential or VM isolation.
[Publisher protocol and limits](publisher-notes.md).

The direct worker additions passed 37 checks: 18 signing, 18 push and one
combined two-ticket flow. Independent review checked signing and the combined
flow; the coordinator repeated the push suite. The full public security suite
passed 73 checks after copying the generic source here. Both direct-worker
suites include a positive control proving same-UID access to disposable service
secrets. These passing checks explicitly demonstrate the missing OS boundary.
[Signing protocol](worker-signing-notes.md), [push protocol](worker-push-notes.md).

A bounded native sandbox probe could not apply its policy in the current
execution environment (`sandbox_apply: Operation not permitted`). It is an
unavailable probe, not evidence of a successful secret-read denial. No system
identity, protected daemon or real credential has been installed.

The input module is an experiment, not a privileged file-reading endpoint. Its
caller must enrol a source directory securely, including its ancestors, or
retain an enrolled directory descriptor. A safer production design collects
inputs under the submitting user's restricted identity and sends bounded bytes
to the service, rather than asking a more privileged service to open supplied
host paths. It does not detect secrets pasted into Markdown. Its limited link
parser is not a general dependency resolver. Repository documentation references
must resolve against the job clone rather than the host filesystem. Explicit
ticket statuses and dependencies are metadata; this spike is not a scheduler.

Ignoring a file addresses Git inclusion. Owner-only permissions address other
OS identities. Neither separates agents using the owner's identity. Storing a
token in Keychain also does not remove the authority of an authenticated CLI:
`gh api` uses the logged-in account. A callable signer can authorise an operation
without revealing key bytes. A protected SDLC credential store therefore does
not protect the interactive user's existing authenticated clients from an
unrestricted agent running as that user. [GitHub CLI login](https://cli.github.com/manual/gh_auth_login),
[authenticated API](https://cli.github.com/manual/gh_api),
[1Password authorisation](https://www.1password.dev/ssh/agent/security).

The outer agent must have enforced restrictions on filesystem reads, credential
clients, signing/SSH sockets and Docker/VM management, or run under an identity
without those capabilities. A prompt instruction or a repository allowlist is
insufficient. The current shell sandbox denied access to the shared Docker API;
that observation says nothing about an unrestricted same-user process.

OrbStack isolated machines remove useful host integrations, but its documented
machines and containers share a Linux VM/kernel. It is not a separate kernel
boundary, and its host-side file browsing is not a secret store against the
host controller. Docker daemon access can widen mounts and container rights.
[OrbStack isolation](https://docs.orbstack.dev/machines/isolated),
[OrbStack file sharing](https://docs.orbstack.dev/docker/file-sharing),
[Docker daemon security](https://docs.docker.com/engine/security/).

The compatibility trial needs synthetic SDK/package restore and Compose-backed
checks. A test that expects loopback addresses cannot use services from an
unrelated remote daemon unchanged. Published ports and bind
paths belong to the daemon's host. That investigation considered a job VM
containing its own daemon, checkout and test runner for arbitrary Dockerfile/Compose
changes. This alternative was not adopted; VM provisioning is outside the active
trial. A worker container there would still need VM-scoped host networking or
loopback relays. The current Node Bookworm worker has no .NET or Compose. Use a
coherent pinned SDK installation; the default .NET 10 container images use
Ubuntu 24.04. [Bind mounts](https://docs.docker.com/engine/storage/bind-mounts/),
[Compose networking](https://docs.docker.com/compose/how-tos/networking/),
[.NET image platform](https://learn.microsoft.com/en-us/dotnet/core/compatibility/containers/10.0/default-images-use-ubuntu).

Stock privileged Docker-in-Docker on the shared host is unsuitable. Docker's
documented rootless nested recipe also uses privileged mode. A constrained
sibling-service adapter is an alternative, but admitting arbitrary Compose
changes would require a separate manifest/build policy and endpoint adaptation.
[Rootless nesting](https://docs.docker.com/engine/security/rootless/tips/).

Docker Sandboxes is a researched candidate for a private job VM and Docker
Engine. It is not installed here, and no live containment result was obtained.
The local CLI supports Apple Silicon on macOS 14+ and requires Docker sign-in.
Credential forwarding is a separate policy: omit host workspace mounts and
skills, disable SSH-agent forwarding, use a clean credential store, and admit
only the model provider capability needed for the job. Service secrets default
to global scope and removing a sandbox-scoped secret can expose a global
fallback. The host controller can execute commands inside a sandbox. This
mechanism protects the host from a job; it does not hide guest secrets from
that controller. [Installation](https://docs.docker.com/ai/sandboxes/install/),
[VM isolation](https://docs.docker.com/ai/sandboxes/security/isolation),
[creation](https://docs.docker.com/reference/cli/sbx/create/),
[credentials](https://docs.docker.com/ai/sandboxes/configuration/credentials/),
[settings](https://docs.docker.com/ai/sandboxes/configuration/settings/),
[host execution](https://docs.docker.com/reference/cli/sbx/exec/).

The proposed installation has a machine component and a repository component.
The machine component owns a protected service identity, executable/interpreter
chain, policies, receipts, signing identity and repository-scoped publication
access. Its job API returns bounded results and has no credential-export,
arbitrary-command or arbitrary-signing operation. Peer UID alone cannot
distinguish agents sharing that UID. The submitting agent must not gain control
of the service, its code, its configuration or its job-management authority.
This OS boundary has not been installed or proved.

Repository onboarding supplies a version pin, runtime/check configuration and
local input locations. Credentials belong to the protected machine component,
not the repository or its ignored directories. Running jobs retain their pinned
version. Account-level automation signing can be reused; repository write access
must be enrolled with the provider's scope restrictions. Fine-grained PATs and
GitHub App installation tokens are options requiring organisation policy checks.
A wrapper allowlist does not narrow a broad bearer token.
[Fine-grained tokens](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens),
[App installation tokens](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-an-installation-access-token-for-a-github-app).

The next live experiments now follow the direct worker contract:

1. On a disposable test system, install the protected service identity and
   scoped job API using only fake credentials. A legitimate job must complete
   unattended. Other host processes and the worker must fail to read or use the
   protected marker, alter the installed tools/policy, expand the repository,
   request an arbitrary signature, or reach the unrestricted management API.
   Test fake ambient authenticated clients too; non-extraction is not non-use.
2. Connect the protected signing and token services to a Docker worker using
   disposable credentials. The worker must create signed commits and push the
   same object IDs, including a dependent ticket branch. Use outside canaries
   to test host-file, socket, network and management access. No VM provisioning
   is required by this experiment.
3. Build and run a synthetic .NET/Compose fixture with loopback services,
   generated fixture credentials, resource limits and harness-owned teardown
   after failure. Run the supplied real repository's full check in that proven environment
   with its local input pack and disposable application dependencies. This
   validates compatibility without reimplementing completed tickets.
4. Add durable job admission, candidate-bound independent validation, leases,
   timeouts and restart recovery to the service. Pin the protected executable
   chain. Exercise worker signing, scoped GitHub publication and ambiguous API outcomes in an
   explicitly authorised test destination. Mock PR receipts are not live proof.

Until those live tests pass, the current credential-bearing worker is not an
appropriate unattended execution environment for valuable repository access.
