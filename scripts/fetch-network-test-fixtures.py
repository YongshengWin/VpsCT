#!/usr/bin/env python3
"""Fetch pinned official test fixtures; verify API asset digests before extraction."""
import argparse
import hashlib
import json
from pathlib import Path, PurePosixPath
import subprocess
import tarfile


def fetch(root, repository, version, name, member, output):
    metadata = json.loads(subprocess.check_output(
        ["gh", "api", f"repos/{repository}/releases/tags/{version}"]
    ))
    asset = next(a for a in metadata["assets"] if a["name"] == name)
    digest = asset.get("digest", "")
    if not digest.startswith("sha256:") or len(digest) != 71:
        raise ValueError("official SHA256 unavailable: " + name)
    url = asset["browser_download_url"]
    expected = f"https://github.com/{repository}/releases/download/{version}/{name}"
    if url != expected:
        raise ValueError("unexpected official asset URL")
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


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("destination", type=Path)
    parser.add_argument("--arch", choices=["amd64", "arm64"], required=True)
    args = parser.parse_args()
    args.destination.mkdir(parents=True, exist_ok=True)
    for suffix, output in [("", "sing-box"), ("-musl", "sing-box-musl")]:
        fetch(args.destination, "SagerNet/sing-box", "v1.14.1",
              f"sing-box-1.14.1-linux-{args.arch}{suffix}.tar.gz", "sing-box", output)
    for name in ["mita", "mieru"]:
        fetch(args.destination, "enfein/mieru", "v3.37.0",
              f"{name}_3.37.0_linux_{args.arch}.tar.gz", name, name)
    rust_arch = {"arm64": "aarch64", "amd64": "x86_64"}[args.arch]
    fetch(args.destination, "shadowsocks/shadowsocks-rust", "v1.25.0",
          f"shadowsocks-v1.25.0.{rust_arch}-unknown-linux-musl.tar.xz", "sslocal", "sslocal")


if __name__ == "__main__":
    main()
