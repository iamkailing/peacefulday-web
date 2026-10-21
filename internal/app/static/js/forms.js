/* Small form helpers: gift fields toggle, print, voucher payment polling. */
(() => {
  "use strict";
  document.querySelectorAll("[data-gift-toggle]").forEach((cb) => {
    const box = document.querySelector("[data-gift]");
    const sync = () => { if (box) box.hidden = !cb.checked; };
    cb.addEventListener("change", sync); sync();
  });
  document.querySelectorAll("[data-print]").forEach((b) => b.addEventListener("click", () => window.print()));
  if (document.getElementById("waiting-voucher")) {
    let n = 0;
    const t = setInterval(() => { if (++n > 30) { clearInterval(t); return; } window.location.reload(); }, 4000);
  }
  document.querySelectorAll("form").forEach((f) => f.addEventListener("submit", () => {
    const b = f.querySelector('button[type="submit"], button:not([type])');
    if (b && f.checkValidity()) setTimeout(() => { b.disabled = true; }, 0);
  }));
})();
