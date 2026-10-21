/* Peaceful Day site behaviour: menu, consent-gated tags, first-party page views, map loading. */
(() => {
  "use strict";
  const cfgEl = document.getElementById("pd-config");
  const cfg = cfgEl ? JSON.parse(cfgEl.textContent || "{}") : {};
  const tags = cfg.tags || {};
  const csrf = (document.querySelector('meta[name="csrf"]') || {}).content || "";
  let consent = cfg.consent || "";

  const ready = (fn) => (document.readyState === "loading" ? document.addEventListener("DOMContentLoaded", fn) : fn());

  // ---------- tags (only after the visitor allows them) ----------
  // If GTM is configured we only push to dataLayer and let GTM fire GA4/Ads/Pixel, so nothing is counted twice.
  let tagsLoaded = false;
  function loadScript(src) { const s = document.createElement("script"); s.async = true; s.src = src; document.head.appendChild(s); }
  function loadTags() {
    if (tagsLoaded || consent !== "all") return;
    tagsLoaded = true;
    window.dataLayer = window.dataLayer || [];
    if (tags.gtm_id) {
      window.dataLayer.push({ "gtm.start": Date.now(), event: "gtm.js" });
      loadScript("https://www.googletagmanager.com/gtm.js?id=" + encodeURIComponent(tags.gtm_id));
    } else {
      if (tags.ga4_id || tags.google_ads_id) {
        window.gtag = function () { window.dataLayer.push(arguments); };
        window.gtag("js", new Date());
        if (tags.ga4_id) window.gtag("config", tags.ga4_id);
        if (tags.google_ads_id) window.gtag("config", tags.google_ads_id);
        loadScript("https://www.googletagmanager.com/gtag/js?id=" + encodeURIComponent(tags.ga4_id || tags.google_ads_id));
      }
      if (tags.meta_pixel_id) {
        /* Standard Meta Pixel bootstrap, written out so no inline script is needed. */
        const f = window;
        if (!f.fbq) {
          const n = (f.fbq = function () { n.callMethod ? n.callMethod.apply(n, arguments) : n.queue.push(arguments); });
          if (!f._fbq) f._fbq = n;
          n.push = n; n.loaded = true; n.version = "2.0"; n.queue = [];
          loadScript("https://connect.facebook.net/en_US/fbevents.js");
        }
        f.fbq("init", tags.meta_pixel_id);
        f.fbq("track", "PageView");
      }
    }
    const p = cfg.purchase;
    if (p && p.ID) {
      // event_id / transaction_id = booking reference: lets GA4, Google Ads and Meta (Pixel + Conversions API) de-duplicate.
      track("purchase", { transaction_id: p.ID, value: p.Value, currency: "THB" });
    }
  }
  function track(event, params) {
    if (consent !== "all") return;
    params = params || {};
    window.dataLayer = window.dataLayer || [];
    if (tags.gtm_id) { window.dataLayer.push(Object.assign({ event }, params)); return; }
    if (window.gtag) {
      window.gtag("event", event, params);
      if (event === "purchase" && tags.google_ads_id && tags.google_ads_label) {
        window.gtag("event", "conversion", { send_to: tags.google_ads_id + "/" + tags.google_ads_label, value: params.value, currency: "THB", transaction_id: params.transaction_id });
      }
    }
    if (window.fbq) {
      if (event === "purchase") window.fbq("track", "Purchase", { value: params.value, currency: "THB" }, { eventID: params.transaction_id });
      if (event === "begin_checkout") window.fbq("track", "InitiateCheckout", { value: params.value, currency: "THB" });
    }
  }
  window.pdTrack = track;

  function setConsent(choice) {
    consent = choice;
    const body = new URLSearchParams({ choice, csrf });
    fetch("/api/consent", { method: "POST", credentials: "same-origin", headers: { "X-CSRF": csrf }, body }).catch(() => {});
    hideBanner();
    if (choice === "all") loadTags();
  }
  function hideBanner() { const b = document.querySelector("[data-consent]"); if (b) b.classList.add("hide"); }

  ready(() => {
    // mobile menu
    const btn = document.querySelector("[data-menu]");
    const nav = document.getElementById("mobile-nav");
    if (btn && nav) {
      btn.addEventListener("click", () => {
        const open = btn.getAttribute("aria-expanded") !== "true";
        btn.setAttribute("aria-expanded", open ? "true" : "false");
        nav.classList.toggle("is-open", open);
      });
      document.addEventListener("keydown", (e) => { if (e.key === "Escape" && nav.classList.contains("is-open")) { nav.classList.remove("is-open"); btn.setAttribute("aria-expanded", "false"); btn.focus(); } });
    }
    // header hairline once the page scrolls
    const header = document.querySelector(".site-header");
    if (header) {
      const onScroll = () => header.classList.toggle("is-scrolled", window.scrollY > 8);
      onScroll();
      window.addEventListener("scroll", onScroll, { passive: true });
    }
    // consent banner
    const banner = document.querySelector("[data-consent]");
    if (banner && Object.keys(tags).length) {
      if (!consent) banner.classList.remove("hide");
      banner.querySelectorAll("[data-consent-choice]").forEach((b) => b.addEventListener("click", () => setConsent(b.dataset.consentChoice)));
    }
    document.querySelectorAll("[data-consent-open]").forEach((a) => a.addEventListener("click", (e) => { e.preventDefault(); if (banner) banner.classList.remove("hide"); }));
    loadTags();

    // map: load Google's iframe on request (or straight away once the visitor allowed third-party cookies)
    document.querySelectorAll("[data-map]").forEach((box) => {
      const load = () => {
        if (box.querySelector("iframe")) return;
        const f = document.createElement("iframe");
        f.src = box.dataset.map; f.title = box.dataset.mapTitle || "Map"; f.loading = "lazy"; f.referrerPolicy = "no-referrer-when-downgrade";
        box.replaceChildren(f);
      };
      const b = box.querySelector("[data-map-load]");
      if (b) b.addEventListener("click", load);
      if (consent === "all") load();
    });

    // first-party, anonymous page view count (no personal data)
    if (!document.body.dataset.noTrack) {
      fetch("/api/event", { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", "X-CSRF": csrf }, body: JSON.stringify({ type: "page_view" }) }).catch(() => {});
    }
  });
})();
