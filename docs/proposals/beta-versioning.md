# Beta versioning and component identity

Status: the first source baseline, `0.1.0-beta.1`, is implemented on 5 October 2026. No release archive or tag is created by this version change.

Establish `0.1.0-beta.1` as the first versioned delivery baseline, then increment the numeric beta identifier for each validated, delivered application change or fix. The initial change needs one version source and a changelog. The UI should show current source, executable and runtime evidence; richer build records can follow separately. The [interactive CLI proposal](interactive-cli.md) supplies the intended version screen and project onboarding.

## Current evidence

- [CLI build identity](../../internal/buildinfo/version.go) declares `Version = "0.1.0-beta.1"` and reports VCS revision/dirty state and OS/architecture.
- [Installer](../../internal/install/install.go) builds the selected source with VCS metadata and atomically replaces a validated executable on PATH. It does not record a separate release/build manifest today.
- [Runtime image](../../internal/runtimeimage/runtime.go) uses `sdlc:local`; private runtime state records immutable image ID, engine identity, source revision, build time and tool inventory. Reinstalling the CLI does not rebuild that image.
- The three recent main commits and the opt-in shell first pass are grouped into the beta baseline in the root [changelog](../../CHANGELOG.md). The [shell guide](../interactive-shell.md) describes implemented behaviour and remaining validation limits.

## Version policy

Use the existing `internal/buildinfo.Version` as the canonical application version source; do not introduce a competing hard-coded version in the installer, Dockerfile or UI. The first pass sets it to `0.1.0-beta.1` and adds the dated changelog section. The current installer already builds the source containing this value. A generated version file or richer build manifests are not prerequisites.

During this beta series, one delivered iteration with a user-visible change or fix advances `0.1.0-beta.N` to `0.1.0-beta.N+1`. An iteration may contain several commits. Rebuilding identical code, local edits, failed builds and investigation commits do not consume another version. A feature is listed when it works and passes its acceptance checks, even if later refactoring stages remain incomplete. Documentation-only releases can receive a beta increment when intentionally delivered; routine drafts remain Unreleased.

Never change the contents of an already released version. Keep application version, commit identity and dirty state separate; optional build metadata does not change version precedence. Beta numbering is a project release policy consistent with [Semantic Versioning](https://semver.org/spec/v2.0.0.html), rather than a rule that SemVer requires for every commit. The intended stable CLI contracts include argv parsing, text/JSON output, exit codes and private-state compatibility. Record breaking beta changes and migration steps explicitly.

## Present existing build and installation evidence

Use the running executable's current embedded version/VCS/OS identity, safe selected-source revision/dirty state, managed PATH executable identity when verifiable, and existing runtime manifest. Keep those values separate. A source declaration is not evidence that a new binary was built or installed. Show the last-built artifact as not recorded when no reliable record exists; do not build an artifact registry merely to populate a UI field.

Preserve the installer's current build validation, atomic replacement, PATH shadowing and Windows replacement behaviour. The shell can explain those steps and invoke the existing installer through deliberate terminal handoff. It does not need a new install transaction, update service or migration framework.

A shell left open during an upgrade can still run the previous build. Report installed versus running identity separately when evidence allows; otherwise mark it unknown. Never execute arbitrary PATH candidates. A selected SDLC source checkout is independent of the active work project and must be labelled as such.

## Present existing runtime and job evidence

Use `runtime.json`'s immutable image ID, source revision, engine identity, build time and tool inventory. `sdlc:local` is a mutable selector, not a version. Report a runtime application version only when it was recorded; otherwise the image/source identity still explains whether source or executable updates have rebuilt the runtime.

Use each job's existing captured runtime/source evidence, with unknown fields labelled. The UI must not reinstall, rebuild, replace a running controller or restart provider work while displaying version differences. Retain existing recovery rules. Optional image application-version labels and per-build artifact manifests can improve the screen in separate increments with focused validation; they do not require a supervisor/control protocol.

## Changelog and delivery workflow

Use [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) conventions: Unreleased plus dated version sections, with Added, Changed, Fixed, Deprecated, Removed or Security categories when relevant. Describe observable behaviour and migration actions in plain language. A Git log can supply evidence but is not the release note. Public entries contain no private tickets, paths, accounts or job transcripts.

For each delivery:

1. Collect completed, validated changes under Unreleased. Keep designs and incomplete features labelled clearly.
2. Select the next beta identifier and update the canonical version and dated changelog section in the same delivery change.
3. Run required offline tests/builds and publication-safety checks; verify the built executable reports the selected version and exact source identity.
4. Commit and push using repository policy. Create a release tag only as an explicitly authorised release action; never imply a tag exists from a source constant alone.
5. Build/install through the supported mechanism and verify the executable's identity. Rebuild the runtime only when its source/recipe requires it; show any intentional mismatch.

The first delivery can establish versioning before the terminal refactor is complete. Group the three recent implemented commits into its documented baseline rather than inventing three retrospective beta releases.

## Acceptance checks

- Build and install a beta, retain an older running shell, and show their different identities correctly.
- Modify source without building; show declared version, dirty source and the last actual artifact separately.
- Install a newer CLI without rebuilding Docker; show runtime drift and the existing runtime repair action.
- Rebuild runtime while an older job is active; preserve and report the job's frozen image/controller identity.
- Reject a shadowed or unmanaged PATH candidate without executing it. Missing build records are shown as not recorded.
- Reuse installer-failure and missing/legacy runtime checks with offline disposable fixtures; add view checks only for new identity presentation.
- Confirm a released beta maps to one immutable source/artifact identity and its changelog excludes unimplemented shell features.
