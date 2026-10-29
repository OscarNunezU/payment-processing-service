"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

import styles from "./site-nav.module.css";

// Each section, and the paths that belong to it: a payment's page is part of
// the payments.
const SECTIONS = [
  { href: "/", label: "Pagos", matches: (path: string) => path === "/" || path.startsWith("/payments/") },
];

// SiteNav marks the section of the current page, for the eye and, with
// aria-current, for screen readers.
export function SiteNav() {
  const path = usePathname();
  return (
    <nav className={styles.nav} aria-label="Secciones">
      {SECTIONS.map((s) => {
        const current = s.matches(path);
        return (
          <Link key={s.href} href={s.href} className={current ? styles.current : undefined} aria-current={current ? "page" : undefined}>
            {s.label}
          </Link>
        );
      })}
    </nav>
  );
}
