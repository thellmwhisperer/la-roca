#!/usr/bin/env bash
# The slop gate's runner: it resolves the pinned slopslint binary and runs it
# with whatever arguments it is given.
#
# The binary is not a build dependency of the product and never enters the
# module: it is downloaded once from the public release, cached under .tmp/
# (which is git-ignored) and reused. That is the same shape `make dictionary`
# has for its own tooling, and it is what lets `make check` block on the gate
# without asking anybody to install anything first.
#
# One runner for the laptop and for CI: the workflow calls this script, so what
# blocks a merge is exactly what blocks locally, at exactly the same version.
set -euo pipefail

cd "$(dirname "$0")/.."

# The version is pinned, not "latest". A gate whose measurement can change under
# a repository that did not change is not a gate: the ceilings would move on
# somebody else's release day.
VERSION="${SLOPSLINT_VERSION:-v0.1.0}"
CACHE_DIR=".tmp/slopslint"

case "$(uname -s)" in
  Darwin) os="darwin" ;;
  Linux) os="linux" ;;
  *)
    echo "slopslint: no published binary for $(uname -s)" >&2
    exit 1
    ;;
esac
case "$(uname -m)" in
  arm64 | aarch64) arch="arm64" ;;
  x86_64 | amd64) arch="x64" ;;
  *)
    echo "slopslint: no published binary for $(uname -m)" >&2
    exit 1
    ;;
esac

asset="slopslint-${os}-${arch}"
binary="${CACHE_DIR}/${VERSION}/${asset}"

# An operator who already has slopslint on the PATH is not made to download it.
if [ -n "${SLOPSLINT_BIN:-}" ]; then
  binary="$SLOPSLINT_BIN"
elif [ ! -x "$binary" ]; then
  mkdir -p "$(dirname "$binary")"
  url="https://github.com/thellmwhisperer/slopslint/releases/download/${VERSION}/${asset}"
  echo "slopslint: downloading ${VERSION} (${os}-${arch})"
  # To a temporary name and renamed: a half-downloaded binary at the cached
  # path is a gate that fails for a reason that has nothing to do with the code.
  curl -fsSL "$url" -o "${binary}.part"
  chmod +x "${binary}.part"
  mv "${binary}.part" "$binary"
fi

exec "$binary" "$@"
