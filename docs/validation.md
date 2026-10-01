# Validation record

Checked on **1 October 2026** using macOS/arm64, OrbStack Docker Engine 29.4.0 and Compose 5.1.2. Only disposable generated keys and a fake token were supplied. No user credentials, Codex login, model request or GitHub write was used.

## Passed

- Docker image build on Linux/arm64: Codex CLI 0.159.2 and `gh` 2.81.0 installed successfully. The GitHub CLI release archive passed its published checksum check.
- Compose configuration rendered with loopback-only SSH, named state volumes, all capabilities dropped except the explicit bootstrap/SSH set, and credential secrets outside the service environment.
- Ten offline checks passed on the host and inside the container: expected-key signatures, multiple proposed commits, rejection of an unsigned earlier commit despite a signed HEAD, wrong key, different committer, base-branch publication, encrypted key without prompting, profile allowlist, invalid branch, private-file permissions and print-only publishing. Several assertions share one test method.
- Actual container commit signing using `/run/secrets/signing-key` and the generated allowed-signers file; `git verify-commit` succeeded.
- Git's actual HTTPS credential-helper path returned the supplied fake token without a stored `gh` account or network lookup.
- Tools ran as uid 1000; `SSH_AUTH_SOCK` was absent.
- SSH authentication with a separate control key and a verified generated server key. Both ordinary remote Bash commands and explicit login shells received `GH_TOKEN`, `CODEX_HOME` and Git signing settings. An SSH command verified the test commit and read the result helper.
- Temporary test containers, volumes and network were removed. The built `sdlc-codex-spike:0.159.2` image remains available locally.
- The updated host-only offline suite passed 84 tests, including dependency pin validation and atomic updates, build overrides, dependency merge policy, the runtime PR guard, model selection and argument forwarding, per-container model defaults, failure boundaries for the connected runner, independent marker acceptance before publication, and remote branch/PR matching with synthetic GitHub responses. These checks make no Docker, model or GitHub requests.
- The pinned public catalogue installers passed in an isolated temporary directory: 13 skills from skills commit `21801216eea73e9b75a2a6ac23e0df62b3cd6b8a` and five agents from agents commit `1965dbdea022f2c7bd2a7cec159d982dde59ae3e`. Both installers' `--check` modes passed and supporting file links resolved.
- Compose rendered the new defaults and explicit catalogue revision overrides, with both selected and omitted model values. The Docker bootstrap shell passed a syntax check. These checks did not need daemon access.
- An isolated Codex CLI 0.159.2 app-server probe discovered all 13 installed skills with zero skill errors through `skills/list`, and read its configuration without malformed-agent warnings. It used temporary state and made no model request. The pinned CLI source also confirms discovery of symlinked agent definitions beside an empty system configuration.
- The dependency workflows passed actionlint 1.7.12, and the Dependabot/workflow files parsed as YAML. Compose rendered without build-argument pins overriding the Dockerfile, using the `sdlc-codex-spike:local` image tag.
- [GitHub Actions validation of commit `1000faf`](https://github.com/tjpeel/sdlc/actions/runs/36866995864) passed on Ubuntu 24.04/Linux amd64: all 77 offline tests, an uncached image build, and the updated Docker/SSH smoke test. The smoke test verified bundled skills, agents, resource links and model defaults in disposable and SSH containers sharing a state volume. It used generated keys and a fake token.

## Still to test

- Rebuild and run the updated image on the local macOS/arm64 Docker host. GitHub Actions covers Linux/amd64; this session cannot access the local Docker socket.
- Codex authentication and an actual `exec` ticket run, including JSON output, final-response persistence and resuming after interruption.
- Private repository clone, selected-user API authentication, signed branch push, draft PR creation and GitHub's **Verified** display with real dedicated credentials.
- The connected `tests/github_smoke.py` flow: use an existing signed branch or run a fresh ticket, then automatically publish and verify the remote branch and draft PR from inside the container. It is print-only unless `--execute` is supplied; rebuild the image before running it.
- Two real GitHub profiles and organisation token policy.
- Codex macOS app and T3 Code connections, UI-created signed commits, full-access thread permissions and client disconnect/reconnect behaviour.
- WebSocket app-server transport (proposal only), task-specific language tools, credential expiry and reliable ticket-batch recovery.

## Reproduce

```sh
python3 -m unittest discover -s tests -v
docker build --pull --no-cache --tag sdlc-codex-spike:local runtime
python3 tests/docker_smoke.py
```

`docker_smoke.py` needs Docker access. It generates temporary signing/control keys, uses a fake token, creates a uniquely named test project, and removes only that project's containers, volumes and network. It does not authenticate Codex or call GitHub. Image downloads during the build require network access.
