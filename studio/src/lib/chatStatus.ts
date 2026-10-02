import type { Event } from "./types.ts";

export interface ChatStatus {
  turnId: string;
  turn: number;
  step: number;
  status: string;
  startedAt: number | null;
  endedAt: number | null;
  effectiveMs: number;
  tokensPerSecond: number | null;
  model: string;
  effort: string;
  queueRevision: number;
}

interface Run {
  startedAt: number | null;
  endedAt: number | null;
  counted: boolean;
  steps: Set<string>;
  status: string;
  root: string;
  model: string;
  effort: string;
  tokensPerSecond: number | null;
}
const terminal = new Set(["completed", "done", "failed", "error", "stopped", "interrupted"]);
const calls = new Set(["commandExecution", "mcpToolCall", "dynamicToolCall", "webSearch", "collabAgentToolCall", "subAgentActivity", "fileChange"]);
const blank = (): ChatStatus => ({ turnId: "", turn: 0, step: 0, status: "", startedAt: null, endedAt: null, effectiveMs: 0, tokensPerSecond: null, model: "", effort: "", queueRevision: 0 });
const object = (value: unknown): Record<string, unknown> => value !== null && typeof value === "object" ? value as Record<string, unknown> : {};
const string = (value: unknown) => typeof value === "string" ? value : "";
const timestamp = (value: string) => { const time = Date.parse(value); return Number.isFinite(time) ? time : null; };
const duration = (run: Run) => run.startedAt !== null && run.endedAt !== null ? Math.max(0, run.endedAt - run.startedAt) : 0;

// The statusline is a small append-only projection, separate from Markdown and
// transcript rendering. Text deltas return the same object and cannot wake its
// memoized consumers. Replaced history resets the projection deterministically.
export function createChatStatusProjector() {
  let previous: Event[] = [];
  let runs = new Map<string, Run>();
  let currentId = "", turns = 0, effectiveMs = 0, queueRevision = 0;
  let value = blank();
  return (events: Event[]): ChatStatus => {
    if (events === previous) return value;
    const append = events.length > previous.length && (!previous.length || events[previous.length - 1] === previous[previous.length - 1])
      && (!previous.length || events[0] === previous[0]);
    let start = previous.length;
    if (!append) {
      runs = new Map(); currentId = ""; turns = 0; effectiveMs = 0; queueRevision = 0;
      start = 0;
    }
    let changed = !append;
    for (let index = start; index < events.length; index++) {
      const event = events[index], data = event.data;
      if (event.type === "queue" || event.type.startsWith("queue_")) {
        queueRevision = event.seq; changed = true;
      }
      if (!event.turnId) continue;
      let run = runs.get(event.turnId);
      if (event.type === "turn") {
        if (!run) {
          run = { startedAt: null, endedAt: null, counted: false, steps: new Set(), status: "", root: "", model: "", effort: "", tokensPerSecond: null };
          runs.set(event.turnId, run);
        }
        const status = string(data.status);
        if (!run.counted && !terminal.has(status)) { run.counted = true; turns++; }
        const before = duration(run);
        if (run.startedAt === null && !terminal.has(status)) run.startedAt = timestamp(event.time);
        if (terminal.has(status)) {
          run.endedAt = timestamp(event.time);
          const speed = data.tokensPerSecond;
          run.tokensPerSecond = typeof speed === "number" && Number.isFinite(speed) && speed > 0 ? speed : null;
        } else run.endedAt = null;
        effectiveMs += duration(run) - before;
        run.status = status;
        run.model = string(data.model) || run.model;
        run.effort = string(data.effort) || run.effort;
        currentId = event.turnId; changed = true;
        continue;
      }
      if (!run) continue;
      let callId = "";
      if (event.type === "native") {
        const params = object(data.params), item = object(params.item);
        const root = string(data.rootThreadId);
        run.root ||= root;
        const thread = string(params.threadId) || string(object(params.thread).id);
        if (thread && (root || run.root) && thread !== (root || run.root)) continue;
        const method = string(data.method);
        if ((method === "item/started" || method === "item/completed") && calls.has(string(item.type)))
          callId = string(item.id);
        else if (method === "tool_execution_start" || method === "tool_execution_end")
          callId = string(params.toolCallId);
      } else if (event.type === "agent") {
        const native = object(data.event);
        const type = string(data.type) || string(native.type);
        if (type === "tool_use" || type === "tool_result")
          callId = string(data.toolUseId) || string(native.toolUseId) || string(data.toolCallId) || string(native.toolCallId);
      }
      if (callId && !run.steps.has(callId)) {
        run.steps.add(callId);
        if (event.turnId === currentId) changed = true;
      }
    }
    previous = events;
    if (!changed) return value;
    const run = runs.get(currentId);
    const next = {
      turnId: currentId, turn: turns, step: run?.steps.size || 0, status: run?.status || "",
      startedAt: run?.startedAt ?? null, endedAt: run?.endedAt ?? null,
      effectiveMs, tokensPerSecond: run?.tokensPerSecond ?? null,
      model: run?.model || "", effort: run?.effort || "", queueRevision,
    };
    if (Object.keys(next).every(key => Object.is(next[key as keyof ChatStatus], value[key as keyof ChatStatus]))) return value;
    return value = next;
  };
}

export function turnElapsed(status: ChatStatus, now: number): number {
  return status.startedAt === null ? 0 : Math.max(0, (status.endedAt ?? now) - status.startedAt);
}
export function effectiveSessionTime(status: ChatStatus, now: number): number {
  return status.effectiveMs + (status.endedAt === null ? turnElapsed(status, now) : 0);
}
export function formatElapsed(ms: number): string {
  const seconds = Math.floor(Math.max(0, ms) / 1000);
  const hours = Math.floor(seconds / 3600), minutes = Math.floor(seconds % 3600 / 60), remainder = seconds % 60;
  return `${hours ? `${hours}:${String(minutes).padStart(2, "0")}` : minutes}:${String(remainder).padStart(2, "0")}`;
}
export function effortLabel(effort: string): string {
  return ({ off: "Off", none: "None", minimal: "Minimal", low: "Low", medium: "Medium", high: "High", xhigh: "Extra High", max: "Max", ultra: "Ultra" } as Record<string, string>)[effort] || effort || "Auto";
}
