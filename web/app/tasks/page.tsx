"use client";

import {
  ArrowLeft,
  ArrowRight,
  CircleAlert,
  Network,
  Plus,
  RefreshCw,
  ShieldCheck,
} from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import WorkspaceShell from "../../components/workspace-shell";
import {
  api,
  type Coverage,
  type Detail,
  type Graph,
  type Task,
  type User,
} from "../../lib/api";

type ScopeDraft = {
  effect: "allow" | "deny";
  kind: string;
  value: string;
  ports: string;
};
const initialScope: ScopeDraft = {
  effect: "allow",
  kind: "domain",
  value: "",
  ports: "",
};
const kinds = [
  { value: "domain", label: "域名" },
  { value: "ip", label: "IP" },
  { value: "cidr", label: "CIDR 网段" },
  { value: "url_prefix", label: "URL 前缀" },
  { value: "service", label: "服务 host:port" },
];
const statusName: Record<string, string> = {
  draft: "草稿",
  queued: "排队中",
  running: "运行中",
  paused: "已暂停",
  completed: "已完成",
  failed: "失败",
  cancelled: "已取消",
};
type ModelOption = { id: string; name: string; model: string; enabled: boolean };
type Intent = { id: string; objective: string; success_criteria: string; status: string; blocking_reason?: string; stop_reason?: string; attempt: number; priority: number };
type Todo = { id: string; title: string; detail: string; status: string; depends_on: string[]; intent_id?: string };
type TraceHit = { run_id: string; intent_id: string; ordinal: number; role: string; excerpt: string; created_at: string };
type AgentRun = { id: string; role: string; status: string; prompt_version: number; error_class?: string; started_at: string };
type RunDetail = { run: AgentRun; messages: { ordinal: number; role: string; trust_level: string; content: string }[]; attempts: { profile_id: string; status: string; error_class?: string; input_tokens: number; output_tokens: number; estimated: boolean }[] };
type ToolCall = { id: string; name: string; input: Record<string, unknown>; status: string; summary?: string; policy_reason?: string; output?: { preview?: string; bytes?: number; remote_ip?: string }; scope_version: number; created_at: string };
type Approval = { id: string; tool_call_id: string; task_id: string; tool_name: string; status: string; reason?: string; requested_at: string };
const agentRoles: Record<string,string> = { decomposer: "目标拆解", planner: "规划器", worker: "执行者" };
const runStatuses: Record<string,string> = { running: "运行中", succeeded: "成功", blocked: "待恢复", failed: "失败", interrupted: "运行中断", paused: "已暂停", exhausted: "回合或期限已到", stopped: "已停止" };
const intentStatuses: Record<string,string> = { open: "等待领取", leased: "已领取", running: "执行中", done: "已完成", blocked: "待处理", exhausted: "回合或期限已到", paused: "已暂停", stopped: "已停止" };
const modelErrors: Record<string,string> = { model_unavailable: "模型暂时不可用", transport: "连接失败", quota: "配额已用尽", rate_limit: "请求限流", auth: "凭据无效", model_not_found: "模型不存在", stream_interrupted: "响应流中断" };
const toolStatuses: Record<string,string> = { queued: "排队中", policy_check: "等待审批", running: "执行中", succeeded: "成功", failed: "失败", blocked: "策略阻断", cancelled: "已取消" };

function safeToolInput(call: ToolCall): string {
  if (call.name === "http.read" && typeof call.input.url === "string") {
    try { const url = new URL(call.input.url); return `${String(call.input.method || "GET")} ${url.origin}${url.pathname}${url.search ? "?参数已隐藏" : ""}`; }
    catch { return "URL 格式无效"; }
  }
  if (call.name.startsWith("workspace.")) return typeof call.input.name === "string" ? `文件：${call.input.name}` : "工作区文件";
  if (call.name === "command.run") return "隔离命令（参数已隐藏）";
  return "输入已隐藏";
}

function parsePorts(value: string): number[] {
  return value
    .split(",")
    .map((part) => part.trim())
    .filter(Boolean)
    .map(Number);
}
function scopePayload(entries: ScopeDraft[]) {
  return entries.map((entry) => ({
    effect: entry.effect,
    kind: entry.kind,
    value: entry.value.trim(),
    ports: parsePorts(entry.ports),
  }));
}

function TaskWorkspace() {
  const [tasks, setTasks] = useState<Task[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [detail, setDetail] = useState<Detail | null>(null);
  const [graph, setGraph] = useState<Graph | null>(null);
  const [coverage, setCoverage] = useState<Coverage | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [goals, setGoals] = useState([""]);
  const [scope, setScope] = useState<ScopeDraft[]>([{ ...initialScope }]);
  const [constraint, setConstraint] = useState("");
  const [newGoal, setNewGoal] = useState("");
  const [newConstraint, setNewConstraint] = useState("");
  const [workerLimit, setWorkerLimit] = useState(3);
  const [policyMode, setPolicyMode] = useState("autonomous");
  const [editingScope, setEditingScope] = useState(false);
  const [editScope, setEditScope] = useState<ScopeDraft[]>([]);
  const [models, setModels] = useState<ModelOption[]>([]);
  const [taskChain, setTaskChain] = useState<string[]>([]);
  const [defaultChain, setDefaultChain] = useState<string[]>([]);
  const [agentRoutes, setAgentRoutes] = useState<Record<string,string[]>>({ decomposer: [], planner: [], worker: [] });
  const [routeRole, setRouteRole] = useState("planner");
  const [intents, setIntents] = useState<Intent[]>([]);
  const [runs, setRuns] = useState<AgentRun[]>([]);
  const [todos, setTodos] = useState<Todo[]>([]);
  const [todoTitle, setTodoTitle] = useState("");
  const [todoDetail, setTodoDetail] = useState("");
  const [todoDeps, setTodoDeps] = useState<string[]>([]);
  const [traceQuery, setTraceQuery] = useState("");
  const [traces, setTraces] = useState<TraceHit[]>([]);
  const [intentAction, setIntentAction] = useState<{ id: string; name: "steer" | "stop" } | null>(null);
  const [actionMessage, setActionMessage] = useState("");
  const [runDetail, setRunDetail] = useState<RunDetail | null>(null);
  const [toolCalls, setToolCalls] = useState<ToolCall[]>([]);
  const [toolBindings, setToolBindings] = useState<Record<string, boolean>>({});
  const [approvals, setApprovals] = useState<Approval[]>([]);
  const [viewer, setViewer] = useState<User | null>(null);
  const [approvalReason, setApprovalReason] = useState("");

  const refresh = useCallback(async () => {
    setLoading(true);
    try {
      setTasks(await api<Task[]>("/tasks?limit=100"));
      setError("");
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "加载任务失败");
    } finally {
      setLoading(false);
    }
  }, []);
  const open = useCallback(async (id: string) => {
    setSelected(id);
    setCreating(false);
    setEditingScope(false);
    setRunDetail(null);
    setIntentAction(null);
    setError("");
    window.history.replaceState(
      null,
      "",
      `/tasks/?id=${encodeURIComponent(id)}`,
    );
    try {
      const [nextDetail, nextGraph, nextCoverage, nextModels, nextChain, nextDefault, nextRoutes, nextIntents, nextRuns, nextTodos, nextCalls, nextBindings, nextApprovals] = await Promise.all([
        api<Detail>(`/tasks/${id}`),
        api<Graph>(`/tasks/${id}/graph`),
        api<Coverage>(`/tasks/${id}/coverage`),
        api<ModelOption[]>("/llm-profiles"),
        api<{ profiles: string[] }>(`/tasks/${id}/llm-chain`),
        api<{ profiles: string[] }>("/llm-default-chain"),
        Promise.all(["decomposer","planner","worker"].map((role) => api<{ profiles: string[] }>(`/tasks/${id}/agent-routes/${role}`))),
        api<Intent[]>(`/tasks/${id}/intents`),
        api<AgentRun[]>(`/tasks/${id}/agent-runs`),
        api<Todo[]>(`/tasks/${id}/todos`),
        api<ToolCall[]>(`/tasks/${id}/tool-calls`),
        api<Record<string, boolean>>("/tools"),
        api<Approval[]>("/approvals").catch(() => []),
      ]);
      setDetail(nextDetail);
      setGraph(nextGraph);
      setCoverage(nextCoverage);
      setModels(nextModels); setTaskChain(nextChain.profiles); setDefaultChain(nextDefault.profiles);
      setAgentRoutes({ decomposer: nextRoutes[0].profiles, planner: nextRoutes[1].profiles, worker: nextRoutes[2].profiles });
      setIntents(nextIntents); setRuns(nextRuns);
      setTodos(nextTodos);
      setToolCalls(nextCalls); setToolBindings(nextBindings); setApprovals(nextApprovals.filter((item) => item.task_id === id));
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "加载任务详情失败");
    }
  }, []);
  useEffect(() => {
    void refresh();
    void api<User>("/me").then(setViewer).catch(() => {});
    const params = new URLSearchParams(window.location.search);
    const id = params.get("id");
    if (id) void open(id);
    else if (params.get("new") === "1") setCreating(true);
  }, [refresh, open]);

  useEffect(() => {
    if (!selected || !detail || !["queued", "running"].includes(detail.task.status)) return;
    const timer = window.setInterval(() => {
      void Promise.all([
        api<Detail>(`/tasks/${selected}`),
        api<Intent[]>(`/tasks/${selected}/intents`),
        api<AgentRun[]>(`/tasks/${selected}/agent-runs`),
        api<Todo[]>(`/tasks/${selected}/todos`),
        api<ToolCall[]>(`/tasks/${selected}/tool-calls`),
        api<Approval[]>("/approvals").catch(() => []),
      ]).then(([next, nextIntents, nextRuns, nextTodos, nextCalls, nextApprovals]) => {
        setDetail(next); setIntents(nextIntents); setRuns(nextRuns); setTodos(nextTodos);
        setToolCalls(nextCalls); setApprovals(nextApprovals.filter((item) => item.task_id === selected));
      }).catch(() => {});
    }, 3000);
    return () => window.clearInterval(timer);
  }, [selected, detail?.task.status]);

  function resetForm() {
    setTitle("");
    setDescription("");
    setGoals([""]);
    setScope([{ ...initialScope }]);
    setConstraint("");
    setWorkerLimit(3);
    setPolicyMode("autonomous");
  }
  function newTask() {
    resetForm();
    setCreating(true);
    setSelected(null);
    setDetail(null);
    setError("");
    window.history.replaceState(null, "", "/tasks/?new=1");
  }
  function back() {
    setCreating(false);
    setSelected(null);
    setDetail(null);
    setGraph(null);
    setCoverage(null);
    setError("");
    window.history.replaceState(null, "", "/tasks/");
    void refresh();
  }
  async function create(event: React.FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      const entries = scopePayload(scope);
      if (
        goals.some((goal) => !goal.trim()) ||
        entries.some(
          (entry) =>
            !entry.value || entry.ports.some((port) => !Number.isInteger(port)),
        )
      )
        throw new Error("请填写完整目标、范围和有效端口");
      const result = await api<Detail>("/tasks", {
        method: "POST",
        body: JSON.stringify({
          title: title.trim(),
          description: description.trim(),
          goals: goals.map((text) => ({ text: text.trim(), priority: 5 })),
          scope: { entries },
          constraints: constraint.trim()
            ? [{ effect: "deny", text: constraint.trim() }]
            : [],
          execution: {
            policy_mode: policyMode,
            worker_limit: workerLimit,
          },
        }),
      });
      await refresh();
      await open(result.task.id);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "创建失败");
    } finally {
      setBusy(false);
    }
  }
  async function saveScope(event: React.FormEvent) {
    event.preventDefault();
    if (!detail) return;
    setBusy(true);
    setError("");
    try {
      const result = await api<Detail>(`/tasks/${detail.task.id}/scope`, {
        method: "PUT",
        body: JSON.stringify({
          version: detail.task.version,
          entries: scopePayload(editScope),
        }),
      });
      setDetail(result);
      setEditingScope(false);
      await refresh();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "范围保存失败");
    } finally {
      setBusy(false);
    }
  }
  async function start() {
    if (!detail) return;
    setBusy(true);
    setError("");
    try {
      await api(`/tasks/${detail.task.id}:start`, {
        method: "POST",
        body: JSON.stringify({ version: detail.task.version }),
      });
      await open(detail.task.id);
    } catch (cause) {
      const message = cause instanceof Error ? cause.message : "启动失败";
      await open(detail.task.id);
      setError(message);
    } finally {
      setBusy(false);
    }
  }
  async function advance() {
    if (!detail) return;
    setBusy(true); setError("");
    try { await api(`/tasks/${detail.task.id}:advance`, { method: "POST" }); await open(detail.task.id); }
    catch (cause) { const message = cause instanceof Error ? cause.message : "继续规划失败"; await open(detail.task.id); setError(message); }
    finally { setBusy(false); }
  }
  async function command(name: "pause" | "resume" | "cancel") {
    if (!detail) return;
    setBusy(true); setError("");
    try { await api(`/tasks/${detail.task.id}:${name}`, { method: "POST" }); await open(detail.task.id); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "操作失败"); }
    finally { setBusy(false); }
  }
  async function intentCommand(id: string, name: "steer" | "stop" | "retry") {
    if (!detail) return;
    const message = actionMessage.trim();
    if (name !== "retry" && !message) return;
    setBusy(true); setError("");
    try { await api(`/tasks/${detail.task.id}/intents/${id}:${name}`, { method: "POST", ...(name === "retry" ? {} : { body: JSON.stringify(name === "steer" ? { message } : { reason: message }) }) }); setIntentAction(null); setActionMessage(""); await open(detail.task.id); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "方向操作失败"); }
    finally { setBusy(false); }
  }
  async function addTodo() {
    if (!detail || !todoTitle.trim()) return;
    setBusy(true); setError("");
    try { await api(`/tasks/${detail.task.id}/todos`, { method: "POST", body: JSON.stringify({ title: todoTitle.trim(), detail: todoDetail.trim(), depends_on: todoDeps }) }); setTodoTitle(""); setTodoDetail(""); setTodoDeps([]); await open(detail.task.id); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "添加步骤失败"); }
    finally { setBusy(false); }
  }
  async function completeTodo(id: string) {
    if (!detail) return;
    setBusy(true); setError("");
    try { await api(`/tasks/${detail.task.id}/todos/${id}:complete`, { method: "POST" }); await open(detail.task.id); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "完成步骤失败"); }
    finally { setBusy(false); }
  }
  async function searchTraces() {
    if (!detail) return;
    try { setTraces(await api<TraceHit[]>(`/tasks/${detail.task.id}/worker-traces?q=${encodeURIComponent(traceQuery.trim())}&limit=30`)); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "搜索记录失败"); }
  }
  async function showRun(id: string) {
    try { setRunDetail(await api<RunDetail>(`/agent-runs/${id}`)); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "读取运行记录失败"); }
  }
  async function decideApproval(id: string, approve: boolean) {
    if (!detail) return;
    setBusy(true); setError("");
    try {
      await api(`/approvals/${id}:${approve ? "approve" : "reject"}`, { method: "POST", body: JSON.stringify({ reason: approvalReason.trim() }) });
      setApprovalReason("");
      await open(detail.task.id);
    } catch (cause) { setError(cause instanceof Error ? cause.message : "审批失败"); }
    finally { setBusy(false); }
  }
  async function toggleTool(name: string, enabled: boolean) {
    if (!detail) return;
    setBusy(true); setError("");
    try {
      await api(`/tools/${encodeURIComponent(name)}/binding`, { method: "PUT", body: JSON.stringify({ enabled }) });
      setToolBindings({ ...toolBindings, [name]: enabled });
    } catch (cause) { setError(cause instanceof Error ? cause.message : "工具设置失败"); }
    finally { setBusy(false); }
  }
  async function saveTaskChain() {
    if (!detail) return;
    setBusy(true); setError("");
    try {
      await api(`/tasks/${detail.task.id}/llm-chain`, { method: "PUT", body: JSON.stringify({ profiles: taskChain }) });
      await open(detail.task.id);
    } catch (cause) { setError(cause instanceof Error ? cause.message : "保存任务模型链失败"); }
    finally { setBusy(false); }
  }
  async function saveAgentRoute() {
    if (!detail) return;
    setBusy(true); setError("");
    try { await api(`/tasks/${detail.task.id}/agent-routes/${routeRole}`, { method: "PUT", body: JSON.stringify({ profiles: agentRoutes[routeRole] || [] }) }); await open(detail.task.id); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "保存角色子链失败"); }
    finally { setBusy(false); }
  }
  async function addItem(
    kind: "goals" | "constraints",
    text: string,
    event: React.FormEvent,
  ) {
    event.preventDefault();
    if (!detail) return;
    setBusy(true);
    setError("");
    try {
      await api<Detail>(`/tasks/${detail.task.id}/${kind}`, {
        method: "POST",
        body: JSON.stringify(
          kind === "goals"
            ? { version: detail.task.version, text, priority: 5 }
            : { version: detail.task.version, text, effect: "deny" },
        ),
      });
      if (kind === "goals") setNewGoal("");
      else setNewConstraint("");
      await open(detail.task.id);
      await refresh();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "保存失败");
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <header className="workspace-header">
        <div>
          <span className="workspace-eyebrow">工作台 / 任务</span>
          <h1>
            {creating ? "创建验证任务" : selected ? "任务详情" : "验证任务"}
          </h1>
          <p>
            {creating
              ? "先写清授权目标与范围；创建后可以检查双图血缘。"
              : selected
                ? "查看目标、范围、资产锚点和探索血缘。"
                : "管理授权验证任务，从草稿开始。"}
          </p>
        </div>
        <div className="workspace-header-actions">
          {(creating || selected) && (
            <button
              className="workspace-secondary"
              onClick={back}
              type="button"
            >
              <ArrowLeft size={16} />
              返回列表
            </button>
          )}
          {!creating && !selected && (
            <button
              className="workspace-primary"
              onClick={newTask}
              type="button"
            >
              <Plus size={17} />
              新建任务
            </button>
          )}
        </div>
      </header>
      {error && (
        <div className="form-error workspace-alert" role="alert">
          <CircleAlert size={17} />
          {error}
        </div>
      )}
      {creating ? (
        <form className="task-form" onSubmit={(event) => void create(event)}>
          <section className="workspace-card">
            <div className="card-heading">
              <span className="step-pill">01</span>
              <div>
                <h2>任务目标</h2>
                <p>每个目标应有明确可验证的结果。</p>
              </div>
            </div>
            <div className="form-grid">
              <label className="full">
                任务名称
                <input
                  required
                  maxLength={200}
                  value={title}
                  onChange={(event) => setTitle(event.target.value)}
                  placeholder="例如：测试环境访问控制验证"
                />
              </label>
              <label className="full">
                任务说明
                <textarea
                  value={description}
                  onChange={(event) => setDescription(event.target.value)}
                  rows={3}
                  placeholder="说明授权背景、验证目的和需要关注的系统"
                />
              </label>
            </div>
            <div className="field-label">验证目标</div>
            {goals.map((goal, index) => (
              <div className="inline-fields" key={index}>
                <input
                  required
                  value={goal}
                  onChange={(event) =>
                    setGoals(
                      goals.map((item, position) =>
                        position === index ? event.target.value : item,
                      ),
                    )
                  }
                  placeholder={`目标 ${index + 1}：例如确认是否存在未授权访问`}
                  aria-label={`验证目标 ${index + 1}`}
                />
                {goals.length > 1 && (
                  <button
                    type="button"
                    className="text-button"
                    onClick={() =>
                      setGoals(
                        goals.filter((_, position) => position !== index),
                      )
                    }
                  >
                    移除
                  </button>
                )}
              </div>
            ))}
            <button
              type="button"
              className="add-row"
              onClick={() => setGoals([...goals, ""])}
            >
              <Plus size={15} />
              添加目标
            </button>
          </section>
          <section className="workspace-card">
            <div className="card-heading">
              <span className="step-pill">02</span>
              <div>
                <h2>授权范围</h2>
                <p>至少添加一条允许范围；拒绝范围会优先生效。</p>
              </div>
            </div>
            <ScopeEditor entries={scope} onChange={setScope} />
            <button
              type="button"
              className="add-row"
              onClick={() => setScope([...scope, { ...initialScope }])}
            >
              <Plus size={15} />
              添加范围
            </button>
            <div className="form-hint">
              <ShieldCheck size={15} />
              域名支持 *.example.com；URL 前缀应包含 http:// 或 https://。
            </div>
          </section>
          <section className="workspace-card">
            <div className="card-heading">
              <span className="step-pill">03</span>
              <div>
                <h2>执行约束</h2>
                <p>设置禁止事项、工具审批模式和任务并发上限。</p>
              </div>
            </div>
            <div className="form-grid">
              <label className="full">
                禁止事项
                <textarea
                  value={constraint}
                  onChange={(event) => setConstraint(event.target.value)}
                  rows={2}
                  placeholder="例如：禁止压力测试或修改生产数据"
                />
              </label>
              <label>
                策略模式
                <select
                  value={policyMode}
                  onChange={(event) => setPolicyMode(event.target.value)}
                >
                  <option value="autonomous">自主执行</option>
                  <option value="graded">分级审批</option>
                  <option value="approval">全部审批</option>
                </select>
              </label>
              <label>
                任务并发上限
                <input
                  type="number"
                  min={1}
                  max={50}
                  value={workerLimit}
                  onChange={(event) =>
                    setWorkerLimit(Number(event.target.value))
                  }
                />
              </label>
            </div>
            <div className="form-hint">
              任务会先保存为草稿。启动前可在详情页选择模型链；留空则使用系统默认链。
            </div>
          </section>
          <div className="form-actions">
            <button
              type="button"
              className="workspace-secondary"
              onClick={back}
            >
              取消
            </button>
            <button type="submit" className="workspace-primary" disabled={busy}>
              {busy ? "正在创建…" : "创建草稿"}
              <ArrowRight size={17} />
            </button>
          </div>
        </form>
      ) : selected ? (
        detail ? (
          <div className="detail-grid">
            <div className="detail-primary">
              <section className="workspace-card task-summary">
                <div className="task-title-line">
                  <span className="state-tag">
                    {statusName[detail.task.status] || detail.task.status}
                  </span>
                  <span className="muted">版本 {detail.task.version}</span>
                </div>
                <h2>{detail.task.title}</h2>
                <p>{detail.task.description || "暂无任务说明"}</p>
                <div className="task-meta">
                  <span>策略：{detail.task.policy_mode}</span>
                  <span>并发上限：{detail.task.worker_limit}</span>
                  <span>
                    创建于{" "}
                    {new Date(detail.task.created_at).toLocaleString("zh-CN")}
                  </span>
                </div>
                <div className="form-hint">启动后进入持久队列，页面会自动刷新进度。工具调用会逐次校验授权范围并留下记录。</div>
                {detail.task.status === "draft" && <button
                  type="button"
                  className="workspace-secondary"
                  onClick={() => void start()}
                  disabled={busy}
                >
                  {busy ? "正在排队…" : "启动任务"}
                </button>}
                {detail.task.status === "running" && <div className="workspace-actions"><button type="button" className="workspace-secondary" disabled={busy} onClick={() => void advance()}>唤醒规划器</button><button type="button" className="workspace-secondary" disabled={busy} onClick={() => void command("pause")}>暂停任务</button><button type="button" className="text-button" disabled={busy} onClick={() => void command("cancel")}>取消任务</button></div>}
                {detail.task.status === "paused" && <div className="workspace-actions"><button type="button" className="workspace-secondary" disabled={busy} onClick={() => void command("resume")}>恢复任务</button><button type="button" className="text-button" disabled={busy} onClick={() => void command("cancel")}>取消任务</button></div>}
              </section>
              <section className="workspace-card">
                <div className="section-row"><div><h2>任务模型链</h2><p>留空继承系统默认链。明确设置时按列表顺序尝试。</p></div></div>
                {detail.task.status === "draft" ? <div className="model-chain-add"><select aria-label="添加任务模型" id="task-chain-profile" defaultValue=""><option value="">选择模型</option>{models.filter((item) => item.enabled && !taskChain.includes(item.id)).map((item) => <option key={item.id} value={item.id}>{item.name} · {item.model}</option>)}</select><button type="button" className="workspace-secondary" onClick={() => { const element = document.getElementById("task-chain-profile") as HTMLSelectElement; if (element.value) { setTaskChain([...taskChain, element.value]); element.value = ""; } }}>加入</button></div> : null}
                {taskChain.length ? <ol className="model-chain-list">{taskChain.map((id, index) => <li key={id}><span className="model-order">{index + 1}</span><strong>{models.find((item) => item.id === id)?.name || id}</strong>{detail.task.status === "draft" && <button type="button" className="text-button" onClick={() => setTaskChain(taskChain.filter((value) => value !== id))}>移除</button>}</li>)}</ol> : <p className="muted">使用系统默认主备链</p>}
                {detail.task.status === "draft" && <button type="button" className="workspace-secondary" disabled={busy} onClick={() => void saveTaskChain()}>保存模型链</button>}
              </section>
              <section className="workspace-card">
                <div className="section-row"><div><h2>Agent 角色子链</h2><p>从任务链中选择该角色使用的模型；留空继承任务链。</p></div></div>
                <label className="model-role-picker">角色<select value={routeRole} onChange={(event) => setRouteRole(event.target.value)}><option value="decomposer">目标拆解</option><option value="planner">规划器</option><option value="worker">执行者</option></select></label>
                {detail.task.status === "draft" && <div className="model-chain-add"><select aria-label="添加角色模型" id="agent-route-profile" defaultValue=""><option value="">选择模型</option>{(taskChain.length ? taskChain : defaultChain).filter((id) => !agentRoutes[routeRole]?.includes(id)).map((id) => { const item = models.find((entry) => entry.id === id); return <option key={id} value={id}>{item?.name || id}</option>; })}</select><button type="button" className="workspace-secondary" onClick={() => { const element = document.getElementById("agent-route-profile") as HTMLSelectElement; if (element.value) { setAgentRoutes({ ...agentRoutes, [routeRole]: [...(agentRoutes[routeRole] || []), element.value] }); element.value = ""; } }}>加入</button></div>}
                {agentRoutes[routeRole]?.length ? <ol className="model-chain-list">{agentRoutes[routeRole].map((id,index) => <li key={id}><span className="model-order">{index + 1}</span><strong>{models.find((item) => item.id === id)?.name || id}</strong>{detail.task.status === "draft" && <button type="button" className="text-button" onClick={() => setAgentRoutes({ ...agentRoutes, [routeRole]: agentRoutes[routeRole].filter((value) => value !== id) })}>移除</button>}</li>)}</ol> : <p className="muted">继承任务主备链</p>}
                {detail.task.status === "draft" && <button type="button" className="workspace-secondary" disabled={busy} onClick={() => void saveAgentRoute()}>保存角色子链</button>}
              </section>
              <section className="workspace-card">
                <h2>共享步骤</h2><p>按依赖顺序完成验证步骤；前置步骤未完成时，下游方向不会领取。</p>
                <div className="simple-list">{todos.map((item) => <div key={item.id}><span className="state-tag subtle">{item.status === "done" ? "已完成" : "待完成"}</span><strong>{item.title}</strong>{item.detail && <small>{item.detail}</small>}{item.depends_on.length > 0 && <small>前置步骤：{item.depends_on.map((id) => todos.find((entry) => entry.id === id)?.title || id).join("、")}</small>}{item.status === "open" && !item.intent_id && item.depends_on.every((id) => todos.find((entry) => entry.id === id)?.status === "done") && <button type="button" className="text-button" disabled={busy} onClick={() => void completeTodo(item.id)}>标记完成</button>}</div>)}</div>
                {["draft","queued","running","paused"].includes(detail.task.status) && <div className="workspace-form"><label>新步骤<input value={todoTitle} onChange={(event) => setTodoTitle(event.target.value)} placeholder="例如：验证授权站点首页" maxLength={300} /></label><label>说明<input value={todoDetail} onChange={(event) => setTodoDetail(event.target.value)} placeholder="可选：完成判据或上下文" maxLength={4000} /></label>{todos.filter((item) => item.status === "open").length > 0 && <label>依赖步骤<select aria-label="选择依赖步骤" value="" onChange={(event) => { if (event.target.value && !todoDeps.includes(event.target.value)) setTodoDeps([...todoDeps,event.target.value]); }}><option value="">选择后添加依赖</option>{todos.filter((item) => !todoDeps.includes(item.id)).map((item) => <option key={item.id} value={item.id}>{item.title}</option>)}</select></label>}{todoDeps.length > 0 && <small>已选：{todoDeps.map((id) => todos.find((entry) => entry.id === id)?.title || id).join("、")} <button type="button" className="text-button" onClick={() => setTodoDeps([])}>清空</button></small>}<button type="button" className="workspace-secondary" disabled={busy || !todoTitle.trim()} onClick={() => void addTodo()}>添加步骤</button></div>}
              </section>
              {(intents.length > 0 || runs.length > 0) && <section className="workspace-card">
                <h2>Agent 运行与验证方向</h2><p>模型规划和真实观察分开记录。</p>
                <div className="simple-list">{intents.map((item) => <div key={item.id}>
                  <span className="state-tag subtle">{intentStatuses[item.status] || item.status}</span><strong>{item.objective}</strong><small>{item.blocking_reason || item.stop_reason || item.success_criteria}</small><small>优先级 {item.priority} · 尝试 {item.attempt} 次</small>
                  <div className="workspace-actions">
                    {["open","leased","running","blocked"].includes(item.status) && <button type="button" className="text-button" disabled={busy} onClick={() => { setIntentAction({ id: item.id, name: "steer" }); setActionMessage(""); }}>纠偏</button>}
                    {["open","leased","running","blocked","exhausted","paused"].includes(item.status) && <button type="button" className="text-button" disabled={busy} onClick={() => { setIntentAction({ id: item.id, name: "stop" }); setActionMessage(""); }}>停止</button>}
                    {["blocked","exhausted","stopped"].includes(item.status) && detail.task.status === "running" && <button type="button" className="text-button" disabled={busy} onClick={() => void intentCommand(item.id,"retry")}>重新运行</button>}
                  </div>
                </div>)}</div>
                {intentAction && <div className="run-transcript"><h3>{intentAction.name === "steer" ? "纠偏执行者" : "停止执行方向"}</h3><p>{intentAction.name === "steer" ? "此指令会在下一次模型动作前加入上下文。" : "请说明停止原因，便于后续审计。"}</p><textarea aria-label={intentAction.name === "steer" ? "纠偏指令" : "停止原因"} value={actionMessage} onChange={(event) => setActionMessage(event.target.value)} maxLength={4000} rows={3} placeholder={intentAction.name === "steer" ? "例如：先验证授权域名的响应，不要扩展范围" : "例如：该方向与现有结果重复"} /><div className="workspace-actions"><button type="button" className="workspace-secondary" disabled={busy || !actionMessage.trim()} onClick={() => void intentCommand(intentAction.id,intentAction.name)}>确认</button><button type="button" className="text-button" onClick={() => setIntentAction(null)}>取消</button></div></div>}
                <div className="simple-list">{runs.map((item) => <div key={item.id}><span className="state-tag subtle">{runStatuses[item.status] || item.status}</span><strong>{agentRoles[item.role] || item.role} · Prompt v{item.prompt_version}</strong><small>{item.error_class ? (modelErrors[item.error_class] || item.error_class) : new Date(item.started_at).toLocaleString("zh-CN")}</small><button type="button" className="text-button" onClick={() => void showRun(item.id)}>查看记录</button></div>)}</div>
                {runDetail && <div className="run-transcript"><div className="section-row"><h3>{agentRoles[runDetail.run.role] || runDetail.run.role} 运行记录</h3><button type="button" className="text-button" onClick={() => setRunDetail(null)}>收起</button></div><p>模型尝试：{runDetail.attempts.map((item) => `${runStatuses[item.status] || item.status}${item.error_class ? ` (${modelErrors[item.error_class] || item.error_class})` : ""} · ${item.input_tokens}/${item.output_tokens} Token${item.estimated ? "（估算）" : ""}`).join("；") || "暂无"}</p>{runDetail.messages.map((item) => <div key={item.ordinal}><strong>{item.role}{item.trust_level === "untrusted_observation" ? " · 不可信观察" : ""}</strong><pre>{item.content}</pre></div>)}</div>}
              </section>}
              <section className="workspace-card"><h2>执行记录搜索</h2><p>搜索本任务已持久化的执行者消息，点击结果可查看完整记录。</p><div className="workspace-actions"><input aria-label="搜索执行记录" value={traceQuery} onChange={(event) => setTraceQuery(event.target.value)} onKeyDown={(event) => { if (event.key === "Enter") void searchTraces(); }} placeholder="输入关键词" maxLength={200} /><button type="button" className="workspace-secondary" onClick={() => void searchTraces()}>搜索</button></div><div className="simple-list">{traces.map((hit) => <div key={`${hit.run_id}-${hit.ordinal}`}><strong>{hit.role} · {new Date(hit.created_at).toLocaleString("zh-CN")}</strong><small>{hit.excerpt}</small><button type="button" className="text-button" onClick={() => void showRun(hit.run_id)}>查看完整记录</button></div>)}</div></section>
              <section className="workspace-card">
                <div className="section-row"><div><h2>工具调用</h2><p>查看授权判断、执行结果与完整输出。</p></div><span className="state-tag subtle">{toolCalls.length} 次</span></div>
                {toolCalls.length === 0 ? <p className="empty-small">当前任务还没有工具调用</p> : <div className="simple-list">{toolCalls.map((call) => <div key={call.id}>
                  <span className={`state-tag ${call.status === "blocked" ? "danger" : "subtle"}`}>{toolStatuses[call.status] || call.status}</span>
                  <strong>{call.name}</strong><small>{safeToolInput(call)}</small>
                  <small>Scope v{call.scope_version} · {new Date(call.created_at).toLocaleString("zh-CN")}</small>
                  {(call.policy_reason || call.summary) && <small>{call.policy_reason || call.summary}</small>}
                  {call.output?.remote_ip && <small>实际连接 IP：{call.output.remote_ip}</small>}
                  {call.output?.preview && <details><summary>查看输出片段</summary><pre>{call.output.preview}</pre></details>}
                  {call.status === "succeeded" && <a className="workspace-link" href={`/api/v1/tool-calls/${call.id}/output`} target="_blank" rel="noopener noreferrer">下载完整输出</a>}
                </div>)}</div>}
              </section>
              {approvals.some((item) => item.status === "pending") && <section className="workspace-card"><h2>待审批工具</h2><p>仅管理员可以批准会写入或执行命令的调用。</p><div className="simple-list">{approvals.filter((item) => item.status === "pending").map((item) => <div key={item.id}><strong>{item.tool_name}</strong><small>工具调用 {item.tool_call_id}</small>{viewer?.role === "admin" && <><input aria-label="审批说明" value={approvalReason} onChange={(event) => setApprovalReason(event.target.value)} placeholder="拒绝时填写原因" maxLength={4000} /><div className="workspace-actions"><button type="button" className="workspace-secondary" disabled={busy} onClick={() => void decideApproval(item.id, true)}>批准</button><button type="button" className="text-button" disabled={busy || !approvalReason.trim()} onClick={() => void decideApproval(item.id, false)}>拒绝</button></div></>}</div>)}</div></section>}
              {viewer?.role === "admin" && <section className="workspace-card"><h2>Worker 工具</h2><p>关闭后，后续运行无法调用该工具。</p><div className="simple-list">{Object.entries(toolBindings).map(([name, enabled]) => <div key={name}><strong>{name}</strong><button type="button" className="workspace-secondary" disabled={busy} onClick={() => void toggleTool(name, !enabled)}>{enabled ? "已启用 · 点击关闭" : "已关闭 · 点击启用"}</button></div>)}</div></section>}
              <section className="workspace-card">
                <div className="section-row">
                  <div>
                    <h2>验证目标</h2>
                    <p>每个目标连接到任务根节点。</p>
                  </div>
                </div>
                <div className="simple-list">
                  {detail.goals.map((goal) => (
                    <div key={goal.id}>
                      <span className="state-tag subtle">{goal.state}</span>
                      <strong>{goal.text}</strong>
                    </div>
                  ))}
                </div>
                {detail.task.status === "completed" && <p className="form-hint">添加新目标会重新排队，保留此前的运行与证据。</p>}
                {["draft","completed"].includes(detail.task.status) && (
                  <form
                    className="inline-add-form"
                    onSubmit={(event) => void addItem("goals", newGoal, event)}
                  >
                    <input
                      aria-label="新目标"
                      value={newGoal}
                      onChange={(event) => setNewGoal(event.target.value)}
                      required
                      placeholder="添加可验证的目标"
                    />
                    <button
                      className="workspace-secondary"
                      disabled={busy}
                      type="submit"
                    >
                      <Plus size={15} />
                      添加
                    </button>
                  </form>
                )}
              </section>
              <section className="workspace-card">
                <div className="section-row">
                  <div>
                    <h2>授权范围</h2>
                    <p>
                      版本 {detail.task.scope_version} ·{" "}
                      {
                        detail.scope.filter((item) => item.effect === "allow")
                          .length
                      }{" "}
                      条允许
                    </p>
                  </div>
                  {["draft", "running", "paused"].includes(detail.task.status) && (
                    <button
                      type="button"
                      className="workspace-secondary"
                      onClick={() => {
                        setEditScope(
                          detail.scope.map((entry) => ({
                            effect: entry.effect,
                            kind: entry.kind,
                            value: entry.value,
                            ports: entry.ports.join(","),
                          })),
                        );
                        setEditingScope(true);
                      }}
                    >
                      编辑
                    </button>
                  )}
                </div>
                {editingScope ? (
                  <form onSubmit={(event) => void saveScope(event)}>
                    <ScopeEditor entries={editScope} onChange={setEditScope} />
                    <button
                      type="button"
                      className="add-row"
                      onClick={() =>
                        setEditScope([...editScope, { ...initialScope }])
                      }
                    >
                      <Plus size={15} />
                      添加范围
                    </button>
                    <div className="form-actions">
                      <button
                        type="button"
                        className="workspace-secondary"
                        onClick={() => setEditingScope(false)}
                      >
                        取消
                      </button>
                      <button
                        type="submit"
                        className="workspace-primary"
                        disabled={busy}
                      >
                        保存范围
                      </button>
                    </div>
                  </form>
                ) : (
                  <div className="simple-list">
                    {detail.scope.map((entry) => (
                      <div key={entry.id}>
                        <span
                          className={`state-tag ${entry.effect === "deny" ? "danger" : ""}`}
                        >
                          {entry.effect === "allow" ? "允许" : "拒绝"}
                        </span>
                        <strong>{entry.normalized_value}</strong>
                        <small>{entry.kind}</small>
                      </div>
                    ))}
                  </div>
                )}
              </section>
              <section className="workspace-card">
                <h2>执行约束</h2>
                <div className="simple-list">
                  {detail.constraints.length ? (
                    detail.constraints.map((item) => (
                      <div key={item.id}>
                        <span className="state-tag danger">
                          {item.effect === "deny" ? "禁止" : "允许"}
                        </span>
                        <strong>{item.text}</strong>
                      </div>
                    ))
                  ) : (
                    <p className="empty-small">尚未添加额外约束</p>
                  )}
                </div>
                {detail.task.status === "draft" && (
                  <form
                    className="inline-add-form"
                    onSubmit={(event) =>
                      void addItem("constraints", newConstraint, event)
                    }
                  >
                    <input
                      aria-label="新禁止事项"
                      value={newConstraint}
                      onChange={(event) => setNewConstraint(event.target.value)}
                      required
                      placeholder="添加禁止事项"
                    />
                    <button
                      className="workspace-secondary"
                      disabled={busy}
                      type="submit"
                    >
                      <Plus size={15} />
                      添加
                    </button>
                  </form>
                )}
              </section>
            </div>
            <aside className="detail-side">
              <section className="workspace-card">
                <div className="section-row">
                  <div>
                    <h2>图覆盖</h2>
                    <p>根据已持久化的节点和锚点计算</p>
                  </div>
                  <Network size={20} />
                </div>
                <div className="metric-grid">
                  <div>
                    <strong>{coverage?.goal_count ?? "—"}</strong>
                    <span>目标</span>
                  </div>
                  <div>
                    <strong>{coverage?.proven_goal_count ?? "—"}</strong>
                    <span>已证明</span>
                  </div>
                  <div>
                    <strong>{coverage?.asset_count ?? "—"}</strong>
                    <span>关联资产</span>
                  </div>
                  <div>
                    <strong>{coverage?.anchored_assets ?? "—"}</strong>
                    <span>已锚定</span>
                  </div>
                </div>
              </section>
              <section className="workspace-card">
                <h2>探索血缘</h2>
                <p className="muted">
                  从根事实延伸到目标；后续运行产生的事实会继续写入这里。
                </p>
                <ExplorationGraph graph={graph} />
              </section>
              <a href="/assets/" className="workspace-link">
                查看全局资产 <ArrowRight size={15} />
              </a>
            </aside>
          </div>
        ) : (
          <div className="workspace-loading">正在加载任务…</div>
        )
      ) : (
        <section className="workspace-card">
          <div className="section-row">
            <div>
              <h2>全部任务</h2>
              <p>最近创建的任务优先显示</p>
            </div>
            <button
              type="button"
              className="workspace-secondary"
              onClick={() => void refresh()}
            >
              <RefreshCw size={15} />
              刷新
            </button>
          </div>
          {loading ? (
            <div className="workspace-loading">正在加载任务…</div>
          ) : tasks.length === 0 ? (
            <div className="workspace-empty">
              <Network size={30} />
              <h3>还没有验证任务</h3>
              <p>先创建一个授权范围明确的草稿任务。</p>
              <button
                type="button"
                className="workspace-primary"
                onClick={newTask}
              >
                <Plus size={16} />
                新建任务
              </button>
            </div>
          ) : (
            <div className="task-list">
              {tasks.map((item) => (
                <button
                  type="button"
                  onClick={() => void open(item.id)}
                  key={item.id}
                >
                  <span className="state-tag">
                    {statusName[item.status] || item.status}
                  </span>
                  <span className="task-list-copy">
                    <strong>{item.title}</strong>
                    <small>{item.description || "暂无说明"}</small>
                  </span>
                  <span className="task-list-date">
                    {new Date(item.updated_at).toLocaleDateString("zh-CN")}
                  </span>
                  <ArrowRight size={17} />
                </button>
              ))}
            </div>
          )}
        </section>
      )}
    </>
  );
}

function ExplorationGraph({ graph }: { graph: Graph | null }) {
  if (!graph) return <div className="workspace-loading">正在加载探索图…</div>;
  const nodes = new Map(graph.nodes.map((node) => [node.id, node]));
  const parents = new Map(graph.edges.map((edge) => [edge.to_node_id, edge]));
  const anchors = new Map<string, Graph["anchors"]>();
  for (const anchor of graph.anchors)
    anchors.set(anchor.node_id, [
      ...(anchors.get(anchor.node_id) || []),
      anchor,
    ]);
  const names: Record<string, string> = {
    origin: "根事实",
    goal: "目标",
    intent: "探索方向",
    fact: "事实",
    finding: "安全问题",
    hint: "线索",
    digest: "摘要",
  };
  return (
    <>
      <div className="graph-list">
        {graph.nodes.map((node) => {
          const parent = parents.get(node.id);
          const parentNode = parent ? nodes.get(parent.from_node_id) : null;
          return (
            <div className="graph-entry" key={node.id}>
              {parentNode && (
                <div className="graph-parent">
                  ↳ {names[parentNode.kind] || parentNode.kind} · {parent?.kind}
                </div>
              )}
              <div className="graph-node">
                <span className="graph-kind">
                  {names[node.kind] || node.kind}
                </span>
                <strong>{node.summary || "任务根事实"}</strong>
                <small>{node.state}</small>
                {anchors.get(node.id)?.map((anchor) => (
                  <span className="graph-anchor" key={anchor.asset_id}>
                    资产 · {anchor.asset_kind}: {anchor.asset_value}
                  </span>
                ))}
              </div>
            </div>
          );
        })}
      </div>
      <div className="form-hint">
        {graph.edges.length} 条血缘边 · {graph.anchors.length} 个资产锚点
      </div>
    </>
  );
}

function ScopeEditor({
  entries,
  onChange,
}: {
  entries: ScopeDraft[];
  onChange: (entries: ScopeDraft[]) => void;
}) {
  function update(index: number, patch: Partial<ScopeDraft>) {
    onChange(
      entries.map((entry, position) =>
        position === index ? { ...entry, ...patch } : entry,
      ),
    );
  }
  return (
    <div className="scope-editor">
      {entries.map((entry, index) => (
        <div className="scope-row" key={index}>
          <label>
            规则
            <select
              value={entry.effect}
              onChange={(event) =>
                update(index, {
                  effect: event.target.value as ScopeDraft["effect"],
                })
              }
            >
              <option value="allow">允许</option>
              <option value="deny">拒绝</option>
            </select>
          </label>
          <label>
            类型
            <select
              value={entry.kind}
              onChange={(event) => update(index, { kind: event.target.value })}
            >
              {kinds.map((kind) => (
                <option key={kind.value} value={kind.value}>
                  {kind.label}
                </option>
              ))}
            </select>
          </label>
          <label className="scope-value">
            范围
            <input
              required
              value={entry.value}
              onChange={(event) => update(index, { value: event.target.value })}
              placeholder={
                entry.kind === "url_prefix"
                  ? "https://example.com/app/"
                  : entry.kind === "service"
                    ? "host.example.com:443"
                    : "example.com"
              }
            />
          </label>
          <label>
            端口
            <input
              value={entry.ports}
              onChange={(event) => update(index, { ports: event.target.value })}
              placeholder="可选，如 80,443"
            />
          </label>
          <button
            type="button"
            className="text-button scope-remove"
            aria-label={`移除范围 ${index + 1}`}
            onClick={() =>
              onChange(entries.filter((_, position) => position !== index))
            }
          >
            移除
          </button>
        </div>
      ))}
    </div>
  );
}

export default function TasksPage() {
  return (
    <WorkspaceShell section="tasks">
      <TaskWorkspace />
    </WorkspaceShell>
  );
}
