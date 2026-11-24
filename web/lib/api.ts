export type User = {
  id: string;
  organization_id: string;
  email: string;
  role: "admin" | "operator" | "reviewer" | "viewer";
  status: string;
};
export type ScopeEntry = {
  id: string;
  effect: "allow" | "deny";
  kind: string;
  value: string;
  normalized_value: string;
  ports: number[];
  path_prefix?: string;
};
export type Task = {
  id: string;
  title: string;
  description: string;
  status: string;
  version: number;
  scope_version: number;
  policy_mode: string;
  worker_limit: number;
  created_at: string;
  updated_at: string;
};
export type Detail = {
  task: Task;
  goals: { id: string; text: string; state: string; priority: number }[];
  constraints: { id: string; effect: string; text: string }[];
  scope: ScopeEntry[];
};
export type Graph = {
  nodes: { id: string; kind: string; state: string; summary: string }[];
  edges: { from_node_id: string; to_node_id: string; kind: string }[];
  anchors: {
    node_id: string;
    asset_id: string;
    asset_kind: string;
    asset_value: string;
  }[];
};
export type Coverage = {
  asset_count: number;
  anchored_assets: number;
  goal_count: number;
  proven_goal_count: number;
};
export type Asset = {
  id: string;
  kind: string;
  canonical_key: string;
  display_value: string;
  parent_id?: string;
  source_task_id?: string;
  confidence: number;
  last_seen_at: string;
};

function csrfToken(): string {
  if (typeof document === "undefined") return "";
  return (
    document.cookie
      .split("; ")
      .find((part) => part.startsWith("tianfa_csrf="))
      ?.split("=")[1] ?? ""
  );
}

export async function api<T>(
  path: string,
  options: RequestInit = {},
): Promise<T> {
  const headers = new Headers(options.headers);
  if (options.body) headers.set("Content-Type", "application/json");
  if (options.method && !["GET", "HEAD"].includes(options.method.toUpperCase()))
    headers.set("X-CSRF-Token", csrfToken());
  const response = await fetch(`/api/v1${path}`, {
    ...options,
    headers,
    credentials: "same-origin",
    cache: "no-store",
  });
  if (!response.ok) {
    let detail = `请求失败 (${response.status})`;
    try {
      const body = (await response.json()) as {
        detail?: string;
        title?: string;
      };
      detail = body.detail || body.title || detail;
    } catch {
      /* non-JSON error */
    }
    const error = new Error(detail) as Error & { status: number };
    error.status = response.status;
    throw error;
  }
  if (response.status === 204) return undefined as T;
  return ((await response.json()) as { data: T }).data;
}

export function redirectToLogin() {
  window.location.href = `/login/?next=${encodeURIComponent(window.location.pathname + window.location.search)}`;
}
