"use client";

import { Plus, Users } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import WorkspaceShell from "../../components/workspace-shell";
import { api, type User } from "../../lib/api";

export default function UsersPage() {
  const [users, setUsers] = useState<User[]>([]);
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState("operator");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const refresh = useCallback(async () => {
    try {
      setUsers(await api<User[]>("/users"));
      setError("");
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "读取用户失败");
    }
  }, []);
  useEffect(() => {
    void refresh();
  }, [refresh]);
  async function create(event: React.FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api<User>("/users", {
        method: "POST",
        body: JSON.stringify({ email, password, role }),
      });
      setEmail("");
      setPassword("");
      await refresh();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "创建用户失败");
    } finally {
      setBusy(false);
    }
  }
  async function update(id: string, patch: { role?: string; status?: string }) {
    setBusy(true);
    setError("");
    try {
      await api<User>(`/users/${id}`, {
        method: "PATCH",
        body: JSON.stringify(patch),
      });
      await refresh();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "更新用户失败");
    } finally {
      setBusy(false);
    }
  }
  return (
    <WorkspaceShell section="users">
      <header className="workspace-header">
        <div>
          <span className="workspace-eyebrow">工作台 / 用户管理</span>
          <h1>用户管理</h1>
          <p>管理员创建账户并分配角色；停用账户会立即撤销会话。</p>
        </div>
      </header>
      {error && (
        <div className="form-error workspace-alert" role="alert">
          {error}
        </div>
      )}
      <div className="asset-layout">
        <section className="workspace-card">
          <h2>组织成员</h2>
          {users.length ? (
            <div className="asset-list">
              {users.map((user) => (
                <div key={user.id}>
                  <span className="user-avatar">
                    {user.email.slice(0, 1).toUpperCase()}
                  </span>
                  <div>
                    <strong>{user.email}</strong>
                    <small>
                      {user.status === "active" ? "使用中" : "已停用"}
                    </small>
                  </div>
                  <select
                    aria-label={`${user.email} 的角色`}
                    value={user.role}
                    disabled={busy}
                    onChange={(event) =>
                      void update(user.id, { role: event.target.value })
                    }
                  >
                    <option value="admin">admin</option>
                    <option value="operator">operator</option>
                    <option value="reviewer">reviewer</option>
                    <option value="viewer">viewer</option>
                  </select>
                  <button
                    type="button"
                    className="text-button"
                    disabled={busy}
                    onClick={() =>
                      void update(user.id, {
                        status:
                          user.status === "active" ? "disabled" : "active",
                      })
                    }
                  >
                    {user.status === "active" ? "停用" : "启用"}
                  </button>
                </div>
              ))}
            </div>
          ) : (
            <div className="workspace-empty">
              <Users size={29} />
              <h3>尚无成员</h3>
            </div>
          )}
        </section>
        <form
          className="workspace-card asset-create"
          onSubmit={(event) => void create(event)}
        >
          <h2>添加成员</h2>
          <label>
            邮箱
            <input
              type="email"
              autoComplete="off"
              required
              value={email}
              onChange={(event) => setEmail(event.target.value)}
            />
          </label>
          <label>
            初始密码
            <input
              type="password"
              autoComplete="new-password"
              required
              minLength={12}
              value={password}
              onChange={(event) => setPassword(event.target.value)}
            />
            <small>至少 12 字节，建议使用随机密码。</small>
          </label>
          <label>
            角色
            <select
              value={role}
              onChange={(event) => setRole(event.target.value)}
            >
              <option value="operator">operator · 创建任务与资产</option>
              <option value="reviewer">reviewer · 审查与只读</option>
              <option value="viewer">viewer · 只读</option>
              <option value="admin">admin · 完整管理权限</option>
            </select>
          </label>
          <button type="submit" className="workspace-primary" disabled={busy}>
            <Plus size={16} />
            {busy ? "创建中…" : "创建用户"}
          </button>
        </form>
      </div>
    </WorkspaceShell>
  );
}
