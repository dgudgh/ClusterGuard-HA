#!/usr/bin/env node
"use strict";

// Renders the bilingual hotfix catalogue from the signed patches that actually
// exist on disk. The catalogue is generated, never hand-written: a hand-kept
// list is exactly how a fix ends up shipped without a patch.

const fs = require("node:fs");
const path = require("node:path");
const { execFileSync } = require("node:child_process");

const scriptDirectory = __dirname;
const defaultRepository = path.resolve(scriptDirectory, "..");

function parseArgs(argv) {
  const options = {
    repository: defaultRepository,
    artifactRoot: process.env.CG_HOTFIX_ARTIFACT_ROOT || defaultRepository,
    artifactDir: null,
    outEn: path.join(defaultRepository, "docs/hotfix-patches.md"),
    outZh: path.join(defaultRepository, "docs/zh-CN/hotfix-patches.md")
  };
  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index];
    const value = argv[index + 1];
    switch (argument) {
      case "--repo": options.repository = path.resolve(value); index += 1; break;
      case "--artifact-root": options.artifactRoot = path.resolve(value); index += 1; break;
      case "--artifact-dir": options.artifactDir = value; index += 1; break;
      case "--out-en": options.outEn = path.resolve(value); index += 1; break;
      case "--out-zh": options.outZh = path.resolve(value); index += 1; break;
      case "-h":
      case "--help":
        console.log("usage: render-hotfix-catalog.cjs [--repo DIR] [--artifact-root DIR] [--artifact-dir REL] [--out-en FILE] [--out-zh FILE]");
        process.exit(0);
        break;
      default:
        throw new Error(`unknown argument: ${argument}`);
    }
  }
  return options;
}

function readManifest(archive) {
  const json = execFileSync(
    "tar",
    ["-xzOf", archive, "clusterguard-hotfix/HOTFIX-MANIFEST.json"],
    { encoding: "utf8", maxBuffer: 32 * 1024 * 1024 }
  );
  const manifest = JSON.parse(json);
  if (manifest.kind !== "hotfix") {
    throw new Error(`${archive} is not a hotfix patch`);
  }
  manifest.archive = path.basename(archive);
  manifest.archiveSha256 = execFileSync("shasum", ["-a", "256", archive], { encoding: "utf8" }).split(/\s+/)[0];
  return manifest;
}

// The ledger, not the directory listing, decides which artifact in a directory is
// the one to apply. Several revisions of the same patch legitimately live side by
// side - the superseded ones keep their bytes because those bytes are the evidence
// of what a site once ran - so "newest file wins" would be a guess and "list them
// all as separate patches" tells an operator nothing about which to upload.
function loadLedger(options) {
  const ledgerPath = path.join(options.repository, "hotfixes/hotfix-publications.json");
  if (!fs.existsSync(ledgerPath)) {
    throw new Error(`publication ledger not found: ${ledgerPath}`);
  }
  const ledger = JSON.parse(fs.readFileSync(ledgerPath, "utf8"));
  const byFile = new Map();
  for (const entry of ledger.publications || []) byFile.set(entry.file, entry);
  return byFile;
}

function collect(options) {
  const releaseRoot = path.join(options.artifactRoot, "release");
  if (!fs.existsSync(releaseRoot)) {
    throw new Error(`release root not found: ${releaseRoot}`);
  }
  // Product-version directories and frozen RPM-baseline directories coexist.
  // The signed source and ledger establish admission and current identity.
  const directories = options.artifactDir
    ? [options.artifactDir]
    : fs.readdirSync(releaseRoot)
      .filter((name) => name.endsWith("-hotfixes") || /^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$/.test(name))
      .filter((name) => fs.statSync(path.join(releaseRoot, name)).isDirectory())
      .map((name) => `release/${name}`)
      .sort();
  const ledger = loadLedger(options);
  const manifests = [];
  for (const dir of directories) {
    const directory = path.join(options.artifactRoot, dir);
    for (const name of fs.readdirSync(directory).filter((n) => n.endsWith(".cgpatch")).sort()) {
      const manifest = readManifest(path.join(directory, name));
      manifest.dirName = dir;
      manifest.publication = ledger.get(`${dir}/${name}`) || null;
      manifests.push(manifest);
    }
  }
  if (manifests.length === 0) {
    throw new Error(`no hotfix patches found under ${releaseRoot}`);
  }
  return manifests;
}

// One section per patch, not one per file. The body describes the identity the
// ledger marks as the line's current entry point; every other revision of that
// same patch is listed underneath as identity history. A line with no current
// identity (every patch on it is frozen - the site already ran it and it cannot
// be rebuilt) falls back to its newest identity, and the section says so.
function group(manifests) {
  const byHotfix = new Map();
  for (const manifest of manifests) {
    if (!byHotfix.has(manifest.hotfix_id)) byHotfix.set(manifest.hotfix_id, []);
    byHotfix.get(manifest.hotfix_id).push(manifest);
  }
  const groups = [];
  for (const [hotfixId, entries] of byHotfix) {
    entries.sort((left, right) => (left.revision || 0) - (right.revision || 0));
    const current = entries.find((entry) => entry.publication && entry.publication.status === "current");
    const primary = current || entries[entries.length - 1];
    groups.push({
      hotfixId,
      primary,
      superseded: entries.filter((entry) => entry !== primary)
    });
  }
  return groups.sort((left, right) => right.hotfixId.localeCompare(left.hotfixId));
}

function restartUnits(manifest) {
  const units = [];
  for (const entry of manifest.files) {
    if (entry.restart_unit && !units.includes(entry.restart_unit)) units.push(entry.restart_unit);
  }
  return units;
}

function statusLabel(status, language) {
  const labels = {
    current: { en: "current", zh: "当前" },
    frozen: { en: "frozen evidence", zh: "已冻结的证据" },
    superseded: { en: "superseded", zh: "已被取代" }
  };
  return (labels[status] || { en: status, zh: status })[language];
}

function renderEnglish(groups) {
  const lines = [];
  lines.push("# Hotfix patch catalogue");
  lines.push("");
  lines.push("> Generated by `scripts/render-hotfix-catalog.cjs` from the signed patches that");
  lines.push("> exist on disk. Do not edit by hand — rebuild the patch and re-render instead.");
  lines.push("> `tools/verify-hotfix-patch-catalog.cjs` fails when a fix commit is not covered");
  lines.push("> by a patch or when this file disagrees with the artifacts.");
  lines.push("");
  lines.push("A `.cgupgrade` carries whole RPMs and can only be applied by the rolling upgrade");
  lines.push("executor. A hotfix patch carries exactly what the bug fixes it declares changed:");
  lines.push("the rebuilt binaries, the systemd units they touched, the source diff as evidence,");
  lines.push("and an apply/rollback pair. Every bug fix against a released version must be covered");
  lines.push("by a patch, otherwise a running site can only pick the fix up by waiting for the");
  lines.push("next release.");
  lines.push("");
  lines.push("One patch per site visit, not one per commit: two patches that both replace");
  lines.push("`/usr/local/bin/clusterguard` are order sensitive — applying the older one last");
  lines.push("silently undoes the newer fix. **Apply the newest patch for your baseline only, and");
  lines.push("never mix patches built for different baseline versions** — a patch from another");
  lines.push("baseline silently downgrades binaries back to its own release line.");
  lines.push("");
  lines.push("New hotfix specifications use `patch_version` and the versioned filename");
  lines.push("`clusterguard-MAJOR.MINOR.PATCH.BUGFIX.<arch>.cgpatch`; the new sealed release line starts at");
  lines.push("`3.1.1.1`. The signed manifest still carries the `HF-...` operation identity. Historical");
  lines.push("2.x and HF-named specifications retain their immutable legacy names. A signed patch is never rebuilt in place:");
  lines.push("a correction gets a new Bug-fix version and the earlier bytes stay in the directory");
  lines.push("as the only record of what a site ran. **The table");
  lines.push("below names the one to apply for each patch**; the other identities are history.");
  lines.push("");
  lines.push("| Hotfix | Severity | Fix commits | Build tree | Artifact to apply |");
  lines.push("| --- | --- | --- | --- | --- |");
  for (const { primary } of groups) {
    const commits = primary.fix_commits.map((commit) => `\`${commit.slice(0, 7)}\``).join(", ");
    lines.push(`| ${primary.hotfix_id} | ${primary.severity} | ${commits} | \`${primary.build_commit.slice(0, 7)}\` | \`${primary.dirName}/${primary.archive}\` |`);
  }
  lines.push("");
  lines.push("## Applying a patch");
  lines.push("");
  lines.push("**Preferred: the console, under Version Update -> Upload package.** After the upload the constrained helper applies the patch node by node: a hotfix leaves the installed RPM release alone, replaces only the files its signed manifest names, verifies every destination digest and restarts the units that manifest declares; any failure rolls back with the package's own `rollback.sh` and keeps the maintenance gate held. The console's package detail states whether the archive is a hotfix or a rolling upgrade.");
  lines.push("");
  lines.push("**Alternative: the controller command line.** Same signature and SHA-256 protection, but it does **not** restart services by itself, so restart them as it prints or the processes keep running the old code:");
  lines.push("");
  lines.push("```bash");
  lines.push("tar -xzf <ledger-artifact-path>.cgpatch");
  lines.push("cd clusterguard-hotfix");
  lines.push("bash apply.sh            # backs up, verifies SHA-256, installs, daemon-reload");
  lines.push("systemctl restart <unit> # apply.sh prints the units it needs; it never restarts by itself");
  lines.push("bash rollback.sh         # restores from the newest backup manifest");
  lines.push("```");
  lines.push("");
  for (const { primary, superseded } of groups) {
    lines.push(`## ${primary.hotfix_id} — ${primary.title.en}`);
    lines.push("");
    lines.push(`- Severity: ${primary.severity}`);
    lines.push(`- Fix commits: ${primary.fix_commits.map((commit) => `\`${commit.slice(0, 7)}\``).join(", ")}`);
    lines.push(`- Build tree: \`${primary.build_commit}\` (baseline \`${primary.base_commit}\` plus the fixes above and nothing else)`);
    lines.push(`- Applies to: ${primary.source.version}-${primary.source.release} → ${primary.target.version}-${primary.target.release} (${primary.target.rpm_architecture})`);
    if (primary.patch_version) lines.push(`- Patch version: \`${primary.patch_version}\``);
    lines.push(`- Artifact: \`${primary.dirName}/${primary.archive}\``);
    lines.push(`- SHA-256: \`${primary.archiveSha256}\``);
    if (primary.revision > 0 && primary.supersedes_artifact) {
      lines.push(`- Artifact identity: revision ${primary.revision}, supersedes \`${primary.supersedes_artifact.sha256}\` (\`${primary.supersedes_artifact.file}\`) — ${primary.supersedes_artifact.reason}`);
    }
    lines.push(`- Source diff: \`${primary.source_patch}\``);
    lines.push("- Payload:");
    for (const entry of primary.files) {
      const target = entry.install_path || "installer-only, no site path";
      lines.push(`  - \`${entry.artifact}\` → \`${target}\` (${entry.mode})`);
    }
    const units = restartUnits(primary);
    lines.push(`- Restart required: ${units.length ? units.map((unit) => `\`${unit}\``).join(", ") : "none"}`);
    lines.push("");
    if (primary.publication && primary.publication.status === "frozen") {
      lines.push("> **This identity is frozen.** It is the artifact a site actually ran, kept as evidence; it is not");
      lines.push("> the upload entry point for its line. It still carries the defects listed below, and it is never");
      lines.push("> rebuilt to today's rules — that would destroy the record.");
      lines.push("");
      for (const defect of primary.publication.known_defects || []) lines.push(`- Known defect: ${defect}`);
      if ((primary.publication.known_defects || []).length > 0) lines.push("");
    }
    if (primary.summary) {
      lines.push("### What this patch does");
      lines.push("");
      lines.push(primary.summary.en);
      lines.push("");
    }
    for (const [index, item] of (primary.fixes || []).entries()) {
      const commit = (item.commit || "").slice(0, 7);
      lines.push(`### ${primary.hotfix_id}.${index + 1} ${item.title.en} (\`${commit}\`, ${item.severity})`);
      lines.push("");
      lines.push(`- Symptom: ${item.symptom.en}`);
      lines.push("");
      lines.push(`- Root cause: ${item.root_cause.en}`);
      lines.push("");
      lines.push(`- Fix: ${item.fix.en}`);
      lines.push("");
      if ((item.applies_to || []).length > 0) {
        lines.push("- When to apply:");
        for (const entry of item.applies_to) lines.push(`  - ${entry}`);
        lines.push("");
      }
    }
    lines.push("### Verification");
    lines.push("");
    for (const step of primary.verification || []) lines.push(`- \`${step}\``);
    lines.push("");
    lines.push("### Rollback");
    lines.push("");
    lines.push(typeof primary.rollback === "string" ? primary.rollback : primary.rollback.en);
    lines.push("");
    if (superseded.length > 0) {
      lines.push("### Identity history of this patch");
      lines.push("");
      lines.push(`The section above describes \`${primary.archive}\`. The identities below stay in the delivery`);
      lines.push("directory under the immutability rule — **they are not installation entry points**, only the");
      lines.push("evidence of what a site ran or of what an earlier build of this patch contained. The full");
      lines.push("timeline is in `hotfixes/hotfix-publications.json`.");
      lines.push("");
      for (const entry of [...superseded].reverse()) {
        const status = entry.publication ? statusLabel(entry.publication.status, "en") : "unregistered";
        const size = entry.publication ? `${entry.publication.size.toLocaleString("en-US")} bytes` : "size not recorded";
        const revision = entry.revision > 0 ? `revision ${entry.revision}` : "first publication";
        lines.push(`- **${revision}** \`${entry.archiveSha256}\` (${size}, ${status})`);
        if (entry.supersedes_artifact) {
          lines.push(`  - Why it was replaced — quoted verbatim from its signed manifest (Chinese; the manifest field is a single string): ${entry.supersedes_artifact.reason}`);
        }
      }
      lines.push("");
    }
  }
  return lines.join("\n") + "\n";
}

function renderChinese(groups) {
  const lines = [];
  lines.push("# 热修补丁台账");
  lines.push("");
  lines.push("> 本文件由 `scripts/render-hotfix-catalog.cjs` 依据磁盘上真实存在的签名补丁包生成，");
  lines.push("> 请勿手工编辑：重新构建补丁包后重新生成本文件。");
  lines.push("> `tools/verify-hotfix-patch-catalog.cjs` 会在“修复提交没有补丁包”或“本文件与产物不一致”时失败。");
  lines.push("");
  lines.push("`.cgupgrade` 携带完整 RPM，只能由滚动升级执行器应用；热修补丁包只携带它声明的那些 bug");
  lines.push("修复真正改动的东西：重新构建的二进制、被修改的 systemd 单元、作为证据的源码差异，以及");
  lines.push("一对 apply/rollback 脚本。针对已发布版本的每一个 bug 修复都必须被某个补丁包覆盖，否则");
  lines.push("现场只能等下一个完整版本才能拿到修复。");
  lines.push("");
  lines.push("补丁按“一次现场处理”打包，不按提交拆分：两个都替换 `/usr/local/bin/clusterguard` 的");
  lines.push("补丁如果叠加，结果取决于安装顺序——后装旧的会把新修复盖掉。**只装你所在基线版本的最新一个包，");
  lines.push("不要混装不同基线版本的包**——装错基线的包会把二进制悄悄降级回它自己的发布线。");
  lines.push("");
  lines.push("新规格使用 `patch_version` 和 `clusterguard-MAJOR.MINOR.PATCH.BUGFIX.<架构>.cgpatch`；");
  lines.push("新的封板版本线从 `3.1.1.1` 开始。签名清单仍保留 `HF-...` 作为操作身份；历史 2.x 和 HF 文件名继续冻结。已签名的补丁**永不原地重建**：");
  lines.push("修订使用新的 Bug 修订版本，旧身份的字节留在目录里，作为“现场到底运行过什么”的唯一记录。");
  lines.push("多份文件。**下表列出的才是每个补丁应当安装的那一份**，其余身份只是历史。");
  lines.push("");
  lines.push("| 补丁编号 | 严重级别 | 覆盖修复提交 | 构建树 | 应当安装的产物 |");
  lines.push("| --- | --- | --- | --- | --- |");
  for (const { primary } of groups) {
    const commits = primary.fix_commits.map((commit) => `\`${commit.slice(0, 7)}\``).join("、");
    lines.push(`| ${primary.hotfix_id} | ${primary.severity} | ${commits} | \`${primary.build_commit.slice(0, 7)}\` | \`${primary.dirName}/${primary.archive}\` |`);
  }
  lines.push("");
  lines.push("## 应用补丁");
  lines.push("");
  lines.push("**首选：控制台「版本更新 → 上传升级包」**。上传签名包后由受限 Helper 逐节点应用：热修补丁不改动 RPM 版本，只替换签名清单声明的文件、逐节点核对落地摘要，并按清单重启受影响单元；任一步失败都会用包内 `rollback.sh` 自动回退并保留维护门禁。控制台的包详情会标明该包是「热修补丁」还是「滚动升级」。");
  lines.push("");
  lines.push("**备选：控制节点命令行**。同样受验签与 SHA-256 保护，但**不会自动重启服务**，需按输出自行重启，否则进程仍运行旧代码：");
  lines.push("");
  lines.push("```bash");
  lines.push("tar -xzf <台账中的制品路径>.cgpatch");
  lines.push("cd clusterguard-hotfix");
  lines.push("bash apply.sh            # 备份、校验 SHA-256、安装、daemon-reload");
  lines.push("systemctl restart <单元> # apply.sh 只打印需要重启的单元，不自动重启");
  lines.push("bash rollback.sh         # 按最新备份清单回滚");
  lines.push("```");
  lines.push("");
  for (const { primary, superseded } of groups) {
    lines.push(`## ${primary.hotfix_id} — ${primary.title.zh}`);
    lines.push("");
    lines.push(`- 严重级别：${primary.severity}`);
    lines.push(`- 覆盖修复提交：${primary.fix_commits.map((commit) => `\`${commit.slice(0, 7)}\``).join("、")}`);
    lines.push(`- 构建树：\`${primary.build_commit}\`（基线 \`${primary.base_commit}\` + 上述修复，不含其它提交）`);
    lines.push(`- 适用版本：${primary.source.version}-${primary.source.release} → ${primary.target.version}-${primary.target.release}（${primary.target.rpm_architecture}）`);
    if (primary.patch_version) lines.push(`- 补丁版本：\`${primary.patch_version}\``);
    lines.push(`- 产物：\`${primary.dirName}/${primary.archive}\``);
    lines.push(`- SHA-256：\`${primary.archiveSha256}\``);
    if (primary.revision > 0 && primary.supersedes_artifact) {
      lines.push(`- 产物身份：第 ${primary.revision} 修订，替代 \`${primary.supersedes_artifact.sha256}\`（\`${primary.supersedes_artifact.file}\`）——${primary.supersedes_artifact.reason}`);
    }
    lines.push(`- 源码差异：\`${primary.source_patch}\``);
    lines.push("- 交付内容：");
    for (const entry of primary.files) {
      const target = entry.install_path || "仅安装器，现场无对应路径";
      lines.push(`  - \`${entry.artifact}\` → \`${target}\`（${entry.mode}）`);
    }
    const units = restartUnits(primary);
    lines.push(`- 需要重启：${units.length ? units.map((unit) => `\`${unit}\``).join("、") : "无"}`);
    lines.push("");
    if (primary.publication && primary.publication.status === "frozen") {
      lines.push("> **本身份已冻结。** 它是现场实际执行过的那一份，作为证据保留，**不是该交付线的上传入口**。");
      lines.push("> 它仍带着下列缺陷，且不会按今天的规则回炉重造——那会毁掉这份证据。");
      lines.push("");
      for (const defect of primary.publication.known_defects || []) lines.push(`- 已知缺陷：${defect}`);
      if ((primary.publication.known_defects || []).length > 0) lines.push("");
    }
    if (primary.summary) {
      lines.push("### 本包概要");
      lines.push("");
      lines.push(primary.summary.zh);
      lines.push("");
    }
    for (const [index, item] of (primary.fixes || []).entries()) {
      const commit = (item.commit || "").slice(0, 7);
      lines.push(`### ${primary.hotfix_id}.${index + 1} ${item.title.zh}（\`${commit}\`，${item.severity}）`);
      lines.push("");
      lines.push(`- 现象：${item.symptom.zh}`);
      lines.push("");
      lines.push(`- 根因：${item.root_cause.zh}`);
      lines.push("");
      lines.push(`- 修复：${item.fix.zh}`);
      lines.push("");
      if ((item.applies_to || []).length > 0) {
        lines.push("- 何时需要应用：");
        for (const entry of item.applies_to) lines.push(`  - ${entry}`);
        lines.push("");
      }
    }
    lines.push("### 验证");
    lines.push("");
    for (const step of primary.verification || []) lines.push(`- \`${step}\``);
    lines.push("");
    lines.push("### 回滚");
    lines.push("");
    lines.push(typeof primary.rollback === "string" ? primary.rollback : primary.rollback.zh);
    lines.push("");
    if (superseded.length > 0) {
      lines.push("### 该补丁的身份历史");
      lines.push("");
      lines.push(`上面一节描述的是 \`${primary.archive}\`。下列身份按不可变规则留在交付目录里，`);
      lines.push("**不是安装入口**，只作为「现场到底运行过什么」或「本补丁早先构建成了什么」的证据。");
      lines.push("完整时间线见 `hotfixes/hotfix-publications.json`。");
      lines.push("");
      for (const entry of [...superseded].reverse()) {
        const status = entry.publication ? statusLabel(entry.publication.status, "zh") : "未登记";
        const size = entry.publication ? `${entry.publication.size.toLocaleString("en-US")} 字节` : "大小未登记";
        const revision = entry.revision > 0 ? `第 ${entry.revision} 修订` : "首次发布";
        lines.push(`- **${revision}** \`${entry.archiveSha256}\`（${size}，${status}）`);
        if (entry.supersedes_artifact) {
          lines.push(`  - 被替换的原因（原样引自它自己的签名清单）：${entry.supersedes_artifact.reason}`);
        }
      }
      lines.push("");
    }
  }
  return lines.join("\n") + "\n";
}

const main = () => {
  const options = parseArgs(process.argv.slice(2));
  const groups = group(collect(options));
  fs.mkdirSync(path.dirname(options.outEn), { recursive: true });
  fs.mkdirSync(path.dirname(options.outZh), { recursive: true });
  fs.writeFileSync(options.outEn, renderEnglish(groups));
  fs.writeFileSync(options.outZh, renderChinese(groups));
  const identities = groups.reduce((total, item) => total + 1 + item.superseded.length, 0);
  console.log(`rendered ${groups.length} hotfix patches (${identities} published identities)`);
  console.log(`en=${options.outEn}`);
  console.log(`zh=${options.outZh}`);
};

main();
