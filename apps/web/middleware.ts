import { NextRequest, NextResponse } from "next/server";

/**
 * Gerbang pemeliharaan.
 *
 * Sebelumnya middleware ini memanggil `/health` Core API pada SETIAP request
 * (halaman, aset statis, setiap panggilan /api, prefetch, polling 2 detik).
 * Akibatnya: tiap aksi menunggu 1 round-trip tambahan, beban ke backend berlipat,
 * dan satu probe yang lambat (>2,5 dtk) langsung menampilkan "pemeliharaan"
 * walau backend sebenarnya normal.
 *
 * Sekarang:
 *  - hanya navigasi dokumen HTML yang diperiksa (bukan aset, bukan /api, bukan prefetch RSC);
 *  - hasil probe di-cache singkat per instance;
 *  - fail-open: timeout / error jaringan TIDAK dianggap down. Halaman tetap dirender dan
 *    panggilan API sendiri yang melaporkan error. Pemeliharaan hanya tampil bila Core API
 *    benar-benar menjawab dengan status 5xx (mis. 502/503/530 dari tunnel/proxy).
 */

const API_UNAVAILABLE_MESSAGE = "Layanan sedang dalam pemeliharaan. Coba lagi beberapa saat lagi.";
const PROBE_TIMEOUT_MS = 4000;
const UP_TTL_MS = 20_000;
const DOWN_TTL_MS = 5_000;

type Health = { down: boolean; status: number; detail: string; expires: number };
let cached: Health | null = null;
let inflight: Promise<Health> | null = null;

function escapeHtml(value: string): string {
  return value.replace(/[&<>"']/g, (character) => {
    const entities: Record<string, string> = {
      "&": "&amp;",
      "<": "&lt;",
      ">": "&gt;",
      '"': "&quot;",
      "'": "&#39;",
    };
    return entities[character];
  });
}

function maintenanceResponse(status: number, detail: string): Response {
  const isDevelopment = process.env.NODE_ENV === "development";
  const diagnostics = isDevelopment
    ? `<details open><summary>Detail error (development)</summary><pre>HTTP ${status || "network"}\n${escapeHtml(detail)}</pre></details>`
    : "";

  return new Response(
    `<!doctype html>
<html lang="id" data-theme="dark">
  <head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <meta name="robots" content="noindex">
    <meta name="color-scheme" content="dark light">
    <title>Pemeliharaan | Algentra Capital</title>
    <style>
      :root{color-scheme:dark;--bg:#0d1a24;--panel:#132432;--raised:#1a2f40;--line:#2a4152;--ink:#e8f0f5;--muted:#a3b6c3;--action:#4da3e8;--on-action:#08121a;--gold:#f2c438;--glass:rgb(19 36 50 / 78%);--glass-border:rgb(255 255 255 / 16%)}
      @media (prefers-color-scheme:light){:root{color-scheme:light;--bg:#f2f2f2;--panel:#fff;--raised:#eaf3f8;--line:#d1dce2;--ink:#183345;--muted:#526573;--action:#036fb3;--on-action:#fff;--glass:rgb(255 255 255 / 82%);--glass-border:rgb(24 51 69 / 14%)}}
      *{box-sizing:border-box}
      body{margin:0;min-height:100svh;display:grid;place-items:center;padding:24px;background:var(--bg);color:var(--ink);font:400 16px/1.7 "Plus Jakarta Sans",Inter,ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif}
      main{width:min(100%,560px);padding:clamp(24px,6vw,44px);border:1px solid var(--glass-border);border-radius:20px;background:var(--glass);box-shadow:0 18px 48px rgb(0 0 0 / 18%);backdrop-filter:blur(16px);-webkit-backdrop-filter:blur(16px)}
      .eyebrow{color:var(--action);font-size:11px;font-weight:800;letter-spacing:.16em;text-transform:uppercase}
      h1{margin:12px 0 10px;font:700 clamp(26px,6vw,36px)/1.2 Poppins,"Trebuchet MS",ui-rounded,system-ui,sans-serif;letter-spacing:-.035em}
      p{margin:0;color:var(--muted)}
      .status{display:flex;align-items:center;gap:10px;margin-top:22px;color:var(--muted);font-size:13px}
      .dot{width:9px;height:9px;flex:none;border-radius:50%;background:var(--gold);box-shadow:0 0 12px var(--gold);animation:pulse 1.6s ease-in-out infinite}
      @keyframes pulse{50%{opacity:.35}}
      .actions{display:flex;flex-wrap:wrap;gap:10px;margin-top:26px}
      a,button{display:inline-flex;min-height:42px;align-items:center;justify-content:center;padding:9px 16px;border:1px solid var(--action);border-radius:10px;background:var(--action);color:var(--on-action);font:750 13px/1 inherit;text-decoration:none;cursor:pointer}
      a:hover,button:hover{filter:brightness(1.08)}
      :focus-visible{outline:2px solid var(--action);outline-offset:3px}
      details{margin-top:22px;padding:14px;border:1px solid var(--line);border-radius:10px;color:var(--muted);font-size:13px}
      summary{cursor:pointer;font-weight:700;color:var(--ink)}
      pre{margin:12px 0 0;white-space:pre-wrap;overflow-wrap:anywhere;font:12px/1.55 ui-monospace,SFMono-Regular,Consolas,monospace}
      @media (prefers-reduced-motion:reduce){.dot{animation:none}}
    </style>
  </head>
  <body>
    <main aria-live="polite">
      <div class="eyebrow">Algentra Capital</div>
      <h1>Kami sedang melakukan pemeliharaan</h1>
      <p>${API_UNAVAILABLE_MESSAGE}</p>
      <div class="status"><span class="dot" aria-hidden="true"></span><span id="state">Memeriksa ulang otomatis setiap 10 detikâ€¦</span></div>
      <div class="actions"><button type="button" onclick="location.reload()">Coba lagi</button></div>
      ${diagnostics}
    </main>
    <script>setTimeout(function(){location.reload()},10000)</script>
  </body>
</html>`,
    {
      status: 503,
      headers: {
        "cache-control": "no-store",
        "content-type": "text/html; charset=utf-8",
        "retry-after": "10",
      },
    },
  );
}

async function probe(): Promise<Health> {
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), PROBE_TIMEOUT_MS);
  try {
    const origin = "http://127.0.0.1:8000";
    const headers = new Headers({ accept: "application/json" });
    const response = await fetch(new URL("/health", origin), {
      method: "GET",
      headers,
      cache: "no-store",
      signal: controller.signal,
    });
    if (response.status >= 500) {
      const body = (await response.text().catch(() => "")).slice(0, 2000);
      return {
        down: true,
        status: response.status,
        detail: body || `Core API health returned HTTP ${response.status}`,
        expires: Date.now() + DOWN_TTL_MS,
      };
    }
    // 2xx/3xx/4xx = Core API menjawab. 4xx (mis. Cloudflare Access / rute /health dibatasi)
    // bukan alasan menutup situs; proxy /api melaporkan masalah auth sendiri.
    return { down: false, status: response.status, detail: "", expires: Date.now() + UP_TTL_MS };
  } catch (error) {
    // Timeout / error jaringan: ambigu -> fail-open.
    const detail = error instanceof Error ? `${error.name}: ${error.message}` : String(error);
    return { down: false, status: 0, detail, expires: Date.now() + DOWN_TTL_MS };
  } finally {
    clearTimeout(timeout);
  }
}

async function getHealth(): Promise<Health> {
  if (cached && cached.expires > Date.now()) return cached;
  inflight ??= probe()
    .then((result) => {
      cached = result;
      return result;
    })
    .finally(() => {
      inflight = null;
    });
  return inflight;
}

const SESSION_COOKIES = [
  "alg_admin_session",
  "alg_client_session",
  "__Host-alg_admin_session",
  "__Host-alg_client_session",
];

export async function middleware(request: NextRequest): Promise<Response> {
  const { pathname } = request.nextUrl;
  if (pathname === "/dashboard" || pathname.startsWith("/dashboard/")) {
    const hasSession = SESSION_COOKIES.some((name) => request.cookies.get(name)?.value);
    if (!hasSession) return NextResponse.redirect(new URL("/admin/login", request.url));
  }

  const isDocument =
    request.method === "GET" &&
    request.headers.get("sec-fetch-dest") === "document" &&
    !request.headers.has("next-router-prefetch") &&
    !request.headers.has("rsc");
  if (!isDocument) return NextResponse.next();

  const health = await getHealth();
  return health.down ? maintenanceResponse(health.status, health.detail) : NextResponse.next();
}

export const config = {
  // Lewati aset statis & berkas publik; /api/* tidak perlu probe (proxy sudah menangani error upstream).
  matcher: [
    "/((?!api/|_next/|brand-kit/|favicon\\.ico|robots\\.txt|sitemap\\.xml|musik\\.mp3|.*\\.(?:svg|png|jpg|jpeg|gif|webp|avif|ico|css|js|map|mp3|woff2?|txt)$).*)",
  ],
};
