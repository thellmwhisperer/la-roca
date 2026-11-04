#!/bin/sh
# Prints the highest GLIBC_x.y each Linux binary needs, or `none`, and exits
# non-zero when one needs more than the floor: Ubuntu 22.04's glibc.
set -eu

floor=2.35
status=0
for file in "$@"; do
	needs=$(readelf --version-info --wide "$file")
	need=$(printf '%s\n' "$needs" | grep -o 'GLIBC_[0-9][0-9.]*' | sed 's/^GLIBC_//' | sort -uV | tail -n 1)
	echo "$file ${need:+GLIBC_}${need:-none}"
	if [ -n "$need" ] && [ "$(printf '%s\n%s\n' "$need" "$floor" | sort -V | tail -n 1)" != "$floor" ]; then
		echo "$file needs GLIBC_$need, above the floor GLIBC_$floor" >&2
		status=1
	fi
done
exit "$status"
