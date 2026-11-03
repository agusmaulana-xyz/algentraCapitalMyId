# Development Lokal

- Go API: `go run ./cmd/algentra-backend`
- Web Next.js: masuk ke `apps/web`, jalankan `npm install` dan `npm run dev`
- SQLite lokal: otomatis memakai `data/database.sqlite`; schema embedded dibuat saat database kosong. Atur `DATABASE_FILE` hanya jika ingin mengganti lokasi.
- Kredensial lokal disimpan di `.env.local`; jangan commit `.env`, database, token, atau kredensial
- Untuk produksi gunakan paket Windows standalone dan `install.ps1`
