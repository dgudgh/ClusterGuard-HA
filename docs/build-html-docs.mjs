import { createRequire } from "node:module";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const require = createRequire(import.meta.url);
let marked;
try {
  ({ marked } = require("marked"));
} catch (error) {
  console.error("The HTML documentation builder requires marked 17.0.5. Run 'npm install' in docs/ before rebuilding.");
  throw error;
}

const docsDirectory = path.dirname(fileURLToPath(import.meta.url));
const projectRoot = path.resolve(docsDirectory, "..");
const outputRoot = path.join(docsDirectory, "html");
const sourceAssets = path.join(docsDirectory, "html-src");

// Release notes are discovered from the filesystem instead of being listed by
// hand. A hand-maintained list silently falls behind the files it describes:
// before this change the build only named releases up to 2.2.47, so the 21
// later Chinese notes that already existed were unreachable from the offline
// documentation centre even though their markdown was present.
const versionedReleasePattern = /^release-(\d+(?:\.\d+)*)\.md$/;

function releaseTitle(locale, version) {
  return locale === "zh-CN" ? `${version} 发布说明` : `Release ${version}`;
}

function compareReleaseVersionsDescending(left, right) {
  const leftParts = left.split(".").map(Number);
  const rightParts = right.split(".").map(Number);
  for (let index = 0; index < Math.max(leftParts.length, rightParts.length); index += 1) {
    const difference = (rightParts[index] || 0) - (leftParts[index] || 0);
    if (difference !== 0) return difference;
  }
  return 0;
}

function discoverReleases(locale) {
  return fs
    .readdirSync(path.join(docsDirectory, locale))
    .map((name) => (versionedReleasePattern.exec(name) || [])[1])
    .filter(Boolean)
    .sort(compareReleaseVersionsDescending)
    .map((version) =>
      entry(locale, `release-${version}`, releaseTitle(locale, version), `docs/${locale}/release-${version}.md`)
    );
}

const releases = {
  "en-US": discoverReleases("en-US"),
  "zh-CN": discoverReleases("zh-CN")
};

const releaseSlugs = {
  "en-US": releases["en-US"].map((item) => item.slug),
  "zh-CN": releases["zh-CN"].map((item) => item.slug)
};

const groups = {
  "en-US": [
    ["Start Here", ["index", "product-overview", "product-tour", "architecture", ...releaseSlugs["en-US"]]],
    ["Deploy and Migrate", ["offline-rpm-install", "database-preparation", "orchestrator-migration"]],
    ["Operate", ["operations-manual", "api-operations", "postgresql-ha", "docker-swarm-mysql", "kubernetes-mysql", "update-and-patch", "version-release-policy"]],
    ["Qualification", ["proven-mysql-ha-methods", "mysql-feature-parity-acceptance", "mysql-former-primary-recovery", "mysql-production-qualification", "postgresql-production-qualification", "docker-swarm-mysql-validation", "production-chaos-test", "power-lifecycle-test"]]
  ],
  "zh-CN": [
    ["开始使用", ["index", "product-overview", "product-tour", "architecture", ...releaseSlugs["zh-CN"]]],
    ["部署与迁移", ["offline-rpm-install", "database-preparation", "orchestrator-migration"]],
    ["日常运维", ["operations-manual", "api-operations", "postgresql-ha", "docker-swarm-mysql", "kubernetes-mysql", "update-and-patch", "version-release-policy"]],
    ["验收证据", ["proven-mysql-ha-methods", "mysql-feature-parity-acceptance", "mysql-former-primary-recovery", "mysql-production-qualification", "postgresql-production-qualification", "docker-swarm-mysql-validation", "production-chaos-test", "power-lifecycle-test", "release-recovery-acceptance-checklist"]]
  ]
};

const entries = [
  ...releases["en-US"],
  ...releases["zh-CN"],

  entry("en-US", "index", "ClusterGuard HA Documentation", "docs/en-US/README.md"),
  entry("en-US", "product-overview", "Product Overview", "README.md"),
  entry("en-US", "product-tour", "Console Product Tour", "docs/en-US/product-tour.md"),
  entry("en-US", "architecture", "Architecture", "docs/architecture.md"),
  entry("en-US", "offline-rpm-install", "Offline RPM Installation", "docs/en-US/offline-rpm-install.md"),
  entry("en-US", "database-preparation", "Database Preparation", "docs/en-US/database-preparation.md"),
  entry("en-US", "orchestrator-migration", "Migration from Orchestrator", "docs/en-US/orchestrator-migration.md"),
  entry("en-US", "operations-manual", "Operations Manual", "docs/en-US/operations-manual.md"),
  entry("en-US", "api-operations", "API Operations Reference", "docs/operations.md"),
  entry("en-US", "postgresql-ha", "PostgreSQL HA", "docs/postgresql-ha.md"),
  entry("en-US", "docker-swarm-mysql", "Docker Swarm MySQL", "docs/en-US/docker-swarm-mysql.md"),
  entry("en-US", "kubernetes-mysql", "Kubernetes MySQL", "docs/en-US/kubernetes-mysql.md"),
  entry("en-US", "update-and-patch", "Version Update and Rollback", "docs/en-US/update-and-patch.md"),
  entry("en-US", "version-release-policy", "Version and Release Policy", "docs/en-US/version-release-policy.md"),
  entry("en-US", "proven-mysql-ha-methods", "Proven MySQL HA Methods", "docs/proven-mysql-ha-methods.md"),
  entry("en-US", "mysql-feature-parity-acceptance", "MySQL Feature Acceptance", "docs/mysql-feature-parity-acceptance.md"),
  entry("en-US", "mysql-former-primary-recovery", "Former-Primary Recovery Qualification", "docs/en-US/mysql-former-primary-recovery-qualification-2026-08-09.md"),
  entry("en-US", "mysql-production-qualification", "MySQL Production Qualification", "docs/mysql-production-qualification-2026-07-28.md"),
  entry("en-US", "postgresql-production-qualification", "PostgreSQL 16.4 Production Qualification", "docs/en-US/postgresql-production-qualification-2026-08-23.md"),
  entry("en-US", "docker-swarm-mysql-validation", "Docker Swarm MySQL Lab Qualification", "docs/en-US/docker-swarm-mysql-validation-plan.md"),
  entry("en-US", "production-chaos-test", "Production Chaos Test Report", "docs/en-US/production-chaos-test-report-2026-08-09.md"),
  entry("en-US", "power-lifecycle-test", "Power Lifecycle Test Report", "docs/en-US/power-lifecycle-test-report.md"),

  entry("zh-CN", "index", "ClusterGuard HA 中文文档", "docs/zh-CN/README.md"),
  entry("zh-CN", "product-overview", "产品概览", "README.zh-CN.md"),
  entry("zh-CN", "product-tour", "控制台产品导览", "docs/zh-CN/product-tour.md"),
  entry("zh-CN", "architecture", "系统架构", "docs/zh-CN/architecture.md"),
  entry("zh-CN", "offline-rpm-install", "离线 RPM 安装", "docs/zh-CN/offline-rpm-install.md"),
  entry("zh-CN", "database-preparation", "数据库接入", "docs/zh-CN/database-preparation.md"),
  entry("zh-CN", "orchestrator-migration", "从 Orchestrator 迁移", "docs/zh-CN/orchestrator-migration.md"),
  entry("zh-CN", "operations-manual", "运维操作手册", "docs/zh-CN/operations-manual.md"),
  entry("zh-CN", "api-operations", "API 运维参考", "docs/zh-CN/operations.md"),
  entry("zh-CN", "postgresql-ha", "PostgreSQL 高可用", "docs/zh-CN/postgresql-ha.md"),
  entry("zh-CN", "docker-swarm-mysql", "Docker Swarm MySQL", "docs/zh-CN/docker-swarm-mysql.md"),
  entry("zh-CN", "kubernetes-mysql", "Kubernetes MySQL", "docs/zh-CN/kubernetes-mysql.md"),
  entry("zh-CN", "update-and-patch", "版本升级与回退", "docs/zh-CN/update-and-patch.md"),
  entry("zh-CN", "version-release-policy", "版本与发版规范", "docs/zh-CN/version-release-policy.md"),
  entry("zh-CN", "proven-mysql-ha-methods", "MySQL 已验证方法", "docs/zh-CN/proven-mysql-ha-methods.md"),
  entry("zh-CN", "mysql-feature-parity-acceptance", "MySQL 功能验收", "docs/zh-CN/mysql-feature-parity-acceptance.md"),
  entry("zh-CN", "mysql-former-primary-recovery", "旧主恢复专项验收", "docs/zh-CN/mysql-former-primary-recovery-qualification-2026-08-09.md"),
  entry("zh-CN", "mysql-production-qualification", "MySQL 生产验收", "docs/zh-CN/mysql-production-qualification-2026-07-28.md"),
  entry("zh-CN", "postgresql-production-qualification", "PostgreSQL 16.4 生产验收", "docs/zh-CN/postgresql-production-qualification-2026-08-23.md"),
  entry("zh-CN", "docker-swarm-mysql-validation", "Docker Swarm MySQL 实机验证", "docs/zh-CN/docker-swarm-mysql-validation-plan.md"),
  entry("zh-CN", "production-chaos-test", "生产故障测试报告", "docs/zh-CN/production-chaos-test-report-2026-08-09.md"),
  entry("zh-CN", "power-lifecycle-test", "计划关机测试报告", "docs/zh-CN/power-lifecycle-test-report.md"),
  entry("zh-CN", "release-recovery-acceptance-checklist", "恢复与升级发布验收清单", "docs/zh-CN/release-recovery-acceptance-checklist.md")
];

function entry(locale, slug, title, source) {
  return { locale, slug, title, source };
}

for (const item of entries) {
  item.sourcePath = path.resolve(projectRoot, item.source);
  item.outputPath = path.join(outputRoot, item.locale, `${item.slug}.html`);
}

const entryBySource = new Map(entries.map((item) => [item.sourcePath, item]));
const entryByLocaleAndSlug = new Map(entries.map((item) => [`${item.locale}:${item.slug}`, item]));

// Documents that are referenced from a rendered page but are not part of the
// curated set, and referenced targets that no longer exist. Both are reported at
// the end of the build so a curation gap stays visible instead of silent.
const uncuratedReferences = new Set();
const missingReferences = new Set();

fs.rmSync(outputRoot, { recursive: true, force: true });
fs.mkdirSync(path.join(outputRoot, "assets", "screenshots"), { recursive: true });
fs.copyFileSync(path.join(sourceAssets, "style.css"), path.join(outputRoot, "assets", "style.css"));
fs.copyFileSync(path.join(sourceAssets, "app.js"), path.join(outputRoot, "assets", "app.js"));

for (const imageName of fs.readdirSync(path.join(docsDirectory, "assets", "screenshots"))) {
  fs.copyFileSync(
    path.join(docsDirectory, "assets", "screenshots", imageName),
    path.join(outputRoot, "assets", "screenshots", imageName)
  );
}

for (const item of entries) {
  const markdown = stripSourceLanguageSwitch(fs.readFileSync(item.sourcePath, "utf8"));
  const rendered = marked.parse(markdown, {
    gfm: true,
    walkTokens(token) {
      if (token.type === "link" || token.type === "image") {
        token.href = rewriteReference(token.href, item, token.type === "image");
      }
    }
  });
  const { content, headings } = addHeadingIds(rendered);
  fs.mkdirSync(path.dirname(item.outputPath), { recursive: true });
  fs.writeFileSync(item.outputPath, pageTemplate(item, content, headings));
}

fs.writeFileSync(path.join(outputRoot, "index.html"), landingPage());
console.log(`Generated ${entries.length + 1} HTML pages in ${path.relative(projectRoot, outputRoot)}`);

if (uncuratedReferences.size > 0) {
  console.log(
    `${uncuratedReferences.size} referenced document(s) are outside the curated set and are linked as source files:`
  );
  for (const item of [...uncuratedReferences].sort()) console.log(`  - ${item}`);
}
if (missingReferences.size > 0) {
  console.log(`${missingReferences.size} referenced target(s) are missing from the tree:`);
  for (const item of [...missingReferences].sort()) console.log(`  - ${item}`);
}

function stripSourceLanguageSwitch(markdown) {
  return markdown.replace(/<!-- LANGUAGE-SWITCH -->[\s\S]*?<!-- \/LANGUAGE-SWITCH -->\s*/g, "");
}

function rewriteReference(reference, current, isImage) {
  if (!reference || /^(?:[a-z]+:|#|\/\/)/i.test(reference)) return reference;
  const [pathname, fragment = ""] = reference.split("#", 2);
  const absolute = path.resolve(path.dirname(current.sourcePath), decodeURIComponent(pathname));

  if (isImage && absolute.startsWith(path.join(docsDirectory, "assets", "screenshots"))) {
    const target = path.join(outputRoot, "assets", "screenshots", path.basename(absolute));
    return relativeURL(path.dirname(current.outputPath), target) + (fragment ? `#${fragment}` : "");
  }

  const targetEntry = entryBySource.get(absolute);
  if (targetEntry) {
    return relativeURL(path.dirname(current.outputPath), targetEntry.outputPath) + (fragment ? `#${fragment}` : "");
  }

  if (absolute === path.join(docsDirectory, "README.md")) {
    return relativeURL(path.dirname(current.outputPath), path.join(outputRoot, "index.html"));
  }
  if (absolute === path.join(outputRoot, "index.html")) {
    return relativeURL(path.dirname(current.outputPath), path.join(outputRoot, "index.html"));
  }

  // Not in the curated set, but still a real file in the shipped tree (the
  // markdown sources ship alongside docs/html). Recompute the path from the
  // generated page to that file so the offline HTML never carries a dangling
  // link, and record the gap for the build summary.
  if (absolute.startsWith(projectRoot + path.sep)) {
    const relativeTarget = path.relative(projectRoot, absolute).split(path.sep).join("/");
    (fs.existsSync(absolute) ? uncuratedReferences : missingReferences).add(relativeTarget);
    return relativeURL(path.dirname(current.outputPath), absolute) + (fragment ? `#${fragment}` : "");
  }
  return reference;
}

function relativeURL(fromDirectory, target) {
  return path.relative(fromDirectory, target).split(path.sep).join("/") || ".";
}

function addHeadingIds(html) {
  const seen = new Map();
  const headings = [];
  const content = html.replace(/<h([1-4])>([\s\S]*?)<\/h\1>/g, (match, levelText, inner) => {
    const level = Number(levelText);
    const text = stripHTML(inner).trim();
    let slug = slugify(text) || "section";
    const count = seen.get(slug) || 0;
    seen.set(slug, count + 1);
    if (count > 0) slug = `${slug}-${count + 1}`;
    if (level >= 2) headings.push({ level, text, slug });
    return `<h${level} id="${escapeAttribute(slug)}">${inner}<a class="heading-anchor" href="#${escapeAttribute(slug)}" aria-label="Link to ${escapeAttribute(text)}">#</a></h${level}>`;
  });
  return { content, headings };
}

function slugify(text) {
  return text
    .normalize("NFKC")
    .toLowerCase()
    .replace(/[^\p{Letter}\p{Number}]+/gu, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 72);
}

function stripHTML(value) {
  return value
    .replace(/<[^>]+>/g, "")
    .replace(/&lt;/g, "<")
    .replace(/&gt;/g, ">")
    .replace(/&amp;/g, "&")
    .replace(/&quot;/g, '"')
    .replace(/&#39;/g, "'");
}

function pageTemplate(item, content, headings) {
  const alternateLocale = item.locale === "zh-CN" ? "en-US" : "zh-CN";
  const alternate = entryByLocaleAndSlug.get(`${alternateLocale}:${item.slug}`) || entryByLocaleAndSlug.get(`${alternateLocale}:index`);
  const languageLabel = item.locale === "zh-CN" ? "English" : "简体中文";
  const searchLabel = item.locale === "zh-CN" ? "搜索文档" : "Search documentation";
  const menuLabel = item.locale === "zh-CN" ? "打开目录" : "Open navigation";
  const printLabel = item.locale === "zh-CN" ? "打印" : "Print";
  const onThisPage = item.locale === "zh-CN" ? "本页目录" : "On this page";
  const sourceLabel = item.locale === "zh-CN" ? "源文件" : "Source";
  const updatedLabel = item.locale === "zh-CN" ? "离线文档构建" : "Offline documentation build";
  const relativeSource = path.relative(projectRoot, item.sourcePath).split(path.sep).join("/");

  return `<!doctype html>
<html lang="${item.locale}">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="color-scheme" content="light">
  <meta name="description" content="${escapeAttribute(item.title)} - ClusterGuard HA operational documentation">
  <title>${escapeHTML(item.title)} | ClusterGuard HA</title>
  <link rel="stylesheet" href="../assets/style.css">
  <script src="../assets/app.js" defer></script>
</head>
<body>
  <a class="skip-link" href="#document-content">${item.locale === "zh-CN" ? "跳到正文" : "Skip to content"}</a>
  <div class="docs-shell">
    <aside class="docs-sidebar" id="docs-sidebar">
      <a class="brand" href="index.html" aria-label="ClusterGuard HA documentation home">
        <span class="brand-mark">CG</span>
        <span><strong>ClusterGuard HA</strong><small>Multi-DB HA Control</small></span>
      </a>
      <label class="search-box">
        <span>${searchLabel}</span>
        <input id="docs-search" type="search" autocomplete="off" placeholder="${item.locale === "zh-CN" ? "输入标题关键词" : "Filter by title"}">
      </label>
      <nav class="docs-nav" aria-label="Documentation">${navigation(item)}</nav>
      <div class="sidebar-meta">
        <span>${updatedLabel}</span>
        <strong>2.1 / 2.2</strong>
      </div>
    </aside>
    <div class="docs-main">
      <header class="docs-header">
        <button class="menu-button" id="menu-button" type="button" aria-controls="docs-sidebar" aria-expanded="false">${menuLabel}</button>
        <div class="header-context"><span>ClusterGuard HA</span><strong>${escapeHTML(item.title)}</strong></div>
        <div class="header-actions">
          <a href="${relativeURL(path.dirname(item.outputPath), alternate.outputPath)}" lang="${alternateLocale}">${languageLabel}</a>
          <button id="print-button" type="button">${printLabel}</button>
        </div>
      </header>
      <main class="document-layout" id="document-content">
        <article class="document-content">${content}
          <footer class="document-footer">
            <span>${sourceLabel}: <code>${escapeHTML(relativeSource)}</code></span>
            <a href="../index.html">${item.locale === "zh-CN" ? "文档语言入口" : "Documentation language home"}</a>
          </footer>
        </article>
        <aside class="page-toc" aria-label="${onThisPage}">
          <strong>${onThisPage}</strong>
          ${tableOfContents(headings)}
        </aside>
      </main>
    </div>
  </div>
</body>
</html>`;
}

function navigation(current) {
  return groups[current.locale].map(([groupName, slugs]) => {
    const links = slugs.map((slug) => {
      const item = entryByLocaleAndSlug.get(`${current.locale}:${slug}`);
      if (!item) return "";
      const active = item.slug === current.slug ? ' aria-current="page" class="active"' : "";
      return `<a href="${path.basename(item.outputPath)}" data-doc-title="${escapeAttribute(item.title.toLowerCase())}"${active}>${escapeHTML(item.title)}</a>`;
    }).join("");
    return `<section><h2>${escapeHTML(groupName)}</h2>${links}</section>`;
  }).join("");
}

function tableOfContents(headings) {
  if (headings.length === 0) return "";
  return headings.map((heading) =>
    `<a class="toc-level-${heading.level}" href="#${escapeAttribute(heading.slug)}">${escapeHTML(heading.text)}</a>`
  ).join("");
}

// Newest release note per locale, derived from the discovered files rather than
// hard-coded, so the landing page cannot advertise a stale version.
function releaseLink(locale) {
  const newest = releases[locale][0];
  if (!newest) return "";
  return `<a href="${locale}/${newest.slug}.html">${escapeHTML(newest.title)}</a>`;
}

function landingPage() {
  return `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="color-scheme" content="light">
  <title>ClusterGuard HA Documentation</title>
  <link rel="stylesheet" href="assets/style.css">
</head>
<body class="language-home">
  <main>
    <div class="language-brand"><span class="brand-mark">CG</span><span><strong>ClusterGuard HA</strong><small>Multi-Database High Availability Control Plane</small></span></div>
    <h1>ClusterGuard HA Documentation</h1>
    <p>离线产品文档中心 / Offline product documentation</p>
    <div class="language-options">
      <a href="zh-CN/index.html"><strong>简体中文</strong><span>安装、迁移、运维与生产验收</span></a>
      <a href="en-US/index.html"><strong>English</strong><span>Install, migrate, operate, and qualify production deployments</span></a>
    </div>
    <section class="language-note">
      <h2>Production boundary / 生产边界</h2>
      <p>MySQL 2.1 is sealed at 2.1.45. PostgreSQL delivery begins in 2.2. Oracle Data Guard Broker and SQL Server Always On require independent release qualification.</p>
      <p>MySQL 2.1 已在 2.1.45 封板。PostgreSQL 从 2.2 开始交付。Oracle Data Guard Broker 和 SQL Server Always On 必须单独发版验收。</p>
      <p>Latest release notes / 最新发布说明：${releaseLink("zh-CN")}（简体中文）· ${releaseLink("en-US")}（English）</p>
    </section>
  </main>
</body>
</html>`;
}

function escapeHTML(value) {
  return String(value)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

function escapeAttribute(value) {
  return escapeHTML(value);
}
