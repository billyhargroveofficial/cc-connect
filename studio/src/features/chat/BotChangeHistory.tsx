import { memo, useMemo, useState } from "react";
import { ChevronDown, History } from "lucide-react";
import { botHistoryEntry } from "../../lib/botHistory";
import type { Event } from "../../lib/types";
import { m, controlMotion, motionTransition } from "../../lib/motion";

const pageSize = 10;
const dateFormat = new Intl.DateTimeFormat("en", {
  month: "short", day: "numeric", hour: "2-digit", minute: "2-digit", hour12: false,
});

function eventTime(time: string) {
  const timestamp = Date.parse(time);
  return Number.isFinite(timestamp) ? dateFormat.format(timestamp) : "";
}

function BotChangeHistory({ botId, events }: { botId: string; events: Event[] }) {
  const [open, setOpen] = useState(false);
  const [limit, setLimit] = useState(pageSize);
  // This input is a journal-owned lifecycle slice, never the token stream.
  // The collapsed view and paging therefore stay idle during inference.
  const entries = useMemo(() => events.flatMap(event => {
    const entry = botHistoryEntry(event);
    return entry ? [{ ...entry, seq: event.seq, time: event.time, label: eventTime(event.time) }] : [];
  }).reverse(), [events]);
  const visible = useMemo(() => entries.slice(0, limit), [entries, limit]);
  if (!entries.length) return null;
  const contentId = `bot-change-history-${botId}`;

  return <section className="bot-island-section bot-change-history" aria-label="Bot history">
    <m.button {...controlMotion} type="button" className="bot-history-toggle"
      aria-expanded={open} aria-controls={contentId} onClick={() => setOpen(value => !value)}>
      <History size={13} aria-hidden="true" /><span>History</span>
      <small>{entries.length}</small>
      <m.span animate={{ rotate: open ? 180 : 0 }} transition={motionTransition.quick} aria-hidden="true">
        <ChevronDown size={13} />
      </m.span>
    </m.button>
    <m.div className={`bot-history-disclosure${open ? " is-open" : ""}`} id={contentId}
      initial={false} animate={{ gridTemplateRows: open ? "1fr" : "0fr", opacity: open ? 1 : 0 }}
      transition={motionTransition.disclosure} aria-hidden={!open} inert={!open}>
      <div className="bot-history-disclosure-inner">
        <ol className="bot-history-list">
          {visible.map(entry => <li key={entry.seq} className="bot-history-entry">
            <strong>{entry.title}</strong>
            {entry.label && <time dateTime={entry.time}>{entry.label}</time>}
            <p>{entry.content}</p>
          </li>)}
        </ol>
        {entries.length > visible.length && <m.button {...controlMotion} type="button" className="bot-history-more"
          onClick={() => setLimit(value => value + pageSize)}>Show older</m.button>}
      </div>
    </m.div>
  </section>;
}

export default memo(BotChangeHistory);
