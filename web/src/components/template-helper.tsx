import * as React from "react";
import { Copy } from "lucide-react";
import { buildTemplatePrompt, TEMPLATE_FORMATS, TEMPLATE_GUIDES, type TemplateKind, type TemplateTask, type SurgeOutput } from "@/lib/template-prompt";
import { copyText } from "@/lib/utils";
import { Button, Card, Select, Tabs, Textarea } from "@/components/ui";
import { useToast } from "@/components/toast";

export function TemplateFormatHelp({ kind }: { kind: TemplateKind }) {
  const guide = TEMPLATE_GUIDES[kind];
  return (
    <details className="rounded-xl border px-3 py-2 text-sm">
      <summary className="cursor-pointer font-medium">字段、占位符与生效规则</summary>
      <div className="mt-3 space-y-3 text-xs leading-6 text-muted-foreground">
        <div><p className="font-medium text-foreground">各部分负责什么</p><p>{guide.sections}</p></div>
        <div><p className="font-medium text-foreground">面板会保留或替换什么</p><p>{guide.behavior}</p></div>
        <div><p className="font-medium text-foreground">可用占位符</p><dl className="mt-1 space-y-2">{guide.markers.map(([marker, meaning]) => <div key={marker}><dt className="break-all font-mono text-foreground">{marker}</dt><dd>{meaning}</dd></div>)}</dl></div>
        <div><p className="font-medium text-foreground">格式限制</p><p>{guide.constraints}</p></div>
      </div>
    </details>
  );
}

export function TemplatePromptCard() {
  const toast = useToast();
  const [kind, setKind] = React.useState<TemplateKind>("mihomo");
  const [task, setTask] = React.useState<TemplateTask>("adapt");
  const [surgeOutput, setSurgeOutput] = React.useState<SurgeOutput>("full");
  const label = TEMPLATE_FORMATS.find((f) => f.value === kind)!.label;
  const action = task === "adapt" ? "改造" : "编写";
  const prompt = buildTemplatePrompt({ kind, task, surgeOutput });
  return (
    <Card className="mb-4 space-y-3 p-4">
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <h2 className="text-base font-semibold">让 AI 帮你写模板</h2>
        <p className="text-sm leading-6 text-muted-foreground">{task === "adapt" ? "复制指令和原配置给 AI，生成后粘贴到「新建模板」。" : "复制指令给 AI 并说明需求，生成后粘贴到「新建模板」。"}</p>
      </div>
      <div className="flex flex-wrap items-center gap-3">
        <div role="group" aria-label="目标客户端" className="min-w-0 max-w-full">
          <Tabs value={kind} onChange={setKind} items={[...TEMPLATE_FORMATS]} />
        </div>
        <div role="group" aria-label="模板任务" className="flex max-w-full flex-wrap items-center gap-3">
          <Tabs value={task} onChange={setTask} items={[{ value: "adapt", label: "改造我的配置" }, { value: "create", label: "从零写模板" }]} />
          {kind === "surge" && <Select aria-label="Surge 使用方式" className="w-full sm:w-64" value={surgeOutput} onChange={(e) => setSurgeOutput(e.target.value as SurgeOutput)}><option value="full">完整配置（含分组与分流）</option><option value="proxies">仅节点片段（供主配置引用）</option></Select>}
          <Button onClick={async () => { try { await copyText(prompt); toast.success(`已复制 ${label} ${action}指令，粘贴给 AI 即可`); } catch (e) { toast.fromError(e); } }}><Copy className="h-4 w-4" /> 复制给 AI 的指令</Button>
        </div>
      </div>
      <details className="border-t pt-3">
        <summary className="cursor-pointer text-sm font-medium">指令预览</summary>
        <Textarea key={`${kind}:${task}:${surgeOutput}`} aria-label="指令预览正文" readOnly value={prompt} rows={12} className="mono mt-3 max-h-96 resize-y text-xs leading-6" spellCheck={false} />
      </details>
    </Card>
  );
}
