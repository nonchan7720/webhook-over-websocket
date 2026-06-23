# Changelog

## [1.7.0](https://github.com/nonchan7720/webhook-over-websocket/compare/v1.6.0...v1.7.0) (2026-06-23)


### Features

* stream request body for 307/308 redirect replay via TeeReader ([#54](https://github.com/nonchan7720/webhook-over-websocket/issues/54)) ([a57fd04](https://github.com/nonchan7720/webhook-over-websocket/commit/a57fd04ddd0c9c3e81c7117a4f3a44994a7e4b7c))


### Performance Improvements

* stream WebSocket response and fix 307/308 redirect body replay ([#53](https://github.com/nonchan7720/webhook-over-websocket/issues/53)) ([6227149](https://github.com/nonchan7720/webhook-over-websocket/commit/6227149a24160dab352c9843c3ac3c93b12d8fe4))

## [1.6.0](https://github.com/nonchan7720/webhook-over-websocket/compare/v1.5.0...v1.6.0) (2026-06-21)


### Features

* forward dynamic path suffixes to local server (like Cloudflare Tunnel) ([#50](https://github.com/nonchan7720/webhook-over-websocket/issues/50)) ([2492aac](https://github.com/nonchan7720/webhook-over-websocket/commit/2492aac22c888f6d97b4cc767c1f9b509411aa6a))

## [1.5.0](https://github.com/nonchan7720/webhook-over-websocket/compare/v1.4.1...v1.5.0) (2026-03-20)


### Features

* Allow clients to specify a channel ID when requesting a new channel_id ([#45](https://github.com/nonchan7720/webhook-over-websocket/issues/45)) ([e503712](https://github.com/nonchan7720/webhook-over-websocket/commit/e503712bf4527c17cd9d31f386d2cb958e102e4c))


### Bug Fixes

* patch ([#43](https://github.com/nonchan7720/webhook-over-websocket/issues/43)) ([2bd4d7f](https://github.com/nonchan7720/webhook-over-websocket/commit/2bd4d7f9cea934e3931ab85afa4ae8de0e0daa52))


### Miscellaneous

* modify release please config ([#49](https://github.com/nonchan7720/webhook-over-websocket/issues/49)) ([b5270dc](https://github.com/nonchan7720/webhook-over-websocket/commit/b5270dc4dc49331f59a4151f57a7af93841652f2))

## [1.4.1](https://github.com/nonchan7720/webhook-over-websocket/compare/v1.4.0...v1.4.1) (2026-02-24)


### Bug Fixes

* request base url ([#35](https://github.com/nonchan7720/webhook-over-websocket/issues/35)) ([8d98438](https://github.com/nonchan7720/webhook-over-websocket/commit/8d98438a83b24a88371d701bd05bf5e231399e8a))

## [1.4.0](https://github.com/nonchan7720/webhook-over-websocket/compare/v1.3.0...v1.4.0) (2026-02-22)


### Features

* add GitHub OAuth authentication and per-channel JWT authorization ([#25](https://github.com/nonchan7720/webhook-over-websocket/issues/25)) ([61b1aab](https://github.com/nonchan7720/webhook-over-websocket/commit/61b1aab725e65c1895bcbed3abcea7dea49124d0))

## [1.3.0](https://github.com/nonchan7720/webhook-over-websocket/compare/v1.2.0...v1.3.0) (2026-02-20)


### Features

* add logging ([#18](https://github.com/nonchan7720/webhook-over-websocket/issues/18)) ([aed730c](https://github.com/nonchan7720/webhook-over-websocket/commit/aed730c5c3929b6d631b784876547846449eadfd))
* cluster by memberlist ([#21](https://github.com/nonchan7720/webhook-over-websocket/issues/21)) ([1b7cd23](https://github.com/nonchan7720/webhook-over-websocket/commit/1b7cd2305952fa2cb7976160b8748cfd161c8993))


### Bug Fixes

* memberlist and traefik dynamic router ([#23](https://github.com/nonchan7720/webhook-over-websocket/issues/23)) ([0af6f08](https://github.com/nonchan7720/webhook-over-websocket/commit/0af6f08e32d734380631b58c5ba91f398579ff56))


### Documentation

* Add Japanese README (README_ja.md) ([#22](https://github.com/nonchan7720/webhook-over-websocket/issues/22)) ([2b5fec7](https://github.com/nonchan7720/webhook-over-websocket/commit/2b5fec7b7cad281d09fd359ae9c6b59903b4f2e8))


### Code Refactoring

* server and client ([#16](https://github.com/nonchan7720/webhook-over-websocket/issues/16)) ([86b9af0](https://github.com/nonchan7720/webhook-over-websocket/commit/86b9af0931dd9c3c5a5f2d6a2fe2f1fc8930a458))

## [1.2.0](https://github.com/nonchan7720/webhook-over-websocket/compare/v1.1.0...v1.2.0) (2026-02-20)


### Features

* add insecure connect flag ([#14](https://github.com/nonchan7720/webhook-over-websocket/issues/14)) ([49c905a](https://github.com/nonchan7720/webhook-over-websocket/commit/49c905a0f49a9f0edf0f7507df0d915c9384ede1))

## [1.1.0](https://github.com/nonchan7720/webhook-over-websocket/compare/v1.0.0...v1.1.0) (2026-02-20)


### Features

* healthz ([#12](https://github.com/nonchan7720/webhook-over-websocket/issues/12)) ([9e97066](https://github.com/nonchan7720/webhook-over-websocket/commit/9e97066ffd15398b02359ea45af2af101991180e))

## 1.0.0 (2026-02-20)


### Features

* init app ([#3](https://github.com/nonchan7720/webhook-over-websocket/issues/3)) ([934445c](https://github.com/nonchan7720/webhook-over-websocket/commit/934445ca2993c59cc145b7ce132563af66d9e418))
* init local dev env ([5fd26fe](https://github.com/nonchan7720/webhook-over-websocket/commit/5fd26fe09b65c4c1d665f3ee18ac64e21d91c137))


### Bug Fixes

* app ([#8](https://github.com/nonchan7720/webhook-over-websocket/issues/8)) ([6e13f94](https://github.com/nonchan7720/webhook-over-websocket/commit/6e13f9438a054cf7e759c16fe184969f27bdb9c3))
* correct Dockerfile COPY path to match pkg/cmd directory structure ([#4](https://github.com/nonchan7720/webhook-over-websocket/issues/4)) ([75678c5](https://github.com/nonchan7720/webhook-over-websocket/commit/75678c5413fc2973fba6a9c28ef89e4434104c39))
* release action ([#7](https://github.com/nonchan7720/webhook-over-websocket/issues/7)) ([98c7035](https://github.com/nonchan7720/webhook-over-websocket/commit/98c703534281f4cca33a4816e591e422ec61b89b))
* release actions ([#6](https://github.com/nonchan7720/webhook-over-websocket/issues/6)) ([1c09bc3](https://github.com/nonchan7720/webhook-over-websocket/commit/1c09bc3b39a1f1fbbda60818d422866af9abccd8))
* revert version ([#10](https://github.com/nonchan7720/webhook-over-websocket/issues/10)) ([2ee2c0f](https://github.com/nonchan7720/webhook-over-websocket/commit/2ee2c0f7e83f4c2b709fe18ab3d44011a52d8998))


### Miscellaneous

* **main:** release 1.0.0 ([#2](https://github.com/nonchan7720/webhook-over-websocket/issues/2)) ([40e48a4](https://github.com/nonchan7720/webhook-over-websocket/commit/40e48a45696bce48bad2c24b20838a12d0592d0b))


### Documentation

* update README and fix client command wiring ([#5](https://github.com/nonchan7720/webhook-over-websocket/issues/5)) ([7f582bc](https://github.com/nonchan7720/webhook-over-websocket/commit/7f582bced40ac8b0418630912111ea4fa88fbd54))

## 1.0.0 (2026-02-20)


### Features

* init app ([#3](https://github.com/nonchan7720/webhook-over-websocket/issues/3)) ([934445c](https://github.com/nonchan7720/webhook-over-websocket/commit/934445ca2993c59cc145b7ce132563af66d9e418))
* init local dev env ([5fd26fe](https://github.com/nonchan7720/webhook-over-websocket/commit/5fd26fe09b65c4c1d665f3ee18ac64e21d91c137))


### Bug Fixes

* app ([#8](https://github.com/nonchan7720/webhook-over-websocket/issues/8)) ([6e13f94](https://github.com/nonchan7720/webhook-over-websocket/commit/6e13f9438a054cf7e759c16fe184969f27bdb9c3))
* correct Dockerfile COPY path to match pkg/cmd directory structure ([#4](https://github.com/nonchan7720/webhook-over-websocket/issues/4)) ([75678c5](https://github.com/nonchan7720/webhook-over-websocket/commit/75678c5413fc2973fba6a9c28ef89e4434104c39))
* release action ([#7](https://github.com/nonchan7720/webhook-over-websocket/issues/7)) ([98c7035](https://github.com/nonchan7720/webhook-over-websocket/commit/98c703534281f4cca33a4816e591e422ec61b89b))
* release actions ([#6](https://github.com/nonchan7720/webhook-over-websocket/issues/6)) ([1c09bc3](https://github.com/nonchan7720/webhook-over-websocket/commit/1c09bc3b39a1f1fbbda60818d422866af9abccd8))


### Documentation

* update README and fix client command wiring ([#5](https://github.com/nonchan7720/webhook-over-websocket/issues/5)) ([7f582bc](https://github.com/nonchan7720/webhook-over-websocket/commit/7f582bced40ac8b0418630912111ea4fa88fbd54))
