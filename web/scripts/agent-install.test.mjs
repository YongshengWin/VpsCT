import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, writeFileSync, readFileSync, existsSync, readdirSync, mkdirSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { spawnSync } from "node:child_process";
import { skillInstallCommand } from "../src/lib/agent-install.ts";

test("network installer command accepts HTTPS or explicit loopback HTTP", () => {
  for (const origin of ["https://panel.example", "http://localhost:8080", "http://127.0.0.1:8080", "http://[::1]:8080"]) {
    assert.ok(skillInstallCommand(origin, "codex"));
  }
  for (const origin of ["http://panel.example", "http://127.0.0.2", "ftp://panel.example", "https://user:password@panel.example"]) {
    assert.equal(skillInstallCommand(origin, "codex"), null);
  }
});

test("command executes only a complete successful download, forwards custom directory, and cleans up", () => {
  const root = mkdtempSync(join(tmpdir(), "vpsct-command-"));
  try {
    const bin = join(root, "bin");
    const temp = join(root, "temporary files");
    mkdirSync(bin); mkdirSync(temp);
    // Test substitutes network and interpreter, never installs into a user's skills.
    writeFileSync(join(bin, "curl"), '#!/bin/sh\nwhile [ "$#" -gt 0 ]; do case "$1" in -o) shift; target=$1;; esac; shift; done\nprintf complete > "$target"\nexit "$VPSCT_DOWNLOAD_STATUS"\n', { mode: 0o755 });
    writeFileSync(join(bin, "python3"), '#!/bin/sh\n[ "$(cat "$1")" = complete ] || exit 99\nshift\nprintf "%s\\n" "$@" > "$VPSCT_CALL_FILE"\n', { mode: 0o755 });
    const output = join(root, "called");
    const env = { ...process.env, PATH: `${bin}:${process.env.PATH}`, TMPDIR: temp, VPSCT_CALL_FILE: output, VPSCT_DOWNLOAD_STATUS: "0" };
    const command = skillInstallCommand("https://panel.example", "claude");
    const result = spawnSync("sh", ["-c", `${command} --dir '/custom skills' --update`], { env, encoding: "utf8" });
    assert.equal(result.status, 0, result.stderr);
    assert.equal(readFileSync(output, "utf8"), "--client\nclaude\n--dir\n/custom skills\n--update\n");
    assert.deepEqual(readdirSync(temp), []);
    rmSync(output);
    const failed = spawnSync("sh", ["-c", command], { env: { ...env, VPSCT_DOWNLOAD_STATUS: "22" }, encoding: "utf8" });
    assert.equal(failed.status, 22, failed.stderr);
    assert.equal(existsSync(output), false);
    assert.deepEqual(readdirSync(temp), []);
  } finally { rmSync(root, { recursive: true, force: true }); }
});
