import { useEffect, useRef, useState } from "react";
import { ArrowRight, UserRound, LoaderCircle, RefreshCw } from "lucide-react";
import Brand from "./Brand";
import Avatar from "./Avatar";
import { errorMessage } from "../lib/api";
import type { AccountCredentials } from "../lib/api";
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

export function validateAccountInput(
  username: string,
  password: string,
  confirmation?: string,
) {
  return accountValidation(username, password, confirmation)?.message || "";
}

function accountValidation(username: string, password: string, confirmation?: string): {
  field: "username" | "password" | "confirmation";
  message: string;
} | null {
  if (!/^[a-z0-9][a-z0-9._-]{2,31}$/.test(username.trim().toLowerCase()))
    return { field: "username", message: "Use 3–32 letters, numbers, dots, underscores, or hyphens. Start with a letter or number." };
  const passwordBytes = new TextEncoder().encode(password).length;
  if (passwordBytes < 8 || passwordBytes > 128)
    return { field: "password", message: "Use a password between 8 and 128 bytes." };
  if (confirmation !== undefined && password !== confirmation)
    return { field: "confirmation", message: "Your passwords do not match." };
  return null;
}

function LoginNotice({
  message,
  retry,
  id,
}: {
  message: string;
  retry?: () => void;
  id?: string;
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
      <div className="notice is-error" role="alert" id={id}>
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

function LoginConfirmation({ value, pending, invalid, describedBy, onChange }: {
  value: string;
  pending: boolean;
  invalid?: boolean;
  describedBy?: string;
  onChange: (value: string) => void;
}) {
  const present = useIsPresent();
  const reduced = useReducedMotion();
  return (
    <m.div
      className="login-confirm"
      initial={{ height: 0, opacity: 0 }}
      animate={{ height: "auto", opacity: 1 }}
      exit={{ height: 0, opacity: 0 }}
      transition={reduced ? { duration: 0 } : motionTransition.disclosure}
      inert={!present}
      aria-hidden={!present || undefined}
    >
      <label htmlFor="account-confirmation">Confirm password</label>
      <input
        id="account-confirmation"
        name="password-confirmation"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        type="password"
        autoComplete="new-password"
        placeholder="Repeat your password"
        disabled={pending || !present}
        required
        aria-invalid={invalid || undefined}
        aria-describedby={describedBy}
      />
    </m.div>
  );
}

export default function Login({
  checking,
  error,
  registrationAllowed,
  legacyClaimAvailable,
  setupRequired,
  onLogin,
  onRegister,
  onRetry,
}: {
  checking: boolean;
  error: string;
  registrationAllowed: boolean;
  legacyClaimAvailable?: boolean;
  setupRequired?: boolean;
  onLogin: (credentials: AccountCredentials) => Promise<void>;
  onRegister: (credentials: AccountCredentials) => Promise<void>;
  onRetry: () => void;
}) {
  const present = useIsPresent();
  const [tab, setTab] = useState<"login" | "register">((legacyClaimAvailable || setupRequired) && registrationAllowed ? "register" : "login");
  const interacted = useRef(false);
  const registering = tab === "register" && registrationAllowed;
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [busy, setBusy] = useState(false);
  const submitting = useRef(false);
  const [localError, setLocalError] = useState("");
  const [invalidField, setInvalidField] = useState<"username" | "password" | "confirmation" | null>(null);
  const pending = checking || busy;
  const canSubmit = !!username.trim() && !!password && (!registering || !!confirmation);
  useEffect(() => {
    if (!interacted.current && (legacyClaimAvailable || setupRequired) && registrationAllowed) setTab("register");
  }, [legacyClaimAvailable, setupRequired, registrationAllowed]);
  function switchTab(next: "login" | "register") {
    if (pending) return;
    interacted.current = true;
    setTab(next);
    setPassword("");
    setConfirmation("");
    setLocalError("");
    setInvalidField(null);
  }
  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (pending || submitting.current) return;
    const validation = accountValidation(username, password, registering ? confirmation : undefined);
    if (validation) {
      setLocalError(validation.message);
      setInvalidField(validation.field);
      return;
    }
    submitting.current = true;
    setBusy(true);
    setLocalError("");
    setInvalidField(null);
    try {
      const credentials = { username: username.trim().toLowerCase(), password };
      await (registering ? onRegister(credentials) : onLogin(credentials));
      setPassword("");
      setConfirmation("");
    } catch (error) {
      setLocalError(errorMessage(error));
    } finally {
      submitting.current = false;
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
        <m.form className="login-card" onSubmit={submit} variants={fadeUp} noValidate>
          <div className="login-key">
            <UserRound size={20} />
          </div>
          <h2>{registering ? "Create your workspace" : "Welcome back"}</h2>
          <p>{registering ? "A private space for your bots and files." : "Sign in to your bots and conversations."}</p>
          {registrationAllowed && (
            <div className="login-tabs" role="tablist" aria-label="Account access">
              {(["login", "register"] as const).map((mode) => (
                <m.button
                  key={mode}
                  type="button"
                  role="tab"
                  id={`account-${mode}-tab`}
                  aria-selected={(registering ? "register" : "login") === mode}
                  aria-controls="account-fields"
                  disabled={pending}
                  onClick={() => switchTab(mode)}
                  data-motion-control
                  {...(pending ? {} : controlMotion)}
                >
                  {(registering ? "register" : "login") === mode && (
                    <m.span className="login-tab-active" layoutId="account-tab" transition={motionTransition.enter} aria-hidden="true" />
                  )}
                  <span>{mode === "login" ? "Sign in" : "Create account"}</span>
                </m.button>
              ))}
            </div>
          )}
          <div className="login-fields" id="account-fields" role={registrationAllowed ? "tabpanel" : undefined} aria-labelledby={registrationAllowed ? `account-${registering ? "register" : "login"}-tab` : undefined}>
            <label htmlFor="account-username">Username</label>
            <input
              id="account-username"
              name="username"
              value={username}
              onChange={(e) => { interacted.current = true; setUsername(e.target.value); }}
              type="text"
              autoComplete="username"
              autoCapitalize="none"
              autoCorrect="off"
              spellCheck={false}
              placeholder="Your username"
              maxLength={32}
              disabled={pending}
              required
              aria-invalid={invalidField === "username" || undefined}
              aria-describedby={localError || error ? "account-error" : undefined}
            />
            <label htmlFor="account-password">Password</label>
            <input
              id="account-password"
              name="password"
              value={password}
              onChange={(e) => { interacted.current = true; setPassword(e.target.value); }}
              type="password"
              autoComplete={registering ? "new-password" : "current-password"}
              placeholder={registering ? "At least 8 characters" : "Your password"}
              disabled={pending}
              required
              aria-invalid={invalidField === "password" || undefined}
              aria-describedby={localError || error ? "account-error" : undefined}
            />
            <AnimatePresence initial={false}>
              {registering && (
                <LoginConfirmation
                  key="confirm-password"
                  value={confirmation}
                  pending={pending}
                  invalid={invalidField === "confirmation"}
                  describedBy={localError || error ? "account-error" : undefined}
                  onChange={(value) => { interacted.current = true; setConfirmation(value); }}
                />
              )}
            </AnimatePresence>
          </div>
          <m.button
            type="submit"
            className="primary-button login-submit"
            disabled={!canSubmit || pending}
            aria-busy={pending}
            aria-label={
              checking ? "Checking connection" : registering ? busy ? "Creating account" : "Create account" : busy ? "Signing in" : "Sign in"
            }
            data-motion-control
            {...(!canSubmit || pending ? {} : controlMotion)}
          >
            <m.span
              className={`login-submit-content${pending ? " is-busy" : ""}`}
              animate={{ opacity: 1 }}
              transition={motionTransition.quick}
            >
              {pending ? (
                <>
                  <LoaderCircle size={17} className="spin" />
                  <span>{checking ? "Checking…" : registering ? "Creating account…" : "Signing in…"}</span>
                </>
              ) : (
                <>
                  {registering ? "Create account" : "Sign in"} <ArrowRight size={17} />
                </>
              )}
            </m.span>
          </m.button>
          <AnimatePresence initial={false}>
            {(localError || error) && (
              <LoginNotice
                key="login-error"
                id="account-error"
                message={localError || error}
                retry={error && !localError ? onRetry : undefined}
              />
            )}
          </AnimatePresence>
          <small>{registering && legacyClaimAvailable ? "This account will keep this installation’s existing bots and files." : "Your bots and files stay in your own workspace."}</small>
        </m.form>
      </m.div>
      <m.footer variants={fade}>
        Connect Bots <span>Persistent bots. Clear workflows.</span>
      </m.footer>
    </m.main>
  );
}
