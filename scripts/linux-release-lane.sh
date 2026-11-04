#!/bin/sh
# One Linux leg of the release, as .github/workflows/linux-release-build.yml
# runs it inside ubuntu:22.04: build, prove the companion loads, hold the glibc
# floor on the files the operator gets, and archive the lane.
#
#   linux-release-lane.sh linux-amd64|linux-arm64 [VERSION]
#
# An empty VERSION is the one the Makefile derives from `git describe`.
set -eu

scripts=$(dirname "$0")
target=$1
version=${2:-$(git describe --tags --always --dirty)}

make "$target" VERSION="$version"
case $target in
linux-amd64)
	arch=x64
	make windows-amd64 VERSION="$version"
	;;
*) arch=${target#linux-} ;;
esac

core=bin/roca-$version-linux-$arch
mkdir -p .tmp
bundle_home=$(mktemp -d "$PWD/.tmp/glibc-floor.XXXXXX")
mkdir -p "$bundle_home/bin" "$bundle_home/vector"
HOME="$bundle_home" ROCA_PREFIX="$bundle_home/bin" "$core" _install-bundled-plugins --json
tar -C "$bundle_home/vector" -xzf "bin/roca-vector-$version-linux-$arch.tar.gz"
"$scripts/linux-glibc-floor.sh" "$core" "$bundle_home/bin/roca-vector" "$bundle_home/vector/roca-vector"
reported=$("$bundle_home/bin/roca-vector" --version)
echo "$reported"
case $reported in
*"$version"*) ;;
*)
	echo "the bundled vector binary reports '$reported' and the build is $version" >&2
	exit 1
	;;
esac
"$scripts/archive-release-lane.sh" bin "release-$target.tar.gz"
