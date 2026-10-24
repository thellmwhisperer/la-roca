#!/bin/sh
set -eu
LC_ALL=C
export LC_ALL

cd "$(git rev-parse --show-toplevel)"
base=${COVER_BASE:-}
if [ -z "$base" ]; then
    base=$(git merge-base HEAD origin/integration 2>/dev/null) || {
        printf '%s\n' 'cover-diff: cannot find the base commit'
        git branch -a
        exit 1
    }
fi

diff=$(git -c core.quotePath=false diff --no-color --no-ext-diff \
    --src-prefix=a/ --dst-prefix=b/ -U0 "$base" -- '*.go' \
    ':(exclude)*_test.go' ':(exclude)**/testdata/**' \
    ':(exclude)test/**' ':(exclude)plugins/**')
if [ -z "$diff" ]; then
    printf '%s\n' 'cover-diff: no Go changes'
    exit 0
fi

module=$(go list -m)
scratch=$(cd "${TMPDIR:-/tmp}" && pwd -P)
case "$scratch/" in
    "$(pwd -P)/"*) scratch=/tmp ;;
esac
profile="$scratch/cover-diff-$$.profile"
go tool covdata textfmt -i="${GOCOVERDIR:?cover-diff: GOCOVERDIR is not set}" -o "$profile"
printf '%s\n' "$diff" |
awk -v prefix="$module/" -v root="$(pwd)/" '
    FILENAME == "-" {
        if ($0 ~ /^diff --git /) { hunk = 0; next }
        if (!hunk && $0 ~ /^\+\+\+ b\//) { file = substr($0, 7); sub(/\t$/, "", file); next }
        if ($0 ~ /^@@ /) {
            split($3, range, ",")
            line = substr(range[1], 2) + 0
            hunk = 1
            next
        }
        if (hunk && $0 ~ /^\+/) { changed[file SUBSEP line] = 1; line++ }
        else if (hunk && $0 ~ /^ /) { line++ }
        next
    }
    match($0, /:[0-9]+\.[0-9]+,[0-9]+\.[0-9]+ /) {
        file = substr($0, 1, RSTART - 1)
        split(substr($0, RSTART + 1), block, /[., ]+/)
        if (index(file, prefix) == 1) file = substr(file, length(prefix) + 1)
        else if (index(file, root) == 1) file = substr(file, length(root) + 1)
        for (line = block[1]; line <= block[3]; line++) {
            key = file SUBSEP line
            if (key in changed) {
                counted[key] = 1
                if (block[6] > 0) executed[key] = 1
            }
        }
    }
    END {
        for (key in counted) {
            split(key, location, SUBSEP)
            printf "%s:%d\t%d\n", location[1], location[2], key in executed
        }
    }
' - "$profile" | sort -t: -k1,1 -k2,2n |
awk -F '\t' '
    {
        count++
        if ($2 == 0) {
            if (!failed++) print "cover-diff: these changed lines are not executed by any end-to-end run of the real roca binary. Delete them, or write the acceptance scenario that reaches them from a real roca command."
            print $1
        }
    }
    END {
        if (failed) exit 1
        printf "cover-diff: %d changed lines inside covered blocks, all executed by an end-to-end run\n", count
    }
'
