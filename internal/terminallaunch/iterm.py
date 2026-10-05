"""Background tab bridge using the official iTerm2 Python API.

API: https://iterm2.com/python-api/window.html#iterm2.Window.async_create_tab
No activate/select/write-text calls, shell work arguments, or stored credentials.
The caller must already have an authenticated official scripting environment.
"""
import json
import os
import sys
import stat

if not os.environ.get("ITERM2_COOKIE") or not os.environ.get("ITERM2_KEY"):
    sys.exit(42)
# The official library can request renewed authorization via AppleScript. Its
# PyObjC path runs outside Python's subprocess audit events, so fail closed when
# that route is installed. Source checked alongside the documented API:
# https://github.com/gnachman/iTerm2/blob/master/api/library/python/iterm2/iterm2/auth.py
if "AppKit" in sys.modules or "Foundation" in sys.modules:
    sys.exit(42)


def forbid_process_launch(event, args):
    if event == "import" and args[0] in ("AppKit", "Foundation"):
        raise ImportError("AppleScript authorization imports disabled during launch")
    if event in ("subprocess.Popen", "os.system", "os.posix_spawn", "os.exec"):
        raise PermissionError("authorization subprocesses are disabled during launch")


# Guard rather than patch the official client. A stale credential fails before
# osascript starts, so authorization must be completed separately from Start.
sys.addaudithook(forbid_process_launch)
try:
    import iterm2
except ImportError:
    sys.exit(42)

ack_path = None
if len(sys.argv) == 2:
    payload_path = os.path.abspath(sys.argv[1])
    directory = os.path.dirname(os.path.abspath(__file__))
    if os.path.dirname(payload_path) != directory:
        sys.exit(42)
    info = os.lstat(payload_path)
    if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077 or info.st_nlink != 1 or info.st_size > 65536:
        sys.exit(42)
    with open(payload_path) as stream:
        if os.fstat(stream.fileno()).st_ino != info.st_ino:
            sys.exit(42)
        payload = json.load(stream)
    ack_path = os.path.splitext(payload_path)[0] + ".ack"
else:
    payload = json.load(sys.stdin)


def acknowledge(mode):
    if ack_path is None:
        print("background-tab-created")
    else:
        fd = os.open(ack_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "w") as stream:
            stream.write(mode + "-ready")
# ITERM_SESSION_ID prefixes the native session UUID with the pane coordinates.
session_id = payload["session_id"].split(":")[-1]


async def main(connection):
    app = await iterm2.async_get_app(connection)
    for window in app.windows:
        for tab in window.tabs:
            for session in tab.sessions:
                if session.session_id == session_id:
                    if payload.get("mode") == "setup":
                        acknowledge("setup")
                        return
                    profile = await session.async_get_profile()
                    created = await window.async_create_tab(
                        profile=profile.name, command=payload["command"], select=False
                    )
                    if created is None:
                        raise RuntimeError("background tab closed before acknowledgement")
                    acknowledge("launch")
                    return
    sys.exit(42)


try:
    iterm2.run_until_complete(main, retry=False)
except PermissionError:
    sys.exit(42)
