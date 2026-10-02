import { memo, useEffect, useRef } from "react";
import { DollarSign, LoaderCircle, RotateCcw } from "lucide-react";
import type { Skill } from "../../lib/types";
import { m, useReducedMotion, fade, popoverMotion } from "../../lib/motion";
import { PresenceSurface } from "./ModelPicker";
import "./skill-picker.css";

const scopes: Record<string, string> = { nativeUser: "User", productUser: "Shared", project: "Project", system: "Built-in" };
export default memo(function SkillPicker({ id, skills, index, status, error, onChoose, onHighlight, onClose, onRetry }: {
  id: string; skills: Skill[]; index: number; status: string; error: string;
  onChoose: (skill: Skill) => void; onHighlight: (index: number) => void; onClose: () => void; onRetry: () => void;
}) {
  const reduced = useReducedMotion();
  const surface = useRef<HTMLDivElement>(null);
  const close = useRef(onClose);
  close.current = onClose;
  useEffect(() => {
    const outside = (event: PointerEvent) => {
      const target = event.target as HTMLElement | null;
      if (!surface.current?.contains(target) && target?.tagName !== "TEXTAREA") close.current();
    };
    document.addEventListener("pointerdown", outside);
    return () => document.removeEventListener("pointerdown", outside);
  }, []);
  useEffect(() => {
    const active = surface.current?.querySelector<HTMLElement>(`[data-skill-index="${index}"]`);
    if (!active || !surface.current) return;
    const list = surface.current;
    if (active.offsetTop < list.scrollTop) list.scrollTop = active.offsetTop;
    else if (active.offsetTop + active.offsetHeight > list.scrollTop + list.clientHeight) list.scrollTop = active.offsetTop + active.offsetHeight - list.clientHeight;
  }, [index, skills]);
  return <PresenceSurface ref={surface} variants={reduced ? fade : popoverMotion}
    initial="hidden" animate="visible" exit="exit" className="skill-picker" id={id} role="listbox" aria-label="Attach a skill">
    <div className="skill-picker-heading"><DollarSign size={13} /><span>Skills</span><kbd>↵</kbd></div>
    {status === "loading" || status === "idle" ? <div className="skill-picker-status" role="status"><LoaderCircle size={14} className="spin" />Loading skills…</div>
      : status === "error" ? <div className="skill-picker-status skill-picker-error" role="status"><span>{error || "Could not load skills."}</span>
        <button type="button" onClick={onRetry}><RotateCcw size={13} />Retry</button></div>
        : !skills.length ? <div className="skill-picker-status" role="status">No matching skills.</div>
          : skills.map((skill, offset) => <m.div role="option" aria-selected={offset === index} id={`${id}-option-${offset}`} data-skill-index={offset}
            className={`skill-picker-option${offset === index ? " is-selected" : ""}`} key={skill.id}
            onPointerMove={event => { if (event.pointerType === "mouse") onHighlight(offset); }}
            onMouseDown={event => event.preventDefault()} onClick={() => onChoose(skill)}>
            <DollarSign size={15} aria-hidden="true" /><span className="skill-picker-info"><span><strong>{skill.name}</strong><small>{scopes[skill.scope] || skill.scope}</small></span>
              {skill.description && <span className="skill-picker-description">{skill.description}</span>}</span>
          </m.div>)}
  </PresenceSurface>;
});
