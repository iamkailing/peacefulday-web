/* Peaceful Day booking flow. Vanilla JS, no dependencies.
   Server is the source of truth: availability, prices and holds are always re-checked on submit. */
(() => {
  "use strict";
  const cfgEl = document.getElementById("book-config");
  const form = document.getElementById("book");
  if (!cfgEl || !form) return;
  const cfg = JSON.parse(cfgEl.textContent || "{}");
  const T = cfg.t || {};
  const lang = cfg.lang === "en" ? "en" : "th";
  const locale = lang === "en" ? "en-GB" : "th-TH";
  const csrf = (document.querySelector('meta[name="csrf"]') || {}).content || "";
  const tickets = cfg.tickets || [];

  const $ = (s, el = document) => el.querySelector(s);
  const $$ = (s, el = document) => Array.from(el.querySelectorAll(s));
  const fmt = (s, ...a) => { let i = 0; return String(s || "").replace(/%[ds]/g, () => (i < a.length ? a[i++] : "")); };
  const baht = (satang) => "฿" + (satang / 100).toLocaleString("en-US", { minimumFractionDigits: satang % 100 ? 2 : 0, maximumFractionDigits: 2 });
  const newKey = () => (crypto.randomUUID ? crypto.randomUUID() : String(Date.now()) + Math.random().toString(16).slice(2));
  const el = (tag, attrs = {}, ...kids) => {
    const n = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs)) {
      if (v === false || v == null) continue;
      if (k === "text") n.textContent = v;
      else if (k === "class") n.className = v;
      else if (k.startsWith("on")) n.addEventListener(k.slice(2), v);
      else n.setAttribute(k, v === true ? "" : v);
    }
    for (const k of kids) if (k != null) n.append(k);
    return n;
  };
  const store = {
    get(k) { try { return sessionStorage.getItem("pd_book_" + k) || ""; } catch (e) { return ""; } },
    set(k, v) { try { sessionStorage.setItem("pd_book_" + k, v); } catch (e) { /* storage unavailable */ } },
  };

  const st = { product: null, date: "", slot: null, slots: [], qty: 1, quote: null, busy: false, idem: newKey(), quoteSeq: 0, daysSeq: 0, slotsSeq: 0 };

  // ---------- networking ----------
  async function api(method, url, body) {
    const res = await fetch(url, {
      method, credentials: "same-origin",
      headers: Object.assign({ "X-Lang": lang, "Accept": "application/json" }, body ? { "Content-Type": "application/json", "X-CSRF": csrf } : {}),
      body: body ? JSON.stringify(body) : undefined,
    });
    let data = {};
    try { data = await res.json(); } catch (e) { /* empty body */ }
    return { ok: res.ok, status: res.status, data };
  }

  // ---------- errors ----------
  const errBox = $("#book-error");
  function showError(msg) {
    if (!msg) { errBox.classList.add("hide"); return; }
    $("p", errBox).textContent = msg;
    errBox.classList.remove("hide");
    errBox.focus({ preventScroll: true });
    errBox.scrollIntoView({ behavior: "smooth", block: "center" });
  }
  function fieldError(name, msg) {
    const f = $(`[data-field="${name}"]`);
    if (!f) return false;
    f.classList.toggle("has-error", !!msg);
    const e = $(".err", f);
    if (e) { e.textContent = msg || ""; e.hidden = !msg; }
    const input = $("input, textarea, select", f);
    if (input) input.setAttribute("aria-invalid", msg ? "true" : "false");
    return true;
  }
  function clearErrors() { $$("[data-field]").forEach((f) => fieldError(f.dataset.field, "")); showError(""); }

  // ---------- ticket ----------
  const ticket = () => tickets.find((t) => String(t.id) === String(st.product)) || null;
  function initTickets() {
    const radios = $$('#tickets input[name="product"]');
    const pre = radios.find((r) => r.value === String(cfg.preProduct || ""));
    (pre || radios[0] || {}).checked = true;
    st.product = (pre || radios[0] || {}).value || null;
    if (radios.length <= 1) $("#step-ticket").classList.add("hide");
    radios.forEach((r) => r.addEventListener("change", () => {
      st.product = r.value; st.slot = null; st.idem = newKey();
      clampQty(); loadDays(); renderSummary(); requestQuote();
    }));
  }

  // ---------- dates ----------
  const datesBox = $("#dates");
  const monthLabel = $("#month-label");
  const dayFmt = new Intl.DateTimeFormat(locale, { weekday: "short", timeZone: "Asia/Bangkok" });
  const monFmt = new Intl.DateTimeFormat(locale, { month: "short", timeZone: "Asia/Bangkok" });
  const monthYearFmt = new Intl.DateTimeFormat(locale, { month: "long", year: "numeric", timeZone: "Asia/Bangkok" });
  const longFmt = new Intl.DateTimeFormat(locale, { weekday: "long", day: "numeric", month: "long", timeZone: "Asia/Bangkok" });
  const asDate = (iso) => new Date(iso + "T12:00:00+07:00");

  async function loadDays() {
    const t = ticket();
    if (!t) return;
    const seq = ++st.daysSeq;
    datesBox.replaceChildren(el("div", { class: "skeleton", style: "width:100%" }, el("i"), el("i"), el("i"), el("i")));
    let r;
    try {
      r = await api("GET", `/api/days?exp=${t.exp || ""}&product=${t.id}&days=${Math.min((cfg.horizon || 60) + 1, 62)}`);
    } catch (e) { if (seq === st.daysSeq) datesBox.replaceChildren(el("p", { class: "state", text: T.offline })); return; }
    if (seq !== st.daysSeq) return;
    const days = (r.data && r.data.days) || [];
    datesBox.replaceChildren();
    let firstOpen = "";
    for (const d of days) {
      const dt = asDate(d.date);
      const b = el("button", {
        type: "button", class: "date", "data-date": d.date, "data-status": d.status, "aria-pressed": "false",
        "aria-label": `${longFmt.format(dt)}: ${d.status === "open" ? fmt(T.left, d.remaining) : d.status === "full" ? T.full : T.closed}`,
        disabled: d.status === "closed",
        onclick: () => selectDate(d.date, true),
      }, el("span", { class: "w", text: dayFmt.format(dt) }), el("span", { class: "n", text: String(dt.getDate()) }), el("span", { class: "m", text: monFmt.format(dt) }), el("span", { class: "a", "aria-hidden": "true" }));
      datesBox.append(b);
      if (!firstOpen && d.status === "open") firstOpen = d.date;
    }
    if (!days.length) { datesBox.replaceChildren(el("p", { class: "state", text: T.noslots })); return; }
    const wanted = [st.date, cfg.preDate].find((x) => x && days.some((d) => d.date === x && d.status !== "closed"));
    selectDate(wanted || firstOpen || days[0].date, false);
  }

  function selectDate(iso, user) {
    st.date = iso;
    if (user) { st.slot = null; cfg.preSlot = 0; st.idem = newKey(); }
    $$(".date", datesBox).forEach((b) => b.setAttribute("aria-pressed", b.dataset.date === iso ? "true" : "false"));
    const btn = $(`.date[data-date="${iso}"]`, datesBox);
    if (btn && !user) btn.scrollIntoView({ block: "nearest", inline: "center" });
    monthLabel.textContent = monthYearFmt.format(asDate(iso));
    loadSlots();
    renderSummary();
  }

  // ---------- times ----------
  const timesBox = $("#times");
  async function loadSlots() {
    const t = ticket();
    if (!t || !st.date) return;
    const seq = ++st.slotsSeq;
    timesBox.replaceChildren(el("div", { class: "skeleton", "aria-label": T.loading }, el("i"), el("i"), el("i"), el("i"), el("i"), el("i")));
    let r;
    try { r = await api("GET", `/api/slots?date=${st.date}&exp=${t.exp || ""}&product=${t.id}`); }
    catch (e) { if (seq === st.slotsSeq) timesBox.replaceChildren(el("p", { class: "state", text: T.offline })); return; }
    if (seq !== st.slotsSeq) return;
    st.slots = (r.data && r.data.slots) || [];
    renderSlots();
  }

  function renderSlots() {
    const avail = st.slots.filter((s) => s.bookable);
    if (!st.slots.length || !avail.length) {
      timesBox.replaceChildren(el("p", { class: "state", text: T.noslots }));
      st.slot = null; renderSummary(); requestQuote();
      return;
    }
    const grid = el("div", { class: "times", role: "group" });
    for (const s of st.slots) {
      const low = s.remaining > 0 && s.remaining <= 2;
      grid.append(el("button", {
        type: "button", class: "time", "data-id": s.id, "aria-pressed": st.slot && st.slot.id === s.id ? "true" : "false", disabled: !s.bookable,
        "aria-label": `${s.time}–${s.end}, ${s.remaining > 0 ? fmt(T.left, s.remaining) : T.full}`,
        onclick: () => selectSlot(s, true),
      }, el("b", { text: s.time }), el("span", { class: low ? "low" : "", text: s.remaining > 0 ? fmt(T.left, s.remaining) : T.full })));
    }
    timesBox.replaceChildren(grid);
    const keep = st.slot && st.slots.find((s) => s.id === st.slot.id && s.bookable);
    const pre = cfg.preSlot && st.slots.find((s) => s.id === Number(cfg.preSlot) && s.bookable);
    if (keep) selectSlot(keep, false); else if (pre) selectSlot(pre, false); else { st.slot = null; renderSummary(); requestQuote(); }
  }

  function selectSlot(s, user) {
    st.slot = s;
    if (user) st.idem = newKey();
    $$(".time", timesBox).forEach((b) => b.setAttribute("aria-pressed", Number(b.dataset.id) === s.id ? "true" : "false"));
    clampQty(); renderSummary(); requestQuote();
    if (user && window.matchMedia("(max-width: 999px)").matches) $("#step-people").scrollIntoView({ behavior: "smooth", block: "center" });
  }

  // ---------- guests ----------
  const qtyOut = $("#qty"), minus = $("#qty-minus"), plus = $("#qty-plus"), qtyHint = $("#qty-hint");
  function limits() {
    const t = ticket() || { min: 1, max: 1 };
    let max = t.max;
    if (st.slot) max = Math.min(max, st.slot.remaining);
    return { min: t.min, max: Math.max(max, 0), ticketMax: t.max };
  }
  function clampQty() {
    const { min, max, ticketMax } = limits();
    st.qty = Math.max(min, Math.min(st.qty, Math.max(max, min)));
    qtyOut.textContent = String(st.qty);
    minus.disabled = st.qty <= min;
    plus.disabled = st.qty >= max;
    const hints = [];
    if (min > 1) hints.push(fmt(T.minGroup, min));
    if (st.slot && st.slot.remaining < ticketMax) hints.push(fmt(T.spotsLeft, st.slot.remaining));
    else hints.push(fmt(T.maxGroup, ticketMax));
    qtyHint.textContent = hints.join(" · ");
  }
  minus.addEventListener("click", () => { st.qty--; st.idem = newKey(); clampQty(); renderSummary(); requestQuote(); });
  plus.addEventListener("click", () => { st.qty++; st.idem = newKey(); clampQty(); renderSummary(); requestQuote(); });

  // ---------- quote & summary ----------
  const sum = (k) => $$(`[data-sum="${k}"]`);
  function setSum(k, v) { sum(k).forEach((n) => { n.textContent = v; }); }
  function localTotal() {
    const t = ticket();
    if (!t) return 0;
    return t.mode === "flat" ? t.price : t.price * st.qty;
  }
  function renderSummary() {
    const t = ticket();
    setSum("ticket", t ? t.name : "–");
    setSum("date", st.date ? longFmt.format(asDate(st.date)) : "–");
    setSum("time", st.slot ? `${st.slot.time}–${st.slot.end}` : "–");
    setSum("people", String(st.qty));
    const q = st.quote;
    const subtotal = q ? q.subtotal : localTotal();
    const total = q ? q.total : localTotal();
    setSum("subtotal", baht(subtotal));
    $('[data-row="discount"]').classList.toggle("hide", !(q && q.discount > 0));
    if (q && q.discount > 0) setSum("discount", "−" + baht(q.discount));
    $('[data-row="credits"]').classList.toggle("hide", !(q && q.credits > 0));
    if (q && q.credits > 0) { setSum("credits", "−" + q.creditsText); setSum("credits-label", fmt(T.credits, q.credits)); }
    setSum("total", baht(total));
    const label = total === 0 && (q && q.credits > 0) ? T.confirm : `${T.paynow} · ${baht(total)}`;
    $$("[data-submit]").forEach((b) => { if (!st.busy) b.textContent = label; b.disabled = st.busy || !t; });
  }

  let quoteTimer = 0;
  function requestQuote() {
    clearTimeout(quoteTimer);
    if (!st.slot || !ticket()) { st.quote = null; renderSummary(); return; }
    quoteTimer = setTimeout(doQuote, 150);
  }
  async function doQuote() {
    const seq = ++st.quoteSeq;
    const body = payload();
    let r;
    try { r = await api("POST", "/api/quote", body); } catch (e) { return; }
    if (seq !== st.quoteSeq) return;
    fieldError("promo", ""); fieldError("voucher", "");
    if (r.ok) { st.quote = r.data; renderSummary(); return; }
    st.quote = null; renderSummary();
    const code = r.data && r.data.error;
    if (code === "promo" || code === "promo_used") fieldError("promo", r.data.message);
    else if (code === "pass") fieldError("voucher", r.data.message);
    else if (code === "slot_full" || code === "slot_closed" || code === "too_soon") { showError(r.data.message); loadSlots(); }
  }
  $$("[data-apply]").forEach((b) => b.addEventListener("click", () => { st.idem = newKey(); requestQuote(); }));
  ["#f-promo", "#f-voucher"].forEach((id) => $(id).addEventListener("keydown", (e) => { if (e.key === "Enter") { e.preventDefault(); st.idem = newKey(); requestQuote(); } }));

  // ---------- details ----------
  const fields = { name: $("#f-name"), phone: $("#f-phone"), email: $("#f-email"), note: $("#f-note"), promo: $("#f-promo"), voucher: $("#f-voucher") };
  ["name", "phone", "email"].forEach((k) => {
    if (!fields[k].value) fields[k].value = store.get(k);
    fields[k].addEventListener("input", () => { store.set(k, fields[k].value); fieldError(k, ""); });
  });
  ["#f-agree", "#f-health"].forEach((id) => $(id).addEventListener("change", (e) => fieldError(e.target.name, "")));

  const normPhone = (s) => {
    const plus = s.trim().startsWith("+");
    let d = s.replace(/\D/g, "");
    if (d.startsWith("66") && d.length === 11) return "0" + d.slice(2);
    return plus ? "+" + d : d;
  };
  function validate() {
    const errs = [];
    if (!fields.name.value.trim()) errs.push(["name", T.name]);
    const p = normPhone(fields.phone.value);
    if (!(/^0[689]\d{8}$|^0[2-7]\d{7}$/.test(p) || (/^\+\d{7,15}$/.test(p)))) errs.push(["phone", T.phone]);
    if (!/^[^\s@]+@[^\s@]+\.[^\s@]{2,}$/.test(fields.email.value.trim())) errs.push(["email", T.email]);
    if (!$("#f-health").checked) errs.push(["health", T.health]);
    if (!$("#f-agree").checked) errs.push(["agree", T.agree]);
    errs.forEach(([k, m]) => fieldError(k, m));
    return errs;
  }

  function payload() {
    return {
      slot_id: st.slot ? st.slot.id : 0, product_id: Number(st.product), participants: st.qty,
      promo: fields.promo.value.trim().toUpperCase(), voucher: fields.voucher.value.trim().toUpperCase(),
      name: fields.name.value.trim(), phone: fields.phone.value.trim(), email: fields.email.value.trim(), note: fields.note.value.trim(),
      consent: $("#f-consent").checked, agree: $("#f-agree").checked, health: $("#f-health").checked, lang, idempotency_key: st.idem,
    };
  }

  // ---------- submit ----------
  function setBusy(on, text) {
    st.busy = on;
    $$("[data-submit]").forEach((b) => {
      b.disabled = on;
      if (on) b.replaceChildren(el("span", { class: "spinner", "aria-hidden": "true" }), document.createTextNode(" " + (text || "")));
    });
    if (!on) renderSummary();
  }

  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    if (st.busy) return;
    clearErrors();
    if (!st.slot) {
      showError(T.pickTime);
      $("#step-time").scrollIntoView({ behavior: "smooth", block: "center" });
      return;
    }
    const errs = validate();
    if (errs.length) {
      const first = $(`[data-field="${errs[0][0]}"] input, [data-field="${errs[0][0]}"] textarea`);
      if (first) { first.focus(); first.scrollIntoView({ behavior: "smooth", block: "center" }); }
      return;
    }
    setBusy(true, T.redirecting);
    if (window.pdTrack) window.pdTrack("begin_checkout", { value: (st.quote ? st.quote.total : localTotal()) / 100 });
    let r;
    try { r = await api("POST", "/api/bookings", payload()); }
    catch (err) { setBusy(false); showError(T.offline); return; } // keep the same idempotency key so a retry can't double-book
    if (r.ok && r.data.redirect) { window.location.assign(r.data.redirect); return; }
    setBusy(false);
    st.idem = newKey();
    const code = r.data && r.data.error;
    const msg = (r.data && r.data.message) || T.generic;
    if (code === "csrf") { showError(msg); setTimeout(() => window.location.reload(), 1800); return; }
    if (r.status === 502 && r.data.manage) { showError(msg); return; }
    const map = { name: "name", phone: "phone", email: "email", promo: "promo", promo_used: "promo", pass: "voucher", duplicate: "phone", agree: "agree" };
    if (map[code] && fieldError(map[code], msg)) {
      const f = $(`[data-field="${map[code]}"] input`);
      if (f) { f.focus(); f.scrollIntoView({ behavior: "smooth", block: "center" }); }
      if (code === "promo" || code === "promo_used" || code === "pass") { $("#codes").open = true; }
      return;
    }
    if (code === "slot_full" || code === "slot_closed" || code === "too_soon" || code === "participants") { st.slot = null; loadSlots(); }
    showError(msg);
  });

  // ---------- start ----------
  initTickets();
  clampQty();
  renderSummary();
  loadDays();
  if (fields.promo.value || fields.voucher.value) requestQuote();
  try { fetch("/api/event", { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", "X-CSRF": csrf }, body: JSON.stringify({ type: "booking_start" }) }); } catch (e) { /* analytics are best-effort */ }
})();
