# Changelog

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
