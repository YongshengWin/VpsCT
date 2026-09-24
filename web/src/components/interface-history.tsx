import * as React from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { get, put } from "@/lib/api";
import type { NetworkView, Series } from "@/lib/types";
import { Button, Card, CardHeader, CardTitle, CardContent, Switch, Spinner, Select } from "@/components/ui";
import { TrafficBars } from "@/components/charts";
import { useToast } from "@/components/toast";

type Record = NetworkView["interfaces"][number] & { archived: boolean };
type Page = { items: Record[]; has_more: boolean; next_id: number };
export function InterfaceHistory({serverID}:{serverID:number}) {
 const toast=useToast();
 const [archived,setArchived]=React.useState(false), [pages,setPages]=React.useState<number[]>([0]), [selected,setSelected]=React.useState(0);
 const qc=useQueryClient(); const before=pages[pages.length-1];
 const q=useQuery({queryKey:["interface-history",serverID,archived,before],queryFn:()=>get<Page>(`/api/v1/servers/${serverID}/interfaces?archived=${archived}&before=${before}`)});
 const id=q.data?.items.some(n=>n.id===selected)?selected:q.data?.items[0]?.id??0;
 const traffic=useQuery({queryKey:["interface-archive-traffic",serverID,id],enabled:id>0,queryFn:()=>get<Series>(`/api/v1/servers/${serverID}/interfaces/${id}/traffic?days=90`)});
 const m=useMutation({mutationFn:(n:Record)=>put(`/api/v1/servers/${serverID}/interfaces/${n.id}/archive`,{archived:!n.archived}),onSuccess:()=>{qc.invalidateQueries({queryKey:["interface-history",serverID]});qc.invalidateQueries({queryKey:["server-network",serverID]});},onError:e=>toast.fromError(e)});
 return <Card><CardHeader><CardTitle>接口记录与归档</CardTitle></CardHeader><CardContent className="space-y-3">
 <p className="text-xs text-muted-foreground">按接口身份分页保留历史。归档只隐藏已消失的接口，不删除用量或改变计费；接口再次出现时自动恢复。</p>
 <Switch label="查看已归档接口" checked={archived} onChange={v=>{setArchived(v);setPages([0]);}}/>
 {q.isPending?<Spinner/>:q.isError?<p>{q.error.message}</p>:<>
 {q.data.items.map(n=><div key={n.id} className="flex items-center justify-between gap-2 text-sm"><span>{n.interface.name} · #{n.id} · {n.present?"当前接口":"已消失"}</span><Button size="sm" variant="outline" disabled={(!n.archived&&n.present)||m.isPending} onClick={()=>m.mutate(n)}>{n.archived?"取消归档":"归档"}</Button></div>)}
 {!q.data.items.length&&<p className="text-sm text-muted-foreground">暂无记录</p>}
 <div className="flex gap-2"><Button size="sm" variant="outline" disabled={pages.length===1} onClick={()=>setPages(p=>p.slice(0,-1))}>上一页</Button><Button size="sm" variant="outline" disabled={!q.data.has_more} onClick={()=>setPages(p=>[...p,q.data.next_id])}>下一页</Button></div>
 {!!q.data.items.length&&<><Select aria-label="归档接口历史" value={id} onChange={e=>setSelected(Number(e.target.value))}>{q.data.items.map(n=><option key={n.id} value={n.id}>{n.interface.name} · #{n.id} · 近90天</option>)}</Select>{traffic.data?.has_data?<TrafficBars points={traffic.data.points}/>:<p className="text-xs text-muted-foreground">暂无历史增量</p>}</>}
 </>}
 </CardContent></Card>;
}
