# Kontrak Rute API dan Jaringan

Core API bind ke `127.0.0.1:8000`; Next.js bind ke `127.0.0.1:3000`. Browser menggunakan URL same-origin `/api/...`, yang diteruskan Cloudflare Tunnel langsung ke Core API. Halaman dan aset lain dilayani Next.js.

| Path publik | Tujuan |
| --- | --- |
| `/api/*` | Core API `http://127.0.0.1:8000` |
| `/downloads/*` | Core API `http://127.0.0.1:8000` |
| `/ws/*` (WebSocket) | Core API `http://127.0.0.1:8000` |
| Path lainnya | Next.js `http://127.0.0.1:3000` |

Installer membuat aturan Cloudflare secara otomatis. Rute API, download, dan WebSocket ditempatkan sebelum rute website. Jangan arahkan `/v1/*`, `/health`, atau `/ready` ke Core API. `/v1` dipakai integrasi EA/worker, sedangkan health dan readiness tetap untuk pemeriksaan lokal.

Frontend menambahkan `X-CSRF-Token` pada request perubahan data. Core API membatasi Origin untuk request browser, menerapkan sesi cookie HttpOnly/Secure/SameSite, rate limit, dan autentikasi API key untuk EA/worker. Status pembayaran QRIS diperiksa ke AirPay melalui polling server.
