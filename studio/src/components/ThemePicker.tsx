import { useEffect, useRef, useState } from "react";
import { Check, Monitor, Moon, Sun } from "lucide-react";
import type { ThemePreference } from "../lib/theme";

const options: {
  id: ThemePreference;
  label: string;
  icon: typeof Monitor;
}[] = [
  { id: "system", label: "System", icon: Monitor },
  { id: "light", label: "Light", icon: Sun },
  { id: "dark", label: "Dark", icon: Moon },
];

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
      menu.current?.querySelector<HTMLButtonElement>('[aria-checked="true"]')?.focus();
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
    const items = Array.from(menu.current?.querySelectorAll<HTMLButtonElement>("button") || []);
    if (!items.length) return;
    const current = items.indexOf(document.activeElement as HTMLButtonElement);
    const index = event.key === "Home"
      ? 0
      : event.key === "End"
        ? items.length - 1
        : (current + (event.key === "ArrowDown" ? 1 : -1) + items.length) % items.length;
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
      <button
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
      >
        <TriggerIcon size={17} />
      </button>
      {open && (
        <div className="theme-menu" ref={menu} role="menu" aria-label="Theme" onKeyDown={menuKeyDown}>
          {options.map((option) => {
            const Icon = option.icon;
            const active = value === option.id;
            return (
              <button
                key={option.id}
                type="button"
                role="menuitemradio"
                aria-checked={active}
                className={active ? "is-selected" : ""}
                onClick={() => select(option.id)}
              >
                <Icon size={16} />
                <span>{option.label}</span>
                {active && <Check size={15} className="theme-check" />}
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}
