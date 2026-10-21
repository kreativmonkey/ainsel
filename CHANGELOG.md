# Changelog

## [0.3.0](https://github.com/kreativmonkey/ainsel/compare/v0.2.0...v0.3.0) (2026-10-21)


### ⚠ BREAKING CHANGES

* **pi:** runtime images built from this entrypoint refuse to start without PI_PROVIDER and a model env — pair them with an operator that injects them (any current operator sets PI_PROVIDER and both model names).
* **operator:** existing Agents without spec.llm.provider become Ready=False after upgrade. Set spec.llm.provider (and spec.llm.model) on existing agents before upgrading.
* **api:** kubectl apply of an Agent with a provider outside the enum now fails at the API server.

### Features

* add inline GitHub review command ([#21](https://github.com/kreativmonkey/ainsel/issues/21)) ([5a0ae20](https://github.com/kreativmonkey/ainsel/commit/5a0ae20aec290f3abe2bfd8418a1addc71d762c4))
* **agent:** reference existing env secrets ([#17](https://github.com/kreativmonkey/ainsel/issues/17)) ([33135fb](https://github.com/kreativmonkey/ainsel/commit/33135fbfbe0c444bf52be3499a12dbcda2101f6b))
* **api:** constrain llm.provider to an enum and add per-provider baseUrl overrides ([36b3448](https://github.com/kreativmonkey/ainsel/commit/36b344843f272705ac81299415f79595a026e9f3)), closes [#96](https://github.com/kreativmonkey/ainsel/issues/96)
* **auth:** disable/enable users without deleting them ([a4a9652](https://github.com/kreativmonkey/ainsel/commit/a4a96525cc138d7acbb1609be40cb40908b99ea2))
* **chart:** render ingresses only for the configured ingress controller ([244f138](https://github.com/kreativmonkey/ainsel/commit/244f1386f512e4e2cb2f0f1dff3bdefc8154b8f6))
* **chart:** render nginx regex paths and annotations only for nginx class ([7915b26](https://github.com/kreativmonkey/ainsel/commit/7915b260c1909da0e54b8a7bbbd64ce954907738))
* **chart:** wire oidc+local auth mode ([b0fd078](https://github.com/kreativmonkey/ainsel/commit/b0fd078b60636d59ad5113477f0a454522dce25c))
* **ci:** replace gitleaks with betterleaks for secret scanning ([5cb1e44](https://github.com/kreativmonkey/ainsel/commit/5cb1e4407605162e1b1a2de11553d18cc34cae50)), closes [#65](https://github.com/kreativmonkey/ainsel/issues/65)
* **ci:** replace gitleaks workflow with betterleaks ([5051907](https://github.com/kreativmonkey/ainsel/commit/505190724021d4be3cab1cacf6dd1737b09bcd49))
* configure Pi subscription login in console ([#13](https://github.com/kreativmonkey/ainsel/issues/13)) ([622d1c7](https://github.com/kreativmonkey/ainsel/commit/622d1c7d109ad8229723a656d0eb0a73fddefc37))
* **frontend:** disable/enable toggle and disabled badge in user list ([9960ec5](https://github.com/kreativmonkey/ainsel/commit/9960ec5ef9049f3c1c50f71166d15a86a3db2c04))
* **frontend:** oidc+local login chooser with composite auth provider ([0995b68](https://github.com/kreativmonkey/ainsel/commit/0995b68a475456088f0e2e158f23ce0da4ab68a6))
* **hub:** add oidc+local coexistence auth mode with chained authenticator ([12b21ed](https://github.com/kreativmonkey/ainsel/commit/12b21ed6be4bcf7774589481c99d55e53ff88acc))
* **hub:** add per-user token_version column and store methods ([6c26134](https://github.com/kreativmonkey/ainsel/commit/6c26134ce8bf19c851d2f2f665e9762e01d61c1a)), closes [#112](https://github.com/kreativmonkey/ainsel/issues/112)
* **hub:** add users.disabled registry flag ([a264e65](https://github.com/kreativmonkey/ainsel/commit/a264e655b1bc973be3a21f50983de6c1b8923513))
* **hub:** block disabled users with innermost guard middleware ([b3c47a7](https://github.com/kreativmonkey/ainsel/commit/b3c47a794f5666066a37dd18e1e110679894bdca))
* **hub:** disable/enable users via PATCH /api/v1/users/{id} ([4611d08](https://github.com/kreativmonkey/ainsel/commit/4611d08bfcf1d652beb447b8e5b293100dfb97fd))
* **hub:** reject disabled users at login and user-token validation ([b253244](https://github.com/kreativmonkey/ainsel/commit/b2532449ba91c02f87c0ad4b157d288046258f85))
* **hub:** reject stale local JWTs via token_version claim ([23ce001](https://github.com/kreativmonkey/ainsel/commit/23ce001a8d2354dc723776677ead3c2d2bea593f)), closes [#112](https://github.com/kreativmonkey/ainsel/issues/112)
* **hub:** revoke local session JWTs via token_version claim ([992c209](https://github.com/kreativmonkey/ainsel/commit/992c209c09e302d0f3812a4c44adc44d59c29bc0))
* inspect upstream releases during PR reviews ([#23](https://github.com/kreativmonkey/ainsel/issues/23)) ([9db658a](https://github.com/kreativmonkey/ainsel/commit/9db658a64438fd2885776c8d7ce27e0d1450c50f))
* **observability:** distinguish unreported cache/cost from measured zeros ([b7c5ff3](https://github.com/kreativmonkey/ainsel/commit/b7c5ff3673dfe4f68d33caf7c105c46940ca2778))
* **observability:** distinguish unreported cache/cost from measured zeros ([#75](https://github.com/kreativmonkey/ainsel/issues/75)) ([a296dd5](https://github.com/kreativmonkey/ainsel/commit/a296dd55cd8b4180843ec990ead3243b71677b92))
* oidc+local coexistence auth mode with login chooser ([fceae7a](https://github.com/kreativmonkey/ainsel/commit/fceae7a1739ae7a77efd4e44faa3143ad70fc47e))
* **operator:** fail the reconcile on an unset or unknown LLM provider ([acc30f6](https://github.com/kreativmonkey/ainsel/commit/acc30f6cc487733ace6886cfadb0e6d22412fcbd)), closes [#96](https://github.com/kreativmonkey/ainsel/issues/96)
* **pi:** add GitHub App PR reviews ([5e9ba42](https://github.com/kreativmonkey/ainsel/commit/5e9ba426f945705a4f1071eb9b64ed89694d2935))
* **pi:** add GitHub App PR reviews ([494d485](https://github.com/kreativmonkey/ainsel/commit/494d4859f21f09f451ae8393b5c87e627f5e774e))
* **pi:** constrain PR reviews to verified source ([#14](https://github.com/kreativmonkey/ainsel/issues/14)) ([142d618](https://github.com/kreativmonkey/ainsel/commit/142d618358b0e545e96dbae030935934c1063aa1))
* **pi:** require an explicit provider and model at container start ([b167092](https://github.com/kreativmonkey/ainsel/commit/b1670923638b9a80e862e14add4ef43024fa42b2)), closes [#96](https://github.com/kreativmonkey/ainsel/issues/96)
* provision subscription credential storage ([#18](https://github.com/kreativmonkey/ainsel/issues/18)) ([88056ad](https://github.com/kreativmonkey/ainsel/commit/88056ad9bdb549b09ccf9effd3afba339c0fc0d3))
* support native subscription agent runtimes ([#3](https://github.com/kreativmonkey/ainsel/issues/3)) ([440d47d](https://github.com/kreativmonkey/ainsel/commit/440d47d905dde2ee445b903f42b2554bbe7bb358))
* support Pi Codex subscription auth ([#15](https://github.com/kreativmonkey/ainsel/issues/15)) ([492bc6a](https://github.com/kreativmonkey/ainsel/commit/492bc6a1d07985226fe1ffa02f024a7ee1ecfca3))


### Bug Fixes

* accept outcome=queued in the events list filter ([724dfdc](https://github.com/kreativmonkey/ainsel/commit/724dfdccfdc355e2419bff156420cab2c718769b))
* **agent:** surface image digest resolution failures as a Warning event ([1adc342](https://github.com/kreativmonkey/ainsel/commit/1adc34200dde479a4a91e93c9b089903e7e68954))
* **agent:** surface image digest resolution failures as a Warning event ([95de68f](https://github.com/kreativmonkey/ainsel/commit/95de68fb7642dd9a4ff3eb155b7ce275ca14748e))
* **auth:** default the OIDC roles claim to groups ([2438a7f](https://github.com/kreativmonkey/ainsel/commit/2438a7ff5fff0e9fefa14076428c802d64eb7317))
* **auth:** default the OIDC roles claim to groups ([78ac2fc](https://github.com/kreativmonkey/ainsel/commit/78ac2fceef3a76119b0b0287d133e2f0fdbba0ea))
* **auth:** sort roles taken from a map-shaped claim ([61dc847](https://github.com/kreativmonkey/ainsel/commit/61dc84785b188d0f3931db135b41f0d58b358eec))
* **auth:** support and harden generic OIDC ([405be82](https://github.com/kreativmonkey/ainsel/commit/405be820726e13240098ff8a179619d3f9cb5b35))
* **auth:** support and harden generic OIDC ([86f491f](https://github.com/kreativmonkey/ainsel/commit/86f491fce5b43e13b0f4cc3145b109afac5bb134))
* **auth:** support and harden generic OIDC ([#1](https://github.com/kreativmonkey/ainsel/issues/1)) ([938fb63](https://github.com/kreativmonkey/ainsel/commit/938fb635099e6ad18b4eedfb5c26e9bbd5cc4cd0))
* **auth:** support provider-neutral OIDC ([f3a809e](https://github.com/kreativmonkey/ainsel/commit/f3a809e8bcc3cb8a0f26484c1cd174d20d5055f3))
* **chart:** make the ingress TLS secret names configurable ([83bfa21](https://github.com/kreativmonkey/ainsel/commit/83bfa2145240f2170e3b7eac122054f8a89627f5))
* **chart:** make the ingress TLS secret names configurable ([b139c11](https://github.com/kreativmonkey/ainsel/commit/b139c111bac2134b97d1a6b00b5d96a0ab27ac00))
* **chart:** only emit OIDC_ROLES_CLAIM for MCP when it is set ([ec9f130](https://github.com/kreativmonkey/ainsel/commit/ec9f130462e051243fbc3c8bcf582bd69d0c3960))
* **chart:** run postgres probe as nonroot ([d67edb4](https://github.com/kreativmonkey/ainsel/commit/d67edb4a23415132a0705beeac9d3d5374d3eaae))
* **chart:** stop hardcoding the local-path storage class for postgres ([4cd07a5](https://github.com/kreativmonkey/ainsel/commit/4cd07a51df5cdc34b89f5e770541f08240dbc99f))
* **chart:** stop hardcoding the local-path storage class for postgres ([4dbfde1](https://github.com/kreativmonkey/ainsel/commit/4dbfde1a5a0f8ef5b731b47d222628b230ab0fb9))
* check out verified PR sources before review ([#10](https://github.com/kreativmonkey/ainsel/issues/10)) ([fd77911](https://github.com/kreativmonkey/ainsel/commit/fd77911c06f3ac88bac9364280ac5a75d8438a3d))
* **ci:** stop interpolating release-please outputs into shell ([f65a064](https://github.com/kreativmonkey/ainsel/commit/f65a064b74ced2a73592f0c971831c3a50a284dc))
* **ci:** stop interpolating release-please outputs into shell ([87d7c10](https://github.com/kreativmonkey/ainsel/commit/87d7c10d6c331522f9c5642e56a63c9eb10e1035))
* **ci:** stop the secret scan failing on placeholder credentials in docs ([b6bd84a](https://github.com/kreativmonkey/ainsel/commit/b6bd84a6cc00850175b57bc14d94f53b7cbb2b22))
* **ci:** stop the secret scan failing on placeholder credentials in docs ([0ece676](https://github.com/kreativmonkey/ainsel/commit/0ece6765943b9c9e3c3ea5cfb1dd69ba8d4393df))
* **frontend:** read the all-time token tile from the cumulative ledger ([b1c884f](https://github.com/kreativmonkey/ainsel/commit/b1c884fefc7b145fc2fe70ddd7cfb9cbae58c913))
* **frontend:** read the all-time token tile from the cumulative ledger ([7ebf0ad](https://github.com/kreativmonkey/ainsel/commit/7ebf0ad76ecc60480f45f79c3fc39a707cf7c5de))
* honor Codex CLI default model ([#8](https://github.com/kreativmonkey/ainsel/issues/8)) ([bcf38de](https://github.com/kreativmonkey/ainsel/commit/bcf38de743dd2b21d2fbd13dfb63576f8c75a076))
* **hub:** fail startup on skipped or renumbered migrations ([b73ca8e](https://github.com/kreativmonkey/ainsel/commit/b73ca8e58723183953aa58ab05370b11ff142bba))
* **hub:** fail startup on skipped or renumbered migrations ([ea27b31](https://github.com/kreativmonkey/ainsel/commit/ea27b3154bec77e72dce7cfafb232805aa617ac6))
* **hub:** use the JSON error envelope in the disabled guard ([3ff386d](https://github.com/kreativmonkey/ainsel/commit/3ff386d581f4666c72aa3f4c664aeb38cd1e7553))
* **mcp:** reject credentials in MCP_RESOURCE_URL ([ae936f0](https://github.com/kreativmonkey/ainsel/commit/ae936f06b9d75f0e7611a122c3954cf767a6edf4))
* **observability:** backfill legacy ambiguous zeros to NULL in migration 0025 ([40becaa](https://github.com/kreativmonkey/ainsel/commit/40becaa3f35d4b9e0db8afca34c4a7f15a35b7c6))
* own PR checkout directory under workspace ([#11](https://github.com/kreativmonkey/ainsel/issues/11)) ([b48f3b7](https://github.com/kreativmonkey/ainsel/commit/b48f3b7afee818934ade61adcb062c6f37946793))
* **pi:** expose the scoped PR review tool ([#16](https://github.com/kreativmonkey/ainsel/issues/16)) ([7edc168](https://github.com/kreativmonkey/ainsel/commit/7edc1688d8e7cfe7a9ba5080e283dd402f5f98bb))
* **pi:** record compaction and branch-summary LLM usage ([3185b85](https://github.com/kreativmonkey/ainsel/commit/3185b8538d34ddad181741860e8d77307c044640))
* **pi:** record compaction and branch-summary LLM usage ([72e928c](https://github.com/kreativmonkey/ainsel/commit/72e928c0699179b077fc119f465447c3a0b2130a)), closes [#76](https://github.com/kreativmonkey/ainsel/issues/76)
* resolve golangci-lint findings on main ([b75736d](https://github.com/kreativmonkey/ainsel/commit/b75736d274b72626f4303353ec1b673d09833bae))
* resolve golangci-lint findings on main ([4c01f55](https://github.com/kreativmonkey/ainsel/commit/4c01f550b82fed4f2640b4b20b61e4936266bfc2))
* **shared/api:** resolve filter paths through arrays of objects ([4587dd9](https://github.com/kreativmonkey/ainsel/commit/4587dd9f8da7aba39d8e65afbd7a7d4f42abffdc))
* **shared/api:** resolve filter paths through arrays of objects ([1e03357](https://github.com/kreativmonkey/ainsel/commit/1e0335740c13544c1e225cc507384fd8f6f14f88))
* support f5 nginx connector minions ([#7](https://github.com/kreativmonkey/ainsel/issues/7)) ([62926ca](https://github.com/kreativmonkey/ainsel/commit/62926ca04b8eda0efab5c167847cdfded2a4be7a))
* unblock native subscription agent onboarding ([#5](https://github.com/kreativmonkey/ainsel/issues/5)) ([2af81c2](https://github.com/kreativmonkey/ainsel/commit/2af81c2923405d1ef0cc84cfd7cf847dfc272cb5))
* use compatible f5 connector uri rewrite ([#9](https://github.com/kreativmonkey/ainsel/issues/9)) ([6f1ca61](https://github.com/kreativmonkey/ainsel/commit/6f1ca61f83faa82ba9e4eeae111d4ffe77bdd053))
* use English throughout Kellerkind reviews ([#22](https://github.com/kreativmonkey/ainsel/issues/22)) ([22885db](https://github.com/kreativmonkey/ainsel/commit/22885db25147f0b6918f15be51e72dc37b4b012d))

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
