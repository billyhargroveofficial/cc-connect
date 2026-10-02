import type { Event, Goal, GoalSnapshot } from "./types";
type ObjectValue = Record<string, unknown>;
function record(value: unknown): ObjectValue {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as ObjectValue)
    : {};
}
export function goalActionRevision(events: Event[], threadId: string): number {
  if (!threadId) return 0;
  for (let index = events.length - 1; index >= 0; index--) {
    const event = events[index];
    if (event.type !== "goal_action") continue;
    const data = record(event.data);
    if (data.threadId === threadId && (!data.backend || data.backend === "codex"))
      return event.seq;
  }
  return 0;
}
export function currentGoalUpdate(
  events: Event[],
  threadId: string,
  after = 0,
): { goal: Goal | null } | undefined {
  if (!threadId) return undefined;
  for (let i = events.length - 1; i >= 0; i--) {
    const event = events[i];
    if (event.seq <= after) break;
    const data = record(event.data);
    if (event.type === "goal") {
      const result = record(data.result);
      const value = "goal" in data
        ? data.goal
        : "goal" in result
          ? result.goal
          : data.method === "clear"
            ? null
            : data;
      const goal = record(value);
      const scope = goal.threadId || data.threadId || data.rootThreadId;
      if (
        scope === threadId &&
        (value === null || typeof goal.objective === "string")
      )
        return { goal: value === null ? null : (goal as Goal) };
    }
    if (
      event.type === "native" &&
      (data.method === "thread/goal/updated" ||
        data.method === "thread/goal/cleared")
    ) {
      const params = record(data.params),
        goal = record(params.goal);
      const scope = goal.threadId || params.threadId;
      if (data.rootThreadId && data.rootThreadId !== threadId) continue;
      if (scope !== threadId) continue;
      if (data.method === "thread/goal/cleared") return { goal: null };
      if (typeof goal.objective === "string") return { goal: goal as Goal };
    }
  }
}

export function goalFromSnapshot(
  snapshot: GoalSnapshot,
  events: Event[],
  threadId: string,
): Goal | null {
  if (!Number.isSafeInteger(snapshot.cursor) || snapshot.cursor < 0)
    throw new Error("The server did not report the goal position in the journal.");
  if (!("goal" in snapshot))
    throw new Error("The server did not report the current goal state.");
  const newer = currentGoalUpdate(events, threadId, snapshot.cursor);
  return newer ? newer.goal : snapshot.goal?.threadId === threadId ? snapshot.goal : null;
}
