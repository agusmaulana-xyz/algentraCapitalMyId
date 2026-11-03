"use client";

import { useCallback, useEffect, useState, type MouseEvent as ReactMouseEvent } from "react";
import { usePathname } from "next/navigation";
import { apiRequest } from "../lib/api";
import { MusicToggle } from "./MusicToggle";
import { CsChatWidget } from "./CsChatWidget";

type SessionInfo = { admin: boolean; client: boolean; csrf_token?: string };

const THEME_ICONS = (
  <>
    <svg
      className="icon-moon"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d="M20.2 15.4A8.5 8.5 0 0 1 8.6 3.8 8.6 8.6 0 1 0 20.2 15.4Z" />
      <path d="M16.5 4.5v4M14.5 6.5h4" />
    </svg>
    <svg
      className="icon-sun"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      aria-hidden="true"
    >
      <circle cx="12" cy="12" r="4" />
      <path d="M12 2v2m0 16v2M4.93 4.93l1.42 1.42m11.3 11.3 1.42 1.42M2 12h2m16 0h2M4.93 19.07l1.42-1.42m11.3-11.3 1.42-1.42" />
    </svg>
  </>
);

const NAV_TOGGLE_ICON = (
  <svg
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    strokeWidth="1.8"
    strokeLinecap="round"
    aria-hidden="true"
  >
    <path d="M4 7h16M4 12h16M4 17h16" />
  </svg>
);

function NavLink({
  href,
  active,
  title,
  desc,
  icon,
}: {
  href: string;
  active: boolean;
  title: string;
  desc: string;
  icon?: string;
}) {
  return (
    <a
      className={`nav-link${active ? " active" : ""}`}
      href={href}
      aria-current={active ? "location" : undefined}
    >
      <span className="nav-link__title">
        {icon && <svg aria-hidden="true"><use href={`/brand-kit/icons/algentra-icons-sprite.svg#${icon}`} /></svg>}
        {title}
      </span>
      <small>{desc}</small>
    </a>
  );
}

export function SiteFrame({ children }: { children: React.ReactNode }) {
  const pathname = usePathname();
  const [session, setSession] = useState<SessionInfo | null>(null);
  const [registrationOpen, setRegistrationOpen] = useState(true);
  const [hash, setHash] = useState("");
  const [navOpen, setNavOpen] = useState(false);
  const [loggingOut, setLoggingOut] = useState(false);
  const [theme, setTheme] = useState<"dark" | "light">("dark");
  // Sesi: kontrak apiRequest<{admin, client}>("auth/session"); gagal/401 -> anonim.
  useEffect(() => {
    let alive = true;
    apiRequest<SessionInfo>("auth/session")
      .then((info) => {
        if (alive) setSession({ admin: !!info.admin, client: !!info.client });
      })
      .catch(() => {
        if (alive) setSession(null);
      });
    // Varian nav anonim di halaman auth butuh tahu status pendaftaran (default terbuka).
    apiRequest<{ registration_open?: boolean }>("public/registration-status")
      .then((res) => {
        if (alive && typeof res.registration_open === "boolean") {
          setRegistrationOpen(res.registration_open);
        }
      })
      .catch(() => {
        /* tetap anggap pendaftaran terbuka seperti default template */
      });
    return () => {
      alive = false;
    };
  }, []);

  // Jejak hash untuk sinkronisasi nav aktif (pengganti syncWorkspaceNav/syncPublicNav).
  useEffect(() => {
    const syncHash = () => setHash(window.location.hash);
    syncHash();
    window.addEventListener("hashchange", syncHash);
    return () => window.removeEventListener("hashchange", syncHash);
  }, [pathname]);

  // Theme: baca pilihan awal dari data-theme (layout render dari cookie).
  useEffect(() => {
    const initial =
      document.documentElement.dataset.theme === "light" ? "light" : "dark";
    setTheme(initial);
    const meta = document.querySelector('meta[name="theme-color"]');
    if (meta) {
      meta.setAttribute("content", initial === "light" ? "#f2f2f2" : "#0d1a24");
    }
  }, []);

  const toggleTheme = useCallback(() => {
    setTheme((prev) => {
      const next = prev === "light" ? "dark" : "light";
      document.documentElement.dataset.theme = next;
      const secure = window.location.protocol === "https:" ? "; Secure" : "";
      document.cookie = `theme=${next}; Path=/; Max-Age=31536000; SameSite=Lax${secure}`;
      try {
        localStorage.setItem("theme", next);
      } catch {
        /* cookie keeps the preference across pages */
      }
      const meta = document.querySelector('meta[name="theme-color"]');
      if (meta) {
        meta.setAttribute("content", next === "light" ? "#f2f2f2" : "#0d1a24");
      }
      return next;
    });
  }, []);

  // Nav toggle mobile (pengganti JS base.html): tutup saat klik link / klik luar / Escape.
  useEffect(() => {
    const onDocClick = (event: MouseEvent) => {
      const target = event.target as HTMLElement | null;
      if (!target) return;
      if (!target.closest("#site-nav") && !target.closest("#nav-toggle")) {
        setNavOpen(false);
      }
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") setNavOpen(false);
    };
    const mq = window.matchMedia("(min-width: 601px)");
    const onMqChange = () => setNavOpen(false);
    mq.addEventListener?.("change", onMqChange);
    document.addEventListener("click", onDocClick);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      mq.removeEventListener?.("change", onMqChange);
      document.removeEventListener("click", onDocClick);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, []);

  const closeNavOnLinkClick = useCallback(
    (event: ReactMouseEvent<HTMLElement>) => {
      if ((event.target as HTMLElement).closest("a")) setNavOpen(false);
    },
    []
  );

  const handleLogout = useCallback(async () => {
    setLoggingOut(true);
    try {
      await apiRequest("auth/logout", { method: "POST" });
    } catch {
      /* tetap arahkan keluar walau POST gagal */
    } finally {
      try { sessionStorage.removeItem("algentra-csrf"); } catch { /* Storage may be disabled. */ }
      window.location.href = "/";
    }
  }, []);

  const isAdmin = session?.admin === true;
  const isClient = !isAdmin && session?.client === true;
  const isAuthPage = pathname === "/login";

  const themeLabel =
    theme === "light" ? "Beralih ke tema gelap" : "Beralih ke tema terang";

  const renderNav = () => {
    if (isAdmin) {
      const settingsActive = pathname === "/dashboard" && hash === "#settings";
      return (
        <>
          <NavLink
            href="/dashboard"
            active={pathname === "/dashboard" && !settingsActive}
            title="Workspace"
            desc="Overview & activity"
          />
          <NavLink
            href="/admin/copy-accounts"
            active={pathname === "/admin/copy-accounts"}
            title="Akun copy trading"
            desc="Saldo & riwayat akun"
          />
          <NavLink
            href="/parser-test"
            active={pathname === "/parser-test"}
            title="Parser test"
            desc="Review signal format"
          />
          <NavLink
            href="/telegram-setup"
            active={pathname === "/telegram-setup"}
            title="Telegram"
            desc="Connect & whitelist"
          />
          <NavLink
            href="/dashboard#settings"
            active={settingsActive}
            title="Controls"
            desc="Risk & configuration"
          />
          <button
            className="mobile-client-logout"
            type="button"
            data-workspace-logout
            disabled={loggingOut}
            aria-busy={loggingOut}
            onClick={handleLogout}
          >
            Logout
          </button>
        </>
      );
    }
    if (isClient) {
      return (
        <>
          <NavLink
            href="/account"
            active={pathname === "/account"}
            title="Akun Copy Trading"
            desc="Status akun MT5"
          />
          <NavLink
            href="/transactions"
            active={pathname.startsWith("/transactions")}
            title="Riwayat transaksi"
            desc="Nota & pembayaran"
          />
          <NavLink
            href="/#performance"
            active={false}
            title="Performa"
            desc="Data agregat"
          />
          <button
            className="mobile-client-logout"
            type="button"
            data-workspace-logout
            disabled={loggingOut}
            aria-busy={loggingOut}
            onClick={handleLogout}
          >
            Keluar
          </button>
        </>
      );
    }
    if (isAuthPage) {
      return (
        <>
          <NavLink href="/" active={false} title="Algentra Capital" desc="Beranda" />
          <NavLink href="/login" active title="Portal klien" desc="Masuk akun klien" icon="ac-client-portal" />
          {registrationOpen ? (
            <NavLink href="/register" active={false} title="Daftar" desc="Akun klien" />
          ) : (
            <NavLink
              href="/#registration-launch"
              active={false}
              title="Menunggu launching"
              desc="Pendaftaran client"
            />
          )}
        </>
      );
    }
    const activeHref =
      hash === "#performance"
        ? "/#performance"
        : hash === "#approach"
          ? "/#approach"
          : hash === "#contact"
            ? "/#contact"
            : pathname === "/" && hash === ""
              ? "/"
              : pathname === "/performance"
                ? "/#performance"
                : "";
    return (
      <>
        <NavLink href="/" active={activeHref === "/"} title="Beranda" desc="Algentra Capital" />
        <NavLink
          href="/#performance"
          active={activeHref === "/#performance"}
          title="Performa"
          desc="Data agregat"
        />
        <NavLink
          href="/#approach"
          active={activeHref === "/#approach"}
          title="Cara kerja"
          desc="AI & kontrol risiko"
        />
        <NavLink
          href="/#contact"
          active={activeHref === "/#contact"}
          title="Kontak"
          desc="Hubungi kami"
        />
        <NavLink
          href="/login"
          active={false}
          title="Portal klien"
          desc={registrationOpen ? "Masuk atau daftar" : "Masuk client"}
          icon="ac-client-portal"
        />
      </>
    );
  };

  return (
    <>
      <a className="skip-link" href="#main-content">
        Lewati ke konten
      </a>
      <header className="topbar">
        <a className="brand" href={isAdmin ? "/dashboard" : "/"}>
          <img className="brand-logo brand-logo--dark" src="/brand-kit/logo/algentra-horizontal-dark.svg" alt="Algentra Capital" />
          <img className="brand-logo brand-logo--light" src="/brand-kit/logo/algentra-horizontal-light.svg" alt="" aria-hidden="true" />
          <img className="brand-mark-image brand-mark-image--dark" src="/brand-kit/logo/algentra-mark-dark.svg" alt="" aria-hidden="true" />
          <img className="brand-mark-image brand-mark-image--light" src="/brand-kit/logo/algentra-mark-light.svg" alt="" aria-hidden="true" />
        </a>
        <nav
          className={`site-nav${navOpen ? " is-open" : ""}`}
          id="site-nav"
          aria-label="Navigasi utama"
          onClick={closeNavOnLinkClick}
        >
          {renderNav()}
        </nav>
        <div className="topbar-side">
          <button
            className="secondary icon-button"
            id="theme-toggle"
            type="button"
            aria-label={themeLabel}
            title={themeLabel}
            aria-pressed={theme === "light"}
            onClick={toggleTheme}
          >
            {THEME_ICONS}
          </button>
          <button
            className="secondary nav-toggle"
            id="nav-toggle"
            type="button"
            aria-label={navOpen ? "Tutup menu navigasi" : "Buka menu navigasi"}
            aria-controls="site-nav"
            aria-expanded={navOpen}
            onClick={() => setNavOpen((open) => !open)}
          >
            {NAV_TOGGLE_ICON}
          </button>
          {isAdmin && (
            <button
              className="secondary desktop-client-logout"
              id="logout"
              data-workspace-logout
              type="button"
              disabled={loggingOut}
              aria-busy={loggingOut}
              onClick={handleLogout}
            >
              Logout
            </button>
          )}
          {isClient && (
            <>
              <button
                className="secondary desktop-client-logout"
                id="logout"
                data-workspace-logout
                type="button"
                disabled={loggingOut}
                aria-busy={loggingOut}
                onClick={handleLogout}
              >
                Keluar
              </button>
            </>
          )}
        </div>
      </header>
      {children}
      {!isAdmin && pathname !== "/dashboard" && !pathname.startsWith("/admin") && (
        <CsChatWidget
          mode={pathname === "/account" || pathname.startsWith("/transactions") ? "client" : "public"}
        />
      )}
      <MusicToggle alone={isAdmin || pathname === "/dashboard" || pathname.startsWith("/admin")} />
      <footer>
        {isAdmin ? (
          <>
            Algentra Capital Workspace <span aria-hidden="true">·</span> Review risk controls
            before enabling live orders
          </>
        ) : (
          <>
            Algentra Capital <span aria-hidden="true">·</span> Trading memiliki risiko; hasil
            historis bukan jaminan kinerja mendatang.
          </>
        )}
      </footer>
    </>
  );
}
