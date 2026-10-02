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
import { PresenceSurface } from "./ModelPicker";
import {
  AnimatePresence, m, useIsPresent, useReducedMotion,
  controlMotion, fade, fadeUp, popoverMotion, motionTransition,
} from "../../lib/motion";

function LatestButton({ onClick }: { onClick: () => void }) {
  const present = useIsPresent();
  const reducedMotion = useReducedMotion();
  return <m.button
    {...controlMotion}
    className="scroll-latest"
    style={{ x: "-50%", pointerEvents: present ? "auto" : "none" }}
    variants={reducedMotion ? fade : popoverMotion}
    initial="hidden" animate="visible" exit="exit"
    inert={!present}
    aria-hidden={!present || undefined}
    onClick={onClick}
  >
    <ArrowDown size={14} />Jump to latest
  </m.button>;
}
function turnStats(events: Event[]) {
  for (let i = events.length - 1; i >= 0; i--)
    if (events[i].type === "turn") return events[i].data;
  return {};
}
export default function ChatRoom({
  bot,
  draftScope,
  events,
  capabilities,
  loading,
  suspended,
  onBack,
  onBotChange,
  onArchive,
  onError,
  onHistory,
}: {
  bot: Bot;
  draftScope: string;
  events: Event[];
  capabilities: Capabilities | null;
  loading: boolean;
  suspended: boolean;
  onBack: () => void;
  onBotChange: (bot: Bot) => void;
  onArchive: (id: string) => void;
  onError: (error: string) => void;
  onHistory?: () => void;
}) {
  const scroll = useRef<HTMLDivElement>(null);
  const content = useRef<HTMLDivElement>(null);
  const reducedMotion = useReducedMotion();
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
    if (!suspended) return;
    setGoalOpen(false);
    setDetailsOpen(false);
  }, [suspended]);
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
  useLayoutEffect(() => {
    if (typeof ResizeObserver === "undefined" || !content.current || !scroll.current) return;
    const observer = new ResizeObserver(() => {
      const container = scroll.current;
      // Keep streaming and disclosure animations pinned only while the reader
      // is already following the latest message.
      if (container && nearBottom.current) container.scrollTop = container.scrollHeight;
    });
    observer.observe(content.current);
    observer.observe(scroll.current);
    return () => observer.disconnect();
  }, []);
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
        <m.button
          {...controlMotion}
          className="icon-button chat-corner-control chat-corner-back"
          onClick={onBack}
          aria-label="Back to bots"
        >
          <ArrowLeft size={21} />
        </m.button>
        <m.button
          {...controlMotion}
          ref={detailsTrigger}
          className="icon-button chat-corner-control chat-corner-details"
          onClick={() => setDetailsOpen(true)}
          aria-label="Bot details"
          aria-expanded={detailsOpen}
          aria-haspopup="dialog"
        >
          <PanelRight size={20} />
        </m.button>
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
        <div className="chat-content" ref={content} style={{ height: events.length ? undefined : "100%" }}>
        <AnimatePresence initial={false} mode="wait">
        {loading && !events.length ? (
          <m.div key="loading" className="chat-loading" variants={fade} initial="hidden" animate="visible" exit="exit">
            <LoaderCircle size={23} className="spin" />
            <span>Opening conversation…</span>
          </m.div>
        ) : !events.length ? (
          <m.div
            key="empty"
            className="chat-empty"
            variants={reducedMotion ? fade : fadeUp}
            initial="hidden" animate="visible" exit="exit"
          >
            <m.div variants={reducedMotion ? fade : fadeUp}>
              <Avatar bot={bot} size={76} />
            </m.div>
            <m.span className="eyebrow" variants={fade}>
              {bot.chief ? "YOUR COORDINATOR" : "YOUR PERSISTENT ASSISTANT"}
            </m.span>
            <m.h1 variants={reducedMotion ? fade : fadeUp}>{bot.name} is here.</m.h1>
            <m.p variants={fade}>
              {bot.role ||
                "Give your first task. Context and results stay in this conversation."}
            </m.p>
          </m.div>
        ) : (
          <m.div key="conversation" className="conversation" variants={fade} initial="hidden" animate="visible" exit="exit">
            <Transcript
              bot={bot}
              events={events}
              onPermission={permission}
              onQuestion={permission}
              onRetry={onHistory ? () => onHistory() : undefined}
            />
          </m.div>
        )}
        </AnimatePresence>
        <AnimatePresence initial={false}>
        {pending && (
          <PresenceSurface
            key="pending"
            className="accepted-message" aria-live="polite"
            initial={{ opacity: 0, height: 0, paddingTop: 0, paddingBottom: 0 }}
            animate={{ opacity: 1, height: "auto", paddingTop: 4, paddingBottom: 14 }}
            exit={{ opacity: 0, height: 0, paddingTop: 0, paddingBottom: 0 }}
            transition={reducedMotion ? { duration: 0 } : motionTransition.disclosure}
            style={{ overflow: "hidden" }}
          >
            <LoaderCircle size={14} className="spin" />
            Starting work…
          </PresenceSurface>
        )}
        </AnimatePresence>
        </div>
      </div>
      <div className="chat-input-area">
      <AnimatePresence initial={false}>
      {showScroll && (
        <LatestButton
          key="latest"
          onClick={() => {
            nearBottom.current = true;
            scroll.current?.scrollTo({
              top: scroll.current.scrollHeight,
              behavior: reducedMotion ? "auto" : "smooth",
            });
            setShowScroll(false);
          }}
        />
      )}
      </AnimatePresence>
      <Composer
        key={`${draftScope}:${bot.id}`}
        draftScope={draftScope}
        suspended={suspended}
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
        suspended={suspended}
        onOpen={() => setDetailsOpen(true)}
        onClose={() => setDetailsOpen(false)}
        triggerRef={detailsTrigger}
        capabilities={capabilities}
        onBotChange={onBotChange}
        onArchive={onArchive}
        onGoal={() => {
          setDetailsOpen(false);
          setGoalOpen(true);
        }}
      />
      <AnimatePresence>
      {goalOpen && !suspended && (
        <GoalDialog
          key="goal"
          bot={bot}
          goal={goal}
          onChange={setGoal}
          onClose={() => setGoalOpen(false)}
          onError={onError}
        />
      )}
      </AnimatePresence>
    </section>
  );
}
