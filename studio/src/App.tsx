import { useCallback, useEffect, useState, lazy, Suspense } from "react";
import { Plus, X, LoaderCircle } from "lucide-react";
import { useWorkspace } from "./hooks/useWorkspace";
import type { Bot } from "./lib/types";
import { errorMessage } from "./lib/api";
import Login from "./components/Login";
import Avatar from "./components/Avatar";
import BotRoster from "./components/BotRoster";
import { useTheme } from "./hooks/useTheme";
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
    default: module.SettingsDrawer,
  })),
);
const NewBotDialog = lazy(() =>
  import("./features/settings/NewBotDialog").then((module) => ({
    default: module.NewBotDialog,
  })),
);
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
      aria-hidden={!present || undefined}
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

export default function App() {
  const [selectedId, setSelectedId] = useState(currentBot);
  const workspace = useWorkspace(selectedId);
  const [mobileChat, setMobileChat] = useState(() => !!currentBot());
  const { preference: theme, setPreference: setTheme } = useTheme();
  const [globalSettings, setGlobalSettings] = useState(false);
  const [creating, setCreating] = useState(false);
  const [historyLoading, setHistoryLoading] = useState(false);
  const [mobileViewport, setMobileViewport] = useState(
    () => window.matchMedia("(max-width: 700px)").matches,
  );
  const bot = workspace.bots.find((bot) => bot.id === selectedId) || null;
  useEffect(() => {
    const query = window.matchMedia("(max-width: 700px)");
    const update = () => setMobileViewport(query.matches);
    query.addEventListener("change", update);
    update();
    return () => query.removeEventListener("change", update);
  }, []);
  useEffect(() => {
    const onHash = () => {
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
    if (!bot) return;
    let alive = true;
    setHistoryLoading(true);
    workspace
      .loadHistory(bot.id)
      .catch((error) => {
        if (alive) workspace.setError(errorMessage(error));
      })
      .finally(() => {
        if (alive) setHistoryLoading(false);
      });
    return () => {
      alive = false;
    };
  }, [bot?.id, workspace.loadHistory, workspace.setError]);
  useEffect(() => {
    function keyboard(event: KeyboardEvent) {
      if (event.key === "Escape") {
        setGlobalSettings(false);
        setCreating(false);
      }
    }
    window.addEventListener("keydown", keyboard);
    return () => window.removeEventListener("keydown", keyboard);
  }, []);
  const select = useCallback((id: string) => {
    setSelectedId(id);
    setMobileChat(true);
    location.hash = `/bots/${encodeURIComponent(id)}`;
  }, []);
  const closeSettings = useCallback(() => setGlobalSettings(false), []);
  const closeCreate = useCallback(() => setCreating(false), []);
  function created(bot: Bot) {
    workspace.updateBot(bot);
    select(bot.id);
    setCreating(false);
  }
  return (
    <AnimatePresence mode="wait" initial={false}>
      {workspace.phase !== "ready" ? (
        <Login
          key="login"
          checking={workspace.phase === "checking"}
          error={workspace.error}
          onLogin={workspace.login}
          onRetry={workspace.retry}
        />
      ) : (
        <FadingSurface
          key="workspace"
          className={`workspace ${mobileChat ? "is-chat" : ""}`}
        >
          <BotRoster
            mobileHidden={mobileViewport && mobileChat}
            bots={workspace.bots}
            events={workspace.events}
            selectedId={selectedId}
            onSelect={select}
            onCreate={() => setCreating(true)}
            onSettings={() => setGlobalSettings(true)}
            onTheme={setTheme}
            theme={theme}
            onLogout={() =>
              void workspace
                .logout()
                .catch((error) => workspace.setError(errorMessage(error)))
            }
            connection={workspace.connection}
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
              <AnimatePresence initial={false} mode="wait">
                {bot ? (
                  <FadingSurface
                    key={bot.id}
                    className="workspace-conversation"
                  >
                    <ChatRoom
                      bot={bot}
                      events={workspace.events[bot.id] || []}
                      capabilities={workspace.capabilities}
                      loading={historyLoading}
                      suspended={globalSettings || creating || (mobileViewport && !mobileChat)}
                      onBack={() => {
                        setMobileChat(false);
                        location.hash = "";
                      }}
                      onBotChange={workspace.updateBot}
                      onArchive={workspace.archiveBot}
                      onError={workspace.setError}
                      onHistory={() =>
                        void workspace
                          .loadHistory(bot.id)
                          .catch((error) =>
                            workspace.setError(errorMessage(error)),
                          )
                      }
                    />
                  </FadingSurface>
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
                      {!workspace.loaded ? (
                        <>
                          <LoaderCircle className="spin" size={28} />
                          <p>Opening workspace…</p>
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
          <AnimatePresence initial={false}>
            {workspace.error && (
              <div key="workspace-toast" className="global-toast-position">
                <WorkspaceToast
                  message={workspace.error}
                  onDismiss={() => workspace.setError("")}
                />
              </div>
            )}
          </AnimatePresence>
          <AnimatePresence>
            {globalSettings && (
              <Suspense key="global-settings" fallback={null}>
                <SettingsDrawer
                  bot={null}
                  bots={workspace.allBots}
                  global
                  capabilities={workspace.capabilities}
                  onClose={closeSettings}
                  onBotChange={workspace.updateBot}
                  onArchive={workspace.archiveBot}
                />
              </Suspense>
            )}
            {creating && (
              <Suspense key="new-bot" fallback={null}>
                <NewBotDialog
                  capabilities={workspace.capabilities}
                  onClose={closeCreate}
                  onCreated={created}
                />
              </Suspense>
            )}
          </AnimatePresence>
        </FadingSurface>
      )}
    </AnimatePresence>
  );
}
