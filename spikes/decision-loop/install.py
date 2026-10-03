#!/usr/bin/env python3
"""Install only the public, offline decision-loop spike into a new prefix."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import stat
import sys

SOURCE_ROOT = Path(__file__).resolve().parents[2]
FILES = ("spikes/decision-loop/sdlc.py", "runtime/bin/ticket_input.py")


def checked_path(value):
    path = Path(value)
    if not path.is_absolute() or ".." in path.parts or any(c in str(path) for c in "\x00\r\n"):
        raise ValueError("Use an absolute prefix without traversal or control characters.")
    for ancestor in (path, *path.parents):
        try:
            if stat.S_ISLNK(ancestor.lstat().st_mode):
                raise ValueError("Symbolic links are not accepted in the prefix path.")
        except FileNotFoundError:
            pass
    return path


def install(prefix):
    prefix = checked_path(prefix)
    if prefix == SOURCE_ROOT or SOURCE_ROOT in prefix.parents:
        raise ValueError("Install outside the source checkout.")
    for ancestor in (prefix.parent, *prefix.parent.parents):
        if (ancestor / ".git").exists():
            raise ValueError("Install outside repository checkouts.")
    if not prefix.parent.is_dir():
        raise ValueError("The prefix parent directory must already exist.")

    # Read a fixed allowlist before writing anything. Do not copy the checkout,
    # environment, configuration, credentials, tickets or generated state.
    sources = {}
    for relative in FILES:
        source = checked_path(SOURCE_ROOT / relative)
        descriptor = os.open(source, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        try:
            info = os.fstat(descriptor)
            if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or info.st_size > 1024 * 1024:
                raise ValueError("Package sources must be bounded regular files without hard links.")
            with os.fdopen(descriptor, "rb", closefd=False) as stream:
                sources[relative] = stream.read(1024 * 1024 + 1)
            if len(sources[relative]) > 1024 * 1024:
                raise ValueError("Package source is too large.")
        finally:
            os.close(descriptor)

    # mkdir is deliberately exclusive: never replace an existing installation.
    prefix.mkdir(mode=0o700)
    try:
        package = prefix / "share" / "sdlc-decision-spike"
        package.mkdir(mode=0o700, parents=True)
        (prefix / "share").chmod(0o700)
        hashes = {}
        for relative, content in sources.items():
            destination = package / relative
            destination.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
            for ancestor in destination.parent.parents:
                if ancestor == prefix:
                    break
                ancestor.chmod(0o700)
            with destination.open("xb") as stream:
                stream.write(content)
            destination.chmod(0o600)
            hashes[relative] = hashlib.sha256(content).hexdigest()
        entry = prefix / "bin" / "sdlc"
        entry.parent.mkdir(mode=0o700)
        launcher = (
            "#!/usr/bin/env python3\n"
            '"""SDLC DECISION LOOP SIMULATION: no providers or Docker are invoked."""\n'
            "import os\nfrom pathlib import Path\nimport sys\n"
            'script = Path(__file__).resolve().parents[1] / "share/sdlc-decision-spike/spikes/decision-loop/sdlc.py"\n'
            'os.execv(sys.executable, [sys.executable, "-B", str(script), *sys.argv[1:]])\n'
        )
        with entry.open("x", encoding="utf-8") as stream:
            stream.write(launcher)
        entry.chmod(0o700)
        manifest = prefix / "package.json"
        manifest.write_text(json.dumps({"simulation": True, "files_sha256": hashes}, indent=2) + "\n")
        manifest.chmod(0o600)
    except BaseException:
        shutil.rmtree(prefix)
        raise
    return entry


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--prefix", required=True, help="new external directory; parent must exist")
    args = parser.parse_args()
    try:
        entry = install(args.prefix)
    except (OSError, ValueError) as error:
        parser.exit(2, f"Installation refused: {error}\n")
    print(f"SDLC DECISION LOOP SIMULATION installed: {entry}")
    print("No runtime, credentials or global commands were installed.")


if __name__ == "__main__":
    main()
