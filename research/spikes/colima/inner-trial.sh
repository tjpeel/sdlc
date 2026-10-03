#!/bin/sh
# Runs in the DinD container. This Docker socket belongs to its nested daemon.
set -eu

: "${SDLC_TRIAL_RUN_ID:?The guest launcher must supply a run identifier}"
[ "$DOCKER_HOST" = unix:///var/run/docker.sock ]
[ -f /.dockerenv ]
[ -s /run/trial-secrets/github-token ]
grep -q '^trial-only-' /run/trial-secrets/github-token
[ "$(stat -c %a /run/trial-secrets/github-token)" = 600 ]
grep -q ' /run/trial-secrets tmpfs ' /proc/mounts
export DOCKER_CONFIG=/run/trial-docker-config
mkdir -p "$DOCKER_CONFIG"
chmod 700 "$DOCKER_CONFIG"
docker compose version

project="sdlc-colima-inner-$SDLC_TRIAL_RUN_ID"
sdk_container="$project-sdk"
failure_container="$project-failure"
cd /trial
run() { timeout 300 docker "$@"; }
compose() { run compose --project-name "$project" --file /trial/compose.yaml "$@"; }

cleanup_fixtures() {
  cleanup_result=0
  compose down --volumes --remove-orphans >/dev/null || cleanup_result=1
  for owned_container in "$sdk_container" "$failure_container"; do
    if run inspect "$owned_container" >/dev/null 2>&1; then
      run rm --force --volumes "$owned_container" >/dev/null || cleanup_result=1
    fi
  done
  return "$cleanup_result"
}
finish() {
  result=$?
  trap - EXIT INT TERM
  cleanup_fixtures || result=1
  exit "$result"
}
trap finish EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

run pull busybox:1.37.0
run pull mcr.microsoft.com/dotnet/sdk:10.0
compose up --build --detach --wait --wait-timeout 60 http

# The published port exists only inside the DinD container's network namespace.
[ "$(wget -q -O- http://127.0.0.1:18085/index.html)" = colima-nested-http-ok ]
compose run --rm network-probe
compose run --rm writable-probe
compose run --rm readonly-probe
printf 'Nested Compose localhost, container network and inner volumes passed.\n'

# Source is copied into the stopped SDK container. No filesystem is shared with
# the guest, and restore/build/run has no network once the public SDK is pulled.
run create --name "$sdk_container" --network none \
  --env DOTNET_CLI_TELEMETRY_OPTOUT=1 --env DOTNET_NOLOGO=1 \
  --env DOCKER_CONFIG=/tmp/trial-docker-config \
  mcr.microsoft.com/dotnet/sdk:10.0 sh -ec \
  'dotnet build /source/Trial.csproj --configuration Release --output /out --configfile /source/NuGet.Config && dotnet /out/Trial.dll' >/dev/null
run cp /trial/app "$sdk_container:/source"
run start --attach "$sdk_container" | tee /tmp/trial-dotnet.log
[ "$(run inspect --format '{{.State.ExitCode}}' "$sdk_container")" = 0 ]
grep -qx 'colima-dotnet-10-ok' /tmp/trial-dotnet.log
printf 'Copied source built and ran with .NET 10 and no container network.\n'

# Confirm the runtime token was not baked into the built web image or forwarded
# to an inner container. These checks never print the generated token.
http_container=$(compose ps --quiet http)
http_image=$(run inspect --format '{{.Image}}' "$http_container")
run exec "$http_container" sh -ec 'test ! -e /run/trial-secrets/github-token'
if run image inspect --format '{{json .Config.Env}}' "$http_image" | grep -q trial-only-; then
  printf 'A runtime fake token entered an image environment.\n' >&2
  exit 1
fi
if run history --no-trunc --format '{{.CreatedBy}}' "$http_image" | grep -q trial-only-; then
  printf 'A runtime fake token entered image history.\n' >&2
  exit 1
fi
printf 'Fake credentials were injected into tmpfs at runtime only.\n'

# A failing inner command must also remove its disposable container.
failure_code=0
run run --rm --name "$failure_container" --network none busybox:1.37.0 sh -c 'exit 7' || failure_code=$?
[ "$failure_code" = 7 ]
if run inspect "$failure_container" >/dev/null 2>&1; then
  printf 'The failed probe container remains.\n' >&2
  exit 1
fi

cleanup_fixtures
[ -z "$(run ps --all --quiet --filter "label=com.docker.compose.project=$project")" ]
[ -z "$(run volume ls --quiet --filter "label=com.docker.compose.project=$project")" ]
if run inspect "$sdk_container" >/dev/null 2>&1; then
  printf 'The owned SDK container remains.\n' >&2
  exit 1
fi
printf 'Owned inner containers and volumes were cleaned up, including a failed probe.\n'
trap - EXIT INT TERM
