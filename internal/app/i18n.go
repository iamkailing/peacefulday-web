package app

import (
	"fmt"
	"strings"
	"time"
)

// Lang is "th" (default, no URL prefix) or "en" (/en prefix).
type Lang string

const (
	TH Lang = "th"
	EN Lang = "en"
)

func (l Lang) Prefix() string {
	if l == EN {
		return "/en"
	}
	return ""
}

func (l Lang) Other() Lang {
	if l == EN {
		return TH
	}
	return EN
}

// T looks up a UI string. Missing keys show the key so they're easy to spot.
func (l Lang) T(key string, args ...any) string {
	pair, ok := dict[key]
	if !ok {
		return key
	}
	s := pair[0]
	if l == EN {
		s = pair[1]
	}
	if len(args) > 0 {
		return fmt.Sprintf(s, args...)
	}
	return s
}

// Pick returns th or en from a pair stored on a record, falling back to the other.
func (l Lang) Pick(th, en string) string {
	if l == EN && strings.TrimSpace(en) != "" {
		return en
	}
	if strings.TrimSpace(th) == "" {
		return en
	}
	return th
}

var thMonths = []string{"มกราคม", "กุมภาพันธ์", "มีนาคม", "เมษายน", "พฤษภาคม", "มิถุนายน", "กรกฎาคม", "สิงหาคม", "กันยายน", "ตุลาคม", "พฤศจิกายน", "ธันวาคม"}
var thMonthsShort = []string{"ม.ค.", "ก.พ.", "มี.ค.", "เม.ย.", "พ.ค.", "มิ.ย.", "ก.ค.", "ส.ค.", "ก.ย.", "ต.ค.", "พ.ย.", "ธ.ค."}
var thDays = []string{"อาทิตย์", "จันทร์", "อังคาร", "พุธ", "พฤหัสบดี", "ศุกร์", "เสาร์"}
var thDaysShort = []string{"อา.", "จ.", "อ.", "พ.", "พฤ.", "ศ.", "ส."}

// DateLong: "วันศุกร์ที่ 23 ตุลาคม 2569" / "Friday, 23 October 2026"
func (l Lang) DateLong(t time.Time) string {
	t = t.In(BKK)
	if l == EN {
		return t.Format("Monday, 2 January 2006")
	}
	return fmt.Sprintf("วัน%sที่ %d %s %d", thDays[t.Weekday()], t.Day(), thMonths[t.Month()-1], t.Year()+543)
}

func (l Lang) DateShort(t time.Time) string {
	t = t.In(BKK)
	if l == EN {
		return t.Format("Mon 2 Jan")
	}
	return fmt.Sprintf("%s %d %s", thDaysShort[t.Weekday()], t.Day(), thMonthsShort[t.Month()-1])
}

func (l Lang) Weekday(t time.Time) string {
	if l == EN {
		return t.In(BKK).Format("Mon")
	}
	return thDaysShort[t.In(BKK).Weekday()]
}

func (l Lang) WeekdayName(d int) string {
	if l == EN {
		return time.Weekday(d).String()
	}
	return thDays[d]
}

func (l Lang) Month(t time.Time) string {
	if l == EN {
		return t.In(BKK).Format("Jan")
	}
	return thMonthsShort[t.In(BKK).Month()-1]
}

func HM(t time.Time) string { return t.In(BKK).Format("15:04") }

var dict = map[string][2]string{
	// navigation
	"nav.experiences": {"ประสบการณ์", "Experiences"},
	"nav.pricing":     {"ราคาและแพ็กเกจ", "Pricing"},
	"nav.story":       {"เรื่องราวของเรา", "Our Story"},
	"nav.gallery":     {"แกลเลอรี", "Gallery"},
	"nav.faq":         {"คำถามที่พบบ่อย", "FAQ"},
	"nav.contact":     {"ติดต่อและการเดินทาง", "Contact & Location"},
	"nav.my":          {"การจองของฉัน", "My Bookings"},
	"nav.book":        {"จองรอบของคุณ", "Book Your Session"},
	"nav.menu":        {"เมนู", "Menu"},
	"nav.close":       {"ปิด", "Close"},
	"nav.privacy":     {"นโยบายความเป็นส่วนตัว", "Privacy Policy"},
	"nav.terms":       {"ข้อกำหนดและเงื่อนไข", "Terms & Conditions"},
	"nav.cancel":      {"นโยบายยกเลิกและคืนเงิน", "Cancellation & Refunds"},
	"nav.packages":    {"ซื้อแพ็กเกจ / บัตรของขวัญ", "Packages & Gift Vouchers"},
	"lang.switch":     {"English", "ภาษาไทย"},

	// hero + home
	"hero.eyebrow":     {"Ice Bath · Sauna · Meditation · สุขุมวิท 62", "Ice Bath · Sauna · Meditation · Sukhumvit 62"},
	"hero.title":       {"วันนี้ แค่มาพัก", "Today, just come and rest."},
	"hero.lead":        {"พื้นที่พักใจในบรรยากาศสวน ย่านสุขุมวิท 62 มาแช่น้ำเย็น หายใจช้า ๆ แล้วพักให้สบาย ครั้งแรกก็มาได้ ไม่ต้องสายฮาร์ดคอร์", "A garden space on Sukhumvit 62 to take a cold plunge, slow your breathing and properly rest. First time? You're welcome as you are."},
	"hero.cta2":        {"ดูราคา", "See prices"},
	"hero.hours":       {"เปิดทุกวัน %s–%s", "Open daily %s–%s"},
	"home.intro.k":     {"ประสบการณ์", "The experience"},
	"home.intro.h":     {"เย็นให้สุด แล้วพักให้สบาย", "Cold first, then rest"},
	"home.intro.p":     {"ลงแช่น้ำเย็นให้ร่างกายตื่นเต็มที่ แล้วค่อย ๆ หายใจช้าลง พักในบรรยากาศสวน ปล่อยให้ใจสงบลงเอง", "A cold plunge wakes your whole body up. Then you slow your breathing and rest in the garden, and let your mind settle on its own."},
	"home.why.k":       {"ทำไมต้อง Peaceful Day", "Why Peaceful Day"},
	"home.why.h":       {"ที่พักใจที่อบอุ่น ไม่ใช่ยิมเย็น ๆ", "A warm place to rest, not a cold, hard gym"},
	"why.1.h":          {"ครั้งแรกก็มาได้", "Fine for a first time"},
	"why.1.p":          {"ไม่ต้องเคยแช่น้ำเย็นมาก่อน เริ่มสั้น ๆ ตามที่ร่างกายไหว ไม่มีใครเร่ง", "No ice bath experience needed. Start short and go at your body's pace."},
	"why.2.h":          {"สวน แดด และลม", "Garden, sun and fresh air"},
	"why.2.p":          {"แช่น้ำเย็นในบรรยากาศสวน มีแสงแดดและลมธรรมชาติ", "Your cold plunge happens in a garden setting, with daylight and fresh air."},
	"why.3.h":          {"รอบเล็ก ไม่แออัด", "Small sessions"},
	"why.3.p":          {"รับรอบละไม่เกิน %d คน มาคนเดียวหรือมากับเพื่อนก็สบาย", "No more than %d people per session, whether you come alone or with friends."},
	"home.price.k":     {"ราคา", "Pricing"},
	"home.price.h":     {"ราคาชัดเจน จองแล้วจ่ายออนไลน์ได้ทันที", "Clear prices. Book and pay online in a minute."},
	"home.price.all":   {"ดูราคาและแพ็กเกจทั้งหมด", "See all prices and packages"},
	"home.how.k":       {"ครั้งแรกที่มา", "Your first visit"},
	"home.how.h":       {"ครั้งแรกที่มา ทำแบบนี้", "Your first visit, step by step"},
	"how.1.h":          {"จองรอบออนไลน์", "Book online"},
	"how.1.p":          {"เลือกวัน เวลา และจำนวนคน แล้วจ่ายด้วย PromptPay หรือบัตร ได้ QR ยืนยันทันที", "Pick a date, time and group size, pay by PromptPay or card, and get your QR confirmation straight away."},
	"how.2.h":          {"มาก่อนเวลา 10–15 นาที", "Arrive 10–15 minutes early"},
	"how.2.p":          {"เตรียมชุดที่เปียกน้ำได้มาด้วย ที่ร้านมีห้องน้ำและห้องเปลี่ยนชุด", "Bring clothes that can get wet. There are toilets and a changing room on site."},
	"how.3.h":          {"แช่น้ำเย็นตามจังหวะของคุณ", "Plunge at your own pace"},
	"how.3.p":          {"เริ่มสั้น ๆ ก่อนก็ได้ ฟังร่างกายตัวเองเป็นหลัก", "Short is fine to start with. Your body sets the pace."},
	"how.4.h":          {"แล้วพักให้สบาย", "Then rest"},
	"how.4.p":          {"หายใจช้า ๆ นั่งพักในบรรยากาศสวน แล้วค่อยกลับไปใช้วันต่อแบบเบาขึ้น", "Breathe slowly, sit in the garden, and go back to your day a little lighter."},
	"home.slots.k":     {"รอบที่ยังว่าง", "Open sessions"},
	"home.slots.h":     {"รอบว่างในอีกไม่กี่วันข้างหน้า", "Sessions open in the next few days"},
	"home.slots.none":  {"ยังไม่มีรอบเปิดในช่วงนี้ ลองเลือกวันอื่นในหน้าจอง", "No open sessions right now. Try another date on the booking page."},
	"home.slots.left":  {"ว่าง %d ที่", "%d spots left"},
	"home.reviews.k":   {"รีวิว", "Reviews"},
	"home.reviews.h":   {"เสียงจากคนที่มาพักกับเรา", "From people who rested with us"},
	"home.reviews.src": {"อ่านต้นฉบับที่ %s", "Read the original on %s"},
	"home.loc.k":       {"การเดินทาง", "Getting here"},
	"home.loc.h":       {"สุขุมวิท 62 ใกล้ BTS บางจาก", "Sukhumvit 62, near BTS Bang Chak"},
	"home.gallery.k":   {"แกลเลอรี", "Gallery"},
	"home.gallery.h":   {"บรรยากาศที่ร้าน", "Around the garden"},
	"sample.image":     {"ภาพตัวอย่าง", "Sample image"},
	"sauna.soon":       {"เร็ว ๆ นี้", "Coming soon"},

	// directions
	"dir.bts":   {"BTS บางจาก ทางออก 2 แล้วต่อวินมอเตอร์ไซค์ บอกว่า “แยก 8-3” ค่าบริการประมาณ 15 บาท", "BTS Bang Chak, Exit 2, then a motorbike taxi to “Yaek 8-3” (about ฿15)."},
	"dir.car":   {"จอดรถได้บริเวณด้านข้างกำแพงร้าน กรุณาเว้นแนวกระถางปูนไว้ให้อาคารข้างเคียง ถ้าไม่แน่ใจ ถามทีมงานได้เลย", "You can park along the side wall of the shop. Please keep the planter line clear for the neighbouring building, and ask our team if you're unsure."},
	"dir.open":  {"เปิดในแผนที่", "Open in Maps"},
	"dir.call":  {"โทร", "Call"},
	"dir.hours": {"เวลาเปิด", "Opening hours"},

	// pricing
	"price.k":        {"ราคาและแพ็กเกจ", "Pricing & packages"},
	"price.h":        {"เลือกแบบที่ใช่สำหรับคุณ", "Choose what suits you"},
	"price.per":      {"/ คน", "/ person"},
	"price.for":      {"สำหรับ %d คน", "for %d people"},
	"price.sessions": {"%d ครั้ง", "%d sessions"},
	"price.valid":    {"ใช้ได้ %d วันหลังซื้อ", "Valid %d days from purchase"},
	"price.novalid":  {"ไม่มีวันหมดอายุ", "No expiry"},
	"price.book":     {"จองรอบนี้", "Book this"},
	"price.buy":      {"ซื้อแพ็กเกจ", "Buy package"},
	"price.gift":     {"ซื้อเป็นของขวัญ", "Buy as a gift"},
	"price.passes.h": {"แพ็กเกจและบัตรของขวัญ", "Packages & gift vouchers"},
	"price.passes.p": {"ซื้อเก็บไว้ใช้เอง หรือส่งต่อให้คนที่รัก ใช้รหัสบัตรตอนจองแทนการจ่ายเงิน", "Buy for yourself or someone you love. Use the voucher code at booking instead of paying."},
	"price.weekdays": {"ใช้ได้วัน: %s", "Valid on: %s"},
	"price.after":    {"รอบตั้งแต่ %s น.", "Sessions from %s"},
	"price.none":     {"ยังไม่มีแพ็กเกจเปิดขายตอนนี้", "No packages on sale right now."},

	// booking
	"book.k":           {"จองรอบ", "Book a session"},
	"book.h":           {"จองภายในหนึ่งนาที", "Book in about a minute"},
	"book.step.what":   {"เลือกแบบ", "Choose"},
	"book.step.when":   {"วันและเวลา", "Date & time"},
	"book.step.who":    {"ข้อมูลผู้จอง", "Your details"},
	"book.step.pay":    {"ชำระเงิน", "Pay"},
	"book.experience":  {"ประสบการณ์", "Experience"},
	"book.ticket":      {"แบบที่จอง", "Ticket"},
	"book.date":        {"วันที่", "Date"},
	"book.time":        {"รอบเวลา", "Time"},
	"book.people":      {"จำนวนคน", "Guests"},
	"book.left":        {"ว่าง %d", "%d left"},
	"book.full":        {"เต็ม", "Full"},
	"book.closed":      {"ปิด", "Closed"},
	"book.loading":     {"กำลังโหลดรอบว่าง…", "Loading open sessions…"},
	"book.noslots":     {"วันนี้ไม่มีรอบว่างแล้ว ลองเลือกวันอื่น", "No sessions left on this day. Try another date."},
	"book.pickdate":    {"เลือกวันก่อน แล้วรอบเวลาจะขึ้นตรงนี้", "Choose a date and the times will appear here."},
	"book.name":        {"ชื่อ", "Name"},
	"book.phone":       {"เบอร์โทรศัพท์", "Phone"},
	"book.email":       {"อีเมล (สำหรับส่งใบยืนยัน)", "Email (for your confirmation)"},
	"book.note":        {"มีอะไรอยากบอกเรา (ไม่บังคับ)", "Anything we should know? (optional)"},
	"book.promo":       {"โค้ดส่วนลด", "Discount code"},
	"book.voucher":     {"รหัสบัตร/แพ็กเกจ", "Voucher or package code"},
	"book.apply":       {"ใช้โค้ด", "Apply"},
	"book.consent":     {"รับข่าวสารและโปรโมชันจาก Peaceful Day (ยกเลิกได้ทุกเมื่อ)", "Send me news and offers from Peaceful Day (unsubscribe any time)"},
	"book.agree":       {"ฉันยอมรับ<a href=\"%s\">ข้อกำหนด</a>และ<a href=\"%s\">นโยบายยกเลิก</a>", "I accept the <a href=\"%s\">terms</a> and <a href=\"%s\">cancellation policy</a>"},
	"book.health":      {"ฉันไม่มีโรคหัวใจ ความดันสูงที่ยังคุมไม่ได้ หรือไม่ได้ตั้งครรภ์ หรือได้ปรึกษาแพทย์แล้ว", "I have no heart condition, uncontrolled high blood pressure or pregnancy, or I have checked with a doctor."},
	"book.subtotal":    {"ราคา", "Subtotal"},
	"book.discount":    {"ส่วนลด", "Discount"},
	"book.credits":     {"ใช้สิทธิ์จากบัตร (%d ครั้ง)", "Voucher credits (%d)"},
	"book.total":       {"ยอดชำระ", "Total"},
	"book.paynow":      {"ไปชำระเงิน", "Continue to payment"},
	"book.confirm":     {"ยืนยันการจอง", "Confirm booking"},
	"book.payinfo":     {"จ่ายผ่าน PromptPay หรือบัตรเครดิต/เดบิต ในหน้าชำระเงินที่ปลอดภัยของ Stripe เราไม่เห็นและไม่เก็บเลขบัตรของคุณ", "Pay by PromptPay or credit/debit card on Stripe's secure payment page. We never see or store your card number."},
	"book.holdinfo":    {"เราจะกันที่ไว้ให้ %d นาทีระหว่างชำระเงิน", "We'll hold your spot for %d minutes while you pay."},
	"book.back":        {"ย้อนกลับ", "Back"},
	"book.next":        {"ถัดไป", "Next"},
	"book.summary":     {"สรุปการจอง", "Your booking"},
	"book.err.generic": {"มีบางอย่างผิดพลาด ลองอีกครั้ง หรือโทรหาเรา %s", "Something went wrong. Please try again or call us at %s."},
	"book.simnote":     {"โหมดทดสอบ: ยังไม่มีการเก็บเงินจริง", "Test mode: no real money is charged."},

	// confirmation / manage
	"conf.h":             {"จองเรียบร้อยแล้ว", "You're booked"},
	"conf.pending.h":     {"รอยืนยันการชำระเงิน", "Waiting for payment confirmation"},
	"conf.pending.p":     {"ถ้าชำระแล้ว หน้านี้จะอัปเดตเองในไม่กี่วินาที", "If you've paid, this page updates by itself in a few seconds."},
	"conf.expired.h":     {"หมดเวลาชำระเงิน", "Payment time ran out"},
	"conf.expired.p":     {"ที่ที่กันไว้ถูกปล่อยแล้ว ลองจองใหม่ได้เลย", "Your held spot was released. Please book again."},
	"conf.cancel.h":      {"การจองนี้ถูกยกเลิกแล้ว", "This booking was cancelled"},
	"conf.ref":           {"รหัสการจอง", "Booking reference"},
	"conf.qr":            {"แสดง QR นี้ที่หน้าร้านเพื่อเช็คอิน", "Show this QR code at the front desk to check in"},
	"conf.save":          {"บันทึกลิงก์หน้านี้ไว้ ใช้ดู เลื่อน หรือยกเลิกการจองได้", "Keep this page's link to view, reschedule or cancel."},
	"conf.ics":           {"เพิ่มลงปฏิทิน", "Add to calendar"},
	"conf.status":        {"สถานะ", "Status"},
	"conf.payment":       {"การชำระเงิน", "Payment"},
	"conf.receipt":       {"ดูใบเสร็จ", "View receipt"},
	"conf.reschedule":    {"เลื่อนวัน/เวลา", "Reschedule"},
	"conf.cancel":        {"ยกเลิกการจอง", "Cancel booking"},
	"conf.cancel.sure":   {"ยืนยันยกเลิก", "Yes, cancel it"},
	"conf.cancel.rule":   {"ยกเลิกหรือเลื่อนได้ก่อนเวลาเริ่ม %d ชั่วโมง", "You can cancel or reschedule up to %d hours before the start."},
	"conf.cancel.closed": {"เลยกำหนดยกเลิก/เลื่อนออนไลน์แล้ว กรุณาโทร %s", "It's past the online change deadline. Please call %s."},
	"conf.refund.refund": {"ยอดที่ชำระออนไลน์จะคืนเข้าช่องทางเดิม", "Online payments are refunded to the original method."},
	"conf.refund.credit": {"ยอดที่ชำระจะคืนเป็นบัตรเครดิตเข้าร้าน ใช้จองครั้งต่อไปได้", "Your payment is returned as a voucher you can use next time."},
	"conf.arrive":        {"แนะนำมาก่อนเวลา 10–15 นาที", "Please arrive 10–15 minutes early."},
	"conf.newtime":       {"เลือกรอบใหม่", "Choose a new time"},
	"conf.moved":         {"เลื่อนการจองเรียบร้อย", "Your booking was moved."},
	"conf.cancelled":     {"ยกเลิกการจองเรียบร้อย", "Your booking was cancelled."},

	// statuses
	"st.hold":                {"รอชำระเงิน", "Awaiting payment"},
	"st.confirmed":           {"ยืนยันแล้ว", "Confirmed"},
	"st.checked_in":          {"เช็คอินแล้ว", "Checked in"},
	"st.cancelled":           {"ยกเลิก", "Cancelled"},
	"st.expired":             {"หมดเวลาชำระ", "Expired"},
	"st.no_show":             {"ไม่มาตามนัด", "No-show"},
	"pay.none":               {"–", "–"},
	"pay.pending":            {"รอชำระ", "Pending"},
	"pay.paid":               {"ชำระแล้ว", "Paid"},
	"pay.failed":             {"ชำระไม่สำเร็จ", "Failed"},
	"pay.expired":            {"หมดเวลา", "Expired"},
	"pay.refunded":           {"คืนเงินแล้ว", "Refunded"},
	"pay.partially_refunded": {"คืนเงินบางส่วน", "Partially refunded"},
	"pay.due_at_venue":       {"ชำระที่ร้าน", "Pay at venue"},
	"pay.paid_at_venue":      {"ชำระที่ร้านแล้ว", "Paid at venue"},

	// my bookings
	"my.h":        {"การจองของฉัน", "My bookings"},
	"my.p":        {"ใส่อีเมลที่ใช้จอง เราจะส่งลิงก์เข้าสู่ระบบให้ ไม่ต้องตั้งรหัสผ่าน", "Enter the email you booked with and we'll send you a sign-in link. No password needed."},
	"my.send":     {"ส่งลิงก์เข้าสู่ระบบ", "Email me a sign-in link"},
	"my.sent":     {"ถ้ามีการจองด้วยอีเมลนี้ คุณจะได้รับลิงก์ภายในไม่กี่นาที", "If we have bookings for this email, a link is on its way."},
	"my.or":       {"หรือค้นด้วยรหัสการจอง", "Or find one booking by reference"},
	"my.find":     {"ค้นหา", "Find"},
	"my.ref":      {"รหัสการจอง (เช่น PD-7K3QX9)", "Booking reference (e.g. PD-7K3QX9)"},
	"my.notfound": {"ไม่พบการจองที่ตรงกับรหัสและเบอร์โทรนี้", "No booking matches that reference and phone number."},
	"my.upcoming": {"การจองที่กำลังจะมาถึง", "Upcoming"},
	"my.history":  {"ประวัติ", "History"},
	"my.passes":   {"แพ็กเกจและบัตรของฉัน", "My packages & vouchers"},
	"my.left":     {"เหลือ %d จาก %d ครั้ง", "%d of %d left"},
	"my.until":    {"ใช้ได้ถึง %s", "Valid until %s"},
	"my.empty":    {"ยังไม่มีการจอง", "No bookings yet"},
	"my.signout":  {"ออกจากระบบ", "Sign out"},
	"my.hello":    {"สวัสดีคุณ%s", "Hello %s"},

	// pass purchase
	"pass.h":       {"ซื้อแพ็กเกจ", "Buy a package"},
	"pass.giftq":   {"ซื้อเป็นของขวัญ", "This is a gift"},
	"pass.giftto":  {"ชื่อผู้รับ", "Recipient's name"},
	"pass.giftmsg": {"ข้อความถึงผู้รับ", "Message for them"},
	"pass.done":    {"บัตรของคุณพร้อมใช้แล้ว", "Your voucher is ready"},
	"pass.code":    {"รหัสบัตร", "Voucher code"},
	"pass.howuse":  {"ใส่รหัสนี้ในขั้นตอนจองรอบ ช่อง “รหัสบัตร/แพ็กเกจ”", "Enter this code in the “Voucher or package code” box when booking."},

	// footer
	"foot.tag":    {"พักให้สบายใจ แล้วค่อยกลับไปใช้ชีวิตต่อ", "Rest well, then go back to your day."},
	"foot.visit":  {"แวะมาหาเรา", "Visit"},
	"foot.follow": {"ติดตาม", "Follow"},
	"foot.rights": {"© %d Peaceful Day สงวนลิขสิทธิ์", "© %d Peaceful Day. All rights reserved."},

	// consent
	"consent.p":   {"เราใช้คุกกี้เพื่อจำการตั้งค่าและวัดผลการตลาดเมื่อคุณอนุญาตเท่านั้น", "We use cookies to remember your settings, and for marketing measurement only if you allow it."},
	"consent.yes": {"อนุญาต", "Allow"},
	"consent.no":  {"เฉพาะที่จำเป็น", "Essential only"},

	// errors
	"err.slot_full":      {"รอบนี้เต็มแล้ว กรุณาเลือกรอบอื่น", "That session just filled up. Please pick another time."},
	"err.slot_closed":    {"รอบนี้ไม่เปิดรับจอง", "That session isn't open for booking."},
	"err.too_soon":       {"รอบนี้ใกล้เวลาเริ่มเกินไปสำหรับการจองออนไลน์ โทรหาเราได้เลย", "That session starts too soon to book online. Please call us."},
	"err.too_far":        {"ยังไม่เปิดให้จองล่วงหน้าไกลขนาดนี้", "Bookings aren't open that far ahead yet."},
	"err.participants":   {"จำนวนคนไม่ตรงกับแบบที่เลือก", "The number of guests doesn't fit this ticket."},
	"err.product":        {"แบบที่เลือกไม่สามารถใช้กับรอบนี้ได้", "This ticket can't be used for that session."},
	"err.duplicate":      {"เบอร์โทรหรืออีเมลนี้มีการจองในรอบเดียวกันอยู่แล้ว ถ้าต้องการเพิ่มคน กรุณาโทรหาเรา", "This phone number or email already has a booking in that session. To add guests, please call us."},
	"err.promo":          {"โค้ดส่วนลดใช้ไม่ได้", "That discount code can't be used."},
	"err.promo_used":     {"โค้ดนี้ถูกใช้ครบแล้ว", "That code has been fully used."},
	"err.pass":           {"รหัสบัตรใช้ไม่ได้ หรือสิทธิ์หมดแล้ว", "That voucher code isn't valid or has no credits left."},
	"err.name":           {"กรุณาใส่ชื่อ", "Please enter your name."},
	"err.phone":          {"เบอร์โทรไม่ถูกต้อง (10 หลัก ขึ้นต้นด้วย 0)", "Please enter a valid phone number."},
	"err.email":          {"อีเมลไม่ถูกต้อง", "Please enter a valid email."},
	"err.agree":          {"กรุณายอมรับเงื่อนไขก่อนจอง", "Please accept the terms to continue."},
	"err.rate":           {"มีการส่งคำขอถี่เกินไป รอสักครู่แล้วลองใหม่", "Too many requests. Please wait a moment and try again."},
	"err.change_closed":  {"เลยกำหนดเปลี่ยนแปลงการจองออนไลน์แล้ว", "It's past the deadline for online changes."},
	"err.max_reschedule": {"เลื่อนการจองครบจำนวนครั้งที่กำหนดแล้ว", "This booking has already been rescheduled the maximum number of times."},
	"err.notfound":       {"ไม่พบหน้าที่ต้องการ", "Page not found"},
	"err.notfound.p":     {"ลิงก์อาจเปลี่ยนไปแล้ว ลองกลับไปหน้าแรก", "The link may have changed. Try the homepage."},
	"err.server":         {"ระบบขัดข้องชั่วคราว", "Something went wrong on our side"},
	"err.csrf":           {"หน้านี้หมดอายุ กรุณารีเฟรชแล้วลองใหม่", "This page expired. Please refresh and try again."},
	"hero.next":          {"รอบว่างถัดไป", "Next open sessions"},
	"hero.more":          {"ดูวันอื่น", "More dates"},
	"dir.bts.h":          {"มาด้วย BTS", "By BTS"},
	"dir.car.h":          {"มาด้วยรถยนต์", "By car"},
	"hours.daily":        {"เปิดทุกวัน %s–%s น.", "Open daily %s–%s"},
	"hours.closed":       {"เปิด %s–%s น. หยุดทุก%s", "Open %s–%s, closed on %s"},
}

// RelDay names a date relative to today: "วันนี้", "พรุ่งนี้", or a short weekday date.
func (l Lang) RelDay(t time.Time) string {
	d := DateOnly(t)
	today := DateOnly(Now())
	switch {
	case d.Equal(today):
		return l.Pick("วันนี้", "Today")
	case d.Equal(today.AddDate(0, 0, 1)):
		return l.Pick("พรุ่งนี้", "Tomorrow")
	}
	return l.DateShort(t)
}
