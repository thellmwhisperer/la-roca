#!/usr/bin/env bash
# Fail when a removed adjacent-feature dragon's forbid reappears in the tree.
# Dependency-free: bash, find, grep, awk. Records live in .slop/dragons/.
set -euo pipefail

cd "$(dirname "$0")/.."
REPO=$(pwd)
RECORDS="${REPO}/.slop/dragons"
ROOT=${1:-$REPO}

usage() {
  echo "usage: check-dragons.sh [scan-root]" >&2
  exit 2
}

[ "$#" -le 1 ] || usage
[ -d "$RECORDS" ] || {
  echo "check-dragons: missing ${RECORDS}" >&2
  exit 1
}

# Content search skips the registry itself, git, build output and caches.
grep_tree() {
  local needle=$1
  local root=$2
  find "$root" \
    \( -name .git -o -name .tmp -o -name bin -o -name dist \
       -o -name dragons -o -name .worktrees \) -prune -o \
    -type f \( -name "*.go" -o -name "*.sql" -o -name "*.md" -o -name "*.sh" \
       -o -name "*.yml" -o -name "*.yaml" -o -name "*.toml" -o -name "*.json" \
       -o -name "*.feature" -o -name "*.c" -o -name "*.h" -o -name "*.cpp" \) \
    -exec grep -l -F -- "$needle" {} + 2>/dev/null || true
}

path_exists() {
  local root=$1
  local pattern=$2
  local candidate
  if [[ "$pattern" == *[\*\?[]* ]]; then
    while IFS= read -r -d '' candidate; do
      echo "$candidate"
      return 0
    done < <(find "$root" \( -name .git -o -name .tmp -o -name bin -o -name dist \
      -o -name dragons -o -name .worktrees \) -prune -o -path "$root/$pattern" -print0 2>/dev/null)
    return 1
  fi
  if [ -e "$root/$pattern" ]; then
    echo "$root/$pattern"
    return 0
  fi
  return 1
}

# Prints "section<TAB>value" lines for forbid lists in one record.
forbid_items() {
  awk '
    /^forbid:[[:space:]]*$/ { in_forbid=1; next }
    /^forbid:/ {
      print FILENAME ": forbid must use block lists or []" > "/dev/stderr"
      exit 1
    }
    in_forbid && /^[^[:space:]#]/ { in_forbid=0 }
    in_forbid && /^[[:space:]]+paths:[[:space:]]*(\[\][[:space:]]*)?$/ { section="paths"; next }
    in_forbid && /^[[:space:]]+symbols:[[:space:]]*(\[\][[:space:]]*)?$/ { section="symbols"; next }
    in_forbid && /^[[:space:]]+strings:[[:space:]]*(\[\][[:space:]]*)?$/ { section="strings"; next }
    in_forbid && section != "" && /^[[:space:]]+-[[:space:]]+/ {
      val=$0
      sub(/^[[:space:]]*-[[:space:]]*/, "", val)
      gsub(/^["'\'']|["'\'']$/, "", val)
      if (val != "" && val != "[]") print section "\t" val
      next
    }
    in_forbid && $0 !~ /^[[:space:]]*(#.*)?$/ {
      print FILENAME ": unsupported forbid entry; use block lists or []" > "/dev/stderr"
      exit 1
    }
  ' "$1"
}

record_field() {
  awk -v key="$2" '
    $0 ~ "^" key ":[[:space:]]*" {
      val=$0
      sub("^" key ":[[:space:]]*", "", val)
      gsub(/^["'\'']|["'\'']$/, "", val)
      print val
      exit
    }
  ' "$1"
}

# Returns 0 when no removed forbid matches. Prints hits to stdout.
check_tree() {
  local root=$1
  local hits=0
  local file id status section value match items
  shopt -s nullglob
  for file in "$RECORDS"/*.yaml "$RECORDS"/*.yml; do
    status=$(record_field "$file" status)
    [ "$status" = "removed" ] || continue
    id=$(record_field "$file" id)
    [ -n "$id" ] || id=$(basename "$file")
    items=$(forbid_items "$file") || return 1
    while IFS=$'\t' read -r section value; do
      [ -n "$value" ] || continue
      match=""
      case "$section" in
        paths)
          match=$(path_exists "$root" "$value" || true)
          ;;
        symbols|strings)
          match=$(grep_tree "$value" "$root" || true)
          ;;
      esac
      if [ -n "$match" ]; then
        hits=$((hits + 1))
        echo "check-dragons: ${id} forbid.${section} ${value} present:"
        echo "$match" | sed 's/^/  /'
      fi
    done <<< "$items"
  done
  [ "$hits" -eq 0 ]
}

first_removed_forbid() {
  local file id status section value items
  shopt -s nullglob
  for file in "$RECORDS"/*.yaml "$RECORDS"/*.yml; do
    status=$(record_field "$file" status)
    [ "$status" = "removed" ] || continue
    id=$(record_field "$file" id)
    [ -n "$id" ] || id=$(basename "$file")
    items=$(forbid_items "$file") || return 1
    while IFS=$'\t' read -r section value; do
      if [ -n "$value" ]; then
        printf '%s\t%s\t%s\n' "$id" "$section" "$value"
        return 0
      fi
    done <<< "$items"
  done
  return 1
}

selftest() {
  local tmp id section value probe
  tmp=$(mktemp -d "${TMPDIR:-/tmp}/check-dragons.XXXXXX")
  trap 'rm -rf "$tmp"' RETURN
  probe=$(first_removed_forbid) || {
    echo "check-dragons: no removed record with a forbid; cannot self-test" >&2
    exit 1
  }
  id=${probe%%$'\t'*}
  rest=${probe#*$'\t'}
  section=${rest%%$'\t'*}
  value=${rest#*$'\t'}
  case "$section" in
    paths)
      mkdir -p "$tmp/$(dirname "$value")"
      printf 'package dragonprobe\n' >"$tmp/$value"
      ;;
    symbols|strings)
      mkdir -p "$tmp/probe"
      printf '// %s\npackage dragonprobe\n' "$value" >"$tmp/probe/hit.go"
      ;;
    *)
      echo "check-dragons: unknown forbid section ${section}" >&2
      exit 1
      ;;
  esac
  if check_tree "$tmp"; then
    echo "check-dragons: synthetic ${id} forbid.${section} ${value} should fail" >&2
    exit 1
  fi
  echo "check-dragons: synthetic file matching ${id} forbid.${section} fails (expected)"
  rm -rf "$tmp/$(echo "$value" | cut -d/ -f1)" "$tmp/probe"
  mkdir -p "$tmp/empty"
  if ! check_tree "$tmp"; then
    echo "check-dragons: tree without that file should pass" >&2
    exit 1
  fi
  echo "check-dragons: tree without that file passes"
}

if ! check_tree "$ROOT"; then
  echo "check-dragons: a removed dragon's forbid is present" >&2
  exit 1
fi
echo "check-dragons: removed forbids absent from ${ROOT}"

# The synthetic fail/pass pair is the issue's acceptance evidence. It runs
# against a disposable tree, never the scan root, so a clean checkout stays
# clean.
selftest
