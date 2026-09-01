# Changelog

## Unreleased

### Features

* **plugins:** `roca mcp serve` raises plugin-declared session companions. A `plugin.json` may name an executable inside the plugin directory plus fixed argv; serve starts that child for the session, reaps it on exit, retries crashes with bounded backoff, and leaves a dying companion down without blocking queries. Plugins without the field are unchanged. Telemetry is JSONL under the data directory logs area, never a database.

* **vector:** ship a local embedding engine inside the vector companion. The user downloads exactly one embeddings model. macOS and Linux no longer need a separate embedding runtime; Windows keeps the previous path until its native lane exists. Indexing walks newest material first. MCP sessions keep one companion process for their lifetime and prepare the model in the background. Engine timings land in rotated JSONL under the data directory logs area, never in a database.

* **vector:** remove the `vocab` discovery verb

* remove all model credential machinery; agent models authenticate through their own CLIs

Most users need to do nothing: an already signed-in Codex or Claude CLI is detected and used automatically. Existing remote-provider configuration is tolerated and receives a migration proposal when a supported local CLI is available, or a removal proposal when none is available. A provider table that declares its own `command` keeps that command: the proposal removes only the retired authentication keys. Recovery backups made while retiring those providers are credential-redacted rather than byte-exact. If an older installation left files under `~/.roca/credentials`, La Roca no longer reads them; they never disable a working CLI transport and are offered for removal on their own. `roca init` retires nothing behind its model confirmation, and `roca update` no longer refreshes a remote model catalogue.

The bootstrap JSON field `external_credential` is now named `command_transport`; it reports that the selected model runs through a local agent CLI without implying that La Roca owns authentication.

## [1.78.0](https://github.com/thellmwhisperer/la-roca/compare/v1.77.1...v1.78.0) (2026-09-01)


### Features

* **cli:** add canonical session context loading ([#279](https://github.com/thellmwhisperer/la-roca/issues/279)) ([65cb4e1](https://github.com/thellmwhisperer/la-roca/commit/65cb4e15d791bbabfc73358d66c1dbf6ef517dc9))

## [1.77.1](https://github.com/thellmwhisperer/la-roca/compare/v1.77.0...v1.77.1) (2026-09-01)


### Bug Fixes

* recommend WSL for Windows installation ([#275](https://github.com/thellmwhisperer/la-roca/issues/275)) ([29878e0](https://github.com/thellmwhisperer/la-roca/commit/29878e0ce1715542821d86202ed5fc509ad1c187))

## [1.77.0](https://github.com/thellmwhisperer/la-roca/compare/v1.76.0...v1.77.0) (2026-08-31)


### Features

* **ingest:** add ZCode desktop session ingestion ([#272](https://github.com/thellmwhisperer/la-roca/issues/272)) ([15b7823](https://github.com/thellmwhisperer/la-roca/commit/15b7823991ef91e593003ad063329af32aa80b67))

## [1.76.0](https://github.com/thellmwhisperer/la-roca/compare/v1.75.0...v1.76.0) (2026-08-31)


### Features

* **vector:** add occasion-aware writer acceleration ([#269](https://github.com/thellmwhisperer/la-roca/issues/269)) ([1233a3b](https://github.com/thellmwhisperer/la-roca/commit/1233a3bc6ebfd9ff23d4cc0ea79c569b7462151d))

## [1.75.0](https://github.com/thellmwhisperer/la-roca/compare/v1.74.2...v1.75.0) (2026-08-24)


### Features

* **cli:** add tool-call-observer for live session tool calls ([#254](https://github.com/thellmwhisperer/la-roca/issues/254)) ([bb2db08](https://github.com/thellmwhisperer/la-roca/commit/bb2db08aa5588eec88311c2ee5c0938d96cd51be))
* **cli:** prove word search and ask the vector question inside init ([#255](https://github.com/thellmwhisperer/la-roca/issues/255)) ([6b050dc](https://github.com/thellmwhisperer/la-roca/commit/6b050dc396d77552739ee553c81071ce15649f17))
* declare no-mistakes lint and test commands ([#248](https://github.com/thellmwhisperer/la-roca/issues/248)) ([05552fd](https://github.com/thellmwhisperer/la-roca/commit/05552fd2efa37b9d4efe94aa91003a840883c6ee))
* **distribution:** install and update owner/repo plugins from published releases ([#253](https://github.com/thellmwhisperer/la-roca/issues/253)) ([7a5223d](https://github.com/thellmwhisperer/la-roca/commit/7a5223db2a1df87cd320a038703b26c8ee16b25e))
* **distribution:** store one current row per fact in corpus databases ([#250](https://github.com/thellmwhisperer/la-roca/issues/250)) ([c2b6313](https://github.com/thellmwhisperer/la-roca/commit/c2b631356e5dbe03f8fc03ece6ad0ba8bc857bbd))
* **plugins:** raise plugin session companions from mcp serve ([#251](https://github.com/thellmwhisperer/la-roca/issues/251)) ([f441542](https://github.com/thellmwhisperer/la-roca/commit/f441542ea33df1c35cdcf86e70e926f1eb7b5426))
* **vector:** publish verified embedding model releases ([#256](https://github.com/thellmwhisperer/la-roca/issues/256)) ([96ea64c](https://github.com/thellmwhisperer/la-roca/commit/96ea64cca6b7899803f7a1e6d1343a03bf60085b))


### Bug Fixes

* **vector:** prevent delta ingest hangs under Metal contention ([#257](https://github.com/thellmwhisperer/la-roca/issues/257)) ([392d8af](https://github.com/thellmwhisperer/la-roca/commit/392d8af87d93056f5fd0982e2ef65869e309b856))

## [1.74.2](https://github.com/thellmwhisperer/la-roca/compare/v1.74.1...v1.74.2) (2026-08-23)


### Bug Fixes

* **query:** make hybrid search resilient across the federation ([#245](https://github.com/thellmwhisperer/la-roca/issues/245)) ([27a48ce](https://github.com/thellmwhisperer/la-roca/commit/27a48ced9dae6290b5e2f132174725ecf019304c))

## [1.74.1](https://github.com/thellmwhisperer/la-roca/compare/v1.74.0...v1.74.1) (2026-08-23)


### Bug Fixes

* **ingest:** continue legacy imports after exact-payload overlaps ([#243](https://github.com/thellmwhisperer/la-roca/issues/243)) ([5557301](https://github.com/thellmwhisperer/la-roca/commit/5557301b4983c2a291d12230a7530bc9691fe53d))

## [1.74.0](https://github.com/thellmwhisperer/la-roca/compare/v1.73.0...v1.74.0) (2026-08-23)


### Features

* **vector:** ship embedded local inference engine ([#237](https://github.com/thellmwhisperer/la-roca/issues/237)) ([b8dfc48](https://github.com/thellmwhisperer/la-roca/commit/b8dfc48ef9eafbcae46ba26acb95a09fd26bafd7))

## [1.73.0](https://github.com/thellmwhisperer/la-roca/compare/v1.72.0...v1.73.0) (2026-08-23)


### Features

* **query:** add hybrid FTS and vector search ([#240](https://github.com/thellmwhisperer/la-roca/issues/240)) ([7012ac7](https://github.com/thellmwhisperer/la-roca/commit/7012ac72b64fec4ce326021e6475b77f9f653eb9))

## [1.72.0](https://github.com/thellmwhisperer/la-roca/compare/v1.71.0...v1.72.0) (2026-08-23)


### Features

* **vector:** add contextual chunking and resumable re-embedding ([#238](https://github.com/thellmwhisperer/la-roca/issues/238)) ([5f17187](https://github.com/thellmwhisperer/la-roca/commit/5f17187a561632f150620f11671179bf4b8a7eae))

## [1.71.0](https://github.com/thellmwhisperer/la-roca/compare/v1.70.0...v1.71.0) (2026-08-23)


### Features

* **ingest:** import legacy store into corpus and ops ([#235](https://github.com/thellmwhisperer/la-roca/issues/235)) ([62b2f62](https://github.com/thellmwhisperer/la-roca/commit/62b2f62f52fe844c4f541e99e40e34d05e0d601e))

## [1.70.0](https://github.com/thellmwhisperer/la-roca/compare/v1.69.0...v1.70.0) (2026-08-22)


### Features

* **cli:** create default feature config during init ([#230](https://github.com/thellmwhisperer/la-roca/issues/230)) ([e92a2cf](https://github.com/thellmwhisperer/la-roca/commit/e92a2cfd328fc3c1528738b10f488bfcaf68ad0e))

## [1.69.0](https://github.com/thellmwhisperer/la-roca/compare/v1.68.1...v1.69.0) (2026-08-21)


### Features

* **cli:** add SSH remote query bridge ([#228](https://github.com/thellmwhisperer/la-roca/issues/228)) ([c67dbc7](https://github.com/thellmwhisperer/la-roca/commit/c67dbc7f97d620444dab4579a87dc9dbfaf0307e))

## [1.68.1](https://github.com/thellmwhisperer/la-roca/compare/v1.68.0...v1.68.1) (2026-08-21)


### Bug Fixes

* **vector:** reuse legacy embeddings in federated sidecars ([#226](https://github.com/thellmwhisperer/la-roca/issues/226)) ([8d1542a](https://github.com/thellmwhisperer/la-roca/commit/8d1542a91fef8d9658b0f29fb7e32733f1a343f2))

## [1.68.0](https://github.com/thellmwhisperer/la-roca/compare/v1.67.0...v1.68.0) (2026-08-21)


### Features

* **distribution:** teach federated hybrid retrieval ([#224](https://github.com/thellmwhisperer/la-roca/issues/224)) ([e22beb2](https://github.com/thellmwhisperer/la-roca/commit/e22beb253ee8bdce08bfae01e417b35443cf1609))

## [1.67.0](https://github.com/thellmwhisperer/la-roca/compare/v1.66.0...v1.67.0) (2026-08-21)


### Features

* **vector:** federate queries across routed sidecars ([#222](https://github.com/thellmwhisperer/la-roca/issues/222)) ([86c5e48](https://github.com/thellmwhisperer/la-roca/commit/86c5e48c2799525bda6f392cb8d231eeeb3eb596))

## [1.66.0](https://github.com/thellmwhisperer/la-roca/compare/v1.65.0...v1.66.0) (2026-08-21)


### Features

* **vector:** generate database-owned federated sidecars ([#220](https://github.com/thellmwhisperer/la-roca/issues/220)) ([663f9fa](https://github.com/thellmwhisperer/la-roca/commit/663f9fa2b34b61f13f499255e1f49a1174ed3f69))

## [1.65.0](https://github.com/thellmwhisperer/la-roca/compare/v1.64.0...v1.65.0) (2026-08-21)


### Features

* **plugin:** register vector surface contracts ([#218](https://github.com/thellmwhisperer/la-roca/issues/218)) ([4c22c6c](https://github.com/thellmwhisperer/la-roca/commit/4c22c6cf196aaac83fbe00c36067927a0bf392d6))

## [1.64.0](https://github.com/thellmwhisperer/la-roca/compare/v1.63.0...v1.64.0) (2026-08-21)


### Features

* **corpuswriter:** export shared conversation writer API ([#209](https://github.com/thellmwhisperer/la-roca/issues/209)) ([4fa6d7f](https://github.com/thellmwhisperer/la-roca/commit/4fa6d7fb57db220fcb615568330a45cdaab53bc0))

## [1.63.0](https://github.com/thellmwhisperer/la-roca/compare/v1.62.0...v1.63.0) (2026-08-21)


### Features

* **incrementality:** export reusable unchanged-pass primitives ([#213](https://github.com/thellmwhisperer/la-roca/issues/213)) ([2236fb1](https://github.com/thellmwhisperer/la-roca/commit/2236fb16e06f80d1e5574c0b6e8a935ae10c1a08))

## [1.62.0](https://github.com/thellmwhisperer/la-roca/compare/v1.61.0...v1.62.0) (2026-08-21)


### Features

* **parsers:** export public parser package ([#210](https://github.com/thellmwhisperer/la-roca/issues/210)) ([5fd44e8](https://github.com/thellmwhisperer/la-roca/commit/5fd44e8550942a8b89d39fa4d9a895c929ccb7a9))

## [1.61.0](https://github.com/thellmwhisperer/la-roca/compare/v1.60.1...v1.61.0) (2026-08-21)


### Features

* **ingest:** export provenance mapping package ([#208](https://github.com/thellmwhisperer/la-roca/issues/208)) ([d54a986](https://github.com/thellmwhisperer/la-roca/commit/d54a986bf0ad3e02dedda610615cbab244059e6b))

## [1.60.1](https://github.com/thellmwhisperer/la-roca/compare/v1.60.0...v1.60.1) (2026-08-20)


### Bug Fixes

* **ingest:** prevent write failures from aborting the corpus ([#206](https://github.com/thellmwhisperer/la-roca/issues/206)) ([f21b579](https://github.com/thellmwhisperer/la-roca/commit/f21b579ba4b08a6789ec0403f3a5caedd8345e3c))

## [1.60.0](https://github.com/thellmwhisperer/la-roca/compare/v1.59.0...v1.60.0) (2026-08-19)


### Features

* **query:** add compound, join, and JSON SQL repairs ([#202](https://github.com/thellmwhisperer/la-roca/issues/202)) ([b477241](https://github.com/thellmwhisperer/la-roca/commit/b477241f0986e0b1a486d2b43a55dafc59ca29a1))

## [1.59.0](https://github.com/thellmwhisperer/la-roca/compare/v1.58.0...v1.59.0) (2026-08-19)


### Features

* **query:** add per-question database scoping ([#201](https://github.com/thellmwhisperer/la-roca/issues/201)) ([6d122f6](https://github.com/thellmwhisperer/la-roca/commit/6d122f6ce25c47a2f087b13c5bb35e60cb21495f))

## [1.58.0](https://github.com/thellmwhisperer/la-roca/compare/v1.57.0...v1.58.0) (2026-08-18)


### Features

* **ingest:** ingest Cursor agent-home conversations ([#195](https://github.com/thellmwhisperer/la-roca/issues/195)) ([0a3f511](https://github.com/thellmwhisperer/la-roca/commit/0a3f511c9e0f7ff61ca2474e3a4610c3a1267332))

## [1.57.0](https://github.com/thellmwhisperer/la-roca/compare/v1.56.0...v1.57.0) (2026-08-18)


### Features

* **distribution:** teach exec-first hybrid search doctrine ([#198](https://github.com/thellmwhisperer/la-roca/issues/198)) ([cd6d79a](https://github.com/thellmwhisperer/la-roca/commit/cd6d79aefc7016aac2d66e4d1cccb43f6ec39474))

## [1.56.0](https://github.com/thellmwhisperer/la-roca/compare/v1.55.0...v1.56.0) (2026-08-18)


### Features

* **vector:** remove vocab discovery command ([#196](https://github.com/thellmwhisperer/la-roca/issues/196)) ([16e1bf4](https://github.com/thellmwhisperer/la-roca/commit/16e1bf43c88b3bbae1a73eae9b065cc38b162b3f))

## [1.55.0](https://github.com/thellmwhisperer/la-roca/compare/v1.54.0...v1.55.0) (2026-08-18)


### Features

* **distribution:** auto-install La Roca skills across agent runtimes ([#193](https://github.com/thellmwhisperer/la-roca/issues/193)) ([8e6d5d8](https://github.com/thellmwhisperer/la-roca/commit/8e6d5d8b3f09328353b8c0b6030cd4753677e33c))

## [1.54.0](https://github.com/thellmwhisperer/la-roca/compare/v1.53.1...v1.54.0) (2026-08-18)


### Features

* **query:** support complete FTS schema generation and validation ([#189](https://github.com/thellmwhisperer/la-roca/issues/189)) ([3fec0b6](https://github.com/thellmwhisperer/la-roca/commit/3fec0b6d067b8777abdc4be907e3d4be8a0a7add))

## [1.53.1](https://github.com/thellmwhisperer/la-roca/compare/v1.53.0...v1.53.1) (2026-08-18)


### Bug Fixes

* **query:** remove SQL generation limits ([#188](https://github.com/thellmwhisperer/la-roca/issues/188)) ([19a3add](https://github.com/thellmwhisperer/la-roca/commit/19a3add43248d465301377ee0951fe7d8e893421))

## [1.53.0](https://github.com/thellmwhisperer/la-roca/compare/v1.52.0...v1.53.0) (2026-08-18)


### Features

* **cli:** add privacy-safe doctor support reports ([#185](https://github.com/thellmwhisperer/la-roca/issues/185)) ([8bf9ed1](https://github.com/thellmwhisperer/la-roca/commit/8bf9ed11a9f05cb092fee81b9f2726aaaff7ad8b))


### Bug Fixes

* **distribution:** migrate vector plugin to roca-vector ([#186](https://github.com/thellmwhisperer/la-roca/issues/186)) ([4efa8eb](https://github.com/thellmwhisperer/la-roca/commit/4efa8ebf1318427214df4b2fa2991563d63e35e1))

## [1.52.0](https://github.com/thellmwhisperer/la-roca/compare/v1.51.0...v1.52.0) (2026-08-18)


### Features

* **vector:** document setup and teach hybrid discovery ([#183](https://github.com/thellmwhisperer/la-roca/issues/183)) ([ee100e9](https://github.com/thellmwhisperer/la-roca/commit/ee100e9e29c872d19524be70c374df739f1ee55c))

## [1.51.0](https://github.com/thellmwhisperer/la-roca/compare/v1.50.0...v1.51.0) (2026-08-18)


### Features

* **ingest:** complete Hermes source ingestion ([#180](https://github.com/thellmwhisperer/la-roca/issues/180)) ([469a336](https://github.com/thellmwhisperer/la-roca/commit/469a336a3cb2e8ea6fbff08a1f9cb6d0d43c3cd9))

## [1.50.0](https://github.com/thellmwhisperer/la-roca/compare/v1.49.0...v1.50.0) (2026-08-18)


### Features

* **ingest:** identify OpenCode Telegram sessions ([#178](https://github.com/thellmwhisperer/la-roca/issues/178)) ([9b61e43](https://github.com/thellmwhisperer/la-roca/commit/9b61e433ccfa4080a9813cc8030a9eec281a7954))

## [1.49.0](https://github.com/thellmwhisperer/la-roca/compare/v1.48.0...v1.49.0) (2026-08-17)


### Features

* **vector:** compact paged embedding stores ([#176](https://github.com/thellmwhisperer/la-roca/issues/176)) ([d6c3c68](https://github.com/thellmwhisperer/la-roca/commit/d6c3c6887d697688c8bd97bf1d39d306c1750673))

## [1.48.0](https://github.com/thellmwhisperer/la-roca/compare/v1.47.0...v1.48.0) (2026-08-17)


### Features

* **distribution:** bundle vector with core releases ([#174](https://github.com/thellmwhisperer/la-roca/issues/174)) ([8fdb427](https://github.com/thellmwhisperer/la-roca/commit/8fdb427ec1e08137772ecd177fbc171f43ea5086))

## [1.47.0](https://github.com/thellmwhisperer/la-roca/compare/v1.46.0...v1.47.0) (2026-08-17)


### Features

* **service:** enforce and repair the live layer registry ([#172](https://github.com/thellmwhisperer/la-roca/issues/172)) ([7066fac](https://github.com/thellmwhisperer/la-roca/commit/7066fac3335ec3809531b58bcf6658152cd54b58))
* **vector:** publish installable release archives ([#171](https://github.com/thellmwhisperer/la-roca/issues/171)) ([f388c08](https://github.com/thellmwhisperer/la-roca/commit/f388c089f2b94c175dc617043cbc17870a6e5cef))

## [1.46.0](https://github.com/thellmwhisperer/la-roca/compare/v1.45.1...v1.46.0) (2026-08-17)


### Features

* **vector:** add deterministic vocabulary discovery ([#167](https://github.com/thellmwhisperer/la-roca/issues/167)) ([b24bcbe](https://github.com/thellmwhisperer/la-roca/commit/b24bcbef886f216d10072e81934d69a79776864f))

## [1.45.1](https://github.com/thellmwhisperer/la-roca/compare/v1.45.0...v1.45.1) (2026-08-17)


### Bug Fixes

* **vector:** clean session embeddings and add targeted reindexing ([#164](https://github.com/thellmwhisperer/la-roca/issues/164)) ([fe28f1b](https://github.com/thellmwhisperer/la-roca/commit/fe28f1b161c5362089bd858b7f41cb453f862a9f))

## [1.45.0](https://github.com/thellmwhisperer/la-roca/compare/v1.44.0...v1.45.0) (2026-08-17)


### Features

* **distribution:** verify complete DATA-3 corpus custody ([#166](https://github.com/thellmwhisperer/la-roca/issues/166)) ([7b2f6f3](https://github.com/thellmwhisperer/la-roca/commit/7b2f6f341c9d62ad07ff18fbac2518be09911491))


### Bug Fixes

* **ingest:** index tool-only OpenCode assistant messages ([#163](https://github.com/thellmwhisperer/la-roca/issues/163)) ([84f759d](https://github.com/thellmwhisperer/la-roca/commit/84f759d1f72946bbaba0d7e0ecca1619711c2910))

## [1.44.0](https://github.com/thellmwhisperer/la-roca/compare/v1.43.0...v1.44.0) (2026-08-17)


### Features

* **store:** enforce the exact duplicate law ([#161](https://github.com/thellmwhisperer/la-roca/issues/161)) ([28133af](https://github.com/thellmwhisperer/la-roca/commit/28133af275275f9f9439f2dd932835baa860ce4c))

## [1.43.0](https://github.com/thellmwhisperer/la-roca/compare/v1.42.0...v1.43.0) (2026-08-16)


### Features

* **skill:** two-skill suite with grok and qwen seats and semantic catalog ([#158](https://github.com/thellmwhisperer/la-roca/issues/158)) ([389ef06](https://github.com/thellmwhisperer/la-roca/commit/389ef06a3be4ba80086bd441738a4f984c6f2959))

## [1.42.0](https://github.com/thellmwhisperer/la-roca/compare/v1.41.0...v1.42.0) (2026-08-16)


### Features

* **ingest:** capture vendor export project surfaces and claude-web memories ([#157](https://github.com/thellmwhisperer/la-roca/issues/157)) ([0597ac2](https://github.com/thellmwhisperer/la-roca/commit/0597ac22e7536e7b0e24b6d126dec6e72d59115c))

## [1.41.0](https://github.com/thellmwhisperer/la-roca/compare/v1.40.1...v1.41.0) (2026-08-16)


### Features

* **ingest:** add Cursor conversation parser ([#151](https://github.com/thellmwhisperer/la-roca/issues/151)) ([c542750](https://github.com/thellmwhisperer/la-roca/commit/c542750cd2c254fbc24fbafd99fce3317effb1de))

## [1.40.1](https://github.com/thellmwhisperer/la-roca/compare/v1.40.0...v1.40.1) (2026-08-16)


### Bug Fixes

* **ingest:** close memory coverage gaps and exclude manifests from corpus ([#147](https://github.com/thellmwhisperer/la-roca/issues/147)) ([e5add10](https://github.com/thellmwhisperer/la-roca/commit/e5add1067da7d34f87f95f58f5d145a9d6b3627c))

## [1.40.0](https://github.com/thellmwhisperer/la-roca/compare/v1.39.0...v1.40.0) (2026-08-16)


### Features

* **ingest:** add Qwen Code and GLM parsers ([#149](https://github.com/thellmwhisperer/la-roca/issues/149)) ([a82f29a](https://github.com/thellmwhisperer/la-roca/commit/a82f29a6acb016b23e7b89e9173ae65507215999))

## [1.39.0](https://github.com/thellmwhisperer/la-roca/compare/v1.38.0...v1.39.0) (2026-08-16)


### Features

* **ingest:** recover OpenCode message content ([#148](https://github.com/thellmwhisperer/la-roca/issues/148)) ([c0d2035](https://github.com/thellmwhisperer/la-roca/commit/c0d20352ff826651d277c377e8f869151f7615c3))

## [1.38.0](https://github.com/thellmwhisperer/la-roca/compare/v1.37.0...v1.38.0) (2026-08-16)


### Features

* **distribution:** serve reads through the in-memory federation hub ([#144](https://github.com/thellmwhisperer/la-roca/issues/144)) ([5aefdd5](https://github.com/thellmwhisperer/la-roca/commit/5aefdd54909cd5e48db345275bab7abc4c652b72))

## [1.37.0](https://github.com/thellmwhisperer/la-roca/compare/v1.36.0...v1.37.0) (2026-08-16)


### Features

* **ingest:** complete Pi private-store coverage ([#141](https://github.com/thellmwhisperer/la-roca/issues/141)) ([9fd2467](https://github.com/thellmwhisperer/la-roca/commit/9fd2467f24017db12be8ca605cc22042688e1956))

## [1.36.0](https://github.com/thellmwhisperer/la-roca/compare/v1.35.1...v1.36.0) (2026-08-16)


### Features

* **ingest:** persist canonical harness and source-model provenance ([#136](https://github.com/thellmwhisperer/la-roca/issues/136)) ([eedb57e](https://github.com/thellmwhisperer/la-roca/commit/eedb57eacabc61b1a864af617cce08cda308a703))

## [1.35.1](https://github.com/thellmwhisperer/la-roca/compare/v1.35.0...v1.35.1) (2026-08-16)


### Bug Fixes

* **ingest:** recover all Hermes sessions and their conversational content ([#140](https://github.com/thellmwhisperer/la-roca/issues/140)) ([8afec8e](https://github.com/thellmwhisperer/la-roca/commit/8afec8ee34e9382910564db895f719d40cd1dcec))

## [1.35.0](https://github.com/thellmwhisperer/la-roca/compare/v1.34.0...v1.35.0) (2026-08-15)


### Features

* **ops:** persist durable redacted call history in roca-ops ([#132](https://github.com/thellmwhisperer/la-roca/issues/132)) ([b4a0d4e](https://github.com/thellmwhisperer/la-roca/commit/b4a0d4efbdb8ced67cb8145309067956d3fae482))

## [1.34.0](https://github.com/thellmwhisperer/la-roca/compare/v1.33.0...v1.34.0) (2026-08-15)


### Features

* **distribution:** quarantine legacy execution history in shadow DATA SPLIT import ([#130](https://github.com/thellmwhisperer/la-roca/issues/130)) ([8ea5032](https://github.com/thellmwhisperer/la-roca/commit/8ea5032ce469def85a3812d7d3ac3411c81e6f02))

## [1.33.0](https://github.com/thellmwhisperer/la-roca/compare/v1.32.0...v1.33.0) (2026-08-15)


### Features

* **distribution:** shadow memory custody in the ops plugin database ([#135](https://github.com/thellmwhisperer/la-roca/issues/135)) ([5c43a8e](https://github.com/thellmwhisperer/la-roca/commit/5c43a8ea724290f4fa163ad92fe203965566b80e))

## [1.32.0](https://github.com/thellmwhisperer/la-roca/compare/v1.31.1...v1.32.0) (2026-08-15)


### Features

* **distribution:** shadow the corpus session archive in hidden version tables ([#133](https://github.com/thellmwhisperer/la-roca/issues/133)) ([f093ef2](https://github.com/thellmwhisperer/la-roca/commit/f093ef280e6db712518f4ec42bc881461eae0155))

## [1.31.1](https://github.com/thellmwhisperer/la-roca/compare/v1.31.0...v1.31.1) (2026-08-15)


### Bug Fixes

* **ingest:** read Grok sessions from the durable update stream ([#129](https://github.com/thellmwhisperer/la-roca/issues/129)) ([24e0b1f](https://github.com/thellmwhisperer/la-roca/commit/24e0b1f7ca0e651f64ee7c0b0a2d6116a4542be2))

## [1.31.0](https://github.com/thellmwhisperer/la-roca/compare/v1.30.0...v1.31.0) (2026-08-15)


### Features

* **distribution:** migrate the bundled ops plugin onto the federated manifest ([#127](https://github.com/thellmwhisperer/la-roca/issues/127)) ([e8f438f](https://github.com/thellmwhisperer/la-roca/commit/e8f438fe702041a5fb6cace84b9a9c81dc512614))

## [1.30.0](https://github.com/thellmwhisperer/la-roca/compare/v1.29.0...v1.30.0) (2026-08-15)


### Features

* **distribution:** make bundled plugin databases self-describing and migration-resumable ([#125](https://github.com/thellmwhisperer/la-roca/issues/125)) ([fe68927](https://github.com/thellmwhisperer/la-roca/commit/fe68927c3cca399f62284ec30c96deb239aa63de))

## [1.29.0](https://github.com/thellmwhisperer/la-roca/compare/v1.28.0...v1.29.0) (2026-08-15)


### Features

* **plugins:** add federated plugin manifest engine ([#123](https://github.com/thellmwhisperer/la-roca/issues/123)) ([07b6b6d](https://github.com/thellmwhisperer/la-roca/commit/07b6b6d23048a2da60f1e21d33fb3bee9f79d8ef))

## [1.28.0](https://github.com/thellmwhisperer/la-roca/compare/v1.27.0...v1.28.0) (2026-08-15)


### Features

* **ingest:** ingest Grok Build CLI sessions as a new agent family ([#121](https://github.com/thellmwhisperer/la-roca/issues/121)) ([522fef4](https://github.com/thellmwhisperer/la-roca/commit/522fef4070c06ad8d3a21e0a74f30c8fbdbc4dda))

## [1.27.0](https://github.com/thellmwhisperer/la-roca/compare/v1.26.0...v1.27.0) (2026-08-15)


### Features

* **ingest:** add agent parser contribution kit ([#118](https://github.com/thellmwhisperer/la-roca/issues/118)) ([0e1eb9b](https://github.com/thellmwhisperer/la-roca/commit/0e1eb9bd437222204bac3a11d419e83f6856b637))

## [1.26.0](https://github.com/thellmwhisperer/la-roca/compare/v1.25.2...v1.26.0) (2026-08-14)


### Features

* **plugins:** add opt-in vector search as an isolated executable plugin ([#111](https://github.com/thellmwhisperer/la-roca/issues/111)) ([498add4](https://github.com/thellmwhisperer/la-roca/commit/498add42efcc4edecd9e3b116613247ee47e8a2f))

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
