#!/usr/bin/env bash
set -euo pipefail

tag=${1:?usage: publish-model-release.sh TAG [OUTPUT_DIR]}
output=${2:-bin}
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
if [[ "$output" != /* ]]; then
  output="$root/$output"
fi

(
  cd "$root/plugins/vector"
  go run ./cmd/model-release --tag "$tag" --out "$output"
)

shopt -s nullglob
models=("$output"/*.gguf)
if (( ${#models[@]} != 1 )); then
  echo "model release produced ${#models[@]} model assets, want 1" >&2
  exit 1
fi
for required in "$output/LICENSE-model.txt" "$output/checksums.txt"; do
  if [[ ! -f "$required" ]]; then
    echo "model release did not produce $required" >&2
    exit 1
  fi
done

if gh release view "$tag" >/dev/null 2>&1; then
  gh release edit "$tag" --latest=false
else
  gh release create "$tag" --verify-tag --latest=false \
    --title "roca $tag" \
    --notes "Pinned Apache-2.0 embedding model used by La Roca semantic search, sourced from nomic-ai/nomic-embed-text-v2-moe-GGUF. The asset is verified against the source byte count and SHA-256 before publication."
fi
gh release upload "$tag" --clobber "${models[0]}" "$output/LICENSE-model.txt"
gh release upload "$tag" --clobber "$output/checksums.txt"
