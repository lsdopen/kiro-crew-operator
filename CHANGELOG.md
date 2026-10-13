# Changelog

## [0.2.8](https://github.com/lsdopen/kiro-crew-operator/compare/v0.2.7...v0.2.8) (2026-10-13)


### Features

* console phase 3d (admin accounts and KiroCrewConfig) ([#68](https://github.com/lsdopen/kiro-crew-operator/issues/68)) ([51b0011](https://github.com/lsdopen/kiro-crew-operator/commit/51b00118d867a3195e5a38cecba046b527635453))


### Dependencies

* bump library/golang from 1.26.9 to 1.27.1 in /images/console ([#62](https://github.com/lsdopen/kiro-crew-operator/issues/62)) ([e0395cc](https://github.com/lsdopen/kiro-crew-operator/commit/e0395cc208c481accec01c4f2138cc6f23acb063))

## [0.2.7](https://github.com/lsdopen/kiro-crew-operator/compare/v0.2.6...v0.2.7) (2026-10-13)


### Features

* console phase 3c (start/stop and sign-in brokering) ([#63](https://github.com/lsdopen/kiro-crew-operator/issues/63)) ([969a6ad](https://github.com/lsdopen/kiro-crew-operator/commit/969a6ad279bc2ccb644db309ac0e0d2b47acda9d))

## [0.2.6](https://github.com/lsdopen/kiro-crew-operator/compare/v0.2.5...v0.2.6) (2026-10-13)


### Features

* console phase 3b (requests and approvals) ([#60](https://github.com/lsdopen/kiro-crew-operator/issues/60)) ([8985bc3](https://github.com/lsdopen/kiro-crew-operator/commit/8985bc3a51c2a9040722fedeca46d289e61bd878))
* management console phase 3a (login and read-only views) ([#57](https://github.com/lsdopen/kiro-crew-operator/issues/57)) ([b412872](https://github.com/lsdopen/kiro-crew-operator/commit/b4128720fed3bddc643e81d82913a99d22c0dd9d))


### Dependencies

* bump github.com/go-jose/go-jose/v4 from 4.1.4 to 4.1.5 ([#58](https://github.com/lsdopen/kiro-crew-operator/issues/58)) ([001991e](https://github.com/lsdopen/kiro-crew-operator/commit/001991e3f398fa34e52a2f528e8378fc10166fde))
* bump github.com/onsi/ginkgo/v2 from 2.33.0 to 2.33.1 ([#49](https://github.com/lsdopen/kiro-crew-operator/issues/49)) ([09db40c](https://github.com/lsdopen/kiro-crew-operator/commit/09db40c6c8316e340f99ada2e503454ad139b6e4))

## [0.2.5](https://github.com/lsdopen/kiro-crew-operator/compare/v0.2.4...v0.2.5) (2026-10-13)


### Features

* add KiroCrewAccount and KiroCrewRequest CRDs ([#52](https://github.com/lsdopen/kiro-crew-operator/issues/52)) ([fb04597](https://github.com/lsdopen/kiro-crew-operator/commit/fb04597f350aa4b206a7ed49b9d3f20834baa140))

## [0.2.4](https://github.com/lsdopen/kiro-crew-operator/compare/v0.2.3...v0.2.4) (2026-10-13)


### Features

* add suspend and schedule to KiroCrew ([#51](https://github.com/lsdopen/kiro-crew-operator/issues/51)) ([c39f4c2](https://github.com/lsdopen/kiro-crew-operator/commit/c39f4c2e0d01d1e4e62b9c98380894a96ceca029))


### Dependencies

* bump kirodotdev/kirocrew from 0.7.2 to 0.8.0 in /images/gateway ([#35](https://github.com/lsdopen/kiro-crew-operator/issues/35)) ([aabd543](https://github.com/lsdopen/kiro-crew-operator/commit/aabd5433aff04be0b7520101820037c53536d990))
* bump sigs.k8s.io/controller-runtime from 0.25.1 to 0.25.2 ([#36](https://github.com/lsdopen/kiro-crew-operator/issues/36)) ([b3e5984](https://github.com/lsdopen/kiro-crew-operator/commit/b3e5984aa9f51e14f91d264d4ced1b0816f97ca9))

## [0.2.3](https://github.com/lsdopen/kiro-crew-operator/compare/v0.2.2...v0.2.3) (2026-10-13)


### Bug Fixes

* make a provisioned crew reachable end to end ([#42](https://github.com/lsdopen/kiro-crew-operator/issues/42)) ([049c74d](https://github.com/lsdopen/kiro-crew-operator/commit/049c74d03d1dfdb7f377c884dbf8b47bbf8a3b4b))


### Dependencies

* bump astral-sh/uv from `1946145` to `cdc6093` in /images/gateway ([#34](https://github.com/lsdopen/kiro-crew-operator/issues/34)) ([24f2acd](https://github.com/lsdopen/kiro-crew-operator/commit/24f2acdb984830b2aec28a89d7abd8c529d131bb))
* pin uv and node to precise version tags, not rolling ([#46](https://github.com/lsdopen/kiro-crew-operator/issues/46)) ([d73ae0a](https://github.com/lsdopen/kiro-crew-operator/commit/d73ae0a8fc3b8aa2b7f65431a02a781e7e2a96b2))

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
