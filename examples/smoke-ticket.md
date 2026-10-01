# Test Codex execution in Docker

Complete this small task to check that Codex can edit a repository, run commands
inside Docker, and create a signed commit. The SDLC runner then publishes that
branch and creates a draft pull request. Follow the repository instructions.

## Required change

Create `docs/codex-docker-smoke.md` containing exactly this line, followed by a
newline:

```text
Codex completed this task inside Docker.
```

Keep the change limited to that file.

## Checks

Run these commands from the repository root and confirm that all three succeed:

```sh
test -f /.dockerenv
test "$(id -u)" = 1000
python3 -c 'from pathlib import Path; assert Path("docs/codex-docker-smoke.md").read_bytes() == b"Codex completed this task inside Docker.\n"'
```

## Completion

Create exactly one signed commit with this message:

```text
Add Docker smoke test marker
```

Report the check results, the commit hash, and any remaining issues, then return
control to the SDLC runner. Do not push or create a pull request yourself; the
runner invokes the container's publish helper after your checks and commit.
Do not merge, deploy, or change Git signing or account settings.

## Workflow acceptance criteria

The overall smoke test is complete only when the container's publish helper has:

- Independently checked the exact marker file, Docker environment, worker user,
  one-file change and single-commit requirement before publishing.
- Verified the selected account and every proposed commit's signature.
- Pushed the completed branch to the selected GitHub repository.
- Created a draft pull request targeting the configured base branch.
- Confirmed that the remote branch and the pull request point to the local
  commit and that the pull request is a draft from the selected account.
- Printed the confirmed pull request URL and commit hash.

The connected smoke-test runner performs this stage automatically after Codex
finishes successfully. A plain `exec` run ends after the local signed commit.
