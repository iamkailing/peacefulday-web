// Peaceful Day — automatic transfer-slip check (Supabase Edge Function "pfd-slip").
// The database calls this for every new slip (trigger on the slips table). It sends the newest unchecked slip to EasySlip,
// checks it against the booking and the shop's account, and records the result once through slip_check_set.
// A slip is paid automatically only when the receiver's name AND account clearly match; anything unclear goes to the team.
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

type Slip = { err?: string; wait?: boolean; ref?: string; amount?: number; date?: string; names?: string[]; accts?: string[]; matched?: boolean };
// EasySlip API v2 (document.easyslip.com/en/v2/verify/bank/base64): POST { base64 } → data.rawSlip
async function verify(b64: string): Promise<Slip> {
  const ctl = new AbortController(), timer = setTimeout(() => ctl.abort(), 9000);
  try {
    const r = await fetch(SLIP_URL, { method: "POST", headers: { Authorization: `Bearer ${SLIP_KEY}`, "Content-Type": "application/json" }, body: JSON.stringify({ base64: b64, matchAccount: true }), signal: ctl.signal });
    const j = await r.json().catch(() => ({}));
    if (!r.ok || !j || j.success === false || !j.data) {
      const e = (j && j.error) || {}, code = String(e.code || j.message || "HTTP " + r.status);
      if (code === "SLIP_PENDING") return { err: "สลิปธนาคารกรุงเทพยังตรวจไม่ได้ในช่วง 5 นาทีแรกหลังโอน", wait: true };
      if (/NOT_FOUND|INVALID|QR/i.test(code) && r.status < 500) return { err: "อ่าน QR ในสลิปไม่ได้ ใช้รูปสลิปจากแอปธนาคารที่เห็น QR ชัด" };
      return { err: "ระบบตรวจสลิปตอบว่า " + String(e.message || code).slice(0, 80), wait: true };
    }
    const d = j.data, raw = d.rawSlip || d, rc = raw.receiver || {}, acc = rc.account || {};
    const amt = raw.amount && typeof raw.amount === "object" ? raw.amount.amount : (raw.amount ?? d.amountInSlip);
    // only what the slip itself says about the receiver (not the accounts EasySlip compared it with)
    const names = [acc.name && acc.name.th, acc.name && acc.name.en, acc.displayName, rc.displayName, rc.name].filter((x) => typeof x === "string" && x);
    const accts = [acc.bank && acc.bank.account, acc.proxy && acc.proxy.account, acc.account, acc.value].filter((x) => typeof x === "string" && x);
    const matched = !!(d.matchedAccount && typeof d.matchedAccount === "object" && Object.keys(d.matchedAccount).length);
    return { ref: String(raw.transRef || raw.reference || ""), amount: Number(amt), date: String(raw.date || ""), names, accts, matched };
  } catch (_e) {
    return { err: "ระบบตรวจสลิปไม่ตอบ", wait: true };
  } finally { clearTimeout(timer); }
}

const normName = (s: string) => String(s || "").toLowerCase()
  .replace(/(นางสาว|นาง|นาย|น\.ส\.|บจก\.?|บริษัท|จำกัด|mr\.?|mrs\.?|ms\.?|miss|co\.?,?\s*ltd\.?|company|limited)/g, "")
  .replace(/[^a-z0-9ก-๙]/g, "");

// the receiver name on the slip is the shop's account name (slips often cut long names short, so a long enough start counts)
function nameMatches(slipNames: string[], holder: string): boolean {
  const wants = String(holder || "").split(/[|/,]/).map(normName).filter((x) => x.length >= 3);
  return slipNames.some((n) => { const g = normName(n); return g.length >= 3 && wants.some((w) => g === w || (g.length >= 6 && w.startsWith(g)) || (w.length >= 6 && g.startsWith(w))); });
}
// a masked account number ("xxx-x-x5899-x") agrees with the shop's number digit by digit, in the same positions
function accountMatches(shown: string, full: string): boolean {
  const s = String(shown || "").toLowerCase().replace(/[*•]/g, "x").replace(/[^0-9x]/g, ""), f = String(full || "").replace(/\D/g, "");
  if (!f || s.length !== f.length || s.replace(/x/g, "").length < 4) return false;
  for (let i = 0; i < s.length; i++) if (s[i] !== "x" && s[i] !== f[i]) return false;
  return true;
}

// ok = pays this booking; bad = clearly wrong (the customer is told why and can send another); manual = the team decides
function judge(v: Slip, b: Record<string, any>): { result: "ok" | "bad" | "manual"; note: string } {
  if (!v.ref) return { result: "bad", note: "ไม่พบเลขอ้างอิงในสลิป ใช้รูปสลิปจากแอปธนาคาร" };
  if (!isFinite(v.amount!)) return { result: "manual", note: "อ่านยอดเงินในสลิปไม่ได้" };
  if (v.amount! < Number(b.total)) return { result: "bad", note: `ยอดในสลิป ${v.amount} บาท น้อยกว่ายอดที่ต้องชำระ ${b.total} บาท` };
  const t = Date.parse(v.date || "");
  if (!isFinite(t)) return { result: "manual", note: "อ่านเวลาโอนในสลิปไม่ได้" };
  if (t < Date.parse(b.created_at) - 30 * 60e3) return { result: "bad", note: "สลิปนี้โอนก่อนการจอง กรุณาใช้สลิปของการโอนครั้งนี้" };
  if (t - Date.now() > 10 * 60e3) return { result: "bad", note: "เวลาในสลิปไม่ถูกต้อง" };
  const mine = [b.account, b.promptpay].map((x: string) => String(x || "").replace(/\D/g, "")).filter((x: string) => x.length >= 6);
  const nameOk = nameMatches(v.names || [], b.holder);
  const acctOk = (v.accts || []).some((a) => mine.some((m: string) => accountMatches(a, m)));
  const acctShown = (v.accts || []).some((a) => a.replace(/\D/g, "").length >= 4);
  if (nameOk && (acctOk || v.matched)) return { result: "ok", note: v.amount! > Number(b.total) ? `โอนเกิน ${v.amount! - Number(b.total)} บาท` : "" };
  if (!nameOk && acctShown && !acctOk && !v.matched && (v.names || []).length) return { result: "bad", note: "บัญชีผู้รับในสลิปไม่ตรงกับบัญชีร้าน" };
  return { result: "manual", note: "ตรวจบัญชีผู้รับอัตโนมัติไม่ได้ชัดเจน ทีมงานจะตรวจให้" + ((v.names || []).length ? ` (ผู้รับในสลิป: ${(v.names || []).join(" / ").slice(0, 80)})` : "") };
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
    const { result, note } = v.err ? { result: v.wait ? "manual" : "bad", note: v.err } : judge(v, b);
    const res = await rpc("slip_check_set", { p_slip_id: b.slip_id, p_result: result, p_ref: v.ref || null, p_amount: isFinite(v.amount!) ? v.amount : null, p_note: note });
    return out(res);
  } catch (_e) {
    return out({ status: "manual" });
  }
});
