# Algentra Capital

Algentra Capital berisi Core API Go, portal Next.js, dan copier MT5. Produksi berjalan pada Windows Server dengan SQLite lokal, NSSM untuk API/web/Cloudflare Tunnel, serta Scheduled Task interaktif untuk worker MT5.

## Pengembangan lokal

1. Salin `.env.example` menjadi `.env.local`. Isi kredensial admin dan integrasi yang dipakai. Untuk development, buat `APP_SECRET_KEY` acak; API key integrasi boleh kosong jika fiturnya tidak digunakan.
2. Jalankan Core API dari root repository:

   ```powershell
   go run ./cmd/algentra-backend
   ```

   Database otomatis memakai `data/database.sqlite`; listener API default `127.0.0.1:8000`.
3. Jalankan portal dari folder `apps/web`:

   ```powershell
   npm ci
   npm run dev
   ```

   Portal tersedia di `http://127.0.0.1:3000`.

Nilai seperti alamat listener, lokasi database, cookie secure, origin WebSocket, dan URL direktori broker diturunkan dari konfigurasi deployment. Kredensial QRIS AirPay diisi manual sebagai `AIRPAY_API_KEY` dan `AIRPAY_MERCHANT_UID`. Lihat komentar di `.env.example` untuk membedakan isian manual dan nilai otomatis/default. Worker dan pemasangan EA follower dijelaskan di [panduan copier](apps/copier-worker/README.md); terminal serta EA master tetap disiapkan manual.

## Paket dan instalasi Windows Server

Paket installer harus menempatkan `install.ps1`, source Go (`go.mod`, `cmd`, dan `internal`), source web di `apps\web`, folder `infra\windows`, serta aset MT5 dan `apps\copier-worker` di direktori paket. Binary backend, backup, dan copier boleh disertakan; jika tidak ada, installer membangunnya otomatis ke folder `bin`. Build otomatis memerlukan Go di `PATH`. Installer juga menjalankan `npm ci` dan `npm run build` dari source `apps\web`; Node.js/npm harus terpasang di server. Jika `nssm.exe` atau `cloudflared.exe` tidak disertakan, installer mengunduhnya otomatis dan memeriksa checksum. Jalankan PowerShell sebagai Administrator dari sesi Windows yang akan menjalankan MT5:

```powershell
.\install.ps1 -InstallDir C:\AppServer
```

Installer menyiapkan `.env` dan secret otomatis, lalu membuka file tersebut untuk operator mengisi domain, token API Cloudflare, password admin, dan integrasi yang digunakan. Domain harus sudah aktif di Cloudflare. Tidak perlu mengisi token tunnel. Token API perlu izin **Account: Tunnel Read/Edit**, **Zone: DNS Edit**, dan **Zone: Read**. Installer membuat tunnel, rute website/API, serta DNS domain secara otomatis. Token API sekali pakai dihapus dari `.env`; token tunnel disimpan dalam file rahasia yang hanya bisa dibaca SYSTEM, Administrator, dan service tunnel. Installer memakai `cloudflared` versi 2025.4.0 atau lebih baru. Service NSSM berjalan sebagai `NT AUTHORITY\LocalService` dengan izin file terpisah berdasarkan service SID. API listen di `127.0.0.1:8000`, web di `127.0.0.1:3000`.

**Rute Cloudflare dibuat otomatis oleh installer** dengan tujuan berikut:

| Alamat yang diminta pengunjung | Arahkan ke |
| --- | --- |
| `/api/*`, `/downloads/*`, `/ws/*` | `http://127.0.0.1:8000` (API) |
| Semua alamat lainnya | `http://127.0.0.1:3000` (website) |

Alamat lain akan membuka website. `/v1/*`, `/health`, dan `/ready` tidak diarahkan ke API. Jika domain sudah memiliki DNS A/AAAA, installer menggantinya dengan rute tunnel. Tidak perlu membuka port di firewall; koneksi tunnel dimulai dari server ke Cloudflare.

Saat service siap, `install.ps1` otomatis menjalankan `apps\copier-worker\scripts\install_managed_worker_task.ps1` sebagai pengguna Windows yang sedang login. Skrip menyiapkan template/EA follower dan membuat lalu memulai Scheduled Task worker. Pastikan MT5 terpasang di server. Terminal dan EA master tetap disiapkan manual. Langkah rinci ada di [panduan copier](apps/copier-worker/README.md).

### Perbaiki / build ulang

Setelah mengubah source, jalankan mode repair dari PowerShell Administrator. Repair membangun ulang dari source: backend, worker, master, dan launcher (binary lama tidak dipakai lagi), web (`npm ci` + `npm run build`), lalu menyalin ulang source EA, memasang ulang service dan task, dan memulai ulang worker. `.env`, `data`, dan `secrets` tidak diubah. Tutup terminal MT5 follower yang masih terbuka sebelum menjalankannya.

```powershell
.\install.ps1 -InstallDir C:\AppServer -Repair
```

Untuk memperbaiki satu service saja, gunakan salah satu switch berikut. `-RepairWeb` menyalin source web dari paket installer, menjalankan `npm ci` dan `npm run build`, lalu memasang ulang dan memulai service web. `-RepairCoreApi` memasang ulang service dari binary Core API yang tersedia di instalasi. `-RepairCloudflare` meminta API token Cloudflare secara tersembunyi, lalu memakai API untuk menyegarkan konfigurasi ingress, DNS domain, dan token Tunnel sebelum memasang ulang service. API token hanya digunakan selama perintah berjalan dan tidak disimpan ke disk. Gunakan token dengan izin Tunnel Read/Edit, Zone DNS Edit, dan Zone Read. Service tunnel dipaksa memakai HTTP/2 karena sebagian jaringan memblokir UDP/QUIC; server perlu mengizinkan koneksi keluar TCP port 7844.

```powershell
.\install.ps1 -InstallDir C:\AppServer -RepairCloudflare
.\install.ps1 -InstallDir C:\AppServer -RepairWeb
.\install.ps1 -InstallDir C:\AppServer -RepairCoreApi
```

Switch repair terpisah dapat digabungkan untuk memperbaiki beberapa service sekaligus. `-RepairApiCore` juga tersedia sebagai alias untuk `-RepairCoreApi`.

### Uninstall

`uninstall.ps1` menghapus service NSSM, Scheduled Task, proses dari folder instalasi, environment variable worker (`COPIER_WORKER_API_KEY`, `MASTER_PUBLISHER_TOKEN`), file sementara, dan folder instalasi. Skrip meminta konfirmasi `HAPUS`. MT5 di Program Files serta tunnel/DNS Cloudflare tidak ikut dihapus.

```powershell
.\uninstall.ps1 -InstallDir C:\AppServer              # hapus semuanya, termasuk database dan secret
.\uninstall.ps1 -InstallDir C:\AppServer -KeepData    # pertahankan data, .env, dan secrets
```

Ambil backup sebelum menghapus tanpa `-KeepData`. Opsi `-Force` melewati konfirmasi.

## Operasi

- [Layanan Windows](docs/SERVICE.md)
- [Backup dan pemulihan](docs/BACKUP.md)
- [Operasi produksi](docs/OPERATIONS.md)
- [Kontrak API](packages/contracts/README.md)
