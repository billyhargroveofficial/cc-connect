import { useEffect, useId, useRef, useState } from "react";
import type { RefObject } from "react";
import { ChevronDown, Check, Zap, X, Cpu } from "lucide-react";
import type { Bot, Capabilities, Model } from "../../lib/types";
import { api, errorMessage } from "../../lib/api";

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
    const focusFirst = () => (controls()[0] || element).focus();
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
        element.focus();
      } else if (!element.contains(focused) || focused === element) {
        event.preventDefault();
        (event.shiftKey ? last : first)?.focus();
      } else if (event.shiftKey && focused === first) {
        event.preventDefault();
        last?.focus();
      } else if (!event.shiftKey && focused === last) {
        event.preventDefault();
        first.focus();
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
  bot,
  capabilities,
  disabled,
  onBotChange,
  onOpenChange,
  onError,
}: {
  bot: Bot;
  capabilities: Capabilities | null;
  disabled: boolean;
  onBotChange: (bot: Bot) => void;
  onOpenChange?: (open: boolean) => void;
  onError: (error: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [saving, setSaving] = useState(false);
  const trigger = useRef<HTMLButtonElement>(null);
  const dialog = useRef<HTMLDivElement>(null);
  const popoverId = useId();
  function updateOpen(next: boolean) {
    setOpen(next);
    onOpenChange?.(next);
  }
  function close() {
    updateOpen(false);
    requestAnimationFrame(() => trigger.current?.focus());
  }
  useDialogFocus(open, dialog, close);
  const models = capabilities?.models || [];
  const current = models.find(
    (model) => model.backend === bot.backend && model.id === bot.model,
  );
  const efforts = current?.efforts || [];
  const confirmedEffort = efforts.includes(bot.effort);
  const serviceTiers = current?.serviceTiers || [];
  const selectedTier = serviceTiers.find((tier) => tier.id === bot.serviceTier);
  const autoTier = serviceTiers.find((tier) => tier.id === current?.defaultServiceTier);
  const tierName = selectedTier?.name || bot.serviceTier || "Auto";
  const showServiceTier = serviceTiers.length > 0 || (bot.backend === "codex" && !!bot.serviceTier);
  async function choose(model: Model) {
    const backend = capabilities?.backends[model.backend];
    if (!backend?.available || disabled || saving) return;
    setSaving(true);
    try {
      const effort = model.efforts.includes(bot.effort)
        ? bot.effort
        : model.efforts.includes("max")
          ? "max"
          : model.efforts.at(-1) || "";
      onBotChange(
        await api.updateBot(bot.id, {
          backend: model.backend,
          model: model.id,
          effort,
          serviceTier: model.serviceTiers?.some((tier) => tier.id === bot.serviceTier)
            ? bot.serviceTier
            : "",
        }),
      );
      close();
    } catch (error) {
      onError(errorMessage(error));
    } finally {
      setSaving(false);
    }
  }
  async function changeEffort(effort: string) {
    if (disabled || saving) return;
    setSaving(true);
    try {
      onBotChange(await api.updateBot(bot.id, { effort }));
    } catch (error) {
      onError(errorMessage(error));
    } finally {
      setSaving(false);
    }
  }
  async function changeServiceTier(serviceTier: string) {
    if (disabled || saving || (serviceTier && !serviceTiers.some((tier) => tier.id === serviceTier))) return;
    setSaving(true);
    try {
      onBotChange(await api.updateBot(bot.id, { serviceTier }));
    } catch (error) {
      onError(errorMessage(error));
    } finally {
      setSaving(false);
    }
  }
  return (
    <div className="model-controls model-controls-in-menu">
      <button
        className="model-trigger"
        ref={trigger}
        onClick={() => updateOpen(!open)}
        disabled={saving || disabled}
        aria-expanded={open}
        aria-haspopup="dialog"
        aria-controls={open ? popoverId : undefined}
        aria-label={`Model: ${current?.name || bot.model || bot.backend}${confirmedEffort ? `. Reasoning effort: ${bot.effort}` : ""}${showServiceTier ? `. Service tier: ${tierName}` : ""}`}
        title={
          disabled
            ? "You can change the model after the turn finishes"
            : "Model, effort and service tier"
        }
      >
        <Cpu size={16} />
        <span className="model-trigger-label">
          <strong>Model settings</strong>
          <small>
            <span className="model-current-name">{current?.name || bot.model || bot.backend}</span>
            {confirmedEffort && (
              <span className={`model-current-effort ${bot.effort === "ultra" ? "is-ultra" : ""}`}>
                {bot.effort === "ultra" && <Zap size={11} />}
                {bot.effort === "ultra" ? "Ultra" : bot.effort}
              </span>
            )}
            {showServiceTier && <span className="model-current-tier">{tierName}</span>}
          </small>
        </span>
        <ChevronDown size={12} />
      </button>
      {open && (
        <>
          <button
            className="popover-backdrop"
            tabIndex={-1}
            onClick={close}
            aria-label="Close model picker"
          />
          <div
            className="model-popover"
            ref={dialog}
            tabIndex={-1}
            id={popoverId}
            role="dialog"
            aria-modal="true"
            aria-label="Model, effort and service tier"
          >
            <header>
              <span>Model</span>
              <button
                className="icon-button"
                onClick={close}
                aria-label="Close"
              >
                <X size={15} />
              </button>
            </header>
            {(efforts.length > 0 || showServiceTier) && <div className={`model-settings ${efforts.length && showServiceTier ? "has-two-settings" : ""}`}>
            {efforts.length > 0 && (
              <label className="model-setting">
                <span>Effort</span>
                <select
                  aria-label="Reasoning effort"
                  value={confirmedEffort ? bot.effort : ""}
                  onChange={(event) => void changeEffort(event.target.value)}
                  disabled={disabled || saving}
                >
                  {!confirmedEffort && <option value="" disabled>—</option>}
                  {efforts.map((effort) => (
                    <option key={effort} value={effort}>
                      {effort === "ultra" ? "Ultra" : effort}
                    </option>
                  ))}
                </select>
              </label>
            )}
            {showServiceTier && (
              <label className="model-setting">
                <span>Service tier</span>
                <select
                  aria-label="Service tier"
                  value={bot.serviceTier || ""}
                  title={selectedTier?.description || (autoTier ? `Auto: ${autoTier.name}. ${autoTier.description}` : "Service tier for the next response")}
                  onChange={(event) => void changeServiceTier(event.target.value)}
                  disabled={disabled || saving}
                >
                  <option value="">Auto</option>
                  {bot.serviceTier && !selectedTier && <option value={bot.serviceTier} disabled>{bot.serviceTier} (unavailable)</option>}
                  {serviceTiers.map((tier) => <option key={tier.id} value={tier.id} title={tier.description}>{tier.name || tier.id}</option>)}
                </select>
              </label>
            )}
            </div>}
            {[
              "codex",
              "pi",
              ...new Set(
                models
                  .map((model) => model.backend)
                  .filter((b) => b !== "codex" && b !== "pi"),
              ),
            ].map((backend) => {
              const available = capabilities?.backends[backend];
              const choices = models.filter(
                (model) => model.backend === backend,
              );
              return (
                <section key={backend}>
                  <div className="model-group-label">
                    <Cpu size={12} />
                    {backend === "codex"
                      ? "Codex"
                      : backend === "pi"
                        ? "Pi · DeepSeek"
                        : backend}
                  </div>
                  {!available?.available && (
                    <p className="model-unavailable">
                      {available?.reason || "Unavailable in this installation"}
                    </p>
                  )}
                  {choices.map((model) => (
                    <button
                      key={`${model.backend}:${model.id}`}
                      className="model-choice"
                      aria-pressed={bot.backend === model.backend && bot.model === model.id}
                      disabled={!available?.available || disabled || saving}
                      onClick={() => void choose(model)}
                    >
                      <span>
                        <strong>{model.name || model.id}{model.efforts.includes("ultra") && <Zap size={11} aria-label="Supports Ultra" />}</strong>
                      </span>
                      {bot.backend === model.backend &&
                        bot.model === model.id && <Check size={16} />}
                    </button>
                  ))}
                </section>
              );
            })}
            <footer>Conversation history is preserved when you change models.</footer>
          </div>
        </>
      )}
    </div>
  );
}
