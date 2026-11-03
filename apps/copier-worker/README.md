# Copier worker MT5

Copier worker menjalankan copier MT5 pada Windows. Mode managed mengambil job follower dari Core API; mode standalone menjalankan daftar follower dari file konfigurasi lokal. Gunakan akun demo sampai alur, simbol, dan batas risikonya sudah diperiksa.

Portal client menyewakan terminal MT5 seharga Rp2.000/hari, minimum 10 hari. Masa sewa dimulai setelah pembayaran disetujui dan worker menghentikan terminal saat masa sewa habis. Login memakai nama server logis yang dipilih client melalui katalog server MT5 (perusahaan lalu server), bukan alamat endpoint host:port dari direktori luar. Worker mempertahankan katalog `servers.dat` terminal yang tersedia atau menyalinnya dari instalasi MT5 pengguna. Simbol follower tidak ditebak: sebelum membuka chart, worker membaca simbol yang benar-benar tersedia di akun, yaitu simbol gold yang pernah terverifikasi atau daftar Market Watch akun. Untuk akun baru, worker login sekali tanpa Expert agar broker menyinkronkan daftar simbol, lalu terminal dibuka pada simbol akun tersebut. Setelah login, `MT5LoginProbe` memilih simbol gold dari katalog akun (mis. `XAUUSDm`) dan menyimpannya untuk copier. Suffix broker seperti `c`, `m`, atau `.ds` tidak perlu ditebak satu per satu; override simbol tetap tersedia untuk broker dengan nama instrumen khusus. Satu terminal hanya memiliki satu chart penuh: worker menghapus chart tersimpan di profil sebelum terminal dijalankan, dan EA menutup chart tambahan. Worker memaksimalkan jendela MT5. Sesudah pembayaran, client memilih EA bawaan server atau mengunggah EA sendiri (`.mq5` atau `.ex5`, maksimum 10 MB). Worker mengompilasi `.mq5` menggunakan MetaEditor pada terminal template, lalu memasang EA dan menjalankannya otomatis. EA harus dipercaya oleh pemilik akun dan dapat memerlukan input/izin khusus; pastikan konfigurasi terminal dan broker sesuai sebelum trading live.

## Cara kerja

Pada mode managed, `TelegramSignalEA` di terminal master mengirim snapshot ke Core API. Worker mengambil job aktif dan meneruskan snapshot ke publisher Go lokal. Satu terminal MT5 follower terisolasi dibuat untuk tiap akun, lalu `MT5FollowerCopyEA` mengeksekusi order. Worker juga mengirim heartbeat, status akun, dan data transaksi ke portal.

Mode standalone menggunakan `MT5MasterPublisherEA` pada terminal master dan konfigurasi follower lokal. Kedua mode memakai binary Go untuk supervisor/publisher dan EA MT5 untuk membaca atau mengeksekusi transaksi. Terminal serta EA master selalu disiapkan manual; installer worker mengotomatisasi binary worker, terminal follower bersih, dan EA follower.

```text
Terminal master + EA sumber
          | snapshot
          v
       Core API -- worker Go -- publisher/feed lokal
                                  +-- terminal MT5 follower 1 + EA
                                  +-- terminal MT5 follower 2 + EA
                                  +-- terminal MT5 follower N + EA
```

## Prasyarat

- Windows dengan sesi desktop interaktif untuk akun yang menjalankan MT5.
- Go tersedia jika worker harus dibangun dari source; installer juga dapat memakai binary rilis yang tersedia.
- Instalasi MT5 untuk template dan MetaEditor akan dideteksi otomatis. Jika tidak ada, installer mengunduh serta memasang terminal resmi MetaQuotes untuk menyiapkan follower; gunakan `-MT5SourceTerminalDir` hanya bila ingin memilih instalasi tertentu.
- Core API dapat dijangkau dari mesin worker dan `COPIER_WORKER_API_KEY` tersedia untuk Core API serta akun worker. Gunakan nilai secret yang sama pada keduanya.

Python dan package MetaTrader5 Python tidak diperlukan.

## Pasang managed worker

Pada instalasi production, `install.ps1` menjalankan setup ini otomatis setelah Core API siap. Jalankan perintah berikut hanya jika memasang worker secara terpisah atau development.

Jalankan PowerShell dari root repository, masuk sebagai akun Windows yang akan menjalankan terminal, lalu jalankan:

```powershell
.\apps\copier-worker\scripts\install_managed_worker_task.ps1 `
  -ProjectRoot "C:\Algentra\algentraCapitalProject" `
  -ApiBaseUrl "https://api.example.com"
```

Jika MT5 tidak ditemukan otomatis, tambahkan parameter, misalnya:

```powershell
-MT5SourceTerminalDir "C:\Program Files\MetaTrader 5"
```

Installer membangun atau memilih binary Go, menyiapkan salinan template terminal follower yang bersih, mengompilasi `MT5FollowerCopyEA.mq5` dan `MT5LoginProbe.mq5`, membuat konfigurasi/runtime worker, memeriksa API key dan koneksi backend, kemudian mendaftarkan serta memulai Scheduled Task **Algentra Managed Copy Worker**. Sumber MT5 hanya dibaca; terminal master tidak diubah. Task berjalan saat akun Windows login karena MT5 membutuhkan desktop interaktif.

Worker memerlukan `COPIER_WORKER_API_KEY` yang sama dengan Core API. Installer membacanya dari environment Windows atau file `.env`/`.env.local` di root project, lalu menyimpannya untuk akun Windows worker. Untuk development, helper `run_managed_worker.ps1` dapat membuat konfigurasi awal dan menjalankan worker lokal:

```powershell
.\apps\copier-worker\scripts\run_managed_worker.ps1
```

Jika helper baru saja membuat konfigurasi, tinjau `config\managed-worker.json` dan sesuaikan `api_base_url` sebelum menjalankannya kembali. Template [managed-worker.example.json](config/managed-worker.example.json) memberi contoh nilai konfigurasi; installer mengisi path template dan runtime yang sesuai.

## Siapkan terminal dan EA master

Setup master dilakukan manual pada terminal MT5 master yang login ke akun sumber.

- Untuk managed mode dengan `master_source: telegram_signal`, compile dan pasang `TelegramSignalEA.mq5` sesuai dokumentasi EA dan konfigurasi Core API. Izinkan WebRequest ke API jika diminta EA.
- Untuk standalone dengan `master_source: publisher_ea`, compile dan pasang `MT5MasterPublisherEA.mq5`, izinkan WebRequest ke `http://127.0.0.1:8765`, serta samakan `MASTER_PUBLISHER_TOKEN` antara publisher Go dan input EA.

Jangan jalankan `scripts/start-all.ps1` bersamaan dengan managed worker; managed worker sudah menjalankan publisher yang diperlukan.

## Konfigurasi standalone

Contoh yang dilacak Git tersedia di [master.example.json](config/master.example.json). Salin contoh ke nama konfigurasi lokal; `.gitignore` mengabaikan file konfigurasi JSON lokal:

```powershell
Set-Location apps\copier-worker
Copy-Item config\master.example.json config\master.json
```

Buat konfigurasi follower dan `config\launcher.json` sesuai akun yang akan dijalankan. Setiap follower harus memakai path terminal unik, login/password environment variable sendiri, folder sinyal yang sama, serta batas `max_lot_per_order`, `max_posisi_terbuka`, dan `max_lot_total`. Jangan menyimpan password broker di JSON, argumen proses, atau log. Jalankan `scripts\start-all.ps1` setelah binary dan konfigurasi siap. Scheduled Task standalone dapat dibuat dengan `scripts\install_startup_task.ps1`.

Contoh mode lot: `tetap` memakai `lot_tetap`, `rasio` memakai `rasio_lot`, dan `saldo` membandingkan saldo follower dengan `saldo_master`. Volume dibulatkan mengikuti aturan broker; order yang lebih kecil dari `volume_min` dilewati.

## Runtime, log, dan pemeliharaan

Dengan konfigurasi contoh managed, runtime dibuat relatif terhadap konfigurasi worker di `apps\copier-worker\config\runtime`. Folder berisi log, state worker, sinyal, dan salinan terminal follower. Lokasi runtime ditentukan dari `runtime_dir` di `managed-worker.json`, jadi tidak perlu diisi terpisah di environment.

Log utama:

- `config\runtime\logs\managed_worker.log` — worker managed.
- `config\runtime\logs\managed_worker-task-error.log` — error startup sebelum logger worker aktif.
- `config\runtime\logs\master-publisher.log` dan `managed_<account_id>.log` — publisher dan follower.

Setelah memperbarui source worker, hentikan dan mulai ulang task agar binary baru dipakai:

```powershell
Stop-ScheduledTask -TaskName 'Algentra Managed Copy Worker'
Start-ScheduledTask -TaskName 'Algentra Managed Copy Worker'
Get-ScheduledTaskInfo -TaskName 'Algentra Managed Copy Worker' |
  Select-Object LastRunTime, LastTaskResult
```

Task memasang dan menjalankan worker, tetapi tidak membuat akun follower, menyetujui pembayaran, atau mengaktifkan copy. Client tetap mengelola aktivasi dari portal. Menjeda atau menghapus profil menghentikan copier, tetapi posisi yang sudah terbuka tetap berada di akun broker dan tidak ditutup otomatis.

## Pemeriksaan dan pemecahan masalah

- Uji dengan akun demo, `dry_run`/batas risiko yang sesuai, lalu periksa log dan heartbeat sebelum mengaktifkan copy live.
- Pastikan Auto Trading dan **Allow algorithmic trading** aktif di terminal follower.
- Periksa kecocokan server broker, login, mapping simbol, minimum/step lot, dan batas risiko. Login MT5 tidak menentukan server broker secara unik.
- Jika portal terus menunjukkan `CHECKING`, periksa `managed_worker.log`, hasil EA probe, dan konektivitas worker ke Core API serta server broker.
- Error `10027` biasanya menunjukkan Auto Trading mati; `10030` terkait filling mode; `10014` terkait volume atau spesifikasi lot broker.
- Pada akun Netting, worker mengelola exposure virtual per simbol. Perubahan manual pada simbol yang disalin dapat menyebabkan ketidaksesuaian; gunakan akun khusus copier dan pantau log.
- Ketika worker dihentikan, SL/TP virtual per posisi master pada akun Netting tidak dipantau. Pastikan worker tetap aktif dan pahami aturan risiko broker sebelum menjalankan akun live.

Eksekusi follower dapat berbeda dari master karena spread, slippage, latency, ukuran lot minimum, dan aturan broker. Verifikasi aturan broker/prop firm mengenai copy trading sebelum penggunaan.
