import { useEffect, useMemo, useRef, useState } from "react";
import type { RefObject } from "react";
import { FileText, Image, MessageSquare, Send, Settings2, Target, X } from "lucide-react";
import Avatar from "../../components/Avatar";
import { fileURL } from "../../lib/api";
import { botMessagePresentation, telegramLabel, telegramTitle } from "../../lib/events";
import type { Attachment, Bot, Event } from "../../lib/types";
import { useDialogFocus } from "./ModelPicker";
import "./bot-island.css";

const desktopQuery = "(min-width: 1100px)";
const avatarPriorityStatuses = new Set(["blocked", "waiting", "interrupted", "failed", "error"]);

function attachmentURL(value: unknown): string | undefined {
  if (typeof value !== "string" || !value) return undefined;
  if (value.startsWith("/") && !value.startsWith("//") && !value.includes("\\")) return value;
  try {
    const url = new URL(value);
    return ["http:", "https:"].includes(url.protocol) ? url.href : undefined;
  } catch {
    return undefined;
  }
}

function botIslandContent(events: Event[], botId: string) {
  const requests: { seq: number; label: string }[] = [];
  const files: Attachment[] = [];
  const seenRequests = new Set<string>();
  const seenFiles = new Set<string>();
  const seenEvents = new Set<number>();
  for (const event of [...events].sort((a, b) => b.seq - a.seq)) {
    if (event.botId !== botId || event.type !== "message" || seenEvents.has(event.seq)) continue;
    seenEvents.add(event.seq);
    const data = event.data;
    if (data.role === "user" && data.source !== "goal_context" && requests.length < 3) {
      const content = typeof data.content === "string" ? data.content : "";
      const delegated = botMessagePresentation(content, String(data.source || ""));
      const label = (delegated ? `${delegated.sender}: ${delegated.content}` : content)
        .replace(/\s+/g, " ").trim();
      if (label && !seenRequests.has(label)) {
        seenRequests.add(label);
        requests.push({ seq: event.seq, label });
      }
    }
    if (data.role === "assistant" && Array.isArray(data.attachments)) {
      for (const value of data.attachments) {
        if (!value || typeof value !== "object" || Array.isArray(value)) continue;
        const attachment = value as Record<string, unknown>;
        const id = typeof attachment.id === "string" ? attachment.id : "";
        const url = id ? undefined : attachmentURL(attachment.url);
        const key = id || url;
        if (!key || seenFiles.has(key) || files.length >= 3) continue;
        seenFiles.add(key);
        files.push({
          id,
          name: typeof attachment.name === "string" && attachment.name ? attachment.name : "File",
          mimeType: typeof attachment.mimeType === "string" ? attachment.mimeType : "",
          url,
        });
      }
    }
    if (requests.length === 3 && files.length === 3) break;
  }
  return { requests, files };
}

export default function BotIsland({
  bot, events, status, working, supportsGoal, hasGoal, onGoal, onSettings,
  open, onClose, triggerRef,
}: {
  bot: Bot;
  events: Event[];
  status: string;
  working: boolean;
  supportsGoal: boolean;
  hasGoal: boolean;
  onGoal: () => void;
  onSettings: () => void;
  open: boolean;
  onClose: () => void;
  triggerRef: RefObject<HTMLButtonElement | null>;
}) {
  const [desktop, setDesktop] = useState(() =>
    typeof window !== "undefined" && typeof window.matchMedia === "function"
      ? window.matchMedia(desktopQuery).matches : false,
  );
  const dialog = useRef<HTMLDivElement>(null);
  const overlay = open && !desktop;
  const { requests, files } = useMemo(() => botIslandContent(events, bot.id), [events, bot.id]);
  const avatarStatus = avatarPriorityStatuses.has(bot.status)
    ? bot.status
    : working ? "working" : bot.status;

  useEffect(() => {
    const media = window.matchMedia(desktopQuery);
    const update = () => setDesktop(media.matches);
    update();
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, []);

  useEffect(() => {
    if (!desktop) return;
    const focused = document.activeElement;
    if (focused === triggerRef.current || focused?.classList.contains("bot-island-close")) {
      dialog.current?.querySelector<HTMLButtonElement>(".bot-island-action")?.focus();
    }
  }, [desktop, triggerRef]);

  function close() {
    onClose();
    window.setTimeout(() => triggerRef.current?.focus(), 0);
  }

  function openAction(action: () => void) {
    if (!overlay) {
      action();
      return;
    }
    onClose();
    // Let the island's trap finish before the next dialog captures its return target.
    window.setTimeout(() => {
      triggerRef.current?.focus();
      action();
    }, 0);
  }

  useDialogFocus(overlay, dialog, close);

  return (
    <aside className={`bot-island-shell${open ? " is-open" : ""}`}
      aria-label="Bot details" inert={!desktop && !open}>
      <div className="bot-island-backdrop" onClick={close} aria-hidden="true" />
      <div className="bot-island-card" ref={dialog} tabIndex={-1}
        role={overlay ? "dialog" : undefined} aria-modal={overlay || undefined}
        aria-label={overlay ? "Bot details" : undefined}>
        <button type="button" className="icon-button bot-island-close" onClick={close}
          aria-label="Close bot details"><X size={18} /></button>
        <div className="bot-island-profile">
          <Avatar bot={bot} size={52} status={avatarStatus} />
          <div className="bot-island-identity">
            <strong>{bot.name}</strong>
            <span className={`bot-island-status${working ? " is-working" : ""}`}>
              {working && <span className="bot-island-live-dot" aria-hidden="true" />}
              {status}
            </span>
          </div>
        </div>
        {bot.role && <p className="bot-island-role" title={bot.role}>{bot.role}</p>}
        <div className="bot-island-actions">
          <button type="button" className="bot-island-action" onClick={() => openAction(onSettings)}
            aria-label="Bot settings"><Settings2 size={16} />Settings</button>
          {supportsGoal && (
            <button type="button" className={`bot-island-action${hasGoal ? " has-goal" : ""}`}
              onClick={() => openAction(onGoal)} aria-label="Bot goal"
              title={hasGoal ? "View bot goal" : "Set bot goal"}>
              <Target size={16} />Goal{hasGoal && <span className="bot-island-goal-dot" aria-hidden="true" />}
            </button>
          )}
        </div>
        {bot.telegram?.enabled && (
          <div className={`bot-island-telegram telegram-status-${bot.telegram.status || "configured"}`}
            title={telegramTitle(bot.telegram)}>
            <Send size={15} aria-hidden="true" />
            <span>{telegramLabel(bot.telegram)}
              {bot.telegram.username && <small>@{bot.telegram.username}</small>}
            </span>
          </div>
        )}
        {requests.length > 0 && (
          <section className="bot-island-section" aria-label="Recent tasks">
            <h2>Recent tasks</h2>
            <ul className="bot-island-list">
              {requests.map(request => (
                <li key={request.seq} className="bot-island-request">
                  <MessageSquare size={15} aria-hidden="true" />
                  <span title={request.label}>{request.label}</span>
                </li>
              ))}
            </ul>
          </section>
        )}
        {files.length > 0 && (
          <section className="bot-island-section" aria-label="Outputs">
            <h2>Outputs</h2>
            <ul className="bot-island-list">
              {files.map(file => (
                <li key={file.id || file.url}>
                  <a className="bot-island-file" href={fileURL(bot.id, file)}
                    target="_blank" rel="noreferrer noopener" title={file.name}>
                    {file.mimeType.startsWith("image/") ? <Image size={16} aria-hidden="true" /> : <FileText size={16} aria-hidden="true" />}
                    <span>{file.name}</span>
                  </a>
                </li>
              ))}
            </ul>
          </section>
        )}
      </div>
    </aside>
  );
}
