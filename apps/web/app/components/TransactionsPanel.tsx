"use client";

import { FormEvent, Suspense, useEffect, useRef, useState } from "react";
import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { apiRequest, apiUpload } from "../lib/api";

type Plan = {
  name: string;
  vcpu: string;
  ram: string;
  disk: string;
  price_idr: number;
};

type PaymentOrder = {
  id: number;
  invoice_code: string;
  label: string;
  server: string;
  login: string;
  plan: string;
  execution_mode: string;
  total_idr: number;
  rental_days?: number;
  status: string;
  payment_method: string;
  qris_status: string | null;
  created_at: string;
  has_proof: boolean;
  notification_sent: boolean;
};

type QrisCharge = {
  order_id: number;
  airpay_external_id: string;
  qr_code_url: string;
  gross_amount: number;
};

const money = (value: number) =>
  `Rp ${new Intl.NumberFormat("id-ID", { maximumFractionDigits: 0 }).format(value || 0)}`;

const orderDate = (value: string) =>
  value
    ? `${new Date(value).toLocaleString("id-ID", {
        timeZone: "Asia/Jakarta",
        dateStyle: "medium",
        timeStyle: "short",
      })} WIB`
    : "-";

const statusNames: Record<string, string> = {
  PENDING_PAYMENT: "Menunggu pembayaran",
  PAYMENT_SUBMITTED: "Sedang diproses",
  APPROVED: "Transaksi diterima",
  REJECTED: "Transaksi tidak valid",
  CANCELLED: "Dibatalkan",
};

const filterOptions: Array<[string, string]> = [
  ["ALL", "Semua transaksi"],
  ["PENDING_PAYMENT", "Menunggu pembayaran"],
  ["PAYMENT_SUBMITTED", "Sedang diproses"],
  ["APPROVED", "Transaksi diterima"],
  ["REJECTED", "Tidak valid"],
  ["CANCELLED", "Dibatalkan"],
];

type ManualPaymentDetails = {
  bank_name: string;
  payment_number: string;
  payment_name: string;
};

const manualPaymentFallback =
  "Hubungi admin melalui kanal resmi untuk mendapatkan rekening tujuan. Jangan transfer sebelum detail rekening dan nominal dikonfirmasi.";

function OrderDetails({ order, plans }: { order: PaymentOrder; plans: Record<string, Plan> }) {
  const plan = plans[order.plan] || { name: order.plan, vcpu: "-", ram: "-", disk: "-" };
  const rows: Array<[string, string]> = [
    ["Kode invoice", order.invoice_code || `INV-AC${String(order.id).padStart(4, "0")}`],
    ["Tanggal", orderDate(order.created_at)],
    ["Nama akun", order.label],
    ...(order.plan === "RENTAL" ? [["Masa sewa", `${order.rental_days || 10} hari`] as [string, string]] : []),
  ];
  const serviceRows: Array<[string, string]> = [
    ["Mode layanan", order.plan === "RENTAL" ? "Sewa server MT5" : order.execution_mode === "MANAGED" ? "Dikelola server" : "Pasang EA di MT5 sendiri"],
    ["Login broker", `${order.login} · ${order.server}`],
    ["Metode bayar", paymentMethodName(order.payment_method)],
  ];
  return (
    <div className="transaction-receipt__body">
      <dl className="transaction-receipt__rows">
        {rows.map(([label, value]) => <div key={label}><dt>{label}</dt><dd>{value}</dd></div>)}
      </dl>
      <div className="transaction-receipt__package">
        <strong>{plan.name}</strong>
        <span>{plan.vcpu} core · RAM {plan.ram} · Disk {plan.disk}</span>
      </div>
      <dl className="transaction-receipt__rows transaction-receipt__rows--service">
        {serviceRows.map(([label, value]) => <div key={label}><dt>{label}</dt><dd>{value}</dd></div>)}
      </dl>
      <div className="transaction-receipt__total"><strong>TOTAL</strong><b>{money(order.total_idr)}</b></div>
    </div>
  );
}

function paymentMethodName(value: string): string {
  const normalized = value.trim().toUpperCase();
  if (normalized === "QRIS") return "QRIS (otomatis)";
  if (normalized === "MANUAL") return "Transfer manual";
  return value ? value.replaceAll("_", " ") : "Belum dipilih";
}

function QrisPayment({ order, onRefresh }: { order: PaymentOrder; onRefresh: () => void }) {
  const [charge, setCharge] = useState<QrisCharge | null>(null);
  const [creating, setCreating] = useState(false);
  const [syncing, setSyncing] = useState(false);
  const [error, setError] = useState("");
  const [syncMessage, setSyncMessage] = useState("");
  const syncInFlightRef = useRef(false);
  const refreshRef = useRef(onRefresh);
  refreshRef.current = onRefresh;

  async function createCharge() {
    setCreating(true);
    setError("");
    try {
      const result = await apiRequest<QrisCharge>(`payments/orders/${order.id}/qris`, {
        method: "POST",
      });
      setCharge(result);
      refreshRef.current();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Gagal membuat QRIS.");
    } finally {
      setCreating(false);
    }
  }

  async function loadExisting() {
    try {
      const state = await apiRequest<QrisCharge>(`payments/orders/${order.id}/qris`);
      if (state.qr_code_url) setCharge(state);
    } catch {
      // Belum ada charge QRIS aktif.
    }
  }

  async function syncStatus(automatic = false) {
    if (syncInFlightRef.current) return;
    syncInFlightRef.current = true;
    setSyncing(true);
    if (!automatic) {
      setError("");
      setSyncMessage("");
    }
    try {
      const result = await apiRequest<{ order_status: string; result: string }>(
        `payments/orders/${order.id}/qris/sync`,
        { method: "POST" }
      );
      refreshRef.current();
      if (result.order_status === "APPROVED") {
        setCharge(null);
        if (!automatic) setSyncMessage("Pembayaran terverifikasi! Akun Copy Trading Anda sedang dibuat.");
      } else if (result.result === "PAYMENT_FAILED") {
        setCharge(null);
        setSyncMessage("QRIS dibatalkan atau kedaluwarsa. Buat kode QR baru untuk mencoba lagi.");
      } else if (!automatic) {
        setSyncMessage(
          "Pembayaran belum terdeteksi di AirPay. Jika Anda sudah membayar, tunggu sebentar lalu coba lagi."
        );
      }
    } catch (err) {
      if (automatic) {
        setSyncMessage("Status QRIS sementara tidak dapat diperiksa. Sistem akan mencoba kembali otomatis.");
      } else {
        setError(err instanceof Error ? err.message : "Gagal memeriksa status.");
      }
    } finally {
      syncInFlightRef.current = false;
      setSyncing(false);
    }
  }

  // Pulihkan QR yang sudah pernah dibuat. Charge baru hanya dibuat lewat tombol.
  useEffect(() => {
    if (order.payment_method === "QRIS") void loadExisting();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [order.id]);

  // Poll status AirPay karena dokumentasi provider tidak menyediakan webhook.
  useEffect(() => {
    if (!charge) return;
    const timer = setInterval(() => void syncStatus(true), 15000);
    return () => clearInterval(timer);
  }, [charge, order.id]);

  if (order.status !== "PENDING_PAYMENT") return null;

  return (
    <div className="qris-box">
      <p className="qris-title">Pembayaran otomatis via QRIS</p>
      {!charge ? (
        <>
          <button type="button" onClick={() => void createCharge()} disabled={creating} aria-busy={creating}>
            {creating ? "Membuat kode QR…" : "Buat kode QR"}
          </button>
          {error ? (
            <p className="status" role="alert">
              {error}
            </p>
          ) : null}
          <p className="transaction-note">
            Scan kode QR dengan e-wallet atau m-banking apa pun. Akun dibuat
            otomatis setelah pembayaran terverifikasi — tanpa menunggu admin.
          </p>
        </>
      ) : (
        <>
          <p className="transaction-note">
            Scan untuk membayar {money(charge.gross_amount)}. Status pembayaran
            diperiksa otomatis setiap beberapa detik.
          </p>
          {/* eslint-disable-next-line @next/next/no-img-element */}
          <img
            className="qris-code"
            src={charge.qr_code_url}
            alt={`Kode QRIS untuk pesanan ${order.id}`}
            width={220}
            height={220}
          />
          {syncMessage ? <p className="transaction-note">{syncMessage}</p> : null}
          {error ? (
            <p className="status" role="alert">
              {error}
            </p>
          ) : null}
          <div className="toolbar">
            <button
              type="button"
              onClick={() => void syncStatus()}
              disabled={syncing}
              aria-busy={syncing}
            >
              {syncing ? "Memeriksa…" : "Saya sudah bayar, cek status"}
            </button>
          </div>
        </>
      )}
    </div>
  );
}

function TransactionsPanelInner() {
  const searchParams = useSearchParams();
  const [orders, setOrders] = useState<PaymentOrder[]>([]);
  const [ordersLoaded, setOrdersLoaded] = useState(false);
  const [plans, setPlans] = useState<Record<string, Plan>>({});
  const [manualPayment, setManualPayment] = useState<ManualPaymentDetails | null>(null);
  const [filter, setFilter] = useState("PENDING_PAYMENT");
  const [expandedOrderId, setExpandedOrderId] = useState<number | null>(null);
  const [proofFormOrderId, setProofFormOrderId] = useState<number | null>(null);
  const [message, setMessage] = useState("");
  const [receiptOrder, setReceiptOrder] = useState<PaymentOrder | null>(null);
  const [successOrder, setSuccessOrder] = useState<PaymentOrder | null>(null);
  const [cancelOrder, setCancelOrder] = useState<PaymentOrder | null>(null);
  const [cancelling, setCancelling] = useState(false);
  const [qrisAvailable, setQrisAvailable] = useState(false);
  const fingerprintRef = useRef("");
  const dialogRef = useRef<HTMLDialogElement>(null);
  const successDialogRef = useRef<HTMLDialogElement>(null);
  const cancelDialogRef = useRef<HTMLDialogElement>(null);
  const orderStatusesRef = useRef<Map<number, string> | null>(null);

  async function loadOrders(force = false) {
    try {
      const data = await apiRequest<PaymentOrder[]>("payments/orders");
      const fingerprint = JSON.stringify(data);
      if (!force && fingerprint === fingerprintRef.current) return;
      const previousStatuses = orderStatusesRef.current;
      if (previousStatuses) {
        const newlyPaid = data.find((order) =>
          order.status === "APPROVED" && previousStatuses.get(order.id) !== "APPROVED"
        );
        if (newlyPaid) setSuccessOrder(newlyPaid);
      }
      orderStatusesRef.current = new Map(data.map((order) => [order.id, order.status]));
      fingerprintRef.current = fingerprint;
      setOrders(data);
      setOrdersLoaded(true);
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "Permintaan gagal.");
    }
  }

  useEffect(() => {
    void loadOrders(true);
    apiRequest<Record<string, Plan>>("mt5/plans")
      .then(setPlans)
      .catch(() => {});
    apiRequest<ManualPaymentDetails & { qris_available?: boolean }>("payments/manual-instructions")
      .then((result) => {
        setManualPayment({
          bank_name: result.bank_name || "",
          payment_number: result.payment_number || "",
          payment_name: result.payment_name || "",
        });
        setQrisAvailable(result.qris_available === true);
      })
      .catch(() => {});
    const requested = Number(searchParams.get("order")) || null;
    if (requested && requested > 0) setExpandedOrderId(requested);
    const timer = setInterval(() => {
      if (!document.hidden) void loadOrders();
    }, 7000);
    return () => clearInterval(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    if (receiptOrder && dialogRef.current && !dialogRef.current.open) {
      dialogRef.current.showModal();
    }
  }, [receiptOrder]);

  useEffect(() => {
    if (successOrder && successDialogRef.current && !successDialogRef.current.open) {
      successDialogRef.current.showModal();
    }
  }, [successOrder]);

  useEffect(() => {
    if (cancelOrder && cancelDialogRef.current && !cancelDialogRef.current.open) {
      cancelDialogRef.current.showModal();
    }
  }, [cancelOrder]);

  function closeReceipt() {
    dialogRef.current?.close();
  }

  async function notify(id: number) {
    try {
      const result = await apiRequest<{ notification_sent: boolean }>(
        `payments/orders/${id}/notify`,
        { method: "POST" }
      );
      setMessage(
        result.notification_sent
          ? "Notifikasi transaksi terkirim ke admin."
          : "Bot belum dapat menghubungi admin. Periksa konfigurasi Telegram."
      );
      await loadOrders(true);
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "Permintaan gagal.");
    }
  }

  async function cancel() {
    if (!cancelOrder || cancelling) return;
    const id = cancelOrder.id;
    setCancelling(true);
    try {
      await apiRequest(`payments/orders/${id}/cancel`, { method: "POST" });
      setMessage("Pesanan dibatalkan.");
      setExpandedOrderId(null);
      setProofFormOrderId(null);
      cancelDialogRef.current?.close();
      await loadOrders(true);
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "Permintaan gagal.");
    } finally {
      setCancelling(false);
    }
  }

  async function uploadProof(event: FormEvent<HTMLFormElement>, order: PaymentOrder) {
    event.preventDefault();
    const input = event.currentTarget.querySelector('input[type="file"]') as HTMLInputElement | null;
    const file = input?.files?.[0];
    if (!file) return;
    if (file.size > 5 * 1024 * 1024) {
      setMessage("Ukuran file bukti pembayaran maksimal 5 MB.");
      return;
    }
    const form = new FormData();
    form.append("file", file);
    try {
      await apiUpload(`payments/orders/${order.id}/proof`, form);
      setMessage("Bukti terkirim. Pembuatan akun sedang diproses sambil menunggu pemeriksaan admin.");
      setProofFormOrderId(null);
      setExpandedOrderId(null);
      await loadOrders(true);
    } catch (error) {
      setMessage(error instanceof Error ? error.message : "Permintaan gagal.");
    }
  }

  const visible = filter === "ALL" ? orders : orders.filter((order) => order.status === filter);

  return (
    <>
      <main className="page-shell dashboard-page" id="main-content">
        <div className="page-heading">
          <div>
            <span className="eyebrow">PORTAL KLIEN / PEMBAYARAN</span>
            <h1>Riwayat transaksi</h1>
            <p className="page-description">
              Lihat status pesanan, instruksi pembayaran manual, nota, dan bukti transaksi.
            </p>
          </div>
          <Link className="transaction-back-link" href="/account">
            <span aria-hidden="true">←</span>
            Kembali ke akun MT5
          </Link>
        </div>

        <section aria-labelledby="transactions-title">
          <h2 id="transactions-title">Pesanan dan pembayaran</h2>
          <div className="transaction-toolbar">
            <p className="section-help">
              Nota hanya dapat dibuka oleh pemilik akun. Bukti pembayaran yang dikirim tersimpan
              privat.
            </p>
            <label htmlFor="transaction-filter">
              Tampilkan
              <select
                id="transaction-filter"
                value={filter}
                onChange={(event) => setFilter(event.target.value)}
              >
                {filterOptions.map(([value, label]) => (
                  <option key={value} value={value}>
                    {label}
                  </option>
                ))}
              </select>
            </label>
          </div>
          <p id="transaction-message" className="status" role="status" aria-live="polite">
            {message}
          </p>
          <div id="transactions-list" className="grid" aria-live="polite">
            {!ordersLoaded ? (
              <div className="app-loading app-loading--inline" role="status" aria-live="polite">
                <span className="app-loading__spinner" aria-hidden="true" />
                <p>Memuat transaksi</p>
                <span className="app-loading__hint">Mengambil status pesanan terbaru…</span>
              </div>
            ) : visible.length === 0
              ? orders.length
                ? "Tidak ada transaksi untuk filter ini."
                : "Belum ada transaksi."
              : visible.map((order) => (
                  <article className="transaction-card" key={order.id} id={`order-${order.id}`}>
                    <div className="transaction-card__top">
                      <div className="transaction-receipt__brand"><strong>ALGENTRA CAPITAL</strong><span>STRUK PESANAN</span></div>
                      <span
                        className={`transaction-badge transaction-badge--${order.status.toLowerCase()}`}
                        data-status={order.status}
                      >
                        {statusNames[order.status] || order.status}
                      </span>
                    </div>
                    <OrderDetails
                      order={order}
                      plans={plans}
                    />

                    {order.status === "PAYMENT_SUBMITTED" && (
                      <p className="transaction-note">
                        {order.notification_sent
                          ? "Bukti sudah dikirim. Pembuatan akun sedang diproses dan menunggu pemeriksaan admin."
                          : "Bukti tersimpan, tetapi notifikasi ke admin belum terkirim."}
                      </p>
                    )}
                    {order.status === "APPROVED" && (
                      <p className="transaction-note">
                        {order.execution_mode === "MANAGED"
                          ? "Pembayaran diterima. Buka portal akun untuk mengatur kredensial broker; copy trading tetap dijeda sampai Anda memulainya."
                          : "Pembayaran diterima dan akun Copy Trading sudah dibuat."}
                      </p>
                    )}
                    {order.status === "REJECTED" && (
                      <p className="transaction-note">
                        Admin menandai pembayaran ini tidak valid. Hubungi admin untuk tindak lanjut.
                      </p>
                    )}

                    <div className="transaction-card__actions">
                      {order.has_proof && (
                        <button
                          type="button"
                          className="secondary"
                          onClick={() => setReceiptOrder(order)}
                        >
                          Lihat nota pembayaran
                        </button>
                      )}
                      {order.status === "PENDING_PAYMENT" && (
                        <button
                          type="button"
                          onClick={() =>
                            setExpandedOrderId((current) =>
                              current === order.id ? null : order.id
                            )
                          }
                        >
                          {expandedOrderId === order.id ? "Tutup pembayaran" : "Lanjutkan pembayaran"}
                        </button>
                      )}
                      {order.status === "PAYMENT_SUBMITTED" && !order.notification_sent && (
                        <button type="button" className="secondary" onClick={() => void notify(order.id)}>
                          Kirim ulang notifikasi admin
                        </button>
                      )}
                      {order.status === "APPROVED" && (
                        <button
                          type="button"
                          className="secondary"
                          onClick={() => window.location.assign("/account")}
                        >
                          Buka akun Copy Trading
                        </button>
                      )}
                      {(order.status === "PENDING_PAYMENT" ||
                        order.status === "PAYMENT_SUBMITTED") && (
                        <button
                          type="button"
                          className="secondary"
                          onClick={() => setCancelOrder(order)}
                        >
                          Batalkan pesanan
                        </button>
                      )}
                    </div>

                    {order.status === "PENDING_PAYMENT" && expandedOrderId === order.id && (
                      <>
                        {qrisAvailable && (
                          <QrisPayment
                            key={`qris-${order.id}`}
                            order={order}
                            onRefresh={() => void loadOrders(true)}
                          />
                        )}
                        <div className="manual-payment-option">
                          <p className="qris-title">Transfer manual</p>
                          {qrisAvailable && (
                            <p className="transaction-note">Pilih satu metode pembayaran saja. Jangan membayar melalui QRIS dan transfer sekaligus.</p>
                          )}
                          {manualPayment && (manualPayment.bank_name || manualPayment.payment_number || manualPayment.payment_name) ? (
                            <dl className="manual-payment-details" aria-label="Rekening tujuan pembayaran">
                              {([
                                ["Bank", manualPayment.bank_name],
                                ["Nomor rekening", manualPayment.payment_number],
                                ["Nama penerima", manualPayment.payment_name],
                              ] as const).filter(([, value]) => value).map(([label, value]) => (
                                <div className="manual-payment-details__row" key={label}>
                                  <dt>{label}</dt>
                                  <dd>{value}</dd>
                                </div>
                              ))}
                            </dl>
                          ) : (
                            <p className="transaction-note">{manualPaymentFallback}</p>
                          )}
                          {proofFormOrderId !== order.id ? (
                            <button type="button" onClick={() => setProofFormOrderId(order.id)}>
                              Saya sudah bayar via transfer
                            </button>
                          ) : (
                            <form
                              className="transaction-proof-form"
                              onSubmit={(event) => void uploadProof(event, order)}
                            >
                              <label>
                                Unggah bukti transaksi (JPG, PNG, atau WebP, maksimal 5 MB)
                                <input
                                  type="file"
                                  name="file"
                                  accept=".jpg,.jpeg,.png,.webp"
                                  required
                                />
                              </label>
                              <button type="submit">Kirim bukti transaksi</button>
                            </form>
                          )}
                        </div>
                      </>
                    )}
                  </article>
                ))}
          </div>
        </section>
      </main>

      <dialog
        className="cancel-order-dialog"
        aria-labelledby="cancel-order-title"
        ref={cancelDialogRef}
        onClick={(event) => {
          if (event.target === event.currentTarget && !cancelling) cancelDialogRef.current?.close();
        }}
        onClose={() => setCancelOrder(null)}
      >
        <div className="cancel-order-dialog__icon" aria-hidden="true">!</div>
        <h2 id="cancel-order-title">Batalkan pesanan?</h2>
        <p>Pesanan #{cancelOrder?.id} akan dibatalkan. Anda dapat membuat pesanan baru nanti.</p>
        <div className="cancel-order-dialog__actions">
          <button className="secondary" type="button" disabled={cancelling} onClick={() => cancelDialogRef.current?.close()}>
            Kembali
          </button>
          <button className="danger" type="button" disabled={cancelling} aria-busy={cancelling} onClick={() => void cancel()}>
            {cancelling ? "Membatalkan…" : "Ya, batalkan"}
          </button>
        </div>
      </dialog>

      <dialog
        id="receipt-dialog"
        className="receipt-dialog"
        aria-labelledby="receipt-title"
        ref={dialogRef}
        onClick={(event) => {
          if (event.target === event.currentTarget) closeReceipt();
        }}
        onClose={() => setReceiptOrder(null)}
      >
        <div className="receipt-dialog__head">
          <strong>Nota pembayaran</strong>
          <button
            id="receipt-close"
            className="secondary"
            type="button"
            aria-label="Tutup nota"
            onClick={closeReceipt}
          >
            Tutup
          </button>
        </div>
        <div id="receipt-content" className="receipt-dialog__body">
          {receiptOrder && (
            <>
              <h2 id="receipt-title">Nota transaksi #{receiptOrder.id}</h2>
              <p className="transaction-note">
                Status: {statusNames[receiptOrder.status] || receiptOrder.status}
              </p>
              <OrderDetails order={receiptOrder} plans={plans} />
              {receiptOrder.has_proof ? (
                <>
                  <h3>Bukti pembayaran</h3>
                  {/* eslint-disable-next-line @next/next/no-img-element */}
                  <img
                    className="receipt-proof"
                    src={`/api/payments/orders/${receiptOrder.id}/proof`}
                    alt={`Bukti pembayaran untuk transaksi ${receiptOrder.id}`}
                  />
                </>
              ) : (
                <p className="receipt-empty">
                  Bukti transaksi belum diunggah untuk pesanan ini.
                </p>
              )}
            </>
          )}
        </div>
      </dialog>

      <dialog
        className="payment-success-dialog"
        aria-labelledby="payment-success-title"
        ref={successDialogRef}
        onClick={(event) => {
          if (event.target === event.currentTarget) successDialogRef.current?.close();
        }}
        onClose={() => setSuccessOrder(null)}
      >
        <div className="payment-success-dialog__icon" aria-hidden="true">✓</div>
        <h2 id="payment-success-title">Pembayaran berhasil</h2>
        <p>
          Pembayaran pesanan #{successOrder?.id} telah terverifikasi.
          {successOrder?.execution_mode === "MANAGED"
            ? " Lanjutkan pengaturan akun broker di portal akun."
            : " Akun Copy Trading Anda sedang disiapkan."}
        </p>
        <button type="button" onClick={() => successDialogRef.current?.close()}>Tutup</button>
      </dialog>
    </>
  );
}

export function TransactionsPanel() {
  return (
    <Suspense>
      <TransactionsPanelInner />
    </Suspense>
  );
}
