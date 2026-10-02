import { useEffect, useRef } from "react";
import { LoaderCircle, Shrink } from "lucide-react";
import { errorMessage } from "../../lib/api";
import { contextNumbers } from "../../lib/contextState";
import type { useBotContext } from "../../hooks/useBotContext";
import { useIsPresent } from "../../lib/motion";

// Context lives in the quiet statusline below the composer. Native compaction
// is a direct action here; providers without it retain a read-only usage label.
export default function ContextControl({ state, busy, suspended = false, offline = false, onError }: {
  state: ReturnType<typeof useBotContext>;
  busy: boolean;
  suspended?: boolean;
  offline?: boolean;
  onError: (message: string) => void;
}) {
  const present = useIsPresent();
  const active = useRef(true);
  active.current = present && !suspended && !offline;
  useEffect(() => {
    active.current = present && !suspended && !offline;
    return () => { active.current = false; };
  }, [present, suspended, offline]);
  const { context, compacting, requesting, error, compact, supportsCompaction } = state;
  const { used, window: contextWindow, remaining, percent } = contextNumbers(context);
  const progressing = compacting || requesting;
  const disabled = !active.current || !context || busy || progressing;
  const usage = percent === undefined ? "—" : `${context?.estimated ? "≈" : ""}${Math.round(percent)}%`;
  const action = compacting ? "Compacting context…" : requesting ? "Requesting compaction…"
    : offline ? "Host is offline" : suspended || !present ? "Conversation is inactive"
      : busy ? "Compact context after the bot responds" : !context ? error || "Loading context status"
        : supportsCompaction ? "Compact context" : "Manual compaction is not supported by this provider";
  const title = [
    action,
    context?.estimated ? "Estimated context usage" : "",
    used === undefined ? "The provider has not reported context usage yet" : `${Math.round(used).toLocaleString("en-US")} tokens in context`,
    contextWindow === undefined ? "" : `Window: ${Math.round(contextWindow).toLocaleString("en-US")} tokens`,
    remaining === undefined ? "" : `Remaining: ${Math.round(remaining).toLocaleString("en-US")} tokens`,
  ].filter(Boolean).join("\n");
  const content = <>Context <b>{usage}</b>{progressing
    ? <LoaderCircle size={10} className="spin" aria-hidden="true" />
    : supportsCompaction && <Shrink size={10} aria-hidden="true" />}</>;
  return <span className={`statusline-context${progressing ? " is-compacting" : ""}`} title={title}>
    {supportsCompaction ? <button
      type="button"
      className="statusline-context-action"
      disabled={disabled}
      aria-label={`${action} · Context ${usage}`}
      aria-busy={progressing || undefined}
      onClick={() => {
        if (disabled || !active.current) return;
        void compact().catch(cause => { if (active.current) onError(errorMessage(cause)); });
      }}
    >{content}</button> : content}
  </span>;
}
