/* Peaceful Day admin helpers. No dependencies. */
(() => {
  "use strict";
  const $ = (s, el = document) => el.querySelector(s);
  const $$ = (s, el = document) => Array.from(el.querySelectorAll(s));
  const ready = (fn) => (document.readyState === "loading" ? document.addEventListener("DOMContentLoaded", fn) : fn());

  ready(() => {
    // sidebar on small screens
    const side = $("[data-side]");
    const tog = $("[data-side-toggle]");
    if (side && tog) tog.addEventListener("click", () => { const open = !side.classList.contains("is-open"); side.classList.toggle("is-open", open); tog.setAttribute("aria-expanded", String(open)); });

    // chart tooltips: one floating tip for every [data-tip] mark; same on hover and keyboard focus
    let tip = null;
    const show = (el, x, y) => {
      if (!tip) { tip = document.createElement("div"); tip.className = "tip"; tip.setAttribute("role", "status"); document.body.appendChild(tip); }
      tip.textContent = el.dataset.tip;
      tip.style.display = "block";
      const r = tip.getBoundingClientRect();
      let left = x + 12, top = y - r.height - 12;
      if (left + r.width > window.innerWidth - 8) left = window.innerWidth - r.width - 8;
      if (top < 8) top = y + 16;
      tip.style.left = left + "px"; tip.style.top = top + "px";
    };
    const hide = () => { if (tip) tip.style.display = "none"; };
    document.addEventListener("pointermove", (e) => {
      const el = e.target.closest && e.target.closest("[data-tip]");
      if (el) show(el, e.clientX, e.clientY); else hide();
    });
    document.addEventListener("focusin", (e) => {
      const el = e.target.closest && e.target.closest("[data-tip]");
      if (el) { const r = el.getBoundingClientRect(); show(el, r.left + r.width / 2, r.top); } else hide();
    });
    document.addEventListener("scroll", hide, { passive: true });

    // two-step confirm for destructive buttons (no browser dialogs): first click arms, second submits
    $$("[data-confirm]").forEach((b) => {
      const original = b.textContent;
      let timer = 0;
      b.addEventListener("click", (e) => {
        if (b.dataset.armed === "true") return; // second click: let it submit
        e.preventDefault();
        b.dataset.armed = "true";
        b.textContent = b.dataset.confirm;
        timer = setTimeout(() => { b.dataset.armed = "false"; b.textContent = original; }, 4000);
      });
      b.form && b.form.addEventListener("submit", () => clearTimeout(timer));
    });

    // prevent double submits
    $$("form").forEach((f) => f.addEventListener("submit", (e) => {
      if (f.dataset.busy === "1") { e.preventDefault(); return; }
      setTimeout(() => { f.dataset.busy = "1"; }, 0);
      setTimeout(() => { f.dataset.busy = "0"; }, 8000);
    }));

    // copy buttons
    $$("[data-copy]").forEach((b) => b.addEventListener("click", async () => {
      try { await navigator.clipboard.writeText(b.dataset.copy); const t = b.textContent; b.textContent = "คัดลอกแล้ว"; setTimeout(() => { b.textContent = t; }, 1500); } catch (e) { /* clipboard blocked */ }
    }));

    // auto-submit selects inside filter forms
    $$("form[data-autosubmit] select, form[data-autosubmit] input[type=date]").forEach((s) => s.addEventListener("change", () => s.form.requestSubmit()));

    // UTM link builder
    const utm = $("[data-utm]");
    if (utm) {
      const out = $("[data-utm-out]", utm);
      const build = () => {
        const u = new URL(($("[data-utm-path]", utm).value || "/"), utm.dataset.base || window.location.origin);
        $$("[data-utm-k]", utm).forEach((i) => { const v = i.value.trim().toLowerCase().replace(/\s+/g, "-"); if (v) u.searchParams.set(i.dataset.utmK, v); });
        out.value = u.toString();
      };
      $$("input, select", utm).forEach((i) => i.addEventListener("input", build));
      out.addEventListener("focus", () => out.select());
      build();
    }

    // new booking: load sessions for the chosen date
    const nb = $("[data-new-booking]");
    if (nb) {
      const date = $('input[name="date"]', nb);
      const box = $("[data-slots]", nb);
      const hidden = $('input[name="slot_id"]', nb);
      const load = async () => {
        box.textContent = "กำลังโหลดรอบ…";
        let slots = [];
        try {
          const r = await fetch("/admin/api/slots?date=" + encodeURIComponent(date.value), { credentials: "same-origin" });
          slots = (await r.json()).slots || [];
        } catch (e) { box.textContent = "โหลดรอบไม่สำเร็จ ลองใหม่"; return; }
        if (!slots.length) { box.textContent = "วันนี้ไม่มีรอบ (ปิด หรือยังไม่ได้ตั้งตาราง)"; return; }
        box.replaceChildren(...slots.map((s) => {
          const b = document.createElement("button");
          b.type = "button";
          b.className = "btn btn--small";
          b.textContent = `${s.time} · ว่าง ${s.remaining}/${s.capacity}${s.status === "blocked" ? " · ปิด" : ""}`;
          if (String(s.id) === hidden.value) b.classList.add("btn--primary");
          b.addEventListener("click", () => { hidden.value = s.id; $$("button", box).forEach((x) => x.classList.remove("btn--primary")); b.classList.add("btn--primary"); });
          return b;
        }));
      };
      date.addEventListener("change", () => { hidden.value = ""; load(); });
      load();
    }

    // check-in: camera QR scanning where the browser supports it; USB/Bluetooth scanners type into the input
    const scanBtn = $("[data-scan]");
    if (scanBtn) {
      const input = $("#code");
      if (!("BarcodeDetector" in window)) {
        scanBtn.hidden = true;
      } else {
        let stream = null;
        scanBtn.addEventListener("click", async () => {
          const video = $("[data-scan-video]");
          if (stream) { stream.getTracks().forEach((t) => t.stop()); stream = null; video.hidden = true; scanBtn.textContent = "สแกนด้วยกล้อง"; return; }
          try {
            stream = await navigator.mediaDevices.getUserMedia({ video: { facingMode: "environment" } });
          } catch (e) { scanBtn.textContent = "เปิดกล้องไม่ได้"; return; }
          video.srcObject = stream; video.hidden = false; await video.play();
          scanBtn.textContent = "ปิดกล้อง";
          const det = new window.BarcodeDetector({ formats: ["qr_code"] });
          const loop = async () => {
            if (!stream) return;
            try {
              const codes = await det.detect(video);
              if (codes.length) {
                input.value = codes[0].rawValue;
                stream.getTracks().forEach((t) => t.stop()); stream = null;
                input.form.requestSubmit();
                return;
              }
            } catch (e) { /* frame not ready */ }
            requestAnimationFrame(loop);
          };
          loop();
        });
      }
    }
  });
})();
