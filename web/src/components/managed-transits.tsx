import * as React from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api, get, post } from "@/lib/api";
import type { Node, Server, NetworkView, Series } from "@/lib/types";
import { networkOperationID, type DirectEgress, type NetworkImpact } from "@/lib/network";
import { Button, Card, CardHeader, CardTitle, CardContent, Dialog, Field, Select, Input, Badge } from "@/components/ui";
import { PathFields, DNSFields, FamilyField } from "@/components/egress-editor";
import { NetworkImpactPreview } from "@/components/network-impact";
import { TrafficIO } from "@/components/traffic-ways";
import { useToast } from "@/components/toast";
type Transit={id:string;protocol:"wireguard"|"ss2022";updated_at:string;entry_server_id:number;landing_server_id:number;entry_node_id:number;landing_node_id:number;profile_id:number;stage:string;hidden:boolean;entry_status:string;landing_status:string;entry_checks?:{code:string;ready:boolean;message:string}[]};
type Draft={protocol:"wireguard"|"ss2022";family:DirectEgress["family"];operation_id:string;entry_node_id:number;expected_revision:number;landing_server_id:number;landing_address:string;landing_port:number;outer:DirectEgress;dns:DirectEgress["dns"];expected_impact?:string};
type Preview={ready:boolean;token:string;entry:NetworkImpact;landing:NetworkImpact;checks?:{code:string;ready:boolean;message:string}[]};
const statuses:Record<string,string>={offline:"离线",apply_failed:"应用失败，请查看服务器诊断",publish_failed:"发布失败，等待重试",publishing:"等待发布",waiting:"等待回执",applied:"配置已应用"};
const stages:Record<string,string>={pending_landing:"正在设置落地 VPS",pending_entry:"正在设置入口节点",applied:"两端已应用",stopping_entry:"正在停止入口",stopping_landing:"正在清理落地 VPS",retired:"已退役"};
function TransitUsage({id}:{id:string}){const q=useQuery({queryKey:["transit-traffic",id],queryFn:()=>get<Series>(`/api/v1/transits/${id}/traffic`),refetchInterval:30000});return q.data?<TrafficIO inbound={q.data.total_up} outbound={q.data.total_down}/>:null;}
export function ManagedTransits({server,inventory}:{server:Server;inventory?:NetworkView}){
 const toast=useToast(),qc=useQueryClient();const [open,setOpen]=React.useState(false),[retire,setRetire]=React.useState<Transit|null>(null),[hide,setHide]=React.useState<Transit|null>(null),[showHidden,setShowHidden]=React.useState(false);
 const [review,setReview]=React.useState<{draft:Draft;result:Preview}|null>(null);
 const [draft,setDraft]=React.useState<Draft>({protocol:"wireguard",family:"ipv4",operation_id:networkOperationID(),entry_node_id:0,expected_revision:0,landing_server_id:0,landing_address:"",landing_port:51820,outer:{family:"ipv4",dns:{transport:"udp",address:"1.1.1.1",port:53}},dns:{transport:"udp",address:"1.1.1.1",port:53}});
 const q=useQuery({queryKey:["managed-transits",server.id,showHidden],queryFn:()=>get<Transit[]>(`/api/v1/servers/${server.id}/transits${showHidden?"?include_hidden=1":""}`),refetchInterval:5000});
 const servers=useQuery({queryKey:["servers"],queryFn:()=>get<Server[]>("/api/v1/servers")});
 const nodes=useQuery({queryKey:["transit-entry-nodes",server.id],queryFn:()=>get<Node[]>(`/api/v1/nodes?server_id=${server.id}`)});
 const preview=useMutation({mutationFn:async()=>({draft:{...draft,operation_id:networkOperationID()},result:await post<Preview>("/api/v1/transits/preview",draft)}),onSuccess:setReview,onError:e=>toast.fromError(e)});
 const refresh=()=>{qc.invalidateQueries({queryKey:["managed-transits"]});qc.invalidateQueries({queryKey:["nodes"]});qc.invalidateQueries({queryKey:["egress-profiles"]});};
 const save=useMutation({mutationFn:()=>post<Transit>("/api/v1/transits",{...review!.draft,expected_impact:review!.result.token}),onSuccess:()=>{setOpen(false);setReview(null);refresh();toast.success("托管中转已提交，等待两端应用");},onError:e=>toast.fromError(e)});
 const retry=useMutation({mutationFn:(t:Transit)=>post(`/api/v1/transits/${t.id}/retry`,{expected_updated_at:t.updated_at}),onSuccess:refresh,onError:e=>toast.fromError(e)});
 const retirePreview=useMutation({mutationFn:(t:Transit)=>post<Preview>(`/api/v1/transits/${t.id}/retire-preview`,{}),onError:e=>toast.fromError(e)});
 const remove=useMutation({mutationFn:()=>api(`/api/v1/transits/${retire!.id}`,{method:"DELETE",json:{expected_impact:retirePreview.data!.token}}),onSuccess:()=>{setRetire(null);refresh();},onError:e=>toast.fromError(e)});
 const visibility=useMutation({mutationFn:({item,hidden}:{item:Transit;hidden:boolean})=>post<Transit>(`/api/v1/transits/${item.id}/visibility`,{expected_updated_at:item.updated_at,hidden}),onSuccess:(_,request)=>{setHide(null);refresh();toast.success(request.hidden?"已从列表移除，可通过“显示已移除”找回":"已恢复显示");},onError:e=>{toast.fromError(e);refresh();}});
 return <Card>
  <CardHeader className="flex-row items-center justify-between gap-3">
   <div><CardTitle>让节点经另一台 VPS 出网</CardTitle><p className="mt-1 text-sm text-muted-foreground">用户仍连接原节点，网站看到落地 VPS 的地址。</p></div>
   <Button onClick={()=>{setReview(null);setOpen(true);}}>添加中转</Button>
  </CardHeader>
  <CardContent className="space-y-3">
   {q.isError&&<p role="alert">{q.error.message}</p>}
   {q.data?.length===0&&<div className="rounded-lg border border-dashed p-4 text-sm text-muted-foreground">目前没有中转，节点从这台 VPS 直接访问网站。</div>}
   {q.data?.map(t=>{
    const entry=nodes.data?.find(n=>n.id===t.entry_node_id)?.name??`入口节点 #${t.entry_node_id}`;
    const landing=servers.data?.find(s=>s.id===t.landing_server_id)?.name??`服务器 #${t.landing_server_id}`;
    return <div key={t.id} className="rounded-lg border p-4">
     <div className="flex flex-wrap items-start justify-between gap-3">
      <div><p className="font-medium">{entry} → {landing}</p><p className="mt-1 text-xs text-muted-foreground">{t.stage==="retired"?"入口已阻断，落地服务与计量已清理。":t.stage==="applied"?"两端配置已应用；仍需实际访问确认线路可用。":"配置中，完成前入口不会开放。"}</p></div>
      <div className="flex gap-2"><Badge>{stages[t.stage]??t.stage}</Badge>{t.hidden&&<Badge variant="secondary">已移除</Badge>}</div>
     </div>
     {t.entry_checks?.length?<div role="status" className="mt-3 text-sm"><p>落地已就绪，入口仍被阻断：</p>{t.entry_checks.map(c=><p key={c.code}>{c.message}</p>)}</div>:null}
     {[t.entry_status,t.landing_status].some(s=>s==="apply_failed"||s==="publish_failed")&&!["retired","applied"].includes(t.stage)&&<Button className="mt-3" size="sm" disabled={retry.isPending} onClick={()=>retry.mutate(t)}>重试配置</Button>}
     <div className="mt-3 flex flex-wrap items-center gap-3">
      {t.stage!=="retired"?<Button size="sm" variant="outline" onClick={()=>{setRetire(t);retirePreview.reset();retirePreview.mutate(t);}}>停止中转</Button>:t.hidden?<Button size="sm" variant="outline" disabled={visibility.isPending} onClick={()=>visibility.mutate({item:t,hidden:false})}>恢复显示</Button>:<Button size="sm" variant="outline" onClick={()=>setHide(t)}>从列表移除</Button>}
      <details className="text-xs text-muted-foreground"><summary className="cursor-pointer">协议、状态与用量</summary><div className="mt-3 space-y-2"><p>{t.protocol==="ss2022"?"SS-2022":"WireGuard"} · 入口 #{t.entry_node_id} · 出站规则 #{t.profile_id}</p><p>入口：{statuses[t.entry_status]??"等待"} · 落地：{statuses[t.landing_status]??"等待"}</p><p>落地近 30 天用量，独立统计。</p><TransitUsage id={t.id}/></div></details>
     </div>
    </div>;
   })}
   <button type="button" className="text-xs text-muted-foreground underline-offset-2 hover:underline" onClick={()=>setShowHidden(v=>!v)}>{showHidden?"隐藏已移除记录":"显示已移除记录"}</button>
   {open&&<Dialog open title="添加节点中转" onClose={()=>setOpen(false)} size="lg" footer={<><Button variant="outline" disabled={save.isPending} onClick={()=>{if(review)setReview(null);else setOpen(false);}}>{review?"返回编辑":"取消"}</Button>{review?<Button loading={save.isPending} disabled={!review.result.ready} onClick={()=>save.mutate()}>提交两端配置</Button>:<Button disabled={!draft.entry_node_id||!draft.landing_server_id||!draft.landing_address} loading={preview.isPending} onClick={()=>preview.mutate()}>预览影响</Button>}</>}>
    {review?<div className="space-y-3"><p className="text-sm">用户仍连接原节点，流量将经所选落地 VPS 出去。两端代理可能重启；任一端离线时会等待恢复。</p>{!review.result.ready&&<p role="alert">当前还不能提交，请检查下方原因。</p>}{review.result.checks?.filter(c=>!c.ready).map((c,i)=><p className="text-sm text-destructive" key={`${c.code}-${i}`}>{c.message}</p>)}<NetworkImpactPreview impact={review.result.entry}/><NetworkImpactPreview impact={review.result.landing}/></div>:<fieldset disabled={preview.isPending} className="space-y-4">
     <Field label="1. 用户连接这台 VPS 的哪个节点？" hint="目前仅支持已部署的 Shadowsocks 2022 节点。"><Select aria-label="托管中转入口节点" value={draft.entry_node_id} onChange={e=>{const n=nodes.data?.find(n=>n.id===Number(e.target.value));setDraft({...draft,entry_node_id:n?.id??0,expected_revision:n?.network_revision??0});}}><option value={0}>选择节点</option>{nodes.data?.filter(n=>n.source==="deployed"&&n.protocol==="ss"&&!n.revoked).map(n=><option key={n.id} value={n.id}>{n.name}</option>)}</Select></Field>
     {nodes.data&&!nodes.data.some(n=>n.source==="deployed"&&n.protocol==="ss"&&!n.revoked)&&<p role="status" className="text-sm text-amber-700">这台 VPS 还没有可用的 Shadowsocks 2022 节点。请先用页面顶部的“部署节点”创建一个。</p>}
     <Field label="2. 网站从哪台 VPS 出去？"><Select aria-label="托管中转落地服务器" value={draft.landing_server_id} onChange={e=>{const s=servers.data?.find(s=>s.id===Number(e.target.value));setDraft({...draft,landing_server_id:s?.id??0,landing_address:s?.public_host||s?.agent?.public_ipv4||""});}}><option value={0}>选择落地 VPS</option>{servers.data?.filter(s=>s.id!==server.id&&s.enabled).map(s=><option key={s.id} value={s.id}>{s.name}</option>)}</Select></Field>
     {draft.entry_node_id>0&&draft.landing_server_id>0&&<p className="rounded-lg bg-muted p-3 text-sm">用户 → {server.name} 的节点 → {servers.data?.find(s=>s.id===draft.landing_server_id)?.name} → 网站</p>}
     {draft.landing_server_id>0&&!draft.landing_address&&<p role="alert" className="text-sm text-amber-700">落地 VPS 没有默认连接地址，请展开高级设置填写。</p>}
     <details className="rounded-lg border p-3"><summary className="cursor-pointer text-sm font-medium">高级设置：协议、连接地址、网卡和 DNS</summary><div className="mt-4 space-y-3">
      <Field label="中转协议"><Select aria-label="中转协议" value={draft.protocol} onChange={e=>{const protocol=e.target.value as Draft["protocol"];setDraft({...draft,protocol,landing_port:protocol==="ss2022"?8388:51820});}}><option value="wireguard">WireGuard</option><option value="ss2022">SS-2022（官方 sing-box 1.14.1 / 1.14.x）</option></Select></Field>
      <Field label="落地地址" hint="默认使用落地 VPS 配置的公网地址；私网地址需入口 VPS 本机授权。"><Input aria-label="落地地址" value={draft.landing_address} onChange={e=>setDraft({...draft,landing_address:e.target.value})}/></Field>
      <Field label={draft.protocol==="ss2022"?"落地 TCP/UDP 端口":"落地 UDP 端口"}><Input aria-label="落地端口" type="number" min={1025} max={65535} value={draft.landing_port} onChange={e=>setDraft({...draft,landing_port:Number(e.target.value)})}/></Field>
      <FamilyField label="业务地址族" value={draft.family} onChange={v=>setDraft({...draft,family:v})}/>
      <PathFields value={draft.outer} inventory={inventory} outer onChange={v=>setDraft({...draft,outer:v})}/>
      <DNSFields prefix="外层 " value={draft.outer.dns} onChange={v=>setDraft({...draft,outer:{...draft.outer,dns:v}})}/>
      <DNSFields prefix="隧道内 " value={draft.dns} onChange={v=>setDraft({...draft,dns:v})}/>
     </div></details>
    </fieldset>}
   </Dialog>}
   {retire&&<Dialog open title="停止这条中转？" onClose={()=>setRetire(null)} footer={<><Button variant="outline" onClick={()=>setRetire(null)}>取消</Button><Button variant="destructive" loading={remove.isPending} disabled={!retirePreview.data?.ready||retirePreview.isPending} onClick={()=>remove.mutate()}>阻断并退役</Button></>}><div className="space-y-3">{retirePreview.data&&<><NetworkImpactPreview impact={retirePreview.data.entry}/><NetworkImpactPreview impact={retirePreview.data.landing}/></>}<p>先阻断入口，再撤销落地服务。节点不会自动改为直连；两端代理可能重启。离线端会等待恢复，完成计量结算后才显示“已退役”。</p>{retirePreview.isPending&&<p>正在核对两端影响…</p>}{retirePreview.isError&&<p role="alert">{retirePreview.error.message}</p>}</div></Dialog>}
   {hide&&<Dialog open title="从列表移除已退役中转？" onClose={()=>{if(!visibility.isPending)setHide(null);}} footer={<><Button variant="outline" disabled={visibility.isPending} onClick={()=>setHide(null)}>取消</Button><Button loading={visibility.isPending} onClick={()=>visibility.mutate({item:hide,hidden:true})}>移除显示</Button></>}><p className="text-sm">这条线路已经退役。移除后不会再显示在常用列表；历史流量、审计和计量身份仍保留，可通过“显示已移除记录”找回。</p></Dialog>}
  </CardContent>
 </Card>;
}
