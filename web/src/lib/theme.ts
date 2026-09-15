import * as React from "react";

// Single global theme store shared by every component (sidebar toggle,
// login page, settings selector). Keeping it outside React state avoids the
// classic "each hook instance has its own copy / reads a stale DOM class"
// problem and keeps the <html class="dark"> flag as the one source of truth.

export type Theme = "light" | "dark" | "system";

const KEY = "ctlvps-theme"; // also read by the pre-paint script in index.html

const media = window.matchMedia("(prefers-color-scheme: dark)");
const listeners = new Set<() => void>();

function readStored(): Theme {
  const v = localStorage.getItem(KEY);
  return v === "light" || v === "dark" ? v : "system";
}

function resolveDark(t: Theme): boolean {
  return t === "dark" || (t === "system" && media.matches);
}

let current: Theme = readStored();

function apply() {
  document.documentElement.classList.toggle("dark", resolveDark(current));
  listeners.forEach((l) => l());
}

export function setTheme(t: Theme) {
  current = t;
  localStorage.setItem(KEY, t);
  apply();
}

export function toggleTheme() {
  setTheme(resolveDark(current) ? "light" : "dark");
}

// Follow OS changes while in "system" mode, and other tabs' choices.
media.addEventListener("change", () => current === "system" && apply());
window.addEventListener("storage", (e) => {
  if (e.key === KEY) {
    current = readStored();
    apply();
  }
});
apply(); // make sure the class matches the stored value at module load

function subscribe(l: () => void) {
  listeners.add(l);
  return () => listeners.delete(l);
}

export function useTheme() {
  const theme = React.useSyncExternalStore(subscribe, () => current);
  const isDark = React.useSyncExternalStore(subscribe, () => resolveDark(current));
  return { theme, isDark, setTheme, toggle: toggleTheme };
}
