import { GeistMono } from "geist/font/mono";
import { GeistSans } from "geist/font/sans";
import type { Metadata } from "next";
import { cookies } from "next/headers";
import Link from "next/link";
import type { ReactNode } from "react";

import { SiteNav } from "@/components/site-nav";
import { type Theme, ThemeSwitcher } from "@/components/theme-switcher";

import "./globals.css";

export const metadata: Metadata = {
  title: { default: "Pagos", template: "%s · Payment Processing" },
  description: "El comercio de prueba del servicio de pagos: checkout, pagos, webhooks y el adquirente simulado",
};

export default async function RootLayout({ children }: { children: ReactNode }) {
  const saved = (await cookies()).get("theme")?.value;
  const theme: Theme = saved === "light" || saved === "dark" ? saved : "system";

  return (
    <html
      lang="es"
      data-theme={theme === "system" ? undefined : theme}
      className={`${GeistSans.variable} ${GeistMono.variable}`}
    >
      <body>
        <header className="site-header">
          <div className="header-start">
            <Link href="/" className="brand">
              <span className="brand-logo" aria-hidden="true" />
              Payment Processing
            </Link>
            <SiteNav />
          </div>
          <div className="header-actions">
            <ThemeSwitcher initial={theme} />
            <Link href="/checkout" className="button">
              + Pago de prueba
            </Link>
          </div>
        </header>
        <main className="container">{children}</main>
      </body>
    </html>
  );
}
