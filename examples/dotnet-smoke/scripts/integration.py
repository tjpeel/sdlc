#!/usr/bin/env python3
"""Run disposable Compose API, Mongo and tests on SDLC's check daemon."""

import os
from pathlib import Path
import signal
import subprocess
import sys
import uuid


DOCKER_SOCKET = "unix:///run/sdlc/docker.sock"


def docker(*args, check=True, timeout=600):
    # Pin work, diagnostics and cleanup despite inherited context overrides.
    environment = os.environ.copy()
    environment.pop("DOCKER_CONTEXT", None)
    return subprocess.run(["docker", "--host", DOCKER_SOCKET, *args],
                          check=check, timeout=timeout, env=environment)


def interrupted(signum, frame):
    raise KeyboardInterrupt


def main():
    if os.environ.get("DOCKER_HOST") != DOCKER_SOCKET:
        sys.exit("DOCKER_HOST must be unix:///run/sdlc/docker.sock from a dedicated SDLC check daemon.")
    root = Path(__file__).resolve().parent.parent
    environment_file = root / ".env"
    if environment_file.is_symlink() or not environment_file.is_file():
        sys.exit("Integration checks require a regular, non-symlink root .env supplied as a check input.")
    signal.signal(signal.SIGTERM, interrupted)
    project = "dotnet-smoke-" + uuid.uuid4().hex
    compose = ["compose", "--project-name", project, "--project-directory", str(root),
               "--env-file", str(environment_file), "--file", str(root / "compose.yml")]
    result = 0
    try:
        docker("info")
        docker(*compose, "build", "api", "tests", "host-tests")
        docker(*compose, "up", "-d", "mongo", "api")
        docker(*compose, "exec", "-T", "api", "/bin/sh", "-c",
               "test ! -e /source/.env && test ! -e /app/.env")
        docker(*compose, "run", "--rm", "--no-deps", "tests")
        docker(*compose, "run", "--rm", "--no-deps", "host-tests")
    except (subprocess.SubprocessError, KeyboardInterrupt):
        # Never print rendered Compose configuration or environment values.
        print("Integration check failed or was cancelled.", file=sys.stderr)
        result = 1
        try:
            docker(*compose, "logs", "--no-color", "--tail", "100", "mongo", "api",
                   check=False, timeout=15)
        except subprocess.SubprocessError:
            pass
    finally:
        # Compose owns the namespace, including resources created before a
        # cancelled client returns. Local build tags and Mongo volumes go too.
        try:
            completed = docker(*compose, "--profile", "integration", "down", "--volumes", "--remove-orphans", "--rmi", "local",
                               check=False, timeout=60)
            if completed.returncode:
                print("Integration Compose cleanup failed.", file=sys.stderr)
                result = 1
        except subprocess.SubprocessError:
            print("Integration Compose cleanup failed.", file=sys.stderr)
            result = 1
    return result


if __name__ == "__main__":
    sys.exit(main())
