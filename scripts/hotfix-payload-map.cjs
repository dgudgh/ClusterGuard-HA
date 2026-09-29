"use strict";
// Single source of truth for where a hotfix payload file must land on a site.
//
// The RPM packaging (packaging/rpm/nfpm.yaml) already answers that question for
// every shipped file, including the ones whose site path cannot be guessed from
// the source path:
//
//   scripts/clusterguard-upgrade.sh     -> /usr/local/sbin/clusterguard-upgrade
//   scripts/clusterguard-configure.sh   -> /usr/local/sbin/clusterguard-configure
//   scripts/clusterguard-agent-stdio.sh -> /usr/local/libexec/clusterguard-agent-stdio
//   scripts/clusterguard-mysql-install.sh -> /usr/local/libexec/clusterguard-mysql-install.sh
//
// On 2026-09-28 a hotfix that assumed "/usr/local/libexec/<source name>" shipped
// the console fix to /usr/local/libexec/clusterguard-upgrade.sh while the console
// inspection executes /usr/local/sbin/clusterguard-upgrade, so the fix was
// installed, verified against the wrong file, and had no effect at all. Deriving
// the destination from the packaging manifest removes that whole failure class:
// an unmapped source is a hard error instead of a guess.
//
// Consumers: scripts/build-hotfix-patch.sh (payload + manifest),
// tools/verify-hotfix-patch-catalog.cjs (gate), scripts/scripts_test.go (test).

const ALLOWED_INSTALL_PREFIXES = ["/usr/local/", "/usr/lib/systemd/system/", "/etc/clusterguard/"];

const STAGE_PREFIX = "${CG_RPM_STAGE}/";

const DEFAULT_MODE = { binary: "0755", systemd_unit: "0644", runtime_script: "0755" };

const ARTIFACT_DIRECTORY = { binary: "bin", systemd_unit: "systemd", runtime_script: "scripts" };

// Which unit has to restart before a swapped binary is the one actually running.
// A binary absent from this table has no service of its own: clusterguard-agent
// is a oneshot the reconcile timer invokes every five seconds, cgctl is a CLI,
// and the fence guard runs from a container image. Replacing the file is enough
// for those, and saying otherwise would have the operator restart nothing useful.
const BINARY_RESTART_UNITS = {
  clusterguard: "clusterguard-ha.service",
  "clusterguard-update-helper": "clusterguard-update-helper.service",
};

function parseNfpmDestinations(text) {
  const destinations = new Map();
  let current = null;
  for (const rawLine of text.split(/\r?\n/)) {
    const line = rawLine.replace(/\s+$/, "");
    if (/^[A-Za-z_][A-Za-z0-9_-]*:/.test(line)) {
      current = null;
      continue;
    }
    const src = line.match(/^\s*-\s*src:\s*(\S+)\s*$/);
    if (src) {
      const source = src[1].startsWith(STAGE_PREFIX) ? src[1].slice(STAGE_PREFIX.length) : src[1];
      current = { source, dst: null, mode: null, owner: null, group: null };
      destinations.set(source, current);
      continue;
    }
    if (/^\s*-\s*src:/.test(line)) {
      current = null;
      continue;
    }
    if (!current) continue;
    const field = line.match(/^\s*(dst|mode|owner|group):\s*(\S+)\s*$/);
    if (!field) continue;
    current[field[1]] = field[2];
  }
  return destinations;
}

function normalizeMode(mode) {
  if (!mode) return null;
  const digits = String(mode).trim();
  if (!/^[0-7]{3,4}$/.test(digits)) return null;
  return digits.padStart(4, "0");
}

// Resolves the payload entries for the components a patch carries. Every item
// must be declared by the packaging manifest; anything undeclared is reported in
// `missing` so the caller can refuse to build.
function resolvePayloadItems(items, nfpmText) {
  const destinations = parseNfpmDestinations(nfpmText);
  const entries = [];
  const missing = [];
  const problems = [];

  const resolve = (kind, sourceKey, name) => {
    const declared = destinations.get(sourceKey);
    if (!declared || !declared.dst) {
      missing.push(`${sourceKey} (${kind})`);
      return;
    }
    if (!ALLOWED_INSTALL_PREFIXES.some((prefix) => declared.dst.startsWith(prefix))) {
      problems.push(`${sourceKey} installs outside the permitted roots: ${declared.dst}`);
      return;
    }
    const mode = normalizeMode(declared.mode) || DEFAULT_MODE[kind];
    entries.push({
      artifact: `payload/${ARTIFACT_DIRECTORY[kind]}/${name}`,
      install_path: declared.dst,
      mode,
      owner: declared.owner || "root",
      group: declared.group || "root",
      kind,
      restart_unit: kind === "binary" ? BINARY_RESTART_UNITS[name] || null
        : kind === "systemd_unit" ? name : null,
    });
  };

  for (const name of items.binaries || []) resolve("binary", `bin/${name}`, name);
  for (const name of items.units || []) resolve("systemd_unit", `packaging/${name}`, name);
  for (const name of items.scripts || []) resolve("runtime_script", `scripts/${name}`, name);

  return { entries, missing, problems };
}

if (require.main === module) {
  const fs = require("fs");
  const args = process.argv.slice(2);
  const value = (flag, fallback = "") => {
    const index = args.indexOf(flag);
    return index === -1 ? fallback : args[index + 1];
  };
  const list = (flag) => value(flag).split(/\s+/).filter(Boolean);
  const nfpmPath = value("--nfpm", "packaging/rpm/nfpm.yaml");
  const out = value("--out");
  const result = resolvePayloadItems(
    { binaries: list("--binaries"), units: list("--units"), scripts: list("--scripts") },
    fs.readFileSync(nfpmPath, "utf8"),
  );
  for (const entry of result.entries) {
    const base = entry.artifact.split("/").pop();
    entry.note = entry.kind === "binary" && base === "clusterguard"
      ? "Control plane binary. internal/api/console.html and every other embedded asset is compiled in, so the service must be restarted before the fix is live."
      : entry.kind === "binary" && base === "clusterguard-agent"
        ? "Node agent binary. The reconcile timer runs it as a oneshot unit, so the next five second tick already uses the new build."
        : entry.kind === "binary" && entry.restart_unit
          ? `Shipped binary. ${entry.restart_unit} runs it, so that unit must be restarted before the new build is the one running.`
          : entry.kind === "binary"
            ? "Shipped binary with no service of its own. It is started on demand — a CLI invocation or a container image — so the new build is used the next time it starts."
            : entry.kind === "systemd_unit"
              ? "Unit file. systemctl daemon-reload is mandatory before the change takes effect."
              : "Runtime helper script. It is read the next time the component that calls it runs, so no service restart is needed for the fix to take effect.";
  }
  if (out) fs.writeFileSync(out, JSON.stringify(result, null, 2) + "\n");
  else process.stdout.write(JSON.stringify(result, null, 2) + "\n");
  if (result.missing.length || result.problems.length) {
    process.stderr.write(
      `hotfix payload mapping incomplete:\n  undeclared in ${nfpmPath}: ${result.missing.join(", ") || "none"}\n  refused: ${result.problems.join(", ") || "none"}\n`,
    );
    process.exit(1);
  }
}

module.exports = { parseNfpmDestinations, resolvePayloadItems, normalizeMode, ALLOWED_INSTALL_PREFIXES, BINARY_RESTART_UNITS };
