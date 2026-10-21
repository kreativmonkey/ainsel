# Changelog

## [0.2.0](https://github.com/ainsel-project/ainsel/compare/v0.1.5...v0.2.0) (2026-10-18)


### Features

* **ci:** replace gitleaks with betterleaks for secret scanning ([5cb1e44](https://github.com/ainsel-project/ainsel/commit/5cb1e4407605162e1b1a2de11553d18cc34cae50)), closes [#65](https://github.com/ainsel-project/ainsel/issues/65)
* **ci:** replace gitleaks workflow with betterleaks ([5051907](https://github.com/ainsel-project/ainsel/commit/505190724021d4be3cab1cacf6dd1737b09bcd49))
* **observability:** distinguish unreported cache/cost from measured zeros ([b7c5ff3](https://github.com/ainsel-project/ainsel/commit/b7c5ff3673dfe4f68d33caf7c105c46940ca2778))
* **observability:** distinguish unreported cache/cost from measured zeros ([#75](https://github.com/ainsel-project/ainsel/issues/75)) ([a296dd5](https://github.com/ainsel-project/ainsel/commit/a296dd55cd8b4180843ec990ead3243b71677b92))


### Bug Fixes

* accept outcome=queued in the events list filter ([724dfdc](https://github.com/ainsel-project/ainsel/commit/724dfdccfdc355e2419bff156420cab2c718769b))
* **auth:** default the OIDC roles claim to groups ([2438a7f](https://github.com/ainsel-project/ainsel/commit/2438a7ff5fff0e9fefa14076428c802d64eb7317))
* **auth:** default the OIDC roles claim to groups ([78ac2fc](https://github.com/ainsel-project/ainsel/commit/78ac2fceef3a76119b0b0287d133e2f0fdbba0ea))
* **auth:** sort roles taken from a map-shaped claim ([61dc847](https://github.com/ainsel-project/ainsel/commit/61dc84785b188d0f3931db135b41f0d58b358eec))
* **auth:** support and harden generic OIDC ([405be82](https://github.com/ainsel-project/ainsel/commit/405be820726e13240098ff8a179619d3f9cb5b35))
* **auth:** support and harden generic OIDC ([86f491f](https://github.com/ainsel-project/ainsel/commit/86f491fce5b43e13b0f4cc3145b109afac5bb134))
* **chart:** make the ingress TLS secret names configurable ([83bfa21](https://github.com/ainsel-project/ainsel/commit/83bfa2145240f2170e3b7eac122054f8a89627f5))
* **chart:** make the ingress TLS secret names configurable ([b139c11](https://github.com/ainsel-project/ainsel/commit/b139c111bac2134b97d1a6b00b5d96a0ab27ac00))
* **chart:** only emit OIDC_ROLES_CLAIM for MCP when it is set ([ec9f130](https://github.com/ainsel-project/ainsel/commit/ec9f130462e051243fbc3c8bcf582bd69d0c3960))
* **chart:** stop hardcoding the local-path storage class for postgres ([4cd07a5](https://github.com/ainsel-project/ainsel/commit/4cd07a51df5cdc34b89f5e770541f08240dbc99f))
* **chart:** stop hardcoding the local-path storage class for postgres ([4dbfde1](https://github.com/ainsel-project/ainsel/commit/4dbfde1a5a0f8ef5b731b47d222628b230ab0fb9))
* **ci:** stop interpolating release-please outputs into shell ([f65a064](https://github.com/ainsel-project/ainsel/commit/f65a064b74ced2a73592f0c971831c3a50a284dc))
* **ci:** stop interpolating release-please outputs into shell ([87d7c10](https://github.com/ainsel-project/ainsel/commit/87d7c10d6c331522f9c5642e56a63c9eb10e1035))
* **frontend:** read the all-time token tile from the cumulative ledger ([b1c884f](https://github.com/ainsel-project/ainsel/commit/b1c884fefc7b145fc2fe70ddd7cfb9cbae58c913))
* **frontend:** read the all-time token tile from the cumulative ledger ([7ebf0ad](https://github.com/ainsel-project/ainsel/commit/7ebf0ad76ecc60480f45f79c3fc39a707cf7c5de))
* **mcp:** reject credentials in MCP_RESOURCE_URL ([ae936f0](https://github.com/ainsel-project/ainsel/commit/ae936f06b9d75f0e7611a122c3954cf767a6edf4))
* **observability:** backfill legacy ambiguous zeros to NULL in migration 0025 ([40becaa](https://github.com/ainsel-project/ainsel/commit/40becaa3f35d4b9e0db8afca34c4a7f15a35b7c6))
* **pi:** record compaction and branch-summary LLM usage ([3185b85](https://github.com/ainsel-project/ainsel/commit/3185b8538d34ddad181741860e8d77307c044640))
* **pi:** record compaction and branch-summary LLM usage ([72e928c](https://github.com/ainsel-project/ainsel/commit/72e928c0699179b077fc119f465447c3a0b2130a)), closes [#76](https://github.com/ainsel-project/ainsel/issues/76)
* resolve golangci-lint findings on main ([b75736d](https://github.com/ainsel-project/ainsel/commit/b75736d274b72626f4303353ec1b673d09833bae))
* resolve golangci-lint findings on main ([4c01f55](https://github.com/ainsel-project/ainsel/commit/4c01f550b82fed4f2640b4b20b61e4936266bfc2))
