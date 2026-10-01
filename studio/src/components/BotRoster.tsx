import { useState } from "react";
import {
  Plus,
  Search,
  Settings2,
  Sun,
  Moon,
  LogOut,
  Crown,
  Send,
  ChevronsRight,
} from "lucide-react";
import type { Bot, Event } from "../lib/types";
import {
  isWorking,
  messagePreview,
  statusLabel,
  telegramTitle,
} from "../lib/events";
import Avatar from "./Avatar";
import Brand from "./Brand";
function lastMessageTime(events: Event[]) {
  const event = [...events].reverse().find(event => event.type === "message" && event.data.source !== "goal_context");
  if (!event) return null;
  const date = new Date(event.time), now = new Date();
  if (!Number.isFinite(date.getTime())) return null;
  const yesterday = new Date();
  yesterday.setDate(now.getDate() - 1);
  const label = date.toDateString() === now.toDateString()
    ? new Intl.DateTimeFormat("ru", { hour: "2-digit", minute: "2-digit" }).format(date)
    : date.toDateString() === yesterday.toDateString()
      ? "Вчера"
      : new Intl.DateTimeFormat("ru", { day: "2-digit", month: "2-digit" }).format(date);
  return { label, iso: event.time, full: date.toLocaleString("ru") };
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
  onTheme: () => void;
  theme: "light" | "dark";
  onLogout: () => void;
  connection: string;
}) {
  const [query, setQuery] = useState("");
  const visible = [...bots]
    .sort(
      (a, b) =>
        Number(b.chief) - Number(a.chief) ||
        a.createdAt.localeCompare(b.createdAt),
    )
    .filter((bot) =>
      `${bot.name} ${bot.role}`
        .toLocaleLowerCase()
        .includes(query.toLocaleLowerCase()),
    );
  return (
    <aside className="roster">
      <header className="roster-brand">
        <Brand />
        <button
          className="icon-button"
          onClick={onCreate}
          aria-label="Создать бота"
          title="Создать бота"
        >
          <Plus size={19} />
        </button>
      </header>
      <div className="roster-search">
        <Search size={15} />
        <input
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Найти бота"
          aria-label="Поиск ботов"
        />
        <kbd>⌘ K</kbd>
      </div>
      <div className="roster-label">
        Ваша команда <span>{bots.length}</span>
      </div>
      <nav aria-label="Боты" className="roster-list">
        {visible.map((bot) => {
          const busy = isWorking(bot.status);
          const time = lastMessageTime(events[bot.id] || []);
          const preview = busy
            ? statusLabel(bot.status)
            : messagePreview(events[bot.id] || []) ||
              bot.role ||
              "Начните разговор";
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
        {!visible.length && (
          <div className="roster-empty">
            {query ? "Ничего не найдено." : "Здесь появится ваша команда."}
          </div>
        )}
      </nav>
      <button className="create-row" onClick={onCreate}>
        <Plus size={16} />
        Добавить бота
        <ChevronsRight size={14} />
      </button>
      <footer className="roster-footer">
        <div className="roster-connection">
          <span
            className={`status-dot ${connection === "connected" ? "is-connected" : "is-reconnecting"}`}
          />
          {connection === "connected"
            ? "Пространство подключено"
            : "Восстанавливаем соединение"}
        </div>
        <div className="roster-footer-actions">
          <button onClick={onSettings} className="footer-settings">
            <Settings2 size={16} />
            <span>Настройки</span>
          </button>
          <button
            className="icon-button"
            onClick={onTheme}
            aria-label={theme === "dark" ? "Светлая тема" : "Тёмная тема"}
            title={theme === "dark" ? "Светлая тема" : "Тёмная тема"}
          >
            {theme === "dark" ? <Sun size={17} /> : <Moon size={17} />}
          </button>
          <button
            className="icon-button"
            onClick={onLogout}
            aria-label="Выйти"
            title="Выйти"
          >
            <LogOut size={16} />
          </button>
        </div>
      </footer>
    </aside>
  );
}
