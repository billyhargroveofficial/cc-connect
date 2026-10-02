import { useEffect, useRef, useState } from "react";
import { Check, Monitor, Moon, Sun } from "lucide-react";
import type { ThemePreference } from "../lib/theme";
import {
  AnimatePresence,
  controlMotion,
  LayoutGroup,
  m,
  motionSpring,
  motionTransition,
  popoverMotion,
  useIsPresent,
} from "../lib/motion";

const options: {
  id: ThemePreference;
  label: string;
  icon: typeof Monitor;
}[] = [
  { id: "system", label: "System", icon: Monitor },
  { id: "light", label: "Light", icon: Sun },
  { id: "dark", label: "Dark", icon: Moon },
];

function ThemeMenu({
  children,
  menuRef,
  onKeyDown,
}: {
  children: React.ReactNode;
  menuRef: React.RefObject<HTMLDivElement | null>;
  onKeyDown: (event: React.KeyboardEvent) => void;
}) {
  const present = useIsPresent();
  return (
    <m.div
      className="theme-menu"
      ref={menuRef}
      role="menu"
      aria-label="Theme"
      onKeyDown={onKeyDown}
      variants={popoverMotion}
      initial="hidden"
      animate="visible"
      exit="exit"
      style={{ originX: 1, originY: 1 }}
      inert={!present}
      aria-hidden={!present || undefined}
    >
      {children}
    </m.div>
  );
}

export default function ThemePicker({
  value,
  onChange,
}: {
  value: ThemePreference;
  onChange: (value: ThemePreference) => void;
}) {
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const selected = options.find((option) => option.id === value) || options[0];
  const TriggerIcon = selected.icon;

  useEffect(() => {
    if (!open) return;
    const frame = requestAnimationFrame(() => {
      menu.current
        ?.querySelector<HTMLButtonElement>('[aria-checked="true"]')
        ?.focus();
    });
    const outside = (event: PointerEvent) => {
      if (!root.current?.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener("pointerdown", outside);
    return () => {
      cancelAnimationFrame(frame);
      document.removeEventListener("pointerdown", outside);
    };
  }, [open]);

  function close(returnFocus = false) {
    setOpen(false);
    if (returnFocus) requestAnimationFrame(() => trigger.current?.focus());
  }

  function select(next: ThemePreference) {
    onChange(next);
    close(true);
  }

  function menuKeyDown(event: React.KeyboardEvent) {
    if (event.key === "Escape") {
      event.preventDefault();
      close(true);
      return;
    }
    if (!["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) return;
    event.preventDefault();
    const items = Array.from(
      menu.current?.querySelectorAll<HTMLButtonElement>("button") || [],
    );
    if (!items.length) return;
    const current = items.indexOf(document.activeElement as HTMLButtonElement);
    const index =
      event.key === "Home"
        ? 0
        : event.key === "End"
          ? items.length - 1
          : (current + (event.key === "ArrowDown" ? 1 : -1) + items.length) %
            items.length;
    items[index]?.focus();
  }

  return (
    <div
      className="theme-picker"
      ref={root}
      onBlur={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget as Node | null)) {
          setOpen(false);
        }
      }}
    >
      <m.button
        ref={trigger}
        type="button"
        className={`icon-button theme-trigger${open ? " is-open" : ""}`}
        onClick={() => setOpen((current) => !current)}
        onKeyDown={(event) => {
          if (["ArrowDown", "ArrowUp"].includes(event.key) && !open) {
            event.preventDefault();
            setOpen(true);
          }
        }}
        aria-label={`Theme: ${selected.label}`}
        title={`Theme: ${selected.label}`}
        aria-haspopup="menu"
        aria-expanded={open}
        data-motion-control
        {...controlMotion}
      >
        <AnimatePresence initial={false} mode="wait">
          <m.span
            key={selected.id}
            className="theme-trigger-icon"
            initial={{ opacity: 0, rotate: -15, scale: 0.9 }}
            animate={{ opacity: 1, rotate: 0, scale: 1 }}
            exit={{ opacity: 0, rotate: 15, scale: 0.9 }}
            transition={motionTransition.quick}
          >
            <TriggerIcon size={17} />
          </m.span>
        </AnimatePresence>
      </m.button>
      <LayoutGroup id="theme-picker">
        <AnimatePresence initial={false}>
          {open && (
            <ThemeMenu key="theme-menu" menuRef={menu} onKeyDown={menuKeyDown}>
              {options.map((option) => {
                const Icon = option.icon;
                const active = value === option.id;
                return (
                  <m.button
                    key={option.id}
                    type="button"
                    role="menuitemradio"
                    aria-checked={active}
                    className={active ? "is-selected" : ""}
                    onClick={() => select(option.id)}
                    whileHover={{ x: 1 }}
                    whileTap={{ scale: 0.98 }}
                    transition={motionSpring.control}
                    data-motion-control
                  >
                    <Icon size={16} />
                    <span>{option.label}</span>
                    <AnimatePresence initial={false}>
                      {active && (
                        <m.span
                          className="theme-check"
                          initial={{ opacity: 0, scale: 0.7 }}
                          animate={{ opacity: 1, scale: 1 }}
                          exit={{ opacity: 0, scale: 0.7 }}
                          transition={motionTransition.quick}
                        >
                          <Check size={15} />
                        </m.span>
                      )}
                    </AnimatePresence>
                    {active && (
                      <m.span
                        className="theme-option-selection"
                        layoutId="selected-theme"
                        transition={motionSpring.layout}
                        aria-hidden="true"
                      />
                    )}
                  </m.button>
                );
              })}
            </ThemeMenu>
          )}
        </AnimatePresence>
      </LayoutGroup>
    </div>
  );
}
