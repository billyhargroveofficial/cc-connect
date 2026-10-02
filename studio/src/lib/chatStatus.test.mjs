import assert from "node:assert/strict";
import test from "node:test";
import { createChatStatusProjector, effectiveSessionTime, formatElapsed, turnElapsed } from "./chatStatus.ts";

const event = (seq, type, data, turnId = "one", seconds = seq) => ({ seq, botId: "bot", turnId, type, time: new Date(seconds * 1000).toISOString(), data });
const native = (seq, id, type = "commandExecution", method = "item/started", threadId = "root") => event(seq, "native", { rootThreadId: "root", method, params: { threadId, item: { id, type } } });

test("status projection retains identity for appended text deltas and resets replaced history", () => {
  const project = createChatStatusProjector();
  const start = event(1, "turn", { status: "running" });
  let events = [start];
  const first = project(events);
  events = [...events, event(2, "native", { method: "item/agentMessage/delta", params: { delta: "A token" } })];
  assert.equal(project(events), first, "tokens cannot wake Working/statusline consumers");
  assert.equal(project(events), first, "the same journal snapshot is constant time");
  const replacement = [event(1, "turn", { status: "running" }, "replacement", 10), events[1]];
  const replaced = project(replacement);
  assert.equal(replaced.turnId, "replacement");
  assert.equal(replaced.turn, 1);
  assert.equal(replaced.startedAt, 10000);
  assert.notEqual(replaced, first);
  assert.equal(project([]).turn, 0);
});

test("steps count unique root tool/search/subagent calls across native and adapter mirrors", () => {
  const project = createChatStatusProjector();
  const events = [event(1, "turn", { status: "running" }), native(2, "a"), native(3, "a", "commandExecution", "item/completed"),
    event(4, "agent", { type: "tool_use", toolCallId: "a" }), native(5, "child", "commandExecution", "item/started", "child-thread"),
    native(6, "b", "webSearch"), native(7, "c", "collabAgentToolCall"), native(8, "thinking", "reasoning")];
  assert.equal(project(events).step, 3);
  const next = project([...events, event(9, "turn", { status: "running" }, "two")]);
  assert.equal(next.turn, 2);
  assert.equal(next.step, 0, "step is local to the current turn");
});

test("Pi tool calls are deduplicated and current throughput remains unknown until completion", () => {
  const project = createChatStatusProjector();
  const events = [event(1, "turn", { status: "running", model: "deepseek", effort: "high" }),
    event(2, "native", { method: "tool_execution_start", params: { toolCallId: "pi-one" } }),
    event(3, "native", { method: "tool_execution_end", params: { toolCallId: "pi-one" } })];
  assert.equal(project(events).step, 1);
  assert.equal(project(events).tokensPerSecond, null);
  const end = project([...events, event(4, "turn", { status: "completed", tokensPerSecond: 42.1 }, "one", 5)]);
  assert.equal(end.tokensPerSecond, 42.1);
  assert.equal(end.model, "deepseek");
  assert.equal(end.effort, "high");
});

test("effective session time adds active intervals and excludes idle gaps", () => {
  const project = createChatStatusProjector();
  const events = [event(1, "turn", { status: "running" }, "one", 1), event(2, "turn", { status: "completed" }, "one", 4),
    event(3, "turn", { status: "running" }, "two", 20)];
  const active = project(events);
  assert.equal(turnElapsed(active, 23000), 3000);
  assert.equal(effectiveSessionTime(active, 23000), 6000);
  const ended = project([...events, event(4, "turn", { status: "stopped" }, "two", 25)]);
  assert.equal(turnElapsed(ended, 90000), 5000);
  assert.equal(effectiveSessionTime(ended, 90000), 8000);
  assert.equal(project([...events, event(4, "turn", { status: "stopped" }, "two", 25), event(5, "turn", { status: "stopped" }, "two", 25)]).effectiveMs, 8000,
    "a repeated terminal receipt cannot double count a run");
  assert.equal(formatElapsed(8000), "0:08");
  assert.equal(formatElapsed(3661000), "1:01:01");
});

test("all queue lifecycle events advance the refresh revision without inventing a turn", () => {
  const project = createChatStatusProjector();
  const events = [event(1, "queue", {}, undefined), event(2, "queue_control", {}, undefined), event(3, "queue_steer", {}, undefined)];
  const status = project(events);
  assert.equal(status.queueRevision, 3);
  assert.equal(status.turn, 0);
});
