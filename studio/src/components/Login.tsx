import { useState } from "react";
import { ArrowRight, KeyRound, LoaderCircle, RefreshCw } from "lucide-react";
import Brand from "./Brand";
import Avatar from "./Avatar";
import { errorMessage } from "../lib/api";
export default function Login({
  checking,
  error,
  onLogin,
  onRetry,
}: {
  checking: boolean;
  error: string;
  onLogin: (token: string) => Promise<void>;
  onRetry: () => void;
}) {
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState(false);
  const [localError, setLocalError] = useState("");
  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!token.trim()) return;
    setBusy(true);
    setLocalError("");
    try {
      await onLogin(token.trim());
      setToken("");
    } catch (error) {
      setLocalError(errorMessage(error));
    } finally {
      setBusy(false);
    }
  }
  return (
    <main className="welcome">
      <header>
        <Brand />
        <span className="welcome-local">
          <span className="status-dot" />
          На вашей машине
        </span>
      </header>
      <div className="welcome-layout">
        <section className="welcome-copy">
          <div className="welcome-avatars">
            <Avatar avatar="lavender" size={50} />
            <Avatar avatar="mint" size={44} />
            <Avatar avatar="peach" size={42} />
          </div>
          <span className="eyebrow">ВАША КОМАНДА. ВАШЕ ПРОСТРАНСТВО.</span>
          <h1>
            Дайте направление.
            <br />
            <span>Боты сделают остальное.</span>
          </h1>
          <p>
            Постоянные помощники с памятью и своими задачами.
            <br className="desktop-break" /> Вместе работают, делятся
            результатами и всегда на связи.
          </p>
          <div className="welcome-footnote">
            Codex & Pi <span>·</span> На сервере или вашем компьютере
          </div>
        </section>
        <form className="login-card" onSubmit={submit}>
          <div className="login-key">
            <KeyRound size={20} />
          </div>
          <h2>Откройте пространство</h2>
          <p>Введите ключ доступа этой установки.</p>
          <label htmlFor="access-token">Ключ доступа</label>
          <input
            id="access-token"
            value={token}
            onChange={(e) => setToken(e.target.value)}
            type="password"
            autoComplete="current-password"
            placeholder="Вставьте ключ"
            disabled={checking || busy}
          />
          <button
            type="submit"
            className="primary-button login-submit"
            disabled={!token.trim() || checking || busy}
          >
            {checking || busy ? (
              <LoaderCircle size={17} className="spin" />
            ) : (
              <>
                Войти <ArrowRight size={17} />
              </>
            )}
          </button>
          {(localError || error) && (
            <div className="notice is-error" role="alert">
              {localError || error}
              {error && !localError && (
                <button type="button" className="text-button" onClick={onRetry}>
                  <RefreshCw size={13} />
                  Повторить
                </button>
              )}
            </div>
          )}
          <small>Ваши боты и файлы остаются на вашей машине.</small>
        </form>
      </div>
      <footer>
        Connect Bots <span>Постоянные боты. Понятная работа.</span>
      </footer>
    </main>
  );
}
