import * as React from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus, Trash2, Pencil, ShieldCheck, ShieldOff } from "lucide-react";
import { del, get, post, put } from "@/lib/api";
import type { User, Share, Subscription } from "@/lib/types";
import { displayName, fmtAgo, fmtDate } from "@/lib/utils";
import { Badge, Button, Card, Confirm, Dialog, Empty, Field, Input, PageHeader, Select, Spinner, Switch, Table, Td, Th, Tr } from "@/components/ui";
import { Avatar } from "@/components/avatar";
import { useToast } from "@/components/toast";
import { useAuth } from "@/lib/auth";
import { UserPermissionsDialog } from "@/components/user-permissions";

export function UsersPage() {
  const qc = useQueryClient();
  const toast = useToast();
  const { user: me } = useAuth();
  const q = useQuery({ queryKey: ["users"], queryFn: () => get<User[]>("/api/v1/users") });
  const owner = useQuery({ queryKey: ["controller-owner"], queryFn: () => get<{ user_id: number; is_owner: boolean }>("/api/v1/system/owner") });
  const shares = useQuery({ queryKey: ["shares"], queryFn: () => get<Share[]>("/api/v1/shares") });
  const subs = useQuery({ queryKey: ["subscriptions"], queryFn: () => get<Subscription[]>("/api/v1/subscriptions") });
  const [edit, setEdit] = React.useState<User | null | "new">(null);
  const [confirmDel, setConfirmDel] = React.useState<User | null>(null);
  const [reset2fa, setReset2fa] = React.useState<User | null>(null);
  const [permissions, setPermissions] = React.useState<User | null>(null);
  const [transfer, setTransfer] = React.useState<User | null>(null);
  const invalidate = () => qc.invalidateQueries({ queryKey: ["users"] });
  const delM = useMutation({ mutationFn: (id: number) => del(`/api/v1/users/${id}`), onSuccess: () => { toast.success("已删除"); setConfirmDel(null); invalidate(); }, onError: (e) => toast.fromError(e) });
  const toggle = useMutation({ mutationFn: (u: User) => put(`/api/v1/users/${u.id}`, { enabled: !u.enabled }), onSuccess: invalidate, onError: (e) => toast.fromError(e) });
  const resetM = useMutation({
    mutationFn: (id: number) => post(`/api/v1/users/${id}/2fa/reset`),
    onSuccess: () => { toast.success("已关闭该用户的两步验证"); setReset2fa(null); invalidate(); qc.invalidateQueries({ queryKey: ["auth", "me"] }); },
    onError: (e) => toast.fromError(e),
  });
  return (
    <div>
      <PageHeader title="用户" actions={<Button onClick={() => setEdit("new")}><Plus className="h-4 w-4" /> 添加用户</Button>} />
      {q.isLoading ? <Spinner /> : !q.data?.length ? <Empty title="没有用户" /> : (
        <Card>
          <Table>
            <thead><tr className="border-b"><Th>用户</Th><Th>角色</Th><Th>启用</Th><Th>两步验证</Th><Th>分享</Th><Th>授权订阅</Th><Th>最近登录</Th><Th>创建</Th><Th></Th></tr></thead>
            <tbody>
              {q.data.map((u) => {
                const myShares = shares.data?.filter((s) => s.user_id === u.id) ?? [];
                const mySubs = subs.data?.filter((s) => s.allowed_user_ids?.includes(u.id) || s.owner_user_id === u.id) ?? [];
                return (
                  <Tr key={u.id}>
                    <Td>
                      <div className="flex items-center gap-2.5">
                        <Avatar user={u} size={32} />
                        <span className="min-w-0">
                          <span className="font-medium">{displayName(u)}{me?.id === u.id && <span className="ml-1 text-xs text-muted-foreground">(我)</span>}</span>
                          {displayName(u) !== u.username && <span className="ml-1.5 text-xs text-muted-foreground">{u.username}</span>}
                        </span>
                      </div>
                    </Td>
                    <Td><Badge variant={u.role === "admin" ? "default" : "secondary"}>{owner.data?.user_id === u.id ? "系统所有者" : u.role === "admin" ? "管理员" : "普通用户"}</Badge></Td>
                    <Td><Switch checked={u.enabled} disabled={me?.id === u.id || owner.data?.user_id === u.id} onChange={() => toggle.mutate(u)} /></Td>
                    <Td>
                      {u.totp_enabled ? (
                        <span className="inline-flex items-center gap-1.5">
                          <Badge variant="success"><ShieldCheck className="mr-1 h-3 w-3" />已开启</Badge>
                          {owner.data?.user_id !== u.id && <button className="text-xs text-muted-foreground hover:text-foreground hover:underline" onClick={() => setReset2fa(u)} title="用户丢失验证器时由管理员关闭">重置</button>}
                        </span>
                      ) : (
                        <span className="inline-flex items-center gap-1 text-xs text-muted-foreground"><ShieldOff className="h-3.5 w-3.5" />未开启</span>
                      )}
                    </Td>
                    <Td className="text-xs">{myShares.length ? myShares.map((s) => s.name).join("、") : "—"}</Td>
                    <Td className="text-xs">{mySubs.length ? mySubs.map((s) => s.name).join("、") : "—"}</Td>
                    <Td className="text-xs text-muted-foreground">{fmtAgo(u.last_login_at)}</Td>
                    <Td className="text-xs text-muted-foreground">{fmtDate(u.created_at, false)}</Td>
                    <Td className="text-right">
                      {u.role !== "admin" && <Button size="sm" variant="ghost" onClick={() => setPermissions(u)}>权限</Button>}
                      {owner.data?.is_owner && u.role === "admin" && u.enabled && u.id !== me?.id && <Button size="sm" variant="ghost" onClick={() => setTransfer(u)}>移交所有权</Button>}
                      {(owner.data?.user_id !== u.id || owner.data?.is_owner) && <Button size="icon" variant="ghost" onClick={() => setEdit(u)}><Pencil className="h-4 w-4" /></Button>}
                      {me?.id !== u.id && owner.data?.user_id !== u.id && <Button size="icon" variant="ghost" className="text-red-500" onClick={() => setConfirmDel(u)}><Trash2 className="h-4 w-4" /></Button>}
                    </Td>
                  </Tr>
                );
              })}
            </tbody>
          </Table>
        </Card>
      )}
      <UserDialog u={edit} onClose={() => setEdit(null)} />
      <UserPermissionsDialog target={permissions} onClose={() => setPermissions(null)} />
      <OwnerTransferDialog target={transfer} onClose={() => setTransfer(null)} />
      <Confirm open={!!confirmDel} onClose={() => setConfirmDel(null)} onConfirm={() => confirmDel && delM.mutate(confirmDel.id)} loading={delM.isPending} destructive title={`删除用户「${displayName(confirmDel)}」？`} description="其绑定的分享会解除绑定（不删除），授权订阅会移除该用户。" />
      <Confirm open={!!reset2fa} onClose={() => setReset2fa(null)} onConfirm={() => reset2fa && resetM.mutate(reset2fa.id)} loading={resetM.isPending} destructive title={`重置「${displayName(reset2fa)}」的两步验证？`} description="将关闭其两步验证并退出其所有会话；之后仅凭密码即可登录，用户可自行重新开启。" />
    </div>
  );
}

function OwnerTransferDialog({ target, onClose }: { target: User | null; onClose: () => void }) {
  const { user } = useAuth();
  const qc = useQueryClient();
  const toast = useToast();
  const [password, setPassword] = React.useState("");
  const [code, setCode] = React.useState("");
  React.useEffect(() => { setPassword(""); setCode(""); }, [target?.id]);
  const save = useMutation({
    mutationFn: () => post("/api/v1/system/owner", { user_id: target!.id, password, code }),
    onSuccess: () => { setPassword(""); setCode(""); qc.invalidateQueries({ queryKey: ["controller-owner"] }); toast.success("所有权已移交"); onClose(); },
    onError: e => { setCode(""); toast.fromError(e); },
  });
  return <Dialog open={!!target} onClose={onClose} title={`移交所有权给「${displayName(target)}」？`} description="对方将成为系统所有者，只有新的所有者可以再次移交。你的管理员权限保持不变。" footer={<><Button variant="outline" onClick={onClose}>取消</Button><Button disabled={!password} loading={save.isPending} onClick={() => save.mutate()}>确认移交</Button></>}>
    <div className="grid gap-4"><Field label="你的密码"><Input type="password" autoComplete="current-password" value={password} onChange={e => setPassword(e.target.value)} /></Field>{user?.totp_enabled && <Field label="两步验证码"><Input autoComplete="one-time-code" value={code} onChange={e => setCode(e.target.value)} /></Field>}</div>
  </Dialog>;
}

function UserDialog({ u, onClose }: { u: User | null | "new"; onClose: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const isNew = u === "new";
  const { user: me } = useAuth();
  const [reauthPassword, setReauthPassword] = React.useState("");
  const [reauthCode, setReauthCode] = React.useState("");
  const [f, setF] = React.useState({ username: "", nickname: "", password: "", role: "user" as User["role"], enabled: true });
  React.useEffect(() => {
    setReauthPassword(""); setReauthCode("");
    if (u && u !== "new") setF({ username: u.username, nickname: u.nickname ?? "", password: "", role: u.role, enabled: u.enabled });
    else setF({ username: "", nickname: "", password: "", role: "user", enabled: true });
  }, [u]);
  const m = useMutation({
    mutationFn: () => {
      const body: Record<string, unknown> = { username: f.username, nickname: f.nickname, role: f.role, enabled: f.enabled };
      if (f.password) body.password = f.password;
      if (f.role === "admin" && (isNew || u && u.role !== "admin")) { body.reauth_password = reauthPassword; body.reauth_code = reauthCode; }
      return isNew ? post<User>("/api/v1/users", body) : put<User>(`/api/v1/users/${(u as User).id}`, body);
    },
    onSuccess: () => { toast.success(isNew ? "已添加" : "已保存"); qc.invalidateQueries({ queryKey: ["users"] }); onClose(); },
    onError: (e) => toast.fromError(e),
  });
  return (
    <Dialog open={!!u} onClose={onClose} title={isNew ? "添加用户" : `编辑用户：${f.nickname.trim() || f.username}`} footer={<><Button variant="outline" onClick={onClose}>取消</Button><Button onClick={() => m.mutate()} loading={m.isPending}>保存</Button></>}>
      <div className="grid gap-4">
        <Field label="用户名" hint="登录用，创建后一般不要改"><Input value={f.username} onChange={(e) => setF({ ...f, username: e.target.value })} autoComplete="off" /></Field>
        <Field label="昵称" hint="展示用，可随时改；留空则显示用户名"><Input value={f.nickname} onChange={(e) => setF({ ...f, nickname: e.target.value })} maxLength={32} placeholder={f.username || "选填"} /></Field>
        <Field label={isNew ? "密码" : "新密码"} hint={isNew ? "至少 8 位" : "留空不修改"}><Input type="password" value={f.password} onChange={(e) => setF({ ...f, password: e.target.value })} autoComplete="new-password" /></Field>
        <Field label="角色">
          <Select value={f.role} onChange={(e) => setF({ ...f, role: e.target.value as User["role"] })}>
            <option value="user">普通用户（仅查看自己的分享/订阅）</option>
            <option value="admin">管理员</option>
          </Select>
        </Field>
        {f.role === "admin" && (isNew || u && u.role !== "admin") && <><Field label="你的密码" hint="授予完整管理员权限需要再次验证"><Input type="password" value={reauthPassword} onChange={e => setReauthPassword(e.target.value)} autoComplete="current-password" /></Field>{me?.totp_enabled && <Field label="两步验证码"><Input value={reauthCode} onChange={e => setReauthCode(e.target.value)} autoComplete="one-time-code" /></Field>}</>}
        <Switch checked={f.enabled} onChange={(v) => setF({ ...f, enabled: v })} label="启用（停用后无法登录，现有会话失效）" />
      </div>
    </Dialog>
  );
}
