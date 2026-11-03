"use client";

import { useCallback, useEffect, useRef, useState } from "react";

const STORAGE_KEY = "algentra-music";
const MUSIC_SRC = "/musik.mp3";

/**
 * Tombol musik latar — port dari #site-music + #music-toggle di base.html.
 * Autoplay /musik.mp3 (loop) kecuali preferensi tersimpan "algentra-music" === "off".
 * Klik: play/pause + simpan preferensi. Bila berkas 404, toggle disembunyikan.
 */
export function MusicToggle({ alone = false }: { alone?: boolean }) {
  const audioRef = useRef<HTMLAudioElement>(null);
  const [available, setAvailable] = useState(true);
  const [playing, setPlaying] = useState(false);
  const [labelOverride, setLabelOverride] = useState<string | null>(null);
  const [titleOverride, setTitleOverride] = useState<string | null>(null);

  // Sinkronkan state dengan event play/pause/error dari <audio>.
  useEffect(() => {
    const audio = audioRef.current;
    if (!audio) return;
    const onPlay = () => {
      setPlaying(true);
      setLabelOverride(null);
      setTitleOverride(null);
    };
    const onPause = () => setPlaying(false);
    // Berkas musik 404 / gagal dimuat: sembunyikan toggle (menggantikan HEAD request terpisah).
    const onError = () => setAvailable(false);
    audio.addEventListener("play", onPlay);
    audio.addEventListener("pause", onPause);
    audio.addEventListener("error", onError);
    return () => {
      audio.removeEventListener("play", onPlay);
      audio.removeEventListener("pause", onPause);
      audio.removeEventListener("error", onError);
    };
  }, []);

  // Autoplay saat mount, kecuali pengguna mematikannya.
  useEffect(() => {
    const audio = audioRef.current;
    if (!audio) return;
    let preference: string | null = null;
    try {
      preference = localStorage.getItem(STORAGE_KEY);
    } catch {
      /* Autoplay remains available. */
    }
    if (preference !== "off") {
      audio.play().catch(() => {
        setPlaying(false);
        setTitleOverride("Autoplay diblokir browser. Klik untuk memutar musik.");
      });
    }
  }, []);

  const toggle = useCallback(async () => {
    const audio = audioRef.current;
    if (!audio) return;
    const savePreference = (value: string) => {
      try {
        localStorage.setItem(STORAGE_KEY, value);
      } catch {
        /* Storage may be disabled. */
      }
    };
    if (!audio.paused) {
      audio.pause();
      savePreference("off");
      return;
    }
    savePreference("on");
    try {
      await audio.play();
    } catch {
      setLabelOverride("Coba putar lagi");
    }
  }, []);

  if (!available) return null;

  const label = labelOverride ?? (playing ? "Musik aktif" : "Putar musik");
  const ariaLabel = playing ? "Matikan musik latar" : "Putar musik latar";

  return (
    <>
      <audio ref={audioRef} id="site-music" preload="none" loop>
        <source src={MUSIC_SRC} type="audio/mpeg" />
      </audio>
      <button
        className={alone ? "music-toggle music-toggle--alone" : "music-toggle"}
        id="music-toggle"
        type="button"
        aria-pressed={playing}
        aria-label={ariaLabel}
        title={titleOverride ?? ariaLabel}
        onClick={() => void toggle()}
      >
        <svg
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth="1.7"
          strokeLinecap="round"
          strokeLinejoin="round"
          aria-hidden="true"
        >
          <path d="M4 10v4h4l5 4V6l-5 4H4z"></path>
          <path d="M17 9a5 5 0 0 1 0 6"></path>
          <path d="M19.5 6.5a9 9 0 0 1 0 11"></path>
        </svg>
        <span>{label}</span>
        <span className="music-bars" aria-hidden="true">
          <i></i>
          <i></i>
          <i></i>
        </span>
      </button>
    </>
  );
}
