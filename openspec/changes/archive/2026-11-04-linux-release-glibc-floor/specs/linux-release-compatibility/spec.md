## Purpose

Says the oldest glibc the Linux release artefacts of La Roca run on, the
check that keeps every Linux build at or under that floor, and the Linux
candidate CI keeps so an operator can install it before a release.

## ADDED Requirements

### Requirement: Linux release binaries need at most GLIBC_2.35

The `linux-amd64` and `linux-arm64` release artefacts SHALL need at most
`GLIBC_2.35`: the core `roca`, the `roca-vector` the core carries and
materializes, and the `roca-vector` in the standalone
`roca-vector-<version>-linux-<arch>.tar.gz`. They SHALL run on Ubuntu 22.04
LTS (glibc 2.35) and on every Linux with a later glibc.

#### Scenario: The carried companion starts on glibc 2.35

- **WHEN** the release build for `linux-amd64` or `linux-arm64` has finished,
  and on a glibc 2.35 system `roca-<version>-linux-<arch>
  _install-bundled-plugins --json` runs with an empty `HOME` and
  `ROCA_PREFIX`, and `roca-vector --version` then runs from that prefix
- **THEN** both commands exit 0, `roca-vector --version` contains the build's
  version, and neither prints `GLIBC_` followed by `not found`

#### Scenario: An install on Ubuntu 24.04 LTS

- **WHEN** the `roca-<version>-linux-x64` from the `release-linux-amd64`
  archive that CI kept is installed by `install.sh` from the acceptance
  suite's local release channel into `~/.local/bin` on Ubuntu 24.04 LTS
  (glibc 2.39), and `roca-vector --version` runs
- **THEN** it exits 0 with the candidate's version and prints no
  `version 'GLIBC_2.43' not found`

### Requirement: A floor check reads every Linux binary

`scripts/linux-glibc-floor.sh FILE...` SHALL print, for each file, one line
with the file and the highest `GLIBC_x.y` it needs, or `none` when it needs
no glibc. It SHALL exit 0 when every file needs at most `GLIBC_2.35`, and
non-zero otherwise, naming each file above the floor and the floor.

#### Scenario: The v1.94.1 companion is refused

- **WHEN** the script reads the `roca-vector` inside the published
  `roca-vector-v1.94.1-linux-x64.tar.gz`
- **THEN** it prints `GLIBC_2.43` for that file, names the floor
  `GLIBC_2.35`, and exits non-zero

#### Scenario: Binaries within the floor pass

- **WHEN** the script reads a file whose highest glibc need is `GLIBC_2.34`
  and a `roca` core built with `CGO_ENABLED=0`
- **THEN** it prints `GLIBC_2.34` for the first file and `none` for the core,
  and exits 0

### Requirement: Release and CI build Linux the same way and run the check

The release and CI SHALL build the Linux artefacts through one shared build
definition and SHALL run the floor check on its output, so a pull request that
breaks the floor is red before a tag exists, and a release that breaks it
publishes nothing.

#### Scenario: A pull request over the floor is red

- **WHEN** a pull request changes the Linux build so that one of its binaries
  needs more than `GLIBC_2.35`
- **THEN** the CI Linux release build job for that leg fails, and its log
  shows the script's line for that file

#### Scenario: A release over the floor publishes nothing

- **WHEN** a tag is pushed and a Linux leg of the release build produces a
  binary that needs more than `GLIBC_2.35`
- **THEN** that leg fails, the `publish` job does not run, and the tag's
  GitHub release receives no asset from that run

### Requirement: CI keeps the release-built Linux candidate

Every CI run SHALL keep, as a downloadable workflow artifact, the
`release-linux-amd64` archive made by the shared Linux build, holding the same
file names the release publishes. CI SHALL NOT upload anything to GitHub
Releases.

#### Scenario: A pull request carries an installable Linux candidate

- **WHEN** CI has finished on a pull request
- **THEN** the run has a workflow artifact named `release-linux-amd64` whose
  `release-linux-amd64.tar.gz` holds `roca-<version>-linux-x64` and
  `roca-vector-<version>-linux-x64.tar.gz`, and the repository's releases
  are unchanged
