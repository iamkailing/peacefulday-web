package app

// Integration tests: they run against a real PostgreSQL database because the booking engine's
// safety comes from row locks and constraints. Set TEST_DATABASE_URL (default: local
// peacefulday_test). The schema is dropped and recreated at the start of the run.
//
//	go test ./internal/app -v

import (
	"bufio"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var testDB *sql.DB

func TestMain(m *testing.M) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://postgres@127.0.0.1:5432/peacefulday_test?sslmode=disable"
	}
	ctx := context.Background()
	db, err := OpenDB(ctx, dsn)
	if err != nil {
		fmt.Println("SKIP: no test database:", err)
		os.Exit(0)
	}
	if _, err := db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public;`); err != nil {
		fmt.Println("cannot reset test schema:", err)
		os.Exit(1)
	}
	if err := Migrate(ctx, db); err != nil {
		fmt.Println("migrate:", err)
		os.Exit(1)
	}
	if err := SeedBase(ctx, db, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		fmt.Println("seed:", err)
		os.Exit(1)
	}
	testDB = db
	os.Exit(m.Run())
}

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newTestApp(t *testing.T) *App {
	t.Helper()
	cfg := Config{Env: "development", SessionSecret: "test-secret-0123456789-abcdefghij", BaseURL: "http://test.local", PaymentProvider: "simulated", DisableWorkers: true}
	a, err := New(cfg, testDB, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	return a
}

var phoneSeq int64 = 100000

func uniquePhone() string { return fmt.Sprintf("08%08d", atomic.AddInt64(&phoneSeq, 1)) }

func ticketID(t *testing.T) int64 {
	t.Helper()
	var id int64
	if err := testDB.QueryRow(`SELECT id FROM products WHERE kind='ticket' AND active ORDER BY id LIMIT 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// newSlot creates an isolated open session `days` ahead with the given capacity.
var slotSeq int64

func newSlot(t *testing.T, capacity int) int64 {
	t.Helper()
	var exp int64
	if err := testDB.QueryRow(`SELECT id FROM experiences WHERE slug='ice-bath'`).Scan(&exp); err != nil {
		t.Fatal(err)
	}
	n := atomic.AddInt64(&slotSeq, 1)
	start := DateOnly(Now()).AddDate(0, 0, 3).Add(time.Duration(n) * time.Minute)
	var id int64
	if err := testDB.QueryRow(`INSERT INTO slots(experience_id,starts_at,ends_at,capacity) VALUES($1,$2,$3,$4) RETURNING id`,
		exp, start, start.Add(time.Hour), capacity).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func webBooking(slot int64, n int, phone string) BookingRequest {
	return BookingRequest{SlotID: slot, ProductID: 0, Participants: n, Name: "Test Guest", Phone: phone, Email: "g" + phone + "@example.com", Lang: "th", Channel: "web", IdempotencyKey: RandomToken()}
}

func book(t *testing.T, a *App, r BookingRequest) (BookingResult, error) {
	t.Helper()
	if r.ProductID == 0 {
		r.ProductID = ticketID(t)
	}
	return a.CreateBooking(context.Background(), r)
}

func slotUsage(t *testing.T, slot int64) int {
	t.Helper()
	s, err := GetSlot(context.Background(), testDB, slot, false)
	if err != nil {
		t.Fatal(err)
	}
	return s.Used()
}

func status(t *testing.T, id int64) (string, string) {
	t.Helper()
	var st, ps string
	if err := testDB.QueryRow(`SELECT status, payment_state FROM bookings WHERE id=$1`, id).Scan(&st, &ps); err != nil {
		t.Fatal(err)
	}
	return st, ps
}

func isUserErr(err error, code string) bool {
	var ue UserError
	return errors.As(err, &ue) && ue.Code == code
}

// ---------- booking engine ----------

func TestConcurrentCheckoutsNeverOverbook(t *testing.T) {
	a := newTestApp(t)
	slot := newSlot(t, 5)
	var ok, full int64
	var wg sync.WaitGroup
	for i := 0; i < 25; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := book(t, a, webBooking(slot, 1, uniquePhone()))
			switch {
			case err == nil:
				atomic.AddInt64(&ok, 1)
			case isUserErr(err, "slot_full"):
				atomic.AddInt64(&full, 1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	if ok != 5 || full != 20 {
		t.Fatalf("want 5 successes and 20 full, got %d ok / %d full", ok, full)
	}
	if u := slotUsage(t, slot); u != 5 {
		t.Fatalf("slot usage = %d, want 5", u)
	}
}

func TestGroupBookingRespectsRemainingCapacity(t *testing.T) {
	a := newTestApp(t)
	slot := newSlot(t, 5)
	if _, err := book(t, a, webBooking(slot, 3, uniquePhone())); err != nil {
		t.Fatal(err)
	}
	if _, err := book(t, a, webBooking(slot, 3, uniquePhone())); !isUserErr(err, "slot_full") {
		t.Fatalf("3 more guests in a 5-seat session with 3 used: want slot_full, got %v", err)
	}
	if _, err := book(t, a, webBooking(slot, 2, uniquePhone())); err != nil {
		t.Fatalf("2 guests should fit exactly: %v", err)
	}
}

func TestIdempotentRetryReturnsSameBooking(t *testing.T) {
	a := newTestApp(t)
	slot := newSlot(t, 5)
	req := webBooking(slot, 1, uniquePhone())
	r1, err := book(t, a, req)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := book(t, a, req)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Ref != r2.Ref || !r2.Replayed {
		t.Fatalf("retry created a second booking: %s vs %s (replayed=%v)", r1.Ref, r2.Ref, r2.Replayed)
	}
	if u := slotUsage(t, slot); u != 1 {
		t.Fatalf("usage %d after idempotent retry, want 1", u)
	}
}

func TestDuplicatePhoneInSameSessionRejected(t *testing.T) {
	a := newTestApp(t)
	slot := newSlot(t, 5)
	phone := uniquePhone()
	first, err := book(t, a, webBooking(slot, 1, phone))
	if err != nil {
		t.Fatal(err)
	}
	payBooking(t, a, first.BookingID)
	if _, err := book(t, a, webBooking(slot, 1, phone)); !isUserErr(err, "duplicate") {
		t.Fatalf("same phone again in a session it already paid for: want duplicate, got %v", err)
	}
	other := webBooking(slot, 1, uniquePhone())
	other.Email = "g" + phone + "@example.com" // same email, different phone
	if _, err := book(t, a, other); !isUserErr(err, "duplicate") {
		t.Fatalf("same email: want duplicate, got %v", err)
	}
}

func TestRetryAfterAbandonedHoldReleasesOldHold(t *testing.T) {
	a := newTestApp(t)
	slot := newSlot(t, 1)
	phone := uniquePhone()
	first, err := book(t, a, webBooking(slot, 1, phone))
	if err != nil {
		t.Fatal(err)
	}
	// same person tries again (new idempotency key) while the first hold is still active
	second, err := book(t, a, webBooking(slot, 1, phone))
	if err != nil {
		t.Fatalf("retry by the same customer was blocked: %v", err)
	}
	if st, _ := status(t, first.BookingID); st != "expired" {
		t.Fatalf("old hold should be released, status %s", st)
	}
	if second.Status != "hold" || slotUsage(t, slot) != 1 {
		t.Fatalf("new hold: %s, usage %d", second.Status, slotUsage(t, slot))
	}
}

func TestValidationRejectsBadInput(t *testing.T) {
	a := newTestApp(t)
	slot := newSlot(t, 5)
	r := webBooking(slot, 1, "12345")
	if _, err := book(t, a, r); !isUserErr(err, "phone") {
		t.Fatalf("bad phone: got %v", err)
	}
	r = webBooking(slot, 1, uniquePhone())
	r.Email = "not-an-email"
	if _, err := book(t, a, r); !isUserErr(err, "email") {
		t.Fatalf("bad email: got %v", err)
	}
	r = webBooking(slot, 0, uniquePhone())
	if _, err := book(t, a, r); err == nil {
		t.Fatal("0 participants accepted")
	}
	r = webBooking(slot, 6, uniquePhone())
	if _, err := book(t, a, r); err == nil {
		t.Fatal("more guests than the ticket allows accepted")
	}
}

func TestBlockedAndPastSessionsCannotBeBooked(t *testing.T) {
	a := newTestApp(t)
	slot := newSlot(t, 5)
	testDB.Exec(`UPDATE slots SET status='blocked' WHERE id=$1`, slot)
	if _, err := book(t, a, webBooking(slot, 1, uniquePhone())); !isUserErr(err, "slot_closed") {
		t.Fatalf("blocked session: got %v", err)
	}
	past := newSlot(t, 5)
	testDB.Exec(`UPDATE slots SET starts_at=now()-interval '2 hours', ends_at=now()-interval '1 hour' WHERE id=$1`, past)
	if _, err := book(t, a, webBooking(past, 1, uniquePhone())); err == nil {
		t.Fatal("booked a session in the past")
	}
}

func TestExpiredHoldReleasesCapacity(t *testing.T) {
	a := newTestApp(t)
	slot := newSlot(t, 1)
	r, err := book(t, a, webBooking(slot, 1, uniquePhone()))
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "hold" {
		t.Fatalf("online web booking should start as hold, got %s", r.Status)
	}
	if _, err := book(t, a, webBooking(slot, 1, uniquePhone())); !isUserErr(err, "slot_full") {
		t.Fatalf("held seat must count against capacity, got %v", err)
	}
	testDB.Exec(`UPDATE bookings SET hold_expires_at=now()-interval '1 minute' WHERE id=$1`, r.BookingID)
	if _, err := a.ExpireHolds(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st, _ := status(t, r.BookingID); st != "expired" {
		t.Fatalf("status after expiry = %s", st)
	}
	if _, err := book(t, a, webBooking(slot, 1, uniquePhone())); err != nil {
		t.Fatalf("seat should be free after the hold expired: %v", err)
	}
}

func TestPromoAndVoucherPricing(t *testing.T) {
	ctx := context.Background()
	if _, err := testDB.Exec(`INSERT INTO promo_codes(code,kind,value,per_customer_limit) VALUES('TEST10','percent',10,0)`); err != nil {
		t.Fatal(err)
	}
	slot := newSlot(t, 5)
	s, _ := GetSlot(ctx, testDB, slot, false)
	q, err := BuildQuote(ctx, testDB, QuoteInput{ProductID: ticketID(t), Slot: s, Participants: 2, PromoCode: "test10", Channel: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if q.Subtotal != 118000 || q.Discount != 11800 || q.Total != 106200 {
		t.Fatalf("10%% off 2 x 590: got subtotal %d discount %d total %d", q.Subtotal, q.Discount, q.Total)
	}
	// A 1-credit complimentary voucher covers one guest; the second guest pays.
	var comp int64
	testDB.QueryRow(`SELECT id FROM products WHERE category='complimentary'`).Scan(&comp)
	testDB.Exec(`INSERT INTO passes(code,product_id,credits_total,status) VALUES('PDV-TEST-0001',$1,1,'active')`, comp)
	q, err = BuildQuote(ctx, testDB, QuoteInput{ProductID: ticketID(t), Slot: s, Participants: 2, PassCode: "pdv-test-0001", Channel: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if q.CreditsApplied != 1 || q.Total != 59000 {
		t.Fatalf("voucher: credits %d total %d, want 1 credit and ฿590 to pay", q.CreditsApplied, q.Total)
	}
	if _, err := BuildQuote(ctx, testDB, QuoteInput{ProductID: ticketID(t), Slot: s, Participants: 1, PromoCode: "NOPE", Channel: "web"}); !isUserErr(err, "promo") {
		t.Fatalf("unknown code: %v", err)
	}
}

func TestVoucherCreditIsConsumedOnceAndReturnedOnExpiry(t *testing.T) {
	a := newTestApp(t)
	var comp int64
	testDB.QueryRow(`SELECT id FROM products WHERE category='complimentary'`).Scan(&comp)
	testDB.Exec(`INSERT INTO passes(code,product_id,credits_total,status) VALUES('PDV-TEST-0002',$1,2,'active')`, comp)
	// fully covered by credits → confirmed immediately, no payment
	r := webBooking(newSlot(t, 5), 2, uniquePhone())
	r.PassCode = "PDV-TEST-0002"
	res, err := book(t, a, r)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "confirmed" || res.Total != 0 {
		t.Fatalf("fully-credited booking: status %s total %d", res.Status, res.Total)
	}
	r2 := webBooking(newSlot(t, 5), 1, uniquePhone())
	r2.PassCode = "PDV-TEST-0002"
	if _, err := book(t, a, r2); !isUserErr(err, "pass") {
		t.Fatalf("voucher with no credits left: %v", err)
	}
	// cancelling the credit booking returns the credits
	if _, err := a.CancelBooking(context.Background(), res.BookingID, ChangeActor{StaffID: 0}, CancelOptions{Reason: "test", Refund: "none"}); err != nil {
		t.Fatal(err)
	}
	var used int
	testDB.QueryRow(`SELECT credits_used FROM passes WHERE code='PDV-TEST-0002'`).Scan(&used)
	if used != 0 {
		t.Fatalf("credits_used after cancel = %d, want 0", used)
	}
}

func TestCustomerCancelRulesAndCreditMode(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	SaveSetting(ctx, testDB, "cancel_refund_mode", "credit")
	res, err := book(t, a, webBooking(newSlot(t, 5), 2, uniquePhone()))
	if err != nil {
		t.Fatal(err)
	}
	payBooking(t, a, res.BookingID)
	cr, err := a.CancelBooking(ctx, res.BookingID, ChangeActor{Customer: true}, CancelOptions{Reason: "customer", Refund: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	if cr.CreditPassCode == "" {
		t.Fatal("credit mode should issue a credit code")
	}
	var credits int
	testDB.QueryRow(`SELECT credits_total FROM passes WHERE code=$1 AND note=$2`, cr.CreditPassCode, creditNote(res.BookingID)).Scan(&credits)
	if credits != 2 {
		t.Fatalf("credit pass should hold 2 credits, got %d", credits)
	}
	// too close to the session: the customer can't cancel online
	late, err := book(t, a, webBooking(newSlot(t, 5), 1, uniquePhone()))
	if err != nil {
		t.Fatal(err)
	}
	payBooking(t, a, late.BookingID)
	testDB.Exec(`UPDATE slots SET starts_at=now()+interval '2 hours', ends_at=now()+interval '3 hours' WHERE id=(SELECT slot_id FROM bookings WHERE id=$1)`, late.BookingID)
	if _, err := a.CancelBooking(ctx, late.BookingID, ChangeActor{Customer: true}, CancelOptions{Refund: "auto"}); !isUserErr(err, "change_closed") {
		t.Fatalf("late customer cancel: %v", err)
	}
	SaveSetting(ctx, testDB, "cancel_refund_mode", "refund")
}

func TestRescheduleMovesCapacityAndHonoursLimit(t *testing.T) {
	a := newTestApp(t)
	ctx := context.Background()
	from, to, full := newSlot(t, 5), newSlot(t, 5), newSlot(t, 1)
	res, err := book(t, a, webBooking(from, 2, uniquePhone()))
	if err != nil {
		t.Fatal(err)
	}
	payBooking(t, a, res.BookingID)
	if err := a.RescheduleBooking(ctx, res.BookingID, full, ChangeActor{Customer: true}, false); !isUserErr(err, "slot_full") {
		t.Fatalf("moving 2 guests into a 1-seat session: %v", err)
	}
	if err := a.RescheduleBooking(ctx, res.BookingID, to, ChangeActor{Customer: true}, false); err != nil {
		t.Fatal(err)
	}
	if slotUsage(t, from) != 0 || slotUsage(t, to) != 2 {
		t.Fatalf("capacity not moved: from=%d to=%d", slotUsage(t, from), slotUsage(t, to))
	}
	if err := a.RescheduleBooking(ctx, res.BookingID, from, ChangeActor{Customer: true}, false); !isUserErr(err, "max_reschedule") {
		t.Fatalf("second customer reschedule should hit the limit: %v", err)
	}
}

// payBooking confirms a hold through the simulated provider's signed event path.
func payBooking(t *testing.T, a *App, bookingID int64) string {
	t.Helper()
	ctx := context.Background()
	if _, err := a.StartBookingCheckout(ctx, bookingID, TH, ""); err != nil {
		t.Fatal(err)
	}
	var sess string
	var amount int64
	if err := testDB.QueryRow(`SELECT provider_session, amount FROM payments WHERE booking_id=$1 AND status='pending' ORDER BY id DESC LIMIT 1`, bookingID).Scan(&sess, &amount); err != nil {
		t.Fatal(err)
	}
	ref := "simpi_" + RandomCode(10)
	if err := a.ProcessPaymentEvent(ctx, PaymentEvent{Provider: "simulated", EventID: "ev_" + RandomCode(10), Type: "paid", SessionID: sess, PaymentRef: ref, Amount: amount, Currency: "thb", Method: "promptpay"}); err != nil {
		t.Fatal(err)
	}
	if st, ps := status(t, bookingID); st != "confirmed" || ps != "paid" {
		t.Fatalf("after payment: %s/%s", st, ps)
	}
	return ref
}

func TestBookingIsNotConfirmedWithoutPaymentEvent(t *testing.T) {
	a := newTestApp(t)
	res, err := book(t, a, webBooking(newSlot(t, 5), 1, uniquePhone()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.StartBookingCheckout(context.Background(), res.BookingID, TH, ""); err != nil {
		t.Fatal(err)
	}
	if st, _ := status(t, res.BookingID); st != "hold" {
		t.Fatalf("booking must stay on hold until the provider confirms payment, got %s", st)
	}
	// a payment event for the wrong amount is rejected
	var sess string
	testDB.QueryRow(`SELECT provider_session FROM payments WHERE booking_id=$1`, res.BookingID).Scan(&sess)
	err = a.ProcessPaymentEvent(context.Background(), PaymentEvent{Provider: "simulated", EventID: "ev_bad_" + RandomCode(6), Type: "paid", SessionID: sess, PaymentRef: "x", Amount: 1, Currency: "thb"})
	if err == nil {
		t.Fatal("amount mismatch accepted")
	}
	if st, _ := status(t, res.BookingID); st != "hold" {
		t.Fatalf("wrong-amount event changed status to %s", st)
	}
}

// ---------- Stripe: signature, webhook idempotency, late payment, refunds ----------

func stripeSign(secret string, body []byte, ts time.Time) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.%s", ts.Unix(), body)
	return fmt.Sprintf("t=%d,v1=%s", ts.Unix(), hex.EncodeToString(mac.Sum(nil)))
}

func TestStripeSignatureVerification(t *testing.T) {
	body := []byte(`{"id":"evt_1"}`)
	now := time.Now()
	if err := VerifyStripeSignature(stripeSign("whsec_x", body, now), body, "whsec_x", now); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	if err := VerifyStripeSignature(stripeSign("whsec_other", body, now), body, "whsec_x", now); err == nil {
		t.Fatal("wrong secret accepted")
	}
	if err := VerifyStripeSignature(stripeSign("whsec_x", body, now.Add(-10*time.Minute)), body, "whsec_x", now); err == nil {
		t.Fatal("stale timestamp accepted (replay protection)")
	}
	if err := VerifyStripeSignature(stripeSign("whsec_x", body, now), []byte(`{"id":"evt_2"}`), "whsec_x", now); err == nil {
		t.Fatal("tampered body accepted")
	}
}

// fakeStripe records API calls and answers like Stripe for the endpoints we use.
type fakeStripe struct {
	mu      sync.Mutex
	calls   []string
	refunds int
	srv     *httptest.Server
}

func newFakeStripe() *fakeStripe {
	f := &fakeStripe{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		f.mu.Lock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		f.mu.Unlock()
		switch {
		case r.URL.Path == "/v1/checkout/sessions":
			id := "cs_test_" + RandomCode(10)
			json.NewEncoder(w).Encode(map[string]any{"id": id, "url": "https://checkout.stripe.test/" + id})
		case strings.HasSuffix(r.URL.Path, "/expire"):
			json.NewEncoder(w).Encode(map[string]any{"status": "expired"})
		case r.URL.Path == "/v1/refunds":
			f.mu.Lock()
			f.refunds++
			f.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"id": "re_" + RandomCode(8), "status": "succeeded"})
		default:
			w.WriteHeader(404)
		}
	}))
	return f
}

func newStripeApp(t *testing.T, f *fakeStripe) *App {
	t.Helper()
	cfg := Config{Env: "development", SessionSecret: "test-secret-0123456789-abcdefghij", BaseURL: "http://test.local", PaymentProvider: "stripe",
		StripeSecretKey: "sk_test_x", StripeWebhookSecret: "whsec_test", StripeAPIBase: f.srv.URL, DisableWorkers: true}
	a, err := New(cfg, testDB, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func postWebhook(t *testing.T, a *App, event map[string]any) int {
	t.Helper()
	body, _ := json.Marshal(event)
	req := httptest.NewRequest("POST", "/webhooks/stripe", bytes.NewReader(body))
	req.Header.Set("Stripe-Signature", stripeSign("whsec_test", body, time.Now()))
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)
	return rec.Code
}

func checkoutEvent(id, typ, session string, amount int64, pi string) map[string]any {
	return map[string]any{"id": id, "type": typ, "data": map[string]any{"object": map[string]any{
		"id": session, "payment_status": "paid", "amount_total": amount, "currency": "thb", "payment_intent": pi}}}
}

func TestStripeWebhookConfirmsOnceAndRefundSyncs(t *testing.T) {
	f := newFakeStripe()
	defer f.srv.Close()
	a := newStripeApp(t, f)
	ctx := context.Background()
	res, err := book(t, a, webBooking(newSlot(t, 5), 2, uniquePhone()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.StartBookingCheckout(ctx, res.BookingID, TH, res.ManageToken); err != nil {
		t.Fatal(err)
	}
	var sess string
	testDB.QueryRow(`SELECT provider_session FROM payments WHERE booking_id=$1`, res.BookingID).Scan(&sess)

	// unsigned / badly signed webhook is rejected and changes nothing
	body, _ := json.Marshal(checkoutEvent("evt_forged", "checkout.session.completed", sess, res.Total, "pi_1"))
	req := httptest.NewRequest("POST", "/webhooks/stripe", bytes.NewReader(body))
	req.Header.Set("Stripe-Signature", stripeSign("whsec_wrong", body, time.Now()))
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("forged webhook: status %d", rec.Code)
	}
	if st, _ := status(t, res.BookingID); st != "hold" {
		t.Fatalf("forged webhook confirmed the booking (%s)", st)
	}

	ev := checkoutEvent("evt_paid_"+RandomCode(6), "checkout.session.completed", sess, res.Total, "pi_"+RandomCode(8))
	if code := postWebhook(t, a, ev); code != 200 {
		t.Fatalf("webhook status %d", code)
	}
	if code := postWebhook(t, a, ev); code != 200 { // Stripe retries deliver the same event again
		t.Fatalf("replayed webhook status %d", code)
	}
	if st, ps := status(t, res.BookingID); st != "confirmed" || ps != "paid" {
		t.Fatalf("after webhook: %s/%s", st, ps)
	}
	var notices int
	testDB.QueryRow(`SELECT count(*) FROM notifications WHERE booking_id=$1 AND kind='booking_confirmed' AND channel='email'`, res.BookingID).Scan(&notices)
	if notices != 1 {
		t.Fatalf("confirmation emails queued = %d, want exactly 1", notices)
	}

	// refund made in the Stripe dashboard arrives as charge.refunded: recorded once, revenue stays correct
	pi := ev["data"].(map[string]any)["object"].(map[string]any)["payment_intent"].(string)
	refundEv := map[string]any{"id": "evt_ref_" + RandomCode(6), "type": "charge.refunded", "data": map[string]any{"object": map[string]any{"payment_intent": pi, "amount_refunded": int64(59000)}}}
	if code := postWebhook(t, a, refundEv); code != 200 {
		t.Fatalf("refund webhook status %d", code)
	}
	postWebhook(t, a, refundEv)
	var refunded, rows int64
	testDB.QueryRow(`SELECT refunded_amount FROM payments WHERE booking_id=$1`, res.BookingID).Scan(&refunded)
	testDB.QueryRow(`SELECT count(*) FROM refunds r JOIN payments p ON p.id=r.payment_id WHERE p.booking_id=$1`, res.BookingID).Scan(&rows)
	if refunded != 59000 || rows != 1 {
		t.Fatalf("partial refund: refunded_amount %d, refund rows %d", refunded, rows)
	}
	if _, ps := status(t, res.BookingID); ps != "partially_refunded" {
		t.Fatalf("payment_state = %s", ps)
	}
}

func TestStaffRefundIsIdempotent(t *testing.T) {
	f := newFakeStripe()
	defer f.srv.Close()
	a := newStripeApp(t, f)
	ctx := context.Background()
	res, err := book(t, a, webBooking(newSlot(t, 5), 1, uniquePhone()))
	if err != nil {
		t.Fatal(err)
	}
	a.StartBookingCheckout(ctx, res.BookingID, TH, res.ManageToken)
	var sess string
	testDB.QueryRow(`SELECT provider_session FROM payments WHERE booking_id=$1`, res.BookingID).Scan(&sess)
	postWebhook(t, a, checkoutEvent("evt_"+RandomCode(8), "checkout.session.completed", sess, res.Total, "pi_"+RandomCode(8)))
	var payID int64
	testDB.QueryRow(`SELECT id FROM payments WHERE booking_id=$1`, res.BookingID).Scan(&payID)
	for i := 0; i < 3; i++ { // double-clicks / retries with the same key
		if _, err := a.RefundPayment(ctx, payID, 59000, "test", 0, "staff-same-key"); err != nil && i == 0 {
			t.Fatal(err)
		}
	}
	if f.refunds != 1 {
		t.Fatalf("provider refund calls = %d, want 1", f.refunds)
	}
	var refunded int64
	testDB.QueryRow(`SELECT refunded_amount FROM payments WHERE id=$1`, payID).Scan(&refunded)
	if refunded != 59000 {
		t.Fatalf("refunded_amount = %d", refunded)
	}
}

func TestLatePaymentAfterHoldExpiredAndSeatTakenIsRefunded(t *testing.T) {
	f := newFakeStripe()
	defer f.srv.Close()
	a := newStripeApp(t, f)
	ctx := context.Background()
	slot := newSlot(t, 1)
	res, err := book(t, a, webBooking(slot, 1, uniquePhone()))
	if err != nil {
		t.Fatal(err)
	}
	a.StartBookingCheckout(ctx, res.BookingID, TH, res.ManageToken)
	var sess string
	testDB.QueryRow(`SELECT provider_session FROM payments WHERE booking_id=$1`, res.BookingID).Scan(&sess)
	testDB.Exec(`UPDATE bookings SET hold_expires_at=now()-interval '1 minute' WHERE id=$1`, res.BookingID)
	a.ExpireHolds(ctx)
	// someone else takes the only seat
	if _, err := book(t, a, BookingRequest{SlotID: slot, ProductID: ticketID(t), Participants: 1, Name: "Walk In", Phone: uniquePhone(), Channel: "walk_in", StaffID: 0, PayAtVenue: true, IdempotencyKey: RandomToken()}); err != nil {
		t.Fatal(err)
	}
	// the customer's payment arrives late
	// (the session was already marked expired locally; the customer paid in the race window)
	if code := postWebhook(t, a, checkoutEvent("evt_late_"+RandomCode(6), "checkout.session.completed", sess, res.Total, "pi_late_"+RandomCode(6))); code != 200 {
		t.Fatalf("late webhook status %d", code)
	}
	if f.refunds != 1 {
		t.Fatalf("late payment with no seat should be refunded automatically, refunds=%d", f.refunds)
	}
	var attention string
	testDB.QueryRow(`SELECT needs_attention FROM bookings WHERE id=$1`, res.BookingID).Scan(&attention)
	if attention == "" {
		t.Fatal("late refunded booking should be flagged for staff")
	}
	if u := slotUsage(t, slot); u != 1 {
		t.Fatalf("slot usage %d, want 1 (no overbooking)", u)
	}
}

// ---------- HTTP layer: CSRF, access control ----------

func TestAPIRequiresCSRFAndAccessTokens(t *testing.T) {
	a := newTestApp(t)
	body := `{"slot_id":1,"product_id":1,"participants":1}`
	req := httptest.NewRequest("POST", "/api/bookings", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("booking API without CSRF: status %d", rec.Code)
	}
	res, err := book(t, a, webBooking(newSlot(t, 5), 1, uniquePhone()))
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"/booking/" + res.Ref, "/booking/" + res.Ref + "?s=forged", "/booking/" + res.Ref + "?t=wrong"} {
		rec := httptest.NewRecorder()
		a.ServeHTTP(rec, httptest.NewRequest("GET", u, nil))
		if rec.Code != 404 {
			t.Fatalf("%s without valid access: status %d", u, rec.Code)
		}
	}
	rec = httptest.NewRecorder()
	a.ServeHTTP(rec, httptest.NewRequest("GET", "/booking/"+res.Ref+"?t="+url.QueryEscape(res.ManageToken), nil))
	if rec.Code != 200 {
		t.Fatalf("valid manage token: status %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	a.ServeHTTP(rec, httptest.NewRequest("GET", "/admin/bookings", nil))
	if rec.Code != 303 && rec.Code != 302 {
		t.Fatalf("admin without login should redirect, got %d", rec.Code)
	}
}

func TestEveryPublicPageRenders(t *testing.T) {
	a := newTestApp(t)
	for _, p := range []string{"/", "/en", "/experiences", "/pricing", "/story", "/gallery", "/faq", "/contact", "/privacy", "/terms", "/cancellation", "/book", "/my-bookings",
		"/en/experiences", "/en/pricing", "/en/story", "/en/gallery", "/en/faq", "/en/contact", "/en/privacy", "/en/terms", "/en/cancellation", "/en/book", "/en/my-bookings", "/admin/login"} {
		rec := httptest.NewRecorder()
		a.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code != 200 {
			t.Errorf("%s: status %d", p, rec.Code)
		}
		if b := rec.Body.String(); strings.Contains(b, "err.") || strings.Contains(b, "home.") || strings.Contains(b, "book.") && strings.Contains(b, "book.step") {
			t.Errorf("%s: untranslated key in output", p)
		}
	}
}

func staffCookie(t *testing.T, role string) *http.Cookie {
	t.Helper()
	var id int64
	email := role + "-" + RandomCode(6) + "@test.local"
	h, _ := HashPassword("not-used-in-tests")
	if err := testDB.QueryRow(`INSERT INTO staff_users(email,name,password_hash,role) VALUES($1,$2,$3,$4) RETURNING id`, email, "T "+role, h, role).Scan(&id); err != nil {
		t.Fatal(err)
	}
	tok := RandomToken()
	if _, err := testDB.Exec(`INSERT INTO staff_sessions(token_hash,user_id,csrf,expires_at) VALUES($1,$2,'csrf-x',now()+interval '1 hour')`, HashToken(tok), id); err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: "pd_staff", Value: tok}
}

func TestAdminRolesAndPagesRender(t *testing.T) {
	a := newTestApp(t)
	owner := staffCookie(t, "owner")
	for _, p := range []string{"/admin", "/admin/calendar", "/admin/calendar?view=week", "/admin/bookings", "/admin/bookings/new", "/admin/checkin", "/admin/customers",
		"/admin/analytics", "/admin/analytics?range=today", "/admin/analytics?range=custom&from=2026-01-01&to=2026-12-31", "/admin/payments", "/admin/schedule",
		"/admin/products", "/admin/promos", "/admin/passes", "/admin/marketing", "/admin/content", "/admin/staff", "/admin/settings", "/admin/audit", "/admin/notifications", "/admin/account"} {
		req := httptest.NewRequest("GET", p, nil)
		req.AddCookie(owner)
		rec := httptest.NewRecorder()
		a.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Errorf("owner %s: status %d", p, rec.Code)
		}
	}
	desk := staffCookie(t, "front_desk")
	for p, want := range map[string]int{"/admin/checkin": 200, "/admin/settings": 403, "/admin/staff": 403, "/admin/analytics": 403, "/admin/payments": 403} {
		req := httptest.NewRequest("GET", p, nil)
		req.AddCookie(desk)
		rec := httptest.NewRecorder()
		a.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("front desk %s: status %d, want %d", p, rec.Code, want)
		}
	}
	// a write without the session's CSRF token is refused
	req := httptest.NewRequest("POST", "/admin/settings", strings.NewReader("phone=000"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(owner)
	rec := httptest.NewRecorder()
	a.ServeHTTP(rec, req)
	if rec.Code < 400 {
		t.Errorf("admin POST without CSRF: status %d", rec.Code)
	}
}

func TestSMTPDelivery(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		rd := bufio.NewReader(c)
		fmt.Fprint(c, "220 fake ESMTP\r\n")
		var data strings.Builder
		inData := false
		for {
			line, err := rd.ReadString('\n')
			if err != nil {
				return
			}
			if inData {
				if line == ".\r\n" {
					inData = false
					fmt.Fprint(c, "250 queued\r\n")
					got <- data.String()
					continue
				}
				data.WriteString(line)
				continue
			}
			switch cmd := strings.ToUpper(strings.TrimSpace(line)); {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				fmt.Fprint(c, "250 fake\r\n")
			case strings.HasPrefix(cmd, "DATA"):
				inData = true
				fmt.Fprint(c, "354 go ahead\r\n")
			case strings.HasPrefix(cmd, "QUIT"):
				fmt.Fprint(c, "221 bye\r\n")
				return
			default:
				fmt.Fprint(c, "250 ok\r\n")
			}
		}
	}()
	host, port, _ := net.SplitHostPort(ln.Addr().String())
	a := &App{Cfg: Config{SMTPHost: host, SMTPPort: port, SMTPFrom: "Peaceful Day <hello@example.com>"}}
	if err := a.sendEmail("guest@example.com", "จองเรียบร้อยแล้ว · PD-TEST01", "สวัสดีค่ะ\nรหัสการจอง PD-TEST01"); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-got:
		for _, want := range []string{"Subject: =?utf-8?q?", "Content-Transfer-Encoding: base64", "Message-ID: <", "multipart/alternative"} {
			if !strings.Contains(m, want) {
				t.Errorf("message missing %q", want)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no message received")
	}
}

func TestTranslationsComplete(t *testing.T) {
	for k, v := range dict {
		if v[0] == "" || v[1] == "" {
			t.Errorf("missing translation for %q", k)
		}
	}
}
