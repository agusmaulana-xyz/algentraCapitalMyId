import type { Metadata } from "next";
import { NotFoundActions } from "./components/NotFoundActions";
import { SiteFrame } from "./components/SiteFrame";

export const metadata: Metadata = {
  title: "Halaman tidak ditemukan | Algentra Capital",
  robots: { index: false, follow: false },
};

/**
 * Halaman 404 — dibungkus SiteFrame (header, navigasi, tombol tema, footer) seperti halaman
 * lain, memakai token warna UI saat ini (--action biru, --accent emas, panel kaca) dan
 * maskot robot Algentra.
 */
export default function NotFound() {
  return (
    <SiteFrame>
      <main id="main-content" tabIndex={-1} className="page-shell dashboard-page not-found-shell">
        <section className="not-found-card" aria-labelledby="not-found-title">
          <div className="not-found-copy">
            <span className="eyebrow">ALGENTRA CAPITAL / 404</span>
            <span className="not-found-code">HALAMAN TIDAK DITEMUKAN</span>
            <h1 id="not-found-title">Sinyalnya nyasar?</h1>
            <p>
              Chart-nya jalan, halamannya nggak ketemu. Tenang, ini cuma floating loss satu
              halaman—yuk balik ke area aman.
            </p>
            <NotFoundActions />
          </div>
        <svg className="not-found-art" viewBox="0 0 420 330" role="img" aria-labelledby="nf-art-title nf-art-desc">
          <title id="nf-art-title">Maskot robot Algentra kebingungan melihat chart yang bergerak</title>
          <desc id="nf-art-desc">Chart candlestick XAUUSD bergerak sementara maskot robot Algentra menatap layar dengan secangkir kopi dan ekspresi salah entry.</desc>
          <defs>
            <linearGradient id="nf-panel" x1="0" y1="0" x2="0" y2="1">
              <stop offset="0" stopColor="#17293a" />
              <stop offset="1" stopColor="#0f1d29" />
            </linearGradient>
            <linearGradient id="nf-bot" x1="0" y1="0" x2="1" y2="1">
              <stop offset="0" stopColor="#f1f8fe" />
              <stop offset="1" stopColor="#cfe3f5" />
            </linearGradient>
            <linearGradient id="nf-body" x1="0" y1="0" x2="1" y2="1">
              <stop offset="0" stopColor="#6db8f0" />
              <stop offset="1" stopColor="#3a7fb8" />
            </linearGradient>
          </defs>
          <rect x="25" y="24" width="370" height="246" rx="19" fill="url(#nf-panel)" stroke="#2a4152" strokeWidth="2" />
          <path d="M26 62h368" stroke="#2a4152" />
          <rect x="43" y="36" width="18" height="14" rx="4" fill="#4da3e822" stroke="#6db8f0" />
          <path d="M47 46v-4m4 4v-7m4 7v-5" stroke="#6db8f0" strokeWidth="1.5" strokeLinecap="round" />
          <text x="69" y="48" fill="#e8f0f5" fontSize="11" fontWeight="800" fontFamily="system-ui, sans-serif">XAUUSD</text>
          <text x="125" y="48" fill="#7e95aa" fontSize="9" fontFamily="system-ui, sans-serif">· M1</text>
          <circle className="live-dot" cx="345" cy="43" r="3" fill="#43d17d" />
          <text x="353" y="47" fill="#83e3a8" fontSize="8" fontWeight="700" fontFamily="system-ui, sans-serif">LIVE</text>
          <g className="chart-grid" stroke="#6db8f01a" strokeWidth="1">
            <path d="M49 88h326M49 119h326M49 150h326M49 181h326M49 212h326" />
            <path d="M80 78v144m48-144v144m48-144v144m48-144v144m48-144v144m48-144v144" />
          </g>
          <g fill="#7e95aa" fontSize="7" fontFamily="system-ui, sans-serif">
            <text x="31" y="91">2352</text>
            <text x="31" y="122">2350</text>
            <text x="31" y="153">2348</text>
            <text x="31" y="184">2346</text>
          </g>
          <path className="chart-trace" d="M54 184l20-15 18 9 20-37 19 15 19-25 18 29 20-12 17 18 20-39 19 10 18-19 20 32 19-16 18 21 18-38" fill="none" stroke="#6db8f0" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round" opacity=".75" />
          <g fill="none" strokeWidth="2" strokeLinecap="round">
            <g className="candle" stroke="#43d17d"><path d="M77 142v34" /><rect x="72" y="151" width="10" height="16" rx="1" fill="#43d17d" /></g>
            <g className="candle" stroke="#e18181"><path d="M98 126v39" /><rect x="93" y="136" width="10" height="18" rx="1" fill="#e18181" /></g>
            <g className="candle" stroke="#43d17d"><path d="M119 137v34" /><rect x="114" y="145" width="10" height="16" rx="1" fill="#43d17d" /></g>
            <g className="candle" stroke="#43d17d"><path d="M140 111v48" /><rect x="135" y="122" width="10" height="25" rx="1" fill="#43d17d" /></g>
            <g className="candle" stroke="#e18181"><path d="M161 101v43" /><rect x="156" y="112" width="10" height="19" rx="1" fill="#e18181" /></g>
            <g className="candle" stroke="#43d17d"><path d="M182 119v43" /><rect x="177" y="127" width="10" height="20" rx="1" fill="#43d17d" /></g>
            <g className="candle" stroke="#e18181"><path d="M203 109v47" /><rect x="198" y="119" width="10" height="22" rx="1" fill="#e18181" /></g>
            <g className="candle" stroke="#43d17d"><path d="M224 131v40" /><rect x="219" y="138" width="10" height="19" rx="1" fill="#43d17d" /></g>
            <g className="candle" stroke="#e18181"><path d="M245 98v45" /><rect x="240" y="108" width="10" height="20" rx="1" fill="#e18181" /></g>
            <g className="candle" stroke="#e18181"><path d="M266 105v38" /><rect x="261" y="113" width="10" height="19" rx="1" fill="#e18181" /></g>
            <g className="candle" stroke="#e18181"><path d="M287 122v46" /><rect x="282" y="130" width="10" height="23" rx="1" fill="#e18181" /></g>
          </g>
          <g className="chart-price">
            <path d="M300 171h74" stroke="#f2c438" strokeWidth="1" strokeDasharray="3 3" />
            <rect x="339" y="163" width="40" height="16" rx="4" fill="#3b3212" stroke="#a88a1f" />
            <text x="345" y="174" fill="#f2c438" fontSize="8" fontWeight="700" fontFamily="system-ui, sans-serif">2347.8</text>
          </g>
          <g className="speech-bubble">
            <path d="M262 70q0-7 8-7h101q8 0 8 8v25q0 8-8 8h-34l-11 11 2-11h-58q-8 0-8-8z" fill="#e8f0f5" stroke="#6db8f0" strokeWidth="1.5" />
            <text x="276" y="83" fill="#132432" fontSize="8" fontWeight="800" fontFamily="system-ui, sans-serif">BUY THE DIP?</text>
            <text x="278" y="94" fill="#526573" fontSize="7" fontFamily="system-ui, sans-serif">(katanya sih...)</text>
          </g>
          <ellipse className="trader-shadow" cx="286" cy="298" rx="67" ry="10" fill="#000" opacity=".3" />
          <g className="trader">
            <path d="M240 271q4-35 37-43h29q31 8 35 43v18h-101z" fill="url(#nf-body)" stroke="#1f4f7a" strokeWidth="3" />
            <path d="M266 234q20 24 42 0" fill="none" stroke="#e8f0f5" strokeWidth="3" opacity=".65" />
            <path d="M279 255h14" stroke="#f2c438" strokeWidth="3" strokeLinecap="round" />
            <path d="M286 142v-10" stroke="#16466e" strokeWidth="3" strokeLinecap="round" />
            <circle cx="286" cy="128" r="5" fill="#f2c438" stroke="#16466e" strokeWidth="2" />
            <path d="M247 174h-9m96 0h-9" stroke="#16466e" strokeWidth="3.5" strokeLinecap="round" />
            <circle cx="237" cy="174" r="4" fill="#f2c438" stroke="#16466e" strokeWidth="2" />
            <circle cx="335" cy="174" r="4" fill="#f2c438" stroke="#16466e" strokeWidth="2" />
            <rect x="271" y="210" width="30" height="22" rx="5" fill="#cfe3f5" stroke="#16466e" strokeWidth="3" />
            <rect x="246" y="140" width="80" height="74" rx="26" fill="url(#nf-bot)" stroke="#16466e" strokeWidth="3" />
            <rect x="256" y="152" width="60" height="42" rx="17" fill="#0f2f4d" />
            <ellipse cx="272" cy="170" rx="4" ry="5.5" fill="#7cc4f5" />
            <ellipse cx="300" cy="170" rx="4" ry="5.5" fill="#7cc4f5" />
            <path d="M273 183q6-5 12 0" fill="none" stroke="#7cc4f5" strokeWidth="2.5" strokeLinecap="round" transform="translate(7 1)" />
            <ellipse cx="262" cy="188" rx="4.5" ry="2.5" fill="#9cc9ee" opacity=".7" />
            <ellipse cx="310" cy="188" rx="4.5" ry="2.5" fill="#9cc9ee" opacity=".7" />
            <path className="sweat-drop" d="M330 150q-7 9 0 12 7-3 0-12z" fill="#7cc4f5" />
            <path d="M239 257q-12 2-16-8" fill="none" stroke="#1f4f7a" strokeWidth="9" strokeLinecap="round" />
            <path d="M329 251q13-1 18 9" fill="none" stroke="#1f4f7a" strokeWidth="9" strokeLinecap="round" />
            <path d="M342 252h22v22q0 7-7 7h-8q-7 0-7-7z" fill="#eef4f9" stroke="#8aa4b8" strokeWidth="2" />
            <path d="M364 257q13-1 9 9-2 5-9 4" fill="none" stroke="#bcd0df" strokeWidth="4" />
            <path className="coffee-steam" d="M349 247q-5-6 0-11t0-10m9 21q5-6 0-11t0-10" fill="none" stroke="#a3b6c3" strokeWidth="2" strokeLinecap="round" />
          </g>
        </svg>
        </section>
      </main>
    </SiteFrame>
  );
}
