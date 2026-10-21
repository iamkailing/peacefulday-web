package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
)

// UserError is a validation/business error safe to show customers. Code maps to an i18n key "err.<code>".
type UserError struct{ Code string }

func (e UserError) Error() string { return "user error: " + e.Code }

func uerr(code string) error { return UserError{Code: code} }

type Experience struct {
	ID          int64
	Slug        string
	NameTH      string
	NameEN      string
	SummaryTH   string
	SummaryEN   string
	DurationMin int
	Status      string
	Sort        int
}

type Product struct {
	ID              int64
	Kind            string
	Category        string
	NameTH, NameEN  string
	DescTH, DescEN  string
	Price           int64
	PriceMode       string
	MinParticipants int
	MaxParticipants int
	Credits         int
	ValidityDays    int
	ExperienceID    sql.NullInt64
	AllowedWeekdays []int64
	StartAfter      string
	AvailableFrom   sql.NullTime
	AvailableUntil  sql.NullTime
	Public          bool
	Active          bool
	Sort            int
}

const productCols = `id,kind,category,name_th,name_en,desc_th,desc_en,price,price_mode,min_participants,max_participants,credits,validity_days,experience_id,allowed_weekdays,start_after,available_from,available_until,public,active,sort`

func scanProduct(sc interface{ Scan(...any) error }) (Product, error) {
	var p Product
	var wd pq.Int64Array
	err := sc.Scan(&p.ID, &p.Kind, &p.Category, &p.NameTH, &p.NameEN, &p.DescTH, &p.DescEN, &p.Price, &p.PriceMode,
		&p.MinParticipants, &p.MaxParticipants, &p.Credits, &p.ValidityDays, &p.ExperienceID, &wd, &p.StartAfter,
		&p.AvailableFrom, &p.AvailableUntil, &p.Public, &p.Active, &p.Sort)
	p.AllowedWeekdays = wd
	return p, err
}

type Slot struct {
	ID           int64
	ExperienceID int64
	StartsAt     time.Time
	EndsAt       time.Time
	Capacity     int
	Status       string
	Note         string
	Booked       int // confirmed + checked in
	Held         int // active holds
}

func (s Slot) Used() int { return s.Booked + s.Held }
func (s Slot) Remaining() int {
	r := s.Capacity - s.Used()
	if r < 0 {
		return 0
	}
	return r
}

// ---------- reading catalog ----------

type querier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func ListExperiences(ctx context.Context, q querier, includeHidden bool) ([]Experience, error) {
	where := "WHERE status <> 'hidden'"
	if includeHidden {
		where = ""
	}
	rows, err := q.QueryContext(ctx, `SELECT id,slug,name_th,name_en,summary_th,summary_en,duration_min,status,sort FROM experiences `+where+` ORDER BY sort,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Experience
	for rows.Next() {
		var e Experience
		if err := rows.Scan(&e.ID, &e.Slug, &e.NameTH, &e.NameEN, &e.SummaryTH, &e.SummaryEN, &e.DurationMin, &e.Status, &e.Sort); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func GetProduct(ctx context.Context, q querier, id int64) (Product, error) {
	return scanProduct(q.QueryRowContext(ctx, `SELECT `+productCols+` FROM products WHERE id=$1`, id))
}

func ListProducts(ctx context.Context, q querier, where string, args ...any) ([]Product, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+productCols+` FROM products `+where+` ORDER BY sort,id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Product
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SellableNow reports whether a product is on sale today.
func (p Product) SellableNow() bool {
	if !p.Active {
		return false
	}
	today := DateOnly(Now())
	if p.AvailableFrom.Valid && today.Before(DateOnly(p.AvailableFrom.Time)) {
		return false
	}
	if p.AvailableUntil.Valid && today.After(DateOnly(p.AvailableUntil.Time)) {
		return false
	}
	return true
}

// FitsSlot checks a ticket's restrictions against a session.
func (p Product) FitsSlot(s Slot) bool {
	if p.Kind != "ticket" {
		return false
	}
	if p.ExperienceID.Valid && p.ExperienceID.Int64 != s.ExperienceID {
		return false
	}
	st := s.StartsAt.In(BKK)
	if len(p.AllowedWeekdays) > 0 {
		ok := false
		for _, d := range p.AllowedWeekdays {
			if int(d) == int(st.Weekday()) {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	if p.StartAfter != "" && st.Format("15:04") < p.StartAfter {
		return false
	}
	return true
}

func (p Product) PriceFor(n int) int64 {
	if p.PriceMode == "flat" {
		return p.Price
	}
	return p.Price * int64(n)
}

// ---------- slot generation ----------

// EnsureSlots materialises sessions from the weekly template for [from, to] (dates in Bangkok).
// Existing slots are never touched, so per-date overrides survive. Blackout dates get no slots.
// EnsureSlots materialises sessions from the weekly templates. Past days are never back-filled,
// so reports don't gain capacity for days before the system was in use.
func EnsureSlots(ctx context.Context, q querier, from, to time.Time) error {
	if today := DateOnly(Now()); from.Before(today) {
		from = today
	}
	if to.Before(from) {
		return nil
	}
	return generateSlots(ctx, q, from, to)
}

func generateSlots(ctx context.Context, q querier, from, to time.Time) error {
	from, to = DateOnly(from), DateOnly(to)
	_, err := q.ExecContext(ctx, `
		INSERT INTO slots (experience_id, starts_at, ends_at, capacity)
		SELECT t.experience_id,
		       (d::date + t.start_time::time) AT TIME ZONE 'Asia/Bangkok',
		       (d::date + t.start_time::time + make_interval(mins => t.duration_min)) AT TIME ZONE 'Asia/Bangkok',
		       t.capacity
		FROM generate_series($1::date, $2::date, interval '1 day') d
		JOIN schedule_templates t ON t.weekday = EXTRACT(DOW FROM d)::int
		JOIN experiences e ON e.id = t.experience_id AND e.status = 'active'
		WHERE NOT EXISTS (SELECT 1 FROM blackouts b WHERE d::date BETWEEN b.start_date AND b.end_date)
		ON CONFLICT (experience_id, starts_at) DO NOTHING`,
		from.Format("2006-01-02"), to.Format("2006-01-02"))
	return err
}

const usageSQL = `
  COALESCE(SUM(b.participants) FILTER (WHERE b.status IN ('confirmed','checked_in')),0) AS booked,
  COALESCE(SUM(b.participants) FILTER (WHERE b.status='hold' AND b.hold_expires_at > now()),0) AS held`

// SlotsBetween returns sessions with live usage, ordered by start.
func SlotsBetween(ctx context.Context, q querier, expID int64, from, to time.Time) ([]Slot, error) {
	args := []any{from, to}
	cond := ""
	if expID > 0 {
		cond = " AND s.experience_id=$3"
		args = append(args, expID)
	}
	rows, err := q.QueryContext(ctx, `
		SELECT s.id, s.experience_id, s.starts_at, s.ends_at, s.capacity, s.status, s.note,`+usageSQL+`
		FROM slots s LEFT JOIN bookings b ON b.slot_id = s.id
		WHERE s.starts_at >= $1 AND s.starts_at < $2`+cond+`
		GROUP BY s.id ORDER BY s.starts_at, s.experience_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Slot
	for rows.Next() {
		var s Slot
		if err := rows.Scan(&s.ID, &s.ExperienceID, &s.StartsAt, &s.EndsAt, &s.Capacity, &s.Status, &s.Note, &s.Booked, &s.Held); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func GetSlot(ctx context.Context, q querier, id int64, forUpdate bool) (Slot, error) {
	var s Slot
	lock := ""
	if forUpdate {
		lock = " FOR UPDATE"
	}
	err := q.QueryRowContext(ctx, `SELECT id, experience_id, starts_at, ends_at, capacity, status, note FROM slots WHERE id=$1`+lock, id).
		Scan(&s.ID, &s.ExperienceID, &s.StartsAt, &s.EndsAt, &s.Capacity, &s.Status, &s.Note)
	if err != nil {
		return s, err
	}
	err = q.QueryRowContext(ctx, `SELECT `+usageSQL+` FROM bookings b WHERE b.slot_id=$1`, id).Scan(&s.Booked, &s.Held)
	return s, err
}

// ---------- customers ----------

type Customer struct {
	ID               int64
	Name, Phone      string
	Email, Lang      string
	MarketingConsent bool
	Notes            string
	Tags             []string
	CreatedAt        time.Time
}

// UpsertCustomer finds a customer by phone (then email) and refreshes their details, or creates one.
func UpsertCustomer(ctx context.Context, q querier, name, phone, email, lang string, consent bool) (int64, error) {
	var id int64
	err := sql.ErrNoRows
	if phone != "" {
		err = q.QueryRowContext(ctx, `SELECT id FROM customers WHERE phone=$1 ORDER BY id LIMIT 1 FOR UPDATE`, phone).Scan(&id)
	}
	if errors.Is(err, sql.ErrNoRows) && email != "" {
		err = q.QueryRowContext(ctx, `SELECT id FROM customers WHERE email=$1 ORDER BY id LIMIT 1 FOR UPDATE`, email).Scan(&id)
	}
	switch {
	case err == nil:
		_, err = q.ExecContext(ctx, `UPDATE customers SET
			name = CASE WHEN $2 <> '' THEN $2 ELSE name END,
			phone = CASE WHEN phone = '' THEN $3 ELSE phone END,
			email = CASE WHEN $4 <> '' THEN $4 ELSE email END,
			lang = $5,
			marketing_consent = marketing_consent OR $6,
			consent_at = CASE WHEN $6 AND NOT marketing_consent THEN now() ELSE consent_at END
			WHERE id=$1`, id, name, phone, email, lang, consent)
		return id, err
	case errors.Is(err, sql.ErrNoRows):
		var consentAt any
		if consent {
			consentAt = Now()
		}
		err = q.QueryRowContext(ctx, `INSERT INTO customers(name,phone,email,lang,marketing_consent,consent_at) VALUES($1,$2,$3,$4,$5,$6) RETURNING id`,
			name, phone, email, lang, consent, consentAt).Scan(&id)
		return id, err
	default:
		return 0, err
	}
}

// ---------- pricing ----------

type Quote struct {
	Product        Product
	Participants   int
	Subtotal       int64
	CreditsApplied int
	CreditsValue   int64
	Discount       int64
	Total          int64
	PromoID        int64
	PromoCode      string
	PassID         int64
	PassCode       string
}

type QuoteInput struct {
	ProductID    int64
	Slot         Slot
	Participants int
	PromoCode    string
	PassCode     string
	CustomerID   int64 // 0 if unknown yet
	Phone, Email string
	Channel      string
	Lock         bool // lock promo/pass rows (inside a booking transaction)
}

// BuildQuote prices a booking. Order: subtotal → voucher credits → promo discount on the remainder.
func BuildQuote(ctx context.Context, q querier, in QuoteInput) (Quote, error) {
	p, err := GetProduct(ctx, q, in.ProductID)
	if err != nil {
		return Quote{}, uerr("product")
	}
	staff := in.Channel != "web"
	if p.Kind != "ticket" || !p.Active || (!staff && (!p.Public || !p.SellableNow())) || !p.FitsSlot(in.Slot) {
		return Quote{}, uerr("product")
	}
	n := in.Participants
	if n < p.MinParticipants || n > p.MaxParticipants || n > 50 {
		return Quote{}, uerr("participants")
	}
	qt := Quote{Product: p, Participants: n, Subtotal: p.PriceFor(n)}
	lock := ""
	if in.Lock {
		lock = " FOR UPDATE"
	}

	// Voucher / package credits: one credit covers one guest.
	if code := strings.ToUpper(strings.TrimSpace(in.PassCode)); code != "" {
		var id int64
		var total, used int
		var status string
		var exp sql.NullTime
		err := q.QueryRowContext(ctx, `SELECT id, credits_total, credits_used, status, expires_at FROM passes WHERE code=$1`+lock, code).
			Scan(&id, &total, &used, &status, &exp)
		if err != nil || status != "active" || (exp.Valid && exp.Time.Before(Now())) || total-used <= 0 {
			return Quote{}, uerr("pass")
		}
		apply := total - used
		if apply > n {
			apply = n
		}
		qt.PassID, qt.PassCode, qt.CreditsApplied = id, code, apply
		qt.CreditsValue = qt.Subtotal * int64(apply) / int64(n)
	}
	remaining := qt.Subtotal - qt.CreditsValue

	if code := strings.ToUpper(strings.TrimSpace(in.PromoCode)); code != "" && remaining > 0 {
		var (
			id, value                 int64
			kind                      string
			maxUses, usedCount, perCx int
			minP                      int
			pids                      pq.Int64Array
			from, until               sql.NullTime
			active                    bool
		)
		err := q.QueryRowContext(ctx, `SELECT id, kind, value, max_uses, used_count, per_customer_limit, min_participants, product_ids, valid_from, valid_until, active
			FROM promo_codes WHERE code=$1`+lock, code).Scan(&id, &kind, &value, &maxUses, &usedCount, &perCx, &minP, &pids, &from, &until, &active)
		now := Now()
		if err != nil || !active || (from.Valid && now.Before(from.Time)) || (until.Valid && now.After(until.Time)) || n < minP {
			return Quote{}, uerr("promo")
		}
		if len(pids) > 0 {
			ok := false
			for _, x := range pids {
				if x == p.ID {
					ok = true
				}
			}
			if !ok {
				return Quote{}, uerr("promo")
			}
		}
		if maxUses > 0 && usedCount >= maxUses {
			return Quote{}, uerr("promo_used")
		}
		if perCx > 0 && (in.CustomerID > 0 || in.Phone != "" || in.Email != "") {
			var times int
			_ = q.QueryRowContext(ctx, `SELECT count(*) FROM promo_redemptions r JOIN customers c ON c.id=r.customer_id
				WHERE r.promo_id=$1 AND r.reversed_at IS NULL AND (c.id=$2 OR (c.phone<>'' AND c.phone=$3) OR (c.email<>'' AND c.email=$4))`,
				id, in.CustomerID, in.Phone, in.Email).Scan(&times)
			if times >= perCx {
				return Quote{}, uerr("promo_used")
			}
		}
		d := value
		if kind == "percent" {
			if value > 100 {
				value = 100
			}
			d = remaining * value / 100
		}
		if d > remaining {
			d = remaining
		}
		qt.PromoID, qt.PromoCode, qt.Discount = id, code, d
	}
	qt.Total = qt.Subtotal - qt.CreditsValue - qt.Discount
	if qt.Total < 0 {
		qt.Total = 0
	}
	return qt, nil
}

// ---------- creating bookings ----------

type BookingRequest struct {
	SlotID                                                              int64
	ProductID                                                           int64
	Participants                                                        int
	Name                                                                string
	Phone                                                               string
	Email                                                               string
	Lang                                                                string
	Consent                                                             bool
	PromoCode                                                           string
	PassCode                                                            string
	CustomerNote                                                        string
	IdempotencyKey                                                      string
	Channel                                                             string // web | admin | walk_in | phone | line
	StaffID                                                             int64
	PayAtVenue                                                          bool // staff only
	Complimentary                                                       bool // staff only (manager+)
	OverCapacity                                                        bool // staff only (manager+)
	AllowDuplicate                                                      bool // staff only
	UTMSource, UTMMedium, UTMCampaign, Referrer, LandingPath, VisitorID string
	AdsConsent                                                          bool // visitor accepted advertising cookies (gates Meta CAPI)
}

type BookingResult struct {
	BookingID   int64
	Ref         string
	ManageToken string // returned once; only its hash is stored
	Status      string
	Total       int64
	Replayed    bool
}

// CreateBooking is the single entry point for reservations. Capacity is enforced under a row lock on the slot,
// so concurrent checkouts for the last spots are serialised and can never overbook.
func (a *App) CreateBooking(ctx context.Context, r BookingRequest) (BookingResult, error) {
	if r.Channel == "" {
		r.Channel = "web"
	}
	staff := r.Channel != "web"
	r.Name = clip(r.Name, 120)
	r.Phone = NormPhone(r.Phone)
	r.Email = NormEmail(r.Email)
	if r.Name == "" {
		return BookingResult{}, uerr("name")
	}
	if !ValidPhone(r.Phone) && !(staff && r.Phone == "") {
		return BookingResult{}, uerr("phone")
	}
	if r.Email != "" && !ValidEmail(r.Email) || (!staff && r.Email == "") {
		return BookingResult{}, uerr("email")
	}
	if r.Lang != "en" {
		r.Lang = "th"
	}

	// Idempotent replay: same key returns the same booking.
	if r.IdempotencyKey != "" {
		var res BookingResult
		err := a.DB.QueryRowContext(ctx, `SELECT id, ref, status, total FROM bookings WHERE idempotency_key=$1`, r.IdempotencyKey).
			Scan(&res.BookingID, &res.Ref, &res.Status, &res.Total)
		if err == nil {
			res.Replayed = true
			return res, nil
		}
	}

	set, err := LoadSettings(ctx, a.DB)
	if err != nil {
		return BookingResult{}, err
	}
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return BookingResult{}, err
	}
	defer tx.Rollback()

	slot, err := GetSlot(ctx, tx, r.SlotID, true) // row lock: serialises bookings for this session
	if errors.Is(err, sql.ErrNoRows) {
		return BookingResult{}, uerr("slot_closed")
	} else if err != nil {
		return BookingResult{}, err
	}
	now := Now()
	if slot.Status != "open" && !(staff && r.OverCapacity) {
		return BookingResult{}, uerr("slot_closed")
	}
	if !staff {
		if slot.StartsAt.Before(now.Add(time.Duration(set.Int("min_lead_minutes")) * time.Minute)) {
			return BookingResult{}, uerr("too_soon")
		}
		if slot.StartsAt.After(now.AddDate(0, 0, set.Int("booking_horizon_days")+1)) {
			return BookingResult{}, uerr("too_far")
		}
	} else if slot.EndsAt.Before(now.Add(-12 * time.Hour)) {
		return BookingResult{}, uerr("slot_closed")
	}
	// Duplicate guard. A confirmed booking with the same phone or email in this session blocks a second
	// one (staff can override). An unpaid hold by the same person (phone AND email match) is released
	// instead: they are simply retrying checkout and must not be locked out by their own abandoned hold.
	var releasedSessions []string
	if !r.AllowDuplicate && (r.Phone != "" || r.Email != "") {
		var dup bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM bookings b JOIN customers c ON c.id=b.customer_id
			WHERE b.slot_id=$1 AND b.status IN ('confirmed','checked_in')
			AND ((c.phone<>'' AND c.phone=$2) OR (c.email<>'' AND c.email=$3)))`, slot.ID, r.Phone, r.Email).Scan(&dup); err != nil {
			return BookingResult{}, err
		}
		if dup {
			return BookingResult{}, uerr("duplicate")
		}
		if r.Phone != "" && r.Email != "" {
			var holds []int64
			rows, err := tx.QueryContext(ctx, `SELECT b.id FROM bookings b JOIN customers c ON c.id=b.customer_id
				WHERE b.slot_id=$1 AND b.status='hold' AND c.phone=$2 AND c.email=$3`, slot.ID, r.Phone, r.Email)
			if err != nil {
				return BookingResult{}, err
			}
			for rows.Next() {
				var id int64
				if rows.Scan(&id) == nil {
					holds = append(holds, id)
				}
			}
			rows.Close()
			for _, id := range holds {
				if _, err := tx.ExecContext(ctx, `UPDATE bookings SET status='expired', payment_state='expired', updated_at=now() WHERE id=$1 AND status='hold'`, id); err != nil {
					return BookingResult{}, err
				}
				if err := reverseRedemptions(ctx, tx, id); err != nil {
					return BookingResult{}, err
				}
				prow, err := tx.QueryContext(ctx, `UPDATE payments SET status='expired', updated_at=now() WHERE booking_id=$1 AND status='pending' RETURNING COALESCE(provider_session,'')`, id)
				if err != nil {
					return BookingResult{}, err
				}
				for prow.Next() {
					var sid string
					if prow.Scan(&sid) == nil && sid != "" {
						releasedSessions = append(releasedSessions, sid)
					}
				}
				prow.Close()
			}
			if len(holds) > 0 {
				if slot, err = GetSlot(ctx, tx, r.SlotID, true); err != nil {
					return BookingResult{}, err
				}
			}
		}
	}
	if slot.Remaining() < r.Participants && !(staff && r.OverCapacity) {
		return BookingResult{}, uerr("slot_full")
	}

	custID, err := UpsertCustomer(ctx, tx, r.Name, r.Phone, r.Email, r.Lang, r.Consent)
	if err != nil {
		return BookingResult{}, err
	}
	qt, err := BuildQuote(ctx, tx, QuoteInput{ProductID: r.ProductID, Slot: slot, Participants: r.Participants,
		PromoCode: r.PromoCode, PassCode: r.PassCode, CustomerID: custID, Phone: r.Phone, Email: r.Email, Channel: r.Channel, Lock: true})
	if err != nil {
		return BookingResult{}, err
	}

	status, payMethod, payState := "hold", "online", "pending"
	var holdUntil any = now.Add(time.Duration(set.Int("hold_minutes")) * time.Minute)
	switch {
	case staff && r.Complimentary:
		qt.Discount += qt.Total
		qt.Total = 0
		status, payMethod, payState, holdUntil = "confirmed", "complimentary", "none", nil
	case qt.Total == 0:
		status, payMethod, payState, holdUntil = "confirmed", "credits", "none", nil
	case staff && r.PayAtVenue:
		status, payMethod, payState, holdUntil = "confirmed", "pay_at_venue", "due_at_venue", nil
	}

	token := RandomToken()
	var idem any
	if r.IdempotencyKey != "" {
		idem = r.IdempotencyKey
	}
	var staffID any
	if r.StaffID > 0 {
		staffID = r.StaffID
	}
	res := BookingResult{ManageToken: token, Status: status, Total: qt.Total}
	for attempt := 0; ; attempt++ {
		res.Ref = NewBookingRef()
		err = tx.QueryRowContext(ctx, `INSERT INTO bookings
			(ref,customer_id,slot_id,product_id,participants,status,hold_expires_at,subtotal,discount,credits_value,credits_applied,total,
			 pay_method,payment_state,promo_code_id,pass_id,manage_token_hash,idempotency_key,channel,created_by,customer_note,
			 utm_source,utm_medium,utm_campaign,referrer,landing_path,visitor_id,ads_consent)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28) RETURNING id`,
			res.Ref, custID, slot.ID, qt.Product.ID, r.Participants, status, holdUntil, qt.Subtotal, qt.Discount, qt.CreditsValue, qt.CreditsApplied, qt.Total,
			payMethod, payState, nullID(qt.PromoID), nullID(qt.PassID), HashToken(token), idem, r.Channel, staffID, clip(r.CustomerNote, 500),
			clip(r.UTMSource, 100), clip(r.UTMMedium, 100), clip(r.UTMCampaign, 150), clip(r.Referrer, 300), clip(r.LandingPath, 300), clip(r.VisitorID, 64), r.AdsConsent).Scan(&res.BookingID)
		if err == nil {
			break
		}
		var pqe *pq.Error
		if errors.As(err, &pqe) && pqe.Code == "23505" && strings.Contains(pqe.Constraint, "idempotency") {
			tx.Rollback()
			return a.CreateBooking(ctx, r) // concurrent replay: return the winner
		}
		if errors.As(err, &pqe) && pqe.Code == "23505" && attempt < 5 {
			continue // ref collision, try another
		}
		return BookingResult{}, err
	}

	if qt.PassID > 0 && qt.CreditsApplied > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE passes SET credits_used = credits_used + $2 WHERE id=$1`, qt.PassID, qt.CreditsApplied); err != nil {
			return BookingResult{}, uerr("pass")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO pass_redemptions(pass_id,booking_id,credits) VALUES($1,$2,$3)`, qt.PassID, res.BookingID, qt.CreditsApplied); err != nil {
			return BookingResult{}, err
		}
	}
	if qt.PromoID > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE promo_codes SET used_count = used_count + 1 WHERE id=$1`, qt.PromoID); err != nil {
			return BookingResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO promo_redemptions(promo_id,booking_id,customer_id,amount) VALUES($1,$2,$3,$4)`, qt.PromoID, res.BookingID, custID, qt.Discount); err != nil {
			return BookingResult{}, err
		}
	}
	if status == "confirmed" {
		if err := a.enqueueBookingNotice(ctx, tx, res.BookingID, "booking_confirmed"); err != nil {
			return BookingResult{}, err
		}
	}
	if staff {
		Audit(ctx, tx, r.StaffID, "", "booking.create", "booking", res.Ref, map[string]any{
			"slot": slot.StartsAt.Format(time.RFC3339), "participants": r.Participants, "channel": r.Channel,
			"pay": payMethod, "over_capacity": r.OverCapacity && slot.Remaining() < r.Participants}, "")
	}
	if err := tx.Commit(); err != nil {
		var pqe *pq.Error
		if errors.As(err, &pqe) && pqe.Code == "40001" {
			return BookingResult{}, uerr("slot_full")
		}
		return BookingResult{}, err
	}
	for _, sid := range releasedSessions {
		if err := a.Pay.ExpireCheckout(ctx, sid); err != nil {
			a.Log.Warn("could not expire replaced checkout session", "session", sid, "err", err)
		}
	}
	return res, nil
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// reverseRedemptions gives back voucher credits and promo uses held by a booking. Call inside a tx.
func reverseRedemptions(ctx context.Context, tx querier, bookingID int64) error {
	if _, err := tx.ExecContext(ctx, `
		WITH r AS (UPDATE pass_redemptions SET reversed_at=now() WHERE booking_id=$1 AND reversed_at IS NULL RETURNING pass_id, credits)
		UPDATE passes p SET credits_used = GREATEST(0, p.credits_used - r.credits) FROM r WHERE p.id = r.pass_id`, bookingID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		WITH r AS (UPDATE promo_redemptions SET reversed_at=now() WHERE booking_id=$1 AND reversed_at IS NULL RETURNING promo_id)
		UPDATE promo_codes p SET used_count = GREATEST(0, p.used_count - 1) FROM r WHERE p.id = r.promo_id`, bookingID)
	return err
}

// ExpireHolds releases unpaid holds whose time ran out. Safe to run concurrently (row locks + status check).
func (a *App) ExpireHolds(ctx context.Context) (int, error) {
	rows, err := a.DB.QueryContext(ctx, `SELECT id FROM bookings WHERE status='hold' AND hold_expires_at <= now() ORDER BY id LIMIT 200`)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	n := 0
	for _, id := range ids {
		var sessions []string
		err := a.inTx(ctx, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx, `UPDATE bookings SET status='expired', payment_state='expired', updated_at=now()
				WHERE id=$1 AND status='hold' AND hold_expires_at <= now()`, id)
			if err != nil {
				return err
			}
			if c, _ := res.RowsAffected(); c == 0 {
				return nil
			}
			n++
			if err := reverseRedemptions(ctx, tx, id); err != nil {
				return err
			}
			r, err := tx.QueryContext(ctx, `UPDATE payments SET status='expired', updated_at=now() WHERE booking_id=$1 AND status='pending' RETURNING COALESCE(provider_session,'')`, id)
			if err != nil {
				return err
			}
			for r.Next() {
				var s string
				r.Scan(&s)
				if s != "" {
					sessions = append(sessions, s)
				}
			}
			return r.Close()
		})
		if err != nil {
			return n, err
		}
		for _, s := range sessions {
			if err := a.Pay.ExpireCheckout(ctx, s); err != nil {
				// Not fatal: if the customer pays anyway, the webhook path handles late payment.
				a.Log.Warn("could not expire checkout session", "session", s, "err", err)
			}
		}
	}
	return n, nil
}

func (a *App) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// ---------- reading bookings ----------

type BookingView struct {
	ID              int64
	Ref             string
	Status          string
	PaymentState    string
	PayMethod       string
	Participants    int
	Subtotal        int64
	Discount        int64
	CreditsValue    int64
	CreditsApplied  int
	Total           int64
	HoldExpiresAt   sql.NullTime
	CheckedInAt     sql.NullTime
	CancelledAt     sql.NullTime
	CancelReason    string
	NeedsAttention  string
	CustomerNote    string
	StaffNote       string
	Channel         string
	RescheduleCount int
	CreatedAt       time.Time
	SlotID          int64
	StartsAt        time.Time
	EndsAt          time.Time
	ExperienceID    int64
	ExpNameTH       string
	ExpNameEN       string
	ProductID       int64
	ProdNameTH      string
	ProdNameEN      string
	CustomerID      int64
	CustomerName    string
	Phone           string
	Email           string
	CustLang        string
	PromoCode       sql.NullString
	PassCode        sql.NullString
	UTMSource       string
	UTMCampaign     string
	ReceiptURL      string
}

const bookingViewSQL = `SELECT b.id, b.ref, b.status, b.payment_state, b.pay_method, b.participants, b.subtotal, b.discount, b.credits_value, b.credits_applied, b.total,
	b.hold_expires_at, b.checked_in_at, b.cancelled_at, b.cancel_reason, b.needs_attention, b.customer_note, b.staff_note, b.channel, b.reschedule_count, b.created_at,
	s.id, s.starts_at, s.ends_at, e.id, e.name_th, e.name_en, p.id, p.name_th, p.name_en, c.id, c.name, c.phone, c.email, c.lang,
	pc.code, ps.code, b.utm_source, b.utm_campaign,
	COALESCE((SELECT receipt_url FROM payments py WHERE py.booking_id=b.id AND py.status IN ('paid','refunded','partially_refunded') ORDER BY py.id DESC LIMIT 1),'')
	FROM bookings b
	JOIN slots s ON s.id=b.slot_id JOIN experiences e ON e.id=s.experience_id JOIN products p ON p.id=b.product_id
	JOIN customers c ON c.id=b.customer_id
	LEFT JOIN promo_codes pc ON pc.id=b.promo_code_id LEFT JOIN passes ps ON ps.id=b.pass_id `

func scanBooking(sc interface{ Scan(...any) error }) (BookingView, error) {
	var v BookingView
	err := sc.Scan(&v.ID, &v.Ref, &v.Status, &v.PaymentState, &v.PayMethod, &v.Participants, &v.Subtotal, &v.Discount, &v.CreditsValue, &v.CreditsApplied, &v.Total,
		&v.HoldExpiresAt, &v.CheckedInAt, &v.CancelledAt, &v.CancelReason, &v.NeedsAttention, &v.CustomerNote, &v.StaffNote, &v.Channel, &v.RescheduleCount, &v.CreatedAt,
		&v.SlotID, &v.StartsAt, &v.EndsAt, &v.ExperienceID, &v.ExpNameTH, &v.ExpNameEN, &v.ProductID, &v.ProdNameTH, &v.ProdNameEN,
		&v.CustomerID, &v.CustomerName, &v.Phone, &v.Email, &v.CustLang, &v.PromoCode, &v.PassCode, &v.UTMSource, &v.UTMCampaign, &v.ReceiptURL)
	return v, err
}

func GetBookingByRef(ctx context.Context, q querier, ref string) (BookingView, error) {
	return scanBooking(q.QueryRowContext(ctx, bookingViewSQL+`WHERE b.ref=$1`, strings.ToUpper(strings.TrimSpace(ref))))
}

func GetBookingByID(ctx context.Context, q querier, id int64) (BookingView, error) {
	return scanBooking(q.QueryRowContext(ctx, bookingViewSQL+`WHERE b.id=$1`, id))
}

func ListBookings(ctx context.Context, q querier, where string, args ...any) ([]BookingView, error) {
	rows, err := q.QueryContext(ctx, bookingViewSQL+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BookingView
	for rows.Next() {
		v, err := scanBooking(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (v BookingView) Active() bool {
	return v.Status == "confirmed" || v.Status == "checked_in" || v.Status == "hold"
}

// ---------- changes: cancel, reschedule, check-in ----------

type ChangeActor struct {
	StaffID  int64
	Customer bool
	IP       string
}

// CanCustomerChange applies the admin-defined cutoff and reschedule limit.
func CanCustomerChange(set Settings, v BookingView, kind string) error {
	if v.Status != "confirmed" {
		return uerr("change_closed")
	}
	hours := set.Int("cancel_cutoff_hours")
	if kind == "reschedule" {
		hours = set.Int("reschedule_cutoff_hours")
		if v.RescheduleCount >= set.Int("max_reschedules") {
			return uerr("max_reschedule")
		}
	}
	if Now().Add(time.Duration(hours) * time.Hour).After(v.StartsAt) {
		return uerr("change_closed")
	}
	return nil
}

type CancelOptions struct {
	Reason string
	Refund string // "auto" (customer: per settings) | "refund" | "credit" | "none"
}

type CancelResult struct {
	RefundedAmount int64
	CreditPassCode string
	RefundError    string
}

// CancelBooking cancels and returns voucher credits; paid online amounts are refunded or turned into credit.
// creditNote tags the store-credit pass issued when a booking is cancelled, so the booking page and
// the cancellation email can show its code.
func creditNote(bookingID int64) string {
	return fmt.Sprintf("credit for cancelled booking #%d", bookingID)
}

func (a *App) CancelBooking(ctx context.Context, bookingID int64, actor ChangeActor, opt CancelOptions) (CancelResult, error) {
	var res CancelResult
	set, _ := LoadSettings(ctx, a.DB)
	mode := opt.Refund
	if mode == "auto" || mode == "" {
		mode = set.Str("cancel_refund_mode")
	}
	var payID int64
	var paidAmount, already int64
	var payRef string
	err := a.inTx(ctx, func(tx *sql.Tx) error {
		var status string
		var custID, productID int64
		var participants int
		if err := tx.QueryRowContext(ctx, `SELECT status, customer_id, product_id, participants FROM bookings WHERE id=$1 FOR UPDATE`, bookingID).
			Scan(&status, &custID, &productID, &participants); err != nil {
			return err
		}
		if status != "confirmed" && status != "hold" {
			return uerr("change_closed")
		}
		if actor.Customer {
			v, err := GetBookingByID(ctx, tx, bookingID)
			if err != nil {
				return err
			}
			if err := CanCustomerChange(set, v, "cancel"); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE bookings SET status='cancelled', cancelled_at=now(), cancel_reason=$2, updated_at=now() WHERE id=$1`, bookingID, clip(opt.Reason, 300)); err != nil {
			return err
		}
		if err := reverseRedemptions(ctx, tx, bookingID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE payments SET status='expired', updated_at=now() WHERE booking_id=$1 AND status='pending'`, bookingID); err != nil {
			return err
		}
		err := tx.QueryRowContext(ctx, `SELECT id, amount, refunded_amount, COALESCE(provider_payment,'') FROM payments WHERE booking_id=$1 AND status IN ('paid','partially_refunded') AND provider<>'venue' ORDER BY id DESC LIMIT 1`, bookingID).
			Scan(&payID, &paidAmount, &already, &payRef)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if payID > 0 && mode == "credit" {
			// Issue a store-credit pass worth the paid guests.
			code := NewPassCode()
			var creditProduct int64
			if err := tx.QueryRowContext(ctx, `SELECT id FROM products WHERE kind='pass' AND category='complimentary' ORDER BY id LIMIT 1`).Scan(&creditProduct); err != nil {
				return fmt.Errorf("no complimentary pass product configured: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO passes(code,product_id,customer_id,credits_total,status,expires_at,note)
				VALUES($1,$2,$3,$4,'active',$5,$6)`, code, creditProduct, custID, participants, Now().AddDate(0, 6, 0), creditNote(bookingID)); err != nil {
				return err
			}
			res.CreditPassCode = code
			payID = 0
		}
		who := "customer"
		if !actor.Customer {
			who = "staff"
		}
		Audit(ctx, tx, actor.StaffID, who, "booking.cancel", "booking", bookingID, map[string]any{"reason": opt.Reason, "refund_mode": mode}, actor.IP)
		return a.enqueueBookingNotice(ctx, tx, bookingID, "booking_cancelled")
	})
	if err != nil {
		return res, err
	}
	if payID > 0 && mode == "refund" && paidAmount-already > 0 {
		amt, rerr := a.RefundPayment(ctx, payID, paidAmount-already, "booking cancelled", actor.StaffID, fmt.Sprintf("cancel-%d", bookingID))
		res.RefundedAmount = amt
		if rerr != nil {
			res.RefundError = rerr.Error()
			a.flag(ctx, bookingID, "Refund failed after cancellation: "+rerr.Error())
		}
	}
	return res, nil
}

func (a *App) flag(ctx context.Context, bookingID int64, msg string) {
	_, _ = a.DB.ExecContext(ctx, `UPDATE bookings SET needs_attention=$2, updated_at=now() WHERE id=$1`, bookingID, clip(msg, 500))
	a.AdminAlert(ctx, "Booking needs attention", msg, bookingID)
}

// RescheduleBooking moves a booking to another session, enforcing capacity on the new one.
func (a *App) RescheduleBooking(ctx context.Context, bookingID, newSlotID int64, actor ChangeActor, overCapacity bool) error {
	set, _ := LoadSettings(ctx, a.DB)
	return a.inTx(ctx, func(tx *sql.Tx) error {
		var oldSlot, productID int64
		var participants int
		var status string
		if err := tx.QueryRowContext(ctx, `SELECT slot_id, product_id, participants, status FROM bookings WHERE id=$1 FOR UPDATE`, bookingID).
			Scan(&oldSlot, &productID, &participants, &status); err != nil {
			return err
		}
		if status != "confirmed" && !(status == "checked_in" && !actor.Customer) {
			return uerr("change_closed")
		}
		if oldSlot == newSlotID {
			return nil
		}
		if actor.Customer {
			v, err := GetBookingByID(ctx, tx, bookingID)
			if err != nil {
				return err
			}
			if err := CanCustomerChange(set, v, "reschedule"); err != nil {
				return err
			}
		}
		// Lock both slots in id order to avoid deadlocks between opposite moves.
		first, second := oldSlot, newSlotID
		if second < first {
			first, second = second, first
		}
		if _, err := tx.ExecContext(ctx, `SELECT id FROM slots WHERE id IN ($1,$2) ORDER BY id FOR UPDATE`, first, second); err != nil {
			return err
		}
		ns, err := GetSlot(ctx, tx, newSlotID, false)
		if err != nil {
			return uerr("slot_closed")
		}
		if ns.Status != "open" && !(overCapacity && !actor.Customer) {
			return uerr("slot_closed")
		}
		if ns.Remaining() < participants && !(overCapacity && !actor.Customer) {
			return uerr("slot_full")
		}
		if actor.Customer {
			if ns.StartsAt.Before(Now().Add(time.Duration(set.Int("min_lead_minutes")) * time.Minute)) {
				return uerr("too_soon")
			}
			p, err := GetProduct(ctx, tx, productID)
			if err != nil || !p.FitsSlot(ns) {
				return uerr("product")
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE bookings SET slot_id=$2, reschedule_count=reschedule_count+1, reminder_sent_at=NULL, updated_at=now() WHERE id=$1`, bookingID, newSlotID); err != nil {
			return err
		}
		who := "customer"
		if !actor.Customer {
			who = "staff"
		}
		Audit(ctx, tx, actor.StaffID, who, "booking.reschedule", "booking", bookingID, map[string]any{"from_slot": oldSlot, "to_slot": newSlotID, "to": ns.StartsAt.Format(time.RFC3339)}, actor.IP)
		return a.enqueueBookingNotice(ctx, tx, bookingID, "booking_rescheduled")
	})
}

// CheckIn marks a confirmed booking as arrived; pay-at-venue bookings record the venue payment.
func (a *App) CheckIn(ctx context.Context, bookingID int64, staffID int64, venueMethod string, ip string) error {
	return a.inTx(ctx, func(tx *sql.Tx) error {
		var status, payState string
		var total int64
		if err := tx.QueryRowContext(ctx, `SELECT status, payment_state, total FROM bookings WHERE id=$1 FOR UPDATE`, bookingID).Scan(&status, &payState, &total); err != nil {
			return err
		}
		if status == "checked_in" {
			return nil
		}
		if status != "confirmed" {
			return uerr("change_closed")
		}
		if _, err := tx.ExecContext(ctx, `UPDATE bookings SET status='checked_in', checked_in_at=now(), updated_at=now() WHERE id=$1`, bookingID); err != nil {
			return err
		}
		if payState == "due_at_venue" && venueMethod != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE bookings SET payment_state='paid_at_venue' WHERE id=$1`, bookingID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO payments(booking_id,provider,method,status,amount,paid_at) VALUES($1,'venue',$2,'paid',$3,now())`, bookingID, venueMethod, total); err != nil {
				return err
			}
		}
		Audit(ctx, tx, staffID, "", "booking.checkin", "booking", bookingID, map[string]any{"venue_payment": venueMethod}, ip)
		return nil
	})
}

func (a *App) UndoCheckIn(ctx context.Context, bookingID, staffID int64, ip string) error {
	return a.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE bookings SET status='confirmed', checked_in_at=NULL, updated_at=now() WHERE id=$1 AND status='checked_in'`, bookingID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return uerr("change_closed")
		}
		Audit(ctx, tx, staffID, "", "booking.checkin_undo", "booking", bookingID, nil, ip)
		return nil
	})
}

func (a *App) MarkNoShow(ctx context.Context, bookingID, staffID int64, ip string) error {
	return a.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE bookings SET status='no_show', updated_at=now() WHERE id=$1 AND status='confirmed'`, bookingID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return uerr("change_closed")
		}
		Audit(ctx, tx, staffID, "", "booking.no_show", "booking", bookingID, nil, ip)
		return nil
	})
}
