"use client";

import { ArrowRight, LockKeyhole, ShieldCheck } from "lucide-react";
import { useState } from "react";
import { api, type User } from "../../lib/api";

export default function LoginPage() {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function submit(event: React.FormEvent) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      await api<User>("/auth/login", {
        method: "POST",
        body: JSON.stringify({ email, password }),
      });
      const next =
        new URLSearchParams(window.location.search).get("next") || "/tasks/";
      window.location.href =
        next.startsWith("/") && !next.startsWith("//") ? next : "/tasks/";
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : "登录失败，请重试");
      setBusy(false);
    }
  }
  return (
    <main className="login-page">
      <section className="login-intro">
        <a href="/" className="login-brand">
          <ShieldCheck size={28} />
          天罚
        </a>
        <div>
          <span className="overline">AUTHORIZED SECURITY VALIDATION</span>
          <h1>
            让每一次安全验证
            <br />
            都有清晰边界与证据。
          </h1>
          <p>从授权范围、目标与约束开始，持续追踪资产与探索血缘。</p>
        </div>
        <small>天罚 · 授权安全验证平台</small>
      </section>
      <section className="login-form-wrap">
        <form className="login-card" onSubmit={(event) => void submit(event)}>
          <div className="login-icon">
            <LockKeyhole size={22} />
          </div>
          <h2>登录工作台</h2>
          <p>使用管理员分配的账户继续。</p>
          <label>
            邮箱
            <input
              autoComplete="username"
              type="email"
              required
              value={email}
              onChange={(event) => setEmail(event.target.value)}
              placeholder="name@example.com"
            />
          </label>
          <label>
            密码
            <input
              autoComplete="current-password"
              type="password"
              required
              value={password}
              onChange={(event) => setPassword(event.target.value)}
              placeholder="输入密码"
            />
          </label>
          {error && (
            <div className="form-error" role="alert">
              {error}
            </div>
          )}
          <button type="submit" className="workspace-primary" disabled={busy}>
            {busy ? "正在登录…" : "登录"}
            <ArrowRight size={17} />
          </button>
          <div className="login-help">
            首次部署请在服务主机运行 <code>tianfa-server init-admin</code>{" "}
            创建管理员。
          </div>
        </form>
      </section>
    </main>
  );
}
