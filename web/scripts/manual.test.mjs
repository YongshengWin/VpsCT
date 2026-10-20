import test from "node:test";
import assert from "node:assert/strict";
import { manualDocuments, manualHref, searchManual } from "../src/lib/manual.ts";

test("number searches match the complete hierarchy prefix rather than a numeric substring", () => {
  assert.deepEqual(searchManual("3.2 前提条件").map(doc => doc.number), ["3.2"]);
  assert.deepEqual(searchManual("3.2.4").map(doc => doc.number), ["3.2"]);
  assert.ok(searchManual("3").every(doc => doc.topicNumber === "3"));
  assert.equal(searchManual("999.1").length, 0);
});

test("chapter URLs stay compatible and section URLs resolve to unique numbered headings", () => {
  assert.equal(manualHref("network", "routes"), "/manual?topic=network&chapter=routes");
  assert.equal(manualHref("network", "routes", "parameters"), "/manual?topic=network&chapter=routes#doc-network-routes-parameters");
  const anchors = new Set();
  for (const doc of manualDocuments) {
    for (const section of doc.sections) {
      assert.equal(anchors.has(section.anchor), false);
      anchors.add(section.anchor);
      assert.ok(section.number.startsWith(doc.number + "."));
      assert.equal(new URL(manualHref(doc.topic.id, doc.chapter.id, section.id), "https://docs.example.test").hash, "#" + section.anchor);
    }
  }
});

test("search finds instructions inside prerequisites, tables, and verification sections", () => {
  assert.ok(searchManual("私网 精确授权").some(doc => doc.chapter.id === "routes"));
  assert.ok(searchManual("商家映射 固定端口").some(doc => doc.chapter.id === "routes"));
  assert.ok(searchManual("模板 代理组").some(doc => doc.chapter.id === "templates"));
  assert.equal(searchManual("不存在的文档关键词 xyz-unknown").length, 0);
});
