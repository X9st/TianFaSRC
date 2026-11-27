"use client";

import { Activity, ArrowRight, CircleAlert, Radar, RefreshCw, ShieldCheck } from "lucide-react";
import { useCallback, useEffect, useMemo, useState } from "react";
import WorkspaceShell from "../components/workspace-shell";
import { api, type Task } from "../lib/api";

type Health = { status: string; version: string; checks: Record<string, { status: string; detail?: string }> };
type ReviewFinding = { id: string; task_id: string; task_title: string; title: string; severity: string };
type Metrics = {
  active_workers: number;
  input_tokens: number;
  output_tokens: number;
  cost_minor: number;
  findings: Record<string, number>;
  finding_statuses: Record<string, number>;
  review_queue: ReviewFinding[];
  models: Record<string, number>;
  recent_activity: { task_id: string; kind: string; summary: string; occurred_at: string }[];
};
const names: Record<string, string> = { draft: "草稿", queued: "排队中", running: "挖掘中", paused: "已暂停", completed: "已完成", failed: "失败", cancelled: "已取消" };
const severityNames: Record<string, string> = { critical: "严重", high: "高", medium: "中", low: "低", info: "提示" };

export default function Dashboard() {
  const [tasks, setTasks] = useState<Task[]>([]);
  const [health, setHealth] = useState<Health | null>(null);
  const [metrics, setMetrics] = useState<Metrics | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [updated, setUpdated] = useState<Date | null>(null);
  const refresh = useCallback(async () => {
    try {
      const [items, response, summary] = await Promise.all([api<Task[]>("/tasks?limit=100"), fetch("/health/ready", { cache: "no-store" }), api<Metrics>("/dashboard")]);
      setTasks(items);
      setHealth(await response.json() as Health);
      setMetrics(summary);
      setUpdated(new Date());
      setError("");
    } catch (cause) { setError(cause instanceof Error ? cause.message : "无法加载漏洞挖掘工作台"); }
    finally { setLoading(false); }
  }, []);
  useEffect(() => { void refresh(); const timer = window.setInterval(() => void refresh(), 20000); return () => window.clearInterval(timer); }, [refresh]);
  const counts = useMemo(() => ({
    active: tasks.filter(t => t.status === "running").length,
    needsAttention: tasks.filter(t => t.status === "paused" || t.status === "failed").length,
  }), [tasks]);
  const highRisk = (metrics?.findings.critical ?? 0) + (metrics?.findings.high ?? 0);
  const reviewQueue = metrics?.review_queue ?? [];

  return <WorkspaceShell section="dashboard">
    <header className="mining-hero">
      <div>
        <span className="workspace-eyebrow">天罚 / 漏洞挖掘工作台</span>
        <h1>从授权资产到可复核漏洞</h1>
        <p>跟踪挖掘任务、风险线索、证据和复测进展。每一步都限制在已授权范围内。</p>
        <div className="mining-hero-actions">
          <a className="workspace-primary" href="/tasks/?new=1">新建挖掘任务 <ArrowRight size={17} /></a>
          <a className="workspace-secondary" href="#mining-findings">处理漏洞线索</a>
        </div>
      </div>
      <div className="mining-hero-mark" aria-hidden="true"><Radar size={96} strokeWidth={1.2} /><span>ASSET · INTENT · EVIDENCE</span></div>
    </header>
    {error && <div className="form-error" role="alert">{error}</div>}
    <section className="dashboard-stats" aria-label="漏洞挖掘概况">
      <a href="/findings/?tab=findings"><CircleAlert size={20} /><strong>{metrics ? metrics.finding_statuses.draft ?? 0 : "—"}</strong><span>待复核漏洞线索</span></a>
      <a href="/findings/?tab=findings"><ShieldCheck size={20} /><strong>{metrics ? metrics.finding_statuses.confirmed ?? 0 : "—"}</strong><span>已确认漏洞</span></a>
      <a href="/tasks/"><Activity size={20} /><strong>{counts.active}</strong><span>挖掘中任务</span></a>
      <a href="/tasks/"><CircleAlert size={20} /><strong>{counts.needsAttention}</strong><span>需处理任务</span></a>
    </section>
    <nav className="mining-flow" aria-label="漏洞挖掘流程">
      <a href="/assets/"><span>01</span><strong>资产与范围</strong><small>确定目标和授权边界</small></a>
      <a href="/tasks/"><span>02</span><strong>探索与执行</strong><small>查看方向、Worker 与工具</small></a>
      <a href="/findings/?tab=findings"><span>03</span><strong>漏洞与证据</strong><small>复核线索，绑定证据</small></a>
      <a href="/findings/?tab=report"><span>04</span><strong>复测与报告</strong><small>追踪结论并导出报告</small></a>
    </nav>
    <section className="workspace-card mining-findings" id="mining-findings"><div className="section-row"><div><h2>待复核漏洞线索</h2><p>优先展示高风险线索；点击进入所属任务核查证据</p></div><span className="state-tag subtle">{metrics ? metrics.finding_statuses.draft ?? 0 : "—"} 条</span></div>
      {loading ? <p>正在加载…</p> : reviewQueue.length ? <div className="simple-list">{reviewQueue.map(item => <a className="mining-finding-row" key={item.id} href={`/findings/?task=${item.task_id}&tab=findings`}><span className={`phase5-severity ${item.severity}`}>{severityNames[item.severity] || item.severity}</span><strong>{item.title}</strong><small>{item.task_title}</small><ArrowRight size={15} /></a>)}</div> : <p className="empty-small">目前没有待复核线索。新的漏洞草稿会出现在这里。</p>}
    </section>
    <div className="dashboard-grid">
      <section className="workspace-card"><div className="section-row"><div><h2>挖掘任务</h2><p>最近更新的授权任务</p></div><a className="workspace-link" href="/tasks/">全部任务 <ArrowRight size={15} /></a></div>
        {loading ? <p>正在加载…</p> : tasks.length === 0 ? <div className="empty-small">尚无挖掘任务。<a href="/tasks/?new=1">创建第一个任务</a></div> : <div className="simple-list">{tasks.slice(0, 8).map(task => <a className="dashboard-task" key={task.id} href={`/tasks/?id=${encodeURIComponent(task.id)}`}><span className="state-tag subtle">{names[task.status] || task.status}</span><strong>{task.title}</strong><small>{new Date(task.updated_at).toLocaleString("zh-CN")}</small><ArrowRight size={15} /></a>)}</div>}
      </section>
      <div className="dashboard-side">
        <section className="workspace-card"><h2>漏洞记录</h2><p className="mining-risk-summary"><strong>{metrics ? highRisk : "—"}</strong><span>高危及严重记录，含待复核线索</span></p><div className="dashboard-pills">{Object.entries(severityNames).map(([key, label]) => <span key={key}>{label} {metrics?.findings[key] ?? 0}</span>)}</div><a className="workspace-link" href="/findings/?tab=findings">查看漏洞与证据 <ArrowRight size={15} /></a></section>
        <section className="workspace-card"><h2>运行状态</h2><p>活跃 Worker：<strong>{metrics?.active_workers ?? "—"}</strong></p><p>输入 / 输出 Token：<strong>{metrics ? `${metrics.input_tokens.toLocaleString()} / ${metrics.output_tokens.toLocaleString()}` : "—"}</strong></p><p>模型健康：<strong>{metrics ? Object.entries(metrics.models).map(([key, count]) => `${key} ${count}`).join(" · ") || "尚未配置" : "—"}</strong></p></section>
        <section className="workspace-card"><h2>服务状态</h2><p>{health?.status === "ok" ? "关键服务可用" : "服务需要检查"}</p><div className="simple-list">{Object.entries(health?.checks || {}).map(([key, value]) => <div key={key}><strong>{key}</strong><span className={`state-tag ${value.status === "up" ? "subtle" : "danger"}`}>{value.status === "up" ? "正常" : "异常"}</span>{value.detail && <small>{value.detail}</small>}</div>)}</div>{updated && <small>更新于 {updated.toLocaleTimeString("zh-CN")}</small>}</section>
      </div>
    </div>
    <section className="workspace-card dashboard-activity"><div className="section-row"><div><h2>最近挖掘动态</h2><p>工具、规划和证据事件的任务记录</p></div><button className="workspace-secondary" type="button" onClick={() => void refresh()}><RefreshCw size={15} />刷新</button></div><div className="simple-list">{metrics?.recent_activity.map((item, index) => <a className="dashboard-task" href={`/tasks/?id=${item.task_id}`} key={`${item.task_id}-${index}`}><span className="state-tag subtle">{item.kind}</span><strong>{item.summary}</strong><small>{new Date(item.occurred_at).toLocaleString("zh-CN")}</small><ArrowRight size={15} /></a>)}{metrics && !metrics.recent_activity.length ? <p className="empty-small">暂无挖掘动态</p> : null}</div></section>
  </WorkspaceShell>;
}
