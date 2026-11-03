"use client";

import { useCallback, useEffect, useState } from "react";
import { apiRequest } from "../lib/api";

type EAInput = { name: string; type: string; default: string; comment: string };
type ParamsResponse = {
  custom_ea_name: string;
  inputs: EAInput[];
  params: Record<string, string>;
  free_form: boolean;
  paused: boolean;
};

type Row = { name: string; value: string };

export function CustomEAParamsPanel({ accountId }: { accountId: string }) {
  const [data, setData] = useState<ParamsResponse | null>(null);
  const [values, setValues] = useState<Record<string, string>>({});
  const [rows, setRows] = useState<Row[]>([]);
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const response = await apiRequest<ParamsResponse>(`mt5/accounts/${encodeURIComponent(accountId)}/custom-ea/params`);
      setData(response);
      setValues(response.params);
      setRows(Object.entries(response.params).map(([name, value]) => ({ name, value })));
      setMessage("");
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "Parameter EA tidak dapat dimuat.");
    }
  }, [accountId]);

  useEffect(() => { void load(); }, [load]);

  const save = async () => {
    if (!data) return;
    const params: Record<string, string> = {};
    if (data.free_form) {
      for (const row of rows) if (row.name.trim()) params[row.name.trim()] = row.value;
    } else {
      // Nilai kosong berarti memakai default bawaan EA.
      for (const [name, value] of Object.entries(values)) if (value !== "") params[name] = value;
    }
    setBusy(true);
    setMessage("");
    try {
      await apiRequest(`mt5/accounts/${encodeURIComponent(accountId)}/custom-ea/params`, { method: "PUT", body: JSON.stringify({ params }) });
      setMessage("Parameter tersimpan. Terminal akan dimulai ulang otomatis untuk memakai nilai baru.");
      await load();
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "Parameter EA gagal disimpan.");
    } finally {
      setBusy(false);
    }
  };

  const setPaused = async (paused: boolean) => {
    setBusy(true);
    setMessage("");
    try {
      await apiRequest(`mt5/accounts/${encodeURIComponent(accountId)}/custom-ea/paused`, { method: "PUT", body: JSON.stringify({ paused }) });
      setMessage(paused ? "EA sendiri dijeda." : "EA sendiri akan dijalankan oleh worker.");
      await load();
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "Status EA gagal disimpan.");
    } finally {
      setBusy(false);
    }
  };

  return (
    <main className="page-shell dashboard-page account-page" id="main-content" tabIndex={-1}>
    <section className="panel section ea-params">
      <div className="ea-params__head">
        <span className="eyebrow">EA sendiri</span>
        <h2>Parameter EA</h2>
        {data?.custom_ea_name ? <p className="ea-params__file">{data.custom_ea_name}</p> : null}
        {data ? (
          <span className={`ea-params__state${data.paused ? " ea-params__state--paused" : ""}`}>
            {data.paused ? "Dijeda" : "Berjalan"}
          </span>
        ) : null}
      </div>
      <p className="muted ea-params__hint">
        Kosongkan kolom untuk memakai nilai default EA. {data?.paused ? "EA sedang dijeda; simpan parameter lalu tekan Jalankan EA." : "Perubahan akan memulai ulang terminal akun ini."}
      </p>
      {data && !data.free_form && (
        <div className="ea-params__grid">
          {data.inputs.map((input) => (
            <label key={input.name} className="ea-params__field">
              <span className="ea-params__name">
                {input.name}
                <small>{input.type}{input.comment ? ` · ${input.comment}` : ""}</small>
              </span>
              <input
                value={values[input.name] ?? ""}
                placeholder={input.default}
                maxLength={256}
                onChange={(event) => { const value = event.currentTarget.value; setValues((current) => ({ ...current, [input.name]: value })); }}
              />
            </label>
          ))}
        </div>
      )}
      {data?.free_form && (
        <div className="ea-params__manual">
          <p className="muted">Daftar parameter belum terdeteksi. Server membacanya otomatis dari terminal saat EA dijeda (sekitar 1–2 menit setelah upload); muat ulang halaman ini nanti. Anda juga bisa mengisi nama dan nilai secara manual.</p>
          {rows.map((row, index) => (
            <div className="ea-params__row" key={index}>
              <input aria-label="Nama parameter" placeholder="NamaParameter" maxLength={64} value={row.name} onChange={(event) => { const name = event.currentTarget.value; setRows((current) => current.map((item, i) => (i === index ? { ...item, name } : item))); }} />
              <input aria-label="Nilai parameter" placeholder="Nilai" maxLength={256} value={row.value} onChange={(event) => { const value = event.currentTarget.value; setRows((current) => current.map((item, i) => (i === index ? { ...item, value } : item))); }} />
              <button type="button" className="secondary" onClick={() => setRows((current) => current.filter((_, i) => i !== index))}>Hapus</button>
            </div>
          ))}
          <button type="button" className="secondary ea-params__add" onClick={() => setRows((current) => [...current, { name: "", value: "" }])}>+ Tambah parameter</button>
        </div>
      )}
      <div className="ea-params__actions">
        <button type="button" disabled={busy || !data} onClick={() => void save()}>{busy ? "Menyimpan…" : "Simpan parameter"}</button>
        {data ? <button type="button" className="secondary" disabled={busy} onClick={() => void setPaused(!data.paused)}>{data.paused ? "Jalankan EA" : "Pause EA"}</button> : null}
        <a className="ea-params__back" href="/account">← Kembali ke akun</a>
      </div>
      {message ? <p className="status" role="status">{message}</p> : null}
    </section>
    </main>
  );
}