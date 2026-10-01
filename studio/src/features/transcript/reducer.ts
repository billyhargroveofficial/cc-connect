import type { Event as JournalEvent } from '../../lib/types';

export type RecordValue = Record<string, unknown>;
export type ActivityKind = 'thinking' | 'commentary' | 'tool' | 'search' | 'subagent' | 'plan' | 'goal' | 'event';

export interface TranscriptAttachment {
  id: string;
  name: string;
  mimeType: string;
  url?: string;
}

export interface TranscriptMessage {
  id: string;
  role: string;
  content: string;
  attachments: TranscriptAttachment[];
  source?: string;
  artifact?: boolean;
  time: string;
}

export interface Activity {
  id: string;
  callId?: string;
  kind: ActivityKind;
  title: string;
  status: string;
  text: string;
  input?: unknown;
  output?: unknown;
  data: RecordValue;
  seq: number;
  startedAt: string;
  endedAt?: string;
  aliases: string[];
  native: boolean;
  thread?: TranscriptTurn;
}

export interface Question {
  id: string;
  header: string;
  question: string;
  options: { label: string; description: string }[];
  multiSelect: boolean;
  isSecret: boolean;
  allowOther: boolean;
}

export interface UserRequest {
  id: string;
  title: string;
  input: unknown;
  questions: Question[];
  method: string;
  placeholder: string;
  resolved: boolean;
  behavior?: string;
  seq: number;
}

export interface TranscriptTurn {
  id: string;
  status: string;
  time: string;
  backend: string;
  model: string;
  effort: string;
  serviceTier: string;
  outputTokens?: number;
  tokensPerSecond?: number;
  generationMs?: number;
  error?: string;
  users: TranscriptMessage[];
  responses: TranscriptMessage[];
  activities: Activity[];
  requests: UserRequest[];
  notices: string[];
  events: JournalEvent[];
}

export function record(value: unknown): RecordValue {
  return value !== null && typeof value === 'object' && !Array.isArray(value) ? value as RecordValue : {};
}

export function string(value: unknown): string {
  return typeof value === 'string' ? value : '';
}

export function field(value: RecordValue, camel: string, pascal?: string): unknown {
  return value[camel] ?? value[pascal ?? camel.charAt(0).toUpperCase() + camel.slice(1)];
}

function array(value: unknown): unknown[] {
  return Array.isArray(value) ? value : [];
}

function finiteNumber(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) ? value : undefined;
}

export function json(value: unknown): string {
  if (value === undefined || value === null) return '';
  if (typeof value === 'string') return value;
  try { return JSON.stringify(value, null, 2); } catch { return String(value); }
}

function text(value: unknown): string {
  if (typeof value === 'string') return value;
  if (Array.isArray(value)) return value.map(text).filter(Boolean).join('\n\n');
  const v = record(value);
  return string(v.text) || string(v.thinking) || string(v.content);
}

export function isRunning(status: string): boolean {
  return ['running', 'starting', 'queued', 'inProgress', 'in_progress', 'waiting', 'waiting_permission', 'retrying'].includes(status);
}

export function isFailed(status: string): boolean {
  return ['error', 'failed', 'declined', 'denied'].includes(status);
}

function terminal(status: string): boolean {
  return ['completed', 'complete', 'failed', 'error', 'interrupted', 'stopped', 'cancelled', 'canceled'].includes(status);
}

function normalizedStatus(value: unknown): string {
  const status = string(value);
  if (status === 'in_progress') return 'inProgress';
  if (status === 'complete') return 'completed';
  return status;
}

function newTurn(id: string, time: string, status = 'running'): TranscriptTurn {
  return {
    id, status, time, backend: '', model: '', effort: '', serviceTier: '',
    users: [], responses: [], activities: [], requests: [], notices: [], events: [],
  };
}

function nativeThreadId(params: RecordValue): string {
  return string(params.threadId) || string(record(params.thread).id) || string(record(params.goal).threadId);
}

function productTurnKey(event: JournalEvent): string {
  if (!event.turnId) {
    const data = record(event.data);
    if (event.type === 'compact_action' && string(data.requestId)) return `compact-${event.botId}-${string(data.requestId)}`;
    if (event.type === 'native' && data.backend !== 'pi') {
      const params = record(data.params);
      const item = record(params.item);
      if (item.type === 'contextCompaction' && string(item.id)) {
        return `compact-native-${event.botId}-${nativeThreadId(params)}-${string(item.id)}`;
      }
    }
  }
  return event.turnId || `event-${event.seq}`;
}

interface ManualCompaction {
  id: string;
  requestId: string;
  backend: string;
  threadId: string;
  events: JournalEvent[];
  providerTurns: Set<string>;
}

function compactionProof(data: RecordValue): boolean {
  return record(record(data.params).item).type === 'contextCompaction'
    || data.method === 'thread/compacted' || data.method === 'thread_compacted';
}

/** Older runtimes journal manual native notifications without a product turn.
 * Associate them only within the exact admin request and owning session. Codex
 * compaction items/notifications prove which provider turn belongs to it, so a
 * preceding turn/started can join without absorbing a concurrent goal turn. */
function manualCompactionGroups(events: JournalEvent[]): Map<number, ManualCompaction> {
  const active = new Map<string, ManualCompaction>();
  const operations: ManualCompaction[] = [];
  for (const event of events) {
    const data = record(event.data);
    if (event.type === 'compact_action') {
      const requestId = string(data.requestId);
      if (requestId && (data.status === 'started' || data.status === 'starting')) {
        const operation: ManualCompaction = {
          id: productTurnKey(event), requestId, backend: string(data.backend), threadId: string(data.threadId),
          events: [], providerTurns: new Set(),
        };
        active.set(event.botId, operation); operations.push(operation);
      } else if (terminal(string(data.status)) && active.get(event.botId)?.requestId === requestId) {
        active.delete(event.botId);
      }
      continue;
    }
    if (event.type !== 'native' || event.turnId) continue;
    const operation = active.get(event.botId);
    if (!operation || !operation.backend || data.backend !== operation.backend) continue;
    const params = record(data.params);
    const scope = nativeThreadId(params), root = string(data.rootThreadId);
    const owner = root || scope;
    // Metadata identifies the root; params retain the actual emitting scope.
    // A descendant cannot become the owner even when its provider turn matches.
    if (scope && root && scope !== root) continue;
    if (operation.backend !== 'pi' || owner) {
      if (!operation.threadId || !owner || owner !== operation.threadId) continue;
    }
    operation.events.push(event);
    const providerTurn = string(params.turnId) || string(record(params.turn).id);
    if (providerTurn && compactionProof(data)) operation.providerTurns.add(providerTurn);
  }
  const groups = new Map<number, ManualCompaction>();
  for (const operation of operations) {
    for (const event of operation.events) {
      const data = record(event.data), params = record(data.params);
      const providerTurn = string(params.turnId) || string(record(params.turn).id);
      if (operation.backend === 'pi' || compactionProof(data) || operation.providerTurns.has(providerTurn)) {
        groups.set(event.seq, operation);
      }
    }
  }
  return groups;
}

/** The owning thread is provider metadata, never the first thread that happens
 * to emit an item. Legacy journals may infer it from a turn/started event, after
 * excluding declared subagent receiver ids. Without that evidence, scoped
 * events remain in a session card and cannot change the parent conversation. */
function nativeRoots(events: JournalEvent[]): Map<string, string> {
  const roots = new Map<string, string>();
  const childThreads = new Set<string>();
  for (const event of events) {
    if (event.type !== 'native') continue;
    const data = record(event.data);
    if (data.backend === 'pi') continue;
    const params = record(data.params);
    const rootId = string(data.rootThreadId);
    if (rootId) roots.set(productTurnKey(event), rootId);
    const item = record(params.item);
    if (item.type === 'collabAgentToolCall') array(item.receiverThreadIds).map(string).forEach(id => { if (id) childThreads.add(id); });
    if (item.type === 'subAgentActivity' && string(item.agentThreadId)) childThreads.add(string(item.agentThreadId));
    const thread = record(params.thread);
    if (record(thread.source).subAgent) {
      const id = string(thread.id);
      if (id) childThreads.add(id);
    }
  }
  for (const event of events) {
    if (event.type !== 'native' || roots.has(productTurnKey(event))) continue;
    const data = record(event.data);
    const params = record(data.params);
    if (data.backend !== 'pi' && data.method === 'turn/started') {
      const threadId = nativeThreadId(params);
      if (threadId && !childThreads.has(threadId)) roots.set(productTurnKey(event), threadId);
    }
  }
  return roots;
}

function attachments(value: unknown): TranscriptAttachment[] {
  return array(value).map(record).map(v => ({
    id: string(v.id), name: string(v.name) || 'Файл', mimeType: string(v.mimeType), url: string(v.url) || undefined,
  })).filter(v => Boolean(v.id || v.url));
}

function message(event: JournalEvent, data: RecordValue): TranscriptMessage {
  return {
    id: `message-${event.seq}`, role: string(data.role) || 'assistant', content: string(data.content) || string(data.caption),
    attachments: attachments(data.attachments), source: string(data.source) || undefined, time: event.time,
    artifact: data.artifact === true || data.source === 'files',
  };
}

function question(value: unknown, index: number): Question {
  const q = record(value);
  return {
    id: string(q.id) || `question-${index}`, header: string(field(q, 'header')),
    question: string(field(q, 'question')) || string(q.title),
    options: array(field(q, 'options')).map(value => {
      const option = record(value);
      return typeof value === 'string' ? { label: value, description: '' } : {
        label: string(field(option, 'label')), description: string(field(option, 'description')),
      };
    }).filter(option => Boolean(option.label)),
    multiSelect: field(q, 'multiSelect') === true, isSecret: q.isSecret === true,
    // Native requestUserInput explicitly controls the free-form option; older
    // normalized questions omit isOther and support free-form input.
    allowOther: q.isOther !== false,
  };
}

function addRequest(turn: TranscriptTurn, request: UserRequest) {
  const existing = turn.requests.find(v => v.id === request.id);
  if (existing) {
    const resolved = existing.resolved;
    Object.assign(existing, request, { resolved });
  } else turn.requests.push(request);
}

function addActivity(turn: TranscriptTurn, event: JournalEvent, id: string, kind: ActivityKind): Activity {
  const existing = turn.activities.find(v => v.id === id);
  if (existing) return existing;
  const activity: Activity = {
    id, kind, title: '', status: 'running', text: '', data: {}, aliases: [],
    seq: event.seq, startedAt: event.time, native: true,
  };
  turn.activities.push(activity);
  return activity;
}

function itemKind(type: string): ActivityKind {
  if (type === 'reasoning') return 'thinking';
  if (type === 'webSearch') return 'search';
  if (type === 'collabAgentToolCall' || type === 'subAgentActivity') return 'subagent';
  if (type === 'plan') return 'plan';
  if (['commandExecution', 'mcpToolCall', 'dynamicToolCall', 'fileChange', 'functionCallOutput'].includes(type)) return 'tool';
  return 'event';
}

function searchTitle(item: RecordValue): string {
  const action = record(item.action);
  if (action.type === 'open_page' || action.type === 'openPage') return 'Открывает страницу';
  if (action.type === 'find_in_page' || action.type === 'findInPage') return 'Ищет на странице';
  return 'Поиск в интернете';
}

function setCodexItem(turn: TranscriptTurn, event: JournalEvent, item: RecordValue, completed: boolean) {
  const type = string(item.type);
  const id = `codex-${string(item.id) || event.seq}`;
  if (type === 'userMessage') return; // The product message also holds attachment URLs and its source.
  if (type === 'agentMessage' && item.phase !== 'commentary') {
    const existing = turn.responses.find(v => v.id === id);
    if (existing) existing.content = string(item.text);
    else turn.responses.push({ id, role: 'assistant', content: string(item.text), attachments: [], time: event.time });
    return;
  }
  const activity = addActivity(turn, event, id, type === 'agentMessage' ? 'commentary' : itemKind(type));
  if (activity.kind === 'tool' || activity.kind === 'search') activity.callId = string(item.id) || undefined;
  activity.data = { ...activity.data, ...item };
  activity.status = normalizedStatus(item.status) || (completed ? 'completed' : 'running');
  if (completed) activity.endedAt = event.time;
  switch (type) {
    case 'agentMessage':
      activity.title = 'Сообщение во время работы'; activity.text = string(item.text); break;
    case 'reasoning':
      activity.title = 'Размышления';
      // Summaries and content are distinct provider-exposed fields. Preserve
      // both, but do not display identical copies supplied by an older server.
      activity.text = [...new Set([text(item.summary), text(item.content)].filter(Boolean))].join('\n\n');
      break;
    case 'commandExecution':
      activity.title = 'Команда'; activity.input = item.command;
      activity.output = item.aggregatedOutput ?? activity.output;
      activity.aliases = ['Bash', 'bash', 'shell', 'commandExecution'];
      if (typeof item.exitCode === 'number' && item.exitCode !== 0) activity.status = 'failed';
      break;
    case 'mcpToolCall':
      activity.title = [string(item.server), string(item.tool)].filter(Boolean).join(' · ') || 'MCP';
      activity.input = item.arguments; activity.output = item.result ?? item.error;
      activity.aliases = ['MCP', string(item.tool), `${string(item.server)}:${string(item.tool)}`];
      if (item.error) activity.status = 'failed';
      break;
    case 'dynamicToolCall':
      activity.title = [string(item.namespace), string(item.tool)].filter(Boolean).join(' · ') || 'Инструмент';
      activity.input = item.arguments; activity.output = item.contentItems;
      activity.aliases = [string(item.tool)];
      if (item.success === false) activity.status = 'failed';
      break;
    case 'webSearch': {
      const action = record(item.action);
      activity.title = searchTitle(item); activity.input = item.action ?? item.query;
      activity.text = string(action.query) || array(action.queries).map(string).join(' · ') || string(action.url) || string(item.query);
      activity.output = item.results; activity.aliases = ['WebSearch', 'WebFetch', 'web_search', 'webSearch'];
      break;
    }
    case 'fileChange':
      activity.title = 'Изменения файлов'; activity.input = item.changes; activity.aliases = ['Patch', 'apply_patch', 'fileChange'];
      break;
    case 'functionCallOutput':
      activity.title = string(item.name) || 'Результат инструмента'; activity.output = item.output;
      activity.aliases = [string(item.name)];
      break;
    case 'collabAgentToolCall':
      activity.title = string(item.tool) || 'Сабагенты'; activity.text = string(item.prompt);
      activity.input = { prompt: item.prompt, model: item.model, reasoningEffort: item.reasoningEffort };
      activity.output = item.agentsStates; activity.aliases = [string(item.tool)];
      break;
    case 'subAgentActivity':
      activity.title = string(item.agentPath) ? `Сабагент · ${string(item.agentPath).split('/').filter(Boolean).at(-1)}` : 'Сабагент';
      activity.text = string(item.prompt) || string(item.description);
      activity.status = normalizedStatus(item.kind) || activity.status;
      break;
    case 'plan': activity.title = 'План'; activity.text = string(item.text); break;
    case 'contextCompaction': activity.title = 'Сжатие контекста'; break;
    case 'imageView': activity.title = 'Просмотр изображения'; activity.text = string(item.path); break;
    case 'imageGeneration': activity.title = 'Создание изображения'; break;
    case 'sleep': activity.title = 'Ожидание'; break;
    default: activity.title = type || 'Событие'; activity.text = string(item.text);
  }
}

function codexNative(turn: TranscriptTurn, event: JournalEvent, data: RecordValue) {
  const method = string(data.method);
  const params = record(data.params);
  if (method === 'item/started' || method === 'item/completed') {
    const item = record(params.item);
    const completed = method === 'item/completed';
    setCodexItem(turn, event, item, completed);
    if (!event.turnId && item.type === 'contextCompaction') turn.status = completed ? 'completed' : 'running';
    return;
  }
  if (method === 'item/agentMessage/delta') {
    const id = `codex-${string(params.itemId) || event.seq}`;
    const commentary = turn.activities.find(v => v.id === id && v.kind === 'commentary');
    if (commentary) commentary.text += string(params.delta);
    else {
      const response = turn.responses.find(v => v.id === id);
      if (response) response.content += string(params.delta);
      else turn.responses.push({ id, role: 'assistant', content: string(params.delta), attachments: [], time: event.time });
    }
    return;
  }
  if (method === 'item/reasoning/summaryTextDelta' || method === 'item/reasoning/textDelta') {
    const activity = addActivity(turn, event, `codex-${string(params.itemId) || event.seq}`, 'thinking');
    activity.title = 'Размышления';
    const key = method.includes('summaryText') ? '_summaryParts' : '_contentParts';
    const index = finiteNumber(params.summaryIndex ?? params.contentIndex) ?? 0;
    const parts = array(activity.data[key]);
    parts[index] = string(parts[index]) + string(params.delta);
    activity.data[key] = parts;
    activity.text = [...new Set([text(activity.data._summaryParts), text(activity.data._contentParts)].filter(Boolean))].join('\n\n');
    return;
  }
  if (method === 'item/commandExecution/outputDelta' || method === 'item/fileChange/outputDelta') {
    const activity = addActivity(turn, event, `codex-${string(params.itemId) || event.seq}`, 'tool');
    activity.callId = string(params.itemId) || activity.callId;
    activity.title ||= method.includes('commandExecution') ? 'Команда' : 'Изменения файлов';
    activity.output = string(activity.output) + string(params.delta); return;
  }
  if (method === 'item/plan/delta') {
    const activity = addActivity(turn, event, `codex-${string(params.itemId) || event.seq}`, 'plan');
    activity.title = 'План'; activity.text += string(params.delta); return;
  }
  if (method === 'turn/plan/updated') {
    const activity = addActivity(turn, event, 'codex-plan', 'plan');
    activity.title = 'План'; activity.data = params; activity.text = string(params.explanation);
    activity.status = 'completed'; return;
  }
  if (method === 'thread/goal/updated') {
    const activity = addActivity(turn, event, 'codex-goal', 'goal');
    activity.title = 'Цель'; activity.data = record(params.goal); activity.text = string(activity.data.objective);
    activity.status = normalizedStatus(activity.data.status) || 'active'; return;
  }
  if (method === 'thread/goal/cleared') {
    const activity = addActivity(turn, event, 'codex-goal', 'goal');
    activity.title = 'Цель снята'; activity.status = 'completed'; activity.data = params; return;
  }
  if (method === 'turn/started') {
    turn.status = normalizedStatus(record(params.turn).status) || 'running'; return;
  }
  if (method === 'turn/completed') {
    const nativeTurn = record(params.turn);
    turn.status = normalizedStatus(nativeTurn.status) || 'completed';
    turn.error = string(record(nativeTurn.error).message) || turn.error; return;
  }
  if (method.endsWith('/requestApproval') || method === 'item/tool/requestUserInput') {
    if (data.requestId === undefined) return;
    const id = JSON.stringify(data.requestId);
    addRequest(turn, {
      id, title: method === 'item/tool/requestUserInput' ? 'Нужен ваш ответ' : 'Нужно разрешение',
      input: params.command ?? params.reason ?? params, questions: array(params.questions).map(question),
      method: 'codex', placeholder: '', resolved: false, seq: event.seq,
    });
    return;
  }
  if (method === 'error') {
    const err = string(params.message) || string(record(params.error).message);
    if (err && params.willRetry !== true) turn.error = err;
    if (err) turn.notices.push(err);
  }
}

function scopedCodexNative(turn: TranscriptTurn, event: JournalEvent, data: RecordValue, rootId?: string) {
  const params = record(data.params);
  const threadId = nativeThreadId(params);
  const owningThreadId = string(data.rootThreadId) || rootId;
  // A normal startup thread is connection metadata. An explicitly declared
  // child thread still gets its own visible activity, including old journals.
  if (!event.turnId && !owningThreadId && data.method === 'thread/started'
    && !record(record(params.thread).source).subAgent) return;
  if (!threadId || threadId === owningThreadId) {
    codexNative(turn, event, data); return;
  }
  const activity = addActivity(turn, event, `codex-thread-${threadId}`, 'subagent');
  const thread = { ...record(activity.data.thread), ...record(params.thread) };
  const name = string(thread.name) || string(thread.agentNickname);
  activity.title = owningThreadId ? name ? `Сабагент · ${name}` : 'Сабагент' : 'Сессия агента';
  activity.data = {
    ...activity.data, threadId, rootThreadId: owningThreadId || null,
    ...(Object.keys(thread).length ? { thread } : {}),
  };
  const child = activity.thread ?? newTurn(threadId, event.time);
  activity.thread = child;
  child.backend = 'codex';
  child.events.push(event);
  codexNative(child, event, data);
  activity.status = child.status;
  if (terminal(child.status)) activity.endedAt = event.time;
}

interface PiState { messageIndex: number; activeMessage: string; activeCompaction?: string }

function piBlock(turn: TranscriptTurn, event: JournalEvent, messageId: string, index: number, value: unknown, complete = false) {
  const block = record(value);
  const id = `${messageId}-${index}`;
  switch (block.type) {
    case 'text': {
      const response = turn.responses.find(v => v.id === id);
      if (response) response.content = string(block.text);
      else turn.responses.push({ id, role: 'assistant', content: string(block.text), attachments: [], time: event.time });
      break;
    }
    case 'thinking': {
      const activity = addActivity(turn, event, id, 'thinking');
      activity.title = 'Размышления'; activity.text = string(block.thinking);
      activity.status = complete ? 'completed' : 'running';
      // Retain signatures in the raw journal, not the visual reasoning block.
      activity.data = { type: 'thinking', redacted: block.redacted === true }; break;
    }
    case 'toolCall': {
      const activity = addActivity(turn, event, `pi-tool-${string(block.id) || id}`, 'tool');
      activity.callId = string(block.id) || undefined;
      activity.title = string(block.name) || 'Инструмент'; activity.aliases = [activity.title];
      activity.input = block.arguments; activity.data = { ...activity.data, ...block }; break;
    }
  }
}

function piMessage(turn: TranscriptTurn, event: JournalEvent, params: RecordValue, state: PiState, complete: boolean) {
  const msg = record(params.message);
  if (msg.role === 'toolResult') {
    const activity = addActivity(turn, event, `pi-tool-${string(msg.toolCallId) || event.seq}`, 'tool');
    activity.callId = string(msg.toolCallId) || activity.callId;
    activity.title ||= string(msg.toolName) || 'Инструмент'; activity.aliases = [activity.title];
    activity.output = msg.content; activity.status = msg.isError === true ? 'failed' : 'completed';
    activity.data = { ...activity.data, ...msg }; activity.endedAt = event.time; return;
  }
  if (msg.role !== 'assistant') return;
  array(msg.content).forEach((block, index) => piBlock(turn, event, state.activeMessage, index, block, complete));
  if (string(msg.errorMessage)) turn.notices.push(string(msg.errorMessage));
}

function piNative(turn: TranscriptTurn, event: JournalEvent, data: RecordValue, state: PiState) {
  const params = record(data.params);
  const method = string(data.method) || string(params.type);
  switch (method) {
    case 'message_start':
      if (record(params.message).role === 'assistant') {
        state.messageIndex += 1; state.activeMessage = `pi-message-${state.messageIndex}`;
      }
      piMessage(turn, event, params, state, false); break;
    case 'message_update': {
      const update = record(params.assistantMessageEvent);
      const index = finiteNumber(update.contentIndex) ?? 0;
      const id = `${state.activeMessage}-${index}`;
      if (update.type === 'text_delta') {
        const response = turn.responses.find(v => v.id === id);
        if (response) response.content += string(update.delta);
        else turn.responses.push({ id, role: 'assistant', content: string(update.delta), attachments: [], time: event.time });
      } else if (update.type === 'thinking_delta') {
        const activity = addActivity(turn, event, id, 'thinking');
        activity.title = 'Размышления'; activity.text += string(update.delta);
      } else if (update.type === 'text_end') {
        piBlock(turn, event, state.activeMessage, index, { type: 'text', text: update.content }, true);
      } else if (update.type === 'thinking_end') {
        piBlock(turn, event, state.activeMessage, index, { type: 'thinking', thinking: update.content }, true);
      } else if (update.type === 'toolcall_end') {
        piBlock(turn, event, state.activeMessage, index, update.toolCall);
      }
      break;
    }
    case 'message_end': piMessage(turn, event, params, state, true); break;
    case 'tool_execution_start':
    case 'tool_execution_update':
    case 'tool_execution_end': {
      const activity = addActivity(turn, event, `pi-tool-${string(params.toolCallId) || event.seq}`, 'tool');
      activity.callId = string(params.toolCallId) || undefined;
      activity.title = string(params.toolName) || 'Инструмент'; activity.aliases = [activity.title];
      activity.input = params.args ?? activity.input;
      activity.data = { ...activity.data, ...params };
      if (method === 'tool_execution_update') activity.output = params.partialResult;
      if (method === 'tool_execution_end') {
        activity.output = params.result; activity.status = params.isError === true ? 'failed' : 'completed';
        activity.endedAt = event.time;
      }
      break;
    }
    case 'extension_ui_request': {
      const requestMethod = string(params.method);
      if (['input', 'select', 'confirm'].includes(requestMethod)) {
        addRequest(turn, {
          id: `pi_ext_${string(params.id)}`, title: string(params.title) || 'Нужен ваш ответ',
          input: params.message, method: requestMethod, placeholder: string(params.placeholder),
          questions: requestMethod === 'select' ? [question({ question: params.title, options: params.options }, 0)] : [],
          resolved: false, seq: event.seq,
        });
      } else if (requestMethod === 'notify' && string(params.message)) turn.notices.push(string(params.message));
      break;
    }
    case 'agent_start': turn.status = 'running'; break;
    case 'agent_end':
      if (params.willRetry === true) { turn.status = 'retrying'; break; }
      // In Pi RPC mode agent_end can precede compaction, retries, or queued
      // continuations. Only agent_settled or the product turn confirms finish.
      break;
    case 'agent_settled': turn.status = 'completed'; break;
    case 'auto_retry_start': turn.status = 'retrying'; break;
    case 'auto_retry_end': if (params.success === false) turn.notices.push(string(params.finalError)); break;
    case 'compaction_start':
    case 'auto_compaction_start': {
      const previous = state.activeCompaction && turn.activities.find(activity => activity.id === state.activeCompaction);
      const id = previous && isRunning(previous.status) ? previous.id : `pi-compaction-${event.seq}`;
      state.activeCompaction = id;
      const activity = addActivity(turn, event, id, 'event');
      activity.title = 'Сжатие контекста'; activity.status = 'running'; activity.data = params;
      if (!event.turnId) turn.status = 'running';
      break;
    }
    case 'compaction_end':
    case 'auto_compaction_end': {
      const activity = addActivity(turn, event, state.activeCompaction || `pi-compaction-${event.seq}`, 'event');
      activity.title = 'Сжатие контекста'; activity.status = string(params.errorMessage) ? 'failed' : params.aborted === true ? 'interrupted' : 'completed';
      activity.data = { ...activity.data, ...params }; activity.endedAt = event.time;
      if (!event.turnId) turn.status = activity.status;
      state.activeCompaction = undefined; break;
    }
  }
}

interface NormalizedState { pending: Map<string, string[]> }

function comparableInput(value: unknown): string | undefined {
  if (value === undefined || value === null) return undefined;
  let parsed = value;
  if (typeof value === 'string') {
    try { parsed = JSON.parse(value); } catch { parsed = value.trim(); }
  }
  function stable(value: unknown): unknown {
    if (Array.isArray(value)) return value.map(stable);
    if (value && typeof value === 'object') return Object.fromEntries(Object.entries(record(value))
      .sort(([a], [b]) => a.localeCompare(b)).map(([key, entry]) => [key, stable(entry)]));
    return value;
  }
  return JSON.stringify(stable(parsed));
}

function normalizedAgent(turn: TranscriptTurn, event: JournalEvent, data: RecordValue, state: NormalizedState) {
  const type = string(field(data, 'type'));
  const content = string(field(data, 'content'));
  const name = string(field(data, 'toolName'));
  const metadata = record(field(data, 'metadata'));
  let callId = string(field(data, 'toolCallId', 'ToolCallID')) || string(data.callId)
    || string(metadata.toolCallId) || string(metadata.callId);
  const origin = `${string(metadata.threadId) || 'unscoped'}:${name}`;
  switch (type) {
    case 'thinking': {
      const activity = addActivity(turn, event, `fallback-thinking-${event.seq}`, 'thinking');
      activity.native = false; activity.title = 'Размышления'; activity.text = content; activity.status = 'completed'; break;
    }
    case 'tool_use': {
      const input = field(data, 'toolInputRaw') ?? field(data, 'toolInput');
      if (!callId) {
        // Old journals did not include call IDs on normalized mirrors. Only a
        // uniquely active native call with exactly equal arguments is evidence
        // for correlation. Ambiguous or different calls remain visible.
        const active = turn.activities.filter(activity => activity.native && activity.callId
          && activity.aliases.includes(name) && isRunning(activity.status));
        if (active.length === 1 && comparableInput(input) !== undefined
          && comparableInput(input) === comparableInput(active[0].input)) callId = active[0].callId || '';
      }
      const activity = addActivity(turn, event, callId ? `fallback-call-${callId}` : `fallback-tool-${event.seq}`, 'tool');
      activity.callId = callId || undefined;
      activity.native = false; activity.title = name || 'Инструмент'; activity.aliases = [name];
      activity.input = input; activity.data = data;
      const pending = state.pending.get(origin) || [];
      if (!pending.includes(activity.id)) pending.push(activity.id);
      state.pending.set(origin, pending);
      break;
    }
    case 'tool_result': {
      const pending = state.pending.get(origin) || [];
      let match = callId ? turn.activities.find(activity => !activity.native && activity.callId === callId) : undefined;
      if (!callId) {
        const candidates = pending.map(id => turn.activities.find(activity => activity.id === id))
          // Each origin owns its pending use. Another mirror may already have
          // completed the shared, identified call, but cannot consume this
          // origin's result or erase the identity established at its use.
          .filter((activity): activity is Activity => Boolean(activity && (isRunning(activity.status) || activity.callId)));
        const input = field(data, 'toolInputRaw') ?? field(data, 'toolInput');
        const matchingInput = comparableInput(input) === undefined ? []
          : candidates.filter(activity => comparableInput(activity.input) === comparableInput(input));
        // Never assign an out-of-order result to an arbitrary parallel call.
        // A single pending use or a unique exact argument match is evidence.
        match = candidates.length === 1 ? candidates[0] : matchingInput.length === 1 ? matchingInput[0] : undefined;
      }
      if (match) state.pending.set(origin, pending.filter(id => id !== match.id));
      const activity = match
        ?? addActivity(turn, event, callId ? `fallback-call-${callId}` : `fallback-tool-${event.seq}`, 'tool');
      activity.callId ||= callId || undefined;
      activity.native = false; activity.title ||= name || 'Инструмент'; activity.aliases = [activity.title];
      activity.input ??= field(data, 'toolInput'); activity.output = field(data, 'toolResult') || content;
      activity.status = field(data, 'toolSuccess') === false ? 'failed' : normalizedStatus(field(data, 'toolStatus')) || 'completed';
      activity.data = { ...activity.data, ...data }; activity.endedAt = event.time; break;
    }
    case 'permission_request': {
      const rawInput = record(field(data, 'toolInputRaw'));
      const questions = array(rawInput.questions ?? field(data, 'questions')).map(question);
      addRequest(turn, {
        id: string(field(data, 'requestID', 'RequestID')) || string(data.requestId),
        title: string(rawInput.title) || (questions.length ? 'Нужен ваш ответ' : 'Нужно разрешение'),
        input: rawInput.command ?? rawInput.message ?? field(data, 'toolInput'),
        questions, method: string(rawInput.method) || name, placeholder: string(rawInput.placeholder),
        resolved: false, seq: event.seq,
      });
      break;
    }
    case 'error': {
      const err = field(data, 'error');
      turn.error = string(err) || string(record(err).message) || content || 'Не удалось завершить работу'; break;
    }
    case 'result':
      if (field(data, 'done') === true && !terminal(turn.status)) turn.status = 'completed';
      turn.outputTokens = finiteNumber(field(data, 'outputTokens')) ?? turn.outputTokens;
      break;
  }
}

function finalize(turn: TranscriptTurn) {
  const nativeActivities = turn.activities.filter(v => v.native);
  turn.activities = turn.activities.filter(activity => {
    if (activity.native) return true;
    if (activity.kind === 'thinking') return !nativeActivities.some(v => v.kind === 'thinking' || v.kind === 'commentary');
    if (activity.kind === 'tool') {
      // IDs come from provider/runtime provenance, or from a uniquely active
      // equal-argument legacy use. Names alone cannot identify an invocation.
      const native = activity.callId && nativeActivities.find(native => native.callId === activity.callId);
      if (native) {
        native.input ??= activity.input;
        native.output ??= activity.output;
        return false;
      }
    }
    return true;
  });
  const nativeResponses = turn.responses.filter(v => !v.id.startsWith('message-') && !v.id.startsWith('fallback-'));
  const canonicalResponses = turn.responses.filter(v => v.id.startsWith('message-'));
  const hasCanonicalText = canonicalResponses.some(response => !response.artifact && response.content.trim());
  const nativeText = nativeResponses.map(v => v.content.trim()).filter(Boolean);
  const commentaryText = turn.activities.filter(v => v.kind === 'commentary').map(v => v.text.trim()).filter(Boolean);
  if (canonicalResponses.length) {
    turn.responses = turn.responses.filter(v => !v.id.startsWith('fallback-'));
    for (const response of canonicalResponses) {
      // Files are separate published artifacts with their own caption and seq.
      // An identical final answer does not make that publication a duplicate.
      if (response.artifact) continue;
      const value = response.content.trim();
      if (value && (nativeText.includes(value) || nativeText.join('\n\n') === value || nativeText.join('\n') === value || nativeText.join('') === value || commentaryText.includes(value))) {
        turn.responses = turn.responses.filter(v => v.id !== response.id);
        if (response.attachments.length) {
          const last = nativeResponses.at(-1);
          if (last) last.attachments.push(...response.attachments);
          else turn.responses.push({ ...response, content: '' });
        }
      }
    }
  } else if (nativeResponses.length) turn.responses = turn.responses.filter(v => !v.id.startsWith('fallback-'));
  // Older adapters expose only text deltas. Keep those as one response when no
  // native item or finalized product message exists.
  if (!nativeResponses.length && !hasCanonicalText) {
    const fragments = turn.events.filter(v => v.type === 'agent').map(v => record(v.data))
      .filter(v => field(v, 'type') === 'text').map(v => string(field(v, 'content')));
    if (fragments.length) turn.responses.push({
      id: `fallback-text-${turn.id}`, role: 'assistant', content: fragments.join(''), attachments: [], time: turn.time,
    });
  }
  turn.responses = turn.responses.filter(v => v.content.trim() || v.attachments.length);
  turn.notices = [...new Set(turn.notices.filter(Boolean))];
  for (const activity of turn.activities) {
    if (activity.thread) {
      finalize(activity.thread);
      activity.status = activity.thread.status;
      continue;
    }
    if (terminal(turn.status) && isRunning(activity.status)) activity.status = turn.status === 'completed' ? 'completed' : 'interrupted';
  }
  for (const request of turn.requests) {
    if (terminal(turn.status)) request.resolved = true;
  }
  turn.requests = turn.requests.filter(v => v.id);
}

/** Rebuilds a view from an append-only journal. Replay, overlap and unordered
 * snapshot/SSE delivery are safe: sequence numbers are the deduplication key.
 * No raw event is changed or discarded from its turn's journal. */
export function buildTranscript(events: JournalEvent[], botId?: string): TranscriptTurn[] {
  const unique = new Map<number, JournalEvent>();
  for (const event of events) {
    if (!botId || event.botId === botId) unique.set(event.seq, event);
  }
  const ordered = [...unique.values()].sort((a, b) => a.seq - b.seq);
  const roots = nativeRoots(ordered);
  const manualCompactions = manualCompactionGroups(ordered);
  for (const operation of manualCompactions.values()) {
    if (operation.backend === 'codex' && operation.threadId) roots.set(operation.id, operation.threadId);
  }
  const turns: TranscriptTurn[] = [];
  const byId = new Map<string, TranscriptTurn>();
  const piStates = new Map<string, PiState>();
  const normalizedStates = new Map<string, NormalizedState>();
  const resolvedRequests = new Map<string, string>();
  const backgroundCompactions = new Map<string, string>();
  for (const event of ordered) {
    const data = record(event.data);
    if ((event.type === 'permission' || event.type === 'system') && string(data.requestId)
      && (data.behavior || data.status === 'resolved')) {
      resolvedRequests.set(string(data.requestId), string(data.behavior));
    }
    // Metadata without a product turn stays independent. Do not attach stale
    // startup or goal notifications to the most recent user conversation.
    const manualCompaction = manualCompactions.get(event.seq);
    let id = manualCompaction?.id || productTurnKey(event);
    if (!manualCompaction && !event.turnId && event.type === 'native' && data.backend === 'pi') {
      const method = string(data.method);
      if (method === 'compaction_start' || method === 'auto_compaction_start') {
        id = backgroundCompactions.get(event.botId) || `pi-background-compaction-${event.botId}-${event.seq}`;
        backgroundCompactions.set(event.botId, id);
      } else if (method === 'compaction_end' || method === 'auto_compaction_end') {
        id = backgroundCompactions.get(event.botId) || id;
        backgroundCompactions.delete(event.botId);
      }
    }
    let turn = byId.get(id);
    if (!turn) {
      turn = newTurn(id, event.time, event.turnId ? 'running' : 'completed');
      byId.set(id, turn); turns.push(turn);
      piStates.set(id, { messageIndex: 0, activeMessage: 'pi-message-0' });
      normalizedStates.set(id, { pending: new Map() });
    }
    turn.events.push(event);
    switch (event.type) {
      case 'message':
        if (data.role === 'user' && data.source === 'goal_context') {
          const activity = addActivity(turn, event, `goal-context-${event.seq}`, 'event');
          activity.title = 'Контекст для цели'; activity.status = 'completed'; activity.endedAt = event.time;
          activity.input = { ...data }; activity.data = { ...data };
        }
        else if (data.role === 'user') turn.users.push(message(event, data));
        else if (data.role === 'assistant') turn.responses.push(message(event, data));
        else if (string(data.content)) turn.notices.push(string(data.content));
        break;
      case 'native':
        turn.backend ||= string(data.backend);
        if (data.backend === 'pi') piNative(turn, event, data, piStates.get(id)!);
        else scopedCodexNative(turn, event, data, roots.get(id));
        break;
      case 'agent': normalizedAgent(turn, event, data, normalizedStates.get(id)!); break;
      case 'turn':
        turn.status = normalizedStatus(data.status) || turn.status;
        turn.backend = string(data.backend) || turn.backend; turn.model = string(data.model) || turn.model;
        turn.effort = string(data.effort) || turn.effort;
        if (typeof data.serviceTier === 'string') turn.serviceTier = data.serviceTier;
        turn.outputTokens = finiteNumber(data.outputTokens) ?? turn.outputTokens;
        turn.generationMs = finiteNumber(data.generationMs) ?? turn.generationMs;
        turn.tokensPerSecond = finiteNumber(data.tokensPerSecond) ?? turn.tokensPerSecond;
        turn.error = string(data.error) || turn.error;
        break;
      case 'goal': {
        if (data.method === 'get') break;
        const result = record(data.result);
        const value = 'goal' in data ? data.goal : 'goal' in result ? result.goal : data;
        const activity = addActivity(turn, event, 'codex-goal', 'goal');
        activity.title = value === null || data.method === 'clear' ? 'Цель снята' : 'Цель'; activity.data = record(value);
        activity.text = string(activity.data.objective); activity.status = normalizedStatus(activity.data.status) || 'active';
        if (value === null || data.method === 'clear') activity.status = 'completed';
        break;
      }
      case 'goal_action': {
        const activity = addActivity(turn, event, `goal-action-${event.seq}`, 'event');
        activity.title = 'Управление целью'; activity.status = 'completed'; activity.data = data;
        break;
      }
      case 'compact_action': {
        const activity = addActivity(turn, event, `compact-action-${string(data.requestId) || event.seq}`, 'event');
        activity.title = 'Сжатие контекста';
        activity.status = data.status === 'started' ? 'starting' : normalizedStatus(data.status) || 'starting';
        activity.data = { ...activity.data, ...data }; activity.text = string(data.error);
        turn.backend = string(data.backend) || turn.backend;
        turn.status = activity.status;
        if (isFailed(activity.status)) turn.error = string(data.error) || 'Не удалось сжать контекст';
        if (terminal(activity.status)) activity.endedAt = event.time;
        break;
      }
      case 'handoff': {
        const activity = addActivity(turn, event, `handoff-${event.seq}`, 'event');
        activity.title = 'Контекст передан'; activity.status = 'completed'; activity.endedAt = event.time;
        const from = string(data.from), to = string(data.to);
        activity.text = from && to ? `${from} → ${to}` : '';
        // History belongs in the expanded receipt, never the visible summary.
        // Keep all provider/thread metadata available in its raw details too.
        activity.output = string(data.content) || string(data.message);
        activity.data = { ...data };
        break;
      }
      case 'system':
      case 'permission': {
        const content = string(data.content);
        if (content && content !== 'Session connected.' && content !== 'Session history saved.') turn.notices.push(content);
        const requestId = string(data.requestId);
        const request = turn.requests.find(v => v.id === requestId);
        if (request && (data.behavior || data.status === 'resolved')) {
          request.resolved = true; request.behavior = string(data.behavior);
        }
        break;
      }
    }
  }
  turns.forEach(turn => {
    for (const request of turn.requests) {
      if (resolvedRequests.has(request.id)) {
        request.resolved = true; request.behavior = resolvedRequests.get(request.id);
      }
    }
    finalize(turn);
  });
  return turns;
}
