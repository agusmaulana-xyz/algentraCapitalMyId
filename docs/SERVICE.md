# Operasi Windows Server Standalone

## Service

`AlgentraCoreApi`, `AlgentraWeb`, dan `AlgentraTunnel` dikelola NSSM dan bind hanya ke loopback. Setelah API dan web siap, `install.ps1` otomatis menyiapkan copier managed serta Scheduled Task pada akun Windows yang sedang login. Jalankan installer dari sesi desktop akun yang akan menjalankan MT5.

Pada instalasi awal, `install.ps1` memakai Cloudflare API untuk membuat tunnel dan rute domain: `/api/*`, `/downloads/*`, dan `/ws/*` menuju `http://127.0.0.1:8000`; path lain menuju `http://127.0.0.1:3000`. Token API sekali pakai memerlukan izin Account Tunnel Read/Edit, Zone DNS Edit, dan Zone Read, lalu dihapus dari `.env`. Token tunnel disimpan di file rahasia yang hanya dapat dibaca SYSTEM, Administrators, dan service tunnel. Installer mengganti DNS A/AAAA untuk domain tersebut dengan CNAME tunnel. `/v1/*`, `/health`, dan `/ready` tidak diarahkan ke Core API. Gunakan `cloudflared` 2025.4.0 atau lebih baru. Tidak perlu membuka inbound firewall.

## Konfigurasi

`C:\AppServer\.env` dimuat oleh launcher Core API. Batasi ACL untuk SYSTEM, Administrators, dan service Core API. Pertahankan `APP_SECRET_KEY`; rotasi membutuhkan re-enkripsi data. Database hanya dibuka Core API di `data\database.sqlite`, dengan WAL, foreign keys, dan busy timeout.

## Health dan update

Periksa `http://127.0.0.1:8000/health`, `/ready`, `http://127.0.0.1:3000`, dan domain publik. `/ready` memeriksa koneksi, tabel inti, dan `PRAGMA quick_check`. Jalur worker `/api/mt5/copier/*` dibatasi 1800 permintaan/menit (jalur lain 120), dan worker mengunggah laporan akun paling cepat tiap 2 detik; HTTP 429 berulang di `managed_worker.log` berarti batas ini terlampaui. Gunakan `install.ps1 -Repair` untuk build ulang dan `uninstall.ps1` untuk menghapus instalasi (lihat README utama).

Sebelum update, ambil backup, hentikan worker dan Core API, simpan versi binary/web sebelumnya, lalu deploy paket terverifikasi tanpa mengubah `.env`, `data`, atau `mt5`. Perubahan skema memerlukan skrip SQL manual yang ditinjau dan backup terlebih dahulu.
