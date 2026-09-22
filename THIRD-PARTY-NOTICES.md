# Third-Party Notices / 第三方组件声明

ClusterGuard HA is licensed under the **GNU Affero General Public License, version 3
only** (`AGPL-3.0-only`). See [LICENSE](LICENSE).

This file lists every third-party component that the delivered artifacts contain or
depend on, with its license. **It does not change the license of ClusterGuard HA
itself, and the licenses listed here are not superseded by it.**

ClusterGuard HA 以 **GNU Affero 通用公共许可证第 3 版（仅此版本，`AGPL-3.0-only`）**
授权，见 [LICENSE](LICENSE)。本文件列出交付物包含或依赖的全部第三方组件及其许可。
**本文件不改变 ClusterGuard HA 自身的许可**，下列许可也不被其取代。

Copyright (C) 2026 dgudgh.

---

## 1. Go module dependencies / Go 模块依赖

These are the Go modules recorded in `go.mod` / `go.sum`. They are **linked into the
binary**, so their licenses travel with every distributed build. Determined by reading
the `LICENSE` file shipped in each module, not inferred.

以下模块出自 `go.mod` / `go.sum`，**被链接进二进制**，因此其许可随每一次分发传播。
许可是通过实读各模块自带的 `LICENSE` 文件确定的，不是推断。

| Module | Version | License (SPDX) | Copyright | Upstream |
| --- | --- | --- | --- | --- |
| `github.com/hashicorp/raft` | v1.7.3 | MPL-2.0 | © 2013 HashiCorp, Inc. | https://github.com/hashicorp/raft |
| `github.com/hashicorp/raft-boltdb/v2` | v2.3.1 | MPL-2.0 | © 2015 HashiCorp, Inc. | https://github.com/hashicorp/raft-boltdb |
| `github.com/hashicorp/golang-lru` | v0.5.0 | MPL-2.0 | HashiCorp, Inc. | https://github.com/hashicorp/golang-lru |
| `github.com/hashicorp/go-immutable-radix` | v1.0.0 | MPL-2.0 | HashiCorp, Inc. | https://github.com/hashicorp/go-immutable-radix |
| `github.com/boltdb/bolt` | v1.3.1 | MIT | © 2013 Ben Johnson | https://github.com/boltdb/bolt |
| `github.com/armon/go-metrics` | v0.4.1 | MIT | © 2013 Armon Dadgar | https://github.com/armon/go-metrics |
| `github.com/hashicorp/go-hclog` | v1.6.2 | MIT | © 2017 HashiCorp, Inc. | https://github.com/hashicorp/go-hclog |
| `github.com/hashicorp/go-metrics` | v0.5.4 | MIT | © 2013 HashiCorp, Inc. | https://github.com/hashicorp/go-metrics |
| `github.com/hashicorp/go-msgpack/v2` | v2.1.2 | MIT | © 2012-2015 Ugorji Nwoke | https://github.com/hashicorp/go-msgpack |
| `github.com/fatih/color` | v1.13.0 | MIT | © 2013 Fatih Arslan | https://github.com/fatih/color |
| `github.com/mattn/go-colorable` | v0.1.12 | MIT | © 2016 Yasuhiro Matsumoto | https://github.com/mattn/go-colorable |
| `github.com/mattn/go-isatty` | v0.0.14 | MIT | © Yasuhiro MATSUMOTO | https://github.com/mattn/go-isatty |
| `go.etcd.io/bbolt` | v1.3.5 | MIT | © 2013 Ben Johnson | https://github.com/etcd-io/bbolt |
| `golang.org/x/crypto` | v0.17.0 | BSD-3-Clause | © 2009 The Go Authors | https://cs.opensource.google/go/x/crypto |
| `golang.org/x/sys` | v0.15.0 | BSD-3-Clause | © 2009 The Go Authors | https://cs.opensource.google/go/x/sys |

`github.com/hashicorp/raft` is the only **direct** dependency; the remaining fourteen are
indirect. There is no vendored copy of any of them in this repository: dependencies are
resolved from the Go module proxy at build time.

其中只有 `github.com/hashicorp/raft` 是**直接依赖**，其余 14 个均为间接依赖。本仓库
不含任何 vendored 副本，依赖在构建时从 Go module proxy 解析。

### 1.1 Mozilla Public License 2.0 components / MPL-2.0 组件

Four modules are licensed under MPL-2.0: `hashicorp/raft`, `hashicorp/raft-boltdb/v2`,
`hashicorp/golang-lru`, `hashicorp/go-immutable-radix`.

四个模块采用 MPL-2.0：`hashicorp/raft`、`hashicorp/raft-boltdb/v2`、
`hashicorp/golang-lru`、`hashicorp/go-immutable-radix`。

A copy of the license is shipped at [`docs/licenses/MPL-2.0.txt`](docs/licenses/MPL-2.0.txt)
and installed to `/usr/share/doc/clusterguard-ha/MPL-2.0.txt` by the RPM.

许可副本随附于 [`docs/licenses/MPL-2.0.txt`](docs/licenses/MPL-2.0.txt)，并由 RPM 安装到
`/usr/share/doc/clusterguard-ha/MPL-2.0.txt`。

- These modules are used **unmodified**. Their source form is available from the upstream
  URLs in the table above, at the exact versions in `go.mod`, and can also be produced
  locally with `go mod download -x <module>@<version>`. That satisfies the source-availability
  obligation for MPL-2.0 §3.2 when a binary is distributed.
- ClusterGuard HA's own files remain under `AGPL-3.0-only`. The MPL-2.0 files remain under
  MPL-2.0; per MPL-2.0 §3.3 they are combined into this Larger Work without being relicensed.
- MPL-2.0 is compatible with AGPL-3.0. This is why `GPL-2.0-only` is **not** a valid choice
  for this project: MPL-2.0 does not permit combination with GPL-2.0-only, and no relicense
  of the four modules above is possible here.

- 上述模块**未经修改**使用。其源码可从表内 upstream 地址按 `go.mod` 中的确切版本取得，
  也可本地用 `go mod download -x <module>@<version>` 还原。分发二进制时，这已满足
  MPL-2.0 §3.2 的源码可获得性义务。
- ClusterGuard HA 自身文件仍为 `AGPL-3.0-only`；MPL-2.0 文件仍为 MPL-2.0，按 MPL-2.0 §3.3
  作为 Larger Work 组合，不因此被重新授权。
- MPL-2.0 与 AGPL-3.0 兼容。也正因如此，本项目**不可**选择 `GPL-2.0-only`：MPL-2.0 不允许
  与 GPL-2.0-only 组合，而这四个模块无法在此重新授权。

---

## 2. Components bundled in the offline kit / 离线介质中捆绑的第三方组件

The offline kit and the RPM are installers. Besides ClusterGuard HA's own files they may
carry third-party installers and packages. **Those are separate works under their own
licenses; the obligations they carry are independent of ClusterGuard HA's license and of
each other.**

离线介质与 RPM 是安装器。除 ClusterGuard HA 自身文件外，还会携带第三方安装包。
**它们是独立的作品、适用各自的许可；其义务与 ClusterGuard HA 的许可无关，彼此也无关。**

| Bundled item | License | Where it is recorded |
| --- | --- | --- |
| Rocky Linux 8 base/runtime RPMs (271 packages, target-matched) | per-package; predominantly MIT / BSD / GPL-2.0 / LGPL-2.1 / MPL-1.1 / Public Domain as declared by each RPM | `packaging/offline-dependencies/rocky-8-x86_64/PACKAGE-MANIFEST.txt` and `.../rocky-8-x86_64-runtime/PACKAGE-MANIFEST.txt` |
| MySQL Server binary packages (when embedded by `--database-package`) | GPL-2.0 (MySQL 8.0.x Community) | `packages/database/README.txt` inside the kit |
| PostgreSQL binaries or official release source (when embedded by `--database-package`) | PostgreSQL License | `packages/database/README.txt` inside the kit |
| `jq` (static binary shipped in the RPM) | MIT | n/a — see https://github.com/jqlang/jq |
| Lucide icon geometry inlined in the console | ISC | [`docs/licenses/lucide-ui-review.txt`](docs/licenses/lucide-ui-review.txt) |

Notes / 说明:

- ClusterGuard HA **drives** MySQL, PostgreSQL, Oracle and SQL Server by executing their
  own command-line clients (`mysql`, `psql`, `pg_ctl`, `sqlplus`, `sqlcmd`) as separate
  processes. It links against none of them, and no vendor's client library is compiled into
  the binary. This is why those vendors' licenses impose no obligation on ClusterGuard HA's
  own code. See `adapters/mysql/runner.go`, `adapters/postgresql/runner.go`,
  `adapters/oracle/oracle.go`, `adapters/sqlserver/sqlserver.go`.
- Redistributing MySQL Community binaries carries GPL-2.0 obligations that belong to whoever
  distributes them. The kit's `packages/database/README.txt` states that ClusterGuard HA never
  downloads database software to work around a vendor's license requirements; customers who
  supply their own packages take on that redistribution themselves.
- The `PACKAGE-MANIFEST.txt` files record each bundled RPM's NEVRA. The authoritative license
  of each such RPM is the metadata inside that RPM; query it with
  `rpm -qp --qf '%{NAME} %{LICENSE}\n' <file>.rpm` on a system with `rpm`.

- ClusterGuard HA 通过**以独立进程执行** MySQL、PostgreSQL、Oracle、SQL Server 自带的命令行
  客户端（`mysql`、`psql`、`pg_ctl`、`sqlplus`、`sqlcmd`）来操作它们，不链接任何一家，二进制
  内不含任何厂商客户端库。因此这些厂商的许可不对 ClusterGuard HA 自身代码产生义务。
- 再分发 MySQL Community 二进制会带来 GPL-2.0 义务，该义务属于执行分发的**那一方**。介质内
  `packages/database/README.txt` 已声明 ClusterGuard HA 不会联网下载数据库软件以规避厂商许可
  要求；由客户自行提供数据库包的，其再分发义务由客户承担。
- `PACKAGE-MANIFEST.txt` 记录每个捆绑 RPM 的 NEVRA。各 RPM 的权威许可是该 RPM 自带的元数据，
  可在装有 `rpm` 的系统上执行 `rpm -qp --qf '%{NAME} %{LICENSE}\n' <file>.rpm` 查询。

---

## 3. No Apache-2.0 code in this repository / 本仓库不含 Apache-2.0 代码

This repository is the ClusterGuard HA delivery line and has **no common ancestor** with the
separately published Orchestrator import line. Verified in this tree:

本仓库是 ClusterGuard HA 交付线，与另行发布的 Orchestrator 导入线**没有共同祖先**。在本树中核实：

```bash
git grep -inE "apache|outbrain" -- '*.go' '*.sh' '*.yaml' '*.yml' '*.json'   # 0 matches
git grep -ilnE "apache|outbrain"                                             # 0 files
```

So `Apache-2.0` never applied to this code, and no `NOTICE` file from that line is owed here.
Should any Apache-2.0-licensed code ever be added, it is compatible with AGPL-3.0 but its
`NOTICE` and attribution obligations must be added to this file in the same change.

因此 `Apache-2.0` 从未覆盖本代码，也无需在此附上那条血缘的 `NOTICE`。若将来引入任何
Apache-2.0 代码，它与 AGPL-3.0 兼容，但必须在同一次改动中把其 `NOTICE` 与署名义务写入本文件。

---

## 4. Obtaining the source / 获取本作品源码

`AGPL-3.0-only` §13 requires that users interacting with the software over a network be
offered the Corresponding Source. The complete source of the exact build you are running is
this Git repository, at the tag matching the version in `BUILD-INFO`.

`AGPL-3.0-only` 第 13 条要求：通过网络与软件交互的用户，必须能获得其对应源码。你所运行构建的
完整源码即本 Git 仓库，取 `BUILD-INFO` 中版本所对应的标签。

```bash
git clone https://github.com/dgudgh/ClusterGuard-HA.git
git checkout <version from BUILD-INFO>     # e.g. v2.2.103
```

Each offline kit also ships `BUILD-INFO` (version, commit, build time) and
`build-info.json`, which pin the commit the binaries were built from.

每套离线介质同时附带 `BUILD-INFO`（版本、提交、构建时间）与 `build-info.json`，用于锁定
二进制对应的提交。

If you received a binary without source access, or you are an enterprise customer needing a
specific form of source delivery, contact the maintainer through the channel your contract
provides.

若你收到的二进制无法对应到源码，或你是企业客户、需要有约定形式的源码交付，请通过合同约定的
渠道联系维护者。

---

## 5. Maintaining this file / 维护本文件

`tools/verify-license-consistency.cjs` checks that `LICENSE`, `go.mod` and this file stay in
agreement. Run it after any dependency change:

`tools/verify-license-consistency.cjs` 会校验 `LICENSE`、`go.mod` 与本文件是否仍然一致。
任何依赖变更后请运行：

```bash
node tools/verify-license-consistency.cjs
```
