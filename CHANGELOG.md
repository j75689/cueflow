# Changelog

## [1.1.0](https://github.com/j75689/cueflow/compare/v1.0.1...v1.1.0) (2026-10-20)


### Features

* **crew:** give agents task file tools and list the files in the brief ([#56](https://github.com/j75689/cueflow/issues/56)) ([b2d8d22](https://github.com/j75689/cueflow/commit/b2d8d22efceee42a91ee77e2f521c88fb35e7726))
* **crew:** hand off to a fresh run when the context fills up, through a note the human reviews ([#60](https://github.com/j75689/cueflow/issues/60)) ([0847e7c](https://github.com/j75689/cueflow/commit/0847e7cbdc701a17c6d407f881528032f833af5d))
* **crew:** let an agent continue a hand-off without waiting for review ([#61](https://github.com/j75689/cueflow/issues/61)) ([e1effd5](https://github.com/j75689/cueflow/commit/e1effd5966b9f6c49251242d53355d0948a02bdb))
* **crew:** save large tool results as task files instead of putting them in the history ([#57](https://github.com/j75689/cueflow/issues/57)) ([f70b7e8](https://github.com/j75689/cueflow/commit/f70b7e8848dc46d6e0a921effbb1243659c6834b))
* **crew:** show how full each run's context is, against the model's window ([#59](https://github.com/j75689/cueflow/issues/59)) ([e846223](https://github.com/j75689/cueflow/commit/e846223ab900e1dfee5e9561304d2d98db8348f9))
* **deliverable:** let agents read a deliverable in full with get_deliverable ([#64](https://github.com/j75689/cueflow/issues/64)) ([63f2d8f](https://github.com/j75689/cueflow/commit/63f2d8feba06dee196b5c71a926c9410332679f2))
* **openai:** stream chat completions ([#63](https://github.com/j75689/cueflow/issues/63)) ([917c7c0](https://github.com/j75689/cueflow/commit/917c7c081cdaa60619dcf16998c370b1c019665b))
* **store:** task files on a byte layer shared with media ([#55](https://github.com/j75689/cueflow/issues/55)) ([493fb2a](https://github.com/j75689/cueflow/commit/493fb2aace439e2970e51fee3efaf9f656b0b188))
* **web:** a Files card and viewer on the task page, with routes for a task's files ([#58](https://github.com/j75689/cueflow/issues/58)) ([6ff4dbd](https://github.com/j75689/cueflow/commit/6ff4dbdf36119aa6c6d38973b00615e2080b13c2))


### Bug Fixes

* **anthropic:** stream model calls so a long answer isn't cut off by the HTTP timeout ([#62](https://github.com/j75689/cueflow/issues/62)) ([1009fbc](https://github.com/j75689/cueflow/commit/1009fbc3e9487ea6b1e64319bd0faac8c493f677))
* **crew:** keep the model's history append-only; cache it on Claude; say plainly when a run outgrows the window ([#54](https://github.com/j75689/cueflow/issues/54)) ([6696f61](https://github.com/j75689/cueflow/commit/6696f61e64132be26418f635d3b4262e0a828dd8))
* **crew:** let agents read files MCP tools return as embedded resources; clip long results with a note ([#50](https://github.com/j75689/cueflow/issues/50)) ([18e1c9a](https://github.com/j75689/cueflow/commit/18e1c9ab2779b002733b949384f457f3b4eca97e))

## [1.0.1](https://github.com/j75689/cueflow/compare/v1.0.0...v1.0.1) (2026-10-19)


### Bug Fixes

* **deliverables:** ask the kind before approving; refuse a post its publisher cannot take ([#49](https://github.com/j75689/cueflow/issues/49)) ([0189e67](https://github.com/j75689/cueflow/commit/0189e6744ec0deb22f6e0bdc7905d39bf9848987))
* **events:** scope live updates to the org a tab is watching ([#48](https://github.com/j75689/cueflow/issues/48)) ([e3211fe](https://github.com/j75689/cueflow/commit/e3211fe2dc94c10c80be913209e42f8504ebebc4))
* **web:** live updates, selection and one-request bulk actions on the Deliverables page ([#44](https://github.com/j75689/cueflow/issues/44)) ([c6a1b2e](https://github.com/j75689/cueflow/commit/c6a1b2e5d9957193447e23e8b516d7095d27b40e))


### Documentation

* **templates:** developer team template (product manager, tech lead, architect, engineer, reviewer, QA, tech writer) ([518805e](https://github.com/j75689/cueflow/commit/518805e06815e34ed1dba3b0ce7c4b98fbbbce06))

## [1.0.0](https://github.com/j75689/cueflow/commits/v1.0.0) (2026-10-19)


### Features

* agent team with command center, kanban board and schedules ([aa9c989](https://github.com/j75689/cueflow/commit/aa9c989bc95dd3246433740b836e240132427761))
* AI image generation and image uploads ([a76d2f3](https://github.com/j75689/cueflow/commit/a76d2f33718047638180f5a9420accefc3734019))
* **connectors:** generalize the CueSocial publisher into an MCP publisher ([247cc66](https://github.com/j75689/cueflow/commit/247cc6614741c372cf1cca519e3648e560a688b8))
* **content:** the post flow on deliverables, one agent at a time ([#20](https://github.com/j75689/cueflow/issues/20)) ([a88c878](https://github.com/j75689/cueflow/commit/a88c87880efd244faf05bb641f8d5bd391d5819f))
* **core:** CUEFLOW_MODULES decides which feature modules are mounted ([#19](https://github.com/j75689/cueflow/issues/19)) ([28da96e](https://github.com/j75689/cueflow/commit/28da96e18d678b25be53678b2377fb46d990f76f)), closes [#13](https://github.com/j75689/cueflow/issues/13)
* **crew:** revise_draft can attach or replace media_urls ([c5a5c47](https://github.com/j75689/cueflow/commit/c5a5c47e4ae31ef29ff7c9ae49daff35871c8c8d))
* **crew:** team templates as JSON files, imported and exported per org ([#30](https://github.com/j75689/cueflow/issues/30)) ([2f228b9](https://github.com/j75689/cueflow/commit/2f228b997260192cd5043966de1f7e9dd0df896d))
* CueFlow v0.1.0-alpha - self-hosted social Agent Ops Console ([19c52fc](https://github.com/j75689/cueflow/commit/19c52fc524bedfff4c7abf99cd3b825e115ddcf5))
* CueSocial publisher with per-post account selection ([ae572ce](https://github.com/j75689/cueflow/commit/ae572ced527b75df0cd55d240da9751ef642ada9))
* **cuesocial:** publish threads and multi-image posts ([5e7c04e](https://github.com/j75689/cueflow/commit/5e7c04e3853dde326494c55bcd103f610620ece6))
* **deliverable:** typed deliverables reviewed by a human and acted on after approval ([#15](https://github.com/j75689/cueflow/issues/15)) ([312befe](https://github.com/j75689/cueflow/commit/312befe6cfa351cb35b3fd418af9bdaaf0f103b7)), closes [#12](https://github.com/j75689/cueflow/issues/12)
* drop Postiz and ship a mock publisher for the MCP publisher contract ([#7](https://github.com/j75689/cueflow/issues/7)) ([3b70a62](https://github.com/j75689/cueflow/commit/3b70a629729c5fab352b37eea085938f8f2d1c9e))
* external agents over ACP (Claude Code, Codex, Gemini CLI, ...) ([8dd3aa6](https://github.com/j75689/cueflow/commit/8dd3aa6cac2929af13a6a4d211108a37821f1432))
* external MCP tool servers with per-tool permissions ([2275467](https://github.com/j75689/cueflow/commit/2275467cccf49dde5c45876a253288499e127e1a))
* **gcp:** a service account that may only create files under uploads/ ([b641552](https://github.com/j75689/cueflow/commit/b641552b280a6c8e078899ba9a2e6a239eda1e78))
* **gcp:** let the helper account use Firestore documents, with a TTL on seen items ([642bba5](https://github.com/j75689/cueflow/commit/642bba5d2ef69c68b657c4d6ae11638a16832dbd))
* Gemini on Vertex AI connector ([270a5d2](https://github.com/j75689/cueflow/commit/270a5d2e50cc44b294531c5c61f4c77fb06823d9))
* more LLM auth options, Bedrock/Vertex, OpenRouter and Azure OpenAI ([77f8970](https://github.com/j75689/cueflow/commit/77f8970e4eece4b5297ec0c6bf08f66043a62cd0))
* name connectors from their settings and show a config summary ([afc3273](https://github.com/j75689/cueflow/commit/afc327320ddb9ba728e3b7ebc10f96f799a5949b))
* operator MCP endpoint for external agents ([2c7a29b](https://github.com/j75689/cueflow/commit/2c7a29b67a805e6890eedacaf9629f837510309a))
* orgs with their own agents, board, schedules, review and runs ([62b6c56](https://github.com/j75689/cueflow/commit/62b6c569d755d0c017b2ef530408a5206ee3b355))
* pick Postiz channels per post ([c432224](https://github.com/j75689/cueflow/commit/c432224dfc70cac0656645767dc353aac6c7417f))
* present CueFlow as a general agent console ([6d73637](https://github.com/j75689/cueflow/commit/6d73637454a0ed3fd9a89fcab9ebd1d9ceba6d0b))
* publish YouTube Shorts through the MCP publisher ([86030cc](https://github.com/j75689/cueflow/commit/86030cc0031a3dc93071756702ee8fcdfb41b220))
* publishing defaults on schedules and tasks ([e9722ee](https://github.com/j75689/cueflow/commit/e9722ee90a7f6de6ad4fca1e1d5f293171f08a07))
* **runner:** publish the agents and runner images and document extending them ([#37](https://github.com/j75689/cueflow/issues/37)) ([5fe71e1](https://github.com/j75689/cueflow/commit/5fe71e127b410e225ee31d17541da537e1621807))
* Threads topic tag and link card (CueSocial) ([e72d2b6](https://github.com/j75689/cueflow/commit/e72d2b64e077a0e785e2686a202dd46e35befb4e))
* Usage page and run provenance; warn about missing ACP credentials ([adf981f](https://github.com/j75689/cueflow/commit/adf981f7e49b985eb400503fcd3fb8584b694e91))
* video media for Reels ([3cb2a4c](https://github.com/j75689/cueflow/commit/3cb2a4cafa843e09cc2c53561a115d30703b8506))
* **web:** bulk approve and reject on the Deliverables page ([#36](https://github.com/j75689/cueflow/issues/36)) ([5e83ab8](https://github.com/j75689/cueflow/commit/5e83ab80cec5a9cdbfd1ac3432bf8767e815760d))
* **web:** custom select, date-time picker and dialogs; fix compose Temporal/mock ([d90d3e6](https://github.com/j75689/cueflow/commit/d90d3e610b5ce1545516172a218a9474ddd8eb9e))
* **web:** review page for deliverables ([#17](https://github.com/j75689/cueflow/issues/17)) ([03e945c](https://github.com/j75689/cueflow/commit/03e945cd2acf975f448fb795d4037b68258bf1d8))
* **web:** semantic color tokens and a refined look across the dashboard ([#35](https://github.com/j75689/cueflow/issues/35)) ([758c5fe](https://github.com/j75689/cueflow/commit/758c5fe746686f2b31ccd1ee62f7cb6f0a7a61e9))
* **web:** show the Review and Runs pages only when the content module is mounted ([#10](https://github.com/j75689/cueflow/issues/10)) ([eb2ba37](https://github.com/j75689/cueflow/commit/eb2ba37abc8d3f0d37aa4e0d24d7c0f9f7fe55f5))
* zero-downtime secret key rotation (cueflow rotate-key) ([d99b8a9](https://github.com/j75689/cueflow/commit/d99b8a9e4a029430a8e59d0290d199484c8fb769))


### Bug Fixes

* **docker:** install CA certificates in the full image ([8bbc825](https://github.com/j75689/cueflow/commit/8bbc825cc211fc797cdb52194a7f846631ac9b59))
* **drafts:** keep a thread's first part when a model gives it as content ([b412f81](https://github.com/j75689/cueflow/commit/b412f811468a9b12045d97494a763a2056af3e01))
* **rss:** keep entries apart when a feed gives them all one link ([ceeac4a](https://github.com/j75689/cueflow/commit/ceeac4aa973d347b7d681d6ff697ba9655440f0a))
* **web:** keep the task thread pinned to the newest message ([16a95b5](https://github.com/j75689/cueflow/commit/16a95b5c5ac27616dd1056cf8edb95cb1aff90d7))
* **web:** line up the board columns ([#31](https://github.com/j75689/cueflow/issues/31)) ([d1874ef](https://github.com/j75689/cueflow/commit/d1874efdbca8da39c22307282c67ace7aeeccd09))
* **web:** use the shared Select on the deliverables pages ([#22](https://github.com/j75689/cueflow/issues/22)) ([c9e0cd1](https://github.com/j75689/cueflow/commit/c9e0cd1367fb9549e2a144ff5b96562568b1c759))


### Performance

* stop holding requests open, so Cloud Run bills only real work ([5089ad8](https://github.com/j75689/cueflow/commit/5089ad8a63d32202d8803b5f02c920ca6cea90ab))


### Documentation

* a new install seeds only the Chief, say so (README en/zh-TW, agentic-flow) ([59a65a1](https://github.com/j75689/cueflow/commit/59a65a1de37ae2c132b0afcd15c85e366b106d77))
* add architecture and agentic flow design documents ([8b35efb](https://github.com/j75689/cueflow/commit/8b35efbaeecb9ae206ea2875058ba7ae901d7516))
* deploy guide and comments that name no private infrastructure ([1808e08](https://github.com/j75689/cueflow/commit/1808e08347136dc7348d5a51251a17ea1a9cd82e))
* English as the primary language, Traditional Chinese versions in docs/zh-TW ([5c3ff79](https://github.com/j75689/cueflow/commit/5c3ff79311e1b1e17802537c3d25d82412a711c2))
* model selection ([f415440](https://github.com/j75689/cueflow/commit/f415440c47ab35e5857176e4fdb942aee5ac254a))
* move team templates to templates/ and turn media.md into modules.md ([#33](https://github.com/j75689/cueflow/issues/33)) ([ef616b0](https://github.com/j75689/cueflow/commit/ef616b0f7f728e9ec691231c12250aee0ddcfc19))
* restructure the READMEs: header, quick start by where it runs, numbered flow ([#34](https://github.com/j75689/cueflow/issues/34)) ([a721cca](https://github.com/j75689/cueflow/commit/a721cca26f4e6deff46460488df158dfab8aba62))
* security policy, contributing guide, issue templates and a status note ([78e698e](https://github.com/j75689/cueflow/commit/78e698e6d49864a095907bad4b501a31446fe099))
* shorten the READMEs and move the reference material into docs/ ([#5](https://github.com/j75689/cueflow/issues/5)) ([7d19531](https://github.com/j75689/cueflow/commit/7d19531d45e4ce1c635361509d9f80fbca8bd4f6))


### Refactoring

* **content:** retire the posts path; posts are deliverables ([#21](https://github.com/j75689/cueflow/issues/21)) ([077ad34](https://github.com/j75689/cueflow/commit/077ad348837bc22c881bbaccc6e581156321e466))


### CI

* cut releases with release-please and build images only for releases ([#40](https://github.com/j75689/cueflow/issues/40)) ([8485303](https://github.com/j75689/cueflow/commit/84853034946ccad418d1bb3227a6a9a517ba804e))
