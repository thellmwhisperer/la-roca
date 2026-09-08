#!/usr/bin/env bash
# The Go test reads YAML with the project's existing decoder and checks Git's scope.
set -euo pipefail
cd "$(dirname "$0")/.."
[[ $# == 0 ]] || { echo 'check-dragons takes no arguments' >&2; exit 2; }
go test ./.slop/dragons -count=1
