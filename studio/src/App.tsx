import { useCallback, useEffect, useState, lazy, Suspense } from "react";
import { Plus, X, LoaderCircle } from "lucide-react";
import { useWorkspace } from "./hooks/useWorkspace";
import type { Bot } from "./lib/types";
import { errorMessage } from "./lib/api";
import Login from "./components/Login";
import Avatar from "./components/Avatar";
import BotRoster from "./components/BotRoster";
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
function initialTheme(): "light" | "dark" {
  try {
    const saved = localStorage.getItem("connect-bots:theme");
    return saved === "light" ? "light" : "dark";
  } catch {
    return "dark";
  }
}
export default function App() {
  const [selectedId, setSelectedId] = useState(currentBot);
  const workspace = useWorkspace(selectedId);
  const [mobileChat, setMobileChat] = useState(() => !!currentBot());
  const [theme, setTheme] = useState(initialTheme);
  const [settings, setSettings] = useState<"bot" | "global" | null>(null);
  const [creating, setCreating] = useState(false);
  const [historyLoading, setHistoryLoading] = useState(false);
  const bot = workspace.bots.find((bot) => bot.id === selectedId) || null;
  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    document.documentElement.classList.toggle("dark", theme === "dark");
    try {
      localStorage.setItem("connect-bots:theme", theme);
    } catch {}
  }, [theme]);
  useEffect(() => {
    const onHash = () => {
      const id = currentBot();
      setSelectedId(id);
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
        setSettings(null);
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
  const closeSettings = useCallback(() => setSettings(null), []);
  const closeCreate = useCallback(() => setCreating(false), []);
  function created(bot: Bot) {
    workspace.updateBot(bot);
    select(bot.id);
    setCreating(false);
  }
  if (workspace.phase !== "ready")
    return (
      <Login
        checking={workspace.phase === "checking"}
        error={workspace.error}
        onLogin={workspace.login}
        onRetry={workspace.retry}
      />
    );
  return (
    <div className={`workspace ${mobileChat ? "is-chat" : ""}`}>
      <BotRoster
        bots={workspace.bots}
        events={workspace.events}
        selectedId={selectedId}
        onSelect={select}
        onCreate={() => setCreating(true)}
        onSettings={() => setSettings("global")}
        onTheme={() =>
          setTheme((current) => (current === "dark" ? "light" : "dark"))
        }
        theme={theme}
        onLogout={() =>
          void workspace
            .logout()
            .catch((error) => workspace.setError(errorMessage(error)))
        }
        connection={workspace.connection}
      />
      <main className="workspace-content">
        <Suspense
          fallback={
            <div className="chat-loading">
              <LoaderCircle size={21} className="spin" />
              <span>Открываем разговор…</span>
            </div>
          }
        >
          {bot ? (
            <ChatRoom
              key={bot.id}
              bot={bot}
              events={workspace.events[bot.id] || []}
              capabilities={workspace.capabilities}
              loading={historyLoading}
              onBack={() => {
                setMobileChat(false);
                location.hash = "";
              }}
              onSettings={() => setSettings("bot")}
              onBotChange={workspace.updateBot}
              onError={workspace.setError}
              onHistory={() =>
                void workspace
                  .loadHistory(bot.id)
                  .catch((error) => workspace.setError(errorMessage(error)))
              }
            />
          ) : (
            <div className="workspace-empty">
              {!workspace.loaded ? (
                <>
                  <LoaderCircle className="spin" size={28} />
                  <p>Открываем пространство…</p>
                </>
              ) : (
                <>
                  <Avatar avatar="lavender" size={80} />
                  <span className="eyebrow">НАЧНИТЕ С ОДНОГО ПОМОЩНИКА</span>
                  <h1>Соберите свою команду.</h1>
                  <p>
                    Дайте боту имя, роль и первое поручение. Он сохранит
                    контекст и продолжит работу, когда вы закроете страницу.
                  </p>
                  <button
                    className="primary-button"
                    onClick={() => setCreating(true)}
                  >
                    <Plus size={17} />
                    Создать бота
                  </button>
                </>
              )}
            </div>
          )}
        </Suspense>
      </main>
      {workspace.error && (
        <div className="global-toast" role="alert">
          <p>{workspace.error}</p>
          <button
            className="icon-button"
            onClick={() => workspace.setError("")}
            aria-label="Закрыть уведомление"
          >
            <X size={16} />
          </button>
        </div>
      )}
      {settings && (
        <Suspense fallback={null}>
          <SettingsDrawer
            bot={settings === "bot" ? bot : null}
            bots={workspace.allBots}
            global={settings === "global"}
            capabilities={workspace.capabilities}
            onClose={closeSettings}
            onBotChange={workspace.updateBot}
            onArchive={workspace.archiveBot}
          />
        </Suspense>
      )}{" "}
      {creating && (
        <Suspense fallback={null}>
          <NewBotDialog
            capabilities={workspace.capabilities}
            onClose={closeCreate}
            onCreated={created}
          />
        </Suspense>
      )}
    </div>
  );
}
