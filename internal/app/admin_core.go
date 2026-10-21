package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"
)

// ---------- staff, roles, permissions ----------

type Staff struct {
	ID    int64
	Email string
	Name  string
	Role  string
	CSRF  string
}

var rolePerms = map[string][]string{
	"owner":      {"*"},
	"manager":    {"bookings.view", "bookings.edit", "bookings.override", "refunds", "customers.view", "customers.edit", "customers.export", "schedule", "products", "promos", "passes", "analytics.revenue", "analytics.marketing", "payments", "marketing", "settings", "audit", "notifications"},
	"front_desk": {"bookings.view", "bookings.edit", "customers.view", "customers.edit"},
	"marketing":  {"promos", "analytics.marketing", "marketing"},
	"finance":    {"bookings.view", "customers.view", "refunds", "analytics.revenue", "payments", "customers.export"},
}

var RoleNames = map[string][2]string{
	"owner": {"เจ้าของ / CEO", "Owner / CEO"}, "manager": {"ผู้จัดการ", "Manager"}, "front_desk": {"หน้าร้าน", "Front Desk"},
	"marketing": {"การตลาด", "Marketing"}, "finance": {"การเงิน", "Finance"},
}

func (s *Staff) Can(perm string) bool {
	if s == nil {
		return false
	}
	for _, p := range rolePerms[s.Role] {
		if p == "*" || p == perm {
			return true
		}
	}
	return false
}

func (s *Staff) RoleName() string { return RoleNames[s.Role][0] }

type ctxKey int

const staffKey ctxKey = 1

func staffOf(r *http.Request) *Staff {
	s, _ := r.Context().Value(staffKey).(*Staff)
	return s
}

func (a *App) currentStaff(r *http.Request) *Staff {
	c, err := r.Cookie("pd_staff")
	if err != nil || c.Value == "" {
		return nil
	}
	var s Staff
	err = a.DB.QueryRowContext(r.Context(), `SELECT u.id, u.email, u.name, u.role, ss.csrf FROM staff_sessions ss JOIN staff_users u ON u.id=ss.user_id
		WHERE ss.token_hash=$1 AND ss.expires_at>now() AND u.active`, HashToken(c.Value)).Scan(&s.ID, &s.Email, &s.Name, &s.Role, &s.CSRF)
	if err != nil {
		return nil
	}
	return &s
}

// guard wraps an admin handler: requires a session, the permission, and a CSRF token on writes.
func (a *App) guard(perm string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s := a.currentStaff(r)
		if s == nil {
			http.Redirect(w, r, "/admin/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		if perm != "" && !s.Can(perm) {
			a.adminRender(w, r.WithContext(context.WithValue(r.Context(), staffKey, s)), "admin/forbidden", 403, "ไม่มีสิทธิ์เข้าถึง", map[string]any{"Msg": "ตำแหน่ง " + s.RoleName() + " ไม่มีสิทธิ์เข้าหน้านี้"})
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if !ConstEq(r.FormValue("csrf"), s.CSRF) && !ConstEq(r.Header.Get("X-CSRF"), s.CSRF) {
				http.Error(w, "หน้านี้หมดอายุ กรุณารีเฟรชแล้วลองใหม่ (CSRF)", http.StatusForbidden)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		h(w, r.WithContext(context.WithValue(r.Context(), staffKey, s)))
	}
}

func (a *App) adminRender(w http.ResponseWriter, r *http.Request, name string, status int, title string, data any) {
	s := staffOf(r)
	p := &Page{L: TH, Title: title + " · Peaceful Day Admin", Staff: s, Data: data, NoIndex: true, Nav: name}
	if s != nil {
		p.CSRF = s.CSRF
	} else {
		p.CSRF = "-"
	}
	p.Flash = r.URL.Query().Get("ok")
	p.FlashErr = r.URL.Query().Get("err")
	if s != nil {
		p.Demo = HasDemoData(r.Context(), a.DB)
	}
	a.render(w, r, name, status, p)
}

func back(w http.ResponseWriter, r *http.Request, dest, okMsg, errMsg string) {
	q := url.Values{}
	if okMsg != "" {
		q.Set("ok", okMsg)
	}
	if errMsg != "" {
		q.Set("err", errMsg)
	}
	sep := "?"
	if strings.Contains(dest, "?") {
		sep = "&"
	}
	http.Redirect(w, r, dest+sep+q.Encode(), http.StatusSeeOther)
}

func errText(err error) string {
	var ue UserError
	if errors.As(err, &ue) {
		return TH.T("err." + ue.Code)
	}
	var pqe *pq.Error
	if errors.As(err, &pqe) {
		if pqe.Code == "23505" {
			return "ข้อมูลซ้ำกับที่มีอยู่แล้ว (" + pqe.Constraint + ")"
		}
		return "ฐานข้อมูลปฏิเสธ: " + pqe.Message
	}
	return err.Error()
}

func (a *App) adminRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /admin/login", a.handleAdminLoginPage)
	m.HandleFunc("POST /admin/login", a.handleAdminLogin)
	m.HandleFunc("POST /admin/logout", a.guard("", a.handleAdminLogout))
	m.HandleFunc("GET /admin", a.guard("", a.handleDashboard))
	m.HandleFunc("GET /admin/{$}", a.guard("", a.handleDashboard))
	m.HandleFunc("GET /admin/calendar", a.guard("bookings.view", a.handleCalendar))
	m.HandleFunc("GET /admin/bookings", a.guard("bookings.view", a.handleBookingsList))
	m.HandleFunc("GET /admin/bookings/new", a.guard("bookings.edit", a.handleBookingNewPage))
	m.HandleFunc("POST /admin/bookings/new", a.guard("bookings.edit", a.handleBookingNew))
	m.HandleFunc("GET /admin/bookings/{ref}", a.guard("bookings.view", a.handleBookingDetail))
	m.HandleFunc("POST /admin/bookings/{ref}/action", a.guard("bookings.edit", a.handleBookingAction))
	m.HandleFunc("POST /admin/bookings/{ref}/refund", a.guard("refunds", a.handleBookingRefund))
	m.HandleFunc("GET /admin/checkin", a.guard("bookings.edit", a.handleCheckinPage))
	m.HandleFunc("POST /admin/checkin", a.guard("bookings.edit", a.handleCheckinLookup))
	m.HandleFunc("GET /admin/customers", a.guard("customers.view", a.handleCustomers))
	m.HandleFunc("GET /admin/customers/{id}", a.guard("customers.view", a.handleCustomerDetail))
	m.HandleFunc("POST /admin/customers/{id}", a.guard("customers.edit", a.handleCustomerUpdate))
	m.HandleFunc("GET /admin/api/slots", a.guard("bookings.view", a.handleAdminSlotsAPI))
	a.adminManageRoutes(m)
}

// ---------- login ----------

func (a *App) handleAdminLoginPage(w http.ResponseWriter, r *http.Request) {
	if a.currentStaff(r) != nil {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	a.adminRender(w, r, "admin/login", 200, "เข้าสู่ระบบ", map[string]any{"Next": r.URL.Query().Get("next"), "CSRF": a.csrfToken(w, r)})
}

var (
	dummyOnce sync.Once
	dummyPH   string
)

func dummyHash() string {
	dummyOnce.Do(func() { dummyPH, _ = HashPassword("not-a-real-password") })
	return dummyPH
}

func (a *App) handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	fail := func(msg string) {
		a.adminRender(w, r, "admin/login", 401, "เข้าสู่ระบบ", map[string]any{"Error": msg, "Email": r.FormValue("email"), "Next": r.FormValue("next"), "CSRF": a.csrfToken(w, r)})
	}
	if !a.checkCSRF(r) {
		fail("หน้านี้หมดอายุ กรุณาลองใหม่")
		return
	}
	if !a.allow(r, "login", 10, 15*time.Minute) {
		fail("ลองเข้าสู่ระบบบ่อยเกินไป รอ 15 นาทีแล้วลองใหม่")
		return
	}
	email := NormEmail(r.FormValue("email"))
	var id int64
	var hash string
	var active bool
	var locked sql.NullTime
	err := a.DB.QueryRowContext(r.Context(), `SELECT id, password_hash, active, locked_until FROM staff_users WHERE email=$1`, email).Scan(&id, &hash, &active, &locked)
	if err != nil || !active {
		CheckPassword(dummyHash(), r.FormValue("password")) // same work as a real check, so timing doesn't reveal valid emails
		fail("อีเมลหรือรหัสผ่านไม่ถูกต้อง")
		return
	}
	if locked.Valid && locked.Time.After(time.Now()) {
		fail("บัญชีถูกล็อกชั่วคราวเพราะใส่รหัสผิดหลายครั้ง ลองใหม่ภายหลัง")
		return
	}
	if !CheckPassword(hash, r.FormValue("password")) {
		a.DB.ExecContext(r.Context(), `UPDATE staff_users SET failed_logins=failed_logins+1,
			locked_until = CASE WHEN failed_logins+1 >= 5 THEN now()+interval '15 minutes' ELSE locked_until END WHERE id=$1`, id)
		Audit(r.Context(), a.DB, id, email, "staff.login_failed", "staff", id, nil, clientIP(r))
		fail("อีเมลหรือรหัสผ่านไม่ถูกต้อง")
		return
	}
	tok := RandomToken()
	if _, err := a.DB.ExecContext(r.Context(), `INSERT INTO staff_sessions(token_hash,user_id,csrf,expires_at) VALUES($1,$2,$3,now()+interval '12 hours')`, HashToken(tok), id, RandomToken()); err != nil {
		a.serverError(w, r, err)
		return
	}
	a.DB.ExecContext(r.Context(), `UPDATE staff_users SET failed_logins=0, locked_until=NULL, last_login_at=now() WHERE id=$1`, id)
	Audit(r.Context(), a.DB, id, email, "staff.login", "staff", id, nil, clientIP(r))
	http.SetCookie(w, &http.Cookie{Name: "pd_staff", Value: tok, Path: "/", MaxAge: 12 * 3600, HttpOnly: true, Secure: a.Cfg.Secure(), SameSite: http.SameSiteStrictMode})
	next := r.FormValue("next")
	if !strings.HasPrefix(next, "/admin") || strings.HasPrefix(next, "//") {
		next = "/admin"
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (a *App) handleAdminLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("pd_staff"); err == nil {
		a.DB.ExecContext(r.Context(), `DELETE FROM staff_sessions WHERE token_hash=$1`, HashToken(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: "pd_staff", Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

// ---------- dashboard ----------

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

func num(x int64) string { return strconv.FormatInt(x, 10) }

// revenue = online payments captured + venue payments − refunds, by date in Bangkok.
func (a *App) revenueBetween(ctx context.Context, from, to time.Time) int64 {
	var v int64
	a.DB.QueryRowContext(ctx, `SELECT
		COALESCE((SELECT SUM(amount) FROM payments WHERE paid_at >= $1 AND paid_at < $2 AND status IN ('paid','refunded','partially_refunded')),0)
		- COALESCE((SELECT SUM(r.amount) FROM refunds r WHERE r.status='succeeded' AND r.created_at >= $1 AND r.created_at < $2),0)`, from, to).Scan(&v)
	return v
}

type dayStats struct {
	Bookings, Guests, CheckedIn, Capacity, Cancelled int64
}

func (a *App) statsForDay(ctx context.Context, d time.Time) dayStats {
	var s dayStats
	from, to := DateOnly(d), DateOnly(d).AddDate(0, 0, 1)
	a.DB.QueryRowContext(ctx, `SELECT count(*) FROM bookings WHERE created_at >= $1 AND created_at < $2 AND status <> 'expired'`, from, to).Scan(&s.Bookings)
	a.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(b.participants) FILTER (WHERE b.status IN ('confirmed','checked_in','no_show')),0),
		COALESCE(SUM(b.participants) FILTER (WHERE b.status='checked_in'),0),
		COALESCE(count(*) FILTER (WHERE b.status='cancelled'),0)
		FROM bookings b JOIN slots s ON s.id=b.slot_id WHERE s.starts_at >= $1 AND s.starts_at < $2`, from, to).Scan(&s.Guests, &s.CheckedIn, &s.Cancelled)
	a.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(capacity),0) FROM slots WHERE starts_at >= $1 AND starts_at < $2 AND status='open'`, from, to).Scan(&s.Capacity)
	return s
}

func (a *App) handleDashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s := staffOf(r)
	today := DateOnly(Now())
	EnsureSlots(ctx, a.DB, today, today.AddDate(0, 0, 14))
	yest, lastWeek := today.AddDate(0, 0, -1), today.AddDate(0, 0, -7)
	tS, lwS := a.statsForDay(ctx, today), a.statsForDay(ctx, lastWeek)
	occ := func(st dayStats) int64 {
		if st.Capacity == 0 {
			return 0
		}
		return st.Guests * 100 / st.Capacity
	}
	pctFmt := func(x int64) string { return num(x) + "%" }
	var kpis []Metric
	if s.Can("analytics.revenue") {
		kpis = append(kpis, metric("รายได้วันนี้", a.revenueBetween(ctx, today, today.AddDate(0, 0, 1)), a.revenueBetween(ctx, lastWeek, lastWeek.AddDate(0, 0, 1)), Baht, "เทียบวันเดียวกันสัปดาห์ก่อน"))
		k := metric("เมื่อวาน", a.revenueBetween(ctx, yest, today), a.revenueBetween(ctx, yest.AddDate(0, 0, -7), yest.AddDate(0, 0, -6)), Baht, "เทียบสัปดาห์ก่อน")
		kpis = append(kpis, k)
	}
	kpis = append(kpis,
		metric("การจองที่สร้างวันนี้", tS.Bookings, lwS.Bookings, num, "เทียบวันเดียวกันสัปดาห์ก่อน"),
		metric("ลูกค้าที่คาดว่าจะมาวันนี้", tS.Guests, lwS.Guests, num, "คน"),
		metric("เช็คอินแล้ว", tS.CheckedIn, lwS.CheckedIn, num, fmt.Sprintf("จาก %d คน", tS.Guests)),
		metric("อัตราการใช้ที่นั่งวันนี้", occ(tS), occ(lwS), pctFmt, fmt.Sprintf("%d จาก %d ที่", tS.Guests, tS.Capacity)),
	)
	cx := metric("ยกเลิก (รอบของวันนี้)", tS.Cancelled, lwS.Cancelled, num, "เทียบวันเดียวกันสัปดาห์ก่อน")
	cx.Invert = true
	kpis = append(kpis, cx)
	// 30-day customer + order metrics
	from30 := today.AddDate(0, 0, -30)
	var newC, retC, prevNew, prevRet int64
	custSplit := func(from, to time.Time) (n, rr int64) {
		a.DB.QueryRowContext(ctx, `WITH cx AS (SELECT DISTINCT b.customer_id FROM bookings b WHERE b.created_at >= $1 AND b.created_at < $2 AND b.status IN ('confirmed','checked_in','no_show'))
			SELECT count(*) FILTER (WHERE NOT EXISTS (SELECT 1 FROM bookings p WHERE p.customer_id=cx.customer_id AND p.created_at < $1 AND p.status IN ('confirmed','checked_in','no_show'))),
			       count(*) FILTER (WHERE EXISTS (SELECT 1 FROM bookings p WHERE p.customer_id=cx.customer_id AND p.created_at < $1 AND p.status IN ('confirmed','checked_in','no_show')))
			FROM cx`, from, to).Scan(&n, &rr)
		return
	}
	newC, retC = custSplit(from30, today.AddDate(0, 0, 1))
	prevNew, prevRet = custSplit(from30.AddDate(0, 0, -30), from30)
	kpis = append(kpis, metric("ลูกค้าใหม่ (30 วัน)", newC, prevNew, num, "เทียบ 30 วันก่อนหน้า"), metric("ลูกค้าเก่ากลับมา (30 วัน)", retC, prevRet, num, "เทียบ 30 วันก่อนหน้า"))
	if s.Can("analytics.revenue") {
		aov := func(from, to time.Time) int64 {
			var v int64
			a.DB.QueryRowContext(ctx, `SELECT COALESCE(AVG(amount),0)::bigint FROM payments WHERE paid_at >= $1 AND paid_at < $2 AND status IN ('paid','refunded','partially_refunded')`, from, to).Scan(&v)
			return v
		}
		kpis = append(kpis, metric("ยอดเฉลี่ยต่อบิล (30 วัน)", aov(from30, today.AddDate(0, 0, 1)), aov(from30.AddDate(0, 0, -30), from30), Baht, "เทียบ 30 วันก่อนหน้า"))
	}

	sessions, _ := SlotsBetween(ctx, a.DB, 0, today, today.AddDate(0, 0, 1))
	var pay struct{ Holds, Failed, Due, Attention, Unprocessed int64 }
	a.DB.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE status='hold' AND hold_expires_at>now()),
		count(*) FILTER (WHERE status='hold' AND payment_state='failed'),
		count(*) FILTER (WHERE payment_state='due_at_venue' AND status IN ('confirmed','checked_in')),
		count(*) FILTER (WHERE needs_attention<>'' AND status <> 'expired')
		FROM bookings`).Scan(&pay.Holds, &pay.Failed, &pay.Due, &pay.Attention)
	a.DB.QueryRowContext(ctx, `SELECT count(*) FROM payment_events WHERE processed_at IS NULL AND created_at < now()-interval '10 minutes'`).Scan(&pay.Unprocessed)
	attention, _ := ListBookings(ctx, a.DB, `WHERE b.needs_attention<>'' AND b.status <> 'expired' ORDER BY b.updated_at DESC LIMIT 10`)
	var failedNotes int64
	a.DB.QueryRowContext(ctx, `SELECT count(*) FROM notifications WHERE status='failed'`).Scan(&failedNotes)
	var spark []point
	if s.Can("analytics.revenue") {
		spark = a.dailyRevenue(ctx, today.AddDate(0, 0, -13), today.AddDate(0, 0, 1))
	}
	a.adminRender(w, r, "admin/dashboard", 200, "ภาพรวม", map[string]any{
		"KPIs": kpis, "Sessions": sessions, "Pay": pay, "Attention": attention, "FailedNotes": failedNotes,
		"Today": today, "Spark": barChart(spark, 640, 160, Baht), "Simulated": a.Simulated(),
	})
}

// ---------- calendar (front-desk day view + week view) ----------

func (a *App) handleCalendar(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d, err := ParseDate(r.URL.Query().Get("date"))
	if err != nil {
		d = DateOnly(Now())
	}
	view := r.URL.Query().Get("view")
	if view != "week" {
		view = "day"
	}
	data := map[string]any{"Date": d, "View": view, "Prev": d.AddDate(0, 0, -1), "Next": d.AddDate(0, 0, 1), "TodayStr": DateOnly(Now()).Format("2006-01-02")}
	exps, _ := ListExperiences(ctx, a.DB, true)
	expName := map[int64]string{}
	for _, e := range exps {
		expName[e.ID] = e.NameTH
	}
	data["ExpName"] = expName
	if view == "day" {
		EnsureSlots(ctx, a.DB, d, d)
		slots, _ := SlotsBetween(ctx, a.DB, 0, d, d.AddDate(0, 0, 1))
		bs, _ := ListBookings(ctx, a.DB, `WHERE s.starts_at >= $1 AND s.starts_at < $2 AND b.status IN ('hold','confirmed','checked_in','no_show','cancelled') ORDER BY s.starts_at, b.created_at`, d, d.AddDate(0, 0, 1))
		bySlot := map[int64][]BookingView{}
		for _, b := range bs {
			if b.Status == "hold" && b.HoldExpiresAt.Valid && b.HoldExpiresAt.Time.Before(time.Now()) {
				continue
			}
			bySlot[b.SlotID] = append(bySlot[b.SlotID], b)
		}
		data["Slots"], data["BySlot"] = slots, bySlot
		st := a.statsForDay(ctx, d)
		data["Stats"] = st
	} else {
		start := d.AddDate(0, 0, -int((d.Weekday()+6)%7)) // Monday
		EnsureSlots(ctx, a.DB, start, start.AddDate(0, 0, 6))
		slots, _ := SlotsBetween(ctx, a.DB, 0, start, start.AddDate(0, 0, 7))
		var days []time.Time
		for i := 0; i < 7; i++ {
			days = append(days, start.AddDate(0, 0, i))
		}
		times := map[string]bool{}
		grid := map[string]Slot{}
		for _, s := range slots {
			t := HM(s.StartsAt)
			times[t] = true
			grid[s.StartsAt.In(BKK).Format("2006-01-02")+"|"+t] = s
		}
		var tl []string
		for t := range times {
			tl = append(tl, t)
		}
		sortStrings(tl)
		data["Days"], data["Times"], data["Grid"] = days, tl, grid
		data["Prev"], data["Next"] = start.AddDate(0, 0, -7), start.AddDate(0, 0, 7)
	}
	a.adminRender(w, r, "admin/calendar", 200, "ตารางรอบ", data)
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// ---------- bookings ----------

func (a *App) handleBookingsList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	where := []string{"TRUE"}
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", "$"+strconv.Itoa(len(args))))
	}
	if s := strings.TrimSpace(q.Get("q")); s != "" {
		like := "%" + strings.ToLower(s) + "%"
		digits := NormPhone(s)
		args = append(args, like)
		n := len(args)
		cond := fmt.Sprintf("(lower(c.name) LIKE $%d OR lower(b.ref) LIKE $%d OR c.email LIKE $%d", n, n, n)
		if len(digits) >= 3 {
			args = append(args, "%"+strings.TrimPrefix(digits, "+")+"%")
			cond += fmt.Sprintf(" OR c.phone LIKE $%d", len(args))
		}
		where = append(where, cond+")")
	}
	if st := q.Get("status"); st != "" {
		add("b.status = ?", st)
	} else {
		where = append(where, "b.status <> 'expired'")
	}
	if ps := q.Get("payment"); ps != "" {
		add("b.payment_state = ?", ps)
	}
	if f, err := ParseDate(q.Get("from")); err == nil {
		add("s.starts_at >= ?", f)
	}
	if t, err := ParseDate(q.Get("to")); err == nil {
		add("s.starts_at < ?", t.AddDate(0, 0, 1))
	}
	if q.Get("attention") == "1" {
		where = append(where, "b.needs_attention <> ''")
	}
	order := "s.starts_at DESC"
	if q.Get("sort") == "created" {
		order = "b.created_at DESC"
	}
	list, err := ListBookings(r.Context(), a.DB, "WHERE "+strings.Join(where, " AND ")+" ORDER BY "+order+" LIMIT 300", args...)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if q.Get("format") == "csv" {
		if !staffOf(r).Can("customers.export") {
			http.Error(w, "forbidden", 403)
			return
		}
		Audit(r.Context(), a.DB, staffOf(r).ID, "", "export.bookings", "", "", map[string]any{"rows": len(list)}, clientIP(r))
		rows := [][]string{{"ref", "session_date", "time", "experience", "product", "guests", "status", "payment", "total_thb", "discount_thb", "customer", "phone", "email", "channel", "utm_source", "utm_campaign", "created_at"}}
		for _, b := range list {
			rows = append(rows, []string{b.Ref, b.StartsAt.In(BKK).Format("2006-01-02"), HM(b.StartsAt), b.ExpNameEN, b.ProdNameEN, strconv.Itoa(b.Participants), b.Status, b.PaymentState,
				fmt.Sprintf("%.2f", float64(b.Total)/100), fmt.Sprintf("%.2f", float64(b.Discount)/100), b.CustomerName, b.Phone, b.Email, b.Channel, b.UTMSource, b.UTMCampaign, b.CreatedAt.In(BKK).Format(time.RFC3339)})
		}
		writeCSV(w, "bookings", rows)
		return
	}
	a.adminRender(w, r, "admin/bookings", 200, "การจองทั้งหมด", map[string]any{"List": list, "Q": q})
}

func (a *App) handleBookingDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	v, err := GetBookingByRef(ctx, a.DB, r.PathValue("ref"))
	if err != nil {
		a.adminRender(w, r, "admin/forbidden", 404, "ไม่พบการจอง", map[string]any{"Msg": "ไม่พบการจองนี้"})
		return
	}
	type payRow struct {
		ID                       int64
		Provider, Status, Method string
		Session, Ref, Receipt    sql.NullString
		Amount, Refunded         int64
		PaidAt                   sql.NullTime
		CreatedAt                time.Time
	}
	var pays []payRow
	rows, _ := a.DB.QueryContext(ctx, `SELECT id, provider, status, method, provider_session, provider_payment, receipt_url, amount, refunded_amount, paid_at, created_at FROM payments WHERE booking_id=$1 ORDER BY id`, v.ID)
	for rows != nil && rows.Next() {
		var p payRow
		rows.Scan(&p.ID, &p.Provider, &p.Status, &p.Method, &p.Session, &p.Ref, &p.Receipt, &p.Amount, &p.Refunded, &p.PaidAt, &p.CreatedAt)
		pays = append(pays, p)
	}
	if rows != nil {
		rows.Close()
	}
	type auditRow struct {
		At                    time.Time
		Actor, Action, Detail string
	}
	var audits []auditRow
	rows, _ = a.DB.QueryContext(ctx, `SELECT a.created_at, COALESCE(u.name, a.actor), a.action, COALESCE(a.detail::text,'') FROM audit_log a LEFT JOIN staff_users u ON u.id=a.staff_id
		WHERE a.entity='booking' AND (a.entity_id=$1 OR a.entity_id=$2) ORDER BY a.id DESC LIMIT 50`, v.Ref, strconv.FormatInt(v.ID, 10))
	for rows != nil && rows.Next() {
		var x auditRow
		rows.Scan(&x.At, &x.Actor, &x.Action, &x.Detail)
		audits = append(audits, x)
	}
	if rows != nil {
		rows.Close()
	}
	var notes []map[string]any
	rows, _ = a.DB.QueryContext(ctx, `SELECT kind, channel, status, last_error, created_at FROM notifications WHERE booking_id=$1 ORDER BY id DESC LIMIT 20`, v.ID)
	for rows != nil && rows.Next() {
		var k, c, s, e string
		var t time.Time
		rows.Scan(&k, &c, &s, &e, &t)
		notes = append(notes, map[string]any{"Kind": k, "Channel": c, "Status": s, "Err": e, "At": t})
	}
	if rows != nil {
		rows.Close()
	}
	a.adminRender(w, r, "admin/booking", 200, "การจอง "+v.Ref, map[string]any{
		"B": v, "Payments": pays, "Audit": audits, "Notes": notes, "QRSig": a.qrSig(v.Ref),
		"ManageLink": a.Cfg.BaseURL + Lang(v.CustLang).Prefix() + "/booking/" + v.Ref + "?s=" + a.refSig(v.Ref),
	})
}

func (a *App) handleBookingAction(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s := staffOf(r)
	v, err := GetBookingByRef(ctx, a.DB, r.PathValue("ref"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	dest := "/admin/bookings/" + v.Ref
	if rt := r.FormValue("return"); strings.HasPrefix(rt, "/admin/") {
		dest = rt
	}
	ip := clientIP(r)
	var msg string
	switch r.FormValue("action") {
	case "checkin":
		err = a.CheckIn(ctx, v.ID, s.ID, r.FormValue("venue_method"), ip)
		msg = "เช็คอิน " + v.CustomerName + " แล้ว"
	case "undo_checkin":
		err = a.UndoCheckIn(ctx, v.ID, s.ID, ip)
		msg = "ยกเลิกการเช็คอินแล้ว"
	case "no_show":
		err = a.MarkNoShow(ctx, v.ID, s.ID, ip)
		msg = "บันทึกว่าไม่มาตามนัด"
	case "cancel":
		mode := r.FormValue("refund")
		if mode == "refund" && !s.Can("refunds") {
			mode = "none"
		}
		var res CancelResult
		res, err = a.CancelBooking(ctx, v.ID, ChangeActor{StaffID: s.ID, IP: ip}, CancelOptions{Reason: r.FormValue("reason"), Refund: mode})
		msg = "ยกเลิกการจองแล้ว"
		if res.RefundedAmount > 0 {
			msg += " · คืนเงิน " + Baht(res.RefundedAmount)
		}
		if res.CreditPassCode != "" {
			msg += " · ออกบัตรเครดิต " + res.CreditPassCode
		}
		if res.RefundError != "" {
			back(w, r, dest, msg, "คืนเงินไม่สำเร็จ: "+res.RefundError)
			return
		}
	case "move":
		over := r.FormValue("over") == "on" && s.Can("bookings.override")
		err = a.RescheduleBooking(ctx, v.ID, parseInt64(r.FormValue("slot_id")), ChangeActor{StaffID: s.ID, IP: ip}, over)
		msg = "ย้ายรอบเรียบร้อย"
	case "note":
		_, err = a.DB.ExecContext(ctx, `UPDATE bookings SET staff_note=$2, updated_at=now() WHERE id=$1`, v.ID, clip(r.FormValue("staff_note"), 1000))
		Audit(ctx, a.DB, s.ID, "", "booking.note", "booking", v.Ref, nil, ip)
		msg = "บันทึกโน้ตแล้ว"
	case "participants":
		n, _ := strconv.Atoi(r.FormValue("participants"))
		err = a.changeParticipants(ctx, v, n, s, ip)
		msg = "แก้จำนวนคนแล้ว"
	case "clear_attention":
		_, err = a.DB.ExecContext(ctx, `UPDATE bookings SET needs_attention='' WHERE id=$1`, v.ID)
		Audit(ctx, a.DB, s.ID, "", "booking.attention_cleared", "booking", v.Ref, map[string]any{"was": v.NeedsAttention}, ip)
		msg = "ปิดเรื่องที่ต้องตรวจสอบแล้ว"
	case "resend":
		err = a.enqueueBookingNotice(ctx, a.DB, v.ID, "booking_rescheduled")
		msg = "ส่งอีเมลรายละเอียดการจองอีกครั้งแล้ว"
	default:
		err = errors.New("ไม่รู้จักคำสั่งนี้")
	}
	if err != nil {
		back(w, r, dest, "", errText(err))
		return
	}
	back(w, r, dest, msg, "")
}

// changeParticipants adjusts guests on a pay-at-venue / complimentary booking (online-paid totals stay as charged).
func (a *App) changeParticipants(ctx context.Context, v BookingView, n int, s *Staff, ip string) error {
	if n < 1 || n > 50 {
		return errors.New("จำนวนคนต้องอยู่ระหว่าง 1–50")
	}
	return a.inTx(ctx, func(tx *sql.Tx) error {
		slot, err := GetSlot(ctx, tx, v.SlotID, true)
		if err != nil {
			return err
		}
		if n > v.Participants && slot.Remaining() < n-v.Participants && !s.Can("bookings.override") {
			return uerr("slot_full")
		}
		p, err := GetProduct(ctx, tx, v.ProductID)
		if err != nil {
			return err
		}
		total := v.Total
		if v.PayMethod == "pay_at_venue" {
			total = p.PriceFor(n) - v.Discount
			if total < 0 {
				total = 0
			}
		} else if v.PayMethod == "online" && n > v.Participants {
			return errors.New("การจองที่จ่ายออนไลน์แล้ว เพิ่มคนได้โดยสร้างการจองใหม่สำหรับคนที่เพิ่ม")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE bookings SET participants=$2, subtotal=CASE WHEN pay_method='pay_at_venue' THEN $3 ELSE subtotal END, total=$4, updated_at=now() WHERE id=$1`,
			v.ID, n, p.PriceFor(n), total); err != nil {
			return err
		}
		Audit(ctx, tx, s.ID, "", "booking.participants", "booking", v.Ref, map[string]any{"from": v.Participants, "to": n}, ip)
		return nil
	})
}

func (a *App) handleBookingRefund(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s := staffOf(r)
	v, err := GetBookingByRef(ctx, a.DB, r.PathValue("ref"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	amt, err := ParseBaht(r.FormValue("amount"))
	if err != nil {
		back(w, r, "/admin/bookings/"+v.Ref, "", "จำนวนเงินไม่ถูกต้อง")
		return
	}
	pid := parseInt64(r.FormValue("payment_id"))
	var owner int64
	if a.DB.QueryRowContext(ctx, `SELECT booking_id FROM payments WHERE id=$1`, pid).Scan(&owner); owner != v.ID {
		back(w, r, "/admin/bookings/"+v.Ref, "", "ไม่พบรายการชำระเงินนี้")
		return
	}
	got, err := a.RefundPayment(ctx, pid, amt, r.FormValue("reason"), s.ID, "staff-"+r.FormValue("idem"))
	if err != nil {
		back(w, r, "/admin/bookings/"+v.Ref, "", "คืนเงินไม่สำเร็จ: "+err.Error())
		return
	}
	back(w, r, "/admin/bookings/"+v.Ref, "คืนเงิน "+Baht(got)+" เรียบร้อย", "")
}

type slotOption struct {
	ID        int64
	Label     string
	Remaining int
}

func (a *App) handleAdminSlotsAPI(w http.ResponseWriter, r *http.Request) {
	d, err := ParseDate(r.URL.Query().Get("date"))
	if err != nil {
		writeJSON(w, 400, map[string]any{"error": "date"})
		return
	}
	EnsureSlots(r.Context(), a.DB, d, d)
	slots, _ := SlotsBetween(r.Context(), a.DB, parseInt64(r.URL.Query().Get("exp")), d, d.AddDate(0, 0, 1))
	exps, _ := ListExperiences(r.Context(), a.DB, true)
	names := map[int64]string{}
	for _, e := range exps {
		names[e.ID] = e.NameTH
	}
	var out []map[string]any
	for _, s := range slots {
		out = append(out, map[string]any{"id": s.ID, "time": HM(s.StartsAt), "exp": names[s.ExperienceID], "remaining": s.Remaining(), "capacity": s.Capacity, "status": s.Status})
	}
	writeJSON(w, 200, map[string]any{"slots": out})
}

func (a *App) handleBookingNewPage(w http.ResponseWriter, r *http.Request) {
	prods, _ := ListProducts(r.Context(), a.DB, `WHERE kind='ticket' AND active`)
	d := r.URL.Query().Get("date")
	if d == "" {
		d = DateOnly(Now()).Format("2006-01-02")
	}
	a.adminRender(w, r, "admin/booking_new", 200, "สร้างการจอง", map[string]any{"Products": prods, "Date": d, "SlotID": r.URL.Query().Get("slot"), "Idem": RandomToken(), "Form": r.URL.Query()})
}

func (a *App) handleBookingNew(w http.ResponseWriter, r *http.Request) {
	s := staffOf(r)
	n, _ := strconv.Atoi(r.FormValue("participants"))
	channel := r.FormValue("channel")
	if channel != "walk_in" && channel != "phone" && channel != "line" {
		channel = "admin"
	}
	pay := r.FormValue("pay")
	req := BookingRequest{
		SlotID: parseInt64(r.FormValue("slot_id")), ProductID: parseInt64(r.FormValue("product_id")), Participants: n,
		Name: r.FormValue("name"), Phone: r.FormValue("phone"), Email: r.FormValue("email"), Lang: r.FormValue("lang"),
		PromoCode: r.FormValue("promo"), PassCode: r.FormValue("voucher"), CustomerNote: r.FormValue("note"),
		IdempotencyKey: "admin-" + clip(r.FormValue("idem"), 60), Channel: channel, StaffID: s.ID,
		PayAtVenue: pay == "venue", AllowDuplicate: r.FormValue("allow_dup") == "on",
	}
	if s.Can("bookings.override") {
		req.Complimentary = pay == "complimentary"
		req.OverCapacity = r.FormValue("over") == "on"
	}
	if pay == "online" {
		req.PayAtVenue = false
	} else if !req.Complimentary {
		req.PayAtVenue = true
	}
	res, err := a.CreateBooking(r.Context(), req)
	if err != nil {
		q := r.PostForm
		q.Del("csrf")
		back(w, r, "/admin/bookings/new?"+q.Encode(), "", errText(err))
		return
	}
	if res.Status == "hold" {
		// Staff chose online payment: create the payment link to send to the customer.
		if u, err := a.StartBookingCheckout(r.Context(), res.BookingID, Lang(req.Lang), res.ManageToken); err == nil {
			back(w, r, "/admin/bookings/"+res.Ref, "สร้างการจองแล้ว ส่งลิงก์ชำระเงินนี้ให้ลูกค้า (กันที่ไว้ตามเวลาที่ตั้งค่า): "+u, "")
			return
		}
	}
	back(w, r, "/admin/bookings/"+res.Ref, "สร้างการจอง "+res.Ref+" แล้ว", "")
}

// ---------- check-in ----------

func (a *App) handleCheckinPage(w http.ResponseWriter, r *http.Request) {
	today := DateOnly(Now())
	list, _ := ListBookings(r.Context(), a.DB, `WHERE s.starts_at >= $1 AND s.starts_at < $2 AND b.status IN ('confirmed','checked_in') ORDER BY s.starts_at, c.name`, today, today.AddDate(0, 0, 1))
	a.adminRender(w, r, "admin/checkin", 200, "เช็คอิน", map[string]any{"List": list})
}

// handleCheckinLookup accepts a scanned QR payload (PD1:REF:SIG) or a typed ref / phone.
func (a *App) handleCheckinLookup(w http.ResponseWriter, r *http.Request) {
	code := strings.TrimSpace(r.FormValue("code"))
	ref := strings.ToUpper(code)
	if strings.HasPrefix(ref, "PD1:") {
		parts := strings.Split(code, ":")
		if len(parts) != 3 || !ConstEq(parts[2], a.qrSig(strings.ToUpper(parts[1]))) {
			back(w, r, "/admin/checkin", "", "QR ไม่ถูกต้องหรือถูกแก้ไข")
			return
		}
		ref = strings.ToUpper(parts[1])
	}
	if strings.HasPrefix(ref, "PD-") {
		if v, err := GetBookingByRef(r.Context(), a.DB, ref); err == nil {
			http.Redirect(w, r, "/admin/bookings/"+v.Ref+"?from=checkin", http.StatusSeeOther)
			return
		}
	}
	phone := NormPhone(code)
	if len(phone) >= 9 {
		today := DateOnly(Now())
		list, _ := ListBookings(r.Context(), a.DB, `WHERE c.phone=$1 AND s.starts_at >= $2 AND s.starts_at < $3 AND b.status IN ('confirmed','checked_in') ORDER BY s.starts_at`, phone, today, today.AddDate(0, 0, 1))
		if len(list) == 1 {
			http.Redirect(w, r, "/admin/bookings/"+list[0].Ref+"?from=checkin", http.StatusSeeOther)
			return
		}
	}
	back(w, r, "/admin/bookings?q="+url.QueryEscape(code), "", "ไม่พบการจองวันนี้ที่ตรงกัน ลองค้นหาด้านล่าง")
}

// ---------- customers (CRM) ----------

type CustomerRow struct {
	Customer
	Visits    int64
	Bookings  int64
	Spend     int64
	LastVisit sql.NullTime
	NextVisit sql.NullTime
	Credits   int64
}

const customerAggSQL = `SELECT c.id, c.name, c.phone, c.email, c.lang, c.marketing_consent, c.notes, c.tags, c.created_at,
	(SELECT count(*) FROM bookings b WHERE b.customer_id=c.id AND b.status='checked_in'),
	(SELECT count(*) FROM bookings b WHERE b.customer_id=c.id AND b.status IN ('confirmed','checked_in','no_show')),
	COALESCE((SELECT SUM(p.amount - p.refunded_amount) FROM payments p LEFT JOIN bookings b ON b.id=p.booking_id LEFT JOIN pass_orders o ON o.id=p.pass_order_id
	          WHERE (b.customer_id=c.id OR o.customer_id=c.id) AND p.status IN ('paid','refunded','partially_refunded')),0),
	(SELECT max(s.starts_at) FROM bookings b JOIN slots s ON s.id=b.slot_id WHERE b.customer_id=c.id AND b.status='checked_in'),
	(SELECT min(s.starts_at) FROM bookings b JOIN slots s ON s.id=b.slot_id WHERE b.customer_id=c.id AND b.status='confirmed' AND s.starts_at>now()),
	COALESCE((SELECT SUM(credits_total-credits_used) FROM passes p WHERE p.customer_id=c.id AND p.status='active' AND (p.expires_at IS NULL OR p.expires_at>now())),0)
	FROM customers c `

func scanCustomerRow(sc interface{ Scan(...any) error }) (CustomerRow, error) {
	var c CustomerRow
	var tags pq.StringArray
	err := sc.Scan(&c.ID, &c.Name, &c.Phone, &c.Email, &c.Lang, &c.MarketingConsent, &c.Notes, &tags, &c.CreatedAt,
		&c.Visits, &c.Bookings, &c.Spend, &c.LastVisit, &c.NextVisit, &c.Credits)
	c.Tags = tags
	return c, err
}

var segments = map[string]struct{ Label, SQL string }{
	"new":      {"ลูกค้าใหม่ (มา 1 ครั้ง)", "visits = 1"},
	"regular":  {"ขาประจำ (มา 3 ครั้งขึ้นไป)", "visits >= 3"},
	"lapsed":   {"หายไปเกิน 60 วัน", "last_visit < now() - interval '60 days'"},
	"credits":  {"มีสิทธิ์คงเหลือในบัตร", "credits > 0"},
	"consent":  {"ยินยอมรับข่าวสาร", "marketing_consent"},
	"upcoming": {"มีนัดที่กำลังจะมาถึง", "next_visit IS NOT NULL"},
	"no_visit": {"จองแล้วยังไม่เคยมา", "visits = 0 AND bookings > 0"},
}

func (a *App) handleCustomers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s := staffOf(r)
	inner := customerAggSQL
	var args []any
	cond := []string{"TRUE"}
	if term := strings.TrimSpace(q.Get("q")); term != "" {
		args = append(args, "%"+strings.ToLower(term)+"%")
		c := fmt.Sprintf("(lower(c.name) LIKE $%d OR c.email LIKE $%d", len(args), len(args))
		if d := strings.TrimPrefix(NormPhone(term), "+"); len(d) >= 3 {
			args = append(args, "%"+d+"%")
			c += fmt.Sprintf(" OR c.phone LIKE $%d", len(args))
		}
		cond = append(cond, c+")")
	}
	inner += "WHERE " + strings.Join(cond, " AND ")
	outer := `SELECT * FROM (` + inner + `) x(id,name,phone,email,lang,marketing_consent,notes,tags,created_at,visits,bookings,spend,last_visit,next_visit,credits)`
	if seg, ok := segments[q.Get("segment")]; ok {
		outer += " WHERE " + seg.SQL
	}
	sortBy := map[string]string{"spend": "spend DESC", "visits": "visits DESC", "recent": "last_visit DESC NULLS LAST", "": "created_at DESC"}[q.Get("sort")]
	if sortBy == "" {
		sortBy = "created_at DESC"
	}
	limit := " LIMIT 300"
	if q.Get("format") == "csv" {
		limit = " LIMIT 50000"
	}
	rows, err := a.DB.QueryContext(r.Context(), outer+" ORDER BY "+sortBy+limit, args...)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	defer rows.Close()
	var list []CustomerRow
	for rows.Next() {
		c, err := scanCustomerRow(rows)
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		list = append(list, c)
	}
	if q.Get("format") == "csv" {
		if !s.Can("customers.export") {
			http.Error(w, "forbidden", 403)
			return
		}
		Audit(r.Context(), a.DB, s.ID, "", "export.customers", "", "", map[string]any{"rows": len(list), "segment": q.Get("segment")}, clientIP(r))
		out := [][]string{{"name", "phone", "email", "lang", "marketing_consent", "visits", "bookings", "spend_thb", "last_visit", "credits_left"}}
		for _, c := range list {
			lv := ""
			if c.LastVisit.Valid {
				lv = c.LastVisit.Time.In(BKK).Format("2006-01-02")
			}
			out = append(out, []string{c.Name, c.Phone, c.Email, c.Lang, strconv.FormatBool(c.MarketingConsent), num(c.Visits), num(c.Bookings), fmt.Sprintf("%.2f", float64(c.Spend)/100), lv, num(c.Credits)})
		}
		writeCSV(w, "customers", out)
		return
	}
	a.adminRender(w, r, "admin/customers", 200, "ลูกค้า", map[string]any{"List": list, "Q": q, "Segments": segments})
}

func (a *App) handleCustomerDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := parseInt64(r.PathValue("id"))
	c, err := scanCustomerRow(a.DB.QueryRowContext(ctx, customerAggSQL+"WHERE c.id=$1", id))
	if err != nil {
		a.adminRender(w, r, "admin/forbidden", 404, "ไม่พบลูกค้า", map[string]any{"Msg": "ไม่พบลูกค้านี้"})
		return
	}
	bs, _ := ListBookings(ctx, a.DB, `WHERE b.customer_id=$1 AND b.status <> 'expired' ORDER BY s.starts_at DESC LIMIT 100`, id)
	passes := a.passesFor(r, id)
	a.adminRender(w, r, "admin/customer", 200, c.Name, map[string]any{"C": c, "Bookings": bs, "Passes": passes})
}

func (a *App) handleCustomerUpdate(w http.ResponseWriter, r *http.Request) {
	id := parseInt64(r.PathValue("id"))
	s := staffOf(r)
	var tags []string
	for _, t := range strings.Split(r.FormValue("tags"), ",") {
		if t = strings.TrimSpace(t); t != "" {
			tags = append(tags, clip(t, 30))
		}
	}
	phone := NormPhone(r.FormValue("phone"))
	email := NormEmail(r.FormValue("email"))
	if phone != "" && !ValidPhone(phone) {
		back(w, r, fmt.Sprintf("/admin/customers/%d", id), "", "เบอร์โทรไม่ถูกต้อง")
		return
	}
	if email != "" && !ValidEmail(email) {
		back(w, r, fmt.Sprintf("/admin/customers/%d", id), "", "อีเมลไม่ถูกต้อง")
		return
	}
	consent := r.FormValue("consent") == "on"
	_, err := a.DB.ExecContext(r.Context(), `UPDATE customers SET name=$2, phone=$3, email=$4, lang=$5, notes=$6, tags=$7,
		marketing_consent=$8, consent_at = CASE WHEN $8 AND NOT marketing_consent THEN now() WHEN NOT $8 THEN NULL ELSE consent_at END WHERE id=$1`,
		id, clip(r.FormValue("name"), 120), phone, email, map[bool]string{true: "en", false: "th"}[r.FormValue("lang") == "en"], clip(r.FormValue("notes"), 2000), pq.StringArray(tags), consent)
	if err != nil {
		back(w, r, fmt.Sprintf("/admin/customers/%d", id), "", errText(err))
		return
	}
	Audit(r.Context(), a.DB, s.ID, "", "customer.update", "customer", id, map[string]any{"consent": consent}, clientIP(r))
	back(w, r, fmt.Sprintf("/admin/customers/%d", id), "บันทึกข้อมูลลูกค้าแล้ว", "")
}
