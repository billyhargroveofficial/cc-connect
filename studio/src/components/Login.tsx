import { useState } from "react";
import { ArrowRight, KeyRound, LoaderCircle, RefreshCw } from "lucide-react";
import Brand from "./Brand";
import Avatar from "./Avatar";
import { errorMessage } from "../lib/api";
import {
  AnimatePresence,
  controlMotion,
  fade,
  fadeUp,
  m,
  motionTransition,
  useIsPresent,
  useReducedMotion,
} from "../lib/motion";

const welcomeReveal = {
  hidden: {},
  visible: { transition: { staggerChildren: 0.055, delayChildren: 0.025 } },
};

function LoginNotice({
  message,
  retry,
}: {
  message: string;
  retry?: () => void;
}) {
  const present = useIsPresent();
  const reduced = useReducedMotion();
  return (
    <m.div
      className="login-notice"
      initial={{ height: 0, opacity: 0 }}
      animate={{ height: "auto", opacity: 1 }}
      exit={{ height: 0, opacity: 0 }}
      transition={reduced ? { duration: 0 } : motionTransition.disclosure}
      inert={!present}
      aria-hidden={!present || undefined}
    >
      <div className="notice is-error" role="alert">
        {message}
        {retry && (
          <m.button
            type="button"
            className="text-button"
            onClick={retry}
            data-motion-control
            {...controlMotion}
          >
            <RefreshCw size={13} />
            Retry
          </m.button>
        )}
      </div>
    </m.div>
  );
}

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
  const present = useIsPresent();
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState(false);
  const [localError, setLocalError] = useState("");
  const pending = checking || busy;
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
    <m.main
      className="welcome"
      variants={fade}
      initial="hidden"
      animate="visible"
      exit="exit"
      inert={!present}
      aria-hidden={!present || undefined}
    >
      <m.header variants={fadeUp}>
        <Brand />
        <span className="welcome-local">
          <span className="status-dot" />
          On your machine
        </span>
      </m.header>
      <m.div className="welcome-layout" variants={welcomeReveal}>
        <m.section className="welcome-copy" variants={welcomeReveal}>
          <m.div className="welcome-avatars" variants={welcomeReveal}>
            <m.div variants={fadeUp}>
              <Avatar avatar="lavender" size={50} />
            </m.div>
            <m.div variants={fadeUp}>
              <Avatar avatar="mint" size={44} />
            </m.div>
            <m.div variants={fadeUp}>
              <Avatar avatar="peach" size={42} />
            </m.div>
          </m.div>
          <m.span className="eyebrow" variants={fadeUp}>
            YOUR TEAM. YOUR WORKSPACE.
          </m.span>
          <m.h1 variants={fadeUp}>
            Give them direction.
            <br />
            <span>Bots take it from there.</span>
          </m.h1>
          <m.p variants={fadeUp}>
            Persistent assistants with memory and tasks of their own.
            <br className="desktop-break" /> They work together, share results,
            and stay connected.
          </m.p>
          <m.div className="welcome-footnote" variants={fadeUp}>
            Codex & Pi <span>·</span> On your server or computer
          </m.div>
        </m.section>
        <m.form className="login-card" onSubmit={submit} variants={fadeUp}>
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
            disabled={pending}
          />
          <m.button
            type="submit"
            className="primary-button login-submit"
            disabled={!token.trim() || pending}
            aria-busy={pending}
            aria-label={
              checking ? "Checking connection" : busy ? "Signing in" : "Sign in"
            }
            data-motion-control
            {...(!token.trim() || pending ? {} : controlMotion)}
          >
            <AnimatePresence initial={false} mode="wait">
              <m.span
                key={pending ? "pending" : "ready"}
                className={`login-submit-content${pending ? " is-busy" : ""}`}
                variants={fade}
                initial="hidden"
                animate="visible"
                exit="exit"
                transition={motionTransition.quick}
              >
                {pending ? (
                  <>
                    <LoaderCircle size={17} className="spin" />
                    <span>{checking ? "Checking…" : "Signing in…"}</span>
                  </>
                ) : (
                  <>
                    Sign in <ArrowRight size={17} />
                  </>
                )}
              </m.span>
            </AnimatePresence>
          </m.button>
          <AnimatePresence initial={false}>
            {(localError || error) && (
              <LoginNotice
                key="login-error"
                message={localError || error}
                retry={error && !localError ? onRetry : undefined}
              />
            )}
          </AnimatePresence>
          <small>Your bots and files stay on your machine.</small>
        </m.form>
      </m.div>
      <m.footer variants={fade}>
        Connect Bots <span>Persistent bots. Clear workflows.</span>
      </m.footer>
    </m.main>
  );
}
