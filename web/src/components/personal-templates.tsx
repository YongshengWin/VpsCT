import * as React from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Copy, Pencil, Plus, Trash2, History } from "lucide-react";
import { del, get, post, put } from "@/lib/api";
import type { RuleTemplate } from "@/lib/types";
import { Badge, Button, Card, CardContent, CardHeader, CardTitle, Confirm, Dialog, Select, Spinner } from "@/components/ui";
import { useToast } from "@/components/toast";
import { TemplateDialog, TemplateHistoryDialog } from "@/pages/templates";

const formats = [
  ["mihomo", "Clash / Mihomo"],
  ["surge", "Surge"],
  ["shadowrocket", "Shadowrocket"],
  ["singbox", "sing-box"],
] as const;

export function PersonalTemplates({ shareID }: { shareID: number }) {
  const qc = useQueryClient();
  const toast = useToast();
  const base = `/api/v1/shares/${shareID}/personal-templates`;
  const templates = useQuery({ queryKey: ["personal-templates"], queryFn: () => get<RuleTemplate[]>("/api/v1/me/templates") });
  const defaults = useQuery({ queryKey: ["templates"], queryFn: () => get<RuleTemplate[]>("/api/v1/templates") });
  const selections = useQuery({ queryKey: ["personal-template-selections", shareID], queryFn: () => get<Record<string, number>>(base) });
  const [edit, setEdit] = React.useState<RuleTemplate | null | "new">(null);
  const [remove, setRemove] = React.useState<RuleTemplate | null>(null);
  const [history, setHistory] = React.useState<RuleTemplate | null>(null);
  const [choices, setChoices] = React.useState<Record<string, string>>({});
  const [preview, setPreview] = React.useState<{ format: string; body: string } | null>(null);
  React.useEffect(() => {
    if (selections.data) setChoices(Object.fromEntries(formats.map(([f]) => [f, String(selections.data[f] ?? "")] )));
  }, [selections.data]);
  const refresh = () => { qc.invalidateQueries({ queryKey: ["personal-templates"] }); qc.invalidateQueries({ queryKey: ["personal-template-selections"] }); };
  const apply = useMutation({
    mutationFn: ({ format, id }: { format: string; id: string }) => id ? put(`${base}/${format}`, { template_id: Number(id) }) : del(`${base}/${format}`),
    onSuccess: () => { toast.success("已保存，客户端下次更新订阅时生效"); refresh(); },
    onError: (e) => toast.fromError(e),
  });
  const previewM = useMutation({
    mutationFn: ({ format, id }: { format: string; id: string }) => post<{ format: string; body: string }>(`${base}/preview`, { format, template_id: Number(id) }),
    onSuccess: setPreview,
    onError: (e) => toast.fromError(e),
  });
  const removeM = useMutation({
    mutationFn: (id: number) => del(`/api/v1/me/templates/${id}`),
    onSuccess: () => { setRemove(null); refresh(); toast.success("已删除，使用它的分享恢复默认模板"); },
    onError: (e) => toast.fromError(e),
  });
  const duplicate = (t: RuleTemplate) => setEdit({ ...t, id: 0, name: `${t.name} 副本`, is_builtin: false });
  return (
    <Card className="mt-4">
      <CardHeader><CardTitle>我的客户端模板</CardTitle></CardHeader>
      <CardContent className="space-y-5">
        <p className="text-sm text-muted-foreground">为每种客户端选择配置模板。更改后请在客户端更新订阅；通用节点链接不使用模板。</p>
        {selections.isLoading || templates.isLoading ? <Spinner /> : selections.isError || templates.isError ? <p className="text-sm text-destructive">模板加载失败，请刷新后重试。</p> : (
          <div className="grid gap-3">
            {formats.map(([format, label]) => {
              const choice = choices[format] ?? "";
              return <div key={format} className="flex flex-wrap items-center gap-2">
                <span className="w-40 text-sm font-medium">{label}</span>
                <Select aria-label={`${label} 模板`} className="max-w-64" value={choice} onChange={e => setChoices(old => ({ ...old, [format]: e.target.value }))}>
                  <option value="">管理员或系统默认</option>
                  {(templates.data ?? []).filter(t => t.kind === format).map(t => <option key={t.id} value={String(t.id)}>{t.name}</option>)}
                </Select>
                <Button size="sm" variant="outline" disabled={!choice || previewM.isPending} onClick={() => previewM.mutate({ format, id: choice })}>预览</Button>
                <Button size="sm" disabled={apply.isPending || choice === String(selections.data?.[format] ?? "")} onClick={() => apply.mutate({ format, id: choice })}>应用</Button>
              </div>;
            })}
          </div>
        )}
        <div>
          <div className="mb-2 flex items-center justify-between gap-2"><p className="text-sm font-medium">我的模板</p><Button size="sm" variant="outline" onClick={() => setEdit("new")}><Plus className="h-4 w-4" /> 新建</Button></div>
          <p className="mb-2 text-xs text-muted-foreground">正在使用的模板可复制后修改、预览并重新应用，以保留当前可用配置。</p>
          {(templates.data ?? []).length === 0 ? <p className="text-sm text-muted-foreground">还没有个人模板，可从下方默认模板复制。</p> : <div className="space-y-1">
            {templates.data?.map(t => <div key={t.id} className="flex items-center gap-2 text-sm"><span className="min-w-0 flex-1 truncate">{t.name}</span><Badge variant="outline">{t.kind}</Badge><Button size="icon" variant="ghost" title="历史版本" onClick={() => setHistory(t)}><History className="h-4 w-4" /></Button><Button size="icon" variant="ghost" title="复制" onClick={() => duplicate(t)}><Copy className="h-4 w-4" /></Button><Button size="icon" variant="ghost" title="编辑" onClick={() => setEdit(t)}><Pencil className="h-4 w-4" /></Button><Button size="icon" variant="ghost" title="删除" onClick={() => setRemove(t)}><Trash2 className="h-4 w-4" /></Button></div>)}
          </div>}
        </div>
        <details className="text-sm"><summary className="cursor-pointer font-medium">从内置模板复制</summary><div className="mt-2 space-y-1">{defaults.data?.filter(t => t.is_builtin).map(t => <div key={t.id} className="flex items-center gap-2"><span className="min-w-0 flex-1 truncate">{t.name}</span><Badge variant="outline">{t.kind}</Badge><Button size="sm" variant="outline" onClick={() => duplicate(t)}>复制</Button></div>)}</div></details>
      </CardContent>
      <TemplateDialog t={edit} personal shareID={shareID} onClose={() => setEdit(null)} />
      <TemplateHistoryDialog t={history} personal onClose={() => setHistory(null)} onCopy={v => { setHistory(null); setEdit({ ...v, id: 0, name: `${v.name} 历史副本`, is_builtin: false }); }} />
      <Confirm open={!!remove} onClose={() => setRemove(null)} onConfirm={() => remove && removeM.mutate(remove.id)} loading={removeM.isPending} destructive title={`删除模板「${remove?.name}」？`} description="所有使用该模板的分享会恢复管理员或系统默认模板，客户端下次更新订阅时生效。" />
      <Dialog open={!!preview} onClose={() => setPreview(null)} wide title={`${preview?.format ?? ""} 配置预览`} description="预览包含你的节点连接信息，请妥善保管。" footer={<Button onClick={() => setPreview(null)}>关闭</Button>}><pre className="max-h-96 overflow-auto whitespace-pre-wrap break-all text-xs">{preview?.body}</pre></Dialog>
    </Card>
  );
}
