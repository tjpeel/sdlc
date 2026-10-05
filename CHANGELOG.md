# Changelog

The application version is declared in `internal/buildinfo/version.go`.
Each delivered beta iteration advances its numeric beta identifier. Source,
running/installed executable and Docker runtime identities are reported separately.
See the [versioning plan](docs/proposals/beta-versioning.md).

## Unreleased

## 0.1.0-beta.1 - 2026-10-05

### Added

- List configured GitHub profiles locally and remember the selected GitHub profile and repository identity for a checkout.
- Record run preparation failures and show them in dashboard history before a provider run journal exists.
- Opt-in `sdlc shell` with slash commands, help, local reference/ticket completion, explicit content inspection, project selection and project-scoped history.
- Terminal-filling layout that redraws on resize, inherited terminal colours and font, a Robby Russell prompt, and a plain line mode.
- Offline run-plan review before explicit Start, private one-use terminal launch receipts and existing-controller monitoring. Native iTerm2 launch requires terminal setup; unsupported configurations show a manual command.
- Read-only project onboarding and separate running, PATH, selected-source and recorded-runtime version evidence.
- Structured CLI help, work discovery and run previews for the shell and other clients.

### Fixed

- Preserve committed `.env.example` templates in source snapshots when their parent directory is allowed, while excluding private and untracked environment files.
- Retry failed feature preparation through its owning controller without overwriting an existing run or inventing a provider resume checkpoint.

Earlier changes remain available in Git history. No retrospective release numbers are assigned here.

This establishes the beta source baseline. Release archives/tags and complete
conversation inspection, bulk history clearing and default bare-command shell
launch remain follow-up work.
