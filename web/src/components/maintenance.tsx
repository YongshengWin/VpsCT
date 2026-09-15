import * as React from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { AlertTriangle, RefreshCw, Trash2 } from "lucide-react";
import { get, post } from "@/lib/api";
import { useAuth } from "@/lib/auth";
import { fmtDate } from "@/lib/utils";
import { Button, Card, Dialog, Field, Input, Select } from "@/components/ui";

interface Job {
  id: string;
  role: "controller" | "agent";
  action: "update" | "uninstall";
  version?: string;
  purge?: boolean;
  status: string;
  message: string;
  updated_at: string;
}
interface Status {
  available: boolean;
  reason?: string;
  version: string;
  target_version?: string;
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

export function MaintenancePanel({ server }: { server?: { id: number; name: string } }) {
  const controller = !server;
  const role = controller ? "controller" : "agent";
  const endpoint = controller ? "/api/v1/system/maintenance" : `/api/v1/servers/${server.id}/maintenance`;
  const key = ["maintenance", endpoint];
  const qc = useQueryClient();
  const { user } = useAuth();
  const q = useQuery({ queryKey: key, queryFn: () => get<Status>(endpoint), refetchInterval: 3000, retry: false });
  const [selected, setSelected] = React.useState<"update" | "uninstall" | null>(null);
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
  const upgradeAvailable = !!target && (!controller || newerRelease(target, q.data?.version ?? ""));
  const expected = controller ? "VpsCT" : server.name;
  const jobs = q.data?.jobs?.length ? q.data.jobs : (submitted ? [submitted] : []);
  const busy = jobs.some(active);
  const disconnected = !!q.error || (controller && !!submitted && !q.data?.available);
  const lastKnown = jobs[0] ?? submitted;
  const start = useMutation({
    mutationFn: () => post<Job>(endpoint, {
      id: requestID, role, action: selected, version: selected === "update" ? target : undefined,
      purge: selected === "uninstall" && purge, remove_caddy: selected === "uninstall" && removeCaddy,
      password, code, confirm,
    }),
    onSuccess: (job) => {
      setSubmitted(job); setSelected(null); setPassword(""); setCode(""); setConfirm(""); setError("");
      qc.setQueryData<Status>(key, (old) => ({ available: old?.available ?? true, version: old?.version ?? "", target_version: old?.target_version, jobs: [job, ...(old?.jobs ?? []).filter((j) => j.id !== job.id)] }));
      void qc.invalidateQueries({ queryKey: key });
    },
    onError: (e) => setError(e instanceof Error ? e.message : "提交失败，请检查任务记录后重试"),
  });
  function choose(action: "update" | "uninstall") {
    setSelected(action); setPassword(""); setCode(""); setConfirm(""); setError(""); setPurge(false); setRemoveCaddy(false);
    setRequestID(crypto.randomUUID().replaceAll("-", ""));
  }
  function close() { if (!start.isPending) { setSelected(null); setPassword(""); setCode(""); } }

  return <>
    <Card className="my-4 p-5 sm:p-6">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <h3 className="font-bold">{controller ? "控制端维护" : "agent 维护"}</h3>
          <p className="mt-1 text-sm text-muted-foreground">当前版本 {q.data?.version || "—"}{!controller && target ? ` · 控制端提供 ${target}` : ""}</p>
          <p className="mt-1 text-xs text-muted-foreground">{controller ? "升级前自动备份，启动失败自动恢复。" : "升级同步控制端提供的程序；卸载会停止这台服务器上由 VpsCT 部署的服务。"}</p>
        </div>
        <div className="flex flex-wrap gap-2">
          {controller && <Button size="sm" variant="outline" disabled={!q.data?.available || busy} loading={latest.isPending} onClick={() => latest.mutate()}><RefreshCw className="h-4 w-4" /> 检查新版本</Button>}
          <Button size="sm" variant="outline" disabled={!q.data?.available || !upgradeAvailable || busy} onClick={() => choose("update")}>{controller ? "升级控制端" : "升级 agent"}</Button>
          <Button size="sm" variant="ghost" className="text-destructive" disabled={!q.data?.available || busy} onClick={() => choose("uninstall")}><Trash2 className="h-4 w-4" /> {controller ? "卸载控制端" : "卸载 agent"}</Button>
        </div>
      </div>
      {!q.data?.available && !q.isLoading && <p className="mt-3 text-sm text-amber-600 dark:text-amber-400">{q.data?.reason || "暂时无法读取维护状态"}</p>}
      {latest.error && <p role="alert" className="mt-3 text-sm text-destructive">{latest.error.message}</p>}
      {controller && latest.data && <p className="mt-3 text-sm">最新正式版 <a className="text-primary underline" href={latest.data.url} target="_blank" rel="noreferrer">{latest.data.version}</a>{upgradeAvailable ? "；升级会短暂停止面板。" : "，没有比当前版本更新的正式发行版。"}</p>}
      {disconnected && lastKnown && <div role="status" className="mt-4 rounded-xl bg-amber-500/10 p-3 text-sm leading-6">
        <AlertTriangle className="mr-1 inline h-4 w-4" />连接已中断，暂时无法确认最终结果。独立维护进程会继续执行。
        {lastKnown.action === "uninstall" && controller ? "控制端卸载后，此站点将不可用。" : "服务恢复后会自动刷新进度。"}
      </div>}
      {!!jobs.length && <div className="mt-4 space-y-3 border-t pt-4" aria-live="polite">
        {jobs.slice(0, 3).map((j) => <div key={j.id} className="text-sm">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <span className="font-medium">{j.action === "update" ? `升级 ${j.version || ""}` : `卸载 · ${j.purge ? "清空数据" : "保留数据"}`} <span className={j.status === "succeeded" ? "text-emerald-600" : ["failed", "interrupted"].includes(j.status) ? "text-destructive" : "text-muted-foreground"}>· {labels[j.status] ?? j.status}</span></span>
            <time className="text-xs text-muted-foreground">{fmtDate(j.updated_at)}</time>
          </div>
          <p className="mt-1 text-muted-foreground">{j.message}</p>
          <details className="mt-1 text-xs text-muted-foreground"><summary className="cursor-pointer">终端查看结果与日志</summary><pre className="mt-2 overflow-x-auto rounded-lg bg-muted p-3">{`sudo cat /var/lib/ctlvps-maintenance/${j.id}/status.json\nsudo cat /var/lib/ctlvps-maintenance/${j.id}/worker.log`}</pre></details>
        </div>)}
      </div>}
    </Card>
    <Dialog open={!!selected} onClose={close} title={selected === "update" ? `升级${controller ? "控制端" : " agent"}` : `卸载${controller ? "控制端" : " agent"}`} description={controller ? "目标：当前 VpsCT 控制端" : `目标：${server.name}`} footer={<>
      <Button variant="ghost" disabled={start.isPending} onClick={close}>取消</Button>
      <Button variant={selected === "uninstall" ? "destructive" : "default"} loading={start.isPending} disabled={!password || (user?.totp_enabled && !code) || (selected === "uninstall" && confirm !== expected)} onClick={() => start.mutate()}>{selected === "update" ? "确认升级" : "确认卸载"}</Button>
    </>}>
      <div className="space-y-4">
        {selected === "update" ? <p className="text-sm leading-6">{controller ? `升级至 ${target}。将停服备份数据，再更换程序并检查启动情况；启动失败会恢复旧程序和升级前数据。` : `同步控制端提供的 ${target}。agent 会短暂重启；新程序启动失败时恢复旧程序。`}</p> : <>
          <p className="text-sm leading-6">{controller ? "将停止并移除控制端及维护服务。完成后此网站不可用，已接入的远端 agent 继续保留。" : "将卸载 agent 及它在这台服务器上部署的服务，相关资源分享也会停止；面板中的服务器记录保留。"}</p>
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
