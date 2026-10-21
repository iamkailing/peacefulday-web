// Peaceful Day — automatic transfer-slip check (Supabase Edge Function "pfd-slip").
// The website calls this right after a customer attaches a slip (submit_slip). It sends the slip to EasySlip,
// checks it against the booking and the shop's account, and records the result through two database functions.
// Secrets (Edge Functions → Secrets): SLIP_KEY = EasySlip API key. SLIP_URL is optional (another provider with the same shape).
// "Verify JWT" must be off for this function: the website uses a publishable key, and the booking's own token is checked instead.

const SB_URL = Deno.env.get("SUPABASE_URL") || "";
const SB_KEY = Deno.env.get("SUPABASE_SERVICE_ROLE_KEY") || "";
const SLIP_KEY = Deno.env.get("SLIP_KEY") || "";
const SLIP_URL = Deno.env.get("SLIP_URL") || "https://api.easyslip.com/v2/verify/bank";
const CORS = { "Access-Control-Allow-Origin": "*", "Access-Control-Allow-Headers": "authorization, x-client-info, apikey, content-type", "Access-Control-Allow-Methods": "POST, OPTIONS" };
const out = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { ...CORS, "Content-Type": "application/json" } });

async function rpc(fn: string, args: Record<string, unknown>) {
  const r = await fetch(`${SB_URL}/rest/v1/rpc/${fn}`, { method: "POST", headers: { apikey: SB_KEY, Authorization: `Bearer ${SB_KEY}`, "Content-Type": "application/json" }, body: JSON.stringify(args) });
  const t = await r.text();
  if (!r.ok) throw new Error(t.slice(0, 200));
  return t ? JSON.parse(t) : null;
}

type Slip = { err?: string; wait?: boolean; ref?: string; amount?: number; date?: string; name?: string; acct?: string };
// EasySlip API v2 (document.easyslip.com/en/v2/verify/bank/base64): POST { base64 } → data.rawSlip
async function verify(b64: string): Promise<Slip> {
  const ctl = new AbortController(), timer = setTimeout(() => ctl.abort(), 9000);
  try {
    const r = await fetch(SLIP_URL, { method: "POST", headers: { Authorization: `Bearer ${SLIP_KEY}`, "Content-Type": "application/json" }, body: JSON.stringify({ base64: b64 }), signal: ctl.signal });
    const j = await r.json().catch(() => ({}));
    if (!r.ok || !j || j.success === false || !j.data) {
      const e = (j && j.error) || {}, code = String(e.code || j.message || "HTTP " + r.status);
      if (code === "SLIP_PENDING") return { err: "สลิปธนาคารกรุงเทพยังตรวจไม่ได้ในช่วง 5 นาทีแรกหลังโอน", wait: true };
      if (/NOT_FOUND|INVALID|QR/i.test(code) && r.status < 500) return { err: "อ่าน QR ในสลิปไม่ได้ ใช้รูปสลิปจากแอปธนาคารที่เห็น QR ชัด" };
      return { err: "ระบบตรวจสลิปตอบว่า " + String(e.message || code).slice(0, 80), wait: true };
    }
    const d = j.data, raw = d.rawSlip || d, rc = raw.receiver || {}, acc = rc.account || {};
    const amt = raw.amount && typeof raw.amount === "object" ? raw.amount.amount : (raw.amount ?? d.amountInSlip);
    const names = [acc.name && acc.name.th, acc.name && acc.name.en, d.matchedAccount && d.matchedAccount.nameTh, d.matchedAccount && d.matchedAccount.nameEn].filter(Boolean);
    const accts = [acc.bank && acc.bank.account, acc.proxy && acc.proxy.account, d.matchedAccount && d.matchedAccount.bankNumber].filter((x) => typeof x === "string");
    return { ref: String(raw.transRef || raw.reference || ""), amount: Number(amt), date: String(raw.date || ""), name: names.join(" | "), acct: accts.join(" | ") };
  } catch (_e) {
    return { err: "ระบบตรวจสลิปไม่ตอบ", wait: true };
  } finally { clearTimeout(timer); }
}

const normName = (s: string) => String(s || "").toLowerCase()
  .replace(/(นางสาว|นาง|นาย|น\.ส\.|บจก\.?|บริษัท|จำกัด|mr\.?|mrs\.?|ms\.?|miss|co\.?,?\s*ltd\.?|company|limited)/g, "")
  .replace(/[^a-z0-9ก-๙]/g, "");

// "" = the slip pays for this booking; otherwise the reason in plain Thai
function problem(v: Slip, b: Record<string, any>): string {
  if (!v.ref) return "ไม่พบเลขอ้างอิงในสลิป";
  if (!isFinite(v.amount!)) return "อ่านยอดเงินในสลิปไม่ได้";
  if (v.amount! < Number(b.total)) return `ยอดในสลิป ${v.amount} บาท น้อยกว่ายอดที่ต้องชำระ ${b.total} บาท`;
  const want = normName(b.holder);
  const nameOk = want.length >= 3 && (v.name || "").split("|").some((n) => { const g = normName(n); return g.length >= 3 && (g.includes(want) || want.includes(g)); });
  const mine = [b.account, b.promptpay].map((x: string) => String(x || "").replace(/\D/g, "")).filter((x: string) => x.length >= 4);
  const shown = (v.acct || "").split("|").map((x) => x.replace(/\D/g, "")).filter((x) => x.length >= 4);
  const acctOk = shown.some((x) => mine.some((m: string) => m.includes(x)));
  if (!nameOk && !acctOk) return "บัญชีผู้รับในสลิปไม่ตรงกับบัญชีร้าน";
  const t = Date.parse(v.date || "");
  if (!isFinite(t)) return "อ่านเวลาโอนในสลิปไม่ได้";
  if (t < Date.parse(b.created_at) - 30 * 60e3) return "สลิปนี้โอนก่อนการจอง กรุณาใช้สลิปของการโอนครั้งนี้";
  if (t - Date.now() > 10 * 60e3) return "เวลาในสลิปไม่ถูกต้อง";
  return "";
}

Deno.serve(async (req) => {
  if (req.method === "OPTIONS") return new Response("ok", { headers: CORS });
  try {
    const p = await req.json().catch(() => ({}));
    const ref = String(p.ref || "").slice(0, 20), token = String(p.token || "");
    if (!/^[0-9a-f-]{36}$/i.test(token)) return out({ status: "manual" }, 400);
    const b = await rpc("slip_check_get", { p_ref: ref, p_token: token });
    if (!b) return out({ status: "manual" }, 404);
    if (b.pay !== "review" || b.status !== "confirmed") return out({ status: b.pay });
    if (!SLIP_KEY || !b.auto) return out({ status: "manual" });
    const m = /^data:image\/[a-z]+;base64,(.+)$/.exec(String(b.data || ""));
    if (!m) return out({ status: "manual" });
    const v = await verify(String(b.data)); // EasySlip takes the data URI as is (the prefix helps it detect the image type)
    let result = "ok", note = "";
    if (v.err) { result = v.wait ? "manual" : "bad"; note = v.err; }
    else { note = problem(v, b); if (note) result = "bad"; else if (v.amount! > Number(b.total)) note = `โอนเกิน ${v.amount! - Number(b.total)} บาท`; }
    const res = await rpc("slip_check_set", { p_slip_id: b.slip_id, p_result: result, p_ref: v.ref || null, p_amount: isFinite(v.amount!) ? v.amount : null, p_note: note });
    return out(res);
  } catch (_e) {
    return out({ status: "manual" });
  }
});
