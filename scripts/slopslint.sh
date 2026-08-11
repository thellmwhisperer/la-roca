#!/usr/bin/env bash
# The slop gate's runner: it builds the pinned slopslint commit once and runs
# the cached binary with whatever arguments it is given.
#
# The binary is not a build dependency of the product and never enters the Go
# module. Until slopslint publishes a release carrying the orphan/claims gates,
# the runner fetches their immutable merged commit and compiles it with Bun.
# A local Bun is preferred; otherwise npx supplies a pinned Bun. The result is
# cached under .tmp/ (which is git-ignored) and reused.
#
# One runner for the laptop and for CI: the workflow calls this script, so what
# blocks a merge is exactly what blocks locally, at exactly the same version.
set -euo pipefail

cd "$(dirname "$0")/.."

# The commit is pinned, not main. A gate whose measurement can change under a
# repository that did not change is not a gate.
COMMIT="d505e3ae1ea6102320b8071f0069c33f0f548569"
BUN_VERSION="1.3.5"
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
binary="${CACHE_DIR}/${COMMIT}/${asset}"

# An explicit binary remains useful for offline development and gate debugging.
if [ -n "${SLOPSLINT_BIN:-}" ]; then
  binary="$SLOPSLINT_BIN"
elif [ ! -x "$binary" ]; then
  commit_cache="${CACHE_DIR}/${COMMIT}"
  repository="${commit_cache}/repository.git"
  mkdir -p "$commit_cache"

  if ! git --git-dir="$repository" rev-parse --is-bare-repository >/dev/null 2>&1; then
    git init --bare -q "$repository"
  fi
  if git --git-dir="$repository" remote get-url origin >/dev/null 2>&1; then
    git --git-dir="$repository" remote set-url origin \
      https://github.com/thellmwhisperer/slopslint.git
  else
    git --git-dir="$repository" remote add origin \
      https://github.com/thellmwhisperer/slopslint.git
  fi
  echo "slopslint: fetching ${COMMIT}"
  git --git-dir="$repository" fetch -q --depth 1 origin "$COMMIT"
  fetched="$(git --git-dir="$repository" rev-parse FETCH_HEAD)"
  if [ "$fetched" != "$COMMIT" ]; then
    echo "slopslint: fetched ${fetched}, expected ${COMMIT}" >&2
    exit 1
  fi

  if command -v bun >/dev/null 2>&1 && [ "$(bun --version)" = "$BUN_VERSION" ]; then
    bun_command=(bun)
  elif command -v npx >/dev/null 2>&1; then
    bun_command=(npx --yes "bun@${BUN_VERSION}")
  else
    echo "slopslint: building ${COMMIT} requires bun ${BUN_VERSION} or npx" >&2
    exit 1
  fi

  build_dir="$(mktemp -d "${commit_cache}/build.XXXXXX")"
  git --git-dir="$repository" archive "$COMMIT" | tar -x -C "$build_dir"
  binary_absolute="$(pwd)/${binary}"
  echo "slopslint: building ${COMMIT} (${os}-${arch})"
  (
    cd "$build_dir"
    "${bun_command[@]}" install --frozen-lockfile
    "${bun_command[@]}" build ./src/cli.ts --compile \
      "--target=bun-${os}-${arch}" --outfile "${binary_absolute}.part"
  )
  chmod +x "${binary}.part"
  mv "${binary}.part" "$binary"
fi

exec "$binary" "$@"
