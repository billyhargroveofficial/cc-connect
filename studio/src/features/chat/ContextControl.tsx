import { useEffect, useId, useRef, useState } from "react";
import { Gauge, LoaderCircle, Shrink, X } from "lucide-react";
import { errorMessage } from "../../lib/api";
import { contextNumbers } from "../../lib/contextState";
import type { useBotContext } from "../../hooks/useBotContext";
import { PresenceSurface, useDialogFocus } from "./ModelPicker";
import {
  AnimatePresence, m, useIsPresent, useReducedMotion,
  controlMotion, fade, popoverMotion, motionTransition,
} from "../../lib/motion";

export default function ContextControl({ state, busy, suspended = false, onError }: {
  state: ReturnType<typeof useBotContext>;
  busy: boolean;
  suspended?: boolean;
  onError: (message: string) => void;
}) {
  const present = useIsPresent();
  const reducedMotion = useReducedMotion();
  const [open, setOpen] = useState(false);
  const trigger = useRef<HTMLButtonElement>(null);
  const dialog = useRef<HTMLDivElement>(null);
  const popoverId = useId();
  const { context, compacting, requesting, error, compact } = state;
  const { used, window: contextWindow, remaining, percent } = contextNumbers(context);
  function close() {
    setOpen(false);
    requestAnimationFrame(() => {
      if (present) trigger.current?.focus();
    });
  }
  useDialogFocus(open && present && !suspended, dialog, close);
  useEffect(() => {
    if (suspended) setOpen(false);
  }, [suspended]);
  const format = (value: number | undefined) => value === undefined
    ? "—"
    : Math.round(value).toLocaleString("en-US");
  const title = [
    context?.estimated ? "Estimated context usage" : "",
    used === undefined ? "The provider has not reported context usage yet" : `${Math.round(used).toLocaleString("en-US")} tokens in context`,
    contextWindow === undefined ? "" : `Window: ${Math.round(contextWindow).toLocaleString("en-US")} tokens`,
    remaining === undefined ? "" : `Remaining: ${Math.round(remaining).toLocaleString("en-US")} tokens`,
    error,
  ].filter(Boolean).join("\n");
  return <div className={`context-control ${compacting || requesting ? "is-compacting" : ""}`}>
    <m.button
      {...controlMotion}
      ref={trigger}
      className="context-trigger"
      title={title}
      aria-label={compacting ? "Compacting context" : requesting ? "Requesting compaction" : percent === undefined ? "Context usage is unknown. Open context" : `${percent.toFixed(1)}% of context used. Open context`}
      aria-expanded={open}
      aria-haspopup="dialog"
      aria-controls={open ? popoverId : undefined}
      onClick={() => setOpen(!open)}
    >
      <span className="context-indicator">
        <AnimatePresence initial={false} mode="popLayout">
          <m.span
            key={compacting || requesting ? "compacting" : "usage"}
            variants={fade}
            initial="hidden"
            animate="visible"
            exit="exit"
            transition={reducedMotion ? { duration: 0 } : motionTransition.quick}
          >
            {compacting || requesting ? <LoaderCircle size={13} className="spin" /> : <Gauge size={13} />}
          </m.span>
        </AnimatePresence>
      </span>
      <m.span key={compacting || requesting ? "compacting" : percent === undefined ? "unknown" : "reported"} variants={fade} initial="hidden" animate="visible">
        {compacting || requesting ? "…" : percent === undefined ? "—" : `${context?.estimated ? "≈ " : ""}${Math.round(percent)}%`}
      </m.span>
    </m.button>
    {open && present && !suspended && (
      <button className="popover-backdrop" tabIndex={-1} onClick={close} aria-label="Close context" />
    )}
    <AnimatePresence initial={false}>
      {open && present && !suspended && <PresenceSurface
        key="context"
        variants={reducedMotion ? fade : popoverMotion}
        initial="hidden" animate="visible" exit="exit"
        className="context-popover" ref={dialog} tabIndex={-1} id={popoverId} role="dialog" modal aria-label="Conversation context"
      >
        <header>
          <span>Context</span>
          <m.button {...controlMotion} className="icon-button" onClick={close} aria-label="Close context"><X size={15} /></m.button>
        </header>
        <dl>
          <div><dt>Used</dt><dd>{format(used)}</dd></div>
          <div><dt>Window size</dt><dd>{format(contextWindow)}</dd></div>
          <div><dt>Remaining</dt><dd>{format(remaining)}</dd></div>
        </dl>
        {(used === undefined || context?.estimated) && <p>{used === undefined ? error || "The provider has not reported context usage yet." : "Context usage is estimated by the provider."}</p>}
        <m.button
          {...controlMotion}
          className="compact-button"
          disabled={!context || busy || compacting || requesting}
          title={compacting ? "The provider is compacting context" : requesting ? "Requesting compaction" : busy ? "You can compact context after the bot responds" : !context ? error || "Loading context status" : "Compact context"}
          aria-label={compacting ? "Compacting context" : "Compact context"}
          onClick={() => {
            close();
            void compact().catch(cause => onError(errorMessage(cause)));
          }}
        >
          {compacting || requesting ? <LoaderCircle size={14} className="spin" /> : <Shrink size={14} />}
          <span>{compacting ? "Compacting context…" : requesting ? "Sending request…" : "Compact context"}</span>
        </m.button>
      </PresenceSurface>}
    </AnimatePresence>
  </div>;
}
