#!/usr/bin/env bash
# Run inside a disposable Linux VM. This script never uses a Mac Docker context.
set -euo pipefail

fixture_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
mode=${1:---check}
if [[ $# -gt 1 || "$mode" != --check && "$mode" != --execute ]]; then
  printf 'Usage: bash trial.sh [--check|--execute]\n' >&2
  exit 2
fi

for file in inner-trial.sh compose.yaml http/Dockerfile http/index.html app/Trial.csproj app/Program.cs app/NuGet.Config; do
  [[ -f "$fixture_dir/$file" ]] || { printf 'Missing fixture file: %s\n' "$file" >&2; exit 1; }
done
sh -n "$fixture_dir/inner-trial.sh"
if [[ "$mode" == --check ]]; then
  printf 'Fixture files and inner shell syntax passed. No Docker commands were run.\n'
  exit 0
fi

[[ $(uname -s) == Linux ]] || { printf 'Execute this trial inside the disposable Linux VM.\n' >&2; exit 1; }
for command in docker tar timeout od tr; do
  command -v "$command" >/dev/null || { printf 'Required guest command missing: %s\n' "$command" >&2; exit 1; }
done
[[ -S /var/run/docker.sock ]] || { printf 'The guest Docker socket is missing.\n' >&2; exit 1; }

# Explicitly address the guest engine. Environment settings cannot select the
# operator's ordinary Docker context or another daemon.
outer() { timeout 300 docker --host unix:///var/run/docker.sock "$@"; }
run_id=$(od -An -N8 -tx1 /dev/urandom | tr -d ' \n')
engine="sdlc-colima-dind-$run_id"
dind_image=${SDLC_TRIAL_DIND_IMAGE:-docker:29.4.0-dind}
started=false

cleanup() {
  local result=$?
  trap - EXIT INT TERM
  if [[ "$started" == true ]]; then
    if ! timeout 45 docker --host unix:///var/run/docker.sock rm --force --volumes "$engine" >/dev/null; then
      printf 'Cleanup failed for the owned container: %s\n' "$engine" >&2
      result=1
    fi
  fi
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

printf 'Pulling the disposable DinD image inside the guest.\n'
outer pull "$dind_image"
started=true
outer run --detach --name "$engine" --label sdlc.colima-trial="$run_id" \
  --privileged --network bridge \
  --tmpfs /run/trial-secrets:rw,noexec,nosuid,size=1m \
  --entrypoint dockerd \
  "$dind_image" --host=unix:///var/run/docker.sock >/dev/null

# docker:dind declares an internal data volume. No guest paths or outer Docker
# socket are bound into it; rm --volumes removes that anonymous data volume.
binds=$(outer inspect --format '{{json .HostConfig.Binds}}' "$engine")
[[ "$binds" == null || "$binds" == '[]' ]] || { printf 'Unexpected guest bind mount.\n' >&2; exit 1; }
mounts=$(outer inspect --format '{{range .Mounts}}{{printf "%s %s\n" .Type .Destination}}{{end}}' "$engine")
while read -r kind destination; do
  [[ -z "$kind" ]] && continue
  case "$kind:$destination" in
    volume:/var/lib/docker|tmpfs:/run/trial-secrets) ;;
    *) printf 'Unexpected DinD mount: %s %s\n' "$kind" "$destination" >&2; exit 1 ;;
  esac
done <<< "$mounts"

ready=false
for ((attempt=0; attempt<60; attempt++)); do
  if timeout 5 docker --host unix:///var/run/docker.sock exec \
    --env DOCKER_HOST=unix:///var/run/docker.sock "$engine" docker info >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 1
done
[[ "$ready" == true ]] || { outer logs --tail 50 "$engine" >&2; printf 'The nested daemon did not become ready.\n' >&2; exit 1; }
[[ $(outer inspect --format '{{json .Config.Entrypoint}}' "$engine") == '["dockerd"]' ]]
[[ $(outer inspect --format '{{json .Config.Cmd}}' "$engine") == '["--host=unix:///var/run/docker.sock"]' ]]
# Bypass the stock DinD entrypoint so it cannot add a TCP Docker API listener.
# Check the live network namespace too: 0947/0948 are ports 2375/2376 in hex.
outer exec "$engine" sh -ec \
  'awk '\''$2 ~ /:(0947|0948)$/ && $4 == "0A" { found=1 } END { exit found ? 1 : 0 }'\'' /proc/net/tcp /proc/net/tcp6'
printf 'The nested daemon is ready on its Unix socket, with no Docker API TCP listener.\n'

outer exec "$engine" mkdir -p /trial
tar -C "$fixture_dir" -cf - inner-trial.sh compose.yaml http app | outer cp - "$engine:/trial"

# Generate a fake token here, after the image is pulled. It is piped into tmpfs,
# never supplied in a build context, command argument, or container environment.
fake_token="trial-only-$(od -An -N24 -tx1 /dev/urandom | tr -d ' \n')"
printf '%s' "$fake_token" | outer exec --interactive "$engine" sh -c \
  'umask 077; cat > /run/trial-secrets/github-token'
unset fake_token

printf 'Running nested Docker, Compose, copied-source .NET and runtime-secret checks.\n'
timeout 1200 docker --host unix:///var/run/docker.sock exec \
  --env DOCKER_HOST=unix:///var/run/docker.sock \
  --env SDLC_TRIAL_RUN_ID="$run_id" "$engine" sh /trial/inner-trial.sh
printf 'Colima guest trial passed. Removing the owned DinD container and its data.\n'
