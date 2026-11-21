import * as React from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { get, post } from "@/lib/api";
import type { User } from "@/lib/types";
import { useAuth } from "@/lib/auth";
import { useToast } from "@/components/toast";
import { Button, Dialog, Field, Input, Spinner, Switch } from "@/components/ui";

interface Scope { kind: "all" | "servers" | "shares"; ids?: number[] }
interface Grant { id: number; user_id: number; granted_by: number; permission: string; scope: Scope; effective: boolean }
interface PermissionResponse { full_admin: boolean; grants: Grant[] }

const options = [
  ["ai.access", "使用 AI 能力和开发者 API key"],
  ["templates.create", "创建公共模板"],
  ["templates.edit", "编辑公共模板"],
  ["templates.delete", "删除公共模板"],
  ["shares.view", "查看分享状态和用量"],
  ["shares.links", "查看分享的订阅链接和节点凭据"],
  ["shares.create", "创建自己的分享"],
  ["shares.pause", "暂停或恢复分享"],
  ["shares.reset", "重置分享用量"],
  ["shares.revoke", "撤销分享"],
  ["shares.reissue", "重新签发分享"],
  ["shares.delete", "删除分享"],
] as const;

export function UserPermissionsDialog({ target, onClose }: { target: User | null; onClose: () => void }) {
  const { user: me } = useAuth();
  const qc = useQueryClient();
  const toast = useToast();
  const query = useQuery({ queryKey: ["user-permissions", target?.id], queryFn: () => get<PermissionResponse>(`/api/v1/users/${target!.id}/permissions`), enabled: !!target });
  const [selected, setSelected] = React.useState<string[]>([]);
  const [scopes, setScopes] = React.useState<Record<string, { kind: Scope["kind"]; ids: string }>>({});
  const [password, setPassword] = React.useState("");
  const [code, setCode] = React.useState("");
  React.useEffect(() => {
    setPassword(""); setCode("");
    setSelected((query.data?.grants ?? []).filter(g => g.granted_by === me?.id && options.some(([p]) => p === g.permission)).map(g => g.permission));
    setScopes(Object.fromEntries((query.data?.grants ?? []).filter(g => g.granted_by === me?.id).map(g => [g.permission, { kind: g.scope.kind, ids: (g.scope.ids ?? []).join(", ") }])));
  }, [target?.id, query.data, me?.id]);
  const save = useMutation({
    mutationFn: () => {
      const remaining = (query.data?.grants ?? []).filter(g => g.granted_by === me?.id && !options.some(([p]) => p === g.permission)).map(g => ({ permission: g.permission, scope: g.scope }));
      return post(`/api/v1/users/${target!.id}/permissions`, { grants: [...remaining, ...selected.map(permission => { const scope = scopes[permission] ?? { kind: "all", ids: "" }; return { permission, scope: scope.kind === "all" ? { kind: "all" } : { kind: scope.kind, ids: scope.ids.split(/[,，\s]+/).filter(Boolean).map(Number) } }; })], password, code });
    },
    onSuccess: () => { setPassword(""); setCode(""); qc.invalidateQueries({ queryKey: ["user-permissions", target?.id] }); toast.success("权限已保存，受影响账户需重新登录"); onClose(); },
    onError: e => { setCode(""); toast.fromError(e); },
  });
  return <Dialog open={!!target} onClose={onClose} title={`设置权限：${target?.nickname || target?.username || ""}`} description="普通用户保留自己的分享和个人模板。这里设置额外权限。AI 接入后的操作范围仍由账户权限决定。" footer={<><Button variant="outline" onClick={onClose}>取消</Button><Button loading={save.isPending} disabled={query.isLoading || query.isError || !password} onClick={() => save.mutate()}>保存权限</Button></>}>
    {query.isLoading ? <Spinner /> : query.isError ? <p className="text-sm text-destructive">权限加载失败，请关闭后重试。</p> : <div className="grid gap-4">
      {options.map(([permission, label]) => <div key={permission} className="grid gap-2"><Switch checked={selected.includes(permission)} onChange={checked => setSelected(old => checked ? [...old, permission] : old.filter(p => p !== permission))} label={label} />{selected.includes(permission) && permission.startsWith("shares.") && <div className="grid gap-2 pl-8"><label className="text-xs text-muted-foreground">授权范围<select className="ml-2 rounded border bg-background p-1" value={scopes[permission]?.kind ?? "all"} onChange={e => setScopes(old => ({ ...old, [permission]: { kind: e.target.value as Scope["kind"], ids: old[permission]?.ids ?? "" } }))}>{permission === "shares.create" ? <option value="all">全部服务器</option> : <option value="all">全部分享</option>}<option value="servers">指定服务器</option>{permission !== "shares.create" && <option value="shares">指定分享</option>}</select></label>{scopes[permission]?.kind && scopes[permission].kind !== "all" && <Input aria-label={`${label}范围编号`} value={scopes[permission].ids} onChange={e => setScopes(old => ({ ...old, [permission]: { ...old[permission], ids: e.target.value } }))} placeholder="输入编号，多个用逗号分隔" />}</div>}</div>)}
      {(query.data?.grants ?? []).some(g => g.granted_by !== me?.id) && <p className="text-xs text-muted-foreground">其他管理员授予的权限不会被本次操作修改。</p>}
      <Field label="你的密码" hint="权限调整需要再次验证当前账户"><Input type="password" autoComplete="current-password" value={password} onChange={e => setPassword(e.target.value)} /></Field>
      {me?.totp_enabled && <Field label="两步验证码"><Input value={code} onChange={e => setCode(e.target.value)} autoComplete="one-time-code" /></Field>}
    </div>}
  </Dialog>;
}
