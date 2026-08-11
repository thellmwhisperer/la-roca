#!/usr/bin/env bash
# OSS portability guard: production code cannot carry anybody's machine inside
# it. The roots are configuration, never constants.
#
# Fails the build when an absolute user, mounted-volume or specific-home path
# shows up in cmd/ or internal/. The tests are left out on purpose: a fixture
# may name paths, a binary may not.
set -euo pipefail

cd "$(dirname "$0")/.."

patterns=(
  '/Users/[a-z]'
  '/home/[a-z]'
  '/Volumes/'
  '/private/tmp/'
)

failures=0
for pattern in "${patterns[@]}"; do
  # The _test.go files are excluded: the guard protects the product, not the
  # scaffolding.
  found=$(grep -rnE "$pattern" cmd internal --include='*.go' \
    | grep -v '_test\.go:' || true)
  if [ -n "$found" ]; then
    echo "portability guard: absolute machine path in production"
    echo "$found"
    failures=1
  fi
done

if [ "$failures" -ne 0 ]; then
  exit 1
fi
echo "portability guard: clean"
