// Peaceful Day — emails (Supabase Edge Function "pfd-mail"), sent through Resend from hello@peacefulday.co.
//  { ref }    : payment confirmation for a paid web booking. Called by the database when a booking becomes paid;
//               it only sends once per booking, so calling it again does nothing.
//  { backup } : the weekly backup of members and bookings (CSV files) to the shop owner. Called by pg_cron with a
//               token that lives only in the database.
// Secrets (Edge Functions → Secrets): RESEND_KEY = Resend API key. "Verify JWT" is off: the database calls this without a login.

const SB_URL = Deno.env.get("SUPABASE_URL") || "";
const SB_KEY = Deno.env.get("SUPABASE_SERVICE_ROLE_KEY") || "";
const RESEND_KEY = Deno.env.get("RESEND_KEY") || "";
const FROM = "Peaceful Day <hello@peacefulday.co>";
const SITE = "https://peacefulday.co/";
const out = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

async function rpc(fn: string, args: Record<string, unknown>) {
  const r = await fetch(`${SB_URL}/rest/v1/rpc/${fn}`, { method: "POST", headers: { apikey: SB_KEY, Authorization: `Bearer ${SB_KEY}`, "Content-Type": "application/json" }, body: JSON.stringify(args) });
  const t = await r.text();
  if (!r.ok) throw new Error(t.slice(0, 200));
  return t ? JSON.parse(t) : null;
}
async function send(mail: Record<string, unknown>) {
  const r = await fetch("https://api.resend.com/emails", { method: "POST", headers: { Authorization: `Bearer ${RESEND_KEY}`, "Content-Type": "application/json" }, body: JSON.stringify({ from: FROM, ...mail }) });
  if (!r.ok) throw new Error("resend " + r.status + " " + (await r.text()).slice(0, 200));
}

const esc = (s: unknown) => String(s ?? "").replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c] as string));
const baht = (n: unknown) => "฿" + Number(n || 0).toLocaleString("en-US");
const TH_DAYS = ["อาทิตย์", "จันทร์", "อังคาร", "พุธ", "พฤหัสบดี", "ศุกร์", "เสาร์"];
const TH_MONTHS = ["มกราคม", "กุมภาพันธ์", "มีนาคม", "เมษายน", "พฤษภาคม", "มิถุนายน", "กรกฎาคม", "สิงหาคม", "กันยายน", "ตุลาคม", "พฤศจิกายน", "ธันวาคม"];
const thDate = (d: string) => { const [y, m, dd] = d.split("-").map(Number); const w = new Date(Date.UTC(y, m - 1, dd)).getUTCDay(); return `วัน${TH_DAYS[w]}ที่ ${dd} ${TH_MONTHS[m - 1]} ${y + 543}`; };
const thStamp = (iso: string) => new Date(iso).toLocaleString("th-TH", { timeZone: "Asia/Bangkok", dateStyle: "medium", timeStyle: "short" });
const METHOD: Record<string, string> = { transfer: "โอนเงิน / พร้อมเพย์", cash: "เงินสด", card: "บัตร", qr: "QR" };

function receiptHtml(b: any) {
  const c = b.cfg || {}, link = `${SITE}#booking/${b.ref}/${b.token}`;
  const row = (k: string, v: string) => `<tr><td style="padding:6px 0;color:#5d675f;width:120px">${k}</td><td style="padding:6px 0;color:#22302a"><b>${v}</b></td></tr>`;
  return `<div style="font-family:Arial,sans-serif;max-width:520px;margin:0 auto;color:#22302a;line-height:1.55">
<p style="font-size:13px;color:#5d675f;margin:0 0 4px">Peaceful Day · Ice Bath + Sauna</p>
<h2 style="font-weight:600;margin:0 0 12px">ชำระเงินเรียบร้อย การจองของคุณยืนยันแล้ว</h2>
<p>สวัสดีคุณ ${esc(b.name)} ขอบคุณที่จองกับเรา นี่คือรายละเอียดการจองและการชำระเงินของคุณ</p>
<table style="border-collapse:collapse;width:100%;margin:8px 0 16px;font-size:15px">
${row("รหัสการจอง", esc(b.ref))}
${row("วัน", esc(thDate(b.day)))}
${row("เวลา", `${esc(b.hm)}–${esc(b.end)} น.`)}
${row("จำนวน", `${esc(b.people)} คน`)}
${row("ยอดที่ชำระ", baht(b.total))}
${row("ชำระด้วย", esc(METHOD[b.method] || b.method || "-"))}
${b.paid_at ? row("ชำระเมื่อ", esc(thStamp(b.paid_at))) : ""}
</table>
<p><a href="${link}" style="display:inline-block;background:#2a3d0c;color:#fff;padding:12px 22px;border-radius:10px;text-decoration:none">ดูหรือเลื่อนการจอง</a></p>
<p style="font-size:14px">ทุกคนในกลุ่มต้องลงชื่อรับทราบความเสี่ยงก่อนใช้บริการ <a href="${SITE}#waiver/${b.ref}/${b.token}">ลงชื่อล่วงหน้าในมือถือ</a> ถึงร้านจะได้เข้าใช้ทันที</p>
<p style="font-size:14px">มาก่อนเวลา 10–15 นาที และเตรียมชุดที่เปียกได้ ถ้ามีโรคประจำตัว แจ้งทีมงานเมื่อมาถึง</p>
<p style="font-size:14px">${esc(c.address_th || "")}${c.map_url ? ` · <a href="${esc(c.map_url)}">เปิดในแผนที่</a>` : ""}<br>สอบถาม โทร ${esc(c.phone || "")}</p>
<p style="font-size:12px;color:#5d675f">อีเมลนี้เป็นหลักฐานการชำระเงิน ไม่ใช่ใบกำกับภาษี</p>
</div>`;
}

// one CSV cell: text a spreadsheet would run as a formula gets a leading apostrophe, then quote if needed
const csvCell = (v: unknown) => {
  let t = v === null || v === undefined ? "" : typeof v === "object" ? JSON.stringify(v) : String(v);
  if (/^[=+\-@\t\r]/.test(t) && !/^-?\d+(\.\d+)?$/.test(t)) t = "'" + t;
  return /[",\n\r]/.test(t) ? `"${t.replace(/"/g, '""')}"` : t;
};
function toCsv(rows: Record<string, unknown>[]) {
  if (!rows.length) return "﻿";
  const cols = Object.keys(rows[0]);
  return "﻿" + [cols.join(",")].concat(rows.map((r) => cols.map((k) => csvCell(r[k])).join(","))).join("\r\n");
}
const b64 = (s: string) => { const bytes = new TextEncoder().encode(s); let bin = ""; for (let i = 0; i < bytes.length; i += 0x8000) bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000)); return btoa(bin); };

Deno.serve(async (req) => {
  if (req.method !== "POST") return out({ ok: false }, 405);
  if (!RESEND_KEY) return out({ ok: false, why: "no RESEND_KEY" });
  try {
    const p = await req.json().catch(() => ({}));
    if (p.ref) {
      const b = await rpc("mail_receipt_get", { p_ref: String(p.ref).slice(0, 20) });
      if (!b) return out({ ok: true, sent: false });
      await send({ to: [b.email], subject: `ยืนยันการจอง ${b.ref} · Peaceful Day`, html: receiptHtml(b) });
      await rpc("mail_receipt_done", { p_ref: b.ref });
      return out({ ok: true, sent: true });
    }
    if (p.backup) {
      const d = await rpc("backup_data", { p_token: String(p.backup) });
      if (!d || !d.owners.length) return out({ ok: false }, 403);
      const day = new Date().toLocaleDateString("en-CA", { timeZone: "Asia/Bangkok" });
      await send({
        to: d.owners,
        subject: `สำรองข้อมูล Peaceful Day ${day} (สมาชิก ${d.customers.length} · การจอง ${d.bookings.length})`,
        html: `<p>ไฟล์สำรองข้อมูลประจำสัปดาห์ของ Peaceful Day ประจำวันที่ ${day}</p><ul><li>สมาชิก ${d.customers.length} คน</li><li>การจองทั้งหมด ${d.bookings.length} รายการ</li><li>สลิป ${d.slips.length} ใบ (ยอดและเลขอ้างอิง ไม่รวมรูป)</li><li>ลงชื่อรับทราบความเสี่ยง ${(d.waivers || []).length} รายการ (ไม่รวมรูปลายเซ็น)</li></ul><p>เปิดไฟล์ .csv ด้วย Google Sheets หรือ Excel ได้ เก็บอีเมลนี้ไว้ ถ้าวันไหนข้อมูลในระบบเสีย ใช้ไฟล์เหล่านี้กู้คืนได้</p>`,
        attachments: [
          { filename: `peacefulday-members-${day}.csv`, content: b64(toCsv(d.customers)) },
          { filename: `peacefulday-bookings-${day}.csv`, content: b64(toCsv(d.bookings)) },
          { filename: `peacefulday-slips-${day}.csv`, content: b64(toCsv(d.slips)) },
          { filename: `peacefulday-waivers-${day}.csv`, content: b64(toCsv(d.waivers || [])) },
        ],
      });
      return out({ ok: true });
    }
    return out({ ok: false }, 400);
  } catch (e) {
    console.error(String(e));
    return out({ ok: false }, 500);
  }
});
