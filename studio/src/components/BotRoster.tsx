import { memo, useMemo } from "react";
import { Plus, Settings2, LogOut, Crown, Send } from "lucide-react";
import type { CatalogBot, Event, NodeInfo } from "../lib/types";
import type { StudioUser } from "../lib/api";
import type { ThemePreference } from "../lib/theme";
import { hostCategory, hostLabel } from "../lib/hostCatalog";
import type { HostCategory, HostFilters } from "../lib/hostCatalog";
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
const emptyEvents: Event[] = [];
function lastMessageTime(events: Event[]) {
  let event: Event | undefined;
  for (let index = events.length - 1; index >= 0; index--) {
    if (events[index].type === "message" && events[index].data.source !== "goal_context") {
      event = events[index]; break;
    }
  }
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
  entry,
  events,
  selected,
  layoutDependency,
  onSelect,
}: {
  entry: CatalogBot;
  events: Event[];
  selected: boolean;
  layoutDependency: string;
  onSelect: (id: string, nodeId: string) => void;
}) {
  const { bot, node } = entry;
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
      onClick={() => onSelect(bot.id, node.id)}
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
          <span className={`bot-row-device${node.online ? "" : " is-offline"}`}
            title={`${node.name}${node.hostname ? ` · ${node.hostname}` : ""}${node.online ? "" : " · Offline"}`}>
            {!node.online && <span className="status-dot is-offline" aria-label="Offline" />}
            {hostLabel(node)}
          </span>
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
          {busy && <span className="live-pip" aria-hidden="true" />}
          <span className="bot-row-preview-label">{preview}</span>
        </span>
      </span>
      {busy && <span className="row-working-dot" />}
    </m.button>
  );
}

const MemoRosterRow = memo(RosterRow, (a, b) => a.entry.bot === b.entry.bot && a.entry.node === b.entry.node
  && a.events === b.events && a.selected === b.selected && a.layoutDependency === b.layoutDependency && a.onSelect === b.onSelect);

function BotRoster({
  bots,
  events,
  selectedKey,
  onSelect,
  onCreate,
  onSettings,
  onTheme,
  theme,
  onLogout,
  connection,
  user,
  activeNode,
  hostFilters,
  onFilterChange,
  mobileHidden = false,
}: {
  bots: CatalogBot[];
  events: Record<string, Event[]>;
  selectedKey: string;
  onSelect: (id: string, nodeId: string) => void;
  onCreate: () => void;
  onSettings: () => void;
  onTheme: (theme: ThemePreference) => void;
  theme: ThemePreference;
  onLogout: () => void;
  connection: string;
  user?: StudioUser | null;
  activeNode?: NodeInfo;
  hostFilters: HostFilters;
  onFilterChange: (category: HostCategory, enabled: boolean) => void;
  mobileHidden?: boolean;
}) {
  const sortedBots = useMemo(() => bots.filter(entry => hostFilters[hostCategory(entry.node)]).sort(
    (a, b) =>
      Number(b.bot.chief) - Number(a.bot.chief) ||
      a.bot.createdAt.localeCompare(b.bot.createdAt) || a.key.localeCompare(b.key),
  ), [bots, hostFilters]);
  const layoutDependency = JSON.stringify([sortedBots.map(entry => entry.key), selectedKey]);
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
          <AnimatePresence initial={false} presenceAffectsLayout={false}>
            {sortedBots.map(entry => (
              <MemoRosterRow
                key={entry.key}
                entry={entry}
                events={entry.node.id === activeNode?.id ? events[entry.bot.id] || emptyEvents : emptyEvents}
                selected={selectedKey === entry.key}
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
                {bots.length ? "No bots on the selected devices." : "Your team will appear here."}
              </m.div>
            )}
          </AnimatePresence>
        </m.nav>
      </LayoutGroup>
      <footer className="roster-footer">
        <div className="roster-host-filters" role="group" aria-label="Filter devices">
          {(["server", "mac"] as const).map(category => <label key={category}>
            <input type="checkbox" checked={hostFilters[category]}
              onChange={event => onFilterChange(category, event.target.checked)} />
            <span>{category === "server" ? "Server" : "Mac"}</span>
          </label>)}
        </div>
        <div className="roster-connection" title={`${user ? `Signed in as @${user.username} · ` : ""}${activeNode ? `${activeNode.name} · ` : ""}${activeNode?.online === false ? "Offline" : connection === "connected" ? "Workspace connected" : "Reconnecting"}`}>
          <span
            className={`status-dot ${activeNode?.online === false ? "is-offline" : connection === "connected" ? "is-connected" : "is-reconnecting"}`}
          />
          <AnimatePresence initial={false} mode="wait">
            <m.span
              key={`${user?.id || ""}:${activeNode?.online === false ? "offline" : connection === "connected" ? "connected" : "reconnecting"}`}
              className="roster-connection-copy"
              variants={fade}
              initial="hidden"
              animate="visible"
              exit="exit"
            >
              {user ? `@${user.username} · ${activeNode?.online === false ? "Offline" : connection === "connected" ? "Connected" : "Reconnecting"}` : activeNode?.online === false ? "Host offline" : connection === "connected" ? "Workspace connected" : "Reconnecting"}
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

export default memo(BotRoster);
