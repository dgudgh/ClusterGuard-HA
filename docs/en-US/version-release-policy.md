# ClusterGuard HA Version and Release Policy

<!-- LANGUAGE-SWITCH -->
> **Language:** English | [简体中文](../zh-CN/version-release-policy.md)
<!-- /LANGUAGE-SWITCH -->

## 1. Version Format

ClusterGuard HA packages use the following format:

```text
MAJOR.FEATURE-RELEASE
```

For example:

```text
2.1-45
2.2-1
2.2-2
```

The corresponding Git tag uses a dot-separated format:

```text
v2.1.45
v2.2.1
v2.2.2
```

The rules are as follows:

- A major version change indicates a significant change in the control kernel, data model, or compatibility boundary.
- A feature version change indicates the start of a new product capability line, for example, PostgreSQL starting from 2.2.
- The release number strictly increments within the same feature version. Any changes affecting users, such as fixes, features, installers, configurations, dependencies, and delivery documentation, must increase the release number.

## 2. Current Version Lines

| Version Line | Status | Description |
| --- | --- | --- |
| `2.1-45` | Frozen | MySQL stable line, no new features will be added |
| `2.2-N` | Development Line | PostgreSQL capabilities and new features after 2.1 |

`v2.1.45` permanently points to the commit
`28425925a321655f38686942c6b081d235b0d288`. Subsequent fixes must not move this tag.

## 3. Immutable Release Rules

After a formal release is completed, the following objects must not be overwritten:

- Git tags;
- GitHub Releases;
- RPMs;
- Complete offline packages;
- Independent PostgreSQL dependency packages;
- SHA256 files;
- Release Notes.

Any changes to the content of files with the same name are considered a new version. Even if only the installation script or operation manual inside the package is modified, the release number must be increased because the delivery package has changed.

The following practices are prohibited:

- Deleting old tags and recreating tags with the same name;
- Replacing formal Release attachments with `--clobber`;
- Modifying RPM content but keeping the same NEVRA;
- Manually replacing scripts in the offline package without updating the summary;
- Building a formal release from an uncommitted working tree and making the Release point to another commit.

## 4. Branch Rules

- `main` stores formally reviewed, traceable code.
- `codex/2.2-postgresql` is the starting point for 2.2 PostgreSQL development, with the baseline being
  `v2.1.45`.
- Features and fixes are first formed as independent commits and then merged into the corresponding version line.
- The working tree must be clean before building a formal package; the commits in `RELEASE-INFO` must be consistent with the release tag.

Do not rewrite changes for 2.2 PostgreSQL back to `v2.1.45`, nor deliver 2.2 code using 2.1 filenames.

## 5. PostgreSQL 2.2 Delivery Rules

The main offline package for 2.2 contains only a minimal set of signed dependencies required for ClusterGuard and database operation. PostgreSQL official source code compilation dependencies use the following strategy:

1. When `--engine postgresql` is explicitly specified, the default is to resolve from the software source configured on the build node over the network.
2. Compilation dependencies are installed into a temporary DNF installroot, without modifying the production host's RPM database.
3. If the network connection fails, the process must explicitly stop and prompt the user to upload an independent PostgreSQL dependency package.
4. In purely offline environments, `--postgresql-dependencies` is used to specify the decompressed independent dependency repository.
5. The main package and dependency packages are separately verified for SHA256, RPM signature, architecture, system major version, and repository metadata.

Typical 2.2 dependency add-on package name:

```text
clusterguard-ha-2.2-1-postgresql-build-deps-rocky-8-x86_64.tar.gz
```

This dependency add-on package must not be repackaged into the main offline package, so MySQL-only deployments do not carry the PostgreSQL build toolchain.

## 6. Formal Release Gate

Each formal version must complete at least:

```bash
go test ./...
go vet ./...
find scripts -type f -name '*.sh' -print0 | xargs -0 -n1 bash -n
git diff --check
```

And verify:

1. Git working tree is clean, and the tag points to the current commit.
2. RPM name, version, and architecture are correct.
3. Both layers of SHA256 inside and outside the offline package pass.
4. RPM signature, dependencies, systemd unit, and configuration permissions are correct.
5. The installer's read-only plan and actual installation both pass in a clean three-node environment.
6. Corresponding database versions complete switching, failure, old master recovery, restart, network partition, and concurrency testing.
7. Security gate, Leader forwarding, Raft majority, VIP uniqueness, audit, and report all pass.
8. Release Notes clearly specify the supported scope, known boundaries, and upgrade methods.

If any gate fails, only an internal candidate package can be generated, and formal tags or GitHub Releases cannot be created.

## 7. Release Steps

Formal release is executed in the following order:

1. Gather and submit all changes for this version.
2. Execute full testing and on-site acceptance.
3. Build RPM, main offline package, and necessary independent dependency packages from a clean commit.
4. Verify the summary, signature, package content, and installation process.
5. Create an annotated tag, for example, `v2.2.1`.
6. Push the commit and tag.
7. Create a GitHub Release and upload read-only attachments.
8. Redownload the attachments from GitHub and perform a summary and installation smoke test again.

After the release is completed, only new versions are allowed to fix issues.
