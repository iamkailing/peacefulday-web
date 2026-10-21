package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed all:templates static
var assetsFS embed.FS

type App struct {
	Cfg        Config
	DB         *sql.DB
	Pay        PaymentProvider
	Log        *slog.Logger
	HTTP       *http.Client
	tmpl       map[string]*template.Template
	limit      *rateLimiter
	mux        *http.ServeMux
	static     http.Handler
	started    time.Time
	assets     fs.FS // embedded by default; ASSETS_DIR (development only) serves templates/static from disk
	liveAssets bool
	tmplMu     sync.RWMutex
}

func New(cfg Config, db *sql.DB, log *slog.Logger) (*App, error) {
	a := &App{Cfg: cfg, DB: db, Log: log, HTTP: &http.Client{Timeout: 20 * time.Second}, limit: newRateLimiter(), started: time.Now()}
	switch cfg.PaymentProvider {
	case "stripe":
		a.Pay = &StripeProvider{SecretKey: cfg.StripeSecretKey, WebhookSecret: cfg.StripeWebhookSecret, APIBase: cfg.StripeAPIBase, HTTP: a.HTTP}
	default:
		a.Pay = &SimulatedProvider{BaseURL: cfg.BaseURL, Secret: cfg.SessionSecret}
	}
	a.assets = assetsFS
	if dir := os.Getenv("ASSETS_DIR"); dir != "" && cfg.Env == "development" {
		a.assets, a.liveAssets = os.DirFS(dir), true
		log.Info("serving templates and static files from disk (development)", "dir", dir)
	}
	if err := a.parseTemplates(); err != nil {
		return nil, err
	}
	sub, _ := fs.Sub(a.assets, "static")
	a.static = http.StripPrefix("/static/", cacheStatic(http.FileServer(http.FS(sub))))
	a.routes()
	return a, nil
}

func (a *App) Simulated() bool { return a.Pay.Name() == "simulated" }

// ---------- templates ----------

func (a *App) funcs() template.FuncMap {
	return template.FuncMap{
		"baht":  Baht,
		"trend": trendTag,
		"asset": a.assetURL,
		"img": func(imgs []GalleryImage, i int) *GalleryImage {
			if i >= 0 && i < len(imgs) {
				return &imgs[i]
			}
			return nil
		},
		"div64": func(a int64, b int) int64 {
			if b == 0 {
				return 0
			}
			return a / int64(b)
		},
		"sample":   func(i int) string { return fmt.Sprintf("/static/img/sample-%d.svg", i%6+1) },
		"thdate":   func(t time.Time) string { return TH.DateShort(t) },
		"thlong":   func(t time.Time) string { return TH.DateLong(t) },
		"roleName": func(r string) string { return RoleNames[r][0] },
		"methodName": func(m string) string {
			if l, ok := methodLabels[m]; ok {
				return l
			}
			if l, ok := methodLabels["venue:"+m]; ok {
				return l
			}
			return m
		},
		"hm":    HM,
		"phone": FmtPhone,
		"safe":  func(s string) template.HTML { return template.HTML(s) },
		"add":   func(a, b int) int { return a + b },
		"sub":   func(a, b int) int { return a - b },
		"mul":   func(a, b int) int { return a * b },
		"pct": func(a, b int) int {
			if b == 0 {
				return 0
			}
			return a * 100 / b
		},
		"json":  func(v any) template.JS { b, _ := json.Marshal(v); return template.JS(b) },
		"year":  func() int { return Now().Year() },
		"now":   Now,
		"past":  func(t time.Time) bool { return t.Before(time.Now()) },
		"sub64": func(a, b int64) int64 { return a - b },
		"mul64": func(a any, b int) int64 {
			switch v := a.(type) {
			case int:
				return int64(v) * int64(b)
			case int64:
				return v * int64(b)
			}
			return 0
		},
		"weekdayTH": func(d any) string {
			var n int
			switch v := d.(type) {
			case int:
				n = v
			case int64:
				n = int(v)
			}
			if n < 0 || n > 6 {
				return ""
			}
			return thDays[n]
		},
		"has64": func(list []int64, v int64) bool {
			for _, x := range list {
				if x == v {
					return true
				}
			}
			return false
		},
		"hasInt": func(list []int64, v int) bool {
			for _, x := range list {
				if x == int64(v) {
					return true
				}
			}
			return false
		},
		"gridkey": func(d time.Time, t string) string { return d.In(BKK).Format("2006-01-02") + "|" + t },
		"slotAt": func(m map[string]Slot, k string) *Slot {
			if s, ok := m[k]; ok {
				return &s
			}
			return nil
		},
		"bookingsFor": func(m map[int64][]BookingView, id int64) []BookingView { return m[id] },
		"pctOf": func(a, b int) int {
			if b <= 0 {
				return 0
			}
			v := a * 100 / b
			if v > 100 {
				v = 100
			}
			return v
		},
		"seq": func(n int) []int {
			s := make([]int, n)
			for i := range s {
				s[i] = i
			}
			return s
		},
		"date": func(t time.Time) string { return t.In(BKK).Format("2006-01-02") },
		"datetime": func(t time.Time) string {
			t = t.In(BKK)
			return fmt.Sprintf("%d %s %d %s", t.Day(), thMonthsShort[t.Month()-1], t.Year()+543, t.Format("15:04"))
		},
		"dmy": func(t time.Time) string {
			t = t.In(BKK)
			return fmt.Sprintf("%d %s %d", t.Day(), thMonthsShort[t.Month()-1], t.Year()+543)
		},
		"satang2":   func(v int64) string { return strconv.FormatFloat(float64(v)/100, 'f', -1, 64) },
		"contains":  strings.Contains,
		"hasPrefix": strings.HasPrefix,
		"lower":     strings.ToLower,
		"upper":     strings.ToUpper,
		"split":     strings.Split,
		"join":      strings.Join,
		"eqs":       func(a any, b string) bool { return fmt.Sprint(a) == b },
		"dict": func(kv ...any) map[string]any {
			m := map[string]any{}
			for i := 0; i+1 < len(kv); i += 2 {
				m[fmt.Sprint(kv[i])] = kv[i+1]
			}
			return m
		},
		"qs": func(v ...string) template.URL {
			q := url.Values{}
			for i := 0; i+1 < len(v); i += 2 {
				if v[i+1] != "" {
					q.Set(v[i], v[i+1])
				}
			}
			return template.URL(q.Encode())
		},
	}
}

func (a *App) parseTemplates() error {
	tm := map[string]*template.Template{}
	for _, group := range []struct{ layout, glob string }{
		{"templates/layout.html", "templates/*.html"},
		{"templates/admin/layout.html", "templates/admin/*.html"},
	} {
		pages, err := fs.Glob(a.assets, group.glob)
		if err != nil {
			return err
		}
		for _, p := range pages {
			if p == group.layout || strings.HasPrefix(path.Base(p), "_") {
				continue
			}
			partials, _ := fs.Glob(a.assets, path.Dir(group.layout)+"/_*.html")
			files := append([]string{group.layout}, partials...)
			files = append(files, p)
			t, err := template.New(path.Base(group.layout)).Funcs(a.funcs()).ParseFS(a.assets, files...)
			if err != nil {
				return fmt.Errorf("template %s: %w", p, err)
			}
			key := strings.TrimPrefix(strings.TrimSuffix(p, ".html"), "templates/")
			tm[key] = t
		}
	}
	a.tmplMu.Lock()
	a.tmpl = tm
	a.tmplMu.Unlock()
	return nil
}

// Page is the data passed to every template.
type Page struct {
	L         Lang
	Title     string
	Desc      string
	Path      string // path without language prefix
	Canonical string
	AltURL    string
	Set       Settings
	CSRF      string
	Consent   string // "", "all", "essential"
	Simulated bool
	TestMode  bool
	Staff     *Staff
	Flash     string
	FlashErr  string
	Data      any
	NoIndex   bool
	Nav       string
	JSONLD    template.JS
	Purchase  *PurchaseEvent
	Facts     *SiteFacts
	Demo      bool              // admin: demo rows exist
	Env       string            // development | staging | production
	Base      string            // absolute site URL, no trailing slash
	ThURL     string            // absolute URL of this page in Thai
	EnURL     string            // absolute URL of this page in English
	Tracking  map[string]string // consent-gated tag IDs for static/js/track.js
}

type PurchaseEvent struct {
	ID    string
	Value float64
}

func (a *App) render(w http.ResponseWriter, r *http.Request, name string, status int, p *Page) {
	if a.liveAssets {
		if err := a.parseTemplates(); err != nil {
			a.Log.Error("template reload failed", "err", err)
			http.Error(w, err.Error(), 500)
			return
		}
	}
	a.tmplMu.RLock()
	t, ok := a.tmpl[name]
	a.tmplMu.RUnlock()
	if !ok {
		a.Log.Error("missing template", "name", name)
		http.Error(w, "template missing", 500)
		return
	}
	if p.Set == nil {
		p.Set, _ = LoadSettings(r.Context(), a.DB)
	}
	p.Simulated = a.Simulated()
	p.TestMode = a.Cfg.PaymentTestMode()
	if p.CSRF == "" {
		p.CSRF = a.csrfToken(w, r)
	}
	if c, err := r.Cookie("pd_consent"); err == nil {
		p.Consent = c.Value
	}
	if p.Path == "" {
		p.Path = stripLang(r.URL.Path)
	}
	p.Base = a.Cfg.BaseURL
	p.Env = a.Cfg.Env
	p.ThURL = a.Cfg.BaseURL + p.Path
	if p.Path == "/" {
		p.EnURL = a.Cfg.BaseURL + "/en"
	} else {
		p.EnURL = a.Cfg.BaseURL + "/en" + p.Path
	}
	p.Canonical = p.ThURL
	if p.L == EN {
		p.Canonical = p.EnURL
	}
	p.AltURL = p.L.Other().Prefix() + p.Path
	if p.AltURL == "" {
		p.AltURL = "/"
	}
	if !strings.HasPrefix(name, "admin/") {
		a.applyMeta(r.Context(), p)
		if p.Facts == nil {
			f := a.siteFacts(r.Context())
			p.Facts = &f
		}
		p.Tracking = map[string]string{}
		for _, k := range []string{"gtm_id", "ga4_id", "meta_pixel_id", "google_ads_id", "google_ads_label"} {
			if v := p.Set.Str(k); v != "" {
				p.Tracking[k] = v
			}
		}
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, p); err != nil {
		a.Log.Error("render failed", "template", name, "err", err)
		http.Error(w, "ขออภัย ระบบแสดงผลหน้านี้ไม่ได้ชั่วคราว / Sorry, this page could not be displayed.", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	w.Write(buf.Bytes())
}

func (a *App) applyMeta(ctx context.Context, p *Page) {
	var tth, ten, dth, den string
	if a.DB.QueryRowContext(ctx, `SELECT title_th,title_en,desc_th,desc_en FROM page_meta WHERE path=$1`, p.Path).Scan(&tth, &ten, &dth, &den) == nil {
		if t := p.L.Pick(tth, ten); t != "" {
			p.Title = t
		}
		if d := p.L.Pick(dth, den); d != "" {
			p.Desc = d
		}
	}
}

// ---------- language & routing ----------

func langOf(r *http.Request) Lang {
	if r.URL.Path == "/en" || strings.HasPrefix(r.URL.Path, "/en/") {
		return EN
	}
	return TH
}

func stripLang(p string) string {
	if p == "/en" {
		return "/"
	}
	return strings.TrimPrefix(p, "/en")
}

func (a *App) routes() {
	m := http.NewServeMux()
	pub := func(method, pattern string, h http.HandlerFunc) {
		m.HandleFunc(method+" "+pattern, h)
		if pattern == "/{$}" {
			m.HandleFunc(method+" /en", h)
			m.HandleFunc(method+" /en/{$}", h)
		} else {
			m.HandleFunc(method+" /en"+pattern, h)
		}
	}
	m.Handle("GET /static/", a.static)
	m.HandleFunc("GET /healthz", a.handleHealth)
	m.HandleFunc("GET /robots.txt", a.handleRobots)
	m.HandleFunc("GET /sitemap.xml", a.handleSitemap)
	m.HandleFunc("GET /img/{id}", a.handleImage)
	m.HandleFunc("GET /qr/{ref}", a.handleQR)

	pub("GET", "/{$}", a.handleHome)
	pub("GET", "/experiences", a.handleExperiences)
	pub("GET", "/pricing", a.handlePricing)
	pub("GET", "/story", a.staticPage("story", "nav.story"))
	pub("GET", "/gallery", a.handleGallery)
	pub("GET", "/faq", a.staticPage("faq", "nav.faq"))
	pub("GET", "/contact", a.staticPage("contact", "nav.contact"))
	pub("GET", "/privacy", a.staticPage("privacy", "nav.privacy"))
	pub("GET", "/terms", a.staticPage("terms", "nav.terms"))
	pub("GET", "/cancellation", a.staticPage("cancellation", "nav.cancel"))
	pub("GET", "/book", a.handleBookPage)
	pub("GET", "/booking/{ref}", a.handleBookingPage)
	pub("POST", "/booking/{ref}/cancel", a.handleCustomerCancel)
	pub("POST", "/booking/{ref}/reschedule", a.handleCustomerReschedule)
	pub("GET", "/booking/{ref}/calendar.ics", a.handleICS)
	pub("GET", "/booking/{ref}/status", a.handleBookingStatus)
	pub("GET", "/booking/{ref}/pay", a.handleResumePayment)
	pub("GET", "/my-bookings", a.handleMyBookings)
	pub("POST", "/my-bookings/link", a.handleMagicLinkRequest)
	pub("POST", "/my-bookings/find", a.handleFindBooking)
	pub("GET", "/my-bookings/login", a.handleMagicLogin)
	pub("POST", "/my-bookings/logout", a.handleCustomerLogout)
	pub("GET", "/packages/{id}", a.handlePassBuyPage)
	pub("POST", "/packages/{id}", a.handlePassBuy)
	pub("GET", "/voucher/{ref}", a.handleVoucherPage)
	pub("GET", "/lp/{slug}", a.handleLanding)

	m.HandleFunc("GET /api/days", a.handleAPIDays)
	m.HandleFunc("GET /api/slots", a.handleAPISlots)
	m.HandleFunc("POST /api/quote", a.handleAPIQuote)
	m.HandleFunc("POST /api/bookings", a.handleAPICreateBooking)
	m.HandleFunc("POST /api/consent", a.handleConsent)
	m.HandleFunc("POST /api/event", a.handleWebEvent)

	m.HandleFunc("POST /webhooks/stripe", a.handleStripeWebhook)
	m.HandleFunc("GET /pay/sim/{session}", a.handleSimPage)
	m.HandleFunc("POST /pay/sim/{session}", a.handleSimAction)

	a.adminRoutes(m)
	m.HandleFunc("/", a.handleNotFound)
	a.mux = m
}

func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rw := &statusWriter{ResponseWriter: w, status: 200}
	defer func() {
		if rec := recover(); rec != nil {
			a.Log.Error("panic", "err", rec, "path", r.URL.Path, "stack", string(debug.Stack()))
			a.AdminAlert(context.Background(), "Server error", fmt.Sprintf("%v at %s", rec, r.URL.Path), 0)
			http.Error(rw, "Internal Server Error", 500)
		}
		if !strings.HasPrefix(r.URL.Path, "/static/") && r.URL.Path != "/healthz" {
			a.Log.Info("http", "method", r.Method, "path", r.URL.Path, "status", rw.status, "ms", time.Since(start).Milliseconds(), "ip", clientIP(r))
		}
	}()
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	h.Set("X-Frame-Options", "SAMEORIGIN")
	h.Set("Permissions-Policy", "camera=(self), microphone=(), geolocation=()")
	if a.Cfg.Secure() {
		h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
	}
	h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: https:; style-src 'self' 'unsafe-inline'; font-src 'self'; "+
		"script-src 'self' https://www.googletagmanager.com https://connect.facebook.net https://www.googleadservices.com https://googleads.g.doubleclick.net https://www.google.com; "+
		"connect-src 'self' https://*.google-analytics.com https://*.analytics.google.com https://*.googletagmanager.com https://www.facebook.com https://*.doubleclick.net https://www.google.com https://www.googleadservices.com; "+
		"frame-src https://www.google.com https://www.googletagmanager.com https://td.doubleclick.net; form-action 'self' https://checkout.stripe.com; base-uri 'self'; object-src 'none'; frame-ancestors 'self'")
	if r.Method == http.MethodGet && !strings.HasPrefix(r.URL.Path, "/admin") && !strings.HasPrefix(r.URL.Path, "/api") && !strings.HasPrefix(r.URL.Path, "/static") {
		a.trackVisitor(rw, r)
	}
	a.mux.ServeHTTP(rw, r)
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(c int) { s.status = c; s.ResponseWriter.WriteHeader(c) }

var assetHashes sync.Map

// assetURL returns /static/<p>?v=<content hash> so browsers refetch changed files after a deploy.
func (a *App) assetURL(p string) string {
	if !a.liveAssets {
		if v, ok := assetHashes.Load(p); ok {
			return v.(string)
		}
	}
	b, err := fs.ReadFile(a.assets, "static/"+p)
	if err != nil {
		return "/static/" + p
	}
	sum := sha256.Sum256(b)
	u := "/static/" + p + "?v=" + hex.EncodeToString(sum[:])[:10]
	assetHashes.Store(p, u)
	return u
}

func cacheStatic(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("v") != "" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=3600")
		}
		h.ServeHTTP(w, r)
	})
}

// ---------- cookies, CSRF, visitor attribution ----------

func (a *App) setCookie(w http.ResponseWriter, name, value string, maxAge time.Duration, httpOnly bool) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: int(maxAge.Seconds()), HttpOnly: httpOnly,
		Secure: a.Cfg.Secure(), SameSite: http.SameSiteLaxMode})
}

// csrfToken implements the double-submit pattern for public forms: a random cookie mirrored in the form/header.
func (a *App) csrfToken(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie("pd_csrf"); err == nil && len(c.Value) >= 20 {
		return c.Value
	}
	t := RandomToken()
	a.setCookie(w, "pd_csrf", t, 24*time.Hour*30, true)
	return t
}

func (a *App) checkCSRF(r *http.Request) bool {
	c, err := r.Cookie("pd_csrf")
	if err != nil || c.Value == "" {
		return false
	}
	got := r.Header.Get("X-CSRF")
	if got == "" {
		got = r.FormValue("csrf")
	}
	return ConstEq(got, c.Value)
}

// trackVisitor keeps an anonymous visitor id and first/last-touch UTM parameters in cookies.
func (a *App) trackVisitor(w http.ResponseWriter, r *http.Request) {
	if _, err := r.Cookie("pd_vid"); err != nil {
		a.setCookie(w, "pd_vid", RandomCode(16), 365*24*time.Hour, true)
	}
	q := r.URL.Query()
	if src := q.Get("utm_source"); src != "" {
		v := url.Values{}
		v.Set("s", clip(src, 100))
		v.Set("m", clip(q.Get("utm_medium"), 100))
		v.Set("c", clip(q.Get("utm_campaign"), 150))
		v.Set("l", clip(r.URL.Path, 200))
		a.setCookie(w, "pd_utm", v.Encode(), 30*24*time.Hour, true)
	} else if ref := r.Referer(); ref != "" {
		if u, err := url.Parse(ref); err == nil && u.Host != "" && !strings.Contains(a.Cfg.BaseURL, u.Host) && !strings.HasSuffix(u.Hostname(), "stripe.com") {
			if _, err := r.Cookie("pd_utm"); err != nil {
				v := url.Values{}
				v.Set("s", u.Hostname())
				v.Set("m", "referral")
				v.Set("l", clip(r.URL.Path, 200))
				a.setCookie(w, "pd_utm", v.Encode(), 30*24*time.Hour, true)
			}
		}
	}
}

type attribution struct{ Source, Medium, Campaign, Landing, Referrer, Visitor string }

func attributionOf(r *http.Request) attribution {
	var at attribution
	if c, err := r.Cookie("pd_utm"); err == nil {
		v, _ := url.ParseQuery(c.Value)
		at.Source, at.Medium, at.Campaign, at.Landing = v.Get("s"), v.Get("m"), v.Get("c"), v.Get("l")
	}
	if at.Source == "" {
		at.Source = "direct"
	}
	if c, err := r.Cookie("pd_vid"); err == nil {
		at.Visitor = c.Value
	}
	at.Referrer = r.Referer()
	return at
}

// clientIP returns the visitor's address for rate limiting and audit logs. X-Forwarded-For is only
// trusted when the connection comes from a private-network proxy (the host's load balancer), and then
// only its last entry, which that proxy appended; earlier entries can be forged by the client.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if xf := r.Header.Get("X-Forwarded-For"); xf != "" {
		if ip := net.ParseIP(host); ip != nil && (ip.IsPrivate() || ip.IsLoopback()) {
			parts := strings.Split(xf, ",")
			if last := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(last) != nil {
				return last
			}
		}
	}
	return host
}

// ---------- rate limiting (in-memory token buckets per IP and bucket name) ----------

type bucket struct {
	tokens float64
	last   time.Time
}

type rateLimiter struct {
	mu sync.Mutex
	b  map[string]*bucket
}

func newRateLimiter() *rateLimiter { return &rateLimiter{b: map[string]*bucket{}} }

// Allow permits `burst` requests immediately and refills at burst per `per`.
func (l *rateLimiter) Allow(key string, burst int, per time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if len(l.b) > 50000 {
		l.b = map[string]*bucket{}
	}
	bk, ok := l.b[key]
	if !ok {
		bk = &bucket{tokens: float64(burst), last: now}
		l.b[key] = bk
	}
	bk.tokens += now.Sub(bk.last).Seconds() * float64(burst) / per.Seconds()
	if bk.tokens > float64(burst) {
		bk.tokens = float64(burst)
	}
	bk.last = now
	if bk.tokens < 1 {
		return false
	}
	bk.tokens--
	return true
}

func (a *App) allow(r *http.Request, name string, burst int, per time.Duration) bool {
	return a.limit.Allow(name+"|"+clientIP(r), burst, per)
}

// ---------- JSON helpers ----------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (a *App) jsonError(w http.ResponseWriter, r *http.Request, err error) {
	var ue UserError
	l := Lang(r.Header.Get("X-Lang"))
	if l != EN {
		l = TH
	}
	if errors.As(err, &ue) {
		writeJSON(w, 422, map[string]any{"error": ue.Code, "message": l.T("err." + ue.Code)})
		return
	}
	a.Log.Error("api error", "path", r.URL.Path, "err", err)
	set, _ := LoadSettings(r.Context(), a.DB)
	writeJSON(w, 500, map[string]any{"error": "server", "message": l.T("book.err.generic", set.Str("phone"))})
}

// ---------- background workers ----------

// RunWorkers expires holds, generates future sessions, sends notifications and reminders.
func (a *App) RunWorkers(ctx context.Context) {
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	var lastSlots, lastReminders time.Time
	for {
		if n, err := a.ExpireHolds(ctx); err != nil {
			a.Log.Error("expire holds", "err", err)
		} else if n > 0 {
			a.Log.Info("holds expired", "count", n)
		}
		if time.Since(lastSlots) > time.Hour {
			set, _ := LoadSettings(ctx, a.DB)
			if err := EnsureSlots(ctx, a.DB, Now(), Now().AddDate(0, 0, set.Int("booking_horizon_days")+7)); err != nil {
				a.Log.Error("ensure slots", "err", err)
			}
			a.DB.ExecContext(ctx, `UPDATE passes SET status='expired' WHERE status='active' AND expires_at < now()`)
			a.DB.ExecContext(ctx, `WITH o AS (UPDATE pass_orders SET status='expired' WHERE status='pending' AND created_at < now() - interval '2 hours' RETURNING id, pass_id),
				p AS (UPDATE payments SET status='expired', updated_at=now() WHERE pass_order_id IN (SELECT id FROM o) AND status='pending')
				UPDATE passes SET status='void', note='unpaid order expired' WHERE id IN (SELECT pass_id FROM o) AND status='pending'`)
			a.DB.ExecContext(ctx, `DELETE FROM web_events WHERE created_at < now() - interval '400 days'`)
			a.DB.ExecContext(ctx, `DELETE FROM staff_sessions WHERE expires_at < now()`)
			a.DB.ExecContext(ctx, `DELETE FROM customer_logins WHERE expires_at < now()`)
			lastSlots = time.Now()
		}
		if time.Since(lastReminders) > 5*time.Minute {
			if err := a.QueueReminders(ctx); err != nil {
				a.Log.Error("reminders", "err", err)
			}
			lastReminders = time.Now()
		}
		if _, err := a.DeliverNotifications(ctx); err != nil {
			a.Log.Error("notifications", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
