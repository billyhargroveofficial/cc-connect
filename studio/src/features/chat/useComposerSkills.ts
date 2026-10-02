import { useEffect, useMemo, useRef, useState } from "react";
import type { KeyboardEvent, RefObject } from "react";
import { api, errorMessage, onSkillsChanged } from "../../lib/api";
import type { Skill, SkillReference } from "../../lib/types";
import { workspaceBinding } from "./useMessageQueue";
import { filterSkills, removeSkillMention, skillMentionAt } from "./skillMentions";
import type { SkillMention } from "./skillMentions";

function initialSkills(scope: string, botId: string): SkillReference[] {
  if (!scope) return [];
  try {
    const value: unknown = JSON.parse(localStorage.getItem(`connect-bots:skill-draft:${scope}:${botId}`) || "[]");
    const seen = new Set<string>();
    return Array.isArray(value) ? value.filter((skill): skill is SkillReference => {
      if (!skill || typeof skill.id !== "string" || typeof skill.name !== "string" || typeof skill.path !== "string"
        || !skill.id || !skill.name || !skill.path || seen.has(skill.id)) return false;
      seen.add(skill.id);
      return true;
    }).map(({ id, name, path }) => ({ id, name, path })) : [];
  } catch { return []; }
}

interface Catalog { key: string; status: "idle" | "loading" | "ready" | "error"; items: Skill[]; error: string }

export function useComposerSkills({ botId, draftScope, availabilityKey, canValidate, blocked, text, textarea, onTextChange }: {
  botId: string; draftScope: string; availabilityKey: string; canValidate: boolean; blocked: boolean; text: string;
  textarea: RefObject<HTMLTextAreaElement | null>; onTextChange: (text: string) => void;
}) {
  const identity = `${draftScope}:${botId}`;
  const binding = useMemo(() => workspaceBinding(draftScope), [draftScope]);
  const [selection, setSelection] = useState(() => ({ identity, items: initialSkills(draftScope, botId) }));
  const [trigger, setTrigger] = useState<{ identity: string; mention: SkillMention | null }>({ identity, mention: null });
  const [revision, setRevision] = useState(0);
  const lifecycle = useRef({ identity, active: true, canValidate, validationEpoch: 0, key: "", validatedKey: "" });
  lifecycle.current.identity = identity;
  if (lifecycle.current.canValidate !== canValidate) {
    lifecycle.current.canValidate = canValidate;
    // A reconnect must not reuse either a failed request or a previously
    // validated catalog, including a result that finishes while offline.
    lifecycle.current.validationEpoch += 1;
  }
  const catalogKey = JSON.stringify([identity, availabilityKey, revision, lifecycle.current.validationEpoch]);
  const [catalog, setCatalog] = useState<Catalog>({ key: catalogKey, status: "idle", items: [], error: "" });
  const [activeIndex, setActiveIndex] = useState(0);
  const [retry, setRetry] = useState(0);
  if (lifecycle.current.key !== catalogKey) {
    lifecycle.current.key = catalogKey;
    lifecycle.current.validatedKey = "";
  }
  const selected = selection.identity === identity ? selection.items : [];
  const mention = !blocked && trigger.identity === identity ? trigger.mention : null;
  const open = Boolean(mention);
  const currentCatalog = catalog.key === catalogKey ? catalog : null;
  const validating = selected.length > 0 && lifecycle.current.validatedKey !== catalogKey;
  const needsCatalog = open || validating;
  const matches = useMemo(() => filterSkills(currentCatalog?.items || [], mention?.query || "", selected), [currentCatalog?.items, mention?.query, selected]);
  const index = Math.min(activeIndex, Math.max(0, matches.length - 1));
  const close = () => setTrigger({ identity, mention: null });

  useEffect(() => {
    const current = lifecycle.current;
    current.active = true;
    setSelection({ identity, items: initialSkills(draftScope, botId) });
    setTrigger({ identity, mention: null });
    return () => { current.active = false; };
  }, [identity, draftScope, botId]);
  useEffect(() => {
    if (selection.identity !== identity || !draftScope) return;
    try { localStorage.setItem(`connect-bots:skill-draft:${draftScope}:${botId}`, JSON.stringify(selection.items)); }
    catch { /* Private browsers may disable draft persistence. */ }
  }, [selection, identity, draftScope, botId]);
  useEffect(() => {
    if (blocked) setTrigger({ identity, mention: null });
  }, [blocked, identity]);
  useEffect(() => {
    if (lifecycle.current.key === catalogKey) setTrigger({ identity, mention: null });
  }, [identity, availabilityKey]);
  useEffect(() => onSkillsChanged(change => {
    if (!lifecycle.current.active || lifecycle.current.identity !== identity
      || change.accountId !== binding.accountId || change.nodeId !== binding.nodeId
      || (change.botId && change.botId !== botId)) return;
    // Reject a handler captured before the mutation, even before React commits.
    lifecycle.current.key = "";
    lifecycle.current.validatedKey = "";
    setRevision(value => value + 1);
  }), [identity, botId, binding]);
  useEffect(() => {
    if (!canValidate || !needsCatalog || currentCatalog?.status === "ready" || currentCatalog?.status === "error") return;
    const controller = new AbortController();
    setCatalog({ key: catalogKey, status: "loading", items: [], error: "" });
    void api.skills(botId, controller.signal, binding).then(result => {
      if (controller.signal.aborted || !lifecycle.current.active || !lifecycle.current.canValidate || lifecycle.current.identity !== identity || lifecycle.current.key !== catalogKey) return;
      const seen = new Set<string>();
      const items = Array.isArray(result.skills) ? result.skills.filter(skill => {
        if (!skill || !skill.id || !skill.name || !skill.path || seen.has(skill.id)) return false;
        seen.add(skill.id);
        return true;
      }) : [];
      const installed = new Map(items.filter(skill => skill.enabled).map(skill => [skill.id, skill]));
      setSelection(previous => {
        if (previous.identity !== identity) return previous;
        const next = previous.items.flatMap(reference => {
          const canonical = installed.get(reference.id);
          if (!canonical) return [];
          // Preserve unchanged objects so acknowledgement still distinguishes
          // a submitted attachment from a later reattachment of the same ID.
          return [canonical.name === reference.name && canonical.path === reference.path ? reference
            : { id: canonical.id, name: canonical.name, path: canonical.path }];
        });
        return next.length === previous.items.length && next.every((item, index) => item === previous.items[index])
          ? previous : { identity, items: next };
      });
      lifecycle.current.validatedKey = catalogKey;
      setCatalog({ key: catalogKey, status: "ready", items, error: "" });
    }).catch(error => {
      if (controller.signal.aborted || !lifecycle.current.active || !lifecycle.current.canValidate || lifecycle.current.identity !== identity || lifecycle.current.key !== catalogKey) return;
      setCatalog({ key: catalogKey, status: "error", items: [], error: errorMessage(error) });
    });
    return () => controller.abort();
  // Catalog is retained after a completed request; typing never refetches it.
  // The retry counter explicitly invalidates an error without coupling to text.
  // Opening suggestions cannot restart an existing selection validation.
  }, [identity, catalogKey, botId, binding, canValidate, needsCatalog, retry]);

  function update(value: string, caret: number, end = caret) {
    if (blocked) return;
    const next = skillMentionAt(value, caret, end);
    setTrigger(previous => previous.identity === identity && previous.mention?.start === next?.start
      && previous.mention?.end === next?.end && previous.mention?.query === next?.query ? previous : { identity, mention: next });
    if (trigger.mention?.query !== next?.query) setActiveIndex(0);
  }
  function choose(skill: Skill) {
    if (blocked || !lifecycle.current.active || lifecycle.current.identity !== identity || lifecycle.current.key !== catalogKey || !mention || !skill.enabled || !matches.some(item => item.id === skill.id)) return;
    const reference = { id: skill.id, name: skill.name, path: skill.path };
    setSelection(previous => previous.identity !== identity ? { identity, items: [reference] }
      : previous.items.some(item => item.id === skill.id) ? previous : { identity, items: [...previous.items, reference] });
    const result = removeSkillMention(text, mention);
    onTextChange(result.text);
    close();
    textarea.current?.focus({ preventScroll: true });
    requestAnimationFrame(() => {
      if (lifecycle.current.active && lifecycle.current.identity === identity) textarea.current?.setSelectionRange(result.caret, result.caret);
    });
  }
  function retryCatalog() {
    setCatalog({ key: catalogKey, status: "idle", items: [], error: "" });
    setRetry(value => value + 1);
  }
  function keyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (!open || event.nativeEvent.isComposing) return false;
    if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); close(); return true; }
    if (event.key === "Tab") { close(); return false; }
    if (["ArrowDown", "ArrowUp"].includes(event.key)) {
      event.preventDefault();
      setActiveIndex(value => matches.length ? (value + (event.key === "ArrowDown" ? 1 : -1) + matches.length) % matches.length : 0);
      return true;
    }
    if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      if (currentCatalog?.status === "error") retryCatalog();
      else if (matches[index]) choose(matches[index]);
      return true;
    }
    return false;
  }
  return {
    selected, validating, open, mention, matches, index, choose, update, close, keyDown,
    snapshot: () => !selected.length ? [] : lifecycle.current.key === catalogKey && lifecycle.current.validatedKey === catalogKey ? selected : null,
    status: currentCatalog?.status || "idle", error: currentCatalog?.error || "",
    highlight: setActiveIndex,
    retry: retryCatalog,
    remove: (id: string) => setSelection(previous => previous.identity === identity ? { identity, items: previous.items.filter(skill => skill.id !== id) } : previous),
    acknowledge: (submitted: SkillReference[]) => setSelection(previous => previous.identity === identity
      ? { identity, items: previous.items.filter(skill => !submitted.includes(skill)) } : previous),
  };
}
