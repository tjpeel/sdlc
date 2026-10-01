#!/usr/bin/env python3
"""Prepare one container's account, then run tools as the unprivileged user."""
import os
import json
from pathlib import Path
import shlex
import subprocess
import sys


def run(*args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)


def main():
    context = Path('/run/sdlc')
    context.mkdir(parents=True, exist_ok=True)
    context.chmod(0o700)
    os.chown(context, 1000, 1000)
    for directory in ('/workspace', '/home/node/.codex', '/home/node/.t3'):
        os.chown(directory, 1000, 1000)

    env = {
        'HOME': '/home/node', 'CODEX_HOME': '/home/node/.codex',
        'GH_CONFIG_DIR': '/run/sdlc/gh', 'GIT_CONFIG_GLOBAL': '/run/sdlc/gitconfig',
        'GIT_CONFIG_NOSYSTEM': '1', 'GIT_TERMINAL_PROMPT': '0',
        'GH_PROMPT_DISABLED': '1',
    }
    for key in ('SDLC_REPOSITORY', 'SDLC_GITHUB_LOGIN', 'SDLC_GIT_NAME',
                'SDLC_GIT_EMAIL', 'SDLC_BASE_BRANCH'):
        env[key] = os.environ[key]
    os.environ.update(env)
    os.environ.pop('SSH_AUTH_SOCK', None)
    config = context / 'gitconfig'
    config.touch(mode=0o600, exist_ok=True)
    settings = {
        'user.name': env['SDLC_GIT_NAME'], 'user.email': env['SDLC_GIT_EMAIL'],
        'core.hooksPath': '/dev/null',
        'credential.helper': '',
        'credential.https://github.com.helper': '!/usr/local/bin/gh auth git-credential',
    }

    signing_key = Path('/run/secrets/signing-key')
    token_file = Path('/run/secrets/github-token')
    if signing_key.exists() != token_file.exists():
        raise ValueError('Supply both the signing key and GitHub token, or neither.')
    if signing_key.exists():
        # -P '' fails quickly for encrypted keys instead of waiting for a prompt.
        public = run('/usr/bin/ssh-keygen', '-y', '-P', '', '-f', str(signing_key),
                     capture_output=True, text=True).stdout.strip()
        token = token_file.read_text().strip()
        if not token or any(c.isspace() for c in token):
            raise ValueError('The GitHub token must contain one nonempty value.')
        env['GH_TOKEN'] = token
        os.environ['GH_TOKEN'] = token
        signers = context / 'allowed-signers'
        signers.write_text(f"{env['SDLC_GIT_EMAIL']} {public}\n")
        signers.chmod(0o600)
        os.chown(signers, 1000, 1000)
        settings.update({
            'gpg.format': 'ssh', 'gpg.ssh.program': '/usr/local/bin/ssh-sign-file',
            'gpg.ssh.allowedSignersFile': str(signers),
            'user.signingKey': str(signing_key), 'commit.gpgsign': 'true',
        })
    for key, value in settings.items():
        run('git', 'config', '--file', str(config), key, value)
    os.chown(config, 1000, 1000)
    environment = context / 'environment'
    environment.write_text('unset SSH_AUTH_SOCK\n' + ''.join(
        f'export {key}={shlex.quote(value)}\n' for key, value in env.items()))
    environment.chmod(0o600)
    os.chown(environment, 1000, 1000)
    runtime_json = context / 'environment.json'
    runtime_json.write_text(json.dumps(env))
    runtime_json.chmod(0o600)
    os.chown(runtime_json, 1000, 1000)
    # Bash also reads .bashrc for noninteractive commands invoked through sshd.
    bashrc = Path('/home/node/.bashrc')
    bashrc.write_text('. /run/sdlc/environment\n')
    os.chown(bashrc, 1000, 1000)
    os.environ.update(env)

    args = sys.argv[1:] or ['bash', '-l']
    if args == ['sshd']:
        ssh_dir = Path('/home/node/.ssh')
        ssh_dir.mkdir(mode=0o700, exist_ok=True)
        authorized = ssh_dir / 'authorized_keys'
        authorized.write_text(Path('/run/secrets/ssh-public-key').read_text())
        os.chown(ssh_dir, 1000, 1000)
        authorized.chmod(0o600)
        os.chown(authorized, 1000, 1000)
        host_key = Path('/var/lib/sdlc-ssh/ssh_host_ed25519_key')
        if not host_key.exists():
            run('ssh-keygen', '-q', '-t', 'ed25519', '-N', '', '-f', str(host_key))
        os.execv('/usr/sbin/sshd', ['/usr/sbin/sshd', '-D', '-e', '-f', '/etc/ssh/sshd_config.sdlc'])
    os.execvp('gosu', ['gosu', 'node', *args])


if __name__ == '__main__':
    try:
        main()
    except (ValueError, KeyError, subprocess.CalledProcessError) as exc:
        # Do not print command output or the generated environment containing tokens.
        print(f'Container setup failed: {type(exc).__name__}. Check profile and secret files.',
              file=sys.stderr)
        sys.exit(1)
