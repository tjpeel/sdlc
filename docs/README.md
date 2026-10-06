# Documentation

- [Connected onboarding](onboarding.md): the joint test runbook and remaining gates before real tickets.
- [1Password signing setup](1password-signing-setup.md): provision the vault, Read Items Service Account and dedicated signing key, then use the setup/status wizard.
- [GitHub profiles and signing trial](github-docker-test.md): separate native logins, signing checks and a disposable connected delivery trial.
- [GitHub credentials and signing](github-credentials.md): implemented Docker publication, credential boundaries and remaining risks.
- [Credential security handoff](proposals/credential-security-hardening.md): Sandcastle comparison, current SDLC risks and proposed hardening for unattended work.
- [Open source signing-secret provider](proposals/signing-secret-providers.md): planned implementation of one alternative backend with isolation, bootstrap and verification criteria; no product selected.
- [Earlier 1Password access and signing test](1password-test.md): historical disposable-vault/signing trials and GitHub App experiments; use signing setup for final provisioning.
- [Unattended Docker delivery](proposals/unattended-docker-delivery.md): original design proposal; Docker publication/signing are implemented, while detached supervision remains future work.
- [Remote VM execution](proposals/remote-vm-execution.md): Tailscale connection, shared bootstrap, session and port isolation, host dashboard visibility and a staged implementation plan.
- [Private messaging feedback loop](proposals/private-feedback-loop.md): VPN-only question inbox, self-hosted chat comparison, answer dispatch and recovery plan.
- [CLI](cli.md): install, build the shared image, log in, configure instructions, open interactive sessions and run one ticket through draft PR delivery and review.
- [Interactive shell](interactive-shell.md): the opt-in first pass, slash commands, project scope, offline plan review and version evidence.
- [Interactive CLI proposal](proposals/interactive-cli.md): persistent slash commands, local ticket completion, guided help and onboarding, implementation stages and a visual prototype.
- [Supporting CLI contracts](proposals/interactive-cli/cli-contract.md): shared structured commands, background terminal launch and acknowledgement, following output and preserving existing CLI behaviour.
- [Beta versioning proposal](proposals/beta-versioning.md): changelog increments and distinct source, built/installed CLI and runtime identities.
- [Changelog](../CHANGELOG.md): dated beta changes and fixes.
- [Workflow](workflow.md): the implemented single-ticket process and future stacked-ticket design.
- [Ticket-stream prompt](prompts/implement-ticket-stream.md): drive the existing skills for selected tickets.
- [Development](development.md): repository layout, checks and commit process.
- [Publication safety](publication-safety.md): protect this public repository.
- [Provider usage](provider-usage.md): supported authentication and service rules.
- [Usage metrics](usage-metrics.md): durable native counters, observed quota signals and comparisons across runs.
- [Headroom](headroom.md): opt-in local proxy, pinned image, native authentication routes and offline validation.

Earlier investigations and prototype instructions are in the
[research archive](../research/README.md).
