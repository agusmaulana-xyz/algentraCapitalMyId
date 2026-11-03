"use client";

import Script from "next/script";
import { useRouter } from "next/navigation";
import { FormEvent, useEffect, useRef, useState } from "react";
import { ApiError, apiRequest } from "../lib/api";
import {
  formatInteger,
  formatLots,
  formatMoney,
  formatNumber,
  formatPrice,
  numberTone,
  toneClass,
  type Tone,
} from "../lib/format";

type Stats = Record<string, number | string | boolean | string[] | null | undefined> & { trades_closed?: number };
type Mt5Live = {
  online: boolean;
  available: boolean;
  observed_at?: string | null;
  account?: {
    balance?: number | null;
    equity?: number | null;
    floating_profit?: number | null;
    currency?: string | null;
    trade_mode?: string | null;
  } | null;
  positions?: Array<{
    ticket: string;
    symbol: string;
    action: string;
    lots: number;
    entry_price: number;
    sl: number;
    tp: number;
  }> | null;
  market_quotes?: Array<{ symbol: string; bid: number; ask: number; time_msc: number }> | null;
};
type Signal = {
  created_at: string;
  group_name?: string | null;
  group_id?: string | null;
  raw_text: string;
  normalized_text?: string | null;
  confidence?: number | null;
  status: string;
  ticket?: string | null;
  profit?: number | null;
};
type GroupStat = {
  group_name: string;
  signals_total: number;
  trades_closed?: number;
  wins: number;
  losses: number;
  winrate: number;
  profit: number;
};
type LogEntry = { created_at: string; level: string; source: string; message: string };
type Trade = {
  opened_at: string;
  closed_at?: string | null;
  group_name?: string | null;
  ticket: string;
  symbol: string;
  action: string;
  lots: number;
  entry: number;
  profit?: number | null;
  result?: string | null;
};
type MasterDailyProfit = { date: string; profit: number; currency?: string | null };

const EMPTY = "—";
const TRADE_PAGE_SIZE = 10;

const displayTime = (value?: string | null) => {
  if (!value) return EMPTY;
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return EMPTY;
  return new Intl.DateTimeFormat("id-ID", {
    timeZone: "Asia/Jakarta",
    day: "2-digit",
    month: "short",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hourCycle: "h23",
  }).format(date);
};

const displayQuoteTime = (value: number | string | null | undefined) => {
  const milliseconds = Number(value);
  if (!Number.isFinite(milliseconds) || milliseconds <= 0) return EMPTY;
  const date = new Date(milliseconds);
  return Number.isNaN(date.getTime()) ? EMPTY : displayTime(date.toISOString());
};

const dayLabel = (date: string) =>
  new Date(`${date}T00:00:00+07:00`).toLocaleDateString("id-ID", { timeZone: "Asia/Jakarta", weekday: "short", day: "2-digit", month: "short" });

const shortTime = (value?: string | null) =>
  value
    ? new Date(value).toLocaleString("id-ID", { timeZone: "Asia/Jakarta", day: "2-digit", month: "short", hour: "2-digit", minute: "2-digit" })
    : EMPTY;

const truncateSignalText = (value: string, limit = 140) =>
  value.length > limit ? `${value.slice(0, limit).trimEnd()}......` : value;

const formatPercent = (value: number | string | null | undefined) => {
  const text = formatNumber(value, 0, 2);
  return text === EMPTY ? text : `${text}%`;
};

const STATUS_TONES: Record<string, string[]> = {
  pos: ["EXECUTED", "WIN", "ONLINE", "CONNECTED", "READY", "LIVE", "BUY", "VALID", "RUNNING", "APPROVED", "OK", "TERSAMBUNG"],
  neg: ["REJECTED", "FAILED", "LOSS", "ERROR", "OFFLINE", "DISCONNECTED", "SELL", "INVALID", "STOPPED", "CRITICAL", "KEY BELUM DIATUR", "CANCELLED"],
  warn: ["PENDING", "CLAIMED", "WARN", "WARNING", "PAUSED", "DATA BASI", "MENUNGGU TERMINAL", "BE", "CHECKING", "STARTING", "PAYMENT_SUBMITTED", "PENDING_PAYMENT"],
  info: ["INFO", "MARKET", "LIMIT", "STOP", "AUTO"],
  muted: ["IGNORED", "EXPIRED", "UNCHECKED"],
};

function statusTone(text: unknown): Tone {
  const key = String(text ?? "").trim().toUpperCase();
  for (const [tone, names] of Object.entries(STATUS_TONES)) if (names.includes(key)) return tone as Tone;
  if (key.startsWith("GAGAL")) return "neg";
  return "";
}

function queryWithFilters(base: string, values: Record<string, string>): string {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(values)) if (value) params.set(key, value);
  const qs = params.toString();
  return qs ? `${base}?${qs}` : base;
}

const num = (value: unknown) => (typeof value === "number" ? value : Number(value ?? 0));

export function DashboardPanel() {
  const router = useRouter();
  const [stats, setStats] = useState<Stats | null>(null);
  const [live, setLive] = useState<Mt5Live | null>(null);
  const [masterDailyProfit, setMasterDailyProfit] = useState<MasterDailyProfit[]>([]);
  const [mt5Error, setMt5Error] = useState("");
  const [signals, setSignals] = useState<Signal[]>([]);
  const [groups, setGroups] = useState<GroupStat[]>([]);
  const [logs, setLogs] = useState<LogEntry[]>([]);
  const [trades, setTrades] = useState<Trade[]>([]);
  const [profitTrades, setProfitTrades] = useState<Trade[]>([]);
  const [tradesError, setTradesError] = useState("");
  const [profitChartError, setProfitChartError] = useState("");
  const [tradePage, setTradePage] = useState(0);
  const [hasMoreTrades, setHasMoreTrades] = useState(false);
  const [registrationOpen, setRegistrationOpenState] = useState<boolean | null>(null);
  const [registrationMessage, setRegistrationMessage] = useState("");
  const [registrationBusy, setRegistrationBusy] = useState(false);
  const [settingsMessage, setSettingsMessage] = useState("");
  const [geminiMessage, setGeminiMessage] = useState("");
  const [eaApiKey, setEaApiKey] = useState<string | null>(null);
  const [eaApiKeyVisible, setEaApiKeyVisible] = useState(false);
  const [eaApiKeyMessage, setEaApiKeyMessage] = useState("");
  const [connection, setConnection] = useState("Menghubungkan realtime…");
  const [logLevel, setLogLevel] = useState("");
  const [logSearch, setLogSearch] = useState("");
  const [tradeFilters, setTradeFilters] = useState({
    date_from: "",
    date_to: "",
    group_id: "",
    symbol: "",
    result: "",
  });
  const [confidenceThreshold, setConfidenceThreshold] = useState("");
  const [maxMarketDeviation, setMaxMarketDeviation] = useState("");
  const [entryMode, setEntryMode] = useState("PARTIAL");
  const [symbolMapping, setSymbolMapping] = useState("");
  const [allowUpdates, setAllowUpdates] = useState(false);
  const [killSwitch, setKillSwitch] = useState(false);
  const [geminiModel, setGeminiModel] = useState("");
  const [chartReady, setChartReady] = useState(false);
  const tradePageRef = useRef(0);
  const groupChartRef = useRef<{ destroy: () => void } | null>(null);
  const profitChartRef = useRef<{ destroy: () => void } | null>(null);
  const groupCanvasRef = useRef<HTMLCanvasElement>(null);
  const profitCanvasRef = useRef<HTMLCanvasElement>(null);
  const latestSignalIdRef = useRef<number | null | undefined>(undefined);
  const hasGroupOutcomes = groups.some((group) => group.wins + group.losses > 0);
  const hasClosedProfitTrades = profitTrades.some((trade) => Boolean(trade.closed_at));

  async function loadStatsAndSignals() {
    const [freshStats, freshSignals] = await Promise.all([
      apiRequest<Stats>("dashboard/stats"),
      apiRequest<Signal[]>("dashboard/signals?limit=5"),
    ]);
    setStats(freshStats);
    setSignals(freshSignals);
  }

  async function loadMt5Live() {
    const fresh = await apiRequest<Mt5Live>("dashboard/mt5-live");
    setLive(fresh);
    setMt5Error("");
  }

  async function loadMasterDailyProfit() {
    const report = await apiRequest<{ daily_profit?: MasterDailyProfit[] }>("public/performance?include_equity_curve=false");
    setMasterDailyProfit(report.daily_profit ?? []);
  }

  async function loadGroups() {
    const rows = await apiRequest<GroupStat[]>("dashboard/groups/stats");
    setGroups([...rows].sort((a, b) => b.signals_total - a.signals_total).slice(0, 8));
  }

  async function loadLogs() {
    setLogs(
      await apiRequest<LogEntry[]>(
        queryWithFilters("dashboard/logs", { level: logLevel, search: logSearch, limit: "5" })
      )
    );
  }

  async function loadTrades(page: number) {
    const safePage = Math.max(0, page);
    const values: Record<string, string> = {
      date_from: tradeFilters.date_from ? `${tradeFilters.date_from}T00:00:00` : "",
      date_to: tradeFilters.date_to ? `${tradeFilters.date_to}T23:59:59` : "",
      group_id: tradeFilters.group_id,
      symbol: tradeFilters.symbol,
      result: tradeFilters.result,
      limit: String(TRADE_PAGE_SIZE + 1),
      offset: String(safePage * TRADE_PAGE_SIZE),
    };
    try {
      const rows = await apiRequest<Trade[]>(queryWithFilters("dashboard/trades", values));
      setHasMoreTrades(rows.length > TRADE_PAGE_SIZE);
      setTrades(rows.slice(0, TRADE_PAGE_SIZE));
      setTradePage(safePage);
      tradePageRef.current = safePage;
      setTradesError("");
    } catch (error) {
      setTradesError(error instanceof Error ? error.message : "Riwayat trade gagal dimuat.");
      throw error;
    }
  }

  async function loadProfitChart() {
    try {
      setProfitTrades(await apiRequest<Trade[]>("dashboard/trades?limit=30"));
      setProfitChartError("");
    } catch (error) {
      setProfitChartError(error instanceof Error ? error.message : "Grafik profit gagal dimuat.");
      throw error;
    }
  }

  async function loadSettings() {
    const data = await apiRequest<Record<string, unknown>>("settings");
    setRegistrationOpenState((data.registration_open as boolean | undefined) ?? true);
    setConfidenceThreshold(String((data.confidence_threshold as number | undefined) ?? 0.75));
    setMaxMarketDeviation(String((data.max_market_deviation_pct as number | undefined) ?? 5));
    setEntryMode(String((data.entry_mode as string | undefined) ?? "PARTIAL"));
    const mapping = data.symbol_mapping as Record<string, string> | undefined;
    setSymbolMapping(JSON.stringify(mapping ?? { GOLD: "XAUUSD", XAU: "XAUUSD", EMAS: "XAUUSD" }, null, 2));
    setAllowUpdates((data.allow_updates as boolean | undefined) ?? false);
    setKillSwitch((data.kill_switch as boolean | undefined) ?? false);
    const model = data.gemini_model;
    setGeminiModel(typeof model === "string" ? model : "");
  }

  // Satu endpoint yang gagal tidak lagi menjatuhkan seluruh dashboard:
  // panel lain tetap tampil, dan error dilempar hanya bila SEMUA permintaan gagal.
  async function refresh() {
    const results = await Promise.allSettled([
      loadStatsAndSignals(),
      loadMt5Live(),
      loadMasterDailyProfit(),
      loadGroups(),
      loadLogs(),
      loadTrades(tradePageRef.current),
      loadProfitChart(),
    ]);
    const failures = results.filter((item): item is PromiseRejectedResult => item.status === "rejected");
    if (failures.length === results.length) throw failures[0].reason;
  }

  useEffect(() => {
    let cancelled = false;
    async function boot() {
      try {
        await refresh();
        if (!cancelled) setConnection("Realtime tersambung melalui SSE.");
      } catch (reason) {
        if (reason instanceof ApiError && reason.status === 401) {
          router.replace("/admin/login");
          return;
        }
        if (!cancelled)
          setConnection(`Gagal memuat: ${reason instanceof Error ? reason.message : "Gagal memuat dashboard."}`);
      }
    }
    void boot();
    void loadSettings().catch((reason) => {
      if (!cancelled)
        setSettingsMessage(reason instanceof Error ? reason.message : "Gagal memuat settings.");
    });
    const stream = new EventSource("/api/dashboard/realtime/stream");
    // --- Realtime: SSE + refresh detail terpicu perubahan + watchdog fallback ---
    let lastSseAt = Date.now();
    let lastSignature = "";
    let detailBusy = false;
    let lastDetailAt = 0;
    const refreshDetails = () => {
      if (detailBusy || cancelled) return;
      detailBusy = true;
      lastDetailAt = Date.now();
      // MT5 live dipoll terpisah di bawah; jangan diambil dua kali.
      void Promise.allSettled([loadMasterDailyProfit(), loadGroups(), loadLogs(), loadTrades(tradePageRef.current), loadProfitChart()])
        .finally(() => { detailBusy = false; });
    };
    let detailDebounce: ReturnType<typeof setTimeout> | undefined;
    const scheduleDetails = () => {
      clearTimeout(detailDebounce);
      // Paling cepat 700 ms setelah perubahan, dan minimal 3 dtk antar-refresh agar backend tidak dibanjiri.
      detailDebounce = setTimeout(refreshDetails, Math.max(700, 3000 - (Date.now() - lastDetailAt)));
    };
    const stamp = () => new Date().toLocaleTimeString("en-GB");

    stream.addEventListener("dashboard", (event) => {
      try {
        const payload = JSON.parse((event as MessageEvent<string>).data) as {
          stats?: Stats;
          latest_signal_id?: number | null;
        };
        lastSseAt = Date.now();
        if (payload.stats) {
          setStats(payload.stats);
          // Stats berubah => kemungkinan ada order/profit baru: segarkan panel detail segera.
          const signature = JSON.stringify(payload.stats);
          if (lastSignature && signature !== lastSignature && !document.hidden) scheduleDetails();
          lastSignature = signature;
        }
        setConnection(`Realtime tersambung · ${stamp()}`);
        if (payload.latest_signal_id !== undefined) {
          const previousId = latestSignalIdRef.current;
          latestSignalIdRef.current = payload.latest_signal_id;
          if (previousId !== undefined && previousId !== payload.latest_signal_id) void loadStatsAndSignals().catch(() => {});
        }
      } catch {
        if (!cancelled) setConnection("Data realtime tidak dapat dibaca.");
      }
    });
    stream.onopen = () => { lastSseAt = Date.now(); if (!cancelled) setConnection("Realtime tersambung melalui SSE."); };
    stream.onerror = () => { if (!cancelled) setConnection("Menyambungkan ulang realtime…"); };

    // Polling berlapis dengan penjaga "sedang berjalan": bila backend lambat, permintaan
    // tidak menumpuk (dulu tiap 2 dtk menembak lagi walau yang sebelumnya belum selesai).
    const detailTimer = setInterval(() => {
      if (document.hidden) return;
      refreshDetails();
    }, 6000);

    // Watchdog SSE: bila tidak ada event >15 dtk (mis. ditahan proxy/tunnel), jangan diam saja:
    // beri tanda dan ambil stats+sinyal lewat REST setiap 5 dtk sampai SSE pulih.
    let watchdogBusy = false;
    const watchdog = setInterval(() => {
      if (document.hidden || cancelled || Date.now() - lastSseAt < 15000) return;
      setConnection("Realtime tertunda — memakai pembaruan berkala…");
      if (watchdogBusy) return;
      watchdogBusy = true;
      loadStatsAndSignals().catch(() => {}).finally(() => { watchdogBusy = false; });
    }, 5000);

    let mt5Busy = false;
    let mt5Failures = 0;
    let mt5NextAt = 0;
    const mt5Timer = setInterval(() => {
      if (document.hidden || cancelled || mt5Busy || Date.now() < mt5NextAt) return;
      mt5Busy = true;
      loadMt5Live()
        .then(() => { mt5Failures = 0; })
        .catch((reason) => {
          mt5Failures += 1;
          // Mundur bertahap (2 dtk -> maks. 20 dtk) saat error beruntun.
          mt5NextAt = Date.now() + Math.min(20000, 2000 * 2 ** Math.min(mt5Failures, 4));
          if (!cancelled) setMt5Error(reason instanceof Error ? reason.message : "Gagal memuat MT5.");
        })
        .finally(() => { mt5Busy = false; });
    }, 2000);

    // Kembali ke tab setelah lama tersembunyi: segarkan sekali, jangan tunggu timer.
    const onVisible = () => {
      if (document.hidden || cancelled) return;
      void refresh().catch(() => {});
    };
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      cancelled = true;
      stream.close();
      clearInterval(detailTimer);
      clearInterval(watchdog);
      clearTimeout(detailDebounce);
      clearInterval(mt5Timer);
      document.removeEventListener("visibilitychange", onVisible);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    if (!chartReady) return;
    if (groupChartRef.current) {
      groupChartRef.current.destroy();
      groupChartRef.current = null;
    }
    const canvas = groupCanvasRef.current;
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const ChartLib = (window as any).Chart;
    if (!canvas || !ChartLib || !hasGroupOutcomes) return;
    const labels = groups.map((g) => g.group_name);
    const data = {
      labels,
      datasets: [
        {
          label: "Win",
          data: groups.map((g) => g.wins),
          backgroundColor: "#1f9d72",
          barPercentage: 0.72,
          categoryPercentage: 0.65,
          maxBarThickness: 24,
        },
        {
          label: "Loss",
          data: groups.map((g) => g.losses),
          backgroundColor: "#d85858",
          barPercentage: 0.72,
          categoryPercentage: 0.65,
          maxBarThickness: 24,
        },
      ],
    };
    groupChartRef.current = new ChartLib(canvas, {
      type: "bar",
      data,
      options: {
        responsive: true,
        onResize(chart: { options: Record<string, unknown> }, size: { width: number }) {
          const mobile = size.width <= 600;
          const scales = chart.options.scales as Record<string, Record<string, Record<string, unknown>>>;
          scales.x.ticks.display = !mobile;
          scales.x.ticks.maxRotation = mobile ? 0 : 45;
          scales.x.ticks.minRotation = 0;
        },
        interaction: { mode: "index", intersect: false },
        plugins: { legend: { position: "top", labels: { boxWidth: 14, boxHeight: 8, padding: 12, font: { size: 11 } } } },
        scales: {
          x: {
            ticks: {
              display: window.innerWidth > 600,
              maxRotation: 45,
              minRotation: 0,
              font: { size: 10 },
            },
            grid: { display: false },
          },
          y: {
            beginAtZero: true,
            ticks: {
              precision: 0,
              padding: 4,
              font: { size: 10 },
              callback: (value: unknown) => formatInteger(value as number),
            },
          },
        },
      },
    });
    return () => {
      groupChartRef.current?.destroy();
      groupChartRef.current = null;
    };
  }, [groups, chartReady, hasGroupOutcomes]);

  useEffect(() => {
    if (!chartReady) return;
    if (profitChartRef.current) {
      profitChartRef.current.destroy();
      profitChartRef.current = null;
    }
    const canvas = profitCanvasRef.current;
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const ChartLib = (window as any).Chart;
    if (!canvas || !ChartLib || !hasClosedProfitTrades) return;
    const closed = profitTrades
      .filter((t) => t.closed_at)
      .sort((a, b) => new Date(a.closed_at as string).getTime() - new Date(b.closed_at as string).getTime());
    let total = 0;
    const points = closed.map((t) => ({ x: shortTime(t.closed_at), y: (total += Number(t.profit)) }));
    const data = {
      labels: points.map((p) => p.x),
      datasets: [
        {
          label: "Profit kumulatif",
          data: points.map((p) => p.y),
          borderColor: points.length && points[points.length - 1].y < 0 ? "#ff7b7b" : "#4ade80",
          tension: 0.2,
          borderWidth: 2,
          pointRadius: 2,
        },
      ],
    };
    profitChartRef.current = new ChartLib(canvas, {
      type: "line",
      data,
      options: {
        responsive: true,
        interaction: { mode: "index", intersect: false },
        scales: {
          x: { ticks: { maxTicksLimit: 6, maxRotation: 0, font: { size: 10 } }, grid: { display: false } },
          y: { beginAtZero: false, ticks: { font: { size: 10 }, callback: (value: unknown) => formatNumber(value as number, 0, 2) } },
        },
        plugins: {
          legend: { display: false },
          tooltip: {
            callbacks: {
              label: (ctx: { dataset: { label?: string }; parsed: { y: number } }) =>
                `${ctx.dataset.label}: ${formatNumber(ctx.parsed.y, 2, 2)}`,
            },
          },
        },
      },
    });
    return () => {
      profitChartRef.current?.destroy();
      profitChartRef.current = null;
    };
  }, [profitTrades, chartReady, hasClosedProfitTrades]);

  async function setRegistrationOpen(isOpen: boolean) {
    setRegistrationBusy(true);
    setRegistrationMessage("Menyimpan status pendaftaran...");
    try {
      await apiRequest("settings", {
        method: "PUT",
        body: JSON.stringify({ values: { registration_open: isOpen } }),
      });
      setRegistrationMessage(isOpen ? "Pendaftaran dibuka." : "Pendaftaran ditutup.");
      await loadSettings();
    } catch (reason) {
      setRegistrationMessage(`Gagal mengubah status: ${reason instanceof Error ? reason.message : "unknown"}`);
    } finally {
      setRegistrationBusy(false);
    }
  }

  async function submitSettings(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setSettingsMessage("");
    try {
      const values = {
        confidence_threshold: Number(confidenceThreshold),
        symbol_mapping: JSON.parse(symbolMapping || "{}"),
        max_market_deviation_pct: Number(maxMarketDeviation),
        entry_mode: entryMode,
        allow_updates: allowUpdates,
        kill_switch: killSwitch,
      };
      await apiRequest("settings", { method: "PUT", body: JSON.stringify({ values }) });
      setSettingsMessage("Settings tersimpan.");
      await loadSettings();
      void refresh().catch(() => undefined);
    } catch (reason) {
      setSettingsMessage(`Gagal: ${reason instanceof Error ? reason.message : "unknown"}`);
    }
  }

  async function submitGemini(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setGeminiMessage("");
    try {
      const result = await apiRequest<{ gemini_model?: string; gemini_configured?: boolean }>(
        "settings/gemini-config",
        { method: "PUT", body: JSON.stringify({ model: geminiModel.trim() }) }
      );
      setGeminiMessage(
        `Tersimpan. Model: ${result.gemini_model ?? EMPTY}; API key ${result.gemini_configured ? "terpasang" : "belum ada"}.`
      );
      await loadStatsAndSignals();
    } catch (reason) {
      setGeminiMessage(`Gagal: ${reason instanceof Error ? reason.message : "unknown"}`);
    }
  }

  async function revealEaApiKey() {
    setEaApiKeyMessage("");
    try {
      const result = await apiRequest<{ configured: boolean; api_key: string }>("settings/ea-api-key");
      setEaApiKey(result.configured ? result.api_key : "");
      setEaApiKeyVisible(true);
      if (!result.configured) setEaApiKeyMessage("EA_API_KEY belum dikonfigurasi pada layanan backend.");
    } catch (reason) {
      setEaApiKeyMessage(reason instanceof Error ? reason.message : "EA_API_KEY tidak dapat dimuat.");
    }
  }

  async function copyEaApiKey() {
    if (!eaApiKey) return;
    try {
      await navigator.clipboard.writeText(eaApiKey);
      setEaApiKeyMessage("EA_API_KEY disalin.");
    } catch {
      setEaApiKeyMessage("Gagal menyalin. Pilih dan salin key secara manual.");
    }
  }

  // ---- Turunan tampilan dari state (cermin applyStats di template asli) ----
  const signalInt = (key: string) =>
    stats && stats[key] != null ? formatInteger(stats[key] as number) : EMPTY;
  const wins = stats?.wins != null ? num(stats.wins) : null;
  const losses = stats?.losses != null ? num(stats.losses) : null;
  const winrate = stats?.winrate != null ? num(stats.winrate) : null;
  const closedTradeCount = stats?.trades_closed != null ? num(stats.trades_closed) : null;
  const hasWinLossResults = wins !== null && losses !== null && wins + losses > 0;
  const totalProfit = stats?.total_profit != null ? num(stats.total_profit) : null;
  const dailyLoss = stats?.daily_loss != null && Number.isFinite(num(stats.daily_loss)) ? num(stats.daily_loss) : null;

  const eaOnline = stats?.ea_online;
  const telegramConnected = stats?.telegram_connected;
  const geminiConfigured = stats?.gemini_configured;

  const breakdown =
    ["signals_executed", "signals_pending", "signals_processing", "signals_rejected", "signals_ignored", "signals_expired"].reduce(
      (sum, key) => sum + num(stats?.[key] ?? 0),
      0
    );
  const totalSignals = stats ? Number(stats.signals_total) : NaN;
  const mismatch = Boolean(
    stats &&
      (stats.signal_status_unknown ||
        (Number.isFinite(totalSignals) && breakdown !== totalSignals))
  );
  const mismatchValues = Array.isArray(stats?.signal_status_unknown_values)
    ? (stats?.signal_status_unknown_values as string[])
    : [];

  const liveChip: { text: string; tone: Tone } = mt5Error
    ? { text: "GAGAL", tone: "neg" }
    : !live
      ? { text: "MENGHUBUNGKAN", tone: "warn" }
      : live.online
        ? { text: "LIVE", tone: "pos" }
        : live.available
          ? { text: "DATA BASI", tone: "warn" }
          : { text: "MENUNGGU TERMINAL", tone: "warn" };
  const account = live?.account ?? null;
  const mt5Mode = account?.trade_mode ? account.trade_mode.toUpperCase() : "Mode akun";
  const mt5ModeTone: Tone =
    account?.trade_mode === "real" ? "warn" : account?.trade_mode === "demo" ? "info" : "";
  const mt5Equity = account ? formatMoney(account.equity, account.currency) : EMPTY;
  const mt5EquityTone = account ? numberTone(num(account.equity) - num(account.balance)) : "";
  const mt5Floating = account ? formatMoney(account.floating_profit, account.currency) : EMPTY;
  const mt5FloatingTone = account ? numberTone(account.floating_profit) : "";
  const mt5Updated = live?.observed_at
    ? `Snapshot terakhir ${displayTime(live.observed_at)}`
    : "Belum menerima snapshot dari MT5.";

  const connectionTone: Tone =
    connection.startsWith("Gagal") || connection.includes("tidak dapat")
      ? "neg"
      : connection.startsWith("Realtime tersambung")
        ? "pos"
        : "warn";
  const eaTitle =
    typeof stats?.ea_last_heartbeat === "string" && stats.ea_last_heartbeat
      ? `Heartbeat EA terakhir ${displayTime(stats.ea_last_heartbeat)}`
      : "Status heartbeat EA";
  const telegramTitle =
    typeof stats?.telegram_account === "string" && stats.telegram_account ? stats.telegram_account : "Koneksi akun sumber";
  const geminiTitle =
    typeof stats?.gemini_model === "string" && stats.gemini_model ? stats.gemini_model : "Model parser aktif";

  const logsExportHref = `/api/${queryWithFilters("dashboard/logs/export.csv", {
    level: logLevel,
    search: logSearch,
  })}`;
  const tradeExportValues: Record<string, string> = {
    date_from: tradeFilters.date_from ? `${tradeFilters.date_from}T00:00:00` : "",
    date_to: tradeFilters.date_to ? `${tradeFilters.date_to}T23:59:59` : "",
    group_id: tradeFilters.group_id,
    symbol: tradeFilters.symbol,
    result: tradeFilters.result,
  };
  const tradesExportHref = `/api/${queryWithFilters("dashboard/trades/export.csv", tradeExportValues)}`;

  return (
    <>
      <Script
        src="https://cdn.jsdelivr.net/npm/chart.js@4.4.1/dist/chart.umd.min.js"
        strategy="afterInteractive"
        onLoad={() => {
          // eslint-disable-next-line @typescript-eslint/no-explicit-any
          const ChartLib = (window as any).Chart;
          if (ChartLib) {
            const css = getComputedStyle(document.documentElement);
            ChartLib.defaults.color = css.getPropertyValue("--muted").trim() || "#9bb0c2";
            ChartLib.defaults.borderColor = css.getPropertyValue("--line").trim() || "#253c50";
            setChartReady(true);
          }
        }}
      />
      <main className="page-shell dashboard-page" id="main-content">
        <div className="page-heading">
          <div>
            <span className="eyebrow">Algentra Capital / Operations</span>
            <h1>Operations workspace</h1>
            <p className="page-description">
              Pantau sinyal, hasil eksekusi, performa trade, dan batas risiko dalam satu tampilan.
            </p>
          </div>
          <div className="heading-aside">
            <p id="connection" className={`status sc-dot connection-status ${toneClass(connectionTone)}`} role="status">
              {connection}
            </p>
          </div>
        </div>

        <section id="registration-controls" className="reg-card" aria-labelledby="registration-controls-heading">
          <div className="reg-info">
            <span className="eyebrow">Public site</span>
            <h2 id="registration-controls-heading">Pendaftaran client</h2>
            <p id="registration-state" className="section-help" role="status">
              {registrationOpen === null
                ? "Memuat status pendaftaran..."
                : registrationOpen
                  ? "Pendaftaran terbuka. Form daftar terlihat di situs publik."
                  : "Pendaftaran ditutup. Situs publik menampilkan hitung mundur launching."}
            </p>
            <p className="muted">
              Jika ditutup sebelum launching, pendaftaran otomatis terbuka pada 16 Januari 2027 pukul 09.00 WIB.
            </p>
          </div>
          <div className="reg-actions">
            <div className="toolbar">
              <button
                id="registration-open"
                type="button"
                disabled={registrationOpen === true || registrationBusy}
                aria-busy={registrationBusy}
                onClick={() => void setRegistrationOpen(true)}
              >
                Buka pendaftaran
              </button>
              <button
                id="registration-close"
                className="secondary"
                type="button"
                disabled={registrationOpen === false || registrationBusy}
                aria-busy={registrationBusy}
                onClick={() => void setRegistrationOpen(false)}
              >
                Tutup pendaftaran
              </button>
            </div>
            {registrationMessage && (
              <p id="registration-message" className="status" role="status">
                {registrationMessage}
              </p>
            )}
          </div>
        </section>

        <section className="summary-card" aria-label="Ringkasan operasional">
          <div className="sc-overview">
            <div className="sc-block sc-summary-status">
              <span id="ea-status" title={eaTitle} className={`sc-chip sc-dot ${toneClass(eaOnline === undefined ? "pos" : eaOnline ? "pos" : "neg")}`}>
                {eaOnline === undefined ? "EA Online" : eaOnline ? "EA Online" : "EA Offline"}
              </span>
              <span id="telegram-status" title={telegramTitle} className={`sc-chip sc-dot ${toneClass(telegramConnected === undefined ? "" : telegramConnected ? "pos" : "neg")}`}>
                {telegramConnected === undefined ? "Telegram" : telegramConnected ? "Telegram" : "Telegram Offline"}
              </span>
              <span id="gemini-status" title={geminiTitle} className={`sc-chip sc-dot ${toneClass(geminiConfigured === undefined ? "" : geminiConfigured ? "pos" : "warn")}`}>
                {geminiConfigured === undefined ? "Gemini" : geminiConfigured ? "Gemini" : "Gemini Belum Siap"}
              </span>
            </div>
            <div className="sc-block sc-performance">
              <div className="sc-profit">
                <div className="sc-profit-label">
                  <span>PROFIT TEREALISASI</span>
                </div>
                <b id="total-profit" className={toneClass(totalProfit !== null ? numberTone(totalProfit) : "")}>
                  {totalProfit !== null && (closedTradeCount === null || closedTradeCount > 0) ? formatNumber(totalProfit, 2, 2) : EMPTY}
                </b>
              </div>
              <div className="sc-metrics">
                <div>
                  <b id="winrate" className={toneClass(winrate !== null ? (winrate >= 50 ? "pos" : "warn") : "")}>
                    {winrate !== null && hasWinLossResults ? formatPercent(winrate) : EMPTY}
                  </b>
                  <span>Winrate</span>
                </div>
                <div>
                  <b id="wins-losses-inline">
                    {wins !== null && losses !== null && hasWinLossResults ? (
                      <>
                        <span>{formatInteger(wins)}</span>
                        {" / "}
                        <span>{formatInteger(losses)}</span>
                      </>
                    ) : (
                      <>— / —</>
                    )}
                  </b>
                  <span>Win / Lose</span>
                </div>
                <div>
                  <b id="daily-loss" className={toneClass(dailyLoss !== null && dailyLoss !== 0 ? "neg" : "")}>
                    {dailyLoss !== null ? formatNumber(Math.abs(dailyLoss), 2, 2) : EMPTY}
                  </b>
                  <span>Rugi hari ini</span>
                </div>
              </div>
            </div>
          </div>
          <div className="sc-block sc-signals">
            <div className="sc-label">
              Sinyal <em>Total <b id="signals-total">{signalInt("signals_total")}</b></em>
            </div>
            <table className="sc-table">
              <tbody>
                <tr>
                  <th scope="row">Tereksekusi</th>
                  <td><b id="signals-executed">{signalInt("signals_executed")}</b></td>
                  <th scope="row">Pending</th>
                  <td><b id="signals-pending">{signalInt("signals_pending")}</b></td>
                </tr>
                <tr>
                  <th scope="row">Ditolak</th>
                  <td><b id="signals-rejected">{signalInt("signals_rejected")}</b></td>
                  <th scope="row">Diabaikan</th>
                  <td><b id="signals-ignored">{signalInt("signals_ignored")}</b></td>
                </tr>
                <tr>
                  <th scope="row">Diproses</th>
                  <td><b id="signals-processing">{signalInt("signals_processing")}</b></td>
                  <th scope="row">Kedaluwarsa</th>
                  <td><b id="signals-expired">{signalInt("signals_expired")}</b></td>
                </tr>
              </tbody>
            </table>
            <details className="sc-help">
              <summary>Arti istilah</summary>
              <p>
                Eksekusi mencakup order yang diterima terminal MT5. Pending mencakup sinyal yang menunggu
                diklaim. Diproses mencakup sinyal yang sudah diklaim EA dan sedang menunggu hasil. Ditolak
                mencakup penolakan dan kegagalan. Diabaikan adalah pesan yang bukan sinyal atau tidak lolos
                aturan. Kedaluwarsa adalah sinyal yang tidak sempat masuk posisi.
              </p>
            </details>
          </div>
        </section>
        <p id="signal-status-warning" className="signal-status-warning" role="status" hidden={!mismatch}>
          {mismatch
            ? `Periksa data sinyal: ${mismatchValues.join(", ") || "jumlah kategori tidak sama dengan total"}.`
            : ""}
        </p>

        <section id="mt5-live-section" aria-live="polite">
          <div className="page-heading">
            <div>
              <h2>Data MT5 live</h2>
              <p id="mt5-live-updated" className="section-help">
                {live ? mt5Updated : "Menunggu snapshot terminal..."}
              </p>
              {mt5Error && (
                <p className="status tone-neg" role="alert">
                  Gagal memuat MT5: {mt5Error}
                </p>
              )}
            </div>
          </div>
          <div className="summary-card sc-mt5">
            <div className="sc-block">
              <div className="sc-status sc-summary-status">
                <span id="mt5-mode" className={`sc-chip sc-dot ${toneClass(mt5ModeTone)}`}>
                  {mt5Mode}
                </span>
                <span id="mt5-live-chip" className={`sc-chip sc-dot ${toneClass(liveChip.tone)}`} role="status">
                  {liveChip.text}
                </span>
              </div>
              <div className="sc-profit">
                <div className="sc-profit-label">
                  <span>EQUITY</span>
                </div>
                <b id="mt5-equity" className={toneClass(mt5EquityTone)}>
                  {mt5Equity}
                </b>
              </div>
              <div className="sc-metrics sc-metrics-three">
                <div>
                  <b id="mt5-balance">{account ? formatMoney(account.balance, account.currency) : EMPTY}</b>
                  <span>Balance</span>
                </div>
                <div>
                  <b id="mt5-floating" className={toneClass(mt5FloatingTone)}>
                    {mt5Floating}
                  </b>
                  <span>Floating P/L</span>
                </div>
                <div>
                  <b id="mt5-position-count">{formatInteger(live?.positions?.length ?? 0)}</b>
                  <span>Posisi terbuka</span>
                </div>
              </div>
            </div>
          </div>
          <div className="master-daily-profit">
            <h3>Profit harian akun master · WIB</h3>
            {masterDailyProfit.length ? (
              <div className="table-wrap day-table-wrap">
                <table className="day-table">
                  <thead>
                    <tr>
                      {masterDailyProfit.map((day) => (
                        <th key={day.date}>{dayLabel(day.date)}</th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    <tr>
                      {masterDailyProfit.map((day) => (
                        <td key={day.date} className={toneClass(day.profit >= 0 ? "pos" : "neg")}>
                          {formatMoney(day.profit, day.currency ?? account?.currency)}
                        </td>
                      ))}
                    </tr>
                  </tbody>
                </table>
              </div>
            ) : (
              <p className="muted">Belum ada profit tersimpan untuk tujuh hari terakhir.</p>
            )}
          </div>
          <div className="grid mt5-tables">
            <div>
              <h3>Posisi terminal</h3>
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>Ticket</th>
                      <th>Simbol</th>
                      <th>Arah</th>
                      <th className="num">Lot</th>
                      <th className="num">Entry</th>
                      <th className="num">SL</th>
                      <th className="num">TP</th>
                    </tr>
                  </thead>
                  <tbody id="mt5-positions-table">
                    {(live?.positions ?? []).map((position) => (
                      <tr key={position.ticket}>
                        <td>{position.ticket}</td>
                        <td>{position.symbol}</td>
                        <td>
                          <span className={`pill ${toneClass(statusTone(position.action))}`}>
                            {position.action}
                          </span>
                        </td>
                        <td className="num">{formatLots(position.lots)}</td>
                        <td className="num">{formatPrice(position.entry_price)}</td>
                        <td className="num">{Number(position.sl) > 0 ? formatPrice(position.sl) : EMPTY}</td>
                        <td className="num">{Number(position.tp) > 0 ? formatPrice(position.tp) : EMPTY}</td>
                      </tr>
                    ))}
                    {live && !(live.positions ?? []).length && (
                      <tr>
                        <td className="empty-cell" colSpan={7}>Tidak ada posisi terbuka.</td>
                      </tr>
                    )}
                  </tbody>
                </table>
              </div>
            </div>
            <div>
              <h3>Harga pasar</h3>
              <div className="table-wrap">
                <table>
                  <thead>
                    <tr>
                      <th>Simbol</th>
                      <th className="num">Bid</th>
                      <th className="num">Ask</th>
                      <th>Update</th>
                    </tr>
                  </thead>
                  <tbody id="mt5-quotes-table">
                    {(live?.market_quotes ?? []).map((quote) => (
                      <tr key={quote.symbol}>
                        <td>{quote.symbol}</td>
                        <td className="num">{formatPrice(quote.bid)}</td>
                        <td className="num">{formatPrice(quote.ask)}</td>
                        <td>{displayQuoteTime(quote.time_msc)}</td>
                      </tr>
                    ))}
                    {live && !(live.market_quotes ?? []).length && (
                      <tr>
                        <td className="empty-cell" colSpan={4}>Belum ada harga pasar.</td>
                      </tr>
                    )}
                  </tbody>
                </table>
              </div>
            </div>
          </div>
        </section>

        <section>
          <h2>Signal terbaru</h2>
          <p className="section-help">
            Lima pesan terbaru beserta hasil parsing, confidence, status pemrosesan, dan ticket order.
          </p>
          <div className="table-wrap">
            <table className="signal-table">
              <thead>
                <tr>
                  <th>Waktu</th>
                  <th>Grup</th>
                  <th>Pesan</th>
                  <th>Parsing</th>
                  <th>Confidence</th>
                  <th>Status</th>
                  <th>Ticket</th>
                  <th>Profit</th>
                </tr>
              </thead>
              <tbody id="signals-table">
                {signals.map((signal, index) => {
                  const confidenceTone =
                    signal.confidence === null || signal.confidence === undefined
                      ? ""
                      : Number(signal.confidence) >= 0.75
                        ? "pos"
                        : "warn";
                  return (
                    <tr key={`${signal.created_at}-${index}`}>
                      <td>{displayTime(signal.created_at)}</td>
                      <td>{signal.group_name || signal.group_id || EMPTY}</td>
                      <td className="signal-text-cell" title={signal.raw_text}>
                        <span>{truncateSignalText(signal.raw_text)}</span>
                      </td>
                      <td className="signal-text-cell" title={signal.normalized_text || EMPTY}>
                        <span>{truncateSignalText(signal.normalized_text || EMPTY)}</span>
                      </td>
                      <td className={`num ${toneClass(confidenceTone)}`}>
                        {signal.confidence === null || signal.confidence === undefined
                          ? EMPTY
                          : formatNumber(signal.confidence, 2, 2)}
                      </td>
                      <td>
                        <span className={`pill ${toneClass(statusTone(signal.status))}`}>{signal.status}</span>
                      </td>
                      <td>{signal.ticket || EMPTY}</td>
                      <td className={`num ${toneClass(numberTone(signal.profit))}`}>
                        {signal.profit === null || signal.profit === undefined
                          ? EMPTY
                          : formatNumber(signal.profit, 2, 2)}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        </section>

        <div className="grid">
          <section>
            <h2>Win dan loss per grup</h2>
            <p className="section-help">Bandingkan akurasi dan profit dari setiap grup sumber.</p>
            {hasGroupOutcomes ? (
              <canvas id="group-chart" height={180} ref={groupCanvasRef}></canvas>
            ) : (
              <p className="dashboard-chart-empty">Grafik win/loss akan terisi setelah ada trade yang ditutup.</p>
            )}
            <div className="table-wrap">
              <table>
                <thead>
                  <tr>
                    <th>Grup</th>
                    <th>Signal</th>
                    <th>Win</th>
                    <th>Loss</th>
                    <th>Winrate</th>
                    <th>Profit</th>
                  </tr>
                </thead>
                <tbody id="groups-table">
                  {groups.map((group) => (
                    <tr key={group.group_name}>
                      <td>{group.group_name}</td>
                      <td className="num">{formatInteger(group.signals_total)}</td>
                      <td className="num">{group.wins + group.losses > 0 ? formatInteger(group.wins) : EMPTY}</td>
                      <td className="num">{group.wins + group.losses > 0 ? formatInteger(group.losses) : EMPTY}</td>
                      <td className="num">{group.wins + group.losses > 0 ? formatPercent(group.winrate) : EMPTY}</td>
                      <td className="num">{(group.trades_closed ?? 0) > 0 ? formatNumber(group.profit, 2, 2) : EMPTY}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </section>
          <section>
            <h2>Profit kumulatif</h2>
            <p className="section-help">Perubahan profit terealisasi dari waktu ke waktu.</p>
            {hasClosedProfitTrades ? (
              <canvas id="profit-chart" height={220} ref={profitCanvasRef}></canvas>
            ) : (
              <p className={`dashboard-chart-empty${profitChartError ? " tone-neg" : ""}`}>{profitChartError ? `Grafik gagal dimuat: ${profitChartError}` : "Grafik akan terisi setelah ada trade yang ditutup."}</p>
            )}
          </section>
        </div>

        <section>
          <h2>Log sistem</h2>
          <p className="section-help">Lima log terbaru. Gunakan filter untuk mempersempit level atau kata kunci.</p>
          <div className="toolbar">
            <select id="log-level" title="Batasi log ke level tertentu" value={logLevel} onChange={(event) => setLogLevel(event.target.value)}>
              <option value="">Semua level</option>
              <option value="INFO">INFO</option>
              <option value="WARN">WARN</option>
              <option value="ERROR">ERROR</option>
            </select>
            <input
              id="log-search"
              placeholder="Cari log"
              title="Cari kata di sumber atau pesan log"
              value={logSearch}
              onChange={(event) => setLogSearch(event.target.value)}
            />
            <button id="filter-logs" type="button" onClick={() => void loadLogs().catch(() => undefined)}>
              Filter log
            </button>
            <a id="logs-export" href={logsExportHref}>
              Export CSV
            </a>
          </div>
          <div className="table-wrap">
            <table className="log-table">
              <thead>
                <tr>
                  <th>Waktu</th>
                  <th>Level</th>
                  <th>Sumber</th>
                  <th>Pesan</th>
                </tr>
              </thead>
              <tbody id="logs-table">
                {logs.map((log, index) => (
                  <tr key={`${log.created_at}-${index}`}>
                    <td>{displayTime(log.created_at)}</td>
                    <td>
                      <span className={`pill ${toneClass(statusTone(log.level))}`}>{log.level}</span>
                    </td>
                    <td>{log.source}</td>
                    <td>{log.message}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>

        <section>
          <h2>Riwayat trade</h2>
          <p className="section-help">
            Filter transaksi yang telah dilaporkan EA; tabel menampilkan 10 per halaman (satu query per halaman).
            Unduh CSV untuk data lengkap.
          </p>
          <div className="toolbar trade-filters">
            <input
              type="date"
              id="date-from"
              title="Tanggal awal trade"
              value={tradeFilters.date_from}
              onChange={(event) => setTradeFilters({ ...tradeFilters, date_from: event.target.value })}
            />
            <input
              type="date"
              id="date-to"
              title="Tanggal akhir trade"
              value={tradeFilters.date_to}
              onChange={(event) => setTradeFilters({ ...tradeFilters, date_to: event.target.value })}
            />
            <input
              id="trade-group"
              placeholder="Group ID"
              title="Filter berdasarkan ID grup Telegram"
              value={tradeFilters.group_id}
              onChange={(event) => setTradeFilters({ ...tradeFilters, group_id: event.target.value })}
            />
            <input
              id="trade-symbol"
              placeholder="Symbol"
              title="Filter berdasarkan simbol"
              value={tradeFilters.symbol}
              onChange={(event) => setTradeFilters({ ...tradeFilters, symbol: event.target.value })}
            />
            <select
              id="trade-result"
              title="Filter hasil trade"
              value={tradeFilters.result}
              onChange={(event) => setTradeFilters({ ...tradeFilters, result: event.target.value })}
            >
              <option value="">Semua hasil</option>
              <option value="WIN">WIN</option>
              <option value="LOSS">LOSS</option>
              <option value="BE">BE</option>
            </select>
            <button id="filter-trades" type="button" onClick={() => void loadTrades(0).catch(() => undefined)}>
              Filter trade
            </button>
            <a id="trades-export" href={tradesExportHref}>
              Export CSV
            </a>
          </div>
          {tradesError && <p className="status tone-neg" role="alert">Riwayat trade gagal dimuat: {tradesError}</p>}
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Waktu</th>
                  <th>Grup</th>
                  <th>Ticket</th>
                  <th>Symbol</th>
                  <th>Arah</th>
                  <th>Lot</th>
                  <th>Entry</th>
                  <th>Profit</th>
                  <th>Hasil</th>
                </tr>
              </thead>
              <tbody id="trades-table">
                {trades.map((trade) => (
                  <tr key={trade.ticket}>
                    <td>{displayTime(trade.opened_at)}</td>
                    <td>{trade.group_name || EMPTY}</td>
                    <td>{trade.ticket}</td>
                    <td>{trade.symbol}</td>
                    <td>
                      <span className={`pill ${toneClass(statusTone(trade.action))}`}>{trade.action}</span>
                    </td>
                    <td className="num">{formatLots(trade.lots)}</td>
                    <td className="num">{formatPrice(trade.entry)}</td>
                    <td className={`num ${toneClass(numberTone(trade.profit))}`}>
                      {trade.profit === null || trade.profit === undefined
                        ? EMPTY
                        : formatNumber(trade.profit, 2, 2)}
                    </td>
                    <td>
                      <span className={`pill ${toneClass(statusTone(trade.result))}`}>
                        {trade.result || EMPTY}
                      </span>
                    </td>
                  </tr>
                ))}
                {!trades.length && (
                  <tr><td className="empty-cell" colSpan={9}>{tradesError ? "Data trade belum dapat ditampilkan." : "Belum ada trade yang cocok dengan filter."}</td></tr>
                )}
              </tbody>
            </table>
          </div>
          <div className="pager">
            <button
              id="trades-prev"
              className="secondary"
              type="button"
              disabled={tradePage === 0}
              onClick={() => void loadTrades(tradePage - 1).catch(() => undefined)}
            >
              Sebelumnya
            </button>
            <span id="trades-page">Halaman {tradePage + 1}</span>
            <button
              id="trades-next"
              className="secondary"
              type="button"
              disabled={!hasMoreTrades}
              onClick={() => void loadTrades(tradePage + 1).catch(() => undefined)}
            >
              Berikutnya
            </button>
          </div>
        </section>

        <section id="settings">
          <span className="eyebrow">Risk &amp; configuration</span>
          <h2>Settings</h2>
          <p className="section-help">
            Pengaturan validasi sinyal dan guard backend. Gunakan kill switch untuk menjeda distribusi sinyal.
          </p>
          <div className="settings-block" id="ea-api-key-setting">
            <h3>EA_API_KEY</h3>
            <p className="section-help">Dipakai EA master untuk mengirim sinyal ke server.</p>
            <div className="settings-action">
              <button type="button" onClick={() => {
                if (eaApiKeyVisible) {
                  setEaApiKeyVisible(false);
                  setEaApiKey(null);
                  setEaApiKeyMessage("");
                }
                else void revealEaApiKey();
              }}>
                {eaApiKeyVisible ? "Sembunyikan" : "Tampilkan key"}
              </button>
              {eaApiKeyVisible && eaApiKey && <button type="button" className="secondary" onClick={() => void copyEaApiKey()}>Salin</button>}
            </div>
            {eaApiKeyVisible && eaApiKey && <pre className="ea-api-key-value">{eaApiKey}</pre>}
            {eaApiKeyMessage && <p className="status" role="status">{eaApiKeyMessage}</p>}
          </div>
          <form id="gemini-form" className="settings-block" onSubmit={(event) => void submitGemini(event)}>
            <h3>Parser Gemini</h3>
            <div className="settings-row">
              <label>
                Model Gemini
                <input
                  id="gemini-model-input"
                  placeholder="gemini-3.8-flash"
                  value={geminiModel}
                  onChange={(event) => setGeminiModel(event.target.value)}
                />
              </label>
              <div className="settings-action">
                <button type="submit">Simpan model</button>
              </div>
            </div>
            {geminiMessage && (
              <p id="gemini-message" className="status" role="status">
                {geminiMessage}
              </p>
            )}
          </form>
          <form id="settings-form" className="settings-block" onSubmit={(event) => void submitSettings(event)}>
            <h3>Aturan sinyal</h3>
            <div className="settings-grid">
              <label>
                Confidence threshold
                <small className="field-help">Minimum keyakinan parser agar sinyal diteruskan.</small>
                <input
                  type="number"
                  id="confidence-threshold"
                  min="0"
                  max="1"
                  step="0.01"
                  value={confidenceThreshold}
                  onChange={(event) => setConfidenceThreshold(event.target.value)}
                />
              </label>
              <label>
                Deviasi harga maksimal (%)
                <small className="field-help">Tolak market entry yang terlalu jauh dari harga sinyal.</small>
                <input
                  type="number"
                  id="max-market-deviation"
                  min="0.1"
                  step="0.1"
                  value={maxMarketDeviation}
                  onChange={(event) => setMaxMarketDeviation(event.target.value)}
                />
              </label>
              <label>
                Mode entry
                <small className="field-help">Single: satu order. Partial: dibagi sesuai TP atau sisi zona entry.</small>
                <select id="entry-mode" value={entryMode} onChange={(event) => setEntryMode(event.target.value)}>
                  <option value="PARTIAL">Partial entry</option>
                  <option value="SINGLE">Single entry</option>
                </select>
              </label>
              <label className="span-2">
                Symbol mapping (JSON)
                <small className="field-help">
                  Alias simbol sinyal ke nama instrumen broker (contoh: {"{"}"EURUSD":"EURUSD.pro"{"}"}).
                  Harga dan ukuran lot mengikuti spesifikasi simbol broker; sinyal belum masuk posisi
                  kedaluwarsa setelah satu jam.
                </small>
                <textarea
                  id="symbol-mapping"
                  rows={5}
                  value={symbolMapping}
                  onChange={(event) => setSymbolMapping(event.target.value)}
                />
              </label>
            </div>
            <div className="toggle-list">
              <label className="toggle">
                <input
                  type="checkbox"
                  id="allow-updates"
                  checked={allowUpdates}
                  onChange={(event) => setAllowUpdates(event.target.checked)}
                />
                <span>
                  <b>Izinkan update signal</b>
                  <small className="field-help">Aktifkan pengelolaan posisi seperti close atau geser SL.</small>
                </span>
              </label>
              <label className="toggle">
                <input
                  type="checkbox"
                  id="kill-switch"
                  checked={killSwitch}
                  onChange={(event) => setKillSwitch(event.target.checked)}
                />
                <span>
                  <b>Pause trading (kill switch)</b>
                  <small className="field-help">Hentikan distribusi sinyal ke EA sampai dimatikan.</small>
                </span>
              </label>
            </div>
            <div className="settings-footer">
              <button type="submit">Simpan settings</button>
              <span id="settings-message" className="status">
                {settingsMessage}
              </span>
            </div>
          </form>
        </section>
      </main>
    </>
  );
}
