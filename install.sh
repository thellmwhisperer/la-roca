#!/bin/sh
# La Roca installer. One binary goes on PATH; bundled data plugins go under ~/.roca.
#
# There is no release tree, no `current` symlink and no swap dance, because
# there is nothing to make atomic: the artefact IS the product. What this script
# owes the operator is three things, and they are the ones the campaign paid
# for:
#
#   * It verifies the sha256 against checksums.txt and ABORTS on a mismatch.
#     Nothing is written before that check passes.
#   * It converges over an interrupted installation. A run killed with -9 leaves
#     the previous binary answering, because the target is only ever replaced by
#     `mv` of a fully downloaded file, and the next run cleans the leftovers.
#   * It never overwrites a file it did not put there. A `roca` in the prefix
#     that is not this product is named and the run stops.
#
# The public one-liner is the primary route. Install.sh still accepts
# GITHUB_TOKEN for operators running a private fork or mirror.
#
#   Public repo (the primary route):
#     curl -fsSL https://raw.githubusercontent.com/thellmwhisperer/la-roca/main/install.sh \
#       | sh
#
#   Private repo (forks, mirrors; token travels through curl config stdin):
#     TOKEN="<token>"; REPO="thellmwhisperer/la-roca"
#     printf 'header = "Authorization: Bearer %s"\n' "$TOKEN" \
#       | curl -fsSL --config - -H "Accept: application/vnd.github.raw" \
#           "https://api.github.com/repos/${REPO}/contents/install.sh" \
#       | GITHUB_TOKEN="${TOKEN}" sh -s -- --repo "${REPO}"
#
# POSIX sh on purpose: it runs under the /bin/sh a `curl | sh` gets, whatever
# that shell happens to be on the machine.

set -eu

BINARY=roca
API="${ROCA_GITHUB_API:-https://api.github.com}"
# The repository this product publishes from, which `roca update` also falls
# back to (release.DefaultRepo). A fork, a mirror or a private rebuild is one
# --repo away, and that is the whole reason it is a variable and not a URL.
REPO="${ROCA_REPO:-thellmwhisperer/la-roca}"
VERSION=""
PREFIX="${ROCA_PREFIX:-$HOME/.local/bin}"
TOKEN="${GITHUB_TOKEN:-}"
FORCE=0

usage() {
  cat <<'USAGE'
install.sh [--repo <owner>/<repo>] [--version vX.Y.Z] [--prefix DIR] [--force]

  --repo      the release repository. Default: thellmwhisperer/la-roca (or ROCA_REPO).
  --version   a specific release. Default: the latest published one.
  --prefix    where the binary goes. Default: ~/.local/bin (or ROCA_PREFIX).
  --force     reinstall even when the target version is already installed.

  GITHUB_TOKEN authenticates against a private repository.
USAGE
}

say()  { printf '%s\n' "$*"; }
die()  { printf 'install.sh: %s\n' "$*" >&2; exit 1; }

# The bundled data plugins are placed by the binary that was just installed.
# This script is served from the default branch while --version pins an exact
# release, so it also runs against binaries older than the command: a release
# that never shipped bundled plugins has none to fail to place.
install_bundled_plugins() {
  BUNDLED_REPORT=$("$1" _install-bundled-plugins --json 2>&1) && return 0
  case "$BUNDLED_REPORT" in
    *"unknown command"*|*"unknown flag"*)
      say "note: roca $TAG predates bundled plugins; none were placed"
      return 0
      ;;
  esac
  die "roca $TAG is installed, but its bundled plugins could not be placed: $BUNDLED_REPORT"
}

require_value() {
  [ "$#" -ge 2 ] || die "$1 needs a value"
  case "$2" in --*) die "$1 needs a value, not $2" ;; esac
}

while [ $# -gt 0 ]; do
  case "$1" in
    --repo)    require_value "$@"; REPO="$2"; shift 2 ;;
    --version) require_value "$@"; VERSION="$2"; shift 2 ;;
    --prefix)  require_value "$@"; PREFIX="$2"; shift 2 ;;
    --api)     require_value "$@"; API="$2"; shift 2 ;;
    --force)   FORCE=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *)         die "I do not understand $1. Run --help." ;;
  esac
done

[ -n "$REPO" ] || { usage >&2; die "name the repository with --repo owner/name"; }

# --- the platform ------------------------------------------------------------
#
# The three the release channel builds, spelled exactly as the Makefile and
# `roca update` spell them. A fourth invented here would download an artefact
# that is not published and report it as a network problem.

detect_platform() {
  os=$(uname -s)
  arch=$(uname -m)
  raw="$os/$arch"
  case "$os" in
    Darwin) os=darwin ;;
    Linux)  os=linux ;;
    MINGW*|MSYS*|CYGWIN*|Windows_NT)
      die "Windows is not installed by this script. Download roca-<version>-windows-x64.exe from https://github.com/$REPO/releases and put it on your PATH." ;;
  esac
  case "$arch" in
    arm64|aarch64) arch=arm64 ;;
    x86_64|amd64)  arch=x64 ;;
  esac
  case "$os-$arch" in
    darwin-arm64|linux-x64|linux-arm64) printf '%s' "$os-$arch" ;;
    *) die "there is no published artefact for $raw: the channel builds darwin-arm64, linux-x64 and linux-arm64" ;;
  esac
}

# --- the channel -------------------------------------------------------------

api_get() {
  # $1 url, $2 Accept, $3 output file
  if [ -n "$TOKEN" ]; then
    # curl options are visible in the process list. Feed the secret-bearing
    # header through config stdin so the token never becomes an argv entry.
    printf 'header = "Authorization: Bearer %s"\n' "$TOKEN" \
      | curl -fsSL --config - -H "Accept: $2" -o "$3" "$1" && return 0
    die "the release channel did not answer for $1"
  fi
  curl -fsSL -H "Accept: $2" -o "$3" "$1" && return 0
  die "the release channel did not answer for $1. If the repository is private, export GITHUB_TOKEN with a token that can read it"
}

# download_asset fetches a release asset by URL and emits its HTTP status.
download_asset() {
  if [ -n "$TOKEN" ]; then
    if [ -n "${3:-}" ]; then
      printf 'header = "Authorization: Bearer %s"\n' "$TOKEN" \
        | curl -fsSL --config - -o "$2" -w '%{http_code}' -H "Accept: $3" "$1" 2>/dev/null || true
    else
      printf 'header = "Authorization: Bearer %s"\n' "$TOKEN" \
        | curl -fsSL --config - -o "$2" -w '%{http_code}' "$1" 2>/dev/null || true
    fi
  else
    if [ -n "${3:-}" ]; then
      curl -fsSL -o "$2" -w '%{http_code}' -H "Accept: $3" "$1" 2>/dev/null || true
    else
      curl -fsSL -o "$2" -w '%{http_code}' "$1" 2>/dev/null || true
    fi
  fi
}


# asset_url reads the API url of one asset out of a release document.
#
# The API url and not the browser one: the browser url is anonymous and 404s on
# a private repository, which is the whole reason this script exists in this
# shape. Normalizing the document puts every asset object on a line of its own,
# which is all the structure the one field we need requires, and it
# does not drag a JSON parser into a `curl | sh`.
asset_url() {
  # $1 release json file, $2 asset name
  tr -d '\n' < "$1" \
    | sed -e 's/.*"assets"[[:space:]]*:[[:space:]]*\[//' \
          -e 's/},[[:space:]]*{/}\
{/g' \
    | awk -v want="$2" '
      function field(line, key,   needle, p, rest) {
        # Locate "key" by string, not by regexp: the quote that anchors the key
        # never enters a character class, which keeps this silent under every
        # awk. Older mawk builds warn about an escaped quote inside a regexp
        # class (an unknown regexp operator), where gawk and BSD awk only
        # tolerate it; the two subs that remain are static regexps with no
        # backslash at all.
        needle = "\"" key "\""
        p = index(line, needle)
        if (p == 0) return ""
        rest = substr(line, p + length(needle))
        sub(/^[[:space:]]*:[[:space:]]*["]/, "", rest)
        sub(/["].*/, "", rest)
        return rest
      }
      {
        if (field($0, "name") == want) {
          print field($0, "url")
          exit
        }
      }
    '
}

download_release_asset() {
  # $1 API url (may be empty), $2 direct url, $3 output file, $4 label
  #
  # The API url (authenticated, private-repo safe) is tried first and the
  # conventional release-download url is the fallback, which is the API-first
  # order docs/lifecycle.md describes. On a 200 the artefact is announced once
  # ("downloading <label>"), after the transfer is known to have succeeded and
  # not before, so a fallback to another name never prints two banners and a
  # failed transfer prints none. On failure DOWNLOAD_CODE holds the last HTTP
  # status, so the caller can tell a channel that answered with no artefact (a
  # real status such as 404) from one curl could not reach at all (000): the two
  # have opposite remedies, and blaming the release for a dead network is the
  # diagnosis this used to regress.
  DOWNLOAD_CODE=""
  if [ -n "$1" ]; then
    DOWNLOAD_CODE=$(download_asset "$1" "$3" "application/octet-stream")
    if [ "$DOWNLOAD_CODE" = "200" ]; then
      [ -n "$4" ] && say "downloading $4"
      return 0
    fi
  fi
  DOWNLOAD_CODE=$(download_asset "$2" "$3")
  if [ "$DOWNLOAD_CODE" = "200" ]; then
    [ -n "$4" ] && say "downloading $4"
    return 0
  fi
  return 1
}

# die_network names a failure curl could not get an answer for at all: DNS, a
# refused connection, TLS, or a token a private repository refused before any
# HTTP status reached the script. The token hint stays for the anonymous case,
# the way api_get keeps it, because sending somebody who already exported a token
# to export one is how a diagnosis costs an afternoon.
die_network() {
  if [ -n "$TOKEN" ]; then
    die "the release channel did not answer for $REPO. The token is set, so this is the network or the API and not a private repository"
  fi
  die "the release channel did not answer for $REPO. If the repository is private, export GITHUB_TOKEN with a token that can read it"
}

# download_failed_die names the real cause of a download that did not succeed:
# 000 (or no code at all) is curl never reaching an answer and is surfaced as a
# network failure; any HTTP status is the channel answering with no artefact, and
# $1 is that message.
download_failed_die() {
  case "$DOWNLOAD_CODE" in 000|"") die_network ;; *) die "$1" ;; esac
}

release_tag() {
  sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$1" | head -1
}

# version_of is the one place that asks a binary what it calls itself: three
# steps need the answer and a second spelling would be one of them reading
# something else.
version_of() { "$1" --version 2>/dev/null | head -1; }

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    die "I cannot verify the checksum: neither sha256sum nor shasum is installed"
  fi
}

# --- the run -----------------------------------------------------------------

PLATFORM=$(detect_platform)
TARGET="$PREFIX/$BINARY"

# Whether this operator can write where they asked for the binary, asked before
# a single byte is downloaded.
#
# Without it the run walked all the way to the copy and came apart there with a
# bare `mkdir: Permission denied` from a tool the operator never invoked, saying
# nothing about whether anything had been installed. It covers the two shapes
# that reach here: a prefix that cannot be created, and one that exists and is
# somebody else's. It is asked and not deduced from the mode bits, because a
# read-only mount and an ACL answer it too.
mkdir -p "$PREFIX" 2>/dev/null || \
  die "I cannot create $PREFIX: fix the permissions of its parent, or install elsewhere with --prefix. Nothing was installed"
PROBE="$PREFIX/.roca-install.probe.$$"
if ! (: > "$PROBE") 2>/dev/null; then
  die "I cannot write in $PREFIX: fix its permissions, or install elsewhere with --prefix. Nothing was installed"
fi
rm -f "$PROBE"

# Converge over an interrupted run: whatever a previous kill left staged in the
# prefix is this script's and goes now: what we own
# is deleted whenever it is there, and nothing else is touched.
rm -f "$PREFIX"/.roca-install.* 2>/dev/null || true

# A working directory to build in. The template carries its own X's because GNU
# mktemp refuses one without them (`-t roca-install` is "too few X's"), where BSD
# fills them in; the original `mktemp -d -t roca-install` was a BSD-ism that was
# green on macOS and dead on Linux. TMPDIR is honoured when it names a real
# directory and falls back to /tmp when it does not: a broken or not-yet-created
# TMPDIR is the kind of thing the installer works around rather than its reason
# to stop before a single byte is downloaded.
tmpbase=/tmp
[ -d "${TMPDIR:-}" ] && tmpbase="$TMPDIR"
WORK=$(mktemp -d "$tmpbase/roca-install.XXXXXX") || die "I cannot create a working directory"
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT INT TERM

# A file at the target that is not this product is named and nothing is done.
# The check is "does it answer as roca", because a file that answers as roca is
# a roca whatever its mtime says.
if [ -e "$TARGET" ] && [ "$FORCE" -eq 0 ]; then
  version_of "$TARGET" | grep -qi "^roca " || \
    die "there is a file at $TARGET that is not a La Roca binary. It has not been touched: move it aside, or install elsewhere with --prefix"
fi

if [ -n "$VERSION" ]; then
  RELEASE_PATH="releases/tags/$VERSION"
else
  RELEASE_PATH="releases/latest"
fi
api_get "$API/repos/$REPO/$RELEASE_PATH" "application/vnd.github+json" "$WORK/release.json"

TAG=$(release_tag "$WORK/release.json")
[ -n "$TAG" ] || die "the channel published no version at $REPO/$RELEASE_PATH"

ARTEFACT="$BINARY-$TAG-$PLATFORM"
ASSET_URL=$(asset_url "$WORK/release.json" "$ARTEFACT")
DOWNLOAD_URL="https://github.com/$REPO/releases/download/$TAG/$ARTEFACT"

SUMS_URL=$(asset_url "$WORK/release.json" "checksums.txt")
SUMS_DOWNLOAD_URL="https://github.com/$REPO/releases/download/$TAG/checksums.txt"
# SUMS_URL is left empty when the release metadata carries no checksums asset:
# download_release_asset skips an empty API url and tries the conventional one
# once, which is the API-first order docs/lifecycle.md describes. Assigning the
# conventional url here would invert that order and send it as the first request.

# Already there and already this version: say so and touch nothing. The inode
# stays what it was, which is what a script reinstalling in a loop needs.
if [ -x "$TARGET" ] && [ "$FORCE" -eq 0 ]; then
  INSTALLED_VERSION=$(version_of "$TARGET" | awk 'NR == 1 && $1 == "roca" { print $2 }')
  if [ "$INSTALLED_VERSION" = "$TAG" ]; then
    install_bundled_plugins "$TARGET"
    say "roca $TAG is already installed at $TARGET"
    exit 0
  fi
fi

if ! download_release_asset "$ASSET_URL" "$DOWNLOAD_URL" "$WORK/$ARTEFACT" "$ARTEFACT"; then
  # curl could not answer at all (000): that is the network or a token a private
  # repository refused, not a name the release did not publish. Surface it
  # instead of blaming the release.
  case "$DOWNLOAD_CODE" in 000|"") die_network ;; esac
  download_failed_die "release $TAG publishes no artefact $ARTEFACT for $PLATFORM"
fi
if ! download_release_asset "$SUMS_URL" "$SUMS_DOWNLOAD_URL" "$WORK/checksums.txt" "checksums.txt"; then
  download_failed_die "release $TAG publishes no checksums.txt: nothing is installed unverified"
fi

# Verified BEFORE anything is written where the operator's PATH points. A binary
# that runs is their only way back and it is not risked on a download.
EXPECTED=$(awk -v want="$ARTEFACT" '{ n = $2; sub(/^\.\//, "", n); if (n == want) print $1 }' \
  "$WORK/checksums.txt" | head -1)
[ -n "$EXPECTED" ] || die "checksums.txt has no line for $ARTEFACT: nothing was installed"
COMPUTED=$(sha256_of "$WORK/$ARTEFACT")
if [ "$EXPECTED" != "$COMPUTED" ]; then
  die "the checksum of $ARTEFACT does not match: the channel published $EXPECTED and what came down is $COMPUTED. Nothing was installed"
fi

# The channel publishes bare binaries, which is the branch below
# that is taken every time. The tarball branch stays because whether the channel
# compresses is the channel's decision and not this script's, and the day it does
# what goes on the PATH is the binary inside and not the archive.
case "$ARTEFACT" in
  *.tar.gz)
    tar -xzf "$WORK/$ARTEFACT" -C "$WORK"
    FOUND=$(find "$WORK" -type f -name "$BINARY" -perm -u+x 2>/dev/null | head -1)
    [ -n "$FOUND" ] || FOUND=$(find "$WORK" -type f -name "$BINARY" | head -1)
    [ -n "$FOUND" ] || die "the artefact $ARTEFACT carries no $BINARY inside it"
    ;;
  *)
    FOUND="$WORK/$ARTEFACT"
    ;;
esac

# Staged inside the prefix so the move that follows stays on one filesystem and
# is therefore atomic. A kill before this point leaves the previous binary in
# place, answering; a kill after it is a complete installation.
STAGED="$PREFIX/.roca-install.$$"
cp "$FOUND" "$STAGED"
chmod 755 "$STAGED"
if ! "$STAGED" --version >/dev/null 2>&1; then
  rm -f "$STAGED"
  die "the downloaded binary does not answer --version. Nothing was installed"
fi
mv -f "$STAGED" "$TARGET"

install_bundled_plugins "$TARGET"

say "binary: $TARGET"
# There is no release tree and no `current` link, so the binary IS the entry on
# the PATH. Both lines are printed because both are what an operator looks for.
say "link:   $TARGET (the binary is the link: this product has no release tree)"
say "version: $(version_of "$TARGET")"

case ":$PATH:" in
  *":$PREFIX:"*) ;;
  *) say "warning: $PREFIX is not on your PATH. Add it with: export PATH=\"$PREFIX:\$PATH\"" ;;
esac
say "next: roca init"
