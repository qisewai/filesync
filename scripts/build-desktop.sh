#!/bin/sh
# Build the matched frontend/backend pair; never run go build only on the UI.
set -eu
repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
manager_dir="$repo_dir/filemanager"
[ -f "$manager_dir/go.mod" ] || { echo 'Missing bundled filemanager module' >&2; exit 1; }
output=${1:-"$repo_dir/build"}
mkdir -p "$output"
output=$(CDPATH= cd -- "$output" && pwd)
suffix=
if [ "$(go env GOOS)" = windows ]; then suffix=.exe; fi
(cd "$manager_dir" && go build -o "$output/filemanager$suffix" ./cmd/filemanager)
(cd "$repo_dir" && go build -o "$output/filesync$suffix" .)
