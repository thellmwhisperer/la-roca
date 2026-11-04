## Context

See proposal.md, Why. Today `release.yml` builds each artefact in the
`native-artifacts` matrix on the runner's own OS: `make linux-amd64` on
`ubuntu-latest`, `make linux-arm64` on `ubuntu-24.04-arm`. The Linux targets
in `plugins/vector/Makefile` refuse to cross-compile, so the glibc of the
build host is the glibc the companion needs. The `linux-amd64` leg also runs
`make windows-amd64`, which is `CGO_ENABLED=0` and needs no glibc. CI builds
the companion in `vector-native-linux` and the host artefacts in the `check`
job with `make dist`, both on `ubuntu-latest`, and keeps nothing.

The pinned llama.cpp commit needs CMake 3.14 or later and C++17
(`cmake_minimum_required(VERSION 3.14...3.28)` and `CMAKE_CXX_STANDARD 17`
in its `CMakeLists.txt` and `ggml/CMakeLists.txt`). Ubuntu 22.04 ships CMake
3.22 and g++ 11, which meet both.

## Goals / Non-Goals

**Goals:**

- The Linux artefacts need at most `GLIBC_2.35` whatever the hosted runner
  images do next.
- A pull request runs the release's own Linux build, so the floor and the
  candidate archive come from the same steps the release runs.

**Non-Goals:**

- Static, musl or `zig cc` builds; symbol-version pinning; a sysroot.
- A floor for `GLIBCXX` or `CXXABI`. Building with g++ 11 already keeps
  them at gcc 11's level, which every glibc 2.35 system ships.
- Changing `vector-native-linux` or the `make dist` step of `check`. They
  test code and stay on `ubuntu-latest`.

## Decisions

### Build inside a pinned `ubuntu:22.04` container

Both Linux legs run as container jobs with `container: ubuntu:22.04`, on
`ubuntu-latest` for amd64 and `ubuntu-24.04-arm` for arm64. The first step
installs `git make cmake g++ ca-certificates` with `apt-get`, then
`actions/checkout` with `fetch-depth: 0` and `actions/setup-go` run as
today, and `make linux-<arch>` builds.

Why the container and not pinned `ubuntu-22.04` and `ubuntu-22.04-arm`
runners: a runner label is a host image GitHub retires on its own schedule,
and `ubuntu-latest` has already moved under this release once. The container
image keeps glibc 2.35 whatever host it runs on. The Go toolchain still comes
from `go.mod` through `setup-go`, so only the C side changes.

Alternatives considered:

- Pinned 22.04 runners: one-word change, but the floor ends the day the label
  is retired, and the arm64 label has its own retirement date.
- Static or musl `roca-vector`: llama.cpp through cgo against musl is a new
  build path for the engine, and the engine is not touched here.
- Building on the newest image and capping symbols with `.symver` or a
  sysroot: clever, fragile, and invisible in review.

### One reusable workflow for the Linux legs

A new `.github/workflows/linux-release-build.yml` has `on: workflow_call`
with inputs `ref` and `version`, and a matrix of `linux-amd64` and
`linux-arm64`. Each leg builds, runs the floor check, archives with
`scripts/archive-release-lane.sh` and uploads `release-<target>` as today.
The amd64 leg also runs `make windows-amd64`, as the `compatibility` flag
does today. `release.yml` drops the two Linux entries from
`native-artifacts`, calls the reusable workflow with the tag, and `publish`
needs it. `ci.yml` calls it with the pull request's commit and the version
the Makefile derives from `git describe`.

Why: "built exactly like the release" is then a fact of the file, not a
promise kept by two copies. A duplicated job in `ci.yml` was the other
option; it drifts the first time one copy is edited.

### The floor check reads the files the operator gets

`scripts/linux-glibc-floor.sh` is POSIX `sh` over `readelf --version-info
--wide`, `grep -o 'GLIBC_[0-9][0-9.]*'` and `sort -V`. The floor `2.35` is
a constant in the script. Each leg runs it on three files:

- `bin/roca-<version>-linux-<arch>`, the core, which prints `none`;
- the `roca-vector` that `roca _install-bundled-plugins` materializes into a
  prefix under `.tmp`, the same step `publish` runs, followed by
  `roca-vector --version`, which proves the binary loads on glibc 2.35;
- the `roca-vector` extracted from
  `bin/roca-vector-<version>-linux-<arch>.tar.gz`.

`readelf` comes with `binutils`, which `g++` installs. `objdump -T` would
give the same answer; `readelf -V` reads only the version needs and is
shorter to parse.

The test runs the script with a stub `readelf` first on `PATH` that prints
fixed version-needs output: one case at `GLIBC_2.43`, one at `GLIBC_2.34`,
one with no glibc.

### CI keeps the amd64 archive

The reusable workflow's upload step is the release's, so a CI run carries
`release-linux-amd64` and `release-linux-arm64`. The caller sets
`retention-days: 7` for CI and leaves the release default. An operator
downloads the amd64 archive with `gh run download`, and the acceptance
suite's local release channel serves its core to `install.sh`.

## Risks / Trade-offs

- [The reusable workflow's job names change the required check names on
  `main`] → Re-derive the required checks from a finished CI run, as the
  comment in `ci.yml` says, before the merge.
- [JavaScript actions inside a container on the arm64 runner] → The CI run
  on the pull request runs the arm64 leg, so a failure shows before a tag.
- [Two more runners on every pull request, about 4 minutes each going by the
  v1.94.1 release legs, plus an amd64 archive of about 86 MB of files
  before compression per run, going by the v1.94.1 asset sizes]
  → Seven-day retention; the jobs run in parallel with `check`.
- [A future llama.cpp pin needs a newer CMake or compiler than 22.04 ships]
  → The pull request that moves the pin is red in the CI leg and says so.

## Migration Plan

No data or home changes. The next tag after the merge is the first release
built in the container. Rollback is reverting the merge. Already published
v1.94.1 assets stay as they are; operators on glibc older than 2.43 take the
next release.
