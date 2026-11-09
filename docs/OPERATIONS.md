# Operasi dan fitur aplikasi

Panduan installasi production ada di [README](../README.md); deployment service di [SERVICE.md](./SERVICE.md), development di [DEVELOPMENT.md](./DEVELOPMENT.md), dan backup/restore di [BACKUP.md](./BACKUP.md).

Admin dapat melihat dan menyalin `EA_API_KEY` pada **Settings → EA_API_KEY untuk EA master**. Nilai diambil lewat endpoint khusus yang memerlukan sesi admin dan tidak disimpan di database settings. Untuk mengganti key, ubah `EA_API_KEY` pada environment layanan lalu mulai ulang Core API; konfigurasi EA master harus memakai nilai yang sama.

## Mode Risk/Reward signal Telegram

Admin mengatur mode RR pada **Dashboard → Settings → Aturan sinyal**:

- **Ikuti signal (TP terjauh):** memakai SL dari signal dan TP terjauh sesuai arah BUY/SELL; jarak SL boleh lebih besar daripada jarak TP.
- **Fixed RR:** mengabaikan SL/TP signal, mempertahankan harga entry atau entry zone, lalu menghitung SL dan TP dari jarak pips yang diisi admin. TP harus minimal sejauh SL.
- **RR dari SL signal:** mempertahankan SL signal dan mengalikan jaraknya dari harga entry dengan rasio `1 : X` untuk menghitung TP. Signal harus memiliki SL yang valid.

Pip dikonversi menurut digit simbol broker: simbol 3/5 digit memakai 10 point per pip, simbol lain memakai 1 point per pip. Entry zone tetap bisa dibagi menjadi dua entry bila **Mode entry** memakai Partial. Agar pengaturan RR baru berlaku di terminal master, compile dan pasang versi terbaru `mt5/experts/TelegramSignalEA.mq5` pada chart master.

Signal baru yang berlawanan arah tetap diproses tanpa menutup posisi lama atau membatalkan pending order. Akun MT5 hedging dapat menyimpan posisi BUY dan SELL bersamaan; pada akun netting, broker menggabungkan transaksi pada simbol yang sama ke posisi neto.

## Sewa server MT5

Portal hanya menawarkan sewa server MT5 harian: Rp2.000 per hari dengan minimum 10 hari. Client memilih lama sewa saat membuat pesanan. Masa sewa mulai dihitung setelah pembayaran disetujui; perpanjangan menambah hari dari tanggal kedaluwarsa yang masih berlaku. Saat sewa berakhir, worker menghentikan terminal akun.

Setelah pembayaran, client dapat memilih EA yang disediakan server atau mengunggah EA miliknya sendiri (`.mq5` atau `.ex5`), maksimal 10 MB. Worker mengompilasi `.mq5` dengan MetaEditor di server, kemudian memasang hasil EA di terminal sewaan dan menjalankannya otomatis. Login menggunakan nama server logis pada katalog native MT5; nama host:port yang mungkin ditampilkan terminal adalah endpoint jaringan server tersebut. EA mencocokkan simbol dengan katalog MT5 secara otomatis, termasuk suffix broker, serta menjaga satu chart aktif per terminal managed. Client tetap harus mengisi kredensial MT5 yang benar dan menunggu pemeriksaan login berhasil. File EA yang diunggah dijalankan pada terminal akun tersebut; unggah hanya EA yang dipercaya. Lihat [panduan managed copier](../apps/copier-worker/README.md#managed-copy-dari-portal-client).

AirPay mengonfirmasi pembayaran secara otomatis melalui backend. Transfer manual tetap memerlukan review admin.

Untuk onboarding otomatis, isi `AIRPAY_API_KEY` dan `AIRPAY_MERCHANT_UID` di environment Core API. Backend memanggil endpoint create/status AirPay dengan header `X-API-KEY`, menampilkan QRIS, lalu melakukan polling status selama QR tampil. Pastikan kredensial memiliki akses ke merchant yang benar. Jika QRIS belum dikonfigurasi, portal masih menyediakan transfer manual yang memerlukan review admin. Bot Telegram hanya diperlukan untuk notifikasi/review transfer manual.

## API dan kontrak

OpenAPI tersedia di [packages/contracts/openapi.json](../packages/contracts/openapi.json), dengan tipe TypeScript di [generated.d.ts](../packages/contracts/src/generated.d.ts). API versioned memakai `/v1/auth`, `/v1/cs`, `/v1/mt5`, `/v1/ea`, `/v1/payments`, `/v1/settings`, `/v1/parser`, `/v1/telegram`, dan `/v1/dashboard`; endpoint `/api/...` dipertahankan selama migrasi klien.

- Health: `GET /health` dan `GET /ready`.
- Auth client/admin: `/api/auth/*`; halaman client `/account` dan `/transactions` disajikan oleh web di web lokal.
- Akun MT5: `GET/POST /api/mt5/accounts`, `PUT /api/mt5/accounts/{id}`, `/active`, dan `/rotate-token`.
- Pesanan sewa: `/api/payments/orders` dengan plan `RENTAL` dan `rental_days`; pilihan EA: `/api/mt5/accounts/{id}/ea-choice`; upload EA: `/api/mt5/accounts/{id}/custom-ea`.
- EA: `POST /api/ea/master/snapshot`, `GET /api/ea/pending`, `POST /api/ea/report`, `/result`, dan `/heartbeat`.
- Admin: stats/signals/logs/settings di `/api/stats`, `/api/signals`, `/api/logs`, dan `/api/settings`.
- Telegram: `/api/telegram/status`, send/verify code, groups dan whitelist; halaman admin `/telegram-setup` ada di web lokal.

## Website, customer service, dan admin

Website publik hanya disajikan oleh Next.js di web lokal â€” Core API di Windows tidak lagi menyajikan halaman web apa pun (semua path non-API mengembalikan JSON 404). Performa agregat memakai trade tertutup dan bukan klaim hasil dari akun klien individual. Admin login melalui `/admin/login` di web lokal, lalu memakai `/dashboard` untuk statistik, signal, trade, log, kill switch, settings, dan status integrasi. Halaman `/parser-test` di web lokal menguji parsing/validasi saja; halaman itu tidak mengirim order. EA sinyal tetap live saat dijalankan; tidak tersedia demo/dry-run backend.

Chat ALGENTRA tersedia untuk pengunjung dan client; ringkasan account client dibatasi ke milik client tersebut. Chat dan parser sinyal menggunakan `GEMINI_API_KEY` dan `GEMINI_MODEL` yang sama. Jangan kirim password broker atau token EA melalui chat.

Untuk Telegram, isi `TELEGRAM_API_ID` dan `TELEGRAM_API_HASH`, lalu login admin di web lokal dan buka `/telegram-setup`. Core API Go menyimpan session MTProto di `backend/data/telegram_user.gotd.session`; file itu berisi kredensial login dan harus tetap privat. Instalasi production fresh tidak membutuhkan file session SQLite lama: login pertama membuat session Go baru. Konversi `backend/data/telegram_user.session` hanya untuk mempertahankan login Telegram pada upgrade host yang memang memiliki session Telethon sebelumnya; ini terpisah dari schema/database aplikasi.
