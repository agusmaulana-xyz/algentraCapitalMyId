/* eslint-disable react/no-danger */
/*
 * app/components/PublicPage.tsx
 * Port faithful dari apps/core-api/app/templates/public.html (backup stabil added2df)
 * — struktur section, teks Indonesia, kelas/id, dan perilaku JS (polling, countdown,
 *   grafik equity SVG, watchlist) disalin verbatim dari template asli.
 * CSS .public-shell disediakan oleh Worker 1 di app/globals.css.
 */
"use client";

import { useEffect, useRef, useState } from "react";
import Mascot from "./Mascot";
import { SiteFrame } from "./SiteFrame";
import { apiRequest } from "../lib/api";

type RegistrationStatus = {
  registration_open: boolean;
  registration_launch_pending: boolean;
  registration_launch_at: string | null;
};

type EquityPoint = {
  hour: string;
  equity: number | string;
  currency?: string;
};

type MasterAccount = {
  account_key?: string;
  online?: boolean;
  available?: boolean;
  equity?: number | string;
  balance?: number | string;
  floating_profit?: number | string;
  currency?: string;
  trade_mode?: string;
  updated_at?: string;
};

type MarketQuote = {
  symbol?: string;
  bid?: number | string;
  ask?: number | string;
  updated_at?: string;
};

type PerformanceData = {
  registered_clients?: number;
  master_account?: MasterAccount;
  equity_curve?: EquityPoint[];
  market_quotes?: MarketQuote[];
  market_quotes_online?: boolean;
};

type Social = { url: string; icon: string; label: string; display: string };
type ContactPayload = {
  person: string;
  whatsapp: string;
  email: string;
  socials: Social[];
};

const WATCHLIST = ["XAUUSD", "EURUSD", "USDJPY", "GBPUSD"];
const DIGITS: Record<string, number> = { XAUUSD: 2, EURUSD: 5, USDJPY: 3, GBPUSD: 5 };
const FRESH_MS = 120000;

/** Target fallback bila API tidak memberi registration_launch_at: 16 Jan 2027 09.00 WIB. */
const FALLBACK_LAUNCH_AT = "2027-01-16T09:00:00+07:00";

function SocialIcon({ icon }: { icon: string }) {
  const common = {
    viewBox: "0 0 24 24",
    fill: "none",
    stroke: "currentColor",
    strokeWidth: 1.7,
    strokeLinecap: "round",
    strokeLinejoin: "round",
    "aria-hidden": true,
  } as const;
  if (icon === "instagram")
    return (
      <svg {...common}>
        <rect x="3" y="3" width="18" height="18" rx="5"></rect>
        <circle cx="12" cy="12" r="4"></circle>
        <circle cx="18" cy="6" r="1" fill="currentColor" stroke="none"></circle>
      </svg>
    );
  if (icon === "telegram")
    return (
      <svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
        <path d="M21.5 4.4 18.2 20c-.25 1.1-.9 1.35-1.82.84l-5.04-3.72-2.43 2.34c-.27.27-.5.5-1.02.5l.37-5.14 9.36-8.46c.4-.36-.09-.56-.62-.2L5.43 13.45.48 11.9c-1.08-.34-1.1-1.08.23-1.58L19.99 2.9c.9-.34 1.69.22 1.51 1.5Z"></path>
      </svg>
    );
  if (icon === "tiktok")
    return (
      <svg {...common} strokeWidth={1.8}>
        <path d="M14 3v11.1a4.1 4.1 0 1 1-3.4-4"></path>
        <path d="M14 3c.5 2.8 2.2 4.5 5 5"></path>
      </svg>
    );
  if (icon === "website")
    return (
      <svg {...common}>
        <circle cx="12" cy="12" r="9"></circle>
        <path d="M3 12h18M12 3a14 14 0 0 1 0 18M12 3a14 14 0 0 0 0 18"></path>
      </svg>
    );
  return (
    <svg {...common}>
      <path d="M10 13a5 5 0 0 0 7.07 0l3-3A5 5 0 0 0 13 2.93l-1.72 1.72"></path>
      <path d="M14 11a5 5 0 0 0-7.07 0l-3 3A5 5 0 0 0 11 21.07l1.72-1.72"></path>
    </svg>
  );
}

export default function PublicPage() {
  const [registration, setRegistration] = useState<RegistrationStatus | null>(null);
  const [contact, setContact] = useState<ContactPayload | null>(null);
  const registrationOpenRef = useRef<boolean | null>(null);

  /* ---------- Refs countdown ---------- */
  const daysRef = useRef<HTMLElement | null>(null);
  const hoursRef = useRef<HTMLElement | null>(null);
  const minutesRef = useRef<HTMLElement | null>(null);
  const secondsRef = useRef<HTMLElement | null>(null);
  const countdownNoteRef = useRef<HTMLParagraphElement | null>(null);

  /* ---------- Refs performa (manipulasi DOM imperatif seperti template) ---------- */
  const countRef = useRef<HTMLElement | null>(null);
  const statusRef = useRef<HTMLSpanElement | null>(null);
  const equityLabelRef = useRef<HTMLSpanElement | null>(null);
  const equityValueRef = useRef<HTMLElement | null>(null);
  const equityBalanceRef = useRef<HTMLElement | null>(null);
  const equityFloatingRef = useRef<HTMLElement | null>(null);
  const equityLowRef = useRef<HTMLElement | null>(null);
  const equityHighRef = useRef<HTMLElement | null>(null);
  const equityUpdatedRef = useRef<HTMLSpanElement | null>(null);
  const wrapRef = useRef<HTMLDivElement | null>(null);
  const svgRef = useRef<SVGSVGElement | null>(null);
  const emptyRef = useRef<HTMLDivElement | null>(null);
  const gapRef = useRef<SVGRectElement | null>(null);
  const gapLabelRef = useRef<SVGTextElement | null>(null);
  const gridRef = useRef<SVGPathElement | null>(null);
  const areaRef = useRef<SVGPathElement | null>(null);
  const lineRef = useRef<SVGPathElement | null>(null);
  const pointRef = useRef<SVGCircleElement | null>(null);
  const yLabelsRef = useRef<SVGGElement | null>(null);
  const xLabelsRef = useRef<SVGGElement | null>(null);
  const metaRef = useRef<HTMLDivElement | null>(null);
  const deltaRef = useRef<HTMLSpanElement | null>(null);
  const firstRef = useRef<HTMLSpanElement | null>(null);
  const latestRef = useRef<HTMLSpanElement | null>(null);
  const pricesRef = useRef<HTMLDivElement | null>(null);
  const marketStatusRef = useRef<HTMLSpanElement | null>(null);

  /* ---------- Status pendaftaran: poll tiap 10 dtk, reload bila berubah ---------- */
  useEffect(() => {
    let cancelled = false;
    const checkRegistrationStatus = async () => {
      if (document.hidden) return;
      try {
        const status = await apiRequest<RegistrationStatus>("public/registration-status");
        if (cancelled) return;
        if (registrationOpenRef.current === null) {
          registrationOpenRef.current = status.registration_open;
        } else if (status.registration_open !== registrationOpenRef.current) {
          window.location.reload();
          return;
        }
        setRegistration(status);
      } catch {
        /* abaikan seperti template */
      }
    };
    checkRegistrationStatus();
    const timer = window.setInterval(checkRegistrationStatus, 10000);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, []);

  /* ---------- Countdown launching ---------- */
  useEffect(() => {
    if (!registration || registration.registration_open || !registration.registration_launch_pending) return;
    const raw = registration.registration_launch_at || FALLBACK_LAUNCH_AT;
    const launchAt = new Date(raw).getTime();
    if (!Number.isFinite(launchAt)) return;
    const units = [daysRef.current, hoursRef.current, minutesRef.current, secondsRef.current];
    const note = countdownNoteRef.current;
    let countdownTimer: number | null = null;
    const updateCountdown = () => {
      const remaining = Math.max(0, launchAt - Date.now());
      const values = [
        Math.floor(remaining / 86400000),
        Math.floor((remaining % 86400000) / 3600000),
        Math.floor((remaining % 3600000) / 60000),
        Math.floor((remaining % 60000) / 1000),
      ];
      units.forEach((unit, index) => {
        if (unit) unit.textContent = String(values[index]).padStart(index === 0 ? 1 : 2, "0");
      });
      if (remaining === 0) {
        if (note) note.textContent = "Waktu launching tiba. Memeriksa pembukaan pendaftaran...";
        if (countdownTimer) window.clearInterval(countdownTimer);
      }
    };
    updateCountdown();
    if (Date.now() < launchAt) countdownTimer = window.setInterval(updateCountdown, 1000);
    return () => {
      if (countdownTimer) window.clearInterval(countdownTimer);
    };
  }, [registration]);

  /* ---------- Data kontak ---------- */
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const data = await apiRequest<ContactPayload>("public/contact");
        if (!cancelled) setContact(data);
      } catch {
        /* fallback "-" dipakai di render */
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  /* ---------- Equity, watchlist, polling performa ---------- */
  useEffect(() => {
    const fmt = (value: number | string, min: number, max: number) =>
      Number(value).toLocaleString("id-ID", { minimumFractionDigits: min, maximumFractionDigits: max });
    const money = (value: number | string | undefined, currency?: string) =>
      `${fmt(value || 0, 2, 2)} ${currency || ""}`.trim();
    const signed = (value: number, suffix = "") =>
      `${value > 0 ? "+" : value < 0 ? "\u2212" : ""}${fmt(Math.abs(value), 2, 2)}${suffix}`;
    const price = (symbol: string, value: number | string) => {
      const d = DIGITS[symbol];
      return fmt(value, d ?? 2, d ?? 5);
    };
    const tone = (value: number) => (value > 0 ? "pos" : value < 0 ? "neg" : "");
    const modeLabel = (mode?: string) =>
      ({ real: "Live", demo: "Demo", contest: "Kompetisi" } as Record<string, string>)[mode || ""] || mode || "MT5";
    const wib = (value: string | number | Date, opts?: Intl.DateTimeFormatOptions) =>
      `${new Date(value).toLocaleString("id-ID", { timeZone: "Asia/Jakarta", ...opts })} WIB`;
    const el = (
      tag: string,
      className: string | undefined,
      text: string | undefined,
      parent: HTMLElement | null
    ): HTMLElement => {
      const node = document.createElement(tag);
      if (className) node.className = className;
      if (text !== undefined) node.textContent = text;
      if (parent) parent.append(node);
      return node;
    };
    const setStatus = (node: HTMLElement | null, text: string, state?: string) => {
      if (!node) return;
      node.textContent = text;
      if (state) node.dataset.state = state;
      else node.removeAttribute("data-state");
    };

    /* ---------- Grafik equity ---------- */
    let lastCurve: EquityPoint[] = [];
    let liveCurve: EquityPoint[] = [];
    let equityHistory: EquityPoint[] = [];
    let activeEquityAccountKey: string | null = null;
    const WIB_OFFSET_MS = 7 * 60 * 60 * 1000;
    const equitySessionStart = (timestamp = Date.now()) => {
      const jakartaTime = new Date(timestamp + WIB_OFFSET_MS);
      if (jakartaTime.getUTCHours() < 7) jakartaTime.setUTCDate(jakartaTime.getUTCDate() - 1);
      jakartaTime.setUTCHours(7, 0, 0, 0);
      return jakartaTime.getTime() - WIB_OFFSET_MS;
    };

    const drawChart = () => {
      const curve = lastCurve.filter(
        (point) => Number.isFinite(new Date(point.hour).getTime()) && Number.isFinite(Number(point.equity))
      );
      if (!curve.length) return;
      const svg = svgRef.current;
      const wrap = wrapRef.current;
      if (!svg || !wrap) return;
      const rect = wrap.getBoundingClientRect();
      const width = Math.round(rect.width) || 640;
      const height = Math.round(rect.height) || 200;
      const sessionStart = equitySessionStart();
      const now = Date.now();
      const times = curve.map((point) => new Date(point.hour).getTime());
      const values = curve.map((point) => Number(point.equity));
      const min = Math.min(...values);
      const max = Math.max(...values);
      const padding = Math.max((max - min) * 0.1, Math.max(Math.abs(max) * 0.002, 0.01));
      const lower = min - padding;
      const upper = max + padding;
      const spread = upper - lower;
      const padLeft = 8;
      const padRight = Math.min(90, Math.max(72, width * 0.25));
      const padTop = 14;
      const padBottom = 30;
      const plotH = height - padTop - padBottom;
      svg.setAttribute("viewBox", `0 0 ${width} ${height}`);
      const coords = values.map((value, index) => ({
        x:
          padLeft +
          Math.max(0, Math.min(1, (times[index] - sessionStart) / Math.max(1, now - sessionStart))) *
            (width - padLeft - padRight),
        y: padTop + ((upper - value) * plotH) / spread,
      }));
      const line = coords.map((point, index) => `${index ? "L" : "M"}${point.x.toFixed(1)} ${point.y.toFixed(1)}`).join(" ");
      const base = height - padBottom;
      const plotRight = width - padRight;
      const gap = gapRef.current;
      const gapLabel = gapLabelRef.current;
      const gapWidth = coords[0].x - padLeft;
      if (gap && gapLabel) {
        if (gapWidth > 110) {
          gap.style.display = "";
          gap.setAttribute("x", String(padLeft));
          gap.setAttribute("y", String(padTop));
          gap.setAttribute("width", String(gapWidth));
          gap.setAttribute("height", String(plotH));
          gapLabel.style.display = "";
          gapLabel.setAttribute("x", String(padLeft + gapWidth / 2));
          gapLabel.setAttribute("y", String(padTop + plotH / 2));
          const firstReportTime = wib(new Date(times[0]), { hour: "2-digit", minute: "2-digit" });
          gapLabel.textContent =
            gapWidth > 220 ? `Data tersedia mulai ${firstReportTime}` : `Mulai ${firstReportTime}`;
        } else {
          gap.style.display = "none";
          gapLabel.style.display = "none";
        }
      }
      gridRef.current?.setAttribute(
        "d",
        [0, 1, 2, 3].map((k) => `M${padLeft} ${(padTop + (k * plotH) / 3).toFixed(1)}H${plotRight}`).join(" ")
      );
      lineRef.current?.setAttribute("d", line);
      const lastCoord = coords.at(-1);
      areaRef.current?.setAttribute(
        "d",
        `${line} L${lastCoord?.x.toFixed(1)} ${base} L${coords[0].x.toFixed(1)} ${base} Z`
      );
      if (lastCoord && pointRef.current) {
        pointRef.current.setAttribute("cx", String(lastCoord.x));
        pointRef.current.setAttribute("cy", String(lastCoord.y));
      }
      const yLabels = yLabelsRef.current;
      const xLabels = xLabelsRef.current;
      if (yLabels) yLabels.replaceChildren();
      if (xLabels) xLabels.replaceChildren();
      const svgNS = "http://www.w3.org/2000/svg";
      [0, 1, 2].forEach((index) => {
        if (!yLabels) return;
        const value = upper - (spread * index) / 2;
        const label = document.createElementNS(svgNS, "text");
        label.setAttribute("x", String(width - 4));
        label.setAttribute("y", String(padTop + (plotH * index) / 2 + 4));
        label.setAttribute("text-anchor", "end");
        label.textContent = money(value, curve.at(-1)?.currency || "");
        yLabels.append(label);
      });
      if (xLabels) {
        const timeTicks = width < 460 ? 3 : 5;
        for (let index = 0; index < timeTicks; index++) {
          const ratio = index / (timeTicks - 1);
          const label = document.createElementNS(svgNS, "text");
          label.setAttribute("x", String(padLeft + ratio * (plotRight - padLeft)));
          label.setAttribute("y", String(height - 5));
          label.setAttribute("text-anchor", index === 0 ? "start" : index === timeTicks - 1 ? "end" : "middle");
          label.textContent = wib(new Date(sessionStart + ratio * Math.max(0, now - sessionStart)), {
            hour: "2-digit",
            minute: "2-digit",
          });
          xLabels.append(label);
        }
      }
    };

    const showEquityChart = (curve: EquityPoint[]) => {
      lastCurve = curve;
      const svg = svgRef.current;
      const empty = emptyRef.current;
      const meta = metaRef.current;
      const delta = deltaRef.current;
      const low = equityLowRef.current;
      const high = equityHighRef.current;
      if (!svg || !empty || !meta || !delta || !low || !high) return;
      if (!curve.length) {
        svg.style.display = "none";
        meta.hidden = true;
        empty.hidden = false;
        delta.textContent = "";
        delta.removeAttribute("data-tone");
        low.textContent = "—";
        high.textContent = "—";
        return;
      }
      empty.hidden = true;
      svg.style.display = "";
      meta.hidden = false;
      svg.setAttribute("aria-label", "Grafik equity akun utama sejak pukul 07.00 WIB");
      const currency = curve.at(-1)?.currency || "";
      const values = curve.map((point) => Number(point.equity));
      const change = values.at(-1)! - values[0];
      const pct = values[0] ? (change / values[0]) * 100 : 0;
      delta.textContent = `${signed(change, currency ? ` ${currency}` : "")} (${signed(pct, "%")})`;
      delta.dataset.tone = tone(change);
      low.textContent = money(Math.min(...values), currency);
      high.textContent = money(Math.max(...values), currency);
      low.title = low.textContent;
      high.title = high.textContent;
      const now = Date.now();
      if (firstRef.current)
        firstRef.current.textContent = `Mulai ${wib(new Date(equitySessionStart(now)), { hour: "2-digit", minute: "2-digit" })}`;
      if (latestRef.current)
        latestRef.current.textContent = `Sekarang ${wib(new Date(now), { hour: "2-digit", minute: "2-digit", second: "2-digit" })}`;
      drawChart();
    };

    let chartObserver: ResizeObserver | null = null;
    if (window.ResizeObserver && wrapRef.current) {
      chartObserver = new ResizeObserver(drawChart);
      chartObserver.observe(wrapRef.current);
    }

    /* ---------- Watchlist ---------- */
    const prevPrices: Record<string, { bid?: number; ask?: number }> = {};
    const quoteState = { online: false };
    const ageText = (ms: number) => {
      if (!Number.isFinite(ms)) return "";
      const sec = Math.max(0, Math.round(ms / 1000));
      if (sec < 5) return "baru saja";
      if (sec < 60) return `${sec} dtk lalu`;
      if (sec < 3600) return `${Math.floor(sec / 60)} mnt lalu`;
      return `${Math.floor(sec / 3600)} jam lalu`;
    };
    const isFresh = (updatedAt: number) => {
      const age = updatedAt ? Date.now() - updatedAt : Infinity;
      return quoteState.online && age <= FRESH_MS && age >= -60000;
    };
    const refreshFreshness = () => {
      const pricesBox = pricesRef.current;
      const status = marketStatusRef.current;
      if (!pricesBox || !status) return;
      const cards = pricesBox.querySelectorAll(".quote-card[data-updated]");
      let liveCount = 0;
      cards.forEach((card) => {
        const updatedAt = Number((card as HTMLElement).dataset.updated);
        const live = isFresh(updatedAt);
        card.classList.toggle("is-live", live);
        if (live) liveCount++;
        const age = card.querySelector(".quote-age");
        if (age) {
          const t = ageText(Date.now() - updatedAt);
          age.textContent = live || !t ? t : `Terakhir ${t}`;
        }
      });
      if (quoteState.online) {
        setStatus(status, "ONLINE", "live");
        status.title = liveCount
          ? "Koneksi EA aktif dan harga diterima langsung."
          : "Koneksi EA aktif; belum ada tick harga baru.";
      } else {
        setStatus(status, "RECONNECT", "warn");
        status.title = cards.length
          ? "Koneksi harga EA terputus. Nilai yang tampil adalah harga terakhir."
          : "Menunggu laporan harga EA.";
      }
    };

    const renderQuotes = (quotes: MarketQuote[] | undefined, reportOnline: boolean | undefined) => {
      quoteState.online = !!reportOnline;
      const pricesBox = pricesRef.current;
      if (!pricesBox) return;
      const list = (quotes || []).map((quote) => ({ ...quote, key: String(quote.symbol || "").toUpperCase() }));
      pricesBox.replaceChildren();
      for (const symbol of WATCHLIST) {
        const quote = list.find((item) => item.key === symbol) || list.find((item) => item.key.startsWith(symbol));
        const card = el("article", "quote-card", undefined, pricesBox);
        const head = el("div", "quote-symbol", undefined, card);
        el("i", "live-dot", undefined, head).setAttribute("aria-hidden", "true");
        el("span", "section-kicker", symbol, head);
        if (quote) {
          const bid = Number(quote.bid);
          const ask = Number(quote.ask);
          const updatedAt = quote.updated_at ? new Date(quote.updated_at).getTime() : NaN;
          if (Number.isFinite(updatedAt)) card.dataset.updated = String(updatedAt);
          const prev = prevPrices[symbol] || {};
          const dir = (now: number, before: number | undefined) =>
            before === undefined || now === before ? "" : now > before ? "tick-up" : "tick-down";
          const bidLine = el("div", "quote-line quote-bid", undefined, card);
          el("span", "", "Bid", bidLine);
          el("strong", dir(bid, prev.bid), price(symbol, bid), bidLine);
          const askLine = el("div", "quote-line quote-ask", undefined, card);
          el("span", "", "Ask", askLine);
          el("strong", dir(ask, prev.ask), price(symbol, ask), askLine);
          prevPrices[symbol] = { bid, ask };
          const meta = el("div", "quote-meta", undefined, card);
          const d = DIGITS[symbol] ?? 2;
          el("span", "", `Spread ${fmt(Math.abs(ask - bid), d, d)}`, meta);
          el("span", "quote-age", Number.isFinite(updatedAt) ? ageText(Date.now() - updatedAt) : "", meta);
        } else {
          el("span", "quote-empty", "Belum tersedia", card);
        }
      }
      refreshFreshness();
    };
    const freshnessTimer = window.setInterval(refreshFreshness, 1000);

    /* ---------- Equity & performa ---------- */
    const renderPerformance = (data: PerformanceData) => {
      if (countRef.current)
        countRef.current.textContent = Number(data.registered_clients || 0).toLocaleString("id-ID");
      const master = data.master_account || {};
      if (master.account_key && master.account_key !== activeEquityAccountKey) {
        activeEquityAccountKey = master.account_key;
        liveCurve = [];
        equityHistory = [];
      }
      const status = statusRef.current;
      if (master.online) setStatus(status, "EA utama online", "live");
      else if (master.available) setStatus(status, "Offline · equity terakhir", "warn");
      else setStatus(status, "Menunggu laporan", "");

      const label = equityLabelRef.current;
      const value = equityValueRef.current;
      const balance = equityBalanceRef.current;
      const floating = equityFloatingRef.current;
      const updated = equityUpdatedRef.current;
      if (master.available) {
        const fp = Number(master.floating_profit || 0);
        if (label) label.textContent = `Equity akun utama · ${modeLabel(master.trade_mode)}`;
        if (value) {
          value.textContent = money(master.equity, master.currency);
          value.title = money(master.equity, master.currency);
        }
        if (balance) {
          balance.textContent = money(master.balance, master.currency);
          balance.title = money(master.balance, master.currency);
        }
        if (floating) {
          floating.textContent = money(fp, master.currency);
          floating.title = money(fp, master.currency);
          floating.dataset.tone = tone(fp);
        }
        if (updated)
          updated.textContent = master.updated_at
            ? `Diperbarui ${wib(master.updated_at, { day: "2-digit", month: "short", hour: "2-digit", minute: "2-digit", second: "2-digit" })}`
            : "";
      } else {
        if (label) label.textContent = "Equity akun utama";
        if (value) {
          value.textContent = "—";
          value.removeAttribute("title");
        }
        if (balance) {
          balance.textContent = "—";
          balance.removeAttribute("title");
        }
        if (floating) {
          floating.textContent = "—";
          floating.removeAttribute("data-tone");
          floating.removeAttribute("title");
        }
        if (updated) updated.textContent = "Equity akan tampil setelah EA utama mengirim laporan akun MT5.";
      }
      renderQuotes(data.market_quotes, data.market_quotes_online);
      if (master.available && master.updated_at) {
        const timestamp = new Date(master.updated_at).getTime();
        const equity = Number(master.equity);
        if (Number.isFinite(timestamp) && Number.isFinite(equity)) {
          const latest = liveCurve.at(-1);
          const latestTime = latest ? new Date(latest.hour).getTime() : 0;
          if (!latest || timestamp - latestTime >= 1000 || equity !== Number(latest.equity)) {
            liveCurve.push({ hour: master.updated_at, equity, currency: master.currency || "" });
          }
          const cutoff = equitySessionStart();
          liveCurve = liveCurve.filter((point) => new Date(point.hour).getTime() >= cutoff).slice(-180);
        }
      }
      if (Array.isArray(data.equity_curve)) {
        const cutoff = equitySessionStart();
        equityHistory = data.equity_curve.filter((point) => new Date(point.hour).getTime() >= cutoff);
      }
      const sessionCutoff = equitySessionStart();
      equityHistory = equityHistory.filter((point) => new Date(point.hour).getTime() >= sessionCutoff);
      liveCurve = liveCurve.filter((point) => new Date(point.hour).getTime() >= sessionCutoff);
      const points = new Map<number, EquityPoint>();
      for (const point of [...equityHistory, ...liveCurve]) {
        const timestamp = new Date(point.hour).getTime();
        if (Number.isFinite(timestamp) && Number.isFinite(Number(point.equity))) points.set(timestamp, point);
      }
      const curve = [...points.entries()].sort(([left], [right]) => left - right).map(([, point]) => point);
      showEquityChart(curve);
    };

    /* ---------- Koneksi SSE live ---------- */
    const performanceStream = new EventSource("/api/public/performance/stream");
    performanceStream.addEventListener("performance", (event) => {
      try {
        renderPerformance(JSON.parse((event as MessageEvent<string>).data) as PerformanceData);
      } catch (error) {
        const status = marketStatusRef.current;
        if (status) {
          setStatus(status, "RECONNECT", "warn");
          status.title = `Data performa gagal dibaca: ${error instanceof Error ? error.message : error}`;
        }
      }
    });
    performanceStream.onerror = () => {
      const status = marketStatusRef.current;
      if (status) setStatus(status, "RECONNECT", "warn");
    };

    let resizeFrame = 0;
    const onResize = () => {
      cancelAnimationFrame(resizeFrame);
      resizeFrame = requestAnimationFrame(() => {
        if (lastCurve.length) showEquityChart(lastCurve);
      });
    };
    window.addEventListener("resize", onResize);

    return () => {
      performanceStream.close();
      window.clearInterval(freshnessTimer);
      window.removeEventListener("resize", onResize);
      cancelAnimationFrame(resizeFrame);
      chartObserver?.disconnect();
    };
  }, []);
  /* ================= RENDER ================= */
  const person = contact?.person || "";
  const whatsapp = contact?.whatsapp || "";
  const email = contact?.email || "";
  const socials = contact?.socials ?? [];
  const hasContactDirect = Boolean(whatsapp || email);
  const registrationOpen = registration?.registration_open;

  return (
    <SiteFrame>
      <main id="main-content" tabIndex={-1} className="public-shell">
        <section className="hero">
          <div className="hero-copy">
            <span className="section-kicker">Analisis AI • Copy Trading • Kontrol Risiko</span>
            <h1>
              Sinyal dianalisis AI, eksekusi berjalan <em>otomatis.</em>
            </h1>
            <p className="hero-text">
              AI membantu membaca dan menyusun sinyal XAUUSD dari kanal Telegram pilihan. EA (robot MetaTrader 5)
              menjalankan instruksi sesuai pengaturan dan batas risiko; semua posisi terbuka di akun utama, termasuk
              entry manual, dapat disalin ke akun Copy Trading yang terhubung.
            </p>
            <div className="hero-actions">
              <a className="public-button" href="#performance">
                Lihat performa
              </a>
              <a className="public-button secondary" href="#approach">
                Pelajari cara kerja
              </a>
              {registrationOpen === true && (
                <a className="public-button" href="/register">
                  Daftar client
                </a>
              )}
              {registrationOpen === false && (
                <a className="public-button secondary" href="/#registration-launch">
                  Menunggu launching
                </a>
              )}
            </div>
          </div>
          <div className="hero-visual" aria-hidden="true">
            <div className="signal-orbit">
              <span className="orbit-line"></span>
              <span className="orbit-node one"></span>
              <span className="orbit-node two"></span>
              <span className="orbit-node three"></span>
              <div className="orbit-core">
                <svg
                  viewBox="0 0 24 24"
                  fill="none"
                  stroke="currentColor"
                  strokeWidth="1.5"
                  strokeLinecap="round"
                  strokeLinejoin="round"
                >
                  <path d="M12 16v5" />
                  <path d="M16 14.639V21" />
                  <path d="M20 10.656V21" />
                  <path d="m22 3-8.646 8.646a.5.5 0 0 1-.708 0L9.354 8.354a.5.5 0 0 0-.707 0L2 15" />
                  <path d="M4 18.463V21" />
                  <path d="M8 14.656V21" />
                </svg>
                <small>AI • MT5</small>
              </div>
            </div>
            <span className="visual-caption">Dari sinyal ke eksekusi otomatis</span>
          </div>
        </section>

        {registration && !registration.registration_open && (
          <section
            className="registration-launch public-section"
            id="registration-launch"
            aria-labelledby="registration-launch-heading"
          >
            {registration.registration_launch_pending ? (
              <>
                <span className="section-kicker">Launching pendaftaran client</span>
                <h2 id="registration-launch-heading">Pendaftaran segera dibuka</h2>
                <p>Hitung mundur menuju 16 Januari 2027 pukul 09.00 WIB.</p>
                <div
                  className="registration-countdown"
                  id="registration-countdown"
                  role="timer"
                  aria-label="Hitung mundur menuju pembukaan pendaftaran"
                >
                  <div className="registration-countdown-unit">
                    <strong id="registration-days" ref={daysRef}>
                      --
                    </strong>
                    <span>Hari</span>
                  </div>
                  <div className="registration-countdown-unit">
                    <strong id="registration-hours" ref={hoursRef}>
                      --
                    </strong>
                    <span>Jam</span>
                  </div>
                  <div className="registration-countdown-unit">
                    <strong id="registration-minutes" ref={minutesRef}>
                      --
                    </strong>
                    <span>Menit</span>
                  </div>
                  <div className="registration-countdown-unit">
                    <strong id="registration-seconds" ref={secondsRef}>
                      --
                    </strong>
                    <span>Detik</span>
                  </div>
                </div>
                <p id="registration-countdown-note" ref={countdownNoteRef}>
                  Pendaftaran akan terbuka otomatis saat waktu launching tiba.
                </p>
              </>
            ) : (
              <>
                <span className="section-kicker">Pendaftaran client</span>
                <h2 id="registration-launch-heading">Pendaftaran sedang ditutup</h2>
                <p>Launching telah dimulai. Pantau informasi terbaru untuk mengetahui kapan pendaftaran dibuka kembali.</p>
              </>
            )}
          </section>
        )}

        <section className="community-card" aria-labelledby="community-heading">
          <div className="community-emblem" aria-hidden="true">
            <img src="/brand-kit/icons/client-portal.svg" alt="" />
          </div>
          <div className="community-copy">
            <span className="section-kicker">Portal klien</span>
            <h2 id="community-heading">
              Pengguna terverifikasi
              <img className="inline-brand-icon" src="/brand-kit/icons/verified.svg" alt="" />
            </h2>
            <p>Jumlah akun klien yang menyelesaikan verifikasi email.</p>
          </div>
          <div className="community-count" aria-label="Jumlah akun klien terverifikasi">
            <strong id="registered-client-count" aria-live="polite" ref={countRef}>
              &mdash;
            </strong>
            <span>akun</span>
          </div>
        </section>

        <section className="public-section" id="performance">
          <div className="section-heading">
            <div>
              <span className="section-kicker">Performa</span>
              <h2>Index Equity Algentra Capital</h2>
              <p>Equity akun utama MT5 yang menjalankan Telegram Signal EA.</p>
            </div>
            <span id="master-account-status" className="status-pill" ref={statusRef}>
              Menunggu laporan
            </span>
          </div>
          <div className="market-overview-grid">
            <article className="panel equity-panel">
              <div className="equity-layout">
                <div className="equity-info">
                  <div className="panel-head">
                    <span id="equity-label" className="section-kicker" ref={equityLabelRef}>
                      Equity account
                    </span>
                  </div>
                  <strong id="equity-value" className="equity-value" ref={equityValueRef}>
                    &mdash;
                  </strong>
                  <div className="equity-stats">
                    <div className="stat">
                      <small>Saldo MT5</small>
                      <b id="equity-balance" ref={equityBalanceRef}>
                        &mdash;
                      </b>
                    </div>
                    <div className="stat">
                      <small>Floating profit</small>
                      <b id="equity-floating" ref={equityFloatingRef}>
                        &mdash;
                      </b>
                    </div>
                    <div className="stat">
                      <small>Terendah sesi</small>
                      <b id="equity-low" ref={equityLowRef}>
                        &mdash;
                      </b>
                    </div>
                    <div className="stat">
                      <small>Tertinggi sesi</small>
                      <b id="equity-high" ref={equityHighRef}>
                        &mdash;
                      </b>
                    </div>
                  </div>
                  <span id="equity-updated" className="equity-updated" ref={equityUpdatedRef}></span>
                </div>
                <div className="equity-chart">
                  <div className="chart-head">
                    <span className="chart-caption">Realtime Equity</span>
                    <span
                      id="equity-chart-delta"
                      className="delta-chip"
                      title="Perubahan equity sejak sesi dimulai pukul 07.00 WIB"
                      ref={deltaRef}
                    ></span>
                  </div>
                  <div id="equity-chart-wrap" className="chart-wrap" ref={wrapRef}>
                    <div id="equity-chart-empty" className="chart-empty" ref={emptyRef}>
                      Grafik akan terisi setelah EA utama melaporkan equity.
                    </div>
                    <svg
                      id="equity-chart"
                      ref={svgRef}
                      viewBox="0 0 640 200"
                      role="img"
                      aria-label="Grafik equity akun utama sejak pukul 07.00 WIB"
                      style={{ display: "none" }}
                    >
                      <defs>
                        <linearGradient id="equityFill" x1="0" x2="0" y1="0" y2="1">
                          <stop offset="0%" stopColor="currentColor" stopOpacity=".22" />
                          <stop offset="100%" stopColor="currentColor" stopOpacity="0" />
                        </linearGradient>
                      </defs>
                      <rect id="equity-chart-gap" className="chart-gap" ref={gapRef} style={{ display: "none" }} />
                      <path id="equity-chart-grid" className="chart-grid" ref={gridRef} />
                      <path id="equity-chart-area" className="chart-area" ref={areaRef} fill="url(#equityFill)" />
                      <path id="equity-chart-line" className="chart-line" ref={lineRef} />
                      <circle id="equity-chart-point" className="chart-point" ref={pointRef} r="5" />
                      <text id="equity-chart-gap-label" className="chart-gap-label" ref={gapLabelRef} style={{ display: "none" }}>
                        Riwayat belum tersedia
                      </text>
                      <g id="equity-chart-y-labels" className="chart-axis-label" ref={yLabelsRef}></g>
                      <g id="equity-chart-x-labels" className="chart-axis-label" ref={xLabelsRef}></g>
                    </svg>
                  </div>
                  <div id="equity-chart-meta" className="chart-meta" ref={metaRef} hidden>
                    <span id="equity-chart-first" ref={firstRef}></span>
                    <span id="equity-chart-latest" ref={latestRef}></span>
                  </div>
                </div>
              </div>
            </article>
            <article className="panel watchlist-panel">
              <div className="panel-head">
                <span className="section-kicker">
                  Watchlist harga
                </span>
                <span id="market-status" className="status-pill" data-state="warn" ref={marketStatusRef}>
                  RECONNECT
                </span>
              </div>
              <div id="market-prices" className="quote-list" ref={pricesRef}></div>
            </article>
          </div>
        </section>

        <section className="public-section" id="approach">
          <div className="section-heading">
            <div>
              <span className="section-kicker">Alur copy trading</span>
              <h2>Dari sinyal hingga posisi tersalin.</h2>
              <p>
                AI membantu menyusun informasi sinyal. EA MetaTrader menjalankan instruksi yang lolos, sementara
                posisi terbuka di akun utama termasuk entry manual disinkronkan ke akun Copy Trading.
              </p>
            </div>
          </div>
          <div className="approach-grid">
            <article className="approach-card">
              <div className="approach-top">
                <span className="approach-icon">
                  <img src="/brand-kit/icons/signal-source.svg" alt="" />
                </span>
                <span className="index">01 / SUMBER</span>
              </div>
              <h3>Pilih kanal sinyal</h3>
              <p>Pesan dari kanal Telegram yang dipilih disaring untuk menemukan sinyal XAUUSD yang relevan.</p>
            </article>
            <article className="approach-card">
              <div className="approach-top">
                <span className="approach-icon">
                  <img src="/brand-kit/icons/automation-ea.svg" alt="" />
                </span>
                <span className="index">02 / ANALISIS AI</span>
              </div>
              <h3>Susun detail sinyal</h3>
              <p>AI membantu mengenali arah, harga masuk, target, dan batas rugi sebelum instruksi diteruskan.</p>
            </article>
            <article className="approach-card">
              <div className="approach-top">
                <span className="approach-icon">
                  <img src="/brand-kit/icons/copy-trade.svg" alt="" />
                </span>
                <span className="index">03 / OTOMASI MT5</span>
              </div>
              <h3>Eksekusi dan salin</h3>
              <p>
                EA menjalankan instruksi sesuai pengaturan. Posisi terbuka di akun utama, termasuk entry manual,
                dapat disalin ke akun Copy Trading.
              </p>
            </article>
          </div>
        </section>

        <section className="public-section" id="contact">
          <div className="section-heading">
            <div>
              <span className="section-kicker">Informasi dan bantuan</span>
              <h2>Terhubung dengan Algentra.</h2>
              <p>Hubungi kami melalui kanal resmi atau lanjutkan ke portal klien.</p>
            </div>
          </div>
          <div className="contact-grid">
            <article className="contact-card">
              <h3>Narahubung</h3>
              {person ? <p className="contact-person">{person}</p> : null}
              <div className="contact-links">
                {whatsapp && (
                  <a
                    className="contact-link"
                    href={`https://wa.me/${whatsapp}`}
                    target="_blank"
                    rel="noopener noreferrer"
                  >
                    <svg
                      viewBox="0 0 24 24"
                      fill="none"
                      stroke="currentColor"
                      strokeWidth="1.7"
                      strokeLinecap="round"
                      strokeLinejoin="round"
                      aria-hidden="true"
                    >
                      <path d="M3 21l1.65-4.9A8.5 8.5 0 1 1 8 19.4z" />
                      <path d="M9 9.5c0 3 2.5 5.5 5.5 5.5l1-1.5-2-1-.8.8c-.9-.4-1.6-1.1-2-2l.8-.8-1-2z" />
                    </svg>
                    <span>
                      WhatsApp<small>+{whatsapp}</small>
                    </span>
                  </a>
                )}
                <div className="contact-email-cs">
                  {email && (
                    <a className="contact-link" href={`mailto:${email}`}>
                      <svg
                        viewBox="0 0 24 24"
                        fill="none"
                        stroke="currentColor"
                        strokeWidth="1.7"
                        strokeLinecap="round"
                        strokeLinejoin="round"
                        aria-hidden="true"
                      >
                        <rect x="3" y="5" width="18" height="14" rx="2"></rect>
                        <path d="m4 7 8 6 8-6"></path>
                      </svg>
                      <span>
                        Email<small>{email}</small>
                      </span>
                    </a>
                  )}
                  <div className={`cs-chat-widget${email ? "" : " cs-chat-widget--only"}`}>
                    <button
                      className="contact-link cs-chat-launcher cs-chat-launcher--contact"
                      id="cs-chat-open"
                      type="button"
                      onClick={() => window.dispatchEvent(new CustomEvent("algentra:cs-open"))}
                    >
                      <Mascot className="cs-contact-mascot" />
                      <span>
                        Chat CS<small>ALGENTRA siap membantu</small>
                      </span>
                    </button>
                  </div>
                </div>
                {!hasContactDirect && (
                  <>
                    {registrationOpen ? (
                      <a className="contact-link" href="/register">
                        Buat akun klien
                      </a>
                    ) : (
                      <a className="contact-link" href="/#registration-launch">
                        Pendaftaran ditutup
                      </a>
                    )}
                    <a className="contact-link" href="/login">
                      Masuk
                    </a>
                  </>
                )}
              </div>
            </article>
            <article className="contact-card">
              <h3>Kanal resmi</h3>
              <p>Ikuti informasi dan pembaruan dari Algentra Capital.</p>
              <div className="contact-links">
                {socials
                  .filter((social) => social.url)
                  .map((social) => (
                    <a
                      key={social.url}
                      className="contact-link"
                      href={social.url}
                      target="_blank"
                      rel="noopener noreferrer"
                    >
                      <SocialIcon icon={social.icon} />
                      <span>
                        {social.label}
                        <small>{social.display}</small>
                      </span>
                    </a>
                  ))}
                {socials.length === 0 &&
                  (registrationOpen ? (
                    <a className="contact-link" href="/register">
                      Portal klien
                    </a>
                  ) : (
                    <a className="contact-link" href="/#registration-launch">
                      Pendaftaran ditutup
                    </a>
                  ))}
              </div>
            </article>
          </div>
        </section>

        <section className="closing">
          <div>
            <h2>Copy trading otomatis dengan AI.</h2>
            <p>Analisis sinyal, eksekusi MetaTrader, dan penyalinan posisi dalam satu alur.</p>
          </div>
          {registrationOpen === true && (
            <a className="public-button" href="/register">
              Buat akun klien
            </a>
          )}
          {registrationOpen === false && (
            <a className="public-button secondary" href="/#registration-launch">
              Tunggu launching
            </a>
          )}
        </section>
        <p className="risk-copy">
          Trading memiliki risiko kerugian. Equity dan perubahan historis bukan jaminan hasil di masa depan. Data
          publik ini berasal dari akun utama MT5 yang menjalankan Telegram Signal EA.
          <img className="inline-brand-icon" src="/brand-kit/icons/risk-shield.svg" alt="" />
        </p>

      </main>
    </SiteFrame>
  );
}
