import { useEffect, useLayoutEffect, useRef, useState } from "react";
import {
  ArrowLeft,
  ChevronDown,
  Ellipsis,
  Send,
  Target,
  LoaderCircle,
  ArrowDown,
} from "lucide-react";
import type { Attachment, Bot, Capabilities, Event } from "../../lib/types";
import { api, errorMessage } from "../../lib/api";
import { isWorking, statusLabel, telegramTitle } from "../../lib/events";
import Avatar from "../../components/Avatar";
import Composer from "./Composer";
import { GoalDialog, useGoal } from "./GoalPanel";
import Transcript from "../transcript/Transcript";
import { useBotContext } from "../../hooks/useBotContext";
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
      <header className="chat-header">
        <button
          className="icon-button mobile-back"
          onClick={onBack}
          aria-label="К списку ботов"
        >
          <ArrowLeft size={21} />
        </button>
        <button className="chat-profile" onClick={onSettings}>
          <Avatar bot={bot} size={36} />
          <span>
            <strong>
              {bot.name}
              <ChevronDown size={13} />
            </strong>
            <small className={working ? "is-working" : ""}>
              {status}
            </small>
          </span>
        </button>
        <div className="chat-header-actions">
          {bot.telegram?.enabled && (
            <span
              className={`header-telegram telegram-status-${bot.telegram.status || "configured"}`}
              title={telegramTitle(bot.telegram)}
            >
              <Send size={14} />
              <span>Telegram</span>
            </span>
          )}
          {supportsGoal && (
            <button
              className={`icon-button ${goal ? "has-goal" : ""}`}
              onClick={() => setGoalOpen(true)}
              title="Цель"
              aria-label="Цель бота"
            >
              <Target size={19} />
            </button>
          )}
          <button
            className="icon-button"
            onClick={onSettings}
            title="Настройки бота"
            aria-label="Настройки бота"
          >
            <Ellipsis size={20} />
          </button>
        </div>
      </header>
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
