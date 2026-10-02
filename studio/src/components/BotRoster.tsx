import { Plus, Settings2, LogOut, Crown, Send } from "lucide-react";
import type { Bot, Event } from "../lib/types";
import type { ThemePreference } from "../lib/theme";
import {
  isWorking,
  messagePreview,
  statusLabel,
  telegramTitle,
} from "../lib/events";
import Avatar from "./Avatar";
import SidebarResizeHandle from "./SidebarResizeHandle";
import ThemePicker from "./ThemePicker";
import {
  AnimatePresence,
  controlMotion,
  fade,
  LayoutGroup,
  m,
  motionSpring,
  motionTransition,
  rowMotion,
  useIsPresent,
} from "../lib/motion";
function lastMessageTime(events: Event[]) {
  const event = [...events]
    .reverse()
    .find(
      (event) =>
        event.type === "message" && event.data.source !== "goal_context",
    );
  if (!event) return null;
  const date = new Date(event.time),
    now = new Date();
  if (!Number.isFinite(date.getTime())) return null;
  const yesterday = new Date();
  yesterday.setDate(now.getDate() - 1);
  const label =
    date.toDateString() === now.toDateString()
      ? new Intl.DateTimeFormat("en-GB", {
          hour: "2-digit",
          minute: "2-digit",
        }).format(date)
      : date.toDateString() === yesterday.toDateString()
        ? "Yesterday"
        : new Intl.DateTimeFormat("en-GB", {
            day: "2-digit",
            month: "2-digit",
          }).format(date);
  return { label, iso: event.time, full: date.toLocaleString("en-GB") };
}

function RosterRow({
  bot,
  events,
  selected,
  layoutDependency,
  onSelect,
}: {
  bot: Bot;
  events: Event[];
  selected: boolean;
  layoutDependency: string;
  onSelect: (id: string) => void;
}) {
  const present = useIsPresent();
  const busy = isWorking(bot.status);
  const time = lastMessageTime(events);
  const preview = busy
    ? statusLabel(bot.status)
    : messagePreview(events) || bot.role || "Start a conversation";
  return (
    <m.button
      type="button"
      layout="position"
      layoutDependency={layoutDependency}
      variants={rowMotion}
      initial="hidden"
      animate="visible"
      exit="exit"
      transition={{ ...motionSpring.control, layout: motionSpring.layout }}
      whileTap={{ scale: 0.985 }}
      className={`bot-row ${selected ? "is-selected" : ""}`}
      onClick={() => onSelect(bot.id)}
      aria-current={selected ? "page" : undefined}
      disabled={!present}
      aria-hidden={!present || undefined}
    >
      {selected && (
        <m.span
          className="bot-row-selection"
          layoutId="selected-bot"
          transition={motionSpring.layout}
          aria-hidden="true"
        />
      )}
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
          {time && (
            <time
              className="bot-row-time"
              dateTime={time.iso}
              title={time.full}
            >
              {time.label}
            </time>
          )}
        </span>
        <span className={`bot-row-preview ${busy ? "is-working" : ""}`}>
          {busy && <span className="live-pip" />}
          {preview}
        </span>
      </span>
      {busy && <span className="row-working-dot" />}
    </m.button>
  );
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
  mobileHidden = false,
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
  mobileHidden?: boolean;
}) {
  const sortedBots = [...bots].sort(
    (a, b) =>
      Number(b.chief) - Number(a.chief) ||
      a.createdAt.localeCompare(b.createdAt),
  );
  const layoutDependency = JSON.stringify([sortedBots.map((bot) => bot.id), selectedId]);
  return (
    <m.aside
      id="bot-roster"
      className="roster"
      initial={false}
      animate={{ opacity: mobileHidden ? 0 : 1 }}
      transition={motionTransition.enter}
      inert={mobileHidden}
      aria-hidden={mobileHidden || undefined}
    >
      <LayoutGroup id="bot-roster">
        <m.nav aria-label="Bots" className="roster-list" layoutScroll>
          <AnimatePresence initial={false}>
            {sortedBots.map((bot) => (
              <RosterRow
                key={bot.id}
                bot={bot}
                events={events[bot.id] || []}
                selected={selectedId === bot.id}
                layoutDependency={layoutDependency}
                onSelect={onSelect}
              />
            ))}
            {!sortedBots.length && (
              <m.div
                key="empty-roster"
                className="roster-empty"
                variants={fade}
                initial="hidden"
                animate="visible"
                exit="exit"
              >
                Your team will appear here.
              </m.div>
            )}
          </AnimatePresence>
        </m.nav>
      </LayoutGroup>
      <footer className="roster-footer">
        <div className="roster-connection">
          <span
            className={`status-dot ${connection === "connected" ? "is-connected" : "is-reconnecting"}`}
          />
          <AnimatePresence initial={false} mode="wait">
            <m.span
              key={connection === "connected" ? "connected" : "reconnecting"}
              variants={fade}
              initial="hidden"
              animate="visible"
              exit="exit"
            >
              {connection === "connected"
                ? "Workspace connected"
                : "Reconnecting"}
            </m.span>
          </AnimatePresence>
        </div>
        <div className="roster-footer-actions">
          <m.button
            type="button"
            onClick={onSettings}
            className="footer-settings"
            data-motion-control
            {...controlMotion}
          >
            <Settings2 size={16} />
            <span>Settings</span>
          </m.button>
          <m.button
            type="button"
            className="icon-button"
            onClick={onCreate}
            aria-label="Create bot"
            title="Create bot"
            data-motion-control
            {...controlMotion}
          >
            <Plus size={19} />
          </m.button>
          <ThemePicker value={theme} onChange={onTheme} />
          <m.button
            type="button"
            className="icon-button"
            onClick={onLogout}
            aria-label="Sign out"
            title="Sign out"
            data-motion-control
            {...controlMotion}
          >
            <LogOut size={16} />
          </m.button>
        </div>
      </footer>
      <SidebarResizeHandle />
    </m.aside>
  );
}
