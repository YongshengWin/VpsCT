"""Delegate controllers inside this disposable private cgroup namespace only."""
import errno
import os
from pathlib import Path

assert os.environ.get("CTLVPS_ALPINE_FIXTURE") == "1"
assert Path("/.dockerenv").exists()
base = Path("/sys/fs/cgroup")
assert Path("/proc/1/cgroup").read_text().strip() == "0::/"
child = base / "fixture-init"
child.mkdir(exist_ok=True)
# Docker's namespace root is a domain cgroup containing init. Empty it before
# enabling controllers, as required by the kernel's no-internal-process rule.
for pid in (base / "cgroup.procs").read_text().split():
    try:
        (child / "cgroup.procs").write_text(pid)
    except OSError as error:
        if error.errno != errno.ESRCH:
            raise
(base / "cgroup.subtree_control").write_text("+memory +pids")
