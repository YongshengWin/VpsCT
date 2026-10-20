import * as React from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Copy, Download, ExternalLink, KeyRound, Plus, Trash2 } from "lucide-react";
import { del, get, post } from "@/lib/api";
import type { User } from "@/lib/types";
import { fmtDate } from "@/lib/utils";
import { skillInstallCommand, type SkillClient } from "@/lib/agent-install";
import { Badge, Button, Card, Dialog, Empty, Field, Input, Select, Spinner } from "@/components/ui";
import { useToast } from "@/components/toast";

interface DeveloperAPIKey {
  id: number;
  name: string;
  token_hint: string;
  scopes: ("read" | "write")[];
  created_at: string;
  expires_at?: string | null;
  last_used_at?: string | null;
  revoked_at?: string | null;
}

const keyQuery = ["api-keys"];

function keyStatus(key: DeveloperAPIKey) {
  if (key.revoked_at) return "已撤销";
  if (key.expires_at && new Date(key.expires_at).getTime() <= Date.now()) return "已过期";
  return "可使用";
}

export function APIKeysCard({ user }: { user: User }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [creating, setCreating] = React.useState(false);
  const [revoking, setRevoking] = React.useState<DeveloperAPIKey | null>(null);
  const keys = useQuery({ queryKey: keyQuery, queryFn: () => get<{ list: DeveloperAPIKey[] }>("/api/v1/api-keys") });
  const revoke = useMutation({
    mutationFn: (id: number) => del(`/api/v1/api-keys/${id}`),
    onSuccess: () => {
      setRevoking(null);
      void qc.invalidateQueries({ queryKey: keyQuery });
      toast.success("API key 已撤销");
    },
    onError: (e) => toast.fromError(e),
  });
  return (
    <Card className="p-5 sm:p-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <div className="flex items-center gap-2"><KeyRound className="h-4 w-4 text-muted-foreground" /><h2 className="font-semibold">开发者 API key</h2></div>
          <p className="mt-1 text-sm text-muted-foreground">让 AI agent 和脚本调用 VpsCT。每个 key 都受当前账户的角色和资源权限约束。</p>
          <p className="mt-1 text-xs text-muted-foreground">只读接口也可能返回节点凭据和订阅链接，请只把 key 交给可信任的 agent。</p>
        </div>
        <Button size="sm" onClick={() => setCreating(true)}><Plus className="h-4 w-4" /> 创建 API key</Button>
      </div>
      <div className="mt-5">
        {keys.isPending ? <Spinner /> : keys.isError ? (
          <div role="alert" className="flex flex-wrap items-center gap-3 text-sm"><p className="text-destructive">API key 加载失败：{keys.error.message}</p><Button variant="outline" size="sm" onClick={() => void keys.refetch()}>重试</Button></div>
        ) : !keys.data.list.length ? <Empty title="还没有 API key" description="为每个 agent 单独创建一个 key，便于查看使用情况和撤销。" /> : (
          <div className="space-y-3">
            {keys.data.list.map((key) => {
              const status = keyStatus(key);
              return (
                <div key={key.id} className="rounded-xl border p-4">
                  <div className="flex flex-wrap items-start justify-between gap-3">
                    <div className="min-w-0">
                      <div className="flex flex-wrap items-center gap-2"><span className="break-all font-medium">{key.name}</span><Badge variant={key.scopes.includes("write") ? "secondary" : "outline"}>{key.scopes.includes("write") ? "读写" : "只读"}</Badge><Badge variant={status === "可使用" ? "success" : "outline"}>{status}</Badge></div>
                      <p className="mono mt-1 break-all text-xs text-muted-foreground">{key.token_hint}</p>
                    </div>
                    {!key.revoked_at && <Button variant="ghost" size="sm" className="text-destructive" onClick={() => setRevoking(key)}><Trash2 className="h-4 w-4" /> 撤销</Button>}
                  </div>
                  <dl className="mt-3 grid gap-2 text-xs text-muted-foreground sm:grid-cols-3">
                    <div><dt className="mb-1">创建时间</dt><dd>{fmtDate(key.created_at)}</dd></div>
                    <div><dt className="mb-1">到期时间</dt><dd>{key.expires_at ? fmtDate(key.expires_at) : "永不过期"}</dd></div>
                    <div><dt className="mb-1">最近使用</dt><dd>{key.last_used_at ? fmtDate(key.last_used_at) : "尚未使用"}</dd></div>
                  </dl>
                </div>
              );
            })}
          </div>
        )}
      </div>
      {creating && <CreateAPIKeyDialog user={user} onClose={() => setCreating(false)} onCreated={() => { void qc.invalidateQueries({ queryKey: keyQuery }); }} />}
      {revoking && <Dialog open title={`撤销 API key · ${revoking.name}`} description="撤销后，使用这个 key 的 agent 和脚本将立即失去访问权限。" onClose={() => { if (!revoke.isPending) setRevoking(null); }} footer={<><Button variant="outline" disabled={revoke.isPending} onClick={() => setRevoking(null)}>取消</Button><Button variant="destructive" loading={revoke.isPending} onClick={() => revoke.mutate(revoking.id)}>撤销</Button></>}><p className="mono break-all text-sm text-muted-foreground">{revoking.token_hint}</p></Dialog>}
    </Card>
  );
}

export function AgentConnectionCard() {
  const toast = useToast();
  const [copied, setCopied] = React.useState(false);
  const [installCopied, setInstallCopied] = React.useState(false);
  const [client, setClient] = React.useState<SkillClient>("codex");
  const baseURL = `${window.location.origin}/api/v1`;
  const installCommand = skillInstallCommand(window.location.origin, client);
  async function copyBaseURL() {
    try {
      await navigator.clipboard.writeText(baseURL);
      setCopied(true);
      toast.success("API 地址已复制");
    } catch (e) {
      toast.fromError(e);
    }
  }
  async function copyInstallCommand() {
    if (!installCommand) return;
    try {
      await navigator.clipboard.writeText(installCommand);
      setInstallCopied(true);
      toast.success("Skill 安装命令已复制");
    } catch (e) {
      toast.fromError(e);
    }
  }
  return (
    <Card className="p-5 sm:p-6">
      <h2 className="font-semibold">接入 AI agent</h2>
      <p className="mt-1 text-sm leading-6 text-muted-foreground">在运行 agent 的电脑上执行安装命令，安装与当前面板版本一致的 Skill。需要 macOS / Linux、curl 和 Python 3.9 或更新版本。</p>
      <div className="mt-4 rounded-xl border bg-muted/30 p-4">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <label className="flex items-center gap-2 text-sm">安装到<Select className="w-36" aria-label="Skill 安装目标" value={client} onChange={(e) => { setClient(e.target.value as SkillClient); setInstallCopied(false); }}><option value="codex">Codex</option><option value="claude">Claude Code</option></Select></label>
          <Button variant="outline" size="sm" disabled={!installCommand} onClick={() => void copyInstallCommand()}>{installCopied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}复制安装命令</Button>
        </div>
        {installCommand ? <pre className="mono mt-3 whitespace-pre-wrap break-all text-xs leading-6"><code>{installCommand}</code></pre> : <p className="mt-3 text-sm text-muted-foreground">请通过 HTTPS 面板地址生成网络安装命令，也可下载 ZIP 手动安装。</p>}
        <p className="mt-2 text-xs leading-5 text-muted-foreground">默认安装到 {client === "codex" ? "$CODEX_HOME/skills（未设置时为 ~/.codex/skills）" : "~/.claude/skills"}。其他 agent 可在命令末尾加 <code>--dir '/你的/skills/目录'</code>；更新已有 Skill 时加 <code>--update</code>。安装完成后重新打开 agent 会话。</p>
      </div>
      <p className="mt-4 text-sm leading-6 text-muted-foreground">将 key 放在 agent 的凭据或 <code>VPSCT_API_KEY</code> 环境变量中，将 <code className="break-all">{window.location.origin}</code> 设为 <code>VPSCT_URL</code>。每次请求使用下列认证头。</p>
      <div className="mt-3 flex flex-wrap items-center gap-2"><span className="text-xs text-muted-foreground">API 地址</span><code className="break-all text-xs">{baseURL}</code><Button variant="ghost" size="sm" onClick={() => void copyBaseURL()} aria-label="复制 API 地址">{copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}</Button></div>
      <code className="mt-2 block break-all text-xs">Authorization: Bearer &lt;API_KEY&gt;</code>
      <div className="mt-4 flex flex-wrap gap-x-5 gap-y-2 text-sm">
        <a className="inline-flex items-center gap-1.5 text-primary hover:underline" href="/api/v1/agent/skill.zip" download="vpsct-skill.zip"><Download className="h-4 w-4" /> 下载 Skill</a>
        <a className="inline-flex items-center gap-1.5 text-primary hover:underline" href="/api/v1/agent/capabilities" target="_blank" rel="noreferrer"><ExternalLink className="h-4 w-4" /> 能力清单</a>
        <a className="inline-flex items-center gap-1.5 text-primary hover:underline" href="/api/v1/agent/openapi.json" target="_blank" rel="noreferrer"><ExternalLink className="h-4 w-4" /> OpenAPI</a>
      </div>
      <details className="mt-3 text-xs text-muted-foreground">
        <summary className="cursor-pointer">单独下载 Skill 文件</summary>
        <p className="mt-2 leading-6">在同一个 Skill 目录中，分别保存为 <a className="text-primary hover:underline" href="/api/v1/agent/skill" download="SKILL.md">SKILL.md</a>、<a className="text-primary hover:underline" href="/api/v1/agent/client.py" download="vpsct.py">scripts/vpsct.py</a> 和 <a className="text-primary hover:underline" href="/api/v1/agent/reference" download="api.md">references/api.md</a>。</p>
      </details>
    </Card>
  );
}

function CreateAPIKeyDialog({ user, onClose, onCreated }: { user: User; onClose: () => void; onCreated: () => void }) {
  const toast = useToast();
  const [name, setName] = React.useState("");
  const [permission, setPermission] = React.useState("read");
  const [days, setDays] = React.useState("30");
  const [password, setPassword] = React.useState("");
  const [code, setCode] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [key, setKey] = React.useState("");
  const [copied, setCopied] = React.useState(false);
  const close = () => {
    if (busy) return;
    setKey("");
    setPassword("");
    setCode("");
    onClose();
  };
  async function create(e: React.FormEvent) {
    e.preventDefault();
    if (busy || key) return;
    setBusy(true);
    try {
      // Keep the one-time secret out of React Query's query and mutation caches.
      const result = await post<{ api_key: DeveloperAPIKey; key: string }>("/api/v1/api-keys", {
        name: name.trim(),
        scopes: permission === "write" ? ["read", "write"] : ["read"],
        ...(days !== "never" ? { expires_at: new Date(Date.now() + Number(days) * 86400000).toISOString() } : {}),
        password,
        ...(user.totp_enabled ? { code: code.trim() } : {}),
      });
      setKey(result.key);
      setPassword("");
      setCode("");
      onCreated();
    } catch (e) {
      toast.fromError(e);
    } finally {
      setBusy(false);
    }
  }
  async function copyKey() {
    try {
      await navigator.clipboard.writeText(key);
      setCopied(true);
      toast.success("API key 已复制");
    } catch (e) {
      toast.fromError(e);
    }
  }
  return (
    <Dialog open title={key ? "API key 已创建" : "创建 API key"} description={key ? "完整 key 只显示这一次，关闭后无法再次查看。" : "用当前账户密码确认。建议每个 agent 使用独立的只读 key。"} onClose={close} footer={key ? <Button onClick={close}>我已保存，关闭</Button> : <><Button variant="outline" disabled={busy} onClick={close}>取消</Button><Button type="submit" form="create-api-key" loading={busy}>创建</Button></>}>
      {key ? <div className="space-y-4"><Field label="完整 API key" hint="请保存到凭据管理器或环境变量，避免写入对话、代码或日志。"><Input aria-label="完整 API key" className="mono" value={key} readOnly onFocus={(e) => e.target.select()} autoComplete="off" spellCheck={false} /></Field><Button variant="outline" onClick={() => void copyKey()}>{copied ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />}{copied ? "已复制" : "复制 API key"}</Button></div> : (
        <form id="create-api-key" className="space-y-4" onSubmit={(e) => void create(e)}>
          <Field label="名称" hint="用于识别调用方，例如：本地运维 agent"><Input autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="本地运维 agent" maxLength={80} required disabled={busy} /></Field>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="权限" hint="读写 key 也受当前账户权限约束。"><Select aria-label="API key 权限" value={permission} onChange={(e) => setPermission(e.target.value)} disabled={busy}><option value="read">只读</option><option value="write">读写</option></Select></Field>
            <Field label="有效期"><Select aria-label="API key 有效期" value={days} onChange={(e) => setDays(e.target.value)} disabled={busy}><option value="7">7 天</option><option value="30">30 天</option><option value="90">90 天</option><option value="365">365 天</option><option value="never">永不过期</option></Select></Field>
          </div>
          <Field label="当前密码"><Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" required disabled={busy} /></Field>
          {user.totp_enabled && <Field label="验证码或恢复码"><Input value={code} onChange={(e) => setCode(e.target.value)} placeholder="123456 或 xxxx-xxxx" className="mono" autoComplete="one-time-code" required disabled={busy} /></Field>}
        </form>
      )}
    </Dialog>
  );
}
