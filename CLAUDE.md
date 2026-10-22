# Peaceful Day booking site — notes for AI assistants

Read this before changing anything. The owner (หลิน / มะเด้ง) is not a developer: explain in Thai, plainly, and do the technical work yourself.

## What this is
Booking website for Peaceful Day (Ice Bath + Sauna, Sukhumvit 62, Bangkok) at https://peacefulday.co.
- **Front end:** static single-page app on GitHub Pages (this repo). `index.html`, `config.js`, `assets/app.js` (all screens, hash routes like `#book`, `#queue`), `assets/site.css`, `assets/vendor/qrcode.js`.
- **Back end:** Supabase project `tisybvfjejtdjtykowcb`. Every rule lives in `supabase/schema.sql` (tables locked with RLS; the site only calls SECURITY DEFINER functions). The browser never decides prices, capacity or permissions.
- **Edge functions:** `supabase/functions/pfd-slip` (automatic transfer-slip check via EasySlip) and `pfd-mail` (receipt emails and the weekly backup via Resend, from hello@peacefulday.co). Both have "Verify JWT" off.
- **Secrets** (Supabase → Edge Functions → Secrets, never in this repo): `SLIP_KEY` (EasySlip), `RESEND_KEY` (Resend). The owner pastes keys herself.

## How it works
- Customers sign up (email + password, no email confirmation), fill a member profile (first + last name required), book, then pay by PromptPay/transfer within `hold_min` (15) minutes and attach a slip. Unpaid holds expire and free the places.
- Pay states: `due` (pay at shop), `await` (waiting for transfer), `review` (slip sent), `expired`, `paid`, `none`. Statuses: `confirmed`, `checked_in`, `cancelled`, `no_show`, `waiting`.
- Slips: a DB trigger calls pfd-slip; it auto-pays only when receiver name AND account match; unclear → staff review; transient errors retried by the `pfd-slip-recheck` cron. One bank reference / image can pay one booking only.
- Everyone who uses the facilities registers and signs (waivers table) via `#waiver/REF/WTOKEN`, capped at the booking's head count; staff check in each person; visits are counted per person.
- Staff roles: `staff` (queue page only), `manager` (+ settings, closing sessions, customers), `owner` (+ payment details, team invites). Staff join with an invite code.
- "Register on this device" locks the staff device to the registration page; hold "ทีมงาน" for 2 seconds to return.
- Paid bookings can't be cancelled online: customers phone 096-901-4999 or LINE @peacefulday. The refund policy is not decided yet.
- Cron jobs: `pfd-weekly-backup` (Mon 08:00 BKK email to owners), `pfd-receipt-retry` (15 min), `pfd-slip-recheck` (5 min).

## Changing things
- **Database:** edit `supabase/schema.sql`; it must stay safe to run again on the live database (add columns with `if not exists`, never drop data). Run the whole file in Supabase → SQL Editor. Grants are at the end: new functions must be granted explicitly (everything is revoked first).
- **Site:** after editing `assets/*`, bump `?v=N` in `index.html` (cache busting), commit and push to `main`; GitHub Pages deploys in about a minute.
- **Edge functions:** paste the file into the Supabase function editor and deploy.
- UI text is Thai first (English via the language toggle). Errors map through the `ERR` table in app.js.
- Keep the Content-Security-Policy in `index.html` in sync if you add outside scripts or connections (Turnstile from challenges.cloudflare.com is allowed).

## Rules from the owner
- Keep all customer data (unlink rather than delete; backups go by email).
- No customer data, keys or tokens in this public repo.
- No invented reviews, claims or promotions.
- Ask before anything that can't be undone or touches money/payment settings.

## Open questions
- Sessions start hourly but last 2 hours, so consecutive sessions overlap (up to 2× capacity on site). Confirm with the owner whether that's intended.
- Refund policy for paid cancellations.
