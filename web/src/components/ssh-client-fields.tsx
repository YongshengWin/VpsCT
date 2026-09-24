import { Field, Input, Select, Textarea } from "@/components/ui";

// Credentials stay in this node's client configuration; this form never reads
// the VPS management account, local SSH agent or filesystem key paths.
export function SSHClientFields({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  let params: Record<string, unknown> = {};
  try { params = JSON.parse(value || "{}"); } catch { /* validation on save */ }
  const str = (key: string) => typeof params[key] === "string" ? params[key] as string : "";
  const keyMode = "private-key" in params;
  const patch = (fields: Record<string, unknown>) => onChange(JSON.stringify({ ...params, ...fields }));
  const hostKeys = Array.isArray(params["host-key"]) ? (params["host-key"] as string[]).join("\n") : "";
  return <div className="space-y-4 rounded-lg border p-3">
    <p className="text-sm text-muted-foreground">SSH 客户端节点仅支持 TCP。使用专用转发账号，并通过可信渠道核对主机公钥。不会部署或修改远端 SSH 服务。</p>
    <Field label="SSH 用户名"><Input aria-label="SSH 用户名" autoComplete="off" value={str("username")} onChange={(e) => patch({ username: e.target.value })} /></Field>
    <Field label="SSH 认证方式"><Select aria-label="SSH 认证方式" value={keyMode ? "key" : "password"} onChange={(e) => { const next = { ...params }; delete next.password; delete next["private-key"]; delete next["private-key-passphrase"]; next[e.target.value === "key" ? "private-key" : "password"] = ""; onChange(JSON.stringify(next)); }}><option value="password">密码</option><option value="key">私钥</option></Select></Field>
    {keyMode ? <><Field label="SSH 私钥" hint="粘贴完整 PEM / OpenSSH 私钥内容，不支持文件路径。"><Textarea aria-label="SSH 私钥" rows={5} value={str("private-key")} onChange={(e) => patch({ "private-key": e.target.value })} /></Field><Field label="私钥口令（可选）"><Input aria-label="私钥口令" type="password" autoComplete="new-password" value={str("private-key-passphrase")} onChange={(e) => patch({ "private-key-passphrase": e.target.value })} /></Field></> : <Field label="SSH 密码"><Input aria-label="SSH 密码" type="password" autoComplete="new-password" value={str("password")} onChange={(e) => patch({ password: e.target.value })} /></Field>}
    <Field label="SSH 主机公钥" hint="每行一个完整公钥，例如 ssh-ed25519 后接公钥内容。不是指纹，不能留空。"><Textarea aria-label="SSH 主机公钥" rows={3} value={hostKeys} onChange={(e) => patch({ "host-key": e.target.value.split("\n") })} /></Field>
    <p className="text-xs text-muted-foreground">这些认证信息会交付给有权访问该节点订阅的客户端。支持 mihomo 与 sing-box 配置；不生成通用分享 URI。</p>
  </div>;
}
