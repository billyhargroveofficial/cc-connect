import { useId, useRef, useState } from "react";
import { Gauge, LoaderCircle, Shrink, X } from "lucide-react";
import { errorMessage } from "../../lib/api";
import { contextNumbers } from "../../lib/contextState";
import type { useBotContext } from "../../hooks/useBotContext";
import { useDialogFocus } from "./ModelPicker";

export default function ContextControl({ state, busy, onError }: {
  state: ReturnType<typeof useBotContext>;
  busy: boolean;
  onError: (message: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const trigger = useRef<HTMLButtonElement>(null);
  const dialog = useRef<HTMLDivElement>(null);
  const popoverId = useId();
  const { context, compacting, requesting, error, compact } = state;
  const { used, window: contextWindow, remaining, percent } = contextNumbers(context);
  function close() {
    setOpen(false);
    requestAnimationFrame(() => trigger.current?.focus());
  }
  useDialogFocus(open, dialog, close);
  const format = (value: number | undefined) => value === undefined
    ? "—"
    : Math.round(value).toLocaleString("ru");
  const title = [
    context?.estimated ? "Оценка заполнения контекста" : "",
    used === undefined ? "Провайдер ещё не сообщил размер контекста" : `${Math.round(used).toLocaleString("ru")} токенов в контексте`,
    contextWindow === undefined ? "" : `Окно: ${Math.round(contextWindow).toLocaleString("ru")} токенов`,
    remaining === undefined ? "" : `Осталось: ${Math.round(remaining).toLocaleString("ru")} токенов`,
    error,
  ].filter(Boolean).join("\n");
  return <div className={`context-control ${compacting || requesting ? "is-compacting" : ""}`}>
    <button
      ref={trigger}
      className="context-trigger"
      title={title}
      aria-label={compacting ? "Сжимается контекст" : requesting ? "Отправляем запрос на сжатие" : percent === undefined ? "Размер контекста неизвестен. Открыть контекст" : `Использовано ${percent.toFixed(1)}% контекста. Открыть контекст`}
      aria-expanded={open}
      aria-haspopup="dialog"
      aria-controls={open ? popoverId : undefined}
      onClick={() => setOpen(!open)}
    >
      {compacting || requesting ? <LoaderCircle size={13} className="spin" /> : <Gauge size={13} />}
      <span>{compacting ? "Сжимаем…" : requesting ? "Запрос…" : percent === undefined ? "—" : `${context?.estimated ? "≈ " : ""}${Math.round(percent)}%`}</span>
    </button>
    {open && <>
      <button className="popover-backdrop" tabIndex={-1} onClick={close} aria-label="Закрыть контекст" />
      <div className="context-popover" ref={dialog} tabIndex={-1} id={popoverId} role="dialog" aria-modal="true" aria-label="Контекст диалога">
        <header>
          <span>Контекст</span>
          <button className="icon-button" onClick={close} aria-label="Закрыть контекст"><X size={15} /></button>
        </header>
        <dl>
          <div><dt>Использовано</dt><dd>{format(used)}</dd></div>
          <div><dt>Размер окна</dt><dd>{format(contextWindow)}</dd></div>
          <div><dt>Осталось</dt><dd>{format(remaining)}</dd></div>
        </dl>
        {(used === undefined || context?.estimated) && <p>{used === undefined ? error || "Провайдер ещё не сообщил размер контекста." : "Размер контекста оценён провайдером."}</p>}
        <button
          className="compact-button"
          disabled={!context || busy || compacting || requesting}
          title={compacting ? "Провайдер сжимает контекст" : requesting ? "Отправляем запрос на сжатие" : busy ? "Сжать контекст можно после ответа бота" : !context ? error || "Читаем состояние контекста" : "Сжать контекст"}
          aria-label={compacting ? "Сжимается контекст" : "Сжать контекст"}
          onClick={() => {
            close();
            void compact().catch(cause => onError(errorMessage(cause)));
          }}
        >
          {compacting || requesting ? <LoaderCircle size={14} className="spin" /> : <Shrink size={14} />}
          <span>{compacting ? "Сжимаем контекст…" : requesting ? "Отправляем запрос…" : "Сжать контекст"}</span>
        </button>
      </div>
    </>}
  </div>;
}
