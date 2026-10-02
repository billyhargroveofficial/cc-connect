import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api, errorMessage } from "../../lib/api";
import type { WorkspaceBinding } from "../../lib/api";
import type { MessageQueueSnapshot } from "../../lib/types";

const empty: MessageQueueSnapshot = { messages: [], paused: false };
export function workspaceBinding(scope: string): WorkspaceBinding {
  const separator = scope.lastIndexOf(":");
  return separator < 0 ? {} : { accountId: scope.slice(0, separator), nodeId: scope.slice(separator + 1) };
}

export function useMessageQueue(botId: string, draftScope: string, revision: number, offline: boolean, onError: (error: string) => void) {
  const identity = `${draftScope}:${botId}`;
  const binding = useMemo(() => workspaceBinding(draftScope), [draftScope]);
  const [state, setState] = useState<{ identity: string; snapshot: MessageQueueSnapshot }>({ identity, snapshot: empty });
  const [pending, setPending] = useState("");
  const [error, setError] = useState("");
  const [refresh, setRefresh] = useState(0);
  const lifecycle = useRef({ identity, active: true });
  const operation = useRef("");
  const mutationError = useRef("");
  lifecycle.current.identity = identity;
  const snapshot = state.identity === identity ? state.snapshot : empty;
  useEffect(() => {
    const current = lifecycle.current;
    current.active = true;
    operation.current = "";
    mutationError.current = "";
    setPending("");
    setError("");
    return () => { current.active = false; };
  }, [identity]);
  useEffect(() => {
    if (offline) return;
    const controller = new AbortController();
    const timer = setTimeout(async () => {
      try {
        const result = await api.queue(botId, controller.signal, binding);
        if (controller.signal.aborted) return;
        const next = { messages: Array.isArray(result.messages) ? result.messages.map(message => ({
          ...message, attachments: Array.isArray(message.attachments) ? message.attachments : [],
          skills: Array.isArray(message.skills) ? message.skills : [],
        })) : [], paused: result.paused === true };
        setState(previous => previous.identity === identity && JSON.stringify(previous.snapshot) === JSON.stringify(next) ? previous : { identity, snapshot: next });
        if (!mutationError.current) setError("");
      } catch (cause) {
        if (!controller.signal.aborted && !mutationError.current) setError(errorMessage(cause));
      }
    }, revision ? 40 : 0);
    return () => { controller.abort(); clearTimeout(timer); };
  }, [botId, identity, binding, revision, refresh, offline]);
  const refreshQueue = useCallback(() => setRefresh(value => value + 1), []);
  const mutate = useCallback(async (action: "steer" | "remove" | "resume", id = "") => {
    const current = () => lifecycle.current.active && lifecycle.current.identity === identity;
    if (offline || !current() || operation.current) return;
    operation.current = identity;
    mutationError.current = "";
    setPending(action === "resume" ? action : `${action}:${id}`);
    setError("");
    try {
      if (action === "steer") await api.steerQueued(botId, id, binding);
      else if (action === "remove") await api.removeQueued(botId, id, binding);
      else await api.resumeQueue(botId, binding);
      if (current()) refreshQueue();
    } catch (cause) {
      if (!current()) return;
      // Read the authoritative state after errors too. A rejected instruction
      // stays queued; an ambiguous accepted steer may be quarantined by the
      // server and pause the remaining queue instead of being safe to retry.
      const message = errorMessage(cause);
      mutationError.current = message;
      setError(message); onError(message);
      refreshQueue();
    } finally {
      if (operation.current === identity) operation.current = "";
      if (current()) setPending("");
    }
  }, [botId, identity, binding, offline, onError, refreshQueue]);
  const steer = useCallback((id: string) => mutate("steer", id), [mutate]);
  const remove = useCallback((id: string) => mutate("remove", id), [mutate]);
  const resume = useCallback(() => mutate("resume"), [mutate]);
  return { snapshot, pending, error, refresh: refreshQueue, steer, remove, resume, binding };
}
