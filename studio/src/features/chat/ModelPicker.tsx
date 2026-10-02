import { useEffect, useId, useRef, useState } from "react";
import type { ComponentProps, CSSProperties, RefObject } from "react";
import { ArrowLeft, ChevronDown, ChevronRight, Check, Zap, X, RotateCcw } from "lucide-react";
import { effortLabel } from "../../lib/chatStatus";
import type { Bot, Capabilities, Model } from "../../lib/types";
import { api, errorMessage } from "../../lib/api";
import {
  AnimatePresence, m, useIsPresent, useReducedMotion,
  controlMotion, fade, popoverMotion, motionTransition,
} from "../../lib/motion";

// Presence keeps a closing surface in the DOM for its exit animation. Remove
// its interaction and modal semantics as soon as it leaves the active UI.
export function PresenceSurface({ modal, ...props }: ComponentProps<typeof m.div> & { modal?: boolean }) {
  const present = useIsPresent();
  return <m.div
    {...props}
    inert={!present}
    aria-hidden={!present || undefined}
    aria-modal={present && modal ? true : undefined}
    style={{ ...props.style, pointerEvents: present ? props.style?.pointerEvents : "none" }}
  />;
}

export function useDialogFocus(
  active: boolean,
  dialog: RefObject<HTMLDivElement | null>,
  onClose: () => void,
) {
  const close = useRef(onClose);
  close.current = onClose;
  useEffect(() => {
    const element = dialog.current;
    if (!active || !element) return;
    const controls = () => Array.from(element.querySelectorAll<HTMLElement>(
      'button:not([disabled]), a[href], input:not([disabled]):not([type="hidden"]), textarea:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])',
    )).filter(control => control.tabIndex >= 0 && control.getClientRects().length > 0);
    const focusFirst = () => (controls()[0] || element).focus({ preventScroll: true });
    const keydown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        event.stopPropagation();
        close.current();
        return;
      }
      if (event.key !== "Tab") return;
      const items = controls();
      const first = items[0], last = items.at(-1);
      const focused = document.activeElement;
      if (!first) {
        event.preventDefault();
        element.focus({ preventScroll: true });
      } else if (!element.contains(focused) || focused === element) {
        event.preventDefault();
        (event.shiftKey ? last : first)?.focus({ preventScroll: true });
      } else if (event.shiftKey && focused === first) {
        event.preventDefault();
        last?.focus({ preventScroll: true });
      } else if (!event.shiftKey && focused === last) {
        event.preventDefault();
        first.focus({ preventScroll: true });
      }
    };
    const focusin = (event: FocusEvent) => {
      if (!element.contains(event.target as Node | null)) focusFirst();
    };
    focusFirst();
    window.addEventListener("keydown", keydown, true);
    document.addEventListener("focusin", focusin, true);
    return () => {
      window.removeEventListener("keydown", keydown, true);
      document.removeEventListener("focusin", focusin, true);
    };
  }, [active, dialog]);
}

export default function ModelPicker({
  bot, capabilities, disabled, suspended = false, onBotChange, onOpenChange, onError,
}: {
  bot: Bot;
  capabilities: Capabilities | null;
  disabled: boolean;
  suspended?: boolean;
  onBotChange: (bot: Bot) => void;
  onOpenChange?: (open: boolean) => void;
  onError: (error: string) => void;
}) {
  const present = useIsPresent();
  const reducedMotion = useReducedMotion();
  const [open, setOpen] = useState(false);
  const [modelsOpen, setModelsOpen] = useState(false);
  const [saving, setSaving] = useState(false);
  const [previewEffort, setPreviewEffort] = useState(bot.effort);
  const mutation = useRef(false);
  const committedEffort = useRef(bot.effort);
  const trigger = useRef<HTMLButtonElement>(null);
  const dialog = useRef<HTMLDivElement>(null);
  const popoverId = useId();
  function updateOpen(next: boolean) {
    setOpen(next);
    if (next) setModelsOpen(false);
    onOpenChange?.(next);
  }
  function close() {
    updateOpen(false);
    requestAnimationFrame(() => {
      if (present && !suspended) trigger.current?.focus({ preventScroll: true });
    });
  }
  useDialogFocus(open && present && !suspended, dialog, close);
  useEffect(() => {
    if (suspended) updateOpen(false);
  }, [suspended]);
  useEffect(() => { setPreviewEffort(bot.effort); committedEffort.current = bot.effort; }, [bot.effort, bot.model, bot.backend]);
  const models = capabilities?.models || [];
  const current = models.find(model => model.backend === bot.backend && model.id === bot.model);
  const efforts = current?.efforts || [];
  const confirmedEffort = efforts.includes(bot.effort);
  const selectedEffort = efforts.includes(previewEffort) ? previewEffort : confirmedEffort ? bot.effort : efforts[0] || "";
  const serviceTiers = current?.serviceTiers || [];
  const selectedTier = serviceTiers.find(tier => tier.id === bot.serviceTier);
  const autoTier = serviceTiers.find(tier => tier.id === current?.defaultServiceTier);
  const tierName = selectedTier?.name || bot.serviceTier || "Auto";
  const showServiceTier = serviceTiers.length > 0 || (bot.backend === "codex" && !!bot.serviceTier);
  const modelName = current?.name || bot.model || bot.backend;
  const defaultEffort = efforts.includes("max") ? "max" : efforts.at(-1) || "";
  const effortIndex = Math.max(0, efforts.indexOf(selectedEffort));
  const canChange = !disabled && !saving && present && !suspended;
  async function choose(model: Model) {
    if (!capabilities?.backends[model.backend]?.available || !canChange || mutation.current) return;
    mutation.current = true;
    setSaving(true);
    try {
      const effort = model.efforts.includes(bot.effort) ? bot.effort
        : model.efforts.includes("max") ? "max" : model.efforts.at(-1) || "";
      const updated = await api.updateBot(bot.id, {
        backend: model.backend, model: model.id, effort,
        serviceTier: model.serviceTiers?.some(tier => tier.id === bot.serviceTier) ? bot.serviceTier : "",
      });
      committedEffort.current = updated.effort;
      onBotChange(updated);
      close();
    } catch (error) { onError(errorMessage(error)); }
    finally { mutation.current = false; setSaving(false); }
  }
  async function changeEffort(effort: string) {
    if (!canChange || mutation.current || !efforts.includes(effort) || effort === committedEffort.current) return;
    mutation.current = true;
    setSaving(true);
    try { const updated = await api.updateBot(bot.id, { effort }); committedEffort.current = effort; onBotChange(updated); }
    catch (error) { setPreviewEffort(bot.effort); onError(errorMessage(error)); }
    finally { mutation.current = false; setSaving(false); }
  }
  async function changeServiceTier(serviceTier: string) {
    if (!canChange || mutation.current || (serviceTier && !serviceTiers.some(tier => tier.id === serviceTier))) return;
    mutation.current = true;
    setSaving(true);
    try { onBotChange(await api.updateBot(bot.id, { serviceTier })); }
    catch (error) { onError(errorMessage(error)); }
    finally { mutation.current = false; setSaving(false); }
  }
  async function reset() {
    if (!canChange || mutation.current) return;
    mutation.current = true;
    setSaving(true);
    try {
      onBotChange(await api.updateBot(bot.id, { effort: defaultEffort, serviceTier: "" }));
      setPreviewEffort(defaultEffort);
      committedEffort.current = defaultEffort;
    } catch (error) { onError(errorMessage(error)); }
    finally { mutation.current = false; setSaving(false); }
  }
  const commitRange = (value: string) => { const effort = efforts[Number(value)]; if (effort) void changeEffort(effort); };
  return <div className="model-controls model-controls-direct">
    <m.button {...controlMotion} className="model-trigger" ref={trigger}
      onClick={() => updateOpen(!open)} disabled={saving || disabled || suspended}
      aria-expanded={open} aria-haspopup="dialog" aria-controls={open ? popoverId : undefined}
      aria-label={`Model: ${modelName}${confirmedEffort ? `. Reasoning effort: ${effortLabel(bot.effort)}` : ""}${showServiceTier ? `. Service tier: ${tierName}` : ""}`}
      title={disabled ? "You can change inference settings after the turn finishes" : "Model, effort and service tier"}>
      <Zap size={14} className={bot.serviceTier === "fast" ? "is-fast" : ""} fill={bot.serviceTier === "fast" ? "currentColor" : "none"} />
      <span className="model-current-name">{modelName}</span>
      {confirmedEffort && <span className={`model-current-effort ${bot.effort === "ultra" ? "is-ultra" : ""}`}>{effortLabel(bot.effort)}</span>}
      {showServiceTier && <span className="model-current-tier">{tierName}</span>}
      <m.span className="model-trigger-chevron" animate={{ rotate: open ? 180 : 0 }} transition={reducedMotion ? { duration: 0 } : motionTransition.quick}><ChevronDown size={12} /></m.span>
    </m.button>
    {open && present && !suspended && <button className="popover-backdrop" tabIndex={-1} onClick={close} aria-label="Close model picker" />}
    <AnimatePresence initial={false}>
      {open && present && !suspended && <PresenceSurface key="model-picker" variants={reducedMotion ? fade : popoverMotion}
        initial="hidden" animate="visible" exit="exit" className={`model-popover ${modelsOpen ? "is-model-list" : "is-effort-picker"}`}
        ref={dialog} tabIndex={-1} id={popoverId} role="dialog" modal aria-label="Model, effort and service tier">
        {modelsOpen ? <>
          <header><span>Select model</span><m.button {...controlMotion} className="icon-button" onClick={() => setModelsOpen(false)} aria-label="Back to effort"><ArrowLeft size={16} /></m.button></header>
          {["codex", "pi", ...new Set(models.map(model => model.backend).filter(backend => backend !== "codex" && backend !== "pi"))].map(backend => {
            const available = capabilities?.backends[backend];
            const choices = models.filter(model => model.backend === backend);
            if (!choices.length && !available) return null;
            return <section key={backend}>
              <div className="model-group-label">{backend === "codex" ? "Codex" : backend === "pi" ? "Pi · DeepSeek" : backend}</div>
              {!available?.available && <p className="model-unavailable">{available?.reason || "Unavailable in this installation"}</p>}
              {choices.map(model => <m.button {...controlMotion} key={`${model.backend}:${model.id}`} className="model-choice"
                aria-pressed={bot.backend === model.backend && bot.model === model.id} disabled={!available?.available || disabled || saving}
                onClick={() => void choose(model)}>
                <span><strong>{model.name || model.id}{model.efforts.includes("ultra") && <Zap size={11} aria-label="Supports Ultra" />}</strong></span>
                {bot.backend === model.backend && bot.model === model.id && <Check size={15} />}
              </m.button>)}
            </section>;
          })}
        </> : <>
          <div className="inference-heading">
            <Zap size={19} className={bot.serviceTier === "fast" ? "is-fast" : ""} />
            <m.button {...controlMotion} className="model-select-current" onClick={() => setModelsOpen(true)}>
              <strong>{efforts.length ? effortLabel(selectedEffort) : "Select model"}</strong>
              <span>{modelName}<ChevronRight size={12} /></span>
            </m.button>
            <m.button {...controlMotion} className="icon-button inference-reset" onClick={() => void reset()} disabled={!canChange}
              aria-label="Reset effort and service tier" title="Reset inference preferences"><RotateCcw size={17} /></m.button>
          </div>
          {efforts.length > 0 && <div className="effort-slider-control">
            <div className="effort-slider-ticks" aria-hidden="true">{efforts.map(effort => <span key={effort} className={effort === selectedEffort ? "is-current" : ""} />)}</div>
            <input type="range" min={0} max={Math.max(0, efforts.length - 1)} step={1} value={effortIndex}
              style={{ "--effort-progress": `${efforts.length > 1 ? effortIndex / (efforts.length - 1) * 100 : 0}%` } as CSSProperties}
              aria-label="Reasoning effort" aria-valuetext={effortLabel(selectedEffort)} disabled={!canChange || efforts.length < 2}
              onChange={event => setPreviewEffort(efforts[Number(event.target.value)] || bot.effort)}
              onPointerUp={event => commitRange(event.currentTarget.value)}
              onKeyUp={event => { if (["ArrowLeft", "ArrowRight", "ArrowUp", "ArrowDown", "Home", "End", "PageUp", "PageDown"].includes(event.key)) commitRange(event.currentTarget.value); }}
              onBlur={event => commitRange(event.currentTarget.value)} />
            <div className="effort-slider-labels"><span>{effortLabel(efforts[0])}</span><span>{effortLabel(efforts.at(-1) || "")}</span></div>
          </div>}
          {showServiceTier && <label className="model-tier-setting"><span><Zap size={12} />Service tier</span>
            <select aria-label="Service tier" value={bot.serviceTier || ""} disabled={!canChange}
              title={selectedTier?.description || (autoTier ? `Auto: ${autoTier.name}. ${autoTier.description}` : "Service tier for the next response")}
              onChange={event => void changeServiceTier(event.target.value)}>
              <option value="">Auto</option>
              {bot.serviceTier && !selectedTier && <option value={bot.serviceTier} disabled>{bot.serviceTier} (unavailable)</option>}
              {serviceTiers.map(tier => <option key={tier.id} value={tier.id} title={tier.description}>{tier.name || tier.id}</option>)}
            </select>
          </label>}
          <m.button {...controlMotion} className="model-more-choice" onClick={() => setModelsOpen(true)}>Choose model<ChevronRight size={14} /></m.button>
        </>}
        <m.button {...controlMotion} className="model-popover-close" onClick={close} aria-label="Close"><X size={14} /></m.button>
      </PresenceSurface>}
    </AnimatePresence>
  </div>;
}
