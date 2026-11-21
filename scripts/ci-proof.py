#!/usr/bin/env python3
"""Bind reusable CI evidence and build inputs to the exact tested Git tree."""
import argparse
import datetime as dt
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import subprocess
import tarfile

REQUIRED = {"lint", "source", "race-store", "race", "integration", "network", "alpine", "soak", "upgrade", "acme", "security"}
VERSION = "v0.1.999-ci"


def digest(path):
    with Path(path).open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def tree():
    return subprocess.check_output(["git", "rev-parse", "HEAD^{tree}"], text=True).strip()


def context():
    return {"schema": 1, "repository": os.environ["GITHUB_REPOSITORY"],
            "run_id": int(os.environ["GITHUB_RUN_ID"]), "source_head": os.environ["GITHUB_SHA"],
            "source_tree": tree(), "created_at": dt.datetime.now(dt.timezone.utc).isoformat()}


def gate_results(results, source_tree):
    """Reject incomplete runs before reading outputs that failed jobs never set."""
    if not isinstance(results, dict):
        raise ValueError("CI job results must be an object")
    missing, unexpected = REQUIRED - results.keys(), results.keys() - REQUIRED
    statuses = {name: value.get("result", "missing") if isinstance(value, dict) else "invalid"
                for name, value in results.items()}
    failed = {name: status for name, status in statuses.items() if status != "success"}
    problems = []
    if missing:
        problems.append("missing gates: " + ", ".join(sorted(missing)))
    if unexpected:
        problems.append("unexpected gates: " + ", ".join(sorted(unexpected)))
    if failed:
        problems.append("unsuccessful gates: " + ", ".join(f"{name}={status}" for name, status in sorted(failed.items())))
    if problems:
        raise ValueError("CI evidence rejected; " + "; ".join(problems))
    outputs = results["source"].get("outputs")
    if not isinstance(outputs, dict) or not outputs.get("tree"):
        raise ValueError("CI evidence rejected; successful source job did not publish its tested tree")
    if outputs["tree"] != source_tree:
        raise ValueError("final checkout differs from the tested build")
    return statuses


def validate(proof, repository, run_id, source_tree, source_head, now=None, require_full=True):
    if proof.get("schema") != 1 or proof.get("repository") != repository or proof.get("run_id") != run_id:
        raise ValueError("CI evidence identity mismatch")
    if proof.get("source_tree") != source_tree or proof.get("source_head") != source_head:
        raise ValueError("CI evidence does not match the tested source")
    if not re.fullmatch(r"[0-9a-f]{40}", source_tree):
        raise ValueError("invalid source tree")
    results = proof.get("results", {})
    if set(results) != REQUIRED or any(result != "success" for result in results.values()):
        raise ValueError("CI evidence is missing a successful required gate")
    if proof.get("soak_sessions") not in (1024, 8192):
        raise ValueError("missing UDP session profile")
    duration = proof.get("soak_seconds")
    if duration not in (60, 600) or (require_full and duration != 600):
        raise ValueError("release evidence requires the complete 600-second UDP soak")
    acme_profile = proof.get("acme_profile")
    if acme_profile not in ("handshake", "renewal") or (require_full and acme_profile != "renewal"):
        raise ValueError("release evidence requires the complete ACME renewal profile")
    if acme_profile != ("renewal" if duration == 600 else "handshake"):
        raise ValueError("ACME profile differs from the selected daily/release acceptance profile")
    age = (now or dt.datetime.now(dt.timezone.utc)) - dt.datetime.fromisoformat(proof["created_at"])
    if age.total_seconds() < -600 or age > dt.timedelta(days=7):
        raise ValueError("CI evidence is stale or future-dated")


def unpack(directory):
    directory = Path(directory)
    info = json.loads((directory / "ci-build.json").read_text())
    current = context()
    for field in ("schema", "repository", "run_id", "source_head", "source_tree"):
        if info.get(field) != current[field]:
            raise ValueError("build input identity mismatch: " + field)
    archive = directory / "ci-build.tar.gz"
    if digest(archive) != info["sha256"]:
        raise ValueError("build input digest mismatch")
    prefixes = ("web/dist", "bin", "release/" + VERSION, ".ci/fixtures")
    with tarfile.open(archive) as data:
        for member in data.getmembers():
            name = PurePosixPath(member.name)
            if name.is_absolute() or ".." in name.parts or not (member.isfile() or member.isdir()):
                raise ValueError("unsafe build input member")
            if not any(str(name) == p or str(name).startswith(p + "/") for p in prefixes):
                raise ValueError("unexpected build input member: " + str(name))
        data.extractall(filter="data")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["build", "unpack", "evidence"])
    parser.add_argument("directory", type=Path)
    args = parser.parse_args()
    args.directory.mkdir(parents=True, exist_ok=True)
    if args.action == "unpack":
        unpack(args.directory)
        return
    proof = context()
    if args.action == "build":
        subprocess.run(["git", "diff", "--exit-code", "--", ".", ":!web/dist"], check=True)
        if {path.name for path in Path(".ci/fixtures").iterdir()} != {"catalog.json"}:
            raise ValueError("shared CI artifact may contain publisher metadata only, not upstream binaries")
        archive = args.directory / "ci-build.tar.gz"
        with tarfile.open(archive, "w:gz", compresslevel=1) as data:
            for name in ("web/dist", "bin", "release/" + VERSION, ".ci/fixtures"):
                data.add(name, arcname=name)
        proof["sha256"] = digest(archive)
        filename = "ci-build.json"
    else:
        proof["soak_sessions"] = int(os.environ["UDP_STRESS_SESSIONS"])
        proof["soak_seconds"] = int(os.environ["UDP_STRESS_SECONDS"])
        results = json.loads(os.environ["CI_RESULTS"])
        proof["results"] = gate_results(results, proof["source_tree"])
        proof["acme_profile"] = os.environ.get("ACME_PROFILE")
        validate(proof, proof["repository"], proof["run_id"], proof["source_tree"], proof["source_head"], require_full=False)
        filename = "ci-evidence.json"
    (args.directory / filename).write_text(json.dumps(proof, indent=2) + "\n")


if __name__ == "__main__":
    try:
        main()
    except ValueError as error:
        raise SystemExit(str(error)) from error
