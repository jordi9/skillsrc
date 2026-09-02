# Changelog

All notable changes to this project are documented in this file.


## [v0.5.0] - 2026-09-02

### Changes

- ci: publish curated changelog notes ([47e8f17](https://github.com/jordi9/skillsrc/commit/47e8f176615f435b6f9c2744ef2dc0ee26948d13))
- discovery: keep first marketplace duplicate ([c93afbb](https://github.com/jordi9/skillsrc/commit/c93afbb73e76933a7d02b134713a4b76053cd55e))
- discovery: support manifest-scoped plugins/skill sources ([4c70d54](https://github.com/jordi9/skillsrc/commit/4c70d544d6991beb1c1ae47aa7000ebd64bc55c2))
- docs: unslop README ([d470854](https://github.com/jordi9/skillsrc/commit/d4708549ef64e5595edd10c48455e3027f1b9210))
- fix: preserve symlinks when saving ([e9ce23e](https://github.com/jordi9/skillsrc/commit/e9ce23e7ee391e6b8a7a6109fbe388a4327d4df7))
- discovery: find skills from plugins ([dc3ada8](https://github.com/jordi9/skillsrc/commit/dc3ada85c184ff0a99576d7e5b1fecfa7c157aef))

## [v0.4.1] - 2026-08-13

### Changes

- docs: fix fmt ([a69750d](https://github.com/jordi9/skillsrc/commit/a69750d417eaed626fe3a8c10a8e09f15105e00f))

## [v0.4.0] - 2026-08-13

### Changes

- add: ignore skills before installation
  ([4140cfa](https://github.com/jordi9/skillsrc/commit/4140cfaeb2f3dae563d9db0e03e7fe9db3de3834))
- discovery: read Claude plugin manifests
  ([4a052ed](https://github.com/jordi9/skillsrc/commit/4a052ed770486759066161ff63210645a94482eb))
- outdated: report only changed skill content
  ([2e6057f](https://github.com/jordi9/skillsrc/commit/2e6057fda4da26d414d794d7a330d8359ed60be5))
- release: reject releases without new commits
  ([82dce4b](https://github.com/jordi9/skillsrc/commit/82dce4b472ac8bfa949a5be4d078bbc7ef3f7da3))

## [v0.3.0] - 2026-08-07

### Changes

- add: install only newly selected skills
  ([5d71ebe](https://github.com/jordi9/skillsrc/commit/5d71ebe35c890cf76b3ae32ff770121057c07b63))
- add: install the sole discovered skill automatically
  ([44742ed](https://github.com/jordi9/skillsrc/commit/44742ed5f4c90e104529cf7010bfc2eb0bde6f2d))

## [v0.2.0] - 2026-07-29

### Changes

- discovery: prioritize duplicated skill locations
  ([a825863](https://github.com/jordi9/skillsrc/commit/a825863dc1e44cd71426d1a87024497c20f4e40b))
- release: preserve external release notes
  ([af077b8](https://github.com/jordi9/skillsrc/commit/af077b8fb8e37875459bca8bf925586438da491d))
- repo: add golangci-lint checks
  ([14e8189](https://github.com/jordi9/skillsrc/commit/14e818982171e128292d8cb9c09f7fcc53fcbdd6))

## [v0.1.0] - 2026-07-27

Initial public release of `skillsrc`.

- Declare and lock skills from Git repositories or local directories.
- Reproduce exact `.agents/skills` installations across machines.
- Detect drift and protect unmanaged skills with ownership-aware updates.

### Changes

- build: add make install target
  ([92f1311](https://github.com/jordi9/skillsrc/commit/92f131147807ea404080678a74de331cb8413d37))
- ci: keep release notes outside checkout
  ([afe4f38](https://github.com/jordi9/skillsrc/commit/afe4f38614007728ca27e1e3af8b0963257fcc99))
- ci: limit checks to Ubuntu and simplify release
  ([ffe5495](https://github.com/jordi9/skillsrc/commit/ffe549508119265f79dc8315672b458ebf4b3dbd))
- ci: update actions and remove race job
  ([7fe7fcd](https://github.com/jordi9/skillsrc/commit/7fe7fcdf4b51e6539044ff4b6742060c910094c4))
- cli: add declarative add and remove commands
  ([651f6a9](https://github.com/jordi9/skillsrc/commit/651f6a9722e026731156bb1ee9e34d12df7283fb))
- cli: clarify missing-manifest recovery
  ([1bf8812](https://github.com/jordi9/skillsrc/commit/1bf8812df7723652ebc91cc97b50eb0a6e4fc95b))
- cli: explain sync work and repository fetches
  ([92bd9ce](https://github.com/jordi9/skillsrc/commit/92bd9cede39f74efc42b0fffff6c6df01af87f07))
- cli: show help by default and polish output
  ([2c7cffd](https://github.com/jordi9/skillsrc/commit/2c7cffdf8bcdbe61ad19e75b1013e35e34f094f7))
- config: rename default manifest to skills.toml
  ([fe74064](https://github.com/jordi9/skillsrc/commit/fe74064a51f43d4eaf08e6a1ee8886ee1dfa79b2))
- core: add manifest model and Git source cache
  ([5942b2d](https://github.com/jordi9/skillsrc/commit/5942b2d0b0746d61a40e473b477868102d5d2f28))
- core: simplify CLI and persistence
  ([9550ef1](https://github.com/jordi9/skillsrc/commit/9550ef16bea02191bec666faa55abd86b858b81f))
- docs: improve README and enforce markdown formatting
  ([6e4694e](https://github.com/jordi9/skillsrc/commit/6e4694ea1f9405135b15dd9cfff5b114539edbc4))
- docs: introduce skillsrc
  ([a2677a1](https://github.com/jordi9/skillsrc/commit/a2677a1b019e41b495300f7eaf43f83619e69121))
- git: unify fetch tracking and cache locking
  ([b90a522](https://github.com/jordi9/skillsrc/commit/b90a52255572471ac8824281a3c7b767f36892ef))
- help: add command-specific guidance
  ([dde52b9](https://github.com/jordi9/skillsrc/commit/dde52b9d169378c77137c5ad1587db3b53595439))
- list: improve human-readable output
  ([9a1d144](https://github.com/jordi9/skillsrc/commit/9a1d144e93a4a520bb4a18843382d97508de78a7))
- list: improve terminal readability
  ([40c532c](https://github.com/jordi9/skillsrc/commit/40c532cb27ac7a22fec3847e0cbcbec11b224043))
- list: show standalone unmanaged skills
  ([fac5d92](https://github.com/jordi9/skillsrc/commit/fac5d92111592cbc0541b5f35df60fdb2aea0ba9))
- manifest: add model invocation overrides
  ([9eab509](https://github.com/jordi9/skillsrc/commit/9eab50996323405100bef7f65896d9fc22e08759))
- outdated: show skills affected by updates
  ([11f1a0e](https://github.com/jordi9/skillsrc/commit/11f1a0e543e0519f87b545f9b27f5bb5b8d720f5))
- release: group changelog entries by topic
  ([d3210b4](https://github.com/jordi9/skillsrc/commit/d3210b4befffc6bb6c42b416b0261c8def11314b))
- release: prepare skillsrc for public release
  ([a849a0c](https://github.com/jordi9/skillsrc/commit/a849a0c905ceb7e7f7f95ee5e3e17cabc0026d16))
- release: publish artifacts with GoReleaser
  ([d96420a](https://github.com/jordi9/skillsrc/commit/d96420a2e51a6a8c4078b6379c70e74fb645b0d8))
- repo: ignore local .now files
  ([dedf835](https://github.com/jordi9/skillsrc/commit/dedf835adb2a41ca08a227c57ea0e484e7b38761))
- scope: add project scopes and update checks
  ([0d22ed5](https://github.com/jordi9/skillsrc/commit/0d22ed54262565608187b9fba70dc44245f66944))
- scope: infer user configuration and improve diagnostics
  ([84409e8](https://github.com/jordi9/skillsrc/commit/84409e8b406bb0502c2cadd6f8b45d3f487f204f))
- status: detect local source drift and distinguish repairs
  ([6e4297d](https://github.com/jordi9/skillsrc/commit/6e4297d11108780f35d62e396e657f841a23dd08))
- status: show model invocation provenance
  ([92ad859](https://github.com/jordi9/skillsrc/commit/92ad85931447a8e858b9e3c80b9351d2059c6f02))
- sync: add safe update, list, and doctor workflows
  ([1d6360c](https://github.com/jordi9/skillsrc/commit/1d6360c3a4dfdc393b02d3d1c908b603dc0b6f02))
- sync: harden reconciliation and document migration
  ([4c354fa](https://github.com/jordi9/skillsrc/commit/4c354fa3c30959d3144ec422333c0bfe2de9a255))
- tests: remove completed migration fixture
  ([9128c93](https://github.com/jordi9/skillsrc/commit/9128c93758246a718d314f46ebb6b47e3b12f6d1))
