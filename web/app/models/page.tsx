"use client";

import { ArrowDown, ArrowUp, KeyRound, Plus, RefreshCw, Save, Sparkles, Trash2 } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import WorkspaceShell from "../../components/workspace-shell";
import { api, type User } from "../../lib/api";

type Profile = {
  id: string; name: string; format: string; base_url: string; model: string;
  credential_ref: string | null; credential_hint: string | null;
  priority: number; enabled: boolean; stream: boolean; context_window: number;
  max_output_tokens: number; rate_per_second: number; rate_per_minute: number;
  health_state: string; circuit_state: string; cooldown_until: string | null;
};
type Prompt = { role: string; version: number; body: string; content_hash: string };
type Form = {
  name: string; format: string; base_url: string; model: string; key: string;
  priority: number; context_window: number; max_output_tokens: number;
  rate_per_second: number; rate_per_minute: number; stream: boolean; enabled: boolean;
};
const emptyForm: Form = {
  name: "", format: "openai-chat", base_url: "https://api.openai.com/v1", model: "", key: "",
  priority: 100, context_window: 128000, max_output_tokens: 8192,
  rate_per_second: 2, rate_per_minute: 60, stream: true, enabled: true,
};
const labels: Record<string, string> = { "openai-chat": "OpenAI Chat", "openai-responses": "OpenAI Responses", anthropic: "Anthropic Messages" };
const health: Record<string, string> = { healthy: "可用", degraded: "降级", open_circuit: "熔断冷却", half_open: "恢复探测", disabled: "已禁用" };
const roleLabels: Record<string, string> = { decomposer: "目标拆解", planner: "规划器", worker: "执行者" };

export default function ModelsPage() {
  const [user, setUser] = useState<User | null>(null);
  const [profiles, setProfiles] = useState<Profile[]>([]);
  const [chain, setChain] = useState<string[]>([]);
  const [form, setForm] = useState<Form>({ ...emptyForm });
  const [editing, setEditing] = useState<string | null>(null);
  const [role, setRole] = useState("planner");
  const [prompt, setPrompt] = useState<Prompt | null>(null);
  const [promptBody, setPromptBody] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const admin = user?.role === "admin";

  const refresh = useCallback(async () => {
    try {
      const [who, items, route] = await Promise.all([
        api<User>("/me"), api<Profile[]>("/llm-profiles"), api<{ profiles: string[] }>("/llm-default-chain"),
      ]);
      setUser(who); setProfiles(items); setChain(route.profiles);
    } catch (cause) { setError(cause instanceof Error ? cause.message : "读取模型配置失败"); }
  }, []);
  useEffect(() => { void refresh(); }, [refresh]);
  useEffect(() => {
    if (!admin) return;
    api<Prompt>(`/agent-prompts/${role}`).then((item) => { setPrompt(item); setPromptBody(item.body); }).catch((cause) => setError(cause instanceof Error ? cause.message : "读取提示模板失败"));
  }, [role, admin]);
  function edit(item: Profile) {
    setEditing(item.id);
    setForm({ name: item.name, format: item.format, base_url: item.base_url, model: item.model, key: "", priority: item.priority,
      context_window: item.context_window, max_output_tokens: item.max_output_tokens, rate_per_second: item.rate_per_second,
      rate_per_minute: item.rate_per_minute, stream: item.stream, enabled: item.enabled });
    setError(""); setNotice("");
    window.scrollTo({ top: 0, behavior: "smooth" });
  }
  async function save(event: React.FormEvent) {
    event.preventDefault(); setBusy(true); setError(""); setNotice("");
    try {
      if (!editing && !form.key.trim()) throw new Error("请填写 API Key");
      const { key, ...fields } = form;
      const payload = { ...fields, ...(key ? { credential: { secret_value: key } } : {}) };
      await api<Profile>(editing ? `/llm-profiles/${editing}` : "/llm-profiles", { method: editing ? "PATCH" : "POST", body: JSON.stringify(payload) });
      setForm({ ...emptyForm }); setEditing(null); setNotice("模型配置已保存；密钥不会在页面回显。");
      await refresh();
    } catch (cause) { setError(cause instanceof Error ? cause.message : "保存失败"); }
    finally { setBusy(false); }
  }
  async function remove(id: string) {
    if (!window.confirm("删除此模型配置？已被主备链、任务或历史运行引用的配置无法删除，可以先停用。")) return;
    setBusy(true); setError("");
    try { await api<void>(`/llm-profiles/${id}`, { method: "DELETE" }); await refresh(); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "删除失败"); }
    finally { setBusy(false); }
  }
  async function saveChain() {
    setBusy(true); setError("");
    try { await api("/llm-default-chain", { method: "PUT", body: JSON.stringify({ profiles: chain }) }); setNotice("默认主备链已保存。新任务会按这个顺序选择模型。") }
    catch (cause) { setError(cause instanceof Error ? cause.message : "保存主备链失败"); }
    finally { setBusy(false); }
  }
  function move(index: number, by: number) {
    const next = [...chain]; const target = index + by;
    if (target < 0 || target >= next.length) return;
    [next[index], next[target]] = [next[target], next[index]]; setChain(next);
  }
  async function savePrompt() {
    setBusy(true); setError("");
    try { const item = await api<Prompt>(`/agent-prompts/${role}`, { method: "PUT", body: JSON.stringify({ body: promptBody }) }); setPrompt(item); setNotice(`${roleLabels[role]}模板已更新到 v${item.version}。`); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "保存模板失败"); }
    finally { setBusy(false); }
  }
  return (
    <WorkspaceShell section="models">
      <header className="workspace-header">
        <div><span className="workspace-eyebrow">工作台 / 模型与 Agent</span><h1>模型与 Agent</h1>
          <p>配置独立密钥、主备顺序和角色提示模板。每个 Profile 独立计量、限速与熔断。</p></div>
        <div className="workspace-header-actions"><button type="button" className="workspace-secondary" onClick={() => void refresh()}><RefreshCw size={16} />刷新</button></div>
      </header>
      {error && <div className="form-error workspace-alert" role="alert">{error}</div>}
      {notice && <div className="form-success workspace-alert" role="status">{notice}</div>}
      <div className="model-layout">
        <section className="workspace-card">
          <div className="card-heading"><div><h2>模型 Profile</h2><p>相同模型的不同 Key 请分别建成 Profile。</p></div><Sparkles size={20} /></div>
          {profiles.length === 0 ? <div className="workspace-empty"><KeyRound size={28} /><h3>还没有模型配置</h3><p>管理员填写右侧信息后，即可建立主备链。</p></div> :
            <div className="model-profile-list">{profiles.map((item) => <article key={item.id} className="model-profile">
              <div><strong>{item.name}</strong><span className="model-badge">{health[item.health_state] || item.health_state}</span></div>
              <p>{labels[item.format] || item.format} · {item.model}</p>
              <small>{item.base_url} · Key 尾号 {item.credential_hint || "未设置"}</small>
              {item.cooldown_until && <small>冷却至 {new Date(item.cooldown_until).toLocaleString("zh-CN")}</small>}
              {admin && <div className="model-actions"><button type="button" className="text-button" onClick={() => edit(item)}>编辑 / 换 Key</button><button type="button" className="text-button" disabled={busy} onClick={() => void remove(item.id)}><Trash2 size={14} />删除</button></div>}
            </article>)}</div>}
        </section>
        {admin && <form className="workspace-card model-form" onSubmit={(event) => void save(event)}>
          <div className="card-heading"><div><h2>{editing ? "编辑模型配置" : "添加模型配置"}</h2><p>{editing ? "留空 Key 会保留原凭据；换服务主机需要新 Key。" : "Key 加密保存在服务端，只显示尾号。"}</p></div><KeyRound size={20} /></div>
          <div className="form-grid">
            <label>名称<input required maxLength={120} value={form.name} onChange={(event) => setForm({ ...form, name: event.target.value })} placeholder="primary-key-a" /></label>
            <label>接口格式<select value={form.format} onChange={(event) => { const next = event.target.value; const defaultURL = form.format === "anthropic" ? "https://api.anthropic.com/v1" : "https://api.openai.com/v1"; setForm({ ...form, format: next, base_url: form.base_url === defaultURL ? (next === "anthropic" ? "https://api.anthropic.com/v1" : "https://api.openai.com/v1") : form.base_url }); }}><option value="openai-chat">OpenAI Chat</option><option value="openai-responses">OpenAI Responses</option><option value="anthropic">Anthropic Messages</option></select></label>
            <label className="full">Base URL<input required type="url" value={form.base_url} onChange={(event) => setForm({ ...form, base_url: event.target.value })} placeholder="https://gateway.example.com/v1" /></label>
            <label>模型名称<input required value={form.model} onChange={(event) => setForm({ ...form, model: event.target.value })} placeholder="模型 ID" /></label>
            <label>API Key<input type="password" autoComplete="new-password" value={form.key} onChange={(event) => setForm({ ...form, key: event.target.value })} placeholder={editing ? "留空则保留" : "请输入 API Key"} /></label>
            <label>优先级<input type="number" value={form.priority} onChange={(event) => setForm({ ...form, priority: Number(event.target.value) })} /></label>
            <label>上下文窗口<input required type="number" min={1024} value={form.context_window} onChange={(event) => setForm({ ...form, context_window: Number(event.target.value) })} /></label>
            <label>最大输出 Token<input required type="number" min={1} value={form.max_output_tokens} onChange={(event) => setForm({ ...form, max_output_tokens: Number(event.target.value) })} /></label>
            <label>每秒请求上限<input required type="number" min={1} value={form.rate_per_second} onChange={(event) => setForm({ ...form, rate_per_second: Number(event.target.value) })} /></label>
            <label>每分钟请求上限<input required type="number" min={1} value={form.rate_per_minute} onChange={(event) => setForm({ ...form, rate_per_minute: Number(event.target.value) })} /></label>
          </div>
          <div className="model-checks"><label><input type="checkbox" checked={form.enabled} onChange={(event) => setForm({ ...form, enabled: event.target.checked })} /> 启用</label><label><input type="checkbox" checked={form.stream} onChange={(event) => setForm({ ...form, stream: event.target.checked })} /> 流式响应</label></div>
          <div className="form-actions"><button className="workspace-primary" type="submit" disabled={busy}><Save size={16} />保存配置</button>{editing && <button type="button" className="workspace-secondary" onClick={() => { setEditing(null); setForm({ ...emptyForm }); }}>取消编辑</button>}</div>
        </form>}
      </div>
      <section className="workspace-card model-chain"><div className="card-heading"><div><h2>系统默认主备链</h2><p>从上到下依次尝试；配额、认证和服务故障会自动切换到下一项。</p></div><Plus size={20} /></div>
        {admin && <div className="model-chain-add"><select aria-label="选择要加入主备链的 Profile" id="chain-profile" defaultValue=""><option value="">选择模型配置</option>{profiles.filter((p) => !chain.includes(p.id) && p.enabled).map((p) => <option key={p.id} value={p.id}>{p.name} · {p.model}</option>)}</select><button type="button" className="workspace-secondary" onClick={() => { const input = document.getElementById("chain-profile") as HTMLSelectElement; if (input.value) { setChain([...chain, input.value]); input.value = ""; } }}>加入链</button></div>}
        {chain.length === 0 ? <p className="muted">暂无默认链。添加至少一个启用的模型配置后保存。</p> : <ol className="model-chain-list">{chain.map((id, index) => { const item = profiles.find((p) => p.id === id); return <li key={id}><span className="model-order">{index + 1}</span><div><strong>{item?.name || "已删除的配置"}</strong><small>{item?.model || id}</small></div>{admin && <div className="model-actions"><button type="button" aria-label="上移" disabled={index === 0} onClick={() => move(index, -1)}><ArrowUp size={16} /></button><button type="button" aria-label="下移" disabled={index === chain.length - 1} onClick={() => move(index, 1)}><ArrowDown size={16} /></button><button type="button" aria-label="移除" onClick={() => setChain(chain.filter((value) => value !== id))}><Trash2 size={16} /></button></div>}</li>; })}</ol>}
        {admin && <div className="form-actions"><button type="button" className="workspace-primary" disabled={busy} onClick={() => void saveChain()}><Save size={16} />保存主备链</button></div>}
      </section>
      {admin && <section className="workspace-card model-prompt"><div className="card-heading"><div><h2>Agent 角色提示模板</h2><p>编辑角色正文。范围、工具写回与证据要求由系统附加，不可删除；每次运行记录版本和内容哈希。</p></div><Sparkles size={20} /></div>
        <label>角色<select value={role} onChange={(event) => setRole(event.target.value)}>{Object.entries(roleLabels).map(([key, value]) => <option key={key} value={key}>{value}</option>)}</select></label>
        <label>模板正文<textarea rows={7} value={promptBody} onChange={(event) => setPromptBody(event.target.value)} /></label>
        <div className="form-actions"><button type="button" className="workspace-primary" disabled={busy || !promptBody.trim()} onClick={() => void savePrompt()}><Save size={16} />保存新版本</button><span className="muted">当前 v{prompt?.version ?? "…"}</span></div>
      </section>}
    </WorkspaceShell>
  );
}
