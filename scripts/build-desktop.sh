#!/bin/sh
# Use MyGo packaging so the terminal's native runtime ships with the matched pair.
set -eu
repo_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
output=${1:-"$repo_dir/build"}
platform="$(go env GOOS)/$(go env GOARCH)"
python3 "$repo_dir/scripts/package-desktop.py" --unsigned --platform "$platform" --out "$output"
