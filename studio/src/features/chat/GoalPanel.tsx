import { useEffect, useState, useRef, useCallback } from "react";
import {
  Target,
  X,
  Pause,
  Play,
  Check,
  LoaderCircle,
  ChevronRight,
  Trash2,
} from "lucide-react";
import type { Bot, Event, Goal, GoalSnapshot } from "../../lib/types";
import { api, errorMessage } from "../../lib/api";
import { currentGoalUpdate, goalActionRevision, goalFromSnapshot } from "../../lib/currentGoal";
const labels: Record<string, string> = {
  active: "В работе",
  paused: "На паузе",
  blocked: "Нужна помощь",
  usageLimited: "Лимит использования",
  budgetLimited: "Лимит бюджета",
  complete: "Достигнута",
};
export function useGoal(bot: Bot, events: Event[], enabled: boolean) {
  const [goal, setGoal] = useState<Goal | null>(null);
  const [loaded, setLoaded] = useState(false);
  const eventsRef = useRef(events);
  eventsRef.current = events;
  const cursor = useRef(0);
  const threadId =
    enabled && bot.backend === "codex" ? bot.threads?.codex || "" : "";
  const actionRevision = goalActionRevision(events, threadId);
  const applySnapshot = useCallback((snapshot: GoalSnapshot) => {
    if (!enabled) return;
    const resolved = goalFromSnapshot(snapshot, eventsRef.current, threadId);
    cursor.current = Math.max(snapshot.cursor, eventsRef.current.at(-1)?.seq || 0);
    setGoal(resolved);
    setLoaded(true);
  }, [enabled, threadId]);
  useEffect(() => {
    let alive = true;
    setGoal(null);
    setLoaded(false);
    cursor.current = eventsRef.current.at(-1)?.seq || 0;
    if (enabled)
      api
        .goalSnapshot(bot.id)
        .then((result) => {
          if (alive) applySnapshot(result);
        })
        .catch(() => {
          if (alive) setLoaded(true);
        });
    return () => {
      alive = false;
    };
  }, [bot.id, bot.backend, bot.model, bot.effort, threadId, actionRevision, enabled, applySnapshot]);
  useEffect(() => {
    if (!enabled || !threadId || !loaded) return;
    const change = currentGoalUpdate(events, threadId, cursor.current);
    cursor.current = Math.max(cursor.current, events.at(-1)?.seq || 0);
    if (change) setGoal(change.goal);
  }, [events, threadId, enabled, loaded]);
  return { goal: enabled ? goal : null, setGoal: applySnapshot };
}
export function GoalSummary({
  goal,
  onOpen,
}: {
  goal: Goal;
  onOpen: () => void;
}) {
  const used = typeof goal.tokensUsed === "number" ? goal.tokensUsed : 0;
  const budget = typeof goal.tokenBudget === "number" ? goal.tokenBudget : 0;
  return (
    <button className="goal-summary" onClick={onOpen}>
      <span
        className={`goal-summary-icon ${goal.status === "complete" ? "is-complete" : ""}`}
      >
        {goal.status === "complete" ? (
          <Check size={16} />
        ) : (
          <Target size={17} />
        )}
      </span>
      <span className="goal-summary-text">
        <strong>{goal.objective}</strong>
        <span>
          {labels[goal.status || ""] || goal.status || "Цель"}
          {budget > 0 &&
            ` · ${used.toLocaleString("ru-RU")} / ${budget.toLocaleString("ru-RU")} токенов`}
        </span>
      </span>
      {budget > 0 && (
        <span className="goal-progress">
          <span style={{ width: `${Math.min(100, (used / budget) * 100)}%` }} />
        </span>
      )}
      <ChevronRight size={15} />
    </button>
  );
}
export function GoalDialog({
  bot,
  goal,
  onChange,
  onClose,
  onError,
}: {
  bot: Bot;
  goal: Goal | null;
  onChange: (snapshot: GoalSnapshot) => void;
  onClose: () => void;
  onError: (error: string) => void;
}) {
  const [objective, setObjective] = useState(goal?.objective || "");
  const [budget, setBudget] = useState(
    goal?.tokenBudget ? String(goal.tokenBudget) : "",
  );
  const [busy, setBusy] = useState(false);
  const dialog = useRef<HTMLElement>(null);
  const closeRef = useRef(onClose);
  closeRef.current = onClose;
  useEffect(() => {
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const keyboard = (event: KeyboardEvent) => {
      if (event.key === "Escape") closeRef.current();
      if (event.key !== "Tab" || !dialog.current) return;
      const controls = [...dialog.current.querySelectorAll<HTMLElement>(
        'button:not(:disabled), input:not(:disabled), textarea:not(:disabled), [tabindex="0"]',
      )].filter(element => element.getClientRects().length > 0);
      const first = controls[0], last = controls.at(-1);
      if (!first || !last) return;
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault(); last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault(); first.focus();
      }
    };
    window.addEventListener("keydown", keyboard);
    return () => {
      window.removeEventListener("keydown", keyboard);
      if (previous?.isConnected) previous.focus();
    };
  }, []);
  async function save(fields: Goal) {
    setBusy(true);
    try {
      onChange(await api.setGoal(bot.id, fields));
      onClose();
    } catch (error) {
      onError(errorMessage(error));
    } finally {
      setBusy(false);
    }
  }
  async function clear() {
    setBusy(true);
    try {
      onChange(await api.clearGoal(bot.id));
      onClose();
    } catch (error) {
      onError(errorMessage(error));
    } finally {
      setBusy(false);
    }
  }
  return (
    <div
      className="dialog-layer"
      role="presentation"
      onClick={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <section
        ref={dialog}
        className="goal-dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="goal-title"
      >
        <header>
          <span className="goal-dialog-symbol">
            <Target size={21} />
          </span>
          <button
            className="icon-button"
            onClick={onClose}
            aria-label="Закрыть цель"
          >
            <X size={18} />
          </button>
        </header>
        <h2 id="goal-title">{goal ? "Цель бота" : "Куда движемся?"}</h2>
        <p>
          Задайте результат, к которому {bot.name} будет возвращаться до
          завершения работы.
        </p>
        <form
          onSubmit={(event) => {
            event.preventDefault();
            if (objective.trim())
              void save({
                objective: objective.trim(),
                status: goal?.status || "active",
                ...(budget
                  ? { tokenBudget: Number(budget) }
                  : goal?.tokenBudget
                    ? { tokenBudget: null }
                    : {}),
              });
          }}
        >
          <label htmlFor="goal-objective">Ожидаемый результат</label>
          <textarea
            id="goal-objective"
            autoFocus
            rows={4}
            value={objective}
            onChange={(e) => setObjective(e.target.value)}
            placeholder="Что должно быть готово и как это проверить?"
            required
          />
          <label htmlFor="goal-budget">
            Бюджет токенов <span>необязательно</span>
          </label>
          <input
            id="goal-budget"
            type="number"
            min="1"
            step="1"
            value={budget}
            onChange={(e) => setBudget(e.target.value)}
            placeholder="Без заданного ограничения"
          />
          {goal && (
            <div className="goal-dialog-status">
              <span>{labels[goal.status || ""] || goal.status}</span>
              <span>
                {Number(goal.tokensUsed || 0).toLocaleString("ru-RU")} токенов
                использовано
              </span>
            </div>
          )}
          <div className="goal-dialog-actions">
            {goal && (
              <>
                <button
                  type="button"
                  className="icon-button danger-button"
                  disabled={busy}
                  onClick={() => void clear()}
                  aria-label="Удалить цель"
                >
                  <Trash2 size={17} />
                </button>
                {goal.status === "active" ? (
                  <button
                    type="button"
                    className="secondary-button"
                    disabled={busy}
                    onClick={() => void save({ status: "paused" })}
                  >
                    <Pause size={14} />
                    Пауза
                  </button>
                ) : goal.status !== "complete" ? (
                  <button
                    type="button"
                    className="secondary-button"
                    disabled={busy}
                    onClick={() => void save({ status: "active" })}
                  >
                    <Play size={14} />
                    Продолжить
                  </button>
                ) : null}
              </>
            )}
            <button
              className="primary-button"
              type="submit"
              disabled={busy || !objective.trim()}
            >
              {busy ? (
                <LoaderCircle size={16} className="spin" />
              ) : (
                "Сохранить цель"
              )}
            </button>
          </div>
        </form>
      </section>
    </div>
  );
}
