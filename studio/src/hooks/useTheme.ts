import { useEffect, useLayoutEffect, useState } from "react";
import {
  readThemePreference,
  resolveTheme,
  saveThemePreference,
  subscribeSystemTheme,
  systemThemeQuery,
  themeColor,
  type ThemePreference,
} from "../lib/theme";

function initialPreference(): ThemePreference {
  try {
    return readThemePreference(window.localStorage);
  } catch {
    return "system";
  }
}

function initialSystemDark(): boolean {
  try {
    return typeof window.matchMedia === "function"
      ? window.matchMedia(systemThemeQuery).matches
      : true;
  } catch {
    return true;
  }
}

export function useTheme() {
  const [preference, setPreference] = useState<ThemePreference>(initialPreference);
  const [systemDark, setSystemDark] = useState(initialSystemDark);
  const resolved = resolveTheme(preference, systemDark);

  useEffect(() => {
    if (typeof window.matchMedia !== "function") return;
    return subscribeSystemTheme(window.matchMedia(systemThemeQuery), setSystemDark);
  }, []);

  useLayoutEffect(() => {
    const root = document.documentElement;
    root.dataset.theme = resolved;
    root.dataset.themePreference = preference;
    root.classList.toggle("dark", resolved === "dark");
    document.querySelector<HTMLMetaElement>('meta[name="theme-color"]')
      ?.setAttribute("content", themeColor(resolved));
    try {
      saveThemePreference(window.localStorage, preference);
    } catch {}
  }, [preference, resolved]);

  return { preference, resolved, setPreference };
}
