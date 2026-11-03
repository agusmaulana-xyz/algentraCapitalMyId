package database

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaFS embed.FS

type Pool struct {
	writer *sql.DB
	reader *sql.DB
}
type CommandTag struct{ affected int64 }

func (c CommandTag) RowsAffected() int64 { return c.affected }

type Tx struct{ tx *sql.Tx }
type Rows struct{ rows *sql.Rows }
type Row struct{ row *sql.Row }

func Open(ctx context.Context, filename string) (*Pool, error) {
	if strings.TrimSpace(filename) == "" {
		return nil, errors.New("DATABASE_FILE wajib diisi")
	}
	if err := ensureSchema(ctx, filename); err != nil {
		return nil, err
	}
	dsn, err := sqliteDSN(filename)
	if err != nil {
		return nil, err
	}
	writer, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("buka SQLite: %w", err)
	}
	writer.SetMaxOpenConns(1)
	reader, err := sql.Open("sqlite", dsn)
	if err != nil {
		writer.Close()
		return nil, fmt.Errorf("buka pool baca SQLite: %w", err)
	}
	reader.SetMaxOpenConns(4)
	reader.SetMaxIdleConns(4)
	if err := writer.PingContext(ctx); err != nil {
		writer.Close()
		reader.Close()
		return nil, fmt.Errorf("ping SQLite: %w", err)
	}
	if err := reader.PingContext(ctx); err != nil {
		writer.Close()
		reader.Close()
		return nil, fmt.Errorf("ping pool baca SQLite: %w", err)
	}
	return &Pool{writer: writer, reader: reader}, nil
}

// Separate schema check uses the embedded schema and the same SQLite PRAGMAs.
func ensureSchema(ctx context.Context, filename string) error {
	dsn, err := sqliteDSN(filename)
	if err != nil {
		return err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		schema, err := schemaFS.ReadFile("schema.sql")
		if err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, string(schema)); err != nil {
			return fmt.Errorf("inisialisasi schema SQLite: %w", err)
		}
	}
	for _, migration := range []struct{ table, column, definition string }{
		{"payment_orders", "qris_total_idr", "INTEGER NOT NULL DEFAULT 0"},
		{"payment_orders", "rental_days", "INTEGER NOT NULL DEFAULT 10"},
		{"mt5_accounts", "rental_expires_at", "TEXT"},
		{"mt5_accounts", "ea_mode", "VARCHAR(16) NOT NULL DEFAULT 'SERVER'"},
		{"mt5_accounts", "custom_ea_name", "VARCHAR(128)"},
		{"mt5_accounts", "custom_ea_data", "BLOB"},
		{"mt5_accounts", "custom_ea_updated_at", "TEXT"},
		{"mt5_accounts", "custom_ea_inputs", "TEXT"},
		{"mt5_accounts", "custom_ea_params", "TEXT"},
		{"mt5_accounts", "custom_ea_paused", "INTEGER NOT NULL DEFAULT 0"},
	} {
		query := `ALTER TABLE ` + migration.table + ` ADD COLUMN ` + migration.column + ` ` + migration.definition
		if _, err := db.ExecContext(ctx, query); err != nil && !strings.Contains(strings.ToLower(err.Error()), "duplicate column name") {
			return fmt.Errorf("migrasi kolom %s.%s: %w", migration.table, migration.column, err)
		}
	}
	return nil
}

func sqliteDSN(filename string) (string, error) {
	absolute, err := filepath.Abs(filename)
	if err != nil {
		return "", fmt.Errorf("jalur DATABASE_FILE tidak valid: %w", err)
	}
	path := filepath.ToSlash(absolute)
	if filepath.VolumeName(absolute) != "" {
		path = "/" + path
	}
	uri := (&url.URL{Scheme: "file", Path: path}).String()
	return uri + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)", nil
}
func (p *Pool) Close() {
	if p != nil {
		if p.writer != nil {
			_ = p.writer.Close()
		}
		if p.reader != nil {
			_ = p.reader.Close()
		}
	}
}
func (p *Pool) Ping(ctx context.Context) error {
	if err := p.writer.PingContext(ctx); err != nil {
		return err
	}
	return p.reader.PingContext(ctx)
}
func (p *Pool) Exec(ctx context.Context, q string, args ...any) (CommandTag, error) {
	q, args = sqliteQuery(q, args)
	r, err := p.writer.ExecContext(ctx, q, args...)
	if err != nil {
		return CommandTag{}, err
	}
	n, err := r.RowsAffected()
	return CommandTag{n}, err
}
func (p *Pool) Query(ctx context.Context, q string, args ...any) (*Rows, error) {
	q, args = sqliteQuery(q, args)
	r, e := p.reader.QueryContext(ctx, q, args...)
	return &Rows{r}, e
}
func (p *Pool) QueryRow(ctx context.Context, q string, args ...any) *Row {
	q, args = sqliteQuery(q, args)
	return &Row{p.reader.QueryRowContext(ctx, q, args...)}
}
func (p *Pool) Begin(ctx context.Context) (*Tx, error) {
	t, e := p.writer.BeginTx(ctx, nil)
	return &Tx{t}, e
}
func (t *Tx) Exec(ctx context.Context, q string, args ...any) (CommandTag, error) {
	q, args = sqliteQuery(q, args)
	r, err := t.tx.ExecContext(ctx, q, args...)
	if err != nil {
		return CommandTag{}, err
	}
	n, err := r.RowsAffected()
	return CommandTag{n}, err
}
func (t *Tx) Query(ctx context.Context, q string, args ...any) (*Rows, error) {
	q, args = sqliteQuery(q, args)
	r, e := t.tx.QueryContext(ctx, q, args...)
	return &Rows{r}, e
}
func (t *Tx) QueryRow(ctx context.Context, q string, args ...any) *Row {
	q, args = sqliteQuery(q, args)
	return &Row{t.tx.QueryRowContext(ctx, q, args...)}
}
func (t *Tx) Commit(ctx context.Context) error   { return t.tx.Commit() }
func (t *Tx) Rollback(ctx context.Context) error { return t.tx.Rollback() }
func (r *Row) Scan(dest ...any) error {
	err := scanSQLiteTimes(r.row.Scan, dest...)
	if errors.Is(err, sql.ErrNoRows) {
		return sql.ErrNoRows
	}
	return err
}
func (r *Rows) Next() bool             { return r.rows.Next() }
func (r *Rows) Scan(dest ...any) error { return scanSQLiteTimes(r.rows.Scan, dest...) }

// SQLite stores the schema's timestamps as TEXT. Normalize them here so scans
// into time.Time and sql.NullTime work consistently across SQLite driver versions.
func scanSQLiteTimes(scan func(...any) error, dest ...any) error {
	type timeTarget struct {
		index    int
		nullable bool
	}
	targets := append([]any(nil), dest...)
	var times []timeTarget
	for i, target := range dest {
		switch target.(type) {
		case *time.Time:
			times = append(times, timeTarget{index: i})
			var raw any
			targets[i] = &raw
		case *sql.NullTime:
			times = append(times, timeTarget{index: i, nullable: true})
			var raw any
			targets[i] = &raw
		}
	}
	if err := scan(targets...); err != nil {
		return err
	}
	for i := range times {
		item := &times[i]
		rawPtr := targets[item.index].(*any)
		value, valid, err := parseSQLiteTime(*rawPtr)
		if err != nil {
			return fmt.Errorf("parse SQLite timestamp: %w", err)
		}
		if item.nullable {
			*(dest[item.index].(*sql.NullTime)) = sql.NullTime{Time: value, Valid: valid}
		} else if valid {
			*(dest[item.index].(*time.Time)) = value
		} else {
			*(dest[item.index].(*time.Time)) = time.Time{}
		}
	}
	return nil
}

func parseSQLiteTime(raw any) (time.Time, bool, error) {
	if raw == nil {
		return time.Time{}, false, nil
	}
	if value, ok := raw.(time.Time); ok {
		return value, true, nil
	}
	var text string
	switch value := raw.(type) {
	case string:
		text = value
	case []byte:
		text = string(value)
	default:
		return time.Time{}, false, fmt.Errorf("unsupported timestamp value %T", raw)
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999 -07:00 MST",
		"2006-01-02 15:04:05.999999999 -0700 MST",
		"2006-01-02 15:04:05 -07:00 MST",
		"2006-01-02 15:04:05 -0700 MST",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02",
	} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed, true, nil
		}
	}
	return time.Time{}, false, fmt.Errorf("unrecognized timestamp %q", text)
}
func (r *Rows) Err() error   { return r.rows.Err() }
func (r *Rows) Close() error { return r.rows.Close() }

func CheckReady(ctx context.Context, pool *Pool, _, _ string) error {
	if pool == nil {
		return errors.New("database belum dikonfigurasi")
	}
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("database tidak tersedia: %w", err)
	}
	var check string
	if err := pool.QueryRow(ctx, "PRAGMA quick_check").Scan(&check); err != nil {
		return fmt.Errorf("SQLite quick_check gagal: %w", err)
	}
	if check != "ok" {
		return fmt.Errorf("SQLite quick_check: %s", check)
	}
	for table, cols := range requiredColumns {
		rows, err := pool.Query(ctx, "SELECT name FROM pragma_table_info(?)", table)
		if err != nil {
			return err
		}
		available := map[string]bool{}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				return err
			}
			available[name] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(available) == 0 {
			return fmt.Errorf("tabel inti %s tidak ditemukan", table)
		}
		for _, col := range cols {
			if !available[col] {
				return fmt.Errorf("kolom inti %s.%s tidak ditemukan", table, col)
			}
		}
	}
	return nil
}

var forUpdate = regexp.MustCompile(`(?i)\s+FOR\s+UPDATE(?:\s+OF\s+[a-zA-Z0-9_]+(?:\s*,\s*[a-zA-Z0-9_]+)*)?(?:\s+SKIP\s+LOCKED)?`)
var anyArray = regexp.MustCompile(`(?i)([a-zA-Z0-9_.]+)\s*=\s*ANY\(\$(\d+)(?:::[a-zA-Z0-9_\[\]]+)?\)`)
var positional = regexp.MustCompile(`\$(\d+)`)
var offsetLimit = regexp.MustCompile(`(?i)OFFSET\s+(\$[0-9]+)\s+LIMIT\s+(\$[0-9]+)`)
var pgCast = regexp.MustCompile(`::[a-zA-Z_][a-zA-Z0-9_]*`)

func sqliteQuery(query string, args []any) (string, []any) {
	query = forUpdate.ReplaceAllString(query, "")
	matches := anyArray.FindAllStringSubmatch(query, -1)
	for _, m := range matches {
		idx := 0
		fmt.Sscanf(m[2], "%d", &idx)
		idx--
		if idx < 0 || idx >= len(args) {
			continue
		}
		var values []any
		switch v := args[idx].(type) {
		case []string:
			for _, x := range v {
				values = append(values, x)
			}
		case []int64:
			for _, x := range v {
				values = append(values, x)
			}
		case []int:
			for _, x := range v {
				values = append(values, x)
			}
		default:
			continue
		}
		marks := make([]string, len(values))
		for i := range marks {
			marks[i] = fmt.Sprintf("$%d", idx+i+1)
		}
		replacement := "1=0"
		if len(marks) > 0 {
			replacement = m[1] + " IN (" + strings.Join(marks, ",") + ")"
		}
		// Shift positional placeholders to account for expanded array elements.
		shift := len(values) - 1
		if shift > 0 {
			query = positional.ReplaceAllStringFunc(query, func(token string) string {
				n := 0
				fmt.Sscanf(token, "$%d", &n)
				if n > idx+1 {
					return fmt.Sprintf("$%d", n+shift)
				}
				return token
			})
		}
		query = strings.Replace(query, m[0], replacement, 1)
		args = append(args[:idx], append(values, args[idx+1:]...)...)
	}
	query = offsetLimit.ReplaceAllStringFunc(query, func(s string) string {
		m := offsetLimit.FindStringSubmatch(s)
		return "LIMIT " + m[2] + " OFFSET " + m[1]
	})
	query = strings.ReplaceAll(query, "ILIKE", "LIKE")
	query = strings.ReplaceAll(query, "GREATEST", "MAX")
	query = strings.ReplaceAll(query, "LEAST", "MIN")
	query = pgCast.ReplaceAllString(query, "")
	return query, args
}
