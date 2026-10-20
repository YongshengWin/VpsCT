#!/usr/bin/env python3
"""Fetch pinned official test fixtures; verify API asset digests before extraction."""
import argparse
import functools
import hashlib
import json
from pathlib import Path, PurePosixPath
import re
import subprocess
import tarfile


@functools.lru_cache(maxsize=8)
def release(repository, version):
    return json.loads(subprocess.check_output(["gh", "api", f"repos/{repository}/releases/tags/{version}"]))


def describe(repository, version, name, member, output):
    asset = next(a for a in release(repository, version)["assets"] if a["name"] == name)
    return {"repository": repository, "version": version, "name": name, "member": member,
            "output": output, "digest": asset.get("digest", ""), "url": asset["browser_download_url"]}


def validate_asset(asset, descriptor):
    keys = ("repository", "version", "name", "member", "output")
    if any(asset.get(key) != value for key, value in zip(keys, descriptor)):
        raise ValueError("fixture catalog identity mismatch")
    if not re.fullmatch(r"sha256:[0-9a-f]{64}", asset.get("digest", "")):
        raise ValueError("official SHA256 unavailable: " + descriptor[2])
    expected = f"https://github.com/{descriptor[0]}/releases/download/{descriptor[1]}/{descriptor[2]}"
    if asset.get("url") != expected:
        raise ValueError("unexpected official asset URL")


def fetch(root, repository, version, name, member, output, asset=None):
    asset = asset or describe(repository, version, name, member, output)
    validate_asset(asset, (repository, version, name, member, output))
    digest, url = asset["digest"], asset["url"]
    archive = root / name
    if not archive.exists() or "sha256:" + hashlib.sha256(archive.read_bytes()).hexdigest() != digest:
        subprocess.run(["curl", "-fLsS", "--retry", "2", "--connect-timeout", "15",
                        "--max-time", "180", "--proto", "=https", "--proto-redir", "=https",
                        url, "-o", str(archive)], check=True)
    if "sha256:" + hashlib.sha256(archive.read_bytes()).hexdigest() != digest:
        raise ValueError("official digest mismatch: " + name)
    with tarfile.open(archive) as tar:
        matches = [m for m in tar.getmembers() if m.isfile() and PurePosixPath(m.name).name == member]
        if len(matches) != 1:
            raise ValueError("unexpected archive members: " + name)
        target = root / output
        target.write_bytes(tar.extractfile(matches[0]).read())
        target.chmod(0o755)
    print("Verified official fixture:", name, flush=True)


def descriptors(arch):
    items = [("SagerNet/sing-box", "v1.14.1", f"sing-box-1.14.1-linux-{arch}{suffix}.tar.gz", "sing-box", output)
             for suffix, output in [("", "sing-box"), ("-musl", "sing-box-musl")]]
    items += [("enfein/mieru", "v3.37.0", f"{name}_3.37.0_linux_{arch}.tar.gz", name, name) for name in ("mita", "mieru")]
    rust_arch = {"arm64": "aarch64", "amd64": "x86_64"}[arch]
    items.append(("shadowsocks/shadowsocks-rust", "v1.25.0",
                  f"shadowsocks-v1.25.0.{rust_arch}-unknown-linux-musl.tar.xz", "sslocal", "sslocal"))
    return items


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("destination", type=Path)
    parser.add_argument("--arch", choices=["amd64", "arm64"], required=True)
    parser.add_argument("--only", nargs="+", choices=["sing-box", "sing-box-musl", "mita", "mieru", "sslocal"])
    parser.add_argument("--manifest-only", action="store_true", help="Share only verified publisher metadata, never upstream binaries")
    parser.add_argument("--catalog", type=Path, help="Use the source job's run-bound publisher metadata")
    args = parser.parse_args()
    if args.manifest_only and args.catalog:
        parser.error("--manifest-only and --catalog are mutually exclusive")
    args.destination.mkdir(parents=True, exist_ok=True)
    saved = {}
    if args.catalog:
        catalog = json.loads(args.catalog.read_text())
        if catalog.get("schema") != 1 or catalog.get("arch") != args.arch:
            raise ValueError("fixture catalog architecture mismatch")
        saved = {a["output"]: a for a in catalog["fixtures"]}
    records = []
    for descriptor in descriptors(args.arch):
        if args.only and descriptor[-1] not in args.only:
            continue
        asset = saved[descriptor[-1]] if args.catalog else describe(*descriptor)
        validate_asset(asset, descriptor)
        if args.manifest_only:
            records.append(asset)
        else:
            fetch(args.destination, *descriptor, asset=asset)
    if args.manifest_only:
        (args.destination / "catalog.json").write_text(json.dumps({"schema": 1, "arch": args.arch, "fixtures": records}, indent=2) + "\n")
        print("Verified publisher metadata catalog; upstream binaries are not included in CI artifacts", flush=True)


if __name__ == "__main__":
    main()
