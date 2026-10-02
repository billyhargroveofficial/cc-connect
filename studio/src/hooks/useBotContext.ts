import { useCallback, useMemo, useEffect, useRef, useState } from "react";
import type { Bot, BotContext, Event } from "../lib/types";
import { api } from "../lib/api";
import { contextCompacting, contextEventRevision } from "../lib/contextState";

export function useBotContext(bot: Bot, events: Event[]) {
  const [snapshot, setSnapshot] = useState<{ identity: string; context: BotContext; cursor: number } | null>(null);
  const [requesting, setRequesting] = useState(false);
  const [refresh, setRefresh] = useState(0);
  const [error, setError] = useState("");
  const thread = bot.threads?.[bot.backend] || "";
  const identity = `${bot.id}:${bot.backend}:${bot.model}:${thread}`;
  const eventsRef = useRef(events);
  eventsRef.current = events;
  const current = snapshot?.identity === identity ? snapshot : null;
  const revision = contextEventRevision(events, bot.backend, thread);
  const compacting = contextCompacting(current?.context.compacting ?? false, events, bot.backend, thread, current?.cursor ?? events.at(-1)?.seq ?? 0);

  useEffect(() => {
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    setError("");
    async function load() {
      // Capture a baseline before the request; lifecycle events arriving while
      // it is in flight remain authoritative over this response.
      const cursor = eventsRef.current.at(-1)?.seq ?? 0;
      try {
        const context = await api.context(bot.id, controller.signal);
        if (controller.signal.aborted) return;
        setSnapshot({ identity, context, cursor });
        if (context.compacting) timer = setTimeout(() => void load(), 1500);
      } catch (cause) {
        if (controller.signal.aborted) return;
        setError(cause instanceof Error ? cause.message : "Could not read context.");
        if (compacting) timer = setTimeout(() => void load(), 3000);
      }
    }
    timer = setTimeout(() => void load(), revision ? 250 : 0);
    return () => { controller.abort(); clearTimeout(timer); };
  }, [bot.id, identity, revision, refresh]);

  const compact = useCallback(async () => {
    setRequesting(true);
    try {
      await api.compact(bot.id);
      setRefresh(value => value + 1);
    } finally {
      setRequesting(false);
    }
  }, [bot.id]);
  return useMemo(() => ({ context: current?.context ?? null, compacting, requesting, error, compact }),
    [current?.context, compacting, requesting, error, compact]);
}
