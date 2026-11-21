#!/usr/bin/env python3
"""Advisory PR scope report. Never emits workflow outputs or release evidence."""
import html
import json
import os
from pathlib import Path
import re
import subprocess

DOCS = {"README.md", "CHANGELOG.md", "docs/operations.md", "docs/network.md",
        "docs/subscriptions.md", "docs/traffic-accounting.md"}
UI = {"web/src/components/logo.tsx", "web/src/index.css", "web/src/pages/manual.tsx",
      "web/src/lib/manual.ts", "scripts/security-browser.mjs"}
CHECKS = {
    "docs": "文档链接、锚点与格式；完整安全门禁",
    "ui": "前端测试、类型检查、生产构建、内嵌网页的控制端、真实浏览器与 fuzz；完整安全门禁",
    "full": "完整 PR 矩阵与安全门禁",
}


def git(*args):
    return subprocess.check_output(["git", *args], stderr=subprocess.DEVNULL, timeout=30)


def classify(raw):
    groups = set()
    examples = []
    fields = raw.split(b"\0")
    if fields.pop() or not fields or len(fields) % 2:
        raise ValueError("empty or incomplete diff")
    for meta, name in zip(fields[::2], fields[1::2]):
        match = re.fullmatch(rb":([0-7]{6}) ([0-7]{6}) [0-9a-f]{40} [0-9a-f]{40} ([AMD])", meta)
        if not match or not name:
            raise ValueError("unexpected raw diff")
        old, new, status = match.groups()
        path = os.fsdecode(name)
        group = "docs" if path in DOCS else "ui" if path in UI else "full"
        if (old, new) != {b"M": (b"100644", b"100644"), b"A": (b"000000", b"100644"),
                          b"D": (b"100644", b"000000")}[status]:
            group = "full"
        groups.add(group)
        if len(examples) < 8 or (group == "full" and all(g != "full" for g, _ in examples)):
            examples.append((group, path))
    profile = "full" if "full" in groups else "ui" if "ui" in groups else "docs"
    checks = CHECKS[profile]
    if profile == "ui" and "docs" in groups:
        checks += "；同时检查文档"
    return profile, checks, len(fields) // 2, examples


def report(event, base, head, release):
    if event != "pull_request" or release != "false":
        return "**full**：手动、发布候选或非普通 PR。保留全部适用检查。"
    if not all(re.fullmatch(r"[0-9a-f]{40}", sha) for sha in (base, head)):
        return "**full**：缺少有效的 PR 基线或头提交。"
    try:
        bases = git("merge-base", "--all", base, head).splitlines()
        if len(bases) != 1 or not re.fullmatch(rb"[0-9a-f]{40}", bases[0]):
            raise ValueError("ambiguous merge base")
        # No rename detection: both paths of a rename are evaluated independently.
        raw = git("diff", "--no-ext-diff", "--no-renames", "--raw", "--no-abbrev", "-z",
                  bases[0].decode(), head, "--")
        profile, checks, count, samples = classify(raw)
    except (ValueError, OSError, subprocess.SubprocessError):
        return "**full**：差异为空、无法解析、无唯一共同祖先或读取失败。"
    details = "\n".join(f"{group}: {html.escape(json.dumps(path, ensure_ascii=True))}" for group, path in samples)
    return (f"**{profile}**：{checks}。\n\n比较 `{bases[0].decode()[:12]}…{head[:12]}`，"
            f"共 {count} 个路径记录。未入白名单或非常规文件类型一律 full。\n\n<pre>{details}</pre>")


if __name__ == "__main__":
    body = report(*(os.environ.get(k, "") for k in ("GITHUB_EVENT_NAME", "BASE_SHA", "HEAD_SHA", "RELEASE_CANDIDATE")))
    summary = "### CI 范围建议（观察模式）\n\n本次不跳过任何任务，不生成发布证据。\n\n" + body + "\n"
    if os.environ.get("GITHUB_STEP_SUMMARY"):
        with Path(os.environ["GITHUB_STEP_SUMMARY"]).open("a", encoding="utf-8") as output:
            output.write(summary)
    else:
        print(summary)
