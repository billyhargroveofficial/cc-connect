import { useEffect, useId, useMemo, useState } from 'react';
import type { FormEvent, ReactNode } from 'react';
import {
  ArrowUpRight, Check, CheckCheck, ChevronDown, Code2, Copy, Download,
  File, FilePenLine, Globe2, LoaderCircle, MessageSquare, Search,
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
  AnimatePresence, controlMotion, fade, fadeUp, m, motionTransition,
  rowMotion, softControlMotion, useIsPresent, useReducedMotion,
} from '../../lib/motion';
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
  running: 'Working', starting: 'Starting', queued: 'Queued', waiting: 'Awaiting reply',
  waiting_permission: 'Awaiting approval', inProgress: 'In progress', in_progress: 'In progress',
  completed: 'Done', complete: 'Done', failed: 'Error', error: 'Error',
  stopped: 'Stopped', cancelled: 'Cancelled', canceled: 'Cancelled', interrupted: 'Interrupted',
  active: 'Active', paused: 'Paused', blocked: 'Needs help',
  usageLimited: 'Usage limit reached', budgetLimited: 'Budget exhausted',
  retrying: 'Retrying', started: 'Started', interacted: 'Message received',
};

function statusLabel(status: string) { return statusLabels[status] || status; }

function dateTime(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '';
  return new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit' }).format(date);
}

function duration(value: unknown) {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? `${(value / 1000).toFixed(1)} s` : '';
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

function CopyButton({ content, label = 'Copy' }: { content: string; label?: string }) {
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
        if (!success) throw new Error('The browser did not allow copying');
      }
      setCopied(true); setError('');
    } catch { setError('Unable to copy. Select the text manually.'); }
  }
  return <m.button {...controlMotion} type="button" className="transcript-icon-button" onClick={() => void copy()}
    aria-label={copied ? 'Copied' : label} title={error || (copied ? 'Copied' : label)}>
    <AnimatePresence initial={false} mode="wait"><m.span key={copied ? 'copied' : 'copy'} className="transcript-motion-icon"
      initial={{ opacity: 0, scale: .8 }} animate={{ opacity: 1, scale: 1 }} exit={{ opacity: 0, scale: .8 }} transition={motionTransition.quick}>
      {copied ? <Check size={14} /> : <Copy size={14} />}
    </m.span></AnimatePresence>
  </m.button>;
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
      pre: ({ children }) => <div className="transcript-code-block"><CopyButton content={codeText(children)} label="Copy code" /><pre>{children}</pre></div>,
      table: ({ children }) => <div className="transcript-table-scroll"><table>{children}</table></div>,
      img: ({ src, alt }) => <a href={src} target="_blank" rel="noreferrer noopener"><img src={src} alt={alt || 'Illustration'} loading="lazy" /></a>,
    }}
  >{content}</ReactMarkdown></div>;
}

function attachmentUrl(botId: string, attachment: TranscriptAttachment) {
  return attachment.id
    ? `/api/studio/bots/${encodeURIComponent(botId)}/files/${encodeURIComponent(attachment.id)}`
    : safeUrl(attachment.url || '', true);
}

function attachmentKind(mimeType: string) {
  if (mimeType.startsWith('image/')) return 'image';
  if (mimeType.startsWith('audio/')) return 'audio';
  if (mimeType.startsWith('video/')) return 'video';
  return 'file';
}

function attachmentFormat(attachment: TranscriptAttachment, kind: string) {
  const extension = attachment.name.match(/\.([^.]{1,10})$/)?.[1];
  const format = extension || attachment.mimeType.split('/')[1]?.split(/[;+]/)[0];
  const noun = kind === 'image' ? 'image' : kind === 'audio' ? 'audio' : kind === 'video' ? 'video' : 'file';
  return format ? `${format.toUpperCase()} ${noun}` : noun[0].toUpperCase() + noun.slice(1);
}

function Attachments({ botId, attachments }: { botId: string; attachments: TranscriptAttachment[] }) {
  if (!attachments.length) return null;
  return <div className="transcript-attachments"><AnimatePresence initial={false}>{attachments.map((attachment, index) => {
    const url = attachmentUrl(botId, attachment);
    const kind = attachmentKind(attachment.mimeType);
    const media = kind === 'audio' || kind === 'video';
    return <m.div variants={rowMotion} initial="hidden" animate="visible" exit="exit"
      className={`transcript-attachment is-${kind}`} key={attachment.id || `${attachment.name}-${index}`}>
      {kind === 'image' && url ? <m.a {...softControlMotion} href={url} target="_blank" rel="noreferrer noopener"
        className="transcript-attachment-preview" aria-label={`Open ${attachment.name}`}>
        <img src={url} alt={attachment.name} loading="lazy" />
      </m.a> : !media && <span className="transcript-attachment-preview is-file" aria-hidden="true"><File size={20} /></span>}
      {url && kind === 'audio' && <audio src={url} controls preload="metadata" />}
      {url && kind === 'video' && <video src={url} controls preload="metadata" />}
      <span className="transcript-attachment-copy">
        <span title={attachment.name}>{attachment.name}</span>
        <small>{attachmentFormat(attachment, kind)}</small>
      </span>
      {url && <m.a {...controlMotion} href={url} download={attachment.name} className="transcript-attachment-download"
        aria-label={`Download ${attachment.name}`} title={`Download ${attachment.name}`}><Download size={15} /></m.a>}
    </m.div>;
  })}</AnimatePresence></div>;
}

function Message({ message, botId }: { message: TranscriptMessage; botId: string }) {
  const user = message.role === 'user';
  const delegated = user ? botMessagePresentation(message.content, message.source || '') : null;
  const content = delegated?.content ?? message.content;
  return <m.article variants={user ? fadeUp : fade} initial="hidden" animate="visible" exit="exit"
    className={user ? 'transcript-user-message' : `transcript-assistant-message${message.artifact ? ' transcript-artifact-message' : ''}`}>
    <div className={user ? 'transcript-user-bubble' : 'transcript-message-body'}>
      {content && <Markdown content={content} />}
      <Attachments botId={botId} attachments={message.attachments} />
    </div>
    {user ? <div className={`transcript-message-meta${!delegated && message.source !== 'telegram' ? ' is-time-only' : ''}`}>
      {delegated && <span>From bot {delegated.sender}</span>}{message.source === 'telegram' && <span>Telegram</span>}
      <time className="transcript-message-time" dateTime={message.time}>{dateTime(message.time)}</time>
    </div>
      : message.content && <div className="transcript-message-actions"><CopyButton content={message.content} label="Copy response" /></div>}
  </m.article>;
}

function Status({ value }: { value: string }) {
  return <span className={`transcript-status${isRunning(value) ? ' is-running' : ''}${isFailed(value) ? ' is-failed' : ''}`}>
    <AnimatePresence initial={false} mode="wait"><m.span key={value} className="transcript-status-content"
      variants={fade} initial="hidden" animate="visible" exit="exit">
      {isRunning(value) ? <LoaderCircle size={12} className="transcript-spin" />
        : isFailed(value) ? <CircleAlert size={12} /> : value === 'completed' || value === 'complete' ? <Check size={12} /> : null}
      {statusLabel(value)}
    </m.span></AnimatePresence>
  </span>;
}

function Chevron({ open, size = 12 }: { open: boolean; size?: number }) {
  return <m.span className="transcript-chevron" animate={{ rotate: open ? 180 : 0 }} transition={motionTransition.quick} aria-hidden="true">
    <ChevronDown size={size} />
  </m.span>;
}

function useDisclosureMotion(open: boolean) {
  const reduced = useReducedMotion();
  return {
    initial: false as const,
    animate: { height: open ? 'auto' : 0, opacity: open ? 1 : 0 },
    transition: reduced ? { duration: 0 } : motionTransition.disclosure,
  };
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

function RawDetails({ value, label = 'Action data' }: { value: unknown; label?: string }) {
  const contentId = useId();
  const [open, setOpen] = useState(false);
  const renderContent = useDisclosureContent(open);
  const disclosureMotion = useDisclosureMotion(open);
  return <div className="transcript-raw-details">
    <m.button type="button" className="transcript-raw-toggle" aria-expanded={open} aria-controls={contentId}
      whileTap={{ opacity: .7 }} onClick={() => setOpen(value => !value)}><Code2 size={12} />{label}<Chevron open={open} /></m.button>
    <m.div {...disclosureMotion} className={`transcript-disclosure${open ? ' is-open' : ''}`} id={contentId} aria-hidden={!open} inert={!open}>
      {renderContent && <Payload label="JSON" value={value} />}
    </m.div>
  </div>;
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
  return <ul className="transcript-search-results"><AnimatePresence initial={false}>{sources.map(source => <m.li key={source.url}
    variants={rowMotion} initial="hidden" animate="visible" exit="exit">
    <m.a whileHover={{ x: 2 }} whileTap={{ opacity: .65 }} transition={motionTransition.quick}
      href={source.url} target="_blank" rel="noreferrer noopener"><Globe2 size={13} /><span>{source.title}</span><ArrowUpRight size={13} /></m.a>
    <small>{new URL(source.url).hostname}</small>{source.snippet && <p>{source.snippet}</p>}
  </m.li>)}</AnimatePresence></ul>;
}

function Plan({ activity }: { activity: Activity }) {
  const steps = Array.isArray(activity.data.plan) ? activity.data.plan : [];
  return <>{activity.text && <Markdown content={activity.text} />}
    {steps.length > 0 && <ul className="transcript-plan"><AnimatePresence initial={false}>{steps.map((value, index) => {
      const step = record(value);
      return <m.li key={index} variants={rowMotion} initial="hidden" animate="visible" exit="exit" className={step.status === 'completed' ? 'is-done' : ''}>
        <AnimatePresence initial={false} mode="wait"><m.span className="transcript-plan-icon" key={string(step.status)}
          initial={{ opacity: 0, scale: .8 }} animate={{ opacity: 1, scale: 1 }} exit={{ opacity: 0, scale: .8 }} transition={motionTransition.quick}>
          {step.status === 'completed' ? <Check size={14} /> : step.status === 'inProgress' ? <LoaderCircle size={14} className="transcript-spin" /> : <span className="transcript-step-dot" />}
        </m.span></AnimatePresence>
        {string(step.step) || string(step.description) || json(value)}
      </m.li>;
    })}</AnimatePresence></ul>}
  </>;
}

function Goal({ activity }: { activity: Activity }) {
  const reduced = useReducedMotion();
  const used = typeof activity.data.tokensUsed === 'number' ? activity.data.tokensUsed : undefined;
  const budget = typeof activity.data.tokenBudget === 'number' && activity.data.tokenBudget > 0 ? activity.data.tokenBudget : undefined;
  return <div className="transcript-goal">
    {activity.text && <Markdown content={activity.text} />}
    {(used !== undefined || budget !== undefined) && <div className="transcript-goal-budget">
      <span>{used === undefined ? '—' : used.toLocaleString('en')}{budget ? ` / ${budget.toLocaleString('en')}` : ''} tokens</span>
      {typeof activity.data.timeUsedSeconds === 'number' && <span>{Math.round(activity.data.timeUsedSeconds / 60)} min</span>}
    </div>}
    {used !== undefined && budget && <div className="transcript-goal-progress" role="progressbar" aria-valuemin={0} aria-valuemax={budget}
      aria-valuenow={Math.min(used, budget)} aria-label="Tokens used from the goal budget">
      <m.span initial={false} animate={{ scaleX: Math.min(used, budget) / budget }}
        transition={reduced ? { duration: 0 } : motionTransition.disclosure} />
    </div>}
  </div>;
}

const collabLabels: Record<string, string> = {
  spawnAgent: 'Starting subagent', spawn_agent: 'Starting subagent',
  sendInput: 'Message to subagent', send_message: 'Message to subagent',
  wait: 'Waiting for subagents', closeAgent: 'Stopping subagent', resumeAgent: 'Resuming subagent',
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
  const reduced = useReducedMotion();
  const [retained, setRetained] = useState(open);
  useEffect(() => {
    if (open) { setRetained(true); return; }
    if (!retained) return;
    const timer = window.setTimeout(() => setRetained(false), reduced ? 0 : 200);
    return () => window.clearTimeout(timer);
  }, [open, retained, reduced]);
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
    <AnimatePresence initial={false}>{ids.map(id => {
      const state = record(states[id]);
      return <m.div key={id} variants={rowMotion} initial="hidden" animate="visible" exit="exit" className="transcript-subagent"><div className="transcript-subagent-header">
        <span title={id}><Users size={13} />{string(state.name) || string(state.agentNickname) || 'Subagent'}</span>{string(state.status) && <Status value={string(state.status)} />}
      </div>{string(state.message) && <Markdown content={string(state.message)} />}</m.div>;
    })}</AnimatePresence>
    <AnimatePresence initial={false}>{child && <m.div key={child.id} variants={fade} initial="hidden" animate="visible" exit="exit"
      className="transcript-subagent-thread" data-thread-id={child.id}>
      <div className="transcript-subagent-header"><span title={child.id}><Users size={13} />{string(record(activity.data.thread).name) || string(record(activity.data.thread).agentNickname) || 'Subagent conversation'}</span><Status value={child.status} /></div>
      <AnimatePresence initial={false}>{child.activities.map(item => <ActivityItem key={item.id} activity={item} onPermission={onPermission} onQuestion={onQuestion} />)}</AnimatePresence>
      <AnimatePresence initial={false}>{child.responses.map(response => <m.section className="transcript-subagent-response" key={response.id}
        variants={fade} initial="hidden" animate="visible" exit="exit">
        <Markdown content={response.content} /><CopyButton content={response.content} label="Copy subagent response" />
      </m.section>)}</AnimatePresence>
      <AnimatePresence initial={false}>{child.requests.filter(request => request.resolved).map(request => <RequestCard key={request.id} request={request} onPermission={onPermission} onQuestion={onQuestion} />)}</AnimatePresence>
      {child.notices.map((notice, index) => <p className="transcript-muted" key={index}>{notice}</p>)}
      {child.error && <p className="transcript-error">{child.error}</p>}
      <RawJournal events={child.events} title="Session events" />
    </m.div>}</AnimatePresence>
  </div>;
}

function ActivityItem({ activity, onPermission, onQuestion }: { activity: Activity; onPermission: PermissionHandler; onQuestion?: PermissionHandler }) {
  const contentId = useId();
  const present = useIsPresent();
  const running = isRunning(activity.status);
  const [override, setOverride] = useState<{ running: boolean; open: boolean } | null>(null);
  const open = override?.running === running ? override.open : false;
  const renderContent = useDisclosureContent(open);
  const disclosureMotion = useDisclosureMotion(open);
  const simpleText = activity.kind === 'thinking' || activity.kind === 'commentary';
  const title = activity.kind === 'subagent' ? collabLabels[activity.title] || activity.title : activity.title;
  const input = json(activity.input);
  const preview = activity.text || input;
  return <m.div variants={rowMotion} initial="hidden" animate="visible" exit="exit"
    className={`transcript-activity-item transcript-activity-${activity.kind}${open ? ' is-open' : ''}`} aria-hidden={!present} inert={!present}>
    <m.button type="button" className="transcript-activity-summary" aria-expanded={open} aria-controls={contentId} whileTap={{ opacity: .7 }}
      onClick={() => setOverride({ running, open: !open })}>
      <span className="transcript-activity-icon">{activityIcon(activity)}</span><span className="transcript-activity-title">{title}
      {!simpleText && preview && <small>{preview.split('\n')[0]}</small>}
    </span><span className="transcript-activity-summary-meta"><Status value={activity.status} /><Chevron open={open} size={13} /></span></m.button>
    <m.div {...disclosureMotion} className={`transcript-disclosure${open ? ' is-open' : ''}`} id={contentId} aria-hidden={!open} inert={!open}>
      <div className="transcript-disclosure-inner">{renderContent && <div className="transcript-activity-content">
      {simpleText && activity.text && <Markdown content={activity.text} />}
      {activity.kind === 'thinking' && !activity.text && <p className="transcript-muted">Waiting for content from the model.</p>}
      {activity.kind === 'plan' && <Plan activity={activity} />}
      {activity.kind === 'goal' && <Goal activity={activity} />}
      {activity.kind === 'subagent' && <Subagents activity={activity} onPermission={onPermission} onQuestion={onQuestion} />}
      {activity.kind === 'search' && <SearchResults value={activity.output} />}
      {!simpleText && activity.kind !== 'subagent' && <Payload label="Input" value={activity.input} />}
      {!simpleText && activity.kind !== 'subagent' && <Payload label="Output" value={activity.output} />}
      {activity.kind === 'event' && activity.text && <Markdown content={activity.text} />}
      {Boolean(activity.data.cwd || activity.data.exitCode !== undefined || activity.data.durationMs) && <div className="transcript-tool-meta">
        {string(activity.data.cwd) && <span>{string(activity.data.cwd)}</span>}
        {typeof activity.data.exitCode === 'number' && <span>Exit code: {activity.data.exitCode}</span>}
        {duration(activity.data.durationMs) && <span>{duration(activity.data.durationMs)}</span>}
      </div>}
      <RawDetails value={activity.data} />
      </div>}</div>
    </m.div>
  </m.div>;
}

function currentActivityLabel(turn: TranscriptTurn) {
  if (pendingRequests(turn).length || turn.status === 'waiting_permission') return 'Awaiting your reply';
  if (turn.status === 'queued') return 'Queued';
  if (turn.status === 'retrying') return 'Retrying';
  const activity = turn.activities.slice().reverse().find(item => isRunning(item.status)
    || Boolean(item.thread && isRunning(item.thread.status)));
  if (!activity) return turn.responses.some(message => !message.artifact && message.content)
    ? 'Writing response' : turn.status === 'starting' ? 'Starting work' : 'Working';
  if (activity.kind === 'thinking') return 'Thinking';
  if (activity.kind === 'search') return 'Searching the web';
  if (activity.kind === 'plan') return 'Planning';
  if (activity.kind === 'goal') return 'Working on goal';
  if (activity.kind === 'subagent') return activity.title === 'wait' ? 'Waiting for subagents' : 'Subagents working';
  if (activity.kind === 'commentary') return activity.text.trim().split('\n').find(Boolean)?.replace(/^[#>*\s]+/, '').slice(0, 120) || 'Updating progress';
  if (activity.data.type === 'contextCompaction' || /compac/i.test(activity.title)) return 'Compacting context';
  if (activity.data.type === 'fileChange') return 'Editing files';
  if (activity.data.type === 'commandExecution' || /^(bash|exec_command|shell|terminal)$/i.test(activity.title)) return 'Running command';
  if (/^(read|read_file|readfile)$/i.test(activity.title)) return 'Reading file';
  if (/^(write|edit|apply_patch|write_file)$/i.test(activity.title)) return 'Editing files';
  return activity.title.replace(/_/g, ' ') || 'Working';
}

function TurnActivity({ turn, onPermission, onQuestion }: { turn: TranscriptTurn; onPermission: PermissionHandler; onQuestion?: PermissionHandler }) {
  const contentId = useId();
  const [override, setOverride] = useState<{ running: boolean; open: boolean } | null>(null);
  const running = isRunning(turn.status);
  const open = override?.running === running ? override.open : false;
  const renderContent = useDisclosureContent(open);
  const disclosureMotion = useDisclosureMotion(open);
  const resolvedRequests = turn.requests.filter(request => request.resolved);
  const hasStats = Boolean(turn.model || turn.serviceTier || turn.outputTokens !== undefined || turn.tokensPerSecond !== undefined);
  if (!turn.activities.length && !running && !resolvedRequests.length && !hasStats) return null;
  const serviceTitle = !turn.users.length && !turn.responses.length && turn.activities.length === 1
    && turn.activities[0].kind === 'event' ? turn.activities[0].title : '';
  const label = running ? currentActivityLabel(turn) : serviceTitle || (isFailed(turn.status) ? 'Execution failed'
    : ['stopped', 'interrupted', 'cancelled', 'canceled'].includes(turn.status) ? statusLabel(turn.status)
      : turn.activities.length ? 'Activity' : 'Response details');
  return <m.section variants={fade} initial="hidden" animate="visible" exit="exit" className={`transcript-turn-activity${running ? ' is-running' : ''}`}>
    <m.button className="transcript-activity-toggle" type="button" aria-expanded={open} aria-controls={contentId} whileTap={{ opacity: .7 }}
      onClick={() => setOverride({ running, open: !open })}>
      <AnimatePresence initial={false} mode="wait"><m.span className="transcript-motion-icon" key={running ? 'running' : isFailed(turn.status) ? 'failed' : 'done'}
        variants={fade} initial="hidden" animate="visible" exit="exit" aria-hidden="true">
        {running ? <LoaderCircle size={14} className="transcript-spin" />
          : isFailed(turn.status) ? <CircleAlert size={14} /> : <CheckCheck size={14} />}
      </m.span></AnimatePresence>
      <span className="transcript-current-activity" role={running ? 'status' : undefined} aria-live={running ? 'polite' : undefined} title={label}>
        <AnimatePresence initial={false} mode="wait"><m.span key={running ? turn.activities.at(-1)?.id || turn.status : turn.status}
          variants={fade} initial="hidden" animate="visible" exit="exit">{label}</m.span></AnimatePresence>
      </span>
      {turn.activities.length > 0 && <small className="transcript-activity-count" aria-label={`Action count: ${turn.activities.length}`}>
        <AnimatePresence initial={false} mode="wait"><m.span key={turn.activities.length} variants={fade} initial="hidden" animate="visible" exit="exit">{turn.activities.length}</m.span></AnimatePresence>
      </small>}
      <Chevron open={open} size={13} />
    </m.button>
    <m.div {...disclosureMotion} className={`transcript-disclosure${open ? ' is-open' : ''}`} id={contentId} aria-hidden={!open} inert={!open}>
      <div className="transcript-disclosure-inner">{renderContent && <div className="transcript-activity-list">
      <AnimatePresence initial={false}>{turn.activities.map(activity => <ActivityItem key={activity.id} activity={activity} onPermission={onPermission} onQuestion={onQuestion} />)}</AnimatePresence>
      <AnimatePresence initial={false}>{resolvedRequests.map(request => <RequestCard key={request.id} request={request} onPermission={onPermission} onQuestion={onQuestion} />)}</AnimatePresence>
      {!running && hasStats && <div className="transcript-turn-stats">
        <span>{[turn.backend === 'pi' ? 'Pi' : turn.backend === 'codex' ? 'Codex' : turn.backend, turn.model, turn.effort,
          ['priority', 'fast'].includes(turn.serviceTier) ? 'Fast' : turn.serviceTier].filter(Boolean).join(' · ')}</span>
        <span title="Estimated from generation time. Tool execution time is excluded when reported by the runtime.">{turn.tokensPerSecond !== undefined && turn.tokensPerSecond > 0 ? `≈ ${turn.tokensPerSecond.toFixed(1)}` : '—'} tok/s</span>
        {turn.outputTokens !== undefined && turn.outputTokens > 0 && <span>{turn.outputTokens.toLocaleString('en')} tokens</span>}
        {duration(turn.generationMs) && <span>{duration(turn.generationMs)} generation</span>}
      </div>}
      <RawJournal events={turn.events} />
      </div>}</div>
    </m.div>
  </m.section>;
}

function QuestionField({ question, value, onChange }: {
  question: Question; value: string[]; onChange: (answer: string[]) => void;
}) {
  const selected = value.filter(v => question.options.some(option => option.label === v));
  const other = value.find(v => !question.options.some(option => option.label === v)) || '';
  return <fieldset className="transcript-question"><legend>{question.header && <small>{question.header}</small>}{question.question}</legend>
    {question.options.map(option => <m.label whileTap={{ scale: .99 }} transition={motionTransition.quick}
      className={`transcript-question-option${selected.includes(option.label) ? ' is-selected' : ''}`} key={option.label}>
      <input type={question.multiSelect ? 'checkbox' : 'radio'} name={question.id} value={option.label} checked={selected.includes(option.label)}
        onChange={() => onChange(question.multiSelect
          ? selected.includes(option.label) ? value.filter(v => v !== option.label) : [...value, option.label]
          : [option.label])} />
      <span><strong>{option.label}</strong>{option.description && <small>{option.description}</small>}</span>
    </m.label>)}
    {(question.allowOther || !question.options.length) && <label className="transcript-free-answer">
      <span>{question.options.length ? 'Other' : 'Your answer'}</span>
      <input type={question.isSecret ? 'password' : 'text'} autoComplete="off" value={other}
        onChange={event => onChange(question.multiSelect ? [...selected, event.target.value] : [event.target.value])}
        placeholder={question.options.length ? 'Or write an answer…' : 'Write an answer…'} />
    </label>}
  </fieldset>;
}

function RequestCard({ request, onPermission, onQuestion }: {
  request: UserRequest; onPermission: PermissionHandler; onQuestion?: PermissionHandler;
}) {
  const present = useIsPresent();
  const [answers, setAnswers] = useState<Record<string, string[]>>({});
  const [input, setInput] = useState('');
  const [busy, setBusy] = useState(false);
  const [answered, setAnswered] = useState(false);
  const [error, setError] = useState('');
  const resolved = request.resolved || answered;
  const formOpen = !resolved && present;
  const renderForm = useDisclosureContent(formOpen);
  const disclosureMotion = useDisclosureMotion(formOpen);
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
    } catch (err) { setError(err instanceof Error ? err.message : 'Unable to send response'); }
    finally { setBusy(false); }
  }
  const canSubmit = request.questions.length
    ? request.questions.every(q => (answers[q.id] || []).some(v => v.trim()))
    : request.method !== 'input' || Boolean(input.trim());
  function submit(event: FormEvent) { event.preventDefault(); void respond('allow'); }
  return <m.section variants={fadeUp} initial="hidden" animate="visible" exit="exit"
    className={`transcript-request${resolved ? ' is-resolved' : ''}`} aria-label={request.title} aria-hidden={!present} inert={!present}>
    <div className="transcript-request-header"><ShieldCheck size={17} /><strong>{request.title}</strong>
      <AnimatePresence initial={false}>{resolved && <m.span key="resolved" variants={fade} initial="hidden" animate="visible" exit="exit">{request.behavior === 'deny' ? 'Denied' : 'Closed'}</m.span>}</AnimatePresence>
    </div>
    <m.div {...disclosureMotion} className="transcript-disclosure transcript-request-decision" aria-hidden={!formOpen} inert={!formOpen}>
      {renderForm && <form onSubmit={submit}>
        {request.questions.length ? request.questions.map(q => <QuestionField question={q} key={q.id} value={answers[q.id] || []}
          onChange={value => setAnswers(v => ({ ...v, [q.id]: value }))} />)
          : <>{Boolean(request.input) && <Payload label="Action" value={request.input} />}
            {request.method === 'input' && <input className="transcript-request-input" value={input} onChange={event => setInput(event.target.value)}
              placeholder={request.placeholder || 'Your answer'} autoComplete="off" aria-label={request.title} />}</>}
        <AnimatePresence initial={false}>{error && <m.p key="error" variants={fade} initial="hidden" animate="visible" exit="exit" className="transcript-error" role="alert">{error}</m.p>}</AnimatePresence>
        <div className="transcript-request-actions">
          <m.button {...softControlMotion} className="transcript-button is-primary" type="submit" disabled={busy || !formOpen || !canSubmit}>
            <AnimatePresence initial={false} mode="wait"><m.span className="transcript-motion-icon" key={busy ? 'busy' : 'ready'} variants={fade} initial="hidden" animate="visible" exit="exit">
              {busy ? <LoaderCircle size={14} className="transcript-spin" /> : <Check size={14} />}
            </m.span></AnimatePresence>
            {request.questions.length || request.method === 'input' ? 'Send response' : 'Allow'}
          </m.button>
          <m.button {...softControlMotion} className="transcript-button" type="button" disabled={busy || !formOpen} onClick={() => void respond('deny')}><X size={14} />{request.questions.length ? 'Skip' : 'Deny'}</m.button>
        </div>
      </form>}
    </m.div>
    <AnimatePresence initial={false}>{resolved && <m.p key="closed" variants={fade} initial="hidden" animate="visible" exit="exit" className="transcript-muted">
      {request.questions.length ? request.questions.map(q => q.question).join(' · ') : 'This request no longer needs a decision.'}
    </m.p>}</AnimatePresence>
  </m.section>;
}

function JournalItem({ event }: { event: JournalEvent }) {
  const contentId = useId();
  const [open, setOpen] = useState(false);
  const renderContent = useDisclosureContent(open);
  const disclosureMotion = useDisclosureMotion(open);
  const data = record(event.data);
  return <div className="transcript-journal-event"><m.button type="button" className="transcript-journal-event-toggle"
    aria-expanded={open} aria-controls={contentId} whileTap={{ opacity: .7 }} onClick={() => setOpen(value => !value)}>
      <code>#{event.seq}</code><span>{event.type === 'native' ? string(data.method) : string(field(data, 'type')) || event.type}</span>
      <time dateTime={event.time}>{dateTime(event.time)}</time><Chevron open={open} />
    </m.button><m.div {...disclosureMotion} className={`transcript-disclosure${open ? ' is-open' : ''}`} id={contentId} aria-hidden={!open} inert={!open}>
      {renderContent && <Payload label="Event" value={event} />}
    </m.div>
  </div>;
}

function RawJournal({ events, title }: { events: JournalEvent[]; title?: string }) {
  const contentId = useId();
  const [query, setQuery] = useState('');
  const [open, setOpen] = useState(false);
  const renderContent = useDisclosureContent(open);
  const disclosureMotion = useDisclosureMotion(open);
  const visible = query ? events.filter(event => {
    const data = record(event.data);
    return `${event.seq} ${event.type} ${string(data.method)} ${string(field(data, 'type'))}`.toLowerCase().includes(query.toLowerCase());
  }) : events;
  if (!events.length) return null;
  return <div className="transcript-journal"><m.button type="button" className="transcript-journal-toggle"
    aria-expanded={open} aria-controls={contentId} whileTap={{ opacity: .7 }} onClick={() => setOpen(value => !value)}>
    <Code2 size={12} /><span>{title || 'Turn journal'}</span><small>{events.length}</small><Chevron open={open} />
    </m.button><m.div {...disclosureMotion} className={`transcript-disclosure${open ? ' is-open' : ''}`} id={contentId} aria-hidden={!open} inert={!open}>
    {renderContent && <div className="transcript-journal-body"><div className="transcript-journal-toolbar">
      <label><Search size={13} /><input value={query} onChange={event => setQuery(event.target.value)} placeholder="Event type or method…" aria-label="Find an event in the journal" /></label>
      <CopyButton content={events.map(event => JSON.stringify(event)).join('\n')} label="Copy JSONL journal" />
    </div><div className="transcript-journal-events">{visible.map(event => <JournalItem key={event.seq} event={event} />)}
      {!visible.length && <p className="transcript-muted">No matching events.</p>}</div></div>}
    </m.div>
  </div>;
}

function Turn({ bot, turn, onPermission, onQuestion, onRetry }: TranscriptProps & { turn: TranscriptTurn }) {
  const present = useIsPresent();
  const running = isRunning(turn.status);
  const service = !turn.users.length && !turn.responses.length && !turn.requests.length && !running && !turn.error;
  const hasBotContent = Boolean(turn.responses.length || turn.activities.length || turn.requests.length || running || turn.error);
  return <m.section variants={fadeUp} initial="hidden" animate="visible" exit="exit"
    className={`transcript-turn${service ? ' is-service' : ''}`} data-turn-id={turn.id} aria-hidden={!present} inert={!present}>
    <AnimatePresence initial={false}>{turn.users.map(message => <Message key={message.id} message={message} botId={bot.id} />)}</AnimatePresence>
    <AnimatePresence initial={false}>{turn.notices.map((notice, index) => <m.div className="transcript-notice" key={index}
      variants={fade} initial="hidden" animate="visible" exit="exit"><MessageSquare size={13} /><Markdown content={notice} /></m.div>)}</AnimatePresence>
    {hasBotContent && <div className="transcript-bot-response">
      <div className="transcript-response-body">
        <TurnActivity turn={turn} onPermission={onPermission} onQuestion={onQuestion} />
        <AnimatePresence initial={false}>{pendingRequests(turn).map(request => <RequestCard key={request.id} request={request} onPermission={onPermission} onQuestion={onQuestion} />)}</AnimatePresence>
        <AnimatePresence initial={false}>{turn.responses.map(message => <Message key={message.id} message={message} botId={bot.id} />)}</AnimatePresence>
        <AnimatePresence initial={false}>{turn.error && <m.div key="error" variants={fadeUp} initial="hidden" animate="visible" exit="exit"
          className="transcript-turn-error" role="alert"><CircleAlert size={16} /><div><strong>Unable to finish work</strong><p>{turn.error}</p>
          {onRetry && <m.button {...softControlMotion} type="button" className="transcript-button" onClick={() => void onRetry(turn.id)}>Refresh status</m.button>}
        </div></m.div>}</AnimatePresence>
      </div>
    </div>}
  </m.section>;
}

export function Transcript(props: TranscriptProps) {
  const { bot, events } = props;
  const turns = useMemo(() => buildTranscript(events, bot.id), [events, bot.id]);
  const visible = turns.filter(turn => turn.users.length || turn.responses.length || turn.activities.length || turn.requests.length || turn.notices.length || turn.error || isRunning(turn.status));
  return <div className="transcript" aria-label={`Conversation with ${bot.name}`}>
    <AnimatePresence initial={false}>{!visible.length && <m.div key="empty" variants={fadeUp} initial="hidden" animate="visible" exit="exit" className="transcript-empty">
      <m.div className="transcript-empty-avatar" whileHover={{ rotate: 3, y: -2 }} transition={motionTransition.enter}><Avatar bot={bot} size={72} /></m.div>
      <span className="transcript-empty-eyebrow">{bot.chief ? 'YOUR COORDINATOR' : 'YOUR PERSISTENT ASSISTANT'}</span>
      <h2>{bot.name} is ready.</h2><p>{bot.role || 'Tell me what you want to achieve. Your bot will keep the context and continue working here.'}</p>
    </m.div>}
    {visible.map(turn => <Turn key={turn.id} {...props} turn={turn} />)}</AnimatePresence>
    {events.length > 0 && <div className="transcript-all-events"><RawJournal events={events.filter(event => event.botId === bot.id)} title="All bot events" /></div>}
    <div className="transcript-end" />
  </div>;
}

export default Transcript;
