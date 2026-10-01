import { useCallback, useEffect, useReducer, useRef, useState } from "react";
import { api, ApiError, errorMessage } from "../lib/api";
import type { Bot, Capabilities, Event } from "../lib/types";
import { eventBot, mergeEvents, mergeBots } from "../lib/events";
import { createSessionProbe } from "../lib/sessionProbe";
interface WorkspaceState {
  bots: Bot[];
  events: Record<string, Event[]>;
  capabilities: Capabilities | null;
  loaded: boolean;
}
type Action =
  | { type: "bots"; bots: Bot[] }
  | { type: "event"; event: Event }
  | { type: "history"; id: string; events: Event[] }
  | { type: "capabilities"; capabilities: Capabilities }
  | { type: "update"; bot: Bot }
  | { type: "archive"; id: string }
  | { type: "clear" };
const initial: WorkspaceState = {
  bots: [],
  events: {},
  capabilities: null,
  loaded: false,
};
function reducer(state: WorkspaceState, action: Action): WorkspaceState {
  switch (action.type) {
    case "clear":
      return initial;
    case "bots":
      return {
        ...state,
        bots: mergeBots(state.bots, action.bots),
        loaded: true,
      };
    case "update": {
      return { ...state, bots: mergeBots(state.bots, [action.bot]) };
    }
    case "archive":
      return {
        ...state,
        bots: state.bots.map((bot) =>
          bot.id === action.id
            ? {
                ...bot,
                status: "archived",
                updatedAt: new Date().toISOString(),
              }
            : bot,
        ),
      };
    case "capabilities":
      return { ...state, capabilities: action.capabilities };
    case "history":
      return {
        ...state,
        events: {
          ...state.events,
          [action.id]: mergeEvents(
            state.events[action.id] || [],
            action.events,
          ),
        },
      };
    case "event": {
      const event = action.event;
      const bot = eventBot(event);
      let bots = state.bots;
      if (bot) bots = mergeBots(bots, [bot]);
      return {
        ...state,
        bots,
        events: {
          ...state.events,
          [event.botId]: mergeEvents(state.events[event.botId] || [], [event]),
        },
      };
    }
  }
}
export function useWorkspace(selectedId = "") {
  const [state, dispatch] = useReducer(reducer, initial);
  const [phase, setPhase] = useState<"checking" | "login" | "ready">(
    "checking",
  );
  const [connection, setConnection] = useState<
    "connecting" | "connected" | "reconnecting"
  >("connecting");
  const [error, setError] = useState("");
  const cursor = useRef(0);
  const [revision, setRevision] = useState(0);
  useEffect(() => {
    let alive = true;
    api
      .session()
      .then((session) => {
        if (alive) {
          setPhase(session.authenticated ? "ready" : "login");
          setError("");
        }
      })
      .catch((error) => {
        if (alive) {
          setPhase("login");
          setError(errorMessage(error));
        }
      });
    return () => {
      alive = false;
    };
  }, [revision]);
  useEffect(() => {
    if (phase !== "ready") return;
    let alive = true;
    api.bots().then((result) => {
      if (alive) dispatch({ type: "bots", bots: result.bots || [] });
    }).catch((error) => {
      if (!alive) return;
      setError(errorMessage(error));
      if (error instanceof ApiError && error.status === 401) setPhase("login");
    });
    const source = new EventSource(
      `/api/studio/events?after=${cursor.current}`,
    );
    const sessionProbe = createSessionProbe({
      check: api.session,
      expired: () => {
        if (alive) {
          setError("Сессия завершилась. Войдите снова — черновики сохранены.");
          setPhase("login");
        }
      },
    });
    source.onopen = () => {
      sessionProbe.connected();
      setConnection("connected");
      setError("");
    };
    source.onerror = () => {
      setConnection("reconnecting");
      sessionProbe.reconnect();
    };
    source.addEventListener("event", (incoming: MessageEvent<string>) => {
      try {
        const event = JSON.parse(incoming.data) as Event;
        if (typeof event.seq === "number" && event.type) {
          cursor.current = Math.max(cursor.current, event.seq);
          dispatch({ type: "event", event });
        }
      } catch {
        setError("Не удалось прочитать событие сервера.");
      }
    });
    return () => {
      alive = false;
      sessionProbe.dispose();
      source.close();
    };
  }, [phase, revision]);
  const selectedBot = state.bots.find((bot) => bot.id === selectedId);
  const catalogScope = selectedBot
    ? JSON.stringify([
        selectedBot.id,
        selectedBot.backend,
        selectedBot.model,
        selectedBot.threads?.[selectedBot.backend] || "",
      ])
    : "";
  const hasBots = state.bots.some((bot) => bot.status !== "archived");
  useEffect(() => {
    if (phase !== "ready" || !state.loaded || (hasBots && !selectedBot)) return;
    const controller = new AbortController();
    let alive = true;
    api.capabilities(selectedBot?.id, controller.signal).then((capabilities) => {
      if (alive) dispatch({ type: "capabilities", capabilities });
    }).catch((error) => {
      if (!alive) return;
      setError(errorMessage(error));
      if (error instanceof ApiError && error.status === 401) setPhase("login");
    });
    return () => {
      alive = false;
      controller.abort();
    };
    // A Pi session reports reasoning levels for its selected model. Refresh
    // that real catalog after switching models or replacing a bot's session.
  }, [phase, revision, state.loaded, hasBots, catalogScope]);
  const login = useCallback(async (token: string) => {
    await api.login(token);
    setError("");
    setPhase("ready");
  }, []);
  const logout = useCallback(async () => {
    await api.logout();
    cursor.current = 0;
    dispatch({ type: "clear" });
    setPhase("login");
  }, []);
  const loadHistory = useCallback(async (id: string) => {
    const { events } = await api.events(id);
    dispatch({ type: "history", id, events: events || [] });
  }, []);
  const updateBot = useCallback(
    (bot: Bot) => dispatch({ type: "update", bot }),
    [],
  );
  const archiveBot = useCallback(
    (id: string) => dispatch({ type: "archive", id }),
    [],
  );
  const retry = useCallback(() => {
    setRevision((r) => r + 1);
    setPhase("checking");
  }, []);
  return {
    ...state,
    allBots: state.bots,
    bots: state.bots.filter((bot) => bot.status !== "archived"),
    phase,
    connection,
    error,
    setError,
    login,
    logout,
    loadHistory,
    updateBot,
    archiveBot,
    retry,
  };
}
