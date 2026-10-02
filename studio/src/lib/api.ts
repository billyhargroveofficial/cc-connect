import type {
  Attachment,
  Bot,
  BotContext,
  Capabilities,
  Event,
  Goal,
  GoalSnapshot,
  Instructions,
  Maintenance,
  MessageMode,
  MessageQueueSnapshot,
  MessageReceipt,
  NodeEnrollment,
  NodeInfo,
  Skill,
} from "./types";
const BASE = "/api/studio";
export interface StudioUser {
  id: string;
  username: string;
}
export interface StudioSession {
  authenticated: boolean;
  user?: StudioUser;
  registrationAllowed: boolean;
  legacyClaimAvailable?: boolean;
  setupRequired?: boolean;
}
export interface AccountCredentials {
  username: string;
  password: string;
}
export interface WorkspaceBinding {
  accountId?: string;
  nodeId?: string;
}
let activeAccount: string | null = null;
let activeNode = "local";
const accountChangedListeners = new Set<(accountId: string) => void>();
const publicPaths = new Set(["/session", "/login", "/register", "/health"]);
export function setApiAccount(accountId: string | null) {
  if (accountId !== activeAccount) activeNode = "local";
  activeAccount = accountId;
}
export function setApiNode(nodeId: string | null) { activeNode = nodeId || "local"; }
const workspacePath = (path: string) => {
  const pathname = path.split("?")[0];
  return !publicPaths.has(pathname) && pathname !== "/logout" &&
    pathname !== "/nodes" && !pathname.startsWith("/nodes/");
};
export function onApiAccountChanged(listener: (accountId: string) => void) {
  accountChangedListeners.add(listener);
  return () => { accountChangedListeners.delete(listener); };
}
export class ApiError extends Error {
  status: number;
  code?: string;
  constructor(
    message: string,
    status: number,
    code?: string,
  ) {
    super(message);
    this.status = status;
    this.code = code;
    this.name = "ApiError";
  }
}
export function isAccountChanged(error: unknown): error is ApiError {
  return error instanceof ApiError && error.code === "account_changed";
}
export async function request<T>(
  path: string,
  options: RequestInit = {},
  expectedAccount?: string,
  expectedNode?: string,
): Promise<T> {
  const headers = new Headers(options.headers);
  const protectedPath = !publicPaths.has(path.split("?")[0]);
  const accountId = expectedAccount || activeAccount;
  if (protectedPath && accountId) headers.set("X-Connect-Bots-Account", accountId);
  else if (protectedPath && path !== "/logout")
    throw new ApiError("Sign in again to open this workspace.", 401);
  if (workspacePath(path)) headers.set("X-Connect-Bots-Node", expectedNode || activeNode);
  if (
    options.body &&
    !(options.body instanceof FormData) &&
    !headers.has("Content-Type")
  )
    headers.set("Content-Type", "application/json");
  const response = await fetch(`${BASE}${path}`, {
    ...options,
    headers,
    credentials: "same-origin",
  });
  const raw = await response.text();
  let data: unknown;
  try {
    data = raw ? JSON.parse(raw) : undefined;
  } catch {
    throw new ApiError(
      response.ok
        ? "The server returned an unexpected response."
        : "Could not connect to the server.",
      response.status,
    );
  }
  if (!response.ok) {
    const error =
      data && typeof data === "object" && "error" in data
        ? String(data.error)
        : response.status >= 500
          ? "The server is temporarily unavailable. Please try reconnecting."
          : "The request could not be completed.";
    const code = data && typeof data === "object" && "code" in data ? String(data.code) : undefined;
    if (code === "account_changed" && accountId && accountId === activeAccount)
      for (const listener of accountChangedListeners) listener(accountId);
    throw new ApiError(error, response.status, code);
  }
  return data as T;
}
function json<T>(path: string, method: string, body?: unknown, expectedAccount?: string, expectedNode?: string) {
  return request<T>(path, {
    method,
    body: body === undefined ? undefined : JSON.stringify(body),
  }, expectedAccount, expectedNode);
}
const botPath = (id: string) => `/bots/${encodeURIComponent(id)}`;
const scopePath = (id?: string) => (id ? botPath(id) : "/user");
export const api = {
  session: () => request<StudioSession>("/session"),
  login: (credentials: AccountCredentials) =>
    json<StudioSession>("/login", "POST", credentials),
  register: (credentials: AccountCredentials) =>
    json<StudioSession>("/register", "POST", credentials),
  logout: (expectedAccount?: string) => json<void>("/logout", "POST", undefined, expectedAccount),
  nodes: (expectedAccount?: string) => request<{ nodes: NodeInfo[] }>("/nodes", {}, expectedAccount),
  createNodeEnrollment: (name: string) =>
    json<NodeEnrollment>("/nodes/enrollments", "POST", { name }),
  removeNode: (id: string) => json<void>(`/nodes/${encodeURIComponent(id)}`, "DELETE"),
  bots: (nodeId?: string, signal?: AbortSignal, expectedAccount?: string) =>
    request<{ bots: Bot[] }>("/bots", { signal }, expectedAccount, nodeId),
  createBot: (fields: Partial<Bot>, nodeId?: string) =>
    json<Bot>("/bots", "POST", fields, undefined, nodeId),
  updateBot: (id: string, fields: Partial<Bot>) =>
    json<Bot>(botPath(id), "PATCH", fields),
  archiveBot: (id: string) => json<void>(botPath(id), "DELETE"),
  capabilities: (botId?: string, signal?: AbortSignal, nodeId?: string) =>
    request<Capabilities>(
      `/capabilities${botId ? `?botId=${encodeURIComponent(botId)}` : ""}`,
      { signal },
      undefined,
      nodeId,
    ),
  events: (id: string, after = 0) =>
    request<{ events: Event[] }>(`${botPath(id)}/events?after=${after}`),
  send: (id: string, text: string, attachments: Attachment[] = [], mode?: MessageMode, binding?: WorkspaceBinding) =>
    json<MessageReceipt>(`${botPath(id)}/messages`, "POST", {
      text,
      attachments,
      ...(mode ? { mode } : {}),
    }, binding?.accountId, binding?.nodeId),
  queue: (id: string, signal?: AbortSignal, binding?: WorkspaceBinding) =>
    request<MessageQueueSnapshot>(`${botPath(id)}/queue`, { signal }, binding?.accountId, binding?.nodeId),
  removeQueued: (id: string, messageId: string, binding?: WorkspaceBinding) =>
    json<void>(`${botPath(id)}/queue/${encodeURIComponent(messageId)}`, "DELETE", undefined, binding?.accountId, binding?.nodeId),
  steerQueued: (id: string, messageId: string, binding?: WorkspaceBinding) =>
    json<MessageReceipt>(`${botPath(id)}/queue/${encodeURIComponent(messageId)}/steer`, "POST", {}, binding?.accountId, binding?.nodeId),
  resumeQueue: (id: string, binding?: WorkspaceBinding) =>
    json<void>(`${botPath(id)}/queue/resume`, "POST", {}, binding?.accountId, binding?.nodeId),
  stop: (id: string) => json<void>(`${botPath(id)}/stop`, "POST"),
  context: (id: string, signal?: AbortSignal) =>
    request<BotContext>(`${botPath(id)}/context`, { signal }),
  compact: (id: string) =>
    json<{ requestId: string }>(`${botPath(id)}/compact`, "POST", {}),
  permission: (
    id: string,
    requestId: string,
    behavior: string,
    updatedInput?: Record<string, unknown>,
    message?: string,
  ) =>
    json<void>(`${botPath(id)}/permission`, "POST", {
      requestId,
      behavior,
      updatedInput,
      message,
    }),
  goalSnapshot: (id: string) =>
    request<GoalSnapshot>(`${botPath(id)}/goal`),
  goal: async (id: string) =>
    (await request<{ goal: Goal | null }>(`${botPath(id)}/goal`)).goal,
  setGoal: (id: string, fields: Goal) =>
    json<GoalSnapshot>(`${botPath(id)}/goal`, "PUT", fields),
  clearGoal: async (id: string): Promise<GoalSnapshot> => {
    const result = await json<{ cleared: boolean; cursor: number }>(
      `${botPath(id)}/goal`, "DELETE",
    );
    if (typeof result?.cleared !== "boolean")
      throw new ApiError("The server returned an unexpected response when clearing the goal.", 502);
    return { goal: null, cursor: result.cursor };
  },
  instructions: (id?: string) =>
    request<Instructions>(`${scopePath(id)}/instructions`),
  saveInstructions: (id: string | undefined, content: string) =>
    json<Instructions>(`${scopePath(id)}/instructions`, "PUT", { content }),
  skills: (id?: string) =>
    request<{ skills: Skill[] }>(`${scopePath(id)}/skills`),
  saveDisabledSkills: (id: string, disabledSkills: string[]) =>
    json<Bot>(`${botPath(id)}/skills`, "PATCH", { disabledSkills }),
  createSkill: (id: string | undefined, name: string, content: string) =>
    json<Skill>(`${scopePath(id)}/skills`, "POST", { name, content }),
  skillContent: (path: string) =>
    request<Instructions>(`/skills/content?path=${encodeURIComponent(path)}`),
  saveSkillContent: (path: string, content: string) =>
    json<Instructions>(
      `/skills/content?path=${encodeURIComponent(path)}`,
      "PUT",
      { content },
    ),
  upload: (id: string, file: File) => {
    const body = new FormData();
    body.append("file", file);
    return request<Attachment>(`${botPath(id)}/uploads`, {
      method: "POST",
      body,
    });
  },
  transcribe: (file: Blob, name = "recording.webm") => {
    const body = new FormData();
    body.append("file", file, name);
    return request<{ text: string }>("/transcribe", { method: "POST", body });
  },
  maintenance: () => request<Maintenance>("/maintenance"),
  saveMaintenance: (patch: Partial<Maintenance>) =>
    json<Maintenance>("/maintenance", "PATCH", patch),
  runMaintenance: () => json<Maintenance>("/maintenance/run", "POST"),
};
export function fileURL(botId: string, attachment: Attachment) {
  return accountURL(
    attachment.url ||
    `${BASE}${botPath(botId)}/files/${encodeURIComponent(attachment.id)}`
  );
}
export function accountURL(path: string) {
  if (!activeAccount) return path;
  const origin = typeof location === "undefined" ? "http://connect-bots.local" : location.origin;
  let url: URL;
  try { url = new URL(path, origin); } catch { return path; }
  if (url.origin !== origin || !url.pathname.startsWith(`${BASE}/`)) return path;
  url.searchParams.set("expectedAccount", activeAccount);
  if (workspacePath(url.pathname.slice(BASE.length))) url.searchParams.set("node", activeNode);
  return path.startsWith("/") ? `${url.pathname}${url.search}${url.hash}` : url.toString();
}
export function nodeBinaryURL(platform: "darwin", arch: "arm64" | "amd64") {
  return accountURL(`${BASE}/nodes/binary/${encodeURIComponent(platform)}/${encodeURIComponent(arch)}`);
}
export function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : "Something went wrong.";
}
