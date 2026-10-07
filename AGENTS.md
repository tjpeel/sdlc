# Development workflow

Work directly on `main`. Commit and push each completed, validated iteration.
Use plain commit messages without prefixes.

For each completed, user-visible application iteration, advance the numeric beta
identifier in `internal/buildinfo/version.go` and add a dated section to
`CHANGELOG.md` in the same delivery. Update the README's current beta reference.
An iteration may contain several commits; investigation, test maintenance and
rebuilding unchanged source do not require a new version. Keep delivered
changelog sections unchanged; collect completed changes under Unreleased until
the next delivery.
Follow `docs/proposals/beta-versioning.md` and verify the built/installed CLI's
version and source revision before reporting delivery complete.

# Provider service rules

Account safety and compliance with provider terms are requirements throughout
development. Read `docs/provider-usage.md` before changing authentication or
executing connected provider work, and verify current official documentation for
the proposed account type and execution mode.

- Use unmodified official clients and their documented authentication flows.
- Do not extract or replay subscription OAuth tokens through custom API clients
  or SDKs, share account credentials, or bypass provider restrictions. An explicitly
  selected local proxy may forward the official client's requests through a
  documented native base-URL setting; follow `docs/provider-usage.md` and never
  collect credentials from the cache or inject replacement authentication.
- Do not rotate accounts, change identities or repeatedly restart jobs to evade
  rate limits, usage limits, access denials or account suspensions.
- Stop and report an unclear or unsupported authentication/execution route before
  connecting an account. Historical prototypes do not establish permission.
- Keep provider tests offline with disposable fake data unless a connected test
  is explicitly authorised. Never transmit fake credentials to provider services.

# Public repository

This repository is public. Keep source, documentation, examples and committed
test data free of credentials and private work material.

- Use placeholders for example account names, emails, private repositories,
  vault references and personal filesystem paths. Public source citations are fine.
- Keep actual account settings in ignored `profiles.local.json` files and
  credentials in ignored `.secrets/` directories or an external secret store.
- Keep Codex/T3 authentication state, supplied work tickets, job output and logs
  outside the tracked source tree. Logs can contain credentials or private code.
- Generate disposable keys and fake tokens for tests. Never copy host credentials
  or secret-store exports into source, Docker build contexts or test fixtures.
- Before publishing, inspect the actual staged files for sensitive content.
  Ignore rules do not protect files that are already tracked or force-added.
- Install the repository pre-commit hook with
  `git config --local core.hooksPath .githooks`; preserve any existing hooks.
  See `docs/publication-safety.md` for installation and review guidance.
- Run `python3 scripts/check_sensitive.py --worktree` before staging and
  `python3 scripts/check_sensitive.py --staged` before committing. Before pushing,
  also run `python3 scripts/check_sensitive.py --history` and inspect unpublished
  ancestors; deleting private content in a later commit does not remove it.
- Run `python3 -m unittest discover -s tests -v` for changes to the safeguards.
