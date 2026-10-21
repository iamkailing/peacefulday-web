package app

import (
	"context"
	"fmt"
	"html/template"
	"math"
	"net/http"
	"net/url"
	"sort"
	"time"
)

// ---------- date range filter ----------

type rangeSel struct {
	Key        string
	From, To   time.Time // [From, To) at Bangkok midnight
	PFrom, PTo time.Time // previous period of equal length
	Days       int
	Label      string
	PLabel     string
	FromStr    string // inclusive, for <input type=date>
	ToStr      string
}

var rangePresets = []struct{ Key, Label string }{
	{"today", "วันนี้"}, {"yesterday", "เมื่อวาน"}, {"7d", "7 วัน"}, {"30d", "30 วัน"}, {"90d", "90 วัน"}, {"custom", "กำหนดเอง"},
}

func parseRange(q url.Values) rangeSel {
	today := DateOnly(Now())
	r := rangeSel{Key: q.Get("range")}
	switch r.Key {
	case "today":
		r.From, r.To = today, today.AddDate(0, 0, 1)
	case "yesterday":
		r.From, r.To = today.AddDate(0, 0, -1), today
	case "7d":
		r.From, r.To = today.AddDate(0, 0, -6), today.AddDate(0, 0, 1)
	case "90d":
		r.From, r.To = today.AddDate(0, 0, -89), today.AddDate(0, 0, 1)
	case "custom":
		f, e1 := ParseDate(q.Get("from"))
		t, e2 := ParseDate(q.Get("to"))
		if e1 == nil && e2 == nil && !t.Before(f) {
			if t.Sub(f) > 400*24*time.Hour {
				f = t.AddDate(0, 0, -400)
			}
			r.From, r.To = f, t.AddDate(0, 0, 1)
			break
		}
		fallthrough
	default:
		r.Key = "30d"
		r.From, r.To = today.AddDate(0, 0, -29), today.AddDate(0, 0, 1)
	}
	r.Days = int(math.Round(r.To.Sub(r.From).Hours() / 24))
	r.PTo, r.PFrom = r.From, r.From.AddDate(0, 0, -r.Days)
	label := func(f, t time.Time) string {
		last := t.AddDate(0, 0, -1)
		if f.Equal(last) {
			return TH.DateShort(f)
		}
		return TH.DateShort(f) + " – " + TH.DateShort(last)
	}
	r.Label, r.PLabel = label(r.From, r.To), label(r.PFrom, r.PTo)
	r.FromStr, r.ToStr = r.From.Format("2006-01-02"), r.To.AddDate(0, 0, -1).Format("2006-01-02")
	return r
}

// ---------- daily facts ----------

type dayRow struct {
	Day                                                   time.Time
	Gross, Refunds, Orders, Created, Guests, CheckedIn    int64
	Cancelled, NoShow, Capacity, Visitors, Starts, Checks int64
}

func (d dayRow) Net() int64 { return d.Gross - d.Refunds }

const dailySQL = `
WITH days AS (SELECT generate_series($1::date, $2::date, interval '1 day')::date AS day)
SELECT days.day, COALESCE(pay.gross,0), COALESCE(ref.amt,0), COALESCE(pay.orders,0), COALESCE(bk.created,0),
  COALESCE(se.guests,0), COALESCE(se.checked,0), COALESCE(se.cancelled,0), COALESCE(se.noshow,0), COALESCE(cap.capacity,0),
  COALESCE(we.visitors,0), COALESCE(we.starts,0), COALESCE(we.checkouts,0)
FROM days
LEFT JOIN (SELECT (paid_at AT TIME ZONE 'Asia/Bangkok')::date d, SUM(amount) gross, count(*) orders FROM payments
  WHERE status IN ('paid','refunded','partially_refunded') AND paid_at >= $3 AND paid_at < $4 GROUP BY 1) pay ON pay.d=days.day
LEFT JOIN (SELECT (created_at AT TIME ZONE 'Asia/Bangkok')::date d, SUM(amount) amt FROM refunds
  WHERE status='succeeded' AND created_at >= $3 AND created_at < $4 GROUP BY 1) ref ON ref.d=days.day
LEFT JOIN (SELECT (created_at AT TIME ZONE 'Asia/Bangkok')::date d, count(*) created FROM bookings
  WHERE status IN ('confirmed','checked_in','no_show','cancelled') AND created_at >= $3 AND created_at < $4 GROUP BY 1) bk ON bk.d=days.day
LEFT JOIN (SELECT (s.starts_at AT TIME ZONE 'Asia/Bangkok')::date d,
    SUM(b.participants) FILTER (WHERE b.status IN ('confirmed','checked_in','no_show')) guests,
    SUM(b.participants) FILTER (WHERE b.status='checked_in') checked,
    count(*) FILTER (WHERE b.status='cancelled') cancelled,
    count(*) FILTER (WHERE b.status='no_show') noshow
  FROM bookings b JOIN slots s ON s.id=b.slot_id WHERE s.starts_at >= $3 AND s.starts_at < $4 GROUP BY 1) se ON se.d=days.day
LEFT JOIN (SELECT (starts_at AT TIME ZONE 'Asia/Bangkok')::date d, SUM(capacity) capacity FROM slots
  WHERE status='open' AND starts_at >= $3 AND starts_at < $4 GROUP BY 1) cap ON cap.d=days.day
LEFT JOIN (SELECT (created_at AT TIME ZONE 'Asia/Bangkok')::date d,
    count(DISTINCT visitor_id) FILTER (WHERE type='page_view') visitors,
    count(DISTINCT visitor_id) FILTER (WHERE type='booking_start') starts,
    count(DISTINCT visitor_id) FILTER (WHERE type='checkout_start') checkouts
  FROM web_events WHERE created_at >= $3 AND created_at < $4 GROUP BY 1) we ON we.d=days.day
ORDER BY days.day`

func (a *App) dailyRows(ctx context.Context, from, to time.Time) ([]dayRow, error) {
	rows, err := a.DB.QueryContext(ctx, dailySQL, from.Format("2006-01-02"), to.AddDate(0, 0, -1).Format("2006-01-02"), from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []dayRow
	for rows.Next() {
		var d dayRow
		if err := rows.Scan(&d.Day, &d.Gross, &d.Refunds, &d.Orders, &d.Created, &d.Guests, &d.CheckedIn, &d.Cancelled, &d.NoShow, &d.Capacity, &d.Visitors, &d.Starts, &d.Checks); err != nil {
			return nil, err
		}
		d.Day = time.Date(d.Day.Year(), d.Day.Month(), d.Day.Day(), 0, 0, 0, 0, BKK)
		out = append(out, d)
	}
	return out, rows.Err()
}

func sumRows(rs []dayRow) dayRow {
	var t dayRow
	for _, r := range rs {
		t.Gross += r.Gross
		t.Refunds += r.Refunds
		t.Orders += r.Orders
		t.Created += r.Created
		t.Guests += r.Guests
		t.CheckedIn += r.CheckedIn
		t.Cancelled += r.Cancelled
		t.NoShow += r.NoShow
		t.Capacity += r.Capacity
	}
	return t
}

// dailyRevenue is used by the dashboard sparkline: net revenue per day.
func (a *App) dailyRevenue(ctx context.Context, from, to time.Time) []point {
	rs, err := a.dailyRows(ctx, from, to)
	if err != nil {
		a.Log.Error("daily revenue", "err", err)
		return nil
	}
	var pts []point
	for _, r := range rs {
		pts = append(pts, point{Key: r.Day.Format("2006-01-02"), Label: fmt.Sprintf("%d %s", r.Day.Day(), TH.Month(r.Day)), Long: TH.DateShort(r.Day), Value: r.Net()})
	}
	return pts
}

// hourly net revenue for single-day ranges
func (a *App) hourlyRevenue(ctx context.Context, from, to time.Time) []int64 {
	out := make([]int64, 24)
	rows, err := a.DB.QueryContext(ctx, `SELECT EXTRACT(HOUR FROM paid_at AT TIME ZONE 'Asia/Bangkok')::int, SUM(amount) FROM payments
		WHERE status IN ('paid','refunded','partially_refunded') AND paid_at >= $1 AND paid_at < $2 GROUP BY 1
		UNION ALL SELECT EXTRACT(HOUR FROM created_at AT TIME ZONE 'Asia/Bangkok')::int, -SUM(amount) FROM refunds
		WHERE status='succeeded' AND created_at >= $1 AND created_at < $2 GROUP BY 1`, from, to)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var h int
		var v int64
		if rows.Scan(&h, &v) == nil && h >= 0 && h < 24 {
			out[h] += v
		}
	}
	return out
}

// Metric is a KPI tile on the analytics page. Kind separates measured values from estimates and gaps.
type Metric struct {
	Label, Value, Prev, Note string
	Delta                    int
	HasDelta, Up, Invert     bool
	Kind                     string // measured | estimate | unavailable
}

func (m Metric) Good() bool { return m.Up != m.Invert }

func metric(label string, cur, prev int64, f func(int64) string, note string) Metric {
	m := Metric{Label: label, Value: f(cur), Prev: f(prev), Note: note, Kind: "measured", Up: cur >= prev}
	if prev != 0 {
		m.HasDelta = true
		m.Delta = int(math.Round(float64(cur-prev) * 100 / math.Abs(float64(prev))))
	}
	return m
}

func ratio(a, b int64) int64 {
	if b == 0 {
		return 0
	}
	return a * 100 / b
}

func pctStr(v int64) string { return fmt.Sprintf("%d%%", v) }
func daysStr(v int64) string {
	return fmt.Sprintf("%.1f วัน", float64(v)/10)
}

var methodLabels = map[string]string{
	"promptpay": "PromptPay (ออนไลน์)", "card": "บัตรเครดิต/เดบิต (ออนไลน์)", "online": "ออนไลน์ (ไม่ระบุวิธี)",
	"venue:cash": "เงินสด (หน้าร้าน)", "venue:transfer": "โอนเงิน (หน้าร้าน)", "venue:card_terminal": "รูดบัตร (หน้าร้าน)", "venue:": "หน้าร้าน",
}

var channelLabels = map[string]string{"web": "เว็บไซต์", "admin": "พนักงานบันทึก", "walk_in": "Walk-in", "phone": "โทรศัพท์", "line": "LINE"}

// hbarQuery runs a two/three-column query (label, value[, count]) into bars.
func (a *App) hbarQuery(ctx context.Context, labels map[string]string, noteFmt string, q string, args ...any) []hbar {
	rows, err := a.DB.QueryContext(ctx, q, args...)
	if err != nil {
		a.Log.Error("analytics query", "err", err)
		return nil
	}
	defer rows.Close()
	var out []hbar
	cols, _ := rows.Columns()
	for rows.Next() {
		var k string
		var v, n int64
		if len(cols) >= 3 {
			rows.Scan(&k, &v, &n)
		} else {
			rows.Scan(&k, &v)
		}
		lbl := k
		if labels != nil {
			if l, ok := labels[k]; ok {
				lbl = l
			}
		}
		h := hbar{Label: lbl, Value: v}
		if noteFmt != "" && len(cols) >= 3 {
			h.Note = fmt.Sprintf(noteFmt, n)
		}
		out = append(out, h)
	}
	return out
}

type sourceRow struct {
	Source, Medium                           string
	Visitors, Bookings, Guests, Value, Spend int64
	ROAS                                     string
	CPA                                      string
}

type cohortRow struct {
	Month         string
	Customers     int64
	R30, R60, R90 string
}

func (a *App) handleAnalytics(w http.ResponseWriter, r *http.Request) {
	s := staffOf(r)
	canRev, canMkt := s.Can("analytics.revenue"), s.Can("analytics.marketing")
	if !canRev && !canMkt {
		a.adminRender(w, r, "admin/forbidden", 403, "ไม่มีสิทธิ์", map[string]any{"Msg": "บัญชีนี้ไม่มีสิทธิ์ดูรายงาน"})
		return
	}
	ctx := r.Context()
	rg := parseRange(r.URL.Query())
	cur, err := a.dailyRows(ctx, rg.From, rg.To)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	prev, _ := a.dailyRows(ctx, rg.PFrom, rg.PTo)
	tc, tp := sumRows(cur), sumRows(prev)

	if r.URL.Query().Get("format") == "csv" {
		Audit(ctx, a.DB, s.ID, "", "export.analytics", "", "", map[string]any{"from": rg.FromStr, "to": rg.ToStr}, clientIP(r))
		out := [][]string{{"date", "bookings_created", "guests", "capacity_open", "occupancy_pct", "checked_in_guests", "cancelled_bookings", "no_show_bookings", "web_visitors", "visitors_started_booking", "visitors_started_checkout"}}
		if canRev {
			out[0] = append(out[0], "gross_paid_thb", "refunds_thb", "net_revenue_thb", "paid_orders")
		}
		for _, d := range cur {
			row := []string{d.Day.Format("2006-01-02"), num(d.Created), num(d.Guests), num(d.Capacity), num(ratio(d.Guests, d.Capacity)), num(d.CheckedIn), num(d.Cancelled), num(d.NoShow), num(d.Visitors), num(d.Starts), num(d.Checks)}
			if canRev {
				row = append(row, fmt.Sprintf("%.2f", float64(d.Gross)/100), fmt.Sprintf("%.2f", float64(d.Refunds)/100), fmt.Sprintf("%.2f", float64(d.Net())/100), num(d.Orders))
			}
			out = append(out, row)
		}
		writeCSV(w, "analytics-"+rg.FromStr+"-to-"+rg.ToStr, out)
		return
	}

	data := map[string]any{"R": rg, "Presets": rangePresets, "CanRev": canRev, "CanMkt": canMkt, "Simulated": a.Simulated(), "Generated": Now()}
	dayPts := func(val func(dayRow) int64) []point {
		var pts []point
		for i, d := range cur {
			p := point{Key: d.Day.Format("2006-01-02"), Label: fmt.Sprintf("%d %s", d.Day.Day(), TH.Month(d.Day)), Long: TH.DateShort(d.Day), Value: val(d)}
			if i < len(prev) {
				p.Prev = val(prev[i])
			}
			pts = append(pts, p)
		}
		return pts
	}
	compare := rg.Days > 1
	todayKey := DateOnly(Now()).Format("2006-01-02")

	// --- bookings & occupancy (everyone who can see analytics) ---
	var leadTenths, avgPartyTenths int64
	a.DB.QueryRowContext(ctx, `SELECT COALESCE(ROUND(AVG(EXTRACT(EPOCH FROM (s.starts_at - b.created_at))/86400)*10),0)::bigint,
		COALESCE(ROUND(AVG(b.participants)*10),0)::bigint
		FROM bookings b JOIN slots s ON s.id=b.slot_id WHERE b.created_at >= $1 AND b.created_at < $2 AND b.status IN ('confirmed','checked_in','no_show')`, rg.From, rg.To).Scan(&leadTenths, &avgPartyTenths)
	var pastGuests, pastChecked int64 // check-in rate only over sessions that already started
	a.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(b.participants),0), COALESCE(SUM(b.participants) FILTER (WHERE b.status='checked_in'),0)
		FROM bookings b JOIN slots s ON s.id=b.slot_id WHERE s.starts_at >= $1 AND s.starts_at < LEAST($2, now()) AND b.status IN ('confirmed','checked_in','no_show')`, rg.From, rg.To).Scan(&pastGuests, &pastChecked)
	var ppastGuests, ppastChecked int64
	a.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(b.participants),0), COALESCE(SUM(b.participants) FILTER (WHERE b.status='checked_in'),0)
		FROM bookings b JOIN slots s ON s.id=b.slot_id WHERE s.starts_at >= $1 AND s.starts_at < $2 AND b.status IN ('confirmed','checked_in','no_show')`, rg.PFrom, rg.PTo).Scan(&ppastGuests, &ppastChecked)
	bm := []Metric{
		metric("การจองใหม่", tc.Created, tp.Created, num, "นับตามวันที่กดจอง (ไม่รวมที่ไม่ได้ชำระ)"),
		metric("จำนวนลูกค้า (คน)", tc.Guests, tp.Guests, num, "นับตามวันที่มาใช้บริการ"),
		metric("อัตราการใช้ที่นั่ง", ratio(tc.Guests, tc.Capacity), ratio(tp.Guests, tp.Capacity), pctStr, fmt.Sprintf("%d จาก %d ที่ที่เปิดขาย", tc.Guests, tc.Capacity)),
		metric("มาจริง (เช็คอิน)", ratio(pastChecked, pastGuests), ratio(ppastChecked, ppastGuests), pctStr, "ของลูกค้าในรอบที่เริ่มไปแล้ว"),
	}
	mc := metric("ยกเลิก", tc.Cancelled, tp.Cancelled, num, "การจอง")
	mc.Invert = true
	mn := metric("ไม่มาตามนัด", tc.NoShow, tp.NoShow, num, "การจอง")
	mn.Invert = true
	bm = append(bm, mc, mn,
		Metric{Label: "จองล่วงหน้าเฉลี่ย", Value: daysStr(leadTenths), Note: "จากวันที่จองถึงวันมาใช้บริการ", Kind: "measured"},
		Metric{Label: "ขนาดกลุ่มเฉลี่ย", Value: fmt.Sprintf("%.1f คน", float64(avgPartyTenths)/10), Note: "ต่อการจอง", Kind: "measured"},
	)
	data["BookingMetrics"] = bm
	data["GuestsChart"] = colChart(dayPts(func(d dayRow) int64 { return d.Guests }), colChartOpts{Name: "จำนวนลูกค้าต่อวัน", Compare: compare, CurrentName: "ช่วงนี้", CompareName: "ช่วงก่อนหน้า", HighlightKey: todayKey,
		Fmt: func(v int64) string { return num(v) + " คน" }})

	// occupancy heatmap: weekday × start time over the range
	cells := map[string]heatCell{}
	timesSet := map[string]bool{}
	hrows, err := a.DB.QueryContext(ctx, `SELECT EXTRACT(DOW FROM s.starts_at AT TIME ZONE 'Asia/Bangkok')::int, to_char(s.starts_at AT TIME ZONE 'Asia/Bangkok','HH24:MI'),
		count(*), SUM(s.capacity), COALESCE(SUM((SELECT SUM(b.participants) FROM bookings b WHERE b.slot_id=s.id AND b.status IN ('confirmed','checked_in','no_show'))),0)
		FROM slots s WHERE s.status='open' AND s.starts_at >= $1 AND s.starts_at < $2 GROUP BY 1,2`, rg.From, rg.To)
	if err == nil {
		for hrows.Next() {
			var d int
			var t string
			var c heatCell
			hrows.Scan(&d, &t, &c.Sessions, &c.Capacity, &c.Guests)
			cells[fmt.Sprintf("%d|%s", d, t)] = c
			timesSet[t] = true
		}
		hrows.Close()
	}
	var times []string
	for t := range timesSet {
		times = append(times, t)
	}
	sort.Strings(times)
	data["Heatmap"] = heatmap(times, []int{1, 2, 3, 4, 5, 6, 0}, cells)
	data["Channels"] = hbars(a.hbarQuery(ctx, channelLabels, "%d คน", `SELECT channel, count(*), SUM(participants) FROM bookings
		WHERE created_at >= $1 AND created_at < $2 AND status IN ('confirmed','checked_in','no_show','cancelled') GROUP BY 1 ORDER BY 2 DESC`, rg.From, rg.To),
		func(v int64) string { return num(v) + " การจอง" }, "")
	data["ProductMix"] = hbars(a.hbarQuery(ctx, nil, "%d การจอง", `SELECT p.name_th, SUM(b.participants), count(*) FROM bookings b JOIN products p ON p.id=b.product_id
		WHERE b.created_at >= $1 AND b.created_at < $2 AND b.status IN ('confirmed','checked_in','no_show') GROUP BY 1 ORDER BY 2 DESC LIMIT 10`, rg.From, rg.To),
		func(v int64) string { return num(v) + " คน" }, "")

	// --- revenue ---
	if canRev {
		aov := func(t dayRow) int64 {
			if t.Orders == 0 {
				return 0
			}
			return t.Gross / t.Orders
		}
		var valuePerGuest, pValuePerGuest int64
		vpg := func(from, to time.Time) int64 {
			var v int64
			a.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(b.total)/NULLIF(SUM(b.participants),0),0)::bigint FROM bookings b JOIN slots s ON s.id=b.slot_id
				WHERE s.starts_at >= $1 AND s.starts_at < $2 AND b.status IN ('confirmed','checked_in','no_show')`, from, to).Scan(&v)
			return v
		}
		valuePerGuest, pValuePerGuest = vpg(rg.From, rg.To), vpg(rg.PFrom, rg.PTo)
		mr := metric("ยอดคืนเงิน", tc.Refunds, tp.Refunds, Baht, "")
		mr.Invert = true
		var due int64
		a.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(b.total),0) FROM bookings b JOIN slots s ON s.id=b.slot_id
			WHERE s.starts_at >= $1 AND s.starts_at < $2 AND b.payment_state='due_at_venue' AND b.status IN ('confirmed','checked_in')`, rg.From, rg.To).Scan(&due)
		rm := []Metric{
			metric("รายได้สุทธิ", tc.Net(), tp.Net(), Baht, "เงินที่รับจริง หักเงินคืนแล้ว"),
			metric("ยอดรับเงิน", tc.Gross, tp.Gross, Baht, fmt.Sprintf("%d รายการ", tc.Orders)),
			mr,
			metric("ยอดเฉลี่ยต่อบิล", aov(tc), aov(tp), Baht, ""),
			metric("มูลค่าการจองเฉลี่ยต่อคน", valuePerGuest, pValuePerGuest, Baht, "หลังส่วนลด ตามวันที่มาใช้บริการ"),
			{Label: "รอเก็บเงินหน้าร้าน", Value: Baht(due), Note: "การจองในช่วงนี้ที่ยังไม่ชำระ", Kind: "measured"},
		}
		// unredeemed pass credits: count is measured; baht value is an estimate (price ÷ credits)
		var credits, liability int64
		a.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(ps.credits_total-ps.credits_used),0),
			COALESCE(SUM((ps.credits_total-ps.credits_used) * COALESCE((SELECT o.amount FROM pass_orders o WHERE o.pass_id=ps.id AND o.status IN ('paid') LIMIT 1),0) / NULLIF(ps.credits_total,0)),0)::bigint
			FROM passes ps WHERE ps.status='active' AND (ps.expires_at IS NULL OR ps.expires_at > now())`).Scan(&credits, &liability)
		rm = append(rm, Metric{Label: "สิทธิ์แพ็กเกจที่ยังไม่ใช้", Value: fmt.Sprintf("%d ครั้ง", credits), Note: "ทุกบัตรที่ยังใช้ได้ (ไม่ขึ้นกับช่วงวันที่)", Kind: "measured"},
			Metric{Label: "มูลค่าสิทธิ์ค้างใช้ (ประมาณ)", Value: Baht(liability), Note: "ราคาที่ขาย ÷ จำนวนครั้ง × ครั้งที่เหลือ (บัตรฟรีไม่นับ)", Kind: "estimate"})
		data["RevenueMetrics"] = rm
		if rg.Days == 1 {
			hr := a.hourlyRevenue(ctx, rg.From, rg.To)
			var pts []point
			for h := 0; h < 24; h++ {
				pts = append(pts, point{Key: fmt.Sprint(h), Label: fmt.Sprintf("%02d", h), Long: fmt.Sprintf("%02d:00–%02d:59", h, h), Value: hr[h]})
			}
			data["RevenueChart"] = colChart(pts, colChartOpts{Name: "รายได้สุทธิรายชั่วโมง", Fmt: Baht, Tick: compactBaht, CurrentName: "รายได้สุทธิ"})
			data["RevenueChartTitle"] = "รายได้สุทธิรายชั่วโมง"
		} else {
			data["RevenueChart"] = colChart(dayPts(func(d dayRow) int64 { return d.Net() }), colChartOpts{Name: "รายได้สุทธิรายวัน", Fmt: Baht, Tick: compactBaht, Compare: compare,
				CurrentName: "ช่วงนี้", CompareName: "ช่วงก่อนหน้า", HighlightKey: todayKey})
			data["RevenueChartTitle"] = "รายได้สุทธิรายวัน"
		}
		data["RevenueByProduct"] = hbars(a.hbarQuery(ctx, nil, "%d รายการ", `SELECT COALESCE(p1.name_th, p2.name_th, 'อื่น ๆ'), SUM(pay.amount), count(*)
			FROM payments pay LEFT JOIN bookings b ON b.id=pay.booking_id LEFT JOIN products p1 ON p1.id=b.product_id
			LEFT JOIN pass_orders o ON o.id=pay.pass_order_id LEFT JOIN passes ps ON ps.id=o.pass_id LEFT JOIN products p2 ON p2.id=ps.product_id
			WHERE pay.status IN ('paid','refunded','partially_refunded') AND pay.paid_at >= $1 AND pay.paid_at < $2 GROUP BY 1 ORDER BY 2 DESC LIMIT 10`, rg.From, rg.To), Baht, "")
		data["RevenueByMethod"] = hbars(a.hbarQuery(ctx, methodLabels, "%d รายการ", `SELECT CASE WHEN provider='venue' THEN 'venue:'||method ELSE COALESCE(NULLIF(method,''),'online') END, SUM(amount), count(*)
			FROM payments WHERE status IN ('paid','refunded','partially_refunded') AND paid_at >= $1 AND paid_at < $2 GROUP BY 1 ORDER BY 2 DESC`, rg.From, rg.To), Baht, "")
	}

	// --- customers ---
	var newC, retC, pNewC, pRetC int64
	split := func(from, to time.Time) (n, rr int64) {
		a.DB.QueryRowContext(ctx, `WITH cx AS (SELECT DISTINCT b.customer_id FROM bookings b JOIN slots s ON s.id=b.slot_id
				WHERE s.starts_at >= $1 AND s.starts_at < $2 AND b.status IN ('confirmed','checked_in','no_show'))
			SELECT count(*) FILTER (WHERE NOT EXISTS (SELECT 1 FROM bookings p JOIN slots ps ON ps.id=p.slot_id WHERE p.customer_id=cx.customer_id AND ps.starts_at < $1 AND p.status IN ('checked_in','confirmed','no_show'))),
			       count(*) FILTER (WHERE EXISTS (SELECT 1 FROM bookings p JOIN slots ps ON ps.id=p.slot_id WHERE p.customer_id=cx.customer_id AND ps.starts_at < $1 AND p.status IN ('checked_in','confirmed','no_show')))
			FROM cx`, from, to).Scan(&n, &rr)
		return
	}
	newC, retC = split(rg.From, rg.To)
	pNewC, pRetC = split(rg.PFrom, rg.PTo)
	var everC, repeatC, lapsed, consented int64
	a.DB.QueryRowContext(ctx, `WITH v AS (SELECT b.customer_id, count(*) n, max(s.starts_at) last FROM bookings b JOIN slots s ON s.id=b.slot_id
			WHERE b.status IN ('checked_in','confirmed','no_show') AND s.starts_at < now() GROUP BY 1)
		SELECT count(*), count(*) FILTER (WHERE n >= 2), count(*) FILTER (WHERE last < now() - interval '60 days') FROM v`).Scan(&everC, &repeatC, &lapsed)
	a.DB.QueryRowContext(ctx, `SELECT count(*) FROM customers WHERE marketing_consent`).Scan(&consented)
	cm := []Metric{
		metric("ลูกค้าใหม่", newC, pNewC, num, "มาครั้งแรกในช่วงนี้"),
		metric("ลูกค้าเดิมกลับมา", retC, pRetC, num, "เคยมาก่อนช่วงนี้"),
		{Label: "อัตรากลับมาซ้ำ (ทั้งหมด)", Value: pctStr(ratio(repeatC, everC)), Note: fmt.Sprintf("%d จาก %d คนมามากกว่า 1 ครั้ง", repeatC, everC), Kind: "measured"},
		{Label: "ไม่ได้มา 60 วันขึ้นไป", Value: num(lapsed), Note: "กลุ่ม ‘หายไป’ ในหน้าลูกค้า", Kind: "measured"},
		{Label: "ยินยอมรับข่าวสาร", Value: num(consented), Note: "ติดต่อทางการตลาดได้", Kind: "measured"},
	}
	if canRev {
		var clv int64
		a.DB.QueryRowContext(ctx, `SELECT COALESCE(AVG(t),0)::bigint FROM (
			SELECT c.id, COALESCE((SELECT SUM(p.amount-p.refunded_amount) FROM payments p JOIN bookings b ON b.id=p.booking_id WHERE b.customer_id=c.id AND p.status IN ('paid','refunded','partially_refunded')),0)
			 + COALESCE((SELECT SUM(p.amount-p.refunded_amount) FROM payments p JOIN pass_orders o ON o.id=p.pass_order_id WHERE o.customer_id=c.id AND p.status IN ('paid','refunded','partially_refunded')),0) t
			FROM customers c WHERE EXISTS (SELECT 1 FROM bookings b WHERE b.customer_id=c.id AND b.status IN ('checked_in','confirmed','no_show'))) x`).Scan(&clv)
		cm = append(cm, Metric{Label: "รายได้เฉลี่ยต่อลูกค้า (ประมาณ CLV)", Value: Baht(clv), Note: "เฉลี่ยตั้งแต่เริ่มใช้ระบบ ยังไม่ใช่มูลค่าตลอดชีพจริง", Kind: "estimate"})
	}
	data["CustomerMetrics"] = cm
	// cohorts: first-visit month → share who came back within 30/60/90 days (only shown once that window has fully passed)
	var cohorts []cohortRow
	crows, err := a.DB.QueryContext(ctx, `WITH v AS (SELECT b.customer_id, s.starts_at FROM bookings b JOIN slots s ON s.id=b.slot_id WHERE b.status IN ('checked_in','confirmed') AND s.starts_at < now()),
		f AS (SELECT customer_id, min(starts_at) first FROM v GROUP BY 1)
		SELECT to_char(date_trunc('month', f.first AT TIME ZONE 'Asia/Bangkok'),'YYYY-MM') m, count(*),
		  count(*) FILTER (WHERE EXISTS (SELECT 1 FROM v WHERE v.customer_id=f.customer_id AND v.starts_at > f.first + interval '1 hour' AND v.starts_at <= f.first + interval '30 days')),
		  count(*) FILTER (WHERE EXISTS (SELECT 1 FROM v WHERE v.customer_id=f.customer_id AND v.starts_at > f.first + interval '1 hour' AND v.starts_at <= f.first + interval '60 days')),
		  count(*) FILTER (WHERE EXISTS (SELECT 1 FROM v WHERE v.customer_id=f.customer_id AND v.starts_at > f.first + interval '1 hour' AND v.starts_at <= f.first + interval '90 days')),
		  max(f.first)
		FROM f WHERE f.first >= now() - interval '7 months' GROUP BY 1 ORDER BY 1 DESC`)
	if err == nil {
		for crows.Next() {
			var c cohortRow
			var r30, r60, r90 int64
			var last time.Time
			crows.Scan(&c.Month, &c.Customers, &r30, &r60, &r90, &last)
			show := func(n int64, days int) string {
				if time.Since(last) < time.Duration(days)*24*time.Hour {
					return "รอข้อมูล"
				}
				return pctStr(ratio(n, c.Customers))
			}
			if t, err := time.Parse("2006-01", c.Month); err == nil {
				c.Month = thMonths[t.Month()-1] + " " + fmt.Sprint(t.Year()+543)
			}
			c.R30, c.R60, c.R90 = show(r30, 30), show(r60, 60), show(r90, 90)
			cohorts = append(cohorts, c)
		}
		crows.Close()
	}
	data["Cohorts"] = cohorts

	// --- marketing ---
	if canMkt {
		var fv, fs, fc, fp int64
		a.DB.QueryRowContext(ctx, `SELECT count(DISTINCT visitor_id) FILTER (WHERE type='page_view'), count(DISTINCT visitor_id) FILTER (WHERE type='booking_start'),
			count(DISTINCT visitor_id) FILTER (WHERE type='checkout_start') FROM web_events WHERE created_at >= $1 AND created_at < $2`, rg.From, rg.To).Scan(&fv, &fs, &fc)
		a.DB.QueryRowContext(ctx, `SELECT count(*) FROM bookings WHERE channel='web' AND created_at >= $1 AND created_at < $2 AND status IN ('confirmed','checked_in','no_show','cancelled')`, rg.From, rg.To).Scan(&fp)
		type step struct {
			Label string
			N     int64
			Pct   int64 // of first step
			Conv  int64 // from previous step
		}
		steps := []step{{"เข้าชมเว็บไซต์ (ผู้เข้าชมไม่ซ้ำ)", fv, 100, 100}, {"เริ่มจอง (เปิดหน้าจอง)", fs, 0, 0}, {"ไปหน้าชำระเงิน/ยืนยัน", fc, 0, 0}, {"จองสำเร็จทางเว็บ", fp, 0, 0}}
		for i := 1; i < len(steps); i++ {
			steps[i].Pct = ratio(steps[i].N, fv)
			steps[i].Conv = ratio(steps[i].N, steps[i-1].N)
		}
		data["Funnel"] = steps
		var conv, pconv int64
		conv = ratioBP(fp, fv)
		var pfv, pfp int64
		a.DB.QueryRowContext(ctx, `SELECT count(DISTINCT visitor_id) FILTER (WHERE type='page_view') FROM web_events WHERE created_at >= $1 AND created_at < $2`, rg.PFrom, rg.PTo).Scan(&pfv)
		a.DB.QueryRowContext(ctx, `SELECT count(*) FROM bookings WHERE channel='web' AND created_at >= $1 AND created_at < $2 AND status IN ('confirmed','checked_in','no_show','cancelled')`, rg.PFrom, rg.PTo).Scan(&pfp)
		pconv = ratioBP(pfp, pfv)
		// abandoned checkout = hold expired without payment
		var abN, abValue, abCust, abConsent int64
		a.DB.QueryRowContext(ctx, `SELECT count(*), COALESCE(SUM(b.total),0), count(DISTINCT b.customer_id), count(DISTINCT b.customer_id) FILTER (WHERE c.marketing_consent)
			FROM bookings b JOIN customers c ON c.id=b.customer_id
			WHERE b.status='expired' AND b.created_at >= $1 AND b.created_at < $2
			AND NOT EXISTS (SELECT 1 FROM bookings x WHERE x.customer_id=b.customer_id AND x.created_at > b.created_at AND x.status IN ('confirmed','checked_in','no_show'))`, rg.From, rg.To).Scan(&abN, &abValue, &abCust, &abConsent)
		mm := []Metric{
			metric("ผู้เข้าชมเว็บไซต์", fv, pfv, num, "นับจากคุกกี้ของเว็บเอง (ไม่ระบุตัวตน)"),
			metric("อัตราการจองสำเร็จ", conv, pconv, func(v int64) string { return fmt.Sprintf("%.2f%%", float64(v)/100) }, "จองสำเร็จทางเว็บ ÷ ผู้เข้าชม"),
			{Label: "ทิ้งตะกร้า (ไม่ชำระจนหมดเวลา)", Value: num(abN), Note: fmt.Sprintf("มูลค่า %s · %d คน · ยินยอมให้ติดต่อ %d คน", Baht(abValue), abCust, abConsent), Kind: "measured"},
		}
		data["MarketingMetrics"] = mm
		// sources: visitors from web_events (first-party), bookings/value from bookings, spend entered manually
		srcRows, err := a.DB.QueryContext(ctx, `WITH b AS (SELECT lower(COALESCE(NULLIF(utm_source,''),'direct')) src, lower(utm_medium) med, count(*) n, SUM(participants) g, SUM(total) v FROM bookings
				WHERE created_at >= $1 AND created_at < $2 AND status IN ('confirmed','checked_in','no_show') GROUP BY 1,2),
			w AS (SELECT lower(COALESCE(NULLIF(utm_source,''),'direct')) src, lower(utm_medium) med, count(DISTINCT visitor_id) n FROM web_events WHERE type='page_view' AND created_at >= $1 AND created_at < $2 GROUP BY 1,2)
			SELECT COALESCE(b.src,w.src), COALESCE(b.med,w.med,''), COALESCE(w.n,0), COALESCE(b.n,0), COALESCE(b.g,0), COALESCE(b.v,0)
			FROM b FULL JOIN w ON w.src=b.src AND w.med=b.med ORDER BY 6 DESC, 3 DESC LIMIT 25`, rg.From, rg.To)
		spend := map[string]int64{}
		sp, _ := a.DB.QueryContext(ctx, `SELECT lower(source), SUM(amount) FROM ad_spend WHERE spend_date >= $1::date AND spend_date <= $2::date GROUP BY 1`, rg.FromStr, rg.ToStr)
		for sp != nil && sp.Next() {
			var k string
			var v int64
			sp.Scan(&k, &v)
			spend[k] = v
		}
		if sp != nil {
			sp.Close()
		}
		var sources []sourceRow
		seenSpend := map[string]bool{}
		if err == nil {
			for srcRows.Next() {
				var x sourceRow
				srcRows.Scan(&x.Source, &x.Medium, &x.Visitors, &x.Bookings, &x.Guests, &x.Value)
				if !seenSpend[x.Source] { // attribute a source's spend once (to its highest-value medium row)
					x.Spend = spend[x.Source]
					seenSpend[x.Source] = true
				}
				x.ROAS, x.CPA = roas(x.Value, x.Spend), cpa(x.Spend, x.Bookings)
				sources = append(sources, x)
			}
			srcRows.Close()
		}
		for k, v := range spend { // spend recorded for a source with no traffic or bookings
			if !seenSpend[k] {
				sources = append(sources, sourceRow{Source: k, Spend: v, ROAS: roas(0, v), CPA: "—"})
			}
		}
		data["Sources"] = sources
		type campRow struct {
			Campaign               string
			Bookings, Value, Spend int64
			ROAS, CPA              string
		}
		var camps []campRow
		cr, err := a.DB.QueryContext(ctx, `WITH b AS (SELECT utm_campaign c, count(*) n, SUM(total) v FROM bookings WHERE utm_campaign<>'' AND created_at >= $1 AND created_at < $2 AND status IN ('confirmed','checked_in','no_show') GROUP BY 1),
			s AS (SELECT campaign c, SUM(amount) amt FROM ad_spend WHERE campaign<>'' AND spend_date >= $3::date AND spend_date <= $4::date GROUP BY 1)
			SELECT COALESCE(b.c,s.c), COALESCE(b.n,0), COALESCE(b.v,0), COALESCE(s.amt,0) FROM b FULL JOIN s ON s.c=b.c ORDER BY 3 DESC, 4 DESC LIMIT 25`, rg.From, rg.To, rg.FromStr, rg.ToStr)
		if err == nil {
			for cr.Next() {
				var x campRow
				cr.Scan(&x.Campaign, &x.Bookings, &x.Value, &x.Spend)
				x.ROAS, x.CPA = roas(x.Value, x.Spend), cpa(x.Spend, x.Bookings)
				camps = append(camps, x)
			}
			cr.Close()
		}
		data["Campaigns"] = camps
		data["Promos"] = hbars(a.hbarQuery(ctx, nil, "ส่วนลดรวม ฿%d", `SELECT p.code, count(*), COALESCE(SUM(pr.amount),0)/100 FROM promo_redemptions pr JOIN promo_codes p ON p.id=pr.promo_id
			JOIN bookings b ON b.id=pr.booking_id WHERE pr.reversed_at IS NULL AND b.created_at >= $1 AND b.created_at < $2 GROUP BY 1 ORDER BY 2 DESC LIMIT 15`, rg.From, rg.To),
			func(v int64) string { return num(v) + " ครั้ง" }, "ยังไม่มีการใช้โค้ดในช่วงนี้")
		data["Landing"] = hbars(a.hbarQuery(ctx, nil, "", `SELECT path, count(DISTINCT visitor_id) FROM web_events WHERE type='page_view' AND path LIKE '/lp/%' AND created_at >= $1 AND created_at < $2 GROUP BY 1 ORDER BY 2 DESC LIMIT 10`, rg.From, rg.To),
			func(v int64) string { return num(v) + " คน" }, "ยังไม่มีผู้เข้าชมหน้าแคมเปญ (/lp/...) ในช่วงนี้")
		set, _ := LoadSettings(ctx, a.DB)
		data["Tracking"] = []struct {
			Name   string
			On     bool
			Detail string
		}{
			{"Google Tag Manager", set.Str("gtm_id") != "", "ถ้าตั้ง GTM ไว้ ระบบจะส่งเหตุการณ์เข้า dataLayer อย่างเดียว (ตั้งค่า GA4/Ads/Pixel ใน GTM) เพื่อไม่ให้นับซ้ำ"},
			{"Google Analytics 4", set.Str("ga4_id") != "", "ส่ง purchase พร้อม transaction_id = เลขการจอง"},
			{"Google Ads conversion", set.Str("google_ads_id") != "" && set.Str("google_ads_label") != "", "ต้องมีทั้ง Conversion ID และ Label"},
			{"Meta Pixel", set.Str("meta_pixel_id") != "", "Purchase ใช้ eventID = เลขการจอง"},
			{"Meta Conversions API", a.Cfg.MetaAccessToken != "" && set.Str("meta_pixel_id") != "", "ส่งจากเซิร์ฟเวอร์ด้วย event_id เดียวกับ Pixel (Meta ตัดรายการซ้ำให้)"},
		}
	}
	a.adminRender(w, r, "admin/analytics", 200, "รายงานธุรกิจ", data)
}

// ratioBP returns a/b in basis points (1/100 of a percent).
func ratioBP(a, b int64) int64 {
	if b == 0 {
		return 0
	}
	return a * 10000 / b
}

func roas(value, spend int64) string {
	if spend == 0 {
		return "—"
	}
	return fmt.Sprintf("%.1fx", float64(value)/float64(spend))
}

func cpa(spend, n int64) string {
	if spend == 0 || n == 0 {
		return "—"
	}
	return Baht(spend / n)
}

// trendTag renders a small delta label for metric tiles (icon + text, never colour alone).
func trendTag(m Metric) template.HTML {
	if !m.HasDelta {
		if m.Kind == "measured" && m.Prev != "" && m.Prev != m.Value {
			return template.HTML(`<span class="delta">ช่วงก่อน ` + template.HTMLEscapeString(m.Prev) + `</span>`)
		}
		return ""
	}
	cls, arrow := "delta bad", "▼"
	if m.Up {
		arrow = "▲"
	}
	if m.Good() {
		cls = "delta good"
	}
	if m.Delta == 0 {
		cls, arrow = "delta", "■"
	}
	sign := ""
	if m.Delta > 0 {
		sign = "+"
	}
	return template.HTML(fmt.Sprintf(`<span class="%s"><b aria-hidden="true">%s</b> %s%d%% <small>จาก %s</small></span>`, cls, arrow, sign, m.Delta, template.HTMLEscapeString(m.Prev)))
}
