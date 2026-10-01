import type { BotContext, Event } from "./types.ts";

type Value = Record<string, unknown>;
function object(value: unknown): Value {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as Value)
    : {};
}
function count(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) && value >= 0
    ? value
    : undefined;
}

// Missing provider data stays missing. In particular, an empty context is 0;
// a context whose size is unknown is not 0%.
export function contextNumbers(context: BotContext | null) {
  const used = count(context?.usedTokens);
  const window = count(context?.contextWindow);
  const reported = count(context?.percent);
  const percent = reported ?? (
    used !== undefined && window !== undefined && window > 0
      ? used / window * 100
      : undefined
  );
  return {
    used,
    window,
    remaining: count(context?.remainingTokens),
    percent,
  };
}

function belongs(event: Event, backend: string, threadId: string) {
  const data = object(event.data);
  if (data.backend && data.backend !== backend) return false;
  const params = object(data.params), thread = object(params.thread);
  const scope = params.threadId || thread.id || data.threadId;
  if (threadId && (data.rootThreadId && data.rootThreadId !== threadId || scope && scope !== threadId))
    return false;
  return true;
}

export function contextEventRevision(events: Event[], backend: string, threadId: string): number {
  for (let index = events.length - 1; index >= 0; index--) {
    const event = events[index];
    if (!belongs(event, backend, threadId)) continue;
    if (event.type === "compact_action") return event.seq;
    if (event.type === "turn" && ["completed", "failed", "stopped", "error"].includes(String(event.data.status)))
      return event.seq;
    if (event.type !== "native") continue;
    const data = object(event.data), params = object(data.params), item = object(params.item);
    if (["thread/tokenUsage/updated", "turn/completed", "agent_settled", "compaction_start", "compaction_end", "auto_compaction_start", "auto_compaction_end"].includes(String(data.method)))
      return event.seq;
    if (item.type === "contextCompaction") return event.seq;
  }
  return 0;
}

export function compactionState(events: Event[], backend: string, threadId: string, after: number): boolean | undefined {
  for (let index = events.length - 1; index >= 0; index--) {
    const event = events[index];
    if (event.seq <= after) break;
    if (!belongs(event, backend, threadId)) continue;
    const data = object(event.data);
    if (event.type === "compact_action") {
      if (data.status === "starting" || data.status === "started") return true;
      if (data.status === "completed" || data.status === "error" || data.status === "failed") return false;
    }
    if (event.type !== "native") continue;
    const params = object(data.params), item = object(params.item);
    if (data.method === "auto_compaction_start" || data.method === "compaction_start") return true;
    if (data.method === "auto_compaction_end" || data.method === "compaction_end") return false;
    if (item.type === "contextCompaction") {
      if (data.method === "item/started") return true;
      if (data.method === "item/completed") return false;
    }
  }
}

function manualState(events: Event[], backend: string, threadId: string, after = 0): boolean | undefined {
  for (let index = events.length - 1; index >= 0; index--) {
    const event = events[index];
    if (event.seq <= after) break;
    if (event.type !== "compact_action" || !belongs(event, backend, threadId)) continue;
    if (["started", "starting"].includes(String(event.data.status))) return true;
    if (["completed", "failed", "error"].includes(String(event.data.status))) return false;
  }
}

export function contextCompacting(snapshot: boolean, events: Event[], backend: string, threadId: string, cursor: number): boolean {
  // A finished native item does not release the server's manual-operation
  // gate. Keep that spinner until its tracked receipt confirms completion.
  const action = manualState(events, backend, threadId, cursor);
  if (action !== undefined) return action;
  if (snapshot && manualState(events, backend, threadId) === true) return true;
  return compactionState(events, backend, threadId, cursor) ?? snapshot;
}
