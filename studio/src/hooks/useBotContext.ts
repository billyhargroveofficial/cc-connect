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
  const supportsCompaction = bot.backend === "codex" || bot.backend === "pi";
  const eventsRef = useRef(events);
  eventsRef.current = events;
  const snapshotRevision = useRef(0);
  const current = snapshot?.identity === identity ? snapshot : null;
  const revision = contextEventRevision(events, bot.backend, thread);
  const compacting = contextCompacting(current?.context.compacting ?? false, events, bot.backend, thread, current?.cursor ?? events.at(-1)?.seq ?? 0);
  const operation = useRef({ identity, requesting: false, compacting });
  if (operation.current.identity !== identity)
    operation.current = { identity, requesting: false, compacting };
  operation.current.compacting = compacting;
  const mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  useEffect(() => { setRequesting(false); }, [identity]);

  useEffect(() => {
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;
    setError("");
    async function load() {
      // Capture a baseline before the request; lifecycle events arriving while
      // it is in flight remain authoritative over this response.
      const cursor = eventsRef.current.at(-1)?.seq ?? 0;
      const revision = snapshotRevision.current;
      try {
        const context = await api.context(bot.id, controller.signal);
        if (controller.signal.aborted || revision !== snapshotRevision.current) return;
        setSnapshot({ identity, context, cursor });
        if (context.compacting) timer = setTimeout(() => void load(), 1500);
      } catch (cause) {
        if (controller.signal.aborted || revision !== snapshotRevision.current) return;
        setError(cause instanceof Error ? cause.message : "Could not read context.");
        if (compacting) timer = setTimeout(() => void load(), 3000);
      }
    }
    timer = setTimeout(() => void load(), revision ? 250 : 0);
    return () => { controller.abort(); clearTimeout(timer); };
  }, [bot.id, identity, revision, refresh]);

  const compact = useCallback(async () => {
    const pending = operation.current;
    if (!mounted.current || pending.identity !== identity || !supportsCompaction || !current || pending.requesting || pending.compacting) return;
    // Repeated clicks before a React commit cannot submit another operation.
    pending.requesting = true;
    const cursor = eventsRef.current.at(-1)?.seq ?? 0;
    setRequesting(true);
    try {
      await api.compact(bot.id);
      if (!mounted.current || operation.current !== pending) return;
      snapshotRevision.current++;
      pending.compacting = true;
      // An accepted receipt owns the native gate before its SSE event or
      // refreshed snapshot arrives. Keep progress visible across that gap.
      setSnapshot(value => value?.identity === identity
        ? { identity, context: { ...value.context, compacting: true }, cursor }
        : value);
      setRefresh(value => value + 1);
    } finally {
      pending.requesting = false;
      if (mounted.current && operation.current === pending) setRequesting(false);
    }
  }, [bot.id, identity, supportsCompaction, !!current]);
  return useMemo(() => ({ context: current?.context ?? null, compacting, requesting, error, compact, supportsCompaction }),
    [current?.context, compacting, requesting, error, compact, supportsCompaction]);
}
