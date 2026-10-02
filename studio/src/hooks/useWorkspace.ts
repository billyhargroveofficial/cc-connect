import { useCallback, useEffect, useMemo, useReducer, useRef, useState } from "react";
import { api, ApiError, errorMessage, isAccountChanged, onApiAccountChanged, setApiAccount, setApiNode } from "../lib/api";
import type { AccountCredentials, StudioSession, StudioUser } from "../lib/api";
import type { Bot, Capabilities, Event, NodeInfo } from "../lib/types";
import { eventBot, mergeBots } from "../lib/events";
import { createSessionProbe } from "../lib/sessionProbe";
import { catalogRows } from "../lib/hostCatalog";
import type { HostCategory, HostFilters } from "../lib/hostCatalog";
import { EventJournal } from "../lib/eventJournal";
import { hasTransientDiscoveryFailure, retainCapabilityCatalog } from "../lib/capabilityCatalog";
const localNode: NodeInfo = {
  id: "local", name: "This server", status: "online", online: true, local: true,
};
const nodeStorageKey = (accountId: string) => `connect-bots:account:${accountId}:node`;
function savedNode(accountId?: string) {
  if (!accountId) return "local";
  try { return localStorage.getItem(nodeStorageKey(accountId)) || "local"; }
  catch { return "local"; }
}
function rememberNode(accountId: string, nodeId: string) {
  try { localStorage.setItem(nodeStorageKey(accountId), nodeId); } catch { /* Private browsing may disable storage. */ }
}
interface WorkspaceState {
  bots: Bot[];
  capabilities: Capabilities | null;
  capabilitiesScope: string;
  capabilitiesLoading: boolean;
  capabilitiesError: string;
  loaded: boolean;
}
type Action =
  | { type: "bots"; bots: Bot[] }
  | { type: "capabilities"; capabilities: Capabilities | null; scope: string; loading: boolean; error: string }
  | { type: "update"; bot: Bot }
  | { type: "updates"; bots: Bot[] }
  | { type: "archive"; id: string }
  | { type: "clear" };
const initial: WorkspaceState = {
  bots: [],
  capabilities: null,
  capabilitiesScope: "",
  capabilitiesLoading: false,
  capabilitiesError: "",
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
    case "updates": {
      const bots = mergeBots(state.bots, action.bots);
      return bots === state.bots ? state : { ...state, bots };
    }
    case "archive":
      return {
        ...state,
        bots: state.bots.map((bot) => bot.id === action.id
          ? { ...bot, status: "archived", updatedAt: new Date().toISOString() }
          : bot),
      };
    case "capabilities":
      return { ...state, capabilities: action.capabilities, capabilitiesScope: action.scope,
        capabilitiesLoading: action.loading, capabilitiesError: action.error };
  }
}

export function useWorkspace() {
  const [state, dispatch] = useReducer(reducer, initial);
  const journalRef = useRef<EventJournal | null>(null);
  const eventJournal = journalRef.current ??= new EventJournal();
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
  const [workspaceVersion, setWorkspaceVersion] = useState(0);
  const [capabilitiesRevision, setCapabilitiesRevision] = useState(0);
  const [nodes, setNodes] = useState<NodeInfo[]>([]);
  const [activeNodeId, setActiveNodeId] = useState("local");
  const [catalogs, setCatalogs] = useState<Record<string, Bot[]>>({});
  const [hostFilters, setHostFilters] = useState<HostFilters>({ server: true, mac: true });
  const cursor = useRef(0);
  const generation = useRef(0);
  const verifiedGeneration = useRef(-1);
  const connectionEpoch = useRef(0);
  const userRef = useRef<StudioUser | null>(null);
  const accountGeneration = useRef(0);
  const nodesRequestVersion = useRef(0);
  const nodesRef = useRef<NodeInfo[]>([]);
  const activeNodeRef = useRef("local");
  const catalogsRef = useRef<Record<string, Bot[]>>({});
  const capabilityCatalogs = useRef(new Map<string, Capabilities>());
  const historyRequests = useRef(new Map<string, Promise<void>>());
  const stopStream = useRef<(() => void) | null>(null);

  const publishEvents = useCallback((events: Event[], historyBotId?: string) => {
    eventJournal.merge(events, historyBotId);
    if (historyBotId) return;
    const updates = events.map(eventBot).filter((bot): bot is Bot => bot !== null);
    if (updates.length) dispatch({ type: "updates", bots: updates });
  }, [eventJournal]);

  const updateCatalog = useCallback((nodeId: string, bots: Bot[], snapshot = false) => {
    const previous = catalogsRef.current[nodeId] || [];
    // A snapshot removes deleted rows, while timestamps preserve newer stream
    // updates for the bots that still exist in the snapshot.
    const base = snapshot ? previous.filter(bot => bots.some(next => next.id === bot.id)) : previous;
    const merged = mergeBots(base, bots);
    if (merged.length === previous.length && merged.every((bot, index) => bot === previous[index])) return;
    const next = { ...catalogsRef.current, [nodeId]: merged };
    catalogsRef.current = next;
    setCatalogs(next);
  }, []);

  // A node has its own bot IDs and event sequence. Invalidate its entire
  // workspace before a new node can publish any response or stream event.
  const resetWorkspace = useCallback((nodeId: string) => {
    generation.current++;
    verifiedGeneration.current = -1;
    setConnectionVersion(++connectionEpoch.current);
    stopStream.current?.();
    stopStream.current = null;
    cursor.current = 0;
    historyRequests.current.clear();
    eventJournal.clear();
    activeNodeRef.current = nodeId;
    setApiNode(nodeId);
    setActiveNodeId(nodeId);
    dispatch({ type: "clear" });
    setSelectedId("");
    setConnection("connecting");
    setSessionVerified(false);
    setWorkspaceVersion((version) => version + 1);
    return generation.current;
  }, [eventJournal]);
  // Node administration belongs to an account and survives a node switch.
  const resetAccount = useCallback((nextUser: StudioUser | null) => {
    accountGeneration.current++;
    nodesRequestVersion.current++;
    userRef.current = nextUser;
    setApiAccount(nextUser?.id || null);
    nodesRef.current = [];
    setNodes([]);
    catalogsRef.current = {};
    capabilityCatalogs.current.clear();
    setCatalogs({});
    setHostFilters({ server: true, mac: true });
    setUser(nextUser);
    setAccountVersion((version) => version + 1);
    return resetWorkspace(savedNode(nextUser?.id));
  }, [resetWorkspace]);
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

  const accountOwner = accountGeneration.current;
  const refreshNodes = useCallback(async (options: { background?: boolean } = {}) => {
    const accountId = user?.id;
    if (phase !== "ready" || !accountId || accountOwner !== accountGeneration.current || userRef.current?.id !== accountId)
      return [];
    const requestVersion = ++nodesRequestVersion.current;
    const current = () => accountOwner === accountGeneration.current && userRef.current?.id === accountId &&
      requestVersion === nodesRequestVersion.current;
    try {
      const result = await api.nodes(accountId);
      if (!current()) return [];
      const next = result.nodes || [];
      nodesRef.current = next;
      setNodes(previous => JSON.stringify(previous) === JSON.stringify(next) ? previous : next);
      const retained = Object.fromEntries(Object.entries(catalogsRef.current)
        .filter(([id]) => id === "local" || next.some(node => node.id === id)));
      if (Object.keys(retained).length !== Object.keys(catalogsRef.current).length) {
        catalogsRef.current = retained;
        setCatalogs(retained);
      }
      if (activeNodeRef.current !== "local" && !next.some((node) => node.id === activeNodeRef.current)) {
        rememberNode(accountId, "local");
        resetWorkspace("local");
      }
      return next;
    } catch (error) {
      if (!current()) return [];
      if (error instanceof ApiError && error.status === 401) expired();
      // Connectivity polling is best effort. A temporary outage or an older
      // hub without node support must not repeatedly interrupt the chat.
      else if (options.background) return nodesRef.current;
      else setError(errorMessage(error));
      if (options.background) return [];
      throw error;
    }
  }, [phase, user?.id, accountVersion, accountOwner, expired, resetWorkspace]);
  const selectNode = useCallback((id: string) => {
    const accountId = user?.id;
    if (!accountId || accountOwner !== accountGeneration.current || userRef.current?.id !== accountId ||
      (id !== "local" && !nodesRef.current.some((node) => node.id === id)) || activeNodeRef.current === id) return;
    rememberNode(accountId, id);
    setError("");
    resetWorkspace(id);
  }, [user?.id, accountVersion, accountOwner, resetWorkspace]);
  useEffect(() => {
    if (phase !== "ready" || !user) return;
    let alive = true;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const poll = async () => {
      await refreshNodes({ background: true });
      if (alive) timer = setTimeout(() => void poll(), 10000);
    };
    void poll();
    return () => { alive = false; if (timer !== undefined) clearTimeout(timer); };
  }, [phase, user?.id, accountVersion, refreshNodes]);

  // The roster spans all owned hosts; only the selected host owns an event
  // stream. Fetch other hosts with explicit bindings and keep their last known
  // catalogs in this account's memory while they are offline.
  useEffect(() => {
    if (phase !== "ready" || !user || !sessionVerified) return;
    const accountId = user.id;
    const account = accountGeneration.current;
    const controller = new AbortController();
    let alive = true;
    const current = (nodeId: string) => alive && !controller.signal.aborted &&
      account === accountGeneration.current && userRef.current?.id === accountId &&
      (nodeId === "local" || nodesRef.current.some(node => node.id === nodeId && node.online));
    const available = nodes.some(node => node.id === "local") ? nodes : [localNode, ...nodes];
    for (const node of available) {
      if (!node.online || node.id === activeNodeId) continue;
      void api.bots(node.id, controller.signal, accountId).then(result => {
        if (current(node.id)) updateCatalog(node.id, result.bots || [], true);
      }).catch(error => {
        if (!current(node.id)) return;
        if (error instanceof ApiError && error.status === 401) expired();
        // A host can become unavailable between snapshots. Background roster
        // refreshes retain the last catalog without interrupting a conversation.
      });
    }
    return () => { alive = false; controller.abort(); };
  }, [phase, user?.id, accountVersion, nodes, activeNodeId, sessionVerified, expired, updateCatalog]);

  useEffect(() => {
    if (phase !== "ready" || !user) return;
    let alive = true;
    let verified = false;
    let botsRequested = false;
    let sessionCheck: { epoch: number; promise: Promise<StudioSession> } | null = null;
    const owner = generation.current;
    const accountId = user.id;
    const nodeId = activeNodeId;
    const pendingEvents: Event[] = [];
    const current = () => alive && owner === generation.current && userRef.current?.id === accountId && activeNodeRef.current === nodeId;
    const sameConnection = (epoch: number) => current() && epoch === connectionEpoch.current;
    let queued: Event[] = [];
    let flushTimer: ReturnType<typeof setTimeout> | undefined;
    const flushEvents = () => {
      if (flushTimer !== undefined) clearTimeout(flushTimer);
      flushTimer = undefined;
      const events = queued;
      queued = [];
      if (current() && events.length) publishEvents(events);
    };
    const receive = (event: Event) => {
      if (!current()) return;
      cursor.current = Math.max(cursor.current, event.seq);
      queued.push(event);
      const delta = event.type === "native" && (/delta/i.test(String(event.data.method)) || event.data.method === "message_update")
        || event.type === "agent" && ["text", "thinking"].includes(String(event.data.type));
      // Preserve every event, but publish token bursts at most every 32 ms.
      // Completion, questions and errors flush immediately in original order.
      if (!delta) flushEvents();
      else if (flushTimer === undefined) flushTimer = setTimeout(flushEvents, 32);
      const bot = eventBot(event);
      if (bot) updateCatalog(nodeId, [bot]);
    };
    const loadBots = (epoch: number) => {
      if (botsRequested) return;
      botsRequested = true;
      api.bots(nodeId, undefined, accountId).then((result) => {
        if (sameConnection(epoch) && verified) {
          dispatch({ type: "bots", bots: result.bots || [] });
          updateCatalog(nodeId, result.bots || []);
        }
      }).catch((error) => {
        if (!sameConnection(epoch)) return;
        if (error instanceof ApiError && error.status === 401) expired();
        else if (!(error instanceof ApiError && ["node_offline", "node_disconnected"].includes(error.code || "")))
          setError(errorMessage(error));
      });
    };
    const source = new EventSource(`/api/studio/events?after=${cursor.current}&expectedAccount=${encodeURIComponent(accountId)}&node=${encodeURIComponent(nodeId)}`);
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
      flushEvents();
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
      if (flushTimer !== undefined) clearTimeout(flushTimer);
      queued = [];
      pendingEvents.length = 0;
      sessionProbe.dispose();
      source.close();
      if (stopStream.current === stop) stopStream.current = null;
    };
    stopStream.current = stop;
    verifyConnection();
    return stop;
  }, [phase, user?.id, activeNodeId, revision, accountVersion, workspaceVersion, applySession, expired, updateCatalog, publishEvents]);

  const selectedBot = state.bots.find((bot) => bot.id === selectedId);
  const catalogScope = JSON.stringify([
    activeNodeId, selectedBot?.id || "", selectedBot?.backend || "", selectedBot?.model || "",
  ]);
  const hasBots = state.bots.some((bot) => bot.status !== "archived");
  useEffect(() => {
    if (phase !== "ready" || !sessionVerified || !state.loaded || (hasBots && !selectedBot)) return;
    const controller = new AbortController();
    let alive = true;
    const owner = generation.current;
    const connectionOwner = connectionEpoch.current;
    const accountId = userRef.current?.id;
    const current = () => alive && owner === generation.current && verifiedGeneration.current === owner &&
      connectionOwner === connectionEpoch.current && accountId === userRef.current?.id;
    let retryTimer: ReturnType<typeof setTimeout> | undefined;
    const publish = (loading: boolean, error = "") => dispatch({ type: "capabilities", scope: catalogScope,
      capabilities: capabilityCatalogs.current.get(catalogScope) || null, loading, error });
    const schedule = (attempt: number) => {
      retryTimer = setTimeout(() => { retryTimer = undefined; if (current()) void load(attempt + 1); }, [1000, 3000][attempt]);
    };
    const load = async (attempt: number) => {
      try {
        const capabilities = await api.capabilities(selectedBot?.id, controller.signal, activeNodeId, accountId);
        if (!current()) return;
        capabilityCatalogs.current.set(catalogScope,
          retainCapabilityCatalog(capabilityCatalogs.current.get(catalogScope), capabilities));
        const transientFailure = hasTransientDiscoveryFailure(capabilities);
        const retry = attempt < 2 && transientFailure;
        publish(retry, transientFailure ? "Some model settings are temporarily unavailable. Try again." : "");
        if (retry) schedule(attempt);
      } catch (error) {
        if (!current()) return;
        if (error instanceof ApiError && error.status === 401) { expired(); return; }
        const retry = attempt < 2 && (!(error instanceof ApiError) || error.status >= 500 || [408, 429].includes(error.status));
        publish(retry, "Model settings could not be refreshed. Try again.");
        if (retry) schedule(attempt);
      }
    };
    publish(true);
    void load(0);
    return () => { alive = false; controller.abort(); if (retryTimer !== undefined) clearTimeout(retryTimer); };
  }, [phase, sessionVerified, connectionVersion, revision, accountVersion, workspaceVersion, activeNodeId, state.loaded, hasBots, catalogScope, capabilitiesRevision, expired]);

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

  // Callbacks held by an exiting chat or dialog belong to one workspace.
  const owner = generation.current;
  const refreshCapabilities = useCallback(() => {
    if (owner === generation.current && userRef.current) setCapabilitiesRevision(value => value + 1);
  }, [owner]);
  const loadHistory = useCallback((id: string, force = false): Promise<void> => {
    if (owner !== generation.current || verifiedGeneration.current !== owner || !userRef.current) return Promise.resolve();
    if (nodesRef.current.find(node => node.id === activeNodeRef.current)?.online === false) return Promise.resolve();
    const cached = historyRequests.current.get(id);
    if (!force && cached) return cached;
    const connectionOwner = connectionEpoch.current;
    const forget = () => { if (historyRequests.current.get(id) === request) historyRequests.current.delete(id); };
    const request = api.events(id).then(({ events }) => {
      if (owner === generation.current && verifiedGeneration.current === owner && connectionOwner === connectionEpoch.current)
        publishEvents(events || [], id);
      else forget();
    }).catch(error => {
      forget();
      if (owner !== generation.current || connectionOwner !== connectionEpoch.current) return;
      if (error instanceof ApiError && error.status === 401) expired();
      else throw error;
    });
    historyRequests.current.set(id, request);
    return request;
  }, [owner, expired, publishEvents]);
  const updateBot = useCallback((bot: Bot) => {
    if (owner === generation.current && userRef.current) {
      dispatch({ type: "update", bot });
      updateCatalog(activeNodeRef.current, [bot]);
    }
  }, [owner, updateCatalog]);
  const archiveBot = useCallback((id: string) => {
    if (owner === generation.current && userRef.current) {
      dispatch({ type: "archive", id });
      const bot = catalogsRef.current[activeNodeRef.current]?.find(bot => bot.id === id);
      if (bot) updateCatalog(activeNodeRef.current, [{ ...bot, status: "archived", updatedAt: new Date().toISOString() }]);
    }
  }, [owner, updateCatalog]);
  const setHostFilter = useCallback((category: HostCategory, enabled: boolean) => {
    if (user && accountOwner === accountGeneration.current && userRef.current?.id === user.id)
      setHostFilters(previous => ({ ...previous, [category]: enabled }));
  }, [accountOwner, user?.id]);
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
  const activeNode = nodes.find(node => node.id === activeNodeId) || (activeNodeId === "local" ? localNode : {
    id: activeNodeId, name: "Remote host", status: "connecting", online: false, local: false,
  });
  const available = nodes.some(node => node.id === "local") ? nodes : [localNode, ...nodes];
  const activeBots = activeNode.online ? state.bots : catalogs[activeNodeId] || state.bots;
  const bots = useMemo(() => activeBots.filter(bot => bot.status !== "archived"), [activeBots]);
  const catalogBots = useMemo(() => catalogRows(available, catalogs), [nodes, catalogs]);
  return {
    ...state,
    capabilities: state.capabilitiesScope === catalogScope ? state.capabilities : capabilityCatalogs.current.get(catalogScope) || null,
    capabilitiesLoading: state.capabilitiesScope === catalogScope ? state.capabilitiesLoading : phase === "ready",
    capabilitiesError: state.capabilitiesScope === catalogScope ? state.capabilitiesError : "",
    refreshCapabilities,
    // Snapshots remain available to diagnostics/tests. Renderers subscribe to
    // eventJournal directly and therefore do not wake the app shell.
    events: eventJournal.all(),
    rosterEvents: eventJournal.allRoster(),
    eventJournal,
    allBots: activeBots,
    bots,
    catalogBots, hostFilters, setHostFilter,
    user, registrationAllowed, legacyClaimAvailable, setupRequired, accountVersion, workspaceVersion,
    nodes, activeNodeId,
    activeNode,
    phase, connection, error, setError: reportError,
    login, register, logout, loadHistory, updateBot, archiveBot, selectCatalogBot, selectNode, refreshNodes, retry,
  };
}
