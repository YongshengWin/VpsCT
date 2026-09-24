import * as React from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, RefreshCw, Trash2 } from "lucide-react";
import { ApiError, del, get, post } from "@/lib/api";
import { useAuth } from "@/lib/auth";
import { fmtDate } from "@/lib/utils";
import type { AgentUpdateInfo } from "@/lib/types";
import { Button, Card, Dialog, Field, Input, Pre, Select } from "@/components/ui";

interface Job {
  id: string;
  role: "controller" | "agent";
  action: "update" | "uninstall";
  version?: string;
  purge?: boolean;
  delete_server?: boolean;
  status: string;
  message: string;
  updated_at: string;
}
interface Status {
  available: boolean;
  reason?: string;
  version: string;
  target_version?: string;
  agent_update?: AgentUpdateInfo;
  jobs: Job[];
}
interface Release { version: string; url: string }
const active = (j: Job) => j.status === "queued" || j.status === "running";
const labels: Record<string, string> = { queued: "等待接收", running: "执行中", succeeded: "已完成", failed: "失败", rolled_back: "已回退", interrupted: "结果待核实", expired: "已过期", cancelled: "已取消" };
function newerRelease(target: string, current: string) {
  const parts = (value: string) => /^v(\d+)\.(\d+)\.(\d+)/.exec(value)?.slice(1).map(Number);
  const a = parts(target), b = parts(current);
  if (!a || !b) return target !== current;
  for (let i = 0; i < 3; i++) { if (a[i] !== b[i]) return a[i] > b[i]; }
  return current.includes("-") && !target.includes("-");
}

export function MaintenancePanel({ server, open = false, onOpen, onClose, onBusyChange, deleteOpen = false, onDeleteClose, onDeleted }: {
  deleteOpen?: boolean;
  onDeleteClose?: () => void;
  onDeleted?: () => void;
  server?: { id: number; name: string };
  open?: boolean;
  onOpen?: () => void;
  onClose?: () => void;
  onBusyChange?: (busy: boolean) => void;
}) {
  const controller = !server;
  const role = controller ? "controller" : "agent";
  const endpoint = controller ? "/api/v1/system/maintenance" : `/api/v1/servers/${server.id}/maintenance`;
  const key = ["maintenance", endpoint];
  const qc = useQueryClient();
  const { user } = useAuth();
  const q = useQuery({ queryKey: key, queryFn: () => get<Status>(endpoint), refetchInterval: 3000, retry: false });
  const [selected, setSelected] = React.useState<"update" | "uninstall" | null>(null);
  const [deleteMode, setDeleteMode] = React.useState(true);
  const [deleteAfter, setDeleteAfter] = React.useState(false);
  const [password, setPassword] = React.useState("");
  const [code, setCode] = React.useState("");
  const [confirm, setConfirm] = React.useState("");
  const [purge, setPurge] = React.useState(false);
  const [removeCaddy, setRemoveCaddy] = React.useState(false);
  const [requestID, setRequestID] = React.useState("");
  const [submitted, setSubmitted] = React.useState<Job | null>(null);
  const [error, setError] = React.useState("");
  const latest = useMutation({ mutationFn: () => get<Release>(endpoint + "/latest") });
  const target = controller ? latest.data?.version : q.data?.target_version;
  const update = q.data?.agent_update;
  const versionKnown = !!update?.current_sha && !!update?.latest_sha;
  const upgradeAvailable = !!target && (controller ? newerRelease(target, q.data?.version ?? "") : !!update?.outdated);
  const reinstall = !!target && (controller ? target === q.data?.version : versionKnown && !update?.outdated);
  const canUpdate = upgradeAvailable || reinstall;
  const updateLabel = controller ? (reinstall ? "重新安装当前版本" : "升级控制端") : (reinstall ? "重新安装 agent" : "升级 agent");
  const expected = controller ? "VpsCT" : server.name;
  const jobs = q.data?.jobs?.length ? q.data.jobs : (submitted ? [submitted] : []);
  const busy = jobs.some(active);
  React.useEffect(() => { onBusyChange?.(busy); }, [busy, onBusyChange]);
  const deleted = q.error instanceof ApiError && q.error.status === 404 && jobs.some(j => j.delete_server);
  React.useEffect(() => { if (deleted) onDeleted?.(); }, [deleted, onDeleted]);
  React.useEffect(() => { if (deleteOpen) setDeleteMode(true); }, [deleteOpen]);
  const remove = useMutation({
    mutationFn: () => del(`/api/v1/servers/${server!.id}`),
    onSuccess: () => { onDeleteClose?.(); onDeleted?.(); },
  });
  const disconnected = !!q.error || (controller && !!submitted && !q.data?.available);
  const lastKnown = jobs[0] ?? submitted;
  // A terminal update failure remains in history after a manual recovery,
  // but matching binary hashes confirm that it is no longer actionable.
  const agentUpdateRecovered = !controller && !q.error && q.data?.available && versionKnown && update?.current_sha === update?.latest_sha && lastKnown?.action === "update";
  const attention = jobs.find(active) ?? (lastKnown && !agentUpdateRecovered && ["failed", "rolled_back", "interrupted", "expired"].includes(lastKnown.status) ? lastKnown : null);
  const start = useMutation({
    mutationFn: () => post<Job>(endpoint, {
      id: requestID, role, action: selected, version: selected === "update" ? target : undefined,
      purge: selected === "uninstall" && purge, remove_caddy: selected === "uninstall" && removeCaddy,
      password, code, confirm, delete_server: selected === "uninstall" && deleteAfter,
    }),
    onSuccess: (job) => {
      setSubmitted(job); setSelected(null); setPassword(""); setCode(""); setConfirm(""); setError("");
      qc.setQueryData<Status>(key, (old) => ({ ...old, available: old?.available ?? true, version: old?.version ?? "", jobs: [job, ...(old?.jobs ?? []).filter((j) => j.id !== job.id)] }));
      void qc.invalidateQueries({ queryKey: key });
    },
    onError: (e) => setError(e instanceof Error ? e.message : "提交失败，请检查任务记录后重试"),
  });
  function choose(action: "update" | "uninstall", removeServer = false) {
    setDeleteAfter(removeServer);
    setSelected(action); setPassword(""); setCode(""); setConfirm(""); setError(""); setPurge(false); setRemoveCaddy(false);
    setRequestID(crypto.randomUUID().replaceAll("-", ""));
  }
  function close() { if (!start.isPending) { setSelected(null); setPassword(""); setCode(""); } }

  const content = <>
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          {controller && <h3 className="font-bold">控制端维护</h3>}
          <p className="mt-1 text-sm text-muted-foreground">当前版本 {q.data?.version || "—"}{!controller && target ? ` · 控制端提供 ${target}` : ""}</p>
          <p className="mt-1 text-xs text-muted-foreground">{controller ? "升级前自动备份，启动失败自动恢复。" : "升级同步控制端提供的程序；卸载会停止这台服务器上由 VpsCT 部署的服务。"}</p>
        </div>
        <div className="flex flex-wrap gap-2">
          {controller && <Button size="sm" variant="outline" disabled={!q.data?.available || busy} loading={latest.isPending} onClick={() => latest.mutate()}><RefreshCw className="h-4 w-4" /> 检查新版本</Button>}
          <Button size="sm" variant="outline" disabled={!q.data?.available || !canUpdate || busy || latest.isPending} onClick={() => choose("update")}>{controller ? updateLabel : busy ? "维护进行中" : canUpdate ? updateLabel : "等待版本信息"}</Button>
          <Button size="sm" variant="ghost" className="text-destructive" disabled={!q.data?.available || busy} onClick={() => choose("uninstall")}><Trash2 className="h-4 w-4" /> {controller ? "卸载控制端" : "卸载 agent"}</Button>
        </div>
      </div>
      {!q.data?.available && !q.isLoading && <p className="mt-3 text-sm text-amber-600 dark:text-amber-400">{q.data?.reason || "暂时无法读取维护状态"}</p>}
      {latest.error && <p role="alert" className="mt-3 text-sm text-destructive">{latest.error.message}</p>}
      {controller && latest.data && <p className="mt-3 text-sm">最新正式版 <a className="text-primary underline" href={latest.data.url} target="_blank" rel="noreferrer">{latest.data.version}</a>{upgradeAvailable ? "；升级会短暂停止面板。" : reinstall ? "，与当前版本相同，可重新下载安装；操作会短暂停止面板。" : "，没有比当前版本更新的正式发行版。"}</p>}
      {disconnected && lastKnown && <div role="status" className="mt-4 rounded-xl bg-amber-500/10 p-3 text-sm leading-6">
        <AlertTriangle className="mr-1 inline h-4 w-4" />连接已中断，暂时无法确认最终结果。独立维护进程会继续执行。
        {lastKnown.action === "uninstall" && controller ? "控制端卸载后，此站点将不可用。" : "服务恢复后会自动刷新进度。"}
      </div>}
      {!!jobs.length && <details className="mt-4 border-t pt-4" open={controller || !!attention || undefined}>
        <summary className="cursor-pointer text-sm font-medium">操作记录</summary>
        <div className="mt-3 space-y-3" aria-live="polite">
        {jobs.slice(0, 3).map((j) => <div key={j.id} className="text-sm">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <span className="font-medium">{j.action === "update" ? `升级 ${j.version || ""}` : `卸载${j.delete_server ? "并删除记录" : ""} · ${j.purge ? "清空数据" : "保留数据"}`} <span className={j.status === "succeeded" ? "text-emerald-600" : ["failed", "interrupted"].includes(j.status) ? "text-destructive" : "text-muted-foreground"}>· {labels[j.status] ?? j.status}</span></span>
            <time className="text-xs text-muted-foreground">{fmtDate(j.updated_at)}</time>
          </div>
          <p className="mt-1 text-muted-foreground">{j.message}</p>
          <details className="mt-1 text-xs text-muted-foreground"><summary className="cursor-pointer">终端查看结果与日志</summary><pre className="mt-2 overflow-x-auto rounded-lg bg-muted p-3">{`sudo cat /var/lib/ctlvps-maintenance/${j.id}/status.json\nsudo cat /var/lib/ctlvps-maintenance/${j.id}/worker.log`}</pre></details>
        </div>)}
        </div>
      </details>}
  </>;

  return <>
    {controller ? <Card className="my-4 p-5 sm:p-6">{content}</Card> : <>
      {attention && <div role="status" className={`mb-4 flex flex-wrap items-center justify-between gap-x-4 gap-y-1 rounded-xl px-4 py-2 text-sm ${active(attention) ? "bg-sky-500/10 text-sky-700 dark:text-sky-300" : "bg-amber-500/10 text-amber-700 dark:text-amber-300"}`}>
        <span>{active(attention) ? <RefreshCw className="mr-2 inline h-4 w-4 animate-spin" /> : <AlertTriangle className="mr-2 inline h-4 w-4" />}agent {attention.action === "update" ? "升级" : "卸载"} · {q.error ? "连接中断，结果待确认" : labels[attention.status] ?? attention.status}</span>
        <Button variant="link" size="sm" onClick={onOpen}>查看详情</Button>
      </div>}
      <Dialog open={open && !selected} onClose={() => onClose?.()} title="agent 维护" description={server.name}>{content}</Dialog>
    </>}
    <Dialog open={deleteOpen} onClose={() => { if (!remove.isPending) onDeleteClose?.(); }} title="删除服务器？" footer={<>
      <Button variant="ghost" disabled={remove.isPending} onClick={onDeleteClose}>取消</Button>
      <Button variant="destructive" loading={remove.isPending} disabled={busy || (deleteMode && !q.data?.available)} onClick={() => {
        if (deleteMode) { onDeleteClose?.(); choose("uninstall", true); } else remove.mutate();
      }}>{deleteMode ? "继续验证并卸载" : "删除记录"}</Button>
    </>}>
      <div className="space-y-4 text-sm leading-6">
        <p>将删除该服务器、关联节点及依赖这些节点的链式节点记录。</p>
        <label className="flex items-start gap-2"><input type="checkbox" className="mt-1" checked={deleteMode} onChange={e => setDeleteMode(e.target.checked)} />同时卸载这台服务器上的 agent 和由 VpsCT 部署的服务</label>
        <p className="text-muted-foreground">{deleteMode ? "先卸载，成功后自动删除面板记录。配置和数据默认保留；离线、失败或结果未确认时保留记录。关闭页面不影响任务执行。" : "仅删除面板记录，不卸载 VPS 上的程序，已有节点服务可能继续运行。重新添加后需要重新注册 agent。"}</p>
        {!deleteMode && <details className="rounded-xl border p-3">
          <summary className="cursor-pointer font-medium">删除后如何清理 VPS 上的程序？</summary>
          <p className="mt-2 text-muted-foreground">请先保存命令。SSH 登录目标 VPS 后执行，不需要安装控制端，也不需要面板记录或注册令牌。</p>
          <p className="mt-2">新版安装器会保留独立卸载脚本，先预览：</p>
          <Pre>{"sudo bash /usr/local/libexec/ctlvps-agent-uninstall.sh --agent --purge --dry-run"}</Pre>
          <p className="mt-2">确认范围后去掉 --dry-run，按终端提示确认。--purge 会清空 Agent 配置和数据；要保留数据请去掉该参数。同机控制端不受影响。</p>
          <p className="mt-2">旧版没有该文件时，从官方发行附件下载，再预览：</p>
          <Pre>{`vpsct_uninstaller="$(mktemp)" &&
curl -fLsS --proto '=https' --proto-redir '=https' --max-time 120 https://github.com/YongshengWin/VpsCT/releases/latest/download/uninstall.sh -o "$vpsct_uninstaller" &&
sudo bash "$vpsct_uninstaller" --agent --purge --dry-run`}</Pre>
          <p className="mt-2">预览无误后，在同一终端执行：</p>
          <Pre>{'sudo bash "$vpsct_uninstaller" --agent --purge'}</Pre>
        </details>}
        {deleteMode && !q.data?.available && <p role="alert" className="text-amber-600">{q.isLoading ? "正在检查 agent 状态…" : q.data?.reason || "无法读取维护状态，请稍后重试"}</p>}
        {remove.error && <p role="alert" className="text-destructive">{remove.error.message}</p>}
      </div>
    </Dialog>
    <Dialog open={!!selected} onClose={close} title={selected === "update" ? updateLabel : `卸载${controller ? "控制端" : " agent"}${deleteAfter ? "并删除记录" : ""}`} description={controller ? "目标：当前 VpsCT 控制端" : `目标：${server.name}`} footer={<>
      <Button variant="ghost" disabled={start.isPending} onClick={close}>取消</Button>
      <Button variant={selected === "uninstall" ? "destructive" : "default"} loading={start.isPending} disabled={!password || (user?.totp_enabled && !code) || (selected === "uninstall" && confirm !== expected)} onClick={() => start.mutate()}>{selected === "update" ? (reinstall ? "确认重新安装" : "确认升级") : deleteAfter ? "确认卸载并删除记录" : "确认卸载"}</Button>
    </>}>
      <div className="space-y-4">
        {selected === "update" ? <p className="text-sm leading-6">{controller ? `${reinstall ? "重新安装" : "升级至"} ${target}。${reinstall ? "重新下载并校验官方发行包，同步更新控制端及 agent 分发文件。" : ""}将停服备份数据，再更换程序并检查启动情况；启动失败会恢复旧程序和升级前数据。${reinstall ? "若当前程序无法通过恢复校验，会在停服前终止，不会跳过安全检查。" : ""}` : `${reinstall ? "重新安装" : "同步"}控制端提供的 ${target}。${reinstall ? "即使当前程序已同步，也会重新下载、校验并替换。" : ""}agent 会短暂重启；新程序启动失败时恢复旧程序。`}</p> : <>
          <p className="text-sm leading-6">{controller ? "将停止并移除控制端及维护服务。完成后此网站不可用，已接入的远端 agent 继续保留。" : deleteAfter ? "将卸载 agent 及它在这台服务器上部署的服务，相关资源分享也会停止。收到卸载成功结果后，自动删除服务器及关联节点记录；失败时保留记录。" : "将卸载 agent 及它在这台服务器上部署的服务，相关资源分享也会停止；面板中的服务器记录保留。"}</p>
          <Field label="数据处理"><Select value={purge ? "purge" : "keep"} onChange={(e) => { setPurge(e.target.value === "purge"); if (e.target.value !== "purge") setRemoveCaddy(false); }}>
            <option value="keep">保留配置和数据（默认）</option><option value="purge">同时清空配置、凭据、日志和备份</option>
          </Select></Field>
          {controller && purge && <label className="flex items-start gap-2 text-sm leading-6"><input type="checkbox" className="mt-1" checked={removeCaddy} onChange={(e) => setRemoveCaddy(e.target.checked)} />同时清理安装器创建的独占 HTTPS 站点和证书（共享配置会拒绝执行）</label>}
          {purge && <p role="alert" className="text-sm text-destructive">所选端的数据将永久删除，请先保存需要的备份。</p>}
          <Field label={`输入「${expected}」确认目标`}><Input aria-label="确认目标名称" autoComplete="off" value={confirm} onChange={(e) => setConfirm(e.target.value)} /></Field>
        </>}
        <Field label="管理员密码"><Input aria-label="管理员密码" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} /></Field>
        {user?.totp_enabled && <Field label="两步验证码或恢复码"><Input aria-label="两步验证码或恢复码" autoComplete="one-time-code" value={code} onChange={(e) => setCode(e.target.value)} /></Field>}
        {error && <p role="alert" className="text-sm text-destructive">{error}</p>}
      </div>
    </Dialog>
  </>;
}
