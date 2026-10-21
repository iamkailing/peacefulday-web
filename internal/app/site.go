package app

import (
	"context"
	"fmt"
	"strings"
)

// SiteFacts are derived from live configuration, so public copy never states hours or group
// sizes the shop has not actually set up.
type SiteFacts struct {
	OpenDays   []int // weekdays with at least one session template
	ClosedDays []int
	MaxGroup   int // largest per-session capacity in the weekly schedule
}

func (a *App) siteFacts(ctx context.Context) SiteFacts {
	var f SiteFacts
	open := map[int]bool{}
	rows, err := a.DB.QueryContext(ctx, `SELECT t.weekday, MAX(t.capacity) FROM schedule_templates t JOIN experiences e ON e.id=t.experience_id AND e.status='active' GROUP BY 1`)
	if err == nil {
		for rows.Next() {
			var d, c int
			if rows.Scan(&d, &c) == nil {
				open[d] = true
				if c > f.MaxGroup {
					f.MaxGroup = c
				}
			}
		}
		rows.Close()
	}
	for _, d := range []int{1, 2, 3, 4, 5, 6, 0} {
		if open[d] {
			f.OpenDays = append(f.OpenDays, d)
		} else {
			f.ClosedDays = append(f.ClosedDays, d)
		}
	}
	return f
}

// HoursLine renders "Open 09:00–21:00, closed on Thursdays" from settings + schedule.
func (p *Page) HoursLine() string {
	if p.Facts == nil || p.Set == nil {
		return ""
	}
	o, c := p.Set.Str("open_time"), p.Set.Str("close_time")
	if len(p.Facts.OpenDays) == 0 {
		return ""
	}
	if len(p.Facts.ClosedDays) == 0 {
		return p.L.T("hours.daily", o, c)
	}
	var names []string
	for _, d := range p.Facts.ClosedDays {
		if p.L == EN {
			names = append(names, p.L.WeekdayName(d)+"s")
		} else {
			names = append(names, "วัน"+p.L.WeekdayName(d))
		}
	}
	sep := ", "
	if p.L == TH {
		sep = " และ"
	}
	return p.L.T("hours.closed", o, c, strings.Join(names, sep))
}

// PolicyLine summarises the self-service change rules from live settings.
func (p *Page) PolicyLine() string {
	if p.Set == nil {
		return ""
	}
	cancelH, moveH, maxMoves := p.Set.Int("cancel_cutoff_hours"), p.Set.Int("reschedule_cutoff_hours"), p.Set.Int("max_reschedules")
	phone := p.Set.Str("phone")
	var b strings.Builder
	if p.L == EN {
		fmt.Fprintf(&b, "You can reschedule up to %d hours before your session", moveH)
		if maxMoves > 0 {
			fmt.Fprintf(&b, " (up to %d time%s)", maxMoves, map[bool]string{true: "s", false: ""}[maxMoves > 1])
		}
		fmt.Fprintf(&b, " and cancel up to %d hours before, from your booking page. ", cancelH)
		if p.Set.Str("cancel_refund_mode") == "credit" {
			b.WriteString("When you cancel, what you paid comes back as a credit for your next booking. ")
		} else {
			b.WriteString("When you cancel, online payments are refunded to the original payment method. ")
		}
		if phone != "" {
			fmt.Fprintf(&b, "After that, please call us on %s.", phone)
		}
		return b.String()
	}
	fmt.Fprintf(&b, "เลื่อนรอบได้ก่อนเวลาเริ่ม %d ชั่วโมง", moveH)
	if maxMoves > 0 {
		fmt.Fprintf(&b, " (สูงสุด %d ครั้ง)", maxMoves)
	}
	fmt.Fprintf(&b, " และยกเลิกได้ก่อนเวลาเริ่ม %d ชั่วโมง ทำเองได้ในหน้าการจองของคุณ ", cancelH)
	if p.Set.Str("cancel_refund_mode") == "credit" {
		b.WriteString("ถ้ายกเลิก ยอดที่ชำระจะคืนเป็นเครดิตไว้ใช้จองครั้งต่อไป ")
	} else {
		b.WriteString("ถ้ายกเลิก ยอดที่ชำระออนไลน์จะคืนเข้าช่องทางเดิม ")
	}
	if phone != "" {
		fmt.Fprintf(&b, "หลังจากนั้นกรุณาโทรหาเราที่ %s", phone)
	}
	return b.String()
}
