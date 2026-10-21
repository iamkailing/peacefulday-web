package app

import (
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/lib/pq"
)

func (a *App) adminManageRoutes(m *http.ServeMux) {
	m.HandleFunc("GET /admin/schedule", a.guard("schedule", a.handleSchedule))
	m.HandleFunc("POST /admin/schedule/template", a.guard("schedule", a.handleTemplateSave))
	m.HandleFunc("POST /admin/schedule/blackout", a.guard("schedule", a.handleBlackout))
	m.HandleFunc("POST /admin/schedule/slot", a.guard("schedule", a.handleSlotOverride))
	m.HandleFunc("POST /admin/schedule/experience", a.guard("schedule", a.handleExperienceSave))
	m.HandleFunc("GET /admin/products", a.guard("products", a.handleProducts))
	m.HandleFunc("POST /admin/products", a.guard("products", a.handleProductSave))
	m.HandleFunc("GET /admin/promos", a.guard("promos", a.handlePromos))
	m.HandleFunc("POST /admin/promos", a.guard("promos", a.handlePromoSave))
	m.HandleFunc("GET /admin/passes", a.guard("passes", a.handlePasses))
	m.HandleFunc("POST /admin/passes", a.guard("passes", a.handlePassIssue))
	m.HandleFunc("POST /admin/passes/{id}", a.guard("passes", a.handlePassUpdate))
	m.HandleFunc("GET /admin/payments", a.guard("payments", a.handlePayments))
	m.HandleFunc("GET /admin/analytics", a.guard("", a.handleAnalytics))
	m.HandleFunc("GET /admin/marketing", a.guard("marketing", a.handleMarketing))
	m.HandleFunc("POST /admin/marketing", a.guard("marketing", a.handleMarketingSave))
	m.HandleFunc("GET /admin/content", a.guard("marketing", a.handleContent))
	m.HandleFunc("POST /admin/content", a.guard("marketing", a.handleContentSave))
	m.HandleFunc("GET /admin/staff", a.guard("staff", a.handleStaff))
	m.HandleFunc("POST /admin/staff", a.guard("staff", a.handleStaffSave))
	m.HandleFunc("GET /admin/settings", a.guard("settings", a.handleSettings))
	m.HandleFunc("POST /admin/settings", a.guard("settings", a.handleSettingsSave))
	m.HandleFunc("GET /admin/audit", a.guard("audit", a.handleAudit))
	m.HandleFunc("GET /admin/notifications", a.guard("notifications", a.handleNotifications))
	m.HandleFunc("POST /admin/notifications", a.guard("notifications", a.handleNotificationRetry))
	m.HandleFunc("GET /admin/account", a.guard("", a.handleAccount))
	m.HandleFunc("POST /admin/account", a.guard("", a.handleAccountSave))
}

func writeCSV(w http.ResponseWriter, name string, rows [][]string) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=peaceful-day-%s-%s.csv", name, Now().Format("20060102")))
	io.WriteString(w, "\ufeff") // Excel opens UTF-8 Thai correctly with a BOM
	cw := csv.NewWriter(w)
	for _, r := range rows {
		for i, c := range r { // neutralise spreadsheet formula injection
			if len(c) > 0 && strings.ContainsAny(c[:1], "=+-@") && !regexp.MustCompile(`^-?\d`).MatchString(c) {
				r[i] = "'" + c
			}
		}
		cw.Write(r)
	}
	cw.Flush()
}

var hmRe = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

// ---------- schedule ----------

type templateRow struct {
	ID, ExperienceID   int64
	Weekday            int
	Start              string
	Duration, Capacity int
}

func (a *App) handleSchedule(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	exps, _ := ListExperiences(ctx, a.DB, true)
	var tpl []templateRow
	rows, _ := a.DB.QueryContext(ctx, `SELECT id, experience_id, weekday, start_time, duration_min, capacity FROM schedule_templates ORDER BY experience_id, weekday, start_time`)
	for rows != nil && rows.Next() {
		var t templateRow
		rows.Scan(&t.ID, &t.ExperienceID, &t.Weekday, &t.Start, &t.Duration, &t.Capacity)
		tpl = append(tpl, t)
	}
	if rows != nil {
		rows.Close()
	}
	type bo struct {
		ID         int64
		Start, End time.Time
		Reason     string
	}
	var bos []bo
	rows, _ = a.DB.QueryContext(ctx, `SELECT id, start_date, end_date, reason FROM blackouts WHERE end_date >= current_date - 30 ORDER BY start_date`)
	for rows != nil && rows.Next() {
		var b bo
		rows.Scan(&b.ID, &b.Start, &b.End, &b.Reason)
		bos = append(bos, b)
	}
	if rows != nil {
		rows.Close()
	}
	// group template rows: experience -> weekday -> rows
	grid := map[int64]map[int][]templateRow{}
	for _, t := range tpl {
		if grid[t.ExperienceID] == nil {
			grid[t.ExperienceID] = map[int][]templateRow{}
		}
		grid[t.ExperienceID][t.Weekday] = append(grid[t.ExperienceID][t.Weekday], t)
	}
	d, err := ParseDate(r.URL.Query().Get("date"))
	if err != nil {
		d = DateOnly(Now())
	}
	EnsureSlots(ctx, a.DB, d, d)
	daySlots, _ := SlotsBetween(ctx, a.DB, 0, d, d.AddDate(0, 0, 1))
	a.adminRender(w, r, "admin/schedule", 200, "ตารางเปิดรอบ", map[string]any{
		"Exps": exps, "Grid": grid, "Blackouts": bos, "Weekdays": []int{1, 2, 3, 4, 5, 6, 0}, "Date": d, "DaySlots": daySlots,
	})
}

// handleTemplateSave replaces one weekday's sessions for an experience from a compact text list ("09:00, 10:00 ...").
func (a *App) handleTemplateSave(w http.ResponseWriter, r *http.Request) {
	s := staffOf(r)
	exp := parseInt64(r.FormValue("experience_id"))
	capacity, _ := strconv.Atoi(r.FormValue("capacity"))
	duration, _ := strconv.Atoi(r.FormValue("duration"))
	if capacity < 0 || capacity > 200 || duration < 10 || duration > 600 {
		back(w, r, "/admin/schedule", "", "ความจุหรือระยะเวลาไม่ถูกต้อง")
		return
	}
	days := r.Form["weekday"]
	var times []string
	for _, t := range strings.FieldsFunc(r.FormValue("times"), func(c rune) bool { return c == ',' || c == ' ' || c == '\n' }) {
		t = strings.TrimSpace(t)
		if len(t) == 4 && t[1] == ':' {
			t = "0" + t
		}
		if !hmRe.MatchString(t) {
			back(w, r, "/admin/schedule", "", "รูปแบบเวลาไม่ถูกต้อง: "+t+" (ใช้ HH:MM)")
			return
		}
		times = append(times, t)
	}
	if len(days) == 0 {
		back(w, r, "/admin/schedule", "", "เลือกอย่างน้อย 1 วัน")
		return
	}
	apply := r.FormValue("apply_future") == "on"
	err := a.inTx(r.Context(), func(tx *sql.Tx) error {
		for _, ds := range days {
			d, err := strconv.Atoi(ds)
			if err != nil || d < 0 || d > 6 {
				continue
			}
			if _, err := tx.ExecContext(r.Context(), `DELETE FROM schedule_templates WHERE experience_id=$1 AND weekday=$2`, exp, d); err != nil {
				return err
			}
			for _, t := range times {
				if _, err := tx.ExecContext(r.Context(), `INSERT INTO schedule_templates(experience_id,weekday,start_time,duration_min,capacity) VALUES($1,$2,$3,$4,$5)`, exp, d, t, duration, capacity); err != nil {
					return err
				}
			}
			if apply {
				// Future sessions without bookings and not individually overridden are rebuilt from the new template.
				if _, err := tx.ExecContext(r.Context(), `DELETE FROM slots s WHERE s.experience_id=$1 AND EXTRACT(DOW FROM s.starts_at AT TIME ZONE 'Asia/Bangkok')=$2
					AND s.starts_at > now() AND NOT s.overridden AND NOT EXISTS (SELECT 1 FROM bookings b WHERE b.slot_id=s.id)`, exp, d); err != nil {
					return err
				}
				if _, err := tx.ExecContext(r.Context(), `UPDATE slots s SET capacity=$3 WHERE s.experience_id=$1 AND EXTRACT(DOW FROM s.starts_at AT TIME ZONE 'Asia/Bangkok')=$2
					AND s.starts_at > now() AND NOT s.overridden`, exp, d, capacity); err != nil {
					return err
				}
			}
		}
		Audit(r.Context(), tx, s.ID, "", "schedule.template", "experience", exp, map[string]any{"days": days, "times": times, "capacity": capacity, "apply_future": apply}, clientIP(r))
		return nil
	})
	if err != nil {
		back(w, r, "/admin/schedule", "", errText(err))
		return
	}
	set, _ := LoadSettings(r.Context(), a.DB)
	EnsureSlots(r.Context(), a.DB, Now(), Now().AddDate(0, 0, set.Int("booking_horizon_days")+7))
	back(w, r, "/admin/schedule", "บันทึกตารางแล้ว", "")
}

func (a *App) handleBlackout(w http.ResponseWriter, r *http.Request) {
	s := staffOf(r)
	ctx := r.Context()
	if id := parseInt64(r.FormValue("delete")); id > 0 {
		err := a.inTx(ctx, func(tx *sql.Tx) error {
			var from, to time.Time
			if err := tx.QueryRowContext(ctx, `DELETE FROM blackouts WHERE id=$1 RETURNING start_date, end_date`, id).Scan(&from, &to); err != nil {
				return err
			}
			from = time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, BKK)
			to = time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, BKK).AddDate(0, 0, 1)
			// Reopen sessions this blackout closed, unless another blackout still covers that day.
			if _, err := tx.ExecContext(ctx, `UPDATE slots s SET status='open', note='', overridden=FALSE
				WHERE s.starts_at >= $1 AND s.starts_at < $2 AND s.status='blocked' AND s.note LIKE 'ปิด: %'
				AND NOT EXISTS (SELECT 1 FROM blackouts b WHERE (s.starts_at AT TIME ZONE 'Asia/Bangkok')::date BETWEEN b.start_date AND b.end_date)`, from, to); err != nil {
				return err
			}
			Audit(ctx, tx, s.ID, "", "schedule.blackout_delete", "blackout", id, map[string]any{"from": from.Format("2006-01-02"), "to": to.AddDate(0, 0, -1).Format("2006-01-02")}, clientIP(r))
			return nil
		})
		if err != nil {
			back(w, r, "/admin/schedule", "", errText(err))
			return
		}
		set, _ := LoadSettings(ctx, a.DB)
		EnsureSlots(ctx, a.DB, Now(), Now().AddDate(0, 0, set.Int("booking_horizon_days")+7))
		back(w, r, "/admin/schedule", "ลบวันปิดแล้ว รอบในวันนั้นถูกสร้างใหม่ตามตาราง", "")
		return
	}
	from, err1 := ParseDate(r.FormValue("start"))
	to, err2 := ParseDate(r.FormValue("end"))
	if err1 != nil || err2 != nil || to.Before(from) {
		back(w, r, "/admin/schedule", "", "ช่วงวันที่ไม่ถูกต้อง")
		return
	}
	reason := clip(r.FormValue("reason"), 200)
	var affected int
	err := a.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO blackouts(start_date,end_date,reason) VALUES($1,$2,$3)`, from.Format("2006-01-02"), to.Format("2006-01-02"), reason); err != nil {
			return err
		}
		// Block existing sessions in the range; flag any bookings so staff can contact customers.
		if _, err := tx.ExecContext(ctx, `UPDATE slots SET status='blocked', note=$3, overridden=TRUE WHERE starts_at >= $1 AND starts_at < $2`, from, to.AddDate(0, 0, 1), "ปิด: "+reason); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE bookings b SET needs_attention='ร้านปิดวันนี้ ('||$3||') ต้องติดต่อลูกค้าเพื่อเลื่อนหรือยกเลิก' FROM slots s
			WHERE s.id=b.slot_id AND s.starts_at >= $1 AND s.starts_at < $2 AND b.status IN ('confirmed','hold')`, from, to.AddDate(0, 0, 1), reason)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		affected = int(n)
		Audit(ctx, tx, s.ID, "", "schedule.blackout", "blackout", from.Format("2006-01-02"), map[string]any{"to": to.Format("2006-01-02"), "reason": reason, "bookings_affected": affected}, clientIP(r))
		return nil
	})
	if err != nil {
		back(w, r, "/admin/schedule", "", errText(err))
		return
	}
	msg := "ตั้งวันปิดแล้ว"
	if affected > 0 {
		msg += fmt.Sprintf(" · มี %d การจองที่ต้องติดต่อลูกค้า (ดูที่ ‘ต้องตรวจสอบ’ ในหน้าการจอง)", affected)
	}
	back(w, r, "/admin/schedule", msg, "")
}

func (a *App) handleSlotOverride(w http.ResponseWriter, r *http.Request) {
	s := staffOf(r)
	ctx := r.Context()
	id := parseInt64(r.FormValue("slot_id"))
	capacity, err := strconv.Atoi(r.FormValue("capacity"))
	status := r.FormValue("status")
	dest := "/admin/schedule?date=" + r.FormValue("date")
	if rt := r.FormValue("return"); strings.HasPrefix(rt, "/admin/") {
		dest = rt
	}
	if err != nil || capacity < 0 || capacity > 500 || (status != "open" && status != "blocked") {
		back(w, r, dest, "", "ค่าที่กรอกไม่ถูกต้อง")
		return
	}
	err = a.inTx(ctx, func(tx *sql.Tx) error {
		sl, err := GetSlot(ctx, tx, id, true)
		if err != nil {
			return err
		}
		if capacity < sl.Used() && !s.Can("bookings.override") {
			return fmt.Errorf("รอบนี้มีคนจองแล้ว %d คน ตั้งความจุต่ำกว่านั้นไม่ได้", sl.Used())
		}
		if _, err := tx.ExecContext(ctx, `UPDATE slots SET capacity=$2, status=$3, note=$4, overridden=TRUE WHERE id=$1`, id, capacity, status, clip(r.FormValue("note"), 200)); err != nil {
			return err
		}
		Audit(ctx, tx, s.ID, "", "slot.override", "slot", id, map[string]any{"capacity_from": sl.Capacity, "capacity_to": capacity, "status": status, "starts": sl.StartsAt.Format(time.RFC3339)}, clientIP(r))
		return nil
	})
	if err != nil {
		back(w, r, dest, "", errText(err))
		return
	}
	back(w, r, dest, "อัปเดตรอบแล้ว", "")
}

func (a *App) handleExperienceSave(w http.ResponseWriter, r *http.Request) {
	s := staffOf(r)
	id := parseInt64(r.FormValue("id"))
	dur, _ := strconv.Atoi(r.FormValue("duration_min"))
	sort, _ := strconv.Atoi(r.FormValue("sort"))
	status := r.FormValue("status")
	if status != "active" && status != "coming_soon" && status != "hidden" {
		status = "hidden"
	}
	slug := strings.ToLower(regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(strings.ToLower(r.FormValue("slug")), "-"))
	var err error
	if id > 0 {
		_, err = a.DB.ExecContext(r.Context(), `UPDATE experiences SET name_th=$2,name_en=$3,summary_th=$4,summary_en=$5,duration_min=$6,status=$7,sort=$8 WHERE id=$1`,
			id, clip(r.FormValue("name_th"), 100), clip(r.FormValue("name_en"), 100), clip(r.FormValue("summary_th"), 600), clip(r.FormValue("summary_en"), 600), dur, status, sort)
	} else {
		err = a.DB.QueryRowContext(r.Context(), `INSERT INTO experiences(slug,name_th,name_en,summary_th,summary_en,duration_min,status,sort) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`,
			slug, clip(r.FormValue("name_th"), 100), clip(r.FormValue("name_en"), 100), clip(r.FormValue("summary_th"), 600), clip(r.FormValue("summary_en"), 600), dur, status, sort).Scan(&id)
	}
	if err != nil {
		back(w, r, "/admin/schedule", "", errText(err))
		return
	}
	Audit(r.Context(), a.DB, s.ID, "", "experience.save", "experience", id, map[string]any{"status": status}, clientIP(r))
	back(w, r, "/admin/schedule", "บันทึกประสบการณ์แล้ว", "")
}

// ---------- products ----------

func (a *App) handleProducts(w http.ResponseWriter, r *http.Request) {
	prods, _ := ListProducts(r.Context(), a.DB, "")
	exps, _ := ListExperiences(r.Context(), a.DB, true)
	var edit *Product
	if id := parseInt64(r.URL.Query().Get("edit")); id > 0 {
		if p, err := GetProduct(r.Context(), a.DB, id); err == nil {
			edit = &p
		}
	}
	a.adminRender(w, r, "admin/products", 200, "สินค้า ราคา และแพ็กเกจ", map[string]any{"Products": prods, "Exps": exps, "Edit": edit, "Weekdays": []int{1, 2, 3, 4, 5, 6, 0}})
}

func (a *App) handleProductSave(w http.ResponseWriter, r *http.Request) {
	s := staffOf(r)
	ctx := r.Context()
	id := parseInt64(r.FormValue("id"))
	price, err := ParseBaht(r.FormValue("price"))
	if err != nil {
		back(w, r, "/admin/products?edit="+r.FormValue("id"), "", "ราคาไม่ถูกต้อง")
		return
	}
	atoi := func(k string, def int) int {
		n, err := strconv.Atoi(r.FormValue(k))
		if err != nil {
			return def
		}
		return n
	}
	kind := r.FormValue("kind")
	if kind != "pass" {
		kind = "ticket"
	}
	cat := r.FormValue("category")
	if !map[string]bool{"single": true, "group": true, "package": true, "promotion": true, "gift": true, "membership": true, "complimentary": true}[cat] {
		cat = "single"
	}
	mode := r.FormValue("price_mode")
	if mode != "flat" {
		mode = "per_person"
	}
	minP, maxP := atoi("min_participants", 1), atoi("max_participants", 10)
	if minP < 1 || maxP < minP || maxP > 50 {
		back(w, r, "/admin/products?edit="+r.FormValue("id"), "", "จำนวนคนต่ำสุด/สูงสุดไม่ถูกต้อง")
		return
	}
	credits := atoi("credits", 0)
	if kind == "pass" && credits < 1 {
		back(w, r, "/admin/products?edit="+r.FormValue("id"), "", "แพ็กเกจต้องมีจำนวนครั้งอย่างน้อย 1")
		return
	}
	if r.FormValue("active") == "on" && price == 0 && cat != "complimentary" {
		back(w, r, "/admin/products?edit="+r.FormValue("id"), "", "สินค้าที่เปิดขายต้องมีราคามากกว่า 0 (ยกเว้นประเภท ‘ฟรี/ชดเชย’)")
		return
	}
	var wd pq.Int64Array
	for _, d := range r.Form["weekdays"] {
		if n, err := strconv.Atoi(d); err == nil && n >= 0 && n <= 6 {
			wd = append(wd, int64(n))
		}
	}
	startAfter := strings.TrimSpace(r.FormValue("start_after"))
	if startAfter != "" && !hmRe.MatchString(startAfter) {
		back(w, r, "/admin/products?edit="+r.FormValue("id"), "", "เวลาเริ่มต้องเป็น HH:MM")
		return
	}
	dateOrNil := func(k string) any {
		if d, err := ParseDate(r.FormValue(k)); err == nil {
			return d.Format("2006-01-02")
		}
		return nil
	}
	var exp any
	if e := parseInt64(r.FormValue("experience_id")); e > 0 {
		exp = e
	}
	nameTH, nameEN := clip(r.FormValue("name_th"), 120), clip(r.FormValue("name_en"), 120)
	if r.FormValue("active") == "on" { // publishing a draft: drop the draft marker from its name
		nameTH = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(nameTH), "(ร่าง)"))
		nameEN = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(nameEN), "(draft)"))
	}
	args := []any{kind, cat, nameTH, nameEN, clip(r.FormValue("desc_th"), 1000), clip(r.FormValue("desc_en"), 1000),
		price, mode, minP, maxP, credits, atoi("validity_days", 0), exp, wd, startAfter, dateOrNil("available_from"), dateOrNil("available_until"),
		r.FormValue("public") == "on", r.FormValue("active") == "on", atoi("sort", 0)}
	if id > 0 {
		_, err = a.DB.ExecContext(ctx, `UPDATE products SET kind=$1,category=$2,name_th=$3,name_en=$4,desc_th=$5,desc_en=$6,price=$7,price_mode=$8,min_participants=$9,max_participants=$10,
			credits=$11,validity_days=$12,experience_id=$13,allowed_weekdays=$14,start_after=$15,available_from=$16,available_until=$17,public=$18,active=$19,sort=$20 WHERE id=$21`, append(args, id)...)
	} else {
		err = a.DB.QueryRowContext(ctx, `INSERT INTO products(kind,category,name_th,name_en,desc_th,desc_en,price,price_mode,min_participants,max_participants,credits,validity_days,experience_id,
			allowed_weekdays,start_after,available_from,available_until,public,active,sort) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20) RETURNING id`, args...).Scan(&id)
	}
	if err != nil {
		back(w, r, "/admin/products", "", errText(err))
		return
	}
	Audit(ctx, a.DB, s.ID, "", "product.save", "product", id, map[string]any{"name": r.FormValue("name_th"), "price": price, "active": r.FormValue("active") == "on"}, clientIP(r))
	back(w, r, "/admin/products", "บันทึก "+r.FormValue("name_th")+" แล้ว", "")
}

// ---------- promo & referral codes ----------

type promoRow struct {
	ID                         int64
	Code, Description, Kind    string
	Value                      int64
	MaxUses, Used, PerCustomer int
	MinParticipants            int
	ProductIDs                 []int64
	From, Until                sql.NullTime
	Referrer                   sql.NullString
	Campaign                   string
	Active                     bool
	Revenue                    int64
	Discounts                  int64
}

func (a *App) handlePromos(w http.ResponseWriter, r *http.Request) {
	rows, err := a.DB.QueryContext(r.Context(), `SELECT p.id, p.code, p.description, p.kind, p.value, p.max_uses, p.used_count, p.per_customer_limit, p.min_participants, p.product_ids,
		p.valid_from, p.valid_until, c.name, p.campaign, p.active,
		COALESCE((SELECT SUM(b.total) FROM bookings b WHERE b.promo_code_id=p.id AND b.status IN ('confirmed','checked_in','no_show')),0),
		COALESCE((SELECT SUM(amount) FROM promo_redemptions pr WHERE pr.promo_id=p.id AND pr.reversed_at IS NULL),0)
		FROM promo_codes p LEFT JOIN customers c ON c.id=p.referrer_customer_id ORDER BY p.active DESC, p.id DESC`)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	defer rows.Close()
	var list []promoRow
	for rows.Next() {
		var p promoRow
		var pids pq.Int64Array
		rows.Scan(&p.ID, &p.Code, &p.Description, &p.Kind, &p.Value, &p.MaxUses, &p.Used, &p.PerCustomer, &p.MinParticipants, &pids, &p.From, &p.Until, &p.Referrer, &p.Campaign, &p.Active, &p.Revenue, &p.Discounts)
		p.ProductIDs = pids
		list = append(list, p)
	}
	prods, _ := ListProducts(r.Context(), a.DB, `WHERE kind='ticket'`)
	a.adminRender(w, r, "admin/promos", 200, "โค้ดส่วนลดและโค้ดแนะนำเพื่อน", map[string]any{"List": list, "Products": prods})
}

var codeRe = regexp.MustCompile(`^[A-Z0-9_-]{3,30}$`)

func (a *App) handlePromoSave(w http.ResponseWriter, r *http.Request) {
	s := staffOf(r)
	ctx := r.Context()
	if id := parseInt64(r.FormValue("toggle")); id > 0 {
		a.DB.ExecContext(ctx, `UPDATE promo_codes SET active = NOT active WHERE id=$1`, id)
		Audit(ctx, a.DB, s.ID, "", "promo.toggle", "promo", id, nil, clientIP(r))
		back(w, r, "/admin/promos", "อัปเดตสถานะโค้ดแล้ว", "")
		return
	}
	code := strings.ToUpper(strings.TrimSpace(r.FormValue("code")))
	if !codeRe.MatchString(code) {
		back(w, r, "/admin/promos", "", "โค้ดต้องเป็น A-Z 0-9 ยาว 3–30 ตัว")
		return
	}
	kind := r.FormValue("kind")
	var value int64
	if kind == "percent" {
		v, err := strconv.Atoi(r.FormValue("value"))
		if err != nil || v < 1 || v > 100 {
			back(w, r, "/admin/promos", "", "เปอร์เซ็นต์ต้องอยู่ระหว่าง 1–100")
			return
		}
		value = int64(v)
	} else {
		kind = "fixed"
		v, err := ParseBaht(r.FormValue("value"))
		if err != nil || v <= 0 {
			back(w, r, "/admin/promos", "", "ส่วนลดไม่ถูกต้อง")
			return
		}
		value = v
	}
	atoi := func(k string) int { n, _ := strconv.Atoi(r.FormValue(k)); return n }
	ts := func(k string, end bool) any {
		d, err := ParseDate(r.FormValue(k))
		if err != nil {
			return nil
		}
		if end {
			return d.AddDate(0, 0, 1).Add(-time.Second)
		}
		return d
	}
	var pids pq.Int64Array
	for _, p := range r.Form["product_ids"] {
		if n := parseInt64(p); n > 0 {
			pids = append(pids, n)
		}
	}
	var referrer any
	if ph := NormPhone(r.FormValue("referrer_phone")); ph != "" {
		var cid int64
		if a.DB.QueryRowContext(ctx, `SELECT id FROM customers WHERE phone=$1 ORDER BY id LIMIT 1`, ph).Scan(&cid) != nil {
			back(w, r, "/admin/promos", "", "ไม่พบลูกค้าผู้แนะนำจากเบอร์นี้")
			return
		}
		referrer = cid
	}
	minP := atoi("min_participants")
	if minP < 1 {
		minP = 1
	}
	perC := atoi("per_customer_limit")
	var id int64
	err := a.DB.QueryRowContext(ctx, `INSERT INTO promo_codes(code,description,kind,value,max_uses,per_customer_limit,min_participants,product_ids,valid_from,valid_until,referrer_customer_id,campaign)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING id`,
		code, clip(r.FormValue("description"), 200), kind, value, atoi("max_uses"), perC, minP, pids, ts("valid_from", false), ts("valid_until", true), referrer, clip(r.FormValue("campaign"), 100)).Scan(&id)
	if err != nil {
		back(w, r, "/admin/promos", "", errText(err))
		return
	}
	Audit(ctx, a.DB, s.ID, "", "promo.create", "promo", code, map[string]any{"kind": kind, "value": value}, clientIP(r))
	back(w, r, "/admin/promos", "สร้างโค้ด "+code+" แล้ว", "")
}

// ---------- passes / gift vouchers ----------

func (a *App) handlePasses(w http.ResponseWriter, r *http.Request) {
	q := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("q")))
	rows, err := a.DB.QueryContext(r.Context(), `SELECT ps.id, ps.code, p.name_th, ps.credits_total, ps.credits_used, ps.status, ps.expires_at, ps.gift_to_name, ps.note, ps.created_at,
		COALESCE(c.name,''), COALESCE(c.phone,''), COALESCE(c.id,0)
		FROM passes ps JOIN products p ON p.id=ps.product_id LEFT JOIN customers c ON c.id=ps.customer_id
		WHERE ($1='' OR ps.code LIKE '%'||$1||'%' OR upper(c.name) LIKE '%'||$1||'%' OR c.phone LIKE '%'||$1||'%') AND ps.status <> 'pending'
		ORDER BY ps.id DESC LIMIT 300`, q)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	defer rows.Close()
	type pr struct {
		ID              int64
		Code, Product   string
		Total, Used     int
		Status          string
		Exp             sql.NullTime
		GiftTo, Note    string
		Created         time.Time
		Customer, Phone string
		CustomerID      int64
	}
	var list []pr
	for rows.Next() {
		var x pr
		rows.Scan(&x.ID, &x.Code, &x.Product, &x.Total, &x.Used, &x.Status, &x.Exp, &x.GiftTo, &x.Note, &x.Created, &x.Customer, &x.Phone, &x.CustomerID)
		list = append(list, x)
	}
	prods, _ := ListProducts(r.Context(), a.DB, `WHERE kind='pass'`)
	a.adminRender(w, r, "admin/passes", 200, "แพ็กเกจ บัตรของขวัญ และสิทธิ์", map[string]any{"List": list, "Products": prods, "Q": q})
}

// handlePassIssue lets managers issue a pass manually (complimentary, paid at the counter, migration from the old sheet).
func (a *App) handlePassIssue(w http.ResponseWriter, r *http.Request) {
	s := staffOf(r)
	ctx := r.Context()
	p, err := GetProduct(ctx, a.DB, parseInt64(r.FormValue("product_id")))
	if err != nil || p.Kind != "pass" {
		back(w, r, "/admin/passes", "", "เลือกแพ็กเกจ")
		return
	}
	credits, _ := strconv.Atoi(r.FormValue("credits"))
	if credits <= 0 {
		credits = p.Credits
	}
	var custID any
	if name := strings.TrimSpace(r.FormValue("name")); name != "" {
		phone := NormPhone(r.FormValue("phone"))
		if phone != "" && !ValidPhone(phone) {
			back(w, r, "/admin/passes", "", "เบอร์โทรไม่ถูกต้อง")
			return
		}
		id, err := UpsertCustomer(ctx, a.DB, clip(name, 120), phone, NormEmail(r.FormValue("email")), "th", false)
		if err != nil {
			back(w, r, "/admin/passes", "", errText(err))
			return
		}
		custID = id
	}
	var exp any
	if d, err := ParseDate(r.FormValue("expires")); err == nil {
		exp = d.AddDate(0, 0, 1).Add(-time.Second)
	} else if p.ValidityDays > 0 {
		exp = Now().AddDate(0, 0, p.ValidityDays)
	}
	code := NewPassCode()
	note := clip(r.FormValue("note"), 300)
	paidAt := r.FormValue("paid_venue")
	err = a.inTx(ctx, func(tx *sql.Tx) error {
		var passID int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO passes(code,product_id,customer_id,credits_total,status,expires_at,issued_by,note,gift_to_name) VALUES($1,$2,$3,$4,'active',$5,$6,$7,$8) RETURNING id`,
			code, p.ID, custID, credits, exp, s.ID, note, clip(r.FormValue("gift_to"), 120)).Scan(&passID); err != nil {
			return err
		}
		if paidAt == "cash" || paidAt == "transfer" || paidAt == "card_terminal" {
			// Counter sale: record revenue as a venue payment against a pass order.
			if custID == nil {
				return fmt.Errorf("ขายที่หน้าร้านต้องใส่ชื่อลูกค้า")
			}
			var oid int64
			if err := tx.QueryRowContext(ctx, `INSERT INTO pass_orders(ref,pass_id,customer_id,amount,status,manage_token_hash) VALUES($1,$2,$3,$4,'paid',$5) RETURNING id`,
				"PDV-"+RandomCode(6), passID, custID, p.Price, HashToken(RandomToken())).Scan(&oid); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO payments(pass_order_id,provider,method,status,amount,paid_at) VALUES($1,'venue',$2,'paid',$3,now())`, oid, paidAt, p.Price); err != nil {
				return err
			}
		}
		Audit(ctx, tx, s.ID, "", "pass.issue", "pass", code, map[string]any{"product": p.NameTH, "credits": credits, "paid": paidAt, "note": note}, clientIP(r))
		return nil
	})
	if err != nil {
		back(w, r, "/admin/passes", "", errText(err))
		return
	}
	back(w, r, "/admin/passes?q="+code, "ออกบัตร "+code+" แล้ว", "")
}

func (a *App) handlePassUpdate(w http.ResponseWriter, r *http.Request) {
	s := staffOf(r)
	id := parseInt64(r.PathValue("id"))
	var err error
	switch r.FormValue("action") {
	case "void":
		_, err = a.DB.ExecContext(r.Context(), `UPDATE passes SET status='void' WHERE id=$1`, id)
	case "extend":
		d, perr := ParseDate(r.FormValue("expires"))
		if perr != nil {
			err = perr
			break
		}
		_, err = a.DB.ExecContext(r.Context(), `UPDATE passes SET expires_at=$2, status=CASE WHEN status='expired' THEN 'active' ELSE status END WHERE id=$1`, id, d.AddDate(0, 0, 1).Add(-time.Second))
	case "adjust":
		n, _ := strconv.Atoi(r.FormValue("credits_total"))
		_, err = a.DB.ExecContext(r.Context(), `UPDATE passes SET credits_total=$2 WHERE id=$1 AND $2 >= credits_used`, id, n)
	}
	if err != nil {
		back(w, r, "/admin/passes", "", errText(err))
		return
	}
	Audit(r.Context(), a.DB, s.ID, "", "pass."+r.FormValue("action"), "pass", id, map[string]any{"expires": r.FormValue("expires"), "credits": r.FormValue("credits_total")}, clientIP(r))
	back(w, r, "/admin/passes", "อัปเดตบัตรแล้ว", "")
}

// ---------- payments ledger ----------

func (a *App) handlePayments(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from, err := ParseDate(q.Get("from"))
	if err != nil {
		from = DateOnly(Now()).AddDate(0, 0, -30)
	}
	to, err := ParseDate(q.Get("to"))
	if err != nil {
		to = DateOnly(Now())
	}
	rows, err := a.DB.QueryContext(r.Context(), `SELECT p.id, p.provider, p.method, p.status, p.amount, p.refunded_amount, COALESCE(p.provider_session,''), COALESCE(p.provider_payment,''),
		p.paid_at, p.created_at, COALESCE(b.ref, o.ref, ''), COALESCE(c1.name, c2.name, '')
		FROM payments p LEFT JOIN bookings b ON b.id=p.booking_id LEFT JOIN pass_orders o ON o.id=p.pass_order_id
		LEFT JOIN customers c1 ON c1.id=b.customer_id LEFT JOIN customers c2 ON c2.id=o.customer_id
		WHERE p.created_at >= $1 AND p.created_at < $2 AND ($3='' OR p.status=$3) ORDER BY p.id DESC LIMIT 2000`, from, to.AddDate(0, 0, 1), q.Get("status"))
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	defer rows.Close()
	type pr struct {
		ID                       int64
		Provider, Method, Status string
		Amount, Refunded         int64
		Session, PaymentRef      string
		PaidAt                   sql.NullTime
		Created                  time.Time
		Ref, Customer            string
	}
	var list []pr
	var totals struct{ Paid, Refunded, Pending int64 }
	for rows.Next() {
		var x pr
		rows.Scan(&x.ID, &x.Provider, &x.Method, &x.Status, &x.Amount, &x.Refunded, &x.Session, &x.PaymentRef, &x.PaidAt, &x.Created, &x.Ref, &x.Customer)
		list = append(list, x)
		switch x.Status {
		case "paid", "refunded", "partially_refunded":
			totals.Paid += x.Amount
			totals.Refunded += x.Refunded
		case "pending":
			totals.Pending += x.Amount
		}
	}
	if q.Get("format") == "csv" {
		Audit(r.Context(), a.DB, staffOf(r).ID, "", "export.payments", "", "", map[string]any{"rows": len(list)}, clientIP(r))
		out := [][]string{{"id", "ref", "customer", "provider", "method", "status", "amount_thb", "refunded_thb", "provider_session", "provider_payment", "paid_at", "created_at"}}
		for _, x := range list {
			pa := ""
			if x.PaidAt.Valid {
				pa = x.PaidAt.Time.In(BKK).Format(time.RFC3339)
			}
			out = append(out, []string{num(x.ID), x.Ref, x.Customer, x.Provider, x.Method, x.Status, fmt.Sprintf("%.2f", float64(x.Amount)/100), fmt.Sprintf("%.2f", float64(x.Refunded)/100), x.Session, x.PaymentRef, pa, x.Created.In(BKK).Format(time.RFC3339)})
		}
		writeCSV(w, "payments", out)
		return
	}
	var unprocessed []map[string]any
	er, _ := a.DB.QueryContext(r.Context(), `SELECT provider, event_id, type, error, created_at FROM payment_events WHERE processed_at IS NULL ORDER BY id DESC LIMIT 20`)
	for er != nil && er.Next() {
		var p, id, t, e string
		var c time.Time
		er.Scan(&p, &id, &t, &e, &c)
		unprocessed = append(unprocessed, map[string]any{"Provider": p, "ID": id, "Type": t, "Err": e, "At": c})
	}
	if er != nil {
		er.Close()
	}
	a.adminRender(w, r, "admin/payments", 200, "การชำระเงิน", map[string]any{"List": list, "Totals": totals, "From": from, "To": to, "Q": q, "Unprocessed": unprocessed, "Provider": a.Pay.Name()})
}

// ---------- marketing: campaigns, ad spend, tracking ----------

func (a *App) handleMarketing(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	set, _ := LoadSettings(ctx, a.DB)
	type spend struct {
		ID               int64
		Date             time.Time
		Source, Campaign string
		Amount           int64
		Note             string
	}
	var spends []spend
	rows, _ := a.DB.QueryContext(ctx, `SELECT id, spend_date, source, campaign, amount, note FROM ad_spend ORDER BY spend_date DESC, id DESC LIMIT 100`)
	for rows != nil && rows.Next() {
		var x spend
		rows.Scan(&x.ID, &x.Date, &x.Source, &x.Campaign, &x.Amount, &x.Note)
		spends = append(spends, x)
	}
	if rows != nil {
		rows.Close()
	}
	type camp struct {
		ID               int64
		Name, UTM, Notes string
		Starts, Ends     sql.NullTime
		Bookings         int64
		Revenue, Spend   int64
	}
	var camps []camp
	rows, _ = a.DB.QueryContext(ctx, `SELECT c.id, c.name, c.utm_campaign, c.notes, c.starts_on, c.ends_on,
		(SELECT count(*) FROM bookings b WHERE b.utm_campaign=c.utm_campaign AND b.status IN ('confirmed','checked_in','no_show')),
		COALESCE((SELECT SUM(p.amount-p.refunded_amount) FROM payments p JOIN bookings b ON b.id=p.booking_id WHERE b.utm_campaign=c.utm_campaign AND p.status IN ('paid','refunded','partially_refunded')),0),
		COALESCE((SELECT SUM(amount) FROM ad_spend s WHERE s.campaign=c.utm_campaign),0)
		FROM campaigns c ORDER BY c.id DESC`)
	for rows != nil && rows.Next() {
		var x camp
		rows.Scan(&x.ID, &x.Name, &x.UTM, &x.Notes, &x.Starts, &x.Ends, &x.Bookings, &x.Revenue, &x.Spend)
		camps = append(camps, x)
	}
	if rows != nil {
		rows.Close()
	}
	a.adminRender(w, r, "admin/marketing", 200, "การตลาด", map[string]any{"Set": set, "Spend": spends, "Campaigns": camps, "BaseURL": a.Cfg.BaseURL, "CAPI": a.Cfg.MetaAccessToken != ""})
}

func (a *App) handleMarketingSave(w http.ResponseWriter, r *http.Request) {
	s := staffOf(r)
	ctx := r.Context()
	var err error
	msg := "บันทึกแล้ว"
	switch r.FormValue("form") {
	case "tracking":
		for _, k := range []string{"ga4_id", "gtm_id", "meta_pixel_id", "google_ads_id", "google_ads_label", "instagram_url", "facebook_url", "tiktok_url", "google_business_url", "line_id"} {
			v := clip(r.FormValue(k), 300)
			if strings.HasSuffix(k, "_url") && v != "" && !strings.HasPrefix(v, "https://") {
				back(w, r, "/admin/marketing", "", "ลิงก์ต้องขึ้นต้นด้วย https://")
				return
			}
			if (k == "ga4_id" || k == "gtm_id" || k == "meta_pixel_id" || k == "google_ads_id" || k == "google_ads_label") && v != "" && !regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(v) {
				back(w, r, "/admin/marketing", "", "รหัสติดตามมีตัวอักษรไม่ถูกต้อง: "+k)
				return
			}
			if err = SaveSetting(ctx, a.DB, k, v); err != nil {
				break
			}
		}
		Audit(ctx, a.DB, s.ID, "", "marketing.tracking", "settings", "", nil, clientIP(r))
	case "spend":
		d, derr := ParseDate(r.FormValue("date"))
		amt, aerr := ParseBaht(r.FormValue("amount"))
		if derr != nil || aerr != nil {
			back(w, r, "/admin/marketing", "", "วันที่หรือจำนวนเงินไม่ถูกต้อง")
			return
		}
		_, err = a.DB.ExecContext(ctx, `INSERT INTO ad_spend(spend_date,source,campaign,amount,note) VALUES($1,$2,$3,$4,$5)`, d.Format("2006-01-02"),
			strings.ToLower(clip(r.FormValue("source"), 60)), clip(r.FormValue("campaign"), 150), amt, clip(r.FormValue("note"), 200))
		msg = "บันทึกค่าโฆษณาแล้ว"
	case "spend_delete":
		_, err = a.DB.ExecContext(ctx, `DELETE FROM ad_spend WHERE id=$1`, parseInt64(r.FormValue("id")))
		msg = "ลบรายการแล้ว"
	case "campaign":
		utm := strings.ToLower(regexp.MustCompile(`[^a-zA-Z0-9_-]+`).ReplaceAllString(r.FormValue("utm_campaign"), "-"))
		if utm == "" {
			back(w, r, "/admin/marketing", "", "ใส่ utm_campaign")
			return
		}
		dn := func(k string) any {
			if d, err := ParseDate(r.FormValue(k)); err == nil {
				return d.Format("2006-01-02")
			}
			return nil
		}
		_, err = a.DB.ExecContext(ctx, `INSERT INTO campaigns(name,utm_campaign,starts_on,ends_on,notes) VALUES($1,$2,$3,$4,$5)`, clip(r.FormValue("name"), 120), utm, dn("starts_on"), dn("ends_on"), clip(r.FormValue("notes"), 500))
		msg = "สร้างแคมเปญแล้ว"
	}
	if err != nil {
		back(w, r, "/admin/marketing", "", errText(err))
		return
	}
	back(w, r, "/admin/marketing", msg, "")
}

// ---------- content: gallery, reviews, SEO meta, landing pages ----------

func (a *App) handleContent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	type img struct {
		ID               int64
		AltTH, Placement string
		Sort, Size       int
	}
	var imgs []img
	rows, _ := a.DB.QueryContext(ctx, `SELECT id, alt_th, placement, sort, length(data) FROM gallery_images ORDER BY placement, sort, id`)
	for rows != nil && rows.Next() {
		var x img
		rows.Scan(&x.ID, &x.AltTH, &x.Placement, &x.Sort, &x.Size)
		imgs = append(imgs, x)
	}
	if rows != nil {
		rows.Close()
	}
	type rev struct {
		ID                        int64
		Author, Body, Source, URL string
		Rating                    int
		Published                 bool
	}
	var revs []rev
	rows, _ = a.DB.QueryContext(ctx, `SELECT id, author, body, source, source_url, rating, published FROM reviews ORDER BY id DESC`)
	for rows != nil && rows.Next() {
		var x rev
		rows.Scan(&x.ID, &x.Author, &x.Body, &x.Source, &x.URL, &x.Rating, &x.Published)
		revs = append(revs, x)
	}
	if rows != nil {
		rows.Close()
	}
	type meta struct{ Path, TitleTH, TitleEN, DescTH, DescEN string }
	var metas []meta
	rows, _ = a.DB.QueryContext(ctx, `SELECT path, title_th, title_en, desc_th, desc_en FROM page_meta ORDER BY path`)
	for rows != nil && rows.Next() {
		var x meta
		rows.Scan(&x.Path, &x.TitleTH, &x.TitleEN, &x.DescTH, &x.DescEN)
		metas = append(metas, x)
	}
	if rows != nil {
		rows.Close()
	}
	type lp struct {
		ID                                            int64
		Slug, TitleTH, TitleEN, BodyTH, BodyEN, Promo string
		Product                                       sql.NullInt64
		Published                                     bool
	}
	var lps []lp
	rows, _ = a.DB.QueryContext(ctx, `SELECT id, slug, title_th, title_en, body_th, body_en, promo_code, cta_product, published FROM landing_pages ORDER BY id DESC`)
	for rows != nil && rows.Next() {
		var x lp
		rows.Scan(&x.ID, &x.Slug, &x.TitleTH, &x.TitleEN, &x.BodyTH, &x.BodyEN, &x.Promo, &x.Product, &x.Published)
		lps = append(lps, x)
	}
	if rows != nil {
		rows.Close()
	}
	prods, _ := ListProducts(ctx, a.DB, `WHERE kind='ticket'`)
	a.adminRender(w, r, "admin/content", 200, "เนื้อหาเว็บไซต์และ SEO", map[string]any{"Images": imgs, "Reviews": revs, "Meta": metas, "Landing": lps, "Products": prods, "BaseURL": a.Cfg.BaseURL,
		"Paths": []string{"/", "/experiences", "/pricing", "/book", "/story", "/gallery", "/faq", "/contact"}})
}

func (a *App) handleContentSave(w http.ResponseWriter, r *http.Request) {
	s := staffOf(r)
	ctx := r.Context()
	r.Body = http.MaxBytesReader(w, r.Body, 6<<20)
	form := r.FormValue("form")
	if form == "" && r.MultipartForm == nil {
		r.ParseMultipartForm(6 << 20)
		form = r.FormValue("form")
	}
	var err error
	msg := "บันทึกแล้ว"
	switch form {
	case "image":
		f, hdr, ferr := r.FormFile("file")
		if ferr != nil {
			back(w, r, "/admin/content", "", "เลือกไฟล์รูป (JPG/PNG/WebP ไม่เกิน 5 MB)")
			return
		}
		defer f.Close()
		data, _ := io.ReadAll(io.LimitReader(f, 5<<20+1))
		mime := http.DetectContentType(data)
		if len(data) > 5<<20 || !(mime == "image/jpeg" || mime == "image/png" || mime == "image/webp") {
			back(w, r, "/admin/content", "", "รองรับ JPG/PNG/WebP ไม่เกิน 5 MB")
			return
		}
		pl := r.FormValue("placement")
		if pl != "hero" && pl != "experience" && pl != "story" {
			pl = "gallery"
		}
		sort, _ := strconv.Atoi(r.FormValue("sort"))
		_, err = a.DB.ExecContext(ctx, `INSERT INTO gallery_images(data,mime,alt_th,alt_en,placement,sort) VALUES($1,$2,$3,$4,$5,$6)`, data, mime,
			clip(r.FormValue("alt_th"), 200), clip(r.FormValue("alt_en"), 200), pl, sort)
		msg = "อัปโหลด " + hdr.Filename + " แล้ว"
	case "image_delete":
		_, err = a.DB.ExecContext(ctx, `DELETE FROM gallery_images WHERE id=$1`, parseInt64(r.FormValue("id")))
		msg = "ลบรูปแล้ว"
	case "review":
		rating, _ := strconv.Atoi(r.FormValue("rating"))
		src := strings.TrimSpace(r.FormValue("source_url"))
		if !strings.HasPrefix(src, "https://") || rating < 1 || rating > 5 || strings.TrimSpace(r.FormValue("body")) == "" {
			back(w, r, "/admin/content", "", "รีวิวต้องมีข้อความ คะแนน 1–5 และลิงก์ต้นฉบับ (https://) เพื่อยืนยันว่าเป็นรีวิวจริง")
			return
		}
		var d any
		if x, e := ParseDate(r.FormValue("review_date")); e == nil {
			d = x.Format("2006-01-02")
		}
		_, err = a.DB.ExecContext(ctx, `INSERT INTO reviews(author,rating,body,source,source_url,review_date) VALUES($1,$2,$3,$4,$5,$6)`,
			clip(r.FormValue("author"), 80), rating, clip(r.FormValue("body"), 800), clip(r.FormValue("source"), 40), src, d)
		msg = "เพิ่มรีวิวแล้ว"
	case "review_toggle":
		_, err = a.DB.ExecContext(ctx, `UPDATE reviews SET published = NOT published WHERE id=$1`, parseInt64(r.FormValue("id")))
	case "meta":
		path := r.FormValue("path")
		_, err = a.DB.ExecContext(ctx, `INSERT INTO page_meta(path,title_th,title_en,desc_th,desc_en) VALUES($1,$2,$3,$4,$5)
			ON CONFLICT (path) DO UPDATE SET title_th=$2,title_en=$3,desc_th=$4,desc_en=$5`, path,
			clip(r.FormValue("title_th"), 70), clip(r.FormValue("title_en"), 70), clip(r.FormValue("desc_th"), 170), clip(r.FormValue("desc_en"), 170))
		msg = "บันทึก SEO ของ " + path + " แล้ว"
	case "landing":
		slug := strings.Trim(regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(strings.ToLower(r.FormValue("slug")), "-"), "-")
		if slug == "" {
			back(w, r, "/admin/content", "", "ใส่ slug ภาษาอังกฤษ")
			return
		}
		var prod any
		if p := parseInt64(r.FormValue("cta_product")); p > 0 {
			prod = p
		}
		_, err = a.DB.ExecContext(ctx, `INSERT INTO landing_pages(slug,title_th,title_en,body_th,body_en,cta_product,promo_code,published) VALUES($1,$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT (slug) DO UPDATE SET title_th=$2,title_en=$3,body_th=$4,body_en=$5,cta_product=$6,promo_code=$7,published=$8`,
			slug, clip(r.FormValue("title_th"), 120), clip(r.FormValue("title_en"), 120), clip(r.FormValue("body_th"), 4000), clip(r.FormValue("body_en"), 4000), prod,
			strings.ToUpper(clip(r.FormValue("promo_code"), 30)), r.FormValue("published") == "on")
		msg = "บันทึกหน้า /lp/" + slug + " แล้ว"
	}
	if err != nil {
		back(w, r, "/admin/content", "", errText(err))
		return
	}
	Audit(ctx, a.DB, s.ID, "", "content."+form, "content", "", nil, clientIP(r))
	back(w, r, "/admin/content", msg, "")
}

// ---------- staff ----------

func (a *App) handleStaff(w http.ResponseWriter, r *http.Request) {
	rows, _ := a.DB.QueryContext(r.Context(), `SELECT id, email, name, role, active, last_login_at, locked_until FROM staff_users ORDER BY active DESC, id`)
	type st struct {
		ID                int64
		Email, Name, Role string
		Active            bool
		Last, Locked      sql.NullTime
	}
	var list []st
	for rows != nil && rows.Next() {
		var x st
		rows.Scan(&x.ID, &x.Email, &x.Name, &x.Role, &x.Active, &x.Last, &x.Locked)
		list = append(list, x)
	}
	if rows != nil {
		rows.Close()
	}
	a.adminRender(w, r, "admin/staff", 200, "ทีมงานและสิทธิ์", map[string]any{"List": list, "Roles": RoleNames, "Perms": rolePerms})
}

func (a *App) handleStaffSave(w http.ResponseWriter, r *http.Request) {
	s := staffOf(r)
	ctx := r.Context()
	role := r.FormValue("role")
	if _, ok := RoleNames[role]; !ok {
		back(w, r, "/admin/staff", "", "เลือกตำแหน่ง")
		return
	}
	if id := parseInt64(r.FormValue("id")); id > 0 {
		if id == s.ID && (role != "owner" || r.FormValue("active") != "on") {
			back(w, r, "/admin/staff", "", "เปลี่ยนสิทธิ์หรือปิดบัญชีของตัวเองไม่ได้")
			return
		}
		_, err := a.DB.ExecContext(ctx, `UPDATE staff_users SET name=$2, role=$3, active=$4 WHERE id=$1`, id, clip(r.FormValue("name"), 80), role, r.FormValue("active") == "on")
		if err == nil && r.FormValue("active") != "on" {
			a.DB.ExecContext(ctx, `DELETE FROM staff_sessions WHERE user_id=$1`, id)
		}
		if pw := r.FormValue("password"); err == nil && pw != "" {
			if len(pw) < 10 {
				back(w, r, "/admin/staff", "", "รหัสผ่านต้องยาวอย่างน้อย 10 ตัวอักษร")
				return
			}
			h, _ := HashPassword(pw)
			_, err = a.DB.ExecContext(ctx, `UPDATE staff_users SET password_hash=$2, failed_logins=0, locked_until=NULL WHERE id=$1`, id, h)
			a.DB.ExecContext(ctx, `DELETE FROM staff_sessions WHERE user_id=$1`, id)
		}
		if err != nil {
			back(w, r, "/admin/staff", "", errText(err))
			return
		}
		Audit(ctx, a.DB, s.ID, "", "staff.update", "staff", id, map[string]any{"role": role, "active": r.FormValue("active") == "on", "password_reset": r.FormValue("password") != ""}, clientIP(r))
		back(w, r, "/admin/staff", "บันทึกแล้ว", "")
		return
	}
	email := NormEmail(r.FormValue("email"))
	pw := r.FormValue("password")
	if !ValidEmail(email) || len(pw) < 10 {
		back(w, r, "/admin/staff", "", "ใส่อีเมลที่ถูกต้อง และรหัสผ่านอย่างน้อย 10 ตัวอักษร")
		return
	}
	h, _ := HashPassword(pw)
	var id int64
	if err := a.DB.QueryRowContext(ctx, `INSERT INTO staff_users(email,name,password_hash,role) VALUES($1,$2,$3,$4) RETURNING id`, email, clip(r.FormValue("name"), 80), h, role).Scan(&id); err != nil {
		back(w, r, "/admin/staff", "", errText(err))
		return
	}
	Audit(ctx, a.DB, s.ID, "", "staff.create", "staff", id, map[string]any{"email": email, "role": role}, clientIP(r))
	back(w, r, "/admin/staff", "เพิ่ม "+email+" แล้ว ส่งรหัสผ่านให้เขาทางช่องทางที่ปลอดภัย", "")
}

func (a *App) handleAccount(w http.ResponseWriter, r *http.Request) {
	a.adminRender(w, r, "admin/account", 200, "บัญชีของฉัน", nil)
}

func (a *App) handleAccountSave(w http.ResponseWriter, r *http.Request) {
	s := staffOf(r)
	var hash string
	a.DB.QueryRowContext(r.Context(), `SELECT password_hash FROM staff_users WHERE id=$1`, s.ID).Scan(&hash)
	if !CheckPassword(hash, r.FormValue("current")) {
		back(w, r, "/admin/account", "", "รหัสผ่านปัจจุบันไม่ถูกต้อง")
		return
	}
	pw := r.FormValue("new")
	if len(pw) < 10 || pw != r.FormValue("confirm") {
		back(w, r, "/admin/account", "", "รหัสผ่านใหม่ต้องยาวอย่างน้อย 10 ตัว และพิมพ์ให้ตรงกันทั้งสองช่อง")
		return
	}
	h, _ := HashPassword(pw)
	a.DB.ExecContext(r.Context(), `UPDATE staff_users SET password_hash=$2 WHERE id=$1`, s.ID, h)
	a.DB.ExecContext(r.Context(), `DELETE FROM staff_sessions WHERE user_id=$1 AND token_hash <> $2`, s.ID, func() string { c, _ := r.Cookie("pd_staff"); return HashToken(c.Value) }())
	Audit(r.Context(), a.DB, s.ID, "", "staff.password_change", "staff", s.ID, nil, clientIP(r))
	back(w, r, "/admin/account", "เปลี่ยนรหัสผ่านแล้ว", "")
}

// ---------- settings ----------

var settingFields = []struct {
	Key, Label, Kind, Help string
}{
	{"business_name", "ชื่อร้าน", "text", ""},
	{"phone", "เบอร์โทรร้าน", "text", "แสดงบนเว็บและในอีเมล"},
	{"email", "อีเมลร้าน", "text", ""},
	{"address_th", "ที่อยู่ (ไทย)", "text", ""},
	{"address_en", "ที่อยู่ (อังกฤษ)", "text", ""},
	{"maps_url", "ลิงก์ Google Maps", "text", ""},
	{"maps_query", "คำค้นแผนที่ (สำหรับแผนที่ฝังในหน้าเว็บ)", "text", ""},
	{"open_time", "เวลาเปิด (แสดงบนเว็บ)", "text", "HH:MM — รอบจริงกำหนดในหน้าตารางเปิดรอบ"},
	{"close_time", "เวลาปิด (แสดงบนเว็บ)", "text", "HH:MM"},
	{"hold_minutes", "เวลากันที่ระหว่างชำระเงิน (นาที)", "int", "Stripe กำหนดขั้นต่ำ 30 นาที"},
	{"booking_horizon_days", "เปิดให้จองล่วงหน้าได้ (วัน)", "int", ""},
	{"min_lead_minutes", "ปิดรับจองออนไลน์ก่อนรอบเริ่ม (นาที)", "int", ""},
	{"cancel_cutoff_hours", "ลูกค้ายกเลิกเองได้ก่อนรอบ (ชั่วโมง)", "int", ""},
	{"reschedule_cutoff_hours", "ลูกค้าเลื่อนเองได้ก่อนรอบ (ชั่วโมง)", "int", ""},
	{"max_reschedules", "เลื่อนได้สูงสุด (ครั้ง)", "int", ""},
	{"cancel_refund_mode", "เมื่อลูกค้ายกเลิกเอง", "select:refund=คืนเงินเข้าช่องทางเดิม,credit=คืนเป็นบัตรเครดิตเข้าร้าน", ""},
	{"reminder_hours", "ส่งอีเมลเตือนก่อนรอบ (ชั่วโมง, 0 = ไม่ส่ง)", "int", ""},
	{"show_sauna_coming_soon", "แสดงคำถาม ‘ซาวน่าเปิดหรือยัง’ ในหน้าคำถามที่พบบ่อย", "bool", ""},
	{"story_th", "เรื่องราวของเรา (ไทย)", "textarea", "เว้นว่างไว้เพื่อใช้ข้อความเริ่มต้น ขึ้นบรรทัดใหม่ = ย่อหน้าใหม่"},
	{"story_en", "เรื่องราวของเรา (อังกฤษ)", "textarea", ""},
}

func (a *App) handleSettings(w http.ResponseWriter, r *http.Request) {
	set, _ := LoadSettings(r.Context(), a.DB)
	a.adminRender(w, r, "admin/settings", 200, "ตั้งค่า", map[string]any{"Set": set, "Fields": settingFields, "Cfg": map[string]any{
		"Payment": a.Pay.Name(), "BaseURL": a.Cfg.BaseURL, "SMTP": a.Cfg.SMTPHost != "", "AdminAlert": a.Cfg.AdminAlertEmail, "CAPI": a.Cfg.MetaAccessToken != "", "LINE": a.Cfg.LineChannelToken != "", "Env": a.Cfg.Env,
	}})
}

func (a *App) handleSettingsSave(w http.ResponseWriter, r *http.Request) {
	s := staffOf(r)
	ctx := r.Context()
	changed := map[string]any{}
	for _, f := range settingFields {
		var v any
		raw := strings.TrimSpace(r.FormValue(f.Key))
		switch {
		case f.Kind == "int":
			n, err := strconv.Atoi(raw)
			if err != nil || n < 0 || n > 100000 {
				back(w, r, "/admin/settings", "", "ค่าไม่ถูกต้อง: "+f.Label)
				return
			}
			if f.Key == "hold_minutes" && n < 30 && a.Pay.Name() == "stripe" {
				n = 30
			}
			v = n
		case f.Kind == "bool":
			v = raw == "on"
		default:
			if (f.Key == "open_time" || f.Key == "close_time") && !hmRe.MatchString(raw) {
				back(w, r, "/admin/settings", "", "เวลาต้องเป็น HH:MM")
				return
			}
			limit := 300
			if f.Kind == "textarea" {
				limit = 4000
			}
			v = clip(strings.ReplaceAll(raw, "\r\n", "\n"), limit)
		}
		if err := SaveSetting(ctx, a.DB, f.Key, v); err != nil {
			back(w, r, "/admin/settings", "", errText(err))
			return
		}
		changed[f.Key] = v
	}
	Audit(ctx, a.DB, s.ID, "", "settings.update", "settings", "", changed, clientIP(r))
	back(w, r, "/admin/settings", "บันทึกการตั้งค่าแล้ว", "")
}

// ---------- audit & notifications ----------

func (a *App) handleAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rows, err := a.DB.QueryContext(r.Context(), `SELECT a.created_at, COALESCE(u.name, NULLIF(a.actor,''), 'system'), a.action, a.entity, a.entity_id, COALESCE(a.detail::text,''), a.ip
		FROM audit_log a LEFT JOIN staff_users u ON u.id=a.staff_id WHERE ($1='' OR a.action LIKE $1||'%') ORDER BY a.id DESC LIMIT 500`, q.Get("action"))
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	defer rows.Close()
	type ar struct {
		At                                          time.Time
		Actor, Action, Entity, EntityID, Detail, IP string
	}
	var list []ar
	for rows.Next() {
		var x ar
		rows.Scan(&x.At, &x.Actor, &x.Action, &x.Entity, &x.EntityID, &x.Detail, &x.IP)
		list = append(list, x)
	}
	a.adminRender(w, r, "admin/audit", 200, "บันทึกการเปลี่ยนแปลง", map[string]any{"List": list, "Q": q})
}

func (a *App) handleNotifications(w http.ResponseWriter, r *http.Request) {
	rows, err := a.DB.QueryContext(r.Context(), `SELECT id, kind, channel, recipient, subject, status, attempts, last_error, created_at, sent_at FROM notifications ORDER BY id DESC LIMIT 300`)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	defer rows.Close()
	type nr struct {
		ID                                             int64
		Kind, Channel, Recipient, Subject, Status, Err string
		Attempts                                       int
		Created                                        time.Time
		Sent                                           sql.NullTime
	}
	var list []nr
	for rows.Next() {
		var x nr
		rows.Scan(&x.ID, &x.Kind, &x.Channel, &x.Recipient, &x.Subject, &x.Status, &x.Attempts, &x.Err, &x.Created, &x.Sent)
		list = append(list, x)
	}
	a.adminRender(w, r, "admin/notifications", 200, "การแจ้งเตือน", map[string]any{"List": list, "SMTP": a.Cfg.SMTPHost != ""})
}

func (a *App) handleNotificationRetry(w http.ResponseWriter, r *http.Request) {
	a.DB.ExecContext(r.Context(), `UPDATE notifications SET status='queued', next_attempt_at=now(), attempts=0 WHERE id=$1 AND status IN ('failed','skipped')`, parseInt64(r.FormValue("id")))
	go a.DeliverNotifications(context.Background())
	back(w, r, "/admin/notifications", "ส่งใหม่แล้ว", "")
}
