#!/bin/sh
# Fetch the pinned llama.cpp commit into the repository scratch directory.
set -eu
COMMIT="${LLAMA_COMMIT:-b21e4de74567f5eef213765c9476a843c2e43f0d}"
ROOT="$(CDPATH= cd -- "$(dirname "$0")/../../.." && pwd)"
DIR="${LLAMA_DIR:-$ROOT/.tmp/llama.cpp}"
if [ -d "$DIR/.git" ]; then
	current="$(git -C "$DIR" rev-parse HEAD 2>/dev/null || true)"
	if [ "$current" = "$COMMIT" ]; then
		exit 0
	fi
fi
mkdir -p "$(dirname "$DIR")"
if [ ! -d "$DIR/.git" ]; then
	rm -rf "$DIR"
	git init "$DIR"
	git -C "$DIR" remote add origin https://github.com/ggml-org/llama.cpp
fi
git -C "$DIR" fetch --depth 1 origin "$COMMIT"
git -C "$DIR" checkout --force FETCH_HEAD
