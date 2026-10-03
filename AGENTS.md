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
