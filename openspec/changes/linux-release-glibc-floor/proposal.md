## Why

The v1.94.1 Linux x64 `roca-vector` does not start on Ubuntu 24.04 LTS, so
`roca vector install` exits 1 there. The binary needs `GLIBC_2.43` and Ubuntu
24.04 ships glibc 2.39. A Linux with glibc 2.44 runs it.

- The core `roca` is built with `CGO_ENABLED=0` and needs no glibc. The
  `roca-vector` it carries links llama.cpp through cgo, so it needs the glibc
  of the machine that built it.
- In `.github/workflows/release.yml`, job `native-artifacts`, the
  `linux-amd64` leg runs on `ubuntu-latest`. The v1.94.1 job log shows
  `Image: ubuntu-26.04`, so the runner moved under the release and the glibc
  requirement moved with it. The `linux-arm64` leg runs on `ubuntu-24.04-arm`
  (glibc 2.39), which only holds because that label has not moved yet.
- Nothing checks the glibc a Linux binary needs, in CI or in the release, so
  the next runner image change ships the same failure again.

Who asked: `proposal` for all three requirements: the glibc floor, the floor
check, and the Linux candidate CI keeps.

Proof: the published `roca-vector-v1.94.1-linux-x64.tar.gz`, its `roca-vector`
read with `objdump -T`:

```
$ objdump -T roca-vector | grep -o 'GLIBC_2\.[0-9]*' | sort -uV | tail -3
GLIBC_2.34
GLIBC_2.38
GLIBC_2.43
$ objdump -T roca-vector | grep 'GLIBC_2.43'
0000000000000000      DF *UND*	0000000000000000 (GLIBC_2.43) sqrtf
```

On Ubuntu 24.04 LTS, after `install.sh`, `roca vector install` exits 1 with:

```
/lib/x86_64-linux-gnu/libm.so.6: version 'GLIBC_2.43' not found (required by .../roca-vector)
```

## What Changes

- The `linux-amd64` and `linux-arm64` release artefacts, the core and the
  `roca-vector` it carries plus the standalone `roca-vector` archive, are
  built inside a pinned `ubuntu:22.04` container (glibc 2.35), so they need at
  most `GLIBC_2.35` and run on Ubuntu 22.04 and every later glibc.
- A new `scripts/linux-glibc-floor.sh` prints the highest `GLIBC_x.y` each
  Linux binary needs, with `readelf`, and exits non-zero above `GLIBC_2.35`.
- One reusable workflow builds the two Linux legs and runs the check. The
  release calls it in place of the two Linux matrix legs, and CI calls it on
  every pull request, so a regression is red before a tag exists.
- CI keeps the `release-linux-amd64` archive that build makes as a workflow
  artifact, so an operator can install a candidate on a real Linux machine
  with `install.sh` through a local release channel without publishing.

## Capabilities

### New Capabilities

- `linux-release-compatibility`: the oldest glibc the Linux release artefacts
  run on, the check that holds it, and the Linux candidate CI keeps.

### Modified Capabilities

None.

## Impact

- Code: a new reusable workflow `.github/workflows/linux-release-build.yml`
  with the Linux legs that today live in `native-artifacts` of
  `.github/workflows/release.yml`; `release.yml` calls it and `publish` waits
  for it; `.github/workflows/ci.yml` calls it; the new
  `scripts/linux-glibc-floor.sh` and its Go test in `scripts/`; one sentence on
  the glibc floor in `docs/lifecycle.md`.
- Checks that run the change: the CI job of the reusable workflow, both legs,
  on the pull request; `go test ./scripts` in `make check` for the floor
  script; the `@acceptance` scenario "Installing bundles the core and optional
  vector executable with the domain stores" in
  `features/distribution/install.feature`, with a new step that runs
  `roca-vector --version`, run by `make accept` in CI and run once by the
  operator against the CI archive on an Ubuntu 24.04 LTS machine.
- Real sizes, from the v1.94.1 release assets: `roca-v1.94.1-linux-x64` is
  41,873,864 bytes, `roca-v1.94.1-linux-arm64` 38,675,928,
  `roca-v1.94.1-windows-x64.exe` 37,376,568,
  `roca-vector-v1.94.1-linux-x64.tar.gz` 7,094,230 and
  `roca-vector-v1.94.1-linux-arm64.tar.gz` 6,389,641; the `roca-vector`
  inside the x64 archive is 16,847,064 bytes. Each Linux leg took about 4
  minutes in the v1.94.1 release run.
- Not to build: no static or musl build of `roca-vector`, no glibc
  symbol-version pinning or `.symver` tricks, no cross-compiling toolchain or
  sysroot, no check of `GLIBCXX` or other libraries, no new published asset,
  no download of a published release inside CI.
- Not touched: the `darwin-arm64` artefact and its runner, the Windows
  artefact and its Make target, `install.sh`, the vector engine and llama.cpp
  pin, the embedding model, the `vector-native-linux` test job, the `make dist`
  step of the `check` job, and the upgrade gauntlet.
