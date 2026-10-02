import { memo, useEffect, useState } from "react";
import { CornerDownRight, LoaderCircle, Play, Square, Trash2, Zap } from "lucide-react";
import type { BotContext, MessageQueueSnapshot } from "../../lib/types";
import type { ChatStatus } from "../../lib/chatStatus";
import { effectiveSessionTime, effortLabel, formatElapsed, turnElapsed } from "../../lib/chatStatus";
import { contextNumbers } from "../../lib/contextState";
import { m, controlMotion } from "../../lib/motion";

function useClock(active: boolean) {
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    setNow(Date.now());
    if (!active) return;
    const timer = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(timer);
  }, [active]);
  return now;
}

// Clocks are leaves: a second passing never re-renders the composer, Markdown,
// the roster or the chat scroll container.
const RuntimeTime = memo(function RuntimeTime({ status, active, session = false }: {
  status: ChatStatus; active: boolean; session?: boolean;
}) {
  const now = useClock(active);
  const ms = session ? effectiveSessionTime(status, now) : turnElapsed(status, now);
  return <time className={session ? "session-time" : "working-time"} aria-hidden={!session || undefined} title={session ? "Time spent in active turns" : "Current turn duration"}>
    {formatElapsed(ms)}
  </time>;
});

export const WorkingStrip = memo(function WorkingStrip({ working, suspended, compacting, status, offline, onStop, onError }: {
  working: boolean; suspended: boolean; compacting: boolean; status: ChatStatus; offline?: boolean;
  onStop: () => Promise<void>; onError: (error: string) => void;
}) {
  const [stopping, setStopping] = useState(false);
  if (!working && !compacting) return null;
  async function stop() {
    if (stopping || offline || suspended) return;
    setStopping(true);
    try { await onStop(); }
    catch (error) { onError(error instanceof Error ? error.message : "Could not stop the bot."); }
    finally { setStopping(false); }
  }
  return <div className="working-strip" role="status">
    <span className="working-identity">
      <span className="working-dot" aria-hidden="true" />
      <span className="working-label">{compacting ? "Compacting" : stopping ? "Stopping" : "Working"}</span>
      {working && <RuntimeTime status={status} active={!suspended && !offline && working} />}
    </span>
    {working && <m.button {...controlMotion} className="working-stop" disabled={stopping || offline || suspended}
      onClick={() => void stop()} aria-label="Stop bot" title="Stop this turn">
      {stopping ? <LoaderCircle size={13} className="spin" /> : <Square size={11} fill="currentColor" />}
    </m.button>}
  </div>;
});

export const SessionStatus = memo(function SessionStatus({ status, working, suspended, offline, context, model, effort, tier }: {
  status: ChatStatus; working: boolean; suspended: boolean; offline?: boolean;
  context: BotContext | null; model: string; effort: string; tier: string;
}) {
  const percent = contextNumbers(context).percent;
  const speed = status.tokensPerSecond;
  return <div className="session-statusline" aria-label="Session status">
    <span title="Unique tool, search and subagent calls in this turn">Step <b>{status.step}</b></span>
    <span title="Conversation turns">Turn <b>{status.turn}</b></span>
    <span title="Inference speed from the last completed turn"><b>{working || speed === null ? "—" : speed.toFixed(1)}</b> tok/s</span>
    <span title={context?.estimated ? "Estimated context usage" : "Context usage"}>Context <b>{percent === undefined ? "—" : `${context?.estimated ? "≈" : ""}${Math.round(percent)}%`}</b></span>
    <span className="statusline-model" title={model}>{model}</span>
    <span>{effortLabel(effort)}</span>
    {tier && <span className="statusline-tier" title="Service tier"><Zap size={9} />{tier}</span>}
    <span className="statusline-session" title="Effective session time: active turns, excluding idle time">Session <RuntimeTime status={status} active={working && !suspended && !offline} session /></span>
  </div>;
});

export const QueuePanel = memo(function QueuePanel({ snapshot, pending, busy, offline, suspended, error, onSteer, onRemove, onResume }: {
  snapshot: MessageQueueSnapshot; pending: string; busy: boolean; offline?: boolean; suspended: boolean; error: string;
  onSteer: (id: string) => Promise<void>; onRemove: (id: string) => Promise<void>; onResume: () => Promise<void>;
}) {
  if (!snapshot.messages.length && !snapshot.paused) return null;
  const disabled = offline || suspended || !!pending;
  return <section className="message-queue" aria-label="Queued messages">
    {snapshot.messages.map(message => <div className="queued-message" key={message.id}>
      <CornerDownRight size={14} className="queue-mark" aria-hidden="true" />
      <span className="queued-message-content" title={message.text || message.attachments.map(file => file.name).join(", ")}>
        {message.attachments.length > 0 && <span className="queue-attachment-count">{message.attachments.length} {message.attachments.length === 1 ? "file" : "files"}</span>}
        {message.skills?.[0] && <span className="queue-skill-chip" title={message.skills[0].name}>${message.skills[0].name}</span>}
        {(message.skills?.length || 0) > 1 && <span className="queue-skill-more"
          title={message.skills!.slice(1).map(skill => `$${skill.name}`).join(", ")}
          aria-label={`${message.skills!.length - 1} more attached skills: ${message.skills!.slice(1).map(skill => skill.name).join(", ")}`}>+{message.skills!.length - 1}</span>}
        <span>{message.text || message.attachments.map(file => file.name).join(", ")}</span>
      </span>
      <m.button {...controlMotion} className="queue-steer" disabled={disabled || !busy} onClick={() => void onSteer(message.id)}
        aria-label={`Steer queued message: ${message.text || message.attachments[0]?.name || message.skills?.[0]?.name || "attachment"}`} title={busy ? "Send to the current turn" : "Steer is available during a turn"}>
        {pending === `steer:${message.id}` ? <LoaderCircle size={13} className="spin" /> : <CornerDownRight size={13} />}<span>Steer</span>
      </m.button>
      <m.button {...controlMotion} className="queue-remove" disabled={disabled} onClick={() => void onRemove(message.id)}
        aria-label={`Remove queued message: ${message.text || message.attachments[0]?.name || message.skills?.[0]?.name || "attachment"}`}>
        {pending === `remove:${message.id}` ? <LoaderCircle size={13} className="spin" /> : <Trash2 size={13} />}
      </m.button>
    </div>)}
    {snapshot.paused && <div className="queue-paused"><span>Queue paused</span><m.button {...controlMotion} onClick={() => void onResume()} disabled={disabled}>
      {pending === "resume" ? <LoaderCircle size={12} className="spin" /> : <Play size={12} />}Resume
    </m.button></div>}
    {error && <p className="queue-error" role="alert">{error}</p>}
  </section>;
});
