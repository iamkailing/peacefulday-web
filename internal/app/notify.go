package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"mime"
	"net"
	"net/http"
	"net/smtp"
	"strings"
	"time"
)

// Notifications use an outbox table: rows are written in the same transaction as the booking change,
// then a worker delivers them with retries and backoff. Nothing is lost if the process restarts.

func (a *App) enqueue(ctx context.Context, q querier, kind, channel, to, subject, body string, payload any, bookingID int64, dedupe string) error {
	if strings.TrimSpace(to) == "" {
		return nil
	}
	var raw []byte
	if payload != nil {
		raw, _ = json.Marshal(payload)
	}
	var dd any
	if dedupe != "" {
		dd = dedupe
	}
	_, err := q.ExecContext(ctx, `INSERT INTO notifications(kind,channel,recipient,subject,body,payload,booking_id,dedupe_key)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (dedupe_key) DO NOTHING`,
		kind, channel, to, subject, body, nullJSON(raw), nullID(bookingID), dd)
	return err
}

func (a *App) bookingLink(v BookingView) string {
	// Emails carry a fresh manage link: rotating the token keeps old links from lingering forever is not needed;
	// we sign a stable link instead so the stored hash stays authoritative.
	return fmt.Sprintf("%s%s/booking/%s?s=%s", a.Cfg.BaseURL, Lang(v.CustLang).Prefix(), v.Ref, a.refSig(v.Ref))
}

// refSig is an HMAC of the ref, used in links we send by email (manage access without the one-time token).
func (a *App) refSig(ref string) string { return Sign(a.Cfg.SessionSecret, "manage:"+ref)[:22] }

func (a *App) enqueueBookingNotice(ctx context.Context, q querier, bookingID int64, kind string) error {
	v, err := GetBookingByID(ctx, q, bookingID)
	if err != nil {
		return err
	}
	l := Lang(v.CustLang)
	when := l.DateLong(v.StartsAt) + " · " + HM(v.StartsAt)
	link := a.bookingLink(v)
	var subject, lead string
	switch kind {
	case "booking_confirmed":
		subject = l.T("conf.h") + " · " + v.Ref
		lead = map[Lang]string{TH: "ขอบคุณที่จองกับ Peaceful Day ค่ะ แล้วพบกันนะคะ", EN: "Thank you for booking with Peaceful Day. See you soon."}[l]
	case "booking_cancelled":
		subject = l.T("conf.cancelled") + " · " + v.Ref
		var credit string
		q.QueryRowContext(ctx, `SELECT code FROM passes WHERE note=$1`, creditNote(bookingID)).Scan(&credit)
		switch {
		case credit != "":
			lead = map[Lang]string{
				TH: "การจองนี้ถูกยกเลิกแล้ว ยอดที่ชำระคืนเป็นเครดิตสำหรับจองครั้งต่อไป รหัสเครดิตของคุณ: " + credit + " (ใช้ได้ 6 เดือน ใส่ในช่อง “รหัสบัตร/แพ็กเกจ” ตอนจอง)",
				EN: "This booking has been cancelled. What you paid is returned as credit for your next booking. Your credit code: " + credit + " (valid for 6 months; enter it in the “Voucher or package code” box when you book).",
			}[l]
		case v.PaymentState == "paid" || v.PaymentState == "partially_refunded" || v.PaymentState == "refunded":
			lead = map[Lang]string{TH: "การจองนี้ถูกยกเลิกแล้ว ยอดที่ชำระออนไลน์จะคืนเข้าช่องทางเดิม ระยะเวลาขึ้นอยู่กับธนาคารหรือผู้ออกบัตร", EN: "This booking has been cancelled. Your online payment will be refunded to the original payment method; timing depends on your bank or card issuer."}[l]
		default:
			lead = map[Lang]string{TH: "การจองนี้ถูกยกเลิกแล้ว", EN: "This booking has been cancelled."}[l]
		}
	case "booking_rescheduled":
		subject = l.T("conf.moved") + " · " + v.Ref
		lead = map[Lang]string{TH: "เวลาใหม่ของคุณอยู่ด้านล่างค่ะ", EN: "Your new time is below."}[l]
	case "booking_reminder":
		subject = map[Lang]string{TH: "ใกล้ถึงรอบของคุณแล้ว · ", EN: "See you soon · "}[l] + v.Ref
		lead = map[Lang]string{TH: "เตือนความจำการจองของคุณ แนะนำมาก่อนเวลา 10–15 นาที เตรียมชุดที่เปียกได้มาด้วยนะคะ", EN: "A reminder of your booking. Please arrive 10–15 minutes early with clothes that can get wet."}[l]
	}
	lines := []string{
		lead, "",
		l.T("conf.ref") + ": " + v.Ref,
		l.Pick(v.ExpNameTH, v.ExpNameEN) + " · " + l.Pick(v.ProdNameTH, v.ProdNameEN),
		when,
		l.T("book.people") + ": " + fmt.Sprint(v.Participants),
	}
	if v.Total > 0 {
		lines = append(lines, l.T("book.total")+": "+Baht(v.Total)+" ("+l.T("pay."+v.PaymentState)+")")
	}
	lines = append(lines, "", link)
	body := strings.Join(lines, "\n")
	dedupe := fmt.Sprintf("%s:%d:%d", kind, bookingID, time.Now().UnixNano())
	if kind == "booking_confirmed" {
		dedupe = fmt.Sprintf("%s:%d", kind, bookingID)
	} else if kind == "booking_reminder" {
		// keyed by session start so a rescheduled booking gets a reminder for its new time
		dedupe = fmt.Sprintf("%s:%d:%d", kind, bookingID, v.StartsAt.Unix())
	}
	if v.Email != "" {
		if err := a.enqueue(ctx, q, kind, "email", v.Email, subject, body, nil, bookingID, dedupe); err != nil {
			return err
		}
	}
	if a.Cfg.AdminAlertEmail != "" && kind != "booking_reminder" {
		admin := fmt.Sprintf("[%s] %s · %s · %s · %d pax · %s", kind, v.Ref, v.CustomerName, when, v.Participants, Baht(v.Total))
		if err := a.enqueue(ctx, q, kind, "admin_email", a.Cfg.AdminAlertEmail, admin, admin, nil, bookingID, "admin-"+dedupe); err != nil {
			return err
		}
	}
	var adsConsent bool
	q.QueryRowContext(ctx, `SELECT ads_consent FROM bookings WHERE id=$1`, bookingID).Scan(&adsConsent)
	if kind == "booking_confirmed" && a.Cfg.MetaAccessToken != "" && adsConsent {
		set, _ := LoadSettings(ctx, a.DB)
		if pixel := set.Str("meta_pixel_id"); pixel != "" && v.Total > 0 {
			// Same event_id as the browser Pixel "Purchase" so Meta de-duplicates the two.
			payload := map[string]any{"pixel": pixel, "event_id": v.Ref, "value": float64(v.Total) / 100, "email": v.Email, "phone": v.Phone, "time": time.Now().Unix()}
			if err := a.enqueue(ctx, q, "capi_purchase", "meta_capi", pixel, "Purchase", "", payload, bookingID, "capi:"+v.Ref); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *App) enqueuePassNotice(ctx context.Context, q querier, orderID int64) error {
	var ref, email, lang, code, nameTH, nameEN, giftTo string
	var credits int
	var exp sql.NullTime
	if err := q.QueryRowContext(ctx, `SELECT o.ref, c.email, c.lang, ps.code, p.name_th, p.name_en, ps.credits_total, ps.expires_at, ps.gift_to_name
		FROM pass_orders o JOIN customers c ON c.id=o.customer_id JOIN passes ps ON ps.id=o.pass_id JOIN products p ON p.id=ps.product_id WHERE o.id=$1`, orderID).
		Scan(&ref, &email, &lang, &code, &nameTH, &nameEN, &credits, &exp, &giftTo); err != nil {
		return err
	}
	l := Lang(lang)
	lines := []string{l.T("pass.done"), "", l.T("pass.code") + ": " + code, l.Pick(nameTH, nameEN) + " · " + l.T("price.sessions", credits)}
	if exp.Valid {
		lines = append(lines, l.T("my.until", l.DateLong(exp.Time)))
	}
	if giftTo != "" {
		lines = append(lines, l.T("pass.giftto")+": "+giftTo)
	}
	lines = append(lines, "", l.T("pass.howuse"), a.Cfg.BaseURL+l.Prefix()+"/book")
	body := strings.Join(lines, "\n")
	if err := a.enqueue(ctx, q, "pass_purchased", "email", email, l.T("pass.done")+" · "+ref, body, nil, 0, "pass:"+ref); err != nil {
		return err
	}
	return a.enqueue(ctx, q, "pass_purchased", "admin_email", a.Cfg.AdminAlertEmail, "[pass] "+ref+" "+code, body, nil, 0, "admin-pass:"+ref)
}

// AdminAlert queues an alert email to the configured admin address (no-op when unset).
func (a *App) AdminAlert(ctx context.Context, subject, body string, bookingID int64) {
	if err := a.enqueue(ctx, a.DB, "admin_alert", "admin_email", a.Cfg.AdminAlertEmail, "[Peaceful Day] "+subject, body, nil, bookingID,
		fmt.Sprintf("alert:%d:%s:%d", bookingID, subject, time.Now().Unix()/600)); err != nil {
		a.Log.Error("admin alert enqueue failed", "err", err)
	}
}

// ---------- delivery worker ----------

type sender interface {
	Send(ctx context.Context, n outboxRow) (skipped bool, err error)
}

type outboxRow struct {
	ID        int64
	Kind      string
	Channel   string
	Recipient string
	Subject   string
	Body      string
	Payload   []byte
	Attempts  int
}

// DeliverNotifications sends due rows. Each row is claimed with SKIP LOCKED so multiple instances can run.
func (a *App) DeliverNotifications(ctx context.Context) (int, error) {
	sent := 0
	for i := 0; i < 50; i++ {
		var n outboxRow
		var claimed bool
		err := a.inTx(ctx, func(tx *sql.Tx) error {
			err := tx.QueryRowContext(ctx, `SELECT id, kind, channel, recipient, subject, body, COALESCE(payload::text,''), attempts FROM notifications
				WHERE status='queued' AND next_attempt_at <= now() ORDER BY id LIMIT 1 FOR UPDATE SKIP LOCKED`).
				Scan(&n.ID, &n.Kind, &n.Channel, &n.Recipient, &n.Subject, &n.Body, &n.Payload, &n.Attempts)
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			} else if err != nil {
				return err
			}
			claimed = true
			// push next attempt out while we send so a crash mid-send doesn't hot-loop
			_, err = tx.ExecContext(ctx, `UPDATE notifications SET attempts=attempts+1, next_attempt_at=now()+interval '5 minutes' WHERE id=$1`, n.ID)
			return err
		})
		if err != nil {
			return sent, err
		}
		if !claimed {
			return sent, nil
		}
		skipped, serr := a.sendOne(ctx, n)
		switch {
		case serr == nil && skipped:
			a.DB.ExecContext(ctx, `UPDATE notifications SET status='skipped', last_error='no provider configured' WHERE id=$1`, n.ID)
		case serr == nil:
			sent++
			a.DB.ExecContext(ctx, `UPDATE notifications SET status='sent', sent_at=now(), last_error='' WHERE id=$1`, n.ID)
		default:
			attempts := n.Attempts + 1
			if attempts >= 6 {
				a.DB.ExecContext(ctx, `UPDATE notifications SET status='failed', last_error=$2 WHERE id=$1`, n.ID, clip(serr.Error(), 500))
			} else {
				backoff := time.Duration(1<<attempts) * time.Minute // 2,4,8,16,32 min
				a.DB.ExecContext(ctx, `UPDATE notifications SET last_error=$2, next_attempt_at=now()+$3::interval WHERE id=$1`, n.ID, clip(serr.Error(), 500), fmt.Sprintf("%d seconds", int(backoff.Seconds())))
			}
			a.Log.Warn("notification failed", "id", n.ID, "channel", n.Channel, "err", serr)
		}
	}
	return sent, nil
}

func (a *App) sendOne(ctx context.Context, n outboxRow) (bool, error) {
	switch n.Channel {
	case "email", "admin_email":
		if a.Cfg.SMTPHost == "" || a.Cfg.SMTPFrom == "" {
			return true, nil
		}
		return false, a.sendEmail(n.Recipient, n.Subject, n.Body)
	case "meta_capi":
		if a.Cfg.MetaAccessToken == "" {
			return true, nil
		}
		return false, a.sendMetaCAPI(ctx, n.Payload)
	case "line":
		// Reserved for LINE Messaging API push messages (needs the customer's LINE userId from a LINE Login flow).
		return true, nil
	}
	return true, nil
}

func (a *App) sendEmail(to, subject, body string) error {
	from := a.Cfg.SMTPFrom
	envelopeFrom := from
	if i := strings.LastIndex(from, "<"); i >= 0 {
		envelopeFrom = strings.Trim(from[i:], "<> ")
	}
	domain := "localhost"
	if i := strings.LastIndex(envelopeFrom, "@"); i >= 0 {
		domain = envelopeFrom[i+1:]
	}
	var msg bytes.Buffer
	boundary := "pd" + RandomCode(16)
	fmt.Fprintf(&msg, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <%s@%s>\r\nMIME-Version: 1.0\r\n",
		from, to, mime.QEncoding.Encode("utf-8", subject), time.Now().Format(time.RFC1123Z), RandomToken(), domain)
	fmt.Fprintf(&msg, "Content-Type: multipart/alternative; boundary=%s\r\n\r\n", boundary)
	part := func(ctype, content string) {
		fmt.Fprintf(&msg, "--%s\r\nContent-Type: %s; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n", boundary, ctype)
		enc := base64.StdEncoding.EncodeToString([]byte(content))
		for len(enc) > 76 {
			msg.WriteString(enc[:76] + "\r\n")
			enc = enc[76:]
		}
		msg.WriteString(enc + "\r\n")
	}
	part("text/plain", body)
	part("text/html", emailHTML(subject, body))
	fmt.Fprintf(&msg, "--%s--\r\n", boundary)

	addr := net.JoinHostPort(a.Cfg.SMTPHost, a.Cfg.SMTPPort)
	conn, err := net.DialTimeout("tcp", addr, 15*time.Second)
	if err != nil {
		return err
	}
	conn.SetDeadline(time.Now().Add(45 * time.Second))
	var c *smtp.Client
	if a.Cfg.SMTPPort == "465" { // implicit TLS
		tc := tls.Client(conn, &tls.Config{ServerName: a.Cfg.SMTPHost})
		if c, err = smtp.NewClient(tc, a.Cfg.SMTPHost); err != nil {
			conn.Close()
			return err
		}
	} else {
		if c, err = smtp.NewClient(conn, a.Cfg.SMTPHost); err != nil {
			conn.Close()
			return err
		}
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: a.Cfg.SMTPHost}); err != nil {
				c.Close()
				return err
			}
		}
	}
	defer c.Close()
	if a.Cfg.SMTPUser != "" {
		if err := c.Auth(smtp.PlainAuth("", a.Cfg.SMTPUser, a.Cfg.SMTPPass, a.Cfg.SMTPHost)); err != nil {
			return err
		}
	}
	if err := c.Mail(envelopeFrom); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	wc, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := wc.Write(msg.Bytes()); err != nil {
		return err
	}
	if err := wc.Close(); err != nil {
		return err
	}
	return c.Quit()
}

func emailHTML(subject, body string) string {
	var b strings.Builder
	b.WriteString(`<div style="font-family:system-ui,sans-serif;max-width:520px;margin:auto;padding:24px;background:#fffdf6;color:#23301f;border-radius:16px">`)
	b.WriteString(`<div style="font-size:13px;letter-spacing:.08em;color:#55724a">PEACEFUL DAY</div>`)
	b.WriteString(`<h1 style="font-size:22px;margin:8px 0 16px">` + html.EscapeString(subject) + `</h1>`)
	for _, line := range strings.Split(body, "\n") {
		esc := html.EscapeString(line)
		if strings.HasPrefix(line, "http") {
			esc = `<a href="` + esc + `" style="display:inline-block;background:#f4d64e;color:#23301f;padding:10px 16px;border-radius:999px;text-decoration:none;font-weight:600">` +
				map[bool]string{true: "ดู / จัดการการจอง · Manage", false: "Open"}[strings.Contains(line, "/booking/")] + `</a>`
		}
		b.WriteString(`<p style="margin:0 0 8px;line-height:1.55">` + esc + `</p>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}

func sha256hex(s string) string {
	h := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(s))))
	return hex.EncodeToString(h[:])
}

// sendMetaCAPI posts a Purchase event to Meta's Conversions API with hashed identifiers.
func (a *App) sendMetaCAPI(ctx context.Context, payload []byte) error {
	var p struct {
		Pixel   string  `json:"pixel"`
		EventID string  `json:"event_id"`
		Value   float64 `json:"value"`
		Email   string  `json:"email"`
		Phone   string  `json:"phone"`
		Time    int64   `json:"time"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return err
	}
	ud := map[string]any{}
	if p.Email != "" {
		ud["em"] = []string{sha256hex(p.Email)}
	}
	if p.Phone != "" {
		ph := strings.TrimPrefix(p.Phone, "+")
		if strings.HasPrefix(ph, "0") {
			ph = "66" + ph[1:]
		}
		ud["ph"] = []string{sha256hex(ph)}
	}
	body, _ := json.Marshal(map[string]any{"data": []map[string]any{{
		"event_name": "Purchase", "event_time": p.Time, "event_id": p.EventID, "action_source": "website",
		"user_data": ud, "custom_data": map[string]any{"currency": "THB", "value": p.Value},
	}}})
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://graph.facebook.com/v19.0/"+p.Pixel+"/events?access_token="+a.Cfg.MetaAccessToken, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("meta capi: status %d", resp.StatusCode)
	}
	return nil
}

// QueueReminders enqueues reminders for bookings starting within the configured window.
func (a *App) QueueReminders(ctx context.Context) error {
	set, _ := LoadSettings(ctx, a.DB)
	hours := set.Int("reminder_hours")
	if hours <= 0 {
		return nil
	}
	rows, err := a.DB.QueryContext(ctx, `SELECT b.id FROM bookings b JOIN slots s ON s.id=b.slot_id
		WHERE b.status='confirmed' AND b.reminder_sent_at IS NULL AND s.starts_at > now() AND s.starts_at <= now() + make_interval(hours => $1)
		AND b.created_at < now() - interval '2 hours' LIMIT 200`, hours)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		err := a.inTx(ctx, func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx, `UPDATE bookings SET reminder_sent_at=now() WHERE id=$1 AND reminder_sent_at IS NULL`, id)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return nil
			}
			return a.enqueueBookingNotice(ctx, tx, id, "booking_reminder")
		})
		if err != nil {
			return err
		}
	}
	return nil
}
