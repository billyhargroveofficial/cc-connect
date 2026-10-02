import { lazy, memo, Suspense, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import {
  ArrowLeft,
  PanelRight,
  LoaderCircle,
  ArrowDown,
} from "lucide-react";
import type { Attachment, Bot, Capabilities, Event, NodeInfo } from "../../lib/types";
import { api, errorMessage } from "../../lib/api";
import { isWorking, statusLabel } from "../../lib/events";
import Avatar from "../../components/Avatar";
import Composer from "./Composer";
import { GoalDialog, useGoal } from "./GoalPanel";
import { useBotContext } from "../../hooks/useBotContext";
import BotIsland from "./BotIsland";
import { createChatStatusProjector } from "../../lib/chatStatus";
import { QueuePanel, SessionStatus, WorkingStrip } from "./LiveStatus";
import { useMessageQueue } from "./useMessageQueue";
import {
  AnimatePresence, m, useIsPresent, useReducedMotion,
  controlMotion, fade, fadeUp, popoverMotion,
} from "../../lib/motion";
const Transcript = lazy(() => import("../transcript/Transcript"));

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
function ChatRoom({
  bot,
  node,
  offline,
  draftScope,
  events,
  messages,
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
  node?: NodeInfo;
  offline?: boolean;
  draftScope: string;
  events: Event[];
  messages: Event[];
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
  const pendingAt = useRef(0);
  const activeScope = useRef(draftScope);
  activeScope.current = draftScope;
  const statusProjector = useMemo(createChatStatusProjector, []);
  const supportsGoal = !!capabilities?.backends[bot.backend]?.goals;
  const { goal, setGoal } = useGoal(bot, events, supportsGoal);
  const context = useBotContext(bot, events);
  const stats = statusProjector(events);
  const working =
    isWorking(bot.status) || isWorking(stats.status) || !!pending;
  const runtimeStatus = useMemo(() => pending && stats.turnId !== pending
    ? { ...stats, turnId: pending, turn: stats.turn + 1, step: 0, status: "running", startedAt: pendingAt.current, endedAt: null, tokensPerSecond: null }
    : stats, [pending, stats]);
  const queue = useMessageQueue(bot.id, draftScope, stats.queueRevision, !!offline, onError);
  const selectedModel = capabilities?.models.find(model => model.backend === bot.backend && model.id === bot.model);
  const statusModel = working && stats.model ? capabilities?.models.find(model => model.id === stats.model)?.name || stats.model
    : selectedModel?.name || bot.model || bot.backend;
  const statusEffort = working && stats.effort ? stats.effort : bot.effort;
  const serviceTier = selectedModel?.serviceTiers?.find(tier => tier.id === bot.serviceTier)?.name || bot.serviceTier || "";
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
  // ResizeObserver follows actual height changes. Token events that do not
  // change layout must not force a synchronous scrollHeight measurement.
  }, [bot.id, loading, events.length > 0,
    typeof ResizeObserver === "undefined" ? events.at(-1)?.seq : 0]);
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
  const send = useCallback(async (text: string, attachments: Attachment[]) => {
    const started = Date.now();
    const response = await api.send(bot.id, text, attachments, working ? "queue" : undefined, queue.binding);
    if (activeScope.current !== draftScope) return;
    if (response.status === "queued") queue.refresh();
    else if (response.status !== "steered") {
      pendingAt.current = started;
      setPending(response.turnId);
      nearBottom.current = true;
    }
  }, [bot.id, draftScope, working, queue.binding, queue.refresh]);
  const permission = useCallback(async (
    requestId: string,
    behavior: string,
    updatedInput?: Record<string, unknown>,
    message?: string,
  ) => {
    try {
      await api.permission(bot.id, requestId, behavior, updatedInput, message);
    } catch (error) {
      onError(errorMessage(error));
      throw error;
    }
  }, [bot.id, onError]);
  const stop = useCallback(async () => { await api.stop(bot.id); setPending(""); }, [bot.id]);
  const openDetails = useCallback(() => setDetailsOpen(true), []);
  const closeDetails = useCallback(() => setDetailsOpen(false), []);
  const openGoal = useCallback(() => { setDetailsOpen(false); setGoalOpen(true); }, []);
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
        <AnimatePresence presenceAffectsLayout={false} initial={false} mode="wait">
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
            <Suspense fallback={<div className="chat-loading"><LoaderCircle size={19} className="spin" /><span>Rendering conversation…</span></div>}>
              <Transcript
                bot={bot}
                events={events}
                onPermission={permission}
                onQuestion={permission}
                onRetry={onHistory}
              />
            </Suspense>
          </m.div>
        )}
        </AnimatePresence>
        </div>
      </div>
      <div className="chat-input-area">
      <AnimatePresence presenceAffectsLayout={false} initial={false}>
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
      <div className="chat-input-stack">
      <QueuePanel snapshot={queue.snapshot} pending={queue.pending} busy={working} offline={offline} suspended={suspended}
        error={queue.error} onSteer={queue.steer} onRemove={queue.remove} onResume={queue.resume} />
      <WorkingStrip working={working} compacting={context.compacting || context.requesting} status={runtimeStatus}
        offline={offline} suspended={suspended} onStop={stop} onError={onError} />
      <Composer
        key={`${draftScope}:${bot.id}`}
        draftScope={draftScope}
        suspended={suspended}
        offline={offline}
        bot={bot}
        capabilities={capabilities}
        busy={working}
        context={context}
        onSend={send}
        onBotChange={onBotChange}
        onError={onError}
      />
      <SessionStatus status={runtimeStatus} working={working} suspended={suspended} offline={offline} context={context.context}
        model={statusModel} effort={statusEffort} tier={serviceTier} />
      </div>
      </div>
      </div>
      <BotIsland
        bot={bot}
        events={messages}
        status={status}
        working={working}
        supportsGoal={supportsGoal}
        hasGoal={!!goal}
        open={detailsOpen}
        suspended={suspended}
        onOpen={openDetails}
        onClose={closeDetails}
        triggerRef={detailsTrigger}
        capabilities={capabilities}
        onBotChange={onBotChange}
        onArchive={onArchive}
        node={node}
        onGoal={openGoal}
      />
      <AnimatePresence presenceAffectsLayout={false}>
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

export default memo(ChatRoom);
