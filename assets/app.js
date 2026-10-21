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
  async function custVerifyLink(email) { await authCall("otp?redirect_to=" + custBack(), { email, create_user: false }); }
  async function custReset(email) { await authCall("recover?redirect_to=" + custBack(), { email }); }
  async function custSetPassword(password) {
    const r = await fetch(CFG.url + "/auth/v1/user", { method: "PUT", headers: Object.assign({ Authorization: "Bearer " + (await custToken()) }, H), body: JSON.stringify({ password }) });
    if (!r.ok) { const j = await r.json().catch(() => ({})); throw new Error(j.error_code || j.msg || j.message || "password"); }
  }
  function custOut() { cust = null; store.del(CUST_KEY); store.del(ME_KEY); if (typeof R !== "undefined") { R.profile = undefined; R.pf = null; } }
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
        window.__pfdVerify = q.get("type") === "magiclink"; // the optional "confirm your email" link
        history.replaceState(null, "", location.pathname + (window.__pfdCustRecovery ? "#my" : window.__pfdVerify ? "#profile" : "#book"));
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
    slot_full: ["รอบนี้มีที่ว่างไม่พอ กรุณาเลือกรอบอื่น ข้อมูลที่กรอกไว้ยังอยู่", "There aren't enough places left in this session. Please choose another; your details are saved."],
    slot_closed: ["รอบนี้ปิดรับจองแล้ว กรุณาเลือกรอบอื่น", "That session isn't open for booking. Please pick another."],
    too_soon: ["รอบนี้ใกล้เวลาเริ่มเกินไปสำหรับการจองออนไลน์ โทรหาเราได้เลย", "That session starts too soon to book online. Please call us."],
    duplicate: ["คุณมีการจองรอบนี้อยู่แล้ว ดูได้ที่ “การจองของฉัน” ถ้าต้องการเพิ่มคน กรุณาโทรหาเรา", "You already have a booking in that session (see My bookings). To add people, please call us."],
    too_many: ["จองถี่เกินไป ลองใหม่อีกครั้งในอีกสักครู่ หรือโทรหาเรา", "Too many bookings in a short time. Please try again later or call us."],
    unpaid_many: ["คุณมีการจองที่ยังไม่ได้ชำระอยู่ 2 รายการ ชำระหรือยกเลิกรายการเดิมที่หน้า “การจองของฉัน” ก่อน แล้วค่อยจองเพิ่ม", "You have 2 bookings waiting for payment. Pay for or cancel them under My bookings first."],
    change_closed: ["เลยกำหนดเปลี่ยนแปลงออนไลน์แล้ว กรุณาโทรหาเรา", "It's past the online change deadline. Please call us."],
    notfound: ["ไม่พบการจองนี้", "We couldn't find that booking."],
    forbidden: ["ตำแหน่งของคุณทำรายการนี้ไม่ได้", "Your role can't do this."],
    not_staff: ["บัญชีนี้ยังไม่ได้รับสิทธิ์เข้าหลังบ้าน", "This account doesn't have back-office access yet."],
    settings: ["ค่าที่ตั้งไม่ถูกต้อง", "Some settings aren't valid."],
    method: ["เลือกวิธีรับเงินก่อน", "Choose how the payment was made."],
    login: ["เข้าสู่ระบบก่อนจอง", "Please sign in first."],
    package: ["ใส่ชื่อแพ็กเกจก่อน", "Enter the package name."],
    birth: ["วันเกิดไม่ถูกต้อง", "That date of birth doesn't look right."],
    agree: ["กรุณาติ๊กว่าได้อ่านและเข้าใจแล้ว", "Please tick to confirm you have read and understood."],
    any_phone: ["เบอร์โทรไม่ถูกต้อง (เบอร์ไทย 9–10 หลัก หรือเบอร์ต่างประเทศขึ้นต้นด้วย +)", "That phone number doesn't look right (Thai number, or + and country code)."],
    waiver_busy: ["ตอนนี้ลงทะเบียนออนไลน์แบบไม่มีการจองได้ไม่สะดวก กรุณาลงทะเบียนกับทีมงานที่เคาน์เตอร์", "Online registration without a booking is busy right now. Please register with our team at the counter."],
    waiver_full: ["การจองนี้ลงทะเบียนครบแล้ว ถ้ามีคนเพิ่ม กรุณาแจ้งทีมงาน", "Everyone on this booking has registered. If someone was added, please tell our team."],
    slip_used: ["สลิปนี้เคยใช้ชำระการจองอื่นแล้ว ตรวจในแอปธนาคารก่อนยืนยัน", "This slip already paid for another booking. Check the bank app first."],
    signature: ["กรุณาลงลายมือชื่อในกรอบ", "Please sign in the box."],
    terms: ["กรุณายอมรับนโยบายความเป็นส่วนตัวก่อน", "Please accept the privacy policy first."],
    restore_full: ["กู้คืนไม่ได้ รอบนั้นเต็มแล้ว ใช้ “นัดวันใหม่” หรือให้ผู้จัดการย้ายรอบให้", "That session is full now."],
    slip: ["ไฟล์สลิปใช้ไม่ได้ ลองถ่ายหรือแคปหน้าจอใหม่เป็นรูปภาพ", "That slip file can't be used. Please attach a photo or screenshot."],
    slip_many: ["ส่งสลิปครบ 5 ครั้งแล้ว กรุณาโทรหาเรา", "You've sent 5 slips already. Please call us."],
    hold_expired: ["หมดเวลาชำระและรอบนี้มีคนจองไปแล้ว ถ้าโอนแล้วกรุณาโทรหาเรา ทีมงานจะช่วยเลื่อนรอบหรือคืนเงินให้", "Time ran out and this session has filled up. If you've already paid, please call us and we'll sort it out."],
    promptpay: ["เบอร์พร้อมเพย์ต้องเป็นเบอร์มือถือ 10 หลัก หรือเลขผู้เสียภาษี 13 หลัก", "PromptPay must be a 10-digit mobile number or 13-digit tax ID."],
  };
  // ---------- PromptPay QR (EMVCo payload with the amount filled in) ----------
  function promptPayPayload(id, amount) {
    const f = (tag, v) => tag + String(v.length).padStart(2, "0") + v;
    const d = String(id || "").replace(/\D/g, "");
    const acc = d.length >= 13 ? f(d.length === 15 ? "03" : "02", d) : f("01", ("0000000000000" + "66" + d.replace(/^0/, "")).slice(-13));
    let s = f("00", "01") + f("01", amount ? "12" : "11") + f("29", f("00", "A000000677010111") + acc) + f("53", "764") + (amount ? f("54", Number(amount).toFixed(2)) : "") + f("58", "TH") + "6304";
    let crc = 0xffff;
    for (let i = 0; i < s.length; i++) { crc ^= s.charCodeAt(i) << 8; for (let j = 0; j < 8; j++) crc = crc & 0x8000 ? ((crc << 1) ^ 0x1021) & 0xffff : (crc << 1) & 0xffff; }
    return s + crc.toString(16).toUpperCase().padStart(4, "0");
  }
  function qrSVG(text) {
    if (!window.qrcode) return "";
    const q = window.qrcode(0, "M"); q.addData(text); q.make();
    const n = q.getModuleCount(), m = 2; let path = "";
    for (let r = 0; r < n; r++) for (let c = 0; c < n; c++) if (q.isDark(r, c)) path += `M${c + m} ${r + m}h1v1h-1z`;
    return `<svg class="qr" viewBox="0 0 ${n + m * 2} ${n + m * 2}" role="img" aria-label="PromptPay QR" shape-rendering="crispEdges"><rect width="100%" height="100%" fill="#fff"/><path d="${path}" fill="#000"/></svg>`;
  }
  // shrink a slip photo to a small JPEG before sending it
  function slipData(file) {
    return new Promise((resolve, reject) => {
      if (!file || !/^image\//.test(file.type)) return reject(Object.assign(new Error("slip"), { code: "slip" }));
      const img = new Image(), url = URL.createObjectURL(file);
      img.onload = () => {
        const k = Math.min(1, 1400 / Math.max(img.width, img.height)), cv = document.createElement("canvas");
        cv.width = Math.round(img.width * k); cv.height = Math.round(img.height * k);
        const cx = cv.getContext("2d"); cx.fillStyle = "#fff"; cx.fillRect(0, 0, cv.width, cv.height); cx.drawImage(img, 0, 0, cv.width, cv.height);
        URL.revokeObjectURL(url);
        let q = 0.82, out = cv.toDataURL("image/jpeg", q);
        while (out.length > 700000 && q > 0.35) { q -= 0.12; out = cv.toDataURL("image/jpeg", q); }
        resolve(out);
      };
      img.onerror = () => { URL.revokeObjectURL(url); reject(Object.assign(new Error("slip"), { code: "slip" })); };
      img.src = url;
    });
  }
  // one CSV cell: text that a spreadsheet would run as a formula gets a leading apostrophe, then quote if needed
  // links from the shop settings: only real https addresses ever become a link
  const safeUrl = (u) => (/^https:\/\/[^\s"'<>]+$/i.test(u || "") ? u : "#");
  const csvCell = (v) => { let t = String(v == null ? "" : v); if (/^[=+\-@\t\r]/.test(t) && !/^-?\d+(\.\d+)?$/.test(t)) t = "'" + t; return /[",\n\r]/.test(t) ? '"' + t.replace(/"/g, '""') + '"' : t; };
  const hasPayInfo = () => !!(C.promptpay || C.bank_account);
  const errText = (e) => { const m = ERR[e && e.code] || ERR[e && e.message]; return m ? P(m[0], m[1]) : P("ระบบขัดข้อง ลองใหม่อีกครั้ง", "Something went wrong. Please try again."); };

  // ---------- state ----------
  const R = { view: "home", flash: "", flashErr: "", busy: false, recovery: !!window.__pfdRecovery, custRecovery: !!window.__pfdCustRecovery };
  let C = null; // shop settings from the database
  // the 2-person promotion: each pair pays pair_price, anyone left over pays the normal price (same rule as the database)
  const pairOn = () => Number(C.pair_price) > 0;
  const priceFor = (n) => (pairOn() ? Math.floor(n / 2) * Number(C.pair_price) + (n % 2) * Number(C.price) : n * Number(C.price));
  const priceLine = (n) => (pairOn() && n >= 2 ? `${P("โปร 2 คน", "2-person deal")} ${baht(C.pair_price)}${Math.floor(n / 2) > 1 ? ` × ${Math.floor(n / 2)}` : ""}${n % 2 ? ` + ${baht(C.price)}` : ""}` : `${baht(C.price)} × ${n}`);
  const promoText = () => P(`โปร 2 คน ${baht(C.pair_price)} (ปกติ ${baht(C.price * 2)})`, `Two people for ${baht(C.pair_price)} (usually ${baht(C.price * 2)})`);
  const endHM = (hm) => { const [h, m] = hm.split(":").map(Number), t = h * 60 + m + Number(C.duration || 120); return String(Math.floor(t / 60) % 24).padStart(2, "0") + ":" + String(t % 60).padStart(2, "0"); };
  // gallery: [file, small width, Thai caption, English caption, Thai alt, English alt]; the first one runs full width
  const GALLERY = [
    ["night", 800, "ยามค่ำ", "At night", "อ่างไม้ใส่น้ำแข็งใต้ร่มลายเหลืองและไฟประดับยามค่ำ", "Ice-filled wooden tubs under yellow-striped umbrellas and string lights at night"],
    ["sauna", 640, "Sauna", "Sauna", "ห้องซาวน่าไม้ประตูกระจก มีเก้าอี้ผ้าใบลายเหลืองข้างใน", "The wooden sauna with a glass door and yellow-striped deck chairs inside"],
    ["garden", 640, "สวน", "Garden", "สนามหญ้าและทางเดินในสวนใต้ร่มลายเหลือง", "The lawn and stepping stones under a yellow-striped umbrella"],
    ["garden-path", 640, "ทางเดินในสวน", "Garden path", "ทางเดินหินบนสนามหญ้าในสวน", "Stepping stones across the lawn in the garden"],
    ["lounge", 640, "ห้องนั่งเล่น", "Lounge", "ห้องนั่งเล่นในร้าน มีโคมไม้ไผ่และโซฟาหนัง", "The indoor lounge with bamboo lamps and a leather sofa"],
  ];
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
    `ชำระเงินภายใน ${C.hold_min || 15} นาทีหลังจอง เลื่อนรอบได้เองก่อนเวลาเริ่ม ${C.cancel_hours} ชั่วโมง (${C.max_moves} ครั้ง) ถ้าชำระแล้วต้องการยกเลิก หรือเลยกำหนดเลื่อน กรุณาโทร ${C.phone}`,
    `Pay within ${C.hold_min || 15} minutes of booking. Move your booking yourself up to ${C.cancel_hours} hours before it starts (${C.max_moves} time${C.max_moves > 1 ? "s" : ""}). To cancel after paying, or after that deadline, please call ${C.phone}.`);

  // ---------- public layout ----------
  function pubLayout(main, opts) {
    opts = opts || {};
    const nav = [["home#experience", P("บรรยากาศ", "Our space")], ["book", P("ราคาและจอง", "Prices & booking")], ["home#first", P("มาครั้งแรก", "First visit")], ["home#visit", P("การเดินทาง", "Getting here")], ["my", P("การจองของฉัน", "My bookings")]];
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
    <p class="foot-brand">${P('แช่เย็น อบร้อน <span class="nw">แล้วพักในสวน</span>', "Cold plunge, warm sauna, a quiet garden.")}</p>
    <div class="foot-col"><h2>${P("ที่ตั้งและเวลาเปิด", "Visit us")}</h2><p>${esc(P(C.address_th, C.address_en))}</p><p>${esc(hoursLine())}</p><p><a href="${esc(safeUrl(C.map_url))}" target="_blank" rel="noopener">${P("เปิดในแผนที่", "Open in Maps")}</a></p></div>
    <div class="foot-col"><h2>${P("ติดต่อ", "Contact")}</h2><p>${P("โทร", "Call")} <a href="tel:${esc((C.phone || "").replace(/\D/g, ""))}">${esc(C.phone)}</a></p>${C.line ? `<p>LINE ${esc(C.line)}</p>` : ""}</div>
    <div class="foot-col"><h2>${P("การจอง", "Bookings")}</h2><ul><li><a href="#book" data-go="book">${P("จองรอบ", "Book a session")}</a></li><li><a href="#my" data-go="my">${P("ดูหรือแก้ไขการจอง", "View or change a booking")}</a></li><li><a href="#terms" data-go="terms">${P("เงื่อนไขการจอง", "Booking terms")}</a></li><li><a href="#privacy" data-go="privacy">${P("นโยบายความเป็นส่วนตัว", "Privacy policy")}</a></li><li><a href="#admin" data-go="admin">${P("สำหรับทีมงาน", "Staff")}</a></li></ul></div>
  </div>
  <div class="foot-base"><span>© ${new Date().getFullYear()} Peaceful Day</span><a href="#" data-lang>${P("English", "ภาษาไทย")}</a></div>
</div></footer>${opts.after || ""}</div>`;
  }

  // ---------- home: the eight parts from the review ----------
  async function viewHome() {
    const faq = faqItems();
    return pubLayout(`
<section class="hero" aria-labelledby="hero-title"><div class="wrap">
  <div class="hero-copy">
    <p class="eyebrow"><span></span>Ice Bath + Sauna · ${P("สุขุมวิท 62", "Sukhumvit 62")}</p>
    <h1 id="hero-title">${P('<span class="line">วันนี้</span><span class="line">แค่มาพักก็พอ</span>', '<span class="line">A little time</span><span class="line">to unwind.</span>')}</h1>
    <p class="lede">${P("แช่น้ำเย็น อบซาวน่า แล้วนั่งพักในสวนที่สุขุมวิท 62 ไม่เคยลองมาก่อนก็มาได้ ค่อย ๆ เริ่มในแบบที่สบายกับตัวเอง", "Ice baths, a warm sauna and a garden to unwind in on Sukhumvit 62. First time? You're welcome to take it at your own pace.")}</p>
    <p class="hero-price"><strong>${baht(C.price)}</strong>${P(`ต่อคน ใช้บริการได้ ${C.duration / 60} ชั่วโมง`, `per person, ${C.duration / 60} hours`)}</p>${pairOn() ? `<p class="hero-promo">${promoText()}</p>` : ""}
    <div class="hero-cta"><a class="btn btn--primary" href="#book" data-go="book">${P("ดูรอบว่างและจอง", "See times and book")}</a><a class="btn" href="#first" data-go="home#first">${P("เตรียมตัวก่อนมา", "First time here?")}</a></div>
    <p class="hero-hours">${ICON.clock}<span>${esc(hoursLine())}</span></p>
  </div>
  <div class="hero-media"><div class="pebble"><img src="${IMG("hero-sky.jpg")}" srcset="${IMG("hero-sky-800.jpg")} 800w, ${IMG("hero-sky.jpg")} 941w" sizes="(min-width: 900px) 46vw, 92vw" width="941" height="1672" alt="${P("อ่างไม้ใส่น้ำแข็งกับเป็ดยางสีเหลือง ใต้ร่มลายเหลืองในสวนของ Peaceful Day", "Ice-filled wooden tubs with yellow rubber ducks under a yellow-striped umbrella in the Peaceful Day garden")}" fetchpriority="high"></div></div>
</div></section>

<section class="section" id="included" aria-labelledby="inc-title"><div class="wrap">
  <div class="section-head"><h2 id="inc-title">${P(`${C.duration / 60} ชั่วโมง ใช้ได้ทั้งสามมุม`, `${C.duration / 60} hours, all three spaces`)}</h2></div>
  <ul class="why">
    <li><span class="ico">${ICON.snow}</span><div><h3>Ice Bath</h3><p>${P("แช่น้ำเย็นในสวน เริ่มทีละนิดตามที่ไหว มีทีมงานอธิบายวิธีก่อนเริ่ม", "Ice baths in the garden, with guidance from the team before you start. Take short dips at your own pace.")}</p></div></li>
    <li><span class="ico">${ICON.flame}</span><div><h3>Sauna</h3><p>${P("อบซาวน่าสลับกับแช่น้ำเย็นได้ภายในเวลาของรอบ", "Enjoy the sauna between cold dips during your session.")}</p></div></li>
    <li><span class="ico">${ICON.leaf}</span><div><h3>${P("มุมพักในสวน", "Garden")}</h3><p>${P(`นั่งพักในสวนหลังแช่ รับรอบละไม่เกิน ${C.capacity} คน (เสาร์-อาทิตย์และวันหยุดนักขัตฯ ${C.capacity_weekend || C.capacity} คน)`, `Take a break in the garden afterwards. Each session welcomes up to ${C.capacity} people (${C.capacity_weekend || C.capacity} on weekends and public holidays).`)}</p></div></li>
  </ul>
</div></section>

<section class="section section--tint" id="experience" aria-labelledby="exp-title"><div class="wrap">
  <div class="section-head"><h2 id="exp-title">${P('อ่างน้ำเย็น <span class="nw">ซาวน่า</span> และสวน', "Cold tubs, sauna and garden")}</h2></div>
  <div class="trio trio--gallery">${GALLERY.map(([f, w, th, en, ath, aen], i) => `<figure${i === 0 ? ' class="wide"' : ""}><img src="${IMG(f + ".jpg")}" srcset="${IMG(f + "-" + w + ".jpg")} ${w}w, ${IMG(f + ".jpg")} ${i === 0 ? 1448 : 1000}w" sizes="${i === 0 ? "92vw" : "(min-width: 980px) 23vw, (min-width: 560px) 46vw, 92vw"}" alt="${P(ath, aen)}" loading="lazy"><figcaption>${P(th, en)}</figcaption></figure>`).join("")}</div>
</div></section>

<section class="section" id="first" aria-labelledby="first-title"><div class="wrap">
  <div class="section-head"><h2 id="first-title">${P("เตรียมตัวสำหรับครั้งแรก", "Your first visit")}</h2></div>
  <ol class="steps">
    <li><div><h3>${P("จองรอบออนไลน์", "Book online")}</h3><p>${P("เลือกวัน เวลา และจำนวนคน แล้วโอนผ่านพร้อมเพย์ แนบสลิปเพื่อยืนยันที่นั่ง", "Choose a day, time and how many people, then pay by PromptPay and attach the slip to confirm.")}</p></div></li>
    <li><div><h3>${P("มาก่อนเวลา 10–15 นาที", "Arrive 10–15 minutes early")}</h3><p>${P("เตรียมชุดว่ายน้ำหรือชุดที่เปียกได้ ทีมงานจะอธิบายวิธีแช่ให้", "Bring swimwear or clothes that can get wet. Our team will explain how to get started.")}</p></div></li>
    <li><div><h3>${P("แช่น้ำเย็นและอบซาวน่า", "Ice bath and sauna")}</h3><p>${P("เริ่มแช่ช่วงสั้น ๆ ไม่ต้องฝืน และพักได้เมื่อรู้สึกว่าพอแล้ว", "Keep your first dips short, go at your own pace and take breaks when you need them.")}</p></div></li>
    <li><div><h3>${P("พักในสวนก่อนกลับ", "Rest in the garden")}</h3><p>${P("ใช้เวลานั่งพักในสวนสักหน่อยก่อนกลับ", "Take a little time to relax in the garden before heading home.")}</p></div></li>
  </ol>
</div></section>

<section class="section section--sage" id="price" aria-labelledby="price-title"><div class="wrap">
  <div class="section-head"><h2 id="price-title">${P("ราคาต่อรอบ", "Session prices")}</h2><p class="lede">${P(`Ice Bath + Sauna ใช้บริการได้ ${C.duration / 60} ชั่วโมง เริ่มรอบใหม่ทุกชั่วโมง ชำระตอนจองออนไลน์`, `Ice bath and sauna for ${C.duration / 60} hours. Sessions start every hour; pay online when you book.`)}</p></div>
  <div class="prices">
    <article class="price-tile"><h3>${P("1 คน", "1 person")}</h3><p class="price-amount">${baht(C.price)}</p><p class="price-sub">${P("ต่อคน", "per person")}</p></article>
    ${pairOn() ? `<article class="price-tile price-tile--deal"><h3>${P("มา 2 คน", "2 people")}</h3><p class="price-amount">${baht(C.pair_price)}</p><p class="price-sub">${P(`ปกติ ${baht(C.price * 2)} · ใช้ราคาโปรได้ทุกคู่ในกลุ่ม`, `instead of ${baht(C.price * 2)}, for every pair in your group`)}</p></article>` : ""}
  </div>
  <p class="price-cta"><a class="btn btn--primary" href="#book" data-go="book">${P("ดูรอบว่างและจอง", "See times and book")}</a></p>
</div></section>

<section class="section" id="faq" aria-labelledby="faq-title"><div class="wrap">
  <div class="section-head"><h2 id="faq-title">${P("คำถามที่พบบ่อย", "Frequently asked questions")}</h2></div>
  <div class="faq">${faq.map(([q, a]) => `<details><summary>${esc(q)}</summary><div class="ans"><p>${esc(a)}</p></div></details>`).join("")}</div>
</div></section>

<section class="section section--tint" id="visit" aria-labelledby="loc-title"><div class="wrap loc">
  <div><div class="section-head"><h2 id="loc-title">${esc(P(C.address_th, C.address_en))}</h2></div>
  <ul class="dir">
    <li><span class="ico">${ICON.train}</span><div><h3>BTS</h3><p>${P("ลงสถานีบางจาก ทางออก 2 เดินเข้าซอยสุขุมวิท 62 ประมาณ 5 นาที", "Bang Chak station, Exit 2, then about 5 minutes' walk into Sukhumvit 62.")}</p></div></li>
    <li><span class="ico">${ICON.car}</span><div><h3>${P("เดินทางด้วยรถยนต์", "By car")}</h3><p>${P("ที่จอดรถมีจำกัด โทรสอบถามก่อนเดินทางได้", "Parking is limited; call ahead if you're driving.")}</p></div></li>
    <li><span class="ico">${ICON.clock}</span><div><h3>${P("เวลาเปิด", "Hours")}</h3><p>${esc(hoursLine())}</p></div></li>
  </ul>
  <div class="loc-actions"><a class="btn btn--primary" href="${esc(safeUrl(C.map_url))}" target="_blank" rel="noopener">${ICON.pin}${P("เปิดในแผนที่", "Open in Maps")}</a><a class="btn" href="tel:${esc((C.phone || "").replace(/\D/g, ""))}">${P("โทร", "Call")} ${esc(C.phone)}</a></div></div>
  <div class="map"><div class="map-placeholder"><img src="${IMG("map-sketch.svg")}" alt=""><a class="btn" href="${esc(safeUrl(C.map_url))}" target="_blank" rel="noopener">${P("เปิด Google Maps", "Open Google Maps")}</a></div></div>
</div></section>`, { paybar: true, after: `<div class="paybar paybar--home"><div class="tot"><small>${P("ต่อคน ต่อรอบ", "per person")}</small><b>${baht(C.price)}</b></div><a class="btn btn--primary" href="#book" data-go="book">${P("ดูรอบว่าง", "See times")}</a></div>` });
  }
  function faqItems() {
    return [
      [P("ไม่เคยแช่น้ำเย็นมาก่อน มาได้ไหม", "I've never done an ice bath. Can I come?"), P("ได้ค่ะ ทีมงานจะอธิบายวิธีก่อนเริ่ม ครั้งแรกค่อย ๆ แช่ช่วงสั้น ๆ ตามที่ไหว", "Yes. Our team will explain how to get started. Begin with short dips at your own pace.")],
      [P("ต้องเตรียมอะไรมา", "What should I bring?"), P("ชุดว่ายน้ำหรือชุดที่เปียกได้ และมาก่อนเวลา 10–15 นาที", "Swimwear or clothes that can get wet. Please arrive 10–15 minutes early.")],
      [P(`${baht(C.price)} รวมอะไรบ้าง`, `What does ${baht(C.price)} include?`), P(`Ice Bath และ Sauna ใช้ได้ ${C.duration / 60} ชั่วโมงนับจากเวลาที่จอง ต่อคน${pairOn() ? ` มา 2 คนจ่าย ${baht(C.pair_price)}` : ""}`, `The ice bath and sauna for ${C.duration / 60} hours from your booked time, per person.${pairOn() ? ` Two people pay ${baht(C.pair_price)}.` : ""}`)],
      [P("ชำระเงินอย่างไร", "How do I pay?"), P(`โอนผ่านพร้อมเพย์ตอนจอง แล้วแนบสลิป ต้องชำระภายใน ${C.hold_min || 15} นาที ไม่อย่างนั้นที่นั่งจะถูกปล่อยให้คนอื่น`, `Pay by PromptPay when you book and attach the slip. Pay within ${C.hold_min || 15} minutes or the places are released.`)],
      [P("มากันเป็นกลุ่มได้ไหม", "Can we come as a group?"), P(`ได้ รับรอบละไม่เกิน ${C.capacity} คน (เสาร์-อาทิตย์และวันหยุดนักขัตฯ ${C.capacity_weekend || C.capacity} คน) เลือกจำนวนคนตอนจอง ถ้าต้องการจองทั้งรอบหรือกลุ่มใหญ่ โทรหาเรา`, `Yes. Each session takes up to ${C.capacity} people (${C.capacity_weekend || C.capacity} on weekends and public holidays). For a private session or a bigger group, please call us.`)],
      [P("เลื่อนหรือยกเลิกได้ไหม", "Can I move or cancel?"), policyLine()],
      [P("มีข้อควรระวังด้านสุขภาพไหม", "Any health precautions?"), P("การแช่น้ำเย็นและซาวน่าอาจไม่เหมาะกับผู้มีโรคหัวใจ ความดันสูงที่ยังคุมไม่ได้ หรือผู้ตั้งครรภ์ ถ้าไม่แน่ใจกรุณาปรึกษาแพทย์ก่อน และบอกทีมงานเมื่อมาถึง", "Cold water and sauna may not suit people with heart conditions, uncontrolled high blood pressure, or who are pregnant. If unsure, ask your doctor first and tell our team when you arrive.")],
    ];
  }

  // ---------- customer sign-in: email + password (sign up once, then sign in) ----------
  function loginPanel(note) {
    const m = R.authMode || "in";
    if (R.authSent) return `<div class="login-box" id="otp-box"><p>${R.authSent}</p><p class="muted small">${P("หากยังไม่พบอีเมล ลองตรวจสอบโฟลเดอร์อีเมลขยะ", "If you don't see the email, check your spam folder.")}</p><button class="btn btn--ghost" type="button" data-auth-mode="in">${P("กลับไปหน้าเข้าสู่ระบบ", "Back to sign in")}</button></div>`;
    const tab = (k, th, en) => `<button type="button" class="chip${m === k ? " on" : ""}" data-auth-mode="${k}" aria-pressed="${m === k}">${P(th, en)}</button>`;
    return `<div class="login-box" id="otp-box">${note ? `<p>${note}</p>` : ""}
      ${m === "forgot" ? `<p>${P("ใส่อีเมลที่ใช้สมัคร เราจะส่งลิงก์ตั้งรหัสผ่านใหม่ไปให้", "Enter your email and we'll send a link to set a new password.")}</p>` : `<div class="btn-row auth-tabs">${tab("in", "เข้าสู่ระบบ", "Sign in")}${tab("up", "สมัครใหม่", "New account")}</div>`}
      <div id="auth-form">
      <div class="field"><label for="au-email">${P("อีเมล", "Email")}</label><input id="au-email" type="email" autocomplete="email" maxlength="254" value="${esc(R.otpTyped || "")}"></div>
      ${m === "forgot" ? "" : `<div class="field"><label for="au-pass">${P("รหัสผ่าน", "Password")}${m === "up" ? ` <span class="muted small">${P("(อย่างน้อย 8 ตัว)", "(at least 8 characters)")}</span>` : ""}</label><input id="au-pass" type="password" minlength="8" maxlength="72" autocomplete="${m === "up" ? "new-password" : "current-password"}"></div>`}
      <div id="captcha" class="captcha"></div>
      <button class="btn btn--primary" type="button" data-auth-go>${m === "up" ? P("สร้างบัญชี", "Create account") : m === "forgot" ? P("ส่งลิงก์ตั้งรหัสผ่านใหม่", "Send reset link") : P("เข้าสู่ระบบ", "Sign in")}</button>
      </div>
      ${m === "in" ? `<p class="small"><a href="#" data-auth-mode="forgot">${P("ลืมรหัสผ่าน", "Forgot password?")}</a></p>` : m === "forgot" ? `<p class="small"><a href="#" data-auth-mode="in">${P("กลับไปหน้าเข้าสู่ระบบ", "Back to sign in")}</a></p>` : ""}</div>`;
  }
  function authErr(msg) {
    if (/invalid_credentials|invalid login/i.test(msg)) return P("อีเมลหรือรหัสผ่านไม่ถูกต้อง", "Wrong email or password.");
    if (/email_not_confirmed|not confirmed/i.test(msg)) return P("ยังไม่ได้ยืนยันอีเมล กดลิงก์ในอีเมลที่เราส่งไปตอนสมัครก่อน", "Please confirm your email first using the link we sent when you signed up.");
    if (/user_already_exists|already registered/i.test(msg)) return P("อีเมลนี้มีบัญชีอยู่แล้ว กรุณาเข้าสู่ระบบ หรือเลือก “ลืมรหัสผ่าน”", "That email already has an account. Sign in, or use Forgot password.");
    if (/weak_password|password/i.test(msg)) return P("รหัสผ่านสั้นหรือคาดเดาง่ายเกินไป กรุณาใช้อย่างน้อย 8 ตัวอักษร", "That password is too short or too simple. Use at least 8 characters.");
    if (/rate|seconds|many/i.test(msg)) return P("ทำรายการถี่เกินไป รอสักครู่แล้วลองใหม่", "Too many tries. Please wait a moment and try again.");
    if (/captcha/i.test(msg)) return P("การตรวจสอบว่าไม่ใช่บอทไม่สำเร็จ กรุณาลองอีกครั้ง", "The robot check didn't pass. Please try again.");
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
        R.flashErr = ""; R.authMode = "in"; pwKeep = ""; R.profile = undefined; R.pf = null;
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

  // ---------- booking: day → time → people → details → confirm ----------
  async function viewBook() {
    if (R.pre) { const [d, h] = R.pre.split("|"); BK.day = d; BK.hm = h; R.pre = ""; }
    if (cust && !(await ensureProfile())) return toProfile("book");
    if (cust && R.profile && !BK.name && !BK.phone) { BK.name = (R.profile.first_name + " " + R.profile.last_name).trim(); BK.phone = R.profile.phone; }
    if (cust && !BK.name && !BK.phone) {
      const me = store.get(ME_KEY);
      if (me && me.email === cust.email) { BK.name = me.name || ""; BK.phone = me.phone || ""; }
      else if (!R.meTried) { R.meTried = true; try { const mb = await rpc("my_bookings", {}, "cust"); if (mb && mb.last) { BK.name = mb.last.name || ""; BK.phone = mb.last.phone || ""; } } catch (e) { /* first booking */ } }
    }
    const from = today(), days = Math.min(Number(C.horizon_days) + 1, 42);
    let avail = [];
    try { avail = await rpc("availability", { p_from: from, p_days: days, p_people: 1 }); } catch (e) { R.flashErr = errText(e); }
    const map = {}; avail.forEach((d) => (map[d.day] = d));
    const dayState = (k) => { const d = map[k]; if (!d || !d.slots.length) return "closed"; return d.slots.some((s) => s.ok) ? "open" : "full"; };
    const list = Array.from({ length: days }, (_, i) => addDays(from, i));
    if (!BK.day || dayState(BK.day) === "closed") BK.day = list.find((k) => dayState(k) === "open") || "";
    const slots = BK.day && map[BK.day] ? map[BK.day].slots : [];
    if (!slots.some((s) => s.hm === BK.hm && s.ok)) BK.hm = "";
    // how many people this booking can take: the chosen session's free places, never more than the online group limit
    const pick = slots.find((s) => s.hm === BK.hm);
    const maxPeople = Math.max(1, Math.min(Number(C.max_group) || 8, pick ? pick.remaining : Number(C.max_group) || 8));
    if (BK.people > maxPeople) BK.people = maxPeople;
    if (BK.people < 1) BK.people = 1;
    const endOf = (hm) => { const [h, m] = hm.split(":").map(Number); const t = h * 60 + m + Number(C.duration); return String(Math.floor(t / 60) % 24).padStart(2, "0") + ":" + String(t % 60).padStart(2, "0"); };
    const slotNote = (s) => (s.remaining <= 0 ? P("เต็ม", "Full") : s.remaining <= 3 ? P(`เหลือ ${s.remaining} ที่`, `${s.remaining} left`) : P("ว่าง", "Open"));
    const total = priceFor(BK.people);
    const fe = BK.fieldErr;
    const field = (k, label, input) => `<div class="field${fe[k] ? " has-error" : ""}"><label for="f-${k}">${label}</label>${input}${fe[k] ? `<span class="err">${esc(fe[k])}</span>` : ""}</div>`;
    const main = `
<header class="page-head"><div class="wrap"><h1>${P("จองรอบ", "Book your session")}</h1><p class="lede">${P(`${baht(C.price)} ต่อคน${pairOn() ? ` · 2 คน ${baht(C.pair_price)}` : ""} · ใช้บริการได้ ${C.duration / 60} ชั่วโมง · โอนจ่ายตอนจอง`, `${baht(C.price)} per person${pairOn() ? ` · 2 people ${baht(C.pair_price)}` : ""} · ${C.duration / 60} hours · pay when you book`)}</p></div></header>
<section style="padding-bottom:40px"><div class="wrap">
  ${flashHTML()}${BK.err ? `<div class="alert alert--bad" role="alert">${ICON.alert}<p>${esc(BK.err)}</p></div>` : ""}
  <form class="book" id="book" novalidate>
    <div class="book-main">
      <div class="step"><div class="step-title"><span class="num" aria-hidden="true"></span><h2>${P("เลือกวัน", "Choose a day")}</h2></div>
        <div class="dates" role="group" aria-label="${P("วันที่", "Date")}">${list.map((k) => { const st = dayState(k); return `<button type="button" class="date" data-day="${k}" data-status="${st}" aria-pressed="${k === BK.day}"${st === "closed" ? " disabled" : ""} aria-label="${esc(dShort(k))}"><span class="w">${esc(wd(k))}</span><span class="n">${Number(k.slice(8))}</span><span class="m">${esc(mon(k))}</span><span class="a" aria-hidden="true">${st === "closed" ? P("ปิด", "Closed") : st === "full" ? P("เต็ม", "Full") : ""}</span></button>`; }).join("")}</div>
      </div>
      <div class="step"><div class="step-title"><span class="num" aria-hidden="true"></span><h2>${P("เลือกเวลา", "Choose a time")}</h2></div>
        <p class="hint hint--top">${P(`ใช้บริการได้ ${C.duration / 60} ชั่วโมงนับจากเวลาที่เลือก`, `You can stay ${C.duration / 60} hours from your start time.`)}</p>
        ${!BK.day ? `<p class="state">${P("ช่วงนี้รอบเต็มหมดแล้ว โทรหาเราได้เลย", "Every session is booked up right now. Please call us.")}</p>` :
          !slots.some((s) => s.ok) ? `<p class="state">${P("วันนี้เต็มแล้ว ลองวันอื่น", "That day is full. Try another day.")}</p>` :
          `<div class="times" role="group">${slots.map((s) => `<button type="button" class="time" data-hm="${s.hm}" aria-pressed="${s.hm === BK.hm}"${s.ok ? "" : " disabled"}><b>${s.hm}</b>${s.remaining <= 0 ? `<span>${P("เต็ม", "Full")}</span>` : `<span class="until">${P("ถึง " + endOf(s.hm), "until " + endOf(s.hm))}</span>${s.remaining <= 3 ? `<span class="low">${slotNote(s)}</span>` : ""}`}</button>`).join("")}</div>`}
      </div>
      <div class="step"><div class="step-title"><span class="num" aria-hidden="true"></span><h2>${P("จำนวนผู้ใช้บริการ", "How many people?")}</h2></div>
        <div class="stepper" role="group" aria-label="${P("จำนวนคน", "Number of people")}">
          <button type="button" class="stepper-btn" data-pp="-1" aria-label="${P("ลดจำนวนคน", "Fewer people")}"${BK.people <= 1 ? " disabled" : ""}>−</button>
          <output class="stepper-val" aria-live="polite"><b>${BK.people}</b> ${P("คน", BK.people > 1 ? "people" : "person")}</output>
          <button type="button" class="stepper-btn" data-pp="1" aria-label="${P("เพิ่มจำนวนคน", "More people")}"${BK.people >= maxPeople ? " disabled" : ""}>+</button>
          <span class="stepper-price">${baht(total)}${pairOn() && BK.people >= 2 ? ` <small>${P("ราคาโปร 2 คน", "2-person deal")}</small>` : ""}</span>
        </div>
        <p class="hint">${pick && pick.remaining < (Number(C.max_group) || 8) ? P(`รอบ ${BK.hm} รับได้อีก ${pick.remaining} คน`, `The ${BK.hm} session has room for ${pick.remaining} more.`) : pairOn() ? P(`มา 2 คนจ่าย ${baht(C.pair_price)} มากกว่า ${C.max_group} คนหรืออยากเหมารอบ โทรหาเรา`, `Two people pay ${baht(C.pair_price)}. More than ${C.max_group}, or a private session? Call us.`) : P(`มากกว่า ${C.max_group} คนหรืออยากเหมารอบ โทรหาเรา`, `More than ${C.max_group}, or a private session? Call us.`)}</p>
      </div>
      <div class="step"><div class="step-title"><span class="num" aria-hidden="true"></span><h2>${P("ข้อมูลผู้จอง", "Your details")}</h2></div>
        ${!cust ? loginPanel(P("เข้าสู่ระบบเพื่อจองรอบ หากยังไม่มีบัญชี เลือก “สมัครใหม่” แล้วกรอกอีเมลและตั้งรหัสผ่าน", "Sign in to book. If you don't have an account, choose New account and enter your email and a password.")) : `${signedInLine()}
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
      <div class="sum-total"><span>${P("ยอดที่ต้องโอน", "To pay")}</span><b>${baht(total)}</b></div>
      <button class="btn btn--primary btn--block submit-desktop" type="submit"${R.busy ? " disabled" : ""}>${P("จองและไปหน้าชำระเงิน", "Book and pay")}</button>
      <p class="paynote">${ICON.info}<span>${P(`ขั้นต่อไปจะได้ QR พร้อมเพย์ โอนแล้วแนบสลิปภายใน ${C.hold_min || 15} นาที ระบบกันที่ไว้ให้ระหว่างนี้`, `Next you'll get a PromptPay QR. Pay and attach the slip within ${C.hold_min || 15} minutes; we hold your places meanwhile.`)}</span></p>
    </div></aside>
  </form>
</div></section>`;
    return pubLayout(main, { paybar: true, after: `<div class="paybar"><div class="tot"><small>${BK.people} ${P("คน", "people")}${BK.hm ? " · " + BK.hm : ""}</small><b>${baht(total)}</b></div><button class="btn btn--primary" type="submit" form="book"${R.busy ? " disabled" : ""}>${P("จองและชำระเงิน", "Book and pay")}</button></div>` });
  }
  function bindBook() {
    $$("[data-pp]").forEach((b) => b.addEventListener("click", () => { BK.people += Number(b.dataset.pp); BK.err = ""; render(); }));
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
        if (e.code === "slot_full" || e.code === "too_soon" || e.code === "slot_closed") BK.hm = "";
        if (e.code === "login" || /JWT/i.test(e.message)) custOut();
        BK.err = errText(e); render();
      }
    });
  }

  // ---------- paying for an online booking: PromptPay QR, then the slip ----------
  let holdTimer = null;
  function payBox(b, owe, checking, lapsed) {
    if (checking) return `<div class="paybox paybox--done"><p>${ICON.ok} ${P("ทีมงานกำลังตรวจสลิป ระหว่างนี้ที่นั่งของคุณถูกกันไว้แล้ว ไม่ต้องทำอะไรเพิ่ม", "Our team is checking your slip. Your places are held; there's nothing else to do.")}</p></div>`;
    const payload = C.promptpay ? promptPayPayload(C.promptpay, b.total) : "";
    return `<section class="paybox" aria-labelledby="pay-h">
    <div class="paybox-head"><h2 id="pay-h">${lapsed ? P("โอนแล้วแต่ไม่ทัน?", "Paid but ran out of time?") : P("ชำระเงิน", "Pay now")}</h2>${owe && b.hold_until ? `<p class="hold" data-hold="${esc(b.hold_until)}">${P("เหลือเวลา", "Time left")} <b data-left>–</b></p>` : ""}</div>
    ${lapsed ? `<p class="muted">${P("ถ้าโอนไปแล้ว แนบสลิปได้เลย ถ้ารอบนี้ยังว่าง ระบบจะคืนที่ให้ทันที", "If you've already paid, attach the slip. If the session still has room, we'll put your booking back.")}</p>` : ""}
    <div class="paybox-body">
      ${payload ? `<figure class="qr-wrap">${qrSVG(payload)}<figcaption>${P("สแกนด้วยแอปธนาคาร ยอดขึ้นให้อัตโนมัติ", "Scan with your banking app; the amount fills in")}</figcaption>
        <button class="btn btn--small" type="button" data-qrsave="${esc(b.ref)}">${P("บันทึกรูป QR", "Save QR image")}</button>
        <p class="qr-tip">${P("จองในมือถือเครื่องเดียวกับที่ใช้จ่าย? บันทึกรูป QR แล้วเปิดแอปธนาคาร เลือก “สแกน” → “รูปภาพ”", "Paying on this same phone? Save the QR, then in your banking app choose Scan → from photo.")}</p></figure>` : ""}
      <div class="pay-info">
        <p class="pay-amt"><small>${P("ยอดโอน", "Amount")}</small><b>${baht(b.total)}</b></p>
        ${C.promptpay ? `<p><small>${P("พร้อมเพย์", "PromptPay")}</small><span class="mono">${esc(C.promptpay)}</span></p>` : ""}
        ${C.bank_account ? `<p><small>${esc(C.bank_name || P("บัญชีธนาคาร", "Bank account"))}</small><span class="mono">${esc(C.bank_account)}</span> <button class="btn btn--small btn--ghost" type="button" data-copy="${esc(String(C.bank_account).replace(/\D/g, ""))}">${P("คัดลอก", "Copy")}</button></p>` : ""}
        ${C.bank_holder ? `<p><small>${P("ชื่อบัญชี", "Account name")}</small><span>${esc(C.bank_holder)}</span></p>` : ""}
        ${!hasPayInfo() ? `<p class="note-box">${esc(P("ขออภัย ร้านกำลังตั้งค่าบัญชีรับเงิน กรุณาโทร ", "Sorry, our payment details aren't set up yet. Please call ") + C.phone)}</p>` : ""}
        <div class="field slip-field"><label for="slip">${P("แนบสลิปการโอน", "Attach your transfer slip")}</label><input type="file" id="slip" accept="image/*"></div>
        <button class="btn btn--primary btn--block" type="button" data-slip-send disabled>${P("ส่งสลิป", "Send slip")}</button>
      </div>
    </div>
  </section>`;
  }
  function bindPayBox() {
    clearInterval(holdTimer);
    const h = $("[data-hold]");
    if (h) {
      const end = new Date(h.dataset.hold).getTime(), out = $("[data-left]", h);
      const tick = () => {
        const ms = end - (Date.now() + (R.skew || 0)); // the server's clock, not the phone's
        if (!document.body.contains(h)) return clearInterval(holdTimer);
        if (ms <= 0) { clearInterval(holdTimer); out.textContent = "0:00"; if (R.holdShown !== h.dataset.hold) { R.holdShown = h.dataset.hold; render(); } return; }
        const m = Math.floor(ms / 60000), sec = Math.floor((ms % 60000) / 1000);
        out.textContent = m + ":" + String(sec).padStart(2, "0");
        h.classList.toggle("is-late", ms < 3 * 60000);
      };
      tick(); holdTimer = setInterval(tick, 1000);
    }
    const qs = $("[data-qrsave]");
    if (qs) qs.addEventListener("click", () => {
      const svg = $(".qr-wrap svg"); if (!svg) return;
      const img = new Image(), px = 720;
      img.onload = () => {
        const cv = document.createElement("canvas"); cv.width = px; cv.height = px + 110;
        const cx = cv.getContext("2d"); cx.fillStyle = "#fff"; cx.fillRect(0, 0, cv.width, cv.height); cx.imageSmoothingEnabled = false; cx.drawImage(img, 0, 0, px, px);
        cx.fillStyle = "#22302a"; cx.textAlign = "center"; cx.font = "600 34px sans-serif"; cx.fillText("Peaceful Day · " + qs.dataset.qrsave, px / 2, px + 50);
        cx.font = "28px sans-serif"; cx.fillText($(".pay-amt b").textContent, px / 2, px + 92);
        const a = document.createElement("a"); a.download = "peacefulday-" + qs.dataset.qrsave + ".png"; a.href = cv.toDataURL("image/png"); document.body.appendChild(a); a.click(); a.remove();
      };
      img.src = "data:image/svg+xml;charset=utf-8," + encodeURIComponent(new XMLSerializer().serializeToString(svg));
    });
    const f = $("#slip"), send = $("[data-slip-send]");
    if (!f || !send) return;
    f.addEventListener("change", () => { send.disabled = !f.files.length; });
    send.addEventListener("click", async () => {
      send.disabled = true; send.textContent = P("กำลังส่ง…", "Sending…");
      try {
        const data = await slipData(f.files[0]);
        await rpc("submit_slip", { p_ref: R.ref, p_token: R.token, p_image: data });
        send.textContent = P("กำลังตรวจสลิป…", "Checking your slip…");
        // the server checks every slip on its own; wait up to ~40 seconds for the answer
        let nb = null;
        for (let i = 0; i < 16; i++) {
          await new Promise((r) => setTimeout(r, 2500));
          try { nb = await rpc("get_booking", { p_ref: R.ref, p_token: R.token }); } catch (e2) { nb = null; }
          if (nb && nb.pay !== "review") break;
        }
        if (nb && nb.pay === "paid") { R.flash = P("ตรวจสลิปผ่านแล้ว การจองของคุณยืนยันเรียบร้อย แล้วพบกันที่สวน", "Slip checked. Your booking is confirmed. See you in the garden."); R.flashErr = ""; }
        else if (nb && nb.pay === "await") { R.flashErr = P("สลิปนี้ยังใช้ไม่ได้: ", "This slip didn't pass: ") + (nb.slip_note || "") + P(" กรุณาแนบสลิปที่ถูกต้องอีกครั้ง หรือโทรหาเรา", ". Please attach the right slip, or call us."); R.flash = ""; }
        else { R.flash = P("ส่งสลิปแล้ว ทีมงานจะตรวจและยืนยันการจองให้", "Slip sent. Our team will check it and confirm your booking."); R.flashErr = ""; }
      } catch (e) { R.flashErr = errText(e); R.flash = ""; }
      R.justBooked = false; render(); window.scrollTo(0, 0);
    });
  }

  // ---------- a customer's booking page ----------
  async function viewBooking() {
    let b = null;
    try { b = await rpc("get_booking", { p_ref: R.ref, p_token: R.token }); } catch (e) { b = null; }
    if (!b) return pubLayout(`<section class="err-page"><div class="wrap"><h1>${P("ไม่พบการจองนี้", "Booking not found")}</h1><p class="lede">${P("ลิงก์อาจไม่ครบ เข้าสู่ระบบด้วยอีเมลที่ใช้จอง แล้วดูที่ “การจองของฉัน”", "The link may be incomplete. Sign in with the email you booked with and open My bookings.")}</p><a class="btn btn--primary" href="#my" data-go="my">${P("ค้นหาการจอง", "Find my booking")}</a></div></section>`);
    const live = b.status === "confirmed" || b.status === "checked_in";
    const owe = b.status === "confirmed" && b.pay === "await", checking = b.status === "confirmed" && b.pay === "review", lapsed = b.status === "cancelled" && b.pay === "expired";
    const st = { waiting: P("รอนัดวันใหม่", "Waiting for a new date"), confirmed: owe ? P("รอชำระเงิน", "Awaiting payment") : checking ? P("รอตรวจสลิป", "Checking your slip") : P("จองแล้ว", "Booked"), checked_in: P("เช็คอินแล้ว", "Checked in"), cancelled: P("ยกเลิกแล้ว", "Cancelled"), no_show: P("ไม่ได้มา", "Missed") }[b.status];
    const link = location.origin + location.pathname + "#booking/" + b.ref + "/" + R.token;
    const moveDays = R.moveOpen ? await (async () => { try { return await rpc("availability", { p_from: today(), p_days: Math.min(Number(C.horizon_days) + 1, 42), p_people: b.people }); } catch (e) { return []; } })() : [];
    const md = R.moveDay && moveDays.find((d) => d.day === R.moveDay);
    return pubLayout(`<section class="section" style="padding-top:clamp(28px,5vw,56px)"><div class="wrap">
  ${R.justBooked && !owe ? `<div class="alert alert--ok" role="status">${ICON.ok}<p>${P("จองเรียบร้อยแล้ว ดูรายละเอียดและตัวเลือกเลื่อนหรือยกเลิกได้ที่", "You're booked. View booking details and available rescheduling or cancellation options in")} <a href="#my" data-go="my">${P("การจองของฉัน", "My bookings")}</a>${P(" (เข้าสู่ระบบด้วยอีเมลเดิม)", " (sign in with the same email)")}</p></div>` : ""}${flashHTML()}
  ${owe || checking || lapsed ? payBox(b, owe, checking, lapsed) : ""}
  <article class="ticket">
    <div class="ticket-top"><p class="status"><span class="badge ${live && !owe && !checking ? "badge--ok" : live ? "badge--warn" : "badge--bad"}">${st}</span></p><h1>${owe ? P("กันที่ไว้ให้แล้ว รอชำระเงิน", "Places held. Please pay to confirm") : checking ? P("ได้รับสลิปแล้ว", "We've got your slip") : live ? P("แล้วพบกันที่สวน", "See you in the garden") : lapsed ? P("หมดเวลาชำระเงิน", "Payment time ran out") : st}</h1>${live && !owe ? `<p class="muted">${P("มาก่อนเวลา 10–15 นาที และเตรียมชุดที่เปียกได้", "Please arrive 10–15 minutes early with clothes that can get wet.")}</p>` : ""}</div>
    <div class="ticket-body">
      <div><p class="muted" style="margin:0">${P("รหัสการจอง", "Booking reference")}</p><p class="ticket-ref" style="margin:0">${esc(b.ref)}</p></div>
      <dl class="kv">
        <dt>${P("วัน", "Day")}</dt><dd>${esc(dLong(b.day))}</dd>
        <dt>${P("เวลา", "Time")}</dt><dd>${esc(b.hm)}–${esc(b.end)}</dd>
        <dt>${P("จำนวน", "People")}</dt><dd>${b.people}</dd>
        <dt>${P("ชื่อ", "Name")}</dt><dd>${esc(b.name)}</dd>
        <dt>${P("ยอด", "Total")}</dt><dd>${baht(b.total)} ${b.pay === "paid" ? P("(ชำระแล้ว)", "(paid)") : b.pay === "due" ? P("(ชำระที่ร้าน)", "(pay at the shop)") : b.pay === "await" ? P("(รอโอน)", "(not paid yet)") : b.pay === "review" ? P("(รอตรวจสลิป)", "(slip being checked)") : ""}</dd>
      </dl>
      ${live ? `<div class="field"><label for="my-link">${P("เก็บลิงก์นี้ไว้ดูการจอง", "Save your booking link")}</label><div class="code-row"><input id="my-link" type="text" readonly value="${esc(link)}"><button class="btn btn--small" type="button" data-copy="${esc(link)}">${P("คัดลอก", "Copy")}</button></div></div>` : ""}
    </div>
  </article>
  ${b.status === "confirmed" ? `<div class="manage">
    ${b.can_move ? `<details${R.moveOpen ? " open" : ""} data-move-toggle><summary>${P("เลื่อนวันหรือเวลา", "Reschedule your booking")}</summary><div class="inner">
      ${R.moveOpen ? `<div class="dates">${moveDays.map((d) => { const ok = d.slots.some((s) => s.ok); return `<button type="button" class="date" data-mday="${d.day}" aria-pressed="${d.day === R.moveDay}"${ok ? "" : " disabled"}><span class="w">${esc(wd(d.day))}</span><span class="n">${Number(d.day.slice(8))}</span><span class="m">${esc(mon(d.day))}</span></button>`; }).join("")}</div>
      ${md ? `<div class="times">${md.slots.map((s) => `<button type="button" class="time" data-mhm="${s.hm}" aria-pressed="${s.hm === R.moveHm}"${s.ok && !(md.day === b.day && s.hm === b.hm) ? "" : " disabled"}><b>${s.hm}</b><span class="${s.remaining > 0 && s.remaining <= 3 ? "low" : ""}">${s.remaining <= 0 ? P("เต็ม", "Full") : s.remaining <= 3 ? P("เหลือ " + s.remaining + " ที่", s.remaining + " left") : P("ถึง " + endHM(s.hm), "until " + endHM(s.hm))}</span></button>`).join("")}</div>` : ""}
      <button class="btn btn--primary mt-m" type="button" data-move-go${R.moveHm ? "" : " disabled"}>${P("ยืนยันเวลาใหม่", "Confirm new time")}</button>` : ""}
      <p class="muted"><small>${P(`เลื่อนได้ ${C.max_moves} ครั้ง ก่อนเวลาเริ่ม ${C.cancel_hours} ชั่วโมง`, `Reschedule up to ${C.max_moves} times, at least ${C.cancel_hours} hours before the start.`)}</small></p></div></details>` : ""}
    ${b.can_cancel ? `<details><summary>${P("ยกเลิกการจอง", "Cancel booking")}</summary><div class="inner"><p>${P("ยกเลิกแล้วที่นั่งจะเปิดให้คนอื่นจองทันที", "Your places will be released for others straight away.")}</p><button class="btn btn--danger" type="button" data-cancel data-confirm="${P("กดอีกครั้งเพื่อยืนยันการยกเลิก", "Tap again to confirm")}">${P("ยกเลิกการจองนี้", "Cancel this booking")}</button></div></details>` : ""}
    ${!b.can_cancel && (b.pay === "paid" || b.pay === "review") ? `<p class="muted small">${esc((b.pay === "review" ? P("ส่งสลิปแล้ว ถ้าต้องการยกเลิก กรุณาโทร ", "Slip sent. To cancel, please call ") : P("ชำระเงินแล้ว ถ้าต้องการยกเลิก กรุณาโทร ", "Already paid. To cancel, please call ")) + C.phone)}</p>` : ""}
    ${!b.can_move && b.moves >= Number(C.max_moves) && new Date(b.cutoff) > new Date() ? `<p class="muted small">${esc(P(`เลื่อนรอบครบ ${C.max_moves} ครั้งแล้ว ถ้าต้องเปลี่ยนอีก กรุณาโทร `, `You've used all ${C.max_moves} reschedule(s). To change again, please call `) + C.phone)}</p>` : ""}
    ${!b.can_move && !b.can_cancel && !(b.moves >= Number(C.max_moves) && new Date(b.cutoff) > new Date()) ? `<p class="note-box">${esc(P("เลยกำหนดเปลี่ยนแปลงออนไลน์แล้ว กรุณาโทร ", "It's past the online change deadline. Please call ") + C.phone)}</p>` : ""}
  </div>` : ""}
  ${live && !owe ? `<div class="note-box waiver-cta"><p><b>${P("ลงทะเบียนผู้ใช้บริการล่วงหน้า", "Register everyone ahead")}</b> ${P("ทุกคนในกลุ่ม รวมถึงเพื่อนที่มาด้วย ต้องกรอกชื่อ อีเมล เบอร์ และลงชื่อรับทราบความเสี่ยง ส่งลิงก์ให้เพื่อนทำในมือถือได้เลย ถึงร้านจะได้เข้าใช้ทันที", "Everyone in your group, including friends, gives their name, email and phone and signs the risk acknowledgment. Send friends the link; you'll go straight in when you arrive.")}</p><a class="btn btn--small btn--primary" href="#waiver/${esc(b.ref)}/${esc(b.wtoken)}">${P("ลงทะเบียน", "Register")}</a><button class="btn btn--small" type="button" data-copy="${esc(location.origin + location.pathname + "#waiver/" + b.ref + "/" + b.wtoken)}">${P("คัดลอกลิงก์ส่งให้เพื่อน", "Copy link for friends")}</button></div>` : ""}
  <p class="mt-m"><a class="btn" href="#book" data-go="book">${P("จองรอบอื่น", "Book another session")}</a></p>
</div></section>`);
  }
  function bindBooking() {
    bindPayBox();
    const tg = $("[data-move-toggle]");
    if (tg) tg.addEventListener("toggle", () => { if (tg.open && !R.moveOpen) { R.moveOpen = true; render(); } });
    $$("[data-mday]").forEach((b) => b.addEventListener("click", () => { R.moveDay = b.dataset.mday; R.moveHm = ""; render(); }));
    $$("[data-mhm]").forEach((b) => b.addEventListener("click", () => { R.moveHm = b.dataset.mhm; render(); }));
    const mg = $("[data-move-go]");
    if (mg) mg.addEventListener("click", async () => {
      try { await rpc("move_booking", { p_ref: R.ref, p_token: R.token, p_day: R.moveDay, p_hm: R.moveHm }); R.flash = P("เลื่อนการจองเรียบร้อย", "Your booking has been rescheduled."); R.flashErr = ""; }
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

  // ---------- member profile: filled in once after signing up ----------
  async function ensureProfile() {
    if (!cust) return true;
    if (R.profile === undefined) { try { R.profile = await rpc("my_profile", {}, "cust"); } catch (e) { if (e.code === "login" || /JWT/i.test(e.message)) custOut(); return true; } }
    return !!R.profile;
  }
  function toProfile(after) { R.afterProfile = after; R.view = "profile"; try { history.replaceState(null, "", "#profile"); } catch (e) { /* ignore */ } return viewProfile(); }
  async function viewProfile() {
    if (cust && R.profile === undefined) await ensureProfile();
    if (cust && window.__pfdVerify) {
      window.__pfdVerify = false;
      try { const pr = await rpc("verify_email", {}, "cust"); if (pr) { R.profile = pr; if (pr.email_verified) R.flash = P("ยืนยันอีเมลแล้ว ถ้าลืมรหัสผ่าน เราจะส่งลิงก์ตั้งรหัสใหม่ไปที่อีเมลนี้", "Email confirmed. If you forget your password, we'll send the reset link here."); } } catch (e) { /* not important enough to block the page */ }
    }
    const pr = R.profile || {}, first = !R.profile, F = R.pf || (R.pf = { phone: (pr.phone || BK.phone || "").replace(/^0/, ""), first_name: pr.first_name || "", last_name: pr.last_name || "", birth_date: pr.birth_date || "", gender: pr.gender || "", marketing_consent: !!pr.marketing_consent });
    const fe = R.pfErr || {};
    const field = (k, label, input, opt) => `<div class="field${fe[k] ? " has-error" : ""}"><label for="pf-${k}">${label}${opt ? ` <span class="muted small">${P("(ไม่บังคับ)", "(optional)")}</span>` : ""}</label>${input}${fe[k] ? `<span class="err">${esc(fe[k])}</span>` : ""}</div>`;
    const g = (v, th, en) => `<label class="radio"><input type="radio" name="pf-gender" value="${v}"${F.gender === v ? " checked" : ""}><span>${P(th, en)}</span></label>`;
    return pubLayout(`<header class="page-head"><div class="wrap"><h1>${!cust ? P("ข้อมูลสมาชิก", "Your profile") : first ? P("กรอกข้อมูลสมาชิก", "Complete your profile") : P("ข้อมูลสมาชิก", "Your profile")}</h1>${first && cust ? `<p class="lede">${P("กรอกครั้งเดียว ครั้งต่อไปจองได้เลยไม่ต้องกรอกซ้ำ", "Fill this in once and booking is quicker next time.")}</p>` : ""}</div></header>
<section class="section" style="padding-top:8px"><div class="wrap">${flashHTML()}
  ${!cust ? `<div class="panel" style="max-width:520px">${loginPanel(P("เข้าสู่ระบบก่อน", "Please sign in first."))}</div>` : `
  <form class="panel profile-form" id="pf" novalidate>
    <h2>${P("ข้อมูลบัญชี", "Account")}</h2>
    <p class="muted small">${P("อีเมล", "Email")}: <b>${esc(cust.email)}</b>${pr.email_verified ? ` <span class="badge badge--ok">${P("ยืนยันแล้ว", "Confirmed")}</span>` : ""}</p>
    ${!first && !pr.email_verified ? `<div class="verify-box">${R.verifySent ? `<p class="small">${ICON.ok} ${P(`ส่งลิงก์ไปที่ ${esc(cust.email)} แล้ว เปิดอีเมลแล้วกดลิงก์ภายใน 1 ชั่วโมง (ดูในจดหมายขยะด้วย)`, `We've sent a link to ${esc(cust.email)}. Open it within an hour (check spam too).`)}</p>` : `<p class="small">${P("ยืนยันอีเมลไว้ (ไม่บังคับ) เพื่อให้แน่ใจว่าได้รับลิงก์ตอนลืมรหัสผ่าน", "Confirm your email (optional) so you're sure to get the link if you forget your password.")}</p>${R.verifyOpen ? `<div id="captcha" class="captcha"></div>` : ""}<button class="btn btn--small" type="button" data-verify>${R.verifyOpen ? P("ส่งลิงก์ยืนยัน", "Send the link") : P("ยืนยันอีเมล", "Confirm email")}</button>`}</div>` : ""}
    ${field("phone", P("เบอร์มือถือ", "Mobile number"), `<div class="tel-row"><span class="tel-cc" aria-hidden="true">🇹🇭 +66</span><input id="pf-phone" type="tel" inputmode="tel" autocomplete="tel-national" maxlength="12" placeholder="8x xxx xxxx" value="${esc(F.phone)}" aria-label="${P("เบอร์มือถือ ไม่ต้องใส่ 0 ข้างหน้า", "Mobile number")}"></div>`)}
    <h2>${P("ข้อมูลส่วนตัว", "Personal details")}</h2>
    <div class="row-2">${field("first_name", P("ชื่อ", "First name"), `<input id="pf-first_name" type="text" autocomplete="given-name" maxlength="60" value="${esc(F.first_name)}">`)}${field("last_name", P("นามสกุล", "Last name"), `<input id="pf-last_name" type="text" autocomplete="family-name" maxlength="60" value="${esc(F.last_name)}">`, true)}</div>
    ${field("birth_date", P("วันเกิด", "Date of birth"), `<input id="pf-birth_date" type="date" max="${today()}" value="${esc(F.birth_date)}">`, true)}
    <fieldset class="field"><legend>${P("เพศ", "Gender")} <span class="muted small">${P("(ไม่บังคับ)", "(optional)")}</span></legend><div class="radio-row">${g("male", "ชาย", "Male")}${g("female", "หญิง", "Female")}${g("unspecified", "ไม่ระบุ", "Prefer not to say")}</div></fieldset>
    <label class="check"><input type="checkbox" id="pf-mkt"${F.marketing_consent ? " checked" : ""}><span>${P("ยินยอมให้ Peaceful Day ส่งข่าวสาร โปรโมชัน และข้อเสนอพิเศษทางอีเมลหรือ SMS (เลิกรับได้ทุกเมื่อ)", "I agree to receive news, promotions and offers from Peaceful Day by email or SMS (you can opt out any time).")}</span></label>
    ${first ? `<p class="muted small">${P('การกดบันทึกถือว่าคุณยอมรับ<a href="#privacy" target="_blank" rel="noopener">นโยบายความเป็นส่วนตัว</a>และ<a href="#terms" target="_blank" rel="noopener">เงื่อนไขการจอง</a>ของ Peaceful Day (เปิดในแท็บใหม่)', 'By saving, you agree to the Peaceful Day <a href="#privacy" target="_blank" rel="noopener">privacy policy</a> and <a href="#terms" target="_blank" rel="noopener">booking terms</a> (open in a new tab).')}</p>` : ""}
    <div class="btn-row"><button class="btn btn--primary" type="submit" id="pf-save">${first ? P("บันทึกและไปจองรอบ", "Save and book") : P("บันทึกข้อมูล", "Save")}</button>${!first ? `<a class="btn btn--ghost" href="#my" data-go="my">${P("กลับ", "Back")}</a>` : ""}</div>
  </form>`}
</div></section>`);
  }
  function bindProfile() {
    if (!cust) { bindLoginPanel(); return; }
    const f = $("#pf"); if (!f) return;
    const vb = $("[data-verify]");
    if (vb) {
      if (R.verifyOpen) mountCaptcha();
      vb.addEventListener("click", async () => {
        keep();
        if (!R.verifyOpen) { R.verifyOpen = true; render(); return; }
        if (needCaptcha()) { R.flashErr = P("รอให้ช่องตรวจสอบด้านบนขึ้นเครื่องหมายถูกก่อน", "Wait for the check above to finish."); render(); return; }
        try { await custVerifyLink(cust.email); R.verifySent = true; R.verifyOpen = false; R.flashErr = ""; }
        catch (e) { R.flashErr = authErr(e.message); }
        render();
      });
    }
    const keep = () => { const F = R.pf; F.phone = $("#pf-phone").value; F.first_name = $("#pf-first_name").value; F.last_name = $("#pf-last_name").value; F.birth_date = $("#pf-birth_date").value; const g = $("input[name=pf-gender]:checked"); F.gender = g ? g.value : ""; F.marketing_consent = $("#pf-mkt").checked; };
    const ready = () => { const d = $("#pf-phone").value.replace(/\D/g, ""); $("#pf-save").disabled = !($("#pf-first_name").value.trim() && d.length >= 8); };
    f.addEventListener("input", ready); ready();
    f.addEventListener("submit", async (e) => {
      e.preventDefault(); keep(); const F = R.pf; R.pfErr = {};
      let d = F.phone.replace(/\D/g, ""); if (d.startsWith("66") && d.length >= 11) d = d.slice(2); if (!d.startsWith("0")) d = "0" + d;
      if (!/^0[2-9]\d{7,8}$/.test(d)) R.pfErr.phone = errText({ code: "phone" });
      if (!F.first_name.trim()) R.pfErr.first_name = errText({ code: "name" });
      if (Object.keys(R.pfErr).length) { render(); return; }
      try {
        const first = !R.profile;
        R.profile = await rpc("save_profile", { p: { phone: d, first_name: F.first_name, last_name: F.last_name, birth_date: F.birth_date || "", gender: F.gender, marketing_consent: F.marketing_consent, terms: true } }, "cust");
        BK.name = (R.profile.first_name + " " + R.profile.last_name).trim(); BK.phone = R.profile.phone; R.pf = null;
        if (first) go(R.afterProfile || "book", { flash: P("บันทึกข้อมูลแล้ว เลือกรอบที่ต้องการได้เลย", "Saved. Choose your session.") });
        else { R.flash = P("บันทึกข้อมูลแล้ว", "Saved."); render(); }
      } catch (err) { if (err.code === "phone" || err.code === "name" || err.code === "birth") R.pfErr[err.code === "name" ? "first_name" : err.code === "birth" ? "birth_date" : "phone"] = errText(err); else R.flashErr = errText(err); render(); }
    });
  }

  // ---------- privacy notice and booking terms (written for this shop; facts match how the system works) ----------
  const UPDATED = "22 ต.ค. 2569";
  function docPage(titleTh, titleEn, sections) {
    return pubLayout(`<header class="page-head"><div class="wrap"><h1>${P(titleTh, titleEn)}</h1><p class="lede small">${P("ปรับปรุงล่าสุด " + UPDATED, "Last updated 22 Oct 2026")}</p></div></header>
<section class="section" style="padding-top:8px"><div class="wrap prose">${sections.map(([hTh, hEn, items]) => `<h2>${P(hTh, hEn)}</h2>${Array.isArray(items) ? `<ul>${items.map(([a, b]) => `<li>${P(a, b)}</li>`).join("")}</ul>` : `<p>${items}</p>`}`).join("")}
  <p class="muted small" style="margin-top:28px">${P(`สอบถามเพิ่มเติม โทร ${esc(C.phone)} หรือแจ้งทีมงานที่ร้าน`, `Questions? Call ${esc(C.phone)} or ask our team at the shop.`)}</p>
</div></section>`);
  }
  function viewPrivacy() {
    return docPage("นโยบายความเป็นส่วนตัว", "Privacy policy", [
      ["ข้อมูลที่เราเก็บ", "What we collect", [
        ["อีเมลและรหัสผ่านสำหรับเข้าสู่ระบบ รหัสผ่านถูกเข้ารหัสไว้ ทีมงานมองไม่เห็น", "Your email and password for signing in. Passwords are encrypted; our team cannot see them."],
        ["ชื่อ นามสกุล และเบอร์มือถือ", "Your name and mobile number."],
        ["วันเกิดและเพศ เฉพาะเมื่อคุณเลือกกรอก", "Date of birth and gender, only if you choose to give them."],
        ["ประวัติการจอง เช่น วัน เวลา จำนวนคน หมายเหตุที่คุณแจ้ง และการชำระเงิน", "Your bookings: day, time, number of people, any note you leave and your payments."],
        ["รูปสลิปการโอนที่คุณแนบ ใช้ตรวจยอดและยืนยันการจอง", "The transfer slips you attach, used to check payment and confirm your booking."],
        ["การลงทะเบียนผู้ใช้บริการทุกคน รวมถึงเพื่อนที่มากับสมาชิก: ชื่อ นามสกุล อีเมล เบอร์โทร บริการที่เลือก ลายมือชื่อ เวลาที่ลงชื่อ และเวลาที่เช็คอิน เก็บเป็นหลักฐานการรับทราบความเสี่ยงและประวัติการมาใช้บริการ", "Registration of everyone who uses the facilities, including friends who come with a member: name, email, phone, chosen services, signature, the time you signed and the times you checked in, kept as a record of the risk acknowledgment and of your visits."],
        ["ข่าวสารและโปรโมชันส่งเฉพาะคนที่ติ๊กยินยอมตอนสมัครหรือลงทะเบียน", "News and offers go only to people who ticked the consent box when they signed up or registered."],
        ["ข้อมูลทางเทคนิคที่ผู้ให้บริการเว็บบันทึกอัตโนมัติเพื่อความปลอดภัย เช่น ที่อยู่ IP และประเภทเบราว์เซอร์", "Technical details our web providers record automatically for security, such as IP address and browser type."]]],
      ["เราใช้ข้อมูลเพื่อ", "How we use it", [
        ["รับจอง ยืนยัน เลื่อน หรือยกเลิกการจอง และติดต่อคุณเรื่องการจอง", "To take, confirm, move or cancel your bookings and contact you about them."],
        ["ยืนยันตัวตนตอนเข้าสู่ระบบ และป้องกันบอทหรือการใช้งานที่ผิดปกติ", "To confirm it's you when you sign in, and to block bots and abuse."],
        ["ส่งข่าวสารและโปรโมชัน เฉพาะเมื่อคุณติ๊กยินยอม และเลิกรับได้ทุกเมื่อที่หน้าข้อมูลสมาชิก", "To send news and offers, only if you ticked the consent box. You can opt out any time on your profile page."],
        ["ดูภาพรวมของร้าน เช่น จำนวนผู้ใช้บริการต่อวัน โดยไม่ระบุตัวบุคคล", "To see overall figures, such as visits per day, without identifying anyone."]]],
      ["ใครเห็นข้อมูลของคุณ", "Who can see your data", [
        ["ทีมงานของร้านที่ดูแลการจอง เห็นชื่อ เบอร์ และรายละเอียดการจองเท่าที่จำเป็นต่อการให้บริการ", "Our booking team sees your name, number and booking details, as needed to serve you."],
        ["ผู้ให้บริการระบบที่เราใช้: Supabase (ฐานข้อมูลและบัญชีผู้ใช้), EasySlip (ตรวจสลิปโอนเงินอัตโนมัติ), Resend (ส่งอีเมล), Cloudflare (ตรวจว่าไม่ใช่บอทและโดเมน), GitHub Pages (เว็บไซต์) ข้อมูลบางส่วนอาจถูกจัดเก็บหรือประมวลผลบนเซิร์ฟเวอร์นอกประเทศไทย", "Service providers we use: Supabase (database and accounts), EasySlip (automatic transfer-slip checks), Resend (email), Cloudflare (robot check and domain) and GitHub Pages (website). Some data may be stored or processed on servers outside Thailand."],
        ["เราไม่ขายข้อมูลของคุณ และจะเปิดเผยต่อหน่วยงานรัฐเฉพาะเมื่อกฎหมายกำหนด", "We do not sell your data, and only disclose it to authorities when the law requires."]]],
      ["สิทธิของคุณ", "Your rights", [
        ["ดูและแก้ไขชื่อ เบอร์ วันเกิด เพศ และการรับข่าวสารได้เองที่หน้าข้อมูลสมาชิก", "View and edit your name, number, date of birth, gender and marketing choice on your profile page."],
        ["ขอสำเนาข้อมูล ขอลบบัญชี หรือถอนความยินยอม ได้โดยโทรหรือแจ้งทีมงานที่ร้าน", "Ask for a copy of your data, deletion of your account, or to withdraw consent by calling us or asking at the shop."]]],
      ["เราเก็บข้อมูลนานแค่ไหน", "How long we keep it", [
        ["เก็บข้อมูลสมาชิกและประวัติการจองตลอดระยะเวลาที่คุณเป็นสมาชิก เพื่อให้บริการและดูประวัติการใช้บริการ", "We keep your member details and booking history for as long as you are a member, to serve you and keep a record of your visits."],
        ["ข้อมูลการชำระเงินและสลิปเก็บตามระยะเวลาที่กฎหมายบัญชีและภาษีกำหนด", "Payment records and slips are kept for as long as accounting and tax law requires."],
        ["เราสำรองข้อมูลเป็นประจำเพื่อป้องกันข้อมูลสูญหาย ไฟล์สำรองเข้าถึงได้เฉพาะเจ้าของร้าน", "We back up the data regularly so nothing is lost. Only the shop owner can open the backups."]]],
      ["ความปลอดภัย", "Security", [
        ["ข้อมูลถูกส่งผ่านการเชื่อมต่อแบบเข้ารหัส (HTTPS) และฐานข้อมูลเปิดให้เข้าถึงผ่านระบบของร้านเท่านั้น", "Data travels over encrypted connections (HTTPS), and the database is only reachable through the shop's system."],
        ["ไม่มีระบบออนไลน์ใดปลอดภัยได้ 100% กรุณาตั้งรหัสผ่านที่เดายากและไม่ใช้ซ้ำกับที่อื่น", "No online system is perfectly secure. Please use a strong password you don't use elsewhere."]]],
      ["การเปลี่ยนแปลงนโยบาย", "Changes to this policy", P("ถ้ามีการเปลี่ยนแปลง เราจะแก้ไขหน้านี้และวันที่ปรับปรุงด้านบน", "If this policy changes, we will update this page and the date above.")],
    ]);
  }
  function viewTerms() {
    const hrs = C.cancel_hours, moves = C.max_moves;
    return docPage("เงื่อนไขการจอง", "Booking terms", [
      ["การจอง", "Booking", [
        ["จองออนไลน์ได้เมื่อเข้าสู่ระบบด้วยบัญชีสมาชิก การจองสำเร็จเมื่อหน้าเว็บแสดงหมายเลขการจอง (PD-xxxxxx)", "Online booking needs a member account. Your booking is made when the page shows a booking reference (PD-xxxxxx)."],
        [`ใช้บริการ Ice Bath และ Sauna ได้ ${C.duration / 60} ชั่วโมงนับจากเวลาที่จอง`, `You can use the ice bath and sauna for ${C.duration / 60} hours from your booked time.`],
        [`แต่ละรอบรับจำนวนจำกัด (วันธรรมดา ${C.capacity} คน, เสาร์-อาทิตย์และวันหยุดนักขัตฤกษ์ ${C.capacity_weekend || C.capacity} คน) กลุ่มใหญ่หรือต้องการเหมารอบ กรุณาโทรหาเรา`, `Each session has limited places (${C.capacity} on weekdays, ${C.capacity_weekend || C.capacity} on weekends and public holidays). For larger groups or a private session, please call us.`],
        ["ผู้ใช้บริการทุกคน รวมถึงเพื่อนที่มาด้วย ต้องลงทะเบียน (ชื่อ นามสกุล อีเมล เบอร์โทร) และลงชื่อรับทราบความเสี่ยงก่อนใช้บริการ ทำล่วงหน้าผ่านลิงก์ในหน้าการจองหรือที่เคาน์เตอร์ก็ได้", "Everyone who uses the facilities, including friends, registers (name, email, phone) and signs the risk acknowledgment before their session, ahead of time from the booking page link or at the counter."],
        ["กรุณามาถึงก่อนเวลา 10–15 นาที ถ้ามาสาย เวลาใช้บริการยังสิ้นสุดตามรอบเดิม", "Please arrive 10–15 minutes early. If you are late, your session still ends at the original time."]]],
      ["ราคาและการชำระเงิน", "Price and payment", [
        [`ราคา ${baht(C.price)} ต่อคน${pairOn() ? ` และโปรมา 2 คน ${baht(C.pair_price)}` : ""} ตามที่แสดงตอนจอง`, `${baht(C.price)} per person${pairOn() ? `, or ${baht(C.pair_price)} for two people` : ""}, as shown when you book.`],
        [`ชำระเต็มจำนวนตอนจองผ่านพร้อมเพย์หรือโอนเข้าบัญชีร้าน แล้วแนบสลิปภายใน ${C.hold_min || 15} นาที ระหว่างนี้ระบบกันที่ไว้ให้`, `Pay in full when you book, by PromptPay or bank transfer, and attach the slip within ${C.hold_min || 15} minutes. We hold your places meanwhile.`],
        ["ถ้าไม่ได้แนบสลิปในเวลาที่กำหนด การจองจะถูกยกเลิกและที่นั่งจะเปิดให้คนอื่นจอง", "If no slip arrives in time, the booking is cancelled and the places are released."],
        ["การจองยืนยันเมื่อทีมงานตรวจสลิปแล้ว", "Your booking is confirmed once our team has checked the slip."]]],
      ["เลื่อนและยกเลิก", "Moving and cancelling", [
        [`เลื่อนได้เองที่หน้า "การจองของฉัน" ก่อนเวลาเริ่มอย่างน้อย ${hrs} ชั่วโมง (เลื่อนได้ ${moves} ครั้งต่อการจอง)`, `Move your booking yourself under "My bookings" up to ${hrs} hours before your session (up to ${moves} move${moves > 1 ? "s" : ""} per booking).`],
        [`ถ้าชำระเงินแล้วต้องการยกเลิก กรุณาโทร ${esc(C.phone)}`, `To cancel after paying, please call ${esc(C.phone)}.`],
        [`หลังจากนั้น กรุณาโทร ${esc(C.phone)} ทีมงานจะช่วยดูให้ตามรอบที่ยังว่าง`, `After that, please call ${esc(C.phone)} and our team will help if there is space.`],
        ["ร้านอาจปิดรอบหรือปิดร้านในบางกรณี เช่น สภาพอากาศหรือการซ่อมบำรุง ถ้ากระทบการจองของคุณ ทีมงานจะติดต่อเพื่อเลื่อนให้", "We may close a session or the shop, for example for weather or maintenance. If this affects your booking, our team will contact you to move it."]]],
      ["สุขภาพและความปลอดภัย", "Health and safety", [
        ["การแช่น้ำเย็นและซาวน่าอาจไม่เหมาะกับผู้มีโรคหัวใจ ความดันสูงที่ยังคุมไม่ได้ ผู้ตั้งครรภ์ หรือมีโรคประจำตัว หากไม่แน่ใจกรุณาปรึกษาแพทย์ก่อน", "Cold water and sauna may not suit people with heart conditions, uncontrolled high blood pressure, pregnancy or other medical conditions. If unsure, ask your doctor first."],
        ["แจ้งทีมงานเมื่อมาถึงถ้ามีโรคประจำตัว เริ่มแช่ช่วงสั้น ๆ และหยุดพักทันทีเมื่อรู้สึกไม่สบาย", "Tell our team about any medical condition when you arrive, keep first dips short and stop as soon as you feel unwell."],
        ["กรุณาทำตามคำแนะนำของทีมงานและป้ายในร้าน งดเครื่องดื่มแอลกอฮอล์ก่อนใช้บริการ", "Please follow our team's guidance and the signs at the shop, and don't drink alcohol before your session."]]],
      ["บัญชีสมาชิก", "Your account", [
        ["ดูแลรหัสผ่านของคุณให้ปลอดภัย การจองที่ทำผ่านบัญชีของคุณถือเป็นการจองของคุณ", "Keep your password safe. Bookings made with your account are treated as yours."],
        ["ร้านอาจระงับบัญชีที่ใช้งานผิดปกติ เช่น จองแล้วไม่มาซ้ำหลายครั้งโดยไม่แจ้ง", "We may suspend accounts that are misused, for example repeated no-shows without notice."]]],
      ["การเปลี่ยนแปลงเงื่อนไข", "Changes to these terms", P("ถ้ามีการเปลี่ยนแปลง เราจะแก้ไขหน้านี้และวันที่ปรับปรุงด้านบน การจองที่ทำไว้แล้วใช้เงื่อนไข ณ วันที่จอง", "If these terms change, we will update this page and the date above. Existing bookings keep the terms from when they were made.")],
    ]);
  }

  // ---------- risk acknowledgment: read, tick, sign with a finger (one person at a time; a group signs one after another) ----------
  const WAIVER = {
    th: { title: "แบบรับทราบความเสี่ยง", intro: "ใช้สำหรับบริการอ่างน้ำเย็นและซาวน่าที่เลือกในครั้งนี้เท่านั้น กรุณาอ่าน สอบถาม และลงชื่อเมื่อเข้าใจแล้ว เอกสารนี้ไม่ใช่ใบรับรองแพทย์หรือการรับประกันความปลอดภัย",
      sections: [
        ["สิ่งที่ควรทราบก่อนตัดสินใจ", ["บริการนี้เป็นกิจกรรมเพื่อความเป็นอยู่ที่ดีทั่วไป ไม่ใช่การวินิจฉัยหรือรักษาโรค ผลที่ได้รับแตกต่างกันในแต่ละบุคคล ไม่จำเป็นต้องอยู่จนครบเวลา สลับร้อนเย็น หรือทำตามผู้อื่น คุณสามารถไม่เริ่มหรือหยุดได้ทุกเมื่อ"]],
        ["ความเสี่ยงสำคัญ", ["อ่างน้ำเย็น: อาจเกิดอาการหอบหรือสูดหายใจอย่างฉับพลัน หายใจเร็ว ใจเต้นและความดันเปลี่ยน เวียนศีรษะ ชา สูญเสียการควบคุมร่างกาย หมดสติ จมน้ำ หรืออุณหภูมิร่างกายต่ำผิดปกติ ในบางกรณีอาจเกิดหัวใจเต้นผิดจังหวะหรือเหตุร้ายแรงถึงชีวิต",
          "ซาวน่า: อาจเกิดขาดน้ำ ความดันเปลี่ยน หน้ามืด เป็นลม ผิวหนังไหม้ หรือเจ็บป่วยจากความร้อน รวมถึงภาวะฮีตสโตรก ซึ่งอาจรุนแรงถึงชีวิต",
          "การสลับร้อนเย็นเพิ่มภาระต่อระบบหัวใจและหลอดเลือด การเคยทนได้หรือรู้สึกสบายไม่ได้ยืนยันว่าปลอดภัยในครั้งนี้",
          "พื้นเปียก ขอบอ่าง และอุปกรณ์อาจทำให้ลื่นล้ม บาดเจ็บ หรือเกิดอุบัติเหตุได้ โรคประจำตัว ยา และสภาพร่างกายในวันนั้นอาจเพิ่มความเสี่ยง"]],
        ["ข้อตกลงด้านความปลอดภัย", ["แจ้งพนักงานเมื่อสุขภาพเปลี่ยน และปฏิบัติตามคำแนะนำด้านความปลอดภัยที่แจ้งก่อนเริ่ม หากไม่แน่ใจให้ชะลอและปรึกษาผู้ประกอบวิชาชีพที่เหมาะสม",
          "ไม่ใช้บริการเมื่อมึนเมา อยู่ภายใต้อิทธิพลของสารที่ทำให้การตัดสินใจหรือการทรงตัวผิดปกติ มีไข้ ไม่สบาย หรือขาดน้ำ",
          "ไม่ใช้เพียงลำพัง ไม่กระโดดลงอ่าง ไม่จุ่มศีรษะ และไม่กลั้นหายใจหรือฝึกหายใจเร็วแรงในน้ำ ก่อนลงน้ำทันที หรือใกล้น้ำ เข้าออกช้า ๆ และพักก่อนเปลี่ยนกิจกรรม",
          "หยุดทันทีและเรียกพนักงานเมื่อเจ็บหน้าอก หายใจลำบาก หน้ามืด ใจสั่น สับสน อ่อนแรง หรือมีอาการผิดปกติ อย่าฝืน อย่าลุกหรือออกจากอ่างโดยลำพังหากรู้สึกจะเป็นลม"]]],
      agree: "ข้าพเจ้าได้อ่านและเข้าใจความเสี่ยง มีโอกาสถามคำถาม และเลือกเข้าร่วมโดยสมัครใจ รับทราบว่าพนักงานอาจให้พักหรือยุติการใช้บริการเพื่อความปลอดภัย การลงชื่อไม่ใช่การสละสิทธิเรียกร้องตามกฎหมาย และไม่ยกเว้นความรับผิดของผู้ให้บริการที่กฎหมายไม่อนุญาตให้ยกเว้น" },
    en: { title: "Risk acknowledgment", intro: "Applies only to the cold-water immersion and sauna services selected for this visit. Please read, ask questions and sign only when you understand. This is not medical clearance or a guarantee of safety.",
      sections: [
        ["Before you decide", ["These are general wellbeing activities, not diagnosis or treatment. Individual responses vary. You do not have to complete any duration, alternate hot and cold, or match another person. You may decline to start or stop at any time."]],
        ["Important risks", ["Cold-water immersion may cause an involuntary gasp, rapid breathing, changes in heart rate and blood pressure, dizziness, numbness, loss of physical control, unconsciousness, drowning or hypothermia. In some cases, abnormal heart rhythms or other life-threatening events can occur.",
          "Sauna may cause dehydration, blood-pressure changes, lightheadedness, fainting, burns or heat illness, including potentially life-threatening heatstroke.",
          "Alternating heat and cold adds cardiovascular stress. Previous tolerance or feeling comfortable does not establish that a session is safe.",
          "Wet floors, bath edges and equipment can cause slips, injuries and accidents. Health conditions, medicines and how you feel that day can increase risk."]],
        ["Safety commitments", ["Tell staff about changes in your health, and follow the safety instructions given before starting. If unsure, pause and seek advice from an appropriate healthcare professional.",
          "Do not participate while intoxicated, affected by substances that impair judgment or balance, feverish, unwell or dehydrated.",
          "Do not use the facilities alone, jump into the bath, submerge your head, hold your breath or practise rapid forceful breathing in, immediately before entering, or near water. Enter and exit slowly, and rest before changing activities.",
          "Stop and alert staff immediately for chest pain, difficulty breathing, dizziness, palpitations, confusion, weakness or other unusual symptoms. Do not push through symptoms or stand up or exit alone if you feel faint."]]],
      agree: "I have read and understood the risks, had an opportunity to ask questions, and choose to participate voluntarily. I understand that staff may pause or end participation for safety. Signing does not waive my legal rights or exclude any provider liability that cannot lawfully be excluded." },
  };
  const freshWF = () => ({ first_name: "", last_name: "", email: "", phone: "", services: "both", agree: false, mkt: false });
  async function viewWaiver() {
    const W = WAIVER[lang === "en" ? "en" : "th"], F = R.wf || (R.wf = freshWF()), fe = R.wErr || {};
    const team = !!session; // a signed-in team member on the shop's tablet can look people up
    let bk = null;
    if (R.wref && R.wtok) { try { bk = await rpc("waiver_booking", { p_ref: R.wref, p_token: R.wtok }); } catch (e) { bk = null; } }
    // the person who booked usually registers first, on their own phone: start from their member profile
    if (bk && !bk.signed && !R.wdone && !F.first_name && !F.email && !team) {
      if (cust && R.profile === undefined) await ensureProfile();
      if (cust && R.profile) Object.assign(F, { first_name: R.profile.first_name || "", last_name: R.profile.last_name || "", email: R.profile.email || cust.email || "", phone: R.profile.phone || "" });
    }
    if (R.wdone) {
      const d = R.wdone, more = d.people && d.signed < d.people;
      return pubLayout(`<section class="section"><div class="wrap waiver"><div class="alert alert--ok" role="status">${ICON.ok}<p>${P("ลงทะเบียนเรียบร้อย ขอบคุณค่ะ", "Registered. Thank you.")}${d.people ? P(` (ลงทะเบียนแล้ว ${d.signed} จาก ${d.people} คนในการจอง ${esc(d.ref)})`, ` (${d.signed} of ${d.people} people registered for ${esc(d.ref)})`) : ""}</p></div>
        <p class="lede">${more ? P("ส่งเครื่องให้คนถัดไปในกลุ่มลงทะเบียนได้เลย", "Pass the phone to the next person in your group.") : P("แล้วพบกันที่สวน", "See you in the garden.")}</p>
        <div class="btn-row"><button class="btn btn--primary" type="button" data-wnext>${P("ลงทะเบียนคนถัดไป", "Next person")}</button>${team ? `<a class="btn" href="#queue" data-go="queue">กลับหน้าจองคิว</a>` : `<a class="btn" href="#home" data-go="home">${P("กลับหน้าแรก", "Back to home")}</a>`}</div></div></section>`);
    }
    const field = (k, label, input) => `<div class="field${fe[k] ? " has-error" : ""}"><label for="w-${k}">${label}</label>${input}${fe[k] ? `<span class="err">${esc(fe[k])}</span>` : ""}</div>`;
    const svc = (v, th, en) => `<label class="radio"><input type="radio" name="w-svc" value="${v}"${F.services === v ? " checked" : ""}><span>${P(th, en)}</span></label>`;
    return pubLayout(`<header class="page-head"><div class="wrap waiver"><h1>${P("ลงทะเบียนและรับทราบความเสี่ยง", "Registration and risk acknowledgment")}</h1>
      <p class="lede">${P("ผู้ใช้บริการทุกคน รวมถึงเพื่อนที่มาด้วย กรอกข้อมูลและลงชื่อคนละครั้งก่อนใช้บริการ", "Everyone who uses the facilities, including friends who come along, registers and signs once before their session.")}</p>
      ${bk ? `<p class="small">${P("การจอง", "Booking")} <b>${esc(bk.ref)}</b> ${P("ของคุณ", "by")} ${esc(bk.name)} · ${esc(dShort(bk.day))} ${esc(bk.hm)} · ${P(`ลงทะเบียนแล้ว ${bk.signed} จาก ${bk.people} คน`, `${bk.signed} of ${bk.people} registered`)}</p>`
        : R.wref ? `<p class="note-box small">${P("ลิงก์นี้ไม่ครบหรือใช้ไม่ได้แล้ว ลงทะเบียนต่อได้ แต่จะไม่ผูกกับการจอง ขอลิงก์ใหม่จากผู้จอง (หน้าการจองของเขา) หรือแจ้งทีมงานที่ร้าน", "This link is incomplete or no longer valid. You can still register, but it won't be linked to the booking. Ask the person who booked for the link again, or tell our team.")}</p>` : ""}</div></header>
<section class="section" style="padding-top:8px"><div class="wrap waiver">${flashHTML()}
  ${team ? `<div class="panel w-find"><label for="w-q"><b>เคยลงทะเบียนแล้ว?</b> ค้นหาด้วยชื่อ เบอร์ หรืออีเมล <span class="muted small">(สำหรับทีมงาน)</span></label>
    <div class="code-row"><input id="w-q" type="search" autocomplete="off" value="${esc(R.wq || "")}"><button class="btn btn--small" type="button" data-wfind>ค้นหา</button></div>
    ${R.wfound ? (R.wfound.length ? `<ul class="w-found">${R.wfound.map((x, i) => `<li><button type="button" data-wpick="${i}"><b>${esc((x.first_name + " " + x.last_name).trim())}</b><span>${esc(fmtPhone(x.phone))}${x.email ? " · " + esc(x.email) : ""}${x.member ? " · สมาชิก" : ""}${x.visits ? " · มาแล้ว " + x.visits + " ครั้ง" : ""}</span></button></li>`).join("")}</ul>` : '<p class="muted small">ไม่พบ ลงทะเบียนใหม่ด้านล่าง</p>') : ""}</div>` : ""}
  <form class="panel waiver-form" id="wf" novalidate>
    <h2>${P("ข้อมูลผู้ใช้บริการ", "Your details")}</h2>
    <div class="row-2">${field("first_name", P("ชื่อ", "First name"), `<input id="w-first_name" type="text" maxlength="60" autocomplete="given-name" value="${esc(F.first_name)}">`)}${field("last_name", P("นามสกุล", "Last name"), `<input id="w-last_name" type="text" maxlength="60" autocomplete="family-name" value="${esc(F.last_name)}">`)}</div>
    <div class="row-2">${field("email", P("อีเมล", "Email"), `<input id="w-email" type="email" maxlength="200" autocomplete="email" inputmode="email" value="${esc(F.email)}">`)}${field("phone", P("เบอร์โทร", "Phone"), `<input id="w-phone" type="tel" maxlength="20" autocomplete="tel" inputmode="tel" placeholder="08x-xxx-xxxx" value="${esc(F.phone)}">`)}</div>
    <fieldset class="field"><legend>${P("บริการที่เลือก", "Selected services")}</legend><div class="radio-row">${svc("cold", "อ่างน้ำเย็น", "Cold-water immersion")}${svc("sauna", "ซาวน่า", "Sauna")}${svc("both", "ใช้ทั้งสองบริการ", "Both")}</div></fieldset>
    <div class="prose w-terms"><h2>${esc(W.title)}</h2><p class="small">${esc(W.intro)}</p>${W.sections.map(([h, items]) => `<h3>${esc(h)}</h3><ul>${items.map((x) => `<li>${esc(x)}</li>`).join("")}</ul>`).join("")}</div>
    <label class="check${fe.agree ? " has-error" : ""}"><input type="checkbox" id="w-agree"${F.agree ? " checked" : ""}><span>${esc(W.agree)}</span></label>
    <label class="check"><input type="checkbox" id="w-mkt"${F.mkt ? " checked" : ""}><span>${P("ยินยอมรับข่าวสารและโปรโมชันจาก Peaceful Day ทางอีเมลหรือ SMS (ไม่บังคับ เลิกรับได้ทุกเมื่อ)", "Send me news and offers from Peaceful Day by email or SMS (optional; opt out any time).")}</span></label>
    <div class="field"><label>${P("ลงชื่อด้วยนิ้วหรือเมาส์ในกรอบ", "Sign with your finger or mouse in the box")}</label>
      <div class="sigpad"><canvas id="sig" aria-label="${P("ช่องลงลายมือชื่อ", "Signature box")}"></canvas><span class="sig-hint" aria-hidden="true">${P("ลงชื่อที่นี่", "Sign here")}</span></div>
      <span class="err" id="sig-err" role="alert"></span>
      <button class="btn btn--small btn--ghost" type="button" data-sigclear>${P("ล้างลายเซ็น", "Clear")}</button></div>
    <p class="muted small">${P('ข้อมูล ลายเซ็น และเวลาที่ลงชื่อจะถูกเก็บเป็นหลักฐาน ตาม<a href="#privacy" target="_blank" rel="noopener">นโยบายความเป็นส่วนตัว</a>', 'Your details, signature and the time are kept as a record, as described in our <a href="#privacy" target="_blank" rel="noopener">privacy policy</a>.')}</p>
    <button class="btn btn--primary btn--block" type="submit" id="w-go">${P("ลงทะเบียนและลงชื่อ", "Register and sign")}</button>
  </form>
</div></section>`);
  }
  function bindWaiver() {
    const nx = $("[data-wnext]"); if (nx) nx.addEventListener("click", () => { R.wdone = null; R.wf = freshWF(); R.wErr = {}; R.wfound = null; R.wq = ""; R.wsig = null; render(); window.scrollTo(0, 0); });
    const keep = () => { const F = R.wf; ["first_name", "last_name", "email", "phone"].forEach((k) => { const i = $("#w-" + k); if (i) F[k] = i.value; }); const r = $("input[name=w-svc]:checked"); F.services = r ? r.value : "both"; const a = $("#w-agree"); if (a) F.agree = a.checked; const m = $("#w-mkt"); if (m) F.mkt = m.checked; };
    // team: look up someone who registered before, so they only tick and sign
    const fq = $("#w-q"), fb = $("[data-wfind]");
    const find = async () => { keep(); R.wq = fq.value.trim(); if (R.wq.length < 2) return; try { R.wfound = await rpc("staff_people_search", { p_q: R.wq }, true); } catch (e) { R.wfound = []; R.flashErr = errText(e); } saveSig(); render(); };
    if (fb) { fb.addEventListener("click", find); fq.addEventListener("keydown", (e) => { if (e.key === "Enter") { e.preventDefault(); find(); } }); }
    $$("[data-wpick]").forEach((b) => b.addEventListener("click", () => { keep(); const x = R.wfound[Number(b.dataset.wpick)]; Object.assign(R.wf, { first_name: x.first_name || "", last_name: x.last_name || "", email: x.email || "", phone: x.phone || "" }); R.wfound = null; R.wErr = {}; saveSig(); render(); const a = $("#w-agree"); if (a) a.scrollIntoView({ block: "center" }); }));
    const cv = $("#sig"); if (!cv) return;
    const ratio = Math.max(1, window.devicePixelRatio || 1), box = cv.parentElement;
    cv.width = box.clientWidth * ratio; cv.height = box.clientHeight * ratio;
    const cx = cv.getContext("2d"); cx.scale(ratio, ratio); cx.lineWidth = 2.4; cx.lineCap = "round"; cx.lineJoin = "round"; cx.strokeStyle = "#111";
    let drawing = false, inked = 0, last = null;
    // a signature already drawn survives a re-render (a lookup, or a form error)
    function saveSig() { R.wsig = inked >= 40 ? cv.toDataURL("image/png") : null; }
    if (R.wsig) { const img = new Image(); img.onload = () => { cx.drawImage(img, 0, 0, box.clientWidth, box.clientHeight); }; img.src = R.wsig; inked = 100; box.classList.add("has-ink"); }
    const pt = (e) => { const r = cv.getBoundingClientRect(); return [e.clientX - r.left, e.clientY - r.top]; };
    cv.addEventListener("pointerdown", (e) => { e.preventDefault(); cv.setPointerCapture(e.pointerId); drawing = true; last = pt(e); box.classList.add("has-ink"); box.classList.remove("has-error"); $("#sig-err").textContent = ""; });
    cv.addEventListener("pointermove", (e) => { if (!drawing) return; const p = pt(e); cx.beginPath(); cx.moveTo(last[0], last[1]); cx.lineTo(p[0], p[1]); cx.stroke(); inked += Math.hypot(p[0] - last[0], p[1] - last[1]); last = p; });
    const up = () => { drawing = false; }; cv.addEventListener("pointerup", up); cv.addEventListener("pointercancel", up);
    $("[data-sigclear]").addEventListener("click", () => { cx.clearRect(0, 0, cv.width, cv.height); inked = 0; R.wsig = null; box.classList.remove("has-ink"); });
    $("#wf").addEventListener("submit", async (e) => {
      e.preventDefault(); keep(); const F = R.wf, er = {};
      if (!F.first_name.trim()) er.first_name = P("กรุณากรอกชื่อ", "Please enter your first name.");
      if (!F.last_name.trim()) er.last_name = P("กรุณากรอกนามสกุล", "Please enter your last name.");
      if (!/^[^@\s]+@[^@\s]+\.[^@\s]+$/.test(F.email.trim())) er.email = errText({ code: "email" });
      if (F.phone.replace(/[^\d]/g, "").length < 9) er.phone = errText({ code: "any_phone" });
      if (!F.agree) er.agree = P("กรุณาติ๊กว่าได้อ่านและเข้าใจแล้ว", "Please tick to confirm you have read and understood.");
      if (Object.keys(er).length) { R.wErr = er; R.flashErr = Object.values(er)[0]; saveSig(); render(); const f = $(".has-error"); if (f) f.scrollIntoView({ block: "center" }); return; }
      if (inked < 40) { const pad = $(".sigpad"); pad.classList.add("has-error"); $("#sig-err").textContent = P("กรุณาลงลายมือชื่อในกรอบ", "Please sign in the box."); pad.scrollIntoView({ block: "center" }); return; }
      const go2 = $("#w-go"); go2.disabled = true; saveSig();
      try {
        const res = await rpc("sign_waiver", { p: { ref: R.wref || "", token: R.wtok || "", first_name: F.first_name, last_name: F.last_name, email: F.email, phone: F.phone, services: F.services, agree: true, marketing_consent: F.mkt, signature: R.wsig, lang } }, !!session);
        R.wdone = res; R.flashErr = ""; R.wErr = {}; R.wf = freshWF(); R.wsig = null; R.wfound = null; R.wq = "";
      } catch (err) {
        R.flashErr = errText(err); R.wErr = {};
        if (err.code === "email") R.wErr.email = errText(err); if (err.code === "any_phone") R.wErr.phone = errText(err); if (err.code === "name") R.wErr.first_name = errText(err);
      }
      render(); window.scrollTo(0, 0);
    });
  }

  // ---------- find my booking ----------
  async function viewMy() {
    let mine = null;
    if (cust && !R.custRecovery && !(await ensureProfile())) return toProfile("my");
    if (cust) { try { mine = await rpc("my_bookings", {}, "cust"); } catch (e) { if (e.code === "login" || /JWT/i.test(e.message)) custOut(); } }
    const st = { confirmed: P("จองแล้ว", "Booked"), checked_in: P("เช็คอินแล้ว", "Checked in"), cancelled: P("ยกเลิกแล้ว", "Cancelled"), no_show: P("ไม่ได้มา", "Missed"), waiting: P("รอนัดวันใหม่", "Waiting for a new date") };
    return pubLayout(`<header class="page-head"><div class="wrap"><h1>${P("การจองของฉัน", "My bookings")}</h1></div></header>
<section class="section" style="padding-top:8px"><div class="wrap">${flashHTML()}
  ${cust && R.custRecovery ? newPasswordPanel() : ""}
  ${mine ? `${signedInLine()}<p class="small"><a href="#profile" data-go="profile">${P("แก้ไขข้อมูลสมาชิก", "Edit your profile")}</a></p>${mine.bookings.length ? `<div class="cards">${mine.bookings.map((b) => `<a class="bcard" href="#booking/${esc(b.ref)}/${esc(b.token)}" data-open="${esc(b.ref)}|${esc(b.token)}"><span class="date-badge"><b>${Number(b.day.slice(8))}</b><span>${esc(mon(b.day))}</span></span><span class="info"><b>${esc(b.hm)} · ${b.people} ${P("คน", "people")}</b><span>${esc(b.ref)} · ${esc(dShort(b.day))}</span></span>${b.status === "confirmed" && b.pay === "await" ? `<span class="badge badge--warn">${P("รอชำระ", "Pay now")}</span>` : b.status === "confirmed" && b.pay === "review" ? `<span class="badge badge--warn">${P("รอตรวจสลิป", "Checking slip")}</span>` : `<span class="badge ${b.status === "confirmed" || b.status === "checked_in" ? "badge--ok" : b.status === "waiting" ? "badge--warn" : "badge--bad"}">${b.pay === "expired" ? P("หมดเวลาชำระ", "Not paid in time") : st[b.status] || esc(b.status)}</span>`}</a>`).join("")}</div>` : `<p class="muted">${P("ยังไม่มีการจอง", "No bookings yet.")}</p>`}<p class="mt-m"><a class="btn btn--primary" href="#book" data-go="book">${P("จองรอบ", "Book a session")}</a></p>`
  : `<div class="panel" style="max-width:520px"><h2 style="font-size:var(--step-2);margin-bottom:12px">${P("เข้าสู่ระบบเพื่อดูการจอง", "Sign in to see your bookings")}</h2>${loginPanel("")}</div>`}
</div></section>`);
  }
  function bindMy() { if (!cust) bindLoginPanel(); else bindNewPassword(); }


  // ================= back office =================
  const ROLE = { owner: "เจ้าของ", manager: "ผู้จัดการ", staff: "แอดมินจองคิว" };
  const ST = { confirmed: ["จองแล้ว", "badge--ok"], checked_in: ["เช็คอินแล้ว", "badge--ok"], cancelled: ["ยกเลิก", "badge--bad"], no_show: ["ไม่มา", "badge--bad"], waiting: ["รอนัดวันใหม่", "badge--warn"] };
  const PAY = { due: ["รอเก็บเงิน", "badge--warn"], await: ["รอโอน", "badge--warn"], review: ["รอตรวจสลิป", "badge--warn"], expired: ["หมดเวลาชำระ", "badge--muted"], paid: ["ชำระแล้ว", "badge--ok"], none: ["ไม่มียอด", "badge--muted"] };
  const owes = (b) => b.pay === "due" || b.pay === "await" || b.pay === "review";
  const METHOD = { cash: "เงินสด", transfer: "โอน", card: "บัตร", qr: "QR" };
  const METHOD_ALL = Object.assign({ package: "คูปองแพ็กเกจ" }, METHOD);
  const CH = { web: "เว็บ", walk_in: "Walk-in", phone: "โทร", line: "LINE" };
  const badge = (m, k) => `<span class="badge ${(m[k] || ["", ""])[1]}">${(m[k] || [k])[0]}</span>`;
  const ADMIN = ["acustomers", "admin", "queue", "aday", "abooking", "anew", "asearch", "asettings", "ateam", "apassword"];

  function admLayout(main) {
    const nav = [["admin", "ภาพรวมวันนี้"], ["queue", "หน้าจองคิว (แอดมิน)"], ["aday", "ตารางรอบ"], ["anew", "+ สร้างการจอง"], ["asearch", "ค้นหาการจอง"], ["acustomers", "ลูกค้า"], ["asettings", "ตั้งค่าร้าน"], ["ateam", "ทีมงาน"]];
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
  function viewInviteCode(wrong, locked) {
    if (locked) return `<div class="adm"><div class="login-wrap"><div class="card login"><div class="brand-row">${LOGO}<h1>รหัสเชิญถูกล็อก</h1></div><p>ใส่รหัสผิดเกิน 5 ครั้ง เพื่อความปลอดภัยรหัสนี้ใช้ไม่ได้แล้ว ให้เจ้าของร้านเปิดหน้า “ทีมงาน” แล้วเพิ่มคำเชิญอีเมลนี้อีกครั้งเพื่อรับรหัสใหม่</p><p><button class="btn" type="button" data-signout>ออกจากระบบ</button></p></div></div></div>`;
    return `<div class="adm"><div class="login-wrap"><form class="card login" id="icode">
  <div class="brand-row">${LOGO}<h1>ใส่รหัสเชิญทีมงาน</h1></div>
  <p class="muted">อีเมล <b>${esc(session.email || "")}</b> ได้รับเชิญเป็นทีมงาน ใส่รหัส 6 หลักที่เจ้าของร้านส่งให้ เพื่อยืนยันว่าเป็นคุณ (ใส่ครั้งเดียว)</p>
  ${wrong ? `<div class="alert alert--bad" role="alert">รหัสไม่ถูกต้อง ลองใหม่อีกครั้ง หรือขอรหัสจากเจ้าของร้าน</div>` : ""}
  <div class="field"><label for="ic">รหัสเชิญ</label><input id="ic" inputmode="numeric" maxlength="6" autocomplete="one-time-code" required></div>
  <button class="btn btn--primary btn--block" type="submit">ยืนยัน</button>
  <p class="muted small"><a href="#admin" data-signout>ออกจากระบบ</a></p>
</form></div></div>`;
  }
  function bindLogin() {
    const ic = $("#icode");
    if (ic) { ic.addEventListener("submit", (e) => { e.preventDefault(); R.inviteCode = $("#ic").value.trim(); ME = null; render(); }); $("#ic").focus(); return; }
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
    ${kpi("ยังไม่ชำระ (วันนี้)", ov.due_today, null, n, "การจอง")}
  </div>
  ${ov.slips ? `<div class="alert alert--warn" role="status" style="margin-top:14px"><p>มีสลิปรอตรวจ <b>${ov.slips}</b> รายการ · <a href="#queue" data-go="queue" data-qtabgo="slips">ไปตรวจสลิป</a></p></div>` : ""}
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
    const row = (b) => `<li><a class="name" href="#abooking" data-aref="${esc(b.ref)}">${esc(b.name)}</a><span>${b.people} คน</span>${badge(ST, b.status)} ${badge(PAY, b.pay)}<span class="muted grow">${esc(fmtPhone(b.phone))} · ${CH[b.channel] || b.channel}${b.note ? " · “" + esc(b.note) + "”" : ""}</span>${b.status === "confirmed" ? `<button class="btn btn--small btn--primary" type="button" data-toqueue="${esc(b.ref)}|${b.day}">เช็คอินรายคน</button>` : ""}</li>`;
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
    bindToQueue();
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
  // open a booking's people (registration and per-person check-in) on the queue page
  function bindToQueue() {
    $$("[data-toqueue]").forEach((b) => b.addEventListener("click", () => { const [ref, day] = b.dataset.toqueue.split("|"); Object.assign(R, { qtab: "day", qday: day, qstrip: day, qPeople: ref, qOpen: "", qSlip: "" }); go("queue"); }));
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
    let slip = null; if (b.slip_at) { try { slip = await rpc("staff_slip", { p_ref: b.ref }, true); } catch (e) { slip = null; } }
    let ppl = []; try { ppl = await rpc("staff_people", { p_ref: b.ref }, true); } catch (e) { ppl = []; }
    return admLayout(`<div class="topline"><h1>${esc(b.name)} <span class="mono">${esc(b.ref)}</span></h1>${badge(ST, b.status)} ${badge(PAY, b.pay)}</div>
  <div class="grid grid-21"><div>
    <section class="card"><h2>รายละเอียด</h2><dl class="kv">
      <dt>รอบ</dt><dd><b>${esc(dLong(b.day, "th-TH"))} ${esc(b.hm)}–${esc(b.end)}</b></dd>
      <dt>จำนวนคน</dt><dd>${b.people}</dd>
      <dt>ลูกค้า</dt><dd>${esc(b.name)}${b.phone ? " · " + esc(fmtPhone(b.phone)) : ""}${b.email ? " · " + esc(b.email) : ""}</dd>
      <dt>ยอด</dt><dd>${baht(b.total)}${b.method ? " · " + (METHOD_ALL[b.method] || esc(b.method)) : ""}${b.package ? " · " + esc(b.package) : ""}</dd>
      <dt>ช่องทาง</dt><dd>${CH[b.channel] || esc(b.channel)} · จองเมื่อ ${esc(dt(b.created_at))}${b.moves ? ` · ลูกค้าเลื่อนแล้ว ${b.moves} ครั้ง` : ""}</dd>
      ${b.note ? `<dt>ลูกค้าแจ้ง</dt><dd>“${esc(b.note)}”</dd>` : ""}
    </dl></section>
    ${slip ? slipCard(b, slip) : ""}
    <section class="card"><h2>ผู้ใช้บริการ <span class="muted" style="font-weight:400">ลงทะเบียน ${ppl.length}/${b.people} · มาแล้ว ${ppl.filter((x) => x.checked_at).length}</span></h2>
      ${ppl.length ? `<ul class="pp-list">${ppl.map((x) => `<li class="${x.checked_at ? "is-in" : ""}"><img src="${esc(x.signature)}" alt="ลายเซ็นของ ${esc(x.name)}"><span class="pp-who"><b>${esc(x.name)}</b><span class="muted small">${esc(fmtPhone(x.phone))}${x.email ? " · " + esc(x.email) : ""} · ${x.visits ? "มาแล้ว " + x.visits + " ครั้ง" : "ยังไม่เคยเช็คอิน"}</span>${x.came_with ? `<span class="q-note">${esc(x.came_with)}</span>` : ""}</span>${x.checked_at ? `<span class="pp-ok">✓ ${esc(hhmm(x.checked_at))}${x.checked_by ? " · " + esc(x.checked_by) : ""}</span>` : '<span class="muted small">ยังไม่เช็คอิน</span>'}</li>`).join("")}</ul>` : '<p class="muted">ยังไม่มีใครลงทะเบียน</p>'}
      <p class="small"><a href="#waiver/${esc(b.ref)}/${esc(b.wtoken)}">+ ลงทะเบียนผู้ใช้บริการ</a></p></section>
    <section class="card"><h2>จัดการ</h2>
      ${b.status === "confirmed" || (b.status === "checked_in" && (b.arrived || 0) < b.people) ? `<div class="btn-row"><button class="btn btn--primary" type="button" data-toqueue="${esc(b.ref)}|${b.day}">เช็คอินรายคน (หน้าจองคิว)</button></div>` : ""}
      ${owes(b) && (b.status === "confirmed" || b.status === "checked_in") ? `${b.pay === "review" ? '<p class="small"><b>ลูกค้าแนบสลิปแล้ว</b> ตรวจสลิปด้านบนก่อนรับเงิน จะได้ไม่เก็บซ้ำ</p>' : ""}<div class="btn-row" style="margin-top:8px">${Object.entries(METHOD).map(([k, l]) => `<button class="btn btn--small" type="button" data-bpay="${k}">รับ${l} ${baht(b.total)}</button>`).join("")}</div>` : ""}
      ${b.status === "confirmed" ? `
        <div class="btn-row" style="margin-top:10px"><button class="btn btn--small" type="button" data-bst="no_show" data-confirm="กดอีกครั้ง: บันทึกว่าไม่มา">บันทึกว่าไม่มา</button><button class="btn btn--small btn--danger" type="button" data-bst="cancelled" data-confirm="กดอีกครั้งเพื่อยกเลิก">ยกเลิกการจอง</button><button class="btn btn--small" type="button" data-amove>${R.amove ? "ปิดการย้ายรอบ" : "ย้ายรอบ"}</button></div>` : ""}

      ${b.status === "cancelled" ? `<p><button class="btn btn--small" type="button" data-qrestore="${esc(b.ref)}" data-confirm="กดอีกครั้งเพื่อกู้คืน">กู้คืนคิว</button></p>` : ""}
      ${b.status === "checked_in" || b.status === "no_show" ? `<p style="margin-top:10px"><button class="btn btn--small" type="button" data-bst="confirmed" data-confirm="กดอีกครั้งเพื่อย้อนสถานะ">ย้อนเป็น “จองแล้ว”</button></p>` : ""}
      ${R.amove ? `<div class="card" style="margin-top:12px"><div class="field"><label for="amd">ย้ายไปวันที่</label><input type="date" id="amd" value="${esc(R.amoveDay || b.day)}"></div><div class="btn-row" style="margin:10px 0">${mslots.map((s) => `<button type="button" class="btn btn--small${R.amoveHm === s.hm ? " btn--primary" : ""}" data-amhm="${s.hm}">${s.hm} · ว่าง ${Math.max(0, s.capacity - s.used)}/${s.capacity}</button>`).join("") || '<span class="muted">วันนั้นไม่มีรอบ</span>'}</div>${ME.role !== "staff" ? '<label class="check"><input type="checkbox" id="amover"> อนุญาตเกินความจุ</label>' : ""}<p><button class="btn btn--primary" type="button" data-amgo${R.amoveHm ? "" : " disabled"}>ย้ายไปรอบที่เลือก</button></p></div>` : ""}
      <div class="field" style="margin-top:16px"><label for="snote">โน้ตทีมงาน (ลูกค้าไม่เห็น)</label><textarea id="snote" maxlength="1000">${esc(b.staff_note || "")}</textarea></div><button class="btn btn--small" type="button" data-snote style="margin-top:8px">บันทึกโน้ต</button>
    </section></div>
    <section class="card"><h2>ประวัติ</h2>${b.history.length ? `<ul class="timeline">${b.history.map((h) => `<li><time>${esc(dt(h.at))}</time><span>${esc(h.action)} <small class="muted">${esc(h.who)}</small></span></li>`).join("")}</ul>` : '<p class="muted">ยังไม่มี</p>'}</section>
  </div>`);
  }
  // the customer's slip, with the button that confirms the transfer
  function slipCard(b, slip) {
    return `<section class="card slip-card"><h2>สลิปโอนเงิน</h2><p class="muted small">ส่งเมื่อ ${esc(dt(slip.at))} · ยอดที่ต้องได้ <b>${baht(b.total)}</b></p>${slip.result ? `<p class="small">ระบบตรวจอัตโนมัติ: ${esc(slip.result)}</p>` : ""}
      <a href="${esc(slip.data)}" target="_blank" rel="noopener" class="slip-img"><img src="${esc(slip.data)}" alt="สลิปโอนเงินของ ${esc(b.name)}"></a>
      ${b.pay === "review" ? `<div class="btn-row" style="margin-top:10px"><button class="btn btn--primary" type="button" data-slipok="${esc(b.ref)}">ยอดถูกต้อง ยืนยันรับโอน</button><button class="btn btn--small btn--danger" type="button" data-slipno="${esc(b.ref)}" data-confirm="กดอีกครั้ง: ยกเลิกการจองนี้">สลิปไม่ถูกต้อง ยกเลิกคิว</button></div><p class="muted small">เช็คในแอปธนาคารว่าเงินเข้าจริงก่อนกดยืนยัน</p>` : ""}</section>`;
  }
  function bindSlipButtons() {
    $$("[data-qrestore]").forEach((b) => b.addEventListener("click", () => act(async () => {
      try { await rpc("staff_restore", { p_ref: b.dataset.qrestore, p_over: false }, true); }
      catch (e) { if (e.code === "slot_full") throw Object.assign(new Error("restore_full"), { code: "restore_full" }); throw e; }
      return "กู้คืนคิว " + b.dataset.qrestore + " แล้ว กลับไปอยู่ในตารางคิวตามเดิม";
    })));
    $$("[data-slipok]").forEach((b) => b.addEventListener("click", () => act(async () => { await rpc("staff_take_payment", { p_ref: b.dataset.slipok, p_method: "transfer" }, true); R.qSlip = ""; return "ยืนยันรับโอนแล้ว การจองนี้ชำระแล้ว"; })));
    $$("[data-slipno]").forEach((b) => b.addEventListener("click", () => act(async () => { await rpc("staff_set_status", { p_ref: b.dataset.slipno, p_status: "cancelled" }, true); R.qSlip = ""; return "ยกเลิกการจองแล้ว (กู้คืนได้ที่แท็บ “ยกเลิกแล้ว”) ถ้าลูกค้าโอนมาจริง อย่าลืมคืนเงิน"; })));
  }
  function bindAdminBooking() {
    const ref = R.aref;
    bindSlipButtons(); bindToQueue();
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
    const can = ME.role !== "staff", own = ME.role === "owner" ? "" : " disabled";
    const inp = (id, label, val, type, extra) => `<div class="field"><label for="${id}">${label}</label><input type="${type || "text"}" id="${id}" value="${esc(val)}"${can ? "" : " disabled"}${extra || ""}></div>`;
    return admLayout(`<div class="topline"><h1>ตั้งค่าร้าน</h1></div>
  ${can ? "" : '<p class="muted">ผู้จัดการหรือเจ้าของเท่านั้นที่แก้ค่าได้</p>'}
  <form class="card" id="setf"><h2>บริการและรอบ</h2>
    <div class="form-grid">${inp("s-price", "ราคาต่อคน (บาท)", cfg.price, "number", ' min="0"')}${inp("s-pair", "โปร 2 คน (บาท, ใส่ 0 = ปิดโปร)", cfg.pair_price || 0, "number", ' min="0"')}${inp("s-cap", "รับต่อรอบ วันธรรมดา (คน)", cfg.capacity, "number", ' min="1"')}${inp("s-capw", "รับต่อรอบ เสาร์-อาทิตย์/นักขัตฯ (คน)", cfg.capacity_weekend || cfg.capacity, "number", ' min="1"')}${inp("s-dur", "ใช้บริการได้ (นาที)", cfg.duration, "number", ' min="10"')}${inp("s-maxg", "จองออนไลน์ได้สูงสุดต่อครั้ง (คน)", cfg.max_group, "number", ' min="1"')}</div>
    <div class="field"><label for="s-times">เวลาเริ่มแต่ละรอบ (คั่นด้วย ,)</label><input type="text" id="s-times" value="${esc((cfg.times || []).join(", "))}"${can ? "" : " disabled"}></div>
    <fieldset style="margin-top:12px"><legend>หยุดประจำสัปดาห์</legend><div class="checks">${[1, 2, 3, 4, 5, 6, 0].map((d) => `<label class="check"><input type="checkbox" id="s-c${d}" data-closed="${d}"${(cfg.closed || []).includes(d) ? " checked" : ""}${can ? "" : " disabled"}> ${thDays[d]}</label>`).join("")}</div></fieldset>
    <h2 style="margin-top:18px">เงื่อนไขการจอง</h2>
    <div class="form-grid">${inp("s-lead", "ปิดจองออนไลน์ก่อนรอบ (นาที)", cfg.lead_min, "number")}${inp("s-hor", "เปิดให้จองล่วงหน้า (วัน)", cfg.horizon_days, "number")}${inp("s-cut", "ลูกค้ายกเลิก/เลื่อนเองได้ก่อนรอบ (ชม.)", cfg.cancel_hours, "number")}${inp("s-mm", "ลูกค้าเลื่อนได้ (ครั้ง)", cfg.max_moves, "number")}</div>
    <h2 style="margin-top:18px">รับเงินออนไลน์</h2>
    <p class="muted small">ลูกค้าจะเห็น QR พร้อมเพย์พร้อมยอดเงิน และบัญชีนี้ตอนจอง ใส่อย่างน้อยพร้อมเพย์หรือเลขบัญชี${ME.role === "owner" ? " · ระบบตรวจสลิปจะผ่านอัตโนมัติเมื่อชื่อในสลิปตรงกับ “ชื่อบัญชี” และเลขบัญชีตรงกัน (ถ้ามีหลายชื่อ เช่นไทย/อังกฤษ คั่นด้วย /)" : " · ส่วนนี้เจ้าของร้านเท่านั้นที่แก้ได้"}</p>
    <div class="form-grid">${inp("s-pp", "พร้อมเพย์ (เบอร์มือถือ / เลขผู้เสียภาษี / เลขอ้างอิง 15 หลักใต้ QR ของ K+)", cfg.promptpay || "", "text", ' inputmode="numeric"' + own)}${inp("s-bank", "ธนาคาร", cfg.bank_name || "", "text", own)}${inp("s-acct", "เลขบัญชี", cfg.bank_account || "", "text", ' inputmode="numeric"' + own)}${inp("s-holder", "ชื่อบัญชี (ตรงกับในแอปธนาคาร)", cfg.bank_holder || "", "text", own)}${inp("s-hold", "กันที่ให้ลูกค้าโอน (นาที)", cfg.hold_min || 15, "number", ' min="5" max="1440"' + own)}</div>
    <label class="check" style="margin-top:10px"><input type="checkbox" id="s-auto"${cfg.slip_auto !== false ? " checked" : ""}${can && ME.role === "owner" ? "" : " disabled"}> ตรวจสลิปอัตโนมัติด้วย EasySlip (ผ่านแล้วยืนยันการจองทันที ถ้าระบบตรวจไม่ได้ จะส่งมาให้ทีมงานตรวจเอง)</label>
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
        C = Object.assign(C, await rpc("staff_save_settings", { p: { price: num("#s-price"), pair_price: num("#s-pair"), capacity: num("#s-cap"), capacity_weekend: num("#s-capw"), duration: num("#s-dur"), max_group: num("#s-maxg"), times: Array.from(new Set(times)).sort(), closed: $$("[data-closed]").filter((c) => c.checked).map((c) => Number(c.dataset.closed)), lead_min: num("#s-lead"), horizon_days: num("#s-hor"), cancel_hours: num("#s-cut"), max_moves: num("#s-mm"), phone: $("#s-phone").value.trim(), line: $("#s-line").value.trim(), address_th: $("#s-addr").value.trim(), address_en: $("#s-addren").value.trim(), map_url: $("#s-map").value.trim(), promptpay: $("#s-pp").value.trim(), bank_name: $("#s-bank").value.trim(), bank_account: $("#s-acct").value.trim(), bank_holder: $("#s-holder").value.trim(), hold_min: num("#s-hold"), slip_auto: $("#s-auto").checked } }, true));
        return "บันทึกแล้ว หน้าเว็บลูกค้าใช้ค่าใหม่ทันที";
      });
    });
  }

  // members: who they are, how often they come, and the list as a CSV file (owner and managers)
  async function viewCustomers() {
    if (ME.role === "staff") return admLayout('<div class="card"><p class="muted">เจ้าของร้านหรือผู้จัดการเท่านั้นที่ดูรายชื่อลูกค้าได้</p></div>');
    const all = await rpc("staff_customers", { p_q: R.cq || "" }, true);
    const kind = R.ckind || "all", list = all.filter((c) => kind === "all" || c.type === kind);
    const members = all.filter((c) => c.type === "member").length, guests = all.length - members;
    const tab = (k, l) => `<button type="button" class="btn btn--small${kind === k ? " btn--primary" : ""}" data-ckind="${k}">${l}</button>`;
    return admLayout(`<div class="topline"><h1>ลูกค้า <span class="muted" style="font-weight:400;font-size:1rem">${all.length} คน</span></h1><button class="btn" type="button" data-ccsv>ส่งออก CSV</button></div>
  <form class="filters" id="cq"><div class="field" style="flex:1 1 260px"><label for="cqq">ชื่อ เบอร์ หรืออีเมล</label><input type="search" id="cqq" value="${esc(R.cq || "")}"></div><button class="btn" type="submit">ค้นหา</button></form>
  <div class="btn-row" style="margin-bottom:10px">${tab("all", `ทั้งหมด ${all.length}`)}${tab("member", `สมาชิก ${members}`)}${tab("guest", `ลงทะเบียนที่ร้าน / มากับเพื่อน ${guests}`)}</div>
  ${list.length ? `<section class="card"><div class="table-wrap"><table><thead><tr><th>ชื่อ</th><th>ประเภท</th><th>เบอร์</th><th>อีเมล</th><th>มาแล้ว (ครั้ง)</th><th>จอง / ลงทะเบียน</th><th>ยอดรวม</th><th>ล่าสุด</th><th>หมายเหตุ</th><th>รับข่าวสาร</th><th>เริ่มเมื่อ</th></tr></thead><tbody>
    ${list.map((c) => `<tr><td>${esc((c.first_name + " " + c.last_name).trim())}</td><td>${c.type === "member" ? '<span class="badge badge--ok">สมาชิก</span>' : '<span class="badge badge--muted">ลงทะเบียนที่ร้าน</span>'}</td><td>${esc(fmtPhone(c.phone))}</td><td>${esc(c.email)}${c.email_verified ? ' <span class="badge badge--ok">ยืนยัน</span>' : ""}</td><td><b>${c.visits}</b></td><td>${c.bookings}</td><td>${c.type === "member" ? baht(c.spent) : "–"}</td><td>${c.last_day ? esc(dShort(c.last_day, "th-TH")) : "–"}</td><td class="small">${esc(c.came_with || "")}</td><td>${c.marketing_consent ? "ยินยอม" : "–"}</td><td>${esc(dShort(String(c.created_at).slice(0, 10), "th-TH"))}</td></tr>`).join("")}
  </tbody></table></div></section>` : '<p class="empty">ยังไม่มีรายชื่อ</p>'}
  <p class="muted small">“มาแล้ว” นับจากการเช็คอินรายคนที่หน้าจองคิว · “ลงทะเบียนที่ร้าน” คือคนที่ไม่ได้สมัครสมาชิก เช่นเพื่อนที่มากับสมาชิก หรือ walk-in · ข้อมูลทั้งหมดส่งไฟล์สำรองเข้าอีเมลเจ้าของร้านทุกวันจันทร์ 8 โมงเช้า</p>`);
  }
  function bindCustomers() {
    const f = $("#cq"); if (f) f.addEventListener("submit", (e) => { e.preventDefault(); R.cq = $("#cqq").value; render(); });
    $$("[data-ckind]").forEach((b) => b.addEventListener("click", () => { R.ckind = b.dataset.ckind; render(); }));
    const b = $("[data-ccsv]");
    if (b) b.addEventListener("click", () => act(async () => {
      const list = await rpc("staff_customers", { p_q: "" }, true);
      const head = ["ประเภท", "ชื่อ", "นามสกุล", "เบอร์โทร", "อีเมล", "อีเมลยืนยันแล้ว", "วันเกิด", "เพศ", "ยินยอมรับข่าวสาร", "มาแล้ว (ครั้ง)", "จอง/ลงทะเบียน (ครั้ง)", "ยอดรวม (บาท)", "ล่าสุด", "หมายเหตุ (มากับ)", "เริ่มเมื่อ"];
      const G = { male: "ชาย", female: "หญิง", unspecified: "ไม่ระบุ" };
      const rows = list.map((c) => [c.type === "member" ? "สมาชิก" : "ลงทะเบียนที่ร้าน", c.first_name, c.last_name, c.phone ? "'" + c.phone : "", c.email, c.email_verified ? "ใช่" : "", c.birth_date || "", G[c.gender] || "", c.marketing_consent ? "ยินยอม" : "", c.visits, c.bookings, c.type === "member" ? c.spent : "", c.last_day || "", c.came_with || "", dt(c.created_at)]);
      const blob = new Blob(["﻿" + [head].concat(rows).map((r) => r.map(csvCell).join(",")).join("\r\n")], { type: "text/csv;charset=utf-8" });
      const a = document.createElement("a"); a.href = URL.createObjectURL(blob); a.download = "peacefulday-customers-" + today() + ".csv"; document.body.appendChild(a); a.click(); setTimeout(() => { URL.revokeObjectURL(a.href); a.remove(); }, 1000);
      return `ดาวน์โหลดรายชื่อลูกค้า ${list.length} คนแล้ว`;
    }));
  }

  async function viewTeam() {
    const t = await rpc("staff_team", {}, true);
    return admLayout(`<div class="topline"><h1>ทีมงาน</h1></div>
  <section class="card"><div class="table-wrap"><table><thead><tr><th>อีเมล</th><th>ชื่อ</th><th>ตำแหน่ง</th><th></th></tr></thead><tbody>
    ${t.staff.map((s) => `<tr><td>${esc(s.email)}</td><td>${esc(s.name)}</td><td>${ROLE[s.role]}</td><td>${ME.role === "owner" && s.role !== "owner" ? `<button class="btn btn--small btn--ghost" type="button" data-remove="${esc(s.email)}" data-confirm="กดอีกครั้งเพื่อลบ">ลบ</button>` : ""}</td></tr>`).join("")}
    ${t.invites.map((s) => `<tr class="is-dim"><td>${esc(s.email)}</td><td>${esc(s.name)}</td><td>${ROLE[s.role]} (รอสร้างบัญชี)${s.code ? ` · รหัสเชิญ <b class="mono">${esc(s.code)}</b>` : ""}</td><td>${ME.role === "owner" ? `<button class="btn btn--small btn--ghost" type="button" data-remove="${esc(s.email)}">ยกเลิกคำเชิญ</button>` : ""}</td></tr>`).join("")}
  </tbody></table></div></section>
  ${ME.role === "owner" ? `<form class="card" id="inv"><h2>เชิญทีมงาน</h2><p class="muted">เพิ่มอีเมลที่นี่ ระบบจะให้รหัสเชิญ 6 หลัก ส่งรหัสนั้นให้ทีมงาน แล้วให้ทีมงานเปิดหน้า “สำหรับทีมงาน” กด “สร้างบัญชีด้วยอีเมลที่ได้รับเชิญ” และใส่รหัสตอนเข้าครั้งแรก</p>
    <div class="form-grid"><div class="field"><label for="i-email">อีเมล</label><input type="email" id="i-email" required></div><div class="field"><label for="i-name">ชื่อ</label><input type="text" id="i-name"></div><div class="field"><label for="i-role">ตำแหน่ง</label><select id="i-role"><option value="staff">แอดมินจองคิว (เข้าได้แค่หน้าจองคิว: จองให้ลูกค้า เช็คอิน รับเงิน)</option><option value="manager">ผู้จัดการ (+ ปิดรอบ ตั้งค่า)</option></select></div></div>
    <p><button class="btn btn--primary" type="submit">เพิ่มคำเชิญ</button></p></form>` : ""}`);
  }
  function bindTeam() {
    const f = $("#inv"); if (f) f.addEventListener("submit", (e) => { e.preventDefault(); act(async () => { const em = $("#i-email").value.trim(); const code = await rpc("staff_invite", { p_email: em, p_role: $("#i-role").value, p_name: $("#i-name").value }, true); return `เพิ่มคำเชิญแล้ว ส่งรหัสเชิญ ${code} ให้ ${em} ใช้ตอนเข้าหลังบ้านครั้งแรก`; }); });
    $$("[data-remove]").forEach((b) => b.addEventListener("click", () => act(async () => { await rpc("staff_remove", { p_email: b.dataset.remove }, true); return "ลบแล้ว"; })));
  }

  function viewPassword() {
    return admLayout(`<div class="topline"><h1>${R.recovery ? "ตั้งรหัสผ่านใหม่" : "เปลี่ยนรหัสผ่าน"}</h1></div><form class="card" id="pw" style="max-width:460px"><div class="field"><label for="pw1">รหัสผ่านใหม่ (อย่างน้อย 8 ตัว)</label><input type="password" id="pw1" autocomplete="new-password" minlength="8" required></div><p><button class="btn btn--primary" type="submit">บันทึกรหัสผ่าน</button></p></form>`);
  }
  function bindPassword() { $("#pw").addEventListener("submit", (e) => { e.preventDefault(); act(async () => { await setPassword($("#pw1").value); R.recovery = false; return "เปลี่ยนรหัสผ่านแล้ว"; }); }); }

  // ================= queue page: a separate, simple page for admins who take bookings for customers =================
  const PT = { online: ["ชำระออนไลน์", "online"], venue: ["ชำระหน้าร้าน", "venue"], package: ["คูปองแพ็กเกจ", "package"], free: ["ไม่มีค่าใช้จ่าย (รีวิว/Influ)", "free"] };
  const payType = (b) => (b.pay === "none" ? "free" : b.pay === "due" ? "venue" : b.pay === "await" ? "await" : b.pay === "review" ? "review" : b.pay === "expired" ? "expired" : b.method === "package" || b.package ? "package" : "online");
  const PT_SHOW = Object.assign({ await: ["รอลูกค้าโอน", "await"], review: ["รอตรวจสลิป", "review"], expired: ["หมดเวลาชำระ", "free"] }, PT);
  const PKGS = ["Happy Duo", "Happy Duo+1", "Happy Duo+2", "Happy Duo*2", "Happy Group", "Member 1"];
  const cleanNote = (n) => (n || "").replace(/^นำเข้าจาก Google Sheet:?\s*/, "").replace(/\s*\(จ่ายแล้วก่อนย้ายระบบ\)$/, "");
  function qHeader() {
    const closedNames = (C.closed || []).map((d) => thDays[d]).join(" ");
    const times = C.times || [];
    return `<header class="q-head"><div><h1>${LOGO}<span>Peaceful Day · จองคิว</span></h1><p class="muted small">${closedNames ? "ร้านหยุดทุกวัน" + esc(closedNames) + " · " : ""}รอบ ${esc(times[0] || "")}–${esc(times[times.length - 1] || "")} · อยู่ได้ ${C.duration / 60} ชม.</p></div>
      <div class="btn-row"><button class="btn" type="button" data-qqr>QR ลงชื่อ</button><button class="btn" type="button" data-qcsv>ส่งออก CSV</button><button class="btn btn--primary" type="button" data-qnew>+ จองคิวใหม่</button>${ME.role !== "staff" ? `<a class="btn btn--ghost" href="#admin" data-go="admin">หลังบ้าน</a>` : `<button class="btn btn--ghost" type="button" data-signout>ออกจากระบบ</button>`}</div></header>
      ${R.flash ? `<div class="alert alert--ok" role="status">${esc(R.flash)}</div>` : ""}${R.flashErr ? `<div class="alert alert--bad" role="alert">${esc(R.flashErr)}</div>` : ""}
      ${R.qQr ? `<div class="card q-qr"><div>${qrSVG("https://peacefulday.co/#waiver")}</div><div><h2>ให้ลูกค้าสแกนเพื่อลงทะเบียน</h2><p class="muted">สำหรับ walk-in สแกนแล้วลงทะเบียนในมือถือ หรือเปิด <b>peacefulday.co/#waiver</b> บนแท็บเล็ตของร้าน คนที่มากับการจอง ให้กด “+ ลงทะเบียนเพื่อน” จากคิวนั้น จะได้ผูกกับการจองและนับว่ามากับใคร</p><p><button class="btn btn--small" type="button" data-print>พิมพ์ QR</button></p></div></div>` : ""}`;
  }
  function qRow(b, opts) {
    const t = payType(b), o = opts || {};
    const note = [b.package ? b.package : "", cleanNote(b.staff_note), b.note ? "ลูกค้าแจ้ง: " + b.note : ""].filter(Boolean).join(" · ");
    const open = R.qOpen === b.ref;
    return `<li class="q-row${b.status === "checked_in" ? " is-in" : ""}${b.status === "no_show" ? " is-off" : ""}">
      <span class="dot dot--${PT_SHOW[t][1]}" title="${PT_SHOW[t][0]}"></span>
      <div class="q-who"><b>${esc(b.name)}</b> <span class="muted">${b.people} คน${b.phone ? " · " + esc(fmtPhone(b.phone)) : ""}${o.when ? " · " + esc(dShort(b.day, "th-TH")) + " " + esc(b.hm) : ""}</span>${note ? `<span class="q-note">${esc(note)}</span>` : ""}</div>
      <span class="q-tags">${b.status === "checked_in" ? '<span class="badge badge--ok">มาแล้ว</span>' : b.status === "no_show" ? '<span class="badge badge--bad">ไม่มา</span>' : b.status === "cancelled" ? '<span class="badge badge--bad">ยกเลิก</span>' : b.status === "waiting" ? '<span class="badge badge--warn">รอนัดวันใหม่</span>' : ""}${t === "venue" && b.status !== "cancelled" ? `<span class="badge badge--warn">เก็บ ${baht(b.total)}</span>` : ""}${t === "await" && b.status !== "cancelled" ? `<span class="badge badge--muted">รอโอน ถึง ${esc(new Date(b.hold_until).toLocaleTimeString("th-TH", { hour: "2-digit", minute: "2-digit", timeZone: "Asia/Bangkok" }))}</span>` : ""}${b.status !== "cancelled" && b.status !== "waiting" ? `<button class="badge ${(b.arrived || 0) >= b.people ? "badge--ok" : (b.signed || 0) < b.people ? "badge--warn" : "badge--muted"} badge-btn" type="button" data-qpeople="${esc(b.ref)}" title="ผู้ใช้บริการ: ลงทะเบียนและเช็คอินรายคน">ลงทะเบียน ${b.signed || 0}/${b.people}${b.arrived ? ` · มา ${b.arrived}` : ""}</button>` : ""}${t === "review" && b.status !== "cancelled" ? `<button class="badge badge--warn badge-btn" type="button" data-qslip="${esc(b.ref)}">ดูสลิป ${baht(b.total)}</button>` : ""}</span>
      <span class="q-act">${b.status === "cancelled" ? `<span class="muted small">${b.cancelled_at ? "ยกเลิก " + esc(dt(b.cancelled_at)) : ""}${b.pay === "expired" ? " · หมดเวลาชำระ" : ""}${(b.pay === "paid" || b.pay === "review") && b.total > 0 && b.method !== "package" ? " · <b>ลูกค้าจ่ายแล้ว ต้องคืนเงิน?</b>" : ""}</span><button class="btn btn--small" type="button" data-qrestore="${esc(b.ref)}" data-confirm="กดอีกครั้งเพื่อกู้คืน">กู้คืนคิว</button>` : ""}${o.waiting ? `<button class="btn btn--small btn--primary" type="button" data-qre="${esc(b.ref)}">นัดวันใหม่</button>` : (b.status === "confirmed" || (b.status === "checked_in" && (b.arrived || 0) < b.people)) && !o.when ? `<button class="btn btn--small btn--primary" type="button" data-qpeople="${esc(b.ref)}">${b.status === "checked_in" ? "เช็คอินคนที่เหลือ" : "เช็คอิน"}</button>` : ""}${b.status !== "cancelled" ? `<button class="btn btn--small btn--ghost" type="button" data-qmore="${esc(b.ref)}" aria-expanded="${open}">⋯</button>` : ""}</span>
      ${R.qPeople === b.ref ? `<div class="q-panel q-people">${peoplePanel(b, R.qPeopleData)}</div>` : ""}
      ${R.qSlip === b.ref ? `<div class="q-panel q-slip">${R.qSlipData ? slipCard(b, R.qSlipData) : '<span class="muted">กำลังโหลดสลิป…</span>'}</div>` : ""}
      ${open ? `<div class="q-panel">${b.status === "confirmed" ? `<button class="btn btn--small" type="button" data-qst="waiting|${esc(b.ref)}">ย้ายไปรอนัดวันใหม่</button><button class="btn btn--small" type="button" data-qst="no_show|${esc(b.ref)}">ไม่มา</button>` : ""}${b.status === "checked_in" || b.status === "no_show" ? `<button class="btn btn--small" type="button" data-qst="confirmed|${esc(b.ref)}">ย้อนเป็นจองแล้ว</button>` : ""}${["confirmed", "waiting", "no_show"].includes(b.status) ? `<button class="btn btn--small btn--danger" type="button" data-qst="cancelled|${esc(b.ref)}" data-confirm="กดอีกครั้งเพื่อยกเลิก">ยกเลิกคิว</button>` : ""}<span class="muted small">${esc(b.ref)} · ${CH[b.channel] || esc(b.channel)}</span>${ME.role !== "staff" ? ` <a class="small" href="#abooking" data-aref="${esc(b.ref)}">รายละเอียด</a>` : ""}</div>` : ""}
    </li>`;
  }
  // everyone on a booking: registration, a check-in per person, and taking payment
  const hhmm = (iso) => new Date(iso).toLocaleTimeString("th-TH", { hour: "2-digit", minute: "2-digit", timeZone: "Asia/Bangkok" });
  function peoplePanel(b, list) {
    const L = list || [], here = L.filter((x) => x.checked_at).length, missing = Math.max(0, b.people - L.length);
    const reg = "#waiver/" + b.ref + "/" + b.wtoken, regAbs = location.origin + location.pathname + reg;
    const pay = owes(b) && (b.status === "confirmed" || b.status === "checked_in") ? `<div class="pp-pay">${b.pay === "review" ? `<span class="small"><b>ลูกค้าแนบสลิปแล้ว</b> ดูสลิปก่อนรับเงิน จะได้ไม่เก็บซ้ำ <button class="btn btn--small" type="button" data-qslip="${esc(b.ref)}">ดูสลิป</button></span>` : ""}<span>รับเงิน <b>${baht(b.total)}</b> ด้วย</span> ${Object.entries(METHOD).map(([k, l]) => `<button class="btn btn--small btn--primary" type="button" data-qpay2="${k}|${esc(b.ref)}|${b.status}">${l}</button>`).join("")}</div>` : "";
    return `<div class="pp-head"><b>ผู้ใช้บริการ ${b.people} คน</b><span class="muted small">ลงทะเบียนแล้ว ${L.length} · เช็คอินแล้ว ${here}</span></div>
      ${L.length ? `<ul class="pp-list">${L.map((x) => `<li class="${x.checked_at ? "is-in" : ""}"><img src="${esc(x.signature)}" alt="ลายเซ็นของ ${esc(x.name)}"><span class="pp-who"><b>${esc(x.name)}</b><span class="muted small">${esc(fmtPhone(x.phone))}${x.email ? " · " + esc(x.email) : ""} · ${x.visits ? "มาแล้ว " + x.visits + " ครั้ง" : "ครั้งแรก"}</span>${x.came_with ? `<span class="q-note">${esc(x.came_with)}</span>` : ""}</span>${x.checked_at ? `<span class="pp-ok">✓ มาแล้ว ${esc(hhmm(x.checked_at))}</span><button class="btn btn--small btn--ghost" type="button" data-pout="${x.id}" data-confirm="กดอีกครั้งเพื่อยกเลิก">ยกเลิก</button>` : `<button class="btn btn--small btn--primary" type="button" data-pin="${x.id}">เช็คอิน</button>`}</li>`).join("")}</ul>` : ""}
      ${missing ? `<p class="pp-missing"><span>ยังไม่ลงทะเบียน <b>${missing}</b> คน</span><a class="btn btn--small" href="${esc(reg)}">+ ลงทะเบียนเพื่อน</a><button class="btn btn--small btn--ghost" type="button" data-copy="${esc(regAbs)}">คัดลอกลิงก์ให้ลูกค้า</button></p>` : `<p class="small"><a href="${esc(reg)}">+ ลงทะเบียนเพิ่ม</a></p>`}
      ${pay}
      ${ME.role !== "staff" && b.status === "confirmed" && missing ? `<p class="small muted">ลูกค้าลงทะเบียนไม่ได้จริง ๆ? <button class="btn btn--small btn--ghost" type="button" data-qgroup="${esc(b.ref)}" data-confirm="กดอีกครั้ง: เช็คอินทั้งกลุ่มโดยไม่ลงทะเบียน">เช็คอินทั้งกลุ่มโดยไม่ลงทะเบียน</button> (ผู้จัดการ/เจ้าของ)</p>` : ""}`;
  }
  function qForm(slots) {
    const N = R.qn;
    const types = Object.entries(PT).map(([k, [l]]) => `<label class="q-pt"><input type="radio" name="qn-pt" value="${k}"${N.pt === k ? " checked" : ""}><span><i class="dot dot--${PT[k][1]}"></i>${l}</span></label>`).join("");
    return `<form class="card q-form" id="qn"><div class="q-form-head"><h2>จองคิวใหม่</h2><button class="btn btn--small btn--ghost" type="button" data-qclose>ปิด</button></div>
      <div class="form-grid"><div class="field"><label for="qn-day">วันที่</label><input type="date" id="qn-day" value="${N.day}"></div><div class="field"><label for="qn-people">จำนวนคน</label><input type="number" id="qn-people" min="1" max="50" value="${N.people}"></div></div>
      <p class="q-label">เวลา</p><div class="btn-row">${slots.length ? slots.map((s) => { const left = Math.max(0, s.capacity - s.used); return `<button type="button" class="btn btn--small${N.hm === s.hm ? " btn--primary" : ""}" data-qnhm="${s.hm}"${s.blocked ? " disabled" : ""}>${s.hm} · ${s.blocked ? "ปิด" : left ? "ว่าง " + left : "เต็ม"}</button>`; }).join("") : '<span class="muted">วันนั้นร้านปิด</span>'}</div>
      <div class="form-grid" style="margin-top:12px"><div class="field"><label for="qn-name">ชื่อลูกค้า</label><input type="text" id="qn-name" maxlength="120" value="${esc(N.name)}"></div><div class="field"><label for="qn-phone">เบอร์โทร</label><input type="tel" id="qn-phone" maxlength="20" value="${esc(N.phone)}"></div>
        <div class="field"><label for="qn-ch">จองผ่าน</label><select id="qn-ch">${[["line", "LINE"], ["phone", "โทรศัพท์"], ["walk_in", "หน้าร้าน (Walk-in)"]].map(([k, l]) => `<option value="${k}"${N.channel === k ? " selected" : ""}>${l}</option>`).join("")}</select></div></div>
      <p class="q-label">การชำระเงิน</p><div class="q-pts">${types}</div>
      ${N.pt === "package" ? `<div class="field" style="max-width:320px;margin-top:10px"><label for="qn-pkg">ชื่อแพ็กเกจ</label><input type="text" id="qn-pkg" list="qn-pkgs" maxlength="60" value="${esc(N.pkg)}" placeholder="เช่น Happy Duo"><datalist id="qn-pkgs">${PKGS.map((p) => `<option value="${p}">`).join("")}</datalist></div>` : ""}
      <p class="muted small" style="margin-top:8px">${N.pt === "venue" ? `ยอดที่ต้องเก็บหน้าร้าน ${baht(priceFor(N.people))}` : N.pt === "online" ? `บันทึกว่าจ่ายแล้ว ${baht(priceFor(N.people))}` : N.pt === "package" ? "ใช้สิทธิ์คูปอง ไม่เก็บเงินเพิ่ม ถ้าต้องเก็บส่วนต่าง ใส่ไว้ในหมายเหตุ" : "ไม่เก็บเงิน"}</p>
      <div class="field" style="margin-top:10px"><label for="qn-note">หมายเหตุทีมงาน</label><input type="text" id="qn-note" maxlength="300" value="${esc(N.note)}"></div>
      ${ME.role !== "staff" ? `<label class="check" style="margin-top:8px"><input type="checkbox" id="qn-over"${N.over ? " checked" : ""}> อนุญาตเกินความจุ</label>` : ""}
      <p style="margin-top:14px"><button class="btn btn--primary" type="submit">บันทึกคิว</button></p></form>`;
  }
  async function viewQueue() {
    const tab = R.qtab || "day", d = R.qday || today();
    const strip0 = R.qstrip || today();
    const calls = [rpc("staff_days", { p_from: strip0, p_days: 14 }, true), rpc("staff_waiting", {}, true), rpc("staff_slips", {}, true)];
    if (tab === "day") calls.push(rpc("staff_day", { p_day: d }, true));
    if (tab === "search" && (R.qq || "").trim()) calls.push(rpc("staff_search", { p_q: R.qq }, true));
    if (tab === "cancelled") calls.push(rpc("staff_cancelled", {}, true));
    if (R.qSlip) calls.push(rpc("staff_slip", { p_ref: R.qSlip }, true).catch(() => null));
    const sigP = R.qSig ? rpc("staff_waivers", { p_ref: "", p_day: R.qday || today() }, true).catch(() => []) : null;
    const pplP = R.qPeople ? rpc("staff_people", { p_ref: R.qPeople }, true).catch(() => []) : null;
    const res = await Promise.all(calls);
    R.qSigData = sigP ? await sigP : null;
    R.qPeopleData = pplP ? await pplP : null;
    const [days, waiting, slips] = res, x = tab === "day" || tab === "cancelled" || (tab === "search" && (R.qq || "").trim()) ? res[3] : null;
    R.qSlipData = R.qSlip ? res[res.length - 1] : null;
    let formSlots = [];
    if (R.qn) { try { formSlots = (await rpc("staff_day", { p_day: R.qn.day }, true)).slots; } catch (e) { formSlots = []; } }
    const tabs = [["day", "ตารางคิว"], ["slips", `รอตรวจสลิป${slips.length ? ` <span class="count">${slips.length}</span>` : ""}`], ["search", "ค้นหาลูกค้า"], ["cancelled", "ยกเลิกแล้ว"], ["waiting", `รอนัดวันใหม่${waiting.length ? ` <span class="count">${waiting.length}</span>` : ""}`]];
    let body = "";
    if (tab === "day") {
      const day = x, all = day.slots.flatMap((s) => s.bookings).concat(day.off_schedule);
      const live = all.filter((b) => b.status !== "no_show");
      const stat = (n, l) => `<span class="q-stat"><b>${n}</b> ${l}</span>`;
      const closedText = day.closed ? "ปิดร้านวันนี้" : (C.closed || []).includes(dow(d)) ? "ร้านหยุดวัน" + thDays[dow(d)] : "ไม่มีรอบวันนี้";
      body = `<div class="q-days" role="group" aria-label="เลือกวัน"><button class="btn btn--small btn--ghost q-shift" type="button" data-qstrip="${addDays(strip0, -7)}" aria-label="7 วันก่อน">‹</button>${days.map((x) => `<button type="button" class="q-day${x.day === d ? " on" : ""}${x.closed ? " closed" : ""}" data-qday="${x.day}" aria-pressed="${x.day === d}"><span class="w">${x.day === today() ? "วันนี้" : esc(wd(x.day))}</span><span class="n">${Number(x.day.slice(8))}</span><span class="c">${x.closed ? "ปิด" : x.people ? x.people + " คน" : "–"}</span></button>`).join("")}<button class="btn btn--small btn--ghost q-shift" type="button" data-qstrip="${addDays(strip0, 7)}" aria-label="7 วันถัดไป">›</button></div>
      <div class="q-dayhead"><button class="btn" type="button" data-qday="${addDays(d, -1)}" aria-label="วันก่อน">‹</button><h2>${esc(dLong(d, "th-TH"))}${day.holiday != null ? ` <span class="badge badge--warn">วันหยุดนักขัตฯ</span>` : ""}</h2><input type="date" id="qdate" value="${d}" aria-label="ไปวันที่"><button class="btn" type="button" data-qday="${addDays(d, 1)}" aria-label="วันถัดไป">›</button><button class="btn" type="button" data-qday="${today()}">วันนี้</button></div>
      <div class="q-stats">${stat(live.reduce((a, b) => a + b.people, 0), "คนที่จอง")}${stat(all.filter((b) => b.status === "checked_in").reduce((a, b) => a + b.people, 0), "มาแล้ว")}${stat(all.filter((b) => b.status === "confirmed" && owes(b)).length, "ยังไม่ชำระ")}${stat(all.filter((b) => payType(b) === "package").length, "ใช้คูปอง")}${day.cancelled ? `<button class="q-stat q-stat-btn" type="button" data-qtab="cancelled"><b>${day.cancelled}</b> ยกเลิก</button>` : stat(0, "ยกเลิก")}</div>
      <p class="small"><button class="btn btn--small btn--ghost" type="button" data-qsig="*">${R.qSig === "*" ? "ปิดรายชื่อ" : "ดูทุกคนที่ลงทะเบียนวันนี้ (รวม walk-in)"}</button></p>
      ${R.qSig === "*" ? `<div class="card q-sigs">${(R.qSigData || []).length ? R.qSigData.map((w) => `<figure><img src="${esc(w.signature)}" alt="ลายเซ็นของ ${esc(w.name)}"><figcaption><b>${esc(w.name)}</b>${w.ref ? " · " + esc(w.ref) : " · ไม่มีรหัสจอง (walk-in)"} · ${{ cold: "อ่างน้ำเย็น", sauna: "ซาวน่า", both: "ทั้งสองบริการ" }[w.services] || ""} · ${esc(dt(w.signed_at))}</figcaption></figure>`).join("") : '<span class="muted">ยังไม่มีใครลงทะเบียนในวันนี้</span>'}</div>` : ""}
      <p class="q-legend">${Object.entries(PT_SHOW).filter(([k]) => k !== "expired").map(([, v]) => v).map(([l, k]) => `<span><i class="dot dot--${k}"></i>${l}</span>`).join("")}</p>
      ${day.slots.length ? `<div class="q-slots">${day.slots.map((s) => `<section class="q-slot${s.blocked ? " is-blocked" : ""}"><div class="q-slot-head"><b>${s.hm}–${s.end}</b><span class="meter${s.used >= s.capacity ? " full" : ""}" aria-hidden="true"><i style="width:${s.capacity ? Math.min(100, Math.round((s.used * 100) / s.capacity)) : 0}%"></i></span><span class="muted">${s.used}/${s.capacity} คน</span>${s.blocked ? '<span class="badge badge--muted">ปิดรอบ</span>' : `<button class="btn btn--small" type="button" data-qnewat="${s.hm}">+ จองรอบนี้</button>`}</div>${s.bookings.length ? `<ul>${s.bookings.map((b) => qRow(b)).join("")}</ul>` : ""}</section>`).join("")}
        ${day.off_schedule.length ? `<section class="q-slot"><div class="q-slot-head"><b>นอกตารางรอบ</b></div><ul>${day.off_schedule.map((b) => qRow(b)).join("")}</ul></section>` : ""}</div>`
        : `<div class="card q-closed"><h3>${esc(closedText)}</h3><p class="muted">ไม่รับจองวันนี้</p></div>`}
      <p class="muted small" style="margin-top:14px">วันนี้รับได้รอบละ ${day.slots[0] ? day.slots[0].capacity : (C.capacity)} คน · วันธรรมดา ${C.capacity} คน / เสาร์-อาทิตย์และวันหยุดนักขัตฯ ${C.capacity_weekend || C.capacity} คน${ME.role !== "staff" ? ` · <a href="#aday" data-go="aday">ปิดรอบหรือตั้งวันหยุด</a>` : ""}</p>`;
    } else if (tab === "slips") {
      body = slips.length ? `<p class="muted small">ลูกค้าโอนผ่านเว็บแล้วแนบสลิป เช็คในแอปธนาคารว่าเงินเข้าจริงแล้วกด “ยืนยันรับโอน”</p><ul class="q-list">${slips.map((b) => qRow(b, { when: true })).join("")}</ul>` : '<p class="empty">ไม่มีสลิปรอตรวจ</p>';
    } else if (tab === "cancelled") {
      const list = x || [];
      body = list.length ? `<p class="muted small">คิวที่ยกเลิกใน 30 วันที่ผ่านมาและล่วงหน้า กดผิดหรือลูกค้าเปลี่ยนใจ กด “กู้คืนคิว” ได้ ถ้ารอบนั้นยังมีที่ว่าง</p><ul class="q-list">${list.map((b) => qRow(b, { when: true, cancelled: true })).join("")}</ul>` : '<p class="empty">ไม่มีคิวที่ยกเลิก</p>';
    } else if (tab === "search") {
      const list = x || [];
      body = `<form class="filters" id="qs"><div class="field" style="flex:1 1 260px"><label for="qq">ชื่อ เบอร์ หรือรหัส PD-</label><input type="search" id="qq" value="${esc(R.qq || "")}" autofocus></div><button class="btn" type="submit">ค้นหา</button></form>
        ${(R.qq || "").trim() ? (list.length ? `<ul class="q-list">${list.map((b) => qRow(b, { when: true })).join("")}</ul>` : '<p class="empty">ไม่พบลูกค้า</p>') : '<p class="muted">พิมพ์ชื่อ เบอร์ หรือรหัสการจอง</p>'}`;
    } else {
      let re = "";
      if (R.qre) { let sl = []; try { sl = (await rpc("staff_day", { p_day: R.qreDay || today() }, true)).slots; } catch (e) { sl = []; }
        re = `<div class="card" style="margin-bottom:12px"><h2>นัดวันใหม่ ${esc(R.qre)}</h2><div class="field" style="max-width:220px"><label for="qred">วันที่</label><input type="date" id="qred" value="${esc(R.qreDay || today())}"></div><div class="btn-row" style="margin-top:10px">${sl.map((s) => `<button class="btn btn--small" type="button" data-qregoto="${s.hm}"${s.blocked ? " disabled" : ""}>${s.hm} · ว่าง ${Math.max(0, s.capacity - s.used)}</button>`).join("") || '<span class="muted">วันนั้นร้านปิด</span>'}<button class="btn btn--small btn--ghost" type="button" data-qrex>ปิด</button></div></div>`; }
      body = re + (waiting.length ? `<ul class="q-list">${waiting.map((b) => qRow(b, { when: true, waiting: true })).join("")}</ul>` : '<p class="empty">ไม่มีคิวที่รอนัดวันใหม่</p>');
    }
    return `<div class="adm q-page"><div class="q-wrap">${qHeader()}
      ${R.qn ? qForm(formSlots) : ""}
      <nav class="q-tabs" aria-label="มุมมอง">${tabs.map(([k, l]) => `<button type="button" data-qtab="${k}"${tab === k ? ' aria-current="page"' : ""}>${l}</button>`).join("")}</nav>
      ${body}</div></div>`;
  }
  function bindQueue() {
    const strip = $(".q-days"), on = $(".q-day.on"); if (strip && on) strip.scrollLeft = on.offsetLeft - strip.offsetLeft - strip.clientWidth / 2 + on.clientWidth / 2;
    const keepForm = () => { const N = R.qn; if (!N || !$("#qn")) return; N.day = $("#qn-day").value || N.day; N.people = Math.max(1, Number($("#qn-people").value) || 1); N.name = $("#qn-name").value; N.phone = $("#qn-phone").value; N.channel = $("#qn-ch").value; const pt = $("input[name=qn-pt]:checked"); N.pt = pt ? pt.value : N.pt; const pk = $("#qn-pkg"); if (pk) N.pkg = pk.value; N.note = $("#qn-note").value; const ov = $("#qn-over"); N.over = !!(ov && ov.checked); };
    const fresh = (hm) => ({ day: R.qday || today(), hm: hm || "", people: 2, name: "", phone: "", channel: "line", pt: "venue", pkg: "", note: "", over: false });
    $$("[data-qtab]").forEach((b) => b.addEventListener("click", () => { keepForm(); R.qtab = b.dataset.qtab; R.qOpen = R.qCheckin = ""; R.flash = R.flashErr = ""; render(); }));
    $$("[data-qday]").forEach((b) => b.addEventListener("click", () => { keepForm(); R.qday = b.dataset.qday; R.qOpen = R.qCheckin = ""; R.flash = R.flashErr = ""; render(); }));
    $$("[data-qstrip]").forEach((b) => b.addEventListener("click", () => { R.qstrip = b.dataset.qstrip; render(); }));
    const qd = $("#qdate"); if (qd) qd.addEventListener("change", () => { if (qd.value) { R.qday = qd.value; R.qstrip = qd.value < today() || qd.value > addDays(R.qstrip || today(), 13) ? qd.value : R.qstrip; render(); } });
    const nw = $("[data-qnew]"); if (nw) nw.addEventListener("click", () => { R.qn = R.qn || fresh(); R.flash = R.flashErr = ""; render(); window.scrollTo(0, 0); });
    $$("[data-qnewat]").forEach((b) => b.addEventListener("click", () => { R.qn = fresh(b.dataset.qnewat); R.flash = R.flashErr = ""; render(); window.scrollTo(0, 0); }));
    const cl = $("[data-qclose]"); if (cl) cl.addEventListener("click", () => { R.qn = null; render(); });
    const f = $("#qn");
    if (f) {
      $("#qn-day").addEventListener("change", () => { keepForm(); R.qn.hm = ""; render(); });
      $("#qn-people").addEventListener("change", () => { keepForm(); render(); });
      $$("input[name=qn-pt]").forEach((r) => r.addEventListener("change", () => { keepForm(); render(); }));
      $$("[data-qnhm]").forEach((b) => b.addEventListener("click", () => { keepForm(); R.qn.hm = b.dataset.qnhm; render(); }));
      f.addEventListener("submit", (e) => {
        e.preventDefault(); keepForm(); const N = R.qn;
        if (!N.hm) { R.flashErr = "เลือกเวลาก่อน"; render(); return; }
        if (N.pt === "package" && !N.pkg.trim()) { R.flashErr = "ใส่ชื่อแพ็กเกจก่อน"; render(); return; }
        act(async () => { const b = await rpc("staff_create_booking", { p: { day: N.day, hm: N.hm, people: N.people, name: N.name, phone: N.phone, channel: N.channel, pay_type: N.pt, package: N.pkg, note: N.note, over: N.over } }, true); R.qn = null; R.qtab = "day"; R.qday = b.day; return `บันทึกคิว ${b.name} ${b.people} คน ${dShort(b.day, "th-TH")} ${b.hm} แล้ว (${b.ref})`; });
      });
    }
    bindSlipButtons();
    $$("[data-qsig]").forEach((b) => b.addEventListener("click", () => { R.qSig = R.qSig === b.dataset.qsig ? "" : b.dataset.qsig; R.qOpen = R.qCheckin = R.qSlip = ""; render(); }));
    $$("[data-qpeople]").forEach((b) => b.addEventListener("click", () => { R.qPeople = R.qPeople === b.dataset.qpeople ? "" : b.dataset.qpeople; R.qOpen = R.qCheckin = R.qSlip = ""; R.flash = R.flashErr = ""; render(); }));
    $$("[data-pin]").forEach((b) => b.addEventListener("click", () => act(async () => { await rpc("staff_checkin_person", { p_id: Number(b.dataset.pin), p_on: true }, true); return "เช็คอินแล้ว"; })));
    $$("[data-pout]").forEach((b) => b.addEventListener("click", () => act(async () => { await rpc("staff_checkin_person", { p_id: Number(b.dataset.pout), p_on: false }, true); return "ยกเลิกเช็คอินคนนี้แล้ว"; })));
    $$("[data-qpay2]").forEach((b) => b.addEventListener("click", () => act(async () => {
      const [m, ref, st] = b.dataset.qpay2.split("|");
      if (st === "confirmed") await rpc("staff_checkin", { p_ref: ref, p_method: m }, true); else await rpc("staff_take_payment", { p_ref: ref, p_method: m }, true);
      return "รับเงิน" + METHOD[m] + "แล้ว";
    })));
    $$("[data-qgroup]").forEach((b) => b.addEventListener("click", () => act(async () => { await rpc("staff_checkin", { p_ref: b.dataset.qgroup, p_method: "" }, true); return "เช็คอินทั้งกลุ่มแล้ว (ไม่ได้ลงทะเบียนรายคน)"; })));
    const qq = $("[data-qqr]"); if (qq) qq.addEventListener("click", () => { R.qQr = !R.qQr; render(); });
    $$("[data-qslip]").forEach((b) => b.addEventListener("click", () => { R.qSlip = R.qSlip === b.dataset.qslip ? "" : b.dataset.qslip; R.qOpen = R.qCheckin = ""; render(); }));
    $$("[data-qmore]").forEach((b) => b.addEventListener("click", () => { R.qOpen = R.qOpen === b.dataset.qmore ? "" : b.dataset.qmore; R.qCheckin = ""; render(); }));
    $$("[data-qin]").forEach((b) => b.addEventListener("click", () => {
      if (b.dataset.due === "1") { R.qCheckin = R.qCheckin === b.dataset.qin ? "" : b.dataset.qin; R.qOpen = ""; render(); return; }
      act(async () => { await rpc("staff_checkin", { p_ref: b.dataset.qin, p_method: "" }, true); return "เช็คอินแล้ว"; });
    }));
    $$("[data-qpay]").forEach((b) => b.addEventListener("click", () => act(async () => { const ref = R.qCheckin; R.qCheckin = ""; await rpc("staff_checkin", { p_ref: ref, p_method: b.dataset.qpay }, true); return b.dataset.qpay ? "เช็คอินและรับเงิน" + METHOD[b.dataset.qpay] + "แล้ว" : "เช็คอินแล้ว (ยังไม่รับเงิน)"; })));
    $$("[data-qst]").forEach((b) => b.addEventListener("click", () => act(async () => { const [st, ref] = b.dataset.qst.split("|"); R.qOpen = ""; await rpc("staff_set_status", { p_ref: ref, p_status: st }, true); return { waiting: "ย้ายไปรอนัดวันใหม่แล้ว", cancelled: "ยกเลิกคิวแล้ว ถ้ากดผิด กู้คืนได้ที่แท็บ “ยกเลิกแล้ว”", no_show: "บันทึกว่าไม่มาแล้ว", confirmed: "ย้อนเป็นจองแล้ว" }[st]; })));
    const qs = $("#qs"); if (qs) qs.addEventListener("submit", (e) => { e.preventDefault(); R.qq = $("#qq").value; render(); });
    $$("[data-qre]").forEach((b) => b.addEventListener("click", () => { R.qre = b.dataset.qre; R.qreDay = R.qreDay || today(); render(); window.scrollTo(0, 0); }));
    const qred = $("#qred"); if (qred) qred.addEventListener("change", () => { R.qreDay = qred.value; render(); });
    const qrex = $("[data-qrex]"); if (qrex) qrex.addEventListener("click", () => { R.qre = ""; render(); });
    $$("[data-qregoto]").forEach((b) => b.addEventListener("click", () => act(async () => { const ref = R.qre, day = R.qreDay || today(); await rpc("staff_move", { p_ref: ref, p_day: day, p_hm: b.dataset.qregoto, p_over: false }, true); R.qre = ""; return `นัดวันใหม่ ${dShort(day, "th-TH")} ${b.dataset.qregoto} แล้ว`; })));
    const csv = $("[data-qcsv]");
    if (csv) csv.addEventListener("click", () => act(async () => {
      const from = addDays(today(), -90), to = addDays(today(), Number(C.horizon_days) || 45);
      const rows = await rpc("staff_export", { p_from: from, p_to: to }, true);
      const STX = { confirmed: "จองแล้ว", checked_in: "มาแล้ว", cancelled: "ยกเลิก", no_show: "ไม่มา", waiting: "รอนัดวันใหม่" };
      const head = ["รหัส", "วันที่", "เวลา", "ชื่อลูกค้า", "เบอร์โทร", "จำนวนคน", "สถานะ", "การชำระ", "แพ็กเกจ", "ยอด (บาท)", "รับเงินด้วย", "จองผ่าน", "หมายเหตุลูกค้า", "หมายเหตุทีมงาน", "จองเมื่อ"];
      const cell = csvCell;
      const lines = [head].concat(rows.map((b) => [b.ref, b.day, b.hm, b.name, b.phone ? "'" + b.phone : "", b.people, STX[b.status] || b.status, (PT_SHOW[payType(b)] || [b.pay])[0], b.package, b.total, METHOD[b.method] || b.method, CH[b.channel] || b.channel, b.note, b.staff_note, dt(b.created_at)]));
      const blob = new Blob(["﻿" + lines.map((r) => r.map(cell).join(",")).join("\r\n")], { type: "text/csv;charset=utf-8" });
      const a = document.createElement("a"); a.href = URL.createObjectURL(blob); a.download = "peacefulday-bookings-" + today() + ".csv"; document.body.appendChild(a); a.click(); setTimeout(() => { URL.revokeObjectURL(a.href); a.remove(); }, 1000);
      return `ดาวน์โหลด ${rows.length} รายการแล้ว (${dShort(from, "th-TH")} – ${dShort(to, "th-TH")})`;
    }));
  }

  // ---------- render ----------
  let renderSeq = 0;
  async function render() {
    const seq = ++renderSeq;
    const isAdm = ADMIN.includes(R.view);
    document.documentElement.lang = isAdm ? "th" : lang;
    document.body.dataset.mode = isAdm ? "adm" : "pub";
    let html;
    try {
      if (!C) { C = await rpc("public_config", {}); R.skew = C.now ? Date.parse(C.now) - Date.now() : 0; }
      if (isAdm) {
        if (!session) html = viewLogin();
        else {
          if (!ME) { try { ME = await rpc("staff_me", { p_code: R.inviteCode || "" }, true); } catch (e) { ME = null; } }
          if (!session) html = viewLogin();
          else if (ME && ME.need_code) { const wrong = ME.wrong, locked = ME.locked; ME = null; html = viewInviteCode(wrong, locked); }
          else if (!ME) { signOut(); R.flashErr = errText({ code: "not_staff" }) + " — ให้เจ้าของร้านเพิ่มอีเมลนี้ในหน้า “ทีมงาน” ก่อน"; html = viewLogin(); }
          else if (R.recovery && R.view !== "apassword") { R.view = "apassword"; html = viewPassword(); }
          else if (ME.role === "staff" && !["queue", "apassword"].includes(R.view)) { R.view = "queue"; history.replaceState(null, "", "#queue"); html = await viewQueue(); }
          else html = await ({ queue: viewQueue, admin: viewDashboard, aday: viewDay, abooking: viewAdminBooking, anew: viewNew, asearch: viewSearch, asettings: viewSettings, ateam: viewTeam, acustomers: viewCustomers, apassword: viewPassword }[R.view])();
        }
      } else html = await ({ home: viewHome, book: viewBook, booking: viewBooking, my: viewMy, profile: viewProfile, privacy: viewPrivacy, terms: viewTerms, waiver: viewWaiver }[R.view] || viewHome)();
    } catch (e) {
      if (e.code === "not_staff" || /JWT/i.test(e.message)) { signOut(); ME = null; R.flashErr = errText(e); html = viewLogin(); }
      else html = `<div class="pub"><section class="err-page"><div class="wrap"><h1>${P("โหลดข้อมูลไม่สำเร็จ", "Couldn't load")}</h1><p class="lede">${esc(errText(e))}</p><button class="btn btn--primary" type="button" data-reload>${P("ลองใหม่", "Try again")}</button></div></section></div>`;
    }
    if (seq !== renderSeq) return; // a newer screen was requested meanwhile
    app.innerHTML = html;
    bindCommon();
    const binders = { waiver: bindWaiver, profile: bindProfile, queue: bindQueue, book: bindBook, booking: bindBooking, my: bindMy, aday: bindDay, abooking: bindAdminBooking, anew: bindNew, asearch: bindSearch, asettings: bindSettings, ateam: bindTeam, acustomers: bindCustomers, apassword: bindPassword };
    if (isAdm && !$(".adm .shell") && !$(".q-page")) bindLogin(); else if (binders[R.view]) binders[R.view]();
    if (R.anchor) { const a = document.getElementById(R.anchor); R.anchor = ""; if (a) a.scrollIntoView(); }
  }
  function bindCommon() {
    $$("[data-go]").forEach((a) => a.addEventListener("click", (e) => { e.preventDefault(); if (a.dataset.qtabgo) R.qtab = a.dataset.qtabgo; const [v, anchor] = a.dataset.go.split("#"); const m = $("#mobile-nav"); if (m) m.classList.remove("is-open"); if (v === R.view && anchor) { const el = document.getElementById(anchor); if (el) el.scrollIntoView({ behavior: "smooth" }); return; } go(v, { anchor: anchor || "" }); }));
    $$("[data-lang]").forEach((a) => a.addEventListener("click", (e) => { e.preventDefault(); lang = lang === "th" ? "en" : "th"; store.set("pfd-lang", lang); render(); }));
    $$("[data-book]").forEach((a) => a.addEventListener("click", (e) => { e.preventDefault(); BK.people = Math.min(BK.people, 1) || 1; go("book", { pre: a.dataset.book }); }));
    $$("[data-open]").forEach((a) => a.addEventListener("click", (e) => { e.preventDefault(); const [ref, tok] = a.dataset.open.split("|"); go("booking", { hashArg: ref + "/" + tok, ref, token: tok, justBooked: false }); }));
    $$("[data-aref]").forEach((a) => a.addEventListener("click", (e) => { e.preventDefault(); go("abooking", { aref: a.dataset.aref, amove: false, amoveDay: "", amoveHm: "" }); }));
    $$("[data-print]").forEach((b) => b.addEventListener("click", () => window.print()));
    $$("[data-reload]").forEach((b) => b.addEventListener("click", () => location.reload()));
    $$("[data-copy]").forEach((b) => b.addEventListener("click", () => { const t = b.dataset.copy, l = b.textContent; const done = (ok) => { b.textContent = ok ? P("คัดลอกแล้ว", "Copied") : P("เลือกข้อความไว้แล้ว", "Selected"); setTimeout(() => (b.textContent = l), 2000); }; try { navigator.clipboard.writeText(t).then(() => done(true), () => { const i = b.parentElement.querySelector("input"); if (i) i.select(); done(false); }); } catch (e) { done(false); } }));
    const menu = $("[data-menu]"); if (menu) menu.addEventListener("click", () => { const n = $("#mobile-nav"); const o = !n.classList.contains("is-open"); n.classList.toggle("is-open", o); menu.setAttribute("aria-expanded", String(o)); });
    const side = $("[data-side]"); if (side) side.addEventListener("click", () => { R.sideOpen = !R.sideOpen; render(); });
    $$("[data-cust-out]").forEach((a) => a.addEventListener("click", (e) => { e.preventDefault(); custOut(); BK.name = ""; BK.phone = ""; render(); }));
    $$("[data-signout]").forEach((so) => so.addEventListener("click", (e) => { e.preventDefault(); signOut(); ME = null; R.inviteCode = ""; go("admin"); }));
    $$("[data-confirm]").forEach((b) => b.addEventListener("click", (e) => { if (b.dataset.armed === "1") return; e.stopImmediatePropagation(); e.preventDefault(); b.dataset.armed = "1"; const o = b.textContent; b.textContent = b.dataset.confirm; setTimeout(() => { if (b.isConnected) { b.dataset.armed = ""; b.textContent = o; } }, 4000); }, true));
  }

  // ---------- address bar ----------
  function fromHash() {
    let h = (location.hash || "").slice(1);
    try { h = decodeURIComponent(h); } catch (e) { /* keep the raw text */ }
    const [v, a, b] = h.split("/");
    if (v === "booking" && a && b) Object.assign(R, { view: "booking", ref: a, token: b, justBooked: false });
    else if (v === "waiver") Object.assign(R, { view: "waiver", wref: (a || "").toUpperCase(), wtok: /^[0-9a-f-]{36}$/i.test(b || "") ? b : "", wdone: null, wErr: {}, wfound: null });
    else if (["home", "book", "my", "profile", "privacy", "terms"].concat(ADMIN.filter((x) => x !== "abooking")).includes(v)) R.view = v;
    else R.view = "home";
  }
  // back-office screens fetch fresh data on their own: every 30 seconds, and whenever the tab comes back into view,
  // but never while someone is typing or has a form or panel open
  const LIVE = ["queue", "aday", "admin", "asearch"];
  function quietRefresh() {
    if (!LIVE.includes(R.view) || !session || document.hidden || R.busy) return;
    const a = document.activeElement;
    if (a && /^(INPUT|TEXTAREA|SELECT)$/.test(a.tagName)) return;
    if (R.qn || R.qre || R.qCheckin || R.qOpen || R.checkinFor || R.qSig || R.qQr || R.qSlip || R.qPeople) return;
    const y = window.scrollY; R.flash = ""; render().then(() => window.scrollTo(0, y));
  }
  setInterval(quietRefresh, 30000);
  document.addEventListener("visibilitychange", () => { if (!document.hidden) quietRefresh(); });
  window.addEventListener("popstate", () => { R.flash = R.flashErr = ""; fromHash(); render(); });
  fromHash();
  render();
})();

