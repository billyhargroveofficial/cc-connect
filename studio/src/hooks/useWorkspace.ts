import { useCallback, useEffect, useReducer, useRef, useState } from "react";
import { api, ApiError, errorMessage, isAccountChanged, onApiAccountChanged, setApiAccount } from "../lib/api";
import type { AccountCredentials, StudioSession, StudioUser } from "../lib/api";
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
      return { ...state, bots: mergeBots(state.bots, action.bots), loaded: true };
    case "update":
      return { ...state, bots: mergeBots(state.bots, [action.bot]) };
    case "archive":
      return {
        ...state,
        bots: state.bots.map((bot) => bot.id === action.id
          ? { ...bot, status: "archived", updatedAt: new Date().toISOString() }
          : bot),
      };
    case "capabilities":
      return { ...state, capabilities: action.capabilities };
    case "history":
      return {
        ...state,
        events: { ...state.events, [action.id]: mergeEvents(state.events[action.id] || [], action.events) },
      };
    case "event": {
      const event = action.event;
      const bot = eventBot(event);
      return {
        ...state,
        bots: bot ? mergeBots(state.bots, [bot]) : state.bots,
        events: { ...state.events, [event.botId]: mergeEvents(state.events[event.botId] || [], [event]) },
      };
    }
  }
}

export function useWorkspace() {
  const [state, dispatch] = useReducer(reducer, initial);
  const [phase, setPhase] = useState<"checking" | "login" | "ready">("checking");
  const [user, setUser] = useState<StudioUser | null>(null);
  const [registrationAllowed, setRegistrationAllowed] = useState(false);
  const [legacyClaimAvailable, setLegacyClaimAvailable] = useState(false);
  const [setupRequired, setSetupRequired] = useState(false);
  const [connection, setConnection] = useState<"connecting" | "connected" | "reconnecting">("connecting");
  const [sessionVerified, setSessionVerified] = useState(false);
  const [connectionVersion, setConnectionVersion] = useState(0);
  const [error, setError] = useState("");
  const [selectedId, setSelectedId] = useState("");
  const [revision, setRevision] = useState(0);
  const [accountVersion, setAccountVersion] = useState(0);
  const cursor = useRef(0);
  const generation = useRef(0);
  const verifiedGeneration = useRef(-1);
  const connectionEpoch = useRef(0);
  const userRef = useRef<StudioUser | null>(null);
  const stopStream = useRef<(() => void) | null>(null);

  // Invalidate all account-owned work before React paints the next account.
  const resetAccount = useCallback((nextUser: StudioUser | null) => {
    generation.current++;
    verifiedGeneration.current = -1;
    setConnectionVersion(++connectionEpoch.current);
    stopStream.current?.();
    stopStream.current = null;
    cursor.current = 0;
    userRef.current = nextUser;
    setApiAccount(nextUser?.id || null);
    dispatch({ type: "clear" });
    setUser(nextUser);
    setSelectedId("");
    setConnection("connecting");
    setSessionVerified(false);
    setAccountVersion((version) => version + 1);
    return generation.current;
  }, []);
  const applySession = useCallback((session: StudioSession, fresh = false) => {
    setRegistrationAllowed(session.registrationAllowed === true);
    setLegacyClaimAvailable(session.legacyClaimAvailable === true);
    setSetupRequired(session.setupRequired === true);
    const nextUser = session.authenticated && session.user?.id && session.user?.username
      ? session.user
      : null;
    if (fresh || nextUser?.id !== userRef.current?.id) resetAccount(nextUser);
    else {
      userRef.current = nextUser;
      setApiAccount(nextUser?.id || null);
      setUser(nextUser);
    }
    setPhase(nextUser ? "ready" : "login");
    setError(session.authenticated && !nextUser ? "The server returned an invalid session. Please sign in again." : "");
  }, [resetAccount]);
  const expired = useCallback(() => {
    resetAccount(null);
    setPhase("login");
    setError("Your session has expired. Sign in again.");
  }, [resetAccount]);

  useEffect(() => onApiAccountChanged((accountId) => {
    if (userRef.current?.id !== accountId) return;
    resetAccount(null);
    setPhase("checking");
    setError("");
    setRevision((value) => value + 1);
  }), [resetAccount]);

  useEffect(() => {
    let alive = true;
    const owner = generation.current;
    api.session().then((session) => {
      if (alive && owner === generation.current) applySession(session);
    }).catch((error) => {
      if (!alive || owner !== generation.current) return;
      resetAccount(null);
      setPhase("login");
      setError(errorMessage(error));
    });
    return () => { alive = false; };
  }, [revision, applySession, resetAccount]);

  useEffect(() => {
    if (phase !== "ready" || !user) return;
    let alive = true;
    let verified = false;
    let botsRequested = false;
    let sessionCheck: { epoch: number; promise: Promise<StudioSession> } | null = null;
    const owner = generation.current;
    const accountId = user.id;
    const pendingEvents: Event[] = [];
    const current = () => alive && owner === generation.current && userRef.current?.id === accountId;
    const sameConnection = (epoch: number) => current() && epoch === connectionEpoch.current;
    const receive = (event: Event) => {
      if (!current()) return;
      cursor.current = Math.max(cursor.current, event.seq);
      dispatch({ type: "event", event });
    };
    const loadBots = (epoch: number) => {
      if (botsRequested) return;
      botsRequested = true;
      api.bots().then((result) => {
        if (sameConnection(epoch) && verified)
          dispatch({ type: "bots", bots: result.bots || [] });
      }).catch((error) => {
        if (!sameConnection(epoch)) return;
        if (error instanceof ApiError && error.status === 401) expired();
        else setError(errorMessage(error));
      });
    };
    const source = new EventSource(`/api/studio/events?after=${cursor.current}&expectedAccount=${encodeURIComponent(accountId)}`);
    const checkSession = () => {
      const epoch = connectionEpoch.current;
      if (sessionCheck?.epoch === epoch) return sessionCheck.promise;
      const promise = api.session().then((session) => {
        if (!sameConnection(epoch)) return session;
        if (!session.authenticated || !session.user?.id || !session.user.username) expired();
        else if (session.user.id !== accountId) applySession(session);
        else {
          verified = true;
          verifiedGeneration.current = owner;
          setSessionVerified(true);
          for (const event of pendingEvents.splice(0)) receive(event);
          loadBots(epoch);
          setRegistrationAllowed(session.registrationAllowed === true);
          setConnection("connected");
          setError("");
        }
        return session;
      }).finally(() => { if (sessionCheck?.promise === promise) sessionCheck = null; });
      sessionCheck = { epoch, promise };
      return promise;
    };
    // The connection-aware check owns expiry. An older timer's result must
    // not expire a later, successfully verified connection.
    const sessionProbe = createSessionProbe({ check: checkSession, expired: () => {} });
    const invalidateConnection = () => {
      setConnectionVersion(++connectionEpoch.current);
      verified = false;
      verifiedGeneration.current = -1;
      setSessionVerified(false);
      botsRequested = false;
      pendingEvents.length = 0;
    };
    const verifyConnection = () => {
      if (!current()) return;
      sessionProbe.connected();
      invalidateConnection();
      const epoch = connectionEpoch.current;
      void checkSession().catch((error) => {
        if (!sameConnection(epoch)) return;
        setConnection("reconnecting");
        setError(errorMessage(error));
        sessionProbe.reconnect();
      });
    };
    source.onopen = verifyConnection;
    source.onerror = () => {
      if (!current()) return;
      invalidateConnection();
      setConnection("reconnecting");
      sessionProbe.reconnect();
    };
    source.addEventListener("event", (incoming: MessageEvent<string>) => {
      if (!current()) return;
      try {
        const event = JSON.parse(incoming.data) as Event;
        if (typeof event.seq === "number" && event.type) {
          if (verified) receive(event);
          else pendingEvents.push(event);
        }
      } catch {
        setError("Could not read the server event.");
      }
    });
    const stop = () => {
      alive = false;
      pendingEvents.length = 0;
      sessionProbe.dispose();
      source.close();
      if (stopStream.current === stop) stopStream.current = null;
    };
    stopStream.current = stop;
    verifyConnection();
    return stop;
  }, [phase, user?.id, revision, accountVersion, applySession, expired]);

  const selectedBot = state.bots.find((bot) => bot.id === selectedId);
  const catalogScope = selectedBot ? JSON.stringify([
    selectedBot.id, selectedBot.backend, selectedBot.model,
    selectedBot.threads?.[selectedBot.backend] || "",
  ]) : "";
  const hasBots = state.bots.some((bot) => bot.status !== "archived");
  useEffect(() => {
    if (phase !== "ready" || !sessionVerified || !state.loaded || (hasBots && !selectedBot)) return;
    const controller = new AbortController();
    let alive = true;
    const owner = generation.current;
    const connectionOwner = connectionEpoch.current;
    api.capabilities(selectedBot?.id, controller.signal).then((capabilities) => {
      if (alive && owner === generation.current && verifiedGeneration.current === owner && connectionOwner === connectionEpoch.current)
        dispatch({ type: "capabilities", capabilities });
    }).catch((error) => {
      if (!alive || owner !== generation.current || connectionOwner !== connectionEpoch.current) return;
      if (error instanceof ApiError && error.status === 401) expired();
      else setError(errorMessage(error));
    });
    return () => { alive = false; controller.abort(); };
  }, [phase, sessionVerified, connectionVersion, revision, accountVersion, state.loaded, hasBots, catalogScope, expired]);

  const authenticate = useCallback(async (credentials: AccountCredentials, register: boolean) => {
    const owner = resetAccount(null);
    setPhase("login");
    setError("");
    try {
      const session = await (register ? api.register(credentials) : api.login(credentials));
      if (owner === generation.current) applySession(session, true);
    } catch (error) {
      if (owner === generation.current) throw error;
    }
  }, [applySession, resetAccount]);
  const login = useCallback((credentials: AccountCredentials) => authenticate(credentials, false), [authenticate]);
  const register = useCallback((credentials: AccountCredentials) => authenticate(credentials, true), [authenticate]);
  const logout = useCallback(async () => {
    const expectedAccount = userRef.current?.id;
    const owner = resetAccount(null);
    setPhase("checking");
    setError("");
    try {
      await api.logout(expectedAccount);
      if (owner === generation.current) setPhase("login");
    } catch (error) {
      if (owner === generation.current) {
        if (isAccountChanged(error)) {
          setRevision((value) => value + 1);
          return;
        }
        setPhase("login");
        setError(errorMessage(error));
        throw error;
      }
    }
  }, [resetAccount]);

  // Callbacks held by an exiting chat or dialog also belong to one account.
  const owner = generation.current;
  const loadHistory = useCallback(async (id: string) => {
    if (owner !== generation.current || verifiedGeneration.current !== owner || !userRef.current) return;
    const connectionOwner = connectionEpoch.current;
    try {
      const { events } = await api.events(id);
      if (owner === generation.current && verifiedGeneration.current === owner && connectionOwner === connectionEpoch.current)
        dispatch({ type: "history", id, events: events || [] });
    } catch (error) {
      if (owner !== generation.current || connectionOwner !== connectionEpoch.current) return;
      if (error instanceof ApiError && error.status === 401) expired();
      else throw error;
    }
  }, [owner, expired]);
  const updateBot = useCallback((bot: Bot) => {
    if (owner === generation.current && userRef.current) dispatch({ type: "update", bot });
  }, [owner]);
  const archiveBot = useCallback((id: string) => {
    if (owner === generation.current && userRef.current) dispatch({ type: "archive", id });
  }, [owner]);
  const reportError = useCallback((message: string) => {
    if (owner === generation.current) setError(message);
  }, [owner]);
  const selectCatalogBot = useCallback((id: string) => {
    if (owner === generation.current && userRef.current) setSelectedId(id);
  }, [owner]);
  const retry = useCallback(() => {
    resetAccount(null);
    setError("");
    setPhase("checking");
    setRevision((value) => value + 1);
  }, [resetAccount]);
  return {
    ...state,
    allBots: state.bots,
    bots: state.bots.filter((bot) => bot.status !== "archived"),
    user, registrationAllowed, legacyClaimAvailable, setupRequired, accountVersion,
    phase, connection, error, setError: reportError,
    login, register, logout, loadHistory, updateBot, archiveBot, selectCatalogBot, retry,
  };
}
