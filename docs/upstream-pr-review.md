# Upstream PR review

- [#2399: prevent redemption code reuse](https://github.com/songquanpeng/one-api/pull/2399): adopted the GORM row lock for the redemption transaction. Added a one-use regression test.
- [#2447: atomic quota deduction](https://github.com/songquanpeng/one-api/pull/2447): adapted guarded user and token deductions and a transaction for each combined charge. Also removed the trusted-user bypass and kept cache updates after reservation.
- [#2390: image URL SSRF protection](https://github.com/songquanpeng/one-api/pull/2390): reviewed, but not applied. Its URL check covers `GetImageFromUrl` only; `GetImageSizeFromUrl` still fetches user supplied URLs. A complete fix also needs redirect and DNS rebinding checks at connection time.

The quota changes prevent balances from going below zero during concurrent reservations. Actual usage can exceed a reserved estimate, so billing policy for that case still needs a separate decision.
