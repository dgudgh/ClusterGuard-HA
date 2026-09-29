#!/usr/bin/env node
'use strict';

// The observation watermark is the only thing standing between an
// out-of-order topology refresh and a fresh observation being replaced by a
// staler one. It is also written by whatever clock the deployment happens to
// have: an isolated installation has no external time reference at all, so the
// watermark is exactly as trustworthy as the clock that produced it.
//
// On 2026-09-29 the site ran three nodes whose RTC held local time while the
// kernel read it as UTC, so every node believed it was eight hours ahead.
// Correcting that was the point of the work, but the correction itself moved
// every subsequent observation *behind* the watermark the wrong clock had
// already written. Strict monotonicity then rejected every refresh forever:
// topology never became fresh again, the ownership keeper stopped renewing
// leases, and the data node agents released the VIP and forced every instance
// read-only. The cluster stayed down even though the clock was right, and no
// amount of restarting could recover it because the stored watermark could
// never be reached again.
//
// Two things have to stay true at once, which is why this is a gate rather than
// a comment: an earlier observation must still be refused while it is plausible
// (the ordering protection), and a gap far wider than any probe, retry or
// scheduling delay must reset the watermark instead of deadlocking the cluster.
// Dropping either half brings back a real outage, in opposite directions.

const fs = require('fs');
const path = require('path');

const args = process.argv.slice(2);
let repo = path.resolve(__dirname, '..');
for (let index = 0; index < args.length; index += 1) {
  if (args[index] === '--repo') {
    repo = path.resolve(args[index + 1]);
    index += 1;
  }
}

const failures = [];
function check(name, ok, detail) {
  if (ok) {
    console.log(`ok   ${name}`);
    return;
  }
  failures.push(`${name}${detail ? ` — ${detail}` : ''}`);
  console.log(`FAIL ${name}${detail ? ` — ${detail}` : ''}`);
}

function read(relative) {
  const absolute = path.join(repo, relative);
  try {
    return fs.readFileSync(absolute, 'utf8');
  } catch (error) {
    failures.push(`unreadable ${relative}: ${error.message}`);
    console.log(`FAIL unreadable ${relative}`);
    return '';
  }
}

const repository = read('internal/store/repository.go');
const repositoryTests = read('internal/store/repository_test.go');

// The tolerance itself: present, and a positive duration. Zero would mean an
// equal timestamp is the only rejected observation, which silently removes the
// ordering protection; anything unbounded would never let a corrected clock
// recover.
const toleranceDeclaration = repository.match(/^const observationWatermarkRewindTolerance = ([^\n]+)$/m);
check(
  'the rewind tolerance is declared as a package constant',
  Boolean(toleranceDeclaration),
  'observationWatermarkRewindTolerance was not found as a top-level const',
);
const toleranceValue = toleranceDeclaration ? toleranceDeclaration[1].trim() : '';
check(
  'the rewind tolerance is a positive, bounded duration',
  /^[1-9][0-9]* \* time\.(Minute|Second|Hour)$/.test(toleranceValue),
  `declared value is ${JSON.stringify(toleranceValue)}, which must be a positive multiple of an hour, minute or second`,
);
if (/^[1-9][0-9]* \* time\.Hour$/.test(toleranceValue)) {
  const hours = Number.parseInt(toleranceValue, 10);
  check(
    'the rewind tolerance is shorter than a full offset-sized rewind',
    hours < 8,
    `${hours}h is at or above the eight hour offset this tolerance exists to absorb`,
  );
}

// The ordering protection must survive: an observation that is not newer than
// the watermark is still refused.
check(
  'an observation behind the watermark is still refused while it is plausible',
  /!observedAt\.After\(watermark\)/.test(repository),
  'the !observedAt.After(watermark) ordering test is missing from the refresh path',
);
check(
  'the refusal is bounded by the rewind tolerance',
  /watermark\.Sub\(observedAt\) <= observationWatermarkRewindTolerance/.test(repository),
  'the refresh path does not compare the rewind distance against observationWatermarkRewindTolerance',
);
check(
  'the refusal still reports a stale observation rather than a new error kind',
  /ErrStaleObservation/.test(repository),
  'ErrStaleObservation is no longer produced by the refresh path',
);

// The tolerance must be reachable only through the watermark comparison: if it
// were also subtracted from the stored watermark the ordering test would stop
// meaning what the comment says.
check(
  'the tolerance is not used to rewrite the stored watermark in place',
  !/ObservationWatermarks\[[^\]]+\]\s*=\s*[\s\S]{0,80}observationWatermarkRewindTolerance/.test(repository),
  'the stored watermark appears to be adjusted by the tolerance instead of being reset through the normal path',
);

// Both halves need a test that fails when the half they cover is removed. The
// rewind test alone would pass with the ordering protection deleted, and the
// ordering test alone would pass with the deadlock restored.
check(
  'a test covers resetting the watermark after the clock moved backwards',
  /func TestApplyDiscoveryRefreshResetsTheWatermarkWhenTheClockMovedBackwards\(/.test(repositoryTests),
  'TestApplyDiscoveryRefreshResetsTheWatermarkWhenTheClockMovedBackwards is missing',
);
check(
  'a test covers refusing observations that are still plausibly out of order',
  /func TestApplyDiscoveryRefreshRejectsEqualAndOlderObservationsAtomically\(/.test(repositoryTests),
  'TestApplyDiscoveryRefreshRejectsEqualAndOlderObservationsAtomically is missing',
);

const rewindTest = (repositoryTests.match(/func TestApplyDiscoveryRefreshResetsTheWatermarkWhenTheClockMovedBackwards\([\s\S]*?\n}\n/) || [''])[0];
check(
  'the rewind test actually moves the clock by an offset-sized amount',
  /Add\(-8 \* time\.Hour\)/.test(rewindTest),
  'the rewind test does not exercise an eight hour correction',
);
check(
  'the rewind test still asserts that a small rewind is refused',
  /errors\.Is\(err, ErrStaleObservation\)/.test(rewindTest),
  'the rewind test does not prove the ordering protection survived',
);
check(
  'the rewind test proves the reset is durable, not just in-memory',
  /reopened\.ObservationWatermark\(/.test(rewindTest),
  'the rewind test does not reopen the repository to check the persisted watermark',
);

// The comment above the constant is what stops the next reader from deleting
// the tolerance as dead weight. Require it to name the consequence.
const comment = (repository.match(/\/\/[\s\S]{0,2400}?const observationWatermarkRewindTolerance/) || [''])[0];
check(
  'the tolerance documents why the watermark cannot be strictly monotonic',
  /read-?only|read only/i.test(comment),
  'the comment above the constant does not name the read-only safety state the deadlock causes',
);
check(
  'the tolerance names the isolated-deployment clock as the cause',
  /isolated/i.test(comment),
  'the comment above the constant does not explain that an isolated deployment has no external time reference',
);

if (failures.length > 0) {
  console.error(`\n${failures.length} observation watermark tolerance check(s) failed:`);
  for (const failure of failures) console.error(`  - ${failure}`);
  process.exit(1);
}
console.log('\nobservation watermark tolerance: all checks passed');
