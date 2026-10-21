package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ---------- provider abstraction ----------

type CheckoutRequest struct {
	Reference      string // our booking/order ref, echoed back in events
	Description    string
	Amount         int64 // satang
	CustomerEmail  string
	Locale         string
	SuccessURL     string
	CancelURL      string
	ExpiresAt      time.Time
	IdempotencyKey string
}

type CheckoutSession struct {
	ID  string
	URL string
}

// PaymentEvent is a provider event normalised to what the booking engine needs.
type PaymentEvent struct {
	Provider       string
	EventID        string
	Type           string // paid | failed | expired | refunded | ignored
	SessionID      string
	PaymentRef     string // payment intent / charge
	Method         string // card | promptpay
	Amount         int64
	Currency       string
	RefundedAmount int64
	ReceiptURL     string
	Raw            []byte
}

type PaymentProvider interface {
	Name() string
	CreateCheckout(ctx context.Context, r CheckoutRequest) (CheckoutSession, error)
	ExpireCheckout(ctx context.Context, sessionID string) error
	Refund(ctx context.Context, paymentRef string, amount int64, idemKey string) (string, error)
	// ParseWebhook authenticates and decodes an incoming webhook.
	ParseWebhook(r *http.Request, body []byte) (PaymentEvent, error)
}

var ErrBadSignature = errors.New("webhook signature invalid")

// ---------- Stripe (Checkout Sessions, card + PromptPay) ----------

type StripeProvider struct {
	SecretKey     string
	WebhookSecret string
	APIBase       string
	HTTP          *http.Client
}

func (s *StripeProvider) Name() string { return "stripe" }

func (s *StripeProvider) call(ctx context.Context, method, path string, form url.Values, idem string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, method, s.APIBase+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(s.SecretKey, "")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Stripe-Version", "2024-06-20")
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	if resp.StatusCode >= 300 {
		msg := string(raw)
		if e, ok := out["error"].(map[string]any); ok {
			msg = fmt.Sprint(e["message"])
		}
		return out, fmt.Errorf("stripe %s %s: %d %s", method, path, resp.StatusCode, msg)
	}
	return out, nil
}

func (s *StripeProvider) CreateCheckout(ctx context.Context, r CheckoutRequest) (CheckoutSession, error) {
	f := url.Values{}
	f.Set("mode", "payment")
	f.Set("line_items[0][quantity]", "1")
	f.Set("line_items[0][price_data][currency]", "thb")
	f.Set("line_items[0][price_data][unit_amount]", strconv.FormatInt(r.Amount, 10))
	f.Set("line_items[0][price_data][product_data][name]", r.Description)
	f.Add("payment_method_types[]", "card")
	f.Add("payment_method_types[]", "promptpay")
	f.Set("client_reference_id", r.Reference)
	f.Set("metadata[ref]", r.Reference)
	f.Set("payment_intent_data[metadata][ref]", r.Reference)
	f.Set("success_url", r.SuccessURL)
	f.Set("cancel_url", r.CancelURL)
	if r.CustomerEmail != "" {
		f.Set("customer_email", r.CustomerEmail)
	}
	if r.Locale == "th" || r.Locale == "en" {
		f.Set("locale", r.Locale)
	}
	// Stripe requires expires_at at least 30 minutes out.
	exp := r.ExpiresAt
	if min := time.Now().Add(31 * time.Minute); exp.Before(min) {
		exp = min
	}
	f.Set("expires_at", strconv.FormatInt(exp.Unix(), 10))
	out, err := s.call(ctx, "POST", "/v1/checkout/sessions", f, r.IdempotencyKey)
	if err != nil {
		return CheckoutSession{}, err
	}
	id, _ := out["id"].(string)
	u, _ := out["url"].(string)
	if id == "" || u == "" {
		return CheckoutSession{}, errors.New("stripe: checkout session missing id/url")
	}
	return CheckoutSession{ID: id, URL: u}, nil
}

func (s *StripeProvider) ExpireCheckout(ctx context.Context, id string) error {
	_, err := s.call(ctx, "POST", "/v1/checkout/sessions/"+url.PathEscape(id)+"/expire", url.Values{}, "expire-"+id)
	if err != nil && strings.Contains(err.Error(), "status") {
		return nil // already complete/expired
	}
	return err
}

func (s *StripeProvider) Refund(ctx context.Context, paymentRef string, amount int64, idem string) (string, error) {
	f := url.Values{}
	f.Set("payment_intent", paymentRef)
	f.Set("amount", strconv.FormatInt(amount, 10))
	out, err := s.call(ctx, "POST", "/v1/refunds", f, idem)
	if err != nil {
		return "", err
	}
	id, _ := out["id"].(string)
	if st, _ := out["status"].(string); st == "failed" || st == "canceled" {
		return id, fmt.Errorf("stripe refund %s", st)
	}
	return id, nil
}

// VerifyStripeSignature checks the Stripe-Signature header (t=..., v1=...) with a 5 minute tolerance.
func VerifyStripeSignature(header string, body []byte, secret string, now time.Time) error {
	var ts string
	var sigs []string
	for _, part := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			ts = v
		case "v1":
			sigs = append(sigs, v)
		}
	}
	if ts == "" || len(sigs) == 0 {
		return ErrBadSignature
	}
	t, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return ErrBadSignature
	}
	if d := now.Sub(time.Unix(t, 0)); d > 5*time.Minute || d < -5*time.Minute {
		return ErrBadSignature
	}
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(ts + "."))
	m.Write(body)
	want := hex.EncodeToString(m.Sum(nil))
	for _, s := range sigs {
		if hmac.Equal([]byte(s), []byte(want)) {
			return nil
		}
	}
	return ErrBadSignature
}

func (s *StripeProvider) ParseWebhook(r *http.Request, body []byte) (PaymentEvent, error) {
	if err := VerifyStripeSignature(r.Header.Get("Stripe-Signature"), body, s.WebhookSecret, time.Now()); err != nil {
		return PaymentEvent{}, err
	}
	var ev struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Data struct {
			Object json.RawMessage `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &ev); err != nil {
		return PaymentEvent{}, err
	}
	out := PaymentEvent{Provider: "stripe", EventID: ev.ID, Raw: body, Type: "ignored"}
	switch ev.Type {
	case "checkout.session.completed", "checkout.session.async_payment_succeeded", "checkout.session.async_payment_failed", "checkout.session.expired":
		var cs struct {
			ID            string   `json:"id"`
			PaymentStatus string   `json:"payment_status"`
			AmountTotal   int64    `json:"amount_total"`
			Currency      string   `json:"currency"`
			PaymentIntent any      `json:"payment_intent"`
			PMTypes       []string `json:"payment_method_types"`
		}
		if err := json.Unmarshal(ev.Data.Object, &cs); err != nil {
			return out, err
		}
		out.SessionID, out.Amount, out.Currency = cs.ID, cs.AmountTotal, cs.Currency
		switch pi := cs.PaymentIntent.(type) {
		case string:
			out.PaymentRef = pi
		case map[string]any:
			out.PaymentRef, _ = pi["id"].(string)
		}
		switch {
		case ev.Type == "checkout.session.expired":
			out.Type = "expired"
		case ev.Type == "checkout.session.async_payment_failed":
			out.Type = "failed"
		case cs.PaymentStatus == "paid":
			out.Type = "paid"
		default:
			out.Type = "ignored" // completed but unpaid (async PromptPay): wait for async_payment_succeeded
		}
	case "charge.succeeded":
		var ch struct {
			PaymentIntent        string `json:"payment_intent"`
			ReceiptURL           string `json:"receipt_url"`
			PaymentMethodDetails struct {
				Type string `json:"type"`
			} `json:"payment_method_details"`
		}
		if err := json.Unmarshal(ev.Data.Object, &ch); err != nil {
			return out, err
		}
		out.Type, out.PaymentRef, out.ReceiptURL, out.Method = "charge_info", ch.PaymentIntent, ch.ReceiptURL, ch.PaymentMethodDetails.Type
	case "charge.refunded":
		var ch struct {
			PaymentIntent  string `json:"payment_intent"`
			AmountRefunded int64  `json:"amount_refunded"`
		}
		if err := json.Unmarshal(ev.Data.Object, &ch); err != nil {
			return out, err
		}
		out.Type, out.PaymentRef, out.RefundedAmount = "refunded", ch.PaymentIntent, ch.AmountRefunded
	}
	return out, nil
}

// ---------- Simulated provider (development & demos only) ----------

// SimulatedProvider sends customers to our own /pay/sim page. Events it produces are HMAC-signed and
// travel through exactly the same processing code as real webhooks.
type SimulatedProvider struct {
	BaseURL string
	Secret  string
}

func (s *SimulatedProvider) Name() string { return "simulated" }

func (s *SimulatedProvider) CreateCheckout(ctx context.Context, r CheckoutRequest) (CheckoutSession, error) {
	id := "sim_" + RandomCode(14)
	return CheckoutSession{ID: id, URL: s.BaseURL + "/pay/sim/" + id}, nil
}

func (s *SimulatedProvider) ExpireCheckout(ctx context.Context, id string) error { return nil }

func (s *SimulatedProvider) Refund(ctx context.Context, paymentRef string, amount int64, idem string) (string, error) {
	return "simre_" + RandomCode(10), nil
}

func (s *SimulatedProvider) SignEvent(body []byte) string { return Sign(s.Secret, "sim:"+string(body)) }

func (s *SimulatedProvider) ParseWebhook(r *http.Request, body []byte) (PaymentEvent, error) {
	if !ConstEq(r.Header.Get("X-Sim-Signature"), s.SignEvent(body)) {
		return PaymentEvent{}, ErrBadSignature
	}
	var ev PaymentEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return ev, err
	}
	ev.Provider, ev.Raw = "simulated", body
	return ev, nil
}

// ---------- starting a checkout ----------

// StartBookingCheckout creates (or reuses) the provider checkout for a booking on hold.
// accessQS is the query string that lets the customer back into their booking/voucher page:
// the one-time token when we still have it, otherwise the signed link.
func (a *App) accessQS(ref, token string) string {
	if token != "" {
		return "t=" + url.QueryEscape(token)
	}
	return "s=" + a.refSig(ref)
}

func (a *App) StartBookingCheckout(ctx context.Context, bookingID int64, lang Lang, manageToken string) (string, error) {
	v, err := GetBookingByID(ctx, a.DB, bookingID)
	if err != nil {
		return "", err
	}
	if v.Status != "hold" || v.Total <= 0 {
		return "", errors.New("booking is not awaiting payment")
	}
	var existing string
	err = a.DB.QueryRowContext(ctx, `SELECT checkout_url FROM payments WHERE booking_id=$1 AND status='pending' AND checkout_url<>'' ORDER BY id DESC LIMIT 1`, bookingID).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	// A new attempt (e.g. after a failed PromptPay payment) needs a fresh provider idempotency key.
	var attempt int
	a.DB.QueryRowContext(ctx, `SELECT count(*) FROM payments WHERE booking_id=$1`, bookingID).Scan(&attempt)
	manage := a.Cfg.BaseURL + lang.Prefix() + "/booking/" + v.Ref + "?" + a.accessQS(v.Ref, manageToken)
	desc := fmt.Sprintf("Peaceful Day · %s · %s %s · %d", lang.Pick(v.ProdNameTH, v.ProdNameEN), v.StartsAt.In(BKK).Format("2 Jan 2006"), HM(v.StartsAt), v.Participants)
	sess, err := a.Pay.CreateCheckout(ctx, CheckoutRequest{
		Reference: v.Ref, Description: desc, Amount: v.Total, CustomerEmail: v.Email, Locale: string(lang),
		SuccessURL: manage + "&paid=1", CancelURL: manage + "&cancelled=1",
		ExpiresAt: v.HoldExpiresAt.Time, IdempotencyKey: fmt.Sprintf("checkout-%s-%d", v.Ref, attempt),
	})
	if err != nil {
		return "", err
	}
	if _, err := a.DB.ExecContext(ctx, `INSERT INTO payments(booking_id,provider,provider_session,status,amount,checkout_url) VALUES($1,$2,$3,'pending',$4,$5)
		ON CONFLICT (provider_session) DO NOTHING`, bookingID, a.Pay.Name(), sess.ID, v.Total, sess.URL); err != nil {
		return "", err
	}
	return sess.URL, nil
}

func (a *App) StartPassCheckout(ctx context.Context, orderID int64, lang Lang, manageToken string) (string, error) {
	var ref, email, nameTH, nameEN string
	var amount int64
	var created time.Time
	err := a.DB.QueryRowContext(ctx, `SELECT o.ref, o.amount, o.created_at, c.email, p.name_th, p.name_en FROM pass_orders o
		JOIN customers c ON c.id=o.customer_id JOIN passes ps ON ps.id=o.pass_id JOIN products p ON p.id=ps.product_id WHERE o.id=$1 AND o.status='pending'`, orderID).
		Scan(&ref, &amount, &created, &email, &nameTH, &nameEN)
	if err != nil {
		return "", err
	}
	var attempt int
	a.DB.QueryRowContext(ctx, `SELECT count(*) FROM payments WHERE pass_order_id=$1`, orderID).Scan(&attempt)
	manage := a.Cfg.BaseURL + lang.Prefix() + "/voucher/" + ref + "?" + a.accessQS(ref, manageToken)
	sess, err := a.Pay.CreateCheckout(ctx, CheckoutRequest{Reference: ref, Description: "Peaceful Day · " + lang.Pick(nameTH, nameEN), Amount: amount,
		CustomerEmail: email, Locale: string(lang), SuccessURL: manage + "&paid=1", CancelURL: manage + "&cancelled=1",
		ExpiresAt: created.Add(60 * time.Minute), IdempotencyKey: fmt.Sprintf("checkout-%s-%d", ref, attempt)})
	if err != nil {
		return "", err
	}
	if _, err := a.DB.ExecContext(ctx, `INSERT INTO payments(pass_order_id,provider,provider_session,status,amount,checkout_url) VALUES($1,$2,$3,'pending',$4,$5)
		ON CONFLICT (provider_session) DO NOTHING`, orderID, a.Pay.Name(), sess.ID, amount, sess.URL); err != nil {
		return "", err
	}
	return sess.URL, nil
}

// ---------- processing events (idempotent) ----------

// ProcessPaymentEvent records the event once and applies its effect. Replays of the same event id are no-ops.
func (a *App) ProcessPaymentEvent(ctx context.Context, ev PaymentEvent) error {
	if ev.EventID == "" {
		return errors.New("event without id")
	}
	raw := ev.Raw
	if !json.Valid(raw) {
		raw, _ = json.Marshal(map[string]any{"type": ev.Type})
	}
	res, err := a.DB.ExecContext(ctx, `INSERT INTO payment_events(provider,event_id,type,payload) VALUES($1,$2,$3,$4) ON CONFLICT (provider,event_id) DO NOTHING`,
		ev.Provider, ev.EventID, ev.Type, raw)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var processed sql.NullTime
		_ = a.DB.QueryRowContext(ctx, `SELECT processed_at FROM payment_events WHERE provider=$1 AND event_id=$2`, ev.Provider, ev.EventID).Scan(&processed)
		if processed.Valid {
			return nil // already handled
		}
	}
	perr := a.applyPaymentEvent(ctx, ev)
	msg := ""
	if perr != nil {
		msg = perr.Error()
	}
	_, _ = a.DB.ExecContext(ctx, `UPDATE payment_events SET processed_at = CASE WHEN $3='' THEN now() ELSE NULL END, error=$3 WHERE provider=$1 AND event_id=$2`, ev.Provider, ev.EventID, msg)
	return perr
}

func (a *App) applyPaymentEvent(ctx context.Context, ev PaymentEvent) error {
	switch ev.Type {
	case "paid":
		return a.markPaid(ctx, ev)
	case "failed", "expired":
		return a.markUnpaid(ctx, ev)
	case "charge_info":
		_, err := a.DB.ExecContext(ctx, `UPDATE payments SET receipt_url=COALESCE(NULLIF($2,''),receipt_url), method=COALESCE(NULLIF($3,''),method), updated_at=now() WHERE provider_payment=$1`,
			ev.PaymentRef, ev.ReceiptURL, ev.Method)
		return err
	case "refunded":
		return a.syncRefund(ctx, ev)
	}
	return nil
}

func (a *App) markPaid(ctx context.Context, ev PaymentEvent) error {
	var lateBookingID int64
	var lateMsg string
	err := a.inTx(ctx, func(tx *sql.Tx) error {
		var payID int64
		var bookingID, orderID sql.NullInt64
		var amount int64
		var status string
		err := tx.QueryRowContext(ctx, `SELECT id, booking_id, pass_order_id, amount, status FROM payments WHERE provider=$1 AND provider_session=$2 FOR UPDATE`,
			ev.Provider, ev.SessionID).Scan(&payID, &bookingID, &orderID, &amount, &status)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("payment for session %s not found", ev.SessionID)
		} else if err != nil {
			return err
		}
		if status == "paid" || status == "refunded" || status == "partially_refunded" {
			return nil
		}
		if ev.Amount != amount || (ev.Currency != "" && !strings.EqualFold(ev.Currency, "thb")) {
			return fmt.Errorf("amount mismatch for session %s: expected %d thb, got %d %s", ev.SessionID, amount, ev.Amount, ev.Currency)
		}
		method := ev.Method
		if _, err := tx.ExecContext(ctx, `UPDATE payments SET status='paid', provider_payment=NULLIF($2,''), method=COALESCE(NULLIF($3,''),method), paid_at=now(), updated_at=now() WHERE id=$1`,
			payID, ev.PaymentRef, method); err != nil {
			return err
		}
		if ev.PaymentRef != "" {
			// charge.succeeded can arrive before checkout.session.completed; pick up its receipt link now.
			if _, err := tx.ExecContext(ctx, `UPDATE payments p SET receipt_url = COALESCE(e.payload->'data'->'object'->>'receipt_url', ''),
				method = COALESCE(NULLIF(p.method,''), e.payload->'data'->'object'->'payment_method_details'->>'type', '')
				FROM (SELECT payload FROM payment_events WHERE provider=$2 AND type='charge_info' AND payload->'data'->'object'->>'payment_intent'=$3 ORDER BY id DESC LIMIT 1) e
				WHERE p.id=$1 AND p.receipt_url=''`, payID, ev.Provider, ev.PaymentRef); err != nil {
				return err
			}
		}
		if orderID.Valid {
			return a.activatePassOrder(ctx, tx, orderID.Int64)
		}
		bid := bookingID.Int64
		var bstatus string
		var slotID int64
		var participants int
		if err := tx.QueryRowContext(ctx, `SELECT status, slot_id, participants FROM bookings WHERE id=$1 FOR UPDATE`, bid).Scan(&bstatus, &slotID, &participants); err != nil {
			return err
		}
		switch bstatus {
		case "hold":
			if _, err := tx.ExecContext(ctx, `UPDATE bookings SET status='confirmed', payment_state='paid', hold_expires_at=NULL, updated_at=now() WHERE id=$1`, bid); err != nil {
				return err
			}
			return a.enqueueBookingNotice(ctx, tx, bid, "booking_confirmed")
		case "expired", "cancelled":
			// Paid after the hold ran out. Re-take the spot if it is still free; otherwise refund.
			slot, err := GetSlot(ctx, tx, slotID, true)
			if err != nil {
				return err
			}
			if bstatus == "expired" && slot.Status == "open" && slot.Remaining() >= participants {
				if _, err := tx.ExecContext(ctx, `UPDATE bookings SET status='confirmed', payment_state='paid', hold_expires_at=NULL,
					needs_attention='Paid after hold expired; spot re-confirmed. Check voucher/promo was re-applied if used.', updated_at=now() WHERE id=$1`, bid); err != nil {
					return err
				}
				return a.enqueueBookingNotice(ctx, tx, bid, "booking_confirmed")
			}
			if _, err := tx.ExecContext(ctx, `UPDATE bookings SET payment_state='paid', updated_at=now() WHERE id=$1`, bid); err != nil {
				return err
			}
			lateBookingID, lateMsg = bid, "Payment arrived after the booking was "+bstatus+" and the session has no space. Automatic refund started."
			return nil
		default:
			_, err := tx.ExecContext(ctx, `UPDATE bookings SET payment_state='paid', updated_at=now() WHERE id=$1`, bid)
			return err
		}
	})
	if err != nil {
		return err
	}
	if lateBookingID > 0 {
		var payID, amount int64
		if a.DB.QueryRowContext(ctx, `SELECT id, amount FROM payments WHERE provider=$1 AND provider_session=$2`, ev.Provider, ev.SessionID).Scan(&payID, &amount) == nil {
			if _, rerr := a.RefundPayment(ctx, payID, amount, "late payment, no space", 0, "late-"+ev.SessionID); rerr != nil {
				lateMsg += " Refund failed: " + rerr.Error()
			}
		}
		a.flag(ctx, lateBookingID, lateMsg)
	}
	return nil
}

func (a *App) markUnpaid(ctx context.Context, ev PaymentEvent) error {
	return a.inTx(ctx, func(tx *sql.Tx) error {
		var payID int64
		var bookingID, orderID sql.NullInt64
		var status string
		err := tx.QueryRowContext(ctx, `SELECT id, booking_id, pass_order_id, status FROM payments WHERE provider=$1 AND provider_session=$2 FOR UPDATE`,
			ev.Provider, ev.SessionID).Scan(&payID, &bookingID, &orderID, &status)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		if status != "pending" {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE payments SET status=$2, updated_at=now() WHERE id=$1`, payID, ev.Type); err != nil {
			return err
		}
		if orderID.Valid {
			_, err := tx.ExecContext(ctx, `UPDATE pass_orders SET status=$2 WHERE id=$1 AND status='pending'`, orderID.Int64, ev.Type)
			return err
		}
		// A failed attempt leaves the hold in place so the customer can retry until it expires.
		if ev.Type == "failed" {
			_, err := tx.ExecContext(ctx, `UPDATE bookings SET payment_state='failed', updated_at=now() WHERE id=$1 AND status='hold'`, bookingID.Int64)
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE bookings SET status='expired', payment_state='expired', updated_at=now() WHERE id=$1 AND status='hold'`, bookingID.Int64)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			return reverseRedemptions(ctx, tx, bookingID.Int64)
		}
		return nil
	})
}

func (a *App) syncRefund(ctx context.Context, ev PaymentEvent) error {
	return a.inTx(ctx, func(tx *sql.Tx) error {
		var payID, amount int64
		var bookingID sql.NullInt64
		err := tx.QueryRowContext(ctx, `SELECT id, amount, booking_id FROM payments WHERE provider_payment=$1 FOR UPDATE`, ev.PaymentRef).Scan(&payID, &amount, &bookingID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		st := "partially_refunded"
		if ev.RefundedAmount >= amount {
			st = "refunded"
		}
		// A refund made directly in the provider dashboard has no row yet: record the difference so
		// revenue reports (which subtract refunds by date) stay correct. Pending rows count as known,
		// so a webhook racing our own RefundPayment call never double-records.
		var known int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(amount),0) FROM refunds WHERE payment_id=$1 AND status IN ('pending','succeeded')`, payID).Scan(&known); err != nil {
			return err
		}
		if extra := ev.RefundedAmount - known; extra > 0 {
			if _, err := tx.ExecContext(ctx, `INSERT INTO refunds(payment_id,amount,status,reason,idempotency_key) VALUES($1,$2,'succeeded','refund made in payment provider dashboard',$3)
				ON CONFLICT (idempotency_key) DO NOTHING`, payID, extra, "ext:"+ev.Provider+":"+ev.EventID); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE payments SET refunded_amount=LEAST(amount, GREATEST(refunded_amount,$2)), status=$3, updated_at=now() WHERE id=$1`, payID, ev.RefundedAmount, st); err != nil {
			return err
		}
		if bookingID.Valid {
			_, err = tx.ExecContext(ctx, `UPDATE bookings SET payment_state=$2, updated_at=now() WHERE id=$1`, bookingID.Int64, st)
		}
		return err
	})
}

// RefundPayment asks the provider to refund and records the outcome. The idempotency key makes retries safe.
func (a *App) RefundPayment(ctx context.Context, paymentID, amount int64, reason string, staffID int64, idem string) (int64, error) {
	var provider, ref, status string
	var paid, refunded int64
	var bookingID sql.NullInt64
	if err := a.DB.QueryRowContext(ctx, `SELECT provider, COALESCE(provider_payment,''), status, amount, refunded_amount, booking_id FROM payments WHERE id=$1`, paymentID).
		Scan(&provider, &ref, &status, &paid, &refunded, &bookingID); err != nil {
		return 0, err
	}
	if status != "paid" && status != "partially_refunded" {
		return 0, fmt.Errorf("payment is %s, not refundable", status)
	}
	if provider == "venue" {
		return 0, errors.New("venue payments are refunded at the counter, not online")
	}
	if amount <= 0 || amount > paid-refunded {
		return 0, fmt.Errorf("refund amount must be between 1 and %s", Baht(paid-refunded))
	}
	if ref == "" {
		return 0, errors.New("payment has no provider reference yet")
	}
	var refundRowID int64
	var staff any
	if staffID > 0 {
		staff = staffID
	}
	err := a.DB.QueryRowContext(ctx, `INSERT INTO refunds(payment_id,amount,status,reason,idempotency_key,staff_id) VALUES($1,$2,'pending',$3,$4,$5)
		ON CONFLICT (idempotency_key) DO NOTHING RETURNING id`, paymentID, amount, clip(reason, 300), idem, staff).Scan(&refundRowID)
	if errors.Is(err, sql.ErrNoRows) {
		var st string
		a.DB.QueryRowContext(ctx, `SELECT status FROM refunds WHERE idempotency_key=$1`, idem).Scan(&st)
		if st == "succeeded" {
			return amount, nil
		}
		// earlier attempt failed or is pending: retry with the same provider idempotency key below
		a.DB.QueryRowContext(ctx, `SELECT id FROM refunds WHERE idempotency_key=$1`, idem).Scan(&refundRowID)
	} else if err != nil {
		return 0, err
	}
	pid, perr := a.Pay.Refund(ctx, ref, amount, "refund-"+idem)
	if perr != nil {
		a.DB.ExecContext(ctx, `UPDATE refunds SET status='failed', error=$2 WHERE id=$1`, refundRowID, clip(perr.Error(), 500))
		return 0, perr
	}
	err = a.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE refunds SET status='succeeded', provider_refund_id=$2, error='' WHERE id=$1`, refundRowID, pid); err != nil {
			return err
		}
		var newRefunded int64
		// Recompute from the ledger rather than incrementing, so a charge.refunded webhook that
		// arrived first (and already raised refunded_amount) is not counted twice.
		if err := tx.QueryRowContext(ctx, `UPDATE payments SET refunded_amount = LEAST(amount, GREATEST(refunded_amount,
				(SELECT COALESCE(SUM(amount),0) FROM refunds WHERE payment_id=$1 AND status='succeeded'))), updated_at=now()
			WHERE id=$1 RETURNING refunded_amount`, paymentID).Scan(&newRefunded); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE payments SET status = CASE WHEN refunded_amount >= amount THEN 'refunded' ELSE 'partially_refunded' END WHERE id=$1`, paymentID); err != nil {
			return err
		}
		st := "partially_refunded"
		if newRefunded >= paid {
			st = "refunded"
		}
		if bookingID.Valid {
			if _, err := tx.ExecContext(ctx, `UPDATE bookings SET payment_state=$2, updated_at=now() WHERE id=$1`, bookingID.Int64, st); err != nil {
				return err
			}
		} else {
			tx.ExecContext(ctx, `UPDATE pass_orders o SET status='refunded' FROM payments p WHERE p.id=$1 AND o.id=p.pass_order_id AND $2='refunded'`, paymentID, st)
		}
		Audit(ctx, tx, staffID, "system", "payment.refund", "payment", paymentID, map[string]any{"amount": amount, "reason": reason}, "")
		return nil
	})
	return amount, err
}

// ---------- pass orders ----------

func (a *App) activatePassOrder(ctx context.Context, tx *sql.Tx, orderID int64) error {
	var passID int64
	var validity int
	if err := tx.QueryRowContext(ctx, `SELECT o.pass_id, p.validity_days FROM pass_orders o JOIN passes ps ON ps.id=o.pass_id JOIN products p ON p.id=ps.product_id
		WHERE o.id=$1 FOR UPDATE OF o`, orderID).Scan(&passID, &validity); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE pass_orders SET status='paid' WHERE id=$1`, orderID); err != nil {
		return err
	}
	var exp any
	if validity > 0 {
		exp = Now().AddDate(0, 0, validity)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE passes SET status='active', expires_at=$2 WHERE id=$1 AND status='pending'`, passID, exp); err != nil {
		return err
	}
	return a.enqueuePassNotice(ctx, tx, orderID)
}

type PassOrderRequest struct {
	ProductID                                    int64
	Name, Phone, Email, Lang                     string
	Consent                                      bool
	GiftTo, GiftMessage                          string
	IdempotencyKey                               string
	UTMSource, UTMMedium, UTMCampaign, VisitorID string
}

func (a *App) CreatePassOrder(ctx context.Context, r PassOrderRequest) (orderID int64, ref, token string, err error) {
	r.Name, r.Phone, r.Email = clip(r.Name, 120), NormPhone(r.Phone), NormEmail(r.Email)
	if r.Name == "" {
		return 0, "", "", uerr("name")
	}
	if !ValidPhone(r.Phone) {
		return 0, "", "", uerr("phone")
	}
	if !ValidEmail(r.Email) {
		return 0, "", "", uerr("email")
	}
	if r.IdempotencyKey != "" {
		if a.DB.QueryRowContext(ctx, `SELECT id, ref FROM pass_orders WHERE idempotency_key=$1`, r.IdempotencyKey).Scan(&orderID, &ref) == nil {
			return orderID, ref, "", nil
		}
	}
	p, err := GetProduct(ctx, a.DB, r.ProductID)
	if err != nil || p.Kind != "pass" || !p.Public || !p.SellableNow() || p.Price <= 0 {
		return 0, "", "", uerr("product")
	}
	token = RandomToken()
	err = a.inTx(ctx, func(tx *sql.Tx) error {
		custID, err := UpsertCustomer(ctx, tx, r.Name, r.Phone, r.Email, r.Lang, r.Consent)
		if err != nil {
			return err
		}
		var passID int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO passes(code,product_id,customer_id,credits_total,status,gift_to_name,gift_message) VALUES($1,$2,$3,$4,'pending',$5,$6) RETURNING id`,
			NewPassCode(), p.ID, custID, p.Credits, clip(r.GiftTo, 120), clip(r.GiftMessage, 500)).Scan(&passID); err != nil {
			return err
		}
		ref = "PDV-" + RandomCode(6)
		var idem any
		if r.IdempotencyKey != "" {
			idem = r.IdempotencyKey
		}
		return tx.QueryRowContext(ctx, `INSERT INTO pass_orders(ref,pass_id,customer_id,amount,manage_token_hash,idempotency_key,utm_source,utm_medium,utm_campaign,visitor_id)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id`, ref, passID, custID, p.Price, HashToken(token), idem,
			clip(r.UTMSource, 100), clip(r.UTMMedium, 100), clip(r.UTMCampaign, 150), clip(r.VisitorID, 64)).Scan(&orderID)
	})
	return orderID, ref, token, err
}
