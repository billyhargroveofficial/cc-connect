import assert from 'node:assert/strict';
import test from 'node:test';
import { buildTranscript, isRunning } from './reducer.ts';

function event(seq, type, data, turnId = 'turn-1', botId = 'bot-1') {
  return { seq, botId, turnId, type, time: `2026-10-01T10:00:${String(seq % 60).padStart(2, '0')}Z`, data };
}
function native(seq, method, params, backend = 'codex', extra = {}) {
  return event(seq, 'native', { backend, method, params, ...extra });
}
function backgroundNative(seq, method, params, backend = 'codex', extra = {}) {
  return { ...native(seq, method, params, backend, extra), turnId: '' };
}

test('turn service tier stays scoped to its historical turn and survives sparse lifecycle updates', () => {
  const [fast, automatic] = buildTranscript([
    event(1, 'turn', { status: 'running', backend: 'codex', model: 'gpt-test', effort: 'max', serviceTier: 'priority' }),
    event(2, 'turn', { status: 'completed', outputTokens: 20 }),
    event(3, 'turn', { status: 'running', backend: 'codex', serviceTier: '' }, 'turn-2'),
    event(4, 'turn', { status: 'completed' }, 'turn-2'),
  ]);
  assert.equal(fast.serviceTier, 'priority');
  assert.equal(fast.status, 'completed');
  assert.equal(automatic.serviceTier, '');

  const [cleared] = buildTranscript([
    event(1, 'turn', { status: 'running', serviceTier: 'priority' }),
    event(2, 'turn', { status: 'completed', serviceTier: '' }),
  ]);
  assert.equal(cleared.serviceTier, '', 'an explicit automatic tier overrides an earlier value');
});

test('snapshot/SSE overlap is ordered and idempotent, scoped to the bot, with complete tool output', () => {
  const longOutput = 'verified line\n'.repeat(600);
  const start = native(2, 'item/started', { item: { id: 'shell-1', type: 'commandExecution', command: 'run-check', status: 'inProgress' } });
  const completed = native(4, 'item/completed', { item: { id: 'shell-1', type: 'commandExecution', command: 'run-check', status: 'completed', aggregatedOutput: longOutput, exitCode: 0 } });
  const source = [
    event(1, 'message', { role: 'user', content: 'Check this' }), start,
    event(3, 'agent', { Type: 'tool_use', ToolName: 'Bash', ToolInput: 'run-check' }), completed,
    event(5, 'agent', { Type: 'tool_result', ToolName: 'Bash', ToolResult: longOutput.slice(0, 500) }),
    event(6, 'turn', { status: 'completed' }), event(7, 'message', { role: 'user', content: 'Other bot' }, 'other-turn', 'bot-2'),
  ];
  const [turn] = buildTranscript([source[5], ...source.slice().reverse(), start, completed], 'bot-1');
  assert.equal(turn.events.length, 6);
  assert.deepEqual(turn.events.map(e => e.seq), [1, 2, 3, 4, 5, 6]);
  assert.equal(turn.users[0].content, 'Check this');
  assert.equal(turn.activities.length, 1);
  assert.equal(turn.activities[0].output, longOutput);
  assert.equal(turn.activities[0].status, 'completed');
  assert.equal(turn.status, 'completed');
  assert.equal(start.data.params.item.status, 'inProgress', 'projection must not change original payloads');
});

test('Codex deltas and final snapshots render one reasoning item and one reply, retaining finalized attachments', () => {
  const events = [
    native(1, 'item/started', { item: { id: 'think', type: 'reasoning', summary: [], content: [] } }),
    native(2, 'item/reasoning/summaryTextDelta', { itemId: 'think', delta: 'Check ', summaryIndex: 0 }),
    native(3, 'item/reasoning/summaryTextDelta', { itemId: 'think', delta: 'evidence.', summaryIndex: 0 }),
    native(4, 'item/completed', { item: { id: 'think', type: 'reasoning', summary: ['Check evidence.'], content: [] } }),
    event(5, 'agent', { Type: 'thinking', Content: 'Check evidence.' }),
    native(6, 'item/started', { item: { id: 'answer', type: 'agentMessage', text: '', phase: 'final_answer' } }),
    native(7, 'item/agentMessage/delta', { itemId: 'answer', delta: 'Result: ' }),
    native(8, 'item/agentMessage/delta', { itemId: 'answer', delta: '42.' }),
    native(9, 'item/completed', { item: { id: 'answer', type: 'agentMessage', text: 'Result: 42.', phase: 'final_answer' } }),
    event(10, 'agent', { Type: 'text', Content: 'Result: 42.' }),
    event(11, 'message', { role: 'assistant', content: 'Result: 42.', attachments: [{ id: 'file-1', name: 'result.csv', mimeType: 'text/csv' }] }),
    event(12, 'turn', { status: 'completed', tokensPerSecond: 42.5, outputTokens: 10 }),
  ];
  const [turn] = buildTranscript(events);
  assert.equal(turn.activities.length, 1);
  assert.equal(turn.activities[0].text, 'Check evidence.');
  assert.equal(turn.responses.length, 1);
  assert.equal(turn.responses[0].content, 'Result: 42.');
  assert.equal(turn.responses[0].attachments[0].id, 'file-1');
  assert.equal(turn.tokensPerSecond, 42.5);
  assert.equal(turn.events.length, events.length);
});

test('Pi parallel tool results use toolCallId, keep long outputs and do not duplicate normalized events', () => {
  const outputA = 'A'.repeat(9000);
  const events = [
    native(1, 'tool_execution_start', { toolCallId: 'a', toolName: 'read', args: { path: 'a.txt' } }, 'pi'),
    event(2, 'agent', { Type: 'tool_use', ToolName: 'read', ToolInput: 'a.txt', Metadata: { toolCallId: 'a' } }),
    native(3, 'tool_execution_start', { toolCallId: 'b', toolName: 'read', args: { path: 'b.txt' } }, 'pi'),
    event(4, 'agent', { Type: 'tool_use', ToolName: 'read', ToolInput: 'b.txt', Metadata: { toolCallId: 'b' } }),
    native(5, 'tool_execution_end', { toolCallId: 'b', toolName: 'read', result: { content: [{ type: 'text', text: 'B' }] }, isError: false }, 'pi'),
    native(6, 'tool_execution_update', { toolCallId: 'a', toolName: 'read', partialResult: { content: [{ type: 'text', text: 'partial' }] } }, 'pi'),
    native(7, 'tool_execution_end', { toolCallId: 'a', toolName: 'read', result: { content: [{ type: 'text', text: outputA }] }, isError: false }, 'pi'),
    native(8, 'agent_settled', {}, 'pi'),
  ];
  const [turn] = buildTranscript(events);
  assert.equal(turn.activities.length, 2);
  assert.equal(turn.activities.find(v => v.id === 'pi-tool-a').output.content[0].text, outputA);
  assert.equal(turn.activities.find(v => v.id === 'pi-tool-b').output.content[0].text, 'B');
  assert.equal(turn.status, 'completed');
});

test('Pi linear text/thinking deltas are replaced by authoritative message_end snapshots', () => {
  const events = [
    native(1, 'message_start', { message: { role: 'assistant', content: [] } }, 'pi'),
    native(2, 'message_update', { assistantMessageEvent: { type: 'thinking_delta', contentIndex: 0, delta: 'Consider ' } }, 'pi'),
    native(3, 'message_update', { assistantMessageEvent: { type: 'thinking_delta', contentIndex: 0, delta: 'the result.' } }, 'pi'),
    native(4, 'message_update', { assistantMessageEvent: { type: 'text_delta', contentIndex: 1, delta: 'Done' } }, 'pi'),
    native(5, 'message_end', { message: { role: 'assistant', content: [
      { type: 'thinking', thinking: 'Consider the result.', thinkingSignature: 'opaque-signature' },
      { type: 'text', text: 'Done.' },
    ] } }, 'pi'),
    event(6, 'message', { role: 'assistant', content: 'Done.' }),
    native(7, 'agent_end', {}, 'pi'),
  ];
  const [turn] = buildTranscript(events);
  assert.equal(turn.responses.length, 1);
  assert.equal(turn.responses[0].content, 'Done.');
  assert.equal(turn.activities[0].text, 'Consider the result.');
  assert.equal(turn.activities[0].data.thinkingSignature, undefined);
  assert.equal(turn.events[4].data.params.message.content[0].thinkingSignature, 'opaque-signature');
});

test('permission/question requests are deduplicated, native metadata retained, and shared decisions resolve them', () => {
  const rawQuestion = { id: 'name', header: 'Name', question: 'Which name?', isOther: true, isSecret: true, options: [{ label: 'A', description: 'First' }] };
  const events = [
    native(1, 'item/tool/requestUserInput', { questions: [rawQuestion] }, 'codex', { requestId: 'question-1' }),
    event(2, 'agent', { Type: 'permission_request', RequestID: '"question-1"', ToolName: 'AskUserQuestion', ToolInputRaw: { questions: [rawQuestion] }, Questions: [{ question: 'Which name?', options: [{ label: 'A' }] }] }),
    event(3, 'permission', { requestId: '"question-1"', behavior: 'allow', status: 'resolved' }, ''),
  ];
  const [turn] = buildTranscript(events);
  assert.equal(turn.requests.length, 1);
  assert.equal(turn.requests[0].id, '"question-1"');
  assert.equal(turn.requests[0].questions[0].id, 'name');
  assert.equal(turn.requests[0].questions[0].isSecret, true);
  assert.equal(turn.requests[0].resolved, true);
  assert.equal(turn.requests[0].behavior, 'allow');
});

test('interrupted turn closes pending requests and activities without fabricating an answer', () => {
  const events = [
    event(1, 'message', { role: 'user', content: 'Run' }),
    native(2, 'item/started', { item: { id: 'shell', type: 'commandExecution', command: 'run', status: 'inProgress' } }),
    event(3, 'agent', { Type: 'permission_request', RequestID: '7', ToolName: 'Bash', ToolInput: 'run' }),
    event(4, 'turn', { status: 'interrupted', error: 'Server restarted' }),
  ];
  const [turn] = buildTranscript(events);
  assert.equal(turn.status, 'interrupted');
  assert.equal(turn.activities[0].status, 'interrupted');
  assert.equal(turn.requests[0].resolved, true);
  assert.equal(turn.responses.length, 0);
  assert.equal(turn.error, 'Server restarted');
});

test('normalized-only adapter has useful streaming, thinking and paired tool fallback', () => {
  const events = [
    event(1, 'agent', { type: 'thinking', content: 'Check the source' }),
    event(2, 'agent', { type: 'tool_use', toolName: 'read', toolInput: 'note.txt' }),
    event(3, 'agent', { type: 'tool_result', toolName: 'read', toolResult: 'Contents', toolSuccess: true }),
    event(4, 'agent', { type: 'text', content: 'Hello ' }),
    event(5, 'agent', { type: 'text', content: 'world.' }),
    event(6, 'agent', { type: 'result', done: true }),
  ];
  const [turn] = buildTranscript(events);
  assert.equal(turn.activities.length, 2);
  assert.equal(turn.activities[1].output, 'Contents');
  assert.equal(turn.responses[0].content, 'Hello world.');
  assert.equal(turn.status, 'completed');
});

test('native goals, plan and subagent state remain inspectable, including unrecognized provider events', () => {
  const events = [
    native(1, 'thread/goal/updated', { goal: { objective: 'Finish the report', status: 'active', tokenBudget: 10000, tokensUsed: 50 } }),
    native(2, 'turn/plan/updated', { plan: [{ step: 'Read', status: 'completed' }, { step: 'Write', status: 'inProgress' }] }),
    native(3, 'item/completed', { item: { id: 'spawn', type: 'collabAgentToolCall', tool: 'spawnAgent', status: 'completed', receiverThreadIds: ['worker'], model: 'gpt-6-luna', reasoningEffort: 'max', agentsStates: { worker: { status: 'running', message: 'Reviewing' } } } }),
    native(4, 'future/method', { arbitrary: { nested: 'preserved' } }),
    event(5, 'turn', { status: 'completed' }),
  ];
  const [turn] = buildTranscript(events);
  assert.equal(turn.activities.length, 3);
  assert.equal(turn.activities.find(v => v.kind === 'goal').data.tokensUsed, 50);
  assert.equal(turn.activities.find(v => v.kind === 'plan').data.plan[1].step, 'Write');
  assert.equal(turn.activities.find(v => v.kind === 'subagent').data.agentsStates.worker.status, 'running');
  assert.equal(turn.events[3].data.params.arbitrary.nested, 'preserved');
});

test('null aggregate in a completed command does not erase output already streamed', () => {
  const [turn] = buildTranscript([
    native(1, 'item/started', { item: { id: 'shell', type: 'commandExecution', command: 'check' } }),
    native(2, 'item/commandExecution/outputDelta', { itemId: 'shell', delta: 'Output\n' }),
    native(3, 'item/completed', { item: { id: 'shell', type: 'commandExecution', command: 'check', aggregatedOutput: null, status: 'completed', exitCode: 0 } }),
  ]);
  assert.equal(turn.activities[0].output, 'Output\n');
});

test('completed native calls cannot identify fallback invocations by name alone', () => {
  const [turn] = buildTranscript([
    native(1, 'item/completed', { item: { id: 'shell', type: 'commandExecution', command: 'first', aggregatedOutput: '1', status: 'completed' } }),
    event(2, 'agent', { Type: 'tool_use', ToolName: 'Bash', ToolInput: 'first' }),
    event(3, 'agent', { Type: 'tool_result', ToolName: 'Bash', ToolResult: '1' }),
    event(4, 'agent', { Type: 'tool_use', ToolName: 'Bash', ToolInput: 'second' }),
    event(5, 'agent', { Type: 'tool_result', ToolName: 'Bash', ToolResult: '2' }),
  ]);
  assert.equal(turn.activities.length, 3);
  assert.equal(turn.activities[1].input, 'first');
  assert.equal(turn.activities[2].input, 'second');
  assert.equal(turn.activities[2].output, '2');
});

test('temporary Pi retries leave the turn open', () => {
  const [turn] = buildTranscript([
    native(1, 'agent_start', {}, 'pi'),
    native(2, 'agent_end', { willRetry: true }, 'pi'),
  ]);
  assert.equal(turn.status, 'retrying');
  assert.equal(turn.error, undefined);
});

test('Pi multi-message loop does not repeat its aggregated product response', () => {
  const [turn] = buildTranscript([
    native(1, 'message_start', { message: { role: 'assistant', content: [] } }, 'pi'),
    native(2, 'message_end', { message: { role: 'assistant', content: [{ type: 'text', text: 'Reading.' }] } }, 'pi'),
    native(3, 'message_start', { message: { role: 'assistant', content: [] } }, 'pi'),
    native(4, 'message_end', { message: { role: 'assistant', content: [{ type: 'text', text: 'Done.' }] } }, 'pi'),
    event(5, 'message', { role: 'assistant', content: 'Reading.Done.' }),
    native(6, 'agent_end', {}, 'pi'),
  ]);
  assert.equal(turn.responses.length, 2);
  assert.deepEqual(turn.responses.map(v => v.content), ['Reading.', 'Done.']);
});

test('Codex child responses, tool output and completion stay in its card while the parent remains active', () => {
  const childOutput = 'child verified output\n'.repeat(600);
  const scoped = (seq, method, threadId, params = {}) => native(seq, method, { threadId, ...params }, 'codex', { rootThreadId: 'root-thread' });
  const initial = [
    event(1, 'message', { role: 'user', content: 'Research and review the result.' }),
    scoped(2, 'turn/started', 'root-thread', { turn: { id: 'root-turn', status: 'inProgress' } }),
    native(3, 'thread/started', { thread: { id: 'child-thread', name: 'Researcher', source: { subAgent: { parentThreadId: 'root-thread' } } } }, 'codex', { rootThreadId: 'root-thread' }),
    scoped(4, 'turn/started', 'child-thread', { turn: { id: 'child-turn', status: 'inProgress' } }),
    scoped(5, 'item/completed', 'child-thread', { item: { id: 'shell', type: 'commandExecution', command: 'research', aggregatedOutput: childOutput, status: 'completed', exitCode: 0 } }),
    scoped(6, 'item/started', 'child-thread', { item: { id: 'answer', type: 'agentMessage', text: '', phase: 'final_answer' } }),
    scoped(7, 'item/agentMessage/delta', 'child-thread', { itemId: 'answer', delta: 'Intermediate child result' }),
    scoped(8, 'item/completed', 'child-thread', { item: { id: 'answer', type: 'agentMessage', text: 'Intermediate child result', phase: 'final_answer' } }),
    scoped(9, 'turn/completed', 'child-thread', { turn: { id: 'child-turn', status: 'completed' } }),
  ];
  const [running] = buildTranscript(initial);
  assert.equal(running.status, 'inProgress', 'child completion cannot complete the parent');
  assert.equal(running.responses.length, 0, 'child response is never attributed to the parent');
  const childCard = running.activities.find(v => v.thread?.id === 'child-thread');
  assert.equal(childCard.title, 'Subagent · Researcher');
  assert.equal(childCard.thread.status, 'completed');
  assert.equal(childCard.thread.responses[0].content, 'Intermediate child result');
  assert.equal(childCard.thread.activities[0].output, childOutput);
  assert.equal(childCard.thread.events.length, 7);
  assert.equal(initial[6].data.params.threadId, 'child-thread', 'source scope is preserved');
  assert.equal(running.events.length, initial.length);

  const [completed] = buildTranscript([...initial,
    scoped(10, 'item/completed', 'root-thread', { item: { id: 'answer', type: 'agentMessage', text: 'Reviewed final answer', phase: 'final_answer' } }),
    scoped(11, 'turn/completed', 'root-thread', { turn: { id: 'root-turn', status: 'completed' } }),
    event(12, 'message', { role: 'assistant', content: 'Reviewed final answer' }),
  ]);
  assert.equal(completed.status, 'completed');
  assert.deepEqual(completed.responses.map(v => v.content), ['Reviewed final answer']);
  assert.equal(completed.activities.find(v => v.thread).thread.responses[0].content, 'Intermediate child result');
});

test('legacy Codex history uses the parent turn/started scope, excluding declared child thread ids', () => {
  const events = [
    native(1, 'thread/started', { thread: { id: 'child', source: { subAgent: { parentThreadId: 'root' } } } }),
    native(2, 'turn/started', { threadId: 'child', turn: { id: 'child-turn', status: 'inProgress' } }),
    native(3, 'turn/started', { threadId: 'root', turn: { id: 'root-turn', status: 'inProgress' } }),
    native(4, 'item/completed', { threadId: 'root', item: { id: 'spawn', type: 'collabAgentToolCall', tool: 'spawnAgent', status: 'completed', receiverThreadIds: ['child'] } }),
    native(5, 'item/completed', { threadId: 'child', item: { id: 'answer', type: 'agentMessage', text: 'Child result' } }),
    native(6, 'turn/completed', { threadId: 'child', turn: { id: 'child-turn', status: 'completed' } }),
  ];
  const [turn] = buildTranscript(events);
  assert.equal(turn.status, 'inProgress');
  assert.equal(turn.responses.length, 0);
  assert.equal(turn.activities.find(v => v.thread?.id === 'child').thread.responses[0].content, 'Child result');
});

test('Codex scoped history without root metadata or turn/started never guesses a parent answer', () => {
  const [turn] = buildTranscript([
    event(1, 'turn', { status: 'running' }),
    native(2, 'item/completed', { threadId: 'unknown-a', item: { id: 'message', type: 'agentMessage', text: 'A' } }),
    native(3, 'item/completed', { threadId: 'unknown-b', item: { id: 'message', type: 'agentMessage', text: 'B' } }),
    native(4, 'turn/completed', { threadId: 'unknown-b', turn: { status: 'completed' } }),
  ]);
  assert.equal(turn.status, 'running');
  assert.equal(turn.responses.length, 0);
  assert.equal(turn.activities.length, 2);
  assert.equal(turn.activities[0].title, 'Agent session');
  assert.equal(turn.activities[0].thread.responses[0].content, 'A');
  assert.equal(turn.activities[1].thread.responses[0].content, 'B');
});

test('connection metadata and old GET goal snapshots stay inspectable without creating empty conversation cards', () => {
  const setup = [
    { ...native(1, 'thread/started', { thread: { id: 'root', source: 'appServer' } }), turnId: undefined },
    event(2, 'system', { content: 'Session connected.' }, undefined),
    { ...event(3, 'goal', { method: 'get', result: { goal: null } }), turnId: undefined },
  ];
  // The helper's default argument is intentional for actual turns; remove it
  // explicitly for the system setup event as well.
  setup[1].turnId = undefined;
  const turns = buildTranscript(setup);
  assert.equal(turns.flatMap(turn => turn.events).length, setup.length);
  assert.equal(turns.flatMap(turn => turn.activities).length, 0);
  assert.equal(turns.flatMap(turn => turn.notices).length, 0);
});

test('long handoff context stays in completed collapsible activity instead of hiding actual replies in notices', () => {
  const history = 'user: Previous question\nassistant: Previous answer\n\n'.repeat(500);
  const handoff = event(2, 'handoff', {
    from: 'codex', to: 'pi', reason: 'backend_changed',
    threads: { codex: 'codex-thread', pi: 'pi-session' }, content: history,
  });
  const activeEvents = [event(1, 'message', { role: 'user', content: 'Continue' }), handoff];
  const [active] = buildTranscript(activeEvents);
  assert.equal(active.status, 'running', 'handoff receipt must not complete the owner turn');
  assert.equal(active.notices.length, 0);
  assert.equal(active.activities.length, 1);
  const receipt = active.activities[0];
  assert.equal(receipt.kind, 'event');
  assert.equal(receipt.title, 'Context transferred');
  assert.equal(receipt.status, 'completed');
  assert.equal(isRunning(receipt.status), false, 'completed activity must default collapsed in the transcript');
  assert.equal(receipt.text, 'codex → pi', 'compact summary must never contain the history');
  assert.equal(receipt.output, history, 'expanded activity must retain the entire transferred history');
  assert.deepEqual(receipt.data, handoff.data);
  assert.strictEqual(active.events[1], handoff, 'raw journal must retain the original event');
  const [done] = buildTranscript([...activeEvents,
    event(3, 'message', { role: 'assistant', content: 'Current answer' }),
    event(4, 'turn', { status: 'completed' }),
  ]);
  assert.deepEqual(done.responses.map(response => response.content), ['Current answer']);
  assert.deepEqual(done.notices, []);
  assert.equal(done.activities[0].output, history);
});

test('session connected and history saved boilerplate stay raw only while meaningful system notices remain visible', () => {
  const events = [
    event(1, 'system', { content: 'Session connected.', threadId: 'root' }, ''),
    event(2, 'system', { content: 'Session history saved.', threadId: 'root', configSignature: 'saved' }, ''),
    event(3, 'system', { content: 'Session history saved.', threadId: 'root' }),
    event(4, 'system', { content: 'Session connected.', threadId: 'root' }),
    event(5, 'system', { content: 'Connection lost; retry required.' }),
  ];
  const turns = buildTranscript(events);
  assert.deepEqual(turns.flatMap(turn => turn.notices), ['Connection lost; retry required.']);
  assert.equal(turns.flatMap(turn => turn.activities).length, 0);
  assert.deepEqual(turns.flatMap(turn => turn.events), events, 'boilerplate payloads must stay inspectable in the journal');
});

test('goal_context source collapses internal context while preserving identical owner text and the native acknowledgement', () => {
  const content = 'Previous user message\nPrevious assistant answer\n\n'.repeat(500);
  const internal = event(1, 'message', { role: 'user', source: 'goal_context', content, backend: 'codex' });
  const ordinary = event(2, 'message', { role: 'user', source: 'web', content });
  const activeEvents = [internal, ordinary];
  const [active] = buildTranscript(activeEvents);
  assert.equal(active.users.length, 1, 'only the trusted internal source is removed from user bubbles');
  assert.equal(active.users[0].content, content, 'ordinary owner content must not be classified by its text');
  assert.equal(active.status, 'running', 'the internal receipt must not finish the actual turn');
  assert.equal(active.activities.length, 1);
  const receipt = active.activities[0];
  assert.equal(receipt.kind, 'event');
  assert.equal(receipt.title, 'Goal context');
  assert.equal(receipt.status, 'completed');
  assert.equal(isRunning(receipt.status), false);
  assert.deepEqual(receipt.input, internal.data, 'the expanded input must retain the complete content and metadata');
  assert.deepEqual(receipt.data, internal.data);
  assert.strictEqual(active.events[0], internal);
  assert.equal(active.notices.length, 0);
  const [done] = buildTranscript([...activeEvents,
    native(3, 'item/completed', { item: { id: 'ack', type: 'agentMessage', text: 'Context received.' } }),
    event(4, 'turn', { status: 'completed' }),
  ]);
  assert.equal(done.status, 'completed');
  assert.deepEqual(done.responses.map(response => response.content), ['Context received.']);
  assert.deepEqual(done.activities[0].input, internal.data);
  assert.equal(done.events.length, 4);
});

test('a child goal nested in native params does not overwrite parent activity', () => {
  const [turn] = buildTranscript([
    native(1, 'turn/started', { threadId: 'root', turn: { status: 'inProgress' } }, 'codex', { rootThreadId: 'root' }),
    native(2, 'thread/goal/updated', { goal: { threadId: 'child', objective: 'Child goal', status: 'active' } }, 'codex', { rootThreadId: 'root' }),
  ]);
  assert.equal(turn.status, 'inProgress');
  assert.equal(turn.activities.some(activity => activity.kind === 'goal'), false);
  assert.equal(turn.activities[0].thread.activities[0].text, 'Child goal');
});

test('runtime and adapter mirrors of the same native call yield one card with complete output', () => {
  const args = { botId: 'worker', message: 'Review' };
  const output = [{ type: 'inputText', text: 'review result\n'.repeat(600) }];
  const events = [
    native(1, 'item/started', { item: { id: 'call-1', type: 'dynamicToolCall', tool: 'bots_send', arguments: args, status: 'inProgress' } }),
    event(2, 'agent', { type: 'tool_use', toolName: 'bots_send', toolInput: JSON.stringify(args), metadata: { toolCallId: 'call-1', threadId: 'root' } }),
    event(3, 'agent', { type: 'tool_use', toolName: 'bots_send', toolInputRaw: args, toolCallId: 'call-1' }),
    event(4, 'agent', { type: 'tool_result', toolName: 'bots_send', toolCallId: 'call-1', toolResult: JSON.stringify(output), toolSuccess: true }),
    native(5, 'item/completed', { item: { id: 'call-1', type: 'dynamicToolCall', tool: 'bots_send', arguments: args, contentItems: output, status: 'completed', success: true } }),
    event(6, 'agent', { type: 'tool_result', toolName: 'bots_send', toolResult: 'short legacy preview', metadata: { toolCallId: 'call-1', threadId: 'root' } }),
    event(7, 'turn', { status: 'completed' }),
  ];
  const [turn] = buildTranscript(events);
  assert.equal(turn.activities.length, 1);
  assert.equal(turn.activities[0].callId, 'call-1');
  assert.deepEqual(turn.activities[0].output, output);
  assert.equal(turn.events.length, events.length);
});

test('identical tool names and arguments with distinct call IDs remain distinct', () => {
  const args = { botId: 'worker', message: 'Same input' };
  const [turn] = buildTranscript([
    native(1, 'item/started', { item: { id: 'call-1', type: 'dynamicToolCall', tool: 'bots_send', arguments: args } }),
    native(2, 'item/started', { item: { id: 'call-2', type: 'dynamicToolCall', tool: 'bots_send', arguments: args } }),
    event(3, 'agent', { type: 'tool_use', toolName: 'bots_send', toolCallId: 'call-1', toolInputRaw: args }),
    event(4, 'agent', { type: 'tool_use', toolName: 'bots_send', metadata: { toolCallId: 'call-2' }, toolInput: JSON.stringify(args) }),
    native(5, 'item/completed', { item: { id: 'call-2', type: 'dynamicToolCall', tool: 'bots_send', arguments: args, contentItems: [{ type: 'inputText', text: 'second' }], status: 'completed' } }),
    event(6, 'agent', { type: 'tool_result', toolName: 'bots_send', toolCallId: 'call-2', toolResult: 'second' }),
    native(7, 'item/completed', { item: { id: 'call-1', type: 'dynamicToolCall', tool: 'bots_send', arguments: args, contentItems: [{ type: 'inputText', text: 'first' }], status: 'completed' } }),
    event(8, 'agent', { type: 'tool_result', toolName: 'bots_send', toolCallId: 'call-1', toolResult: 'first' }),
  ]);
  assert.equal(turn.activities.length, 2);
  assert.equal(turn.activities.find(v => v.callId === 'call-1').output[0].text, 'first');
  assert.equal(turn.activities.find(v => v.callId === 'call-2').output[0].text, 'second');
});

test('legacy mirror correlates only a uniquely active same-tool invocation with equal canonical arguments', () => {
  const [turn] = buildTranscript([
    native(1, 'item/started', { item: { id: 'call-1', type: 'dynamicToolCall', tool: 'bots_send', arguments: { message: 'Review', botId: 'worker' } } }),
    event(2, 'agent', { type: 'tool_use', toolName: 'bots_send', toolInput: '{"botId":"worker","message":"Review"}' }),
    native(3, 'item/completed', { item: { id: 'call-1', type: 'dynamicToolCall', tool: 'bots_send', arguments: { message: 'Review', botId: 'worker' }, contentItems: [{ type: 'inputText', text: 'verified' }], status: 'completed' } }),
    event(4, 'agent', { type: 'tool_result', toolName: 'bots_send', toolResult: 'verified' }),
  ]);
  assert.equal(turn.activities.length, 1);
  assert.equal(turn.activities[0].callId, 'call-1');
  assert.equal(turn.activities[0].output[0].text, 'verified');
});

test('ambiguous legacy mirrors do not hide distinct native calls', () => {
  const args = { message: 'same', botId: 'worker' };
  const [turn] = buildTranscript([
    native(1, 'item/started', { item: { id: 'call-1', type: 'dynamicToolCall', tool: 'bots_send', arguments: args } }),
    native(2, 'item/started', { item: { id: 'call-2', type: 'dynamicToolCall', tool: 'bots_send', arguments: args } }),
    event(3, 'agent', { type: 'tool_use', toolName: 'bots_send', toolInputRaw: args }),
    event(4, 'agent', { type: 'tool_result', toolName: 'bots_send', toolResult: 'unattributed result' }),
  ]);
  assert.equal(turn.activities.length, 3);
  assert.equal(turn.activities[2].callId, undefined);
  assert.equal(turn.activities[2].output, 'unattributed result');
  assert.equal(turn.events.length, 4);
});

test('non-equal or missing legacy arguments retain native and normalized calls', () => {
  for (const input of [{ message: 'different' }, undefined]) {
    const [turn] = buildTranscript([
      native(1, 'item/started', { item: { id: 'call-1', type: 'dynamicToolCall', tool: 'bots_send', arguments: { message: 'original' } } }),
      event(2, 'agent', { type: 'tool_use', toolName: 'bots_send', toolInputRaw: input }),
      event(3, 'agent', { type: 'tool_result', toolName: 'bots_send', toolResult: 'result' }),
    ]);
    assert.equal(turn.activities.length, 2);
    assert.equal(turn.activities[1].callId, undefined);
    assert.equal(turn.activities[1].output, 'result');
  }
});

test('normalized calls without native events correlate by explicit identity despite parallel completion order', () => {
  const [turn] = buildTranscript([
    event(1, 'agent', { type: 'tool_use', toolName: 'bots_send', toolCallId: 'a', toolInputRaw: { message: 'same' } }),
    event(2, 'agent', { type: 'tool_use', toolName: 'bots_send', toolCallId: 'b', toolInputRaw: { message: 'same' } }),
    event(3, 'agent', { type: 'tool_result', toolName: 'bots_send', toolCallId: 'a', toolResult: 'A' }),
    event(4, 'agent', { type: 'tool_result', toolName: 'bots_send', toolCallId: 'b', toolResult: 'B' }),
  ]);
  assert.equal(turn.activities.length, 2);
  assert.equal(turn.activities.find(v => v.callId === 'a').output, 'A');
  assert.equal(turn.activities.find(v => v.callId === 'b').output, 'B');
});

test('legacy parallel results without identity or exact input stay separate instead of guessing a call', () => {
  const [turn] = buildTranscript([
    event(1, 'agent', { type: 'tool_use', toolName: 'read', toolInputRaw: { path: 'a' } }),
    event(2, 'agent', { type: 'tool_use', toolName: 'read', toolInputRaw: { path: 'b' } }),
    event(3, 'agent', { type: 'tool_result', toolName: 'read', toolResult: 'Unknown result' }),
  ]);
  assert.equal(turn.activities.length, 3);
  assert.equal(turn.activities[0].output, undefined);
  assert.equal(turn.activities[1].output, undefined);
  assert.equal(turn.activities[2].output, 'Unknown result');
});

test('attachment-only published messages remain visible alongside a live normalized text response', () => {
  const [turn] = buildTranscript([
    event(1, 'agent', { type: 'text', content: 'Created ' }),
    event(2, 'message', { role: 'assistant', content: '', source: 'files', artifact: true, attachments: [{ id: 'image', name: 'figure.png', mimeType: 'image/png' }] }),
    event(3, 'agent', { type: 'text', content: 'the files.' }),
  ]);
  assert.equal(turn.responses.length, 2);
  const file = turn.responses.find(v => v.artifact);
  assert.equal(file.content, '');
  assert.equal(file.attachments[0].id, 'image');
  assert.equal(file.attachments[0].mimeType, 'image/png');
  assert.equal(turn.responses.find(v => !v.artifact).content, 'Created the files.');
});

test('multiple file publications with identical captions are separate from each other and the native answer', () => {
  const [turn] = buildTranscript([
    event(1, 'message', { role: 'assistant', content: 'Готовые файлы', source: 'files', artifact: true,
      attachments: [{ id: 'document', name: 'report.pdf', mimeType: 'application/pdf' }] }),
    event(2, 'message', { role: 'assistant', content: 'Готовые файлы', source: 'files', artifact: true,
      attachments: [{ id: 'data', name: 'data.csv', mimeType: 'text/csv' }, { id: 'figure', name: 'figure.png', mimeType: 'image/png' }] }),
    native(3, 'item/completed', { item: { id: 'answer', type: 'agentMessage', text: 'Готовые файлы' } }),
    event(4, 'message', { role: 'assistant', content: 'Готовые файлы', source: 'codex' }),
    event(5, 'turn', { status: 'completed' }),
  ]);
  assert.equal(turn.responses.length, 3);
  assert.deepEqual(turn.responses.filter(v => v.artifact).map(v => v.attachments.map(a => a.id)), [['document'], ['data', 'figure']]);
  assert.equal(turn.responses.filter(v => !v.artifact).length, 1);
});

test('Pi waits for settled across real compaction events and preserves separate compaction cycles', () => {
  const initial = [
    native(1, 'agent_start', {}, 'pi'),
    native(2, 'agent_end', { willRetry: false }, 'pi'),
    native(3, 'compaction_start', { reason: 'threshold' }, 'pi'),
  ];
  const [running] = buildTranscript(initial);
  assert.equal(running.status, 'running');
  assert.equal(running.activities[0].status, 'running');
  const [done] = buildTranscript([...initial,
    native(4, 'compaction_end', { reason: 'threshold', result: { summary: 'Actual summary' }, aborted: false }, 'pi'),
    native(5, 'auto_compaction_start', {}, 'pi'),
    native(6, 'auto_compaction_end', { errorMessage: 'Provider unavailable', aborted: false }, 'pi'),
    native(7, 'agent_settled', {}, 'pi'),
  ]);
  assert.equal(done.status, 'completed');
  assert.equal(done.activities.length, 2);
  assert.equal(done.activities[0].status, 'completed');
  assert.equal(done.activities[0].data.result.summary, 'Actual summary');
  assert.equal(done.activities[1].status, 'failed');
  assert.equal(done.events.length, 7);
});

test('background native Codex and Pi compaction keep an actual start/end lifecycle without product turn IDs', () => {
  const codexStart = { ...native(1, 'item/started', { threadId: 'root', item: { id: 'compact', type: 'contextCompaction' } }, 'codex', { rootThreadId: 'root' }), turnId: '' };
  const codexEnd = { ...native(2, 'item/completed', { threadId: 'root', item: { id: 'compact', type: 'contextCompaction' } }, 'codex', { rootThreadId: 'root' }), turnId: '' };
  assert.equal(buildTranscript([codexStart])[0].activities[0].status, 'running');
  const codex = buildTranscript([codexStart, codexEnd]);
  assert.equal(codex.length, 1);
  assert.equal(codex[0].activities[0].status, 'completed');

  const piStart = { ...native(3, 'compaction_start', { reason: 'manual' }, 'pi'), turnId: '' };
  const piEnd = { ...native(4, 'compaction_end', { reason: 'manual', aborted: false }, 'pi'), turnId: '' };
  assert.equal(buildTranscript([piStart])[0].activities[0].status, 'running');
  const pi = buildTranscript([piStart, piEnd]);
  assert.equal(pi.length, 1);
  assert.equal(pi[0].activities[0].status, 'completed');
});

test('manual compact actions group by request identity and reflect only journal start/finish/error', () => {
  const starting = event(1, 'compact_action', { requestId: 'request-a', backend: 'codex', status: 'starting' }, '');
  const [active] = buildTranscript([starting]);
  assert.equal(active.status, 'starting');
  assert.equal(active.activities[0].status, 'starting');
  const done = buildTranscript([starting,
    event(2, 'compact_action', { requestId: 'request-a', backend: 'codex', status: 'completed' }, ''),
    event(3, 'compact_action', { requestId: 'request-b', backend: 'pi', status: 'error', error: 'Summary failed' }, ''),
  ]);
  assert.equal(done.length, 2);
  assert.equal(done[0].activities.length, 1);
  assert.equal(done[0].status, 'completed');
  assert.equal(done[1].status, 'error');
  assert.equal(done[1].error, 'Summary failed');
});

test('compact_action native started/failed aliases preserve real operation status', () => {
  const started = event(1, 'compact_action', { requestId: 'real-request', backend: 'pi', status: 'started' }, '');
  const [running] = buildTranscript([started]);
  assert.equal(running.status, 'starting');
  assert.equal(running.activities[0].status, 'starting');
  const [failed] = buildTranscript([started,
    event(2, 'compact_action', { requestId: 'real-request', backend: 'pi', status: 'failed', error: 'Actual failure' }, ''),
  ]);
  assert.equal(failed.activities.length, 1);
  assert.equal(failed.status, 'failed');
  assert.equal(failed.activities[0].status, 'failed');
  assert.equal(failed.error, 'Actual failure');
});

test('old manual Codex compaction groups native provider turn lifecycle with its exact admin request', () => {
  const events = [
    event(1, 'compact_action', { requestId: 'manual-a', backend: 'codex', threadId: 'root', status: 'started' }, ''),
    backgroundNative(2, 'turn/started', { threadId: 'root', turn: { id: 'compact-turn', status: 'inProgress' } }),
    backgroundNative(3, 'item/started', { threadId: 'root', turnId: 'compact-turn', item: { id: 'compact-item', type: 'contextCompaction' } }),
    backgroundNative(4, 'thread/tokenUsage/updated', { threadId: 'root', turnId: 'compact-turn', tokenUsage: { last: { totalTokens: 100 } } }),
    backgroundNative(5, 'item/completed', { turnId: 'compact-turn', item: { id: 'compact-item', type: 'contextCompaction' } }, 'codex', { rootThreadId: 'root' }),
    backgroundNative(6, 'turn/completed', { thread: { id: 'root' }, turn: { id: 'compact-turn', status: 'completed' } }),
    event(7, 'compact_action', { requestId: 'manual-a', backend: 'codex', threadId: 'root', status: 'completed' }, ''),
  ];
  const turns = buildTranscript(events);
  assert.equal(turns.length, 1, 'the native start must not remain a separate permanently running card');
  assert.equal(turns[0].id, 'compact-bot-1-manual-a');
  assert.equal(turns[0].status, 'completed');
  assert.equal(turns[0].activities.length, 2);
  assert.equal(turns[0].activities.find(activity => activity.id === 'codex-compact-item').status, 'completed');
  assert.deepEqual(turns[0].events, events);
  assert.equal(turns[0].users.length, 0);
  assert.equal(turns[0].responses.length, 0);
});

test('legacy thread/compacted notification proves the provider turn belongs to the manual operation', () => {
  const events = [
    event(1, 'compact_action', { requestId: 'legacy', backend: 'codex', threadId: 'root', status: 'starting' }, ''),
    backgroundNative(2, 'turn/started', { threadId: 'root', turn: { id: 'legacy-turn', status: 'inProgress' } }),
    backgroundNative(3, 'thread/compacted', { threadId: 'root', turnId: 'legacy-turn' }),
    backgroundNative(4, 'turn/completed', { threadId: 'root', turn: { id: 'legacy-turn', status: 'completed' } }),
    event(5, 'compact_action', { requestId: 'legacy', backend: 'codex', threadId: 'root', status: 'completed' }, ''),
  ];
  const turns = buildTranscript(events);
  assert.equal(turns.length, 1);
  assert.equal(turns[0].status, 'completed');
  assert.deepEqual(turns[0].events, events);
});

test('manual compaction never absorbs another provider turn, child, scope, backend, bot or explicit product turn', () => {
  const events = [
    event(1, 'compact_action', { requestId: 'manual', backend: 'codex', threadId: 'root', status: 'started' }, ''),
    backgroundNative(2, 'turn/started', { threadId: 'root', turn: { id: 'compact-turn', status: 'inProgress' } }),
    backgroundNative(3, 'item/started', { threadId: 'root', turnId: 'compact-turn', item: { id: 'compact-item', type: 'contextCompaction' } }),
    backgroundNative(4, 'turn/started', { threadId: 'root', turn: { id: 'actual-goal-turn', status: 'inProgress' } }),
    backgroundNative(5, 'turn/completed', { threadId: 'child', turn: { id: 'compact-turn', status: 'completed' } }, 'codex', { rootThreadId: 'root' }),
    backgroundNative(6, 'turn/completed', { threadId: 'different', turn: { id: 'compact-turn', status: 'completed' } }),
    backgroundNative(7, 'compaction_start', {}, 'pi'),
    backgroundNative(8, 'turn/completed', { turn: { id: 'compact-turn', status: 'completed' } }),
    native(9, 'turn/started', { threadId: 'root', turn: { id: 'owner-turn', status: 'inProgress' } }, 'codex', { rootThreadId: 'root' }),
    { ...backgroundNative(10, 'turn/completed', { threadId: 'root', turn: { id: 'compact-turn', status: 'completed' } }), botId: 'another-bot' },
    backgroundNative(11, 'item/completed', { threadId: 'root', turnId: 'compact-turn', item: { id: 'compact-item', type: 'contextCompaction' } }),
    event(12, 'compact_action', { requestId: 'manual', backend: 'codex', threadId: 'root', status: 'completed' }, ''),
    backgroundNative(13, 'turn/started', { threadId: 'root', turn: { id: 'later-goal-turn', status: 'inProgress' } }),
  ];
  const turns = buildTranscript(events);
  const compact = turns.find(turn => turn.id === 'compact-bot-1-manual');
  assert.deepEqual(compact.events.map(value => value.seq), [1, 2, 3, 11, 12]);
  assert.equal(compact.status, 'completed');
  assert.equal(turns.find(turn => turn.events.some(value => value.seq === 4)).status, 'inProgress');
  assert.equal(turns.find(turn => turn.events.some(value => value.seq === 13)).status, 'inProgress');
  assert.equal(turns.find(turn => turn.id === 'turn-1').events[0].seq, 9);
  assert.equal(turns.flatMap(turn => turn.events).length, events.length);
});

test('manual Pi compaction with no thread scope joins only its active exact bot and backend', () => {
  const events = [
    event(1, 'compact_action', { requestId: 'pi-manual', backend: 'pi', threadId: 'pi-session', status: 'started' }, ''),
    backgroundNative(2, 'compaction_start', { reason: 'manual' }, 'pi'),
    backgroundNative(3, 'compaction_end', { reason: 'manual', aborted: true }, 'pi'),
    event(4, 'compact_action', { requestId: 'pi-manual', backend: 'pi', threadId: 'pi-session', status: 'failed', error: 'Aborted' }, ''),
    backgroundNative(5, 'compaction_start', { reason: 'threshold' }, 'pi'),
  ];
  const turns = buildTranscript(events);
  assert.equal(turns.length, 2);
  assert.deepEqual(turns[0].events, events.slice(0, 4));
  assert.equal(turns[0].status, 'failed');
  assert.equal(turns[0].activities.find(activity => activity.id.startsWith('pi-compaction')).status, 'interrupted');
  assert.equal(turns[1].activities[0].status, 'running');
});

test('future manual native events with explicit product request turn IDs keep their natural association', () => {
  const events = [
    event(1, 'compact_action', { requestId: 'request-turn', backend: 'codex', threadId: 'root', status: 'started' }, 'request-turn'),
    { ...native(2, 'turn/started', { threadId: 'root', turn: { id: 'provider-turn', status: 'inProgress' } }, 'codex', { rootThreadId: 'root' }), turnId: 'request-turn' },
    { ...native(3, 'turn/completed', { threadId: 'root', turn: { id: 'provider-turn', status: 'completed' } }, 'codex', { rootThreadId: 'root' }), turnId: 'request-turn' },
    event(4, 'compact_action', { requestId: 'request-turn', backend: 'codex', threadId: 'root', status: 'completed' }, 'request-turn'),
  ];
  const turns = buildTranscript(events);
  assert.equal(turns.length, 1);
  assert.equal(turns[0].id, 'request-turn');
  assert.equal(turns[0].status, 'completed');
  assert.deepEqual(turns[0].events, events);
});

test('legacy runtime and adapter origin queues resolve a shared call even after the other mirror completed it', () => {
  const args = { botId: 'reviewer', message: 'Review this change' };
  const output = [{ type: 'inputText', text: 'Complete verified result\n'.repeat(500) }];
  const events = [
    native(160, 'item/started', { threadId: 'root', item: { id: 'known-call', type: 'dynamicToolCall', tool: 'bots_send', arguments: args, status: 'inProgress' } }, 'codex', { rootThreadId: 'root' }),
    event(161, 'agent', { type: 'tool_use', toolName: 'bots_send', toolInputRaw: args }),
    event(162, 'agent', { type: 'tool_use', toolName: 'bots_send', toolInput: JSON.stringify(args), metadata: { threadId: 'root' } }),
    native(165, 'item/completed', { threadId: 'root', item: { id: 'known-call', type: 'dynamicToolCall', tool: 'bots_send', arguments: args, status: 'completed', contentItems: output } }, 'codex', { rootThreadId: 'root' }),
    event(166, 'agent', { type: 'tool_result', toolName: 'bots_send', toolResult: JSON.stringify(output) }),
    event(167, 'agent', { type: 'tool_result', toolName: 'bots_send', toolResult: 'Short adapter preview', metadata: { threadId: 'root' } }),
    event(168, 'turn', { status: 'completed' }),
  ];
  const [turn] = buildTranscript(events);
  assert.equal(turn.activities.length, 1);
  assert.equal(turn.activities[0].callId, 'known-call');
  assert.deepEqual(turn.activities[0].output, output);
  assert.equal(turn.events.length, events.length);
});
