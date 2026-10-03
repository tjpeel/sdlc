#!/usr/bin/env bash
# Run only in a fresh Linux VM whose Mac filesystem sharing is disabled.
set -euo pipefail

fixture_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
source_root=$(cd -- "$fixture_dir/../../.." && pwd)
mode=${1:---check}
[[ $# == 0 ]] || shift
control_id=""
while [[ $# -gt 0 ]]; do
  [[ "$1" == --host-control && $# -ge 2 ]] || { printf 'Unknown fixture argument.\n' >&2; exit 2; }
  control_id=$2
  shift 2
done
[[ "$mode" == --check || "$mode" == --execute ]] || { printf 'Use --check or --execute.\n' >&2; exit 2; }
[[ -z "$control_id" || "$control_id" =~ ^host-control-[0-9a-f]{12}$ ]] || exit 2
for file in research/container-spike/sdlc.py runtime/Dockerfile tests/container_ticket_smoke.py tests/nested_docker_smoke.py \
  research/spikes/colima/full_fixture.py .github/dependency-files-filter.jq \
  .github/dependency-pr-filter.jq .github/workflows/update-runtime-pins.yml; do
  [[ -f "$source_root/$file" ]] || { printf 'Missing public source: %s\n' "$file" >&2; exit 1; }
done
python3 - "$fixture_dir/full_fixture.py" <<'PY'
import ast, pathlib, sys
ast.parse(pathlib.Path(sys.argv[1]).read_text())
PY
if [[ "$mode" == --check ]]; then
  printf 'Full-worker fixture inputs and Python syntax passed. No Docker commands were run.\n'
  exit 0
fi
[[ $(uname -s) == Linux ]] || { printf 'Execute inside the disposable Linux VM.\n' >&2; exit 1; }
for command in docker python3 tar timeout od tr ssh-keygen; do
  command -v "$command" >/dev/null || { printf 'Missing guest command: %s\n' "$command" >&2; exit 1; }
done
[[ -S /var/run/docker.sock ]] || { printf 'The guest Docker socket is missing.\n' >&2; exit 1; }

# Ignore Docker context/environment selection; only use this guest's daemon.
outer() { timeout 300 docker --host unix:///var/run/docker.sock "$@"; }
run_id=$(od -An -N8 -tx1 /dev/urandom | tr -d ' \n')
image="sdlc-colima-full-worker:$run_id"
temporary=$(mktemp -d /tmp/sdlc-colima-full.XXXXXXXX)
containers=()
volumes=()
image_created=false
cleanup() {
  local result=$?
  trap - EXIT INT TERM
  for container in "${containers[@]}"; do
    if ! timeout 45 docker --host unix:///var/run/docker.sock rm --force --volumes "$container" >/dev/null; then
      printf 'Could not remove owned container: %s\n' "$container" >&2; result=1
    fi
  done
  for volume in "${volumes[@]}"; do
    if ! outer volume rm "$volume" >/dev/null; then
      printf 'Could not remove owned volume: %s\n' "$volume" >&2; result=1
    fi
  done
  if [[ "$image_created" == true ]]; then
    outer image rm "$image" >/dev/null || result=1
  fi
  # Generated snapshots have read-only directories. Restore operator write
  # access before removing the private guest fixture directory.
  chmod -R u+rwX -- "$temporary" || result=1
  rm -rf -- "$temporary" || result=1
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

printf 'Building the actual worker image with runtime/ as its entire build context.\n'
timeout 2400 docker --host unix:///var/run/docker.sock build --tag "$image" "$source_root/runtime"
image_created=true
outer pull docker:29.8.2-dind
ssh-keygen -q -t ed25519 -N '' -f "$temporary/signing-key"

for implementation in codex claude; do
  [[ "$implementation" == codex ]] && review=claude || review=codex
  identity=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')
  job="$temporary/$identity"
  worker="sdlc-colima-full-worker-$identity"
  engine="sdlc-colima-full-engine-$identity"
  workspace="sdlc-colima-full-workspace-$identity"
  socket="sdlc-colima-full-socket-$identity"
  data="sdlc-colima-full-data-$identity"
  control_args=()
  control_labels=()
  control_environment=""
  if [[ -n "$control_id" && "$implementation" == codex ]]; then
    control_args=(--ticket-text-file "$source_root/research/spikes/host-control/protected-input.txt")
    control_labels=(--label "sdlc.host-control.trial=$control_id")
    control_environment=$control_id
  fi
  python3 "$fixture_dir/full_fixture.py" --prepare "$job" --identity "$identity" \
    --implementation "$implementation" --review "$review" "${control_args[@]}"
  for volume in "$workspace" "$socket" "$data"; do
    outer volume create --label "sdlc.colima-full-trial=$run_id" "$volume" >/dev/null
    volumes+=("$volume")
  done
  outer run --detach --name "$engine" --label "sdlc.colima-full-trial=$run_id" \
    --privileged --mount "type=volume,src=$workspace,dst=/workspace" \
    --mount "type=volume,src=$socket,dst=/run/job-docker" \
    --mount "type=volume,src=$data,dst=/var/lib/docker" \
    --entrypoint /bin/sh docker:29.8.2-dind -ec \
    'addgroup -g 1000 job-docker; exec dockerd --host=unix:///run/job-docker/docker.sock --group=job-docker' >/dev/null
  containers+=("$engine")
  ready=false
  for ((attempt=0; attempt<60; attempt++)); do
    if timeout 5 docker --host unix:///var/run/docker.sock exec "$engine" \
      docker --host unix:///run/job-docker/docker.sock info >/dev/null 2>&1; then
      ready=true; break
    fi
    sleep 1
  done
  [[ "$ready" == true ]] || { outer logs --tail 50 "$engine" >&2; exit 1; }
  outer exec "$engine" sh -ec \
    'awk '\''$2 ~ /:(0947|0948)$/ && $4 == "0A" { found=1 } END { exit found ? 1 : 0 }'\'' /proc/net/tcp /proc/net/tcp6'
  test_argument=()
  [[ "$implementation" == codex ]] && test_argument=(--run-tests)
  outer create --name "$worker" --label "sdlc.colima-full-trial=$run_id" --init \
    "${control_labels[@]}" \
    --network "container:$engine" --security-opt no-new-privileges:true \
    --cap-drop ALL --cap-add CHOWN --cap-add DAC_OVERRIDE --cap-add SETUID --cap-add SETGID \
    --mount "type=volume,src=$workspace,dst=/workspace" \
    --mount "type=volume,src=$socket,dst=/run/job-docker" \
    --tmpfs /run/sdlc:rw,noexec,nosuid,mode=0700 \
    --tmpfs /run/secrets:rw,noexec,nosuid,size=1m,mode=0700 \
    --env SDLC_REPOSITORY=example/offline-test --env SDLC_GITHUB_LOGIN=example \
    --env 'SDLC_GIT_NAME=Offline Fixture' --env SDLC_GIT_EMAIL=offline@example.invalid \
    --env SDLC_BASE_BRANCH=main --env "SDLC_JOB_ID=$identity" \
    --env "SDLC_HOST_CONTROL_TRIAL=$control_environment" \
    --env DOCKER_HOST=unix:///run/job-docker/docker.sock --env DOCKER_CONFIG=/run/sdlc/docker \
    --entrypoint /bin/sh "$image" -ec \
    'while [ ! -f /run/secrets/ready ]; do sleep 1; done; exec /usr/local/bin/sdlc-entrypoint "$@"' \
    -- python3 /trial/research/spikes/colima/full_fixture.py --inside "${test_argument[@]}" >/dev/null
  containers+=("$worker")
  # Copy an explicit public source allowlist. No repo/.git/auth/ticket directories
  # from the Mac are included. The generated ticket is transferred separately.
  outer cp "$job/job" "$worker:/input-job"
  outer start "$worker" >/dev/null
  outer exec "$worker" mkdir /trial
  outer cp - "$worker:/trial" < <(tar -C "$source_root" -cf - runtime scripts tests research/container-spike/sdlc.py \
    .github/dependency-files-filter.jq .github/dependency-pr-filter.jq \
    .github/workflows/update-runtime-pins.yml research/spikes/colima/full_fixture.py)
  outer exec "$worker" sh -ec \
    'mkdir /input; mv /input-job /input/job; chown -R 0:0 /input/job; chmod -R a-w /input/job'
  cat "$temporary/signing-key" | outer exec --interactive "$worker" sh -ec \
    'umask 077; cat > /run/secrets/signing-key; chmod 0400 /run/secrets/signing-key; chown 1000:1000 /run/secrets/signing-key'
  printf 'fake-colima-full-%s' "$identity" | outer exec --interactive "$worker" sh -ec \
    'umask 077; cat > /run/secrets/github-token; chmod 0400 /run/secrets/github-token; chown 1000:1000 /run/secrets/github-token'
  # Establish that neither outer container binds a guest path or exposes the
  # guest Docker socket. Only named workspace/socket/data volumes are shared.
  outer inspect "$worker" "$engine" | python3 -c '
import json, sys
items = json.load(sys.stdin)
worker, engine = items
assert worker["HostConfig"]["Privileged"] is False
assert engine["HostConfig"]["Privileged"] is True
assert worker["HostConfig"]["NetworkMode"] in (
    "container:" + engine["Id"], "container:" + engine["Name"].lstrip("/"))
for item in items:
    assert not item["HostConfig"]["Binds"]
    assert all(mount["Type"] in ("volume", "tmpfs") for mount in item["Mounts"])
    assert all(mount["Destination"] != "/var/run/docker.sock" for mount in item["Mounts"])
print("Worker and DinD have no guest bind mounts or guest Docker socket; only DinD is privileged.")'
  outer exec "$worker" sh -ec 'chown 1000:1000 /run/secrets; touch /run/secrets/ready'
  printf 'Running actual worker: %s implementation, %s review (simulated responses).\n' "$implementation" "$review"
  if [[ -n "$control_id" && "$implementation" == codex ]]; then
    gate="/tmp/$control_id"
    mkdir -m 0700 "$gate"
    admission_ready=false
    for ((attempt=0; attempt<240; attempt++)); do
      if outer exec "$worker" test -f /run/sdlc/host-control-admission.json; then
        admission_ready=true; break
      fi
      [[ $(outer inspect --format '{{.State.Running}}' "$worker") == true ]] || { outer logs "$worker"; exit 1; }
      sleep 1
    done
    [[ "$admission_ready" == true ]] || exit 1
    outer exec "$worker" cat /run/sdlc/host-control-admission.json > "$gate/admission.json"
    outer inspect "$worker" > "$gate/worker.json"
    python3 - "$gate" <<'PY'
import json, pathlib, sys
folder = pathlib.Path(sys.argv[1])
value = json.loads((folder / 'admission.json').read_text())
worker = json.loads((folder / 'worker.json').read_text())[0]
value['worker_id'] = worker['Id']
assert worker['Config']['Labels']['sdlc.host-control.trial'] == value['trial_id']
(folder / 'ready.json').write_text(json.dumps(value) + '\n')
PY
    while [[ ! -f "$gate/release" ]]; do
      [[ $(outer inspect --format '{{.State.Running}}' "$worker") == true ]] || { outer logs "$worker"; exit 1; }
      sleep 1
    done
    outer exec "$worker" touch /run/sdlc/host-control-release
  fi
  code=$(timeout 1200 docker --host unix:///var/run/docker.sock wait "$worker")
  outer logs "$worker"
  [[ "$code" == 0 ]] || { printf 'The worker exited with status %s.\n' "$code" >&2; exit 1; }
  outer rm --volumes "$worker" >/dev/null
  outer rm --force --volumes "$engine" >/dev/null
  containers=()
done
printf 'Full Colima worker trial passed in both provider orders. No model request, authenticated GitHub API call or GitHub write was made.\n'
printf 'Removing the generated key, worker image and owned Docker resources.\n'
