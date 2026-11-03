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

function parseSetPreset(text: string): Row[] {
  const rows: Row[] = [];
  const seen = new Set<string>();
  for (const line of text.replace(/^\uFEFF/, "").split(/\r?\n/)) {
    const separator = line.indexOf("=");
    if (separator < 1) continue;
    const name = line.slice(0, separator).trim();
    if (!/^[A-Za-z_][A-Za-z0-9_]{0,63}$/.test(name) || seen.has(name)) continue;
    const value = (line.slice(separator + 1).split("||", 1)[0] ?? "").trim();
    if (value.length > 256 || rows.length >= 200) continue;
    seen.add(name);
    rows.push({ name, value });
  }
  return rows;
}

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

  const importPreset = async (event: React.ChangeEvent<HTMLInputElement>) => {
    const file = event.currentTarget.files?.[0];
    event.currentTarget.value = "";
    if (!file) return;
    if (file.size > 256 * 1024) {
      setMessage("File .set terlalu besar (maksimum 256 KB).");
      return;
    }
    try {
      const bytes = new Uint8Array(await file.arrayBuffer());
      let text: string;
      if (bytes[0] === 0xff && bytes[1] === 0xfe) text = new TextDecoder("utf-16le").decode(bytes.subarray(2));
      else if (bytes[0] === 0xfe && bytes[1] === 0xff) text = new TextDecoder("utf-16be").decode(bytes.subarray(2));
      else if (bytes.length > 3 && bytes[1] === 0 && bytes[3] === 0) text = new TextDecoder("utf-16le").decode(bytes);
      else text = new TextDecoder("utf-8").decode(bytes);
      const imported = parseSetPreset(text);
      if (!imported.length) {
        setMessage("Tidak ada parameter yang terbaca. Simpan file .set dari tab Inputs EA di MT5 lalu coba lagi.");
        return;
      }
      setRows(imported);
      setMessage(`${imported.length} parameter berhasil dimuat dari ${file.name}. Tekan Simpan parameter untuk menerapkannya.`);
    } catch {
      setMessage("File .set tidak dapat dibaca.");
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
          <p className="muted">Daftar input EA belum terdeteksi otomatis. Di MT5, buka properti EA pada chart, pilih tab Inputs, lalu klik Save untuk membuat file .set. Impor file itu di sini agar semua nama dan nilai parameter dimuat sekaligus.</p>
          <label className="secondary ea-params__import">
            Impor parameter dari file .set
            <input type="file" accept=".set,text/plain" onChange={(event) => void importPreset(event)} />
          </label>
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
