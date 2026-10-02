import { useEffect, useLayoutEffect, useRef, useState } from "react";
import {
  ArrowLeft,
  PanelRight,
  LoaderCircle,
  ArrowDown,
} from "lucide-react";
import type { Attachment, Bot, Capabilities, Event } from "../../lib/types";
import { api, errorMessage } from "../../lib/api";
import { isWorking, statusLabel } from "../../lib/events";
import Avatar from "../../components/Avatar";
import Composer from "./Composer";
import { GoalDialog, useGoal } from "./GoalPanel";
import Transcript from "../transcript/Transcript";
import { useBotContext } from "../../hooks/useBotContext";
import BotIsland from "./BotIsland";
function turnStats(events: Event[]) {
  for (let i = events.length - 1; i >= 0; i--)
    if (events[i].type === "turn") return events[i].data;
  return {};
}
export default function ChatRoom({
  bot,
  events,
  capabilities,
  loading,
  onBack,
  onSettings,
  onBotChange,
  onError,
  onHistory,
}: {
  bot: Bot;
  events: Event[];
  capabilities: Capabilities | null;
  loading: boolean;
  onBack: () => void;
  onSettings: () => void;
  onBotChange: (bot: Bot) => void;
  onError: (error: string) => void;
  onHistory?: () => void;
}) {
  const scroll = useRef<HTMLDivElement>(null);
  const nearBottom = useRef(true);
  const [showScroll, setShowScroll] = useState(false);
  const [goalOpen, setGoalOpen] = useState(false);
  const [detailsOpen, setDetailsOpen] = useState(false);
  const detailsTrigger = useRef<HTMLButtonElement>(null);
  const [pending, setPending] = useState("");
  const supportsGoal = !!capabilities?.backends[bot.backend]?.goals;
  const { goal, setGoal } = useGoal(bot, events, supportsGoal);
  const context = useBotContext(bot, events);
  const stats = turnStats(events);
  const working =
    isWorking(bot.status) || isWorking(String(stats.status || "")) || !!pending;
  useEffect(() => {
    if (
      pending &&
      events.some((event) => event.turnId === pending && event.type === "turn")
    )
      setPending("");
  }, [events, pending]);
  useLayoutEffect(() => {
    if (nearBottom.current && scroll.current)
      scroll.current.scrollTop = scroll.current.scrollHeight;
    else setShowScroll(true);
  }, [events.length, events.at(-1)?.seq]);
  async function send(text: string, attachments: Attachment[]) {
    const response = await api.send(bot.id, text, attachments);
    setPending(response.turnId);
    nearBottom.current = true;
  }
  async function permission(
    requestId: string,
    behavior: string,
    updatedInput?: Record<string, unknown>,
    message?: string,
  ) {
    try {
      await api.permission(bot.id, requestId, behavior, updatedInput, message);
    } catch (error) {
      onError(errorMessage(error));
      throw error;
    }
  }
  const status = working
    ? statusLabel(bot.status === "idle" ? "working" : bot.status)
    : statusLabel(bot.status);
  return (
    <section className="chat-room">
      <div className="chat-dialog">
        <button
          className="icon-button chat-corner-control chat-corner-back"
          onClick={onBack}
          aria-label="К списку ботов"
        >
          <ArrowLeft size={21} />
        </button>
        <button
          ref={detailsTrigger}
          className="icon-button chat-corner-control chat-corner-details"
          onClick={() => setDetailsOpen(true)}
          aria-label="Сведения о боте"
          aria-expanded={detailsOpen}
          aria-haspopup="dialog"
        >
          <PanelRight size={20} />
        </button>
      <div
        className="chat-scroll"
        ref={scroll}
        onScroll={() => {
          const container = scroll.current;
          if (container) {
            nearBottom.current =
              container.scrollHeight -
                container.scrollTop -
                container.clientHeight <
              100;
            setShowScroll(!nearBottom.current);
          }
        }}
      >
        {loading && !events.length ? (
          <div className="chat-loading">
            <LoaderCircle size={23} className="spin" />
            <span>Открываем разговор…</span>
          </div>
        ) : !events.length ? (
          <div className="chat-empty">
            <Avatar bot={bot} size={76} />
            <span className="eyebrow">
              {bot.chief ? "ВАШ КООРДИНАТОР" : "ПОСТОЯННЫЙ ПОМОЩНИК"}
            </span>
            <h1>На связи, {bot.name}.</h1>
            <p>
              {bot.role ||
                "Дайте первое поручение. Контекст и результаты останутся в этом разговоре."}
            </p>
          </div>
        ) : (
          <div className="conversation">
            <Transcript
              bot={bot}
              events={events}
              onPermission={permission}
              onQuestion={permission}
              onRetry={onHistory ? () => onHistory() : undefined}
            />
          </div>
        )}
        {pending && (
          <div className="accepted-message" aria-live="polite">
            <LoaderCircle size={14} className="spin" />
            Начинает работу…
          </div>
        )}
      </div>
      <div className="chat-input-area">
      {showScroll && (
        <button
          className="scroll-latest"
          onClick={() => {
            nearBottom.current = true;
            scroll.current?.scrollTo({
              top: scroll.current.scrollHeight,
              behavior: "smooth",
            });
            setShowScroll(false);
          }}
        >
          <ArrowDown size={14} />К новым событиям
        </button>
      )}
      <Composer
        key={bot.id}
        bot={bot}
        capabilities={capabilities}
        busy={working}
        context={context}
        onSend={send}
        onStop={async () => {
          await api.stop(bot.id);
          setPending("");
        }}
        onBotChange={onBotChange}
        onError={onError}
      />
      </div>
      </div>
      <BotIsland
        bot={bot}
        events={events}
        status={status}
        working={working}
        supportsGoal={supportsGoal}
        hasGoal={!!goal}
        open={detailsOpen}
        onClose={() => setDetailsOpen(false)}
        triggerRef={detailsTrigger}
        onGoal={() => {
          setDetailsOpen(false);
          setGoalOpen(true);
        }}
        onSettings={() => {
          setDetailsOpen(false);
          onSettings();
        }}
      />
      {goalOpen && (
        <GoalDialog
          bot={bot}
          goal={goal}
          onChange={setGoal}
          onClose={() => setGoalOpen(false)}
          onError={onError}
        />
      )}
    </section>
  );
}
