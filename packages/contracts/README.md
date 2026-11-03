# Kontrak API

`openapi.json` adalah kontrak API yang dilacak Git. Selaraskan file ini dengan Core API saat menambah atau mengubah endpoint. Tipe TypeScript hasil generate ditulis ke `src/generated.d.ts` dan digunakan oleh aplikasi web.

Pasang dependensi sesuai lockfile dan generate ulang tipe dari direktori ini:

```powershell
npm ci
npm run generate:types
```

Saat bentuk API berubah, periksa lalu commit kontrak dan tipe hasil generate. Jangan masukkan kredensial database atau secret API ke kode browser; kode server web yang memanggil Core API.
