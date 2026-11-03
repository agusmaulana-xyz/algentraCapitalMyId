/**
 * Klien API browser -> Core API (/api/*).
 *
 * Perbaikan dibanding versi lama:
 *  - timeout per request (sebelumnya bisa menggantung selamanya -> UI "loading" terus);
 *  - GET otomatis diulang 2x untuk kegagalan transien (jaringan putus, 502/503/504, timeout);
 *    non-GET TIDAK pernah diulang agar tidak menggandakan aksi (order, pembayaran, dsb.);
 *  - GET identik yang sedang berjalan digabung (dedupe) -> SiteFrame + halaman + polling
 *    tidak lagi memukul endpoint yang sama berkali-kali;
 *  - cache singkat untuk data publik yang jarang berubah;
 *  - ApiError membawa status HTTP, sehingga komponen tidak perlu fetch ulang hanya untuk
 *    mengetahui 401.
 */

export class ApiError extends Error {
  status: number;
  constructor(message: string, status: number) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

const SAFE_METHODS = new Set(["GET", "HEAD", "OPTIONS"]);
const RETRY_STATUS = new Set([502, 503, 504]);
const GET_TIMEOUT_MS = 20_000;
const WRITE_TIMEOUT_MS = 30_000;
const UPLOAD_TIMEOUT_MS = 90_000;
const RETRY_DELAYS_MS = [400, 1200];
const NETWORK_MESSAGE = "Server tidak dapat dihubungi. Periksa koneksi Anda lalu coba lagi.";
const TIMEOUT_MESSAGE = "Server terlalu lama merespons. Coba lagi sebentar.";

// Data publik yang jarang berubah: boleh dipakai ulang beberapa detik.
const CACHE_TTL_MS: Record<string, number> = {
  "public/registration-status": 3_000,
  "public/contact": 60_000,
  "mt5/plans": 60_000,
  "mt5/pricing": 60_000,
  "payments/manual-instructions": 60_000,
};

const inflight = new Map<string, Promise<unknown>>();
const cache = new Map<string, { expires: number; data: unknown }>();

function readError(data: unknown, status: number): string {
  if (typeof data === "object" && data !== null && "detail" in data && typeof data.detail === "string" && data.detail.trim()) {
    return data.detail;
  }
  return `Permintaan tidak berhasil (HTTP ${status}).`;
}

function reportApiActivity(phase: "start" | "end"): void {
  if (typeof window !== "undefined") {
    window.dispatchEvent(new Event(`algentra:api-${phase}`));
  }
}

function currentCsrf(): string | null {
  if (typeof window === "undefined") return null;
  try {
    return sessionStorage.getItem("algentra-csrf");
  } catch {
    return null;
  }
}

function rememberCsrf(data: unknown): void {
  if (typeof window === "undefined" || typeof data !== "object" || data === null) return;
  const token = (data as { csrf_token?: unknown }).csrf_token;
  if (typeof token === "string" && token.length >= 32) {
    try { sessionStorage.setItem("algentra-csrf", token); } catch { /* Storage may be disabled. */ }
  }
}

function beginActionButtonLoading(): () => void {
  if (typeof document === "undefined") return () => {};
  const active = document.activeElement;
  let button = active instanceof HTMLButtonElement ? active : null;
  if (!button && active instanceof HTMLElement) {
    button = active.closest("form")?.querySelector<HTMLButtonElement>(
      'button[type="submit"], button:not([type])'
    ) ?? null;
  }
  if (!button || button.hasAttribute("aria-busy")) return () => {};
  button.dataset.autoLoading = "true";
  button.setAttribute("aria-busy", "true");
  return () => {
    delete button!.dataset.autoLoading;
    button!.removeAttribute("aria-busy");
  };
}

function sleep(ms: number, signal?: AbortSignal | null): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, ms);
    signal?.addEventListener("abort", () => { clearTimeout(timer); resolve(); }, { once: true });
  });
}

/** Satu percobaan fetch dengan timeout; sinyal milik pemanggil tetap dihormati. */
async function attempt(url: string, init: RequestInit, timeoutMs: number): Promise<Response> {
  const controller = new AbortController();
  let timedOut = false;
  const timer = setTimeout(() => { timedOut = true; controller.abort(); }, timeoutMs);
  const outer = init.signal;
  const onOuterAbort = () => controller.abort();
  if (outer) {
    if (outer.aborted) controller.abort();
    else outer.addEventListener("abort", onOuterAbort, { once: true });
  }
  try {
    return await fetch(url, { ...init, signal: controller.signal });
  } catch (error) {
    if (outer?.aborted) throw error; // dibatalkan pemanggil (unmount, dll.)
    throw new ApiError(timedOut ? TIMEOUT_MESSAGE : NETWORK_MESSAGE, 0);
  } finally {
    clearTimeout(timer);
    outer?.removeEventListener("abort", onOuterAbort);
  }
}

async function execute<T>(url: string, init: RequestInit, method: string, timeoutMs: number): Promise<T> {
  const canRetry = method === "GET" || method === "HEAD";
  const maxRetries = canRetry ? RETRY_DELAYS_MS.length : 0;
  let lastError: unknown;

  for (let tryIndex = 0; tryIndex <= maxRetries; tryIndex += 1) {
    if (tryIndex > 0) {
      await sleep(RETRY_DELAYS_MS[tryIndex - 1], init.signal);
      if (init.signal?.aborted) throw lastError;
    }
    try {
      const response = await attempt(url, init, timeoutMs);
      if (RETRY_STATUS.has(response.status) && tryIndex < maxRetries) {
        lastError = new ApiError(`HTTP ${response.status}`, response.status);
        continue;
      }
      const data: unknown = await response.json().catch(() => ({}));
      rememberCsrf(data);
      if (!response.ok) throw new ApiError(readError(data, response.status), response.status);
      return data as T;
    } catch (error) {
      lastError = error;
      const transient = error instanceof ApiError && error.status === 0;
      if (!(transient && tryIndex < maxRetries)) throw error;
    }
  }
  throw lastError;
}

export async function apiRequest<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  const method = (init.method ?? "GET").toUpperCase();
  const safe = SAFE_METHODS.has(method);
  if (init.body && !headers.has("content-type")) headers.set("content-type", "application/json");
  if (!safe) {
    const csrf = currentCsrf();
    if (csrf) headers.set("x-csrf-token", csrf);
  }
  const url = `/api/${path}`;
  const requestInit: RequestInit = { ...init, method, headers, cache: "no-store" };

  // Perubahan data -> buang cache agar bacaan berikutnya selalu segar.
  if (!safe) cache.clear();

  // Dedupe & cache hanya untuk GET tanpa sinyal pembatalan milik pemanggil.
  const shareable = method === "GET" && !init.signal;
  if (shareable) {
    const hit = cache.get(path);
    if (hit && hit.expires > Date.now()) return hit.data as T;
    const pending = inflight.get(path);
    if (pending) return pending as Promise<T>;
  }

  const stopLoading = safe ? () => {} : beginActionButtonLoading();
  reportApiActivity("start");
  const run = execute<T>(url, requestInit, method, safe ? GET_TIMEOUT_MS : WRITE_TIMEOUT_MS)
    .then((data) => {
      const ttl = CACHE_TTL_MS[path];
      if (shareable && ttl) cache.set(path, { expires: Date.now() + ttl, data });
      return data;
    })
    .finally(() => {
      stopLoading();
      reportApiActivity("end");
      if (shareable) inflight.delete(path);
    });

  if (shareable) inflight.set(path, run);
  return run;
}

export async function apiUpload<T>(path: string, form: FormData): Promise<T> {
  const headers = new Headers();
  const csrf = currentCsrf();
  if (csrf) headers.set("x-csrf-token", csrf);
  cache.clear();
  const stopLoading = beginActionButtonLoading();
  reportApiActivity("start");
  try {
    return await execute<T>(`/api/${path}`, { method: "POST", headers, body: form, cache: "no-store" }, "POST", UPLOAD_TIMEOUT_MS);
  } finally {
    stopLoading();
    reportApiActivity("end");
  }
}
