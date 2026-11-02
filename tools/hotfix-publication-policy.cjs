'use strict';

// Frozen identities remain auditable. Retirement changes future coverage
// obligations, never the bytes, signatures or exact historical defect record.
function assessProblems(items, problemsByArtifact) {
  const live = [], undeclared = [], stale = [];
  for (const item of items) {
    const found = problemsByArtifact.get(item.archive) || [];
    const declared = item.publication?.known_defects || [];
    if (item.publication?.status !== 'frozen') { live.push(...found); continue; }
    for (const problem of found) {
      if (!declared.includes(problem)) undeclared.push(`${item.body.id}: 冻结产物有未声明的缺陷 — ${problem}`);
    }
    for (const problem of declared) {
      if (!found.includes(problem)) stale.push(`${item.body.id}: 已声明的缺陷不再存在 — ${problem}`);
    }
  }
  return {live, undeclared, stale};
}

function coverageBaselines(specs, publications, artifactPath) {
  return [...new Set(specs.filter(spec => publications.get(artifactPath(spec.body))?.status !== 'frozen')
    .map(spec => spec.body.base_commit).filter(base => typeof base === 'string' && base.length > 0))];
}

function localizedDescription(manifest, item, field, language) {
  const value = item[field]?.[language];
  if (typeof value === 'string' && value.trim()) return value;
  if (manifest.publication?.status !== 'frozen') {
    throw new Error(`${manifest.hotfix_id}: missing signed ${field}.${language}`);
  }
  return language === 'zh' ? '此冻结签名清单未记录该描述（非可安装入口）。' :
    'Not recorded in this frozen signed manifest (not an install entry point).';
}

module.exports = {assessProblems, coverageBaselines, localizedDescription};
