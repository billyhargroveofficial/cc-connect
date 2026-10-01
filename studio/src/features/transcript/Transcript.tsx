import { useEffect, useId, useMemo, useState } from 'react';
import type { FormEvent, ReactNode } from 'react';
import {
  ArrowUpRight, Check, CheckCheck, ChevronDown, Code2, Copy, Download,
  File, FilePenLine, Globe2, LoaderCircle, MessageSquare, Paperclip, Search,
  ShieldCheck, Sparkles, Target, Terminal, Users, X, CircleAlert,
} from 'lucide-react';
import ReactMarkdown from 'react-markdown';
import remarkGfm from 'remark-gfm';
import remarkMath from 'remark-math';
import rehypeHighlight from 'rehype-highlight';
import rehypeKatex from 'rehype-katex';
import type { Bot, Event as JournalEvent } from '../../lib/types';
import Avatar from '../../components/Avatar';
import { botMessagePresentation } from '../../lib/events';
import {
  buildTranscript, field, isFailed, isRunning, json, record, string,
} from './reducer';
import type {
  Activity, Question, RecordValue, TranscriptAttachment, TranscriptMessage, TranscriptTurn, UserRequest,
} from './reducer';
import './transcript.css';

export type PermissionHandler = (
  requestId: string, behavior: string, updatedInput?: Record<string, unknown>, message?: string,
) => Promise<void>;

export interface TranscriptProps {
  bot: Bot;
  events: JournalEvent[];
  onPermission: PermissionHandler;
  onQuestion?: PermissionHandler;
  onRetry?: (turnId: string) => Promise<void> | void;
}

const statusLabels: Record<string, string> = {
  running: 'Работает', starting: 'Запускается', queued: 'В очереди', waiting: 'Ждёт ответ',
  waiting_permission: 'Ждёт разрешение', inProgress: 'Выполняется', in_progress: 'Выполняется',
  completed: 'Готово', complete: 'Готово', failed: 'Ошибка', error: 'Ошибка',
  stopped: 'Остановлено', cancelled: 'Отменено', canceled: 'Отменено', interrupted: 'Прервано',
  active: 'В работе', paused: 'На паузе', blocked: 'Нужна помощь',
  usageLimited: 'Лимит использования', budgetLimited: 'Бюджет исчерпан',
  retrying: 'Повторяет запрос', started: 'Запущен', interacted: 'Получил сообщение',
};

function statusLabel(status: string) { return statusLabels[status] || status; }

function dateTime(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '';
  return new Intl.DateTimeFormat('ru', { hour: '2-digit', minute: '2-digit' }).format(date);
}

function duration(value: unknown) {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? `${(value / 1000).toFixed(1)} с` : '';
}

function safeUrl(value: string, allowLocal = false): string | undefined {
  if (!value) return undefined;
  if (allowLocal && value.startsWith('/') && !value.startsWith('//')) return value;
  try {
    const url = new URL(value);
    if (url.protocol === 'http:' || url.protocol === 'https:') return url.href;
  } catch { /* A tool may supply a filesystem path; keep it as text. */ }
  return undefined;
}

function CopyButton({ content, label = 'Копировать' }: { content: string; label?: string }) {
  const [copied, setCopied] = useState(false);
  const [error, setError] = useState('');
  useEffect(() => {
    if (!copied) return;
    const timer = window.setTimeout(() => setCopied(false), 1800);
    return () => window.clearTimeout(timer);
  }, [copied]);
  async function copy() {
    try {
      // Clipboard API needs a secure context. A local HTTP preview still has a
      // user-initiated fallback on browsers that permit execCommand('copy').
      if (navigator.clipboard && window.isSecureContext) await navigator.clipboard.writeText(content);
      else {
        const area = document.createElement('textarea');
        area.value = content; area.style.position = 'fixed'; area.style.opacity = '0';
        document.body.append(area); area.select();
        let success = false;
        try { success = document.execCommand('copy'); } finally { area.remove(); }
        if (!success) throw new Error('Браузер не разрешил копирование');
      }
      setCopied(true); setError('');
    } catch { setError('Не удалось скопировать. Выделите текст вручную.'); }
  }
  return <button type="button" className="transcript-icon-button" onClick={() => void copy()}
    aria-label={copied ? 'Скопировано' : label} title={error || (copied ? 'Скопировано' : label)}>
    {copied ? <Check size={14} /> : <Copy size={14} />}
  </button>;
}

function codeText(children: ReactNode): string {
  if (typeof children === 'string' || typeof children === 'number') return String(children);
  if (Array.isArray(children)) return children.map(codeText).join('');
  if (children && typeof children === 'object' && 'props' in children) {
    return codeText((children.props as { children?: ReactNode }).children);
  }
  return '';
}

export function Markdown({ content }: { content: string }) {
  return <div className="transcript-markdown"><ReactMarkdown
    remarkPlugins={[remarkGfm, remarkMath]} rehypePlugins={[rehypeHighlight, [rehypeKatex, { strict: false }]]}
    components={{
      a: ({ href, children }) => <a href={href} target="_blank" rel="noreferrer noopener">{children}<ArrowUpRight size={11} className="transcript-link-arrow" /></a>,
      pre: ({ children }) => <div className="transcript-code-block"><CopyButton content={codeText(children)} label="Копировать код" /><pre>{children}</pre></div>,
      table: ({ children }) => <div className="transcript-table-scroll"><table>{children}</table></div>,
      img: ({ src, alt }) => <a href={src} target="_blank" rel="noreferrer noopener"><img src={src} alt={alt || 'Иллюстрация'} loading="lazy" /></a>,
    }}
  >{content}</ReactMarkdown></div>;
}

function attachmentUrl(botId: string, attachment: TranscriptAttachment) {
  return attachment.id
    ? `/api/studio/bots/${encodeURIComponent(botId)}/files/${encodeURIComponent(attachment.id)}`
    : safeUrl(attachment.url || '', true);
}

function Attachments({ botId, attachments }: { botId: string; attachments: TranscriptAttachment[] }) {
  if (!attachments.length) return null;
  return <div className="transcript-attachments">{attachments.map((attachment, index) => {
    const url = attachmentUrl(botId, attachment);
    return <div className="transcript-attachment" key={attachment.id || `${attachment.name}-${index}`}>
      {url && attachment.mimeType.startsWith('image/') && <a href={url} target="_blank" rel="noreferrer" className="transcript-image-link">
        <img src={url} alt={attachment.name} loading="lazy" />
      </a>}
      {url && attachment.mimeType.startsWith('audio/') && <audio src={url} controls preload="metadata" />}
      {url && attachment.mimeType.startsWith('video/') && <video src={url} controls preload="metadata" />}
      {url ? <a href={url} download={attachment.name} className="transcript-file-link" aria-label={`Скачать ${attachment.name}`}>
        <Paperclip size={14} /><span>{attachment.name}</span><Download size={14} />
      </a> : <span className="transcript-file-link"><File size={14} />{attachment.name}</span>}
    </div>;
  })}</div>;
}

function Message({ message, botId }: { message: TranscriptMessage; botId: string }) {
  const user = message.role === 'user';
  const delegated = user ? botMessagePresentation(message.content, message.source || '') : null;
  const content = delegated?.content ?? message.content;
  return <article className={user ? 'transcript-user-message' : `transcript-assistant-message${message.artifact ? ' transcript-artifact-message' : ''}`}>
    <div className={user ? 'transcript-user-bubble' : 'transcript-message-body'}>
      {content && <Markdown content={content} />}
      <Attachments botId={botId} attachments={message.attachments} />
    </div>
    {user ? <div className={`transcript-message-meta${!delegated && message.source !== 'telegram' ? ' is-time-only' : ''}`}>
      {delegated && <span>От бота {delegated.sender}</span>}{message.source === 'telegram' && <span>Telegram</span>}
      <time className="transcript-message-time" dateTime={message.time}>{dateTime(message.time)}</time>
    </div>
      : message.content && <div className="transcript-message-actions"><CopyButton content={message.content} label="Копировать ответ" /></div>}
  </article>;
}

function Status({ value }: { value: string }) {
  return <span className={`transcript-status${isRunning(value) ? ' is-running' : ''}${isFailed(value) ? ' is-failed' : ''}`}>
    {isRunning(value) ? <LoaderCircle size={12} className="transcript-spin" />
      : isFailed(value) ? <CircleAlert size={12} /> : value === 'completed' || value === 'complete' ? <Check size={12} /> : null}
    {statusLabel(value)}
  </span>;
}

function activityIcon(activity: Activity) {
  if (activity.kind === 'thinking') return <Sparkles size={15} />;
  if (activity.kind === 'commentary') return <MessageSquare size={15} />;
  if (activity.kind === 'search') return <Globe2 size={15} />;
  if (activity.kind === 'subagent') return <Users size={15} />;
  if (activity.kind === 'plan') return <CheckCheck size={15} />;
  if (activity.kind === 'goal') return <Target size={15} />;
  if (activity.data.type === 'fileChange') return <FilePenLine size={15} />;
  if (activity.data.type === 'commandExecution' || activity.title.toLowerCase() === 'bash') return <Terminal size={15} />;
  return <Code2 size={15} />;
}

function Payload({ label, value }: { label: string; value: unknown }) {
  const content = json(value);
  if (!content) return null;
  return <section className="transcript-payload"><div className="transcript-payload-label"><span>{label}</span><CopyButton content={content} /></div>
    <pre>{content}</pre>
  </section>;
}

function RawDetails({ value, label = 'Данные действия' }: { value: unknown; label?: string }) {
  const [open, setOpen] = useState(false);
  return <details className="transcript-raw-details" onToggle={event => setOpen(event.currentTarget.open)}><summary><Code2 size={12} />{label}<ChevronDown size={12} /></summary>
    {open && <Payload label="JSON" value={value} />}
  </details>;
}

function SearchResults({ value }: { value: unknown }) {
  const sources: { url: string; title: string; snippet: string }[] = [];
  const seen = new Set<string>();
  function visit(value: unknown, depth: number) {
    if (depth > 6 || sources.length >= 30) return;
    if (Array.isArray(value)) { value.forEach(item => visit(item, depth + 1)); return; }
    const v = record(value);
    const url = safeUrl(string(v.url) || string(v.link));
    if (url && !seen.has(url)) {
      seen.add(url); sources.push({ url, title: string(v.title) || new URL(url).hostname,
        snippet: string(v.snippet) || string(v.description) });
    }
    Object.values(v).forEach(child => {
      if (child !== null && typeof child === 'object') visit(child, depth + 1);
    });
  }
  visit(value, 0);
  if (!sources.length) return null;
  return <ul className="transcript-search-results">{sources.map(source => <li key={source.url}>
    <a href={source.url} target="_blank" rel="noreferrer noopener"><Globe2 size={13} /><span>{source.title}</span><ArrowUpRight size={13} /></a>
    <small>{new URL(source.url).hostname}</small>{source.snippet && <p>{source.snippet}</p>}
  </li>)}</ul>;
}

function Plan({ activity }: { activity: Activity }) {
  const steps = Array.isArray(activity.data.plan) ? activity.data.plan : [];
  return <>{activity.text && <Markdown content={activity.text} />}
    {steps.length > 0 && <ul className="transcript-plan">{steps.map((value, index) => {
      const step = record(value);
      return <li key={index} className={step.status === 'completed' ? 'is-done' : ''}>
        {step.status === 'completed' ? <Check size={14} /> : step.status === 'inProgress' ? <LoaderCircle size={14} className="transcript-spin" /> : <span className="transcript-step-dot" />}
        {string(step.step) || string(step.description) || json(value)}
      </li>;
    })}</ul>}
  </>;
}

function Goal({ activity }: { activity: Activity }) {
  const used = typeof activity.data.tokensUsed === 'number' ? activity.data.tokensUsed : undefined;
  const budget = typeof activity.data.tokenBudget === 'number' && activity.data.tokenBudget > 0 ? activity.data.tokenBudget : undefined;
  return <div className="transcript-goal">
    {activity.text && <Markdown content={activity.text} />}
    {(used !== undefined || budget !== undefined) && <div className="transcript-goal-budget">
      <span>{used === undefined ? '—' : used.toLocaleString('ru')}{budget ? ` / ${budget.toLocaleString('ru')}` : ''} токенов</span>
      {typeof activity.data.timeUsedSeconds === 'number' && <span>{Math.round(activity.data.timeUsedSeconds / 60)} мин</span>}
    </div>}
    {used !== undefined && budget && <progress value={Math.min(used, budget)} max={budget} aria-label="Использовано токенов из бюджета цели" />}
  </div>;
}

const collabLabels: Record<string, string> = {
  spawnAgent: 'Запускает сабагента', spawn_agent: 'Запускает сабагента',
  sendInput: 'Сообщение сабагенту', send_message: 'Сообщение сабагенту',
  wait: 'Ожидает сабагентов', closeAgent: 'Завершает сабагента', resumeAgent: 'Продолжает работу сабагента',
};

function pendingRequests(turn: TranscriptTurn): UserRequest[] {
  const requests = new Map<string, UserRequest>();
  const visited = new Set<TranscriptTurn>();
  function visit(thread: TranscriptTurn) {
    if (visited.has(thread)) return;
    visited.add(thread);
    thread.requests.forEach(request => { if (!request.resolved) requests.set(request.id, request); });
    thread.activities.forEach(activity => { if (activity.thread) visit(activity.thread); });
  }
  visit(turn);
  return [...requests.values()];
}

// Keep only an opened disclosure's content mounted for its short exit
// animation. Collapsed tool payloads stay lazy even in long conversations.
function useDisclosureContent(open: boolean) {
  const [retained, setRetained] = useState(open);
  useEffect(() => {
    if (open) { setRetained(true); return; }
    if (!retained) return;
    const timer = window.setTimeout(() => setRetained(false), 200);
    return () => window.clearTimeout(timer);
  }, [open, retained]);
  return open || retained;
}

function Subagents({ activity, onPermission, onQuestion }: { activity: Activity; onPermission: PermissionHandler; onQuestion?: PermissionHandler }) {
  const states = record(activity.data.agentsStates);
  const targets = Array.isArray(activity.data.receiverThreadIds) ? activity.data.receiverThreadIds.map(string) : [];
  const ids = [...new Set([...targets, ...Object.keys(states)])].filter(Boolean);
  const child = activity.thread;
  return <div className="transcript-subagents">
    {Boolean(activity.data.model || activity.data.reasoningEffort) && <div className="transcript-subagent-model">
      {[string(activity.data.model), string(activity.data.reasoningEffort)].filter(Boolean).join(' · ')}
    </div>}
    {activity.text && <Markdown content={activity.text} />}
    {ids.map(id => {
      const state = record(states[id]);
      return <div key={id} className="transcript-subagent"><div className="transcript-subagent-header">
        <span title={id}><Users size={13} />{string(state.name) || string(state.agentNickname) || 'Сабагент'}</span>{string(state.status) && <Status value={string(state.status)} />}
      </div>{string(state.message) && <Markdown content={string(state.message)} />}</div>;
    })}
    {child && <div className="transcript-subagent-thread" data-thread-id={child.id}>
      <div className="transcript-subagent-header"><span title={child.id}><Users size={13} />{string(record(activity.data.thread).name) || string(record(activity.data.thread).agentNickname) || 'Диалог сабагента'}</span><Status value={child.status} /></div>
      {child.activities.map(item => <ActivityItem key={item.id} activity={item} onPermission={onPermission} onQuestion={onQuestion} />)}
      {child.responses.map(response => <section className="transcript-subagent-response" key={response.id}>
        <Markdown content={response.content} /><CopyButton content={response.content} label="Копировать ответ сабагента" />
      </section>)}
      {child.requests.filter(request => request.resolved).map(request => <RequestCard key={request.id} request={request} onPermission={onPermission} onQuestion={onQuestion} />)}
      {child.notices.map((notice, index) => <p className="transcript-muted" key={index}>{notice}</p>)}
      {child.error && <p className="transcript-error">{child.error}</p>}
      <RawJournal events={child.events} title="События этой сессии" />
    </div>}
  </div>;
}

function ActivityItem({ activity, onPermission, onQuestion }: { activity: Activity; onPermission: PermissionHandler; onQuestion?: PermissionHandler }) {
  const contentId = useId();
  const running = isRunning(activity.status);
  const [override, setOverride] = useState<{ running: boolean; open: boolean } | null>(null);
  const open = override?.running === running ? override.open : false;
  const renderContent = useDisclosureContent(open);
  const simpleText = activity.kind === 'thinking' || activity.kind === 'commentary';
  const title = activity.kind === 'subagent' ? collabLabels[activity.title] || activity.title : activity.title;
  const input = json(activity.input);
  const preview = activity.text || input;
  return <div className={`transcript-activity-item transcript-activity-${activity.kind}${open ? ' is-open' : ''}`}>
    <button type="button" className="transcript-activity-summary" aria-expanded={open} aria-controls={contentId}
      onClick={() => setOverride({ running, open: !open })}>
      <span className="transcript-activity-icon">{activityIcon(activity)}</span><span className="transcript-activity-title">{title}
      {!simpleText && preview && <small>{preview.split('\n')[0]}</small>}
    </span><span className="transcript-activity-summary-meta"><Status value={activity.status} /><ChevronDown size={13} aria-hidden="true" /></span></button>
    <div className={`transcript-disclosure${open ? ' is-open' : ''}`} id={contentId} aria-hidden={!open} inert={!open}>
      <div className="transcript-disclosure-inner">{renderContent && <div className="transcript-activity-content">
      {simpleText && activity.text && <Markdown content={activity.text} />}
      {activity.kind === 'thinking' && !activity.text && <p className="transcript-muted">Ожидаем доступное от модели содержание.</p>}
      {activity.kind === 'plan' && <Plan activity={activity} />}
      {activity.kind === 'goal' && <Goal activity={activity} />}
      {activity.kind === 'subagent' && <Subagents activity={activity} onPermission={onPermission} onQuestion={onQuestion} />}
      {activity.kind === 'search' && <SearchResults value={activity.output} />}
      {!simpleText && activity.kind !== 'subagent' && <Payload label="Вход" value={activity.input} />}
      {!simpleText && activity.kind !== 'subagent' && <Payload label="Результат" value={activity.output} />}
      {activity.kind === 'event' && activity.text && <Markdown content={activity.text} />}
      {Boolean(activity.data.cwd || activity.data.exitCode !== undefined || activity.data.durationMs) && <div className="transcript-tool-meta">
        {string(activity.data.cwd) && <span>{string(activity.data.cwd)}</span>}
        {typeof activity.data.exitCode === 'number' && <span>Код выхода: {activity.data.exitCode}</span>}
        {duration(activity.data.durationMs) && <span>{duration(activity.data.durationMs)}</span>}
      </div>}
      <RawDetails value={activity.data} />
      </div>}</div>
    </div>
  </div>;
}

function currentActivityLabel(turn: TranscriptTurn) {
  if (pendingRequests(turn).length || turn.status === 'waiting_permission') return 'Ждёт вашего ответа';
  if (turn.status === 'queued') return 'В очереди';
  if (turn.status === 'retrying') return 'Повторяет запрос';
  const activity = turn.activities.slice().reverse().find(item => isRunning(item.status)
    || Boolean(item.thread && isRunning(item.thread.status)));
  if (!activity) return turn.responses.some(message => !message.artifact && message.content)
    ? 'Пишет ответ' : turn.status === 'starting' ? 'Начинает работу' : 'Работает';
  if (activity.kind === 'thinking') return 'Размышляет';
  if (activity.kind === 'search') return 'Ищет в интернете';
  if (activity.kind === 'plan') return 'Составляет план';
  if (activity.kind === 'goal') return 'Работает над целью';
  if (activity.kind === 'subagent') return activity.title === 'wait' ? 'Ждёт сабагентов' : 'Работают сабагенты';
  if (activity.kind === 'commentary') return activity.text.trim().split('\n').find(Boolean)?.replace(/^[#>*\s]+/, '').slice(0, 120) || 'Обновляет ход работы';
  if (activity.data.type === 'contextCompaction' || /compac|сжат|сжим/i.test(activity.title)) return 'Сжимает контекст';
  if (activity.data.type === 'fileChange') return 'Редактирует файлы';
  if (activity.data.type === 'commandExecution' || /^(bash|exec_command|shell|terminal)$/i.test(activity.title)) return 'Выполняет команду';
  if (/^(read|read_file|readfile)$/i.test(activity.title)) return 'Читает файл';
  if (/^(write|edit|apply_patch|write_file)$/i.test(activity.title)) return 'Редактирует файлы';
  return activity.title.replace(/_/g, ' ') || 'Работает';
}

function TurnActivity({ turn, onPermission, onQuestion }: { turn: TranscriptTurn; onPermission: PermissionHandler; onQuestion?: PermissionHandler }) {
  const contentId = useId();
  const [override, setOverride] = useState<{ running: boolean; open: boolean } | null>(null);
  const running = isRunning(turn.status);
  const open = override?.running === running ? override.open : false;
  const renderContent = useDisclosureContent(open);
  const resolvedRequests = turn.requests.filter(request => request.resolved);
  const hasStats = Boolean(turn.model || turn.serviceTier || turn.outputTokens !== undefined || turn.tokensPerSecond !== undefined);
  if (!turn.activities.length && !running && !resolvedRequests.length && !hasStats) return null;
  const serviceTitle = !turn.users.length && !turn.responses.length && turn.activities.length === 1
    && turn.activities[0].kind === 'event' ? turn.activities[0].title : '';
  const label = running ? currentActivityLabel(turn) : serviceTitle || (isFailed(turn.status) ? 'Ошибка выполнения'
    : ['stopped', 'interrupted', 'cancelled', 'canceled'].includes(turn.status) ? statusLabel(turn.status)
      : turn.activities.length ? 'Ход работы' : 'Детали ответа');
  return <section className={`transcript-turn-activity${running ? ' is-running' : ''}`}>
    <button className="transcript-activity-toggle" type="button" aria-expanded={open} aria-controls={contentId}
      onClick={() => setOverride({ running, open: !open })}>
      {running ? <LoaderCircle size={14} className="transcript-spin" aria-hidden="true" />
        : isFailed(turn.status) ? <CircleAlert size={14} aria-hidden="true" /> : <CheckCheck size={14} aria-hidden="true" />}
      <span className="transcript-current-activity" role={running ? 'status' : undefined} aria-live={running ? 'polite' : undefined} title={label}>{label}</span>
      {turn.activities.length > 0 && <small className="transcript-activity-count" aria-label={`Количество действий: ${turn.activities.length}`}>{turn.activities.length}</small>}
      <ChevronDown size={13} className={open ? 'is-open' : ''} aria-hidden="true" />
    </button>
    <div className={`transcript-disclosure${open ? ' is-open' : ''}`} id={contentId} aria-hidden={!open} inert={!open}>
      <div className="transcript-disclosure-inner">{renderContent && <div className="transcript-activity-list">
      {turn.activities.map(activity => <ActivityItem key={activity.id} activity={activity} onPermission={onPermission} onQuestion={onQuestion} />)}
      {resolvedRequests.map(request => <RequestCard key={request.id} request={request} onPermission={onPermission} onQuestion={onQuestion} />)}
      {!running && hasStats && <div className="transcript-turn-stats">
        <span>{[turn.backend === 'pi' ? 'Pi' : turn.backend === 'codex' ? 'Codex' : turn.backend, turn.model, turn.effort,
          ['priority', 'fast'].includes(turn.serviceTier) ? 'Fast' : turn.serviceTier].filter(Boolean).join(' · ')}</span>
        <span title="Оценка по времени генерации. Время выполнения инструментов исключено, где runtime сообщил тайминги.">{turn.tokensPerSecond !== undefined && turn.tokensPerSecond > 0 ? `≈ ${turn.tokensPerSecond.toFixed(1)}` : '—'} ток/с</span>
        {turn.outputTokens !== undefined && turn.outputTokens > 0 && <span>{turn.outputTokens.toLocaleString('ru')} токенов</span>}
        {duration(turn.generationMs) && <span>{duration(turn.generationMs)} генерации</span>}
      </div>}
      <RawJournal events={turn.events} />
      </div>}</div>
    </div>
  </section>;
}

function QuestionField({ question, value, onChange }: {
  question: Question; value: string[]; onChange: (answer: string[]) => void;
}) {
  const selected = value.filter(v => question.options.some(option => option.label === v));
  const other = value.find(v => !question.options.some(option => option.label === v)) || '';
  return <fieldset className="transcript-question"><legend>{question.header && <small>{question.header}</small>}{question.question}</legend>
    {question.options.map(option => <label className={`transcript-question-option${selected.includes(option.label) ? ' is-selected' : ''}`} key={option.label}>
      <input type={question.multiSelect ? 'checkbox' : 'radio'} name={question.id} value={option.label} checked={selected.includes(option.label)}
        onChange={() => onChange(question.multiSelect
          ? selected.includes(option.label) ? value.filter(v => v !== option.label) : [...value, option.label]
          : [option.label])} />
      <span><strong>{option.label}</strong>{option.description && <small>{option.description}</small>}</span>
    </label>)}
    {(question.allowOther || !question.options.length) && <label className="transcript-free-answer">
      <span>{question.options.length ? 'Свой вариант' : 'Ваш ответ'}</span>
      <input type={question.isSecret ? 'password' : 'text'} autoComplete="off" value={other}
        onChange={event => onChange(question.multiSelect ? [...selected, event.target.value] : [event.target.value])}
        placeholder={question.options.length ? 'Или напишите ответ…' : 'Напишите ответ…'} />
    </label>}
  </fieldset>;
}

function RequestCard({ request, onPermission, onQuestion }: {
  request: UserRequest; onPermission: PermissionHandler; onQuestion?: PermissionHandler;
}) {
  const [answers, setAnswers] = useState<Record<string, string[]>>({});
  const [input, setInput] = useState('');
  const [busy, setBusy] = useState(false);
  const [answered, setAnswered] = useState(false);
  const [error, setError] = useState('');
  const resolved = request.resolved || answered;
  async function respond(behavior: string) {
    setBusy(true); setError('');
    try {
      const questionAnswers: RecordValue = {};
      for (const q of request.questions) {
        const values = (answers[q.id] || []).filter(v => v.trim());
        // Adapters key answers by the question text, then map to native ids.
        questionAnswers[q.question] = q.multiSelect ? values : values[0] || '';
      }
      const updatedInput = request.questions.length ? { answers: questionAnswers } : undefined;
      const handler = request.questions.length && onQuestion ? onQuestion : onPermission;
      await handler(request.id, behavior, updatedInput, request.method === 'input' ? input : undefined);
      setAnswered(true); setAnswers({}); setInput('');
    } catch (err) { setError(err instanceof Error ? err.message : 'Не удалось отправить ответ'); }
    finally { setBusy(false); }
  }
  const canSubmit = request.questions.length
    ? request.questions.every(q => (answers[q.id] || []).some(v => v.trim()))
    : request.method !== 'input' || Boolean(input.trim());
  function submit(event: FormEvent) { event.preventDefault(); void respond('allow'); }
  return <section className={`transcript-request${resolved ? ' is-resolved' : ''}`} aria-label={request.title}>
    <div className="transcript-request-header"><ShieldCheck size={17} /><strong>{request.title}</strong>
      {resolved && <span>{request.behavior === 'deny' ? 'Отклонено' : 'Закрыто'}</span>}
    </div>
    {resolved ? <p className="transcript-muted">{request.questions.length ? request.questions.map(q => q.question).join(' · ') : 'Запрос больше не ожидает решения.'}</p>
      : <form onSubmit={submit}>
        {request.questions.length ? request.questions.map(q => <QuestionField question={q} key={q.id} value={answers[q.id] || []}
          onChange={value => setAnswers(v => ({ ...v, [q.id]: value }))} />)
          : <>{Boolean(request.input) && <Payload label="Действие" value={request.input} />}
            {request.method === 'input' && <input className="transcript-request-input" value={input} onChange={event => setInput(event.target.value)}
              placeholder={request.placeholder || 'Ваш ответ'} autoComplete="off" aria-label={request.title} />}</>}
        {error && <p className="transcript-error" role="alert">{error}</p>}
        <div className="transcript-request-actions">
          <button className="transcript-button is-primary" type="submit" disabled={busy || !canSubmit}>
            {busy ? <LoaderCircle size={14} className="transcript-spin" /> : <Check size={14} />}
            {request.questions.length || request.method === 'input' ? 'Отправить ответ' : 'Разрешить'}
          </button>
          <button className="transcript-button" type="button" disabled={busy} onClick={() => void respond('deny')}><X size={14} />{request.questions.length ? 'Пропустить' : 'Отклонить'}</button>
        </div>
      </form>}
  </section>;
}

function JournalItem({ event }: { event: JournalEvent }) {
  const [open, setOpen] = useState(false);
  const data = record(event.data);
  return <details className="transcript-journal-event" onToggle={e => setOpen(e.currentTarget.open)}><summary>
    <code>#{event.seq}</code><span>{event.type === 'native' ? string(data.method) : string(field(data, 'type')) || event.type}</span>
    <time dateTime={event.time}>{dateTime(event.time)}</time><ChevronDown size={12} />
  </summary>{open && <Payload label="Событие" value={event} />}</details>;
}

function RawJournal({ events, title }: { events: JournalEvent[]; title?: string }) {
  const [query, setQuery] = useState('');
  const [open, setOpen] = useState(false);
  const visible = query ? events.filter(event => {
    const data = record(event.data);
    return `${event.seq} ${event.type} ${string(data.method)} ${string(field(data, 'type'))}`.toLowerCase().includes(query.toLowerCase());
  }) : events;
  if (!events.length) return null;
  return <details className="transcript-journal" onToggle={event => setOpen(event.currentTarget.open)}><summary><Code2 size={12} /><span>{title || 'Журнал хода'}</span>
    <small>{events.length}</small><ChevronDown size={12} /></summary>
    {open && <div className="transcript-journal-body"><div className="transcript-journal-toolbar">
      <label><Search size={13} /><input value={query} onChange={event => setQuery(event.target.value)} placeholder="Тип события или метод…" aria-label="Найти событие в журнале" /></label>
      <CopyButton content={events.map(event => JSON.stringify(event)).join('\n')} label="Копировать журнал JSONL" />
    </div><div className="transcript-journal-events">{visible.map(event => <JournalItem key={event.seq} event={event} />)}
      {!visible.length && <p className="transcript-muted">Таких событий нет.</p>}</div></div>}
  </details>;
}

function Turn({ bot, turn, onPermission, onQuestion, onRetry }: TranscriptProps & { turn: TranscriptTurn }) {
  const running = isRunning(turn.status);
  const service = !turn.users.length && !turn.responses.length && !turn.requests.length && !running && !turn.error;
  const hasBotContent = Boolean(turn.responses.length || turn.activities.length || turn.requests.length || running || turn.error);
  return <section className={`transcript-turn${service ? ' is-service' : ''}`} data-turn-id={turn.id}>
    {turn.users.map(message => <Message key={message.id} message={message} botId={bot.id} />)}
    {turn.notices.map((notice, index) => <div className="transcript-notice" key={index}><MessageSquare size={13} /><Markdown content={notice} /></div>)}
    {hasBotContent && <div className="transcript-bot-response">
      <div className="transcript-response-body">
        <TurnActivity turn={turn} onPermission={onPermission} onQuestion={onQuestion} />
        {pendingRequests(turn).map(request => <RequestCard key={request.id} request={request} onPermission={onPermission} onQuestion={onQuestion} />)}
        {turn.responses.map(message => <Message key={message.id} message={message} botId={bot.id} />)}
        {turn.error && <div className="transcript-turn-error" role="alert"><CircleAlert size={16} /><div><strong>Не удалось завершить работу</strong><p>{turn.error}</p>
          {onRetry && <button type="button" className="transcript-button" onClick={() => void onRetry(turn.id)}>Обновить состояние</button>}
        </div></div>}
      </div>
    </div>}
  </section>;
}

export function Transcript(props: TranscriptProps) {
  const { bot, events } = props;
  const turns = useMemo(() => buildTranscript(events, bot.id), [events, bot.id]);
  const visible = turns.filter(turn => turn.users.length || turn.responses.length || turn.activities.length || turn.requests.length || turn.notices.length || turn.error || isRunning(turn.status));
  return <div className="transcript" aria-label={`Диалог с ${bot.name}`}>
    {!visible.length && <div className="transcript-empty"><div className="transcript-empty-avatar"><Avatar bot={bot} size={72} /></div>
      <span className="transcript-empty-eyebrow">{bot.chief ? 'ВАШ КООРДИНАТОР' : 'ВАШ ПОСТОЯННЫЙ ПОМОЩНИК'}</span>
      <h2>{bot.name} на связи.</h2><p>{bot.role || 'Расскажите, чего хотите достичь. Бот сохранит контекст и продолжит работу здесь.'}</p>
    </div>}
    {visible.map(turn => <Turn key={turn.id} {...props} turn={turn} />)}
    {events.length > 0 && <div className="transcript-all-events"><RawJournal events={events.filter(event => event.botId === bot.id)} title="Все события бота" /></div>}
    <div className="transcript-end" />
  </div>;
}

export default Transcript;
