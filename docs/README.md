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
- [CLI](cli.md): install, build the shared image, log in, configure instructions, open interactive sessions and run one ticket through draft PR delivery and review.
- [Workflow](workflow.md): the implemented single-ticket process and future stacked-ticket design.
- [Ticket-stream prompt](prompts/implement-ticket-stream.md): drive the existing skills for selected tickets.
- [Development](development.md): repository layout, checks and commit process.
- [Publication safety](publication-safety.md): protect this public repository.
- [Provider usage](provider-usage.md): supported authentication and service rules.

Earlier investigations and prototype instructions are in the
[research archive](../research/README.md).
