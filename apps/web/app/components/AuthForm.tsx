"use client";

import { FormEvent, useRef, useState } from "react";

export type AuthFormMode = "client" | "admin" | "register" | "forgot";

interface CountdownButton extends HTMLButtonElement {
  _resendTimer?: ReturnType<typeof setInterval> | null;
}

interface PostError extends Error {
  retryAfter?: number;
}

function readCsrfToken(): string | null {
  try {
    return sessionStorage.getItem("algentra-csrf");
  } catch {
    return null;
  }
}

function saveCsrfToken(result: Record<string, unknown>): void {
  if (typeof result.csrf_token === "string") {
    try {
      sessionStorage.setItem("algentra-csrf", result.csrf_token);
    } catch {
      /* abaikan: penyimpanan tidak tersedia */
    }
  }
}

async function post(
  path: string,
  body: Record<string, unknown>,
  fallback = "Permintaan gagal. Coba lagi."
): Promise<Record<string, unknown>> {
  const active = typeof document !== "undefined" ? document.activeElement : null;
  const actionButton = active instanceof HTMLButtonElement
    ? active
    : active instanceof HTMLElement
      ? active.closest("form")?.querySelector<HTMLButtonElement>('button[type="submit"]') ?? null
      : null;
  const ownsLoading = Boolean(actionButton && !actionButton.hasAttribute("aria-busy"));
  if (ownsLoading && actionButton) {
    actionButton.dataset.autoLoading = "true";
    actionButton.setAttribute("aria-busy", "true");
  }
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  const token = readCsrfToken();
  if (token) headers["X-CSRF-Token"] = token;
  try {
    const response = await fetch(`/api/${path}`, {
      method: "POST",
      headers,
      body: JSON.stringify(body),
      cache: "no-store"
    });
    const result = (await response.json().catch(() => ({}))) as Record<string, unknown>;
    if (!response.ok) {
      const detail = result.detail;
      const error = new Error(
        typeof detail === "string" && detail.trim() ? detail.trim() : fallback
      ) as PostError;
      error.retryAfter =
        Number(response.headers.get("Retry-After")) ||
        Number(result.retry_after_seconds) ||
        0;
      throw error;
    }
    return result;
  } finally {
    if (ownsLoading && actionButton) {
      delete actionButton.dataset.autoLoading;
      actionButton.removeAttribute("aria-busy");
    }
  }
}

// Replika startResendCountdown dari base.html (verbatim perilaku):
// baca detik dari Retry-After / retry_after_seconds, disable tombol, tampilkan hitung mundur.
function startResendCountdown(
  button: CountdownButton | null,
  seconds: unknown,
  label = "Kirim ulang kode"
): void {
  if (!button) return;
  if (button._resendTimer) clearInterval(button._resendTimer);
  let remaining = Math.max(0, Math.ceil(Number(seconds) || 0));
  const update = () => {
    if (remaining <= 0) {
      button.disabled = false;
      button.textContent = label;
      if (button._resendTimer) clearInterval(button._resendTimer);
      button._resendTimer = null;
      return;
    }
    const hours = Math.floor(remaining / 3600);
    const minutes = Math.floor((remaining % 3600) / 60);
    const secondsLeft = remaining % 60;
    const time = hours
      ? `${String(hours).padStart(2, "0")}:${String(minutes).padStart(2, "0")}:${String(secondsLeft).padStart(2, "0")}`
      : `${String(minutes).padStart(2, "0")}:${String(secondsLeft).padStart(2, "0")}`;
    button.disabled = true;
    button.textContent = `Kirim ulang tersedia dalam ${time}`;
    remaining -= 1;
  };
  update();
  if (remaining > 0) button._resendTimer = setInterval(update, 1000);
}

export function AuthForm({ mode }: { mode: AuthFormMode }) {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [rememberMe, setRememberMe] = useState(false);
  const [pendingEmail, setPendingEmail] = useState("");
  const [pendingPassword, setPendingPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [registerStep, setRegisterStep] = useState<"register" | "verify">("register");
  const [forgotStep, setForgotStep] = useState<"request" | "confirm" | "success">("request");
  const [forgotEmail, setForgotEmail] = useState("");
  const [clientMessage, setClientMessage] = useState("");
  const [registerMessage, setRegisterMessage] = useState("");
  const [registerMessageState, setRegisterMessageState] = useState<"success" | "error" | "">("");
  const [resetMessage, setResetMessage] = useState("");
  const [adminMessage, setAdminMessage] = useState("");
  const registerSubmitRef = useRef<CountdownButton | null>(null);
  const resendRef = useRef<CountdownButton | null>(null);
  const resetRequestSubmitRef = useRef<CountdownButton | null>(null);
  const resetResendRef = useRef<CountdownButton | null>(null);

  // ---------- client login ----------
  async function handleClientSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setClientMessage("");
    try {
      const result = await post(
        "auth/client-login",
        { email: email.trim(), password, remember_me: rememberMe },
        "Gagal masuk. Periksa kembali email dan kata sandi Anda."
      );
      saveCsrfToken(result);
      window.location.assign("/account");
    } catch (error) {
      setClientMessage(error instanceof Error ? error.message : "Gagal masuk.");
    }
  }

  // ---------- register ----------
  async function handleRegisterSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const submitButton = registerSubmitRef.current;
    const nextEmail = email.trim();
    setPendingEmail(nextEmail);
    setPendingPassword(password);
    setRegisterMessage("");
    setRegisterMessageState("");
    if (submitButton) { submitButton.disabled = true; submitButton.setAttribute("aria-busy", "true"); }
    try {
      const result = await post("auth/register", { email: nextEmail, password });
      setRegisterStep("verify");
      startResendCountdown(resendRef.current, result.retry_after_seconds, "Kirim ulang kode");
      setRegisterMessageState("success");
      setRegisterMessage(
        "Succes: Check your inbox. if you can't find the message, check your spam folder."
      );
    } catch (error) {
      const err = error as PostError;
      setRegisterMessageState("error");
      setRegisterMessage(`Failled: ${err.message}`);
      if (err.retryAfter) {
        const target = registerStep === "verify" ? resendRef.current : submitButton;
        startResendCountdown(
          target,
          err.retryAfter,
          registerStep === "verify" ? "Kirim ulang kode" : "Kirim kode"
        );
      }
    } finally {
      if (submitButton) {
        submitButton.removeAttribute("aria-busy");
        if (!submitButton._resendTimer) submitButton.disabled = false;
      }
    }
  }

  async function handleVerifySubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setRegisterMessage("");
    setRegisterMessageState("");
    try {
      const result = await post("auth/verify-email", { email: pendingEmail, code });
      saveCsrfToken(result);
      window.location.assign("/account");
    } catch (error) {
      const err = error as PostError;
      setRegisterMessageState("error");
      setRegisterMessage(`Failled: ${err.message}`);
      if (err.retryAfter) startResendCountdown(resendRef.current, err.retryAfter, "Kirim ulang kode");
    }
  }

  async function handleResendClick() {
    const resendButton = resendRef.current;
    if (!resendButton || resendButton.disabled) return;
    resendButton.disabled = true;
    resendButton.setAttribute("aria-busy", "true");
    try {
      const result = await post("auth/resend-code", {
        email: pendingEmail,
        password: pendingPassword
      });
      startResendCountdown(resendButton, result.retry_after_seconds, "Kirim ulang kode");
      setRegisterMessageState("success");
      setRegisterMessage(
        "Succes: Check your inbox. If you can't find the message, check your spam folder."
      );
    } catch (error) {
      const err = error as PostError;
      setRegisterMessageState("error");
      setRegisterMessage(`Failled: ${err.message}`);
      if (err.retryAfter) startResendCountdown(resendButton, err.retryAfter, "Kirim ulang kode");
      else resendButton.disabled = false;
    } finally {
      resendButton.removeAttribute("aria-busy");
    }
  }

  // ---------- forgot password ----------
  async function requestResetCode(forEmail: string) {
    const resendButton = resetResendRef.current;
    if (resendButton) resendButton.disabled = true;
    try {
      const result = await post("auth/password-reset/request", { email: forEmail });
      setForgotStep("confirm");
      startResendCountdown(resendButton, result.retry_after_seconds, "Kirim ulang kode");
      setResetMessage(
        "Jika email terdaftar, kode reset sudah dikirim. Periksa kotak masuk dan folder spam."
      );
    } catch (error) {
      const err = error as PostError;
      setResetMessage(err.message);
      if (err.retryAfter) {
        const target = forgotStep === "confirm" ? resendButton : resetRequestSubmitRef.current;
        startResendCountdown(
          target,
          err.retryAfter,
          forgotStep === "confirm" ? "Kirim ulang kode" : "Kirim kode reset"
        );
      } else if (resendButton) {
        resendButton.disabled = false;
      }
    }
  }

  async function handleResetRequestSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const submitButton = resetRequestSubmitRef.current;
    const nextEmail = email.trim();
    setForgotEmail(nextEmail);
    setResetMessage("");
    if (submitButton) { submitButton.disabled = true; submitButton.setAttribute("aria-busy", "true"); }
    await requestResetCode(nextEmail);
    if (submitButton) {
      submitButton.removeAttribute("aria-busy");
      if (!submitButton._resendTimer) submitButton.disabled = false;
    }
  }

  async function handleResetConfirmSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (newPassword !== confirmPassword) {
      setResetMessage("Konfirmasi kata sandi tidak sama.");
      return;
    }
    const button = (event.currentTarget.querySelector('button[type="submit"]') as HTMLButtonElement) ?? null;
    if (button) { button.disabled = true; button.setAttribute("aria-busy", "true"); }
    try {
      await post("auth/password-reset/confirm", {
        email: forgotEmail,
        code,
        password: newPassword
      });
      setForgotStep("success");
      setResetMessage("Kata sandi berhasil direset.");
    } catch (error) {
      const err = error as PostError;
      setResetMessage(err.message);
      if (err.retryAfter) startResendCountdown(resetResendRef.current, err.retryAfter, "Kirim ulang kode");
    } finally {
      if (button) { button.disabled = false; button.removeAttribute("aria-busy"); }
    }
  }

  function handleChangeEmail() {
    setForgotStep("request");
    setResetMessage("");
  }

  // ---------- admin login ----------
  async function handleAdminSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setAdminMessage("");
    try {
      const result = await post(
        "auth/login",
        { username: email, password },
        "Login gagal."
      );
      saveCsrfToken(result);
      window.location.assign("/dashboard");
    } catch (error) {
      setAdminMessage(error instanceof Error ? error.message : "Login gagal.");
    }
  }

  return (
    <main id="main" tabIndex={-1} className="page-shell dashboard-page login-shell">
      <section className="login-card">
        {mode === "client" && (
          <>
            <span className="eyebrow">PORTAL KLIEN / MASUK</span>
            <h1>Masuk ke akun</h1>
            <p className="page-description">Kelola akun Copy Trading MT5 dan pantau koneksinya.</p>
            <form id="client-login-form" onSubmit={handleClientSubmit}>
              <label htmlFor="email">Email</label>
              <input
                id="email"
                type="email"
                autoComplete="email"
                required
                value={email}
                onChange={(event) => setEmail(event.target.value)}
              />
              <label htmlFor="password">Kata sandi</label>
              <input
                id="password"
                type="password"
                autoComplete="current-password"
                required
                value={password}
                onChange={(event) => setPassword(event.target.value)}
              />
              <div className="toolbar">
                <label>
                  <input
                    id="remember-me"
                    type="checkbox"
                    checked={rememberMe}
                    onChange={(event) => setRememberMe(event.target.checked)}
                  />{" "}
                  Biarkan saya tetap masuk selama 30 hari
                </label>
              </div>
              <button type="submit">Masuk</button>
              <p id="client-message" className="status" role="status">
                {clientMessage}
              </p>
            </form>
            <p className="muted">
              <a href="/forgot-password">Lupa kata sandi?</a>
            </p>
            <p className="muted">
              Belum punya akun? <a href="/register">Buat akun dengan verifikasi email</a>.
            </p>
          </>
        )}

        {mode === "register" && (
          <>
            <span className="eyebrow">REGISTRASI KLIEN / VERIFIKASI EMAIL</span>
            <h1>Buat akun klien</h1>
            <p className="page-description">
              Buat akun untuk menghubungkan akun Copy Trading MT5. Kode verifikasi akan dikirim ke
              email Anda.
            </p>
            <form id="register-form" hidden={registerStep !== "register"} onSubmit={handleRegisterSubmit}>
              <label htmlFor="email">Email</label>
              <input
                id="email"
                type="email"
                autoComplete="email"
                required
                value={email}
                onChange={(event) => setEmail(event.target.value)}
              />
              <label htmlFor="password">
                Kata sandi
                <small className="field-help">Minimal 8 karakter.</small>
              </label>
              <input
                id="password"
                type="password"
                autoComplete="new-password"
                minLength={8}
                required
                value={password}
                onChange={(event) => setPassword(event.target.value)}
              />
              <button type="submit" ref={registerSubmitRef}>
                Kirim kode
              </button>
            </form>
            <form id="verify-form" hidden={registerStep !== "verify"} onSubmit={handleVerifySubmit}>
              <label htmlFor="code">
                Kode verifikasi
                <small className="field-help">
                  Masukkan enam digit yang dikirim ke email. Kode berlaku 10 menit.
                </small>
              </label>
              <input
                id="code"
                inputMode="numeric"
                autoComplete="one-time-code"
                pattern="[0-9]{6}"
                maxLength={6}
                required
                value={code}
                onChange={(event) => setCode(event.target.value)}
              />
              <button type="submit">Verifikasi email</button>
              <button id="resend" className="secondary" type="button" ref={resendRef} onClick={handleResendClick}>
                Kirim ulang kode
              </button>
            </form>
            <p id="message" className="status" data-state={registerMessageState} role="status" aria-live="polite">
              {registerMessage}
            </p>
            <p className="muted">
              Sudah punya akun? <a href="/login">Masuk ke portal klien</a>.
            </p>
          </>
        )}

        {mode === "forgot" && (
          <>
            <span className="eyebrow">PORTAL KLIEN / RESET KATA SANDI</span>
            <h1>Lupa kata sandi?</h1>
            <p className="page-description">
              Kami akan mengirim kode enam digit ke email akun Anda. Kode berlaku 10 menit.
            </p>
            <form
              id="reset-request-form"
              hidden={forgotStep !== "request"}
              onSubmit={handleResetRequestSubmit}
            >
              <label htmlFor="reset-email">Email akun</label>
              <input
                id="reset-email"
                type="email"
                autoComplete="email"
                required
                value={email}
                onChange={(event) => setEmail(event.target.value)}
              />
              <button type="submit" ref={resetRequestSubmitRef}>
                Kirim kode reset
              </button>
            </form>
            <form
              id="reset-confirm-form"
              hidden={forgotStep !== "confirm"}
              onSubmit={handleResetConfirmSubmit}
            >
              <label htmlFor="reset-code">Kode email</label>
              <input
                id="reset-code"
                inputMode="numeric"
                autoComplete="one-time-code"
                pattern="[0-9]{6}"
                maxLength={6}
                required
                value={code}
                onChange={(event) => setCode(event.target.value)}
              />
              <label htmlFor="new-password">
                Kata sandi baru
                <small className="field-help">Minimal 8 karakter.</small>
              </label>
              <input
                id="new-password"
                type="password"
                autoComplete="new-password"
                minLength={8}
                required
                value={newPassword}
                onChange={(event) => setNewPassword(event.target.value)}
              />
              <label htmlFor="confirm-password">Ulangi kata sandi baru</label>
              <input
                id="confirm-password"
                type="password"
                autoComplete="new-password"
                minLength={8}
                required
                value={confirmPassword}
                onChange={(event) => setConfirmPassword(event.target.value)}
              />
              <button type="submit">Simpan kata sandi baru</button>
              <button
                id="reset-resend"
                className="secondary"
                type="button"
                ref={resetResendRef}
                onClick={() => requestResetCode(forgotEmail)}
              >
                Kirim ulang kode
              </button>
            </form>
            <button
              id="change-reset-email"
              className="secondary"
              type="button"
              hidden={forgotStep !== "confirm"}
              onClick={handleChangeEmail}
            >
              Gunakan email lain
            </button>
            <p id="reset-message" className="status" role="status">
              {resetMessage}
            </p>
            <p id="reset-success" className="muted" hidden={forgotStep !== "success"}>
              <a href="/login">Masuk dengan kata sandi baru</a>
            </p>
            <p className="muted">
              <a href="/login">Kembali ke halaman masuk</a>
            </p>
          </>
        )}

        {mode === "admin" && (
          <>
            <span className="eyebrow">ALGENTRA CAPITAL / PRIVATE ACCESS</span>
            <h1>Admin Login</h1>
            <p className="page-description">Masuk ke workspace operasional.</p>
            <form id="admin-login-form" onSubmit={handleAdminSubmit}>
              <label htmlFor="username">Username</label>
              <input
                id="username"
                autoComplete="username"
                required
                value={email}
                onChange={(event) => setEmail(event.target.value)}
              />
              <label htmlFor="password">Password</label>
              <input
                id="password"
                type="password"
                autoComplete="current-password"
                required
                value={password}
                onChange={(event) => setPassword(event.target.value)}
              />
              <button type="submit">Login</button>
              <p id="admin-message" className="status" role="status">
                {adminMessage}
              </p>
            </form>
          </>
        )}
      </section>
    </main>
  );
}
