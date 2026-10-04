# Omarchy and Linux execution

Assessed: 4 October 2026. Documentation and source review only. No Omarchy
installation, VM connection, provider login or connected execution was performed.

Linux is a suitable direction for a known SDLC execution VM. Omarchy is worth
trying as a development desktop, while keeping SDLC's worker contract independent
of the desktop distribution. The [remote VM proposal](../../docs/proposals/remote-vm-execution.md)
records the implementation plan; this note supplies the OS assessment and the
Omarchy defaults relevant to that work. These are recommendations, not an adopted
OS choice or evidence of a working remote executor.

## Where Omarchy helps

Omarchy combines Arch Linux and the Hyprland desktop with configured terminals,
editors, shell tools and development environments. Its manual documents Docker
and Compose, GitHub CLI, Go and .NET setup, and launchers for Codex and Claude.
That makes it a candidate workstation for developing SDLC, running local checks
and supervising jobs. Its desktop integrations do not provide SDLC's ticket,
check, review or publication contract.
[Platform description](https://omarchy.org/manual/omarchy-on/),
[development tools](https://omarchy.org/manual/development-tools/),
[agent tools](https://omarchy.org/manual/ai/).

The Omarchy website links a [Mac VM application](https://github.com/omacom/try-omarchy)
for trying the desktop. This is an optional workstation experiment; SDLC runtime
compatibility still needs its own validation.

For a headless executor, keep a maintained Linux distribution already on the
known VM if it meets the Docker requirements. For a new image, Ubuntu Server LTS
or Debian stable are reasonable starting choices. Docker documents installation
support for both. This recommendation avoids introducing the desktop as a worker
dependency; it does not claim a measured reliability or performance advantage.
Record the selected release and its support deadline. Omarchy uses Arch's rolling
release model, so its host updates need a separate maintenance policy from the
pinned SDLC worker image.
[Docker on Ubuntu](https://docs.docker.com/engine/install/ubuntu/),
[Docker on Debian](https://docs.docker.com/engine/install/debian/),
[Ubuntu lifecycle](https://ubuntu.com/about/release-cycle),
[Debian releases](https://www.debian.org/releases/),
[Omarchy security and updates](https://omarchy.org/manual/security/).

## Defaults that affect SDLC

- **Docker permissions:** Omarchy requires `sudo` for Docker by default and leaves
  the user outside the Docker group. SDLC invokes `docker` directly. Provision
  deliberate engine access for the trusted controller; ordinary rootful Docker
  access grants substantial authority over the VM. Do not solve this by running
  every SDLC command as root. Rootless compatibility has not been assessed here.
  [Omarchy Docker defaults](https://omarchy.org/manual/development-tools/).
- **Agent launch settings:** Omarchy's agent shortcuts use automatic approval
  modes. They are separate from SDLC's container launches and should not replace
  its recorded runtime, provider settings or credential boundaries.
  [Agent launchers](https://omarchy.org/manual/ai/).
- **Account switching:** Omarchy offers optional account autoswitching near
  usage limits. SDLC must stop at exhausted limits and must not rotate accounts
  to evade them. Keep that feature outside SDLC provider execution.
  [Omarchy accounts](https://omarchy.org/manual/ai/),
  [SDLC provider rules](../../docs/provider-usage.md).
- **Remote access:** Omarchy leaves SSH disabled initially. Provision access
  explicitly if using it as the worker OS. An existing maintained server image
  may already have the required administrative route.
  [Omarchy security defaults](https://omarchy.org/manual/security/).

## SDLC foundations and remaining work

At assessment, the recorded runtime uses Debian Bookworm and supports AMD64 and
ARM64. The CLI cross-compiles for Linux, but native Linux execution remains
unverified. The runtime preflight accepts a local Unix socket or Windows named
pipe and rejects remote Docker endpoints. Keep these as dated findings: publisher
and controller implementation is active and can supersede them.
[Runtime recipe](../../runtime/Dockerfile),
[CLI validation record](../../docs/cli.md),
[engine preflight](../../internal/runtimeimage/runtime.go).

Recommendation: keep the controller, Docker engine, captured inputs and private
run state together on the VM. Use SSH for SDLC control operations. Docker itself
supports SSH contexts, but its bind mounts resolve paths on the daemon host;
changing the Docker context does not transfer the client's workspace.
[Docker SSH connections](https://docs.docker.com/engine/security/protect-access/#use-ssh-to-protect-the-docker-daemon-socket),
[bind mount constraints](https://docs.docker.com/engine/storage/bind-mounts/#considerations-and-constraints).

Foreground runs currently depend on the launching terminal. Durable remote jobs
need supervised controllers, attachment, cancellation and restart reconciliation.
The [unattended delivery proposal](../../docs/proposals/unattended-docker-delivery.md)
already defines the separate publisher, machine signing and controller work.
The [remote VM proposal](../../docs/proposals/remote-vm-execution.md) extends those
components with registration, bootstrap, input transfer and dashboard visibility.
Remote execution should share that lifecycle with local detached execution.

Omarchy includes Herdr for persistent terminal workspaces. It could be an optional
human interface after the SDLC worker contract exists. The earlier
[Herdr assessment](herdr-remote-assessment.md) distinguishes terminal persistence
from job recovery and SDLC publication guarantees. A VM also adds a machine
boundary around execution, while its administrator and Docker engine remain
trusted.
[Omarchy terminal tools](https://omarchy.org/manual/tuis/).

## First VM trial and handoff

Start with one existing VM and one account owner. The first trial should be
credential-free:

1. Record OS release, architecture, kernel, Docker/Compose versions, backing
   filesystem, storage driver, available memory and disk, and controller engine
   permissions. Confirm the selected distribution's support period.
2. Install the Linux CLI and build the shared runtime from a recorded revision.
   Run offline runtime validation and the Docker boundary checks documented in
   the [development guide](../../docs/development.md).
3. Run the disposable [.NET smoke example](../../examples/dotnet-smoke/README.md)
   and the concurrent port-isolation probe on that VM. Record resource demand,
   check-only input separation and cleanup results. Existing local passes do not
   establish a VM pass.
4. Use those results to continue the remote proposal: shared detached lifecycle,
   bootstrap parity, bounded source transfer, named worker registration and host
   status. Validate disconnect and restart behavior independently of a provider.

Before any connected trial, review current official documentation for the exact
account and execution mode. OpenAI recommends API keys for automation and
documents device login for headless machines. Anthropic permits owner sign-in to
the unmodified Claude Code client in hosted environments under its stated terms.
Neither an installed agent launcher nor successful login approves every
unattended workflow. Use the official clients and flows, preserve the existing
provider/check/publisher separation, and stop on refusals or exhausted limits.
Remote authentication support and a connected trial remain separate work.
[OpenAI automation](https://learn.chatgpt.com/docs/non-interactive-mode),
[headless login](https://learn.chatgpt.com/docs/auth),
[Claude authentication rules](https://code.claude.com/docs/en/legal-and-compliance),
[SDLC provider rules](../../docs/provider-usage.md).

Unresolved inputs for that trial are the VM's OS and architecture, SSH/private
network access, workload capacity, publication identity and the supported provider
authentication mode. No VM size, cloud cost or throughput claim was validated.
