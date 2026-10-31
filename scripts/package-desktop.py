#!/usr/bin/env python3
"""Build a matched MyGo bundle in an isolated staging tree; never modify sources."""
import argparse
import base64
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
from urllib.parse import urlparse


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--platform', default='linux/amd64')
    parser.add_argument('--out', default='build/releases')
    parser.add_argument('--unsigned', action='store_true', help='Local package only; no updater or upload')
    parser.add_argument('--upload', action='store_true', help='Upload with MyGo to configured release source')
    args = parser.parse_args()
    if args.platform not in ['linux/amd64', 'linux/arm64', 'windows/amd64', 'windows/arm64', 'darwin/amd64', 'darwin/arm64']:
        parser.error('Unsupported platform; build each architecture separately')
    if args.platform.startswith('windows/') and shutil.which('makensis') is None:
        parser.error('Windows installers require NSIS (makensis on PATH)')
    root = Path(__file__).resolve().parent.parent
    config = json.loads((root / 'mygo.json').read_text())
    if args.unsigned:
        if args.upload:
            parser.error('--unsigned cannot be uploaded')
        config.pop('updates', None)
    else:
        updates = dict(config.get('updates', {}))
        key = os.environ.get('FILESYNC_UPDATE_PUBLIC_KEY', updates.get('publicKey', ''))
        github = os.environ.get('FILESYNC_UPDATE_GITHUB', updates.get('github', ''))
        url = os.environ.get('FILESYNC_UPDATE_URL', updates.get('url', ''))
        try:
            valid_key = len(base64.b64decode(key, validate=True)) == 32
        except ValueError:
            valid_key = False
        if not valid_key or not os.environ.get('MYGO_UPDATER_PRIVATE_KEY'):
            parser.error('Signed releases require a 32-byte base64 public key and MYGO_UPDATER_PRIVATE_KEY')
        if bool(github) == bool(url):
            parser.error('Configure exactly one of FILESYNC_UPDATE_GITHUB or FILESYNC_UPDATE_URL')
        if url and (urlparse(url).scheme != 'https' or not urlparse(url).netloc):
            parser.error('Update URL must be HTTPS')
        if github and (len(github.split('/')) != 2 or any(not x for x in github.split('/'))):
            parser.error('GitHub source must be owner/repo')
        config['updates'] = {**{k: v for k, v in updates.items() if k not in ('publicKey', 'github', 'url', 'privateKey')}, 'publicKey': key, **({'github': github} if github else {'url': url})}
    out = Path(args.out).resolve()
    out.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='filesync-package-') as tmp:
        stage = Path(tmp)
        app = stage / 'filesync'
        shutil.copytree(root, app,
                        ignore=shutil.ignore_patterns('.git', 'build', '.mygo', '__pycache__', 'filesync.exe'))
        (app / 'mygo.json').write_text(json.dumps(config, indent=2) + '\n')
        goos, goarch = args.platform.split('/')
        resources = app / 'resources'
        resources.mkdir(exist_ok=True)
        helper = resources / ('filemanager.exe' if goos == 'windows' else 'filemanager')
        # Staging intentionally excludes .git; do not probe the caller's VCS.
        build_env = {**os.environ, 'GOFLAGS': os.environ.get('GOFLAGS', '') + ' -buildvcs=false'}
        env = {**build_env, 'GOOS': goos, 'GOARCH': goarch, 'CGO_ENABLED': '0'}
        subprocess.run(['go', 'build', '-trimpath'] + (['-ldflags=-H=windowsgui'] if goos == 'windows' else []) + ['-o', str(helper), './cmd/filemanager'],
                       cwd=app / 'filemanager', env=env, check=True)
        subprocess.run(['go', 'tool', 'mygo', 'build', '-platform', args.platform, '-o', str(out)]
                       + (['-upload'] if args.upload else []), cwd=app, env=build_env, check=True)


if __name__ == '__main__':
    main()
