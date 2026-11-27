"use client";

import {
  Activity,
  Boxes,
  LayoutDashboard,
  LogOut,
  FileSearch,
  Sparkles,
  ShieldCheck,
  Users,
  Wrench,
  FolderOpen,
  MessageSquare,
  ShieldAlert,
} from "lucide-react";
import { useEffect, useState } from "react";
import { api, redirectToLogin, type User } from "../lib/api";

export default function WorkspaceShell({
  section,
  children,
}: {
  section: "dashboard" | "tasks" | "assets" | "users" | "models" | "tools" | "findings" | "workspace" | "conversation" | "system";
  children: React.ReactNode;
}) {
  const [user, setUser] = useState<User | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    api<User>("/me")
      .then(setUser)
      .catch((cause: Error & { status?: number }) => {
        if (cause.status === 401) redirectToLogin();
        else setError(cause.message || "无法验证登录状态");
      });
  }, []);
  async function logout() {
    try {
      await api<void>("/auth/logout", { method: "POST" });
    } finally {
      window.location.href = "/login/";
    }
  }
  return (
    <div className="workspace-layout">
      <aside className="workspace-sidebar">
        <a href="/" className="workspace-brand">
          <span className="workspace-brand-mark">
            <ShieldCheck size={22} />
          </span>
          <span>
            <strong>天罚</strong>
            <small>TIANFA SECURITY</small>
          </span>
        </a>
        <div className="workspace-nav-label">工作台</div>
        <nav aria-label="主导航">
          <a href="/" className={section === "dashboard" ? "selected" : ""} aria-current={section === "dashboard" ? "page" : undefined}>
            <LayoutDashboard size={18} />
            总览
          </a>
          <a
            href="/tasks/"
            className={section === "tasks" ? "selected" : ""}
            aria-current={section === "tasks" ? "page" : undefined}
          >
            <Activity size={18} />
            任务
          </a>
          <a
            href="/assets/"
            className={section === "assets" ? "selected" : ""}
            aria-current={section === "assets" ? "page" : undefined}
          >
            <Boxes size={18} />
            全局资产
          </a>
          <a href="/models/" className={section === "models" ? "selected" : ""} aria-current={section === "models" ? "page" : undefined}>
            <Sparkles size={18} />
            模型与 Agent
          </a>
          <a href="/tools/" className={section === "tools" ? "selected" : ""} aria-current={section === "tools" ? "page" : undefined}>
            <Wrench size={18} />
            工具与 Skill
          </a>
          <a href="/findings/" className={section === "findings" ? "selected" : ""} aria-current={section === "findings" ? "page" : undefined}>
            <FileSearch size={18} />
            流量与漏洞
          </a>
          <a href="/conversation/" className={section === "conversation" ? "selected" : ""} aria-current={section === "conversation" ? "page" : undefined}><MessageSquare size={18}/>任务会话</a>
          {(user?.role === "admin" || user?.role === "operator") && <a href="/workspace/" className={section === "workspace" ? "selected" : ""} aria-current={section === "workspace" ? "page" : undefined}><FolderOpen size={18}/>任务文件</a>}
          {user?.role === "admin" && <a href="/system/" className={section === "system" ? "selected" : ""} aria-current={section === "system" ? "page" : undefined}><ShieldAlert size={18}/>策略与审计</a>}
          {user?.role === "admin" && (
            <a
              href="/users/"
              className={section === "users" ? "selected" : ""}
              aria-current={section === "users" ? "page" : undefined}
            >
              <Users size={18} />
              用户管理
            </a>
          )}
        </nav>
        <div className="workspace-account">
          <span>{user?.email ?? "正在确认身份…"}</span>
          <small>{user?.role ?? ""}</small>
          {user && (
            <button
              type="button"
              onClick={() => void logout()}
              aria-label="退出登录"
            >
              <LogOut size={17} />
            </button>
          )}
        </div>
      </aside>
      <main className="workspace-main">
        <div className="workspace-content">
          {user ? (
            children
          ) : error ? (
            <div className="form-error" role="alert">
              {error}，请稍后刷新页面重试。
            </div>
          ) : (
            <div className="workspace-loading">正在验证登录状态…</div>
          )}
        </div>
      </main>
    </div>
  );
}
