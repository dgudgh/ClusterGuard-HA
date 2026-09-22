# Licensing and Compliance

> This chapter states which license applies to ClusterGuard HA, what obligations using it
> creates, and what enterprise customers need to know. It restates the license terms
> themselves and **is not legal advice**; consult your own counsel on how they apply to you.

## In one sentence

**ClusterGuard HA is licensed under `AGPL-3.0-only`. You may use, modify and sell it freely —
but once you make a modified version available to others, including as a network service
only, you must publish the Corresponding Source under the same license.**

## 1. What license applies

| Item | Value |
| --- | --- |
| SPDX identifier | `AGPL-3.0-only` |
| Full name | GNU Affero General Public License, version 3 |
| License text | repository root [`LICENSE`](../../LICENSE) (verbatim official text) |
| Copyright | Copyright (C) 2026 dgudgh |
| Third-party components | [`THIRD-PARTY-NOTICES.md`](../../THIRD-PARTY-NOTICES.md) |

Choosing **only** rather than `or-later` is deliberate: it pins the license to v3 so a future
FSF release does not apply automatically.

The `license` field in `packaging/rpm/nfpm.yaml`, the file
`/usr/share/doc/clusterguard-ha/LICENSE` inside the RPM, the READMEs and this page all state
the same thing.

## 2. Obligations at a glance

| What you do | Must you publish source? | Basis |
| --- | --- | --- |
| Run it internally, unmodified, without giving copies to outsiders | No | no distribution occurred, and §13 is not triggered |
| Modify it for internal use only, not exposed as a service | No (the modifications are still bound by `AGPL-3.0-only`) | no distribution occurred |
| **Modify it and let people reach it over a network (web, API)** | **Yes — offer those users the Corresponding Source** | §13 |
| Give a binary or source copy to a third party (customer, partner, mirror, embedded in your product) | **Yes** — include `LICENSE`, mark your changes, provide Corresponding Source | §4–§6 |
| Ship it inside a product you sell | **Yes**, and the parts you make available must still satisfy this license | §5, §13 |
| Charge money | Unrestricted — charging is fully permitted | §1, §4 |

Two things must be stated plainly:

- **"Corresponding Source" means the complete source of your version**, including your
  modifications — not just the upstream original.
- §13 triggers on **modification plus network interaction**. If you run the official RPM
  unmodified, §13 adds nothing; but the moment distribution occurs, the §4–§6 obligations
  take effect immediately.

## 3. Three typical scenarios

### 3.1 Internal deployment, unmodified

You take the offline kit, install it in your own datacenter and operate your own databases.
**No obligation to publish anything.**

### 3.2 Modified, then exposed as a network service

You adapt the console and open it to another department, a subsidiary, or external customers
through a browser or API. **Those users must be able to obtain the complete source of your
adapted version** — typically a prominent "Source" entry in the console pointing at a place
where it can be fetched. This is the increment AGPL adds over GPL, and the reason this
project chose AGPL.

### 3.3 Redistribution

Handing the RPM or offline kit to a third party, or selling it as part of your product.
You **must**:

1. ship the complete `LICENSE`;
2. mark clearly which changes you made, and when;
3. provide a way to obtain the Corresponding Source;
4. add no terms that conflict with this license.

## 4. Charging is unaffected

`AGPL-3.0-only` **does not forbid commercial use and does not forbid charging**. You may sell
at any price, offer paid support, or offer paid hosting. What it restricts is taking the code
and denying others the code — not earning revenue.

## 5. What enterprise customers must know

This needs to be said outright, because it directly shapes the delivery policy:

> **Under `AGPL-3.0-only`, whoever receives the software has the legal right to redistribute
> it. Copyright cannot stop them.**

The rule in `docs/zh-CN/version-release-policy.md` §3.1 — that update packages go only to
contracted enterprise customers, stay local and are never uploaded to a public channel — is
therefore a **delivery and support policy, not a license restriction**. Contracted customers
are still bound by this license; the maintainer's commercial value comes from version
delivery, production qualification, support response and accountability, not from copyright
containment.

If your business model requires customers to be *forbidden* from redistributing — i.e.
traditional closed-source licensing — `AGPL-3.0-only` cannot deliver that. You would need a
proprietary license or dual licensing, see §8. **The current state is `AGPL-3.0-only`, with no
room for ambiguity on this point.**

## 6. Third-party components

The binaries link 15 Go modules (4 under MPL-2.0, 11 under MIT / BSD-3-Clause), and the
offline kit bundles 271 Rocky Linux 8 dependency RPMs and may embed official MySQL /
PostgreSQL packages. Those licenses are **independent** and are not altered by this project
using AGPL. The per-component list, copyright holders and how to obtain sources are in
[`THIRD-PARTY-NOTICES.md`](../../THIRD-PARTY-NOTICES.md):

- A copy of MPL-2.0 ships at [`docs/licenses/MPL-2.0.txt`](../licenses/MPL-2.0.txt) and is
  installed by the RPM to `/usr/share/doc/clusterguard-ha/MPL-2.0.txt`.
- ClusterGuard HA drives each database by **executing that vendor's own command-line client**,
  and links against no vendor client library, so the vendors' licenses do not constrain this
  project's own code.
- Redistributing the bundled MySQL Community packages carries GPL-2.0 obligations, and those
  belong to whoever performs that distribution.

## 7. How to obtain the source

```bash
git clone https://github.com/dgudgh/ClusterGuard-HA.git
git checkout <version from BUILD-INFO>     # e.g. v2.2.103
```

`BUILD-INFO` and `build-info.json`, shipped with every kit, record the version, commit and
build time, which ties a binary back to an exact commit. That is the operable form of
"Corresponding Source" under §13.

## 8. Maintainer operations: relicensing and dual licensing

All 200 commits in this repository come from a single author with no co-signers, so **there
are no third-party contributors whose consent would be required**. The maintainer can
therefore:

- **relicense**: replace `AGPL-3.0-only` with another license (Apache-2.0, proprietary, …) by
  swapping `LICENSE` and updating every location below;
- **dual license**: issue a commercial license to enterprise customers who do not accept the
  AGPL obligations, while continuing to publish the community edition under AGPL.

Any license change must update all of the following **in the same change**, or the repository
contradicts itself:

| Location | Role |
| --- | --- |
| `LICENSE` | authoritative text |
| `license` in `packaging/rpm/nfpm.yaml` | RPM metadata (SPDX identifier) |
| `contents` in `packaging/rpm/nfpm.yaml` | ensures `LICENSE` lands in the RPM |
| `scripts/build-clusterguard-rpm.sh` | copies `LICENSE` into the packaging stage |
| `scripts/build-clusterguard-offline-kit.sh` | puts `LICENSE` into the offline kit |
| `README.md` / `README.zh-CN.md` | public front page |
| `docs/zh-CN/licensing.md` / `docs/en-US/licensing.md` | this page, both languages |
| `THIRD-PARTY-NOTICES.md` | third-party list and compatibility conclusions |
| `AGENTS.md` | binding rule for subsequent agents |

Then run:

```bash
node tools/verify-license-consistency.cjs
```

## 9. No warranty, and irrevocability

The software is provided "as is", without warranty of any kind. A license is irrevocable:
once someone has obtained your version under `AGPL-3.0-only`, you **cannot** withdraw that
grant retroactively. Deciding the license **before** publishing therefore matters more than
changing it afterwards.

## Related documents

- [Third-Party Notices](../../THIRD-PARTY-NOTICES.md)
- [Version and Release Policy](version-release-policy.md)
- [Update and Patch Manual](update-and-patch.md)
