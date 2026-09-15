import assert from "node:assert/strict";
import test from "node:test";
import { buildTemplatePrompt, TEMPLATE_FORMATS } from "../src/lib/template-prompt.ts";

const source = "# SYNTHETIC_EXISTING_CONFIG\ncustom_value: preserve-this-line";
for (const { value: kind } of TEMPLATE_FORMATS) {
  test(`${kind}: adaptation carries the original and targets one output format`, () => {
    const prompt = buildTemplatePrompt({ kind, task: "adapt", source, requirements: "保留原规则顺序", version: "test-version" });
    assert.equal(prompt.split(source).length - 1, 1);
    assert.deepEqual([...prompt.matchAll(/^格式：(.*)$/gm)].map((match) => match[1]), [kind]);
    assert.match(prompt, /客户端 \/ 内核版本：test-version/);
    assert.match(prompt, /## 我的要求\n保留原规则顺序/);
    assert.match(prompt, /不要用下面的最小示例覆盖我的整份配置/);
    assert.equal((prompt.match(/^```/gm) ?? []).length, 2, "one example code block only");
  });

  test(`${kind}: switching to create cannot leak an earlier source draft`, () => {
    const prompt = buildTemplatePrompt({ kind, task: "create", source });
    assert.ok(!prompt.includes(source));
    assert.ok(!prompt.includes("[原配置开始]"));
    assert.deepEqual([...prompt.matchAll(/^格式：(.*)$/gm)].map((match) => match[1]), [kind]);
  });
}

test("missing source asks the AI to wait instead of substituting the example", () => {
  const prompt = buildTemplatePrompt({ kind: "mihomo", task: "adapt", source: "  \n" });
  assert.match(prompt, /请等待输入后再转换/);
});

test("Surge full configuration and proxy fragment have distinct structures", () => {
  const full = buildTemplatePrompt({ kind: "surge", task: "adapt", surgeOutput: "full" });
  const fragment = buildTemplatePrompt({ kind: "surge", task: "adapt", surgeOutput: "proxies" });
  const example = (prompt) => prompt.match(/```ini\n([\s\S]*?)\n```/)[1];
  assert.match(example(full), /\[Proxy Group\]\n\{\{PROXY_GROUPS\}\}/);
  assert.match(example(full), /FINAL,PROXY$/);
  assert.equal(example(fragment), "[Proxy]\n{{PROXIES}}\n\n[Rule]\nFINAL,DIRECT");
  assert.ok(!example(full).includes("{{all}}"));
  assert.ok(!example(fragment).includes("{{all}}"));
  assert.match(fragment, /原主配置应继续保留/);
});
