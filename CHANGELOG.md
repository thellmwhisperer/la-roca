# Changelog

## Unreleased

### Features

* remove all model credential machinery; agent models authenticate through their own CLIs

Most users need to do nothing: an already signed-in Codex or Claude CLI is detected and used automatically. Existing remote-provider configuration is tolerated and receives a migration proposal when a supported local CLI is available, or a removal proposal when none is available. A provider table that declares its own `command` keeps that command: the proposal removes only the retired authentication keys. Recovery backups made while retiring those providers are credential-redacted rather than byte-exact. If an older installation left files under `~/.roca/credentials`, La Roca no longer reads them; they never disable a working CLI transport and are offered for removal on their own. `roca init` retires nothing behind its model confirmation, and `roca update` no longer refreshes a remote model catalogue.

The bootstrap JSON field `external_credential` is now named `command_transport`; it reports that the selected model runs through a local agent CLI without implying that La Roca owns authentication.

## [1.13.0](https://github.com/thellmwhisperer/la-roca/compare/v1.12.0...v1.13.0) (2026-08-12)


### Features

* remove all credential machinery: models authenticate through their own CLIs ([#59](https://github.com/thellmwhisperer/la-roca/issues/59)) ([62e1b19](https://github.com/thellmwhisperer/la-roca/commit/62e1b193b2a95f21536f732f2e7f05dbe3b05af4))

## [1.12.0](https://github.com/thellmwhisperer/la-roca/compare/v1.11.0...v1.12.0) (2026-08-12)


### Features

* repair known model SQL mistakes before the gate and validate questions first ([#66](https://github.com/thellmwhisperer/la-roca/issues/66)) ([119508f](https://github.com/thellmwhisperer/la-roca/commit/119508f3096a856d43abf706234ea33497d8e015))

## [1.11.0](https://github.com/thellmwhisperer/la-roca/compare/v1.10.0...v1.11.0) (2026-08-12)


### Features

* system-stamped authorship on memories ([#64](https://github.com/thellmwhisperer/la-roca/issues/64)) ([d84b0c7](https://github.com/thellmwhisperer/la-roca/commit/d84b0c7d063079620e90fed10f4a839455211fe4))

## [1.10.0](https://github.com/thellmwhisperer/la-roca/compare/v1.9.2...v1.10.0) (2026-08-12)


### Features

* accent-insensitive search with automatic index rebuild ([#61](https://github.com/thellmwhisperer/la-roca/issues/61)) ([0976970](https://github.com/thellmwhisperer/la-roca/commit/0976970f8d6a15d0cc650dca1df7fd4635a1e4f5))

## [1.9.2](https://github.com/thellmwhisperer/la-roca/compare/v1.9.1...v1.9.2) (2026-08-12)


### Bug Fixes

* isolate malformed ChatGPT conversation envelopes ([#55](https://github.com/thellmwhisperer/la-roca/issues/55)) ([bd7e373](https://github.com/thellmwhisperer/la-roca/commit/bd7e373415227ef411ff5d537aba49d635ed97dd))

## [1.9.1](https://github.com/thellmwhisperer/la-roca/compare/v1.9.0...v1.9.1) (2026-08-12)


### Bug Fixes

* backfill collision groups enrich the numbered original and stay idempotent ([#53](https://github.com/thellmwhisperer/la-roca/issues/53)) ([6cb1847](https://github.com/thellmwhisperer/la-roca/commit/6cb18479bb3ee614e5c93c1f811c4b3f7b5ecd79))

## [1.9.0](https://github.com/thellmwhisperer/la-roca/compare/v1.8.3...v1.9.0) (2026-08-12)


### Features

* ChatGPT data-export ingester ([#51](https://github.com/thellmwhisperer/la-roca/issues/51)) ([eedaeff](https://github.com/thellmwhisperer/la-roca/commit/eedaeff4d7ee0b0d9376ea1a62a09bace1e2497e))

## [1.8.3](https://github.com/thellmwhisperer/la-roca/compare/v1.8.2...v1.8.3) (2026-08-12)


### Bug Fixes

* normalize timestamps in provenance anchor matching ([#49](https://github.com/thellmwhisperer/la-roca/issues/49)) ([c1ed68f](https://github.com/thellmwhisperer/la-roca/commit/c1ed68f37071a93c93b3c36b8d45d1d2f517f97c))

## [1.8.2](https://github.com/thellmwhisperer/la-roca/compare/v1.8.1...v1.8.2) (2026-08-12)


### Bug Fixes

* provenance backfill matches historical rows by content anchor ([#47](https://github.com/thellmwhisperer/la-roca/issues/47)) ([ce984fd](https://github.com/thellmwhisperer/la-roca/commit/ce984fdeaccacbc952a6bcf1e30cd47a3c05da14))

## [1.8.1](https://github.com/thellmwhisperer/la-roca/compare/v1.8.0...v1.8.1) (2026-08-11)


### Bug Fixes

* replay provenance backfill for v2 corpora ([#45](https://github.com/thellmwhisperer/la-roca/issues/45)) ([39bdee4](https://github.com/thellmwhisperer/la-roca/commit/39bdee41ed4ccf2205ceb5d433ed626d9b7e29e1))

## [1.8.0](https://github.com/thellmwhisperer/la-roca/compare/v1.7.0...v1.8.0) (2026-08-11)


### Features

* roca init lists detected agents and models and lets you choose ([#41](https://github.com/thellmwhisperer/la-roca/issues/41)) ([8f41729](https://github.com/thellmwhisperer/la-roca/commit/8f4172949a4bf739a4e82937196a52876d089496))

## [1.7.0](https://github.com/thellmwhisperer/la-roca/compare/v1.6.0...v1.7.0) (2026-08-11)


### Features

* per-exchange provenance across all ingesters ([#39](https://github.com/thellmwhisperer/la-roca/issues/39)) ([2ab6396](https://github.com/thellmwhisperer/la-roca/commit/2ab63965e90e2f5e062d7b8bca28c9244d600234))

## [1.6.0](https://github.com/thellmwhisperer/la-roca/compare/v1.5.0...v1.6.0) (2026-08-11)


### Features

* detected local binaries become the zero-login factory default ([#33](https://github.com/thellmwhisperer/la-roca/issues/33)) ([acf2bc5](https://github.com/thellmwhisperer/la-roca/commit/acf2bc58711b1cc5b1700a371d7b8328bad3a637))

## [1.5.0](https://github.com/thellmwhisperer/la-roca/compare/v1.4.0...v1.5.0) (2026-08-11)


### Features

* post-update capability reconciliation ([#24](https://github.com/thellmwhisperer/la-roca/issues/24)) ([5609810](https://github.com/thellmwhisperer/la-roca/commit/56098100148fb25530ff6a387a85604a6e4e6680))

## [1.4.0](https://github.com/thellmwhisperer/la-roca/compare/v1.3.1...v1.4.0) (2026-08-11)


### Features

* git-style plugin dispatch ([#28](https://github.com/thellmwhisperer/la-roca/issues/28)) ([298fc0c](https://github.com/thellmwhisperer/la-roca/commit/298fc0c016c55c00bebc161345ace1c24d74852d))

## [1.3.1](https://github.com/thellmwhisperer/la-roca/compare/v1.3.0...v1.3.1) (2026-08-11)


### Bug Fixes

* claude-web parent-chain discards no longer cascade ([#15](https://github.com/thellmwhisperer/la-roca/issues/15)) ([a78a074](https://github.com/thellmwhisperer/la-roca/commit/a78a0742941adc99cd8f7180e3b8fea996a9cbfb))

## [1.3.0](https://github.com/thellmwhisperer/la-roca/compare/v1.2.0...v1.3.0) (2026-08-11)


### Features

* ingest the Anthropic data export ([#12](https://github.com/thellmwhisperer/la-roca/issues/12)) ([0632cff](https://github.com/thellmwhisperer/la-roca/commit/0632cff77d6745414524eb82cabe27e98db78bbb))

## [1.2.0](https://github.com/thellmwhisperer/la-roca/compare/v1.1.1...v1.2.0) (2026-08-11)


### Features

* local-binary provider transport ([#11](https://github.com/thellmwhisperer/la-roca/issues/11)) ([cebce80](https://github.com/thellmwhisperer/la-roca/commit/cebce801129b1c66223d72fcd6c5f908bc33a6ec))

## [1.1.1](https://github.com/thellmwhisperer/la-roca/compare/v1.1.0...v1.1.1) (2026-08-11)


### Bug Fixes

* release tags can ship: the refusal scenario stamps its own dev build ([#8](https://github.com/thellmwhisperer/la-roca/issues/8)) ([11fc1d9](https://github.com/thellmwhisperer/la-roca/commit/11fc1d95561be3a59a604cdbabdc91c62dbebc31))

## [1.1.0](https://github.com/thellmwhisperer/la-roca/compare/v1.0.0...v1.1.0) (2026-08-11)


### Features

* launch local semantic memory for agent fleets ([#1](https://github.com/thellmwhisperer/la-roca/issues/1)) ([8e1c70f](https://github.com/thellmwhisperer/la-roca/commit/8e1c70fceb373ab6a71adffe52c42f1a2261c50c))
