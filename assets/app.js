/* Peaceful Day — website and back office.
   Everything that matters (seats, duplicates, cut-off times, staff roles) is decided by the database functions
   in supabase/schema.sql. This file only shows screens and calls those functions. */
(() => {
  "use strict";
  const CFG = window.PFD_CONFIG;
  const app = document.getElementById("app");

  // ---------- helpers ----------
  const $ = (s, el = document) => el.querySelector(s);
  const $$ = (s, el = document) => Array.from(el.querySelectorAll(s));
  const esc = (s) => String(s == null ? "" : s).replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  const store = { get(k) { try { return JSON.parse(localStorage.getItem(k)); } catch (e) { return null; } }, set(k, v) { try { localStorage.setItem(k, JSON.stringify(v)); } catch (e) { /* private mode */ } }, del(k) { try { localStorage.removeItem(k); } catch (e) { /* ignore */ } } };
  let lang = store.get("pfd-lang") === "en" ? "en" : "th";
  const P = (th, en) => (lang === "en" ? en : th);
  const baht = (n) => "฿" + Number(n || 0).toLocaleString("en-US");
  const BKK_MS = 7 * 3600e3;
  const dkey = (d) => new Date(d.getTime() + BKK_MS).toISOString().slice(0, 10);
  const today = () => dkey(new Date());
  const noon = (k) => new Date(k + "T12:00:00+07:00");
  const addDays = (k, n) => { const d = noon(k); d.setUTCDate(d.getUTCDate() + n); return dkey(d); };
  const dow = (k) => noon(k).getUTCDay();
  const FMT = {};
  const df = (lc, o) => { const key = lc + JSON.stringify(o); return FMT[key] || (FMT[key] = new Intl.DateTimeFormat(lc, Object.assign({ timeZone: "Asia/Bangkok" }, o))); };
  const loc = () => (lang === "en" ? "en-GB" : "th-TH");
  const dLong = (k, l) => df(l || loc(), { weekday: "long", day: "numeric", month: "long", year: "numeric" }).format(noon(k));
  const dShort = (k, l) => df(l || loc(), { weekday: "short", day: "numeric", month: "short" }).format(noon(k));
  const wd = (k) => df(loc(), { weekday: "short" }).format(noon(k));
  const mon = (k) => df(loc(), { month: "short" }).format(noon(k));
  const dt = (iso) => (iso ? df("th-TH", { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" }).format(new Date(iso)) : "");
  const fmtPhone = (p) => (/^0\d{9}$/.test(p || "") ? p.slice(0, 3) + "-" + p.slice(3, 6) + "-" + p.slice(6) : p || "");
  const thDays = ["อาทิตย์", "จันทร์", "อังคาร", "พุธ", "พฤหัสบดี", "ศุกร์", "เสาร์"], enDays = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];

  // ---------- Supabase (plain fetch, no library) ----------
  const SESSION_KEY = "pfd-staff-session";
  let session = store.get(SESSION_KEY);
  async function refreshSession() {
    if (!session || !session.refresh_token) return null;
    const r = await fetch(CFG.url + "/auth/v1/token?grant_type=refresh_token", { method: "POST", headers: { apikey: CFG.key, "Content-Type": "application/json" }, body: JSON.stringify({ refresh_token: session.refresh_token }) });
    if (!r.ok) { session = null; store.del(SESSION_KEY); return null; }
    const j = await r.json();
    session = { access_token: j.access_token, refresh_token: j.refresh_token, expires_at: Date.now() + j.expires_in * 1000, email: j.user && j.user.email };
    store.set(SESSION_KEY, session);
    return session;
  }
  async function token() {
    if (!session) return null;
    if (Date.now() > session.expires_at - 60e3) await refreshSession();
    return session && session.access_token;
  }
  // customers sign in with an email code; kept apart from the staff session
  const CUST_KEY = "pfd-customer-session";
  const ME_KEY = "pfd-me"; // the signed-in customer's name and phone, so the form fills itself next time
  let cust = store.get(CUST_KEY);
  const H = { apikey: CFG.key, "Content-Type": "application/json" };
  const keepCust = (j) => { cust = { access_token: j.access_token, refresh_token: j.refresh_token, expires_at: Date.now() + j.expires_in * 1000, email: (j.user && j.user.email) || (cust && cust.email) || "" }; store.set(CUST_KEY, cust); };
  async function custToken() {
    if (!cust) return null;
    if (Date.now() > cust.expires_at - 60e3) {
      const r = await fetch(CFG.url + "/auth/v1/token?grant_type=refresh_token", { method: "POST", headers: H, body: JSON.stringify({ refresh_token: cust.refresh_token }) });
      if (!r.ok) { cust = null; store.del(CUST_KEY); return null; }
      keepCust(await r.json());
    }
    return cust.access_token;
  }
  // links in customer emails come back with ?c=1 so they open the customer side, not the back office
  const custBack = () => encodeURIComponent(location.origin + location.pathname + "?c=1");
  const withCaptcha = (o) => (R.captcha ? Object.assign(o, { gotrue_meta_security: { captcha_token: R.captcha } }) : o);
  async function authCall(path, body) {
    const r = await fetch(CFG.url + "/auth/v1/" + path, { method: "POST", headers: H, body: JSON.stringify(withCaptcha(body)) });
    const j = await r.json().catch(() => ({}));
    if (!r.ok) throw new Error(j.error_code || j.msg || j.error_description || j.message || "auth");
    return j;
  }
  async function custSignIn(email, password) { keepCust(await authCall("token?grant_type=password", { email, password })); }
  async function custSignUp(email, password) {
    const j = await authCall("signup?redirect_to=" + custBack(), { email, password });
    if (j.access_token) { keepCust(j); return true; } // email confirmation is off
    return false; // a confirmation email was sent
  }
  async function custReset(email) { await authCall("recover?redirect_to=" + custBack(), { email }); }
  async function custSetPassword(password) {
    const r = await fetch(CFG.url + "/auth/v1/user", { method: "PUT", headers: Object.assign({ Authorization: "Bearer " + (await custToken()) }, H), body: JSON.stringify({ password }) });
    if (!r.ok) { const j = await r.json().catch(() => ({})); throw new Error(j.error_code || j.msg || j.message || "password"); }
  }
  function custOut() { cust = null; store.del(CUST_KEY); store.del(ME_KEY); }
  // bot check (Cloudflare Turnstile); skipped until a site key is set in config.js
  function mountCaptcha() {
    const el = document.getElementById("captcha");
    if (!el || !CFG.turnstile) return;
    const draw = () => { if (!window.turnstile) return setTimeout(draw, 300); try { window.turnstile.render(el, { sitekey: CFG.turnstile, language: lang, callback: (t) => (R.captcha = t), "expired-callback": () => (R.captcha = ""), "error-callback": () => (R.captcha = "") }); } catch (e) { /* already drawn */ } };
    R.captcha = ""; draw();
  }
  const needCaptcha = () => !!CFG.turnstile && !R.captcha;

  async function rpc(fn, args, asStaff) {
    const h = { apikey: CFG.key, "Content-Type": "application/json" };
    const t = asStaff === "cust" ? await custToken() : asStaff ? await token() : null;
    if (t) h.Authorization = "Bearer " + t;
    const r = await fetch(CFG.url + "/rest/v1/rpc/" + fn, { method: "POST", headers: h, body: JSON.stringify(args || {}) });
    const text = await r.text();
    const body = text ? JSON.parse(text) : null;
    if (!r.ok) { const e = new Error((body && body.message) || "error"); e.code = (body && body.message) || "error"; throw e; }
    return body;
  }
  async function signIn(email, password) {
    const body = { email, password };
    if (R.captcha) body.gotrue_meta_security = { captcha_token: R.captcha };
    const r = await fetch(CFG.url + "/auth/v1/token?grant_type=password", { method: "POST", headers: { apikey: CFG.key, "Content-Type": "application/json" }, body: JSON.stringify(body) });
    const j = await r.json();
    if (!r.ok) throw new Error(j.error_description || j.msg || j.message || "login");
    session = { access_token: j.access_token, refresh_token: j.refresh_token, expires_at: Date.now() + j.expires_in * 1000, email: j.user && j.user.email };
    store.set(SESSION_KEY, session);
  }
  async function signUp(email, password) {
    const r = await fetch(CFG.url + "/auth/v1/signup?redirect_to=" + encodeURIComponent(location.origin + location.pathname), { method: "POST", headers: { apikey: CFG.key, "Content-Type": "application/json" }, body: JSON.stringify(R.captcha ? { email, password, gotrue_meta_security: { captcha_token: R.captcha } } : { email, password }) });
    const j = await r.json();
    if (!r.ok) throw new Error(j.error_description || j.msg || j.message || "signup");
    return j;
  }
  async function resetPassword(email) {
    await fetch(CFG.url + "/auth/v1/recover?redirect_to=" + encodeURIComponent(location.origin + location.pathname), { method: "POST", headers: { apikey: CFG.key, "Content-Type": "application/json" }, body: JSON.stringify(R.captcha ? { email, gotrue_meta_security: { captcha_token: R.captcha } } : { email }) });
  }
  async function setPassword(password) {
    const r = await fetch(CFG.url + "/auth/v1/user", { method: "PUT", headers: { apikey: CFG.key, "Content-Type": "application/json", Authorization: "Bearer " + (await token()) }, body: JSON.stringify({ password }) });
    if (!r.ok) { const j = await r.json().catch(() => ({})); throw new Error(j.msg || j.message || "password"); }
  }
  function signOut() { session = null; store.del(SESSION_KEY); }
  // links from the sign-up and reset emails arrive with the session in the address
  (function readAuthRedirect() {
    const h = location.hash;
    if (h.includes("access_token=")) {
      const q = new URLSearchParams(h.slice(h.indexOf("#") + 1).replace(/^admin&?/, ""));
      const got = { access_token: q.get("access_token"), refresh_token: q.get("refresh_token"), expires_at: Date.now() + Number(q.get("expires_in") || 3600) * 1000, email: "" };
      if (/(^|[?&])c=1/.test(location.search) || q.get("type") === "magiclink") {
        // a customer opened the link in a confirmation or reset email
        try { got.email = JSON.parse(atob(got.access_token.split(".")[1].replace(/-/g, "+").replace(/_/g, "/"))).email || ""; } catch (e) { /* unreadable token */ }
        cust = got; store.set(CUST_KEY, got);
        window.__pfdCustRecovery = q.get("type") === "recovery";
        history.replaceState(null, "", location.pathname + (window.__pfdCustRecovery ? "#my" : "#book"));
      } else {
        session = got; store.set(SESSION_KEY, session);
        window.__pfdRecovery = q.get("type") === "recovery";
        history.replaceState(null, "", location.pathname + "#admin");
      }
    }
  })();

  const ERR = {
    name: ["กรุณากรอกชื่อ", "Please enter your name."],
    phone: ["เบอร์โทรไม่ถูกต้อง ใช้เบอร์ไทย 9–10 หลัก", "Please enter a valid Thai phone number."],
    email: ["อีเมลไม่ถูกต้อง", "That email address doesn't look right."],
    people: ["จำนวนคนไม่ถูกต้อง", "Please choose how many people are coming."],
    slot_full: ["รอบนี้ที่ไม่พอแล้ว กรุณาเลือกรอบอื่น ข้อมูลที่กรอกไว้ยังอยู่", "That session no longer has room. Please pick another; your details are kept."],
    slot_closed: ["รอบนี้ปิดรับจองแล้ว กรุณาเลือกรอบอื่น", "That session isn't open for booking. Please pick another."],
    too_soon: ["รอบนี้ใกล้เวลาเริ่มเกินไปสำหรับการจองออนไลน์ โทรหาเราได้เลย", "That session starts too soon to book online. Please call us."],
    duplicate: ["เบอร์นี้จองรอบนี้ไว้แล้ว ถ้าต้องการเพิ่มคนกรุณาโทรหาเรา", "This phone number already has a booking in that session. To add people, please call us."],
    too_many: ["เบอร์นี้มีการจองที่ยังไม่ถึงวันครบ 3 รายการแล้ว โทรหาเราได้ถ้าต้องการจองเพิ่ม", "This phone number already has 3 upcoming bookings. Please call us to book more."],
    change_closed: ["เลยกำหนดเปลี่ยนแปลงออนไลน์แล้ว กรุณาโทรหาเรา", "It's past the online change deadline. Please call us."],
    notfound: ["ไม่พบการจองนี้", "We couldn't find that booking."],
    forbidden: ["ตำแหน่งของคุณทำรายการนี้ไม่ได้", "Your role can't do this."],
    not_staff: ["บัญชีนี้ยังไม่ได้รับสิทธิ์เข้าหลังบ้าน", "This account doesn't have back-office access yet."],
    settings: ["ค่าที่ตั้งไม่ถูกต้อง", "Some settings aren't valid."],
    method: ["เลือกวิธีรับเงินก่อน", "Choose how the payment was made."],
    login: ["เข้าสู่ระบบก่อนจอง", "Please sign in first."],
  };
  const errText = (e) => { const m = ERR[e && e.code] || ERR[e && e.message]; return m ? P(m[0], m[1]) : P("ระบบขัดข้อง ลองใหม่อีกครั้ง", "Something went wrong. Please try again."); };

  // ---------- state ----------
  const R = { view: "home", flash: "", flashErr: "", busy: false, recovery: !!window.__pfdRecovery, custRecovery: !!window.__pfdCustRecovery };
  let C = null; // shop settings from the database
  // the 2-person promotion: each pair pays pair_price, anyone left over pays the normal price (same rule as the database)
  const pairOn = () => Number(C.pair_price) > 0;
  const priceFor = (n) => (pairOn() ? Math.floor(n / 2) * Number(C.pair_price) + (n % 2) * Number(C.price) : n * Number(C.price));
  const priceLine = (n) => (pairOn() && n >= 2 ? `${P("โปร 2 คน", "2-person deal")} ${baht(C.pair_price)}${Math.floor(n / 2) > 1 ? ` × ${Math.floor(n / 2)}` : ""}${n % 2 ? ` + ${baht(C.price)}` : ""}` : `${baht(C.price)} × ${n}`);
  const promoText = () => P(`โปร มา 2 คน ${baht(C.pair_price)} (ปกติ ${baht(C.price * 2)})`, `Come as two for ${baht(C.pair_price)} (usually ${baht(C.price * 2)})`);
  const BK = { people: 2, day: "", hm: "", name: "", phone: "", email: "", note: "", agree: false, health: false, err: "", fieldErr: {} };
  let ME = null; // signed-in staff member

  function go(view, extra) {
    Object.assign(R, { flash: "", flashErr: "" }, extra || {});
    R.view = view;
    try { history.pushState(null, "", "#" + view + (R.hashArg ? "/" + R.hashArg : "")); } catch (e) { /* ignore */ }
    R.hashArg = "";
    render();
    window.scrollTo(0, 0);
  }

  // ---------- icons & shared bits ----------
  const ICON = {
    clock: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" aria-hidden="true"><circle cx="12" cy="12" r="8.5"/><path d="M12 7.5V12l3 2"/></svg>',
    snow: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" aria-hidden="true"><path d="M12 3v18M4.2 7.5l15.6 9M4.2 16.5l15.6-9M9.5 4.5 12 7l2.5-2.5M9.5 19.5 12 17l2.5 2.5"/></svg>',
    flame: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round" aria-hidden="true"><path d="M12 21c-3.9 0-6.5-2.6-6.5-6.2 0-3.6 3-5.6 4-9.3 2.3 1.8 3.2 4.2 3 6.4 1-.6 1.8-1.7 2.1-3.2 2 1.9 3.9 4 3.9 6.4C18.5 18.6 15.9 21 12 21Z"/></svg>',
    leaf: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M5 19c0-8 5-13 14-14 0 9-5 14-13 14"/><path d="M5 19 13 11"/></svg>',
    people: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" aria-hidden="true"><circle cx="9" cy="8.5" r="3"/><path d="M3.5 19c.6-3.2 2.8-5 5.5-5s4.9 1.8 5.5 5"/><circle cx="16.8" cy="9.5" r="2.4"/><path d="M15.6 14.2c2.4-.2 4.3 1.4 4.9 4.3"/></svg>',
    train: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="6" y="3.5" width="12" height="13" rx="3"/><path d="M6 10.5h12M9.5 13.5h.01M14.5 13.5h.01M8.5 20l1.5-3.5M15.5 20 14 16.5"/></svg>',
    car: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M4.5 16.5V12l2-5h11l2 5v4.5"/><path d="M3.5 12h17v4.5h-17zM7 16.5V19M17 16.5V19"/></svg>',
    pin: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 21s-6.5-5.6-6.5-11a6.5 6.5 0 0 1 13 0c0 5.4-6.5 11-6.5 11Z"/><circle cx="12" cy="10" r="2.3"/></svg>',
    ok: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="m8 12.3 2.7 2.7L16.2 9.5"/></svg>',
    alert: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="M12 7.5v5.5M12 16.4h.01"/></svg>',
    info: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="M12 11v5.5M12 7.6h.01"/></svg>',
    menu: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" aria-hidden="true"><path d="M4 8h16M4 16h16"/></svg>',
  };
  const LOGO = `<img src="assets/img/logo-160.png" alt="" width="40" height="34" class="logo-img">`;
  const IMG = (n) => "assets/img/" + n;
  const flashHTML = () => (R.flash ? `<div class="alert alert--ok" role="status">${ICON.ok}<p>${esc(R.flash)}</p></div>` : "") + (R.flashErr ? `<div class="alert alert--bad" role="alert">${ICON.alert}<p>${esc(R.flashErr)}</p></div>` : "");
  function hoursLine() {
    const closed = (C.closed || []).slice().sort((a, b) => ((a + 6) % 7) - ((b + 6) % 7));
    const first = (C.times || [])[0] || "09:00", last = (C.times || []).slice(-1)[0] || "20:00";
    const end = String((parseInt(last, 10) * 60 + parseInt(last.slice(3), 10) + Number(C.duration)) / 60 | 0).padStart(2, "0") + ":" + String((parseInt(last.slice(3), 10) + Number(C.duration)) % 60).padStart(2, "0");
    if (!closed.length) return P(`เปิดทุกวัน ${first}–${end} น.`, `Open daily ${first}–${end}`);
    return P(`เปิด ${first}–${end} น. หยุดทุก${closed.map((d) => "วัน" + thDays[d]).join(" และ")}`, `Open ${first}–${end}, closed on ${closed.map((d) => enDays[d] + "s").join(", ")}`);
  }
  const policyLine = () => P(
    `ยกเลิกหรือเลื่อนรอบได้เองก่อนเวลาเริ่ม ${C.cancel_hours} ชั่วโมง (เลื่อนได้ ${C.max_moves} ครั้ง) ผ่านลิงก์ในหน้าการจองของคุณ หลังจากนั้นกรุณาโทร ${C.phone}`,
    `You can cancel or move your booking yourself up to ${C.cancel_hours} hours before it starts (one move per booking) from your booking page. After that, please call ${C.phone}.`);

  // ---------- public layout ----------
  function pubLayout(main, opts) {
    opts = opts || {};
    const nav = [["home#experience", P("ประสบการณ์", "Experience")], ["book", P("ราคาและจอง", "Prices & booking")], ["home#first", P("มาครั้งแรก", "First visit")], ["home#visit", P("การเดินทาง", "Getting here")], ["my", P("การจองของฉัน", "My booking")]];
    return `<div class="pub pfd-home${opts.paybar ? " has-paybar" : ""}">
<a class="skip" href="#main">${P("ข้ามไปเนื้อหา", "Skip to content")}</a>
<header class="site-header"><div class="wrap">
  <a class="brand" href="#home" data-go="home" aria-label="Peaceful Day">${LOGO}<span>Peaceful Day</span></a>
  <nav class="nav" aria-label="${P("เมนูหลัก", "Main")}">${nav.map(([v, l]) => `<a href="#${v}" data-go="${v}">${l}</a>`).join("")}</nav>
  <div class="header-actions">
    <a class="lang" href="#" data-lang>${P("English", "ภาษาไทย")}</a>
    <a class="btn btn--primary btn--small" href="#book" data-go="book">${P("จองรอบ", "Book")}</a>
    <button class="menu-btn" type="button" aria-expanded="false" aria-controls="mobile-nav" data-menu><span class="sr-only">${P("เมนู", "Menu")}</span>${ICON.menu}</button>
  </div></div>
  <nav class="mobile-nav" id="mobile-nav" aria-label="${P("เมนู", "Menu")}"><ul>${nav.map(([v, l]) => `<li><a href="#${v}" data-go="${v}">${l}</a></li>`).join("")}<li><a href="#" data-lang>${P("English", "ภาษาไทย")}</a></li></ul></nav>
</header>
<main id="main">${main}</main>
<footer class="site-footer"><div class="wrap">
  <div class="foot-grid">
    <p class="foot-brand">${P("แช่เย็น อบร้อน แล้วพักในสวน", "Cold plunge, warm sauna, a quiet garden.")}</p>
    <div class="foot-col"><h2>${P("ที่ร้าน", "Visit")}</h2><p>${esc(P(C.address_th, C.address_en))}</p><p>${esc(hoursLine())}</p><p><a href="${esc(C.map_url)}" target="_blank" rel="noopener">${P("เปิดในแผนที่", "Open in Maps")}</a></p></div>
    <div class="foot-col"><h2>${P("ติดต่อ", "Contact")}</h2><p>${P("โทร", "Call")} <a href="tel:${esc((C.phone || "").replace(/\D/g, ""))}">${esc(C.phone)}</a></p>${C.line ? `<p>LINE ${esc(C.line)}</p>` : ""}</div>
    <div class="foot-col"><h2>${P("การจอง", "Bookings")}</h2><ul><li><a href="#book" data-go="book">${P("จองรอบ", "Book a session")}</a></li><li><a href="#my" data-go="my">${P("ดูหรือแก้การจอง", "See or change a booking")}</a></li><li><a href="#admin" data-go="admin">${P("สำหรับทีมงาน", "Staff")}</a></li></ul></div>
  </div>
  <div class="foot-base"><span>© ${new Date().getFullYear()} Peaceful Day</span><a href="#" data-lang>${P("English", "ภาษาไทย")}</a></div>
</div></footer>${opts.after || ""}</div>`;
  }

  // ---------- home: the eight parts from the review ----------
  async function viewHome() {
    let week = [];
    try { week = await rpc("availability", { p_from: today(), p_days: 7, p_people: 1 }); } catch (e) { week = []; }
    const next = [];
    week.forEach((d) => d.slots.forEach((s) => { if (s.ok && next.length < 6) next.push({ day: d.day, hm: s.hm, remaining: s.remaining }); }));
    const rel = (k) => (k === today() ? P("วันนี้", "Today") : k === addDays(today(), 1) ? P("พรุ่งนี้", "Tomorrow") : dShort(k));
    const faq = faqItems();
    return pubLayout(`
<section class="hero" aria-labelledby="hero-title"><div class="wrap">
  <div class="hero-copy">
    <p class="eyebrow"><span></span>Ice Bath + Sauna · ${P("สุขุมวิท 62", "Sukhumvit 62")}</p>
    <h1 id="hero-title">${P('<span class="line">วันนี้</span><span class="line">แค่มาพัก</span>', '<span class="line">Today,</span><span class="line">just come and rest.</span>')}</h1>
    <p class="lede">${P("แช่น้ำเย็น อบซาวน่า แล้วพักหายใจช้า ๆ ในสวนกลางสุขุมวิท 62 มาครั้งแรกก็ได้ ไม่ต้องฝืนตัวเอง", "A cold plunge, a warm sauna and slow breathing in a garden off Sukhumvit 62. First-timers welcome; go at your own pace.")}</p>
    <p class="hero-price"><strong>${baht(C.price)}</strong>${P(`ต่อคน ใช้บริการได้ ${C.duration / 60} ชั่วโมง`, `per person, ${C.duration / 60} hours`)}</p>${pairOn() ? `<p class="hero-promo">${promoText()}</p>` : ""}
    <div class="hero-cta"><a class="btn btn--primary" href="#book" data-go="book">${P("ดูรอบว่างและจอง", "See times and book")}</a><a class="btn" href="#first" data-go="home#first">${P("มาครั้งแรกทำยังไง", "First time here?")}</a></div>
    <p class="hero-hours">${ICON.clock}<span>${esc(hoursLine())}</span></p>
    <div class="next"><h2>${P("รอบว่างถัดไป", "Next open sessions")}</h2>
      ${next.length ? `<ul class="chips">${next.map((s) => `<li><a class="chip" href="#book" data-book="${s.day}|${s.hm}"><span>${esc(rel(s.day))}</span><b>${s.hm}</b><em>${P("ว่าง " + s.remaining + " ที่", s.remaining + " spots")}</em></a></li>`).join("")}<li><a class="chip chip--more" href="#book" data-go="book">${P("ดูวันอื่น", "More days")}</a></li></ul>` : `<p class="muted">${P("ตอนนี้ยังไม่มีรอบว่างในสัปดาห์นี้ ลองดูวันถัดไปในหน้าจอง", "No open sessions this week. Try later dates on the booking page.")}</p>`}
    </div>
  </div>
  <div class="hero-media"><div class="pebble"><img src="${IMG("hero.jpg")}" alt="${P("อ่างไม้ใต้ร่มลายเหลืองในสวนของร้าน", "Wooden tubs under yellow-striped umbrellas in the garden")}" fetchpriority="high"></div></div>
</div></section>

<section class="section" id="included" aria-labelledby="inc-title"><div class="wrap">
  <div class="section-head"><p class="eyebrow"><span></span>${P("ในหนึ่งรอบ", "In one session")}</p><h2 id="inc-title">${P(`${C.duration / 60} ชั่วโมง ใช้ได้ทั้งสามมุม`, `${C.duration / 60} hours, all three spaces`)}</h2></div>
  <ul class="why">
    <li><span class="ico">${ICON.snow}</span><div><h3>Ice Bath</h3><p>${P("อ่างน้ำเย็นในสวน แช่สั้น ๆ ตามที่ร่างกายไหว ทีมงานบอกวิธีให้ก่อนเริ่ม", "Cold tubs in the garden. Short dips at your own pace; the team shows you how first.")}</p></div></li>
    <li><span class="ico">${ICON.flame}</span><div><h3>Sauna</h3><p>${P("อบอุ่นสลับกับแช่เย็นได้ตลอดรอบ", "Warm up between cold dips as often as you like during the session.")}</p></div></li>
    <li><span class="ico">${ICON.leaf}</span><div><h3>${P("สวนสำหรับพัก", "Garden to rest")}</h3><p>${P(`นั่งพักหายใจในสวนหลังแช่ รับรอบละไม่เกิน ${C.capacity} คน (เสาร์-อาทิตย์และวันหยุดนักขัตฯ ${C.capacity_weekend || C.capacity} คน) ไม่แออัด`, `Sit and breathe in the garden afterwards. Each session takes up to ${C.capacity} people (${C.capacity_weekend || C.capacity} on weekends and public holidays).`)}</p></div></li>
  </ul>
</div></section>

<section class="section section--tint" id="experience" aria-labelledby="exp-title"><div class="wrap">
  <div class="section-head"><p class="eyebrow"><span></span>${P("บรรยากาศจริง", "The place")}</p><h2 id="exp-title">${P("อ่างน้ำเย็น ซาวน่า และสวน", "Cold tubs, sauna and garden")}</h2></div>
  <div class="trio">
    <figure><img src="${IMG("icebath.jpg")}" alt="${P("อ่างไม้เต็มไปด้วยน้ำแข็ง", "A wooden tub full of ice")}" loading="lazy"><figcaption>Ice Bath</figcaption></figure>
    <figure><img src="${IMG("garden.jpg")}" alt="${P("สวนหน้าบ้านพร้อมเก้าอี้อาบแดดและร่มลายเหลือง", "The garden with sun loungers and yellow umbrellas")}" loading="lazy"><figcaption>${P("สวน", "Garden")}</figcaption></figure>
    <figure><img src="${IMG("evening.jpg")}" alt="${P("ตัวบ้านยามเย็นเมื่อเปิดไฟประดับ", "The house in the evening with string lights")}" loading="lazy"><figcaption>${P("ยามเย็น", "Evening")}</figcaption></figure>
  </div>
</div></section>

<section class="section" id="first" aria-labelledby="first-title"><div class="wrap">
  <div class="section-head"><p class="eyebrow"><span></span>${P("มาครั้งแรก", "First visit")}</p><h2 id="first-title">${P("มาครั้งแรกทำแบบนี้", "How your first visit goes")}</h2></div>
  <ol class="steps">
    <li><div><h3>${P("จองรอบออนไลน์", "Book online")}</h3><p>${P("เลือกจำนวนคน วัน และเวลา แล้วชำระเงินที่ร้านตอนมาถึง", "Choose how many people, the day and time. You pay at the shop when you arrive.")}</p></div></li>
    <li><div><h3>${P("มาก่อนเวลา 10–15 นาที", "Arrive 10–15 minutes early")}</h3><p>${P("เตรียมชุดว่ายน้ำหรือชุดที่เปียกได้ ทีมงานจะอธิบายวิธีแช่ให้", "Bring swimwear or clothes that can get wet. The team will walk you through it.")}</p></div></li>
    <li><div><h3>${P("แช่เย็น สลับอบซาวน่า", "Cold, then warm")}</h3><p>${P("เริ่มสั้น ๆ ไม่ต้องฝืน ร่างกายบอกให้พักเมื่อไหร่ก็พัก", "Start short and don't force it. Rest whenever your body asks.")}</p></div></li>
    <li><div><h3>${P("พักในสวนก่อนกลับ", "Rest in the garden")}</h3><p>${P("นั่งหายใจช้า ๆ ให้ร่างกายปรับตัวก่อนกลับ", "Sit, breathe slowly and let your body settle before you go.")}</p></div></li>
  </ol>
</div></section>

<section class="section section--sage" id="price" aria-labelledby="price-title"><div class="wrap">
  <div class="section-head"><p class="eyebrow"><span></span>${P("ราคา", "Price")}</p><h2 id="price-title">${P("ราคาเดียว ชัดเจน", "One clear price")}</h2></div>
  <div class="price-grid"><article class="price price--feature">
    <h3>${P("Ice Bath + Sauna 1 รอบ", "Ice Bath + Sauna, one session")}</h3>
    <p class="price-amount">${baht(C.price)}<small>${P(" / คน", " / person")}</small></p>
    ${pairOn() ? `<p class="price-promo"><b>${P("โปร 2 คน", "2-person deal")} ${baht(C.pair_price)}</b> ${P(`ปกติ ${baht(C.price * 2)} มากี่คู่ก็ได้ราคานี้ทุกคู่`, `instead of ${baht(C.price * 2)}, for every pair in your group`)}</p>` : ""}
    <p class="price-note">${P(`ใช้บริการได้ ${C.duration / 60} ชั่วโมง เริ่มรอบใหม่ทุกชั่วโมง ชำระที่ร้าน`, `${C.duration / 60} hours from your start time; sessions start every hour. Pay at the shop.`)}</p>
    <a class="btn btn--primary" href="#book" data-go="book">${P("เลือกจำนวนคนและดูรอบว่าง", "Choose your group and see times")}</a>
  </article></div>
</div></section>

<section class="section" id="faq" aria-labelledby="faq-title"><div class="wrap">
  <div class="section-head"><p class="eyebrow"><span></span>FAQ</p><h2 id="faq-title">${P("คำถามก่อนจอง", "Before you book")}</h2></div>
  <div class="faq">${faq.map(([q, a]) => `<details><summary>${esc(q)}</summary><div class="ans"><p>${esc(a)}</p></div></details>`).join("")}</div>
</div></section>

<section class="section section--tint" id="visit" aria-labelledby="loc-title"><div class="wrap loc">
  <div><div class="section-head"><p class="eyebrow"><span></span>${P("การเดินทาง", "Getting here")}</p><h2 id="loc-title">${esc(P(C.address_th, C.address_en))}</h2></div>
  <ul class="dir">
    <li><span class="ico">${ICON.train}</span><div><h3>BTS</h3><p>${P("ลงสถานีบางจาก ทางออก 2 เดินเข้าซอยสุขุมวิท 62 ประมาณ 5 นาที", "Bang Chak station, Exit 2, then about 5 minutes' walk into Sukhumvit 62.")}</p></div></li>
    <li><span class="ico">${ICON.car}</span><div><h3>${P("มาด้วยรถ", "By car")}</h3><p>${P("ที่จอดมีจำกัด โทรถามก่อนมาได้", "Parking is limited; call ahead if you're driving.")}</p></div></li>
    <li><span class="ico">${ICON.clock}</span><div><h3>${P("เวลาเปิด", "Hours")}</h3><p>${esc(hoursLine())}</p></div></li>
  </ul>
  <div class="loc-actions"><a class="btn btn--primary" href="${esc(C.map_url)}" target="_blank" rel="noopener">${ICON.pin}${P("เปิดในแผนที่", "Open in Maps")}</a><a class="btn" href="tel:${esc((C.phone || "").replace(/\D/g, ""))}">${P("โทร", "Call")} ${esc(C.phone)}</a></div></div>
  <div class="map"><div class="map-placeholder"><img src="${IMG("map-sketch.svg")}" alt=""><a class="btn" href="${esc(C.map_url)}" target="_blank" rel="noopener">${P("เปิด Google Maps", "Open Google Maps")}</a></div></div>
</div></section>`, { paybar: true, after: `<div class="paybar paybar--home"><div class="tot"><small>${P("ต่อคน ต่อรอบ", "per person")}</small><b>${baht(C.price)}</b></div><a class="btn btn--primary" href="#book" data-go="book">${P("ดูรอบว่าง", "See times")}</a></div>` });
  }
  function faqItems() {
    return [
      [P("ไม่เคยแช่น้ำเย็นมาก่อน มาได้ไหม", "I've never done an ice bath. Can I come?"), P("ได้ ครั้งแรกเริ่มแช่สั้น ๆ ตามที่ร่างกายไหว ทีมงานอธิบายวิธีให้ก่อนเริ่ม", "Yes. Start with short dips at your own pace; the team explains everything first.")],
      [P("ต้องเตรียมอะไรมา", "What should I bring?"), P("ชุดว่ายน้ำหรือชุดที่เปียกได้ และมาก่อนเวลา 10–15 นาที", "Swimwear or clothes that can get wet. Please arrive 10–15 minutes early.")],
      [P(`${baht(C.price)} รวมอะไรบ้าง`, `What does ${baht(C.price)} include?`), P(`Ice Bath และ Sauna ใช้ได้ ${C.duration / 60} ชั่วโมงนับจากเวลาที่จอง ต่อคน${pairOn() ? ` มา 2 คนจ่าย ${baht(C.pair_price)}` : ""}`, `The ice bath and sauna for ${C.duration / 60} hours from your booked time, per person.${pairOn() ? ` Two people pay ${baht(C.pair_price)}.` : ""}`)],
      [P("จ่ายเงินยังไง", "How do I pay?"), P("จองออนไลน์แล้วชำระที่ร้านตอนมาถึง รับเงินสด โอน และบัตร", "Book online and pay at the shop when you arrive: cash, transfer or card.")],
      [P("มากันเป็นกลุ่มได้ไหม", "Can we come as a group?"), P(`ได้ รับรอบละไม่เกิน ${C.capacity} คน (เสาร์-อาทิตย์และวันหยุดนักขัตฯ ${C.capacity_weekend || C.capacity} คน) เลือกจำนวนคนตอนจอง ถ้าต้องการจองทั้งรอบหรือกลุ่มใหญ่ โทรหาเรา`, `Yes. Each session takes up to ${C.capacity} people (${C.capacity_weekend || C.capacity} on weekends and public holidays). For a private session or a bigger group, please call us.`)],
      [P("เลื่อนหรือยกเลิกได้ไหม", "Can I move or cancel?"), policyLine()],
      [P("มีข้อควรระวังด้านสุขภาพไหม", "Any health precautions?"), P("การแช่น้ำเย็นและซาวน่าอาจไม่เหมาะกับผู้มีโรคหัวใจ ความดันสูงที่ยังคุมไม่ได้ หรือผู้ตั้งครรภ์ ถ้าไม่แน่ใจกรุณาปรึกษาแพทย์ก่อน และบอกทีมงานเมื่อมาถึง", "Cold water and sauna may not suit people with heart conditions, uncontrolled high blood pressure, or who are pregnant. If unsure, ask your doctor first and tell our team when you arrive.")],
    ];
  }

  // ---------- customer sign-in: email + password (sign up once, then sign in) ----------
  function loginPanel(note) {
    const m = R.authMode || "in";
    if (R.authSent) return `<div class="login-box" id="otp-box"><p>${R.authSent}</p><p class="muted small">${P("ถ้าไม่เห็นอีเมล ลองดูในอีเมลขยะ", "Check spam if you don't see it.")}</p><button class="btn btn--ghost" type="button" data-auth-mode="in">${P("กลับไปหน้าเข้าสู่ระบบ", "Back to sign in")}</button></div>`;
    const tab = (k, th, en) => `<button type="button" class="chip${m === k ? " on" : ""}" data-auth-mode="${k}" aria-pressed="${m === k}">${P(th, en)}</button>`;
    return `<div class="login-box" id="otp-box">${note ? `<p>${note}</p>` : ""}
      ${m === "forgot" ? `<p>${P("ใส่อีเมลที่ใช้สมัคร เราจะส่งลิงก์ตั้งรหัสผ่านใหม่ไปให้", "Enter your email and we'll send a link to set a new password.")}</p>` : `<div class="btn-row auth-tabs">${tab("in", "เข้าสู่ระบบ", "Sign in")}${tab("up", "สมัครใหม่", "New account")}</div>`}
      <div id="auth-form">
      <div class="field"><label for="au-email">${P("อีเมล", "Email")}</label><input id="au-email" type="email" autocomplete="email" maxlength="254" value="${esc(R.otpTyped || "")}"></div>
      ${m === "forgot" ? "" : `<div class="field"><label for="au-pass">${P("รหัสผ่าน", "Password")}${m === "up" ? ` <span class="muted small">${P("(อย่างน้อย 8 ตัว)", "(at least 8 characters)")}</span>` : ""}</label><input id="au-pass" type="password" minlength="8" maxlength="72" autocomplete="${m === "up" ? "new-password" : "current-password"}"></div>`}
      <div id="captcha" class="captcha"></div>
      <button class="btn btn--primary" type="button" data-auth-go>${m === "up" ? P("สมัครและเข้าสู่ระบบ", "Create account") : m === "forgot" ? P("ส่งลิงก์ตั้งรหัสใหม่", "Send reset link") : P("เข้าสู่ระบบ", "Sign in")}</button>
      </div>
      ${m === "in" ? `<p class="small"><a href="#" data-auth-mode="forgot">${P("ลืมรหัสผ่าน", "Forgot password?")}</a></p>` : m === "forgot" ? `<p class="small"><a href="#" data-auth-mode="in">${P("กลับไปหน้าเข้าสู่ระบบ", "Back to sign in")}</a></p>` : ""}</div>`;
  }
  function authErr(msg) {
    if (/invalid_credentials|invalid login/i.test(msg)) return P("อีเมลหรือรหัสผ่านไม่ถูกต้อง", "Wrong email or password.");
    if (/email_not_confirmed|not confirmed/i.test(msg)) return P("ยังไม่ได้ยืนยันอีเมล กดลิงก์ในอีเมลที่เราส่งไปตอนสมัครก่อน", "Please confirm your email first using the link we sent when you signed up.");
    if (/user_already_exists|already registered/i.test(msg)) return P("อีเมลนี้สมัครไว้แล้ว ลองเข้าสู่ระบบ หรือกดลืมรหัสผ่าน", "That email already has an account. Sign in, or use Forgot password.");
    if (/weak_password|password/i.test(msg)) return P("รหัสผ่านสั้นหรือง่ายเกินไป ใช้อย่างน้อย 8 ตัว", "That password is too short or too simple. Use at least 8 characters.");
    if (/rate|seconds|many/i.test(msg)) return P("ทำรายการถี่เกินไป รอสักครู่แล้วลองใหม่", "Too many tries. Please wait a moment and try again.");
    if (/captcha/i.test(msg)) return P("ตรวจสอบว่าไม่ใช่บอทไม่ผ่าน ลองใหม่อีกครั้ง", "The robot check didn't pass. Please try again.");
    return P("ทำรายการไม่สำเร็จ ลองใหม่อีกครั้ง", "That didn't work. Please try again.");
  }
  let pwKeep = ""; // typed password survives a re-render for an error message; never stored
  function bindLoginPanel(after) {
    mountCaptcha();
    if ($("#au-pass") && pwKeep) $("#au-pass").value = pwKeep;
    $$("[data-auth-mode]").forEach((b) => b.addEventListener("click", (e) => { e.preventDefault(); const em = $("#au-email"); if (em) R.otpTyped = em.value.trim(); R.authMode = b.dataset.authMode; R.authSent = ""; pwKeep = ""; R.flashErr = ""; render(); }));
    const form = $("#auth-form");
    if (!form) return;
    form.addEventListener("keydown", (e) => { if (e.key === "Enter" && e.target.tagName === "INPUT") { e.preventDefault(); $("[data-auth-go]").click(); } });
    $("[data-auth-go]").addEventListener("click", async (e) => {
      e.preventDefault();
      const m = R.authMode || "in";
      const em = $("#au-email").value.trim().toLowerCase(); R.otpTyped = em;
      const pw = $("#au-pass") ? $("#au-pass").value : ""; pwKeep = pw;
      if (!/^[^\s@]+@[^\s@]+\.[^\s@]{2,}$/.test(em)) { R.flashErr = errText({ code: "email" }); render(); return; }
      if (m !== "forgot" && pw.length < (m === "up" ? 8 : 1)) { R.flashErr = m === "up" ? P("ตั้งรหัสผ่านอย่างน้อย 8 ตัว", "Use a password of at least 8 characters.") : P("ใส่รหัสผ่าน", "Please enter your password."); render(); return; }
      if (needCaptcha()) { R.flashErr = P("รอให้ช่องตรวจสอบว่าไม่ใช่บอทขึ้นเครื่องหมายถูกก่อน", "Please wait for the robot check to finish."); render(); return; }
      const go = $("[data-auth-go]"); go.disabled = true;
      try {
        if (m === "forgot") { await custReset(em); R.authSent = P(`ถ้าอีเมล ${esc(em)} มีบัญชีอยู่ เราส่งลิงก์ตั้งรหัสผ่านใหม่ไปแล้ว`, `If ${esc(em)} has an account, we've sent a link to set a new password.`); R.flashErr = ""; render(); return; }
        if (m === "up") {
          const inNow = await custSignUp(em, pw);
          if (!inNow) { R.authSent = P(`สมัครแล้ว กดลิงก์ยืนยันในอีเมลที่ส่งไปที่ ${esc(em)} แล้วจะเข้าสู่ระบบให้อัตโนมัติ`, `Account created. Open the confirmation link we sent to ${esc(em)} and you'll be signed in.`); R.flashErr = ""; render(); return; }
        } else await custSignIn(em, pw);
        R.flashErr = ""; R.authMode = "in"; pwKeep = "";
        try { const mb = await rpc("my_bookings", {}, "cust"); if (mb && mb.last) { BK.name = BK.name || mb.last.name; BK.phone = BK.phone || mb.last.phone; } } catch (err) { /* first booking */ }
        if (after) after(); render();
      } catch (err) { R.flashErr = authErr(err.message); render(); }
    });
  }
  function newPasswordPanel() {
    return `<div class="panel" style="max-width:520px"><h2 style="font-size:var(--step-2);margin-bottom:12px">${P("ตั้งรหัสผ่านใหม่", "Set a new password")}</h2>
      <form id="np-form" class="login-box" novalidate><div class="field"><label for="np-pass">${P("รหัสผ่านใหม่ (อย่างน้อย 8 ตัว)", "New password (at least 8 characters)")}</label><input id="np-pass" type="password" minlength="8" maxlength="72" autocomplete="new-password"></div>
      <button class="btn btn--primary" type="submit">${P("บันทึกรหัสผ่าน", "Save password")}</button></form></div>`;
  }
  function bindNewPassword() {
    const f = $("#np-form"); if (!f) return;
    f.addEventListener("submit", async (e) => {
      e.preventDefault();
      const pw = $("#np-pass").value;
      if (pw.length < 8) { R.flashErr = P("ตั้งรหัสผ่านอย่างน้อย 8 ตัว", "Use a password of at least 8 characters."); render(); return; }
      try { await custSetPassword(pw); R.custRecovery = false; R.flashErr = ""; R.flash = P("ตั้งรหัสผ่านใหม่แล้ว", "Your new password is saved."); }
      catch (err) { R.flashErr = authErr(err.message); }
      render();
    });
  }
  const signedInLine = () => `<p class="signed-in">${P("เข้าสู่ระบบเป็น", "Signed in as")} <b>${esc(cust.email)}</b> · <a href="#" data-cust-out>${P("ออกจากระบบ", "Sign out")}</a></p>`;

  // ---------- booking: people → day → time → details → confirm ----------
  async function viewBook() {
    if (R.pre) { const [d, h] = R.pre.split("|"); BK.day = d; BK.hm = h; R.pre = ""; }
    if (cust && !BK.name && !BK.phone) {
      const me = store.get(ME_KEY);
      if (me && me.email === cust.email) { BK.name = me.name || ""; BK.phone = me.phone || ""; }
      else if (!R.meTried) { R.meTried = true; try { const mb = await rpc("my_bookings", {}, "cust"); if (mb && mb.last) { BK.name = mb.last.name || ""; BK.phone = mb.last.phone || ""; } } catch (e) { /* first booking */ } }
    }
    const from = today(), days = Math.min(Number(C.horizon_days) + 1, 42);
    let avail = [];
    try { avail = await rpc("availability", { p_from: from, p_days: days, p_people: BK.people }); } catch (e) { R.flashErr = errText(e); }
    const map = {}; avail.forEach((d) => (map[d.day] = d));
    const dayState = (k) => { const d = map[k]; if (!d || !d.slots.length) return "closed"; return d.slots.some((s) => s.ok) ? "open" : "full"; };
    const list = Array.from({ length: days }, (_, i) => addDays(from, i));
    if (!BK.day || dayState(BK.day) === "closed") BK.day = list.find((k) => dayState(k) === "open") || "";
    const slots = BK.day && map[BK.day] ? map[BK.day].slots : [];
    if (!slots.some((s) => s.hm === BK.hm && s.ok)) BK.hm = "";
    const total = priceFor(BK.people);
    const fe = BK.fieldErr;
    const field = (k, label, input) => `<div class="field${fe[k] ? " has-error" : ""}"><label for="f-${k}">${label}</label>${input}${fe[k] ? `<span class="err">${esc(fe[k])}</span>` : ""}</div>`;
    const main = `
<header class="page-head"><div class="wrap"><h1>${P("จองรอบของคุณ", "Book your session")}</h1><p class="lede">${P(`${baht(C.price)} ต่อคน${pairOn() ? ` · 2 คน ${baht(C.pair_price)}` : ""} · ใช้บริการได้ ${C.duration / 60} ชั่วโมง · ชำระที่ร้าน`, `${baht(C.price)} per person${pairOn() ? ` · 2 people ${baht(C.pair_price)}` : ""} · ${C.duration / 60} hours · pay at the shop`)}</p></div></header>
<section style="padding-bottom:40px"><div class="wrap">
  ${flashHTML()}${BK.err ? `<div class="alert alert--bad" role="alert">${ICON.alert}<p>${esc(BK.err)}</p></div>` : ""}
  <form class="book" id="book" novalidate>
    <div class="book-main">
      <div class="step"><div class="step-title"><span class="num" aria-hidden="true"></span><h2>${P("มากี่คน", "How many people?")}</h2></div>
        <div class="people-pick" role="group" aria-label="${P("จำนวนคน", "Number of people")}">${Array.from({ length: Number(C.max_group) }, (_, i) => i + 1).map((n) => `<button type="button" class="time" data-people="${n}" aria-pressed="${n === BK.people}"><b>${n}</b><span>${P("คน", n > 1 ? "people" : "person")}</span></button>`).join("")}</div>
        <p class="hint">${P("เราจะแสดงเฉพาะรอบที่ยังว่างพอสำหรับกลุ่มของคุณ", "We only show sessions with room for your whole group.")}</p>
      </div>
      <div class="step"><div class="step-title"><span class="num" aria-hidden="true"></span><h2>${P("เลือกวัน", "Choose a day")}</h2></div>
        <div class="dates" role="group" aria-label="${P("วันที่", "Date")}">${list.map((k) => { const st = dayState(k); return `<button type="button" class="date" data-day="${k}" data-status="${st}" aria-pressed="${k === BK.day}"${st === "closed" ? " disabled" : ""} aria-label="${esc(dShort(k))}"><span class="w">${esc(wd(k))}</span><span class="n">${Number(k.slice(8))}</span><span class="m">${esc(mon(k))}</span><span class="a" aria-hidden="true"></span></button>`; }).join("")}</div>
      </div>
      <div class="step"><div class="step-title"><span class="num" aria-hidden="true"></span><h2>${P("เลือกเวลา", "Choose a time")}</h2></div>
        ${!BK.day ? `<p class="state">${P("ช่วงนี้ไม่มีรอบที่ว่างพอสำหรับกลุ่มนี้ ลองลดจำนวนคนหรือโทรหาเรา", "No sessions have room for this group right now. Try fewer people or call us.")}</p>` :
          !slots.some((s) => s.ok) ? `<p class="state">${P("วันนี้ไม่มีรอบที่ว่างพอสำหรับกลุ่มนี้ ลองวันอื่น", "No session that day has room for your group. Try another day.")}</p>` :
          `<div class="times" role="group">${slots.map((s) => `<button type="button" class="time" data-hm="${s.hm}" aria-pressed="${s.hm === BK.hm}"${s.ok ? "" : " disabled"}><b>${s.hm}</b><span class="${s.remaining > 0 && s.remaining <= 2 ? "low" : ""}">${s.remaining > 0 ? P("ว่าง " + s.remaining, s.remaining + " left") : P("เต็ม", "Full")}</span></button>`).join("")}</div>`}
      </div>
      <div class="step"><div class="step-title"><span class="num" aria-hidden="true"></span><h2>${P("ข้อมูลผู้จอง", "Your details")}</h2></div>
        ${!cust ? loginPanel(P("เข้าสู่ระบบก่อนจอง ครั้งแรกกด สมัครใหม่ ใช้อีเมลและตั้งรหัสผ่าน", "Sign in to book. First time? Choose New account and set a password.")) : `${signedInLine()}
        ${field("name", P("ชื่อ", "Name"), `<input type="text" id="f-name" data-bk="name" autocomplete="name" maxlength="120" value="${esc(BK.name)}">`)}
        <div class="row-2">
          ${field("phone", P("เบอร์โทร", "Phone"), `<input type="tel" id="f-phone" data-bk="phone" autocomplete="tel" inputmode="tel" maxlength="20" placeholder="08x-xxx-xxxx" value="${esc(BK.phone)}">`)}
        </div>
        <div class="field"><label for="f-note">${P("แจ้งทีมงาน (ไม่บังคับ)", "Anything we should know? (optional)")}</label><textarea id="f-note" data-bk="note" maxlength="500" rows="2">${esc(BK.note)}</textarea></div>
        <label class="check${fe.health ? " has-error" : ""}"><input type="checkbox" id="f-health" data-bkc="health"${BK.health ? " checked" : ""}><span>${P("ฉันอ่านข้อควรระวังด้านสุขภาพแล้ว และจะแจ้งทีมงานถ้ามีโรคประจำตัว", "I've read the health note and will tell the team about any medical condition.")}</span></label>
        <label class="check${fe.agree ? " has-error" : ""}"><input type="checkbox" id="f-agree" data-bkc="agree"${BK.agree ? " checked" : ""}><span>${esc(P("ฉันรับทราบเงื่อนไข: ", "I accept the terms: ") + policyLine())}</span></label>`}
      </div>
    </div>
    <aside class="book-side"><div class="summary">
      <h2>${P("สรุปการจอง", "Summary")}</h2>
      <dl class="sum-lines">
        <div><dt>${P("บริการ", "Service")}</dt><dd>Ice Bath + Sauna</dd></div>
        <div><dt>${P("จำนวน", "People")}</dt><dd>${BK.people} ${P("คน", "")}</dd></div>
        <div><dt>${P("วัน", "Day")}</dt><dd>${BK.day ? esc(dLong(BK.day)) : "–"}</dd></div>
        <div><dt>${P("เวลา", "Time")}</dt><dd>${BK.hm || "–"}</dd></div>
        <div><dt>${P("ราคา", "Price")}</dt><dd>${priceLine(BK.people)}</dd></div>
      </dl>
      <div class="sum-total"><span>${P("ยอดชำระที่ร้าน", "Pay at the shop")}</span><b>${baht(total)}</b></div>
      <button class="btn btn--primary btn--block submit-desktop" type="submit"${R.busy ? " disabled" : ""}>${P("ยืนยันการจอง", "Confirm booking")}</button>
      <p class="paynote">${ICON.info}<span>${P("ยังไม่ต้องจ่ายตอนนี้ ชำระที่ร้านด้วยเงินสด โอน หรือบัตร", "Nothing to pay now. Pay at the shop by cash, transfer or card.")}</span></p>
    </div></aside>
  </form>
</div></section>`;
    return pubLayout(main, { paybar: true, after: `<div class="paybar"><div class="tot"><small>${BK.people} ${P("คน", "people")}${BK.hm ? " · " + BK.hm : ""}</small><b>${baht(total)}</b></div><button class="btn btn--primary" type="submit" form="book"${R.busy ? " disabled" : ""}>${P("ยืนยันการจอง", "Confirm booking")}</button></div>` });
  }
  function bindBook() {
    $$("[data-people]").forEach((b) => b.addEventListener("click", () => { BK.people = Number(b.dataset.people); BK.err = ""; render(); }));
    $$(".date").forEach((b) => b.addEventListener("click", () => { BK.day = b.dataset.day; BK.hm = ""; BK.err = ""; render(); }));
    $$("[data-hm]").forEach((b) => b.addEventListener("click", () => { BK.hm = b.dataset.hm; BK.err = ""; render(); }));
    $$("[data-bk]").forEach((i) => i.addEventListener("input", () => { BK[i.dataset.bk] = i.value; }));
    $$("[data-bkc]").forEach((i) => i.addEventListener("change", () => { BK[i.dataset.bkc] = i.checked; }));
    const strip = $(".dates"), sel = $(".dates [aria-pressed=true]");
    if (strip && sel) strip.scrollLeft = Math.max(0, sel.offsetLeft - strip.offsetLeft - (strip.clientWidth - sel.offsetWidth) / 2);
    bindLoginPanel();
    $("#book").addEventListener("submit", async (ev) => {
      ev.preventDefault();
      if (R.busy) return;
      BK.fieldErr = {}; BK.err = "";
      if (!cust) { BK.err = P("เข้าสู่ระบบด้วยอีเมลก่อนยืนยันการจอง", "Please sign in with your email before confirming."); render(); const ob = $("#otp-box"); if (ob) ob.scrollIntoView({ block: "center" }); return; }
      if (!BK.hm) { BK.err = P("เลือกเวลาก่อน", "Please choose a time."); render(); return; }
      if (!BK.name.trim()) BK.fieldErr.name = errText({ code: "name" });
      if (!/^0[2-9]\d{7,8}$/.test(BK.phone.replace(/\D/g, "").replace(/^66(\d{9})$/, "0$1"))) BK.fieldErr.phone = errText({ code: "phone" });
      if (!BK.health) BK.fieldErr.health = P("กรุณายืนยันข้อควรระวังด้านสุขภาพ", "Please confirm the health note.");
      if (!BK.agree) BK.fieldErr.agree = P("กรุณายอมรับเงื่อนไข", "Please accept the terms.");
      if (Object.keys(BK.fieldErr).length) { BK.err = Object.values(BK.fieldErr)[0]; render(); return; }
      R.busy = true; render();
      try {
        const res = await rpc("create_booking", { p: { day: BK.day, hm: BK.hm, people: BK.people, name: BK.name, phone: BK.phone, note: BK.note, consent: true } }, "cust");
        if (cust) store.set(ME_KEY, { email: cust.email, name: BK.name.trim(), phone: BK.phone });
        const mine = store.get("pfd-mine") || []; mine.unshift({ ref: res.ref, token: res.token }); store.set("pfd-mine", mine.slice(0, 10));
        R.busy = false; BK.hm = ""; BK.note = "";
        go("booking", { hashArg: res.ref + "/" + res.token, ref: res.ref, token: res.token, justBooked: true });
      } catch (e) {
        R.busy = false;
        if (e.code === "name" || e.code === "phone" || e.code === "email") BK.fieldErr[e.code] = errText(e);
        if (e.code === "duplicate" || e.code === "too_many") BK.fieldErr.phone = errText(e);
        if (e.code === "slot_full" || e.code === "too_soon" || e.code === "slot_closed") BK.hm = "";
        if (e.code === "login" || /JWT/i.test(e.message)) custOut();
        BK.err = errText(e); render();
      }
    });
  }

  // ---------- a customer's booking page ----------
  async function viewBooking() {
    let b = null;
    try { b = await rpc("get_booking", { p_ref: R.ref, p_token: R.token }); } catch (e) { b = null; }
    if (!b) return pubLayout(`<section class="err-page"><div class="wrap"><h1>${P("ไม่พบการจองนี้", "Booking not found")}</h1><p class="lede">${P("ลิงก์อาจไม่ครบ ลองค้นด้วยรหัสการจองและเบอร์โทร", "The link may be incomplete. Try finding it with your reference and phone.")}</p><a class="btn btn--primary" href="#my" data-go="my">${P("ค้นหาการจอง", "Find my booking")}</a></div></section>`);
    const live = b.status === "confirmed" || b.status === "checked_in";
    const st = { confirmed: P("จองแล้ว", "Booked"), checked_in: P("เช็คอินแล้ว", "Checked in"), cancelled: P("ยกเลิกแล้ว", "Cancelled"), no_show: P("ไม่ได้มา", "Missed") }[b.status];
    const link = location.origin + location.pathname + "#booking/" + b.ref + "/" + R.token;
    const moveDays = R.moveOpen ? await (async () => { try { return await rpc("availability", { p_from: today(), p_days: Math.min(Number(C.horizon_days) + 1, 42), p_people: b.people }); } catch (e) { return []; } })() : [];
    const md = R.moveDay && moveDays.find((d) => d.day === R.moveDay);
    return pubLayout(`<section class="section" style="padding-top:clamp(28px,5vw,56px)"><div class="wrap">
  ${R.justBooked ? `<div class="alert alert--ok" role="status">${ICON.ok}<p>${P("จองเรียบร้อยแล้ว ดูการจองทั้งหมด เลื่อน หรือยกเลิกได้ทุกเมื่อที่เมนู", "You're booked. See, move or cancel your bookings any time under")} <a href="#my" data-go="my">${P("การจองของฉัน", "My bookings")}</a>${P(" (เข้าสู่ระบบด้วยอีเมลเดิม)", " (sign in with the same email)")}</p></div>` : ""}${flashHTML()}
  <article class="ticket">
    <div class="ticket-top"><p class="status"><span class="badge ${live ? "badge--ok" : "badge--bad"}">${st}</span></p><h1>${live ? P("แล้วพบกันที่สวน", "See you in the garden") : st}</h1>${live ? `<p class="muted">${P("มาก่อนเวลา 10–15 นาที และเตรียมชุดที่เปียกได้", "Please arrive 10–15 minutes early with clothes that can get wet.")}</p>` : ""}</div>
    <div class="ticket-body">
      <div><p class="muted" style="margin:0">${P("รหัสการจอง", "Booking reference")}</p><p class="ticket-ref" style="margin:0">${esc(b.ref)}</p></div>
      <dl class="kv">
        <dt>${P("วัน", "Day")}</dt><dd>${esc(dLong(b.day))}</dd>
        <dt>${P("เวลา", "Time")}</dt><dd>${esc(b.hm)}–${esc(b.end)}</dd>
        <dt>${P("จำนวน", "People")}</dt><dd>${b.people}</dd>
        <dt>${P("ชื่อ", "Name")}</dt><dd>${esc(b.name)}</dd>
        <dt>${P("ยอด", "Total")}</dt><dd>${baht(b.total)} ${b.pay === "paid" ? P("(ชำระแล้ว)", "(paid)") : b.pay === "due" ? P("(ชำระที่ร้าน)", "(pay at the shop)") : ""}</dd>
      </dl>
      ${live ? `<div class="field"><label for="my-link">${P("ลิงก์หน้านี้ (เก็บไว้)", "This page's link (keep it)")}</label><div class="code-row"><input id="my-link" type="text" readonly value="${esc(link)}"><button class="btn btn--small" type="button" data-copy="${esc(link)}">${P("คัดลอก", "Copy")}</button></div></div>` : ""}
    </div>
  </article>
  ${b.status === "confirmed" ? `<div class="manage">
    ${b.can_move ? `<details${R.moveOpen ? " open" : ""} data-move-toggle><summary>${P("เลื่อนวันหรือเวลา", "Move to another time")}</summary><div class="inner">
      ${R.moveOpen ? `<div class="dates">${moveDays.map((d) => { const ok = d.slots.some((s) => s.ok); return `<button type="button" class="date" data-mday="${d.day}" aria-pressed="${d.day === R.moveDay}"${ok ? "" : " disabled"}><span class="w">${esc(wd(d.day))}</span><span class="n">${Number(d.day.slice(8))}</span><span class="m">${esc(mon(d.day))}</span></button>`; }).join("")}</div>
      ${md ? `<div class="times">${md.slots.map((s) => `<button type="button" class="time" data-mhm="${s.hm}" aria-pressed="${s.hm === R.moveHm}"${s.ok && !(md.day === b.day && s.hm === b.hm) ? "" : " disabled"}><b>${s.hm}</b><span>${s.remaining > 0 ? P("ว่าง " + s.remaining, s.remaining + " left") : P("เต็ม", "Full")}</span></button>`).join("")}</div>` : ""}
      <button class="btn btn--primary mt-m" type="button" data-move-go${R.moveHm ? "" : " disabled"}>${P("ยืนยันเวลาใหม่", "Confirm new time")}</button>` : ""}
      <p class="muted"><small>${P(`เลื่อนได้ ${C.max_moves} ครั้ง ก่อนเวลาเริ่ม ${C.cancel_hours} ชั่วโมง`, `One move, up to ${C.cancel_hours} hours before the start.`)}</small></p></div></details>` : ""}
    ${b.can_cancel ? `<details><summary>${P("ยกเลิกการจอง", "Cancel booking")}</summary><div class="inner"><p>${P("ยกเลิกแล้วที่นั่งจะเปิดให้คนอื่นจองทันที", "Your places will be released for others straight away.")}</p><button class="btn btn--danger" type="button" data-cancel data-confirm="${P("กดอีกครั้งเพื่อยืนยันการยกเลิก", "Tap again to confirm")}">${P("ยกเลิกการจองนี้", "Cancel this booking")}</button></div></details>` : ""}
    ${!b.can_move && !b.can_cancel ? `<p class="note-box">${esc(P("เลยกำหนดเปลี่ยนแปลงออนไลน์แล้ว กรุณาโทร ", "It's past the online change deadline. Please call ") + C.phone)}</p>` : ""}
  </div>` : ""}
  <p class="mt-m"><a class="btn" href="#book" data-go="book">${P("จองรอบอื่น", "Book another session")}</a></p>
</div></section>`);
  }
  function bindBooking() {
    const tg = $("[data-move-toggle]");
    if (tg) tg.addEventListener("toggle", () => { if (tg.open && !R.moveOpen) { R.moveOpen = true; render(); } });
    $$("[data-mday]").forEach((b) => b.addEventListener("click", () => { R.moveDay = b.dataset.mday; R.moveHm = ""; render(); }));
    $$("[data-mhm]").forEach((b) => b.addEventListener("click", () => { R.moveHm = b.dataset.mhm; render(); }));
    const mg = $("[data-move-go]");
    if (mg) mg.addEventListener("click", async () => {
      try { await rpc("move_booking", { p_ref: R.ref, p_token: R.token, p_day: R.moveDay, p_hm: R.moveHm }); R.flash = P("เลื่อนการจองเรียบร้อย", "Your booking was moved."); R.flashErr = ""; }
      catch (e) { R.flashErr = errText(e); R.flash = ""; }
      R.moveOpen = false; R.moveDay = ""; R.moveHm = ""; R.justBooked = false; render(); window.scrollTo(0, 0);
    });
    const c = $("[data-cancel]");
    if (c) c.addEventListener("click", async () => {
      try { await rpc("cancel_booking", { p_ref: R.ref, p_token: R.token }); R.flash = P("ยกเลิกการจองแล้ว", "Your booking was cancelled."); R.flashErr = ""; }
      catch (e) { R.flashErr = errText(e); R.flash = ""; }
      R.justBooked = false; render(); window.scrollTo(0, 0);
    });
  }

  // ---------- find my booking ----------
  async function viewMy() {
    let mine = null;
    if (cust) { try { mine = await rpc("my_bookings", {}, "cust"); } catch (e) { if (e.code === "login" || /JWT/i.test(e.message)) custOut(); } }
    const st = { confirmed: P("จองแล้ว", "Booked"), checked_in: P("เช็คอินแล้ว", "Checked in"), cancelled: P("ยกเลิกแล้ว", "Cancelled"), no_show: P("ไม่ได้มา", "Missed") };
    return pubLayout(`<header class="page-head"><div class="wrap"><h1>${P("การจองของฉัน", "My bookings")}</h1></div></header>
<section class="section" style="padding-top:8px"><div class="wrap">${flashHTML()}
  ${cust && R.custRecovery ? newPasswordPanel() : ""}
  ${mine ? `${signedInLine()}${mine.bookings.length ? `<div class="cards">${mine.bookings.map((b) => `<a class="bcard" href="#booking/${esc(b.ref)}/${esc(b.token)}" data-open="${esc(b.ref)}|${esc(b.token)}"><span class="date-badge"><b>${Number(b.day.slice(8))}</b><span>${esc(mon(b.day))}</span></span><span class="info"><b>${esc(b.hm)} · ${b.people} ${P("คน", "people")}</b><span>${esc(b.ref)} · ${esc(dShort(b.day))}</span></span><span class="badge ${b.status === "confirmed" || b.status === "checked_in" ? "badge--ok" : "badge--bad"}">${st[b.status]}</span></a>`).join("")}</div>` : `<p class="muted">${P("ยังไม่มีการจอง", "No bookings yet.")}</p>`}<p class="mt-m"><a class="btn btn--primary" href="#book" data-go="book">${P("จองรอบ", "Book a session")}</a></p>`
  : `<div class="panel" style="max-width:520px"><h2 style="font-size:var(--step-2);margin-bottom:12px">${P("เข้าสู่ระบบเพื่อดูการจอง", "Sign in to see your bookings")}</h2>${loginPanel("")}</div>`}
</div></section>`);
  }
  function bindMy() { if (!cust) bindLoginPanel(); else bindNewPassword(); }


  // ================= back office =================
  const ROLE = { owner: "เจ้าของ", manager: "ผู้จัดการ", staff: "พนักงาน" };
  const ST = { confirmed: ["จองแล้ว", "badge--ok"], checked_in: ["เช็คอินแล้ว", "badge--ok"], cancelled: ["ยกเลิก", "badge--bad"], no_show: ["ไม่มา", "badge--bad"] };
  const PAY = { due: ["รอเก็บเงิน", "badge--warn"], paid: ["ชำระแล้ว", "badge--ok"], none: ["ไม่มียอด", "badge--muted"] };
  const METHOD = { cash: "เงินสด", transfer: "โอน", card: "บัตร", qr: "QR" };
  const CH = { web: "เว็บ", walk_in: "Walk-in", phone: "โทร", line: "LINE" };
  const badge = (m, k) => `<span class="badge ${(m[k] || ["", ""])[1]}">${(m[k] || [k])[0]}</span>`;
  const ADMIN = ["admin", "aday", "abooking", "anew", "asearch", "asettings", "ateam", "apassword"];

  function admLayout(main) {
    const nav = [["admin", "ภาพรวมวันนี้"], ["aday", "ตารางรอบ"], ["anew", "+ สร้างการจอง"], ["asearch", "ค้นหาการจอง"], ["asettings", "ตั้งค่าร้าน"], ["ateam", "ทีมงาน"]];
    const cur = R.view === "abooking" ? "aday" : R.view;
    return `<div class="adm"><div class="shell">
  <aside class="side${R.sideOpen ? " is-open" : ""}">
    <div class="side-top"><a class="brand" href="#admin" data-go="admin">${LOGO}<span>Peaceful Day</span></a><button class="menu" type="button" data-side aria-expanded="${!!R.sideOpen}">เมนู</button></div>
    <nav aria-label="เมนูหลังบ้าน">${nav.map(([v, l]) => `<a href="#${v}" data-go="${v}"${cur === v ? ' aria-current="page"' : ""}>${l}</a>`).join("")}<p class="grp">อื่น ๆ</p><a href="#home" data-go="home">ดูหน้าเว็บลูกค้า</a><a href="#apassword" data-go="apassword">เปลี่ยนรหัสผ่าน</a><button type="button" data-signout>ออกจากระบบ</button></nav>
    <div class="who">${esc(ME.name || ME.email)}<br><small>${ROLE[ME.role]}</small></div>
  </aside>
  <main class="main" id="main">${R.flash ? `<div class="alert alert--ok" role="status">${esc(R.flash)}</div>` : ""}${R.flashErr ? `<div class="alert alert--bad" role="alert">${esc(R.flashErr)}</div>` : ""}${main}</main></div></div>`;
  }

  function viewLogin() {
    return `<div class="adm"><div class="login-wrap"><form class="card login" id="login">
  <div class="brand-row">${LOGO}<h1>Peaceful Day · หลังบ้าน</h1></div>
  ${R.flash ? `<div class="alert alert--ok">${esc(R.flash)}</div>` : ""}${R.flashErr ? `<div class="alert alert--bad" role="alert">${esc(R.flashErr)}</div>` : ""}
  <div class="field"><label for="l-email">อีเมล</label><input type="email" id="l-email" autocomplete="username" required value="${esc(R.loginEmail || "")}"></div>
  <div class="field"><label for="l-pass">รหัสผ่าน</label><input type="password" id="l-pass" autocomplete="${R.signup ? "new-password" : "current-password"}" required minlength="8"></div>
  <div id="captcha" class="captcha"></div>
  <button class="btn btn--primary btn--block" type="submit">${R.signup ? "สร้างบัญชีทีมงาน" : "เข้าสู่ระบบ"}</button>
  <p class="muted small">${R.signup ? `มีบัญชีแล้ว? <a href="#admin" data-signup="0">เข้าสู่ระบบ</a>` : `ครั้งแรก (เจ้าของหรือทีมงานที่ได้รับเชิญ)? <a href="#admin" data-signup="1">สร้างบัญชีด้วยอีเมลที่ได้รับเชิญ</a>`} · <a href="#admin" data-forgot>ลืมรหัสผ่าน</a></p>
  <p class="muted small"><a href="#home" data-go="home">กลับหน้าเว็บลูกค้า</a></p>
</form></div></div>`;
  }
  function bindLogin() {
    mountCaptcha();
    $$("[data-signup]").forEach((a) => a.addEventListener("click", (e) => { e.preventDefault(); R.signup = a.dataset.signup === "1"; R.flash = R.flashErr = ""; render(); }));
    const fg = $("[data-forgot]");
    if (fg) fg.addEventListener("click", async (e) => {
      e.preventDefault();
      const em = $("#l-email").value.trim();
      if (!em) { R.flashErr = "ใส่อีเมลก่อน แล้วกดลืมรหัสผ่านอีกครั้ง"; render(); return; }
      await resetPassword(em); R.loginEmail = em; R.flash = "ถ้าอีเมลนี้มีบัญชี เราส่งลิงก์ตั้งรหัสผ่านใหม่ไปแล้ว"; R.flashErr = ""; render();
    });
    $("#login").addEventListener("submit", async (e) => {
      e.preventDefault();
      const em = $("#l-email").value.trim(), pw = $("#l-pass").value;
      R.loginEmail = em;
      try {
        if (R.signup) {
          const j = await signUp(em, pw);
          if (j.access_token) { session = { access_token: j.access_token, refresh_token: j.refresh_token, expires_at: Date.now() + j.expires_in * 1000, email: em }; store.set(SESSION_KEY, session); }
          else { R.signup = false; R.flash = "สร้างบัญชีแล้ว เปิดอีเมลแล้วกดลิงก์ยืนยัน จากนั้นกลับมาเข้าสู่ระบบ"; R.flashErr = ""; render(); return; }
        } else await signIn(em, pw);
        ME = null; R.flash = R.flashErr = ""; render();
      } catch (err) { R.flashErr = /Invalid login/i.test(err.message) ? "อีเมลหรือรหัสผ่านไม่ถูกต้อง" : /confirm/i.test(err.message) ? "ยังไม่ได้ยืนยันอีเมล เปิดอีเมลแล้วกดลิงก์ยืนยันก่อน" : /registered/i.test(err.message) ? "อีเมลนี้มีบัญชีแล้ว ใช้เข้าสู่ระบบแทน" : "เข้าสู่ระบบไม่สำเร็จ: " + err.message; R.flash = ""; render(); }
    });
  }

  async function viewDashboard() {
    const d = today();
    const [ov, day] = await Promise.all([rpc("staff_overview", { p_day: d }, true), rpc("staff_day", { p_day: d }, true)]);
    const days = ov.days, t = days[days.length - 1], lw = days[days.length - 8] || {};
    const kpi = (label, cur, prev, f, note, invert) => {
      let delta = "";
      if (prev) { const pct = Math.round(((cur - prev) * 100) / prev), up = cur >= prev, good = up !== !!invert; delta = `<span class="delta ${pct === 0 ? "" : good ? "good" : "bad"}"><b aria-hidden="true">${pct === 0 ? "■" : up ? "▲" : "▼"}</b> ${pct > 0 ? "+" : ""}${pct}% <small>จาก ${f(prev)}</small></span>`; }
      return `<div class="kpi"><span class="label">${label}</span><span class="value">${f(cur)}</span>${delta}${note ? `<span class="note">${note}</span>` : ""}</div>`;
    };
    const n = (x) => String(x), occ = (x) => (x.capacity ? Math.round((x.guests * 100) / x.capacity) : 0);
    const max = Math.max(1, ...days.map((x) => x.revenue));
    return admLayout(`<div class="topline"><h1>ภาพรวมวันนี้ <span class="muted" style="font-weight:400;font-size:1rem">${esc(dLong(d, "th-TH"))}</span></h1><a class="btn btn--primary" href="#anew" data-go="anew">+ สร้างการจอง</a><a class="btn" href="#aday" data-go="aday">ตารางรอบ / เช็คอิน</a></div>
  <div class="kpis">
    ${kpi("รับเงินวันนี้", t.revenue, lw.revenue, baht, "เทียบวันเดียวกันสัปดาห์ก่อน")}
    ${kpi("ลูกค้าที่จะมาวันนี้", t.guests, lw.guests, n, "คน")}
    ${kpi("เช็คอินแล้ว", t.checked, null, n, `จาก ${t.guests} คน`)}
    ${kpi("ใช้ที่นั่งวันนี้", occ(t), occ(lw), (x) => x + "%", `${t.guests} จาก ${t.capacity} ที่`)}
    ${kpi("การจองใหม่วันนี้", t.created, lw.created, n, "นับตามวันที่กดจอง")}
    ${kpi("รอเก็บเงินที่ร้าน (วันนี้)", ov.due_today, null, n, "การจอง")}
  </div>
  <div class="grid grid-21" style="margin-top:14px">
    <section class="card"><div class="card-head"><h2>รอบของวันนี้</h2></div>${sessionsTable(day)}</section>
    <section class="card"><h2>รับเงิน 14 วันล่าสุด</h2><ul class="hb">${days.slice().reverse().map((x) => `<li><span class="hb-label">${esc(dShort(x.day, "th-TH"))}</span><span class="hb-track"><i style="width:${((x.revenue / max) * 100).toFixed(1)}%"></i></span><span class="hb-val">${baht(x.revenue)}</span></li>`).join("")}</ul></section>
  </div>`);
  }
  function sessionsTable(day) {
    if (day.closed) return `<p class="empty">วันนี้ปิดร้าน</p>`;
    if (!day.slots.length) return `<p class="empty">วันนี้ไม่มีรอบ (ร้านปิดตามตาราง)</p>`;
    return `<div class="table-wrap"><table><thead><tr><th>เวลา</th><th class="num">จองแล้ว</th><th class="num">ว่าง</th><th>สถานะ</th></tr></thead><tbody>${day.slots.map((s) => `<tr><td class="nowrap"><b>${s.hm}</b>–${s.end}</td><td class="num">${s.used}/${s.capacity}</td><td class="num">${Math.max(0, s.capacity - s.used)}</td><td>${s.blocked ? '<span class="badge badge--muted">ปิด</span>' : s.used >= s.capacity ? '<span class="badge badge--warn">เต็ม</span>' : '<span class="badge badge--ok">เปิด</span>'}</td></tr>`).join("")}</tbody></table></div>`;
  }

  async function viewDay() {
    const d = R.aday || today();
    const day = await rpc("staff_day", { p_day: d }, true);
    const g = day.slots.reduce((a, s) => a + s.used, 0), cap = day.slots.filter((s) => !s.blocked).reduce((a, s) => a + s.capacity, 0);
    const row = (b) => `<li><a class="name" href="#abooking" data-aref="${esc(b.ref)}">${esc(b.name)}</a><span>${b.people} คน</span>${badge(ST, b.status)} ${badge(PAY, b.pay)}<span class="muted grow">${esc(fmtPhone(b.phone))} · ${CH[b.channel] || b.channel}${b.note ? " · “" + esc(b.note) + "”" : ""}</span>${b.status === "confirmed" ? `<button class="btn btn--small btn--primary" type="button" data-checkin="${esc(b.ref)}" data-due="${b.pay === "due" ? 1 : 0}" data-total="${b.total}">เช็คอิน</button>` : ""}</li>`;
    return admLayout(`<div class="topline"><h1>ตารางรอบ</h1></div>
  <div class="filters"><div class="day-nav"><button class="btn" type="button" data-aday="${addDays(d, -1)}" aria-label="วันก่อน">‹</button><input type="date" id="adate" value="${d}" aria-label="วันที่"><button class="btn" type="button" data-aday="${addDays(d, 1)}" aria-label="วันถัดไป">›</button><button class="btn" type="button" data-aday="${today()}">วันนี้</button></div></div>
  <p class="muted">${esc(dLong(d, "th-TH"))} · ลูกค้า ${g} คน · ที่เปิดขาย ${cap}${day.closed ? " · <b>ปิดร้านวันนี้</b>" : ""}${day.holiday != null ? " · <b>วันหยุดนักขัตฯ</b> " + esc(day.holiday) : ""}</p>
  ${R.checkinFor ? checkinBox() : ""}
  <div class="sessions">${day.slots.length ? day.slots.map((s) => `<section class="session"><div class="session-head"><span class="t">${s.hm}–${s.end}</span><span class="meter${s.used >= s.capacity ? " full" : ""}" aria-hidden="true"><i style="width:${s.capacity ? Math.min(100, Math.round((s.used * 100) / s.capacity)) : 0}%"></i></span><span>${s.used}/${s.capacity}</span>${s.blocked ? '<span class="badge badge--muted">ปิด</span>' : ""}
      <span style="margin-left:auto" class="btn-row">${!s.blocked ? `<button class="btn btn--small" type="button" data-newat="${d}|${s.hm}">+ จอง</button>` : ""}${ME.role !== "staff" ? `<button class="btn btn--small btn--ghost" type="button" data-slot="${d}|${s.hm}|${s.blocked ? 0 : 1}">${s.blocked ? "เปิดรอบ" : "ปิดรอบ"}</button>` : ""}</span></div>
      ${s.bookings.length ? `<ul>${s.bookings.map(row).join("")}</ul>` : ""}</section>`).join("") : `<div class="card empty">${day.closed ? "วันนี้ปิดร้าน" : "วันนี้ไม่มีรอบ (ร้านปิดตามตาราง)"}</div>`}
  ${day.off_schedule.length ? `<section class="session"><div class="session-head"><span class="t">นอกตารางรอบ</span></div><ul>${day.off_schedule.map(row).join("")}</ul></section>` : ""}</div>
  ${ME.role !== "staff" ? `<p style="margin-top:16px"><button class="btn btn--small btn--ghost" type="button" data-closeday="${d}|${day.closed ? 0 : 1}" data-confirm="กดอีกครั้งเพื่อยืนยัน">${day.closed ? "เปิดร้านวันนี้" : "ปิดร้านทั้งวัน"}</button> <button class="btn btn--small btn--ghost" type="button" data-holiday="${d}|${day.holiday != null ? 0 : 1}">${day.holiday != null ? "ไม่ใช่วันหยุดนักขัตฯ" : "ตั้งเป็นวันหยุดนักขัตฯ (รับรอบละ " + (C.capacity_weekend || C.capacity) + " คน)"}</button></p>` : ""}`);
  }
  function checkinBox() {
    const c = R.checkinFor;
    return `<div class="card" style="margin-bottom:14px"><h2>เช็คอิน ${esc(c.ref)}</h2><p>รับเงิน ${esc(c.total)} ด้วย</p><div class="btn-row">${Object.entries(METHOD).map(([k, l]) => `<button class="btn btn--primary" type="button" data-cimethod="${k}">${l}</button>`).join("")}<button class="btn" type="button" data-cimethod="">ยังไม่รับเงิน เช็คอินก่อน</button><button class="btn btn--ghost" type="button" data-cicancel>ปิด</button></div></div>`;
  }
  function bindDay() {
    $$("[data-aday]").forEach((b) => b.addEventListener("click", () => { R.aday = b.dataset.aday; R.checkinFor = null; render(); }));
    const d = $("#adate"); if (d) d.addEventListener("change", () => { if (d.value) { R.aday = d.value; render(); } });
    $$("[data-newat]").forEach((b) => b.addEventListener("click", () => { const [day, hm] = b.dataset.newat.split("|"); R.nb = { day, hm, people: 1, name: "", phone: "", channel: "walk_in", free: false, over: false }; go("anew"); }));
    $$("[data-slot]").forEach((b) => b.addEventListener("click", () => act(async () => { const [day, hm, bl] = b.dataset.slot.split("|"); await rpc("staff_set_slot", { p_day: day, p_hm: hm, p_blocked: bl === "1", p_capacity: null }, true); return bl === "1" ? "ปิดรอบแล้ว" : "เปิดรอบแล้ว"; })));
    $$("[data-closeday]").forEach((b) => b.addEventListener("click", () => act(async () => { const [day, c] = b.dataset.closeday.split("|"); await rpc("staff_set_closed_day", { p_day: day, p_closed: c === "1", p_note: "" }, true); return c === "1" ? "ปิดร้านวันนี้แล้ว" : "เปิดร้านวันนี้แล้ว"; })));
    $$("[data-holiday]").forEach((b) => b.addEventListener("click", () => act(async () => { const [day, on] = b.dataset.holiday.split("|"); await rpc("staff_set_holiday", { p_day: day, p_on: on === "1", p_name: "" }, true); return on === "1" ? "ตั้งเป็นวันหยุดนักขัตฯ แล้ว" : "ยกเลิกวันหยุดนักขัตฯ แล้ว"; })));
    $$("[data-checkin]").forEach((b) => b.addEventListener("click", () => {
      if (b.dataset.due === "1") { R.checkinFor = { ref: b.dataset.checkin, total: baht(b.dataset.total) }; render(); window.scrollTo(0, 0); return; }
      act(async () => { await rpc("staff_checkin", { p_ref: b.dataset.checkin, p_method: "" }, true); return "เช็คอิน " + b.dataset.checkin + " แล้ว"; });
    }));
    $$("[data-cimethod]").forEach((b) => b.addEventListener("click", () => act(async () => { const ref = R.checkinFor.ref; R.checkinFor = null; await rpc("staff_checkin", { p_ref: ref, p_method: b.dataset.cimethod }, true); return "เช็คอิน " + ref + (b.dataset.cimethod ? " และรับเงิน" + METHOD[b.dataset.cimethod] + "แล้ว" : " แล้ว (ยังไม่ได้รับเงิน)"); })));
    const cc = $("[data-cicancel]"); if (cc) cc.addEventListener("click", () => { R.checkinFor = null; render(); });
  }
  async function act(fn) {
    try { R.flash = (await fn()) || ""; R.flashErr = ""; } catch (e) { R.flashErr = errText(e); R.flash = ""; }
    render();
  }

  async function viewAdminBooking() {
    const b = await rpc("staff_booking", { p_ref: R.aref }, true);
    if (!b) return admLayout(`<div class="card"><h1>ไม่พบการจอง ${esc(R.aref)}</h1></div>`);
    let mslots = [];
    if (R.amove) { try { const a = await rpc("staff_day", { p_day: R.amoveDay || b.day }, true); mslots = a.slots; } catch (e) { mslots = []; } }
    return admLayout(`<div class="topline"><h1>${esc(b.name)} <span class="mono">${esc(b.ref)}</span></h1>${badge(ST, b.status)} ${badge(PAY, b.pay)}</div>
  <div class="grid grid-21"><div>
    <section class="card"><h2>รายละเอียด</h2><dl class="kv">
      <dt>รอบ</dt><dd><b>${esc(dLong(b.day, "th-TH"))} ${esc(b.hm)}–${esc(b.end)}</b></dd>
      <dt>จำนวนคน</dt><dd>${b.people}</dd>
      <dt>ลูกค้า</dt><dd>${esc(b.name)}${b.phone ? " · " + esc(fmtPhone(b.phone)) : ""}${b.email ? " · " + esc(b.email) : ""}</dd>
      <dt>ยอด</dt><dd>${baht(b.total)}${b.method ? " · " + (METHOD[b.method] || esc(b.method)) : ""}</dd>
      <dt>ช่องทาง</dt><dd>${CH[b.channel] || esc(b.channel)} · จองเมื่อ ${esc(dt(b.created_at))}${b.moves ? ` · ลูกค้าเลื่อนแล้ว ${b.moves} ครั้ง` : ""}</dd>
      ${b.note ? `<dt>ลูกค้าแจ้ง</dt><dd>“${esc(b.note)}”</dd>` : ""}
    </dl></section>
    <section class="card"><h2>จัดการ</h2>
      ${b.status === "confirmed" ? `<div class="btn-row">${b.pay === "due" ? Object.entries(METHOD).map(([k, l]) => `<button class="btn btn--primary" type="button" data-bci="${k}">เช็คอิน + รับ${l}</button>`).join("") + `<button class="btn" type="button" data-bci="">เช็คอิน (ยังไม่รับเงิน)</button>` : `<button class="btn btn--primary" type="button" data-bci="">เช็คอิน</button>`}</div>
        <div class="btn-row" style="margin-top:10px"><button class="btn btn--small" type="button" data-bst="no_show" data-confirm="กดอีกครั้ง: บันทึกว่าไม่มา">บันทึกว่าไม่มา</button><button class="btn btn--small btn--danger" type="button" data-bst="cancelled" data-confirm="กดอีกครั้งเพื่อยกเลิก">ยกเลิกการจอง</button><button class="btn btn--small" type="button" data-amove>${R.amove ? "ปิดการย้ายรอบ" : "ย้ายรอบ"}</button></div>` : ""}
      ${b.status === "checked_in" && b.pay === "due" ? `<div class="btn-row">${Object.entries(METHOD).map(([k, l]) => `<button class="btn btn--primary" type="button" data-bpay="${k}">รับ${l}</button>`).join("")}</div>` : ""}
      ${b.status === "checked_in" || b.status === "no_show" ? `<p style="margin-top:10px"><button class="btn btn--small" type="button" data-bst="confirmed" data-confirm="กดอีกครั้งเพื่อย้อนสถานะ">ย้อนเป็น “จองแล้ว”</button></p>` : ""}
      ${R.amove ? `<div class="card" style="margin-top:12px"><div class="field"><label for="amd">ย้ายไปวันที่</label><input type="date" id="amd" value="${esc(R.amoveDay || b.day)}"></div><div class="btn-row" style="margin:10px 0">${mslots.map((s) => `<button type="button" class="btn btn--small${R.amoveHm === s.hm ? " btn--primary" : ""}" data-amhm="${s.hm}">${s.hm} · ว่าง ${Math.max(0, s.capacity - s.used)}/${s.capacity}</button>`).join("") || '<span class="muted">วันนั้นไม่มีรอบ</span>'}</div>${ME.role !== "staff" ? '<label class="check"><input type="checkbox" id="amover"> อนุญาตเกินความจุ</label>' : ""}<p><button class="btn btn--primary" type="button" data-amgo${R.amoveHm ? "" : " disabled"}>ย้ายไปรอบที่เลือก</button></p></div>` : ""}
      <div class="field" style="margin-top:16px"><label for="snote">โน้ตทีมงาน (ลูกค้าไม่เห็น)</label><textarea id="snote" maxlength="1000">${esc(b.staff_note || "")}</textarea></div><button class="btn btn--small" type="button" data-snote style="margin-top:8px">บันทึกโน้ต</button>
    </section></div>
    <section class="card"><h2>ประวัติ</h2>${b.history.length ? `<ul class="timeline">${b.history.map((h) => `<li><time>${esc(dt(h.at))}</time><span>${esc(h.action)} <small class="muted">${esc(h.who)}</small></span></li>`).join("")}</ul>` : '<p class="muted">ยังไม่มี</p>'}</section>
  </div>`);
  }
  function bindAdminBooking() {
    const ref = R.aref;
    $$("[data-bci]").forEach((b) => b.addEventListener("click", () => act(async () => { await rpc("staff_checkin", { p_ref: ref, p_method: b.dataset.bci }, true); return "เช็คอินแล้ว"; })));
    $$("[data-bpay]").forEach((b) => b.addEventListener("click", () => act(async () => { await rpc("staff_take_payment", { p_ref: ref, p_method: b.dataset.bpay }, true); return "บันทึกรับเงินแล้ว"; })));
    $$("[data-bst]").forEach((b) => b.addEventListener("click", () => act(async () => { await rpc("staff_set_status", { p_ref: ref, p_status: b.dataset.bst }, true); return "อัปเดตสถานะแล้ว"; })));
    const am = $("[data-amove]"); if (am) am.addEventListener("click", () => { R.amove = !R.amove; R.amoveHm = ""; render(); });
    const amd = $("#amd"); if (amd) amd.addEventListener("change", () => { R.amoveDay = amd.value; R.amoveHm = ""; render(); });
    $$("[data-amhm]").forEach((b) => b.addEventListener("click", () => { R.amoveHm = b.dataset.amhm; render(); }));
    const ag = $("[data-amgo]"); if (ag) ag.addEventListener("click", () => act(async () => { const ov = $("#amover"); await rpc("staff_move", { p_ref: ref, p_day: R.amoveDay || $("#amd").value, p_hm: R.amoveHm, p_over: !!(ov && ov.checked) }, true); R.amove = false; return "ย้ายรอบแล้ว"; }));
    const sn = $("[data-snote]"); if (sn) sn.addEventListener("click", () => act(async () => { await rpc("staff_note", { p_ref: ref, p_note: $("#snote").value }, true); return "บันทึกโน้ตแล้ว"; }));
  }

  async function viewNew() {
    const N = R.nb || (R.nb = { day: today(), hm: "", people: 1, name: "", phone: "", channel: "walk_in", free: false, over: false });
    const day = await rpc("staff_day", { p_day: N.day }, true);
    return admLayout(`<div class="topline"><h1>สร้างการจอง (Walk-in / โทร / LINE)</h1></div>
  <form class="card" id="nb">
    <div class="form-grid"><div class="field"><label for="nb-day">วันที่</label><input type="date" id="nb-day" value="${N.day}"></div><div class="field"><label for="nb-people">จำนวนคน</label><input type="number" id="nb-people" min="1" max="50" value="${N.people}"></div></div>
    <div class="btn-row" style="margin:12px 0">${day.slots.map((s) => `<button type="button" class="btn btn--small${N.hm === s.hm ? " btn--primary" : ""}" data-nbhm="${s.hm}">${s.hm} · ว่าง ${Math.max(0, s.capacity - s.used)}/${s.capacity}${s.blocked ? " · ปิด" : ""}</button>`).join("") || '<span class="muted">วันนั้นไม่มีรอบ</span>'}</div>
    <div class="form-grid"><div class="field"><label for="nb-name">ชื่อลูกค้า</label><input type="text" id="nb-name" value="${esc(N.name)}"></div><div class="field"><label for="nb-phone">เบอร์โทร (ไม่บังคับ)</label><input type="tel" id="nb-phone" value="${esc(N.phone)}"></div>
      <div class="field"><label for="nb-ch">ช่องทาง</label><select id="nb-ch">${[["walk_in", "Walk-in"], ["phone", "โทรศัพท์"], ["line", "LINE"]].map(([k, l]) => `<option value="${k}"${N.channel === k ? " selected" : ""}>${l}</option>`).join("")}</select></div></div>
    ${ME.role !== "staff" ? `<label class="check" style="margin-top:10px"><input type="checkbox" id="nb-free"${N.free ? " checked" : ""}> ไม่คิดเงิน (แขกของร้าน)</label><label class="check"><input type="checkbox" id="nb-over"${N.over ? " checked" : ""}> อนุญาตเกินความจุ</label>` : ""}
    <p style="margin-top:14px"><button class="btn btn--primary" type="submit">สร้างการจอง</button></p>
  </form>`);
  }
  function bindNew() {
    const N = R.nb;
    const sync = () => { N.day = $("#nb-day").value || N.day; N.people = Number($("#nb-people").value) || 1; N.name = $("#nb-name").value; N.phone = $("#nb-phone").value; N.channel = $("#nb-ch").value; const f = $("#nb-free"), o = $("#nb-over"); N.free = !!(f && f.checked); N.over = !!(o && o.checked); };
    $("#nb-day").addEventListener("change", () => { sync(); N.hm = ""; render(); });
    $$("[data-nbhm]").forEach((b) => b.addEventListener("click", () => { sync(); N.hm = b.dataset.nbhm; render(); }));
    $("#nb").addEventListener("submit", async (e) => {
      e.preventDefault(); sync();
      if (!N.hm) { R.flashErr = "เลือกรอบก่อน"; render(); return; }
      try { const b = await rpc("staff_create_booking", { p: { day: N.day, hm: N.hm, people: N.people, name: N.name, phone: N.phone, channel: N.channel, free: N.free, over: N.over } }, true); R.nb = null; go("abooking", { aref: b.ref, flash: "สร้างการจอง " + b.ref + " แล้ว" }); }
      catch (err) { R.flashErr = errText(err); render(); }
    });
  }

  async function viewSearch() {
    const list = await rpc("staff_search", { p_q: R.q || "" }, true);
    return admLayout(`<div class="topline"><h1>ค้นหาการจอง</h1></div>
  <form class="filters" id="sq"><div class="field" style="flex:1 1 240px"><label for="q">ชื่อ เบอร์ หรือรหัส PD-</label><input type="search" id="q" value="${esc(R.q || "")}"></div><button class="btn" type="submit">ค้นหา</button></form>
  <div class="card">${list.length ? `<div class="table-wrap"><table><thead><tr><th>รอบ</th><th>ลูกค้า</th><th class="num">คน</th><th>สถานะ</th><th>เงิน</th><th class="num">ยอด</th><th>ช่องทาง</th></tr></thead><tbody>${list.map((b) => `<tr><td class="nowrap">${esc(dShort(b.day, "th-TH"))}<span class="sub">${esc(b.hm)}</span></td><td><a href="#abooking" data-aref="${esc(b.ref)}">${esc(b.name)}</a><span class="sub">${esc(b.ref)} · ${esc(fmtPhone(b.phone))}</span></td><td class="num">${b.people}</td><td>${badge(ST, b.status)}</td><td>${badge(PAY, b.pay)}</td><td class="num">${baht(b.total)}</td><td>${CH[b.channel] || esc(b.channel)}</td></tr>`).join("")}</tbody></table></div>` : '<p class="empty">ไม่พบการจอง</p>'}</div>`);
  }
  function bindSearch() { $("#sq").addEventListener("submit", (e) => { e.preventDefault(); R.q = $("#q").value; render(); }); }

  async function viewSettings() {
    const cfg = await rpc("public_config", {});
    const can = ME.role !== "staff";
    const inp = (id, label, val, type, extra) => `<div class="field"><label for="${id}">${label}</label><input type="${type || "text"}" id="${id}" value="${esc(val)}"${can ? "" : " disabled"}${extra || ""}></div>`;
    return admLayout(`<div class="topline"><h1>ตั้งค่าร้าน</h1></div>
  ${can ? "" : '<p class="muted">ผู้จัดการหรือเจ้าของเท่านั้นที่แก้ค่าได้</p>'}
  <form class="card" id="setf"><h2>บริการและรอบ</h2>
    <div class="form-grid">${inp("s-price", "ราคาต่อคน (บาท)", cfg.price, "number", ' min="0"')}${inp("s-pair", "โปร 2 คน (บาท, ใส่ 0 = ปิดโปร)", cfg.pair_price || 0, "number", ' min="0"')}${inp("s-cap", "รับต่อรอบ วันธรรมดา (คน)", cfg.capacity, "number", ' min="1"')}${inp("s-capw", "รับต่อรอบ เสาร์-อาทิตย์/นักขัตฯ (คน)", cfg.capacity_weekend || cfg.capacity, "number", ' min="1"')}${inp("s-dur", "ใช้บริการได้ (นาที)", cfg.duration, "number", ' min="10"')}${inp("s-maxg", "จองออนไลน์ได้สูงสุดต่อครั้ง (คน)", cfg.max_group, "number", ' min="1"')}</div>
    <div class="field"><label for="s-times">เวลาเริ่มแต่ละรอบ (คั่นด้วย ,)</label><input type="text" id="s-times" value="${esc((cfg.times || []).join(", "))}"${can ? "" : " disabled"}></div>
    <fieldset style="margin-top:12px"><legend>หยุดประจำสัปดาห์</legend><div class="checks">${[1, 2, 3, 4, 5, 6, 0].map((d) => `<label class="check"><input type="checkbox" id="s-c${d}" data-closed="${d}"${(cfg.closed || []).includes(d) ? " checked" : ""}${can ? "" : " disabled"}> ${thDays[d]}</label>`).join("")}</div></fieldset>
    <h2 style="margin-top:18px">เงื่อนไขการจอง</h2>
    <div class="form-grid">${inp("s-lead", "ปิดจองออนไลน์ก่อนรอบ (นาที)", cfg.lead_min, "number")}${inp("s-hor", "เปิดให้จองล่วงหน้า (วัน)", cfg.horizon_days, "number")}${inp("s-cut", "ลูกค้ายกเลิก/เลื่อนเองได้ก่อนรอบ (ชม.)", cfg.cancel_hours, "number")}${inp("s-mm", "ลูกค้าเลื่อนได้ (ครั้ง)", cfg.max_moves, "number")}</div>
    <h2 style="margin-top:18px">ข้อมูลร้าน</h2>
    <div class="form-grid">${inp("s-phone", "เบอร์โทรร้าน", cfg.phone)}${inp("s-line", "LINE ID", cfg.line)}${inp("s-addr", "ที่อยู่ (ไทย)", cfg.address_th)}${inp("s-addren", "ที่อยู่ (อังกฤษ)", cfg.address_en)}${inp("s-map", "ลิงก์ Google Maps", cfg.map_url)}</div>
    ${can ? '<p style="margin-top:14px"><button class="btn btn--primary" type="submit">บันทึก</button></p>' : ""}
  </form>`);
  }
  function bindSettings() {
    const f = $("#setf"); if (!f) return;
    f.addEventListener("submit", (e) => {
      e.preventDefault();
      const times = $("#s-times").value.split(/[,\s]+/).filter(Boolean).map((x) => (x.length === 4 ? "0" + x : x));
      if (!times.every((x) => /^([01]\d|2[0-3]):[0-5]\d$/.test(x))) { R.flashErr = "รูปแบบเวลาไม่ถูกต้อง ใช้ HH:MM เช่น 09:00"; render(); return; }
      const num = (id) => Number($(id).value);
      act(async () => {
        C = Object.assign(C, await rpc("staff_save_settings", { p: { price: num("#s-price"), pair_price: num("#s-pair"), capacity: num("#s-cap"), capacity_weekend: num("#s-capw"), duration: num("#s-dur"), max_group: num("#s-maxg"), times: Array.from(new Set(times)).sort(), closed: $$("[data-closed]").filter((c) => c.checked).map((c) => Number(c.dataset.closed)), lead_min: num("#s-lead"), horizon_days: num("#s-hor"), cancel_hours: num("#s-cut"), max_moves: num("#s-mm"), phone: $("#s-phone").value.trim(), line: $("#s-line").value.trim(), address_th: $("#s-addr").value.trim(), address_en: $("#s-addren").value.trim(), map_url: $("#s-map").value.trim() } }, true));
        return "บันทึกแล้ว หน้าเว็บลูกค้าใช้ค่าใหม่ทันที";
      });
    });
  }

  async function viewTeam() {
    const t = await rpc("staff_team", {}, true);
    return admLayout(`<div class="topline"><h1>ทีมงาน</h1></div>
  <section class="card"><div class="table-wrap"><table><thead><tr><th>อีเมล</th><th>ชื่อ</th><th>ตำแหน่ง</th><th></th></tr></thead><tbody>
    ${t.staff.map((s) => `<tr><td>${esc(s.email)}</td><td>${esc(s.name)}</td><td>${ROLE[s.role]}</td><td>${ME.role === "owner" && s.role !== "owner" ? `<button class="btn btn--small btn--ghost" type="button" data-remove="${esc(s.email)}" data-confirm="กดอีกครั้งเพื่อลบ">ลบ</button>` : ""}</td></tr>`).join("")}
    ${t.invites.map((s) => `<tr class="is-dim"><td>${esc(s.email)}</td><td>${esc(s.name)}</td><td>${ROLE[s.role]} (รอสร้างบัญชี)</td><td>${ME.role === "owner" ? `<button class="btn btn--small btn--ghost" type="button" data-remove="${esc(s.email)}">ยกเลิกคำเชิญ</button>` : ""}</td></tr>`).join("")}
  </tbody></table></div></section>
  ${ME.role === "owner" ? `<form class="card" id="inv"><h2>เชิญทีมงาน</h2><p class="muted">เพิ่มอีเมลที่นี่ แล้วให้ทีมงานเปิดหน้า “สำหรับทีมงาน” กด “สร้างบัญชีด้วยอีเมลที่ได้รับเชิญ”</p>
    <div class="form-grid"><div class="field"><label for="i-email">อีเมล</label><input type="email" id="i-email" required></div><div class="field"><label for="i-name">ชื่อ</label><input type="text" id="i-name"></div><div class="field"><label for="i-role">ตำแหน่ง</label><select id="i-role"><option value="staff">พนักงาน (เช็คอิน รับเงิน สร้างการจอง)</option><option value="manager">ผู้จัดการ (+ ปิดรอบ ตั้งค่า)</option></select></div></div>
    <p><button class="btn btn--primary" type="submit">เพิ่มคำเชิญ</button></p></form>` : ""}`);
  }
  function bindTeam() {
    const f = $("#inv"); if (f) f.addEventListener("submit", (e) => { e.preventDefault(); act(async () => { await rpc("staff_invite", { p_email: $("#i-email").value, p_role: $("#i-role").value, p_name: $("#i-name").value }, true); return "เพิ่มคำเชิญแล้ว"; }); });
    $$("[data-remove]").forEach((b) => b.addEventListener("click", () => act(async () => { await rpc("staff_remove", { p_email: b.dataset.remove }, true); return "ลบแล้ว"; })));
  }

  function viewPassword() {
    return admLayout(`<div class="topline"><h1>${R.recovery ? "ตั้งรหัสผ่านใหม่" : "เปลี่ยนรหัสผ่าน"}</h1></div><form class="card" id="pw" style="max-width:460px"><div class="field"><label for="pw1">รหัสผ่านใหม่ (อย่างน้อย 8 ตัว)</label><input type="password" id="pw1" autocomplete="new-password" minlength="8" required></div><p><button class="btn btn--primary" type="submit">บันทึกรหัสผ่าน</button></p></form>`);
  }
  function bindPassword() { $("#pw").addEventListener("submit", (e) => { e.preventDefault(); act(async () => { await setPassword($("#pw1").value); R.recovery = false; return "เปลี่ยนรหัสผ่านแล้ว"; }); }); }

  // ---------- render ----------
  let renderSeq = 0;
  async function render() {
    const seq = ++renderSeq;
    const isAdm = ADMIN.includes(R.view);
    document.documentElement.lang = isAdm ? "th" : lang;
    document.body.dataset.mode = isAdm ? "adm" : "pub";
    let html;
    try {
      if (!C) C = await rpc("public_config", {});
      if (isAdm) {
        if (!session) html = viewLogin();
        else {
          if (!ME) { try { ME = await rpc("staff_me", {}, true); } catch (e) { ME = null; } }
          if (!session) html = viewLogin();
          else if (!ME) { signOut(); R.flashErr = errText({ code: "not_staff" }) + " — ให้เจ้าของร้านเพิ่มอีเมลนี้ในหน้า “ทีมงาน” ก่อน"; html = viewLogin(); }
          else if (R.recovery && R.view !== "apassword") { R.view = "apassword"; html = viewPassword(); }
          else html = await ({ admin: viewDashboard, aday: viewDay, abooking: viewAdminBooking, anew: viewNew, asearch: viewSearch, asettings: viewSettings, ateam: viewTeam, apassword: viewPassword }[R.view])();
        }
      } else html = await ({ home: viewHome, book: viewBook, booking: viewBooking, my: viewMy }[R.view] || viewHome)();
    } catch (e) {
      if (e.code === "not_staff" || /JWT/i.test(e.message)) { signOut(); ME = null; R.flashErr = errText(e); html = viewLogin(); }
      else html = `<div class="pub"><section class="err-page"><div class="wrap"><h1>${P("โหลดข้อมูลไม่สำเร็จ", "Couldn't load")}</h1><p class="lede">${esc(errText(e))}</p><button class="btn btn--primary" type="button" onclick="location.reload()">${P("ลองใหม่", "Try again")}</button></div></section></div>`;
    }
    if (seq !== renderSeq) return; // a newer screen was requested meanwhile
    app.innerHTML = html;
    bindCommon();
    const binders = { book: bindBook, booking: bindBooking, my: bindMy, aday: bindDay, abooking: bindAdminBooking, anew: bindNew, asearch: bindSearch, asettings: bindSettings, ateam: bindTeam, apassword: bindPassword };
    if (isAdm && !$(".adm .shell")) bindLogin(); else if (binders[R.view]) binders[R.view]();
    if (R.anchor) { const a = document.getElementById(R.anchor); R.anchor = ""; if (a) a.scrollIntoView(); }
  }
  function bindCommon() {
    $$("[data-go]").forEach((a) => a.addEventListener("click", (e) => { e.preventDefault(); const [v, anchor] = a.dataset.go.split("#"); const m = $("#mobile-nav"); if (m) m.classList.remove("is-open"); if (v === R.view && anchor) { const el = document.getElementById(anchor); if (el) el.scrollIntoView({ behavior: "smooth" }); return; } go(v, { anchor: anchor || "" }); }));
    $$("[data-lang]").forEach((a) => a.addEventListener("click", (e) => { e.preventDefault(); lang = lang === "th" ? "en" : "th"; store.set("pfd-lang", lang); render(); }));
    $$("[data-book]").forEach((a) => a.addEventListener("click", (e) => { e.preventDefault(); BK.people = Math.min(BK.people, 1) || 1; go("book", { pre: a.dataset.book }); }));
    $$("[data-open]").forEach((a) => a.addEventListener("click", (e) => { e.preventDefault(); const [ref, tok] = a.dataset.open.split("|"); go("booking", { hashArg: ref + "/" + tok, ref, token: tok, justBooked: false }); }));
    $$("[data-aref]").forEach((a) => a.addEventListener("click", (e) => { e.preventDefault(); go("abooking", { aref: a.dataset.aref, amove: false, amoveDay: "", amoveHm: "" }); }));
    $$("[data-copy]").forEach((b) => b.addEventListener("click", () => { const t = b.dataset.copy, l = b.textContent; const done = (ok) => { b.textContent = ok ? P("คัดลอกแล้ว", "Copied") : P("เลือกข้อความไว้แล้ว", "Selected"); setTimeout(() => (b.textContent = l), 2000); }; try { navigator.clipboard.writeText(t).then(() => done(true), () => { const i = b.parentElement.querySelector("input"); if (i) i.select(); done(false); }); } catch (e) { done(false); } }));
    const menu = $("[data-menu]"); if (menu) menu.addEventListener("click", () => { const n = $("#mobile-nav"); const o = !n.classList.contains("is-open"); n.classList.toggle("is-open", o); menu.setAttribute("aria-expanded", String(o)); });
    const side = $("[data-side]"); if (side) side.addEventListener("click", () => { R.sideOpen = !R.sideOpen; render(); });
    $$("[data-cust-out]").forEach((a) => a.addEventListener("click", (e) => { e.preventDefault(); custOut(); BK.name = ""; BK.phone = ""; render(); }));
    const so = $("[data-signout]"); if (so) so.addEventListener("click", () => { signOut(); ME = null; go("admin"); });
    $$("[data-confirm]").forEach((b) => b.addEventListener("click", (e) => { if (b.dataset.armed === "1") return; e.stopImmediatePropagation(); e.preventDefault(); b.dataset.armed = "1"; const o = b.textContent; b.textContent = b.dataset.confirm; setTimeout(() => { if (b.isConnected) { b.dataset.armed = ""; b.textContent = o; } }, 4000); }, true));
  }

  // ---------- address bar ----------
  function fromHash() {
    const h = decodeURIComponent((location.hash || "").slice(1));
    const [v, a, b] = h.split("/");
    if (v === "booking" && a && b) Object.assign(R, { view: "booking", ref: a, token: b, justBooked: false });
    else if (["home", "book", "my"].concat(ADMIN.filter((x) => x !== "abooking")).includes(v)) R.view = v;
    else R.view = "home";
  }
  window.addEventListener("popstate", () => { fromHash(); render(); });
  fromHash();
  render();
})();
