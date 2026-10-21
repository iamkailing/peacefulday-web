/* Booking page: wait for payment confirmation, reschedule picker, print. */
(() => {
  "use strict";
  const cfgEl = document.getElementById("manage-config");
  const T = cfgEl ? JSON.parse(cfgEl.textContent || "{}") : {};
  const lang = (document.querySelector('meta[name="lang"]') || {}).content === "en" ? "en" : "th";
  const fmt = (s, ...a) => { let i = 0; return String(s || "").replace(/%[ds]/g, () => (i < a.length ? a[i++] : "")); };

  // 1. After returning from the payment page, poll until the webhook has confirmed the payment.
  const w = document.getElementById("waiting");
  if (w) {
    const started = Date.now();
    const tick = async () => {
      try {
        const r = await fetch(w.dataset.statusUrl, { credentials: "same-origin", headers: { Accept: "application/json" } });
        const d = await r.json();
        if (d.status && d.status !== "hold") { window.location.replace(w.dataset.doneUrl); return; }
      } catch (e) { /* keep trying */ }
      const elapsed = Date.now() - started;
      if (elapsed > 180000) return;
      setTimeout(tick, elapsed < 60000 ? 2000 : 5000);
    };
    setTimeout(tick, 1500);
  }

  // 2. Reschedule: pick a date, then a session with room for the whole group.
  const form = document.querySelector("[data-reschedule]");
  if (form) {
    const date = form.querySelector('input[type="date"]');
    const box = form.querySelector("[data-move-times]");
    const submit = form.querySelector("[data-move-submit]");
    const slotInput = form.querySelector('input[name="slot_id"]');
    const need = Number(form.dataset.need || 1);
    const current = Number(form.dataset.current || 0);
    const state = (text) => { const p = document.createElement("p"); p.className = "state"; p.style.gridColumn = "1/-1"; p.textContent = text; box.replaceChildren(p); };
    date.addEventListener("change", async () => {
      slotInput.value = ""; submit.disabled = true;
      if (!date.value) return;
      state(T.loading);
      let slots = [];
      try {
        const r = await fetch(`/api/slots?date=${date.value}&exp=${form.dataset.exp}&product=${form.dataset.product}`, { credentials: "same-origin", headers: { "X-Lang": lang } });
        slots = ((await r.json()).slots || []).filter((s) => s.id !== current);
      } catch (e) { state(T.none); return; }
      const usable = slots.filter((s) => s.bookable && s.remaining >= need);
      if (!usable.length) { state(T.none); return; }
      box.replaceChildren(...slots.map((s) => {
        const ok = s.bookable && s.remaining >= need;
        const b = document.createElement("button");
        b.type = "button"; b.className = "time"; b.disabled = !ok; b.setAttribute("aria-pressed", "false");
        const t = document.createElement("b"); t.textContent = s.time;
        const l = document.createElement("span"); l.textContent = s.remaining > 0 ? fmt(T.left, s.remaining) : T.full;
        b.append(t, l);
        b.addEventListener("click", () => {
          box.querySelectorAll(".time").forEach((x) => x.setAttribute("aria-pressed", "false"));
          b.setAttribute("aria-pressed", "true");
          slotInput.value = String(s.id); submit.disabled = false;
        });
        return b;
      }));
    });
    form.addEventListener("submit", (e) => { if (!slotInput.value) e.preventDefault(); else submit.disabled = true; });
  }

  // 3. Print / save as PDF
  document.querySelectorAll("[data-print]").forEach((b) => b.addEventListener("click", () => window.print()));
})();
