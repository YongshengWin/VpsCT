import datetime as dt
import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
import textwrap
import unittest
from unittest import mock


def load(name):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(name + ".py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


proof = load("ci-proof")
progress = load("ci-go-test")
release = load("verify-release-ci")
fixtures = load("fetch-network-test-fixtures")


class SecurityCheckTests(unittest.TestCase):
    def test_required_security_check_keeps_its_name_and_rejects_incomplete_audits(self):
        # Read the actual workflow step so branch protection and failure handling
        # are covered together without adding a YAML dependency to the lint job.
        workflow = (Path(__file__).resolve().parents[1] / ".github/workflows/ci.yml").read_text()
        match = re.search(r"(?ms)^  security:\n(.*?)(?=^  [A-Za-z0-9_-]+:|\Z)", workflow)
        self.assertIsNotNone(match, "branch protection requires a security check")
        job = match.group(1)
        for line in ("    name: security", "    if: always()", "    needs: security-audit",
                     "    runs-on: ubuntu-24.04", "          AUDIT_RESULT: ${{ needs.security-audit.result }}"):
            self.assertIn(line, job)
        script = re.search(r"(?ms)^        run: \|\n(.*)", job)
        self.assertIsNotNone(script)
        for status in ("success", "failure", "cancelled", "skipped", ""):
            result = subprocess.run(["bash", "-euo", "pipefail", "-c", textwrap.dedent(script.group(1))],
                                    env={**os.environ, "AUDIT_RESULT": status}, capture_output=True, text=True)
            with self.subTest(status=status):
                self.assertEqual(result.returncode == 0, status == "success")
                if status != "success": self.assertIn("Security audit did not succeed", result.stderr)


class EvidenceTests(unittest.TestCase):
    def test_checks_job_requests_every_evidence_gate(self):
        workflow = (Path(__file__).resolve().parents[1] / ".github/workflows/ci.yml").read_text()
        checks = workflow.split("\n  checks:\n", 1)[1]
        needs = re.search(r"^    needs: \[([^\]]+)\]$", checks, re.MULTILINE)
        self.assertIsNotNone(needs, "checks must collect each gate result directly")
        self.assertEqual({name.strip() for name in needs[1].split(",")}, proof.REQUIRED)

    def valid(self):
        return {"schema": 1, "repository": "example/VpsCT", "run_id": 12,
                "source_tree": "a" * 40, "source_head": "b" * 40, "soak_seconds": 600, "soak_sessions": 1024,
                "acme_profile": "renewal",
                "created_at": dt.datetime.now(dt.timezone.utc).isoformat(),
                "results": {name: "success" for name in proof.REQUIRED}}

    def check(self, value):
        proof.validate(value, "example/VpsCT", 12, "a" * 40, "b" * 40)

    def test_exact_success(self):
        self.check(self.valid())

    def test_daily_soak_cannot_authorize_release(self):
        data = self.valid(); data.update(soak_seconds=60, acme_profile="handshake")
        with self.assertRaises(ValueError): self.check(data)
        proof.validate(data, "example/VpsCT", 12, "a" * 40, "b" * 40, require_full=False)
        del data["soak_seconds"]
        with self.assertRaises(ValueError): self.check(data)

    def test_acme_profile_is_required_and_cannot_be_promoted_to_release(self):
        for profile in (None, "", "unknown", "handshake"):
            data = self.valid()
            if profile is None: del data["acme_profile"]
            else: data["acme_profile"] = profile
            with self.subTest(profile=profile), self.assertRaisesRegex(ValueError, "ACME renewal"):
                self.check(data)
        for duration, profile in [(600, "handshake"), (60, "renewal")]:
            data = self.valid(); data.update(soak_seconds=duration, acme_profile=profile)
            with self.subTest(duration=duration), self.assertRaisesRegex(ValueError, "ACME profile differs"):
                proof.validate(data, "example/VpsCT", 12, "a" * 40, "b" * 40, require_full=False)

    def test_identity_and_source_rejection(self):
        for key, value in [("repository", "other/VpsCT"), ("run_id", 13),
                           ("source_tree", "c" * 40), ("source_head", "d" * 40), ("schema", 2), ("soak_sessions", 0)]:
            with self.subTest(key=key), self.assertRaises(ValueError):
                data = self.valid(); data[key] = value; self.check(data)

    def test_incomplete_failed_or_skipped_gates_rejected(self):
        for status in ("failure", "skipped", "cancelled", ""):
            data = self.valid(); data["results"]["soak"] = status
            with self.subTest(status=status), self.assertRaises(ValueError): self.check(data)
        data = self.valid(); del data["results"]["security"]
        with self.assertRaises(ValueError): self.check(data)

    def jobs(self):
        jobs = {name: {"result": "success", "outputs": {}} for name in proof.REQUIRED}
        jobs["source"]["outputs"]["tree"] = "a" * 40
        return jobs

    def test_failed_source_without_outputs_reports_all_failed_gates(self):
        for status in ("failure", "cancelled", "skipped"):
            jobs = self.jobs()
            jobs["source"] = {"result": status}
            jobs["acme"] = {"result": "skipped"}
            with self.subTest(status=status), self.assertRaisesRegex(ValueError, f"acme=skipped.*source={status}"):
                proof.gate_results(jobs, "a" * 40)

    def test_missing_or_unexpected_job_and_source_output_rejected(self):
        for key in proof.REQUIRED:
            jobs = self.jobs(); del jobs[key]
            with self.subTest(missing=key), self.assertRaisesRegex(ValueError, "missing gates: " + key):
                proof.gate_results(jobs, "a" * 40)
        jobs = self.jobs(); jobs["extra"] = {"result": "success"}
        with self.assertRaisesRegex(ValueError, "unexpected gates: extra"):
            proof.gate_results(jobs, "a" * 40)
        for outputs in (None, {}, [], {"tree": ""}):
            jobs = self.jobs(); jobs["source"]["outputs"] = outputs
            with self.subTest(outputs=outputs), self.assertRaisesRegex(ValueError, "did not publish its tested tree"):
                proof.gate_results(jobs, "a" * 40)
        with self.assertRaisesRegex(ValueError, "final checkout differs"):
            proof.gate_results(self.jobs(), "c" * 40)

    def test_evidence_command_writes_only_after_complete_success(self):
        for success in (True, False):
            with self.subTest(success=success), tempfile.TemporaryDirectory() as directory:
                jobs = self.jobs()
                if not success: jobs["source"] = {"result": "failure"}
                with mock.patch.object(proof, "context", return_value=self.valid()), \
                     mock.patch.object(sys, "argv", ["ci-proof.py", "evidence", directory]), \
                     mock.patch.dict(os.environ, {"CI_RESULTS": json.dumps(jobs), "UDP_STRESS_SESSIONS": "1024", "UDP_STRESS_SECONDS": "600", "ACME_PROFILE": "renewal"}):
                    if success:
                        proof.main()
                    else:
                        with self.assertRaisesRegex(ValueError, "source=failure"): proof.main()
                artifact = Path(directory) / "ci-evidence.json"
                self.assertEqual(artifact.exists(), success)
                if success:
                    self.check(json.loads(artifact.read_text()))

    def test_stale_evidence_rejected(self):
        for days in (-1, 8):
            data = self.valid()
            data["created_at"] = (dt.datetime.now(dt.timezone.utc) - dt.timedelta(days=days)).isoformat()
            with self.subTest(days=days), self.assertRaises(ValueError): self.check(data)

    def test_build_inputs_preserve_modes_and_reject_tampering_or_unsafe_paths(self):
        import hashlib
        for name, kind, valid in [("bin/helper", "file", True), ("../escaped", "file", False),
                                   ("bin/link", "symlink", False), ("scripts/changed", "file", False)]:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                root = Path(directory); inputs = root / "inputs"; inputs.mkdir()
                archive = inputs / "ci-build.tar.gz"
                with tarfile.open(archive, "w:gz") as data:
                    member = tarfile.TarInfo(name); member.mode = 0o755
                    if kind == "symlink": member.type = tarfile.SYMTYPE; member.linkname = "../../outside"
                    else: member.size = 4
                    data.addfile(member, io.BytesIO(b"test") if kind == "file" else None)
                info = self.valid(); info.pop("results")
                info["sha256"] = hashlib.sha256(archive.read_bytes()).hexdigest()
                (inputs / "ci-build.json").write_text(json.dumps(info))
                with mock.patch.object(proof, "context", return_value=info):
                    previous = Path.cwd()
                    try:
                        os.chdir(root)
                        if valid:
                            proof.unpack(inputs)
                            self.assertTrue((root / name).stat().st_mode & 0o100)
                            archive.write_bytes(b"tampered")
                            with self.assertRaises(ValueError): proof.unpack(inputs)
                        else:
                            with self.assertRaises(ValueError): proof.unpack(inputs)
                            self.assertFalse((root.parent / "escaped").exists())
                    finally: os.chdir(previous)


class ProgressTests(unittest.TestCase):
    def invoke(self, failed):
        events = [{"Action": "start", "Package": "fixture"},
                  {"Action": "run", "Package": "fixture", "Test": "TestExample"}]
        end = [{"Action": "output", "Package": "fixture", "Test": "TestExample", "Output": "fixture detail\n"},
               {"Action": "fail" if failed else "pass", "Package": "fixture", "Test": "TestExample"}]
        code = "import time; print(%r,flush=True); time.sleep(.08); print(%r,flush=True); raise SystemExit(%d)" % (
            "\n".join(map(json.dumps, events)), "\n".join(map(json.dumps, end)), int(failed))
        output, log = io.StringIO(), io.StringIO()
        result = progress.run([sys.executable, "-c", code], output, log, heartbeat=.02)
        return result, output.getvalue(), log.getvalue()

    def test_live_progress_and_success(self):
        result, output, log = self.invoke(False)
        self.assertEqual(result, 0)
        self.assertIn("Progress:", output)
        self.assertIn("fixture/TestExample", output)
        self.assertIn('"Action": "pass"', log)

    def test_failure_and_diagnostic_preserved(self):
        result, output, _ = self.invoke(True)
        self.assertEqual(result, 1)
        self.assertIn("fixture detail", output)


class ScenarioTests(unittest.TestCase):
    def test_acme_scenarios_reuse_prebuilt_helper_and_propagate_failure(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); scripts = root / "scripts"; scripts.mkdir()
            shutil.copyfile(Path(__file__).with_name("ci-scenarios.sh"), scripts / "ci-scenarios.sh")
            for name in ("fetch-network-test-fixtures.py", "fetch-pebble.py"):
                (scripts / name).write_text("import os, pathlib\nwith pathlib.Path(os.environ['CI_FIXTURE_LOG']).open('a') as log: log.write(%r)\n" % (name + "\n"))
            (scripts / "test-security-boundary-container.sh").write_text('''printf '%s|%s|%s|%s|%s\\n' "$CTLVPS_BOUNDARY_MODE" "$CTLVPS_ACME_SCENARIO" "$SINGBOX_BIN" "$PEBBLE_BIN" "$PROXY_HARDENING_BIN" >> "$CI_FIXTURE_LOG"
exit "${CI_FIXTURE_EXIT:-0}"
''')
            log = root / "calls"
            command = ["bash", str(scripts / "ci-scenarios.sh"), "acme"]
            environment = {**os.environ, "CI_FIXTURE_LOG": str(log)}
            for scenario in ("handshake", "renewal"):
                subprocess.run(command + [scenario], env=environment, check=True, capture_output=True)
            expected = [["candidate", scenario, str(root / ".ci/fixtures/sing-box"),
                         str(root / ".ci/fixtures/pebble"), str(root / "bin/proxy-hardening-test-linux-amd64")]
                        for scenario in ("handshake", "renewal")]
            records = log.read_text().splitlines()
            self.assertEqual([line.split("|") for line in records[2::3]], expected)
            failed = subprocess.run(command + ["handshake"], env={**environment, "CI_FIXTURE_EXIT": "7"}, capture_output=True, text=True)
            self.assertEqual(failed.returncode, 7)
            self.assertNotIn("PASS CI scenario", failed.stdout)
            before = log.read_text()
            invalid = subprocess.run(command + ["unknown"], env=environment, capture_output=True)
            self.assertEqual(invalid.returncode, 2)
            self.assertEqual(log.read_text(), before)

    def test_systemd_wait_requires_ready_state_not_exit_code_one(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            docker = root / "docker"
            docker.write_text('#!/bin/sh\nif [ "${CI_WAIT_FAIL:-0}" = 1 ]; then exit 124; fi\nshift 4\nexec "$@"\n')
            systemctl = root / "systemctl"
            systemctl.write_text('''#!/bin/sh
n=$(cat "$CI_WAIT_COUNT" 2>/dev/null || echo 0); n=$((n + 1)); echo "$n" > "$CI_WAIT_COUNT"
case "$n" in 1) echo 'Failed to connect to bus' >&2; exit 1;; 2) echo starting; exit 1;; *) echo degraded; exit 1;; esac
''')
            for command in (docker, systemctl): command.chmod(0o755)
            environment = {**os.environ, "PATH": str(root) + os.pathsep + os.environ["PATH"], "CI_WAIT_COUNT": str(root / "calls")}
            command = ["bash", str(Path(__file__).with_name("wait-systemd-container.sh")), "ctlvps-test-fixture"]
            result = subprocess.run(command, env=environment, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual((root / "calls").read_text().strip(), "3")
            failed = subprocess.run(command, env={**environment, "CI_WAIT_FAIL": "1"}, capture_output=True)
            self.assertEqual(failed.returncode, 124)

    def test_network_scenarios_and_explicit_soak_profiles(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); scripts = root / "scripts"; scripts.mkdir()
            shutil.copyfile(Path(__file__).with_name("ci-scenarios.sh"), scripts / "ci-scenarios.sh")
            (scripts / "fetch-network-test-fixtures.py").write_text("# fixture metadata downloader is validated separately\n")
            fake = scripts / "test-network-systemd-container.sh"
            fake.write_text('printf "%s|%s|%s|%s\\n" "$NETWORK_SYSTEMD_CASE" "${NETWORK_TEST_CLIENT:-sing-box}" "${UDP_STRESS_SECONDS:-}" "${UDP_STRESS_SESSIONS:-}" >> "$CI_FIXTURE_LOG"\nexit "${CI_FIXTURE_EXIT:-0}"\n')
            log = root / "calls"
            environment = {**os.environ, "CI_FIXTURE_LOG": str(log), "UDP_STRESS_SECONDS": "600", "UDP_STRESS_SESSIONS": "1024"}
            for category, scenario in [("network", x) for x in ("managed", "direct", "ssh", "wireguard", "udp")] + [("soak", "udp")]:
                subprocess.run(["bash", str(scripts / "ci-scenarios.sh"), category, scenario], env=environment,
                               check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
            lines = [line.split("|") for line in log.read_text().splitlines()]
            self.assertEqual([x[0] for x in lines], ["transit-ss2022", "forward", "subscriptions", "direct", "ssh", "wireguard",
                "forward-udp", "forward-both", "forward-udp6", "forward-socks-udp", "forward-wireguard-udp", "forward-stress"])
            self.assertEqual(lines[-1][2:], ["600", "1024"])
            self.assertTrue(all(x[1] == "shadowsocks-rust" for x in lines[3:6]))
            subprocess.run(["bash", str(scripts / "ci-scenarios.sh"), "soak", "udp"],
                env={**environment, "UDP_STRESS_SECONDS": "60"}, check=True, capture_output=True)
            self.assertEqual(log.read_text().splitlines()[-1].split("|")[2], "60")
            subprocess.run(["bash", str(scripts / "ci-scenarios.sh"), "soak", "udp"],
                env={**environment, "UDP_STRESS_SESSIONS": "8192"}, check=True, capture_output=True)
            self.assertEqual(log.read_text().splitlines()[-1].split("|")[3], "8192")
            invalid = subprocess.run(["bash", str(scripts / "ci-scenarios.sh"), "soak", "udp"],
                env={**environment, "UDP_STRESS_SECONDS": "1"}, capture_output=True)
            self.assertEqual(invalid.returncode, 2)
            failed = subprocess.run(["bash", str(scripts / "ci-scenarios.sh"), "network", "direct"],
                env={**environment, "CI_FIXTURE_EXIT": "7"}, capture_output=True, text=True)
            self.assertEqual(failed.returncode, 7)
            self.assertNotIn("PASS CI scenario", failed.stdout)


class ReleaseGateTests(unittest.TestCase):
    def invoke(self, merged_pr=False, valid=True, acme_profile="renewal", soak_seconds=600):
        repository, commit, head, tested = "example/VpsCT", "c" * 40, "b" * 40, "d" * 40
        run = {"id": 12, "head_sha": head if merged_pr else commit, "conclusion": "success",
               "head_repository": {"full_name": repository}, "event": "pull_request" if merged_pr else "workflow_dispatch",
               "html_url": "https://github.com/example/VpsCT/actions/runs/12"}
        evidence = EvidenceTests().valid()
        evidence["source_head"] = tested if merged_pr else commit
        evidence["soak_seconds"] = soak_seconds
        if acme_profile is None: del evidence["acme_profile"]
        else: evidence["acme_profile"] = acme_profile
        if not valid and not merged_pr: evidence["results"]["soak"] = "skipped"

        def gh(*args):
            if args[:2] == ("run", "download"):
                (Path(args[-1]) / "ci-evidence.json").write_text(json.dumps(evidence)); return ""
            endpoint = args[1]
            if endpoint.endswith("/pulls"):
                return json.dumps([{"merged_at": "date", "merge_commit_sha": commit,
                    "base": {"ref": "main"}, "head": {"sha": head, "repo": {"full_name": repository}}}] if merged_pr else [])
            if "/runs?" in endpoint:
                return json.dumps({"workflow_runs": [run] if (not merged_pr or f"head_sha={head}" in endpoint) else []})
            if "/git/commits/" in endpoint:
                return json.dumps({"tree": {"sha": "a" * 40},
                                   "parents": [{"sha": head}] if valid else []})
            raise AssertionError(endpoint)

        with mock.patch.dict(os.environ, {"GITHUB_REPOSITORY": repository, "GITHUB_SHA": commit}), \
             mock.patch.object(release.proof, "tree", return_value="a" * 40), \
             mock.patch.object(release, "gh", side_effect=gh), contextlib.redirect_stdout(io.StringIO()):
            release.verify()

    def test_manual_ci_proof_is_required_to_be_complete(self):
        self.invoke()
        with self.assertRaises(SystemExit): self.invoke(valid=False)

    def test_pr_merge_evidence_is_bound_to_its_actual_parent(self):
        self.invoke(merged_pr=True)
        with self.assertRaises(SystemExit): self.invoke(merged_pr=True, valid=False)

    def test_release_rejects_fast_or_missing_acme_evidence(self):
        for profile, duration in [(None, 600), ("handshake", 600), ("handshake", 60)]:
            with self.subTest(profile=profile, duration=duration), self.assertRaises(SystemExit):
                self.invoke(acme_profile=profile, soak_seconds=duration)


class DownloadRetryTests(unittest.TestCase):
    def test_only_transport_errors_retry_with_bounded_attempts(self):
        for failure, expected, succeeded in [("transport-once", 2, True), ("transport-always", 3, False),
                                             ("checksum", 1, False), ("unknown", 1, False)]:
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                (root / "go").write_text('''#!/usr/bin/env bash
printf '%s\\n' "${GODEBUG:-default}" >> "$CI_CALLS"
count=$(wc -l < "$CI_CALLS")
case "$CI_FAILURE" in
 transport-once) if [[ "$count" -gt 1 ]]; then exit 0; fi; echo 'stream INTERNAL_ERROR' >&2;;
 transport-always) echo 'stream INTERNAL_ERROR' >&2;;
 checksum) echo 'verifying module: checksum mismatch' >&2;;
 *) echo 'unknown compiler or configuration failure' >&2;;
esac
exit 1
''')
                (root / "sleep").write_text('#!/bin/sh\nexit 0\n')
                for name in ("go", "sleep"): (root / name).chmod(0o755)
                calls = root / "calls"
                result = subprocess.run(["bash", str(Path(__file__).with_name("ci-fetch-modules.sh"))],
                    env={**os.environ, "PATH": str(root) + ":" + os.environ["PATH"],
                         "CI_FAILURE": failure, "CI_CALLS": str(calls)}, capture_output=True, text=True)
                records = calls.read_text().splitlines()
                self.assertEqual(len(records), expected)
                self.assertEqual(result.returncode == 0, succeeded)
                self.assertTrue(all("http2client=0" in entry for entry in records[1:]))


class FixtureCatalogTests(unittest.TestCase):
    def test_catalog_keeps_fixed_publisher_version_and_sha256(self):
        descriptor = fixtures.descriptors("amd64")[0]
        asset = dict(zip(("repository", "version", "name", "member", "output"), descriptor))
        asset.update(digest="sha256:" + "a" * 64,
                     url=f"https://github.com/{descriptor[0]}/releases/download/{descriptor[1]}/{descriptor[2]}")
        fixtures.validate_asset(asset, descriptor)
        for key, value in [("url", "http://127.0.0.1/payload"), ("digest", "sha256:bad"),
                           ("repository", "other/project"), ("output", "../outside")]:
            changed = {**asset, key: value}
            with self.subTest(key=key), self.assertRaises(ValueError): fixtures.validate_asset(changed, descriptor)


if __name__ == "__main__":
    unittest.main()
