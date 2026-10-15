# 3.1.3.1 Internal Candidate Rejected

## Rejection reason

This version was never delivered, published as a GitHub Release or installed on site. Final review found that its bundled notes incorrectly made runtime writer lease enforcement a prerequisite for opening primary writes during first installation. Actual first-install bootstrap configures replication and opens writes on the first primary before controller initialization, as in the baseline.

## Replacement candidate

Original package bytes and checksums are retained. The corrected complete candidate is [3.1.3.2](release-3.1.3.2.md), RPM2.2-110. Do not deliver 3.1.3.1; ART/FIELD did not pass.
