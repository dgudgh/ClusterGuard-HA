package scripts

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

type installerMediaFixture struct {
	root        string
	local       string
	remoteData  string
	destination string
	copyLog     string
}

func newInstallerMediaFixture(t *testing.T, localContents string) installerMediaFixture {
	t.Helper()
	root := t.TempDir()
	local := filepath.Join(root, "mysql-8.0.44.tar.xz")
	if err := os.WriteFile(local, []byte(localContents), 0o600); err != nil {
		t.Fatal(err)
	}
	remoteData := filepath.Join(root, "remote", "node-a", "data")
	destination := filepath.Join(remoteData, "packages", "mysql", filepath.Base(local))
	if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(remoteData, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(remoteData, "packages"), 0o711); err != nil {
		t.Fatal(err)
	}
	return installerMediaFixture{
		root:        root,
		local:       local,
		remoteData:  remoteData,
		destination: destination,
		copyLog:     filepath.Join(root, "copy.log"),
	}
}

type installerMediaDirectoryIdentity struct {
	mode os.FileMode
	uid  uint32
	gid  uint32
}

func installerMediaDirectoryStat(t *testing.T, path string) installerMediaDirectoryIdentity {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("unsupported stat type for %s", path)
	}
	return installerMediaDirectoryIdentity{mode: info.Mode().Perm(), uid: stat.Uid, gid: stat.Gid}
}

func (fixture installerMediaFixture) run(t *testing.T, mode string, extraEnv ...string) (string, error) {
	t.Helper()
	command := exec.Command("bash", "-c", `
set -euo pipefail
export CG_INSTALLER_LIBRARY_ONLY=true
source ./install_clusterguard.sh
database_package="$CG_TEST_LOCAL_PACKAGE"
database_engine=mysql
all_nodes=(node-a)

remote_exec() {
  local host="$1" command="$2" mapped_data mapped_command output status remote_path
  mapped_data="$CG_TEST_ROOT/remote/$host/data"
  if [[ "$CG_TEST_MODE" == "remote-failure" && "$command" == *"cleanup()"* && ! -e "$CG_TEST_ROOT/remote-failed" ]]; then
    : >"$CG_TEST_ROOT/remote-failed"
    return 71
  fi
  mapped_command="${command//\/data/$mapped_data}"
  mapped_command="${mapped_command//required_parent_uid=0/required_parent_uid=${CG_TEST_EXPECT_ROOT_UID:-$(id -u)}}"
  mapped_command="${mapped_command//-o root -g root/-o $(id -u) -g $(id -g)}"
  mapped_command="${mapped_command//chown root:root/chown $(id -u):$(id -g)}"
  mapped_command="${mapped_command//0:0:640/$(id -u):$(id -g):640}"
  mapped_command="${mapped_command//0:0:750/$(id -u):$(id -g):750}"
  mapped_command="${mapped_command//mv -fT --/mv -f --}"
  if [[ "$CG_TEST_MODE" == "remote-hash-failure" ]]; then
    if output="$(PATH="$CG_TEST_REMOTE_HASH_PATH:$PATH" /bin/bash -c "$mapped_command" 2>&1)"; then
      status=0
    else
      status=$?
    fi
  elif output="$(/bin/bash -c "$mapped_command" 2>&1)"; then
    status=0
  else
    status=$?
  fi
  remote_path=/data
  output="${output//$mapped_data/$remote_path}"
  printf '%s' "$output"
  return "$status"
}

remote_copy() {
  local source="$1" host="$2" destination="$3" mapped_data mapped_destination old_contents="missing"
  mapped_data="$CG_TEST_ROOT/remote/$host/data"
  mapped_destination="${destination/#\/data/$mapped_data}"
  if [[ -f "$CG_TEST_DESTINATION" && ! -L "$CG_TEST_DESTINATION" ]]; then
    old_contents="$(/bin/cat -- "$CG_TEST_DESTINATION")"
  fi
  printf '%s\told=%s\n' "$destination" "$old_contents" >>"$CG_TEST_COPY_LOG"
  case "$CG_TEST_MODE" in
    corrupt-copy) printf 'corrupt-transfer' >"$mapped_destination" ;;
    copy-failure) return 72 ;;
    *) /bin/cp -- "$source" "$mapped_destination" ;;
  esac
}

distribute_database_package
`)
	command.Env = append(os.Environ(),
		"CG_TEST_ROOT="+fixture.root,
		"CG_TEST_LOCAL_PACKAGE="+fixture.local,
		"CG_TEST_DESTINATION="+fixture.destination,
		"CG_TEST_COPY_LOG="+fixture.copyLog,
		"CG_TEST_MODE="+mode,
	)
	command.Env = append(command.Env, extraEnv...)
	output, err := command.CombinedOutput()
	return string(output), err
}

func writeInstallerMediaRemoteFile(t *testing.T, fixture installerMediaFixture, contents string) {
	t.Helper()
	if err := os.WriteFile(fixture.destination, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readInstallerMediaFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func assertNoInstallerUploadTemps(t *testing.T, fixture installerMediaFixture) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(fixture.destination), ".*.upload.*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("private upload files were not cleaned: %v", matches)
	}
}

func installerMediaBrokenHashPath(t *testing.T) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sha256sum", "shasum"} {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 17\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return directory
}

func TestInstallerDatabaseMediaReusesOnlyMatchingSHA256(t *testing.T) {
	fixture := newInstallerMediaFixture(t, "approved database media\n")
	writeInstallerMediaRemoteFile(t, fixture, "approved database media\n")
	dataBefore := installerMediaDirectoryStat(t, fixture.remoteData)
	packagesPath := filepath.Join(fixture.remoteData, "packages")
	packagesBefore := installerMediaDirectoryStat(t, packagesPath)

	output, err := fixture.run(t, "reuse")
	if err != nil {
		t.Fatalf("reuse failed: %v\n%s", err, output)
	}
	if !strings.Contains(output, "复用数据库介质：node-a") || !strings.Contains(output, "耗时") {
		t.Fatalf("reuse did not report node and elapsed time: %s", output)
	}
	if _, err := os.Stat(fixture.copyLog); !os.IsNotExist(err) {
		t.Fatalf("matching media was unexpectedly copied: %v", err)
	}
	if got := readInstallerMediaFile(t, fixture.destination); got != "approved database media\n" {
		t.Fatalf("reused media changed: %q", got)
	}
	info, err := os.Stat(fixture.destination)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("reused media mode = %v; want 0640", info.Mode().Perm())
	}
	if dataAfter := installerMediaDirectoryStat(t, fixture.remoteData); dataAfter != dataBefore || dataAfter.mode != 0o755 {
		t.Fatalf("existing /data identity changed: before=%+v after=%+v", dataBefore, dataAfter)
	}
	if packagesAfter := installerMediaDirectoryStat(t, packagesPath); packagesAfter != packagesBefore || packagesAfter.mode != 0o711 {
		t.Fatalf("existing /data/packages identity changed: before=%+v after=%+v", packagesBefore, packagesAfter)
	}
}

func TestInstallerDatabaseMediaCorrectsEngineDirectoryIdentityWithoutChangingParents(t *testing.T) {
	fixture := newInstallerMediaFixture(t, "approved database media\n")
	writeInstallerMediaRemoteFile(t, fixture, "approved database media\n")
	engineDirectory := filepath.Dir(fixture.destination)
	if err := os.Chmod(engineDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	wantUID, wantGID := uint32(os.Geteuid()), uint32(os.Getegid())
	differentGID := -1
	if os.Geteuid() == 0 {
		differentGID = 12345
	} else if groups, err := os.Getgroups(); err == nil {
		for _, group := range groups {
			if group != os.Getegid() {
				differentGID = group
				break
			}
		}
	}
	if differentGID < 0 {
		t.Skip("an alternate group is required to emulate a non-root engine directory owner")
	}
	if err := os.Chown(engineDirectory, os.Geteuid(), differentGID); err != nil {
		t.Fatal(err)
	}
	dataBefore := installerMediaDirectoryStat(t, fixture.remoteData)
	packagesPath := filepath.Join(fixture.remoteData, "packages")
	packagesBefore := installerMediaDirectoryStat(t, packagesPath)
	engineBefore := installerMediaDirectoryStat(t, engineDirectory)
	if engineBefore.gid == wantGID {
		t.Fatalf("test setup did not create a non-target engine directory group: %+v", engineBefore)
	}

	output, err := fixture.run(t, "reuse")
	if err != nil {
		t.Fatalf("engine directory reconciliation failed: %v\n%s", err, output)
	}
	engineAfter := installerMediaDirectoryStat(t, engineDirectory)
	if engineAfter.uid != wantUID || engineAfter.gid != wantGID || engineAfter.mode != 0o750 {
		t.Fatalf("engine directory identity was not corrected: before=%+v after=%+v", engineBefore, engineAfter)
	}
	if dataAfter := installerMediaDirectoryStat(t, fixture.remoteData); dataAfter != dataBefore {
		t.Fatalf("engine reconciliation changed /data: before=%+v after=%+v", dataBefore, dataAfter)
	}
	if packagesAfter := installerMediaDirectoryStat(t, packagesPath); packagesAfter != packagesBefore {
		t.Fatalf("engine reconciliation changed /data/packages: before=%+v after=%+v", packagesBefore, packagesAfter)
	}
}

func TestInstallerDatabaseMediaRejectsUnsafeParentDirectoriesWithoutChangingThem(t *testing.T) {
	t.Run("world-writable", func(t *testing.T) {
		fixture := newInstallerMediaFixture(t, "approved database media\n")
		writeInstallerMediaRemoteFile(t, fixture, "approved database media\n")
		if err := os.Chmod(fixture.remoteData, 0o777); err != nil {
			t.Fatal(err)
		}
		before := installerMediaDirectoryStat(t, fixture.remoteData)

		output, err := fixture.run(t, "unsafe-parent")
		if err == nil || !strings.Contains(output, "父目录必须由 root 拥有且不能被组或其他用户写入") {
			t.Fatalf("world-writable /data was not rejected: err=%v\n%s", err, output)
		}
		if after := installerMediaDirectoryStat(t, fixture.remoteData); after != before || after.mode != 0o777 {
			t.Fatalf("unsafe /data was modified: before=%+v after=%+v", before, after)
		}
		if _, err := os.Stat(fixture.copyLog); !os.IsNotExist(err) {
			t.Fatalf("media was copied through unsafe /data: %v", err)
		}
	})

	t.Run("world-writable-packages", func(t *testing.T) {
		fixture := newInstallerMediaFixture(t, "approved database media\n")
		writeInstallerMediaRemoteFile(t, fixture, "approved database media\n")
		packagesPath := filepath.Join(fixture.remoteData, "packages")
		if err := os.Chmod(packagesPath, 0o777); err != nil {
			t.Fatal(err)
		}
		before := installerMediaDirectoryStat(t, packagesPath)

		output, err := fixture.run(t, "unsafe-parent")
		if err == nil || !strings.Contains(output, "父目录必须由 root 拥有且不能被组或其他用户写入") {
			t.Fatalf("world-writable /data/packages was not rejected: err=%v\n%s", err, output)
		}
		if after := installerMediaDirectoryStat(t, packagesPath); after != before || after.mode != 0o777 {
			t.Fatalf("unsafe /data/packages was modified: before=%+v after=%+v", before, after)
		}
		if _, err := os.Stat(fixture.copyLog); !os.IsNotExist(err) {
			t.Fatalf("media was copied through unsafe /data/packages: %v", err)
		}
	})

	t.Run("non-root-owner", func(t *testing.T) {
		fixture := newInstallerMediaFixture(t, "approved database media\n")
		writeInstallerMediaRemoteFile(t, fixture, "approved database media\n")
		extraEnv := []string{"CG_TEST_EXPECT_ROOT_UID=0"}
		if os.Geteuid() == 0 {
			if err := os.Chown(fixture.remoteData, 12345, os.Getegid()); err != nil {
				t.Fatal(err)
			}
		}
		before := installerMediaDirectoryStat(t, fixture.remoteData)
		if before.uid == 0 {
			t.Fatalf("test setup did not create a non-root-owned /data: %+v", before)
		}

		output, err := fixture.run(t, "unsafe-parent", extraEnv...)
		if err == nil || !strings.Contains(output, "父目录必须由 root 拥有且不能被组或其他用户写入") {
			t.Fatalf("non-root-owned /data was not rejected: err=%v\n%s", err, output)
		}
		if after := installerMediaDirectoryStat(t, fixture.remoteData); after != before {
			t.Fatalf("non-root-owned /data was modified: before=%+v after=%+v", before, after)
		}
		if _, err := os.Stat(fixture.copyLog); !os.IsNotExist(err) {
			t.Fatalf("media was copied through non-root-owned /data: %v", err)
		}
	})
}

func TestInstallerDatabaseMediaReplacesSameLengthDifferentContentAtomically(t *testing.T) {
	fixture := newInstallerMediaFixture(t, "new-media")
	writeInstallerMediaRemoteFile(t, fixture, "old-media")

	output, err := fixture.run(t, "transfer")
	if err != nil {
		t.Fatalf("transfer failed: %v\n%s", err, output)
	}
	if !strings.Contains(output, "传输数据库介质：node-a") || !strings.Contains(output, "耗时") {
		t.Fatalf("transfer did not report node and elapsed time: %s", output)
	}
	if got := readInstallerMediaFile(t, fixture.destination); got != "new-media" {
		t.Fatalf("published media = %q, want new-media", got)
	}
	copyRecord := readInstallerMediaFile(t, fixture.copyLog)
	if !strings.Contains(copyRecord, ".mysql-8.0.44.tar.xz.upload.") || strings.HasPrefix(copyRecord, "/data/packages/mysql/mysql-8.0.44.tar.xz\t") {
		t.Fatalf("copy did not use a private temporary path: %s", copyRecord)
	}
	if !strings.Contains(copyRecord, "old=old-media") {
		t.Fatalf("old destination was not preserved through transfer: %s", copyRecord)
	}
	assertNoInstallerUploadTemps(t, fixture)
	info, err := os.Stat(fixture.destination)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("published media mode = %v; want 0640", info.Mode().Perm())
	}
}

func TestInstallerDatabaseMediaKeepsOldFileOnCorruptCopyOrRemoteFailure(t *testing.T) {
	for _, mode := range []string{"corrupt-copy", "copy-failure", "remote-failure"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newInstallerMediaFixture(t, "new-media")
			writeInstallerMediaRemoteFile(t, fixture, "old-media")

			output, err := fixture.run(t, mode)
			if err == nil {
				t.Fatalf("%s unexpectedly succeeded: %s", mode, output)
			}
			if got := readInstallerMediaFile(t, fixture.destination); got != "old-media" {
				t.Fatalf("%s replaced the old file with %q", mode, got)
			}
			assertNoInstallerUploadTemps(t, fixture)
		})
	}
}

func TestInstallerDatabaseMediaRejectsTargetSymlinks(t *testing.T) {
	t.Run("destination", func(t *testing.T) {
		fixture := newInstallerMediaFixture(t, "new-media")
		victim := filepath.Join(fixture.root, "victim")
		if err := os.WriteFile(victim, []byte("do-not-touch"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(victim, fixture.destination); err != nil {
			t.Fatal(err)
		}
		output, err := fixture.run(t, "symlink")
		if err == nil || !strings.Contains(output, "不能是符号链接") {
			t.Fatalf("destination symlink was not rejected: err=%v\n%s", err, output)
		}
		if got := readInstallerMediaFile(t, victim); got != "do-not-touch" {
			t.Fatalf("symlink victim changed: %q", got)
		}
	})

	t.Run("directory", func(t *testing.T) {
		fixture := newInstallerMediaFixture(t, "new-media")
		engineDirectory := filepath.Dir(fixture.destination)
		if err := os.Remove(engineDirectory); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(fixture.root, "outside")
		if err := os.MkdirAll(outside, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, engineDirectory); err != nil {
			t.Fatal(err)
		}
		output, err := fixture.run(t, "symlink")
		if err == nil || !strings.Contains(output, "不能是符号链接") {
			t.Fatalf("directory symlink was not rejected: err=%v\n%s", err, output)
		}
	})
}

func TestInstallerDatabaseMediaFailsClosedWhenHashingFails(t *testing.T) {
	brokenHashPath := installerMediaBrokenHashPath(t)

	t.Run("local", func(t *testing.T) {
		fixture := newInstallerMediaFixture(t, "new-media")
		output, err := fixture.run(t, "local-hash-failure", "PATH="+brokenHashPath+":"+os.Getenv("PATH"))
		if err == nil || !strings.Contains(output, "无法计算数据库介质 SHA256") {
			t.Fatalf("local hash failure was not fail-closed: err=%v\n%s", err, output)
		}
	})

	t.Run("remote", func(t *testing.T) {
		fixture := newInstallerMediaFixture(t, "new-media")
		writeInstallerMediaRemoteFile(t, fixture, "old-media")
		output, err := fixture.run(t, "remote-hash-failure", "CG_TEST_REMOTE_HASH_PATH="+brokenHashPath)
		if err == nil || !strings.Contains(output, "无法安全检查数据库介质目标") {
			t.Fatalf("remote hash failure was not fail-closed: err=%v\n%s", err, output)
		}
		if got := readInstallerMediaFile(t, fixture.destination); got != "old-media" {
			t.Fatalf("remote hash failure replaced old media with %q", got)
		}
	})
}

func TestInstallerDatabaseMediaProductionCommandRequiresRootOwnedModeAndAtomicMove(t *testing.T) {
	contents, err := os.ReadFile("install_clusterguard.sh")
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	for _, required := range []string{
		`required_parent_uid=0`,
		`(8#\${parent_mode} & 8#022)`,
		`install -d -o root -g root -m 0750 \"\${directory}\"`,
		`test \"\${directory_identity}\" = '0:0:750'`,
		`chown root:root \"\${temp}\"`,
		`chmod 0640 \"\${temp}\"`,
		`stat -c '%u:%g:%a'`,
		`= '0:0:640'`,
		`mv -fT -- \"\${temp}\" \"\${destination}\"`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("database media publication is missing %q", required)
		}
	}
}
