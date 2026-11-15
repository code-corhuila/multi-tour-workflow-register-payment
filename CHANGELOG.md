# Changelog

All notable changes of `multi-tour-workflow-register-payment` (Register-payment saga) are documented in this file.
The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [3.0.0] - 2026-11-14

Cut 3 release. Every entry below is a pull request merged into `qa` and re-applied to `release/3.0.0` with `git cherry-pick -x`; the heading of each group is the user story named in the pull request, and the entry is the title of the pull request without its type. The frozen head of `qa` is `65832482e2c182e15aef5ee6c4f4d706a95f80ed`.

### Added

#### HU-WFL-002 - Register a payment across cash and reservations

- Read the validated payment from commercial-reservation after an unclear step 2 ([PR 41](https://github.com/code-corhuila/multi-tour-workflow-register-payment/pull/41))

### Changed

#### HU-WFL-002 - Register a payment across cash and reservations

- The configuration of the register-payment saga (part 1) ([PR 11](https://github.com/code-corhuila/multi-tour-workflow-register-payment/pull/11))
- The saga tables of the register-payment saga (part 2) ([PR 12](https://github.com/code-corhuila/multi-tour-workflow-register-payment/pull/12))
- The domain and use case of the register-payment saga (part 3) ([PR 13](https://github.com/code-corhuila/multi-tour-workflow-register-payment/pull/13))
- The participant calls of the register-payment saga (part 4) ([PR 14](https://github.com/code-corhuila/multi-tour-workflow-register-payment/pull/14))
- The postgres store of the register-payment saga (part 5) ([PR 15](https://github.com/code-corhuila/multi-tour-workflow-register-payment/pull/15))
- The entry route of the register-payment saga (part 6) ([PR 16](https://github.com/code-corhuila/multi-tour-workflow-register-payment/pull/16))
- The wiring and the contract of the register-payment saga (part 7) ([PR 17](https://github.com/code-corhuila/multi-tour-workflow-register-payment/pull/17))
- The healthcheck that follows LISTEN_ADDR ([PR 19](https://github.com/code-corhuila/multi-tour-workflow-register-payment/pull/19))
- The optional reason of the saga ([PR 21](https://github.com/code-corhuila/multi-tour-workflow-register-payment/pull/21))
- The checks on pull requests to main ([PR 23](https://github.com/code-corhuila/multi-tour-workflow-register-payment/pull/23))
- Run the test workflow only on pull requests to develop and qa ([PR 39](https://github.com/code-corhuila/multi-tour-workflow-register-payment/pull/39))

### Fixed
- The store tests give each saga a payment of its own, so they respect the single income per payment ([PR 43](https://github.com/code-corhuila/multi-tour-workflow-register-payment/pull/43))

#### HU-WFL-002 - Register a payment across cash and reservations

- One saga per payment, replay checks tenant and body, log carries tenant and correlation id ([PR 26](https://github.com/code-corhuila/multi-tour-workflow-register-payment/pull/26))
- Leave an unknown outcome of the income step to the resume ([PR 27](https://github.com/code-corhuila/multi-tour-workflow-register-payment/pull/27))
- Settle an unknown outcome of the income step by reading the income in the cash ([PR 29](https://github.com/code-corhuila/multi-tour-workflow-register-payment/pull/29))
- Make the idempotency key unique per tenant ([PR 31](https://github.com/code-corhuila/multi-tour-workflow-register-payment/pull/31))
- Answer a repeated key with its saga whatever the body ([PR 34](https://github.com/code-corhuila/multi-tour-workflow-register-payment/pull/34))
- Read the payment before deciding an unclear outcome of the payment step ([PR 36](https://github.com/code-corhuila/multi-tour-workflow-register-payment/pull/36))
- Read the income of a payment from the cash ([PR 37](https://github.com/code-corhuila/multi-tour-workflow-register-payment/pull/37))

[Unreleased]: https://github.com/code-corhuila/multi-tour-workflow-register-payment/compare/v3.0.0...HEAD
[3.0.0]: https://github.com/code-corhuila/multi-tour-workflow-register-payment/releases/tag/v3.0.0
