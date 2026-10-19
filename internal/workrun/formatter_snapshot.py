"""Capture pending C# edits without changing HEAD, the index or published refs."""
import json, os, pathlib, stat, subprocess, tempfile
export = pathlib.Path('/export')
try:
    root = pathlib.Path('/workspace')
    export = pathlib.Path('/export')
    if not stat.S_ISDIR((root / '.git').lstat().st_mode):
        raise ValueError('invalid repository')
    env = {'PATH': '/usr/local/bin:/usr/bin:/bin', 'HOME': '/tmp', 'GIT_CONFIG_NOSYSTEM': '1', 'GIT_CONFIG_GLOBAL': '/dev/null', 'GIT_TERMINAL_PROMPT': '0', 'GIT_NO_REPLACE_OBJECTS': '1', 'GIT_OPTIONAL_LOCKS': '0'}
    def git(*args, data=None, cwd=root, selected_env=None):
        return subprocess.run(['git', '-c', 'safe.directory=*', '-c', 'core.hooksPath=/dev/null', '-c', 'core.fsmonitor=false', *args], cwd=cwd, env=selected_env or env, input=data, check=True, capture_output=True).stdout
    head = git('rev-parse', 'HEAD').decode().strip()
    # Source is mounted read-only. All newly created Git objects belong to the
    # private export; original objects are read only as alternates during capture.
    destination = export / 'workspace'
    destination.mkdir()
    git('init', '-q', cwd=destination)
    clean_env = dict(env)
    env = dict(env, GIT_OBJECT_DIRECTORY=str(destination / '.git' / 'objects'), GIT_ALTERNATE_OBJECT_DIRECTORIES=str(root / '.git' / 'objects'))
    with tempfile.TemporaryDirectory() as temporary:
        pending_env = dict(env, GIT_INDEX_FILE=temporary + '/index')
        git('read-tree', head, selected_env=pending_env)
        names = set(git('diff', '--no-ext-diff', '--no-textconv', '--no-renames', '--name-only', '-z', head, '--').split(b'\0'))
        names.update(git('ls-files', '--others', '--exclude-standard', '-z').split(b'\0'))
        for raw in sorted(names):
            name = raw.decode('utf-8')
            path = pathlib.PurePosixPath(name)
            config = name == '.config/dotnet-tools.json' or path.name in ('.editorconfig', '.csharpierrc', '.csharpierrc.json', '.csharpierrc.yaml', '.csharpierrc.yml', '.csharpierignore', 'global.json')
            if (path.suffix != '.cs' and not config) or any(p in ('.git', '.sdlc', '..') for p in path.parts) or path.is_absolute():
                continue
            file = root / name
            try:
                mode = file.lstat().st_mode
            except FileNotFoundError:
                git('update-index', '--force-remove', '--', name, selected_env=pending_env)
                continue
            for parent in path.parents:
                if parent != pathlib.PurePosixPath('.') and not stat.S_ISDIR((root / parent).lstat().st_mode):
                    raise ValueError('unsafe source parent')
            if not stat.S_ISREG(mode):
                raise ValueError('formatter source must be regular')
            fd = os.open(file, os.O_RDONLY | os.O_NOFOLLOW)
            with os.fdopen(fd, 'rb') as stream:
                content = stream.read()
            write_env = dict(env)
            write_env.pop('GIT_ALTERNATE_OBJECT_DIRECTORIES', None)
            blob = git('hash-object', '-w', '--stdin', data=content, selected_env=write_env).decode().strip()
            git('update-index', '--add', '--cacheinfo', '100755' if mode & 0o111 else '100644', blob, name, selected_env=pending_env)
        tree = git('write-tree', selected_env=pending_env).decode().strip()
        stamp = git('show', '-s', '--format=%ct', head).decode().strip() + ' +0000'
        author = dict(env, GIT_AUTHOR_NAME='Formatter snapshot', GIT_AUTHOR_EMAIL='formatter@example.invalid', GIT_COMMITTER_NAME='Formatter snapshot', GIT_COMMITTER_EMAIL='formatter@example.invalid', GIT_AUTHOR_DATE=stamp, GIT_COMMITTER_DATE=stamp)
        snapshot = git('-c', 'commit.gpgsign=false', 'commit-tree', tree, '-p', head, '-m', 'Private formatter snapshot', selected_env=author).decode().strip()
        # Materialise parent history without an alternates file or source config.
        git('-c', 'protocol.file.allow=always', 'fetch', '--no-tags', str(root), head, cwd=destination, selected_env=clean_env)
        git('checkout', '-q', '--detach', snapshot, cwd=destination, selected_env=clean_env)
        if git('rev-parse', 'HEAD').decode().strip() != head:
            raise ValueError('candidate moved during capture')
        print(json.dumps({'head': snapshot, 'tree': tree, 'clean': True}))
finally:
    destination = export / 'workspace'
    if destination.is_dir():
        for directory, children, files in os.walk(destination):
            os.chmod(directory, 0o777)
