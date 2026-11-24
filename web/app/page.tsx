"use client";

import {
  Activity,
  ArrowRight,
  Boxes,
  Check,
  ChevronRight,
  CircleAlert,
  Database,
  FileSearch,
  Gauge,
  Globe2,
  LayoutDashboard,
  LoaderCircle,
  Network,
  Plus,
  Radio,
  RefreshCw,
  Settings2,
  ShieldCheck,
  Sparkles,
  TerminalSquare,
} from "lucide-react";
import { useCallback, useEffect, useState } from "react";

type CheckState = { status: "up" | "down"; detail?: string };
type HealthState = {
  status: "ok" | "not_ready";
  service: string;
  version: string;
  checks: Record<string, CheckState>;
};

const navigation = [
  { label: "总览", icon: LayoutDashboard, active: true, href: "/" },
  { label: "任务", icon: Activity, href: "/tasks/" },
  { label: "全局资产", icon: Boxes, href: "/assets/" },
  { label: "流量与证据", icon: Radio, phase: "Phase 5" },
];

const systemNavigation = [
  { label: "模型与 Agent", icon: Sparkles, phase: "Phase 2" },
  { label: "工具与 Skill", icon: TerminalSquare, phase: "Phase 4" },
  { label: "用户管理", icon: Settings2, href: "/users/" },
];

const checkLabels: Record<
  string,
  { name: string; description: string; icon: typeof Database }
> = {
  database: {
    name: "PostgreSQL",
    description: "结构化状态与审计数据",
    icon: Database,
  },
  blob_store: {
    name: "证据存储",
    description: "不可变证据与工具输出",
    icon: FileSearch,
  },
  workspace: {
    name: "任务空间",
    description: "隔离的运行文件目录",
    icon: TerminalSquare,
  },
};

export default function Dashboard() {
  const [health, setHealth] = useState<HealthState | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [updatedAt, setUpdatedAt] = useState<Date | null>(null);

  const refresh = useCallback(async () => {
    setLoading(true);
    try {
      const response = await fetch("/health/ready", { cache: "no-store" });
      const body = (await response.json()) as HealthState;
      setHealth(body);
      setError(null);
      setUpdatedAt(new Date());
    } catch {
      setError("无法连接到天罚服务，请确认后端已经启动。 ");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void refresh();
    const timer = window.setInterval(() => void refresh(), 20_000);
    return () => window.clearInterval(timer);
  }, [refresh]);

  const ready = health?.status === "ok";

  return (
    <div className="app-shell">
      <a className="skip-link" href="#main-content">
        跳到主要内容
      </a>
      <aside className="sidebar" aria-label="主导航">
        <div className="brand">
          <div className="brand-mark" aria-hidden="true">
            <ShieldCheck size={22} strokeWidth={2.2} />
          </div>
          <div>
            <div className="brand-name">天罚</div>
            <div className="brand-caption">TIANFA SECURITY</div>
          </div>
        </div>

        <nav className="nav-groups">
          <div className="nav-group">
            <div className="nav-title">工作台</div>
            {navigation.map((item) => (
              <NavItem key={item.label} {...item} />
            ))}
          </div>
          <div className="nav-group">
            <div className="nav-title">系统</div>
            {systemNavigation.map((item) => (
              <NavItem key={item.label} {...item} />
            ))}
          </div>
        </nav>

        <div className="scope-note">
          <ShieldCheck size={18} aria-hidden="true" />
          <div>
            <strong>授权边界优先</strong>
            <span>工具范围校验将在 Phase 4 开放</span>
          </div>
        </div>
        <div className="profile-row">
          <div className="avatar" aria-hidden="true">
            管
          </div>
          <div>
            <strong>工作账户</strong>
            <span>
              <a href="/login/">登录或切换账户</a>
            </span>
          </div>
          <ChevronRight size={16} aria-hidden="true" />
        </div>
      </aside>

      <main className="main" id="main-content">
        <header className="topbar">
          <div>
            <div className="eyebrow">
              <span>控制中心</span>
              <ChevronRight size={13} />
              <strong>总览</strong>
            </div>
            <h1>欢迎来到天罚控制台</h1>
          </div>
          <div className="top-actions">
            <button
              className="icon-button"
              type="button"
              onClick={() => void refresh()}
              aria-label="刷新系统状态"
              disabled={loading}
            >
              <RefreshCw size={17} className={loading ? "spin" : ""} />
            </button>
            <a className="primary-button" href="/tasks/?new=1">
              <Plus size={17} />
              新建验证任务
            </a>
          </div>
        </header>

        <section
          className={`status-hero ${ready ? "is-ready" : "is-pending"}`}
          aria-labelledby="system-status-title"
        >
          <div className="status-copy">
            <div className="status-kicker">
              {loading ? (
                <LoaderCircle className="spin" size={16} />
              ) : ready ? (
                <Check size={16} />
              ) : (
                <CircleAlert size={16} />
              )}
              {loading
                ? "正在检查"
                : ready
                  ? "基础设施已就绪"
                  : "需要完成基础配置"}
            </div>
            <h2 id="system-status-title">
              {ready ? "基础设施已就绪" : "连接数据库后即可继续配置"}
            </h2>
            <p>
              {ready
                ? "数据库、证据存储和任务空间均正常。登录后可以创建任务并查看资产与探索血缘。"
                : "我们已把缺失项整理在下方。不会在依赖未就绪时悄悄启动任务。"}
            </p>
            <div className="status-meta" aria-live="polite">
              <span
                className={`status-dot ${ready ? "ok" : "warn"}`}
                aria-hidden="true"
              />
              <span>
                {health?.service ?? "tianfa-server"} ·{" "}
                {health?.version ?? "连接中"}
              </span>
              {updatedAt && (
                <span>
                  最近检查{" "}
                  {updatedAt.toLocaleTimeString("zh-CN", {
                    hour: "2-digit",
                    minute: "2-digit",
                    second: "2-digit",
                  })}
                </span>
              )}
            </div>
          </div>
          <div className="hero-visual" aria-hidden="true">
            <div className="radar-ring ring-one" />
            <div className="radar-ring ring-two" />
            <div className="radar-ring ring-three" />
            <div className="radar-core">
              <ShieldCheck size={28} />
            </div>
            <span className="orbit-dot dot-one" />
            <span className="orbit-dot dot-two" />
            <span className="orbit-dot dot-three" />
          </div>
        </section>

        {error && (
          <div className="error-banner" role="alert">
            <CircleAlert size={18} />
            {error}
          </div>
        )}

        <section className="section" aria-labelledby="infra-title">
          <div className="section-heading">
            <div>
              <h2 id="infra-title">基础设施</h2>
              <p>来自后端实时就绪检查，不使用演示数据</p>
            </div>
            <span className="phase-chip">Phase 0</span>
          </div>
          <div className="check-grid">
            {Object.entries(checkLabels).map(([key, meta]) => {
              const check = health?.checks[key];
              return (
                <HealthCard
                  key={key}
                  check={check}
                  loading={loading && !health}
                  {...meta}
                />
              );
            })}
          </div>
        </section>

        <div className="content-grid">
          <section
            className="panel onboarding"
            aria-labelledby="onboarding-title"
          >
            <div className="panel-heading">
              <div className="panel-icon">
                <Gauge size={19} />
              </div>
              <div>
                <h2 id="onboarding-title">从这里开始</h2>
                <p>按顺序完成，避免带着隐患运行</p>
              </div>
            </div>
            <ol className="step-list">
              <SetupStep
                number="01"
                title="连接持久化存储"
                description="配置 PostgreSQL，并确认迁移与可写目录就绪。"
                done={ready}
                action="查看部署说明"
                href="https://github.com/X9st/TianFaSRC#本地启动"
              />
              <SetupStep
                number="02"
                title="创建管理员与组织"
                description="运行 go run ./cmd/server init-admin，然后登录控制台。"
                action="登录"
                href="/login/"
              />
              <SetupStep
                number="03"
                title="配置模型路由"
                description="设置主备 Profile，任务才会被允许启动。"
                phase="Phase 2"
              />
            </ol>
          </section>

          <section className="panel loop-panel" aria-labelledby="loop-title">
            <div className="panel-heading">
              <div className="panel-icon">
                <Network size={19} />
              </div>
              <div>
                <h2 id="loop-title">验证闭环</h2>
                <p>每一步都有来源、边界与证据</p>
              </div>
            </div>
            <div
              className="loop-flow"
              aria-label="目标、规划、执行、证据、复测的数据闭环"
            >
              {["目标", "规划", "执行", "证据", "复测"].map((item, index) => (
                <div className="loop-item" key={item}>
                  <span>{index + 1}</span>
                  <strong>{item}</strong>
                  {index < 4 && <ArrowRight size={14} aria-hidden="true" />}
                </div>
              ))}
            </div>
            <div className="principle-list">
              <div>
                <Globe2 size={17} />
                <span>
                  <strong>双图分离</strong>客观资产与探索血缘各自清晰
                </span>
              </div>
              <div>
                <ShieldCheck size={17} />
                <span>
                  <strong>程序化范围</strong>所有真实工具共享同一策略边界
                </span>
              </div>
              <div>
                <FileSearch size={17} />
                <span>
                  <strong>证据优先</strong>结论可追溯到 Run、工具与原始证据
                </span>
              </div>
            </div>
          </section>
        </div>

        <footer className="footer">
          <span>天罚 · 授权安全验证平台</span>
          <span>Phase 1 / 身份、任务与双图</span>
        </footer>
      </main>
    </div>
  );
}

function NavItem({
  label,
  icon: Icon,
  active,
  phase,
  href,
}: {
  label: string;
  icon: typeof Activity;
  active?: boolean;
  phase?: string;
  href?: string;
}) {
  return href ? (
    <a
      className={`nav-item ${active ? "active" : ""}`}
      href={href}
      aria-current={active ? "page" : undefined}
    >
      <Icon size={18} strokeWidth={1.8} />
      <span>{label}</span>
      {phase && <em>{phase}</em>}
    </a>
  ) : (
    <button
      className={`nav-item ${active ? "active" : ""}`}
      type="button"
      disabled={!active}
      aria-current={active ? "page" : undefined}
    >
      <Icon size={18} strokeWidth={1.8} />
      <span>{label}</span>
      {phase && <em>{phase}</em>}
    </button>
  );
}

function HealthCard({
  name,
  description,
  icon: Icon,
  check,
  loading,
}: {
  name: string;
  description: string;
  icon: typeof Database;
  check?: CheckState;
  loading: boolean;
}) {
  const up = check?.status === "up";
  return (
    <article className="health-card">
      <div className="health-icon">
        <Icon size={20} />
      </div>
      <div className="health-copy">
        <h3>{name}</h3>
        <p>{check?.detail ?? description}</p>
      </div>
      <div
        className={`health-badge ${loading ? "loading" : up ? "up" : "down"}`}
      >
        {loading ? (
          <LoaderCircle className="spin" size={13} />
        ) : up ? (
          <Check size={13} />
        ) : (
          <CircleAlert size={13} />
        )}
        {loading ? "检查中" : up ? "正常" : "未就绪"}
      </div>
    </article>
  );
}

function SetupStep({
  number,
  title,
  description,
  done,
  action,
  href,
  phase,
}: {
  number: string;
  title: string;
  description: string;
  done?: boolean;
  action?: string;
  href?: string;
  phase?: string;
}) {
  return (
    <li className={done ? "done" : ""}>
      <div className="step-number">{done ? <Check size={16} /> : number}</div>
      <div className="step-copy">
        <h3>
          {title}
          {phase && <span>{phase}</span>}
        </h3>
        <p>{description}</p>
      </div>
      {action && href && (
        <a href={href} target="_blank" rel="noreferrer">
          {action}
          <ArrowRight size={14} />
        </a>
      )}
    </li>
  );
}
