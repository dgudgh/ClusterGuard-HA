"use strict";
// Single source of truth for "which shipped artifact carries this change".
//
// The builder and the gate both used to answer that question with two hard-coded
// path prefixes:
//
//   ^(internal/agent|cmd/clusterguard-agent)/  -> clusterguard-agent
//   ^(internal/api|cmd/clusterguard)/          -> clusterguard
//
// A shared package belongs to neither. On 2026-09-29 that stopped being a
// theoretical hole and became a live one: a P0 fix in internal/coordination (a
// VIP ownership lease kept authorising its owner for hours after the clock moved
// backwards, so automatic failover was silently disabled for the whole window)
// matched both prefixes zero times. The patch would have carried the runtime
// script and no binary at all — the site would have applied it successfully, the
// manifest would have called the fix delivered, and nothing would have changed.
//
// "Which binary links this package" is a fact about the import graph, so it is
// read from the toolchain instead of guessed from a name. The candidate set is
// likewise taken from packaging/rpm/nfpm.yaml rather than from a list here, so a
// binary added to the package is covered without anyone remembering this file.
//
// Consumers: scripts/build-hotfix-patch.sh (payload),
// tools/verify-hotfix-patch-catalog.cjs (gate), scripts/scripts_test.go (test).

const fs = require("fs");
const path = require("path");
const { execFileSync } = require("child_process");

const MODULE_PATH = "clusterguard.io/ha";

// Paths that ship to a site but are not Go packages. Their destination is
// decided by packaging/rpm/nfpm.yaml, not by their name.
const PRODUCTION_FILE_PATTERNS = [
  /^packaging\/systemd\/[^/]+\.service$/,
  /^packaging\/systemd\/[^/]+\.timer$/,
  /^scripts\/clusterguard-[a-z0-9-]+\.sh$/,
  /^scripts\/install_clusterguard\.sh$/,
];

// Candidate binaries come from the package manifest: every bin/<name> the RPM
// installs, for which the tree really has a cmd/<name> main package.
function shippedBinaries(nfpmText, tree) {
  const names = [];
  // Only the contents: block declares what a site receives.
  const body = nfpmText.split(/^contents:/m)[1] || "";
  for (const line of body.split(/\r?\n/)) {
    const match = line.match(/^\s*-\s*src:\s*(?:\$\{CG_RPM_STAGE\}\/)?bin\/([A-Za-z0-9._-]+)\s*$/);
    if (!match) continue;
    const name = match[1];
    if (/\.sh$/.test(name)) continue;
    if (!fs.existsSync(path.join(tree, "cmd", name))) continue;
    if (!names.includes(name)) names.push(name);
  }
  if (names.length === 0) {
    throw new Error("packaging/rpm/nfpm.yaml declares no installable bin/<name> that has a cmd/<name> main package");
  }
  return names.sort();
}

// Map<directory relative to the tree, Set<binary name>> for every package of
// this module that a shipped binary links. Import paths outside this module
// (stdlib, third-party) never correspond to a file in the tree and are dropped.
function linkedPackages(tree, binaries, options = {}) {
  const goBin = options.goBin || process.env.CG_GO_BIN || "go";
  const linked = new Map();
  for (const name of binaries) {
    let output;
    try {
      output = execFileSync(goBin, ["-C", tree, "list", "-deps", `./cmd/${name}`], {
        encoding: "utf8",
        maxBuffer: 64 * 1024 * 1024,
        stdio: ["ignore", "pipe", "pipe"],
      });
    } catch (error) {
      const detail = error && error.stderr ? String(error.stderr).trim() : error.message;
      throw new Error(`unable to read the import graph of cmd/${name} (${goBin} list -deps in ${tree}): ${detail}`);
    }
    for (const raw of output.split("\n")) {
      const importPath = raw.trim();
      if (!importPath) continue;
      if (importPath !== MODULE_PATH && !importPath.startsWith(`${MODULE_PATH}/`)) continue;
      const directory = importPath === MODULE_PATH ? "." : importPath.slice(MODULE_PATH.length + 1);
      if (!linked.has(directory)) linked.set(directory, new Set());
      linked.get(directory).add(name);
    }
  }
  if (linked.size === 0) {
    throw new Error("the import graph is empty: no package of this module is linked into any shipped binary");
  }
  return linked;
}

const packageDirectory = (file) => {
  const directory = path.posix.dirname(file.replace(/\\/g, "/"));
  return directory;
};

// A file is production when a site runs it: either it ships as a file the
// package manifest places, or it lives in a directory some shipped binary links.
// The module root only counts for Go sources — README.md and go.mod sit there
// too and neither reaches a site through this route.
function isProductionFile(linked, file) {
  const relative = file.replace(/\\/g, "/");
  if (PRODUCTION_FILE_PATTERNS.some((pattern) => pattern.test(relative))) return true;
  const directory = packageDirectory(relative);
  if (!linked.has(directory)) return false;
  if (directory === "." && !relative.endsWith(".go")) return false;
  return true;
}

const resolvers = new Map();

// One import-graph read per tree; the gate asks about hundreds of files.
function resolver(tree, options = {}) {
  const key = `${path.resolve(tree)}\u0000${options.nfpmText ? "inline" : options.nfpmPath || ""}`;
  if (!resolvers.has(key)) {
    const nfpmText = options.nfpmText
      || fs.readFileSync(options.nfpmPath || path.join(path.resolve(tree), "packaging/rpm/nfpm.yaml"), "utf8");
    const binaries = options.binaries || shippedBinaries(nfpmText, path.resolve(tree));
    const linked = linkedPackages(path.resolve(tree), binaries, options);
    resolvers.set(key, {
      tree: path.resolve(tree),
      binaries,
      linked,
      isProductionPath: (file) => isProductionFile(linked, file),
      // Deterministic order so the payload and the manifest do not churn.
      binariesForFiles: (files) => {
        const needed = new Set();
        for (const file of files) {
          const owners = linked.get(packageDirectory(file.replace(/\\/g, "/")));
          if (owners) for (const owner of owners) needed.add(owner);
        }
        return binaries.filter((name) => needed.has(name));
      },
    });
  }
  return resolvers.get(key);
}

module.exports = { MODULE_PATH, PRODUCTION_FILE_PATTERNS, shippedBinaries, linkedPackages, isProductionFile, resolver };

if (require.main === module) {
  const args = process.argv.slice(2);
  const value = (flag) => {
    const index = args.indexOf(flag);
    return index === -1 ? null : args[index + 1];
  };
  const tree = value("--tree");
  const mode = value("--mode") || "binaries";
  if (!tree) {
    process.stderr.write("usage: hotfix-component-map.cjs --tree DIR [--mode binaries|production] [FILE...]\n");
    process.exit(2);
  }
  const files = args.filter((arg, index) => !arg.startsWith("--") && args[index - 1] !== "--tree" && args[index - 1] !== "--mode");
  const stdin = process.stdin.isTTY ? "" : require("fs").readFileSync(0, "utf8");
  const all = files.concat(stdin.split("\n").map((line) => line.trim()).filter(Boolean));
  let resolved;
  try {
    resolved = resolver(tree);
  } catch (error) {
    process.stderr.write(`${error.message}\n`);
    process.exit(1);
  }
  if (mode === "production") {
    process.stdout.write(`${all.filter((file) => resolved.isProductionPath(file)).join("\n")}\n`);
  } else if (mode === "binaries") {
    process.stdout.write(`${resolved.binariesForFiles(all).join(" ")}\n`);
  } else {
    process.stderr.write(`unknown mode: ${mode}\n`);
    process.exit(2);
  }
}
