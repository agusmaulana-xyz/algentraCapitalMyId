import type { Metadata, Viewport } from "next";
import Script from "next/script";
import { InitialApiLoading } from "./components/InitialApiLoading";
import "./globals.css";

export const metadata: Metadata = {
  title: "Algentra Capital | AI Copy Trading",
  description:
    "Algentra Capital membantu membaca sinyal XAUUSD dengan AI, menjalankan instruksi melalui EA MetaTrader, dan menyalin posisi ke akun Copy Trading.",
  icons: {
    icon: [
      { url: "/brand-kit/favicon/favicon.svg", type: "image/svg+xml" },
      { url: "/brand-kit/favicon/favicon-16.png", sizes: "16x16", type: "image/png" },
      { url: "/brand-kit/favicon/favicon-32.png", sizes: "32x32", type: "image/png" },
      { url: "/brand-kit/favicon/favicon-48.png", sizes: "48x48", type: "image/png" },
      { url: "/brand-kit/favicon/favicon-192.png", sizes: "192x192", type: "image/png" },
      { url: "/brand-kit/favicon/favicon-512.png", sizes: "512x512", type: "image/png" },
    ],
    apple: [{ url: "/brand-kit/favicon/favicon-180.png", sizes: "180x180", type: "image/png" }],
    other: [
      { rel: "mask-icon", url: "/brand-kit/favicon/app-icon-maskable.svg", color: "#0B1F3A" },
      { rel: "icon", url: "/brand-kit/favicon/app-icon-maskable-512.png" },
    ],
  },
};

export const viewport: Viewport = {
  themeColor: "#0d1a24",
};

// Mirror base.html: theme read from cookie/localStorage, fallback dark, applied
// before paint to avoid a flash of the wrong theme.
const themeInitScript = `(function () {
  try {
    var cookieTheme = (document.cookie.match(/(?:^|;)\\s*theme=(dark|light)/) || [])[1];
    var stored = null;
    try { stored = localStorage.getItem("theme"); } catch (e) { /* cookie keeps the preference */ }
    var selected = stored === "dark" || stored === "light" ? stored : (cookieTheme === "light" ? "light" : "dark");
    document.documentElement.setAttribute("data-theme", selected);
    var meta = document.querySelector('meta[name="theme-color"]');
    if (meta) meta.setAttribute("content", selected === "light" ? "#f2f2f2" : "#0d1a24");
  } catch (e) { /* keep server-rendered default */ }
})();`;

export default function RootLayout({
  children,
}: Readonly<{ children: React.ReactNode }>) {
  return (
    <html
      lang="id"
      data-theme="dark"
      suppressHydrationWarning
    >
      <body>
        <InitialApiLoading />
        <Script
          id="theme-init"
          strategy="beforeInteractive"
          dangerouslySetInnerHTML={{ __html: themeInitScript }}
        />
        {children}
      </body>
    </html>
  );
}
