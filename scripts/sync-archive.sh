#!/bin/sh
# Compatibility entry point: the reader now lives in a shared Go module.
set -eu
repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
printf '%s\n' 'Archive code is shared through ../filemanager/archive; validating it instead of copying.'
go -C "$repo_dir/../filemanager" test ./archive
