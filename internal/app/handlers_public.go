package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

func (a *App) page(r *http.Request, titleKey string) *Page {
	l := langOf(r)
	p := &Page{L: l, Nav: titleKey}
	if titleKey != "" {
		p.Title = l.T(titleKey) + " · Peaceful Day"
	}
	return p
}

func (a *App) handleNotFound(w http.ResponseWriter, r *http.Request) {
	p := a.page(r, "")
	p.Title = p.L.T("err.notfound") + " · Peaceful Day"
	p.NoIndex = true
	a.render(w, r, "error", 404, p)
}

func (a *App) serverError(w http.ResponseWriter, r *http.Request, err error) {
	a.Log.Error("server error", "path", r.URL.Path, "err", err)
	p := a.page(r, "")
	p.Title = p.L.T("err.server")
	p.NoIndex = true
	p.Data = map[string]any{"Server": true}
	a.render(w, r, "error", 500, p)
}

func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	if err := a.DB.PingContext(r.Context()); err != nil {
		a.Log.Error("health check: database unavailable", "err", err)
		writeJSON(w, 503, map[string]any{"ok": false, "db": "unavailable"})
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "payments": a.Pay.Name(), "uptime_s": int(time.Since(a.started).Seconds())})
}

// ---------- marketing pages ----------

type GalleryImage struct {
	ID        int64
	AltTH     string
	AltEN     string
	Placement string
}

func (a *App) images(r *http.Request, placement string) []GalleryImage {
	rows, err := a.DB.QueryContext(r.Context(), `SELECT id, alt_th, alt_en, placement FROM gallery_images WHERE ($1='' OR placement=$1) ORDER BY sort, id`, placement)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []GalleryImage
	for rows.Next() {
		var g GalleryImage
		rows.Scan(&g.ID, &g.AltTH, &g.AltEN, &g.Placement)
		out = append(out, g)
	}
	return out
}

type Review struct {
	Author, Body, Source, SourceURL string
	Rating                          int
	Date                            sql.NullTime
}

func (a *App) reviews(r *http.Request) []Review {
	rows, err := a.DB.QueryContext(r.Context(), `SELECT author, body, source, source_url, rating, review_date FROM reviews WHERE published ORDER BY review_date DESC NULLS LAST, id DESC LIMIT 6`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Review
	for rows.Next() {
		var v Review
		rows.Scan(&v.Author, &v.Body, &v.Source, &v.SourceURL, &v.Rating, &v.Date)
		out = append(out, v)
	}
	return out
}

type catalog struct {
	Experiences []Experience
	Tickets     []Product
	Passes      []Product
}

func (a *App) catalog(r *http.Request) catalog {
	var c catalog
	c.Experiences, _ = ListExperiences(r.Context(), a.DB, false)
	all, _ := ListProducts(r.Context(), a.DB, `WHERE active AND public`)
	for _, p := range all {
		if !p.SellableNow() {
			continue
		}
		if p.Kind == "ticket" {
			c.Tickets = append(c.Tickets, p)
		} else if p.Price > 0 {
			c.Passes = append(c.Passes, p)
		}
	}
	return c
}

func (a *App) handleHome(w http.ResponseWriter, r *http.Request) {
	p := a.page(r, "")
	set, _ := LoadSettings(r.Context(), a.DB)
	p.Set = set
	p.Title = "Peaceful Day · Ice Bath & Sauna · Sukhumvit 62"
	if p.L == TH {
		p.Title = "Peaceful Day · ไอซ์บาธและซาวน่า สุขุมวิท 62"
	}
	p.Desc = p.L.T("hero.lead")
	c := a.catalog(r)
	// A few upcoming open sessions for the main experience.
	var upcoming []Slot
	if len(c.Experiences) > 0 {
		EnsureSlots(r.Context(), a.DB, Now(), Now().AddDate(0, 0, 7))
		slots, _ := SlotsBetween(r.Context(), a.DB, 0, Now().Add(time.Duration(set.Int("min_lead_minutes"))*time.Minute), Now().AddDate(0, 0, 5))
		for _, s := range slots {
			if s.Status == "open" && s.Remaining() > 0 {
				upcoming = append(upcoming, s)
			}
			if len(upcoming) == 8 {
				break
			}
		}
	}
	// Seven-day availability strip.
	type dayAvail struct {
		Date      time.Time
		Status    string // open | full | closed
		Remaining int
		Sessions  int
	}
	var week []dayAvail
	today := DateOnly(Now())
	weekSlots, _ := SlotsBetween(r.Context(), a.DB, 0, today, today.AddDate(0, 0, 7))
	earliest := Now().Add(time.Duration(set.Int("min_lead_minutes")) * time.Minute)
	for i := 0; i < 7; i++ {
		d := dayAvail{Date: today.AddDate(0, 0, i), Status: "closed"}
		for _, s := range weekSlots {
			if !DateOnly(s.StartsAt).Equal(d.Date) || s.Status != "open" || s.StartsAt.Before(earliest) {
				continue
			}
			if d.Status == "closed" {
				d.Status = "full"
			}
			if s.Remaining() > 0 {
				d.Status = "open"
				d.Remaining += s.Remaining()
				d.Sessions++
			}
		}
		week = append(week, d)
	}
	p.JSONLD = template.JS(a.businessJSONLD(r.Context(), set))
	p.Data = map[string]any{
		"Catalog": c, "Upcoming": upcoming, "Reviews": a.reviews(r), "Week": week,
		"Hero": a.images(r, "hero"), "Gallery": a.images(r, "gallery"), "ExpImages": a.images(r, "experience"),
	}
	a.render(w, r, "home", 200, p)
}

func (a *App) businessJSONLD(ctx context.Context, set Settings) []byte {
	// Opening days come from the live weekly schedule, so structured data never claims days the shop is closed.
	names := []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}
	var days []string
	rows, err := a.DB.QueryContext(ctx, `SELECT DISTINCT t.weekday FROM schedule_templates t JOIN experiences e ON e.id=t.experience_id AND e.status='active' ORDER BY 1`)
	if err == nil {
		for rows.Next() {
			var d int
			if rows.Scan(&d) == nil && d >= 0 && d < 7 {
				days = append(days, names[d])
			}
		}
		rows.Close()
	}
	biz := map[string]any{
		"@context": "https://schema.org", "@type": "HealthAndBeautyBusiness", "name": set.Str("business_name"),
		"url": a.Cfg.BaseURL, "telephone": set.Str("phone"), "image": a.Cfg.BaseURL + "/static/img/og.png",
		"address": map[string]any{"@type": "PostalAddress", "streetAddress": set.Str("address_en"), "addressLocality": "Bangkok", "addressCountry": "TH"},
	}
	if len(days) > 0 {
		biz["openingHoursSpecification"] = []map[string]any{{"@type": "OpeningHoursSpecification", "opens": set.Str("open_time"), "closes": set.Str("close_time"), "dayOfWeek": days}}
	}
	if u := set.Str("maps_url"); u != "" {
		biz["hasMap"] = u
	}
	var same []string
	for _, k := range []string{"instagram_url", "facebook_url", "tiktok_url", "google_business_url"} {
		if v := set.Str(k); v != "" {
			same = append(same, v)
		}
	}
	if len(same) > 0 {
		biz["sameAs"] = same
	}
	b, _ := json.Marshal(biz)
	return b
}

func (a *App) handleExperiences(w http.ResponseWriter, r *http.Request) {
	p := a.page(r, "nav.experiences")
	p.Data = map[string]any{"Catalog": a.catalog(r), "Images": a.images(r, "experience")}
	a.render(w, r, "experiences", 200, p)
}

func (a *App) handlePricing(w http.ResponseWriter, r *http.Request) {
	p := a.page(r, "nav.pricing")
	p.Data = map[string]any{"Catalog": a.catalog(r)}
	a.render(w, r, "pricing", 200, p)
}

func (a *App) handleGallery(w http.ResponseWriter, r *http.Request) {
	p := a.page(r, "nav.gallery")
	p.Data = map[string]any{"Images": a.images(r, "")}
	a.render(w, r, "gallery", 200, p)
}

func (a *App) staticPage(name, titleKey string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := a.page(r, titleKey)
		p.Data = map[string]any{"Story": a.images(r, "story")}
		a.render(w, r, name, 200, p)
	}
}

func (a *App) handleLanding(w http.ResponseWriter, r *http.Request) {
	var lp struct {
		TitleTH, TitleEN, BodyTH, BodyEN, Promo string
		Product                                 sql.NullInt64
	}
	err := a.DB.QueryRowContext(r.Context(), `SELECT title_th,title_en,body_th,body_en,promo_code,cta_product FROM landing_pages WHERE slug=$1 AND published`, r.PathValue("slug")).
		Scan(&lp.TitleTH, &lp.TitleEN, &lp.BodyTH, &lp.BodyEN, &lp.Promo, &lp.Product)
	if err != nil {
		a.handleNotFound(w, r)
		return
	}
	p := a.page(r, "")
	p.Title = p.L.Pick(lp.TitleTH, lp.TitleEN) + " · Peaceful Day"
	q := url.Values{}
	if lp.Product.Valid {
		q.Set("product", strconv.FormatInt(lp.Product.Int64, 10))
	}
	if lp.Promo != "" {
		q.Set("promo", lp.Promo)
	}
	p.Data = map[string]any{"Title": p.L.Pick(lp.TitleTH, lp.TitleEN), "Body": strings.Split(p.L.Pick(lp.BodyTH, lp.BodyEN), "\n"), "Query": q.Encode()}
	a.render(w, r, "landing", 200, p)
}

// ---------- booking page + API ----------

type ticketJS struct {
	ID         int64   `json:"id"`
	Exp        int64   `json:"exp"`
	Name       string  `json:"name"`
	Desc       string  `json:"desc"`
	Price      int64   `json:"price"`
	PriceText  string  `json:"priceText"`
	Mode       string  `json:"mode"`
	Min        int     `json:"min"`
	Max        int     `json:"max"`
	Weekdays   []int64 `json:"weekdays"`
	StartAfter string  `json:"startAfter"`
}

func (a *App) handleBookPage(w http.ResponseWriter, r *http.Request) {
	p := a.page(r, "nav.book")
	set, _ := LoadSettings(r.Context(), a.DB)
	p.Set = set
	c := a.catalog(r)
	var exps []map[string]any
	for _, e := range c.Experiences {
		if e.Status == "active" {
			exps = append(exps, map[string]any{"id": e.ID, "name": p.L.Pick(e.NameTH, e.NameEN), "summary": p.L.Pick(e.SummaryTH, e.SummaryEN), "duration": e.DurationMin})
		}
	}
	var tickets []ticketJS
	for _, t := range c.Tickets {
		pt := Baht(t.Price)
		if t.PriceMode == "per_person" {
			pt += " " + p.L.T("price.per")
		} else {
			pt += " · " + p.L.T("price.for", t.MinParticipants)
		}
		exp := int64(0)
		if t.ExperienceID.Valid {
			exp = t.ExperienceID.Int64
		}
		wd := t.AllowedWeekdays
		if wd == nil {
			wd = []int64{}
		}
		tickets = append(tickets, ticketJS{t.ID, exp, p.L.Pick(t.NameTH, t.NameEN), p.L.Pick(t.DescTH, t.DescEN), t.Price, pt, t.PriceMode, t.MinParticipants, t.MaxParticipants, wd, t.StartAfter})
	}
	q := r.URL.Query()
	p.Data = map[string]any{
		"Config": map[string]any{
			"lang": p.L, "experiences": exps, "tickets": tickets, "horizon": set.Int("booking_horizon_days"),
			"hold": set.Int("hold_minutes"), "preProduct": q.Get("product"), "prePromo": strings.ToUpper(q.Get("promo")), "preDate": q.Get("date"),
			"preVoucher": strings.ToUpper(q.Get("voucher")), "preSlot": parseInt64(q.Get("slot")), "simulated": a.Simulated(),
			"terms": p.L.Prefix() + "/terms", "cancel": p.L.Prefix() + "/cancellation",
			"t": map[string]string{
				"left": p.L.T("book.left"), "full": p.L.T("book.full"), "closed": p.L.T("book.closed"), "loading": p.L.T("book.loading"),
				"noslots": p.L.T("book.noslots"), "pickdate": p.L.T("book.pickdate"), "paynow": p.L.T("book.paynow"), "confirm": p.L.T("book.confirm"),
				"credits": p.L.T("book.credits"), "generic": p.L.T("book.err.generic", set.Str("phone")), "name": p.L.T("err.name"), "phone": p.L.T("err.phone"),
				"email": p.L.T("err.email"), "agree": p.L.T("err.agree"), "health": p.L.Pick("กรุณายืนยันข้อมูลสุขภาพก่อนจอง", "Please confirm the health statement to continue."),
				"pickTime": p.L.Pick("เลือกรอบเวลาก่อน", "Please choose a time first."), "maxGroup": p.L.Pick("จองได้สูงสุด %d คนต่อการจอง", "Up to %d guests per booking"),
				"spotsLeft": p.L.Pick("รอบนี้ว่าง %d ที่", "%d spots left in this session"), "minGroup": p.L.Pick("ขั้นต่ำ %d คน", "Minimum %d guests"),
				"applied": p.L.Pick("ใช้โค้ดแล้ว", "Code applied"), "redirecting": p.L.Pick("กำลังไปหน้าชำระเงิน…", "Taking you to payment…"),
				"offline":     p.L.Pick("เชื่อมต่ออินเทอร์เน็ตไม่ได้ ลองใหม่อีกครั้ง", "Can't reach the server. Check your connection and try again."),
				"unavailable": p.L.Pick("วันนี้ไม่เปิดรับจองสำหรับแบบที่เลือก", "Not available for this ticket on this day"),
				"perPerson":   p.L.T("price.per"),
			},
		},
	}
	a.render(w, r, "book", 200, p)
}

// accessPart extracts the t= or s= value from an access query string built by bookingAccess.
func accessPart(qs, key string) string {
	v, err := url.ParseQuery(qs)
	if err != nil {
		return ""
	}
	return v.Get(key)
}

func cookieVal(r *http.Request, name string) string {
	if c, err := r.Cookie(name); err == nil {
		return c.Value
	}
	return ""
}

func parseInt64(s string) int64 { n, _ := strconv.ParseInt(s, 10, 64); return n }

// GET /api/days?exp=&product=&from=YYYY-MM-DD&days=N → per-day availability for the calendar strip.
func (a *App) handleAPIDays(w http.ResponseWriter, r *http.Request) {
	if !a.allow(r, "api-read", 120, time.Minute) {
		writeJSON(w, 429, map[string]any{"error": "rate"})
		return
	}
	ctx := r.Context()
	set, _ := LoadSettings(ctx, a.DB)
	exp := parseInt64(r.URL.Query().Get("exp"))
	from, err := ParseDate(r.URL.Query().Get("from"))
	if err != nil || from.Before(DateOnly(Now())) {
		from = DateOnly(Now())
	}
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	horizonEnd := DateOnly(Now()).AddDate(0, 0, set.Int("booking_horizon_days")+1)
	if days <= 0 || days > 62 {
		days = 31
	}
	to := from.AddDate(0, 0, days)
	if to.After(horizonEnd) {
		to = horizonEnd
	}
	var prod *Product
	if pid := parseInt64(r.URL.Query().Get("product")); pid > 0 {
		if pp, err := GetProduct(ctx, a.DB, pid); err == nil {
			prod = &pp
		}
	}
	if err := EnsureSlots(ctx, a.DB, from, to); err != nil {
		a.jsonError(w, r, err)
		return
	}
	slots, err := SlotsBetween(ctx, a.DB, exp, from, to)
	if err != nil {
		a.jsonError(w, r, err)
		return
	}
	earliest := Now().Add(time.Duration(set.Int("min_lead_minutes")) * time.Minute)
	type day struct {
		Date      string `json:"date"`
		Status    string `json:"status"` // open | full | closed
		Remaining int    `json:"remaining"`
	}
	byDate := map[string]*day{}
	var out []*day
	for d := from; d.Before(to); d = d.AddDate(0, 0, 1) {
		x := &day{Date: d.Format("2006-01-02"), Status: "closed"}
		byDate[x.Date] = x
		out = append(out, x)
	}
	for _, s := range slots {
		x := byDate[s.StartsAt.In(BKK).Format("2006-01-02")]
		if x == nil || s.Status != "open" || s.StartsAt.Before(earliest) || (prod != nil && !prod.FitsSlot(s)) {
			continue
		}
		if x.Status == "closed" {
			x.Status = "full"
		}
		if s.Remaining() > 0 {
			x.Status = "open"
			x.Remaining += s.Remaining()
		}
	}
	writeJSON(w, 200, map[string]any{"days": out})
}

// GET /api/slots?exp=&date=&product= → sessions for one day.
func (a *App) handleAPISlots(w http.ResponseWriter, r *http.Request) {
	if !a.allow(r, "api-read", 120, time.Minute) {
		writeJSON(w, 429, map[string]any{"error": "rate"})
		return
	}
	ctx := r.Context()
	set, _ := LoadSettings(ctx, a.DB)
	d, err := ParseDate(r.URL.Query().Get("date"))
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": "date"})
		return
	}
	exp := parseInt64(r.URL.Query().Get("exp"))
	var prod *Product
	if pid := parseInt64(r.URL.Query().Get("product")); pid > 0 {
		if pp, err := GetProduct(ctx, a.DB, pid); err == nil {
			prod = &pp
		}
	}
	EnsureSlots(ctx, a.DB, d, d)
	slots, err := SlotsBetween(ctx, a.DB, exp, d, d.AddDate(0, 0, 1))
	if err != nil {
		a.jsonError(w, r, err)
		return
	}
	earliest := Now().Add(time.Duration(set.Int("min_lead_minutes")) * time.Minute)
	type sj struct {
		ID        int64  `json:"id"`
		Time      string `json:"time"`
		End       string `json:"end"`
		Remaining int    `json:"remaining"`
		Capacity  int    `json:"capacity"`
		Bookable  bool   `json:"bookable"`
	}
	var out []sj
	for _, s := range slots {
		if s.Status != "open" || (prod != nil && !prod.FitsSlot(s)) {
			continue
		}
		out = append(out, sj{s.ID, HM(s.StartsAt), HM(s.EndsAt), s.Remaining(), s.Capacity, s.Remaining() > 0 && !s.StartsAt.Before(earliest)})
	}
	writeJSON(w, 200, map[string]any{"slots": out})
}

type bookingInput struct {
	SlotID       int64  `json:"slot_id"`
	ProductID    int64  `json:"product_id"`
	Participants int    `json:"participants"`
	Promo        string `json:"promo"`
	Voucher      string `json:"voucher"`
	Name         string `json:"name"`
	Phone        string `json:"phone"`
	Email        string `json:"email"`
	Note         string `json:"note"`
	Consent      bool   `json:"consent"`
	Agree        bool   `json:"agree"`
	Health       bool   `json:"health"`
	Lang         string `json:"lang"`
	Idempotency  string `json:"idempotency_key"`
}

func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(v)
}

func (a *App) handleAPIQuote(w http.ResponseWriter, r *http.Request) {
	if !a.checkCSRF(r) {
		writeJSON(w, 403, map[string]any{"error": "csrf", "message": langOf(r).T("err.csrf")})
		return
	}
	if !a.allow(r, "quote", 40, time.Minute) {
		writeJSON(w, 429, map[string]any{"error": "rate", "message": Lang(r.Header.Get("X-Lang")).T("err.rate")})
		return
	}
	var in bookingInput
	if err := readJSON(r, &in); err != nil {
		writeJSON(w, 400, map[string]any{"error": "bad_json"})
		return
	}
	slot, err := GetSlot(r.Context(), a.DB, in.SlotID, false)
	if err != nil {
		a.jsonError(w, r, uerr("slot_closed"))
		return
	}
	qt, err := BuildQuote(r.Context(), a.DB, QuoteInput{ProductID: in.ProductID, Slot: slot, Participants: in.Participants,
		PromoCode: in.Promo, PassCode: in.Voucher, Phone: NormPhone(in.Phone), Email: NormEmail(in.Email), Channel: "web"})
	if err != nil {
		a.jsonError(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"subtotal": qt.Subtotal, "subtotalText": Baht(qt.Subtotal), "discount": qt.Discount, "discountText": Baht(qt.Discount),
		"credits": qt.CreditsApplied, "creditsText": Baht(qt.CreditsValue), "total": qt.Total, "totalText": Baht(qt.Total),
		"promo": qt.PromoCode, "voucher": qt.PassCode, "remaining": slot.Remaining(),
	})
}

func (a *App) handleAPICreateBooking(w http.ResponseWriter, r *http.Request) {
	l := Lang(r.Header.Get("X-Lang"))
	if l != EN {
		l = TH
	}
	if !a.checkCSRF(r) {
		writeJSON(w, 403, map[string]any{"error": "csrf", "message": l.T("err.csrf")})
		return
	}
	if !a.allow(r, "book", 8, 10*time.Minute) {
		writeJSON(w, 429, map[string]any{"error": "rate", "message": l.T("err.rate")})
		return
	}
	var in bookingInput
	if err := readJSON(r, &in); err != nil {
		writeJSON(w, 400, map[string]any{"error": "bad_json"})
		return
	}
	if !in.Agree || !in.Health {
		a.jsonError(w, r, uerr("agree"))
		return
	}
	at := attributionOf(r)
	res, err := a.CreateBooking(r.Context(), BookingRequest{
		SlotID: in.SlotID, ProductID: in.ProductID, Participants: in.Participants, Name: in.Name, Phone: in.Phone, Email: in.Email,
		Lang: string(l), Consent: in.Consent, PromoCode: in.Promo, PassCode: in.Voucher, CustomerNote: in.Note,
		IdempotencyKey: clip(in.Idempotency, 64), Channel: "web",
		UTMSource: at.Source, UTMMedium: at.Medium, UTMCampaign: at.Campaign, Referrer: at.Referrer, LandingPath: at.Landing, VisitorID: at.Visitor,
		AdsConsent: cookieVal(r, "pd_consent") == "all",
	})
	if err != nil {
		a.jsonError(w, r, err)
		return
	}
	access := "t=" + url.QueryEscape(res.ManageToken)
	if res.ManageToken == "" {
		access = "s=" + a.refSig(res.Ref)
	}
	manage := l.Prefix() + "/booking/" + res.Ref + "?" + access
	a.recordEvent(r, "checkout_start", res.Total)
	if res.Status == "hold" {
		u, err := a.StartBookingCheckout(r.Context(), res.BookingID, l, res.ManageToken)
		if err != nil {
			a.Log.Error("checkout start failed", "ref", res.Ref, "err", err)
			writeJSON(w, 502, map[string]any{"error": "payment", "message": l.T("book.err.generic", ""), "manage": manage})
			return
		}
		writeJSON(w, 200, map[string]any{"ref": res.Ref, "status": res.Status, "redirect": u, "manage": manage})
		return
	}
	writeJSON(w, 200, map[string]any{"ref": res.Ref, "status": res.Status, "redirect": manage, "manage": manage})
}

// ---------- booking confirmation / manage ----------

// bookingAccess authorises the visitor for a booking: one-time token, signed email link, or signed-in customer.
func (a *App) bookingAccess(r *http.Request, v BookingView) (ok bool, accessQS string) {
	var tokHash string
	a.DB.QueryRowContext(r.Context(), `SELECT manage_token_hash FROM bookings WHERE id=$1`, v.ID).Scan(&tokHash)
	if t := r.FormValue("t"); t != "" && ConstEq(HashToken(t), tokHash) {
		return true, "t=" + url.QueryEscape(t)
	}
	if s := r.FormValue("s"); s != "" && ConstEq(s, a.refSig(v.Ref)) {
		return true, "s=" + url.QueryEscape(s)
	}
	if cid := a.customerID(r); cid > 0 && cid == v.CustomerID {
		return true, "s=" + a.refSig(v.Ref)
	}
	return false, ""
}

func (a *App) loadBookingForCustomer(w http.ResponseWriter, r *http.Request) (BookingView, string, bool) {
	v, err := GetBookingByRef(r.Context(), a.DB, r.PathValue("ref"))
	if err != nil {
		a.handleNotFound(w, r)
		return v, "", false
	}
	ok, qs := a.bookingAccess(r, v)
	if !ok {
		a.handleNotFound(w, r)
		return v, "", false
	}
	return v, qs, true
}

func (a *App) handleBookingPage(w http.ResponseWriter, r *http.Request) {
	v, qs, ok := a.loadBookingForCustomer(w, r)
	if !ok {
		return
	}
	p := a.page(r, "")
	p.Title = p.L.T("conf.ref") + " " + v.Ref + " · Peaceful Day"
	p.NoIndex = true
	set, _ := LoadSettings(r.Context(), a.DB)
	p.Set = set
	canCancel := CanCustomerChange(set, v, "cancel") == nil
	canMove := CanCustomerChange(set, v, "reschedule") == nil
	if v.Status == "confirmed" && v.PaymentState == "paid" && r.URL.Query().Get("paid") == "1" {
		var sent bool
		// First view after payment: fire the browser purchase event once (de-duplicated by ref).
		if a.DB.QueryRowContext(r.Context(), `UPDATE bookings SET conversion_sent=TRUE WHERE id=$1 AND conversion_sent=FALSE RETURNING TRUE`, v.ID).Scan(&sent) == nil && sent {
			p.Purchase = &PurchaseEvent{ID: v.Ref, Value: float64(v.Total) / 100}
			a.recordEvent(r, "purchase", v.Total)
		}
	}
	p.Flash = map[string]string{"moved": p.L.T("conf.moved"), "cancelled": p.L.T("conf.cancelled")}[r.URL.Query().Get("done")]
	if e := r.URL.Query().Get("err"); e != "" {
		p.FlashErr = p.L.T("err." + e)
	}
	var credit string
	if v.Status == "cancelled" {
		a.DB.QueryRowContext(r.Context(), `SELECT code FROM passes WHERE note=$1`, creditNote(v.ID)).Scan(&credit)
	}
	p.Data = map[string]any{"B": v, "QS": qs, "QRSig": a.qrSig(v.Ref), "CanCancel": canCancel, "CanMove": canMove, "Credit": credit, "PayCancelled": r.URL.Query().Get("cancelled") == "1",
		"MoveCutoff": set.Int("reschedule_cutoff_hours"), "Token": accessPart(qs, "t"), "Sig": accessPart(qs, "s"),
		"Cutoff": set.Int("cancel_cutoff_hours"), "RefundMode": set.Str("cancel_refund_mode"),
		"Waiting": v.Status == "hold" && r.URL.Query().Get("paid") == "1"}
	a.render(w, r, "booking", 200, p)
}

func (a *App) handleBookingStatus(w http.ResponseWriter, r *http.Request) {
	v, err := GetBookingByRef(r.Context(), a.DB, r.PathValue("ref"))
	if err != nil {
		writeJSON(w, 404, map[string]any{})
		return
	}
	if ok, _ := a.bookingAccess(r, v); !ok {
		writeJSON(w, 404, map[string]any{})
		return
	}
	writeJSON(w, 200, map[string]any{"status": v.Status, "payment": v.PaymentState})
}

func (a *App) handleResumePayment(w http.ResponseWriter, r *http.Request) {
	v, qs, ok := a.loadBookingForCustomer(w, r)
	if !ok {
		return
	}
	l := langOf(r)
	if v.Status != "hold" {
		http.Redirect(w, r, l.Prefix()+"/booking/"+v.Ref+"?"+qs, http.StatusSeeOther)
		return
	}
	u, err := a.StartBookingCheckout(r.Context(), v.ID, l, r.FormValue("t"))
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, u, http.StatusSeeOther)
}

func (a *App) handleCustomerCancel(w http.ResponseWriter, r *http.Request) {
	if !a.checkCSRF(r) || !a.allow(r, "change", 10, 10*time.Minute) {
		a.handleNotFound(w, r)
		return
	}
	v, qs, ok := a.loadBookingForCustomer(w, r)
	if !ok {
		return
	}
	l := langOf(r)
	_, err := a.CancelBooking(r.Context(), v.ID, ChangeActor{Customer: true, IP: clientIP(r)}, CancelOptions{Reason: "customer cancelled online", Refund: "auto"})
	dest := l.Prefix() + "/booking/" + v.Ref + "?" + qs
	var ue UserError
	if errors.As(err, &ue) {
		http.Redirect(w, r, dest+"&err="+ue.Code, http.StatusSeeOther)
		return
	} else if err != nil {
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, dest+"&done=cancelled", http.StatusSeeOther)
}

func (a *App) handleCustomerReschedule(w http.ResponseWriter, r *http.Request) {
	if !a.checkCSRF(r) || !a.allow(r, "change", 10, 10*time.Minute) {
		a.handleNotFound(w, r)
		return
	}
	v, qs, ok := a.loadBookingForCustomer(w, r)
	if !ok {
		return
	}
	l := langOf(r)
	err := a.RescheduleBooking(r.Context(), v.ID, parseInt64(r.FormValue("slot_id")), ChangeActor{Customer: true, IP: clientIP(r)}, false)
	dest := l.Prefix() + "/booking/" + v.Ref + "?" + qs
	var ue UserError
	if errors.As(err, &ue) {
		http.Redirect(w, r, dest+"&err="+ue.Code, http.StatusSeeOther)
		return
	} else if err != nil {
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, dest+"&done=moved", http.StatusSeeOther)
}

func icsEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, ";", `\;`, ",", `\,`, "\n", `\n`).Replace(s)
}

func (a *App) handleICS(w http.ResponseWriter, r *http.Request) {
	v, _, ok := a.loadBookingForCustomer(w, r)
	if !ok {
		return
	}
	set, _ := LoadSettings(r.Context(), a.DB)
	l := langOf(r)
	f := "20060102T150405Z"
	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=peaceful-day-"+v.Ref+".ics")
	fmt.Fprintf(w, "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//Peaceful Day//Booking//EN\r\nBEGIN:VEVENT\r\nUID:%s@peacefulday\r\nDTSTAMP:%s\r\nDTSTART:%s\r\nDTEND:%s\r\nSUMMARY:%s\r\nLOCATION:%s\r\nDESCRIPTION:%s\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n",
		v.Ref, time.Now().UTC().Format(f), v.StartsAt.UTC().Format(f), v.EndsAt.UTC().Format(f),
		icsEscape("Peaceful Day · "+l.Pick(v.ExpNameTH, v.ExpNameEN)), icsEscape(l.Pick(set.Str("address_th"), set.Str("address_en"))),
		icsEscape(v.Ref+" · "+strconv.Itoa(v.Participants)+" · "+l.T("conf.arrive")))
}

// ---------- QR codes ----------

func (a *App) qrSig(ref string) string { return Sign(a.Cfg.SessionSecret, "qr:"+ref)[:12] }

// CheckinPayload is what the QR encodes: a short signed string staff scanners verify.
func (a *App) CheckinPayload(ref string) string { return "PD1:" + ref + ":" + a.qrSig(ref) }

func (a *App) handleQR(w http.ResponseWriter, r *http.Request) {
	ref := strings.ToUpper(r.PathValue("ref"))
	content := ""
	switch {
	case strings.HasPrefix(ref, "PD-") && ConstEq(r.URL.Query().Get("s"), a.qrSig(ref)):
		content = a.CheckinPayload(ref)
	case strings.HasPrefix(ref, "PDV-") && ConstEq(r.URL.Query().Get("s"), a.qrSig(ref)):
		content = ref // voucher code
	default:
		http.NotFound(w, r)
		return
	}
	png, err := qrcode.Encode(content, qrcode.Medium, 320)
	if err != nil {
		http.Error(w, "qr", 500)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Write(png)
}

func (a *App) handleImage(w http.ResponseWriter, r *http.Request) {
	var data []byte
	var mime string
	if err := a.DB.QueryRowContext(r.Context(), `SELECT data, mime FROM gallery_images WHERE id=$1`, parseInt64(r.PathValue("id"))).Scan(&data, &mime); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "public, max-age=604800")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}

// ---------- My bookings (guest lookup + passwordless account) ----------

func (a *App) customerID(r *http.Request) int64 {
	c, err := r.Cookie("pd_cust")
	if err != nil || c.Value == "" {
		return 0
	}
	var id int64
	if a.DB.QueryRowContext(r.Context(), `SELECT customer_id FROM customer_logins WHERE token_hash=$1 AND kind='session' AND expires_at>now()`, HashToken(c.Value)).Scan(&id) != nil {
		return 0
	}
	return id
}

type PassView struct {
	Code               string
	NameTH, NameEN     string
	CreditsTotal, Used int
	Status             string
	ExpiresAt          sql.NullTime
	GiftTo             string
}

func (a *App) passesFor(r *http.Request, custID int64) []PassView {
	rows, err := a.DB.QueryContext(r.Context(), `SELECT ps.code, p.name_th, p.name_en, ps.credits_total, ps.credits_used, ps.status, ps.expires_at, ps.gift_to_name
		FROM passes ps JOIN products p ON p.id=ps.product_id WHERE ps.customer_id=$1 AND ps.status IN ('active','expired') ORDER BY ps.id DESC`, custID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []PassView
	for rows.Next() {
		var v PassView
		rows.Scan(&v.Code, &v.NameTH, &v.NameEN, &v.CreditsTotal, &v.Used, &v.Status, &v.ExpiresAt, &v.GiftTo)
		out = append(out, v)
	}
	return out
}

func (a *App) handleMyBookings(w http.ResponseWriter, r *http.Request) {
	p := a.page(r, "nav.my")
	p.NoIndex = true
	data := map[string]any{"Sent": r.URL.Query().Get("sent") == "1", "NotFound": r.URL.Query().Get("nf") == "1"}
	if cid := a.customerID(r); cid > 0 {
		var name string
		a.DB.QueryRowContext(r.Context(), `SELECT name FROM customers WHERE id=$1`, cid).Scan(&name)
		up, _ := ListBookings(r.Context(), a.DB, `WHERE b.customer_id=$1 AND s.starts_at >= now() - interval '2 hours' AND b.status IN ('hold','confirmed','checked_in') ORDER BY s.starts_at`, cid)
		past, _ := ListBookings(r.Context(), a.DB, `WHERE b.customer_id=$1 AND NOT (s.starts_at >= now() - interval '2 hours' AND b.status IN ('hold','confirmed','checked_in')) ORDER BY s.starts_at DESC LIMIT 50`, cid)
		data["Name"], data["Upcoming"], data["Past"], data["Passes"], data["Signed"] = name, up, past, a.passesFor(r, cid), true
		sigs := map[string]string{}
		for _, b := range append(up, past...) {
			sigs[b.Ref] = a.refSig(b.Ref)
		}
		data["Sigs"] = sigs
	}
	p.Data = data
	a.render(w, r, "my", 200, p)
}

func (a *App) handleMagicLinkRequest(w http.ResponseWriter, r *http.Request) {
	l := langOf(r)
	if !a.checkCSRF(r) {
		http.Redirect(w, r, l.Prefix()+"/my-bookings", http.StatusSeeOther)
		return
	}
	email := NormEmail(r.FormValue("email"))
	if a.allow(r, "magic", 5, 30*time.Minute) && a.limit.Allow("magic-email|"+email, 3, time.Hour) && ValidEmail(email) {
		var cid int64
		var lang string
		if a.DB.QueryRowContext(r.Context(), `SELECT id, lang FROM customers WHERE email=$1 ORDER BY id LIMIT 1`, email).Scan(&cid, &lang) == nil {
			tok := RandomToken()
			if _, err := a.DB.ExecContext(r.Context(), `INSERT INTO customer_logins(token_hash,customer_id,kind,expires_at) VALUES($1,$2,'magic',now()+interval '30 minutes')`, HashToken(tok), cid); err == nil {
				link := a.Cfg.BaseURL + l.Prefix() + "/my-bookings/login?token=" + url.QueryEscape(tok)
				body := l.T("my.p") + "\n\n" + link + "\n\n" + map[Lang]string{TH: "ลิงก์นี้ใช้ได้ 30 นาที", EN: "This link works for 30 minutes."}[l]
				a.enqueue(r.Context(), a.DB, "magic_link", "email", email, l.T("my.h")+" · Peaceful Day", body, nil, 0, "")
			}
		}
	}
	// Same response whether or not the email exists, so it can't be used to discover customers.
	http.Redirect(w, r, l.Prefix()+"/my-bookings?sent=1", http.StatusSeeOther)
}

func (a *App) handleMagicLogin(w http.ResponseWriter, r *http.Request) {
	l := langOf(r)
	tok := r.URL.Query().Get("token")
	var cid int64
	err := a.DB.QueryRowContext(r.Context(), `UPDATE customer_logins SET used_at=now() WHERE token_hash=$1 AND kind='magic' AND used_at IS NULL AND expires_at>now() RETURNING customer_id`, HashToken(tok)).Scan(&cid)
	if err != nil {
		http.Redirect(w, r, l.Prefix()+"/my-bookings?nf=1", http.StatusSeeOther)
		return
	}
	session := RandomToken()
	a.DB.ExecContext(r.Context(), `INSERT INTO customer_logins(token_hash,customer_id,kind,expires_at) VALUES($1,$2,'session',now()+interval '30 days')`, HashToken(session), cid)
	a.setCookie(w, "pd_cust", session, 30*24*time.Hour, true)
	http.Redirect(w, r, l.Prefix()+"/my-bookings", http.StatusSeeOther)
}

func (a *App) handleCustomerLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("pd_cust"); err == nil {
		a.DB.ExecContext(r.Context(), `DELETE FROM customer_logins WHERE token_hash=$1`, HashToken(c.Value))
	}
	a.setCookie(w, "pd_cust", "", -time.Hour, true)
	http.Redirect(w, r, langOf(r).Prefix()+"/my-bookings", http.StatusSeeOther)
}

func (a *App) handleFindBooking(w http.ResponseWriter, r *http.Request) {
	l := langOf(r)
	if !a.checkCSRF(r) || !a.allow(r, "find", 10, 10*time.Minute) {
		http.Redirect(w, r, l.Prefix()+"/my-bookings?nf=1", http.StatusSeeOther)
		return
	}
	v, err := GetBookingByRef(r.Context(), a.DB, r.FormValue("ref"))
	phone := NormPhone(r.FormValue("phone"))
	if err != nil || phone == "" || !ConstEq(v.Phone, phone) {
		http.Redirect(w, r, l.Prefix()+"/my-bookings?nf=1", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, l.Prefix()+"/booking/"+v.Ref+"?s="+a.refSig(v.Ref), http.StatusSeeOther)
}

// ---------- packages / gift vouchers ----------

func (a *App) handlePassBuyPage(w http.ResponseWriter, r *http.Request) {
	pr, err := GetProduct(r.Context(), a.DB, parseInt64(r.PathValue("id")))
	if err != nil || pr.Kind != "pass" || !pr.Public || !pr.SellableNow() || pr.Price <= 0 {
		a.handleNotFound(w, r)
		return
	}
	p := a.page(r, "pass.h")
	p.Data = map[string]any{"P": pr, "Gift": r.URL.Query().Get("gift") == "1", "Idem": RandomToken()}
	a.render(w, r, "pass_buy", 200, p)
}

func (a *App) handlePassBuy(w http.ResponseWriter, r *http.Request) {
	l := langOf(r)
	if !a.checkCSRF(r) {
		a.handleNotFound(w, r)
		return
	}
	pr, err := GetProduct(r.Context(), a.DB, parseInt64(r.PathValue("id")))
	if err != nil {
		a.handleNotFound(w, r)
		return
	}
	fail := func(code string) {
		p := a.page(r, "pass.h")
		p.FlashErr = l.T("err." + code)
		p.Data = map[string]any{"P": pr, "Gift": r.FormValue("gift") == "on", "Idem": RandomToken(), "Form": r.PostForm}
		a.render(w, r, "pass_buy", 422, p)
	}
	if !a.allow(r, "passbuy", 6, 10*time.Minute) {
		fail("rate")
		return
	}
	if r.FormValue("agree") != "on" {
		fail("agree")
		return
	}
	at := attributionOf(r)
	gift := r.FormValue("gift") == "on"
	giftTo, giftMsg := "", ""
	if gift {
		giftTo, giftMsg = r.FormValue("gift_to"), r.FormValue("gift_message")
	}
	oid, ref, tok, err := a.CreatePassOrder(r.Context(), PassOrderRequest{ProductID: pr.ID, Name: r.FormValue("name"), Phone: r.FormValue("phone"), Email: r.FormValue("email"),
		Lang: string(l), Consent: r.FormValue("consent") == "on", GiftTo: giftTo, GiftMessage: giftMsg, IdempotencyKey: clip(r.FormValue("idem"), 64),
		UTMSource: at.Source, UTMMedium: at.Medium, UTMCampaign: at.Campaign, VisitorID: at.Visitor})
	var ue UserError
	if errors.As(err, &ue) {
		fail(ue.Code)
		return
	} else if err != nil {
		a.serverError(w, r, err)
		return
	}
	if tok == "" { // replay
		http.Redirect(w, r, l.Prefix()+"/voucher/"+ref+"?s="+a.refSig(ref), http.StatusSeeOther)
		return
	}
	u, err := a.StartPassCheckout(r.Context(), oid, l, tok)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, u, http.StatusSeeOther)
}

func (a *App) handleVoucherPage(w http.ResponseWriter, r *http.Request) {
	ref := strings.ToUpper(r.PathValue("ref"))
	var v struct {
		Status, TokHash, Code, NameTH, NameEN, GiftTo, GiftMsg, PassStatus string
		Amount                                                             int64
		Credits                                                            int
		Exp                                                                sql.NullTime
	}
	err := a.DB.QueryRowContext(r.Context(), `SELECT o.status, o.manage_token_hash, ps.code, p.name_th, p.name_en, ps.gift_to_name, ps.gift_message, ps.status, o.amount, ps.credits_total, ps.expires_at
		FROM pass_orders o JOIN passes ps ON ps.id=o.pass_id JOIN products p ON p.id=ps.product_id WHERE o.ref=$1`, ref).
		Scan(&v.Status, &v.TokHash, &v.Code, &v.NameTH, &v.NameEN, &v.GiftTo, &v.GiftMsg, &v.PassStatus, &v.Amount, &v.Credits, &v.Exp)
	t, s := r.URL.Query().Get("t"), r.URL.Query().Get("s")
	if err != nil || !((t != "" && ConstEq(HashToken(t), v.TokHash)) || (s != "" && ConstEq(s, a.refSig(ref)))) {
		a.handleNotFound(w, r)
		return
	}
	p := a.page(r, "pass.h")
	p.NoIndex = true
	data := map[string]any{"V": v, "Ref": ref, "QRSig": a.qrSig(v.Code)}
	if v.Status == "paid" && r.URL.Query().Get("paid") == "1" {
		p.Purchase = &PurchaseEvent{ID: ref, Value: float64(v.Amount) / 100}
	}
	p.Data = data
	a.render(w, r, "voucher", 200, p)
}

// ---------- consent & first-party events ----------

func (a *App) handleConsent(w http.ResponseWriter, r *http.Request) {
	if !a.checkCSRF(r) {
		writeJSON(w, 403, map[string]any{})
		return
	}
	v := "essential"
	if r.FormValue("choice") == "all" {
		v = "all"
	}
	a.setCookie(w, "pd_consent", v, 180*24*time.Hour, false)
	writeJSON(w, 200, map[string]any{"consent": v})
}

func (a *App) recordEvent(r *http.Request, typ string, value int64) {
	at := attributionOf(r)
	if at.Visitor == "" {
		return
	}
	host := ""
	if u, err := url.Parse(r.Referer()); err == nil {
		host = u.Hostname()
	}
	path := stripLang(r.URL.Path)
	if strings.HasPrefix(r.URL.Path, "/api/") || typ != "page_view" {
		// events posted from the browser describe the page that sent them
		path = ""
		if u, err := url.Parse(r.Referer()); err == nil {
			path = stripLang(u.Path)
		}
	}
	a.DB.ExecContext(r.Context(), `INSERT INTO web_events(visitor_id,type,path,utm_source,utm_medium,utm_campaign,referrer_host,value) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`,
		at.Visitor, typ, clip(path, 200), at.Source, at.Medium, at.Campaign, clip(host, 120), value)
}

// POST /api/event {type: page_view|booking_start}. Anonymous, no personal data.
func (a *App) handleWebEvent(w http.ResponseWriter, r *http.Request) {
	if !a.checkCSRF(r) || !a.allow(r, "event", 60, time.Minute) {
		w.WriteHeader(204)
		return
	}
	var in struct {
		Type string `json:"type"`
	}
	readJSON(r, &in)
	if in.Type == "page_view" || in.Type == "booking_start" {
		a.recordEvent(r, in.Type, 0)
	}
	w.WriteHeader(204)
}

// ---------- payments: webhooks & simulated checkout ----------

func (a *App) handleStripeWebhook(w http.ResponseWriter, r *http.Request) {
	if a.Pay.Name() != "stripe" {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read", 400)
		return
	}
	ev, err := a.Pay.ParseWebhook(r, body)
	if err != nil {
		a.Log.Warn("stripe webhook rejected", "err", err, "ip", clientIP(r))
		http.Error(w, "invalid", 400)
		return
	}
	if err := a.ProcessPaymentEvent(r.Context(), ev); err != nil {
		a.Log.Error("stripe webhook processing failed", "event", ev.EventID, "err", err)
		http.Error(w, "retry", 500) // Stripe retries with backoff
		return
	}
	writeJSON(w, 200, map[string]any{"received": true})
}

type simInfo struct {
	Session, Ref, Desc, Kind string
	Amount                   int64
	Status                   string
	Lang                     Lang
}

func (a *App) simLookup(r *http.Request) (simInfo, error) {
	var s simInfo
	s.Session = r.PathValue("session")
	var bookingRef, orderRef sql.NullString
	var lang sql.NullString
	err := a.DB.QueryRowContext(r.Context(), `SELECT p.amount, p.status, b.ref, o.ref, COALESCE(c1.lang, c2.lang)
		FROM payments p LEFT JOIN bookings b ON b.id=p.booking_id LEFT JOIN pass_orders o ON o.id=p.pass_order_id
		LEFT JOIN customers c1 ON c1.id=b.customer_id LEFT JOIN customers c2 ON c2.id=o.customer_id
		WHERE p.provider='simulated' AND p.provider_session=$1`, s.Session).Scan(&s.Amount, &s.Status, &bookingRef, &orderRef, &lang)
	if err != nil {
		return s, err
	}
	s.Lang = Lang(lang.String)
	if bookingRef.Valid {
		s.Ref, s.Kind = bookingRef.String, "booking"
	} else {
		s.Ref, s.Kind = orderRef.String, "voucher"
	}
	return s, nil
}

func (a *App) handleSimPage(w http.ResponseWriter, r *http.Request) {
	sp, ok := a.Pay.(*SimulatedProvider)
	if !ok {
		http.NotFound(w, r)
		return
	}
	_ = sp
	s, err := a.simLookup(r)
	if err != nil {
		a.handleNotFound(w, r)
		return
	}
	p := &Page{L: s.Lang, Title: "Test payment · Peaceful Day", NoIndex: true, Data: s}
	a.render(w, r, "sim_pay", 200, p)
}

func (a *App) handleSimAction(w http.ResponseWriter, r *http.Request) {
	sp, ok := a.Pay.(*SimulatedProvider)
	if !ok || !a.checkCSRF(r) {
		http.NotFound(w, r)
		return
	}
	s, err := a.simLookup(r)
	if err != nil {
		a.handleNotFound(w, r)
		return
	}
	back := s.Lang.Prefix() + "/" + s.Kind + "/" + s.Ref + "?s=" + a.refSig(s.Ref)
	action := r.FormValue("action")
	if action == "cancel" {
		http.Redirect(w, r, back+"&cancelled=1", http.StatusSeeOther)
		return
	}
	ev := PaymentEvent{EventID: "simev_" + RandomCode(12), SessionID: s.Session, Amount: s.Amount, Currency: "thb"}
	switch action {
	case "promptpay", "card":
		ev.Type, ev.Method, ev.PaymentRef = "paid", action, "simpi_"+RandomCode(12)
	case "fail":
		ev.Type = "failed"
	default:
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	body, _ := json.Marshal(ev)
	// Verify through the same signature + parse path a real webhook uses.
	req, _ := http.NewRequest("POST", "/webhooks/sim", bytes.NewReader(body))
	req.Header.Set("X-Sim-Signature", sp.SignEvent(body))
	parsed, err := sp.ParseWebhook(req, body)
	if err == nil {
		err = a.ProcessPaymentEvent(r.Context(), parsed)
	}
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if ev.Type == "paid" {
		back += "&paid=1"
	} else {
		back += "&cancelled=1"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// ---------- SEO ----------

func (a *App) handleRobots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "User-agent: *\nAllow: /\nDisallow: /admin\nDisallow: /api\nDisallow: /booking/\nDisallow: /voucher/\nDisallow: /pay/\nDisallow: /my-bookings\nSitemap: %s/sitemap.xml\n", a.Cfg.BaseURL)
}

func (a *App) handleSitemap(w http.ResponseWriter, r *http.Request) {
	paths := []string{"/", "/experiences", "/pricing", "/book", "/story", "/gallery", "/faq", "/contact", "/privacy", "/terms", "/cancellation"}
	rows, err := a.DB.QueryContext(r.Context(), `SELECT slug FROM landing_pages WHERE published`)
	if err == nil {
		for rows.Next() {
			var s string
			rows.Scan(&s)
			paths = append(paths, "/lp/"+s)
		}
		rows.Close()
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?>`+"\n"+`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9" xmlns:xhtml="http://www.w3.org/1999/xhtml">`+"\n")
	for _, p := range paths {
		en := "/en" + p
		if p == "/" {
			en = "/en"
		}
		for _, u := range []string{p, en} {
			fmt.Fprintf(w, `<url><loc>%s%s</loc><xhtml:link rel="alternate" hreflang="th" href="%s%s"/><xhtml:link rel="alternate" hreflang="en" href="%s%s"/></url>`+"\n",
				a.Cfg.BaseURL, u, a.Cfg.BaseURL, p, a.Cfg.BaseURL, en)
		}
	}
	fmt.Fprint(w, "</urlset>\n")
}
