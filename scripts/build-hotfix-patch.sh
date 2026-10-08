#!/usr/bin/env bash
set -euo pipefail
export COPYFILE_DISABLE=1

# Builds one signed hotfix patch for every bug fix a site still needs.
#
# A .cgupgrade carries whole RPMs and can only be applied by the rolling
# upgrade executor. A hotfix patch carries exactly what the declared bug fixes
# changed: the rebuilt binaries that embed them, the systemd units they touched,
# the source diff as evidence, and an idempotent apply/rollback pair. Every bug
# fix must be covered by a patch, otherwise a running site can only pick the fix
# up by waiting for the next full release.
#
# One patch per site visit, not one patch per commit: a site applies "what this
# machine needs", and two patches that both replace /usr/local/bin/clusterguard
# are order sensitive — applying the older one last silently undoes the newer
# fix. A patch therefore declares the fix commits it covers and rebuilds a
# single tree that carries all of them.

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repository="$(cd "${script_dir}/.." && pwd)"
spec=""
output_dir=""
signing_key="${CG_HOTFIX_SIGNING_KEY:-}"
expected_public_key="${CG_HOTFIX_EXPECTED_PUBLIC_KEY:-}"
goos="${CG_HOTFIX_GOOS:-linux}"
goarch="${CG_HOTFIX_GOARCH:-amd64}"
node_bin="${CG_NODE_BIN:-node}"

usage() {
  cat <<'EOF'
用法：build-hotfix-patch.sh [选项]

  --spec FILE                热修描述文件（JSON），必填
  --output DIR               产物输出目录，必填
  --signing-key FILE         热修补丁发布私钥（PEM，必须离线保管）
  --expected-public-key FILE 预期现场受信公钥；指定后私钥不匹配即拒绝构建
  --goos NAME                目标操作系统，默认 linux
  --goarch NAME              目标架构，默认 amd64
EOF
}

die() { printf '热修补丁构建失败：%s\n' "$*" >&2; exit 1; }
need_value() { (($# >= 2)) && [[ -n "${2:-}" ]] || die "参数 $1 缺少值"; }

while (($#)); do
  case "$1" in
    --spec) need_value "$@"; spec="$2"; shift 2 ;;
    --output) need_value "$@"; output_dir="$2"; shift 2 ;;
    --signing-key) need_value "$@"; signing_key="$2"; shift 2 ;;
    --expected-public-key) need_value "$@"; expected_public_key="$2"; shift 2 ;;
    --goos) need_value "$@"; goos="$2"; shift 2 ;;
    --goarch) need_value "$@"; goarch="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) die "未知参数：$1" ;;
  esac
done

# Read the supported mandatory contract before constructing or signing bytes.
"${CG_NODE_BIN:-node}" "${script_dir}/../tools/verify-upgrade-validation-chain.cjs" --repo "${script_dir}/.." --contract-only || {
  printf 'CG_CONTRACT_UNAVAILABLE: mandatory upgrade contract failed\n' >&2
  exit 1
}

command -v git >/dev/null 2>&1 || die "需要 git"
command -v openssl >/dev/null 2>&1 || die "需要 openssl"
command -v tar >/dev/null 2>&1 || die "需要 tar"
command -v go >/dev/null 2>&1 || die "需要 go"
command -v "${node_bin}" >/dev/null 2>&1 || die "需要 node"
[[ -f "${spec}" && ! -L "${spec}" ]] || die "热修描述文件不存在或不是普通文件"
[[ -f "${signing_key}" && ! -L "${signing_key}" ]] || die "补丁签名私钥不存在或不是普通文件"
[[ -n "${output_dir}" ]] || die "必须指定 --output"

spec_get() {
  "${node_bin}" -e '
    const fs = require("fs");
    const spec = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
    let value = spec;
    for (const key of process.argv[2].split(".")) {
      if (value === null || typeof value !== "object" || !(key in value)) process.exit(1);
      value = value[key];
    }
    if (value === undefined || value === null) process.exit(1);
    process.stdout.write(typeof value === "string" ? value : JSON.stringify(value));
  ' "${spec}" "$1" || die "热修描述缺少字段：$1"
}

spec_get_optional() {
  "${node_bin}" -e '
    const fs = require("fs");
    const spec = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
    let value = spec;
    for (const key of process.argv[2].split(".")) {
      if (value === null || typeof value !== "object" || !(key in value)) process.exit(1);
      value = value[key];
    }
    if (value === undefined || value === null) process.exit(1);
    process.stdout.write(typeof value === "string" ? value : JSON.stringify(value));
  ' "${spec}" "$1" 2>/dev/null || true
}

hotfix_id="$(spec_get id)"
base_commit="$(spec_get base_commit)"
rpm_version="$(spec_get rpm_version)"
rpm_release="$(spec_get rpm_release)"
patch_version="$(spec_get_optional patch_version)"
patch_version_declared="$("${node_bin}" -e '
  const fs = require("fs");
  const spec = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
  if (!Object.prototype.hasOwnProperty.call(spec, "patch_version")) process.stdout.write("absent");
  else if (typeof spec.patch_version !== "string" || spec.patch_version.length === 0) process.exit(1);
  else process.stdout.write("present");
' "${spec}")" || die "patch_version 必须是非空字符串；历史规格可以省略该字段"
severity="$(spec_get severity)"
if [[ "${patch_version_declared}" == "present" ]]; then
  [[ "${hotfix_id}" == "${patch_version}" ]] || die "新产品包 ID 必须等于 patch_version：${hotfix_id} != ${patch_version}"
else
  [[ "${hotfix_id}" =~ ^HF-[0-9]{4}-[0-9]{4}-[0-9]{2}$ ]] || die "历史热修编号格式必须为 HF-YYYY-MMDD-NN：${hotfix_id}"
fi
[[ "${rpm_version}" =~ ^[0-9][0-9A-Za-z._+~-]*$ ]] || die "rpm version 格式无效"
[[ "${rpm_release}" =~ ^[0-9][0-9A-Za-z._+~-]*$ ]] || die "rpm release 格式无效"
if [[ "${patch_version_declared}" == "present" ]]; then
  [[ "${patch_version}" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+)\.([0-9]+)$ ]] ||
    die "patch_version 格式必须为 MAJOR.MINOR.PATCH.BUGFIX：${patch_version}"
  patch_bugfix="${BASH_REMATCH[4]}"
  (( 10#${patch_bugfix} >= 1 )) || die "patch_version 的 Bug 修订段必须从 1 开始：${patch_version}"
fi

# A patch covers a window of fixes. The build tree is the release baseline plus
# every declared fix and nothing else, so a fix made on a development branch can
# be shipped without dragging unreleased feature commits along with it.
fix_commits=()
while IFS= read -r line; do
  [[ -n "${line}" ]] && fix_commits+=("${line}")
done < <("${node_bin}" -e '
  const fs = require("fs");
  const spec = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
  const raw = spec.fix_commits;
  if (!Array.isArray(raw) || raw.length === 0) process.exit(1);
  for (const item of raw) if (typeof item !== "string" || !item.length) process.exit(1);
  for (const item of raw) console.log(item);
' "${spec}" || die "热修描述缺少非空的 fix_commits 数组")

git -C "${repository}" cat-file -e "${base_commit}^{commit}" 2>/dev/null || die "基线提交不存在：${base_commit}"
resolved_fix_commits=()
for commit in "${fix_commits[@]}"; do
  git -C "${repository}" cat-file -e "${commit}^{commit}" 2>/dev/null || die "修复提交不存在：${commit}"
  full="$(git -C "${repository}" rev-parse "${commit}^{commit}")"
  git -C "${repository}" merge-base --is-ancestor "${base_commit}" "${full}" ||
    die "基线提交 ${base_commit} 不是修复提交 ${commit} 的祖先"
  resolved_fix_commits+=("${full}")
done

build_commit="$(spec_get_optional build_commit)"
if [[ -z "${build_commit}" ]]; then
  build_commit="${resolved_fix_commits[$((${#resolved_fix_commits[@]} - 1))]}"
fi
git -C "${repository}" cat-file -e "${build_commit}^{commit}" 2>/dev/null || die "构建提交不存在：${build_commit}"
build_commit="$(git -C "${repository}" rev-parse "${build_commit}^{commit}")"
short_fix="$(git -C "${repository}" rev-parse --short=7 "${build_commit}")"
git -C "${repository}" merge-base --is-ancestor "${base_commit}" "${build_commit}" ||
  die "基线提交 ${base_commit} 不是构建提交 ${build_commit} 的祖先"

"${node_bin}" "${repository}/tools/hotfix-build-accounting.cjs" "${spec}" "${repository}" "${build_commit}" ||
  die "CG_UNDECLARED_PRODUCTION_CHANGE: 构建前生产提交核算失败"

# A published artifact can never be rebuilt in place: the digest is the only
# record of what a site actually ran, and a second artifact that carries the same
# filename and the same version but different bytes destroys that record on both
# sides at once. A correction is a new identity instead: versioned declarations
# advance patch_version's Bug-fix segment, while historical declarations use
# revision 1, 2, ... in the spec together with the identity they supersede.
hotfix_revision="$("${node_bin}" -e '
  const fs = require("fs");
  const spec = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
  const value = spec.revision === undefined ? 0 : spec.revision;
  if (!Number.isInteger(value) || value < 0) process.exit(1);
  // Not `supersedes`: that name already means "which hotfix package this one
  // replaces" in these specs. This one names an artifact identity.
  const replaced = spec.supersedes_artifact === undefined ? null : spec.supersedes_artifact;
  if (value > 0) {
    if (!replaced || typeof replaced.file !== "string" || !replaced.file.length ||
        typeof replaced.sha256 !== "string" || !replaced.sha256.length ||
        typeof replaced.reason !== "string" || !replaced.reason.length) process.exit(2);
  } else if (replaced) {
    process.exit(3);
  }
  console.log(value);
' "${spec}")" || die "revision/supersedes_artifact 不合法：revision>0 必须给出 supersedes_artifact{file,sha256,reason}，revision=0 不得给出 supersedes_artifact"
revision_suffix=""
[[ "${hotfix_revision}" -gt 0 ]] && revision_suffix="-r${hotfix_revision}"

case "${goarch}" in
  amd64) rpm_arch="x86_64" ;;
  arm64) rpm_arch="aarch64" ;;
  *) die "不支持的目标架构：${goarch}" ;;
esac

if [[ -n "${expected_public_key}" ]]; then
  [[ -f "${expected_public_key}" && ! -L "${expected_public_key}" ]] ||
    die "预期受信公钥不存在或不是普通文件"
  signing_fingerprint="$(openssl pkey -in "${signing_key}" -pubout -outform DER 2>/dev/null | openssl dgst -sha256 2>/dev/null | awk '{print $NF}')"
  expected_fingerprint="$(openssl pkey -pubin -in "${expected_public_key}" -outform DER 2>/dev/null | openssl dgst -sha256 2>/dev/null | awk '{print $NF}')"
  [[ -n "${signing_fingerprint}" && "${signing_fingerprint}" == "${expected_fingerprint}" ]] ||
    die "补丁签名私钥与预期受信公钥不匹配"
fi

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

# --- Decide what this patch actually ships -----------------------------------
# Only production paths decide the payload, and only the declared fix commits
# decide it: the range from the release baseline to the build commit would also
# carry every unrelated commit in between, including unreleased features that
# share a file with a fix. Tests and documentation are carried as source
# evidence but never rebuilt into an artifact.
changed=""
for commit in "${resolved_fix_commits[@]}"; do
  part="$(git -C "${repository}" diff --name-only "${commit}^..${commit}")"
  changed="$(printf '%s\n%s\n' "${changed}" "${part}")"
done
changed="$(printf '%s\n' "${changed}" | sed '/^$/d' | sort -u)"
[[ -n "${changed}" ]] || die "声明的修复提交没有任何改动"

has_path() { printf '%s\n' "${changed}" | grep -qE "$1"; }

binaries=""
systemd_units=""
runtime_scripts=""
installer_only="false"
if has_path '^packaging/systemd/.*\.(service|timer)$'; then
  systemd_units="$(printf '%s\n' "${changed}" | grep -E '^packaging/systemd/.*\.(service|timer)$' | sed 's#^packaging/systemd/##' | sort -u)"
fi
# Runtime helper scripts are read the next time the component that calls them
# runs (boot recovery, engine install, console package inspection), so a fix
# carried here needs no service restart — but it must still travel in the
# payload, otherwise a site patch would silently drop it. Their *site* path is
# never derived from the source name: packaging/rpm/nfpm.yaml is the only truth,
# resolved through scripts/hotfix-payload-map.cjs.
if has_path '^scripts/clusterguard-[a-z0-9-]+\.sh$'; then
  runtime_scripts="$(printf '%s\n' "${changed}" | grep -E '^scripts/clusterguard-[a-z0-9-]+\.sh$' | sed 's#^scripts/##' | sort -u)"
fi
if has_path '^scripts/install_clusterguard\.sh$'; then installer_only="true"; fi

stage="$(mktemp -d /tmp/clusterguard-hotfix.XXXXXX)"
source_tree="${stage}/source"
cleanup() {
  if [[ -d "${source_tree}" ]]; then
    git -C "${repository}" worktree remove --force "${source_tree}" >/dev/null 2>&1 || true
  fi
  rm -rf "${stage}"
}
trap cleanup EXIT
git -C "${repository}" worktree add --detach "${source_tree}" "${build_commit}" >/dev/null 2>&1 ||
  die "无法为构建提交创建临时工作树：${build_commit}"
root="${stage}/clusterguard-hotfix"
mkdir -p "${root}/payload/bin" "${root}/payload/systemd" "${root}/payload/scripts" "${root}/payload/installer" "${root}/src"

# Which binary embeds a change is a fact about the import graph, not about the
# path it sits on. Two hard-coded prefixes used to answer it —
# internal/agent|cmd/clusterguard-agent and internal/api|cmd/clusterguard — and a
# shared package belongs to neither: the 2026-09-29 fix in internal/coordination
# (a VIP ownership lease that stayed valid for hours after the clock moved
# backwards, disabling automatic failover for the whole window) matched both of
# them zero times, so this patch would have shipped the runtime script and no
# binary at all while its manifest called the fix delivered. The graph has to be
# read from the build tree, which is why this cannot happen before the worktree
# exists.
binaries="$("${node_bin}" "${repository}/scripts/hotfix-component-map.cjs" \
  --tree "${source_tree}" --mode binaries <<<"${changed}")" ||
  die "无法从导入图推导修复涉及的二进制（scripts/hotfix-component-map.cjs --tree ${source_tree}）"
[[ -n "${binaries}" || -n "${systemd_units}" || -n "${runtime_scripts}" || "${installer_only}" == "true" ]] ||
  die "该修复没有可交付产物（既不涉及二进制，也不涉及单元、运行时脚本或安装器）"

build_time="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
version_ldflags="-s -w -X clusterguard.io/ha/internal/buildinfo.Version=${rpm_version} -X clusterguard.io/ha/internal/buildinfo.Release=${rpm_release} -X clusterguard.io/ha/internal/buildinfo.Commit=${short_fix} -X clusterguard.io/ha/internal/buildinfo.BuiltAt=${build_time}"
if [[ "${patch_version_declared}" == "present" ]]; then
  version_ldflags+=" -X clusterguard.io/ha/internal/buildinfo.ProductVersion=${patch_version}"
fi
for binary in ${binaries}; do
  CGO_ENABLED=0 GOOS="${goos}" GOARCH="${goarch}" go -C "${source_tree}" build -trimpath -ldflags "${version_ldflags}" \
    -o "${root}/payload/bin/${binary}" "./cmd/${binary}"
done
for unit in ${systemd_units}; do
  install -m 0644 "${source_tree}/packaging/systemd/${unit}" "${root}/payload/systemd/${unit}"
done
for script in ${runtime_scripts}; do
  install -m 0755 "${source_tree}/scripts/${script}" "${root}/payload/scripts/${script}"
done
if [[ "${installer_only}" == "true" ]]; then
  install -m 0755 "${source_tree}/scripts/install_clusterguard.sh" "${root}/payload/installer/install_clusterguard.sh"
fi
git -C "${repository}" diff "${base_commit}..${build_commit}" -- . ':(exclude)docs/html' \
  >"${root}/src/${hotfix_id}-${short_fix}.patch"
find "${root}/payload" -type d -empty -delete

# --- Payload destinations ----------------------------------------------------
# Every payload file's site path, mode and ownership comes from the RPM
# packaging manifest of the build tree. Guessing them is how a console fix once
# shipped to /usr/local/libexec/clusterguard-upgrade.sh while the product runs
# /usr/local/sbin/clusterguard-upgrade: the patch installed cleanly, the operator
# verified the wrong file, and nothing changed. An undeclared source is a build
# failure now, not a silent miss.
payload_map="${stage}/payload-map.json"
"${node_bin}" "${repository}/scripts/hotfix-payload-map.cjs" \
  --nfpm "${source_tree}/packaging/rpm/nfpm.yaml" \
  --binaries "${binaries}" \
  --units "${systemd_units}" \
  --scripts "${runtime_scripts}" \
  --out "${payload_map}" || die "无法从 packaging/rpm/nfpm.yaml 解析补丁落点（未声明的运行时脚本必须先加入打包清单）"

# --- Manifest ----------------------------------------------------------------
payload_files="${stage}/payload-files.json"
"${node_bin}" -e '
  const fs = require("fs");
  const path = require("path");
  const crypto = require("crypto");
  const [root, payloadMapPath, installerOnly, out] = process.argv.slice(1);
  const digest = file => crypto.createHash("sha256").update(fs.readFileSync(file)).digest("hex");
  const files = JSON.parse(fs.readFileSync(payloadMapPath, "utf8")).entries;
  if (installerOnly === "true") {
    files.push({
      artifact: "payload/installer/install_clusterguard.sh",
      install_path: null,
      mode: "0755",
      owner: "root",
      group: "root",
      kind: "installer",
      restart_unit: null,
      note: "Installer-only fix. It changes fresh installs; an installed site is corrected by the operator steps in README.md."
    });
  }
  for (const entry of files) {
    entry.sha256 = digest(path.join(root, entry.artifact));
    entry.size = fs.statSync(path.join(root, entry.artifact)).size;
  }
  fs.writeFileSync(out, JSON.stringify(files, null, 2) + "\n");
' "${root}" "${payload_map}" "${installer_only}" "${payload_files}"

manifest_js="${stage}/manifest.cjs"
cat >"${manifest_js}" <<'MANIFEST_JS'
const fs = require("fs");
const crypto = require("crypto");
const [specPath, payloadFilesPath, sourcePatchName, buildTime, rpmArch, goos, goarch, fixCommitsRaw, buildCommit, out] = process.argv.slice(2);
const spec = JSON.parse(fs.readFileSync(specPath, "utf8"));
const files = JSON.parse(fs.readFileSync(payloadFilesPath, "utf8"));
const supersedes = spec.supersedes || [];
if (!Array.isArray(supersedes) || new Set(supersedes).size !== supersedes.length || supersedes.some(id => typeof id !== "string" || !/^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/.test(id) || id === spec.id)) throw new Error("invalid supersedes identity");
const digest = file => crypto.createHash("sha256").update(fs.readFileSync(file)).digest("hex");
const manifest = {
  schema_version: 2,
  kind: "hotfix",
  product: "ClusterGuard HA",
  hotfix_id: spec.id,
  patch_version: spec.patch_version || null,
  severity: spec.severity,
  base_commit: spec.base_commit,
  build_commit: buildCommit,
  // A revision is a separate artifact, never a rewrite of a published one, so
  // the identity it replaces travels with it into the signed manifest.
  revision: spec.revision || 0,
  supersedes_artifact: spec.supersedes_artifact || null,
  supersedes: spec.supersedes || [],
  baseline_history: spec.baseline_history || [],
  fix_commits: fixCommitsRaw.split(",").filter(Boolean),
  source: { version: spec.rpm_version, release: spec.rpm_release },
  target: {
    version: spec.rpm_version,
    release: `${spec.rpm_release}+${spec.id.toLowerCase()}`,
    rpm_architecture: rpmArch,
    os: goos,
    architecture: goarch
  },
  compatibility: { state_format: 1, update_protocol: 1 },
  database_mutation: false,
  title: spec.title,
  summary: spec.summary || null,
  fixes: spec.fixes || [],
  verification: spec.verification || [],
  rollback: spec.rollback || "Run rollback.sh, then systemctl daemon-reload and restart the services listed in the manifest.",
  files,
  source_patch: `src/${sourcePatchName}`,
  built_at: buildTime
};
fs.writeFileSync(out, JSON.stringify(manifest, null, 2) + "\n");
process.stdout.write(digest(out) + "\n");
MANIFEST_JS
manifest_sha="$("${node_bin}" "${manifest_js}" "${spec}" "${payload_files}" \
  "${hotfix_id}-${short_fix}.patch" "${build_time}" "${rpm_arch}" "${goos}" "${goarch}" \
  "$(IFS=,; printf '%s' "${resolved_fix_commits[*]}")" "${build_commit}" \
  "${root}/HOTFIX-MANIFEST.json")"

openssl dgst -sha256 -sign "${signing_key}" -out "${root}/HOTFIX-MANIFEST.sig" "${root}/HOTFIX-MANIFEST.json"

{
  printf '%s  %s\n' "${manifest_sha}" "HOTFIX-MANIFEST.json"
  "${node_bin}" -e '
    const fs = require("fs");
    for (const entry of JSON.parse(fs.readFileSync(process.argv[1], "utf8"))) {
      console.log(`${entry.sha256}  ${entry.artifact}`);
    }
  ' "${payload_files}"
} >"${root}/SHA256SUMS"

# --- apply.sh / rollback.sh --------------------------------------------------
# The restart hints are the command-line path's only instruction: there is no
# daemon on that path to read the manifest, and a binary that is replaced but
# never restarted leaves the old process running — a fix that applies cleanly and
# changes nothing. So they are the manifest's own restart_unit set, taken from the
# payload map, rather than a list reassembled here: an earlier version added only
# the control-plane unit and would have missed a binary whose unit carries the fix.
restart_units="$("${node_bin}" -e '
  const fs = require("fs");
  const units = new Set();
  for (const entry of JSON.parse(fs.readFileSync(process.argv[1], "utf8")).entries) {
    if (entry.restart_unit) units.add(entry.restart_unit);
  }
  process.stdout.write([...units].join(" "));
' "${payload_map}")"

render_js="${stage}/render-scripts.cjs"
cat >"${render_js}" <<'RENDER_JS'
const fs = require("fs");
const [manifestPath, restartUnitsRaw, applyOut, rollbackOut] = process.argv.slice(2);
const manifest = JSON.parse(fs.readFileSync(manifestPath, "utf8"));
const restartUnits = restartUnitsRaw.split(/\s+/).filter(Boolean);
const quote = value => `'${String(value).replace(/'/g, "'\\''")}'`;
// Text placed inside a bash double-quoted printf argument. Escape what would
// break the generated script and keep what bash should interpret.
const esc = text => text.replace(/\\/g, "\\\\").replace(/"/g, '\\"').replace(/\n/g, "\\n");
const say = line => `printf "${esc(line)}\\n"`;

// This is also used during a legacy-to-v2 update: the installed Helper may not
// understand "contract" yet. Only after verifying the installed trust key's
// signature and every signed digest may we run this package's new Helper.
const verification = [
  'here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"',
  'sha() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk \'{print $1}\'; else shasum -a 256 "$1" | awk \'{print $1}\'; fi; }',
  'trust_key="${CG_HOTFIX_TRUST_KEY:-/etc/clusterguard/trust/patch-signing-public.pem}"',
  '[[ -f "${trust_key}" && ! -L "${trust_key}" ]] || { printf "可信签名公钥不可用\\n" >&2; exit 1; }',
  'openssl dgst -sha256 -verify "${trust_key}" -signature "${here}/HOTFIX-MANIFEST.sig" "${here}/HOTFIX-MANIFEST.json" >/dev/null 2>&1 || { printf "热修签名校验失败\\n" >&2; exit 1; }',
  'for tool in apply rollback; do',
  '  expected="$(jq -er --arg tool "${tool}" \'.tooling[$tool].sha256\' "${here}/HOTFIX-MANIFEST.json")"',
  '  [[ -f "${here}/${tool}.sh" && ! -L "${here}/${tool}.sh" && "$(sha "${here}/${tool}.sh")" == "${expected}" ]] || { printf "热修工具摘要不匹配\\n" >&2; exit 1; }',
  'done',
  'while IFS="|" read -r artifact expected; do',
  '  [[ "${artifact}" == payload/* && "${artifact}" != *".."* && -f "${here}/${artifact}" && ! -L "${here}/${artifact}" && "$(sha "${here}/${artifact}")" == "${expected}" ]] || { printf "热修载荷摘要不匹配：%s\\n" "${artifact}" >&2; exit 1; }',
  'done < <(jq -er \'.files[] | [.artifact, .sha256] | join("|")\' "${here}/HOTFIX-MANIFEST.json")',
  'contract_helper=/usr/local/libexec/clusterguard-update-helper',
  'if jq -e \'.files[] | select(.artifact == "payload/bin/clusterguard-update-helper")\' "${here}/HOTFIX-MANIFEST.json" >/dev/null; then contract_helper="${here}/payload/bin/clusterguard-update-helper"; fi',
  '[[ -x "${contract_helper}" && ! -L "${contract_helper}" ]] && "${contract_helper}" contract >/dev/null || { printf "CG_CONTRACT_UNAVAILABLE: 热修契约加载失败\\n" >&2; exit 1; }',
  ''
];

const apply = [];
apply.push("#!/usr/bin/env bash");
apply.push("set -euo pipefail");
apply.push("");
apply.push("# Applies one ClusterGuard HA hotfix patch. Generated by build-hotfix-patch.sh;");
apply.push("# do not edit by hand. Every replaced file is backed up first so rollback.sh can");
apply.push("# put the site back exactly as it was.");
apply.push(`# hotfix=${manifest.hotfix_id} build_commit=${manifest.build_commit.slice(0, 7)} fix_commits=${manifest.fix_commits.map((commit) => commit.slice(0, 7)).join(",")} source=${manifest.source.version}-${manifest.source.release}`);
apply.push("");
apply.push(`if [[ "$(id -u)" -ne 0 ]]; then printf "${esc("必须以 root 运行 apply.sh。")}\\n" >&2; exit 1; fi`);
apply.push("");
apply.push(...verification);
apply.push("sha() {");
apply.push('  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk "{print \\$1}"; else shasum -a 256 "$1" | awk "{print \\$1}"; fi');
apply.push("}");
apply.push("");
apply.push(say("== 1/4 校验补丁完整性 =="));
apply.push("if command -v sha256sum >/dev/null 2>&1; then");
apply.push('  ( cd "${here}" && sha256sum -c SHA256SUMS ) || { printf "补丁校验失败，拒绝应用。\\n" >&2; exit 1; }');
apply.push("else");
apply.push('  while read -r expected artifact; do');
apply.push('    [[ -n "${artifact}" ]] || continue');
apply.push('    if [[ "$(sha "${here}/${artifact}")" != "${expected}" ]]; then');
apply.push('      printf "补丁校验失败：%s\\n" "${artifact}" >&2; exit 1;');
apply.push("    fi");
apply.push('    printf "  %s OK\\n" "${artifact}"');
apply.push('  done < <(awk "{print \\$1, \\$2}" "${here}/SHA256SUMS")');
apply.push("fi");
apply.push("");
apply.push('stamp="$(date +%Y%m%d-%H%M%S-%N)"');
apply.push('backup_dir="/var/lib/clusterguard/hotfix"');
apply.push(`# The manifest name carries the hotfix id so rollback.sh can prove it is
# restoring this patch's backups and not whichever set happens to be newest.
backup_list="\${backup_dir}/backup-${manifest.hotfix_id}-\${stamp}.txt"`);
apply.push('mkdir -p "${backup_dir}"');
apply.push('( set -C; : >"${backup_list}" ) || { printf "备份清单已存在，拒绝覆盖：%s\\n" "${backup_list}" >&2; exit 1; }');
apply.push(`printf '%s\\n' '# package_id=${manifest.hotfix_id}' >>"\${backup_list}"`);
apply.push("");
apply.push("install_payload() {");
apply.push('  local artifact="$1" destination="$2" mode="$3" ownership="$4"');
apply.push('  if [[ -f "${destination}" ]]; then');
apply.push('    cp -p "${destination}" "${destination}.bak-${stamp}"');
apply.push('    printf "%s\\t%s\\n" "${destination}" "${destination}.bak-${stamp}" >>"${backup_list}"');
apply.push("  else");
apply.push('    printf "%s\\t%s\\n" "${destination}" "" >>"${backup_list}"');
apply.push("  fi");
apply.push('  install -d -m 0755 "$(dirname "${destination}")"');
apply.push('  install -m "${mode}" -o "${ownership%%:*}" -g "${ownership##*:}" "${here}/${artifact}" "${destination}"');
apply.push('  printf "  已安装 %s -> %s (%s %s)\\n" "${artifact}" "${destination}" "${mode}" "${ownership}"');
apply.push("}");
apply.push("");
apply.push(say("== 2/4 安装补丁文件 =="));
for (const entry of manifest.files) {
  if (entry.install_path === null) {
    apply.push(say(`  跳过 ${entry.artifact}（installer-only，现场无对应路径）`));
    continue;
  }
  const ownership = `${entry.owner || "root"}:${entry.group || "root"}`;
  apply.push(`install_payload ${quote(entry.artifact)} ${quote(entry.install_path)} '${entry.mode}' '${ownership}'`);
}
apply.push("");
apply.push(say("== 3/4 重载 systemd =="));
apply.push('if command -v systemctl >/dev/null 2>&1; then systemctl daemon-reload; printf "  已重载\\n"; fi');
apply.push("");
apply.push(say("== 4/4 重启提示（apply.sh 默认不重启服务）=="));
if (restartUnits.length > 0) {
  for (const unit of restartUnits) apply.push(say(`  systemctl restart ${unit}`));
} else {
  apply.push(say("  无需重启"));
}
apply.push(`printf "${esc("备份清单：%s")}\\n" "\${backup_list}"`);
apply.push(`printf "${esc("回滚：bash %s/rollback.sh")}\\n" "\${here}"`);
apply.push(say("== 验证 =="));
for (const step of manifest.verification || []) apply.push(`printf "  %s\\n" ${quote(step)}`);
fs.writeFileSync(applyOut, apply.join("\n") + "\n");

const rollback = [];
rollback.push("#!/usr/bin/env bash");
rollback.push("set -euo pipefail");
rollback.push("");
rollback.push("# Restores the files a hotfix patch replaced, using this patch's own backup");
rollback.push("# manifest written by apply.sh. Generated by build-hotfix-patch.sh; do not");
rollback.push("# edit by hand.");
rollback.push(`# hotfix=${manifest.hotfix_id}`);
rollback.push("");
rollback.push(`if [[ "$(id -u)" -ne 0 ]]; then printf "${esc("必须以 root 运行 rollback.sh。")}\\n" >&2; exit 1; fi`);
rollback.push("");
rollback.push(...verification);
rollback.push('backup_dir="/var/lib/clusterguard/hotfix"');
rollback.push(`# Use only this package's backups, oldest first. A retry may have
# backed up files that were already patched, so its newest manifest is not the
# original state. The first entry for each destination is the state to restore.
backup_lists=( "\${backup_dir}"/backup-${manifest.hotfix_id}-*.txt )`);
rollback.push('[[ -f "${backup_lists[0]}" ]] || { printf "没有找到本补丁的备份清单，无法回滚。\\n" >&2; exit 1; }');
rollback.push(`for backup_list in "\${backup_lists[@]}"; do
  [[ "$(head -n 1 "\${backup_list}")" == '# package_id=${manifest.hotfix_id}' ]] || {
    printf "备份清单身份不匹配：%s\\n" "\${backup_list}" >&2; exit 1;
  }
done`);
rollback.push("");
rollback.push("restore_backup() {");
rollback.push('  local destination="$1" backup restore_tmp backup_list entry=""');
rollback.push('  for backup_list in "${backup_lists[@]}"; do');
rollback.push('    entry="$(awk -F"\\t" -v target="${destination}" \'$1 == target && NF >= 2 { printf "found\\t%s", $2; exit }\' "${backup_list}")"');
rollback.push('    [[ -z "${entry}" ]] || break');
rollback.push('  done');
rollback.push('  [[ -n "${entry}" ]] || { printf "  备份清单缺少目标：%s\\n" "${destination}" >&2; return 1; }');
rollback.push('  backup="${entry#*$\'\\t\'}"');
rollback.push('  if [[ -z "${backup}" ]]; then');
rollback.push('    rm -f "${destination}"');
rollback.push('    printf "  已移除 %s（补丁前不存在）\\n" "${destination}"');
rollback.push("    return");
rollback.push("  fi");
rollback.push('  if [[ -f "${backup}" ]]; then');
rollback.push('    # Swap the inode instead of overwriting the file in place. The files a');
rollback.push('    # hotfix replaces include running binaries, and writing into a live');
rollback.push('    # executable fails with ETXTBSY. That is exactly how the automatic');
rollback.push('    # rollback of HF-2026-0929-04 failed on all three nodes and left the');
rollback.push('    # maintenance gate stuck. Copy to a sibling temp file and rename it over');
rollback.push('    # the target, the same way apply.sh publishes its own writes.');
rollback.push('    restore_tmp="$(mktemp "${destination}.restore.XXXXXX")"');
rollback.push('    cp -p "${backup}" "${restore_tmp}"');
rollback.push('    mv -f "${restore_tmp}" "${destination}"');
rollback.push('    cmp -s "${backup}" "${destination}" || { printf "  恢复后文件校验失败：%s\\n" "${destination}" >&2; return 1; }');
rollback.push('    printf "  已回滚 %s <- %s\\n" "${destination}" "${backup}"');
rollback.push("  else");
rollback.push('    printf "  备份缺失，拒绝回退 %s（%s）\\n" "${destination}" "${backup}" >&2');
rollback.push('    return 1');
rollback.push("  fi");
rollback.push("}");
rollback.push("");
rollback.push(say("== 回滚热修补丁 =="));
for (const entry of manifest.files) {
  if (entry.install_path === null) continue;
  rollback.push(`restore_backup ${quote(entry.install_path)}`);
}
rollback.push("");
rollback.push('if command -v systemctl >/dev/null 2>&1; then systemctl daemon-reload; printf "  已重载\\n"; fi');
rollback.push(say("回滚后必须重启受影响的服务，否则进程仍在运行补丁前的代码。"));
for (const unit of restartUnits) rollback.push(say(`  systemctl restart ${unit}`));
fs.writeFileSync(rollbackOut, rollback.join("\n") + "\n");
RENDER_JS
"${node_bin}" "${render_js}" "${root}/HOTFIX-MANIFEST.json" "${restart_units}" "${root}/apply.sh" "${root}/rollback.sh"
chmod 0755 "${root}/apply.sh" "${root}/rollback.sh"
bash -n "${root}/apply.sh" || die "生成的 apply.sh 语法不合法"
bash -n "${root}/rollback.sh" || die "生成的 rollback.sh 语法不合法"

# --- Bind the generated tooling into the signed manifest ---------------------
# apply.sh and rollback.sh run as root on every node and decide what is written
# where, yet they are generated *after* the manifest and were therefore never
# anchored by the signature: a swapped apply.sh would have satisfied every
# payload digest while installing something else. Re-emit the manifest once the
# scripts exist, carry their digests inside it, and only then sign and publish
# SHA256SUMS. There is no circularity — the generator reads the manifest's file
# list, never its tooling digests.
apply_sha="$(sha256_file "${root}/apply.sh")"
rollback_sha="$(sha256_file "${root}/rollback.sh")"
"${node_bin}" -e '
  const fs = require("fs");
  const [manifestPath, applySha, rollbackSha] = process.argv.slice(1);
  const manifest = JSON.parse(fs.readFileSync(manifestPath, "utf8"));
  manifest.schema_version = 3;
  manifest.tooling = {
    apply: { path: "apply.sh", sha256: applySha },
    rollback: { path: "rollback.sh", sha256: rollbackSha }
  };
  fs.writeFileSync(manifestPath, JSON.stringify(manifest, null, 2) + "\n");
' "${root}/HOTFIX-MANIFEST.json" "${apply_sha}" "${rollback_sha}"
manifest_sha="$(sha256_file "${root}/HOTFIX-MANIFEST.json")"
openssl dgst -sha256 -sign "${signing_key}" -out "${root}/HOTFIX-MANIFEST.sig" "${root}/HOTFIX-MANIFEST.json"
{
  printf '%s  %s\n' "${manifest_sha}" "HOTFIX-MANIFEST.json"
  printf '%s  %s\n' "${apply_sha}" "apply.sh"
  printf '%s  %s\n' "${rollback_sha}" "rollback.sh"
  "${node_bin}" -e '
    const fs = require("fs");
    for (const entry of JSON.parse(fs.readFileSync(process.argv[1], "utf8"))) {
      console.log(`${entry.sha256}  ${entry.artifact}`);
    }
  ' "${payload_files}"
} >"${root}/SHA256SUMS"

# --- README ------------------------------------------------------------------
"${node_bin}" -e '
  const fs = require("fs");
  const [manifestPath, out] = process.argv.slice(1);
  const manifest = JSON.parse(fs.readFileSync(manifestPath, "utf8"));
  // The command-line path has no daemon to consult the manifest, so a replaced
  // binary whose unit is never restarted keeps the old process running: the patch
  // applies cleanly and nothing changes. apply.sh already prints this set; README
  // is what the operator reads first, so it is generated from the same source
  // rather than naming one hard-coded unit (which left the helper and the agent
  // reconcile unit on their old binaries).
  const restartUnits = [...new Set(manifest.files.map((entry) => entry.restart_unit).filter(Boolean))].sort();
  const section = (label, block) => {
    if (!block) return "";
    return Object.entries(block).map(([locale, text]) => {
      const heading = locale === "zh" ? label.zh : label.en;
      return `### ${heading}\n\n${text}\n`;
    }).join("\n");
  };
  const lines = [];
  lines.push(`# ${manifest.hotfix_id} — ${manifest.title.zh} / ${manifest.title.en}`);
  lines.push("");
  lines.push(`- 严重级别：${manifest.severity}`);
  if (manifest.patch_version) lines.push(`- 补丁版本：${manifest.patch_version}`);
  lines.push(`- 覆盖修复提交：${manifest.fix_commits.map((commit) => `\`${commit.slice(0, 7)}\``).join("、")}`);
  lines.push(`- 构建树：\`${manifest.build_commit}\`（基线 \`${manifest.base_commit}\`，只含基线 + 上述修复）`);
  lines.push(`- 适用版本：${manifest.source.version}-${manifest.source.release} → ${manifest.target.version}-${manifest.target.release} (${manifest.target.rpm_architecture})`);
  lines.push("");
  if (manifest.summary) {
    lines.push(section({ zh: "本包概要", en: "What this patch does" }, manifest.summary));
  }
  for (const [index, item] of (manifest.fixes || []).entries()) {
    const commit = (item.commit || "").slice(0, 7);
    lines.push(`## ${index + 1}. ${item.title.zh} / ${item.title.en}（\`${commit}\`, ${item.severity}）`);
    lines.push("");
    lines.push(section({ zh: "现象", en: "Symptom" }, item.symptom));
    lines.push(section({ zh: "根因", en: "Root cause" }, item.root_cause));
    lines.push(section({ zh: "修复", en: "Fix" }, item.fix));
    if ((item.applies_to || []).length > 0) {
      lines.push("### 何时需要应用 / When to apply");
      lines.push("");
      for (const entry of item.applies_to) lines.push(`- ${entry}`);
      lines.push("");
    }
  }
  lines.push("## 产物 / Artifacts");
  lines.push("");
  for (const entry of manifest.files) {
    const path = entry.install_path || "installer-only, no site path";
    lines.push(`- \`${entry.artifact}\` → \`${path}\`（${entry.mode}, sha256 \`${entry.sha256.slice(0, 16)}…\`）`);
  }
  lines.push("");
  lines.push("## 应用 / Apply");
  lines.push("");
  lines.push("```bash");
  lines.push("# 在当前 Leader 控制台上传本 .cgpatch，校验、查看计划并二次确认执行。");
  lines.push("# Runner 建立维护门禁，逐节点应用、重启清单单元、验证摘要和就绪后释放门禁。");
  lines.push("# apply.sh / rollback.sh 是 Runner 的节点工具，不能替代集群更新入口。");
  for (const unit of restartUnits) lines.push(`# Runner 按清单执行：systemctl restart ${unit}`);
  lines.push("```");
  lines.push("");
  lines.push("## 回滚 / Rollback");
  lines.push("");
  lines.push("```bash");
  lines.push("# 在当前 Leader 控制台对本包使用受控回退，并完成二次确认。");
  lines.push("# 回退只读取本包自己的备份；缺少备份或恢复不完整时保留维护门禁。");
  for (const unit of restartUnits) lines.push(`# Runner 按清单执行：systemctl restart ${unit}`);
  lines.push("```");
  lines.push("");
  lines.push("## 验证 / Verification");
  lines.push("");
  for (const step of manifest.verification || []) lines.push(`- \`${step}\``);
  lines.push("");
  lines.push(`源码差异证据：\`${manifest.source_patch}\``);
  lines.push("");
  fs.writeFileSync(out, lines.join("\n"));
' "${root}/HOTFIX-MANIFEST.json" "${root}/README.md"

# --- Package -----------------------------------------------------------------
mkdir -p "${output_dir}"
output_dir="$(cd "${output_dir}" && pwd)"
if [[ "${patch_version_declared}" == "present" ]]; then
  archive_name="clusterguard-${patch_version}.${rpm_arch}.cgpatch"
else
  # Historical declarations retain their immutable HF-based names. New
  # declarations must set patch_version and use the public version grammar.
  archive_name="clusterguard-ha-hotfix-${hotfix_id}${revision_suffix}-${rpm_version}-${rpm_release}.${rpm_arch}.cgpatch"
fi
output="${output_dir}/${archive_name}"
if [[ -e "${output}" ]]; then
  die "产物已存在，拒绝原地覆盖：${output}
已签名的交付物一旦生成即不可变——同一个文件名、同一个版本号、不同的字节，会让现场与仓库
各说一套，事后无法证明节点实际运行过什么。请二选一：
 ① 新格式在 spec 中递增 patch_version 的 Bug 修订段（例如 3.1.1.2），并记录 revision + supersedes_artifact{file,sha256,reason}；
 ② 历史格式在 spec 里加 revision（例如 1）+ supersedes_artifact{file,sha256,reason}，产生新身份与新文件名；
 ③ 若该文件从未交付、从未上传现场，先把它改名移走并写明原因，再重新构建。"
fi
temporary_output="${output}.tmp.$$"
rm -f "${temporary_output}"
tar --no-xattrs -C "${stage}" -czf "${temporary_output}" clusterguard-hotfix
chmod 0644 "${temporary_output}"
mv -f "${temporary_output}" "${output}"
archive_sha="$(sha256_file "${output}")"
printf '%s  %s\n' "${archive_sha}" "${archive_name}" >"${output}.sha256"

printf 'hotfix_patch=%s\n' "${output}"
printf 'hotfix_id=%s\n' "${hotfix_id}"
printf 'patch_version=%s\n' "${patch_version:-legacy}"
printf 'fix_commits=%s\n' "$(IFS=,; printf '%s' "${resolved_fix_commits[*]}")"
printf 'build_commit=%s\n' "${build_commit}"
printf 'source=%s-%s\n' "${rpm_version}" "${rpm_release}"
printf 'architecture=%s\n' "${rpm_arch}"
printf 'components=%s %s\n' "${binaries}" "${systemd_units}"
printf 'sha256=%s\n' "${archive_sha}"
printf 'signature=created\n'
