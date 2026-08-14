# Changelog

## Unreleased

### Features

* remove all model credential machinery; agent models authenticate through their own CLIs

Most users need to do nothing: an already signed-in Codex or Claude CLI is detected and used automatically. Existing remote-provider configuration is tolerated and receives a migration proposal when a supported local CLI is available, or a removal proposal when none is available. A provider table that declares its own `command` keeps that command: the proposal removes only the retired authentication keys. Recovery backups made while retiring those providers are credential-redacted rather than byte-exact. If an older installation left files under `~/.roca/credentials`, La Roca no longer reads them; they never disable a working CLI transport and are offered for removal on their own. `roca init` retires nothing behind its model confirmation, and `roca update` no longer refreshes a remote model catalogue.

The bootstrap JSON field `external_credential` is now named `command_transport`; it reports that the selected model runs through a local agent CLI without implying that La Roca owns authentication.

## [1.25.2](https://github.com/thellmwhisperer/la-roca/compare/v1.25.1...v1.25.2) (2026-08-14)


### Bug Fixes

* **cli:** tell the two empty cascades apart in the model commands ([#114](https://github.com/thellmwhisperer/la-roca/issues/114)) ([262c565](https://github.com/thellmwhisperer/la-roca/commit/262c565d0bb8ac4e2f56a4cc383c586b6d9f6f7f))

## [1.25.1](https://github.com/thellmwhisperer/la-roca/compare/v1.25.0...v1.25.1) (2026-08-14)


### Bug Fixes

* **ingest:** recover legacy Codex prompts from fossil rollouts ([#112](https://github.com/thellmwhisperer/la-roca/issues/112)) ([8b6d896](https://github.com/thellmwhisperer/la-roca/commit/8b6d89648f64f150223e57a79329e93958e4e435))

## [1.25.0](https://github.com/thellmwhisperer/la-roca/compare/v1.24.1...v1.25.0) (2026-08-14)


### Features

* **distribution:** bundle the inert roca-corpus harvest plugin ([#109](https://github.com/thellmwhisperer/la-roca/issues/109)) ([dea41e2](https://github.com/thellmwhisperer/la-roca/commit/dea41e2896a0554aa992e5ed3c24de2f00937af7))

## [1.24.1](https://github.com/thellmwhisperer/la-roca/compare/v1.24.0...v1.24.1) (2026-08-14)


### Bug Fixes

* **distribution:** bind plugin installs to one verified descriptor and gate the release redirect bound ([#106](https://github.com/thellmwhisperer/la-roca/issues/106)) ([d4f0d6a](https://github.com/thellmwhisperer/la-roca/commit/d4f0d6a39283500f8f561b25118e68cafb7c4651))

## [1.24.0](https://github.com/thellmwhisperer/la-roca/compare/v1.23.0...v1.24.0) (2026-08-14)


### Features

* **distribution:** add the roca cron plugin ride train ([#102](https://github.com/thellmwhisperer/la-roca/issues/102)) ([b5c722d](https://github.com/thellmwhisperer/la-roca/commit/b5c722d5b36274743d243ade149b807a3215e289))

## [1.23.0](https://github.com/thellmwhisperer/la-roca/compare/v1.22.0...v1.23.0) (2026-08-14)


### Features

* **vector:** add optional local semantic search under roca vector ([#103](https://github.com/thellmwhisperer/la-roca/issues/103)) ([0b48525](https://github.com/thellmwhisperer/la-roca/commit/0b48525b6b6a2a1234489e4f5e9ceeae7c5c0217))

## [1.22.0](https://github.com/thellmwhisperer/la-roca/compare/v1.21.1...v1.22.0) (2026-08-13)


### Features

* **cli:** split model check from model set and retire login ([#100](https://github.com/thellmwhisperer/la-roca/issues/100)) ([43d5a74](https://github.com/thellmwhisperer/la-roca/commit/43d5a74b8161ce41420ce66b4b231aab77a18d67))

## [1.21.1](https://github.com/thellmwhisperer/la-roca/compare/v1.21.0...v1.21.1) (2026-08-13)


### Bug Fixes

* **ingest:** make account exports one-shot imports instead of standing sources ([#98](https://github.com/thellmwhisperer/la-roca/issues/98)) ([f48b18e](https://github.com/thellmwhisperer/la-roca/commit/f48b18eaf8f456c93a7d137978b05c3e16a3ec93))

## [1.21.0](https://github.com/thellmwhisperer/la-roca/compare/v1.20.0...v1.21.0) (2026-08-13)


### Features

* extract agent operational writes into a bundled roca-ops plugin ([#94](https://github.com/thellmwhisperer/la-roca/issues/94)) ([ea82e3f](https://github.com/thellmwhisperer/la-roca/commit/ea82e3f2bfe91132ae339a4b5fefe460aa6d9e4f))

## [1.20.0](https://github.com/thellmwhisperer/la-roca/compare/v1.19.0...v1.20.0) (2026-08-13)


### Features

* **distribution:** manage agent skill, prompt, and hook as versioned artifacts ([#95](https://github.com/thellmwhisperer/la-roca/issues/95)) ([141beb2](https://github.com/thellmwhisperer/la-roca/commit/141beb25d8f9f681b24e6bb57210c154a76f40f5))

## [1.19.0](https://github.com/thellmwhisperer/la-roca/compare/v1.18.0...v1.19.0) (2026-08-13)


### Features

* grounded exploration mode with plain and deep explore ([#91](https://github.com/thellmwhisperer/la-roca/issues/91)) ([2499dc1](https://github.com/thellmwhisperer/la-roca/commit/2499dc1cce745ec20a9933e5ddd4fc96da9427ea))

## [1.18.0](https://github.com/thellmwhisperer/la-roca/compare/v1.17.0...v1.18.0) (2026-08-13)


### Features

* plugin standard: per-plugin databases with semantic layers, attach-based querying, installer ([#87](https://github.com/thellmwhisperer/la-roca/issues/87)) ([f32a248](https://github.com/thellmwhisperer/la-roca/commit/f32a248b419846e2e7364a8f1e44c27063c72bce))

## [1.17.0](https://github.com/thellmwhisperer/la-roca/compare/v1.16.1...v1.17.0) (2026-08-13)


### Features

* security belt: execution timeout, prompt hardening, refuse, guarded interpretation ([#85](https://github.com/thellmwhisperer/la-roca/issues/85)) ([e2b6adf](https://github.com/thellmwhisperer/la-roca/commit/e2b6adff2c57e1e619fd5a1bdac21af98d725370))

## [1.16.1](https://github.com/thellmwhisperer/la-roca/compare/v1.16.0...v1.16.1) (2026-08-12)


### Bug Fixes

* repair and retry runtime FTS errors, keep subjects in previews, complete audit failures ([#80](https://github.com/thellmwhisperer/la-roca/issues/80)) ([138e256](https://github.com/thellmwhisperer/la-roca/commit/138e2563820f34090e30faa6ef6edaca062b36ec))

## [1.16.0](https://github.com/thellmwhisperer/la-roca/compare/v1.15.1...v1.16.0) (2026-08-12)


### Features

* ingest the sharded ChatGPT export format ([#75](https://github.com/thellmwhisperer/la-roca/issues/75)) ([2e516d7](https://github.com/thellmwhisperer/la-roca/commit/2e516d7904775f1ef2d87c91884975f4ad19b7df))

## [1.15.1](https://github.com/thellmwhisperer/la-roca/compare/v1.15.0...v1.15.1) (2026-08-12)


### Bug Fixes

* parse 2025 codex rollout format ([#62](https://github.com/thellmwhisperer/la-roca/issues/62)) ([03fd863](https://github.com/thellmwhisperer/la-roca/commit/03fd8634a2d68ccdd2300ee5bfd32823bb45a07f))

## [1.15.0](https://github.com/thellmwhisperer/la-roca/compare/v1.14.0...v1.15.0) (2026-08-12)


### Features

* full audit log for every call, surfaced by doctor ([#69](https://github.com/thellmwhisperer/la-roca/issues/69)) ([b64249b](https://github.com/thellmwhisperer/la-roca/commit/b64249b49a9dd4fa7cdaefe62d3735fde7c30051))

## [1.14.0](https://github.com/thellmwhisperer/la-roca/compare/v1.13.0...v1.14.0) (2026-08-12)


### Features

* one corrected retry with the gate error before degrading ([#70](https://github.com/thellmwhisperer/la-roca/issues/70)) ([124a8c7](https://github.com/thellmwhisperer/la-roca/commit/124a8c72efd49ed3ba461c81864e885abf94c8c1))

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
