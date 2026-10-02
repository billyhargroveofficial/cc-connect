export const themeStorageKey = "connect-bots:theme";
export const systemThemeQuery = "(prefers-color-scheme: dark)";

export type ThemePreference = "system" | "light" | "dark";
export type ResolvedTheme = "light" | "dark";

interface ThemeStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
}

interface ThemeMediaQuery {
  matches: boolean;
  addEventListener?: (type: "change", listener: (event: { matches: boolean }) => void) => void;
  removeEventListener?: (type: "change", listener: (event: { matches: boolean }) => void) => void;
  addListener?: (listener: (event: { matches: boolean }) => void) => void;
  removeListener?: (listener: (event: { matches: boolean }) => void) => void;
}

export function parseThemePreference(value: string | null | undefined): ThemePreference {
  return value === "light" || value === "dark" || value === "system"
    ? value
    : "system";
}

export function readThemePreference(storage?: Pick<ThemeStorage, "getItem">): ThemePreference {
  try {
    return parseThemePreference(storage?.getItem(themeStorageKey));
  } catch {
    return "system";
  }
}

export function saveThemePreference(
  storage: Pick<ThemeStorage, "setItem"> | undefined,
  preference: ThemePreference,
) {
  try {
    storage?.setItem(themeStorageKey, preference);
  } catch {}
}

export function resolveTheme(
  preference: ThemePreference,
  systemPrefersDark: boolean,
): ResolvedTheme {
  return preference === "system"
    ? systemPrefersDark ? "dark" : "light"
    : preference;
}

export function themeColor(theme: ResolvedTheme): string {
  return theme === "dark" ? "#111111" : "#f7f7f7";
}

export function subscribeSystemTheme(
  media: ThemeMediaQuery,
  onChange: (dark: boolean) => void,
): () => void {
  const update = (event: { matches: boolean }) => onChange(event.matches);
  onChange(media.matches);
  if (media.addEventListener && media.removeEventListener) {
    media.addEventListener("change", update);
    return () => media.removeEventListener?.("change", update);
  }
  media.addListener?.(update);
  return () => media.removeListener?.(update);
}
