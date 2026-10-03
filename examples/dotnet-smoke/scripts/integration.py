#!/usr/bin/env python3
"""Run disposable API, Mongo and test containers on SDLC's check daemon."""

import os
from pathlib import Path
import signal
import subprocess
import sys
import uuid


DOCKER_SOCKET = "unix:///run/sdlc/docker.sock"


def docker(*args, check=True, timeout=600):
    # An inherited context can override DOCKER_HOST. Pin every command, including
    # diagnostics and cleanup, to the disposable check daemon.
    environment = os.environ.copy()
    environment.pop("DOCKER_CONTEXT", None)
    return subprocess.run(["docker", "--host", DOCKER_SOCKET, *args],
                          check=check, timeout=timeout, env=environment)


def run(*args):
    docker(*args)


def interrupted(signum, frame):
    raise KeyboardInterrupt


def main():
    # SDLC supplies this private socket only to --docker-tests check workers.
    if os.environ.get("DOCKER_HOST") != DOCKER_SOCKET:
        sys.exit("DOCKER_HOST must be unix:///run/sdlc/docker.sock from a dedicated SDLC check daemon.")
    signal.signal(signal.SIGTERM, interrupted)
    prefix = "dotnet-smoke-" + uuid.uuid4().hex
    network = prefix + "-network"
    api_image, tests_image = prefix + "-api:local", prefix + "-tests:local"
    containers = [prefix + "-mongo", prefix + "-api", prefix + "-tests"]
    cleanup = []
    result = 0
    try:
        run("info")
        context = str(Path(__file__).resolve().parent.parent)
        for target, image in [("api", api_image), ("tests", tests_image)]:
            cleanup.append(["image", "rm", "--force", image])
            run("build", "--target", target, "--tag", image, context)
        # Creation can succeed on the daemon before a client timeout or signal.
        cleanup.append(["network", "rm", network])
        run("network", "create", network)
        for container in containers:
            cleanup.append(["rm", "--force", "--volumes", container])
        run("run", "--detach", "--name", containers[0], "--network", network,
            "--network-alias", "mongo", "mongo:8.0.16")
        run("run", "--detach", "--name", containers[1], "--network", network,
            "--network-alias", "api", "--env",
            "Mongo__ConnectionString=mongodb://mongo:27017/?serverSelectionTimeoutMS=2000",
            api_image)
        run("run", "--name", containers[2], "--network", network,
            "--env", "SMOKE_API_URL=http://api:8080", tests_image)
    except (subprocess.SubprocessError, KeyboardInterrupt) as error:
        print(f"Integration check failed: {error}", file=sys.stderr)
        result = 1
        for container in containers[:2]:
            try:
                docker("logs", container, check=False, timeout=15)
            except subprocess.SubprocessError:
                pass
    finally:
        for command in reversed(cleanup):
            try:
                completed = docker(*command, check=False, timeout=30)
                if completed.returncode:
                    print(f"Cleanup failed for {command[-1]}", file=sys.stderr)
                    result = 1
            except subprocess.SubprocessError as error:
                print(f"Cleanup failed for {command[-1]}: {error}", file=sys.stderr)
                result = 1
    return result


if __name__ == "__main__":
    sys.exit(main())
