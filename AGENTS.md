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
