# Changelog

## [0.2.2](https://github.com/lsdopen/kiro-crew-operator/compare/v0.2.1...v0.2.2) (2026-10-06)


### Bug Fixes

* **chart:** name the operator Deployment kiro-crew-operator ([41485c6](https://github.com/lsdopen/kiro-crew-operator/commit/41485c61aa78fb5072d2147d7d6c1717e69df509))


### Dependencies

* bump Go modules and fix GO-2026-6505, GO-2026-6348 ([06d99ce](https://github.com/lsdopen/kiro-crew-operator/commit/06d99ceaec851903dfcee4d757de66fd913cbfd5))
* pin gateway to kirocrew 0.7.2 and tailscale v1.102.5 ([8190e5d](https://github.com/lsdopen/kiro-crew-operator/commit/8190e5d4805ce6b5138f3794410283376d6557f4))

## [0.2.1](https://github.com/lsdopen/kiro-crew-operator/compare/v0.2.0...v0.2.1) (2026-09-11)


### Bug Fixes

* publish releases via workflow_call, not a token-created event ([28a95cd](https://github.com/lsdopen/kiro-crew-operator/commit/28a95cdd03069621103521ba37fc01b2a4604d3f))
* publish releases via workflow_call, not a token-created event ([d160352](https://github.com/lsdopen/kiro-crew-operator/commit/d160352d34e9bab1b952cab3acf91d1f9d86aac0))

## [0.2.0](https://github.com/lsdopen/kiro-crew-operator/compare/v0.1.3...v0.2.0) (2026-09-11)


### ⚠ BREAKING CHANGES

* releases are no longer cut by a hand-pushed tag; merging the release-please PR is the release action. Commit messages must follow Conventional Commits (feat/fix/perf/deps/...) or they will not appear in the changelog or move the version.

### Features

* automate operator releases with release-please ([0681376](https://github.com/lsdopen/kiro-crew-operator/commit/0681376b43d730b83f99e7ee4fe19a07caa8d02e))
* automate operator releases with release-please ([6171e31](https://github.com/lsdopen/kiro-crew-operator/commit/6171e31fa46ee096aa548626b01ee93815ab55b1))
