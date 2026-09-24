import { Field, Input, Select, Textarea } from "@/components/ui";

export function TunnelClientFields({ protocol, value, onChange }: { protocol: "wireguard" | "mieru"; value: string; onChange: (v: string) => void }) {
  let params: Record<string, unknown> = {};
  try { params = JSON.parse(value || "{}"); } catch { /* save validates */ }
  const str = (key: string) => typeof params[key] === "string" ? params[key] as string : "";
  const patch = (fields: Record<string, unknown>) => onChange(JSON.stringify({ ...params, ...fields }));
  const textField = (key: string, label: string, secret = false) => <Field key={key} label={label}><Input aria-label={label} type={secret ? "password" : "text"} autoComplete="off" value={str(key)} onChange={e => patch({ [key]: e.target.value })} /></Field>;
  return <div className="space-y-3 rounded-lg border p-3">
    {protocol === "mieru" ? <>
      <p className="text-sm text-muted-foreground">连接已有 mieru 服务。使用 mihomo 客户端；当前录入一个固定端口。</p>
      {textField("username", "mieru 用户名")}{textField("password", "mieru 密码", true)}
      <Field label="mieru 传输"><Select aria-label="mieru 传输" value={str("transport") || "TCP"} onChange={e => patch({ transport: e.target.value })}><option>TCP</option><option>UDP</option></Select></Field>
      <Field label="多路复用"><Select aria-label="多路复用" value={str("multiplexing") || "MULTIPLEXING_LOW"} onChange={e => patch({ multiplexing: e.target.value })}>{["OFF", "LOW", "MIDDLE", "HIGH"].map(v => <option key={v} value={`MULTIPLEXING_${v}`}>{v}</option>)}</Select></Field>
    </> : <>
      <p className="text-sm text-muted-foreground">连接已有 WireGuard Peer。每台客户端使用独立私钥，避免多个设备互相抢占会话。这些密钥会进入该节点订阅。</p>
      {textField("ip", "隧道 IPv4")}{textField("ipv6", "隧道 IPv6（可选）")}
      {textField("private-key", "客户端私钥", true)}{textField("public-key", "Peer 公钥")}{textField("pre-shared-key", "预共享密钥（可选）", true)}
      <Field label="允许的目标网段" hint="每行一个 CIDR，留空按隧道地址族使用默认路由。"><Textarea aria-label="允许的目标网段" rows={3} value={Array.isArray(params["allowed-ips"]) ? (params["allowed-ips"] as string[]).join("\n") : ""} onChange={e => patch({ "allowed-ips": e.target.value === "" ? [] : e.target.value.split("\n") })} /></Field>
      <div className="grid grid-cols-2 gap-3"><Field label="MTU"><Input aria-label="MTU" type="number" value={Number(params.mtu ?? 1408)} onChange={e => patch({ mtu: Number(e.target.value) })} /></Field><Field label="保活间隔（秒）"><Input aria-label="保活间隔" type="number" value={Number(params["persistent-keepalive"] ?? 25)} onChange={e => patch({ "persistent-keepalive": Number(e.target.value) })} /></Field></div>
      <p className="text-xs text-muted-foreground">支持 mihomo、标准 WireGuard 配置，以及 sing-box 1.11+ endpoint。远端 DNS 节点可输出 mihomo / 标准配置；限制为 TCP 的节点仅输出 mihomo，避免转换时丢失限制。</p>
    </>}
  </div>;
}
