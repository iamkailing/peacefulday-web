package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"strings"
	"time"
)

// SeedBase inserts the minimum catalogue on an empty database. It never overwrites anything
// the owner has edited: each block only runs when its table is empty.
//
// What is seeded and why:
//   - one bookable experience (ice bath session, 60 min) and the sauna as "coming soon";
//   - sessions every hour 09:00–20:00, closed Thursdays, 5 places each — taken from the shop's
//     current booking sheet. All of it is editable under Admin → ตารางเปิดรอบ;
//   - the single-session ticket at ฿590 (reference price from the brief), active;
//   - the shop's earlier Happy Duo / Group / Family vouchers (2, 5, 10 sessions) as INACTIVE drafts,
//     so nothing unapproved is ever sold;
//   - an internal complimentary pass (used for staff-issued free sessions and credit refunds).
//
// No reviews, testimonials or promotions are seeded.
func SeedBase(ctx context.Context, db *sql.DB, log *slog.Logger) error {
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM experiences`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var iceID int64
	if err := tx.QueryRowContext(ctx, `INSERT INTO experiences(slug,name_th,name_en,summary_th,summary_en,duration_min,status,sort) VALUES
		('ice-bath','รอบ Ice Bath & พักผ่อน','Ice Bath & Rest Session',
		 'แช่น้ำเย็นในบรรยากาศสงบ แล้วพักหายใจช้า ๆ ให้ร่างกายและใจได้พักจริง ๆ',
		 'A cold plunge in a calm space, then slow breathing and rest — a proper pause for body and mind.',
		 60,'active',1) RETURNING id`).Scan(&iceID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO experiences(slug,name_th,name_en,summary_th,summary_en,duration_min,status,sort) VALUES
		('sauna','ซาวน่า','Sauna','กำลังจะเปิดให้บริการ','Coming soon',60,'coming_soon',2)`); err != nil {
		return err
	}
	for wd := 0; wd <= 6; wd++ {
		if wd == 4 { // Thursday: closed in the shop's current sheet
			continue
		}
		for h := 9; h <= 20; h++ {
			if _, err := tx.ExecContext(ctx, `INSERT INTO schedule_templates(experience_id,weekday,start_time,duration_min,capacity) VALUES($1,$2,$3,60,5)`,
				iceID, wd, fmt.Sprintf("%02d:00", h)); err != nil {
				return err
			}
		}
	}
	type prod struct {
		kind, cat, nth, nen, dth, den string
		price                         int64
		minP, maxP, credits, validity int
		public, active                bool
		sort                          int
	}
	for _, p := range []prod{
		{"ticket", "single", "รอบเดี่ยว", "Single session", "1 ท่าน ต่อ 1 รอบ (60 นาที)", "One person, one 60-minute session", 59000, 1, 5, 0, 0, true, true, 1},
		// The shop's earlier "Happy Voucher" promotion (sold until 5 Jul 2026, valid 6 months). Seeded as
		// INACTIVE drafts so the owner can re-open them with one click once approved again.
		{"pass", "promotion", "Happy Duo · 2 ครั้ง (ร่าง)", "Happy Duo · 2 sessions (draft)", "ร่างจากโปรเดิม (ขายถึง 5 ก.ค. 2026) ตรวจราคาและเงื่อนไขก่อนเปิดขาย", "Draft based on the earlier promotion. Confirm price and terms before selling.", 90000, 1, 1, 2, 180, true, false, 10},
		{"pass", "promotion", "Happy Group · 5 ครั้ง (ร่าง)", "Happy Group · 5 sessions (draft)", "ร่างจากโปรเดิม (ขายถึง 5 ก.ค. 2026) ตรวจราคาและเงื่อนไขก่อนเปิดขาย", "Draft based on the earlier promotion. Confirm price and terms before selling.", 210000, 1, 1, 5, 180, true, false, 11},
		{"pass", "promotion", "Happy Family · 10 ครั้ง (ร่าง)", "Happy Family · 10 sessions (draft)", "ร่างจากโปรเดิม (ขายถึง 5 ก.ค. 2026) ตรวจราคาและเงื่อนไขก่อนเปิดขาย", "Draft based on the earlier promotion. Confirm price and terms before selling.", 390000, 1, 1, 10, 180, true, false, 12},
		{"pass", "complimentary", "สิทธิ์ใช้บริการ (ฟรี/ชดเชย)", "Complimentary credit", "ออกโดยพนักงาน หรือคืนเป็นเครดิตเมื่อยกเลิก", "Issued by staff or as a cancellation credit", 0, 1, 1, 1, 180, false, true, 99},
	} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO products(kind,category,name_th,name_en,desc_th,desc_en,price,min_participants,max_participants,credits,validity_days,public,active,sort,experience_id)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, p.kind, p.cat, p.nth, p.nen, p.dth, p.den, p.price, p.minP, p.maxP, p.credits, p.validity, p.public, p.active, p.sort, iceID); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	log.Info("seeded base catalogue (1 experience, weekly schedule, single ticket; Happy vouchers as inactive drafts)")
	return nil
}

// EnsureOwner creates the first owner account. In production it requires BOOTSTRAP_OWNER_EMAIL and
// BOOTSTRAP_OWNER_PASSWORD; in development it falls back to a clearly-labelled demo login.
func EnsureOwner(ctx context.Context, db *sql.DB, cfg Config, log *slog.Logger) error {
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM staff_users WHERE role='owner' AND active`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	email, pw, name := NormEmail(cfg.BootstrapOwnerEmail), cfg.BootstrapOwnerPassword, "Owner"
	if email == "" || pw == "" {
		if cfg.Env != "development" {
			log.Warn("no owner account yet: set BOOTSTRAP_OWNER_EMAIL and BOOTSTRAP_OWNER_PASSWORD and restart")
			return nil
		}
		email, pw, name = "owner@demo.local", "peacefulday-demo", "Demo Owner (DEMO)"
		log.Warn("DEVELOPMENT ONLY: created demo owner login", "email", email, "password", pw)
	}
	if !ValidEmail(email) {
		return fmt.Errorf("BOOTSTRAP_OWNER_EMAIL is not a valid email")
	}
	if len(pw) < 10 {
		return errors.New("BOOTSTRAP_OWNER_PASSWORD must be at least 10 characters")
	}
	h, err := HashPassword(pw)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO staff_users(email,name,password_hash,role) VALUES($1,$2,$3,'owner')
		ON CONFLICT (email) DO UPDATE SET role='owner', active=TRUE, password_hash=EXCLUDED.password_hash`, email, name, h)
	if err == nil {
		log.Info("owner account ready", "email", email)
	}
	return err
}

// ResetStaffPassword is the break-glass recovery used from the command line.
func ResetStaffPassword(ctx context.Context, db *sql.DB, email, pw string) error {
	if len(pw) < 10 {
		return errors.New("password must be at least 10 characters")
	}
	h, err := HashPassword(pw)
	if err != nil {
		return err
	}
	res, err := db.ExecContext(ctx, `UPDATE staff_users SET password_hash=$2, failed_logins=0, locked_until=NULL, active=TRUE WHERE email=$1`, NormEmail(email), h)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("no staff user with email %s", email)
	}
	db.ExecContext(ctx, `DELETE FROM staff_sessions WHERE user_id=(SELECT id FROM staff_users WHERE email=$1)`, NormEmail(email))
	Audit(ctx, db, 0, "cli", "staff.password_reset_cli", "staff", email, nil, "")
	return nil
}

// ---------- demo data (development only) ----------

const demoNote = "ข้อมูลตัวอย่าง (DEMO) — ลบได้ด้วยคำสั่ง -purge-demo"

// HasDemoData reports whether demo rows exist (the admin shows a banner when they do).
func HasDemoData(ctx context.Context, db *sql.DB) bool {
	var ok bool
	db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM customers WHERE 'demo' = ANY(tags))`).Scan(&ok)
	return ok
}

// SeedDemo fills ~75 days of realistic-looking but fake activity so dashboards can be explored.
// Every row is tagged: customers.tags ∋ 'demo', bookings.staff_note = 'DEMO', visitor ids 'demo-…',
// ad spend notes 'DEMO'. Refuses to run in production.
func SeedDemo(ctx context.Context, db *sql.DB, cfg Config, log *slog.Logger) error {
	if cfg.Env == "production" {
		return errors.New("refusing to create demo data when APP_ENV=production (use a staging site)")
	}
	if HasDemoData(ctx, db) {
		return errors.New("demo data already present (run -purge-demo first)")
	}
	rng := rand.New(rand.NewSource(62))
	today := DateOnly(Now())
	from, to := today.AddDate(0, 0, -60), today.AddDate(0, 0, 14)
	if err := generateSlots(ctx, db, from, to); err != nil {
		return err
	}
	var ticketID int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM products WHERE kind='ticket' AND active ORDER BY sort, id LIMIT 1`).Scan(&ticketID); err != nil {
		return fmt.Errorf("no active ticket product: %w", err)
	}
	var price int64
	db.QueryRowContext(ctx, `SELECT price FROM products WHERE id=$1`, ticketID).Scan(&price)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// demo staff, one per role
	h, _ := HashPassword("peacefulday-demo")
	for _, s := range [][3]string{{"manager@demo.local", "Demo Manager (DEMO)", "manager"}, {"frontdesk@demo.local", "Demo Front Desk (DEMO)", "front_desk"},
		{"marketing@demo.local", "Demo Marketing (DEMO)", "marketing"}, {"finance@demo.local", "Demo Finance (DEMO)", "finance"}} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO staff_users(email,name,password_hash,role) VALUES($1,$2,$3,$4) ON CONFLICT (email) DO NOTHING`, s[0], s[1], h, s[2]); err != nil {
			return err
		}
	}
	first := []string{"สมชาย", "สุดา", "ณัฐ", "พิมพ์", "ธนา", "กานต์", "อร", "ภูมิ", "มายด์", "ต้น", "Emma", "Liam", "Yuki", "Noah", "Mia", "Ken"}
	var custIDs []int64
	for i := 1; i <= 90; i++ {
		lang := "th"
		if i%5 == 0 {
			lang = "en"
		}
		name := fmt.Sprintf("%s (DEMO %02d)", first[i%len(first)], i)
		var id int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO customers(name,phone,email,lang,marketing_consent,consent_at,notes,tags,created_at)
			VALUES($1,$2,$3,$4,$5,CASE WHEN $5 THEN now() END,$6,'{demo}',now()) RETURNING id`,
			name, fmt.Sprintf("09900%05d", i), fmt.Sprintf("demo%02d@demo.local", i), lang, i%3 == 0, demoNote).Scan(&id); err != nil {
			return err
		}
		custIDs = append(custIDs, id)
	}
	type src struct{ s, m, c string }
	sources := []src{{"direct", "", ""}, {"direct", "", ""}, {"google", "organic", ""}, {"instagram", "social", ""}, {"facebook", "cpc", "demo-autumn"}, {"line", "social", ""}, {"google", "cpc", "demo-search"}}
	channels := []string{"web", "web", "web", "web", "web", "web", "web", "phone", "walk_in", "admin"}
	rows, err := tx.QueryContext(ctx, `SELECT id, starts_at, capacity FROM slots WHERE status='open' AND starts_at >= $1 AND starts_at < $2 ORDER BY starts_at`, from, to.AddDate(0, 0, 1))
	if err != nil {
		return err
	}
	type sl struct {
		id  int64
		at  time.Time
		cap int
	}
	var slots []sl
	for rows.Next() {
		var s sl
		rows.Scan(&s.id, &s.at, &s.cap)
		slots = append(slots, s)
	}
	rows.Close()
	now := time.Now()
	nBookings := 0
	for _, s := range slots {
		at := s.at.In(BKK)
		// busier on weekends and evenings; growing over time
		load := 0.25 + 0.15*float64(at.Sub(from).Hours()/24/75)
		if wd := at.Weekday(); wd == time.Saturday || wd == time.Sunday {
			load += 0.25
		}
		if at.Hour() >= 17 {
			load += 0.2
		} else if at.Hour() <= 10 {
			load += 0.05
		}
		if at.After(now) {
			load *= 0.8 * (1 - float64(at.Sub(now).Hours())/24/20)
		}
		target := int(float64(s.cap)*load + rng.Float64()*1.5)
		if target > s.cap {
			target = s.cap
		}
		used := 0
		for used < target {
			p := 1 + rng.Intn(3)
			if used+p > s.cap {
				p = s.cap - used
			}
			used += p
			cust := custIDs[int(float64(len(custIDs))*rng.Float64()*rng.Float64())] // skewed: some regulars
			lead := time.Duration(rng.Intn(7*24)+1) * time.Hour
			created := s.at.Add(-lead)
			if created.After(now) {
				created = now.Add(-time.Duration(rng.Intn(10*24)+1) * time.Hour)
			}
			ch := channels[rng.Intn(len(channels))]
			so := sources[rng.Intn(len(sources))]
			if ch != "web" {
				so = src{}
			}
			status := "confirmed"
			var checkedIn, cancelledAt any
			if s.at.Before(now) {
				switch x := rng.Float64(); {
				case x < 0.84:
					status, checkedIn = "checked_in", s.at.Add(-5*time.Minute)
				case x < 0.90:
					status = "no_show"
				default:
					status, cancelledAt = "cancelled", created.Add(time.Duration(rng.Intn(20)+1)*time.Hour)
				}
			} else if rng.Float64() < 0.06 {
				status, cancelledAt = "cancelled", created.Add(2*time.Hour)
			}
			venue := ch != "web" && rng.Float64() < 0.7
			payMethod, payState := "online", "paid"
			if venue {
				payMethod, payState = "pay_at_venue", "due_at_venue"
				if status == "checked_in" {
					payState = "paid_at_venue"
				}
			}
			total := price * int64(p)
			if status == "cancelled" && payState == "paid" {
				payState = "refunded"
			}
			var bid int64
			err := tx.QueryRowContext(ctx, `INSERT INTO bookings(ref,customer_id,slot_id,product_id,participants,status,subtotal,total,pay_method,payment_state,manage_token_hash,channel,
				checked_in_at,cancelled_at,cancel_reason,staff_note,utm_source,utm_medium,utm_campaign,visitor_id,conversion_sent,created_at,updated_at)
				VALUES($1,$2,$3,$4,$5,$6,$7,$7,$8,$9,$10,$11,$12,$13,$14,'DEMO',$15,$16,$17,$18,TRUE,$19,$19) RETURNING id`,
				"PD-D"+RandomCode(5), cust, s.id, ticketID, p, status, total, payMethod, payState, HashToken(RandomToken()), ch,
				checkedIn, cancelledAt, map[bool]string{true: "DEMO: ลูกค้ายกเลิก", false: ""}[status == "cancelled"], so.s, so.m, so.c,
				fmt.Sprintf("demo-%d", cust), created).Scan(&bid)
			if err != nil {
				return err
			}
			nBookings++
			switch {
			case payMethod == "online":
				method := "promptpay"
				if rng.Float64() < 0.35 {
					method = "card"
				}
				var payID int64
				if err := tx.QueryRowContext(ctx, `INSERT INTO payments(booking_id,provider,provider_session,provider_payment,method,status,amount,paid_at,created_at)
					VALUES($1,'simulated',$2,$3,$4,$5,$6,$7,$7) RETURNING id`, bid, "demo_cs_"+RandomCode(12), "demo_pi_"+RandomCode(12), method,
					map[bool]string{true: "refunded", false: "paid"}[payState == "refunded"], total, created.Add(3*time.Minute)).Scan(&payID); err != nil {
					return err
				}
				if payState == "refunded" {
					if _, err := tx.ExecContext(ctx, `UPDATE payments SET refunded_amount=amount WHERE id=$1`, payID); err != nil {
						return err
					}
					if _, err := tx.ExecContext(ctx, `INSERT INTO refunds(payment_id,amount,status,reason,idempotency_key,created_at) VALUES($1,$2,'succeeded','DEMO',$3,$4)`,
						payID, total, "demo-"+RandomCode(12), cancelledAt); err != nil {
						return err
					}
				}
			case payState == "paid_at_venue":
				m := []string{"cash", "transfer", "card_terminal"}[rng.Intn(3)]
				if _, err := tx.ExecContext(ctx, `INSERT INTO payments(booking_id,provider,method,status,amount,paid_at,created_at) VALUES($1,'venue',$2,'paid',$3,$4,$4)`,
					bid, m, total, s.at.Add(-5*time.Minute)); err != nil {
					return err
				}
			}
			// funnel events for web bookings
			if ch == "web" {
				vid := fmt.Sprintf("demo-%d", cust)
				for i, ev := range []string{"page_view", "booking_start", "checkout_start", "purchase"} {
					if _, err := tx.ExecContext(ctx, `INSERT INTO web_events(visitor_id,type,path,utm_source,utm_medium,utm_campaign,value,created_at) VALUES($1,$2,'/book',$3,$4,$5,$6,$7)`,
						vid, ev, so.s, so.m, so.c, map[bool]int64{true: total, false: 0}[ev == "purchase"], created.Add(time.Duration(i*2-6)*time.Minute)); err != nil {
						return err
					}
				}
			}
		}
	}
	// browsing visitors who did not book, and a few abandoned checkouts
	for d := from; !d.After(today); d = d.AddDate(0, 0, 1) {
		visitors := 25 + rng.Intn(30)
		for v := 0; v < visitors; v++ {
			so := sources[rng.Intn(len(sources))]
			at := d.Add(time.Duration(8+rng.Intn(14))*time.Hour + time.Duration(rng.Intn(60))*time.Minute)
			vid := fmt.Sprintf("demo-v%s-%d", d.Format("0102"), v)
			if _, err := tx.ExecContext(ctx, `INSERT INTO web_events(visitor_id,type,path,utm_source,utm_medium,utm_campaign,created_at) VALUES($1,'page_view','/',$2,$3,$4,$5)`,
				vid, so.s, so.m, so.c, at); err != nil {
				return err
			}
			if rng.Float64() < 0.22 {
				tx.ExecContext(ctx, `INSERT INTO web_events(visitor_id,type,path,utm_source,utm_medium,utm_campaign,created_at) VALUES($1,'booking_start','/book',$2,$3,$4,$5)`, vid, so.s, so.m, so.c, at.Add(time.Minute))
			}
		}
		if d.Before(today) && rng.Float64() < 0.5 {
			var slotID int64
			if tx.QueryRowContext(ctx, `SELECT id FROM slots WHERE starts_at >= $1 AND starts_at < $2 ORDER BY random() LIMIT 1`, d.AddDate(0, 0, 1), d.AddDate(0, 0, 3)).Scan(&slotID) == nil {
				cust := custIDs[rng.Intn(len(custIDs))]
				if _, err := tx.ExecContext(ctx, `INSERT INTO bookings(ref,customer_id,slot_id,product_id,participants,status,subtotal,total,pay_method,payment_state,manage_token_hash,staff_note,visitor_id,created_at,updated_at)
					VALUES($1,$2,$3,$4,2,'expired',$5,$5,'online','expired',$6,'DEMO',$7,$8,$8)`, "PD-D"+RandomCode(5), cust, slotID, ticketID, price*2, HashToken(RandomToken()), fmt.Sprintf("demo-%d", cust), d.Add(14*time.Hour)); err != nil {
					return err
				}
			}
		}
	}
	for d := today.AddDate(0, 0, -30); !d.After(today); d = d.AddDate(0, 0, 1) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO ad_spend(spend_date,source,campaign,amount,note) VALUES($1,'facebook','demo-autumn',$2,'DEMO')`, d.Format("2006-01-02"), int64(30000+rng.Intn(25000))); err != nil {
			return err
		}
		if d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
			tx.ExecContext(ctx, `INSERT INTO ad_spend(spend_date,source,campaign,amount,note) VALUES($1,'google','demo-search',$2,'DEMO')`, d.Format("2006-01-02"), int64(20000+rng.Intn(20000)))
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO campaigns(name,utm_campaign,starts_on,notes) VALUES('ตัวอย่าง: โฆษณา Facebook (DEMO)','demo-autumn',$1,'DEMO'),('ตัวอย่าง: Google Search (DEMO)','demo-search',$1,'DEMO') ON CONFLICT DO NOTHING`, today.AddDate(0, 0, -30).Format("2006-01-02")); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	log.Info("demo data created", "bookings", nBookings, "customers", len(custIDs), "note", "all rows tagged DEMO; remove with -purge-demo")
	return nil
}

// PurgeDemo deletes every demo row created by SeedDemo (and demo staff logins).
func PurgeDemo(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmts := []string{
		`CREATE TEMP TABLE demo_c ON COMMIT DROP AS SELECT id FROM customers WHERE 'demo' = ANY(tags)`,
		`CREATE TEMP TABLE demo_b ON COMMIT DROP AS SELECT id FROM bookings WHERE customer_id IN (SELECT id FROM demo_c)`,
		`CREATE TEMP TABLE demo_o ON COMMIT DROP AS SELECT id FROM pass_orders WHERE customer_id IN (SELECT id FROM demo_c)`,
		`DELETE FROM notifications WHERE booking_id IN (SELECT id FROM demo_b)`,
		`DELETE FROM promo_redemptions WHERE booking_id IN (SELECT id FROM demo_b)`,
		`DELETE FROM pass_redemptions WHERE booking_id IN (SELECT id FROM demo_b)`,
		`DELETE FROM refunds WHERE payment_id IN (SELECT id FROM payments WHERE booking_id IN (SELECT id FROM demo_b) OR pass_order_id IN (SELECT id FROM demo_o))`,
		`DELETE FROM payments WHERE booking_id IN (SELECT id FROM demo_b) OR pass_order_id IN (SELECT id FROM demo_o)`,
		`DELETE FROM pass_orders WHERE id IN (SELECT id FROM demo_o)`,
		`UPDATE bookings SET pass_id=NULL WHERE pass_id IN (SELECT id FROM passes WHERE customer_id IN (SELECT id FROM demo_c))`,
		`DELETE FROM pass_redemptions WHERE pass_id IN (SELECT id FROM passes WHERE customer_id IN (SELECT id FROM demo_c))`,
		`DELETE FROM passes WHERE customer_id IN (SELECT id FROM demo_c)`,
		`DELETE FROM bookings WHERE id IN (SELECT id FROM demo_b)`,
		`UPDATE promo_codes SET referrer_customer_id=NULL WHERE referrer_customer_id IN (SELECT id FROM demo_c)`,
		`DELETE FROM customer_logins WHERE customer_id IN (SELECT id FROM demo_c)`,
		`DELETE FROM customers WHERE id IN (SELECT id FROM demo_c)`,
		`DELETE FROM web_events WHERE visitor_id LIKE 'demo-%'`,
		`DELETE FROM ad_spend WHERE note='DEMO'`,
		`DELETE FROM campaigns WHERE notes='DEMO'`,
		`UPDATE audit_log SET staff_id=NULL, actor=COALESCE(NULLIF(actor,''),'demo staff') WHERE staff_id IN (SELECT id FROM staff_users WHERE email LIKE '%@demo.local' AND role <> 'owner')`,
		`UPDATE bookings SET created_by=NULL WHERE created_by IN (SELECT id FROM staff_users WHERE email LIKE '%@demo.local' AND role <> 'owner')`,
		`UPDATE passes SET issued_by=NULL WHERE issued_by IN (SELECT id FROM staff_users WHERE email LIKE '%@demo.local' AND role <> 'owner')`,
		`UPDATE refunds SET staff_id=NULL WHERE staff_id IN (SELECT id FROM staff_users WHERE email LIKE '%@demo.local' AND role <> 'owner')`,
		`DELETE FROM staff_users WHERE email LIKE '%@demo.local' AND role <> 'owner'`,
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("%s: %w", strings.SplitN(s, " WHERE", 2)[0], err)
		}
	}
	return tx.Commit()
}
