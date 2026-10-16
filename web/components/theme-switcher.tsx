"use client";

import { type ReactNode, useState } from "react";

import styles from "./theme-switcher.module.css";

export type Theme = "system" | "light" | "dark";

const ONE_YEAR = 60 * 60 * 24 * 365;

const OPTIONS: { value: Theme; label: string; icon: ReactNode }[] = [
  {
    value: "system",
    label: "Sistema",
    icon: <path d="M3 5h18v11H3zM8 20h8M12 16v4" />,
  },
  {
    value: "light",
    label: "Claro",
    icon: (
      <>
        <circle cx="12" cy="12" r="4" />
        <path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" />
      </>
    ),
  },
  {
    value: "dark",
    label: "Oscuro",
    icon: <path d="M20 14.5A8 8 0 0 1 9.5 4 8 8 0 1 0 20 14.5Z" />,
  },
];

// The choice lives in a cookie, so the server renders the right theme from the
// first paint; "system" removes it and lets prefers-color-scheme decide.
export function ThemeSwitcher({ initial }: { initial: Theme }) {
  const [theme, setTheme] = useState(initial);

  function choose(next: Theme) {
    setTheme(next);
    const root = document.documentElement;
    if (next === "system") {
      document.cookie = "theme=; path=/; max-age=0; samesite=lax";
      delete root.dataset.theme;
    } else {
      document.cookie = `theme=${next}; path=/; max-age=${ONE_YEAR}; samesite=lax`;
      root.dataset.theme = next;
    }
  }

  return (
    <div className={styles.switcher} role="radiogroup" aria-label="Tema">
      {OPTIONS.map((option) => (
        <button
          key={option.value}
          type="button"
          role="radio"
          aria-checked={theme === option.value}
          aria-label={option.label}
          title={option.label}
          className={styles.option}
          onClick={() => choose(option.value)}
        >
          <svg viewBox="0 0 24 24" aria-hidden="true">
            {option.icon}
          </svg>
        </button>
      ))}
    </div>
  );
}
