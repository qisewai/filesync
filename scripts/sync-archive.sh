#!/bin/sh
# The client and server are independent Go modules. Copy the read-only reader
# and its tests from this canonical source into the server's internal package.
set -eu
repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
server_dir=${1:-"$repo_dir/../fileindex"}
if [ ! -f "$server_dir/internal/server/server.go" ]; then
    echo "fileindex repository not found: $server_dir" >&2
    exit 1
fi
mkdir -p "$server_dir/internal/archive"
cp "$repo_dir"/internal/archive/*.go "$server_dir/internal/archive/"
cp -R "$repo_dir/internal/archive/testdata" "$server_dir/internal/archive/"
