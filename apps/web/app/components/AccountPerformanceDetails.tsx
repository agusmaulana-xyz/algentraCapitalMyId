"use client";

import { useCallback, useEffect, useId, useRef, useState } from "react";
import { apiRequest } from "../lib/api";
import { formatInteger, formatLots, formatMoney, formatPrice, numberTone, toneClass } from "../lib/format";

type Daily = { date: string; profit: number; currency?: string | null; timezone: string };
type Deal = { ticket: string; symbol: string; action: string; volume: number; open_time?: string | null; close_time?: string | null; open_price?: number | null; close_price?: number | null; status: string; net_profit: number; currency?: string | null };
type Details = { balance?: number | null; equity?: number | null; floating_profit?: number | null; margin?: number | null; free_margin?: number | null; currency?: string | null; open_positions?: number; state_updated_at?: string | null; daily_profit: Daily[]; history: Deal[]; history_page: number; history_pages: number; history_total: number };

const WIB = "Asia/Jakarta";
const EMPTY = "—";
const wibDate = (date: string) => new Date(`${date}T00:00:00+07:00`).toLocaleDateString("id-ID", { timeZone: WIB, weekday: "short", day: "2-digit", month: "short" });
const wibStamp = (date: string) => new Date(date).toLocaleString("id-ID", { timeZone: WIB, day: "2-digit", month: "short", hour: "2-digit", minute: "2-digit" });
const wibFull = (date: string) => new Date(date).toLocaleString("id-ID", { timeZone: WIB });
const sideTone = (action: string) => (action.toUpperCase() === "BUY" ? "pos" : action.toUpperCase() === "SELL" ? "neg" : "");

type Props = { accountId: number; admin?: boolean; compact?: boolean; alwaysOpen?: boolean };

/**
 * compact / alwaysOpen: panel langsung terbuka tanpa tombol dan tanpa bingkai
 * (compact untuk modal yang sudah punya tombol tutup, alwaysOpen untuk dipasang langsung di halaman).
 * Tanpa keduanya, komponen memakai satu tombol buka/tutup.
 */
export function AccountPerformanceDetails({ accountId, admin = false, compact = false, alwaysOpen = false }: Props) {
  const fixedOpen = compact || alwaysOpen;
  const panelId = useId();
  const [open, setOpen] = useState(fixedOpen);
  const [details, setDetails] = useState<Details | null>(null);
  const [page, setPage] = useState(1);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const requestRef = useRef(0);
  const endpoint = admin ? `admin/copy-accounts/${accountId}/details` : `mt5/accounts/${accountId}/details`;

  // Satu jalur pemuatan untuk detail awal maupun pindah halaman; respons lama diabaikan.
  const load = useCallback(async (target: number) => {
    const id = ++requestRef.current;
    setLoading(true);
    setError("");
    try {
      const data = await apiRequest<Details>(`${endpoint}?page=${target}`);
      if (id !== requestRef.current) return;
      setDetails(data);
      setPage(data.history_page);
    } catch (reason) {
      if (id !== requestRef.current) return;
      setError(reason instanceof Error ? reason.message : "Detail akun tidak tersedia.");
    } finally {
      if (id === requestRef.current) setLoading(false);
    }
  }, [endpoint]);

  useEffect(() => {
    setDetails(null);
    setPage(1);
    setError("");
    if (fixedOpen) { setOpen(true); void load(1); }
    else setOpen(false);
    return () => { requestRef.current += 1; };
  }, [fixedOpen, load]);

  function toggle() {
    const next = !open;
    setOpen(next);
    if (next && !details && !loading) void load(page);
  }

  function goTo(target: number) {
    if (loading || target < 1 || (details && target > details.history_pages)) return;
    void load(target);
  }

  const currency = details?.currency;
  const stats: Array<{ label: string; value: string; tone?: string }> = details ? [
    { label: "Saldo", value: formatMoney(details.balance, currency) },
    { label: "Equity", value: formatMoney(details.equity, currency) },
    { label: "Profit floating", value: formatMoney(details.floating_profit, currency), tone: toneClass(numberTone(details.floating_profit)) },
    { label: "Margin", value: formatMoney(details.margin, currency) },
    { label: "Free margin", value: formatMoney(details.free_margin, currency) },
    { label: "Posisi terbuka", value: formatInteger(details.open_positions ?? 0) },
  ] : [];

  return <div className={`account-detail ${fixedOpen ? "account-detail--compact" : "account-detail--inline"}`}>
    {!fixedOpen && <button type="button" className="secondary account-detail__toggle" aria-expanded={open} aria-controls={panelId} onClick={toggle}>
      {open ? "Tutup detail" : "Saldo, profit & riwayat"}
    </button>}
    {open && <div id={panelId} className="account-detail__panel" aria-busy={loading}>
      {loading && !details && <p className="muted">Memuat detail akun…</p>}
      {error && <p className="status tone-neg" role="alert">{error}</p>}
      {details && <>
        <p className="account-detail__meta muted">
          {details.state_updated_at ? `Data terakhir: ${wibFull(details.state_updated_at)} WIB` : "Belum ada data dari terminal."}
        </p>
        <dl className="account-stats">
          {stats.map(item => <div key={item.label}><dt>{item.label}</dt><dd className={item.tone}>{item.value}</dd></div>)}
        </dl>
        <div>
          <h3>Profit harian · WIB</h3>
          {(details.daily_profit ?? []).length === 0 ? <p className="muted">Belum ada profit harian.</p> : <div className="table-wrap day-table-wrap">
            <table className="day-table">
              <thead><tr>{details.daily_profit.map(day => <th key={day.date}>{wibDate(day.date)}</th>)}</tr></thead>
              <tbody><tr>{details.daily_profit.map(day => <td key={day.date} className={toneClass(day.profit >= 0 ? "pos" : "neg")}>{formatMoney(day.profit, day.currency ?? currency)}</td>)}</tr></tbody>
            </table>
          </div>}
        </div>
        <div>
          <div className="account-detail__heading">
            <h3>Riwayat transaksi MT5</h3>
            <span className="muted">{formatInteger(details.history_total)} posisi</span>
          </div>
          {details.history.length === 0 ? <p className="muted">Belum ada transaksi.</p> : <>
            <div className="table-wrap account-history">
              <table>
                <thead><tr><th>Dibuka (WIB)</th><th>Ditutup (WIB)</th><th>Simbol</th><th>Arah</th><th className="num">Volume</th><th className="num">Harga buka</th><th className="num">Harga tutup</th><th className="num">Profit bersih</th></tr></thead>
                <tbody>{details.history.map((deal, index) => <tr key={`${deal.ticket}-${index}`}>
                  <td>{deal.open_time ? wibStamp(deal.open_time) : EMPTY}</td>
                  <td>{deal.close_time ? wibStamp(deal.close_time) : <span className={`pill ${toneClass("warn")}`}>Terbuka</span>}</td>
                  <td>{deal.symbol}</td>
                  <td><span className={`pill ${toneClass(sideTone(deal.action))}`}>{deal.action}</span></td>
                  <td className="num">{formatLots(deal.volume)}</td>
                  <td className="num">{deal.open_price == null ? EMPTY : formatPrice(deal.open_price)}</td>
                  <td className="num">{deal.close_price == null ? EMPTY : formatPrice(deal.close_price)}</td>
                  <td className={`num ${toneClass(numberTone(deal.net_profit))}`}>{formatMoney(deal.net_profit, deal.currency ?? currency)}</td>
                </tr>)}</tbody>
              </table>
            </div>
            {details.history_pages > 1 && <div className="account-detail__pager">
              <button type="button" className="secondary" disabled={page <= 1 || loading} onClick={() => goTo(page - 1)}>Sebelumnya</button>
              <span>Halaman {page} dari {details.history_pages}</span>
              <button type="button" className="secondary" disabled={page >= details.history_pages || loading} onClick={() => goTo(page + 1)}>Berikutnya</button>
            </div>}
          </>}
        </div>
      </>}
    </div>}
  </div>;
}