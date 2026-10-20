#!/usr/bin/env python3
"""Stream Go JSON test progress, preserving failures and the Go exit status."""
import collections
import json
import os
from pathlib import Path
import queue
import signal
import subprocess
import sys
import threading
import time


def run(command, output=sys.stdout, log=None, heartbeat=15):
    started = time.monotonic()
    process = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
                               text=True, errors="replace", bufsize=1, start_new_session=True)
    messages = queue.Queue(maxsize=1000)

    def read():
        for line in process.stdout:
            if log:
                log.write(line)
                log.flush()
            messages.put(line)
        messages.put(None)

    threading.Thread(target=read, daemon=True).start()
    active, buffers = {}, collections.defaultdict(lambda: collections.deque(maxlen=100))
    passed = failed = 0
    next_beat = started + heartbeat

    def emit(message):
        print(f"[{time.monotonic() - started:6.1f}s] {message}", file=output, flush=True)

    emit("Preparing test binaries; Go race instrumentation may require compilation")
    try:
        while True:
            try:
                line = messages.get(timeout=min(1, heartbeat))
            except queue.Empty:
                line = ""
            if line is None:
                break
            if line:
                try:
                    event = json.loads(line)
                except ValueError:
                    emit(line.rstrip())
                    event = {}
                package, test, action = event.get("Package", ""), event.get("Test", ""), event.get("Action")
                key = package + ("/" + test if test else "")
                if action in ("build-output", "build-fail"):
                    emit(event.get("Output", "build failure: " + event.get("ImportPath", "")).rstrip())
                elif action == "start":
                    emit("START " + package)
                elif action == "run":
                    active[key] = time.monotonic()
                elif action == "output":
                    buffers[key].append(event.get("Output", ""))
                elif action in ("pass", "skip", "fail"):
                    active.pop(key, None)
                    if action == "fail":
                        failed += 1
                        emit("FAIL " + key)
                        for detail in buffers[key]:
                            print(detail, end="", file=output, flush=True)
                    elif test:
                        passed += action == "pass"
                    if not test or event.get("Elapsed", 0) >= 1:
                        emit(f"{action.upper()} {key} ({event.get('Elapsed', 0):.2f}s)")
                    buffers.pop(key, None)
            if time.monotonic() >= next_beat:
                running = ", ".join(sorted(active, key=active.get)[:3]) or "compiling / waiting for a package"
                emit(f"Progress: {passed} passed, {failed} failures; active: {running}")
                next_beat = time.monotonic() + heartbeat
        code = process.wait()
        emit(f"Finished: {passed} passed, {failed} failures; exit={code}")
        return code if code else int(failed != 0)
    finally:
        if process.poll() is None:
            os.killpg(process.pid, signal.SIGTERM)
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait()
        process.stdout.close()


if __name__ == "__main__":
    path = Path(os.environ.get("CTLVPS_TEST_LOG", ".ci/go-tests.ndjson"))
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("w") as log:
        sys.exit(run(["go", "test", "-json", *sys.argv[1:]], log=log))
