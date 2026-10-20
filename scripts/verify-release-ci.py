#!/usr/bin/env python3
"""Fail closed unless successful CI evidence matches this release's exact tree."""
import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile

spec = importlib.util.spec_from_file_location("ci_proof", Path(__file__).with_name("ci-proof.py"))
proof = importlib.util.module_from_spec(spec)
spec.loader.exec_module(proof)


def gh(*args):
    return subprocess.check_output(["gh", *args], text=True)


def verify():
    repository = os.environ["GITHUB_REPOSITORY"]
    commit = os.environ["GITHUB_SHA"]
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*", repository) or not re.fullmatch(r"[0-9a-f]{40}", commit):
        raise ValueError("invalid release identity")
    source_tree = proof.tree()
    heads = {commit}
    pulls = json.loads(gh("api", f"repos/{repository}/commits/{commit}/pulls"))
    for pr in pulls:
        if (pr.get("merged_at") and pr.get("merge_commit_sha") == commit
                and pr["base"]["ref"] == "main" and pr["head"]["repo"]
                and pr["head"]["repo"]["full_name"] == repository):
            heads.add(pr["head"]["sha"])
    for head in sorted(heads):
        if not re.fullmatch(r"[0-9a-f]{40}", head):
            continue
        runs = json.loads(gh("api", f"repos/{repository}/actions/workflows/ci.yml/runs?head_sha={head}&status=success&per_page=20"))
        for run in runs["workflow_runs"]:
            if (run["conclusion"] != "success" or run["event"] not in ("pull_request", "workflow_dispatch", "push")
                    or run["head_repository"]["full_name"] != repository):
                continue
            identifier = run["id"]
            with tempfile.TemporaryDirectory() as directory:
                try:
                    gh("run", "download", str(identifier), "--repo", repository,
                       "--name", "ci-evidence", "--dir", directory)
                    evidence = json.loads((Path(directory) / "ci-evidence.json").read_text())
                    # For a PR, GITHUB_SHA identifies the tested merge commit,
                    # while the run API's head_sha identifies the PR head.
                    # Both are recorded and bound by the successful run.
                    tested_head = evidence.get("source_head", "")
                    if not re.fullmatch(r"[0-9a-f]{40}", tested_head):
                        raise ValueError("invalid tested commit")
                    tested_tree = json.loads(gh("api", f"repos/{repository}/git/commits/{tested_head}"))
                    if tested_tree["tree"]["sha"] != source_tree:
                        raise ValueError("tested commit tree differs from release")
                    if evidence.get("source_head") != run["head_sha"]:
                        tested_commit = evidence.get("source_head", "")
                        if run["event"] != "pull_request" or not re.fullmatch(r"[0-9a-f]{40}", tested_commit):
                            raise ValueError("tested commit mismatch")
                        actual = json.loads(gh("api", f"repos/{repository}/git/commits/{tested_commit}"))
                        if actual["tree"]["sha"] != source_tree:
                            raise ValueError("tested PR merge tree mismatch")
                        parents = {p["sha"] for p in actual["parents"]}
                        if run["head_sha"] not in parents:
                            raise ValueError("tested merge commit is not bound to the PR head")
                    proof.validate(evidence, repository, identifier, source_tree, evidence["source_head"])
                except (ValueError, KeyError, OSError, subprocess.CalledProcessError) as error:
                    print(f"CI run {identifier} not reusable: {error}", flush=True)
                    continue
            print(f"Verified CI evidence: {run['html_url']} tree={source_tree}", flush=True)
            return
    raise SystemExit("No fresh successful CI evidence for this exact source tree. Run CI manually on the tag commit before publishing.")


if __name__ == "__main__":
    verify()
