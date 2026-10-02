import type { Bot, Event } from "./types";
import { isSilentSessionNotice } from "./botHistory.ts";
export function mergeEvents(current: Event[], incoming: Event[]): Event[] {
  if (!incoming.length) return current;
  if ((!current.length || incoming[0].seq > current[current.length - 1].seq)
    && incoming.every((event, index) => !index || event.seq > incoming[index - 1].seq))
    return [...current, ...incoming];
  const map = new Map(current.map((event) => [event.seq, event]));
  let changed = false;
  for (const event of incoming) {
    const previous = map.get(event.seq);
    if (previous === event || previous && JSON.stringify(previous) === JSON.stringify(event)) continue;
    changed = true;
    map.set(event.seq, event);
  }
  return changed ? [...map.values()].sort((a, b) => a.seq - b.seq) : current;
}
export function eventBot(event: Event): Bot | null {
  if (event.type !== "bot") return null;
  const data =
    event.data.bot && typeof event.data.bot === "object"
      ? event.data.bot
      : event.data;
  return "id" in data && typeof data.id === "string"
    ? (data as unknown as Bot)
    : null;
}
export function messagePreview(events: Event[]): string {
  for (let i = events.length - 1; i >= 0; i--) {
    const event = events[i];
    if (event.type === "message" && typeof event.data.content === "string") {
      if (event.data.source === "goal_context") continue;
      const delegated = botMessagePresentation(event.data.content, String(event.data.source || ""));
      return (delegated ? `${delegated.sender}: ${delegated.content}` : event.data.content)
        .replace(/[#*_`]/g, "").slice(0, 100);
    }
    if (event.type === "system" && typeof event.data.content === "string"
      && !isSilentSessionNotice(event))
      return event.data.content.slice(0, 100);
  }
  return "";
}
export function botMessagePresentation(content: string, source: string) {
  if (!source.startsWith("bot:")) return null;
  const prefix = "Message from bot ", delimiter = ` (${source.slice(4)}):\n`;
  if (!content.startsWith(prefix)) return null;
  const end = content.indexOf(delimiter, prefix.length);
  if (end < prefix.length) return null;
  return {
    sender: content.slice(prefix.length, end),
    content: content.slice(end + delimiter.length),
  };
}
export function isWorking(status: string) {
  return [
    "working",
    "running",
    "thinking",
    "starting",
    "stopping",
    "waiting",
    "busy",
    "in_progress",
  ].includes(status);
}
export function statusLabel(status: string) {
  const labels: Record<string, string> = {
    idle: "Ready",
    working: "Working",
    running: "Working",
    thinking: "Thinking",
    starting: "Starting",
    stopping: "Stopping",
    waiting: "Waiting for a response",
    blocked: "Needs attention",
    busy: "Working",
    in_progress: "Working",
    interrupted: "Interrupted",
    failed: "Failed",
    error: "Error",
    done: "Done",
    completed: "Done",
    archived: "Archived",
  };
  return labels[status] || status || "Ready";
}

function compareUpdatedAt(left: string, right: string): number {
  const a = Date.parse(left),
    b = Date.parse(right);
  if (Number.isFinite(a) && Number.isFinite(b)) {
    if (a !== b) return a - b;
    // Go's RFC3339Nano timestamps have varying fractional precision. Preserve
    // ordering when several status writes land in the same millisecond.
    const fraction = (value: string) =>
      (value.match(/\.(\d+)(?:Z|[+-]\d\d:\d\d)$/)?.[1] || "").padEnd(9, "0");
    return fraction(left).localeCompare(fraction(right));
  }
  return left.localeCompare(right);
}
export function mergeBots(current: Bot[], incoming: Bot[]): Bot[] {
  const result = new Map(current.map((bot) => [bot.id, bot]));
  for (const bot of incoming) {
    const previous = result.get(bot.id);
    if (
      !previous ||
      !previous.updatedAt ||
      compareUpdatedAt(bot.updatedAt, previous.updatedAt) >= 0
    )
      if (!previous || previous !== bot && JSON.stringify(previous) !== JSON.stringify(bot)) result.set(bot.id, bot);
  }
  const next = [...result.values()];
  return next.length === current.length && next.every((bot, index) => bot === current[index]) ? current : next;
}
export function telegramLabel(binding: Bot["telegram"]): string {
  if (!binding?.enabled) return "Telegram disabled";
  const labels: Record<string, string> = {
    connected: "Connected to Telegram",
    connecting: "Connecting to Telegram",
    error: "Telegram connection error",
    disabled: "Telegram disabled",
  };
  return labels[binding.status || ""] || "Telegram enabled";
}
export function telegramTitle(binding: Bot["telegram"]): string {
  return binding?.status === "error" && binding.error
    ? `${telegramLabel(binding)}: ${binding.error}`
    : binding?.username
      ? `${telegramLabel(binding)} · @${binding.username}`
      : telegramLabel(binding);
}
