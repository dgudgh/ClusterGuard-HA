#!/usr/bin/env python3
"""Isolated systemd regression: no database services or production files touched."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile


def probe(base, fixed):
    data = Path(base) / "pgdata"
    if not fixed:
        try:
            (data / "pg_hba.conf").read_text()
        except PermissionError:
            print("old_sandbox_reproduced_permission_denied")
            return
        raise RuntimeError("old sandbox unexpectedly accessed postgres-owned 0700 PGDATA")
    assert (data / "pg_hba.conf").read_text() == "local all all peer\n"
    for directory in (data, Path(str(data) + ".clusterguard-recovery")):
        fd, temporary = tempfile.mkstemp(prefix=".recovery-", dir=str(directory))
        with os.fdopen(fd, "w") as stream:
            stream.write("fixture only\n")
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, str(directory / "atomic-receipt"))
    try:
        (Path(base) / "not-authorized" / "must-not-write").write_text("denied")
    except OSError as error:
        if error.errno != 30:
            raise
    else:
        raise RuntimeError("sandbox allowed writes outside configured directories")
    print("fixed_sandbox_read_atomic_write_and_sibling_denial_passed")


def main():
    if len(sys.argv) == 4 and sys.argv[1] == "probe":
        probe(sys.argv[2], sys.argv[3] == "fixed")
        return
    if os.geteuid() != 0 or not Path("/run/systemd/system").is_dir():
        raise RuntimeError("requires a root systemd fixture host")
    base = Path(tempfile.mkdtemp(prefix="cg-systemd-recovery-qa.", dir="/data"))
    try:
        data = base / "pgdata"
        data.mkdir(mode=0o700)
        os.chown(str(data), 999, 999)
        hba = data / "pg_hba.conf"
        hba.write_text("local all all peer\n")
        hba.chmod(0o600)
        os.chown(str(hba), 999, 999)
        state = Path(str(data) + ".clusterguard-recovery")
        state.mkdir(mode=0o700)
        (base / "not-authorized").mkdir(mode=0o700)
        script = base / "probe.py"
        shutil.copyfile(__file__, str(script))
        reports = []
        for fixed in (False, True):
            capabilities = "CAP_NET_ADMIN CAP_NET_RAW CAP_SETUID CAP_SETGID"
            if fixed:
                capabilities += " CAP_DAC_OVERRIDE"
            command = ["systemd-run", "--wait", "--collect", "--pipe", "--quiet",
                       "--property=Type=oneshot", "--property=User=root", "--property=Group=root",
                       "--property=ProtectSystem=strict", "--property=ProtectHome=true",
                       "--property=PrivateTmp=true", "--property=NoNewPrivileges=false",
                       "--property=CapabilityBoundingSet=" + capabilities,
                       "--property=AmbientCapabilities=" + capabilities]
            if fixed:
                command += ["--property=ReadWritePaths=" + str(data) + " " + str(state)]
            result = subprocess.run(command + [sys.executable, str(script), "probe", str(base),
                                              "fixed" if fixed else "old"],
                                    stdout=subprocess.PIPE, stderr=subprocess.STDOUT, universal_newlines=True, timeout=30)
            reports.append({"mode": "fixed" if fixed else "old", "exit": result.returncode,
                            "output": result.stdout.strip()})
            if result.returncode:
                raise RuntimeError(json.dumps(reports))
        print(json.dumps({"systemd_regression": "passed", "checks": reports}))
    finally:
        shutil.rmtree(str(base))


if __name__ == "__main__":
    main()
