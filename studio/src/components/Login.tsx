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
          On your machine
        </span>
      </header>
      <div className="welcome-layout">
        <section className="welcome-copy">
          <div className="welcome-avatars">
            <Avatar avatar="lavender" size={50} />
            <Avatar avatar="mint" size={44} />
            <Avatar avatar="peach" size={42} />
          </div>
          <span className="eyebrow">YOUR TEAM. YOUR WORKSPACE.</span>
          <h1>
            Give them direction.
            <br />
            <span>Bots take it from there.</span>
          </h1>
          <p>
            Persistent assistants with memory and tasks of their own.
            <br className="desktop-break" /> They work together, share
            results, and stay connected.
          </p>
          <div className="welcome-footnote">
            Codex & Pi <span>·</span> On your server or computer
          </div>
        </section>
        <form className="login-card" onSubmit={submit}>
          <div className="login-key">
            <KeyRound size={20} />
          </div>
          <h2>Open your workspace</h2>
          <p>Enter the access key for this installation.</p>
          <label htmlFor="access-token">Access key</label>
          <input
            id="access-token"
            value={token}
            onChange={(e) => setToken(e.target.value)}
            type="password"
            autoComplete="current-password"
            placeholder="Paste your key"
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
                Sign in <ArrowRight size={17} />
              </>
            )}
          </button>
          {(localError || error) && (
            <div className="notice is-error" role="alert">
              {localError || error}
              {error && !localError && (
                <button type="button" className="text-button" onClick={onRetry}>
                  <RefreshCw size={13} />
                  Retry
                </button>
              )}
            </div>
          )}
          <small>Your bots and files stay on your machine.</small>
        </form>
      </div>
      <footer>
        Connect Bots <span>Persistent bots. Clear workflows.</span>
      </footer>
    </main>
  );
}
