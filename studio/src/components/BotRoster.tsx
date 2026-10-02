import {
  Plus,
  Settings2,
  LogOut,
  Crown,
  Send,
} from "lucide-react";
import type { Bot, Event } from "../lib/types";
import type { ThemePreference } from "../lib/theme";
import {
  isWorking,
  messagePreview,
  statusLabel,
  telegramTitle,
} from "../lib/events";
import Avatar from "./Avatar";
import ThemePicker from "./ThemePicker";
function lastMessageTime(events: Event[]) {
  const event = [...events].reverse().find(event => event.type === "message" && event.data.source !== "goal_context");
  if (!event) return null;
  const date = new Date(event.time), now = new Date();
  if (!Number.isFinite(date.getTime())) return null;
  const yesterday = new Date();
  yesterday.setDate(now.getDate() - 1);
  const label = date.toDateString() === now.toDateString()
    ? new Intl.DateTimeFormat("en-GB", { hour: "2-digit", minute: "2-digit" }).format(date)
    : date.toDateString() === yesterday.toDateString()
      ? "Yesterday"
      : new Intl.DateTimeFormat("en-GB", { day: "2-digit", month: "2-digit" }).format(date);
  return { label, iso: event.time, full: date.toLocaleString("en-GB") };
}
export default function BotRoster({
  bots,
  events,
  selectedId,
  onSelect,
  onCreate,
  onSettings,
  onTheme,
  theme,
  onLogout,
  connection,
}: {
  bots: Bot[];
  events: Record<string, Event[]>;
  selectedId: string;
  onSelect: (id: string) => void;
  onCreate: () => void;
  onSettings: () => void;
  onTheme: (theme: ThemePreference) => void;
  theme: ThemePreference;
  onLogout: () => void;
  connection: string;
}) {
  const sortedBots = [...bots].sort(
    (a, b) =>
      Number(b.chief) - Number(a.chief) ||
      a.createdAt.localeCompare(b.createdAt),
  );
  return (
    <aside className="roster">
      <nav aria-label="Bots" className="roster-list">
        {sortedBots.map((bot) => {
          const busy = isWorking(bot.status);
          const time = lastMessageTime(events[bot.id] || []);
          const preview = busy
            ? statusLabel(bot.status)
            : messagePreview(events[bot.id] || []) ||
              bot.role ||
              "Start a conversation";
          return (
            <button
              key={bot.id}
              className={`bot-row ${selectedId === bot.id ? "is-selected" : ""}`}
              onClick={() => onSelect(bot.id)}
              aria-current={selectedId === bot.id ? "page" : undefined}
            >
              <Avatar bot={bot} size={42} />
              <span className="bot-row-copy">
                <span className="bot-row-title">
                  <span className="bot-row-name">{bot.name}</span>
                  {bot.chief && <Crown size={12} className="chief-icon" />}
                  {bot.telegram?.enabled && (
                    <span title={telegramTitle(bot.telegram)}>
                      <Send
                        size={12}
                        className={`telegram-icon telegram-status-${bot.telegram.status || "configured"}`}
                        aria-label={telegramTitle(bot.telegram)}
                      />
                    </span>
                  )}
                  {time && <time className="bot-row-time" dateTime={time.iso} title={time.full}>{time.label}</time>}
                </span>
                <span className={`bot-row-preview ${busy ? "is-working" : ""}`}>
                  {busy && <span className="live-pip" />}
                  {preview}
                </span>
              </span>
              {busy && <span className="row-working-dot" />}
            </button>
          );
        })}
        {!sortedBots.length && (
          <div className="roster-empty">
            Your team will appear here.
          </div>
        )}
      </nav>
      <footer className="roster-footer">
        <div className="roster-connection">
          <span
            className={`status-dot ${connection === "connected" ? "is-connected" : "is-reconnecting"}`}
          />
          {connection === "connected"
            ? "Workspace connected"
            : "Reconnecting"}
        </div>
        <div className="roster-footer-actions">
          <button type="button" onClick={onSettings} className="footer-settings">
            <Settings2 size={16} />
            <span>Settings</span>
          </button>
          <button
            type="button"
            className="icon-button"
            onClick={onCreate}
            aria-label="Create bot"
            title="Create bot"
          >
            <Plus size={19} />
          </button>
          <ThemePicker value={theme} onChange={onTheme} />
          <button
            className="icon-button"
            onClick={onLogout}
            aria-label="Sign out"
            title="Sign out"
          >
            <LogOut size={16} />
          </button>
        </div>
      </footer>
    </aside>
  );
}
