#!/usr/bin/env python3
"""Fetch the pinned official Pebble CA for disposable ACME tests only."""
import argparse
import importlib.util
from pathlib import Path

spec = importlib.util.spec_from_file_location("fixtures", Path(__file__).with_name("fetch-network-test-fixtures.py"))
fixtures = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fixtures)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("destination", type=Path)
    parser.add_argument("--arch", choices=["amd64", "arm64"], required=True)
    args = parser.parse_args()
    args.destination.mkdir(parents=True, exist_ok=True)
    fixtures.fetch(args.destination, "letsencrypt/pebble", "v2.10.1",
                   f"pebble-linux-{args.arch}.tar.gz", "pebble", "pebble")


if __name__ == "__main__":
    main()
