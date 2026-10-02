import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import type { KeyboardEvent, PointerEvent } from "react";

export const SIDEBAR_STORAGE_KEY = "connect-bots:sidebar-width";
export const SIDEBAR_MIN_WIDTH = 220;
export const SIDEBAR_MAX_WIDTH = 420;
const SIDEBAR_MIN_CONTENT_WIDTH = 440;
const MOBILE_BREAKPOINT = 700;
const KEYBOARD_STEP = 16;

type StorageLike = Pick<Storage, "getItem" | "setItem" | "removeItem">;

function browserStorage(): StorageLike | null {
  if (typeof window === "undefined") return null;
  try {
    return window.localStorage;
  } catch {
    return null;
  }
}

export function sidebarBounds(viewportWidth: number) {
  return {
    min: SIDEBAR_MIN_WIDTH,
    max: Math.max(
      SIDEBAR_MIN_WIDTH,
      Math.min(SIDEBAR_MAX_WIDTH, viewportWidth - SIDEBAR_MIN_CONTENT_WIDTH),
    ),
  };
}

export function defaultSidebarWidth(viewportWidth: number) {
  const preferred = viewportWidth >= 1500 ? 300 : viewportWidth <= 1050 ? 258 : 284;
  return clampSidebarWidth(preferred, viewportWidth);
}

export function clampSidebarWidth(width: number, viewportWidth: number) {
  const bounds = sidebarBounds(viewportWidth);
  return Math.round(Math.min(bounds.max, Math.max(bounds.min, width)));
}

export function readSidebarWidth(storage: StorageLike | null | undefined) {
  if (!storage) return null;
  try {
    const width = Number(storage.getItem(SIDEBAR_STORAGE_KEY));
    return Number.isFinite(width) && width >= SIDEBAR_MIN_WIDTH && width <= SIDEBAR_MAX_WIDTH
      ? Math.round(width)
      : null;
  } catch {
    return null;
  }
}

export function saveSidebarWidth(storage: StorageLike | null | undefined, width: number | null) {
  if (!storage) return;
  try {
    if (width === null) storage.removeItem(SIDEBAR_STORAGE_KEY);
    else storage.setItem(SIDEBAR_STORAGE_KEY, String(Math.round(width)));
  } catch {
    // A private browsing policy may deny storage while resizing still works.
  }
}

function workspaceFor(handle: HTMLElement | null) {
  return handle?.closest<HTMLElement>(".workspace") || null;
}

function clearWorkspaceWidth(workspace: HTMLElement | null) {
  workspace?.style.removeProperty("--roster-width");
  workspace?.style.removeProperty("--roster-half-width");
}

function applyWorkspaceWidth(workspace: HTMLElement | null, width: number) {
  workspace?.style.setProperty("--roster-width", `${width}px`);
  workspace?.style.setProperty("--roster-half-width", `${width / 2}px`);
}

interface DragState {
  pointerId: number;
  startX: number;
  startWidth: number;
}

export default function SidebarResizeHandle() {
  const initialViewportWidth = typeof window === "undefined" ? 1280 : window.innerWidth;
  const storage = useRef<StorageLike | null>(browserStorage());
  const initialPreference = useRef<number | null>(readSidebarWidth(storage.current));
  const preference = useRef<number | null>(initialPreference.current);
  const handleRef = useRef<HTMLDivElement>(null);
  const drag = useRef<DragState | null>(null);
  const width = useRef(
    initialPreference.current === null
      ? defaultSidebarWidth(initialViewportWidth)
      : clampSidebarWidth(initialPreference.current, initialViewportWidth),
  );
  const [value, setValue] = useState(width.current);
  const [viewportWidth, setViewportWidth] = useState(initialViewportWidth);
  const [resizing, setResizing] = useState(false);

  const syncPreference = useCallback(() => {
    const workspace = workspaceFor(handleRef.current);
    if (window.innerWidth <= MOBILE_BREAKPOINT) {
      clearWorkspaceWidth(workspace);
      width.current = defaultSidebarWidth(window.innerWidth);
    } else if (preference.current === null) {
      clearWorkspaceWidth(workspace);
      width.current = defaultSidebarWidth(window.innerWidth);
    } else {
      width.current = clampSidebarWidth(preference.current, window.innerWidth);
      applyWorkspaceWidth(workspace, width.current);
    }
    setValue(width.current);
  }, []);

  const stopResizing = useCallback((persist: boolean) => {
    if (!drag.current) return;
    drag.current = null;
    document.documentElement.classList.remove("is-resizing-sidebar");
    setResizing(false);
    if (persist) {
      preference.current = width.current;
      saveSidebarWidth(storage.current, preference.current);
      setValue(width.current);
    } else {
      syncPreference();
    }
  }, [syncPreference]);

  useLayoutEffect(() => {
    syncPreference();
    return () => {
      document.documentElement.classList.remove("is-resizing-sidebar");
    };
  }, [syncPreference]);

  useEffect(() => {
    const resize = () => {
      setViewportWidth(window.innerWidth);
      if (window.innerWidth <= MOBILE_BREAKPOINT) stopResizing(true);
      syncPreference();
    };
    const blur = () => stopResizing(true);
    window.addEventListener("resize", resize);
    window.addEventListener("blur", blur);
    return () => {
      window.removeEventListener("resize", resize);
      window.removeEventListener("blur", blur);
    };
  }, [stopResizing, syncPreference]);

  function begin(event: PointerEvent<HTMLDivElement>) {
    if (drag.current || event.button !== 0 || event.isPrimary === false || window.innerWidth <= MOBILE_BREAKPOINT) return;
    const roster = handleRef.current?.parentElement;
    if (!roster) return;
    drag.current = {
      pointerId: event.pointerId,
      startX: event.clientX,
      startWidth: roster.getBoundingClientRect().width,
    };
    width.current = Math.round(drag.current.startWidth);
    event.currentTarget.setPointerCapture(event.pointerId);
    document.documentElement.classList.add("is-resizing-sidebar");
    setResizing(true);
    event.preventDefault();
  }

  function move(event: PointerEvent<HTMLDivElement>) {
    const active = drag.current;
    if (!active || active.pointerId !== event.pointerId) return;
    width.current = clampSidebarWidth(
      active.startWidth + event.clientX - active.startX,
      window.innerWidth,
    );
    applyWorkspaceWidth(workspaceFor(handleRef.current), width.current);
    event.currentTarget.setAttribute("aria-valuenow", String(width.current));
  }

  function end(event: PointerEvent<HTMLDivElement>) {
    if (!drag.current || drag.current.pointerId !== event.pointerId) return;
    if (event.currentTarget.hasPointerCapture(event.pointerId)) {
      event.currentTarget.releasePointerCapture(event.pointerId);
    }
    stopResizing(true);
  }

  function keyboard(event: KeyboardEvent<HTMLDivElement>) {
    if (window.innerWidth <= MOBILE_BREAKPOINT) return;
    const bounds = sidebarBounds(window.innerWidth);
    let next: number | undefined;
    if (event.key === "ArrowLeft") next = width.current - KEYBOARD_STEP;
    if (event.key === "ArrowRight") next = width.current + KEYBOARD_STEP;
    if (event.key === "Home") next = bounds.min;
    if (event.key === "End") next = bounds.max;
    if (next === undefined) return;
    event.preventDefault();
    width.current = clampSidebarWidth(next, window.innerWidth);
    preference.current = width.current;
    applyWorkspaceWidth(workspaceFor(handleRef.current), width.current);
    saveSidebarWidth(storage.current, preference.current);
    setValue(width.current);
  }

  function reset() {
    preference.current = null;
    saveSidebarWidth(storage.current, null);
    syncPreference();
  }

  const bounds = sidebarBounds(viewportWidth);
  return <div
    ref={handleRef}
    className={`roster-resizer${resizing ? " is-resizing" : ""}`}
    role="separator"
    aria-label="Resize bot sidebar"
    aria-controls="bot-roster"
    aria-orientation="vertical"
    aria-valuemin={bounds.min}
    aria-valuemax={bounds.max}
    aria-valuenow={value}
    tabIndex={0}
    title="Drag to resize. Double-click to reset."
    onPointerDown={begin}
    onPointerMove={move}
    onPointerUp={end}
    onPointerCancel={end}
    onLostPointerCapture={(event) => {
      if (drag.current?.pointerId === event.pointerId) stopResizing(true);
    }}
    onKeyDown={keyboard}
    onDoubleClick={reset}
  />;
}
