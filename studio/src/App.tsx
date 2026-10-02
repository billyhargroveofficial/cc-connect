import { memo, useCallback, useEffect, useLayoutEffect, useRef, useState, lazy, Suspense } from "react";
import { Plus, X, LoaderCircle } from "lucide-react";
import { useWorkspace } from "./hooks/useWorkspace";
import type { Bot, Capabilities } from "./lib/types";
import { api, errorMessage, nodeBinaryURL } from "./lib/api";
import { catalogBotKey } from "./lib/hostCatalog";
import Login from "./components/Login";
import Avatar from "./components/Avatar";
import BotRoster from "./components/BotRoster";
import { useTheme } from "./hooks/useTheme";
import { useBotEvents, useRosterEvents } from "./hooks/useEventJournal";
import type { EventJournal } from "./lib/eventJournal";
import {
  AnimatePresence,
  controlMotion,
  fade,
  fadeUp,
  m,
  motionTransition,
  useIsPresent,
} from "./lib/motion";
const ChatRoom = lazy(() => import("./features/chat/ChatRoom"));
const SettingsDrawer = lazy(() =>
  import("./features/settings/SettingsDrawer").then((module) => ({
    default: memo(module.SettingsDrawer),
  })),
);
const NewBotDialog = lazy(() =>
  import("./features/settings/NewBotDialog").then((module) => ({
    default: memo(module.NewBotDialog),
  })),
);
// Creation catalogs must stay stable while the active workspace streams tokens.
const loadNodeCapabilities = (nodeId: string, signal: AbortSignal): Promise<Capabilities> =>
  api.capabilities(undefined, signal, nodeId);
const createNodeBot = (fields: Partial<Bot>, nodeId: string) => api.createBot(fields, nodeId);
function currentBot() {
  const match = location.hash.match(/^#\/bots\/([^/]+)$/);
  try {
    return match ? decodeURIComponent(match[1]) : "";
  } catch {
    return "";
  }
}

function FadingSurface({
  children,
  className,
}: {
  children: React.ReactNode;
  className: string;
}) {
  const present = useIsPresent();
  return (
    <m.div
      className={className}
      variants={fade}
      initial="hidden"
      animate="visible"
      exit="exit"
      inert={!present}
    >
      {children}
    </m.div>
  );
}

function WorkspaceToast({
  message,
  onDismiss,
}: {
  message: string;
  onDismiss: () => void;
}) {
  const present = useIsPresent();
  return (
    <m.div
      className="global-toast"
      role="alert"
      initial={{ opacity: 0, y: 10 }}
      animate={{ opacity: 1, y: 0 }}
      exit={{ opacity: 0, y: 5, transition: motionTransition.exit }}
      transition={motionTransition.enter}
      inert={!present}
      aria-hidden={!present || undefined}
    >
      <p>{message}</p>
      <m.button
        className="icon-button"
        data-motion-control
        {...controlMotion}
        onClick={onDismiss}
        aria-label="Dismiss notification"
      >
        <X size={16} />
      </m.button>
    </m.div>
  );
}

const RosterRegion = memo(function RosterRegion({
  journal,
  ...props
}: {
  journal: EventJournal;
} & Omit<React.ComponentProps<typeof BotRoster>, "events">) {
  const events = useRosterEvents(journal);
  return <BotRoster {...props} events={events} />;
});

const ConversationPane = memo(function ConversationPane({
  journal,
  loadHistory,
  onError,
  ...props
}: {
  journal: EventJournal;
  loadHistory: (id: string, force?: boolean) => Promise<void>;
  onError: (message: string) => void;
} & Omit<React.ComponentProps<typeof ChatRoom>, "events" | "messages" | "loading" | "onHistory" | "onError">) {
  const events = useBotEvents(journal, props.bot.id);
  const messages = journal.messagesFor(props.bot.id);
  const [loading, setLoading] = useState(true);
  useEffect(() => {
    let alive = true;
    setLoading(true);
    loadHistory(props.bot.id).catch(error => {
      if (alive) onError(errorMessage(error));
    }).finally(() => {
      if (alive) setLoading(false);
    });
    return () => { alive = false; };
  }, [props.bot.id, props.node?.id, props.draftScope, journal, loadHistory, onError]);
  const reload = useCallback(() => {
    void loadHistory(props.bot.id, true).catch(error => onError(errorMessage(error)));
  }, [props.bot.id, props.node?.id, props.draftScope, loadHistory, onError]);
  return (
    <FadingSurface className="workspace-conversation">
      <ChatRoom {...props} events={events} messages={messages} loading={loading} onError={onError} onHistory={reload} />
    </FadingSurface>
  );
});

export default function App() {
  const workspace = useWorkspace();
  const { preference: theme, setPreference: setTheme } = useTheme();
  const previousAccount = useRef<string | null>(null);
  const [route, setRoute] = useState<{ key: string; nodeId: string; botId: string } | null>(null);
  const accountKey = workspace.user ? `${workspace.user.id}:${workspace.accountVersion}` : "";
  useLayoutEffect(() => {
    const clearHash = () => {
      if (location.hash) history.replaceState(null, "", location.pathname + location.search);
    };
    if (workspace.phase !== "ready" || !workspace.user) {
      if (previousAccount.current) clearHash();
      setRoute(null);
      return;
    }
    let storedAccount: string | null = null;
    try { storedAccount = sessionStorage.getItem("connect-bots:route-account"); } catch { /* Storage may be disabled. */ }
    const reuseRoute = !previousAccount.current && (!storedAccount || storedAccount === workspace.user.id);
    const botId = reuseRoute ? currentBot() : "";
    previousAccount.current = workspace.user.id;
    try { sessionStorage.setItem("connect-bots:route-account", workspace.user.id); } catch { /* The route still resets within this page. */ }
    setRoute((current) => {
      if (current?.key === accountKey && current.nodeId === workspace.activeNodeId) return current;
      // Keep the route already chosen by a roster click when the host changes.
      // Effect replays for this same workspace must not clear its active chat.
      if (current?.key === accountKey || !reuseRoute) clearHash();
      return { key: accountKey, nodeId: workspace.activeNodeId, botId: current?.key === accountKey ? "" : botId };
    });
  }, [workspace.phase, workspace.user?.id, workspace.activeNodeId, accountKey]);
  const routeChanged = useCallback((nodeId: string, botId: string) => setRoute({ key: accountKey, nodeId, botId }), [accountKey]);
  const ready = workspace.phase === "ready" && route?.key === accountKey;
  return (
    <AnimatePresence presenceAffectsLayout={false} mode="wait" initial={false}>
      {ready ? (
        <AccountWorkspace
          key={`${accountKey}:${workspace.activeNodeId}:${workspace.workspaceVersion}`}
          workspace={workspace}
          initialBot={route.nodeId === workspace.activeNodeId ? route.botId : ""}
          onRouteChange={routeChanged}
          theme={theme}
          setTheme={setTheme}
        />
      ) : (
        <Login
          key="login"
          checking={workspace.phase === "checking" || workspace.phase === "ready"}
          error={workspace.error}
          registrationAllowed={workspace.registrationAllowed}
          legacyClaimAvailable={workspace.legacyClaimAvailable}
          setupRequired={workspace.setupRequired}
          onLogin={workspace.login}
          onRegister={workspace.register}
          onRetry={workspace.retry}
        />
      )}
    </AnimatePresence>
  );
}

function AccountWorkspace({
  workspace,
  initialBot,
  onRouteChange,
  theme,
  setTheme,
}: {
  workspace: ReturnType<typeof useWorkspace>;
  initialBot: string;
  onRouteChange: (nodeId: string, botId: string) => void;
  theme: ReturnType<typeof useTheme>["preference"];
  setTheme: ReturnType<typeof useTheme>["setPreference"];
}) {
  const active = useRef(true);
  active.current = useIsPresent();
  useEffect(() => {
    active.current = true;
    return () => { active.current = false; };
  }, []);
  const [selectedId, setSelectedId] = useState(initialBot);
  const [mobileChat, setMobileChat] = useState(!!initialBot);
  const [globalSettings, setGlobalSettings] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [mobileViewport, setMobileViewport] = useState(
    () => window.matchMedia("(max-width: 700px)").matches,
  );
  const bot = workspace.bots.find((bot) => bot.id === selectedId) || null;
  useEffect(() => workspace.selectCatalogBot(selectedId), [selectedId, workspace.selectCatalogBot]);
  useEffect(() => {
    const query = window.matchMedia("(max-width: 700px)");
    const update = () => setMobileViewport(query.matches);
    query.addEventListener("change", update);
    update();
    return () => query.removeEventListener("change", update);
  }, []);
  useEffect(() => {
    const onHash = () => {
      if (!active.current) return;
      const id = currentBot();
      if (id) setSelectedId(id);
      setMobileChat(!!id);
    };
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);
  useEffect(() => {
    if (
      workspace.loaded &&
      !workspace.bots.some((bot) => bot.id === selectedId)
    ) {
      const first = [...workspace.bots].sort(
        (a, b) => Number(b.chief) - Number(a.chief),
      )[0];
      if (first) setSelectedId(first.id);
      else setSelectedId("");
    }
  }, [workspace.bots, workspace.loaded, selectedId]);
  useEffect(() => {
    function keyboard(event: KeyboardEvent) {
      if (event.key === "Escape") {
        setGlobalSettings(null);
        setCreating(false);
      }
    }
    window.addEventListener("keydown", keyboard);
    return () => window.removeEventListener("keydown", keyboard);
  }, []);
  const select = useCallback((id: string, nodeId = workspace.activeNodeId) => {
    if (!active.current) return;
    if (nodeId !== workspace.activeNodeId) {
      if (nodeId !== "local" && !workspace.nodes.some(node => node.id === nodeId)) return;
      setCreating(false);
      setGlobalSettings(null);
      onRouteChange(nodeId, id);
      location.hash = `/bots/${encodeURIComponent(id)}`;
      workspace.selectNode(nodeId);
      return;
    }
    setSelectedId(id);
    setMobileChat(true);
    onRouteChange(workspace.activeNodeId, id);
    location.hash = `/bots/${encodeURIComponent(id)}`;
  }, [onRouteChange, workspace.activeNodeId, workspace.nodes, workspace.selectNode]);
  const switchNode = useCallback((id: string) => {
    if (!active.current || id === workspace.activeNodeId) return;
    setSelectedId("");
    setMobileChat(false);
    setCreating(false);
    setGlobalSettings(null);
    onRouteChange(id, "");
    if (location.hash) history.replaceState(null, "", location.pathname + location.search);
    workspace.selectNode(id);
  }, [onRouteChange, workspace.activeNodeId, workspace.selectNode]);
  const closeSettings = useCallback(() => setGlobalSettings(null), []);
  const closeCreate = useCallback(() => setCreating(false), []);
  const openCreate = useCallback(() => setCreating(true), []);
  const openSettings = useCallback(() => setGlobalSettings("instructions"), []);
  const signOut = useCallback(() => {
    void workspace.logout().catch(error => workspace.setError(errorMessage(error)));
  }, [workspace.logout, workspace.setError]);
  const backToBots = useCallback(() => {
    if (!active.current) return;
    setMobileChat(false);
    location.hash = "";
  }, []);
  const created = useCallback((bot: Bot, nodeId = workspace.activeNodeId) => {
    if (!active.current) return;
    if (nodeId !== workspace.activeNodeId) {
      setCreating(false);
      onRouteChange(nodeId, bot.id);
      location.hash = `/bots/${encodeURIComponent(bot.id)}`;
      workspace.selectNode(nodeId);
      return;
    }
    workspace.updateBot(bot);
    select(bot.id);
    setCreating(false);
  }, [workspace.activeNodeId, workspace.selectNode, workspace.updateBot, onRouteChange, select]);
  const createEnrollment = useCallback(async (name: string) => {
    const enrollment = await api.createNodeEnrollment(name);
    await workspace.refreshNodes();
    return enrollment;
  }, [workspace.refreshNodes]);
  const removeNode = useCallback(async (id: string) => {
    await api.removeNode(id);
    if (workspace.activeNodeId === id) switchNode("local");
    await workspace.refreshNodes();
  }, [workspace.activeNodeId, switchNode, workspace.refreshNodes]);
  return (
        <FadingSurface
          className={`workspace ${mobileChat ? "is-chat" : ""}`}
        >
          <RosterRegion
            journal={workspace.eventJournal}
            mobileHidden={mobileViewport && mobileChat}
            bots={workspace.catalogBots}
            selectedKey={catalogBotKey(workspace.activeNodeId, selectedId)}
            onSelect={select}
            onCreate={openCreate}
            onSettings={openSettings}
            onTheme={setTheme}
            theme={theme}
            onLogout={signOut}
            connection={workspace.connection}
            user={workspace.user}
            activeNode={workspace.activeNode}
            hostFilters={workspace.hostFilters}
            onFilterChange={workspace.setHostFilter}
          />
          <m.main
            className="workspace-content"
            initial={false}
            animate={{ opacity: mobileViewport && !mobileChat ? 0 : 1 }}
            transition={motionTransition.enter}
            inert={mobileViewport && !mobileChat}
            aria-hidden={(mobileViewport && !mobileChat) || undefined}
          >
            <Suspense
              fallback={
                <m.div
                  className="chat-loading"
                  variants={fade}
                  initial="hidden"
                  animate="visible"
                >
                  <LoaderCircle size={21} className="spin" />
                  <span>Opening conversation…</span>
                </m.div>
              }
            >
              <AnimatePresence presenceAffectsLayout={false} initial={false} mode="wait">
                {bot ? (
                  <ConversationPane
                    key={JSON.stringify([workspace.user?.id || "", workspace.activeNodeId, bot.id])}
                    journal={workspace.eventJournal}
                    loadHistory={workspace.loadHistory}
                      draftScope={`${workspace.user?.id || ""}:${workspace.activeNodeId}`}
                      bot={bot}
                      node={workspace.activeNode}
                      offline={!workspace.activeNode.online}
                      capabilities={workspace.capabilities}
                      suspended={Boolean(globalSettings) || creating || (mobileViewport && !mobileChat)}
                      onBack={backToBots}
                      onBotChange={workspace.updateBot}
                      onArchive={workspace.archiveBot}
                      onError={workspace.setError}
                  />
                ) : (
                  <FadingSurface
                    key="empty-workspace"
                    className="workspace-conversation"
                  >
                    <m.div
                      className="workspace-empty"
                      variants={fadeUp}
                      initial="hidden"
                      animate="visible"
                    >
                      {!workspace.loaded && workspace.activeNode.online ? (
                        <>
                          <LoaderCircle className="spin" size={28} />
                          <p>Opening workspace…</p>
                        </>
                      ) : !workspace.loaded ? (
                        <>
                          <Avatar avatar="slate" size={80} />
                          <span className="eyebrow">HOST OFFLINE</span>
                          <h1>{workspace.activeNode.name} is disconnected.</h1>
                          <p>Start Connect Bots Node on that computer, then this workspace will reconnect automatically.</p>
                          <m.button className="primary-button" data-motion-control {...controlMotion}
                            onClick={() => setGlobalSettings("hosts")}>Manage hosts</m.button>
                        </>
                      ) : (
                        <>
                          <Avatar avatar="lavender" size={80} />
                          <span className="eyebrow">
                            START WITH ONE ASSISTANT
                          </span>
                          <h1>Build your team.</h1>
                          <p>
                            Give your bot a name, a role, and its first task. It
                            keeps its context and continues working after you
                            close the page.
                          </p>
                          <m.button
                            className="primary-button"
                            data-motion-control
                            {...controlMotion}
                            onClick={() => setCreating(true)}
                          >
                            <Plus size={17} />
                            Create bot
                          </m.button>
                        </>
                      )}
                    </m.div>
                  </FadingSurface>
                )}
              </AnimatePresence>
            </Suspense>
          </m.main>
          <AnimatePresence presenceAffectsLayout={false} initial={false}>
            {workspace.error && (
              <div key="workspace-toast" className="global-toast-position">
                <WorkspaceToast
                  message={workspace.error}
                  onDismiss={() => workspace.setError("")}
                />
              </div>
            )}
          </AnimatePresence>
          <AnimatePresence presenceAffectsLayout={false}>
            {globalSettings !== null && (
              <Suspense key="global-settings" fallback={null}>
                <SettingsDrawer
                  bot={null}
                  bots={workspace.allBots}
                  global
                  initialTab={globalSettings}
                  capabilities={workspace.capabilities}
                  onClose={closeSettings}
                  onBotChange={workspace.updateBot}
                  onArchive={workspace.archiveBot}
                  nodes={workspace.nodes}
                  activeNode={workspace.activeNode}
                  onSelectNode={switchNode}
                  onCreateNodeEnrollment={createEnrollment}
                  onRemoveNode={removeNode}
                  onRefreshNodes={workspace.refreshNodes}
                  nodeBinaryURL={nodeBinaryURL}
                />
              </Suspense>
            )}
            {creating && (
              <Suspense key="new-bot" fallback={null}>
                <NewBotDialog
                  capabilities={workspace.capabilities}
                  nodes={workspace.nodes}
                  activeNode={workspace.activeNode}
                  loadCapabilities={loadNodeCapabilities}
                  createBot={createNodeBot}
                  onClose={closeCreate}
                  onCreated={created}
                />
              </Suspense>
            )}
          </AnimatePresence>
        </FadingSurface>
  );
}
