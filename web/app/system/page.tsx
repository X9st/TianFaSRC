"use client";
import { useCallback,useEffect,useState } from "react";
import { ClipboardList,RefreshCw,ShieldAlert } from "lucide-react";
import WorkspaceShell from "../../components/workspace-shell";
import {api,type Task} from "../../lib/api";
type Audit={id:string;action:string;resource_type:string;resource_id?:string;actor:string;occurred_at:string};
type Block={id:string;intent_id:string;tool:string;reason:string;summary:string;created_at:string};
type Policy={global_worker_limit:number;blocked_tool_calls:number;worker_hard_limits:{model_rounds:number;tool_calls:number;tokens:number;seconds:number}};
export default function SystemPage(){
 const [policy,setPolicy]=useState<Policy|null>(null),[audit,setAudit]=useState<Audit[]>([]),[cursor,setCursor]=useState(""),[tasks,setTasks]=useState<Task[]>([]),[taskID,setTaskID]=useState(""),[blocks,setBlocks]=useState<Block[]>([]),[error,setError]=useState("");
 const load=useCallback(async()=>{try{const [p,a,t]=await Promise.all([api<Policy>("/policy"),api<{items:Audit[];next_cursor:string}>("/audit-log?limit=50"),api<Task[]>("/tasks?limit=100")]);setPolicy(p);setAudit(a.items);setCursor(a.next_cursor);setTasks(t);setError("");}catch(e){setError(e instanceof Error?e.message:"加载系统状态失败");}},[]);
 useEffect(()=>{void load();},[load]);
 useEffect(()=>{if(!taskID){setBlocks([]);return;}void api<Block[]>(`/tasks/${taskID}/policy-blocks`).then(setBlocks).catch(e=>setError(e instanceof Error?e.message:"加载阻断记录失败"));},[taskID]);
 async function more(){if(!cursor)return;try{const next=await api<{items:Audit[];next_cursor:string}>(`/audit-log?limit=50&cursor=${encodeURIComponent(cursor)}`);setAudit([...audit,...next.items]);setCursor(next.next_cursor);}catch(e){setError(e instanceof Error?e.message:"加载审计记录失败");}}
 return <WorkspaceShell section="system"><header className="workspace-header"><div><span className="workspace-eyebrow">工作台 / 系统</span><h1>策略与审计</h1><p>查看运行边界、策略阻断与不可修改的审计记录。</p></div><button type="button" className="workspace-secondary" onClick={()=>void load()}><RefreshCw size={16}/>刷新</button></header>{error&&<div className="form-error" role="alert">{error}</div>}
 <div className="phase6-columns"><section className="workspace-card"><h2><ShieldAlert size={18}/> 运行边界</h2>{policy?<div className="simple-list"><div><strong>全局并发上限</strong><span>{policy.global_worker_limit} 个 Worker</span></div><div><strong>Worker 单次硬上限</strong><small>模型 {policy.worker_hard_limits.model_rounds} 回合 · 工具 {policy.worker_hard_limits.tool_calls} 次 · {policy.worker_hard_limits.tokens} Token · {policy.worker_hard_limits.seconds} 秒</small></div><div><strong>策略阻断累计</strong><span>{policy.blocked_tool_calls} 次</span></div></div>:<p>正在加载…</p>}<p className="form-hint">任务范围与策略模式在任务详情设置；工具开关在<a href="/tools/">工具与 Skill</a>管理。调整前请查看影响范围并确认。</p></section>
 <section className="workspace-card"><h2>策略阻断</h2><p>仅显示本组织任务被策略拦截的工具调用。</p><label>选择任务<select value={taskID} onChange={e=>setTaskID(e.target.value)}><option value="">选择任务</option>{tasks.map(t=><option key={t.id} value={t.id}>{t.title}</option>)}</select></label><div className="simple-list">{blocks.map(b=><div key={b.id}><strong>{b.tool}</strong><small>{b.reason||b.summary||"策略拒绝"}</small><small>{new Date(b.created_at).toLocaleString("zh-CN")}</small><a className="workspace-link" href={`/tasks/?id=${taskID}`}>查看任务</a></div>)}{taskID&&!blocks.length&&<p className="empty-small">该任务没有策略阻断记录</p>}</div></section></div>
 <section className="workspace-card phase6-audit"><h2><ClipboardList size={18}/> 审计日志</h2><p>审计日志仅追加，敏感详情不在列表中展示。</p><div className="simple-list">{audit.map(a=><div key={a.id}><strong>{a.action}</strong><small>{a.actor||"系统"} · {a.resource_type}{a.resource_id?` · ${a.resource_id.slice(0,8)}`:""} · {new Date(a.occurred_at).toLocaleString("zh-CN")}</small></div>)}{!audit.length&&<p className="empty-small">暂无审计记录</p>}</div>{cursor&&<button className="workspace-secondary" type="button" onClick={()=>void more()}>加载更早记录</button>}</section>
 </WorkspaceShell>;
}
