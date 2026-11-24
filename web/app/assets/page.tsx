"use client";

import { Boxes, Plus, RefreshCw } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import WorkspaceShell from "../../components/workspace-shell";
import { api, type Asset, type Task } from "../../lib/api";

const kinds = [
  "company",
  "root_domain",
  "subdomain",
  "ip",
  "service",
  "application",
  "endpoint",
];
export default function AssetsPage() {
  const [assets, setAssets] = useState<Asset[]>([]);
  const [tasks, setTasks] = useState<Task[]>([]);
  const [kind, setKind] = useState("");
  const [value, setValue] = useState("");
  const [sourceTask, setSourceTask] = useState("");
  const [parentID, setParentID] = useState("");
  const [filter, setFilter] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [success, setSuccess] = useState("");
  const [loading, setLoading] = useState(true);
  const refresh = useCallback(async (selectedKind: string) => {
    setLoading(true);
    try {
      setAssets(
        await api<Asset[]>(
          `/assets?limit=100&kind=${encodeURIComponent(selectedKind)}`,
        ),
      );
      setError("");
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "加载失败");
    } finally {
      setLoading(false);
    }
  }, []);
  useEffect(() => {
    void refresh("");
    void api<Task[]>("/tasks?limit=100")
      .then(setTasks)
      .catch(() => {});
  }, [refresh]);
  async function create(event: React.FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    setSuccess("");
    try {
      const asset = await api<Asset>("/assets", {
        method: "POST",
        body: JSON.stringify({
          kind,
          value,
          parent_id: parentID || null,
          source_task_id: sourceTask || null,
        }),
      });
      setSuccess(`已保存：${asset.canonical_key}`);
      setValue("");
      await refresh(filter);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "保存资产失败");
    } finally {
      setBusy(false);
    }
  }
  return (
    <WorkspaceShell section="assets">
      <header className="workspace-header">
        <div>
          <span className="workspace-eyebrow">工作台 / 全局资产</span>
          <h1>全局资产</h1>
          <p>同一组织共享资产真值；重复资产会按服务端规范化键合并。</p>
        </div>
      </header>
      {error && (
        <div className="form-error workspace-alert" role="alert">
          {error}
        </div>
      )}
      {success && (
        <div className="form-success" role="status">
          {success}
        </div>
      )}
      <div className="asset-layout">
        <section className="workspace-card">
          <div className="section-row">
            <div>
              <h2>资产清单</h2>
              <p>最近写入的资产</p>
            </div>
            <button
              className="workspace-secondary"
              type="button"
              onClick={() => void refresh(filter)}
            >
              <RefreshCw size={15} />
              刷新
            </button>
          </div>
          <label className="filter-control">
            按类型筛选
            <select
              value={filter}
              onChange={(event) => {
                setFilter(event.target.value);
                void refresh(event.target.value);
              }}
            >
              <option value="">全部类型</option>
              {kinds.map((item) => (
                <option value={item} key={item}>
                  {item}
                </option>
              ))}
            </select>
          </label>
          {loading ? (
            <div className="workspace-loading">正在加载资产…</div>
          ) : assets.length ? (
            <div className="asset-list">
              {assets.map((asset) => (
                <div key={asset.id}>
                  <span className="asset-kind">{asset.kind}</span>
                  <div>
                    <strong>{asset.display_value}</strong>
                    <small>规范化：{asset.canonical_key}</small>
                  </div>
                  <small>
                    {new Date(asset.last_seen_at).toLocaleDateString("zh-CN")}
                  </small>
                </div>
              ))}
            </div>
          ) : (
            <div className="workspace-empty">
              <Boxes size={29} />
              <h3>暂无资产</h3>
              <p>从右侧添加已获授权的资产。</p>
            </div>
          )}
        </section>
        <form
          className="workspace-card asset-create"
          onSubmit={(event) => void create(event)}
        >
          <h2>添加资产</h2>
          <p className="muted">
            可选择来源任务，自动将资产连接到该任务的探索图根节点。
          </p>
          <label>
            资产类型
            <select
              required
              value={kind}
              onChange={(event) => setKind(event.target.value)}
            >
              <option value="">请选择</option>
              {kinds.map((item) => (
                <option value={item} key={item}>
                  {item}
                </option>
              ))}
            </select>
          </label>
          <label>
            资产值
            <input
              required
              value={value}
              onChange={(event) => setValue(event.target.value)}
              placeholder="域名、IP、URL 或服务地址"
            />
          </label>
          <label>
            上级资产（可选）
            <select
              value={parentID}
              onChange={(event) => setParentID(event.target.value)}
            >
              <option value="">无上级</option>
              {assets.map((item) => (
                <option value={item.id} key={item.id}>
                  {item.kind} · {item.display_value}
                </option>
              ))}
            </select>
          </label>
          <label>
            来源任务（可选）
            <select
              value={sourceTask}
              onChange={(event) => setSourceTask(event.target.value)}
            >
              <option value="">不关联任务</option>
              {tasks.map((item) => (
                <option value={item.id} key={item.id}>
                  {item.title}
                </option>
              ))}
            </select>
          </label>
          <button className="workspace-primary" type="submit" disabled={busy}>
            <Plus size={16} />
            {busy ? "保存中…" : "保存资产"}
          </button>
        </form>
      </div>
    </WorkspaceShell>
  );
}
