"use client";

import { Activity, ArrowRight, Boxes, CircleAlert, FileSearch, Gauge, RefreshCw, ShieldCheck, Sparkles } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import WorkspaceShell from "../components/workspace-shell";
import { api, type Task } from "../lib/api";

type Health = { status: string; version: string; checks: Record<string, { status: string; detail?: string }> };
const names: Record<string,string> = { draft:"草稿",queued:"排队中",running:"运行中",paused:"已暂停",completed:"已完成",failed:"失败",cancelled:"已取消" };

export default function Dashboard() {
  const [tasks,setTasks] = useState<Task[]>([]);
  const [health,setHealth] = useState<Health|null>(null);
  const [error,setError] = useState("");
  const [loading,setLoading] = useState(true);
  const [updated,setUpdated] = useState<Date|null>(null);
  const refresh=useCallback(async()=>{
    try {
      const [items,response]=await Promise.all([api<Task[]>("/tasks?limit=100"),fetch("/health/ready",{cache:"no-store"})]);
      setTasks(items);
      setHealth(await response.json() as Health);
      setUpdated(new Date());
      setError("");
    } catch(cause) { setError(cause instanceof Error?cause.message:"无法加载工作台"); }
    finally { setLoading(false); }
  },[]);
  useEffect(()=>{ void refresh();const timer=window.setInterval(()=>void refresh(),20000);return()=>window.clearInterval(timer);},[refresh]);
  const counts=useMemo(()=>({active:tasks.filter(t=>t.status==="running").length,waiting:tasks.filter(t=>t.status==="queued").length,needsAttention:tasks.filter(t=>t.status==="paused"||t.status==="failed").length,completed:tasks.filter(t=>t.status==="completed").length}),[tasks]);
  return <WorkspaceShell section="dashboard"><div className="workspace-header"><div><span className="workspace-eyebrow">TIANFA / 总览</span><h1>天罚工作台</h1><p>查看任务进度、服务状态与需要处理的方向。</p></div><button className="workspace-secondary" type="button" onClick={()=>void refresh()}><RefreshCw size={16}/>刷新</button></div>
    {error&&<div className="form-error" role="alert">{error}</div>}
    <div className="dashboard-stats" aria-label="任务概况">
      <a href="/tasks/"><Activity size={20}/><strong>{counts.active}</strong><span>运行中</span></a>
      <a href="/tasks/"><Gauge size={20}/><strong>{counts.waiting}</strong><span>排队中</span></a>
      <a href="/tasks/"><CircleAlert size={20}/><strong>{counts.needsAttention}</strong><span>需关注</span></a>
      <a href="/tasks/"><ShieldCheck size={20}/><strong>{counts.completed}</strong><span>已完成</span></a>
    </div>
    <div className="dashboard-grid"><section className="workspace-card"><div className="section-row"><div><h2>最近任务</h2><p>当前展示最近 {tasks.length} 个任务</p></div><a className="workspace-link" href="/tasks/">全部任务 <ArrowRight size={15}/></a></div>
      {loading?<p>正在加载…</p>:tasks.length===0?<div className="empty-small">还没有任务。<a href="/tasks/">创建第一个任务</a></div>:<div className="simple-list">{tasks.slice(0,8).map(task=><a className="dashboard-task" key={task.id} href={`/tasks/?id=${encodeURIComponent(task.id)}`}><span className="state-tag subtle">{names[task.status]||task.status}</span><strong>{task.title}</strong><small>{new Date(task.updated_at).toLocaleString("zh-CN")}</small><ArrowRight size={15}/></a>)}</div>}</section>
      <div className="dashboard-side"><section className="workspace-card"><h2>服务状态</h2><p>{health?.status==="ok"?"所有关键服务可用":"部分服务需要检查"}</p><div className="simple-list">{Object.entries(health?.checks||{}).map(([key,value])=><div key={key}><strong>{key}</strong><span className={`state-tag ${value.status==="up"?"subtle":"danger"}`}>{value.status==="up"?"正常":"异常"}</span>{value.detail&&<small>{value.detail}</small>}</div>)}</div>{updated&&<small>更新于 {updated.toLocaleTimeString("zh-CN")}</small>}</section>
      <section className="workspace-card"><h2>快捷入口</h2><div className="dashboard-links"><a href="/assets/"><Boxes size={17}/>全局资产</a><a href="/findings/"><FileSearch size={17}/>流量与漏洞</a><a href="/models/"><Sparkles size={17}/>模型与 Agent</a></div></section></div></div>
  </WorkspaceShell>;
}
