package app

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math/big"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Config is read from the environment. Nothing secret is ever rendered to pages.
type Config struct {
	Addr                                             string
	DatabaseURL                                      string
	BaseURL                                          string // public URL, e.g. https://peacefulday.co.th
	Env                                              string // development | production
	SessionSecret                                    string // HMAC key for cookies/tokens
	PaymentProvider                                  string // simulated | stripe
	StripeSecretKey                                  string
	StripeWebhookSecret                              string
	StripeAPIBase                                    string // overridable in tests
	SMTPHost, SMTPPort, SMTPUser, SMTPPass, SMTPFrom string
	AdminAlertEmail                                  string
	MetaAccessToken                                  string // Conversions API (optional)
	LineChannelToken                                 string // future LINE Messaging API
	BootstrapOwnerEmail, BootstrapOwnerPassword      string
	DisableWorkers                                   bool
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func LoadConfig() (Config, error) {
	c := Config{
		Addr:                   ":" + env("PORT", "8080"),
		DatabaseURL:            env("DATABASE_URL", "postgres://postgres@127.0.0.1:5432/peacefulday?sslmode=disable"),
		BaseURL:                strings.TrimRight(env("BASE_URL", env("RENDER_EXTERNAL_URL", "http://localhost:8080")), "/"),
		Env:                    env("APP_ENV", "development"),
		SessionSecret:          env("SESSION_SECRET", ""),
		PaymentProvider:        env("PAYMENT_PROVIDER", "simulated"),
		StripeSecretKey:        env("STRIPE_SECRET_KEY", ""),
		StripeWebhookSecret:    env("STRIPE_WEBHOOK_SECRET", ""),
		StripeAPIBase:          env("STRIPE_API_BASE", "https://api.stripe.com"),
		SMTPHost:               env("SMTP_HOST", ""),
		SMTPPort:               env("SMTP_PORT", "587"),
		SMTPUser:               env("SMTP_USER", ""),
		SMTPPass:               env("SMTP_PASS", ""),
		SMTPFrom:               env("SMTP_FROM", ""),
		AdminAlertEmail:        env("ADMIN_ALERT_EMAIL", ""),
		MetaAccessToken:        env("META_CAPI_ACCESS_TOKEN", ""),
		LineChannelToken:       env("LINE_CHANNEL_ACCESS_TOKEN", ""),
		BootstrapOwnerEmail:    env("BOOTSTRAP_OWNER_EMAIL", ""),
		BootstrapOwnerPassword: env("BOOTSTRAP_OWNER_PASSWORD", ""),
		DisableWorkers:         env("DISABLE_WORKERS", "") == "1",
	}
	switch c.Env {
	case "development", "staging", "production":
	default:
		return c, fmt.Errorf("APP_ENV must be development, staging or production (got %q)", c.Env)
	}
	if c.SessionSecret == "" {
		if c.Env != "development" {
			return c, errors.New("SESSION_SECRET must be set outside development (any long random string, 32+ characters)")
		}
		c.SessionSecret = "dev-only-insecure-secret"
	}
	if c.Env != "development" && len(c.SessionSecret) < 32 {
		return c, errors.New("SESSION_SECRET is too short: use at least 32 random characters")
	}
	switch c.PaymentProvider {
	case "stripe":
		if c.StripeSecretKey == "" || c.StripeWebhookSecret == "" {
			return c, errors.New("PAYMENT_PROVIDER=stripe needs STRIPE_SECRET_KEY and STRIPE_WEBHOOK_SECRET")
		}
	case "simulated":
		// The simulated checkout lets anyone mark a booking as paid, so it can never run in production.
		if c.Env == "production" {
			return c, errors.New("PAYMENT_PROVIDER=simulated is not allowed when APP_ENV=production: use Stripe test keys (sk_test_...) or set APP_ENV=staging for a preview site")
		}
	default:
		return c, fmt.Errorf("PAYMENT_PROVIDER must be stripe or simulated (got %q)", c.PaymentProvider)
	}
	return c, nil
}

// PaymentTestMode is true when no real money can move (simulated provider or Stripe test keys).
func (c Config) PaymentTestMode() bool {
	return c.PaymentProvider != "stripe" || strings.HasPrefix(c.StripeSecretKey, "sk_test_") || strings.HasPrefix(c.StripeSecretKey, "rk_test_")
}

func (c Config) Secure() bool { return strings.HasPrefix(c.BaseURL, "https://") }

// ---------- database ----------

func OpenDB(ctx context.Context, dsn string) (*sql.DB, error) {
	dsn = withParam(dsn, "timezone", "Asia/Bangkok") // every pooled connection, not just the first
	db, err := openAndPing(ctx, dsn)
	if err != nil && strings.Contains(err.Error(), "SSL is not enabled") && !strings.Contains(dsn, "sslmode=") {
		// private-network database without TLS (e.g. a host's internal connection string)
		db, err = openAndPing(ctx, withParam(dsn, "sslmode", "disable"))
	}
	return db, err
}

func withParam(dsn, key, value string) string {
	if strings.Contains(dsn, key+"=") {
		return dsn
	}
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		return dsn + sep + key + "=" + url.QueryEscape(value)
	}
	return dsn + " " + key + "=" + value
}

func openAndPing(ctx context.Context, dsn string) (*sql.DB, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(30 * time.Minute)
	var lastErr error
	for i := 0; i < 20; i++ {
		if lastErr = db.PingContext(ctx); lastErr == nil {
			return db, nil
		}
		if strings.Contains(lastErr.Error(), "SSL is not enabled") || strings.Contains(lastErr.Error(), "password authentication failed") {
			break // retrying won't help
		}
		select {
		case <-ctx.Done():
			db.Close()
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	db.Close()
	return nil, fmt.Errorf("database not reachable: %w", lastErr)
}

// Migrate applies embedded SQL files in order, once each, inside a transaction guarded by an advisory lock.
func Migrate(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock(727001)`); err != nil {
		return err
	}
	defer conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock(727001)`)
	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	names, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, n := range names {
		var exists bool
		if err := conn.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name=$1)`, n).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		body, _ := migrationsFS.ReadFile(n)
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", n, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(name) VALUES($1)`, n); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		slog.Info("migration applied", "name", n)
	}
	return nil
}

// ---------- time ----------

var BKK = mustLoc("Asia/Bangkok")

func mustLoc(name string) *time.Location {
	l, err := time.LoadLocation(name)
	if err != nil {
		return time.FixedZone("ICT", 7*3600) // Thailand has no DST
	}
	return l
}

// clock is swappable in tests.
var nowFn = time.Now

func Now() time.Time { return nowFn().In(BKK) }

func DateOnly(t time.Time) time.Time {
	t = t.In(BKK)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, BKK)
}

func ParseDate(s string) (time.Time, error) { return time.ParseInLocation("2006-01-02", s, BKK) }

func ParseHM(date time.Time, hm string) (time.Time, error) {
	parts := strings.Split(hm, ":")
	if len(parts) != 2 {
		return time.Time{}, fmt.Errorf("bad time %q", hm)
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return time.Time{}, fmt.Errorf("bad time %q", hm)
	}
	d := DateOnly(date)
	return time.Date(d.Year(), d.Month(), d.Day(), h, m, 0, 0, BKK), nil
}

// ---------- money ----------

// Baht formats satang as "฿1,234" (or with decimals when needed).
func Baht(satang int64) string {
	neg := satang < 0
	if neg {
		satang = -satang
	}
	whole := satang / 100
	frac := satang % 100
	s := strconv.FormatInt(whole, 10)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	out := "฿" + b.String()
	if frac != 0 {
		out += fmt.Sprintf(".%02d", frac)
	}
	if neg {
		out = "-" + out
	}
	return out
}

func ParseBaht(s string) (int64, error) {
	s = strings.TrimSpace(strings.NewReplacer("฿", "", ",", "", " ", "").Replace(s))
	if s == "" {
		return 0, errors.New("empty amount")
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 || f > 10_000_000 {
		return 0, errors.New("invalid amount")
	}
	return int64(f*100 + 0.5), nil
}

// ---------- ids, tokens, hashing ----------

const refAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no 0/O/1/I

func RandomCode(n int) string {
	var b strings.Builder
	max := big.NewInt(int64(len(refAlphabet)))
	for i := 0; i < n; i++ {
		x, _ := rand.Int(rand.Reader, max)
		b.WriteByte(refAlphabet[x.Int64()])
	}
	return b.String()
}

func NewBookingRef() string { return "PD-" + RandomCode(6) }
func NewPassCode() string   { return "PDV-" + RandomCode(4) + "-" + RandomCode(4) }

func RandomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func HashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func Sign(secret, msg string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(msg))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func ConstEq(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

const pbkdfIter = 310000

func HashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	key, err := pbkdf2.Key(sha256.New, pw, salt, pbkdfIter, 32)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", pbkdfIter, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func CheckPassword(stored, pw string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[2])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[3])
	if err1 != nil || err2 != nil {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, pw, salt, iter, len(want))
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

// ---------- validation ----------

var emailRe = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]{2,}$`)

func NormEmail(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
func ValidEmail(s string) bool  { return len(s) <= 254 && emailRe.MatchString(s) }

// NormPhone keeps digits (and a leading + for international numbers). Thai +66 numbers become 0XXXXXXXXX.
func NormPhone(s string) string {
	s = strings.TrimSpace(s)
	plus := strings.HasPrefix(s, "+")
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	d := b.String()
	if plus || strings.HasPrefix(d, "66") && len(d) == 11 {
		if strings.HasPrefix(d, "66") && len(d) == 11 {
			return "0" + d[2:]
		}
		return "+" + d
	}
	return d
}

var thPhoneRe = regexp.MustCompile(`^0[689]\d{8}$|^0[2-7]\d{7}$`)

func ValidPhone(p string) bool {
	if strings.HasPrefix(p, "+") {
		return len(p) >= 8 && len(p) <= 16
	}
	return thPhoneRe.MatchString(p)
}

func FmtPhone(p string) string {
	if len(p) == 10 && p[0] == '0' {
		return p[:3] + "-" + p[3:6] + "-" + p[6:]
	}
	return p
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// ---------- settings ----------

// Settings are stored as JSON values keyed by name; Defaults lists every key with its default.
var Defaults = map[string]any{
	"business_name":           "Peaceful Day",
	"phone":                   "096-901-4999",
	"email":                   "",
	"line_id":                 "",
	"address_th":              "สุขุมวิท 62 กรุงเทพฯ (BTS บางจาก ทางออก 2)",
	"address_en":              "Sukhumvit 62, Bangkok (BTS Bang Chak, Exit 2)",
	"maps_url":                "https://maps.app.goo.gl/NJ1KqAuGzCA9HH1v5",
	"maps_query":              "Peaceful Day Sukhumvit 62 Bangkok",
	"instagram_url":           "",
	"facebook_url":            "",
	"tiktok_url":              "",
	"google_business_url":     "",
	"open_time":               "09:00",
	"close_time":              "21:00",
	"hold_minutes":            30,
	"booking_horizon_days":    60,
	"min_lead_minutes":        30,
	"cancel_cutoff_hours":     24,
	"reschedule_cutoff_hours": 24, // the shop's script: postpone with 1 day notice
	"max_reschedules":         1,
	"cancel_refund_mode":      "credit", // refund | credit — owner decides; credit matches the "postpone" practice
	"reminder_hours":          24,
	"ga4_id":                  "",
	"gtm_id":                  "",
	"meta_pixel_id":           "",
	"google_ads_id":           "",
	"google_ads_label":        "",
	"show_sauna_coming_soon":  true,
	"story_th":                "",
	"story_en":                "",
}

type Settings map[string]any

func LoadSettings(ctx context.Context, db *sql.DB) (Settings, error) {
	s := Settings{}
	for k, v := range Defaults {
		s[k] = v
	}
	rows, err := db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var raw []byte
		if err := rows.Scan(&k, &raw); err != nil {
			return s, err
		}
		var v any
		if json.Unmarshal(raw, &v) == nil {
			s[k] = v
		}
	}
	return s, rows.Err()
}

func (s Settings) Str(k string) string {
	switch v := s[k].(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

func (s Settings) Int(k string) int {
	switch v := s[k].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, _ := strconv.Atoi(v)
		return n
	}
	return 0
}

func (s Settings) Bool(k string) bool {
	switch v := s[k].(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1" || v == "on"
	}
	return false
}

func SaveSetting(ctx context.Context, db *sql.DB, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO settings(key,value,updated_at) VALUES($1,$2,now())
		ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=now()`, key, raw)
	return err
}

// ---------- audit ----------

func Audit(ctx context.Context, q interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, staffID int64, actor, action, entity string, entityID any, detail any, ip string) {
	var raw []byte
	if detail != nil {
		raw, _ = json.Marshal(detail)
	}
	var sid any
	if staffID > 0 {
		sid = staffID
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO audit_log(staff_id,actor,action,entity,entity_id,detail,ip) VALUES($1,$2,$3,$4,$5,$6,$7)`,
		sid, actor, action, entity, fmt.Sprint(entityID), nullJSON(raw), ip); err != nil {
		slog.Error("audit write failed", "err", err, "action", action)
	}
}

func nullJSON(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}
