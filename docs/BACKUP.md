# Backup dan Pemulihan SQLite

Backup memakai `VACUUM INTO` agar snapshot konsisten ketika SQLite memakai WAL. File kemudian dikompres ZIP, dienkripsi dengan AES-GCM menggunakan kunci turunan dari `APP_SECRET_KEY`, diberi checksum SHA-256, dan dikirim ke `TELEGRAM_ADMIN_CHAT_ID` melalui bot. File sementara dihapus pada semua jalur keluar.

Konfigurasi yang diperlukan: `APP_SECRET_KEY`, `TELEGRAM_BOT_TOKEN`, `TELEGRAM_ADMIN_CHAT_ID`, dan `PROJECT_ROOT`. Backup otomatis mengambil database dari `data/database.sqlite` di bawah `PROJECT_ROOT`; set `DATABASE_FILE` hanya untuk memakai lokasi lain. Jika berkas terenkripsi lebih besar dari batas unggah Telegram, bot mengirim notifikasi ukuran dan file sementara dibersihkan; siapkan tujuan backup tambahan untuk database besar.

Simpan salinan `.env` terenkripsi di lokasi terpisah. Tanpa `APP_SECRET_KEY`, backup tidak dapat didekripsi.

Pemulihan: hentikan Core API, dekripsi kontainer `ALGENTRA1` (AES-256-GCM per chunk; kunci SHA-256 dari `APP_SECRET_KEY`), ekstrak database dari ZIP, hapus `database.sqlite-wal` dan `database.sqlite-shm` lama, ganti file database, lalu mulai service dan cek `/ready`. Uji restore berkala pada salinan server.
