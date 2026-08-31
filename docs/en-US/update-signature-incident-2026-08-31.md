# Update Package Signature Verification Incident (2026-08-31)

## Incident Summary

- Source version: `2.2-59`
- Target version: `2.2-60`
- Time observed: 2026-08-31 15:04:52
- Console error: `patch signature verification failed`
- Impact: the package was rejected during upload inspection. No update maintenance gate was created and no node RPM was modified.
- Current status: the cause is confirmed. The `2.2-60` update package is retired and must not be delivered or executed. The replacement must use the lab trust chain and a new release number.

## Confirmed Facts

1. The `2.2-60` RPM built successfully and the complete repository test suite passed.
2. Local checks passed for the manifest signature, embedded SHA-256 checksums, source and target versions, rollback RPM, and bootstrap updater.
3. The failed `2.2-60` package used the formal release trust chain. Its public-key SHA-256 fingerprint was `11d4f9c1c4b6989736de2001e89906414a553991b6f5a693eef1ba53f6168d7a`.
4. The active lab uses a separate lab trust chain. Its public-key SHA-256 fingerprint is `46ac59a2234263c1410d79e6e60d3961a5835257ea3cc6b0e1383803cae95c65`.
5. The `2.2-58` to `2.2-59` package previously accepted and executed by the site verifies with the lab key and fails verification with the formal release key. The two trust domains are therefore different.
6. The console rejection of the formal-key-signed `2.2-60` package matches that fingerprint evidence.
7. The active site trust domain was not checked and no real console upload acceptance test was completed before the artifact was declared usable.

## Process Cause

The direct cause was signing `2.2-60` with the formal release trust chain while the `192.168.102.152-154` lab uses its independent lab trust chain. The process also treated successful verification with the formal local key as proof of lab compatibility. Although the build used `--expected-public-key`, that argument referenced the wrong trust domain and did not check continuity with the last package accepted by the site.

## Mandatory Release Gates

Every `.cgupgrade` must pass these gates in order. Missing any gate makes the artifact non-deliverable:

1. Read the effective configuration and trust key on every controller:

   ```bash
   trust_key="$(jq -r '.trust_key' /etc/clusterguard/update.json)"
   test -f "$trust_key"
   openssl pkey -pubin -in "$trust_key" -outform DER | openssl dgst -sha256
   ```

2. All controller fingerprints must match. Repair site trust configuration before building when they differ.
3. Derive the public fingerprint from the offline signing key and compare it with the site fingerprint:

   ```bash
   openssl pkey -in /secure/offline/clusterguard-patch-signing.key \
     -pubout -outform DER | openssl dgst -sha256
   ```

4. Pass a controlled copy of the site public key to `build-clusterguard-patch.sh --expected-public-key`. Never select a historical release key by file name alone.
5. Extract the artifact, verify `PATCH-MANIFEST.sig` with that same site key, and validate `SHA256SUMS`.
6. Log in to the current Raft Leader and upload the `.cgupgrade` through Settings > Version Update.
7. Mark the artifact deliverable only after the console confirms the signature and displays the correct source and target versions. Do not plan or execute the update during upload acceptance.
8. Record the three site fingerprints, signer-derived fingerprint, package SHA-256, upload time, Leader node, and console result in the release evidence.

## Prohibited Shortcuts

- Do not declare site compatibility after local verification alone.
- Do not infer the site key from a file name or historical release number.
- Do not replace a site trust key merely to make one package pass.
- Do not overwrite an artifact that was already delivered or failed acceptance.
- Do not copy the release private key to a customer site or controller.
- Do not replace a real console-upload test with direct RPM installation.

## Required Remediation

1. Rebuild with the lab signing chain. Do not change the site key to accommodate the failed artifact.
2. Retire the failed `2.2-60` package without overwriting it.
3. Build a new RPM and update package with a new release number.
4. Verify with the lab public key and confirm signing-chain continuity with the last site-accepted package.
5. Deliver only after the full test suite and a successful real console upload.
