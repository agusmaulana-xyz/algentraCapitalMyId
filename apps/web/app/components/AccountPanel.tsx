"use client";

import { Fragment, useCallback, useEffect, useRef, useState } from "react";
import { apiRequest, apiUpload } from "../lib/api";
import {
  formatInteger,
  formatMoney,
  numberTone,
  toneClass,
  type Tone,
} from "../lib/format";
import { AccountPerformanceDetails } from "./AccountPerformanceDetails";

type Plan = {
  name: string;
  vcpu: string;
  ram: string;
  disk: string;
  price_idr: number;
};

type Pricing = {
  daily_rate_idr: number;
  minimum_rental_days: number;
  management_fee_idr: number;
  copy_trading_fee_idr: number;
  vps_options: VpsOption[];
};

type VpsOption = {
  code: string;
  name: string;
  vcpu: string;
  ram: string;
  disk: string;
  price_idr: number;
};

type ManagedCopy = {
  configured: boolean;
  active: boolean;
  status: string;
  status_message?: string | null;
  login_status?: string | null;
  login_message?: string | null;
  last_seen_at?: string | null;
  follower_symbol?: string | null;
  mode_lot?: string | null;
  ratio_lot?: number | null;
  lot_tetap?: number | null;
  max_lot_per_order?: number | null;
  max_lot_total?: number | null;
  max_open_positions?: number | null;
};

type Account = {
  id: number;
  label: string;
  server: string;
  login: string;
  plan: string;
  execution_mode: string;
  rental_expires_at?: string | null;
  ea_mode?: string | null;
  custom_ea_name?: string | null;
  active: boolean;
  last_seen_at?: string | null;
  connection_status: string;
  balance?: number | null;
  equity?: number | null;
  floating_profit?: number | null;
  currency?: string | null;
  trade_mode?: string | null;
  state_updated_at?: string | null;
  managed_copy?: ManagedCopy | null;
};

type BalanceGroup = {
  trade_mode: string;
  currency: string;
  balance: number;
  equity: number;
  floating_profit: number;
  online_accounts: number;
  reported_accounts: number;
  updated_at?: string | null;
};

type PerformanceGroup = {
  trade_mode: string;
  currency: string;
  realized_pnl: number;
  closed_trades: number;
  wins: number;
  losses: number;
  win_rate: number;
};

type Performance = {
  balance_groups: BalanceGroup[];
  performance_groups: PerformanceGroup[];
  updated_at?: string | null;
};

type CopierStatus = {
  configured: boolean;
  available: boolean;
  status?: string | null;
  last_seen_at?: string | null;
};

type TokenBox = { title: string; token: string; accountId: number } | null;
type BrokerServerOption = { name: string; company: string };
const brokerSearchFallback = "Pencarian server sedang tidak tersedia. Gunakan Input Manual dan masukkan nama server persis dari broker.";

async function searchBrokerServers(query: string, signal: AbortSignal): Promise<BrokerServerOption[]> {
  let response: Response;
  try {
    response = await fetch(`/api/mt5/servers?q=${encodeURIComponent(query)}`, { signal, headers: { Accept: "application/json" } });
  } catch (error) {
    if (signal.aborted) throw error;
    throw new Error(brokerSearchFallback);
  }
  if (!/json/i.test(response.headers.get("content-type") || "")) throw new Error(brokerSearchFallback);
  let payload: { servers?: BrokerServerOption[]; detail?: string; error?: string };
  try {
    payload = await response.json();
  } catch {
    throw new Error(brokerSearchFallback);
  }
  if (!response.ok) {
    const detail = payload.detail || payload.error;
    throw new Error(detail ? `${detail} Gunakan Input Manual jika nama server sudah diketahui.` : brokerSearchFallback);
  }
  return Array.isArray(payload.servers) ? payload.servers : [];
}

type MetricItem = [string, number | null | undefined, (string | null | undefined)?, Tone?];

const ZERO_PLAN: Plan = { name: "—", vcpu: "—", ram: "—", disk: "—", price_idr: 0 };

const wib = (value?: string | null) =>
  value
    ? new Date(value).toLocaleString("id-ID", { timeZone: "Asia/Jakarta" })
    : "";

const modeLabel = (mode?: string | null) =>
  ({ real: "Live", demo: "Demo", contest: "Kompetisi" } as Record<string, string>)[
    mode ?? ""
  ] ?? mode ?? "MT5";

const CONNECTION_LABELS: Record<string, string> = {
  ready: "EA Copy Trading dan akun utama terhubung.",
  waiting_for_source:
    "EA Copy Trading terhubung; menunggu pembaruan dari akun utama.",
  offline:
    "EA Copy Trading belum terhubung. Periksa token, ServerURL, dan Allow WebRequest.",
  trading_disabled: "Koneksi aktif, tetapi izin trading MT5 belum aktif.",
  not_real_account: "Mode akun MT5 belum dikenali.",
  disabled: "Akun Copy Trading ini dinonaktifkan.",
};

function Metrics({ items }: { items: MetricItem[] }) {
  return (
    <>
      {items.map(([label, value, currency, tone], index) => (
        <Fragment key={index}>
          {index > 0 ? " · " : null}
          {`${label} `}
          <span className={toneClass(tone ?? "")}>
            {formatMoney(value, currency)}
          </span>
        </Fragment>
      ))}
    </>
  );
}

function EaAccountCard({
  account,
  plan,
  onMessage,
  onReload,
  onToken,
  clearTokenForAccount,
}: {
  account: Account;
  plan: Plan;
  onMessage: (message: string) => void;
  onReload: () => void;
  onToken: (title: string, token: string, accountId: number) => void;
  clearTokenForAccount: (accountId: number) => void;
}) {
  const connectionStatus =
    CONNECTION_LABELS[account.connection_status] !== undefined
      ? account.connection_status
      : "offline";
  const detail = `Akun Copy Trading · ${account.login} · ${account.server} · ${
    plan.name
  } (${plan.vcpu}, RAM ${plan.ram}, Disk ${plan.disk}) · Rp ${formatInteger(
    plan.price_idr
  )}`;
  const stateText = `${account.active ? "Aktif" : "Nonaktif"} · Terakhir terhubung: ${
    account.last_seen_at ? wib(account.last_seen_at) : "belum ada koneksi"
  }`;
  const connectionText =
    connectionStatus === "ready"
      ? `Siap menyalin pada akun ${modeLabel(account.trade_mode)}: ${
          CONNECTION_LABELS.ready
        }`
      : CONNECTION_LABELS[connectionStatus];

  const handleEdit = async () => {
    const label = prompt("Nama akun", account.label);
    if (label === null) return;
    const server = prompt("Broker server", account.server);
    if (server === null) return;
    const login = prompt("Nomor login MT5", account.login);
    if (login === null) return;
    try {
      await apiRequest(`mt5/accounts/${account.id}`, {
        method: "PUT",
        body: JSON.stringify({ label, server, login }),
      });
      onReload();
    } catch (error) {
      onMessage(error instanceof Error ? error.message : "Permintaan tidak berhasil.");
    }
  };

  const handleToggleActive = async () => {
    try {
      await apiRequest(`mt5/accounts/${account.id}/active`, {
        method: "PUT",
        body: JSON.stringify({ active: !account.active }),
      });
      onReload();
    } catch (error) {
      onMessage(error instanceof Error ? error.message : "Permintaan tidak berhasil.");
    }
  };

  const handleRotateToken = async () => {
    try {
      const result = await apiRequest<{ token: string }>(
        `mt5/accounts/${account.id}/rotate-token`,
        { method: "POST" }
      );
      onToken(`Token baru: ${account.label}`, result.token, account.id);
      onReload();
    } catch (error) {
      onMessage(error instanceof Error ? error.message : "Permintaan tidak berhasil.");
    }
  };

  const handleRemove = async () => {
    if (
      !confirm(
        `Hapus akun Copy Trading ${account.label} (${account.login})? Token terminal ini tidak akan bisa digunakan lagi.`
      )
    )
      return;
    onMessage("");
    try {
      await apiRequest(`mt5/accounts/${account.id}`, { method: "DELETE" });
      clearTokenForAccount(account.id);
      onMessage(`Akun ${account.label} berhasil dihapus.`);
      onReload();
    } catch (error) {
      onMessage(error instanceof Error ? error.message : "Permintaan tidak berhasil.");
    }
  };

  return (
    <article className="card">
      <h3>{account.label}</h3>
      <p>{detail}</p>
      <p className="muted">{stateText}</p>
      <div
        className={`account-state account-state--${connectionStatus}`}
        role="status"
      >
        <span className="account-state__dot" aria-hidden="true">
          {connectionStatus === "ready" ? "✓" : ""}
        </span>
        <span>{connectionText}</span>
      </div>
      <div className="toolbar">
        <button type="button" className="secondary" onClick={handleEdit}>
          Edit akun
        </button>
        <button
          type="button"
          className={account.active ? "secondary" : ""}
          onClick={handleToggleActive}
        >
          {account.active ? "Nonaktifkan" : "Aktifkan"}
        </button>
        <button type="button" className="secondary" onClick={handleRotateToken}>
          Rotasi token
        </button>
        <button
          type="button"
          className="danger icon-button"
          title={`Hapus akun ${account.label}`}
          aria-label={`Hapus akun ${account.label}`}
          onClick={handleRemove}
        >
          <svg
            viewBox="0 0 24 24"
            width="18"
            height="18"
            aria-hidden="true"
            fill="none"
            stroke="currentColor"
            strokeWidth="2"
            strokeLinecap="round"
            strokeLinejoin="round"
          >
            <path d="M3 6h18"></path>
            <path d="M8 6V4h8v2"></path>
            <path d="M19 6l-1 14H6L5 6"></path>
            <path d="M10 11v6M14 11v6"></path>
          </svg>
        </button>
      </div>
    </article>
  );
}
function ManagedAccountCard({
  account,
  plan,
  managedWorkerReady,
  onDirty,
  onMessage,
  onReload,
}: {
  account: Account;
  plan: Plan;
  managedWorkerReady: boolean;
  onDirty: () => void;
  onMessage: (message: string) => void;
  onReload: () => void;
}) {
  const [customEAFile, setCustomEAFile] = useState<File | null>(null);
  const [eaBusy, setEABusy] = useState(false);
  const [eaMessage, setEAMessage] = useState("");
  const [liveConfirmOpen, setLiveConfirmOpen] = useState(false);
  const [copyToggleBusy, setCopyToggleBusy] = useState(false);
  const liveConfirmDialogRef = useRef<HTMLDialogElement>(null);
  const [editingIdentity, setEditingIdentity] = useState(false);
  const [identityBusy, setIdentityBusy] = useState(false);
  const [identityServerQuery, setIdentityServerQuery] = useState(account.server);
  const [identityServerOptions, setIdentityServerOptions] = useState<BrokerServerOption[]>([]);
  const [identitySelectedServer, setIdentitySelectedServer] = useState(account.server);
  const [identityManualServer, setIdentityManualServer] = useState(false);
  const [identityServerText, setIdentityServerText] = useState(account.server);
  const [identityServerSearchMessage, setIdentityServerSearchMessage] = useState("");
  const [identityServerSearchBusy, setIdentityServerSearchBusy] = useState(false);
  const profile: ManagedCopy = account.managed_copy ?? {
    configured: false,
    active: false,
    status: "SETUP_REQUIRED",
  };
  const detail = `Dikelola server · ${account.login} · ${account.server} · ${plan.name}`;
  const stateText = `${
    profile.active ? "Copy aktif" : "Copy dijeda"
  } · Worker terakhir terhubung: ${
    profile.last_seen_at ? wib(profile.last_seen_at) : "belum terhubung"
  }`;

  useEffect(() => {
    const query = identityServerQuery.trim();
    if (!editingIdentity || query.length < 3) {
      setIdentityServerOptions([]);
      setIdentityServerSearchBusy(false);
      return;
    }
    const controller = new AbortController();
    const timer = setTimeout(async () => {
      setIdentityServerSearchBusy(true);
      setIdentityServerSearchMessage("");
      try {
        const results = await searchBrokerServers(query, controller.signal);
        const options = results.some((option) => option.name.toLowerCase() === account.server.toLowerCase())
          ? results
          : [{ name: account.server, company: "Server saat ini" }, ...results];
        setIdentityServerOptions(options);
        setIdentityServerSearchMessage(results.length ? `${results.length} server ditemukan.` : "Server tidak ditemukan. Gunakan nama persis dari broker atau coba kata kunci lain.");
      } catch (error) {
        if (controller.signal.aborted) return;
        setIdentityServerOptions([{ name: account.server, company: "Server saat ini" }]);
        setIdentityServerSearchMessage(error instanceof Error ? error.message : "Pencarian server gagal.");
      } finally {
        if (!controller.signal.aborted) setIdentityServerSearchBusy(false);
      }
    }, 350);
    return () => { clearTimeout(timer); controller.abort(); };
  }, [account.server, editingIdentity, identityServerQuery]);

  useEffect(() => {
    const dialog = liveConfirmDialogRef.current;
    if (liveConfirmOpen && dialog && !dialog.open) dialog.showModal();
    else if (!liveConfirmOpen && dialog?.open) dialog.close();
  }, [liveConfirmOpen]);

  const loginMessages: Record<string, string> = {
    UNCHECKED: "Login belum diperiksa. Simpan kredensial untuk memeriksa.",
    CHECKING: "Memeriksa login broker…",
    VALID: profile.login_message || "Login broker berhasil.",
    ERROR:
      profile.login_message ||
      "Login belum berhasil. Periksa data akun.",
  };
  const loginMessage =
    (profile.login_status && loginMessages[profile.login_status]) ||
    loginMessages.UNCHECKED;
  const loginClass = `account-login-check${
    profile.login_status === "VALID"
      ? " account-login-check--valid"
      : profile.login_status === "ERROR"
        ? " account-login-check--error"
        : " account-login-check--pending"
  }`;

  const handleIdentityUpdate = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const nextServer = String(form.get("server") || "").trim();
    if (!nextServer) {
      onMessage("Pilih server dari hasil pencarian MT5 atau masukkan nama server yang diberikan broker.");
      return;
    }
    const nextLogin = String(form.get("account_login") || "").trim();
    const identityChanged = nextServer.toLowerCase() !== account.server.toLowerCase() || nextLogin !== account.login;
    setIdentityBusy(true);
    onMessage("");
    try {
      await apiRequest(`mt5/accounts/${account.id}`, {
        method: "PUT",
        body: JSON.stringify({
          label: form.get("account_label"),
          server: nextServer,
          login: nextLogin,
        }),
      });
      setEditingIdentity(false);
      onMessage(identityChanged
        ? "Akun diperbarui. Masukkan password trading akun ini, lalu simpan pengaturan agar login diperiksa ulang."
        : "Nama akun berhasil diperbarui.");
      onReload();
    } catch (error) {
      onMessage(error instanceof Error ? error.message : "Detail akun tidak dapat diperbarui.");
    } finally {
      setIdentityBusy(false);
    }
  };

  const stateLabels: Record<string, string> = {
    SETUP_REQUIRED: "Isi password broker dan batas copy.",
    PAUSED: "Copy dijeda.",
    STARTING: "Menunggu worker mengambil job…",
    RUNNING: "Worker tersambung.",
    OFFLINE: "Worker belum mengirim heartbeat.",
    ERROR:
      profile.status_message ||
      "Worker melaporkan kendala.",
    STOPPED: "Worker berhenti.",
  };
  const connectionModifier =
    profile.status === "RUNNING"
      ? "ready"
      : profile.status === "PAUSED" || profile.status === "SETUP_REQUIRED"
        ? "disabled"
        : "offline";
  const connectionText =
    stateLabels[profile.status] || stateLabels.SETUP_REQUIRED;


  const handleSubmit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    onMessage("");
    const data = Object.fromEntries(
      new FormData(event.currentTarget).entries()
    ) as Record<string, FormDataEntryValue>;
    for (const key of [
      "ratio_lot",
      "lot_tetap",
      "max_lot_per_order",
      "max_lot_total",
      "max_open_positions",
    ]) {
      data[key] = Number(data[key]) as unknown as FormDataEntryValue;
    }
    try {
      await apiRequest(`mt5/accounts/${account.id}/managed-copy`, {
        method: "PUT",
        body: JSON.stringify(data),
      });
      onMessage(
        "Pengaturan tersimpan. Login broker sedang diperiksa otomatis; copy trading masih dijeda."
      );
      onReload();
    } catch (error) {
      onMessage(error instanceof Error ? error.message : "Permintaan tidak berhasil.");
    }
  };

  const handleServerEA = async () => {
    setEABusy(true);
    setEAMessage("");
    try {
      await apiRequest(`mt5/accounts/${account.id}/ea-choice`, {
        method: "PUT",
        body: JSON.stringify({ mode: "SERVER" }),
      });
      setEAMessage("EA server akan dijalankan pada terminal sewa.");
      onReload();
    } catch (error) {
      setEAMessage(error instanceof Error ? error.message : "Pilihan EA gagal disimpan.");
    } finally {
      setEABusy(false);
    }
  };

  const handleCustomEAUpload = async () => {
    if (!customEAFile) {
      setEAMessage("Pilih file EA .mq5 atau .ex5 terlebih dahulu.");
      return;
    }
    setEABusy(true);
    setEAMessage("");
    try {
      const form = new FormData();
      form.set("ea", customEAFile);
      await apiUpload(`mt5/accounts/${account.id}/custom-ea`, form);
      setEAMessage("EA diunggah. Worker akan memasang dan menjalankannya pada terminal sewa.");
      setCustomEAFile(null);
      onReload();
    } catch (error) {
      setEAMessage(error instanceof Error ? error.message : "Upload EA gagal.");
    } finally {
      setEABusy(false);
    }
  };

  const setCopyActive = async (active: boolean) => {
    setCopyToggleBusy(true);
    onMessage("");
    try {
      await apiRequest(`mt5/accounts/${account.id}/managed-copy/active`, {
        method: "PUT",
        body: JSON.stringify({ active, acknowledge_live_risk: active }),
      });
      setLiveConfirmOpen(false);
      onReload();
    } catch (error) {
      onMessage(error instanceof Error ? error.message : "Permintaan tidak berhasil.");
    } finally {
      setCopyToggleBusy(false);
    }
  };

  const handleToggleCopy = () => {
    if (!profile.active) {
      setLiveConfirmOpen(true);
      return;
    }
    void setCopyActive(false);
  };

  const handleRemove = async () => {
    if (
      !confirm(
        `Hapus akun ${account.label} (${account.login})? Jeda copy trading terlebih dahulu.`
      )
    )
      return;
    try {
      await apiRequest(`mt5/accounts/${account.id}`, { method: "DELETE" });
      onReload();
    } catch (error) {
      onMessage(error instanceof Error ? error.message : "Permintaan tidak berhasil.");
    }
  };

  const toggleDisabled =
    !profile.configured ||
    (!profile.active &&
      (!managedWorkerReady || profile.login_status !== "VALID"));

  return (
    <article className="card">
      <h3>{account.label}</h3>
      <p>{detail}</p>
      <p className="muted">{stateText}</p>
      {account.rental_expires_at ? <p className="muted">Sewa aktif sampai {wib(account.rental_expires_at)}</p> : null}
      <details className="account-identity-editor">
        <summary>Ganti akun login MT5</summary>
        <p className="field-help">Jeda copy sebelum mengganti akun.</p>
        {editingIdentity ? (
          <form className="form-grid" onSubmit={handleIdentityUpdate}>
            <label>Nama akun<input name="account_label" maxLength={80} defaultValue={account.label} required /></label>
            <div className="broker-server-field">
              <label htmlFor={`identity-server-query-${account.id}`}>Cari broker atau server MT5</label>
              <input
                id={`identity-server-query-${account.id}`}
                type="search"
                value={identityServerQuery}
                maxLength={96}
                autoComplete="off"
                placeholder="Contoh: HFM atau HFMarketsGlobal-Live11"
                onChange={(event) => {
                  setIdentityServerQuery(event.currentTarget.value);
                  setIdentitySelectedServer("");
                }}
              />
              {identityManualServer ? (
                <>
                  <label htmlFor={`identity-server-manual-${account.id}`}>Nama server persis dari broker</label>
                  <input id={`identity-server-manual-${account.id}`} name="server" value={identityServerText} maxLength={128} required onChange={(event) => setIdentityServerText(event.currentTarget.value)} />
                </>
              ) : (
                <>
                  <label htmlFor={`identity-server-select-${account.id}`}>Server akun MT5</label>
                  <select id={`identity-server-select-${account.id}`} name="server" value={identitySelectedServer} required onChange={(event) => setIdentitySelectedServer(event.currentTarget.value)}>
                    <option value="">Pilih server dari daftar MT5</option>
                    {identityServerOptions.map((option) => <option key={`${option.company}:${option.name}`} value={option.name}>{option.name} — {option.company}</option>)}
                  </select>
                </>
              )}
              <span className="field-help" role="status">{identityServerSearchBusy ? "Mencari direktori server MT5…" : identityServerSearchMessage || "Ketik minimal 3 karakter nama broker atau server."}</span>
              <button type="button" className="secondary server-manual-toggle" onClick={() => { setIdentityManualServer((manual) => !manual); setIdentityServerText(account.server); setIdentitySelectedServer(""); }}>
                {identityManualServer ? "Pilih dari Daftar Server" : "Input Manual"}
              </button>
            </div>
            <label>Nomor login MT5<input name="account_login" inputMode="numeric" maxLength={32} pattern="[0-9]+" defaultValue={account.login} required /></label>
            <div className="toolbar">
              <button type="submit" disabled={identityBusy || profile.active}>{identityBusy ? "Menyimpan…" : "Simpan akun"}</button>
              <button type="button" className="secondary" onClick={() => setEditingIdentity(false)}>Batal</button>
            </div>
          </form>
        ) : (
          <button type="button" className="secondary" disabled={profile.active} onClick={() => { setIdentityServerQuery(account.server); setIdentitySelectedServer(account.server); setIdentityServerText(account.server); setEditingIdentity(true); }}>
            {profile.active ? "Jeda copy sebelum mengganti akun" : "Ubah server atau nomor login"}
          </button>
        )}
      </details>
      <section className="account-ea-choice" aria-label="Pilihan Expert Advisor server">
        <h4>EA server</h4>
        <div className="toolbar">
          <button type="button" className={account.ea_mode === "CUSTOM" ? "secondary" : ""} disabled={eaBusy} onClick={() => void handleServerEA()}>
            Pakai EA server
          </button>
          <span>{account.ea_mode === "CUSTOM" ? `EA sendiri: ${account.custom_ea_name || "terpasang"}` : "EA server dipilih"}</span>
        </div>
        <label>
          EA sendiri (.mq5 / .ex5, maks 10 MB)
          <input type="file" accept=".mq5,.ex5,application/octet-stream,text/plain" onChange={(event) => setCustomEAFile(event.currentTarget.files?.[0] || null)} />
        </label>
        <button type="button" className="secondary" disabled={eaBusy || !customEAFile} onClick={() => void handleCustomEAUpload()}>
          {eaBusy ? "Memproses…" : "Unggah EA sendiri"}
        </button>
        {eaMessage ? <p className="status" role="status">{eaMessage}</p> : null}
      </section>
      <p className={loginClass} role="status" aria-live="polite">
        {loginMessage}
      </p>
      <div
        className={`account-state account-state--${connectionModifier}`}
        role="status"
      >
        <span>{connectionText}</span>
      </div>
      <form
        className="form-grid managed-form"
        onSubmit={handleSubmit}
        onInput={onDirty}
        onChange={onDirty}
      >
        <label>
          Password akun broker
          <input
            name="broker_password"
            type="password"
            autoComplete="off"
            maxLength={128}
            required
            placeholder={
              profile.configured
                ? "Isi untuk mengganti"
                : "Password trading"
            }
          />
        </label>
        <label>
          Simbol XAUUSD (opsional)
          <input
            name="follower_symbol"
            type="text"
            maxLength={64}
            pattern="[A-Za-z0-9._-]+"
            placeholder="Otomatis"
            defaultValue={profile.follower_symbol && profile.follower_symbol.toUpperCase() !== "XAUUSD" ? profile.follower_symbol : ""}
          />
        </label>
        <label>
          Metode ukuran lot
          <select name="mode_lot" defaultValue={profile.mode_lot || "rasio"}>
            <option value="rasio">Rasio terhadap lot sumber</option>
            <option value="tetap">Lot tetap</option>
          </select>
        </label>
        <label>
          Pengali rasio
          <input
            name="ratio_lot"
            type="number"
            min="0.01"
            max="5"
            step="0.01"
            required
            defaultValue={profile.ratio_lot ?? 1}
          />
        </label>
        <label>
          Lot tetap
          <input
            name="lot_tetap"
            type="number"
            min="0.01"
            max="5"
            step="0.01"
            required
            defaultValue={profile.lot_tetap ?? 0.01}
          />
        </label>
        <label>
          Batas lot per order
          <input
            name="max_lot_per_order"
            type="number"
            min="0.01"
            max="5"
            step="0.01"
            required
            defaultValue={profile.max_lot_per_order ?? 0.05}
          />
        </label>
        <label>
          Batas total lot terbuka
          <input
            name="max_lot_total"
            type="number"
            min="0.01"
            max="10"
            step="0.01"
            required
            defaultValue={profile.max_lot_total ?? 0.1}
          />
        </label>
        <label>
          Maksimum posisi terbuka
          <input
            name="max_open_positions"
            type="number"
            min="1"
            max="50"
            step="1"
            required
            defaultValue={profile.max_open_positions ?? 3}
          />
        </label>
        <div className="form-submit">
          <button type="submit">
            {profile.configured
              ? "Simpan pengaturan"
              : "Simpan kredensial dan pengaturan"}
          </button>
        </div>
      </form>
      {account.ea_mode !== "CUSTOM" ? <div className="toolbar">
        <button
          type="button"
          className={profile.active ? "secondary" : ""}
          disabled={toggleDisabled || copyToggleBusy}
          onClick={handleToggleCopy}
        >
          {copyToggleBusy ? "Menyimpan…" : profile.active ? "Jeda copy trading" : "Mulai copy trading (live)"}
        </button>
        <button type="button" className="danger" onClick={handleRemove}>
          Hapus akun
        </button>
      </div> : <div className="toolbar">
        <span className="muted">Memakai EA sendiri.</span>
        <button type="button" className="danger" onClick={handleRemove}>Hapus akun</button>
      </div>}
      <dialog
        ref={liveConfirmDialogRef}
        className="live-copy-confirm-dialog"
        aria-labelledby={`live-copy-confirm-title-${account.id}`}
        aria-describedby={`live-copy-confirm-description-${account.id}`}
        onCancel={(event) => {
          event.preventDefault();
          if (!copyToggleBusy) setLiveConfirmOpen(false);
        }}
        onClick={(event) => {
          if (event.target === event.currentTarget && !copyToggleBusy) setLiveConfirmOpen(false);
        }}
      >
        <div className="live-copy-confirm-dialog__icon" aria-hidden="true">!</div>
        <h2 id={`live-copy-confirm-title-${account.id}`}>Konfirmasi copy trading live</h2>
        <p id={`live-copy-confirm-description-${account.id}`}>
          Copy trading akan mengirim order live ke akun broker <strong>{account.label}</strong> ({account.login}).
          Pastikan akun demo sudah diuji, login dan server benar, serta batas lot dan posisi sudah sesuai.
          Apakah Anda yakin ingin melanjutkan?
        </p>
        <div className="live-copy-confirm-dialog__actions">
          <button
            type="button"
            className="secondary"
            autoFocus
            disabled={copyToggleBusy}
            onClick={() => setLiveConfirmOpen(false)}
          >
            Batal
          </button>
          <button
            type="button"
            className="live-copy-confirm-dialog__submit"
            disabled={copyToggleBusy}
            onClick={() => void setCopyActive(true)}
          >
            {copyToggleBusy ? "Mengaktifkan…" : "Ya, lanjutkan"}
          </button>
        </div>
      </dialog>
    </article>
  );
}
export function AccountPanel() {
  const [plans, setPlans] = useState<Record<string, Plan>>({});
  const [accounts, setAccounts] = useState<Account[]>([]);
  const [performance, setPerformance] = useState<Performance | null>(null);
  const [copier, setCopier] = useState<CopierStatus | null>(null);
  const [executionMode, setExecutionMode] = useState("EA");
  const [pricing, setPricing] = useState<Pricing | null>(null);
  const [selectedVps, setSelectedVps] = useState<string | null>(null);
  const [accountMessage, setAccountMessage] = useState("");
  const [connectingAccount, setConnectingAccount] = useState(false);
  const [serverSearch, setServerSearch] = useState("");
  const [serverOptions, setServerOptions] = useState<BrokerServerOption[]>([]);
  const [selectedBrokerServer, setSelectedBrokerServer] = useState("");
  const [manualServerEntry, setManualServerEntry] = useState(false);
  const [manualServer, setManualServer] = useState("");
  const [serverSearchBusy, setServerSearchBusy] = useState(false);
  const [serverSearchMessage, setServerSearchMessage] = useState("");
  const [showAccountForm, setShowAccountForm] = useState(false);
  const [rootUrl, setRootUrl] = useState("");
  const [rentalDays, setRentalDays] = useState(10);

  const totalIdr = rentalDays * 2000;
  const [tokenBox, setTokenBox] = useState<TokenBox>(null);
  const [tokenCopied, setTokenCopied] = useState(false);
  const managedFormDirty = useRef(false);
  const modeTouched = useRef(false);

  const managedWorkerReady = copier?.available === true;
  const serviceStatus = !copier
    ? ""
    : copier.available
      ? "Worker managed copy online. Akun managed dapat didaftarkan dan dijalankan."
      : copier.configured
        ? "Worker managed copy offline. Layanan managed belum dapat dipesan atau dimulai."
        : "Layanan managed belum dikonfigurasi di server. Anda tetap dapat memilih Pasang EA sendiri.";

  const loadAccounts = useCallback(async () => {
    try {
      const [accountList, perfData, copierStatus] = await Promise.all([
        apiRequest<Account[]>("mt5/accounts"),
        apiRequest<Performance>("mt5/performance"),
        apiRequest<CopierStatus>("mt5/copier/status"),
      ]);
      setAccounts(accountList);
      setPerformance(perfData);
      setCopier(copierStatus);
      if (!modeTouched.current) {
        setExecutionMode(copierStatus.available === true ? "MANAGED" : "EA");
      }
    } catch (error) {
      if (error instanceof Error && /401|login diperlukan|unauthorized/i.test(error.message)) {
        setAccounts([]);
        setPerformance(null);
        setCopier(null);
        setTokenBox(null);
      }
      setAccountMessage(
        error instanceof Error ? error.message : "Permintaan tidak berhasil."
      );
    }
  }, []);

  useEffect(() => {
    setRootUrl(window.location.origin);
    apiRequest<Record<string, Plan>>("mt5/plans")
      .then(setPlans)
      .catch(() => {});
    apiRequest<Pricing>("mt5/pricing")
      .then(setPricing)
      .catch(() => {});
    const stream = new EventSource("/api/mt5/stream");
    const applySnapshot = (event: MessageEvent<string>) => {
      try {
        const payload = JSON.parse(event.data) as { accounts?: Account[]; performance?: Performance; copier?: CopierStatus };
        if (payload.accounts) setAccounts(payload.accounts);
        if (payload.performance) setPerformance(payload.performance);
        if (payload.copier) {
          setCopier(payload.copier);
          if (!modeTouched.current) setExecutionMode(payload.copier.available ? "MANAGED" : "EA");
        }
      } catch { /* Ignore malformed stream payloads; EventSource reconnects automatically. */ }
    };
    const applyAccounts = (event: MessageEvent<string>) => {
      try {
        const payload = JSON.parse(event.data) as { accounts?: Account[] };
        if (payload.accounts) setAccounts(payload.accounts);
      } catch { /* Ignore malformed account events. */ }
    };
    const applyMeta = (event: MessageEvent<string>) => {
      try {
        const payload = JSON.parse(event.data) as { performance?: Performance; copier?: CopierStatus };
        if (payload.performance) setPerformance(payload.performance);
        if (payload.copier) {
          setCopier(payload.copier);
          if (!modeTouched.current) setExecutionMode(payload.copier.available ? "MANAGED" : "EA");
        }
      } catch { /* Ignore malformed metadata events. */ }
    };
    stream.addEventListener("mt5-snapshot", applySnapshot as EventListener);
    stream.addEventListener("mt5-accounts", applyAccounts as EventListener);
    stream.addEventListener("mt5-meta", applyMeta as EventListener);
    return () => {
      stream.close();
    };
  }, []);

  useEffect(() => {
    const query = serverSearch.trim();
    if (query.length < 3) return;

    const controller = new AbortController();
    const timer = setTimeout(async () => {
      setServerSearchBusy(true);
      setServerSearchMessage("");
      try {
        const options = await searchBrokerServers(query, controller.signal);
        setServerOptions(options);
        setServerSearchMessage(
          options.length ? `${options.length} server ditemukan.` : "Server tidak ditemukan. Coba nama broker atau server yang lebih singkat."
        );
      } catch (error) {
        if (controller.signal.aborted) return;
        setServerOptions([]);
        setServerSearchMessage(
          error instanceof Error ? error.message : "Pencarian server gagal."
        );
      } finally {
        if (!controller.signal.aborted) setServerSearchBusy(false);
      }
    }, 350);

    return () => {
      clearTimeout(timer);
      controller.abort();
    };
  }, [serverSearch]);

  const handleConnectSubmit = async (
    event: React.FormEvent<HTMLFormElement>
  ) => {
    event.preventDefault();
    const form = event.currentTarget;
    if (!(manualServerEntry ? manualServer.trim() : selectedBrokerServer)) {
      setAccountMessage("Pilih server akun dari hasil pencarian atau masukkan nama server yang diberikan broker.");
      return;
    }
    setConnectingAccount(true);
    setAccountMessage("");
    const formData = new FormData(form);
    const payload = {
      execution_mode: "MANAGED",
      plan: "RENTAL",
      rental_days: rentalDays,
      label: formData.get("label"),
      server: formData.get("server"),
      login: formData.get("login"),
    };
    try {
      const order = await apiRequest<{ id: string | number }>(
        "payments/orders",
        { method: "POST", body: JSON.stringify(payload) }
      );
      form.reset();
      setServerSearch("");
      setServerOptions([]);
      setSelectedBrokerServer("");
      setManualServer("");
      setManualServerEntry(false);
      setExecutionMode("EA");
      setSelectedVps(null);
      modeTouched.current = false;
      window.location.href = `/transactions?order=${encodeURIComponent(
        String(order.id)
      )}`;
    } catch (error) {
      setAccountMessage(
        error instanceof Error ? error.message : "Permintaan tidak berhasil."
      );
    } finally {
      setConnectingAccount(false);
    }
  };

  const handleToken = (title: string, token: string, accountId: number) => {
    setTokenCopied(false);
    setTokenBox({ title, token, accountId });
  };

  const clearTokenForAccount = (accountId: number) => {
    setTokenBox((prev) =>
      prev && prev.accountId === accountId ? null : prev
    );
  };

  const handleCopyToken = async () => {
    if (!tokenBox) return;
    try {
      await navigator.clipboard.writeText(tokenBox.token);
      setTokenCopied(true);
    } catch {
      setTokenCopied(false);
    }
  };

  const handleLoginInput = (
    event: React.FormEvent<HTMLInputElement>
  ) => {
    const input = event.currentTarget;
    input.setCustomValidity(
      input.validity.patternMismatch
        ? "Nomor akun MT5 hanya boleh berisi angka, tanpa spasi."
        : ""
    );
  };

  const handleLoginInvalid = (
    event: React.FormEvent<HTMLInputElement>
  ) => {
    event.currentTarget.setCustomValidity(
      "Nomor akun MT5 hanya boleh berisi angka, tanpa spasi."
    );
  };

  const performanceGroups = performance?.performance_groups ?? [];
  const hasEaAccount = accounts.some(
    (account) => account.execution_mode !== "MANAGED"
  );
  const hasExistingPackage = accounts.length > 0;

  return (
    <main className="page-shell dashboard-page account-page" id="main-content" tabIndex={-1}>
      <div className="page-heading">
        <div>
          <span className="eyebrow">PORTAL KLIEN / COPY TRADING</span>
          <h1>Sewa Server MT5</h1>
          <p className="page-description">
            Daftarkan akun follower dan atur layanan salin posisi dari portal ini.
          </p>
        </div>
        <p className="heading-aside">
          Password broker tidak ditampilkan kembali setelah disimpan.
        </p>
      </div>

      <section>
        <h2>Mulai sewa server</h2>
        {hasExistingPackage && !showAccountForm ? (
          <>
            <p className="section-help">Sewa server Anda sudah terdaftar. Lihat transaksi atau pesan masa sewa untuk akun tambahan.</p>
            <div className="account-primary-actions">
              <a className="account-action-button account-action-button--secondary" href="/transactions">Riwayat transaksi dan nota</a>
              <button className="account-action-button" type="button" onClick={() => setShowAccountForm(true)}>Hubungkan akun lain</button>
            </div>
          </>
        ) : (
        <>
        <p className="section-help">
          Sewa server MT5 dibayar di muka. Setelah pembayaran terverifikasi, pilih EA dari server atau unggah EA Anda untuk dipasang otomatis.
        </p>
        <form id="account-form" onSubmit={handleConnectSubmit}>
          <fieldset className="plan-picker plan-picker--compact">
            <legend>Sewa server</legend>
            <label>
              Lama sewa (hari, minimal 10)
              <input type="number" min={10} max={10000} step={1} required value={rentalDays}
                onChange={(event) => setRentalDays(Math.max(10, Number(event.currentTarget.value) || 10))} />
            </label>
            <div className="price-list">
              <div className="price-row">
                <span className="price-row__body">
                  <span className="price-row__name">Sewa server harian</span>
                  <span className="price-row__desc">Rp 2.000 per hari · minimal 10 hari</span>
                </span>
                <span className="price-row__price">Rp {formatInteger(totalIdr)}</span>
              </div>
            </div>
          </fieldset>
          <fieldset hidden className="plan-picker plan-picker--compact">
            <legend>Cara menjalankan copy trading</legend>
            <div
              className="price-list"
              role="radiogroup"
              aria-label="Cara menjalankan copy trading"
            >
              <label
                className="price-row"
                htmlFor="execution-managed"
                {...(!managedWorkerReady
                  ? {
                      "aria-disabled": "true",
                      title: "Layanan dikelola server sedang offline",
                    }
                  : {})}
              >
                <input
                  id="execution-managed"
                  type="radio"
                  name="execution_mode"
                  value="MANAGED"
                  required
                  disabled={!managedWorkerReady}
                  checked={executionMode === "MANAGED"}
                  onChange={() => {
                    modeTouched.current = true;
                    setExecutionMode("MANAGED");
                  }}
                />
                <span className="price-row__body">
                  <span className="price-row__name">
                    Dikelola server
                    {!managedWorkerReady && (
                      <span className="plan-option__availability">
                        Belum tersedia
                      </span>
                    )}
                  </span>
                  <span className="price-row__desc">
                    Pengaturan melalui website. MT5 berjalan di server
                    layanan, bukan di komputer Anda.
                  </span>
                </span>
              </label>
              <label className="price-row" htmlFor="execution-ea">
                <input
                  id="execution-ea"
                  type="radio"
                  name="execution_mode"
                  value="EA"
                  required
                  checked={executionMode === "EA"}
                  onChange={() => {
                    modeTouched.current = true;
                    setExecutionMode("EA");
                  }}
                />
                <span className="price-row__body">
                  <span className="price-row__name">Pasang EA sendiri</span>
                  <span className="price-row__desc">
                    Anda menjalankan terminal MT5 dan EA Copy Trading di
                    perangkat/VPS sendiri.
                  </span>
                </span>
              </label>
            </div>
          </fieldset>
          <p hidden
            id="managed-copy-service-status"
            className="status"
            role="status"
            aria-live="polite"
          >
            {serviceStatus}
          </p>
          <fieldset hidden className="plan-picker plan-picker--compact">
            <legend>Biaya layanan</legend>
            {pricing ? (
              executionMode === "MANAGED" ? (
                <div className="price-list">
                  <div className="price-row">
                    <span className="price-row__body">
                      <span className="price-row__name">
                        Biaya pengelolaan akun
                      </span>
                      <span className="price-row__desc">
                        MT5 berjalan di server layanan.
                      </span>
                    </span>
                    <span className="price-row__price">
                      Rp {formatInteger(pricing.management_fee_idr)}
                    </span>
                  </div>
                </div>
              ) : (
                <div className="account-price-stack">
                  <div className="price-list">
                    <div className="price-row">
                      <span className="price-row__body">
                        <span className="price-row__name">
                          Biaya copy trading
                        </span>
                        <span className="price-row__desc">
                          Setengah dari biaya pengelolaan server.
                        </span>
                      </span>
                      <span className="price-row__price">
                        Rp {formatInteger(pricing.copy_trading_fee_idr)}
                      </span>
                    </div>
                  </div>
                  {pricing.vps_options.length > 0 && (
                    <>
                      <p className="mini-title">
                        Tambah VPS{" "}
                        <span className="mini-title__opt">(opsional)</span>
                      </p>
                      <div
                        className="price-list"
                        role="radiogroup"
                        aria-label="Pilihan VPS"
                      >
                        <label className="price-row">
                          <input
                            type="radio"
                            name="vps_option"
                            checked={selectedVps === null}
                            onChange={() => setSelectedVps(null)}
                          />
                          <span className="price-row__body">
                            <span className="price-row__name">Tanpa VPS</span>
                            <span className="price-row__desc">
                              Jalankan di perangkat atau VPS Anda sendiri.
                            </span>
                          </span>
                          <span className="price-row__price">Rp 0</span>
                        </label>
                        {pricing.vps_options.map((vps) => (
                          <label key={vps.code} className="price-row">
                            <input
                              type="radio"
                              name="vps_option"
                              checked={selectedVps === vps.code}
                              onChange={() => setSelectedVps(vps.code)}
                            />
                            <span className="price-row__body">
                              <span className="price-row__name">
                                {vps.name}
                              </span>
                              <span className="price-row__desc">
                                {vps.vcpu} · RAM {vps.ram} · Disk {vps.disk}
                              </span>
                            </span>
                            <span className="price-row__price">
                              Rp {formatInteger(vps.price_idr)}
                            </span>
                          </label>
                        ))}
                      </div>
                    </>
                  )}
                  <div className="price-list price-list--summary">
                    <div className="price-list__total">
                      <span>Total bayar</span>
                      <strong>Rp {formatInteger(totalIdr)}</strong>
                    </div>
                  </div>
                </div>
              )
            ) : (
              <p className="status">Memuat harga layanan…</p>
            )}
            <p className="field-help">Pembayaran manual, diperiksa admin.</p>
          </fieldset>
          <div className="account-fields">
            <label>
              Nama akun
              <input
                name="label"
                maxLength={80}
                placeholder="Copy Trading 1"
                required
              />
            </label>
            <div className="broker-server-field">
              <label htmlFor="broker-server-query">Cari broker atau server MT5</label>
              <input
                id="broker-server-query"
                type="search"
                value={serverSearch}
                maxLength={96}
                autoComplete="off"
                placeholder="Contoh: HFM atau HFMarketsGlobal-Live11"
                aria-describedby="broker-server-help"
                onChange={(event) => {
                  const value = event.currentTarget.value;
                  setServerSearch(value);
                  setServerOptions([]);
                  setSelectedBrokerServer("");
                  setServerSearchBusy(value.trim().length >= 3);
                  setServerSearchMessage(
                    value.trim().length >= 3
                      ? "Menyiapkan pencarian…"
                      : "Ketik minimal 3 karakter untuk mencari server MT5."
                  );
                }}
              />
              {manualServerEntry ? (
                <>
                  <label htmlFor="manual-broker-server">Nama server akun</label>
                  <input
                    id="manual-broker-server"
                  name="server"
                  value={manualServer}
                  maxLength={128}
                  required
                  placeholder="Nama server persis dari broker"
                  onChange={(event) => setManualServer(event.currentTarget.value)}
                  />
                </>
              ) : (
                <>
                  <label htmlFor="broker-server-selection">Server akun</label>
                  <select
                    id="broker-server-selection"
                    name="server"
                    value={selectedBrokerServer}
                    required
                    onChange={(event) => setSelectedBrokerServer(event.currentTarget.value)}
                    aria-describedby="broker-server-help"
                  >
                    <option value="">Pilih server akun dari hasil pencarian</option>
                    {serverOptions.map((option) => (
                      <option key={`${option.company}:${option.name}`} value={option.name}>
                        {option.name} — {option.company}
                      </option>
                    ))}
                  </select>
                </>
              )}
              <span id="broker-server-help" className="field-help" role="status">
                {serverSearchBusy ? "Mencari direktori server MT5…" : serverSearchMessage || "Ketik minimal 3 karakter."}
              </span>
              <button
                type="button"
                className="server-manual-toggle secondary"
                onClick={() => {
                  setManualServerEntry((value) => !value);
                  setManualServer("");
                  setSelectedBrokerServer("");
                }}
              >
                {manualServerEntry ? "Pilih dari Daftar Server" : "Input Manual"}
              </button>
            </div>
            <label>
              Nomor akun broker
              <input
                name="login"
                maxLength={32}
                inputMode="numeric"
                pattern="[0-9]+"
                title="Nomor akun MT5 hanya boleh berisi angka"
                required
                onInput={handleLoginInput}
                onInvalid={handleLoginInvalid}
              />
            </label>
          </div>
          <div className="form-actions">
            <button type="submit" disabled={connectingAccount} aria-busy={connectingAccount}>{connectingAccount ? "Menyiapkan pembayaran…" : "Lanjutkan ke pembayaran"}</button>
          </div>
        </form>
        <p id="account-message" className="status" role="status">
          {accountMessage}
        </p>
        <div id="new-token" hidden={!tokenBox}>
          {tokenBox && (
            <>
              <h3>{tokenBox.title}</h3>
              <p>
                Salin token ini sekarang, lalu tempel ke input AccountToken pada EA
                MT5. Token tidak ditampilkan lagi setelah panel ditutup.
              </p>
              <pre>{tokenBox.token}</pre>
              <button type="button" onClick={handleCopyToken}>
                {tokenCopied ? "Tersalin" : "Salin token"}
              </button>
            </>
          )}
        </div>
        {hasExistingPackage && showAccountForm && (
          <div className="account-form-actions">
            <button type="button" className="account-action-button account-action-button--secondary" onClick={() => setShowAccountForm(false)}>Tutup formulir</button>
          </div>
        )}
        </>
        )}
      </section>

      <section>
        <h2>Saldo dan performa akun</h2>
        <div id="client-balance-groups" className="account-perf-list">
          {accounts.length === 0 ? (
            <p className="empty-state">
              Belum ada data saldo.
            </p>
          ) : (
            accounts.map((account) => (
              <article key={account.id} className="account-perf-item">
                <h3>
                  {account.label} · {account.login}
                </h3>
                <AccountPerformanceDetails accountId={account.id} alwaysOpen />
              </article>
            ))
          )}
        </div>
        <div id="client-performance-groups" className="grid client-performance-groups">
          {performanceGroups.length === 0 ? (
            <p className="empty-state">
              Belum ada transaksi tertutup.
            </p>
          ) : (
            performanceGroups.map((group) => (
              <article key={`${group.trade_mode}-${group.currency}`} className="card">
                <h3>
                  Performa {modeLabel(group.trade_mode)} · {group.currency}
                </h3>
                <p className="account-balance">
                  <Metrics
                    items={[
                      [
                        "Hasil terealisasi",
                        group.realized_pnl,
                        group.currency,
                        numberTone(group.realized_pnl),
                      ],
                    ]}
                  />
                </p>
                <p className="muted">
                  {`${formatInteger(group.closed_trades)} transaksi ditutup · ${formatInteger(
                    group.wins
                  )} menang · ${formatInteger(group.losses)} kalah · tingkat kemenangan ${formatInteger(
                    group.win_rate
                  )}%`}
                </p>
              </article>
            ))
          )}
        </div>
        <p id="client-performance-updated" className="muted" aria-live="polite">
          {performance?.updated_at
            ? `Diperbarui ${wib(performance.updated_at)} WIB`
            : "Belum ada data MT5."}
        </p>
      </section>

      <section>
        <h2>Akun Copy Trading Anda</h2>
        <div id="accounts" className="grid">
          {accounts.length === 0 ? (
            <p className="empty-state">Belum ada akun MT5.</p>
          ) : (
            accounts.map((account) => {
              const plan = plans[account.plan] || ZERO_PLAN;
              return account.execution_mode === "MANAGED" ? (
                <ManagedAccountCard
                  key={account.id}
                  account={account}
                  plan={plan}
                  managedWorkerReady={managedWorkerReady}
                  onDirty={() => {
                    managedFormDirty.current = true;
                  }}
                  onMessage={setAccountMessage}
                  onReload={() => {
                    managedFormDirty.current = false;
                    void loadAccounts();
                  }}
                />
              ) : (
                <EaAccountCard
                  key={account.id}
                  account={account}
                  plan={plan}
                  onMessage={setAccountMessage}
                  onReload={loadAccounts}
                  onToken={handleToken}
                  clearTokenForAccount={clearTokenForAccount}
                />
              );
            })
          )}
        </div>
      </section>

      <section id="ea-install-section" hidden={!hasEaAccount}>
        <h2>Pasang EA Copy Trading</h2>
        <p className="section-help">Hanya untuk akun yang menjalankan EA sendiri.</p>
        <div className="toolbar">
          <a
            className="workspace-tag ea-download-button"
            href="/downloads/MT5FollowerCopyEA.mq5"
          >
            Unduh source EA Copy Trading
          </a>
        </div>
        <ol>
          <li>Hubungkan akun di atas dan salin token (hanya tampil sekali).</li>
          <li>
            File → Open Data Folder → <code>MQL5/Experts</code>, salin file{" "}
            <code>.mq5</code>, lalu compile di MetaEditor (F7).
          </li>
          <li>
            Tools → Options → Expert Advisors → Allow WebRequest, tambahkan{" "}
            <code>{rootUrl || "root URL situs ini"}</code>.
          </li>
          <li>
            Pasang EA di chart, isi <code>AccountToken</code> dan{" "}
            <code>ServerURL</code>, lalu aktifkan Algo Trading.
          </li>
          <li>Atur <code>VolumeMultiplier</code> sebelum menjalankan.</li>
        </ol>
        <div
          className="notice-box"
          role="note"
          aria-label="Bantuan pesan status EA Copy Trading"
        >
          <h3>Arti pesan di chart MT5</h3>
          <p>
            <strong>HTTP -1 / ERROR 4006:</strong> EA belum mendapat jawaban.
            Samakan <code>ServerURL</code> dengan alamat di{" "}
            <em>Allow WebRequest</em>.
          </p>
          <p>
            <strong>TRADING OFF:</strong> Aktifkan <strong>Algo Trading</strong>{" "}
            dan izin trading akun.
          </p>
        </div>
        <p className="notice-box">
          Akun harus mode <strong>hedging</strong>. Volume = volume sumber ×{" "}
          <code>VolumeMultiplier</code>. Sinkronisasi berhenti jika akun utama
          tidak memperbarui lebih dari 20 detik.
        </p>
        <p className="section-help">
          Jangan bagikan token. Jika bocor, pilih <strong>Rotasi token</strong>.
        </p>
      </section>

    </main>
  );
}
