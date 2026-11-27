"use client";

import { useCallback, useEffect, useState } from "react";
import { CircleAlert, RefreshCw, UploadCloud, Wrench } from "lucide-react";
import WorkspaceShell from "../../components/workspace-shell";
import { api, type User } from "../../lib/api";

type Skill = { id: string; manifest: { name: string; version: string; description: string; agents: string[]; mcps: string[] }; content_hash: string; enabled: boolean };
type MCPServer = { id: string; name: string; transport: string; endpoint?: string; command?: string; args?: string[]; enabled: boolean };
type MCPTool = { name: string; description: string; schema_hash: string; enabled: boolean };
type CustomTool = { id: string; name: string; description: string; url_template: string; method: string; enabled: boolean };
type Approval = { id: string; task_id: string; tool_name: string; status: string; requested_at: string };

function csrfToken() { return document.cookie.split("; ").find((part) => part.startsWith("tianfa_csrf="))?.split("=")[1] || ""; }

export default function ToolsPage() {
  const [viewer, setViewer] = useState<User | null>(null);
  const [bindings, setBindings] = useState<Record<string, boolean>>({});
  const [skills, setSkills] = useState<Skill[]>([]);
  const [servers, setServers] = useState<MCPServer[]>([]);
  const [mcpTools, setMcpTools] = useState<Record<string, MCPTool[]>>({});
  const [custom, setCustom] = useState<CustomTool[]>([]);
  const [approvals, setApprovals] = useState<Approval[]>([]);
  const [transport, setTransport] = useState("stdio");
  const [serverName, setServerName] = useState("");
  const [endpoint, setEndpoint] = useState("");
  const [command, setCommand] = useState("");
  const [args, setArgs] = useState("");
  const [customName, setCustomName] = useState("");
  const [customURL, setCustomURL] = useState("");
  const [customSchema, setCustomSchema] = useState('{"type":"object","properties":{}}');
  const [customMethod, setCustomMethod] = useState("GET");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const reload = useCallback(async () => {
    try {
      const [me, nextBindings, nextSkills, nextServers, nextTools, nextCustom] = await Promise.all([
        api<User>("/me"), api<Record<string, boolean>>("/tools"), api<Skill[]>("/skills"), api<MCPServer[]>("/mcp-servers"), api<Record<string, MCPTool[]>>("/mcp-tools"), api<CustomTool[]>("/custom-tools"),
      ]);
      setViewer(me); setBindings(nextBindings); setSkills(nextSkills); setServers(nextServers); setMcpTools(nextTools); setCustom(nextCustom);
      setApprovals(me.role === "admin" ? await api<Approval[]>("/approvals") : []);
      setError("");
    } catch (cause) { setError(cause instanceof Error ? cause.message : "加载工具失败"); }
  }, []);
  useEffect(() => { void reload(); }, [reload]);

  async function act(action: () => Promise<unknown>, success: string) {
    if (["工具已关闭","工具已启用","Skill 状态已更新","服务状态已更新","MCP 工具状态已更新","自定义工具状态已更新"].includes(success) && !window.confirm("更改工具或服务策略？本组织后续 Worker 调用将受到影响。")) return;
    setBusy(true); setError(""); setNotice("");
    try { await action(); setNotice(success); await reload(); }
    catch (cause) { setError(cause instanceof Error ? cause.message : "操作失败"); }
    finally { setBusy(false); }
  }
  async function upload(file?: File) {
    if (!file) return;
    await act(async () => {
      const response = await fetch("/api/v1/skills", { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/zip", "X-CSRF-Token": csrfToken() }, body: file });
      if (!response.ok) { const problem = await response.json().catch(() => ({})); throw new Error(problem.detail || `上传失败 (${response.status})`); }
    }, "Skill 已安装；Worker 只会先看到名称和简述。");
  }
  const isAdmin = viewer?.role === "admin";
  return <WorkspaceShell section="tools">
    <header className="workspace-header"><div><span className="workspace-eyebrow">工作台 / 工具</span><h1>工具与 Skill</h1><p>配置受控工具、延迟加载 Skill 和 MCP 服务。</p></div><div className="workspace-header-actions"><button className="workspace-secondary" type="button" onClick={() => void reload()}><RefreshCw size={16} />刷新</button></div></header>
    {error && <div className="form-error workspace-alert" role="alert"><CircleAlert size={17} />{error}</div>}
    {notice && <div className="form-hint" role="status">{notice}</div>}
    <div className="tool-admin-grid">
      <section className="workspace-card"><div className="section-row"><div><h2>内置工具</h2><p>Worker 调用前经过校验、策略判断、输出捕获与审计。</p></div><Wrench size={20} /></div><div className="simple-list tool-binding-list">{Object.entries(bindings).map(([name, enabled]) => <div key={name}><strong>{name}</strong><small>{name === "http.read" ? "仅 GET/HEAD；自动记录请求与响应" : name === "command.run" ? "隔离容器；无网络，需配置镜像" : name === "skill.load" ? "按需加载 Skill 内容" : name === "finding.report" ? "依据真实观察创建漏洞草稿" : "任务工作区内执行"}</small>{isAdmin && <button type="button" className="workspace-secondary" disabled={busy} onClick={() => void act(() => api(`/tools/${encodeURIComponent(name)}/binding`, { method: "PUT", body: JSON.stringify({ enabled: !enabled }) }), enabled ? "工具已关闭" : "工具已启用")}>{enabled ? "已启用 · 关闭" : "已关闭 · 启用"}</button>}</div>)}</div></section>
      <section className="workspace-card"><h2>Skill</h2><p>仅安装摘要进入默认上下文；Worker 调用 skill.load 后读取全文。</p>{isAdmin && <label className="tool-upload"><UploadCloud size={20} />上传 Skill ZIP<input type="file" accept=".zip,application/zip" disabled={busy} onChange={(event) => { void upload(event.target.files?.[0]); event.target.value = ""; }} /></label>}<div className="simple-list">{skills.length ? skills.map((item) => <div key={item.id}><strong>{item.manifest.name} · v{item.manifest.version}</strong><small>{item.manifest.description}</small><small>适用：{item.manifest.agents?.join("、") || "未声明"} · 哈希 {item.content_hash.slice(0, 12)}</small>{item.manifest.mcps?.length > 0 && <small>延迟解锁 MCP：{item.manifest.mcps.join("、")}</small>}{isAdmin && <button type="button" className="workspace-secondary" disabled={busy} onClick={() => void act(() => api(`/skills/${item.id}`, { method: "PUT", body: JSON.stringify({ enabled: !item.enabled }) }), "Skill 状态已更新")}>{item.enabled ? "已启用 · 关闭" : "已关闭 · 启用"}</button>}</div>) : <p className="empty-small">尚未安装 Skill</p>}</div></section>
      <section className="workspace-card"><h2>MCP 服务</h2><p>服务默认关闭。发现工具后逐项启用；Skill 加载前其工具不会交给 Worker。</p>{isAdmin && <form className="workspace-form" onSubmit={(event) => { event.preventDefault(); void act(async () => { await api("/mcp-servers", { method: "POST", body: JSON.stringify({ name: serverName.trim(), transport, endpoint: transport === "streamable_http" ? endpoint.trim() : "", command: transport === "stdio" ? command.trim() : "", args: transport === "stdio" ? args.trim().split(/\s+/).filter(Boolean) : [] }) }); setServerName(""); setEndpoint(""); setCommand(""); setArgs(""); }, "MCP 服务已登记，默认关闭"); }}><label>服务名<input value={serverName} onChange={(event) => setServerName(event.target.value)} required placeholder="例如：browser" /></label><label>传输<select value={transport} onChange={(event) => setTransport(event.target.value)}><option value="stdio">stdio（隔离容器）</option><option value="streamable_http">Streamable HTTP</option></select></label>{transport === "stdio" ? <><label>容器镜像<input value={command} onChange={(event) => setCommand(event.target.value)} required placeholder="已安装的 MCP 镜像" /></label><label>启动参数<input value={args} onChange={(event) => setArgs(event.target.value)} placeholder="可选，以空格分隔" /></label></> : <label>HTTPS 端点<input value={endpoint} onChange={(event) => setEndpoint(event.target.value)} required placeholder="https://mcp.example.com/mcp" /></label>}<button type="submit" className="workspace-primary" disabled={busy}>添加 MCP 服务</button></form>}<div className="simple-list">{servers.map((server) => <div key={server.id}><strong>{server.name}</strong><small>{server.transport === "stdio" ? `容器 ${server.command}` : server.endpoint}</small>{isAdmin && <div className="workspace-actions"><button type="button" className="workspace-secondary" disabled={busy} onClick={() => void act(() => api(`/mcp-servers/${server.name}/discover`, { method: "POST" }), "工具发现已更新；Schema 变化的工具会自动关闭")}>发现工具</button><button type="button" className="workspace-secondary" disabled={busy} onClick={() => void act(() => api(`/mcp-servers/${server.name}`, { method: "PUT", body: JSON.stringify({ enabled: !server.enabled }) }), "服务状态已更新")}>{server.enabled ? "关闭服务" : "启用服务"}</button></div>}{mcpTools[server.name]?.map((item) => <div className="tool-subitem" key={item.name}><strong>{item.name}</strong><small>{item.description}</small>{isAdmin && <button type="button" className="text-button" disabled={busy} onClick={() => void act(() => api(`/mcp-servers/${server.name}/tools/${encodeURIComponent(item.name)}`, { method: "PUT", body: JSON.stringify({ enabled: !item.enabled }) }), "MCP 工具状态已更新")}>{item.enabled ? "已启用 · 关闭" : "已关闭 · 启用"}</button>}</div>)}</div>)}</div></section>
      <section className="workspace-card"><h2>自定义 HTTP 工具</h2><p>当前支持只读 GET/HEAD，目标在保存和每次执行时校验。</p>{isAdmin && <form className="workspace-form" onSubmit={(event) => { event.preventDefault(); void act(async () => { await api("/custom-tools", { method: "POST", body: JSON.stringify({ name: customName.trim(), description: "自定义只读 HTTP 工具", url_template: customURL.trim(), method: customMethod, input_schema: JSON.parse(customSchema) }) }); setCustomName(""); setCustomURL(""); }, "自定义工具已登记，默认关闭"); }}><label>工具名<input value={customName} onChange={(event) => setCustomName(event.target.value)} required placeholder="例如：headers" /></label><label>URL 模板<input value={customURL} onChange={(event) => setCustomURL(event.target.value)} required placeholder="https://example.com/path/{{id}}" /></label><label>方法<select value={customMethod} onChange={(event) => setCustomMethod(event.target.value)}><option>GET</option><option>HEAD</option></select></label><label>输入 JSON Schema<textarea value={customSchema} onChange={(event) => setCustomSchema(event.target.value)} rows={4} spellCheck={false} /></label><button type="submit" className="workspace-primary" disabled={busy}>添加自定义工具</button></form>}<div className="simple-list">{custom.map((item) => <div key={item.id}><strong>custom__{item.name}</strong><small>{item.method} {item.url_template}</small>{isAdmin && <button type="button" className="workspace-secondary" disabled={busy} onClick={() => void act(() => api(`/custom-tools/${item.name}`, { method: "PUT", body: JSON.stringify({ enabled: !item.enabled }) }), "自定义工具状态已更新")}>{item.enabled ? "已启用 · 关闭" : "已关闭 · 启用"}</button>}</div>)}</div></section>
      {isAdmin && <section className="workspace-card"><h2>审批队列</h2><p>审批决定和原因会写入审计；可从任务详情处理待审调用。</p><div className="simple-list">{approvals.filter((item) => item.status === "pending").map((item) => <div key={item.id}><strong>{item.tool_name}</strong><small>任务 {item.task_id} · {new Date(item.requested_at).toLocaleString("zh-CN")}</small><a className="workspace-link" href={`/tasks/?id=${item.task_id}`}>进入任务审批</a></div>)}</div></section>}
    </div>
  </WorkspaceShell>;
}
