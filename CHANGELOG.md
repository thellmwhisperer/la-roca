# Changelog

## Unreleased

### Bug Fixes

* **mcp:** retire the leftover `mcp-audit` JSONL stream. MCP calls already land in `executions`; doctor counts those failures and no longer reads leftover `mcp-audit-*.jsonl` ([#483](https://github.com/thellmwhisperer/la-roca/issues/483)). See [streams and contents](docs/operations.md#streams-and-contents).

* **migrate:** remove quadratic custody lookups and avoid repeating unchanged snapshots after interruption ([#455](https://github.com/thellmwhisperer/la-roca/issues/455)); add stage/batch progress and read-only ledger status. See [migration operations](docs/operations.md#explicit-data-split-migration) for output and resume behavior.

* **vector:** reuse the embedding resident across `mcp serve` sessions ([#315](https://github.com/thellmwhisperer/la-roca/issues/315)); see the [MCP lifecycle](docs/mcp.md#1-roca-mcp-serve-the-mcp-over-stdio) for sharing scope and shutdown behavior.

### Features

* **ingest:** one hub can read HOME-shaped mirrors of another machine via `[[sources.remote]]`. Sessions, exchanges, thinking blocks, and tool calls are stamped with that machine name; the local root uses this hostname. Removing the config entry stops reading that root and leaves already ingested rows.

* **plugins:** `roca mcp serve` raises plugin-declared session companions. A `plugin.json` may name an executable inside the plugin directory plus fixed argv; serve starts that child for the session, reaps it on exit, retries crashes with bounded backoff, and leaves a dying companion down without blocking queries. Plugins without the field are unchanged. Telemetry is JSONL under the data directory logs area, never a database.

* **vector:** ship a local embedding engine inside the vector companion. The user downloads exactly one embeddings model. macOS and Linux no longer need a separate embedding runtime; Windows keeps the previous path until its native lane exists. Indexing walks newest material first. Engine timings land in rotated JSONL under the data directory logs area, never in a database.

* **vector:** remove the `vocab` discovery verb

* remove all model credential machinery; agent models authenticate through their own CLIs

Most users need to do nothing: an already signed-in Codex or Claude CLI is detected and used automatically. Existing remote-provider configuration is tolerated and receives a migration proposal when a supported local CLI is available, or a removal proposal when none is available. A provider table that declares its own `command` keeps that command: the proposal removes only the retired authentication keys. Recovery backups made while retiring those providers are credential-redacted rather than byte-exact. If an older installation left files under `~/.roca/credentials`, La Roca no longer reads them; they never disable a working CLI transport and are offered for removal on their own. `roca init` retires nothing behind its model confirmation, and `roca update` no longer refreshes a remote model catalogue.

The bootstrap JSON field `external_credential` is now named `command_transport`; it reports that the selected model runs through a local agent CLI without implying that La Roca owns authentication.

## [1.91.0](https://github.com/thellmwhisperer/la-roca/compare/v1.90.1...v1.91.0) (2026-09-21)


### Features

* accent-insensitive search with automatic index rebuild ([#61](https://github.com/thellmwhisperer/la-roca/issues/61)) ([ba38752](https://github.com/thellmwhisperer/la-roca/commit/ba38752405c043c233c7c51fdbff515517e9ff0e))
* add shared database resident for MCP and CLI clients ([#456](https://github.com/thellmwhisperer/la-roca/issues/456)) ([cb88d5f](https://github.com/thellmwhisperer/la-roca/commit/cb88d5ff35c4fbea89400b7d36fea2bef96ae5f5))
* ChatGPT data-export ingester ([#51](https://github.com/thellmwhisperer/la-roca/issues/51)) ([3e764bd](https://github.com/thellmwhisperer/la-roca/commit/3e764bdd0f712f0309fb3c56c452da298270d9d9))
* **cli:** add canonical session context loading ([#279](https://github.com/thellmwhisperer/la-roca/issues/279)) ([1bb2d42](https://github.com/thellmwhisperer/la-roca/commit/1bb2d42faf81c653b6617c4441d4fb2dbc5d883d))
* **cli:** add pill deletion by slug ([#325](https://github.com/thellmwhisperer/la-roca/issues/325)) ([d96770d](https://github.com/thellmwhisperer/la-roca/commit/d96770d5f4da575f565a49eb63a247d8a2aefae6))
* **cli:** add privacy-safe doctor support reports ([#185](https://github.com/thellmwhisperer/la-roca/issues/185)) ([8265f4f](https://github.com/thellmwhisperer/la-roca/commit/8265f4f095555a5d8367aedaf73a75ed60688080))
* **cli:** add SSH remote query bridge ([#228](https://github.com/thellmwhisperer/la-roca/issues/228)) ([450bd05](https://github.com/thellmwhisperer/la-roca/commit/450bd0572dd8ca8b3fd0772451a41824fc311612))
* **cli:** add tool-call-observer for live session tool calls ([#254](https://github.com/thellmwhisperer/la-roca/issues/254)) ([db7d928](https://github.com/thellmwhisperer/la-roca/commit/db7d928828e56bddf5ea9bbaab6c619e3a516815))
* **cli:** create default feature config during init ([#230](https://github.com/thellmwhisperer/la-roca/issues/230)) ([d7425df](https://github.com/thellmwhisperer/la-roca/commit/d7425df806431328d6e8ee43ce3f6fe392d03a7a))
* **cli:** prove word search and ask the vector question inside init ([#255](https://github.com/thellmwhisperer/la-roca/issues/255)) ([d86ceb7](https://github.com/thellmwhisperer/la-roca/commit/d86ceb78265b0f047df21fbe87a9dd06cea55b77))
* **cli:** split model check from model set and retire login ([#100](https://github.com/thellmwhisperer/la-roca/issues/100)) ([390f0cd](https://github.com/thellmwhisperer/la-roca/commit/390f0cdc8d0e7404a0bf5dcc139a56bfa2e260a9))
* configure and parallelize hybrid query retrieval ([#403](https://github.com/thellmwhisperer/la-roca/issues/403)) ([3971d9e](https://github.com/thellmwhisperer/la-roca/commit/3971d9eff340f85609f66265c611ab5f4aa12688))
* **corpuswriter:** export shared conversation writer API ([#209](https://github.com/thellmwhisperer/la-roca/issues/209)) ([67de526](https://github.com/thellmwhisperer/la-roca/commit/67de526e70616eca1e2852fe7af16e06c23a0c3f))
* declare no-mistakes lint and test commands ([#248](https://github.com/thellmwhisperer/la-roca/issues/248)) ([36bf198](https://github.com/thellmwhisperer/la-roca/commit/36bf19866921b29d0d36c30406ff113eb7c05c22))
* detected local binaries become the zero-login factory default ([#33](https://github.com/thellmwhisperer/la-roca/issues/33)) ([677eb68](https://github.com/thellmwhisperer/la-roca/commit/677eb684051e06875e80384ac26567bec9a2ed3b))
* **distribution:** add Claude Desktop MCP install target ([#307](https://github.com/thellmwhisperer/la-roca/issues/307)) ([e5d9f9e](https://github.com/thellmwhisperer/la-roca/commit/e5d9f9e826814e4542482c54395dcac8dbabe2a8))
* **distribution:** add opt-in ZCode runtime support ([#308](https://github.com/thellmwhisperer/la-roca/issues/308)) ([19bb36e](https://github.com/thellmwhisperer/la-roca/commit/19bb36e943cd7acb0c1bdf9b5885476ac77a7d56))
* **distribution:** add the roca cron plugin ride train ([#102](https://github.com/thellmwhisperer/la-roca/issues/102)) ([5ad3109](https://github.com/thellmwhisperer/la-roca/commit/5ad31096f37a8f483788e6e1f53649d47fd5abb3))
* **distribution:** auto-install La Roca skills across agent runtimes ([#193](https://github.com/thellmwhisperer/la-roca/issues/193)) ([5755eb5](https://github.com/thellmwhisperer/la-roca/commit/5755eb5b3c0ccc2d8b60d55538866a822f194e53))
* **distribution:** bundle the inert roca-corpus harvest plugin ([#109](https://github.com/thellmwhisperer/la-roca/issues/109)) ([c8b2c46](https://github.com/thellmwhisperer/la-roca/commit/c8b2c46da3f094c113c96807872683b883f2eba1))
* **distribution:** bundle vector with core releases ([#174](https://github.com/thellmwhisperer/la-roca/issues/174)) ([96535c7](https://github.com/thellmwhisperer/la-roca/commit/96535c77def842fa2454cfed87da7922c7a61d9a))
* **distribution:** install and update owner/repo plugins from published releases ([#253](https://github.com/thellmwhisperer/la-roca/issues/253)) ([6be7109](https://github.com/thellmwhisperer/la-roca/commit/6be710992dc1bd95929d03bcff83b4ad2a28b9ba))
* **distribution:** make bundled plugin databases self-describing and migration-resumable ([#125](https://github.com/thellmwhisperer/la-roca/issues/125)) ([53955a9](https://github.com/thellmwhisperer/la-roca/commit/53955a98da459581197019caef379ee981a18a70))
* **distribution:** make custody migration explicit with roca migrate ([#370](https://github.com/thellmwhisperer/la-roca/issues/370)) ([561cb02](https://github.com/thellmwhisperer/la-roca/commit/561cb02b3e829758e8e5265286697d0b0734b42e))
* **distribution:** manage agent skill, prompt, and hook as versioned artifacts ([#95](https://github.com/thellmwhisperer/la-roca/issues/95)) ([0c07422](https://github.com/thellmwhisperer/la-roca/commit/0c07422b8186a0046b6f507a70ed2534d3478227))
* **distribution:** migrate the bundled ops plugin onto the federated manifest ([#127](https://github.com/thellmwhisperer/la-roca/issues/127)) ([b2e22ca](https://github.com/thellmwhisperer/la-roca/commit/b2e22ca2af152d5e724f471d86e3985bc2ce2bac))
* **distribution:** quarantine legacy execution history in shadow DATA SPLIT import ([#130](https://github.com/thellmwhisperer/la-roca/issues/130)) ([3bc3ed9](https://github.com/thellmwhisperer/la-roca/commit/3bc3ed92788d976d3cca2d3327fbc90c9673394b))
* **distribution:** serve reads through the in-memory federation hub ([#144](https://github.com/thellmwhisperer/la-roca/issues/144)) ([84d841b](https://github.com/thellmwhisperer/la-roca/commit/84d841bdd606843ac444cddc2f2085152a0c5a3e))
* **distribution:** shadow memory custody in the ops plugin database ([#135](https://github.com/thellmwhisperer/la-roca/issues/135)) ([b7bacd9](https://github.com/thellmwhisperer/la-roca/commit/b7bacd96ba418e05b7f2434c52935236b22964d9))
* **distribution:** shadow the corpus session archive in hidden version tables ([#133](https://github.com/thellmwhisperer/la-roca/issues/133)) ([3e9226b](https://github.com/thellmwhisperer/la-roca/commit/3e9226ba4faaffabb6abbcba8220f7fcbbcbc76a))
* **distribution:** store one current row per fact in corpus databases ([#250](https://github.com/thellmwhisperer/la-roca/issues/250)) ([63c3f26](https://github.com/thellmwhisperer/la-roca/commit/63c3f26eb754ab0b60410572709f09134d523d16))
* **distribution:** teach exec-first hybrid search doctrine ([#198](https://github.com/thellmwhisperer/la-roca/issues/198)) ([7aaa499](https://github.com/thellmwhisperer/la-roca/commit/7aaa499236950c4b55689424ab75ad645aa69e63))
* **distribution:** teach federated hybrid retrieval ([#224](https://github.com/thellmwhisperer/la-roca/issues/224)) ([9722f7e](https://github.com/thellmwhisperer/la-roca/commit/9722f7e9db8491943a9da396e7518289bd0ef660))
* **distribution:** verify complete DATA-3 corpus custody ([#166](https://github.com/thellmwhisperer/la-roca/issues/166)) ([a48e755](https://github.com/thellmwhisperer/la-roca/commit/a48e75556f67de9f186d79073ccf7a67c620c021))
* extract agent operational writes into a bundled roca-ops plugin ([#94](https://github.com/thellmwhisperer/la-roca/issues/94)) ([eb3a974](https://github.com/thellmwhisperer/la-roca/commit/eb3a9741f384ceb876f3ee23cf80d71338d54d2b))
* extract human answering into the optional playground plugin ([#359](https://github.com/thellmwhisperer/la-roca/issues/359)) ([2a53937](https://github.com/thellmwhisperer/la-roca/commit/2a5393759c654bf5e7175c1fa8b185a4103ce438))
* full audit log for every call, surfaced by doctor ([#69](https://github.com/thellmwhisperer/la-roca/issues/69)) ([db85715](https://github.com/thellmwhisperer/la-roca/commit/db857151758e7d9ef3f69fe270dc54af7baa6474))
* git-style plugin dispatch ([#28](https://github.com/thellmwhisperer/la-roca/issues/28)) ([3e84c0f](https://github.com/thellmwhisperer/la-roca/commit/3e84c0f5d07bf4d8f48e37415d6713886bf06a55))
* grounded exploration mode with plain and deep explore ([#91](https://github.com/thellmwhisperer/la-roca/issues/91)) ([f8b39bc](https://github.com/thellmwhisperer/la-roca/commit/f8b39bc2d4aa771a6000752ee23a38b6613154dc))
* **hooks:** install the same session hook on every supported harness ([48e77f9](https://github.com/thellmwhisperer/la-roca/commit/48e77f9a27d6bf0a7e9da3701ba9223f9ff31319))
* **incrementality:** export reusable unchanged-pass primitives ([#213](https://github.com/thellmwhisperer/la-roca/issues/213)) ([f2679ee](https://github.com/thellmwhisperer/la-roca/commit/f2679ee46df6c4c4962b395e314faf9b45a0ff84))
* ingest the Anthropic data export ([#12](https://github.com/thellmwhisperer/la-roca/issues/12)) ([54e51a7](https://github.com/thellmwhisperer/la-roca/commit/54e51a7f91ca03dae450286f639f96723632298e))
* ingest the sharded ChatGPT export format ([#75](https://github.com/thellmwhisperer/la-roca/issues/75)) ([d937801](https://github.com/thellmwhisperer/la-roca/commit/d9378015ca8e529ad9f5bc4722eaa748cf17faf4))
* **ingest:** add agent parser contribution kit ([#118](https://github.com/thellmwhisperer/la-roca/issues/118)) ([9a3086c](https://github.com/thellmwhisperer/la-roca/commit/9a3086c5944c02f776ce292f7590465719a6707c))
* **ingest:** add Cursor conversation parser ([#151](https://github.com/thellmwhisperer/la-roca/issues/151)) ([77a2b3a](https://github.com/thellmwhisperer/la-roca/commit/77a2b3ac63b4f3b15006cee268bff5bc14eb21a5))
* **ingest:** add Qwen Code and GLM parsers ([#149](https://github.com/thellmwhisperer/la-roca/issues/149)) ([3abac08](https://github.com/thellmwhisperer/la-roca/commit/3abac08fbff9c5874ed7af2ce428ff082c919a0c))
* **ingest:** add ZCode desktop session ingestion ([#272](https://github.com/thellmwhisperer/la-roca/issues/272)) ([9118df7](https://github.com/thellmwhisperer/la-roca/commit/9118df7301a271cb344270357b4071b200d6758a))
* **ingest:** capture vendor export project surfaces and claude-web memories ([#157](https://github.com/thellmwhisperer/la-roca/issues/157)) ([1183c4f](https://github.com/thellmwhisperer/la-roca/commit/1183c4fe8c5246ec7f10bea645ddb1a8c9ee19b5))
* **ingest:** complete Hermes source ingestion ([#180](https://github.com/thellmwhisperer/la-roca/issues/180)) ([4248b6b](https://github.com/thellmwhisperer/la-roca/commit/4248b6b46d65e5fdcb3f3f72443b5e9ac07f4052))
* **ingest:** complete Pi private-store coverage ([#141](https://github.com/thellmwhisperer/la-roca/issues/141)) ([2b78651](https://github.com/thellmwhisperer/la-roca/commit/2b78651fd97349fe5cf3e616116e66ecfa225118))
* **ingest:** export provenance mapping package ([#208](https://github.com/thellmwhisperer/la-roca/issues/208)) ([bafcae6](https://github.com/thellmwhisperer/la-roca/commit/bafcae6bc6ab1bc914e8588bf1a90a90e22ec104))
* **ingest:** identify OpenCode Telegram sessions ([#178](https://github.com/thellmwhisperer/la-roca/issues/178)) ([ade17f8](https://github.com/thellmwhisperer/la-roca/commit/ade17f8b183e189fee69f8ad983195cffb846f10))
* **ingest:** import cloud Codex conversations from OpenAI exports ([#287](https://github.com/thellmwhisperer/la-roca/issues/287)) ([0122c9f](https://github.com/thellmwhisperer/la-roca/commit/0122c9fba1f35fbd30a9aff17c5d98bc2eb08c2f))
* **ingest:** import legacy store into corpus and ops ([#235](https://github.com/thellmwhisperer/la-roca/issues/235)) ([a7592c6](https://github.com/thellmwhisperer/la-roca/commit/a7592c680491237b2f19bee970e87669616d47a4))
* **ingest:** ingest Cursor agent-home conversations ([#195](https://github.com/thellmwhisperer/la-roca/issues/195)) ([6c159bd](https://github.com/thellmwhisperer/la-roca/commit/6c159bd2ad5b33d3e05b6181a59545e5196cc51c))
* **ingest:** ingest Grok Build CLI sessions as a new agent family ([#121](https://github.com/thellmwhisperer/la-roca/issues/121)) ([7cd3469](https://github.com/thellmwhisperer/la-roca/commit/7cd34693624d0b9e94ac859dfa1518c8b0f371f8))
* **ingest:** ingest remote machine source roots ([#425](https://github.com/thellmwhisperer/la-roca/issues/425)) ([0cef9f0](https://github.com/thellmwhisperer/la-roca/commit/0cef9f0eef9e07516d249a874b6575f73d89055e))
* **ingest:** persist canonical harness and source-model provenance ([#136](https://github.com/thellmwhisperer/la-roca/issues/136)) ([e8d1ba0](https://github.com/thellmwhisperer/la-roca/commit/e8d1ba00f47574fa5c50c8ee8d4c2b0aa386299b))
* **ingest:** recover OpenCode message content ([#148](https://github.com/thellmwhisperer/la-roca/issues/148)) ([451ad87](https://github.com/thellmwhisperer/la-roca/commit/451ad87637ef76e27632b6eca426b897965b00fa))
* launch local semantic memory for agent fleets ([#1](https://github.com/thellmwhisperer/la-roca/issues/1)) ([1a8a6dc](https://github.com/thellmwhisperer/la-roca/commit/1a8a6dc5338e4db73b4aaed9adbbf0b2f6e692b5))
* local-binary provider transport ([#11](https://github.com/thellmwhisperer/la-roca/issues/11)) ([9f0b8e0](https://github.com/thellmwhisperer/la-roca/commit/9f0b8e0ca20045a0c314d2b50ae8e30ff0151629))
* one corrected retry with the gate error before degrading ([#70](https://github.com/thellmwhisperer/la-roca/issues/70)) ([a002cc9](https://github.com/thellmwhisperer/la-roca/commit/a002cc9ee65d20b3b34bcd372c53d841ec9c7100))
* **ops:** migrate memories to short numeric IDs ([#435](https://github.com/thellmwhisperer/la-roca/issues/435)) ([ff44a5b](https://github.com/thellmwhisperer/la-roca/commit/ff44a5bdd1a8650379929d43c3af0877a3677211))
* **ops:** persist durable redacted call history in roca-ops ([#132](https://github.com/thellmwhisperer/la-roca/issues/132)) ([ecfeed5](https://github.com/thellmwhisperer/la-roca/commit/ecfeed5f9dd3835c9201489280209611d27de29c))
* **parsers:** export public parser package ([#210](https://github.com/thellmwhisperer/la-roca/issues/210)) ([07b87fa](https://github.com/thellmwhisperer/la-roca/commit/07b87fa3af60218d83af81ddd34965211d466c2d))
* per-exchange provenance across all ingesters ([#39](https://github.com/thellmwhisperer/la-roca/issues/39)) ([75c8386](https://github.com/thellmwhisperer/la-roca/commit/75c83865f6047f8d9f7fe2e6dd78a60e9ccb97dd))
* plugin standard: per-plugin databases with semantic layers, attach-based querying, installer ([#87](https://github.com/thellmwhisperer/la-roca/issues/87)) ([8bc0284](https://github.com/thellmwhisperer/la-roca/commit/8bc0284bea09cb17254d7dc60d1b057705765b3d))
* **plugin:** register vector surface contracts ([#218](https://github.com/thellmwhisperer/la-roca/issues/218)) ([38854ff](https://github.com/thellmwhisperer/la-roca/commit/38854ffc9f2fa13128fa1998de960048ee53bb9b))
* **plugins:** add federated plugin manifest engine ([#123](https://github.com/thellmwhisperer/la-roca/issues/123)) ([bc14200](https://github.com/thellmwhisperer/la-roca/commit/bc142000e2b24ccf5c972bd36ca8bf4e1f359d7f))
* **plugins:** add opt-in vector search as an isolated executable plugin ([#111](https://github.com/thellmwhisperer/la-roca/issues/111)) ([a9db9da](https://github.com/thellmwhisperer/la-roca/commit/a9db9daa96323fef36299b8607769244ecd2426b))
* **plugins:** raise plugin session companions from mcp serve ([#251](https://github.com/thellmwhisperer/la-roca/issues/251)) ([1b584a1](https://github.com/thellmwhisperer/la-roca/commit/1b584a1c44ad4a974f9d9fc64ea3327dfbd915ce))
* post-update capability reconciliation ([#24](https://github.com/thellmwhisperer/la-roca/issues/24)) ([d1117cd](https://github.com/thellmwhisperer/la-roca/commit/d1117cdbd29e45733c00542101e9ce76424415cb))
* **query:** add compound, join, and JSON SQL repairs ([#202](https://github.com/thellmwhisperer/la-roca/issues/202)) ([4451919](https://github.com/thellmwhisperer/la-roca/commit/4451919f21bcf4581bfd32b576b2706daa65e0fd))
* **query:** add hybrid FTS and vector search ([#240](https://github.com/thellmwhisperer/la-roca/issues/240)) ([fd4c32d](https://github.com/thellmwhisperer/la-roca/commit/fd4c32d5c4d8e181fdd6300c81852c1ebbb23c5e))
* **query:** add per-question database scoping ([#201](https://github.com/thellmwhisperer/la-roca/issues/201)) ([9398185](https://github.com/thellmwhisperer/la-roca/commit/93981858ec6c25553404448afe11902fdfb366e3))
* **query:** support complete FTS schema generation and validation ([#189](https://github.com/thellmwhisperer/la-roca/issues/189)) ([acfc6c6](https://github.com/thellmwhisperer/la-roca/commit/acfc6c65af3dbbc7e5b6c474fac317f833f21ea0))
* remove all credential machinery: models authenticate through their own CLIs ([#59](https://github.com/thellmwhisperer/la-roca/issues/59)) ([36862fc](https://github.com/thellmwhisperer/la-roca/commit/36862fcb1609bb1b5fc390abb529150ad1fe66f1))
* repair known model SQL mistakes before the gate and validate questions first ([#66](https://github.com/thellmwhisperer/la-roca/issues/66)) ([34d2603](https://github.com/thellmwhisperer/la-roca/commit/34d2603b760ea05cfc5923dcb12f3a34b0445bbb))
* roca init lists detected agents and models and lets you choose ([#41](https://github.com/thellmwhisperer/la-roca/issues/41)) ([69cf358](https://github.com/thellmwhisperer/la-roca/commit/69cf3581ccb52e0325a90ca9d8f97110e29c28da))
* security belt: execution timeout, prompt hardening, refuse, guarded interpretation ([#85](https://github.com/thellmwhisperer/la-roca/issues/85)) ([644adc3](https://github.com/thellmwhisperer/la-roca/commit/644adc3e691c92f0e85b4bc1732478a57e366091))
* **service:** auto-supersede active project handoffs ([#436](https://github.com/thellmwhisperer/la-roca/issues/436)) ([277205d](https://github.com/thellmwhisperer/la-roca/commit/277205d55cd647bf0168fa7bc00d8f9d4bca9db5))
* **service:** enforce and repair the live layer registry ([#172](https://github.com/thellmwhisperer/la-roca/issues/172)) ([588174e](https://github.com/thellmwhisperer/la-roca/commit/588174e193c2bb639ee3476aec574c240aa8596a))
* **skill:** two-skill suite with grok and qwen seats and semantic catalog ([#158](https://github.com/thellmwhisperer/la-roca/issues/158)) ([a5c2aeb](https://github.com/thellmwhisperer/la-roca/commit/a5c2aebbb9c0a68d4e1c03b58da412c001d814b7))
* **store:** enforce the exact duplicate law ([#161](https://github.com/thellmwhisperer/la-roca/issues/161)) ([344533b](https://github.com/thellmwhisperer/la-roca/commit/344533b948947ef75024d590ffc6f74a7d413091))
* support operator-defined scheduled rides ([#405](https://github.com/thellmwhisperer/la-roca/issues/405)) ([b345304](https://github.com/thellmwhisperer/la-roca/commit/b3453049d8e49e29390242dcfc2fb8b365e29002))
* system-stamped authorship on memories ([#64](https://github.com/thellmwhisperer/la-roca/issues/64)) ([5d800af](https://github.com/thellmwhisperer/la-roca/commit/5d800afed9158122dd64bda33babdf842c778186))
* **vector:** add contextual chunking and resumable re-embedding ([#238](https://github.com/thellmwhisperer/la-roca/issues/238)) ([d580f02](https://github.com/thellmwhisperer/la-roca/commit/d580f02089f968450f5a7b1471b73f3dea32fc0d))
* **vector:** add deterministic vocabulary discovery ([#167](https://github.com/thellmwhisperer/la-roca/issues/167)) ([215e7d1](https://github.com/thellmwhisperer/la-roca/commit/215e7d111561fb39de8a50a76909a3cef62d5498))
* **vector:** add occasion-aware writer acceleration ([#269](https://github.com/thellmwhisperer/la-roca/issues/269)) ([0bf5b27](https://github.com/thellmwhisperer/la-roca/commit/0bf5b27ca2fd49d0de7a71d305d5ba49a443bbf0))
* **vector:** add optional local semantic search under roca vector ([#103](https://github.com/thellmwhisperer/la-roca/issues/103)) ([2de8acc](https://github.com/thellmwhisperer/la-roca/commit/2de8acc9358986cd7a8e53ed33771fbdd7b174c5))
* **vector:** compact paged embedding stores ([#176](https://github.com/thellmwhisperer/la-roca/issues/176)) ([c8177ef](https://github.com/thellmwhisperer/la-roca/commit/c8177efdf272c2a1663aaf1ec3c267fa0a499a0f))
* **vector:** document setup and teach hybrid discovery ([#183](https://github.com/thellmwhisperer/la-roca/issues/183)) ([5b7639f](https://github.com/thellmwhisperer/la-roca/commit/5b7639fa0eeb2d2c11c6661ecdf8b6f185520389))
* **vector:** federate queries across routed sidecars ([#222](https://github.com/thellmwhisperer/la-roca/issues/222)) ([662883c](https://github.com/thellmwhisperer/la-roca/commit/662883c9dd5d6c857f8eebca8724f6ffed34d1d1))
* **vector:** generate database-owned federated sidecars ([#220](https://github.com/thellmwhisperer/la-roca/issues/220)) ([493ef8a](https://github.com/thellmwhisperer/la-roca/commit/493ef8ac084954925fd747148a685783f4c7957b))
* **vector:** publish installable release archives ([#171](https://github.com/thellmwhisperer/la-roca/issues/171)) ([9b36b88](https://github.com/thellmwhisperer/la-roca/commit/9b36b8844dc29bf0301d0f83df4932164fe4e49d))
* **vector:** publish verified embedding model releases ([#256](https://github.com/thellmwhisperer/la-roca/issues/256)) ([c06b83d](https://github.com/thellmwhisperer/la-roca/commit/c06b83d5de7db1a4efd71210330b12811ac482d4))
* **vector:** remove vocab discovery command ([#196](https://github.com/thellmwhisperer/la-roca/issues/196)) ([e31c103](https://github.com/thellmwhisperer/la-roca/commit/e31c10370a1ac08a0c788bef834708df996b3e1e))
* **vector:** ship embedded local inference engine ([#237](https://github.com/thellmwhisperer/la-roca/issues/237)) ([5a8306a](https://github.com/thellmwhisperer/la-roca/commit/5a8306a9d44a8ff5b813c4ee3816e13e06cc082d))


### Bug Fixes

* backfill collision groups enrich the numbered original and stay idempotent ([#53](https://github.com/thellmwhisperer/la-roca/issues/53)) ([0f614e6](https://github.com/thellmwhisperer/la-roca/commit/0f614e652feacbde6fbaaee431e23270b9396660))
* claude-web parent-chain discards no longer cascade ([#15](https://github.com/thellmwhisperer/la-roca/issues/15)) ([f6fb987](https://github.com/thellmwhisperer/la-roca/commit/f6fb9875c208a702339809bf132e904436c1d7bb))
* **cli:** tell the two empty cascades apart in the model commands ([#114](https://github.com/thellmwhisperer/la-roca/issues/114)) ([d0981d8](https://github.com/thellmwhisperer/la-roca/commit/d0981d8941e837b376633389df530d28c0e6a351))
* concatenate remote cross results in Go and allow prompt-content searches ([#377](https://github.com/thellmwhisperer/la-roca/issues/377)) ([32628aa](https://github.com/thellmwhisperer/la-roca/commit/32628aa2994fc1569dc5fc724811657fac81f2cd))
* correct cleanup verification and playground delegation ([#364](https://github.com/thellmwhisperer/la-roca/issues/364)) ([958c010](https://github.com/thellmwhisperer/la-roca/commit/958c0107bd03d7c8a322efb54223145a38f8d215))
* diagnose and prevent unsafe state ownership ([#404](https://github.com/thellmwhisperer/la-roca/issues/404)) ([bcfbcd8](https://github.com/thellmwhisperer/la-roca/commit/bcfbcd892515a2483303733fe5656f2e8b97fbc0))
* **distribution:** accept MCP handoffs from runtime aliases ([#414](https://github.com/thellmwhisperer/la-roca/issues/414)) ([1cf310d](https://github.com/thellmwhisperer/la-roca/commit/1cf310db0fd539fa83face33fef900ceea8668e6))
* **distribution:** accept supported MCP handoff writers ([#417](https://github.com/thellmwhisperer/la-roca/issues/417)) ([d04628b](https://github.com/thellmwhisperer/la-roca/commit/d04628b300273487e8a9bd6f49933f564f19cbf1))
* **distribution:** bind plugin installs to one verified descriptor and gate the release redirect bound ([#106](https://github.com/thellmwhisperer/la-roca/issues/106)) ([91d7d06](https://github.com/thellmwhisperer/la-roca/commit/91d7d06e5125c868d38a965a86f86eeb4308a23c))
* **distribution:** bound session-start handoffs and add cross-project view ([#327](https://github.com/thellmwhisperer/la-roca/issues/327)) ([2363e8a](https://github.com/thellmwhisperer/la-roca/commit/2363e8a4a2ab8443b63736220c7ad0c3333defa4))
* **distribution:** harden frozen federation acceptance suite ([#434](https://github.com/thellmwhisperer/la-roca/issues/434)) ([5571820](https://github.com/thellmwhisperer/la-roca/commit/5571820c480faf2f9ccfacc188100f4b0cc198f8))
* **distribution:** honor max-chars in TOON output ([#326](https://github.com/thellmwhisperer/la-roca/issues/326)) ([ca7f73c](https://github.com/thellmwhisperer/la-roca/commit/ca7f73c8fb335ab57d0dae4493be62c9c76ce9bd))
* **distribution:** make search guidance vector-first ([#410](https://github.com/thellmwhisperer/la-roca/issues/410)) ([01ea4a5](https://github.com/thellmwhisperer/la-roca/commit/01ea4a5d9d85e77101e77b17b6a0d8c923c9f964))
* **distribution:** migrate vector plugin to roca-vector ([#186](https://github.com/thellmwhisperer/la-roca/issues/186)) ([88267c6](https://github.com/thellmwhisperer/la-roca/commit/88267c6b40cec0d485bcd35931850886fcf3e32a))
* **distribution:** remove historical ops audit storage ([#383](https://github.com/thellmwhisperer/la-roca/issues/383)) ([2a9fab4](https://github.com/thellmwhisperer/la-roca/commit/2a9fab46ac606af3bb5152e08a6cc2c309402596))
* **distribution:** remove the built-in tool-call observer ([#356](https://github.com/thellmwhisperer/la-roca/issues/356)) ([673b3f9](https://github.com/thellmwhisperer/la-roca/commit/673b3f9c42f1d7914cf302a6eeb267f492caaafe))
* **distribution:** share resident vector query help ([#419](https://github.com/thellmwhisperer/la-roca/issues/419)) ([1899655](https://github.com/thellmwhisperer/la-roca/commit/18996558b11ed37a3aee0f607a41ee781453d046))
* **distribution:** speed up custody migration and report progress ([#462](https://github.com/thellmwhisperer/la-roca/issues/462)) ([6f2c453](https://github.com/thellmwhisperer/la-roca/commit/6f2c453e5cf15b12912c1d754df52f7bdad02166))
* **distribution:** tolerate session collisions during corpus placement ([#443](https://github.com/thellmwhisperer/la-roca/issues/443)) ([9a33b12](https://github.com/thellmwhisperer/la-roca/commit/9a33b12f1262205b49db91cba550f090c967a21f))
* **distribution:** use JSONL as the sole runtime audit destination ([#379](https://github.com/thellmwhisperer/la-roca/issues/379)) ([d74e183](https://github.com/thellmwhisperer/la-roca/commit/d74e183c81529ff0616a1fbcb9ceebaee5d6cd51))
* **hooks:** read the artifact registry before writing a session script ([0ba6692](https://github.com/thellmwhisperer/la-roca/commit/0ba669227da88bfe4aaf7e9b6bba0319f8b77007))
* **hooks:** say what Codex needs, and record what live sessions found ([a3b4b7d](https://github.com/thellmwhisperer/la-roca/commit/a3b4b7d67d4ded1759c55e362c6e85361e738431))
* **ingest:** close memory coverage gaps and exclude manifests from corpus ([#147](https://github.com/thellmwhisperer/la-roca/issues/147)) ([4df3589](https://github.com/thellmwhisperer/la-roca/commit/4df3589f02d1aa6ca820d365fcd803435600e7e8))
* **ingest:** continue Codex history reconciliation after exact-payload collisions ([#321](https://github.com/thellmwhisperer/la-roca/issues/321)) ([786f115](https://github.com/thellmwhisperer/la-roca/commit/786f115a6f833304a9aac33cf5e1fc11ecde36f6))
* **ingest:** continue legacy imports after exact-payload overlaps ([#243](https://github.com/thellmwhisperer/la-roca/issues/243)) ([4cd890d](https://github.com/thellmwhisperer/la-roca/commit/4cd890dfaf116dc46b7518a48dd756bb7081399f))
* **ingest:** index tool-only OpenCode assistant messages ([#163](https://github.com/thellmwhisperer/la-roca/issues/163)) ([7baeb96](https://github.com/thellmwhisperer/la-roca/commit/7baeb96158c8ba196a5f0b7d44cba7913ffe499e))
* **ingest:** make account exports one-shot imports instead of standing sources ([#98](https://github.com/thellmwhisperer/la-roca/issues/98)) ([9aea273](https://github.com/thellmwhisperer/la-roca/commit/9aea273f93c1bca4acd6062dbd436ef24778d8cd))
* **ingest:** prevent write failures from aborting the corpus ([#206](https://github.com/thellmwhisperer/la-roca/issues/206)) ([9ae34a1](https://github.com/thellmwhisperer/la-roca/commit/9ae34a10e4b236ac7b488510f3135f8db9ac6ec8))
* **ingest:** read Grok sessions from the durable update stream ([#129](https://github.com/thellmwhisperer/la-roca/issues/129)) ([a6ce158](https://github.com/thellmwhisperer/la-roca/commit/a6ce158e3a428188e358a4f86a45c0b9d9f21127))
* **ingest:** recover all Hermes sessions and their conversational content ([#140](https://github.com/thellmwhisperer/la-roca/issues/140)) ([34cc532](https://github.com/thellmwhisperer/la-roca/commit/34cc532c60b30c0805ab1b2b6325d2aef4a02e61))
* **ingest:** recover legacy Codex prompts from fossil rollouts ([#112](https://github.com/thellmwhisperer/la-roca/issues/112)) ([9898d4e](https://github.com/thellmwhisperer/la-roca/commit/9898d4e0f5d35d265cc90fe50c91d7e6b819ac3b))
* **ingest:** reunite Codex sessions under exact source thread IDs ([#394](https://github.com/thellmwhisperer/la-roca/issues/394)) ([0a8b34f](https://github.com/thellmwhisperer/la-roca/commit/0a8b34fe7d0af8f165fd01b9131486eb50d310eb))
* isolate malformed ChatGPT conversation envelopes ([#55](https://github.com/thellmwhisperer/la-roca/issues/55)) ([76d858c](https://github.com/thellmwhisperer/la-roca/commit/76d858c54f722eae37f3d2d2141a798c9df30c5c))
* normalize timestamps in provenance anchor matching ([#49](https://github.com/thellmwhisperer/la-roca/issues/49)) ([d6e3aa7](https://github.com/thellmwhisperer/la-roca/commit/d6e3aa7f8b10987a0f4eab98500938cae93860e4))
* parse 2025 codex rollout format ([#62](https://github.com/thellmwhisperer/la-roca/issues/62)) ([2971291](https://github.com/thellmwhisperer/la-roca/commit/29712910b40cc51023dce2fb79f410379bfe18e7))
* **parsers:** preserve Codex failures and orphan tool telemetry ([#278](https://github.com/thellmwhisperer/la-roca/issues/278)) ([45e2a70](https://github.com/thellmwhisperer/la-roca/commit/45e2a70dc3f8d177551e110469a99a9fff8a1256))
* **plugins:** an undeclared table is an orphan, not a broken plugin ([#463](https://github.com/thellmwhisperer/la-roca/issues/463)) ([24ada91](https://github.com/thellmwhisperer/la-roca/commit/24ada91473e143eb791e0a4db2719c76542a695d))
* **plugin:** treat operator rides as ordinary configuration ([#407](https://github.com/thellmwhisperer/la-roca/issues/407)) ([23c5278](https://github.com/thellmwhisperer/la-roca/commit/23c52782bee36f46a7c117485f633dcdec53104b))
* preserve exact memory IDs across JSON and MCP ([#329](https://github.com/thellmwhisperer/la-roca/issues/329)) ([f762dd9](https://github.com/thellmwhisperer/la-roca/commit/f762dd975be0b8d3b984cdbb95c01e7e148ea73a))
* preserve vector query replies received before disconnect ([#376](https://github.com/thellmwhisperer/la-roca/issues/376)) ([8f1705c](https://github.com/thellmwhisperer/la-roca/commit/8f1705cc2dd3046669928cc82d4b30c1f438de64))
* provenance backfill matches historical rows by content anchor ([#47](https://github.com/thellmwhisperer/la-roca/issues/47)) ([5c0e739](https://github.com/thellmwhisperer/la-roca/commit/5c0e739f45c4932dfaab5c5746e71f1194db8f56))
* **provider:** isolate cursor connections and attachment lifetimes ([#386](https://github.com/thellmwhisperer/la-roca/issues/386)) ([1727f73](https://github.com/thellmwhisperer/la-roca/commit/1727f73496fd319c81d1170d51d394034a21ba41))
* **provider:** reject unqualified exec tables with qualified candidates ([#330](https://github.com/thellmwhisperer/la-roca/issues/330)) ([8acd769](https://github.com/thellmwhisperer/la-roca/commit/8acd7690b3069341c730f2a1af134e0a5ee3b68e))
* **provider:** route FTS queries to persistent plugin indexes ([#375](https://github.com/thellmwhisperer/la-roca/issues/375)) ([4294409](https://github.com/thellmwhisperer/la-roca/commit/429440911642e2cfe6c390b51cc6e686fdaed79e))
* **query:** make hybrid search resilient across the federation ([#245](https://github.com/thellmwhisperer/la-roca/issues/245)) ([adbdc0c](https://github.com/thellmwhisperer/la-roca/commit/adbdc0c6410bc27729b4412e57641aa079311580))
* **query:** remove SQL generation limits ([#188](https://github.com/thellmwhisperer/la-roca/issues/188)) ([563b490](https://github.com/thellmwhisperer/la-roca/commit/563b490bc2ba1701f7f166837673ef0c32b06baa))
* recommend WSL for Windows installation ([#275](https://github.com/thellmwhisperer/la-roca/issues/275)) ([eab36d1](https://github.com/thellmwhisperer/la-roca/commit/eab36d1a52a6a473a98dd71191b7a28e4a5912ba))
* release tags can ship: the refusal scenario stamps its own dev build ([#8](https://github.com/thellmwhisperer/la-roca/issues/8)) ([e05ede1](https://github.com/thellmwhisperer/la-roca/commit/e05ede1691de307631b20cc1b7a4b60aa280a210))
* repair and retry runtime FTS errors, keep subjects in previews, complete audit failures ([#80](https://github.com/thellmwhisperer/la-roca/issues/80)) ([85def05](https://github.com/thellmwhisperer/la-roca/commit/85def058344ecd303280138b09476b0bcf4b9871))
* replay provenance backfill for v2 corpora ([#45](https://github.com/thellmwhisperer/la-roca/issues/45)) ([0a6ef70](https://github.com/thellmwhisperer/la-roca/commit/0a6ef70482c018f51753ea02a4c9544ca97471f6))
* **service:** enforce exec timeouts across full SQLite scans ([#441](https://github.com/thellmwhisperer/la-roca/issues/441)) ([6953d85](https://github.com/thellmwhisperer/la-roca/commit/6953d8554d4522ef60e47ecbb5cf1641c6ebbe9e))
* **service:** speed up and harden hybrid search ([#421](https://github.com/thellmwhisperer/la-roca/issues/421)) ([9d77f41](https://github.com/thellmwhisperer/la-roca/commit/9d77f4149a3f2445c9bc604bc99f476e5c4e0eb9))
* **store:** reap lease-proven snapshot orphans ([#302](https://github.com/thellmwhisperer/la-roca/issues/302)) ([fd3a7a4](https://github.com/thellmwhisperer/la-roca/commit/fd3a7a43f13b7d8388f440556da548810e6f343a))
* **store:** repair concurrent schema drift during adoption ([#468](https://github.com/thellmwhisperer/la-roca/issues/468)) ([#469](https://github.com/thellmwhisperer/la-roca/issues/469)) ([39fee4e](https://github.com/thellmwhisperer/la-roca/commit/39fee4e354bb6f905f7e7dd059817a6b7dca97a2))
* **store:** replace read-only snapshots with live database reads ([#342](https://github.com/thellmwhisperer/la-roca/issues/342)) ([7bf58c7](https://github.com/thellmwhisperer/la-roca/commit/7bf58c78a25b33977b61c98c41641b69de0e3d0c))
* unblock schema adoption and corpus updates ([#438](https://github.com/thellmwhisperer/la-roca/issues/438)) ([f8fd153](https://github.com/thellmwhisperer/la-roca/commit/f8fd15341b4e21b6392bf3bcf575c0be4f515c56))
* **vector:** avoid hashing unchanged sources and re-chunking for status ([#371](https://github.com/thellmwhisperer/la-roca/issues/371)) ([1f3f741](https://github.com/thellmwhisperer/la-roca/commit/1f3f741e7dfc1e89cd2ae871b687720869074f87))
* **vector:** avoid rehashing unchanged embedding models ([#392](https://github.com/thellmwhisperer/la-roca/issues/392)) ([2f0bcb3](https://github.com/thellmwhisperer/la-roca/commit/2f0bcb3756898b09e4e30835bb04492c70589256))
* **vector:** batch federation indexing and persist progress per batch ([#368](https://github.com/thellmwhisperer/la-roca/issues/368)) ([9cd7b3d](https://github.com/thellmwhisperer/la-roca/commit/9cd7b3d01a14571e71f9bf5c801a7cdc5e847cae))
* **vector:** clean session embeddings and add targeted reindexing ([#164](https://github.com/thellmwhisperer/la-roca/issues/164)) ([d7830a6](https://github.com/thellmwhisperer/la-roca/commit/d7830a649fd549955715db98f8490c39d1129b13))
* **vector:** fail fast when embedding work or status stalls ([#423](https://github.com/thellmwhisperer/la-roca/issues/423)) ([c7a09c8](https://github.com/thellmwhisperer/la-roca/commit/c7a09c8a83ef50c98c9c1ac7e861244cfda8cb5b))
* **vector:** improve sidecar diagnostics and query startup ([#338](https://github.com/thellmwhisperer/la-roca/issues/338)) ([6dfda90](https://github.com/thellmwhisperer/la-roca/commit/6dfda9017788fb9d2a3c69477aad25acf02e2c41))
* **vector:** make delta ingest exclusive and observable ([#430](https://github.com/thellmwhisperer/la-roca/issues/430)) ([58e24ef](https://github.com/thellmwhisperer/la-roca/commit/58e24ef4537cc264667ba61543f0288465ac2512))
* **vector:** preserve ID indexes during source resolution ([#399](https://github.com/thellmwhisperer/la-roca/issues/399)) ([d074f66](https://github.com/thellmwhisperer/la-roca/commit/d074f664ac5f8757ba876e290dc98ea61690c983))
* **vector:** prevent delta ingest hangs under Metal contention ([#257](https://github.com/thellmwhisperer/la-roca/issues/257)) ([1986bc5](https://github.com/thellmwhisperer/la-roca/commit/1986bc51cab48212f1dc557abd9944c82c8bc9c8))
* **vector:** prevent indexing and status hangs ([#293](https://github.com/thellmwhisperer/la-roca/issues/293)) ([34d44e7](https://github.com/thellmwhisperer/la-roca/commit/34d44e79fed197e5d8cd5f3adb530d33e702bd18))
* **vector:** recover from trapped native indexing calls ([#298](https://github.com/thellmwhisperer/la-roca/issues/298)) ([c938bfc](https://github.com/thellmwhisperer/la-roca/commit/c938bfcdbb521e45fef08c3455582514dec77099))
* **vector:** remove legacy paging and unify migration retries ([#390](https://github.com/thellmwhisperer/la-roca/issues/390)) ([509c571](https://github.com/thellmwhisperer/la-roca/commit/509c571164a7bf01a4dd33eca81de8e0e73327a5))
* **vector:** report honest per-database vector status ([#303](https://github.com/thellmwhisperer/la-roca/issues/303)) ([3b57055](https://github.com/thellmwhisperer/la-roca/commit/3b57055ed3d3c575c5340bdc4ed02e985b7baeb3))
* **vector:** restore default queries across all ready sidecars ([#388](https://github.com/thellmwhisperer/la-roca/issues/388)) ([48a7548](https://github.com/thellmwhisperer/la-roca/commit/48a7548f2d906ca3cebdcd392cc18f1aa53cf247))
* **vector:** reuse legacy embeddings in federated sidecars ([#226](https://github.com/thellmwhisperer/la-roca/issues/226)) ([c91240c](https://github.com/thellmwhisperer/la-roca/commit/c91240c4eb6b401d10e303b3ac19322f012e0f79))
* **vector:** reuse one core reader per ingest or query ([#381](https://github.com/thellmwhisperer/la-roca/issues/381)) ([7116be4](https://github.com/thellmwhisperer/la-roca/commit/7116be4a98aa91c9c4e99705fe11ea7b489638be))
* **vector:** reuse the shared embedding resident for CLI queries ([#340](https://github.com/thellmwhisperer/la-roca/issues/340)) ([74e2392](https://github.com/thellmwhisperer/la-roca/commit/74e2392aa72a783e4fb860d4a5fa3df0d445ef8d))
* **vector:** share embedding resident across MCP sessions ([#331](https://github.com/thellmwhisperer/la-roca/issues/331)) ([87654d0](https://github.com/thellmwhisperer/la-roca/commit/87654d09da88350797ecf268c8998c1392acc80f))

## [1.90.1](https://github.com/thellmwhisperer/la-roca/compare/v1.90.0...v1.90.1) (2026-09-20)


### Bug Fixes

* **distribution:** speed up custody migration and report progress ([#462](https://github.com/thellmwhisperer/la-roca/issues/462)) ([3911e07](https://github.com/thellmwhisperer/la-roca/commit/3911e0780bf5917a60d4faebeb0cbe10357d7a78))
* **plugins:** an undeclared table is an orphan, not a broken plugin ([#463](https://github.com/thellmwhisperer/la-roca/issues/463)) ([213b4da](https://github.com/thellmwhisperer/la-roca/commit/213b4da377333509dc93c87651db5c66ee584e97))
* **store:** repair concurrent schema drift during adoption ([#468](https://github.com/thellmwhisperer/la-roca/issues/468)) ([#469](https://github.com/thellmwhisperer/la-roca/issues/469)) ([2fee7ff](https://github.com/thellmwhisperer/la-roca/commit/2fee7ffa5e12cec04ec86ba3a2aa05f3bbf2ba6d))

## [1.90.0](https://github.com/thellmwhisperer/la-roca/compare/v1.89.0...v1.90.0) (2026-09-19)


### Features

* add shared database resident for MCP and CLI clients ([#456](https://github.com/thellmwhisperer/la-roca/issues/456)) ([31d3244](https://github.com/thellmwhisperer/la-roca/commit/31d3244fe52916b1f088aab1d533b20f193f227a))

## [1.89.0](https://github.com/thellmwhisperer/la-roca/compare/v1.88.2...v1.89.0) (2026-09-19)


### Features

* **ops:** migrate memories to short numeric IDs ([#435](https://github.com/thellmwhisperer/la-roca/issues/435)) ([dfd5d98](https://github.com/thellmwhisperer/la-roca/commit/dfd5d98bc7c52ebe34c691ab19a6d467644ee455))

## [1.88.2](https://github.com/thellmwhisperer/la-roca/compare/v1.88.1...v1.88.2) (2026-09-19)


### Bug Fixes

* **service:** enforce exec timeouts across full SQLite scans ([#441](https://github.com/thellmwhisperer/la-roca/issues/441)) ([1e961e6](https://github.com/thellmwhisperer/la-roca/commit/1e961e63285d92737443e878f64ca234464c4a57))

## [1.88.1](https://github.com/thellmwhisperer/la-roca/compare/v1.88.0...v1.88.1) (2026-09-19)


### Bug Fixes

* **distribution:** tolerate session collisions during corpus placement ([#443](https://github.com/thellmwhisperer/la-roca/issues/443)) ([d8ff877](https://github.com/thellmwhisperer/la-roca/commit/d8ff87712c16c1cfff72612aa539566e977c377f))

## [1.88.0](https://github.com/thellmwhisperer/la-roca/compare/v1.87.2...v1.88.0) (2026-09-19)


### Features

* **service:** auto-supersede active project handoffs ([#436](https://github.com/thellmwhisperer/la-roca/issues/436)) ([69b72b2](https://github.com/thellmwhisperer/la-roca/commit/69b72b2a6d05870718daf28fee74f8d5b3531965))


### Bug Fixes

* **distribution:** harden frozen federation acceptance suite ([#434](https://github.com/thellmwhisperer/la-roca/issues/434)) ([75d4385](https://github.com/thellmwhisperer/la-roca/commit/75d43856d278ba595243ce5f80fcb2f00cae71c5))

## [1.87.2](https://github.com/thellmwhisperer/la-roca/compare/v1.87.1...v1.87.2) (2026-09-19)


### Bug Fixes

* unblock schema adoption and corpus updates ([#438](https://github.com/thellmwhisperer/la-roca/issues/438)) ([1245ccc](https://github.com/thellmwhisperer/la-roca/commit/1245ccc9d7a3e46fa13544ad3643226847aaf9d7))

## [1.87.1](https://github.com/thellmwhisperer/la-roca/compare/v1.87.0...v1.87.1) (2026-09-19)


### Bug Fixes

* **vector:** make delta ingest exclusive and observable ([#430](https://github.com/thellmwhisperer/la-roca/issues/430)) ([bdc597b](https://github.com/thellmwhisperer/la-roca/commit/bdc597b969153fc4509867c4b6b8ef22f4036321))

## [1.87.0](https://github.com/thellmwhisperer/la-roca/compare/v1.86.6...v1.87.0) (2026-09-19)


### Features

* **ingest:** ingest remote machine source roots ([#425](https://github.com/thellmwhisperer/la-roca/issues/425)) ([488a561](https://github.com/thellmwhisperer/la-roca/commit/488a561046e243e4453f1279101117dd160a7c13))

## [1.86.6](https://github.com/thellmwhisperer/la-roca/compare/v1.86.5...v1.86.6) (2026-09-18)


### Bug Fixes

* **vector:** fail fast when embedding work or status stalls ([#423](https://github.com/thellmwhisperer/la-roca/issues/423)) ([37fa355](https://github.com/thellmwhisperer/la-roca/commit/37fa355ff0f8b0bc99370af18d6557b0c31c7642))

## [1.86.5](https://github.com/thellmwhisperer/la-roca/compare/v1.86.4...v1.86.5) (2026-09-18)


### Bug Fixes

* **service:** speed up and harden hybrid search ([#421](https://github.com/thellmwhisperer/la-roca/issues/421)) ([75a06ef](https://github.com/thellmwhisperer/la-roca/commit/75a06efac49a34d0dbda7c834cebfe5f668f4f8e))

## [1.86.4](https://github.com/thellmwhisperer/la-roca/compare/v1.86.3...v1.86.4) (2026-09-16)


### Bug Fixes

* **distribution:** share resident vector query help ([#419](https://github.com/thellmwhisperer/la-roca/issues/419)) ([1f54739](https://github.com/thellmwhisperer/la-roca/commit/1f54739d45e4aa8a4ecf7c214bda47721436a341))

## [1.86.3](https://github.com/thellmwhisperer/la-roca/compare/v1.86.2...v1.86.3) (2026-09-16)


### Bug Fixes

* **distribution:** accept supported MCP handoff writers ([#417](https://github.com/thellmwhisperer/la-roca/issues/417)) ([1b323e9](https://github.com/thellmwhisperer/la-roca/commit/1b323e987e2213e4273e82000b731aa3e1e7ef21))

## [1.86.2](https://github.com/thellmwhisperer/la-roca/compare/v1.86.1...v1.86.2) (2026-09-16)


### Bug Fixes

* **distribution:** accept MCP handoffs from runtime aliases ([#414](https://github.com/thellmwhisperer/la-roca/issues/414)) ([bc738d8](https://github.com/thellmwhisperer/la-roca/commit/bc738d86c629c284c4732e7fc3acffbfa82972c9))

## [1.86.1](https://github.com/thellmwhisperer/la-roca/compare/v1.86.0...v1.86.1) (2026-09-16)


### Bug Fixes

* **distribution:** make search guidance vector-first ([#410](https://github.com/thellmwhisperer/la-roca/issues/410)) ([ce1040e](https://github.com/thellmwhisperer/la-roca/commit/ce1040e53eaabdaecc2866fc26239c3a55f8ea37))

## [1.86.0](https://github.com/thellmwhisperer/la-roca/compare/v1.85.1...v1.86.0) (2026-09-16)


### Features

* configure and parallelize hybrid query retrieval ([#403](https://github.com/thellmwhisperer/la-roca/issues/403)) ([676a650](https://github.com/thellmwhisperer/la-roca/commit/676a65063215f0afdeebcb88b4400392c20f5da7))
* support operator-defined scheduled rides ([#405](https://github.com/thellmwhisperer/la-roca/issues/405)) ([347a9ba](https://github.com/thellmwhisperer/la-roca/commit/347a9bad5e31cc622785745e0fe7fc47466490a9))


### Bug Fixes

* diagnose and prevent unsafe state ownership ([#404](https://github.com/thellmwhisperer/la-roca/issues/404)) ([e8ff10d](https://github.com/thellmwhisperer/la-roca/commit/e8ff10ddfc371aebea1b0da6e0b5068dcd75f9fc))
* **plugin:** treat operator rides as ordinary configuration ([#407](https://github.com/thellmwhisperer/la-roca/issues/407)) ([05bda74](https://github.com/thellmwhisperer/la-roca/commit/05bda741e9852de0377829a77f6c3b8906dc31bc))

## [1.85.1](https://github.com/thellmwhisperer/la-roca/compare/v1.85.0...v1.85.1) (2026-09-15)


### Bug Fixes

* **vector:** preserve ID indexes during source resolution ([#399](https://github.com/thellmwhisperer/la-roca/issues/399)) ([b5a775d](https://github.com/thellmwhisperer/la-roca/commit/b5a775ddd81e2e165c706178bb60edaa6ecc3f0f))

## [1.85.0](https://github.com/thellmwhisperer/la-roca/compare/v1.84.10...v1.85.0) (2026-09-14)


### Features

* **hooks:** install the same session hook on every supported harness ([4f504f6](https://github.com/thellmwhisperer/la-roca/commit/4f504f647438d61014441515bbc6361e7a10d2d1))


### Bug Fixes

* **hooks:** read the artifact registry before writing a session script ([d461120](https://github.com/thellmwhisperer/la-roca/commit/d4611200324f97c27dc91ab325f597f5edaf1afa))
* **hooks:** say what Codex needs, and record what live sessions found ([164380e](https://github.com/thellmwhisperer/la-roca/commit/164380e5489addf85ae5c127f82397a82e3151e6))

## [1.84.10](https://github.com/thellmwhisperer/la-roca/compare/v1.84.9...v1.84.10) (2026-09-09)


### Bug Fixes

* **ingest:** reunite Codex sessions under exact source thread IDs ([#394](https://github.com/thellmwhisperer/la-roca/issues/394)) ([4dfb0cb](https://github.com/thellmwhisperer/la-roca/commit/4dfb0cbfa9c469bd1cedd5ca17fb9549209634d2))

## [1.84.9](https://github.com/thellmwhisperer/la-roca/compare/v1.84.8...v1.84.9) (2026-09-09)


### Bug Fixes

* **vector:** avoid rehashing unchanged embedding models ([#392](https://github.com/thellmwhisperer/la-roca/issues/392)) ([4ea5b62](https://github.com/thellmwhisperer/la-roca/commit/4ea5b621a79eb84df797c0ad91f57f99580a7077))

## [1.84.8](https://github.com/thellmwhisperer/la-roca/compare/v1.84.7...v1.84.8) (2026-09-09)


### Bug Fixes

* **vector:** remove legacy paging and unify migration retries ([#390](https://github.com/thellmwhisperer/la-roca/issues/390)) ([fda49d4](https://github.com/thellmwhisperer/la-roca/commit/fda49d48e73662aa1e9dcfda961f8a1cb8e43b66))

## [1.84.7](https://github.com/thellmwhisperer/la-roca/compare/v1.84.6...v1.84.7) (2026-09-09)


### Bug Fixes

* **vector:** restore default queries across all ready sidecars ([#388](https://github.com/thellmwhisperer/la-roca/issues/388)) ([6ba1975](https://github.com/thellmwhisperer/la-roca/commit/6ba1975b3193cacb3c664f7d64281bd847f933c1))

## [1.84.6](https://github.com/thellmwhisperer/la-roca/compare/v1.84.5...v1.84.6) (2026-09-09)


### Bug Fixes

* **provider:** isolate cursor connections and attachment lifetimes ([#386](https://github.com/thellmwhisperer/la-roca/issues/386)) ([e682d18](https://github.com/thellmwhisperer/la-roca/commit/e682d18fbf619dc8945c0c6020cf4338521d4c98))

## [1.84.5](https://github.com/thellmwhisperer/la-roca/compare/v1.84.4...v1.84.5) (2026-09-09)


### Bug Fixes

* **distribution:** remove historical ops audit storage ([#383](https://github.com/thellmwhisperer/la-roca/issues/383)) ([669c7c7](https://github.com/thellmwhisperer/la-roca/commit/669c7c7cd79b0718223a8f41ec84f175f88e4861))

## [1.84.4](https://github.com/thellmwhisperer/la-roca/compare/v1.84.3...v1.84.4) (2026-09-09)


### Bug Fixes

* **vector:** reuse one core reader per ingest or query ([#381](https://github.com/thellmwhisperer/la-roca/issues/381)) ([815263c](https://github.com/thellmwhisperer/la-roca/commit/815263cf9644181a7869c392568c9664af7fc2ee))

## [1.84.3](https://github.com/thellmwhisperer/la-roca/compare/v1.84.2...v1.84.3) (2026-09-09)


### Bug Fixes

* **distribution:** use JSONL as the sole runtime audit destination ([#379](https://github.com/thellmwhisperer/la-roca/issues/379)) ([d6e4803](https://github.com/thellmwhisperer/la-roca/commit/d6e4803112bdb02b080799677f77480256c227e1))

## [1.84.2](https://github.com/thellmwhisperer/la-roca/compare/v1.84.1...v1.84.2) (2026-09-09)


### Bug Fixes

* concatenate remote cross results in Go and allow prompt-content searches ([#377](https://github.com/thellmwhisperer/la-roca/issues/377)) ([01212c9](https://github.com/thellmwhisperer/la-roca/commit/01212c953c6a9ca060c582204c45f646bc47741d))

## [1.84.1](https://github.com/thellmwhisperer/la-roca/compare/v1.84.0...v1.84.1) (2026-09-09)


### Bug Fixes

* preserve vector query replies received before disconnect ([#376](https://github.com/thellmwhisperer/la-roca/issues/376)) ([a43ba8d](https://github.com/thellmwhisperer/la-roca/commit/a43ba8d77811f578f36456e485032bd059e7b884))
* **provider:** route FTS queries to persistent plugin indexes ([#375](https://github.com/thellmwhisperer/la-roca/issues/375)) ([27d1269](https://github.com/thellmwhisperer/la-roca/commit/27d1269dda2c70a419ad1e71fbe256272e4ce7d5))
* **vector:** avoid hashing unchanged sources and re-chunking for status ([#371](https://github.com/thellmwhisperer/la-roca/issues/371)) ([e38e384](https://github.com/thellmwhisperer/la-roca/commit/e38e3845c4a8d6c9d7085c8612ed42d1e335e95f))

## [1.84.0](https://github.com/thellmwhisperer/la-roca/compare/v1.83.3...v1.84.0) (2026-09-09)


### Features

* **distribution:** make custody migration explicit with roca migrate ([#370](https://github.com/thellmwhisperer/la-roca/issues/370)) ([88ffca8](https://github.com/thellmwhisperer/la-roca/commit/88ffca876c193227e8dc74f82906e55ad6f2389f))

## [1.83.3](https://github.com/thellmwhisperer/la-roca/compare/v1.83.2...v1.83.3) (2026-09-09)


### Bug Fixes

* **vector:** batch federation indexing and persist progress per batch ([#368](https://github.com/thellmwhisperer/la-roca/issues/368)) ([7bbc77d](https://github.com/thellmwhisperer/la-roca/commit/7bbc77d60d226f9e033b71290b6dac2075c1c136))

## [1.83.2](https://github.com/thellmwhisperer/la-roca/compare/v1.83.1...v1.83.2) (2026-09-08)


### Bug Fixes

* correct cleanup verification and playground delegation ([#364](https://github.com/thellmwhisperer/la-roca/issues/364)) ([0299152](https://github.com/thellmwhisperer/la-roca/commit/029915296549ea8269c21d7b8f2ae3d538621bef))

## [1.83.1](https://github.com/thellmwhisperer/la-roca/compare/v1.83.0...v1.83.1) (2026-09-08)


### Bug Fixes

* **vector:** reuse the shared embedding resident for CLI queries ([#340](https://github.com/thellmwhisperer/la-roca/issues/340)) ([9ee3ff1](https://github.com/thellmwhisperer/la-roca/commit/9ee3ff1a9a039b8a719fb86d7aaa93515b58c391))

## [1.83.0](https://github.com/thellmwhisperer/la-roca/compare/v1.82.6...v1.83.0) (2026-09-08)


### Features

* extract human answering into the optional playground plugin ([#359](https://github.com/thellmwhisperer/la-roca/issues/359)) ([5f43089](https://github.com/thellmwhisperer/la-roca/commit/5f43089f92ccd8fd1f84540ed56dc542e0ea6b4f))

## [1.82.6](https://github.com/thellmwhisperer/la-roca/compare/v1.82.5...v1.82.6) (2026-09-08)


### Bug Fixes

* **distribution:** remove the built-in tool-call observer ([#356](https://github.com/thellmwhisperer/la-roca/issues/356)) ([93bf518](https://github.com/thellmwhisperer/la-roca/commit/93bf518583f8db37bb9ba6c5156e5c271283269b))

## [1.82.5](https://github.com/thellmwhisperer/la-roca/compare/v1.82.4...v1.82.5) (2026-09-08)


### Bug Fixes

* **store:** replace read-only snapshots with live database reads ([#342](https://github.com/thellmwhisperer/la-roca/issues/342)) ([42691c8](https://github.com/thellmwhisperer/la-roca/commit/42691c8b04715a1cf86bb193aeb1a8fd47f755cf))

## [1.82.4](https://github.com/thellmwhisperer/la-roca/compare/v1.82.3...v1.82.4) (2026-09-08)


### Bug Fixes

* **vector:** improve sidecar diagnostics and query startup ([#338](https://github.com/thellmwhisperer/la-roca/issues/338)) ([5543a44](https://github.com/thellmwhisperer/la-roca/commit/5543a446ed596bc0fe1ef3f9c3db35cc0410d84d))

## [1.82.3](https://github.com/thellmwhisperer/la-roca/compare/v1.82.2...v1.82.3) (2026-09-07)


### Bug Fixes

* **distribution:** bound session-start handoffs and add cross-project view ([#327](https://github.com/thellmwhisperer/la-roca/issues/327)) ([f500eaa](https://github.com/thellmwhisperer/la-roca/commit/f500eaab7f7f9eda6e7a55b390100cdace7fce85))
* preserve exact memory IDs across JSON and MCP ([#329](https://github.com/thellmwhisperer/la-roca/issues/329)) ([02cff56](https://github.com/thellmwhisperer/la-roca/commit/02cff56da82db8276dc398ec3eaf7210fa44812c))
* **provider:** reject unqualified exec tables with qualified candidates ([#330](https://github.com/thellmwhisperer/la-roca/issues/330)) ([7c94b39](https://github.com/thellmwhisperer/la-roca/commit/7c94b39ea50b2dcbbd99b6ce41780ed8813357f8))

## [1.82.2](https://github.com/thellmwhisperer/la-roca/compare/v1.82.1...v1.82.2) (2026-09-07)


### Bug Fixes

* **vector:** share embedding resident across MCP sessions ([#331](https://github.com/thellmwhisperer/la-roca/issues/331)) ([32327d6](https://github.com/thellmwhisperer/la-roca/commit/32327d6cf8b1824cbb1f105b3747acebc1eeeb39))

## [1.82.1](https://github.com/thellmwhisperer/la-roca/compare/v1.82.0...v1.82.1) (2026-09-07)


### Bug Fixes

* **distribution:** honor max-chars in TOON output ([#326](https://github.com/thellmwhisperer/la-roca/issues/326)) ([877014e](https://github.com/thellmwhisperer/la-roca/commit/877014ec760ccfc9a40b76bd23c27c0d2391ba97))

## [1.82.0](https://github.com/thellmwhisperer/la-roca/compare/v1.81.2...v1.82.0) (2026-09-07)


### Features

* **cli:** add pill deletion by slug ([#325](https://github.com/thellmwhisperer/la-roca/issues/325)) ([4615544](https://github.com/thellmwhisperer/la-roca/commit/4615544039a5ac7d1181f895d8eec395cce89e59))

## [1.81.2](https://github.com/thellmwhisperer/la-roca/compare/v1.81.1...v1.81.2) (2026-09-07)


### Bug Fixes

* **ingest:** continue Codex history reconciliation after exact-payload collisions ([#321](https://github.com/thellmwhisperer/la-roca/issues/321)) ([e9cc11b](https://github.com/thellmwhisperer/la-roca/commit/e9cc11bd3c714da6f0fa713c170cbbd887f0df4f))

## [1.81.1](https://github.com/thellmwhisperer/la-roca/compare/v1.81.0...v1.81.1) (2026-09-02)


### Bug Fixes

* **vector:** report honest per-database vector status ([#303](https://github.com/thellmwhisperer/la-roca/issues/303)) ([dc0b091](https://github.com/thellmwhisperer/la-roca/commit/dc0b09173304c4d025a432d1061a894afc5cd744))

## [1.81.0](https://github.com/thellmwhisperer/la-roca/compare/v1.80.1...v1.81.0) (2026-09-02)


### Features

* **distribution:** add opt-in ZCode runtime support ([#308](https://github.com/thellmwhisperer/la-roca/issues/308)) ([7edad66](https://github.com/thellmwhisperer/la-roca/commit/7edad66e73c1544ef34c6700f6d0a9a83392b3d3))

## [1.80.1](https://github.com/thellmwhisperer/la-roca/compare/v1.80.0...v1.80.1) (2026-09-02)


### Bug Fixes

* **parsers:** preserve Codex failures and orphan tool telemetry ([#278](https://github.com/thellmwhisperer/la-roca/issues/278)) ([45ca839](https://github.com/thellmwhisperer/la-roca/commit/45ca839ac471d7812b128c63554361ebc617b468))

## [1.80.0](https://github.com/thellmwhisperer/la-roca/compare/v1.79.3...v1.80.0) (2026-09-02)


### Features

* **distribution:** add Claude Desktop MCP install target ([#307](https://github.com/thellmwhisperer/la-roca/issues/307)) ([f809ceb](https://github.com/thellmwhisperer/la-roca/commit/f809cebd419c2b1523e92957bc777601cfbc3d6d))

## [1.79.3](https://github.com/thellmwhisperer/la-roca/compare/v1.79.2...v1.79.3) (2026-09-01)


### Bug Fixes

* **store:** reap lease-proven snapshot orphans ([#302](https://github.com/thellmwhisperer/la-roca/issues/302)) ([5254ced](https://github.com/thellmwhisperer/la-roca/commit/5254cedf88e556e27c69146fa5ee87cdaabcbbff))

## [1.79.2](https://github.com/thellmwhisperer/la-roca/compare/v1.79.1...v1.79.2) (2026-09-01)


### Bug Fixes

* **vector:** recover from trapped native indexing calls ([#298](https://github.com/thellmwhisperer/la-roca/issues/298)) ([e426142](https://github.com/thellmwhisperer/la-roca/commit/e42614266a98b2ef9cda96589ceef1d86966e58c))

## [1.79.1](https://github.com/thellmwhisperer/la-roca/compare/v1.79.0...v1.79.1) (2026-09-01)


### Bug Fixes

* **vector:** prevent indexing and status hangs ([#293](https://github.com/thellmwhisperer/la-roca/issues/293)) ([0701f76](https://github.com/thellmwhisperer/la-roca/commit/0701f76c78ccd1326186ba76fa539f788f21fa7f))

## [1.79.0](https://github.com/thellmwhisperer/la-roca/compare/v1.78.0...v1.79.0) (2026-09-01)


### Features

* **ingest:** import cloud Codex conversations from OpenAI exports ([#287](https://github.com/thellmwhisperer/la-roca/issues/287)) ([6101b3f](https://github.com/thellmwhisperer/la-roca/commit/6101b3fa6eb44ea86abd9c029172a46c09bbb068))

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
