package main

import (
	"archive/zip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/rnpproject/algentra-backend/internal/database"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const telegramLimit = 49 * 1024 * 1024

func main() {
	if len(os.Args) == 3 && os.Args[1] == "-kill-switch" {
		if err := setKillSwitch(os.Args[2]); err != nil {
			fmt.Fprintln(os.Stderr, "kill switch gagal:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "backup gagal:", err)
		os.Exit(1)
	}
}

func setKillSwitch(value string) error {
	if value != "on" && value != "off" {
		return errors.New("gunakan -kill-switch on atau off")
	}
	databaseFile := resolveDatabaseFile()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, err := database.Open(ctx, databaseFile)
	if err != nil {
		return err
	}
	defer db.Close()
	if value == "on" {
		var active int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM signals WHERE status='CLAIMED' AND claimed_at >= $1`, time.Now().UTC().Add(-90*time.Second)).Scan(&active); err != nil {
			return err
		}
		if active > 0 {
			return errors.New("masih ada sinyal dalam proses; tunggu selesai sebelum update")
		}
	}
	state := "false"
	if value == "on" {
		state = "true"
	}
	_, err = db.Exec(ctx, `INSERT INTO settings(key,value) VALUES('kill_switch',$1) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, state)
	return err
}
func run() error {
	root := os.Getenv("PROJECT_ROOT")
	if root == "" {
		root, _ = os.Getwd()
	}
	dbFile := resolveDatabaseFile()
	token, chat := os.Getenv("TELEGRAM_BOT_TOKEN"), os.Getenv("TELEGRAM_ADMIN_CHAT_ID")
	if token == "" || chat == "" {
		return errors.New("backup Telegram membutuhkan TELEGRAM_BOT_TOKEN dan TELEGRAM_ADMIN_CHAT_ID")
	}
	secret := os.Getenv("APP_SECRET_KEY")
	if len(secret) < 32 {
		return errors.New("APP_SECRET_KEY wajib tersedia untuk mengenkripsi backup")
	}
	tmp := filepath.Join(root, "tmp")
	if err := os.MkdirAll(tmp, 0700); err != nil {
		return err
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	snapshot := filepath.Join(tmp, "database-"+stamp+".sqlite")
	archive := filepath.Join(tmp, "database-"+stamp+".zip")
	encrypted := filepath.Join(tmp, "database-"+stamp+".zip.enc")
	defer func() {
		for _, p := range []string{snapshot, archive, encrypted} {
			_ = os.Remove(p)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if info, err := os.Stat(dbFile); err != nil || !info.Mode().IsRegular() {
		return errors.New("file database belum ada; backup dibatalkan")
	}
	db, err := database.Open(ctx, dbFile)
	if err != nil {
		return err
	}
	defer db.Close()
	escaped := strings.ReplaceAll(snapshot, "'", "''")
	if _, err = db.Exec(ctx, "VACUUM INTO '"+escaped+"'"); err != nil {
		return fmt.Errorf("snapshot SQLite: %w", err)
	}
	if err = zipSnapshot(snapshot, archive); err != nil {
		return err
	}
	_, err = encryptFile(archive, encrypted, secret)
	if err != nil {
		return err
	}
	digest, hashErr := fileSHA256(encrypted)
	if hashErr != nil {
		return hashErr
	}
	info, err := os.Stat(encrypted)
	if err != nil {
		return err
	}
	caption := fmt.Sprintf("Algentra SQLite backup %s\nSize: %d bytes\nSHA-256: %s", filepath.Base(encrypted), info.Size(), digest)
	if info.Size() > telegramLimit {
		return sendMessage(ctx, token, chat, caption+"\nBackup melebihi batas unggah Telegram; file sementara dibersihkan.")
	}
	return sendDocument(ctx, token, chat, encrypted, caption)
}

func resolveDatabaseFile() string {
	if value := strings.TrimSpace(os.Getenv("DATABASE_FILE")); value != "" {
		return value
	}
	root := strings.TrimSpace(os.Getenv("PROJECT_ROOT"))
	if root == "" {
		root, _ = os.Getwd()
	}
	return filepath.Join(root, "data", "database.sqlite")
}
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func zipSnapshot(input, output string) error {
	f, err := os.Create(output)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(f)
	src, err := os.Open(input)
	if err != nil {
		f.Close()
		return err
	}
	entry, err := zw.Create(filepath.Base(input))
	if err == nil {
		_, err = io.Copy(entry, src)
	}
	src.Close()
	zerr := zw.Close()
	cerr := f.Close()
	if err != nil {
		return err
	}
	if zerr != nil {
		return zerr
	}
	return cerr
}
func encryptFile(input, output, secret string) (string, error) {
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	src, err := os.Open(input)
	if err != nil {
		return "", err
	}
	defer src.Close()
	dst, err := os.OpenFile(output, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	defer dst.Close()
	prefix := make([]byte, 8)
	if _, err = rand.Read(prefix); err != nil {
		return "", err
	}
	if _, err = dst.Write(append([]byte("ALGENTRA1"), prefix...)); err != nil {
		return "", err
	}
	hash := sha256.New()
	buf := make([]byte, 1024*1024)
	var counter uint32
	for {
		n, e := io.ReadFull(src, buf)
		if e == io.EOF {
			break
		}
		if e != nil && e != io.ErrUnexpectedEOF {
			return "", e
		}
		nonce := make([]byte, 12)
		copy(nonce, prefix)
		nonce[8] = byte(counter >> 24)
		nonce[9] = byte(counter >> 16)
		nonce[10] = byte(counter >> 8)
		nonce[11] = byte(counter)
		counter++
		ciphertext := aead.Seal(nil, nonce, buf[:n], nil)
		size := uint32(len(ciphertext))
		head := []byte{byte(size >> 24), byte(size >> 16), byte(size >> 8), byte(size)}
		if _, err = dst.Write(head); err != nil {
			return "", err
		}
		if _, err = dst.Write(ciphertext); err != nil {
			return "", err
		}
		hash.Write(ciphertext)
		if e == io.ErrUnexpectedEOF {
			break
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
func sendDocument(ctx context.Context, token, chat, path, caption string) error {
	return telegramMultipart(ctx, token, "sendDocument", func(w *multipart.Writer) error {
		if err := w.WriteField("chat_id", chat); err != nil {
			return err
		}
		if err := w.WriteField("caption", caption); err != nil {
			return err
		}
		part, err := w.CreateFormFile("document", filepath.Base(path))
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(part, f)
		return err
	})
}
func sendMessage(ctx context.Context, token, chat, message string) error {
	return telegramMultipart(ctx, token, "sendMessage", func(w *multipart.Writer) error {
		if err := w.WriteField("chat_id", chat); err != nil {
			return err
		}
		return w.WriteField("text", message)
	})
}
func telegramMultipart(ctx context.Context, token, method string, fill func(*multipart.Writer) error) error {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		err := fill(mw)
		if closeErr := mw.Close(); err == nil {
			err = closeErr
		}
		_ = pw.CloseWithError(err)
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+token+"/"+method, pr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Telegram upload returned HTTP %d", resp.StatusCode)
	}
	return nil
}
