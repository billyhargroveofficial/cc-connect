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
  Skill,
} from "./types";
const BASE = "/api/studio";
export class ApiError extends Error {
  status: number;
  constructor(
    message: string,
    status: number,
  ) {
    super(message);
    this.status = status;
    this.name = "ApiError";
  }
}
export async function request<T>(
  path: string,
  options: RequestInit = {},
): Promise<T> {
  const headers = new Headers(options.headers);
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
        ? "Сервер вернул неожиданный ответ."
        : "Не удалось подключиться к серверу.",
      response.status,
    );
  }
  if (!response.ok) {
    const error =
      data && typeof data === "object" && "error" in data
        ? String(data.error)
        : response.status >= 500
          ? "Сервер временно недоступен. Попробуйте подключиться снова."
          : "Не удалось выполнить запрос.";
    throw new ApiError(error, response.status);
  }
  return data as T;
}
function json<T>(path: string, method: string, body?: unknown) {
  return request<T>(path, {
    method,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
}
const botPath = (id: string) => `/bots/${encodeURIComponent(id)}`;
const scopePath = (id?: string) => (id ? botPath(id) : "/user");
export const api = {
  session: () =>
    request<{ authenticated?: boolean; [key: string]: unknown }>("/session"),
  login: (token: string) => json<void>("/login", "POST", { token }),
  logout: () => json<void>("/logout", "POST"),
  bots: () => request<{ bots: Bot[] }>("/bots"),
  createBot: (fields: Partial<Bot>) => json<Bot>("/bots", "POST", fields),
  updateBot: (id: string, fields: Partial<Bot>) =>
    json<Bot>(botPath(id), "PATCH", fields),
  archiveBot: (id: string) => json<void>(botPath(id), "DELETE"),
  capabilities: (botId?: string, signal?: AbortSignal) =>
    request<Capabilities>(
      `/capabilities${botId ? `?botId=${encodeURIComponent(botId)}` : ""}`,
      { signal },
    ),
  events: (id: string, after = 0) =>
    request<{ events: Event[] }>(`${botPath(id)}/events?after=${after}`),
  send: (id: string, text: string, attachments: Attachment[] = []) =>
    json<{ turnId: string }>(`${botPath(id)}/messages`, "POST", {
      text,
      attachments,
    }),
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
      throw new ApiError("Сервер вернул неожиданный ответ при снятии цели.", 502);
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
  return (
    attachment.url ||
    `${BASE}${botPath(botId)}/files/${encodeURIComponent(attachment.id)}`
  );
}
export function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : "Что-то пошло не так.";
}
