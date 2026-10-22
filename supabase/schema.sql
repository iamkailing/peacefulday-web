-- Peaceful Day booking system on Supabase.
-- Tables are locked (row level security on, no policies): the website can only call the functions below,
-- and every rule (capacity, duplicates, cut-off times, staff roles) is checked here, not in the browser.
-- Safe to run more than once.

create extension if not exists pgcrypto;

-- ---------- tables ----------
create table if not exists settings (
  id int primary key default 1 check (id = 1),
  data jsonb not null
);
insert into settings (id, data) values (1, jsonb_build_object(
  'price', 590,                  -- baht per person per session (Ice Bath + Sauna)
  'pair_price', 1100,            -- promotion: every 2 people pay this together (0 turns it off)
  'capacity', 5,                 -- people per session on weekdays
  'capacity_weekend', 8,         -- people per session on Saturdays, Sundays and public holidays
  'duration', 120,               -- minutes a guest can stay (a new session starts every hour, so two sessions overlap)
  'times', jsonb_build_array('09:00','10:00','11:00','12:00','13:00','14:00','15:00','16:00','17:00','18:00'),
  'closed', jsonb_build_array(4), -- days closed every week: 0 Sunday … 6 Saturday (4 = Thursday)
  'lead_min', 30,                -- online bookings close this many minutes before a session
  'horizon_days', 45,            -- how far ahead people can book online
  'cancel_hours', 24,            -- customers can cancel or move up to this many hours before
  'max_moves', 1,
  'max_group', 8,                -- most people in one online booking (weekdays still stop at the weekday capacity)
  'phone', '096-901-4999',
  'line', '',
  'address_th', 'สุขุมวิท 62 กรุงเทพฯ (BTS บางจาก ทางออก 2)',
  'address_en', 'Sukhumvit 62, Bangkok (BTS Bang Chak, Exit 2)',
  'map_url', 'https://maps.app.goo.gl/NJ1KqAuGzCA9HH1v5',
  'pay_mode', 'transfer',        -- transfer: customers pay by PromptPay / bank transfer and attach a slip when booking
  'hold_min', 15,                -- minutes an unpaid online booking keeps its places before they are released
  'promptpay', '',               -- PromptPay number (mobile or tax ID) for the payment QR; the owner fills it in under settings
  'bank_name', '', 'bank_account', '', 'bank_holder', ''
)) on conflict (id) do nothing;
-- add settings that came later to a database created before them (keeps any value already set)
update settings set data = jsonb_build_object('pair_price', 1100) || data where id = 1 and not data ? 'pair_price';
update settings set data = jsonb_build_object('slip_auto', true) || data where id = 1 and not data ? 'slip_auto';
update settings set data = jsonb_build_object('hold_min', 15, 'promptpay', '', 'bank_name', '', 'bank_account', '', 'bank_holder', '') || data where id = 1 and not data ? 'hold_min';
-- online bookings are paid before the visit now, not at the shop
update settings set data = data || '{"pay_mode":"transfer"}'::jsonb where id = 1 and data->>'pay_mode' = 'venue';

create table if not exists slot_overrides (
  day date not null,
  hm text not null,
  blocked boolean not null default false,
  capacity int,
  primary key (day, hm)
);

create table if not exists closed_days (
  day date primary key,
  note text not null default ''
);

-- public holidays use the weekend capacity
create table if not exists holidays (
  day date primary key,
  name text not null default ''
);
insert into holidays (day, name) values
  ('2026-10-23','วันปิยมหาราช'), ('2026-12-05','วันพ่อแห่งชาติ'), ('2026-12-10','วันรัฐธรรมนูญ'), ('2026-12-31','วันสิ้นปี'),
  ('2027-01-01','วันขึ้นปีใหม่'), ('2027-04-06','วันจักรี'), ('2027-04-13','วันสงกรานต์'), ('2027-04-14','วันสงกรานต์'),
  ('2027-04-15','วันสงกรานต์'), ('2027-05-01','วันแรงงาน'), ('2027-05-04','วันฉัตรมงคล'), ('2027-06-03','วันเฉลิมพระชนมพรรษาพระราชินี'),
  ('2027-07-28','วันเฉลิมพระชนมพรรษา ร.10'), ('2027-08-12','วันแม่แห่งชาติ'), ('2027-10-13','วันนวมินทรมหาราช'),
  ('2027-10-23','วันปิยมหาราช'), ('2027-12-05','วันพ่อแห่งชาติ'), ('2027-12-10','วันรัฐธรรมนูญ'), ('2027-12-31','วันสิ้นปี')
on conflict (day) do nothing;

create table if not exists bookings (
  id bigserial primary key,
  ref text not null unique,
  token uuid not null default gen_random_uuid(),
  day date not null,
  hm text not null,
  people int not null check (people between 1 and 50),
  name text not null,
  phone text not null default '',
  email text not null default '',
  note text not null default '',
  status text not null default 'confirmed' check (status in ('confirmed','checked_in','cancelled','no_show')),
  pay text not null default 'due' check (pay in ('due','paid','none')),
  method text not null default '',
  total int not null default 0,          -- baht
  channel text not null default 'web' check (channel in ('web','walk_in','phone','line')),
  moves int not null default 0,
  consent boolean not null default false,
  staff_note text not null default '',
  created_at timestamptz not null default now(),
  paid_at timestamptz,
  checked_at timestamptz,
  cancelled_at timestamptz
);
alter table bookings add column if not exists user_id uuid;
alter table bookings add column if not exists package text not null default '';  -- coupon package used (Happy Duo, Member 1 …)
-- 'waiting' = the customer will pick a new day later; it holds no place in any session
alter table bookings drop constraint if exists bookings_status_check;
alter table bookings add constraint bookings_status_check check (status in ('confirmed','checked_in','cancelled','no_show','waiting'));
-- pay: due = pay at the shop (team bookings only), await = waiting for the customer's transfer (held until hold_until),
--      review = slip sent, the team checks it, expired = not paid in time (the booking was released), paid, none
alter table bookings add column if not exists hold_until timestamptz;
alter table bookings add column if not exists slip_at timestamptz;
alter table bookings add column if not exists receipt_sent_at timestamptz;  -- payment confirmation email sent
alter table bookings add column if not exists pay_before text not null default '';  -- payment state just before a cancel, so the team can undo it
-- a second private key used only for the registration/waiver link, which the booker shares with their group
-- (the main token also opens the booking page, where it can be moved or cancelled)
alter table bookings add column if not exists wtoken uuid not null default gen_random_uuid();
alter table bookings drop constraint if exists bookings_pay_check;
alter table bookings add constraint bookings_pay_check check (pay in ('due','await','review','expired','paid','none'));
create index if not exists bookings_slot on bookings (day, hm);
create index if not exists bookings_await on bookings (hold_until) where pay = 'await';
create index if not exists bookings_user on bookings (user_id);
create index if not exists bookings_phone on bookings (phone);
create index if not exists bookings_created on bookings (created_at);

-- member profile, filled in once after signing up (one row per login)
create table if not exists customers (
  user_id uuid primary key,
  email text not null default '',
  phone text not null default '',
  first_name text not null default '',
  last_name text not null default '',
  birth_date date,
  gender text not null default '' check (gender in ('', 'male', 'female', 'unspecified')),
  marketing_consent boolean not null default false,
  consent_at timestamptz,
  terms_at timestamptz,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);
-- sign-ups are not email-confirmed; a member can confirm their email later (optional) by opening a sign-in link we email them
alter table customers add column if not exists email_verified_at timestamptz;
alter table customers enable row level security;
revoke all on customers from anon, authenticated;

-- transfer slips customers attach (a small JPEG, stored as a data URL)
create table if not exists slips (
  id bigserial primary key,
  booking_id bigint not null references bookings(id) on delete cascade,
  data text not null,
  created_at timestamptz not null default now()
);
create index if not exists slips_booking on slips (booking_id);
-- filled in by the automatic slip check (EasySlip) later; one bank reference can only pay for one booking
alter table slips add column if not exists slip_ref text;
alter table slips add column if not exists amount numeric;
alter table slips add column if not exists result text not null default '';
-- every slip is checked once on the server; a bank reference or the same image can pay for one booking only
alter table slips add column if not exists img_hash text not null default '';
alter table slips add column if not exists checked_at timestamptz;
alter table slips add column if not exists used boolean not null default false;   -- this slip paid its booking
alter table slips add column if not exists claimed_at timestamptz;  -- a check is running now (two checks of one slip never run at once)
alter table slips add column if not exists tries int not null default 0;
alter table slips add column if not exists retry_at timestamptz;    -- the checker was busy or the bank needed a few minutes: look again then
drop index if exists slips_ref;
create index if not exists slips_ref on slips (slip_ref) where slip_ref is not null;
create index if not exists slips_hash on slips (img_hash);
alter table slips enable row level security;
revoke all on slips from anon, authenticated;

-- values only the server uses (never sent to the website)
create table if not exists app_private (
  id int primary key default 1 check (id = 1),
  backup_token text not null default encode(gen_random_bytes(24), 'hex')
);
insert into app_private (id) values (1) on conflict (id) do nothing;
alter table app_private enable row level security;
revoke all on app_private from anon, authenticated;

-- signed risk acknowledgments (one per person; a group booking has several)
create table if not exists waivers (
  id bigserial primary key,
  booking_id bigint references bookings(id) on delete set null,
  name text not null,
  services text not null default 'both' check (services in ('cold','sauna','both')),
  signature text not null,           -- PNG data URL of the drawn signature
  version text not null default 'v1',
  lang text not null default 'th',
  signed_at timestamptz not null default now()
);
create index if not exists waivers_booking on waivers (booking_id);
create index if not exists waivers_signed on waivers (signed_at);
alter table waivers add column if not exists src text not null default '';  -- hashed network address, only for rate limits
-- every person registers (members and the friends who come with them) and the team checks each one in
alter table waivers add column if not exists first_name text not null default '';
alter table waivers add column if not exists last_name text not null default '';
alter table waivers add column if not exists email text not null default '';
alter table waivers add column if not exists phone text not null default '';
alter table waivers add column if not exists came_with text not null default '';   -- team-only note: "มากับ คุณเอ (PD-XXXXXX)"
alter table waivers add column if not exists marketing_consent boolean not null default false;
alter table waivers add column if not exists checked_at timestamptz;              -- the team confirmed this person arrived
alter table waivers add column if not exists checked_by text not null default '';
-- registered on a booking by mistake and taken off it by the team; the record itself is kept
alter table waivers add column if not exists removed_from bigint;
create index if not exists waivers_email on waivers (lower(email));
create index if not exists waivers_phone on waivers (phone);
alter table waivers enable row level security;
revoke all on waivers from anon, authenticated;

create table if not exists staff (
  user_id uuid primary key,
  email text not null,
  name text not null default '',
  role text not null default 'staff' check (role in ('owner','manager','staff')),
  created_at timestamptz not null default now()
);

create table if not exists staff_invites (
  email text primary key,
  role text not null default 'staff' check (role in ('owner','manager','staff')),
  name text not null default ''
);
-- sign-ups are not email-confirmed, so an invite is claimed only with the 6-digit code the owner passes on
alter table staff_invites add column if not exists code text not null default lpad((floor(random() * 1000000))::int::text, 6, '0');
alter table staff_invites add column if not exists tries int not null default 0;  -- wrong codes; the invite locks after 5

create table if not exists audit (
  id bigserial primary key,
  at timestamptz not null default now(),
  who text not null default '',
  ref text not null default '',
  action text not null
);
create index if not exists audit_ref on audit (ref);

alter table settings enable row level security;
alter table slot_overrides enable row level security;
alter table closed_days enable row level security;
alter table holidays enable row level security;
alter table bookings enable row level security;
alter table staff enable row level security;
alter table staff_invites enable row level security;
alter table audit enable row level security;
revoke all on settings, slot_overrides, closed_days, holidays, bookings, staff, staff_invites, audit from anon, authenticated;

-- ---------- helpers (not callable from the website) ----------
create or replace function _cfg() returns jsonb language sql stable set search_path = public as $$
  select data from settings where id = 1 $$;

create or replace function _now_bkk() returns timestamp language sql stable as $$
  select (now() at time zone 'Asia/Bangkok') $$;

create or replace function _start(p_day date, p_hm text) returns timestamptz language sql immutable as $$
  select ((p_day + p_hm::time) at time zone 'Asia/Bangkok') $$;

create or replace function _phone(p text) returns text language plpgsql immutable as $$
declare d text := regexp_replace(coalesce(p, ''), '\D', '', 'g');
begin
  if d like '66%' and length(d) = 11 then d := '0' || substr(d, 3); end if;
  return d;
end $$;

-- a phone number from anyone: Thai numbers become 0XXXXXXXXX, other countries keep +country code
create or replace function _any_phone(p text) returns text language plpgsql immutable as $$
declare d text := regexp_replace(coalesce(p, ''), '[^0-9+]', '', 'g');
begin
  if d like '+66%' then d := '0' || substr(d, 4); end if;
  if left(d, 1) <> '+' then
    d := regexp_replace(d, '\D', '', 'g');
    if d like '66%' and length(d) = 11 then d := '0' || substr(d, 3); end if;
  end if;
  return d;
end $$;

create or replace function _ref() returns text language plpgsql volatile as $$
declare a text := 'ABCDEFGHJKLMNPQRSTUVWXYZ23456789'; r text;
begin
  loop
    r := 'PD-';
    for i in 1..6 loop r := r || substr(a, 1 + floor(random() * 32)::int, 1); end loop;
    exit when not exists (select 1 from bookings where ref = r);
  end loop;
  return r;
end $$;

-- the sessions of one day with how many seats are taken
create or replace function _slots(p_day date)
returns table (hm text, capacity int, blocked boolean, used int)
language plpgsql stable set search_path = public as $$
declare c jsonb := _cfg(); cap int;
begin
  if exists (select 1 from closed_days where day = p_day) then return; end if;
  cap := case when extract(dow from p_day) in (0, 6) or exists (select 1 from holidays h where h.day = p_day)
              then coalesce((c->>'capacity_weekend')::int, (c->>'capacity')::int) else (c->>'capacity')::int end;
  return query
  with base as (
    select t.value as hm from jsonb_array_elements_text(c->'times') t
    where not (c->'closed') @> to_jsonb(extract(dow from p_day)::int)
    union
    select o.hm from slot_overrides o where o.day = p_day and o.capacity is not null and not o.blocked
  )
  select b.hm,
         coalesce(o.capacity, cap),
         coalesce(o.blocked, false),
         coalesce((select sum(k.people)::int from bookings k
                   where k.day = p_day and k.hm = b.hm and k.status in ('confirmed','checked_in','no_show')
                     and not (k.status = 'confirmed' and k.pay = 'await' and k.hold_until < now())), 0)
  from base b left join slot_overrides o on o.day = p_day and o.hm = b.hm
  order by b.hm;
end $$;

create or replace function _log(p_ref text, p_action text) returns void language sql set search_path = public as $$
  insert into audit (who, ref, action)
  values (coalesce((select email from staff where user_id = auth.uid()), 'ลูกค้า'), coalesce(p_ref, ''), p_action) $$;

-- release online bookings that were not paid in time
create or replace function _expire_holds() returns void language plpgsql volatile set search_path = public as $$
declare r record;
begin
  for r in update bookings set status = 'cancelled', pay_before = pay, pay = 'expired', cancelled_at = now()
            where pay = 'await' and hold_until < now() and status = 'confirmed' returning ref loop
    insert into audit (who, ref, action) values ('ระบบ', r.ref, 'หมดเวลาชำระ ปล่อยที่นั่งคืน');
  end loop;
end $$;

create or replace function _role() returns text language sql stable set search_path = public as $$
  select role from staff where user_id = auth.uid() $$;

create or replace function _need(p_min text) returns text language plpgsql stable set search_path = public as $$
declare r text := _role();
begin
  if r is null then raise exception 'not_staff'; end if;
  if p_min = 'owner' and r <> 'owner' then raise exception 'forbidden'; end if;
  if p_min = 'manager' and r not in ('owner','manager') then raise exception 'forbidden'; end if;
  return r;
end $$;

create or replace function _booking_json(b bookings) returns jsonb language plpgsql stable set search_path = public as $$
declare c jsonb := _cfg(); st timestamptz := _start(b.day, b.hm); cut timestamptz := st - make_interval(hours => (c->>'cancel_hours')::int);
begin
  return jsonb_build_object(
    'ref', b.ref, 'day', b.day, 'hm', b.hm,
    'end', to_char(b.hm::time + make_interval(mins => (c->>'duration')::int), 'HH24:MI'),
    'people', b.people, 'name', b.name, 'phone', b.phone, 'email', b.email, 'note', b.note,
    'status', b.status, 'pay', b.pay, 'method', b.method, 'total', b.total, 'channel', b.channel,
    'moves', b.moves, 'created_at', b.created_at, 'checked_at', b.checked_at,
    'hold_until', b.hold_until, 'slip_at', b.slip_at, 'wtoken', b.wtoken,
    'signed', (select count(*) from waivers w where w.booking_id = b.id),
    'slip_note', (select s.result from slips s where s.booking_id = b.id order by s.created_at desc limit 1),
    -- after paying, cancelling goes through the team (refunds are handled by hand)
    'can_cancel', b.status = 'confirmed' and now() < cut and b.pay in ('await','due','none'),
    'can_move', b.status = 'confirmed' and now() < cut and b.moves < (c->>'max_moves')::int,
    'cutoff', cut);
end $$;

-- what the team sees for a booking: the customer view plus the team's own fields
create or replace function _staff_json(b bookings) returns jsonb language sql stable set search_path = public as $$
  select _booking_json(b) || jsonb_build_object('staff_note', b.staff_note, 'package', b.package, 'paid_at', b.paid_at, 'cancelled_at', b.cancelled_at,
    'arrived', (select count(*) from waivers w where w.booking_id = b.id and w.checked_at is not null)) $$;

-- the signed-in customer (email confirmed with a one-time code); anonymous visitors cannot book
create or replace function _customer_email() returns text language plpgsql stable as $$
declare em text := lower(coalesce(auth.jwt()->>'email', ''));
begin
  if auth.uid() is null or em = '' or coalesce((auth.jwt()->>'is_anonymous')::boolean, false) then raise exception 'login'; end if;
  return em;
end $$;

-- ---------- public: anyone visiting the website ----------
-- what a group pays: each pair gets the 2-person price when that promotion is on, anyone left over pays the normal price
create or replace function _total(n int) returns int language sql stable set search_path = public as $$
  select case when coalesce((c->>'pair_price')::int, 0) > 0
              then (n / 2) * (c->>'pair_price')::int + (n % 2) * (c->>'price')::int
              else n * (c->>'price')::int end
  from (select _cfg() as c) x $$;

create or replace function public_config() returns jsonb language sql stable security definer set search_path = public as $$
  select _cfg() || jsonb_build_object('now', now(), 'today', (_now_bkk())::date) $$;

-- days and sessions that still have room for a group of p_people
create or replace function availability(p_from date, p_days int, p_people int)
returns jsonb language plpgsql volatile security definer set search_path = public as $$
declare c jsonb := _cfg(); d date; out jsonb := '[]'::jsonb; day_slots jsonb; earliest timestamptz;
  lastday date := (_now_bkk())::date + (c->>'horizon_days')::int;
begin
  perform _expire_holds();
  p_days := least(greatest(coalesce(p_days, 7), 1), 62);
  earliest := now() + make_interval(mins => (c->>'lead_min')::int);
  for i in 0..p_days - 1 loop
    d := p_from + i;
    if d < (_now_bkk())::date or d > lastday then continue; end if;
    select coalesce(jsonb_agg(jsonb_build_object(
             'hm', s.hm,
             'remaining', greatest(s.capacity - s.used, 0),
             'ok', not s.blocked and _start(d, s.hm) >= earliest and s.capacity - s.used >= greatest(p_people, 1))
           order by s.hm), '[]'::jsonb)
      into day_slots
      from _slots(d) s where not s.blocked and _start(d, s.hm) >= earliest;
    out := out || jsonb_build_array(jsonb_build_object('day', d, 'slots', day_slots));
  end loop;
  return out;
end $$;

create or replace function create_booking(p jsonb) returns jsonb
language plpgsql volatile security definer set search_path = public as $$
declare c jsonb := _cfg(); v_day date; v_hm text; v_people int; v_name text; v_phone text; v_email text;
  s record; b bookings;
begin
  v_day := (p->>'day')::date; v_hm := p->>'hm'; v_people := (p->>'people')::int;
  v_email := _customer_email();
  v_name := btrim(coalesce(p->>'name', '')); v_phone := _phone(p->>'phone');
  if length(v_name) < 1 or length(v_name) > 120 then raise exception 'name'; end if;
  if v_phone !~ '^0[2-9][0-9]{7,8}$' then raise exception 'phone'; end if;
  if (select count(*) from bookings where user_id = auth.uid() and created_at > now() - interval '1 hour') >= 10 then
    raise exception 'too_many'; end if;
  if v_people is null or v_people < 1 or v_people > (c->>'max_group')::int then raise exception 'people'; end if;
  if v_day is null or v_day > (_now_bkk())::date + (c->>'horizon_days')::int then raise exception 'slot_closed'; end if;
  if _start(v_day, v_hm) < now() + make_interval(mins => (c->>'lead_min')::int) then raise exception 'too_soon'; end if;

  perform _expire_holds();
  perform pg_advisory_xact_lock(hashtext('slot:' || v_day || ' ' || v_hm));
  select * into s from _slots(v_day) x where x.hm = v_hm;
  if not found or s.blocked then raise exception 'slot_closed'; end if;
  if exists (select 1 from bookings where day = v_day and hm = v_hm and (phone = v_phone or user_id = auth.uid()) and status in ('confirmed','checked_in')) then
    raise exception 'duplicate'; end if;
  -- bookings are paid up front now, so there is no cap on paid bookings; only unpaid holds are limited per member
  if (select count(*) from bookings where user_id = auth.uid() and status = 'confirmed'
        and (pay = 'review' or (pay = 'await' and hold_until > now()))) >= 2 then
    raise exception 'unpaid_many'; end if;
  if s.capacity - s.used < v_people then raise exception 'slot_full'; end if;

  insert into bookings (ref, day, hm, people, name, phone, email, note, total, channel, consent, user_id, pay, hold_until)
  values (_ref(), v_day, v_hm, v_people, v_name, v_phone, v_email, left(coalesce(p->>'note', ''), 500),
          _total(v_people), 'web', coalesce((p->>'consent')::boolean, false), auth.uid(),
          -- until the shop has filled in where to transfer, bookings stay pay-at-the-shop so none are released by mistake
          case when coalesce(c->>'promptpay', '') <> '' or coalesce(c->>'bank_account', '') <> '' then 'await' else 'due' end,
          case when coalesce(c->>'promptpay', '') <> '' or coalesce(c->>'bank_account', '') <> '' then now() + make_interval(mins => greatest(coalesce((c->>'hold_min')::int, 15), 5)) end)
  returning * into b;
  perform _log(b.ref, 'ลูกค้าจองทางเว็บ ' || v_people || ' คน' || case when b.pay = 'await' then ' รอโอน' else '' end);
  return jsonb_build_object('ref', b.ref, 'token', b.token);
end $$;

-- the signed-in customer's bookings, newest first, with the last name and phone they used
create or replace function my_bookings() returns jsonb
language plpgsql volatile security definer set search_path = public as $$
declare em text := _customer_email();
begin
  perform _expire_holds();
  return jsonb_build_object(
    'email', em,
    'last', coalesce((select jsonb_build_object('name', btrim(first_name || ' ' || last_name), 'phone', phone) from customers where user_id = auth.uid()),
                     (select jsonb_build_object('name', name, 'phone', phone) from bookings
                      where user_id = auth.uid() order by created_at desc limit 1)),
    'bookings', coalesce((select jsonb_agg(_booking_json(b) || jsonb_build_object('token', b.token) order by b.day desc, b.hm desc)
                          from (select * from bookings where user_id = auth.uid() order by day desc, hm desc limit 50) b), '[]'::jsonb));
end $$;

-- the signed-in member's profile, or null if they have not filled it in yet
create or replace function my_profile() returns jsonb
language plpgsql stable security definer set search_path = public as $$
declare em text := _customer_email(); c customers;
begin
  select * into c from customers where user_id = auth.uid();
  if not found then return null; end if;
  return jsonb_build_object('email', em, 'phone', c.phone, 'first_name', c.first_name, 'last_name', c.last_name,
    'birth_date', c.birth_date, 'gender', c.gender, 'marketing_consent', c.marketing_consent,
    'email_verified', c.email_verified_at is not null and lower(c.email) = em);
end $$;

-- called after the member opens the emailed sign-in link: that login proves they can read mail at this address
create or replace function verify_email() returns jsonb
language plpgsql volatile security definer set search_path = public as $$
declare em text := _customer_email();
begin
  if exists (select 1 from jsonb_array_elements(coalesce(auth.jwt()->'amr', '[]'::jsonb)) a where a->>'method' in ('otp','magiclink')) then
    update customers set email_verified_at = now(), email = em where user_id = auth.uid();
  end if;
  return my_profile();
end $$;

create or replace function save_profile(p jsonb) returns jsonb
language plpgsql volatile security definer set search_path = public as $$
declare em text := _customer_email(); v_phone text := _phone(p->>'phone');
  v_first text := btrim(coalesce(p->>'first_name', '')); v_last text := btrim(coalesce(p->>'last_name', ''));
  v_birth date; v_gender text := coalesce(p->>'gender', ''); v_mkt boolean := coalesce((p->>'marketing_consent')::boolean, false);
begin
  if length(v_first) < 1 or length(v_first) > 60 then raise exception 'name'; end if;
  if length(v_last) < 1 or length(v_last) > 60 then raise exception 'last_name'; end if;
  if v_phone !~ '^0[2-9][0-9]{7,8}$' then raise exception 'phone'; end if;
  if v_gender not in ('', 'male', 'female', 'unspecified') then v_gender := ''; end if;
  if coalesce(p->>'birth_date', '') <> '' then
    v_birth := (p->>'birth_date')::date;
    if v_birth > (_now_bkk())::date or v_birth < date '1900-01-01' then raise exception 'birth'; end if;
  end if;
  if not coalesce((p->>'terms')::boolean, false) and not exists (select 1 from customers where user_id = auth.uid()) then raise exception 'terms'; end if;
  insert into customers as c (user_id, email, phone, first_name, last_name, birth_date, gender, marketing_consent, consent_at, terms_at)
  values (auth.uid(), em, v_phone, v_first, v_last, v_birth, v_gender, v_mkt, case when v_mkt then now() end, now())
  on conflict (user_id) do update set email = em, phone = v_phone, first_name = v_first, last_name = v_last, birth_date = v_birth,
    gender = v_gender, marketing_consent = v_mkt,
    consent_at = case when v_mkt and not c.marketing_consent then now() when not v_mkt then null else c.consent_at end,
    updated_at = now();
  return my_profile();
end $$;

create or replace function get_booking(p_ref text, p_token uuid) returns jsonb
language plpgsql volatile security definer set search_path = public as $$
begin
  perform _expire_holds();
  return (select _booking_json(b) from bookings b where b.ref = upper(p_ref) and b.token = p_token);
end $$;

-- the customer attaches a transfer slip; a booking released for late payment gets its places back if they are still free
create or replace function submit_slip(p_ref text, p_token uuid, p_image text) returns jsonb
language plpgsql volatile security definer set search_path = public as $$
declare b bookings; s record; v_b64 text; v_head bytea;
begin
  if p_image is null or length(p_image) > 800000 or length(p_image) < 2000 or p_image !~ '^data:image/(jpeg|png|webp);base64,[A-Za-z0-9+/]+={0,2}$' then raise exception 'slip'; end if;
  -- a real picture: whole base64 blocks, starting with a JPEG, PNG or WebP header
  v_b64 := substr(p_image, position(',' in p_image) + 1);
  if length(v_b64) % 4 <> 0 then raise exception 'slip'; end if;
  v_head := decode(left(v_b64, 16), 'base64');
  if not (substr(v_head, 1, 3) = '\xffd8ff'::bytea or substr(v_head, 1, 8) = '\x89504e470d0a1a0a'::bytea
          or (substr(v_head, 1, 4) = '\x52494646'::bytea and substr(v_head, 9, 4) = '\x57454250'::bytea)) then raise exception 'slip'; end if;
  perform _expire_holds();
  select * into b from bookings where ref = upper(p_ref) and token = p_token for update;
  if not found then raise exception 'notfound'; end if;
  if (select count(*) from slips where booking_id = b.id) >= 5 then raise exception 'slip_many'; end if;
  if b.user_id is not null and ((select count(*) from slips q join bookings x on x.id = q.booking_id
      where x.user_id = b.user_id and q.created_at > now() - interval '1 hour') >= 8
      or (select count(*) from slips q join bookings x on x.id = q.booking_id
      where x.user_id = b.user_id and q.created_at > now() - interval '1 day') >= 20) then raise exception 'slip_many'; end if;
  if b.status = 'cancelled' and b.pay = 'expired' then
    if _start(b.day, b.hm) <= now() then raise exception 'hold_expired'; end if;
    -- getting a released booking back counts against the same limit as new unpaid bookings
    if b.user_id is not null and (select count(*) from bookings x where x.user_id = b.user_id and x.id <> b.id and x.status = 'confirmed'
          and (x.pay = 'review' or (x.pay = 'await' and x.hold_until > now()))) >= 2 then raise exception 'unpaid_many'; end if;
    perform pg_advisory_xact_lock(hashtext('slot:' || b.day || ' ' || b.hm));
    select * into s from _slots(b.day) x where x.hm = b.hm;
    if not found or s.blocked or s.capacity - s.used < b.people then raise exception 'hold_expired'; end if;
    if exists (select 1 from bookings x where x.id <> b.id and x.day = b.day and x.hm = b.hm and x.user_id = b.user_id and x.status in ('confirmed','checked_in')) then
      raise exception 'duplicate'; end if;
    update bookings set status = 'confirmed', cancelled_at = null where id = b.id;
  elsif not (b.status = 'confirmed' and b.pay in ('await','review')) then raise exception 'change_closed';
  end if;
  insert into slips (booking_id, data, img_hash) values (b.id, p_image, md5(p_image));
  update bookings set pay = 'review', slip_at = now() where id = b.id returning * into b;
  perform _log(b.ref, 'ลูกค้าแนบสลิปโอนเงิน');
  return _booking_json(b);
end $$;

-- "my booking" lookup with the booking reference and the phone number used
create or replace function find_booking(p_ref text, p_phone text) returns jsonb
language sql stable security definer set search_path = public as $$
  select jsonb_build_object('ref', ref, 'token', token) from bookings
  where ref = upper(btrim(p_ref)) and phone = _phone(p_phone) $$;

create or replace function cancel_booking(p_ref text, p_token uuid) returns jsonb
language plpgsql volatile security definer set search_path = public as $$
declare b bookings;
begin
  select * into b from bookings where ref = upper(p_ref) and token = p_token for update;
  if not found then raise exception 'notfound'; end if;
  if not (_booking_json(b)->>'can_cancel')::boolean then raise exception 'change_closed'; end if;
  update bookings set status = 'cancelled', cancelled_at = now(), pay_before = pay, pay = case when pay in ('due','await') then 'none' else pay end
   where id = b.id returning * into b;
  perform _log(b.ref, 'ลูกค้ายกเลิกเอง');
  return _booking_json(b);
end $$;

create or replace function move_booking(p_ref text, p_token uuid, p_day date, p_hm text) returns jsonb
language plpgsql volatile security definer set search_path = public as $$
declare c jsonb := _cfg(); b bookings; s record; old text;
begin
  select * into b from bookings where ref = upper(p_ref) and token = p_token for update;
  if not found then raise exception 'notfound'; end if;
  if not (_booking_json(b)->>'can_move')::boolean then raise exception 'change_closed'; end if;
  if _start(p_day, p_hm) < now() + make_interval(mins => (c->>'lead_min')::int)
     or p_day > (_now_bkk())::date + (c->>'horizon_days')::int then raise exception 'slot_closed'; end if;
  perform pg_advisory_xact_lock(hashtext('slot:' || p_day || ' ' || p_hm));
  select * into s from _slots(p_day) x where x.hm = p_hm;
  if not found or s.blocked then raise exception 'slot_closed'; end if;
  if s.capacity - s.used < b.people then raise exception 'slot_full'; end if;
  old := b.day || ' ' || b.hm;
  update bookings set day = p_day, hm = p_hm, moves = moves + 1 where id = b.id returning * into b;
  perform _log(b.ref, 'ลูกค้าเลื่อนรอบจาก ' || old);
  return _booking_json(b);
end $$;

-- the risk acknowledgment, signed on the customer's phone before the visit or on the shop's tablet
create or replace function sign_waiver(p jsonb) returns jsonb
language plpgsql volatile security definer set search_path = public as $$
declare v_first text := btrim(coalesce(p->>'first_name', '')); v_last text := btrim(coalesce(p->>'last_name', ''));
  v_name text; v_email text := lower(btrim(coalesce(p->>'email', ''))); v_phone text := _any_phone(p->>'phone');
  v_sig text := coalesce(p->>'signature', ''); v_svc text := coalesce(p->>'services', 'both'); v_with text := '';
  b bookings; w waivers; h json; v_src text := '';
begin
  if length(v_first) < 1 or length(v_first) > 60 or length(v_last) < 1 or length(v_last) > 60 then raise exception 'name'; end if;
  v_name := v_first || ' ' || v_last;
  if v_email !~ '^[^@\s]+@[^@\s]+\.[^@\s]+$' or length(v_email) > 200 then raise exception 'email'; end if;
  if v_phone !~ '^(0[2-9][0-9]{7,8}|\+[1-9][0-9]{7,14})$' then raise exception 'any_phone'; end if;
  if not coalesce((p->>'agree')::boolean, false) then raise exception 'agree'; end if;
  if length(v_sig) < 600 or length(v_sig) > 200000 or v_sig !~ '^data:image/png;base64,[A-Za-z0-9+/]+={0,2}$' then raise exception 'signature'; end if;
  if v_svc not in ('cold','sauna','both') then v_svc := 'both'; end if;
  begin
    h := nullif(current_setting('request.headers', true), '')::json;
    v_src := btrim(coalesce(nullif(h->>'cf-connecting-ip', ''), nullif(h->>'x-real-ip', ''), nullif(split_part(coalesce(h->>'x-forwarded-for', ''), ',', 1), ''), ''));
    -- IPv6 addresses are grouped by their first four blocks (one home or phone network)
    if position(':' in v_src) > 0 then v_src := host(network(set_masklen(v_src::inet, 64))); end if;
    if v_src <> '' then v_src := md5('pfd:' || v_src); end if;
  exception when others then v_src := '';
  end;
  -- a booking takes as many registrations as it has people (the same person signing twice doesn't count again)
  if coalesce(p->>'ref', '') <> '' and coalesce(p->>'token', '') ~ '^[0-9a-fA-F-]{36}$' then
    select * into b from bookings where ref = upper(btrim(p->>'ref')) and wtoken = (p->>'token')::uuid
       and status in ('confirmed','checked_in') for update;
    if found then
      if exists (select 1 from waivers where booking_id = b.id and lower(email) = v_email
                  and lower(btrim(first_name)) = lower(v_first) and lower(btrim(last_name)) = lower(v_last)) then raise exception 'waiver_dup'; end if;
      if (select count(*) from waivers where booking_id = b.id) >= b.people then raise exception 'waiver_full'; end if;
    end if;
  end if;
  -- the shop's own tablet (a signed-in team member) is never limited; others are, so nobody can flood the database
  if _role() is null then
    if v_src <> '' and (select count(*) from waivers where src = v_src and signed_at > now() - interval '1 hour') >= 20 then raise exception 'waiver_busy'; end if;
    if b.id is null and (select count(*) from waivers where booking_id is null and src <> 'staff' and signed_at > now() - interval '1 day') >= 150 then raise exception 'waiver_busy'; end if;
  else
    v_src := 'staff';
  end if;
  -- a quiet note for the team: who this person came with (nothing when it is the person who booked)
  if b.id is not null and not (v_email = lower(b.email) or v_phone = b.phone) then v_with := 'มากับ ' || b.name || ' (' || b.ref || ')'; end if;
  insert into waivers (booking_id, name, first_name, last_name, email, phone, came_with, marketing_consent, services, signature, version, lang, src)
  values (b.id, v_name, v_first, v_last, v_email, v_phone, v_with, coalesce((p->>'marketing_consent')::boolean, false),
          v_svc, v_sig, 'v1', case when p->>'lang' = 'en' then 'en' else 'th' end, v_src) returning * into w;
  if b.id is not null then perform _log(b.ref, 'ลงชื่อรับทราบความเสี่ยง: ' || v_name); end if;
  return jsonb_build_object('id', w.id, 'signed', (select count(*) from waivers where booking_id = b.id), 'people', b.people, 'ref', b.ref, 'name', b.name);
end $$;

-- what the waiver page shows about a booking; needs the booking's private token (from the booking page or email link)
drop function if exists waiver_booking(text);
-- (the link only shows the session and the booker's name; never their phone or email)
create or replace function waiver_booking(p_ref text, p_token uuid) returns jsonb
language sql stable security definer set search_path = public as $$
  select jsonb_build_object('ref', b.ref, 'day', b.day, 'hm', b.hm, 'people', b.people, 'name', b.name,
    'signed', (select count(*) from waivers w where w.booking_id = b.id))
  from bookings b where b.ref = upper(btrim(p_ref)) and b.wtoken = p_token and b.status in ('confirmed','checked_in') $$;

-- ---------- staff (signed in with Supabase Auth) ----------
drop function if exists staff_me();
create or replace function staff_me(p_code text default '') returns jsonb
language plpgsql volatile security definer set search_path = public as $$
declare em text := lower(coalesce(auth.jwt()->>'email', '')); inv staff_invites;
begin
  if auth.uid() is null then return null; end if;
  if not exists (select 1 from staff where user_id = auth.uid()) then
    select * into inv from staff_invites where email = em for update;
    if found then
      if inv.tries >= 5 then return jsonb_build_object('need_code', true, 'locked', true); end if;
      if btrim(coalesce(p_code, '')) <> inv.code then
        if btrim(coalesce(p_code, '')) <> '' then update staff_invites set tries = tries + 1 where email = em; end if;
        return jsonb_build_object('need_code', true, 'wrong', btrim(coalesce(p_code, '')) <> '', 'locked', inv.tries + 1 >= 5 and btrim(coalesce(p_code, '')) <> '');
      end if;
      insert into staff (user_id, email, name, role) values (auth.uid(), em, inv.name, inv.role);
      delete from staff_invites where email = em;
    end if;
  end if;
  return (select jsonb_build_object('email', email, 'name', name, 'role', role) from staff where user_id = auth.uid());
end $$;

create or replace function staff_day(p_day date) returns jsonb
language plpgsql volatile security definer set search_path = public as $$
declare c jsonb := _cfg();
begin
  perform _need('staff');
  perform _expire_holds();
  return jsonb_build_object(
    'day', p_day,
    'closed', exists (select 1 from closed_days where day = p_day),
    'holiday', (select name from holidays where day = p_day),
    'slots', coalesce((select jsonb_agg(jsonb_build_object(
        'hm', s.hm, 'end', to_char(s.hm::time + make_interval(mins => (c->>'duration')::int), 'HH24:MI'),
        'capacity', s.capacity, 'blocked', s.blocked, 'used', s.used,
        'bookings', coalesce((select jsonb_agg(_staff_json(b) order by b.created_at) from bookings b
                              where b.day = p_day and b.hm = s.hm and b.status not in ('cancelled','waiting')), '[]'::jsonb))
        order by s.hm)
      from _slots(p_day) s), '[]'::jsonb),
    'off_schedule', coalesce((select jsonb_agg(_staff_json(b)) from bookings b
        where b.day = p_day and b.status not in ('cancelled','waiting')
          and not exists (select 1 from _slots(p_day) z where z.hm = b.hm)), '[]'::jsonb),
    'cancelled', (select count(*) from bookings where day = p_day and status = 'cancelled'));
end $$;

create or replace function staff_search(p_q text) returns jsonb
language plpgsql stable security definer set search_path = public as $$
declare q text := lower(btrim(coalesce(p_q, ''))); digits text := _phone(p_q);
begin
  perform _need('staff');
  return coalesce((select jsonb_agg(x.j order by x.day desc, x.hm desc) from (
    select _staff_json(b) j, b.day, b.hm from bookings b
    where q = '' or lower(b.name) like '%' || q || '%' or lower(b.ref) = q
       or (length(digits) >= 4 and b.phone like '%' || digits || '%')
    order by b.day desc, b.hm desc limit 150) x), '[]'::jsonb);
end $$;

create or replace function staff_booking(p_ref text) returns jsonb
language plpgsql stable security definer set search_path = public as $$
declare b bookings;
begin
  perform _need('staff');
  select * into b from bookings where ref = upper(btrim(p_ref));
  if not found then return null; end if;
  return _staff_json(b) || jsonb_build_object(
    'history', coalesce((select jsonb_agg(jsonb_build_object('at', at, 'who', who, 'action', action) order by at desc)
                         from audit where ref = b.ref), '[]'::jsonb));
end $$;

create or replace function staff_create_booking(p jsonb) returns jsonb
language plpgsql volatile security definer set search_path = public as $$
-- pay_type: venue = pays at the shop, online = already transferred, package = uses a coupon package, free = review / influencer / guest
declare c jsonb := _cfg(); r text := _need('staff'); v_day date := (p->>'day')::date; v_hm text := p->>'hm';
  v_people int := (p->>'people')::int; v_name text := btrim(coalesce(p->>'name', '')); v_phone text := _phone(p->>'phone');
  v_over boolean := coalesce((p->>'over')::boolean, false);
  v_type text := coalesce(nullif(p->>'pay_type', ''), case when coalesce((p->>'free')::boolean, false) then 'free' else 'venue' end);
  v_pkg text := left(btrim(coalesce(p->>'package', '')), 60);
  v_channel text := coalesce(nullif(p->>'channel', ''), 'walk_in'); s record; b bookings;
begin
  if length(v_name) < 1 or length(v_name) > 120 then raise exception 'name'; end if;
  if v_phone <> '' and v_phone !~ '^0[2-9][0-9]{7,8}$' then raise exception 'phone'; end if;
  if v_people is null or v_people < 1 or v_people > 50 then raise exception 'people'; end if;
  if v_type not in ('venue','online','package','free') then raise exception 'method'; end if;
  if v_type = 'package' and v_pkg = '' then raise exception 'package'; end if;
  if v_over and r = 'staff' then raise exception 'forbidden'; end if;
  perform pg_advisory_xact_lock(hashtext('slot:' || v_day || ' ' || v_hm));
  select * into s from _slots(v_day) x where x.hm = v_hm;
  if not found then raise exception 'slot_closed'; end if;
  if s.capacity - s.used < v_people and not v_over then raise exception 'slot_full'; end if;
  insert into bookings (ref, day, hm, people, name, phone, email, note, staff_note, total, channel, pay, method, package, paid_at)
  values (_ref(), v_day, v_hm, v_people, v_name, v_phone, lower(btrim(coalesce(p->>'email', ''))), '', left(coalesce(p->>'note', ''), 1000),
          case v_type when 'venue' then _total(v_people) when 'online' then _total(v_people) else 0 end, v_channel,
          case v_type when 'venue' then 'due' when 'free' then 'none' else 'paid' end,
          case v_type when 'online' then 'transfer' when 'package' then 'package' else '' end,
          case when v_type = 'package' then v_pkg else '' end,
          case when v_type in ('online','package') then now() end)
  returning * into b;
  perform _log(b.ref, 'สร้างการจอง (' || v_channel || ', ' || case v_type when 'venue' then 'ชำระหน้าร้าน' when 'online' then 'ชำระออนไลน์แล้ว' when 'package' then 'คูปอง ' || v_pkg else 'ไม่มีค่าใช้จ่าย' end || ')' || case when v_over then ' เกินความจุ' else '' end);
  return _staff_json(b);
end $$;

-- the whole group at once; everyone who registered is checked in with it
create or replace function staff_checkin(p_ref text, p_method text) returns jsonb
language plpgsql volatile security definer set search_path = public as $$
declare b bookings; r text := _need('staff');
begin
  select * into b from bookings where ref = upper(p_ref) for update;
  if not found then raise exception 'notfound'; end if;
  if b.status <> 'confirmed' then raise exception 'change_closed'; end if;
  if b.day > (_now_bkk())::date then raise exception 'not_today'; end if;
  -- people check in one by one after registering; letting a group in without everyone registered is for managers
  if r = 'staff' and (select count(*) from waivers where booking_id = b.id) < b.people then raise exception 'forbidden'; end if;
  update waivers set checked_at = now(), checked_by = coalesce((select email from staff where user_id = auth.uid()), '')
   where booking_id = b.id and checked_at is null;
  if coalesce(p_method, '') = 'transfer' and b.pay in ('review','await') then perform _use_slip(b.id); end if;
  update bookings set status = 'checked_in', checked_at = now(),
         pay = case when pay in ('due','await','review') and coalesce(p_method, '') <> '' then 'paid' when pay = 'await' then 'due' else pay end,
         method = case when pay in ('due','await','review') and coalesce(p_method, '') <> '' then p_method else method end,
         paid_at = case when pay in ('due','await','review') and coalesce(p_method, '') <> '' then now() else paid_at end,
         hold_until = null
   where id = b.id returning * into b;
  perform _log(b.ref, 'เช็คอิน' || case when b.pay = 'paid' and coalesce(p_method, '') <> '' then ' รับเงิน ' || p_method else '' end);
  return _booking_json(b);
end $$;

create or replace function staff_take_payment(p_ref text, p_method text) returns jsonb
language plpgsql volatile security definer set search_path = public as $$
declare b bookings;
begin
  perform _need('staff');
  if coalesce(p_method, '') not in ('cash','transfer','card','qr') then raise exception 'method'; end if;
  select * into b from bookings where ref = upper(p_ref) and pay in ('due','await','review') and status <> 'cancelled' for update;
  if not found then raise exception 'change_closed'; end if;
  if p_method = 'transfer' and b.pay in ('review','await') then perform _use_slip(b.id); end if;
  update bookings set pay = 'paid', method = p_method, paid_at = now() where id = b.id returning * into b;
  perform _log(b.ref, case when p_method = 'transfer' then 'ยืนยันรับโอน' else 'รับเงิน ' || p_method end);
  return _booking_json(b);
end $$;

-- the latest slip a customer attached to a booking
create or replace function staff_slip(p_ref text) returns jsonb
language plpgsql stable security definer set search_path = public as $$
begin
  perform _need('staff');
  return (select jsonb_build_object('data', s.data, 'at', s.created_at, 'result', s.result, 'slip_ref', s.slip_ref, 'amount', s.amount,
                 'checking', s.checked_at is null or s.retry_at is not null) from slips s join bookings b on b.id = s.booking_id
          where b.ref = upper(btrim(p_ref)) order by s.created_at desc limit 1);
end $$;

-- online bookings whose slip is waiting to be checked
create or replace function staff_slips() returns jsonb
language plpgsql volatile security definer set search_path = public as $$
begin
  perform _need('staff');
  perform _expire_holds();
  return coalesce((select jsonb_agg(_staff_json(b) order by b.slip_at) from bookings b where b.pay = 'review' and b.status <> 'cancelled'), '[]'::jsonb);
end $$;

create or replace function staff_set_status(p_ref text, p_status text) returns jsonb
language plpgsql volatile security definer set search_path = public as $$
declare b bookings; r text := _need('staff');
begin
  select * into b from bookings where ref = upper(p_ref) for update;
  if not found then raise exception 'notfound'; end if;
  if p_status = 'no_show' and b.status = 'confirmed' then
    update bookings set status = 'no_show', pay = case when pay = 'await' then 'due' else pay end, hold_until = null where id = b.id returning * into b;
  elsif p_status = 'waiting' and b.status = 'confirmed' then
    update bookings set status = 'waiting', pay = case when pay = 'await' then 'due' else pay end, hold_until = null where id = b.id returning * into b;
  elsif p_status = 'cancelled' and b.status in ('confirmed','no_show','waiting') then
    update bookings set status = 'cancelled', cancelled_at = now(), pay_before = pay, pay = case when pay in ('due','await') then 'none' else pay end
     where id = b.id returning * into b;
  elsif p_status = 'confirmed' and b.status in ('checked_in','no_show') then
    update bookings set status = 'confirmed', checked_at = null, pay = case when pay = 'await' then 'due' else pay end, hold_until = null where id = b.id returning * into b;
    update waivers set checked_at = null, checked_by = '' where booking_id = b.id;  -- undoing the visit undoes each person's check-in too
  else raise exception 'change_closed';
  end if;
  perform _log(b.ref, 'เปลี่ยนสถานะเป็น ' || p_status);
  return _booking_json(b);
end $$;

-- undo a cancel (a mis-tap, or the customer changed their mind): back to booked if the session still has room
create or replace function staff_restore(p_ref text, p_over boolean default false) returns jsonb
language plpgsql volatile security definer set search_path = public as $$
declare b bookings; s record; r text := _need('staff'); v text;
begin
  if p_over and r = 'staff' then raise exception 'forbidden'; end if;
  select * into b from bookings where ref = upper(p_ref) for update;
  if not found then raise exception 'notfound'; end if;
  if b.status <> 'cancelled' then raise exception 'change_closed'; end if;
  perform pg_advisory_xact_lock(hashtext('slot:' || b.day || ' ' || b.hm));
  select * into s from _slots(b.day) x where x.hm = b.hm;
  if not found and not coalesce(p_over, false) then raise exception 'slot_closed'; end if;
  if found and s.capacity - s.used < b.people and not coalesce(p_over, false) then raise exception 'slot_full'; end if;
  v := coalesce(nullif(b.pay_before, ''), b.pay);
  -- an online booking that was waiting for its transfer: the slip (if any) goes back to the team, otherwise collect at the shop
  if v in ('await','expired') then v := case when exists (select 1 from slips where booking_id = b.id) then 'review' else 'due' end; end if;
  if v = 'none' and b.total > 0 and b.pay_before = '' then v := 'due'; end if;
  update bookings set status = 'confirmed', cancelled_at = null, pay = v, pay_before = '', hold_until = null where id = b.id returning * into b;
  perform _log(b.ref, 'กู้คืนคิวที่ยกเลิก' || case when p_over then ' เกินความจุ' else '' end);
  return _staff_json(b);
end $$;

-- cancelled bookings from the last 30 days and ahead, newest cancel first
create or replace function staff_cancelled() returns jsonb
language plpgsql volatile security definer set search_path = public as $$
begin
  perform _need('staff');
  perform _expire_holds();
  return coalesce((select jsonb_agg(_staff_json(b) order by b.cancelled_at desc nulls last) from
    (select * from bookings where status = 'cancelled' and day >= (_now_bkk())::date - 30 order by cancelled_at desc nulls last limit 150) b), '[]'::jsonb);
end $$;

create or replace function staff_move(p_ref text, p_day date, p_hm text, p_over boolean) returns jsonb
language plpgsql volatile security definer set search_path = public as $$
declare b bookings; s record; old text; r text := _need('staff'); free_now int;
begin
  if p_over and r = 'staff' then raise exception 'forbidden'; end if;
  select * into b from bookings where ref = upper(p_ref) for update;
  if not found then raise exception 'notfound'; end if;
  if b.status not in ('confirmed','waiting') then raise exception 'change_closed'; end if;
  perform pg_advisory_xact_lock(hashtext('slot:' || p_day || ' ' || p_hm));
  select * into s from _slots(p_day) x where x.hm = p_hm;
  if not found then raise exception 'slot_closed'; end if;
  free_now := s.capacity - s.used;
  if b.status = 'confirmed' and b.day = p_day and b.hm = p_hm then free_now := free_now + b.people; end if;
  if free_now < b.people and not coalesce(p_over, false) then raise exception 'slot_full'; end if;
  old := case when b.status = 'waiting' then 'รอนัดวันใหม่' else b.day || ' ' || b.hm end;
  update bookings set day = p_day, hm = p_hm, status = 'confirmed', pay = case when pay = 'await' then 'due' else pay end, hold_until = null
   where id = b.id returning * into b;
  perform _log(b.ref, 'ย้ายรอบจาก ' || old);
  return _staff_json(b);
end $$;

create or replace function staff_note(p_ref text, p_note text) returns void
language plpgsql volatile security definer set search_path = public as $$
begin
  perform _need('staff');
  update bookings set staff_note = left(coalesce(p_note, ''), 1000) where ref = upper(p_ref);
  perform _log(upper(p_ref), 'บันทึกโน้ตทีมงาน');
end $$;

create or replace function staff_set_slot(p_day date, p_hm text, p_blocked boolean, p_capacity int) returns void
language plpgsql volatile security definer set search_path = public as $$
begin
  perform _need('manager');
  insert into slot_overrides (day, hm, blocked, capacity) values (p_day, p_hm, coalesce(p_blocked, false), p_capacity)
  on conflict (day, hm) do update set blocked = excluded.blocked, capacity = excluded.capacity;
  perform _log('', (case when p_blocked then 'ปิดรอบ ' else 'เปิดรอบ ' end) || p_day || ' ' || p_hm);
end $$;

create or replace function staff_set_closed_day(p_day date, p_closed boolean, p_note text) returns void
language plpgsql volatile security definer set search_path = public as $$
begin
  perform _need('manager');
  if p_closed then insert into closed_days (day, note) values (p_day, coalesce(p_note, '')) on conflict (day) do update set note = excluded.note;
  else delete from closed_days where day = p_day; end if;
  perform _log('', (case when p_closed then 'ปิดร้านวันที่ ' else 'เปิดร้านวันที่ ' end) || p_day);
end $$;

create or replace function staff_set_holiday(p_day date, p_on boolean, p_name text) returns void
language plpgsql volatile security definer set search_path = public as $$
begin
  perform _need('manager');
  if p_on then insert into holidays (day, name) values (p_day, coalesce(p_name, '')) on conflict (day) do update set name = excluded.name;
  else delete from holidays where day = p_day; end if;
  perform _log('', (case when p_on then 'ตั้งเป็นวันหยุดนักขัตฯ ' else 'ยกเลิกวันหยุดนักขัตฯ ' end) || p_day);
end $$;

create or replace function staff_closed_days() returns jsonb
language plpgsql stable security definer set search_path = public as $$
begin
  perform _need('staff');
  return coalesce((select jsonb_agg(jsonb_build_object('day', day, 'note', note) order by day) from closed_days where day >= (_now_bkk())::date), '[]'::jsonb);
end $$;

create or replace function staff_save_settings(p jsonb) returns jsonb
language plpgsql volatile security definer set search_path = public as $$
declare allowed text[] := array['price','pair_price','capacity','capacity_weekend','duration','times','closed','lead_min','horizon_days','cancel_hours','max_moves','max_group','phone','line','address_th','address_en','map_url','hold_min','promptpay','bank_name','bank_account','bank_holder','slip_auto'];
  money text[] := array['promptpay','bank_name','bank_account','bank_holder','slip_auto','hold_min'];
  ints text[] := array['price','pair_price','capacity','capacity_weekend','duration','lead_min','horizon_days','cancel_hours','max_moves','max_group','hold_min'];
  k text; o jsonb := _cfg(); n jsonb := _cfg(); r text := _need('manager'); changed text := '';
begin
  for k in select jsonb_object_keys(p) loop
    if k = any(allowed) then n := jsonb_set(n, array[k], p->k); end if;
  end loop;
  n := jsonb_set(n, '{promptpay}', to_jsonb(regexp_replace(coalesce(n->>'promptpay', ''), '\D', '', 'g')));
  -- where the money goes: only the owner can change it, and every change is written to the log
  foreach k in array money loop
    if (o->k) is distinct from (n->k) then
      if r <> 'owner' then raise exception 'forbidden'; end if;
      changed := changed || ' ' || k || ': ' || coalesce(o->>k, '') || ' → ' || coalesce(n->>k, '') || ';';
    end if;
  end loop;
  foreach k in array ints loop
    if jsonb_typeof(n->k) is distinct from 'number' or (n->>k) !~ '^[0-9]+$' then raise exception 'settings'; end if;
  end loop;
  if jsonb_typeof(n->'slip_auto') is distinct from 'boolean' then raise exception 'settings'; end if;
  if (n->>'capacity')::int not between 1 and 100 or (n->>'capacity_weekend')::int not between 1 and 100 or (n->>'duration')::int not between 10 and 600
     or (n->>'max_group')::int not between 1 and 50 or (n->>'cancel_hours')::int > 720 or (n->>'max_moves')::int > 10 or (n->>'lead_min')::int > 1440
     or (n->>'horizon_days')::int not between 1 and 365 or (n->>'price')::int > 100000 or (n->>'pair_price')::int > 200000 then raise exception 'settings'; end if;
  if coalesce(n->>'map_url', '') <> '' and (n->>'map_url') !~ '^https://[^\s"<>]+$' then raise exception 'settings'; end if;
  if length(coalesce(n->>'phone', '')) > 40 or length(coalesce(n->>'line', '')) > 60 or length(coalesce(n->>'address_th', '')) > 300
     or length(coalesce(n->>'address_en', '')) > 300 or length(coalesce(n->>'bank_name', '')) > 60 or length(coalesce(n->>'bank_account', '')) > 40
     or length(coalesce(n->>'bank_holder', '')) > 120 then raise exception 'settings'; end if;
  if jsonb_typeof(n->'times') is distinct from 'array' or jsonb_typeof(n->'closed') is distinct from 'array' then raise exception 'settings'; end if;
  if exists (select 1 from jsonb_array_elements_text(n->'times') t where t.value !~ '^([01][0-9]|2[0-3]):[0-5][0-9]$') then raise exception 'settings'; end if;
  if exists (select 1 from jsonb_array_elements(n->'closed') e where jsonb_typeof(e) <> 'number' or e::text !~ '^[0-6]$') then raise exception 'settings'; end if;
  if coalesce((n->>'hold_min')::int, 15) not between 5 and 1440 then raise exception 'settings'; end if;
  if (n->>'promptpay') <> '' and length(n->>'promptpay') not in (10, 13, 15) then raise exception 'promptpay'; end if;
  update settings set data = n where id = 1;
  perform _log('', 'แก้ไขการตั้งค่า' || case when changed <> '' then ' (รับเงิน:' || changed || ')' else '' end);
  return n;
end $$;

create or replace function staff_overview(p_day date) returns jsonb
language plpgsql stable security definer set search_path = public as $$
declare d0 date := p_day - 13;
begin
  perform _need('manager');  -- money figures: owner and managers only
  return jsonb_build_object(
    'days', (select jsonb_agg(jsonb_build_object(
        'day', g::date,
        'guests', coalesce((select sum(people) from bookings where day = g::date and status in ('confirmed','checked_in','no_show')), 0),
        'checked', coalesce((select sum(people) from bookings where day = g::date and status = 'checked_in'), 0),
        'revenue', coalesce((select sum(total) from bookings where pay = 'paid' and (paid_at at time zone 'Asia/Bangkok')::date = g::date), 0),
        'created', (select count(*) from bookings where (created_at at time zone 'Asia/Bangkok')::date = g::date),
        'cancelled', (select count(*) from bookings where day = g::date and status = 'cancelled'),
        'capacity', coalesce((select sum(capacity) from _slots(g::date) where not blocked), 0)) order by g)
      from generate_series(d0, p_day, interval '1 day') g),
    'due_today', (select count(*) from bookings where day = p_day and pay in ('due','await','review') and status in ('confirmed','checked_in')),
    'slips', (select count(*) from bookings where pay = 'review' and status <> 'cancelled'));
end $$;

create or replace function staff_team() returns jsonb
language plpgsql stable security definer set search_path = public as $$
begin
  perform _need('staff');
  return jsonb_build_object(
    'staff', coalesce((select jsonb_agg(jsonb_build_object('email', email, 'name', name, 'role', role) order by created_at) from staff), '[]'::jsonb),
    'invites', coalesce((select jsonb_agg(jsonb_build_object('email', email, 'name', name, 'role', role, 'code', case when _role() = 'owner' then code end)) from staff_invites), '[]'::jsonb));
end $$;

drop function if exists staff_invite(text, text, text);
create or replace function staff_invite(p_email text, p_role text, p_name text) returns text
language plpgsql volatile security definer set search_path = public as $$
declare c text;
begin
  perform _need('owner');
  if p_role not in ('manager','staff') then raise exception 'forbidden'; end if;
  -- inviting again gives a fresh code and clears wrong tries
  insert into staff_invites (email, role, name) values (lower(btrim(p_email)), p_role, coalesce(p_name, ''))
  on conflict (email) do update set role = excluded.role, name = excluded.name, tries = 0,
    code = lpad((floor(random() * 1000000))::int::text, 6, '0')
  returning code into c;
  perform _log('', 'เพิ่มพนักงาน ' || lower(btrim(p_email)));
  return c;
end $$;

create or replace function staff_remove(p_email text) returns void
language plpgsql volatile security definer set search_path = public as $$
begin
  perform _need('owner');
  delete from staff_invites where email = lower(btrim(p_email));
  delete from staff where email = lower(btrim(p_email)) and role <> 'owner';
  perform _log('', 'ลบพนักงาน ' || lower(btrim(p_email)));
end $$;

-- ---------- automatic slip check (called only by the pfd-slip edge function, which holds the EasySlip key) ----------
-- the newest slip of a booking, only while it has not been checked yet, with what the slip must match.
-- Taking it marks it as being checked, so two calls at the same moment don't both ask EasySlip.
drop function if exists slip_check_get(text, uuid);
create or replace function slip_check_get(p_ref text, p_token uuid) returns jsonb
language plpgsql volatile security definer set search_path = public as $$
declare b bookings; sl slips; c jsonb := _cfg();
begin
  select * into b from bookings where ref = upper(p_ref) and token = p_token;
  if not found then return null; end if;
  select * into sl from slips where booking_id = b.id order by created_at desc, id desc limit 1 for update;
  if not found or sl.checked_at is not null or sl.claimed_at > now() - interval '2 minutes' then return null; end if;
  update slips set claimed_at = now(), tries = tries + 1 where id = sl.id;
  return jsonb_build_object('slip_id', sl.id, 'data', sl.data, 'pay', b.pay, 'status', b.status, 'total', b.total,
    'created_at', b.created_at, 'auto', coalesce((c->>'slip_auto')::boolean, true), 'used', sl.used, 'has_ref', sl.slip_ref is not null,
    'holder', coalesce(c->>'bank_holder', ''), 'account', coalesce(c->>'bank_account', ''), 'promptpay', coalesce(c->>'promptpay', ''));
end $$;

-- has this bank reference or this exact image already paid another booking?
create or replace function _slip_reused(p_slip slips) returns boolean language sql stable set search_path = public as $$
  select exists (select 1 from slips x where x.used and x.booking_id <> p_slip.booking_id
                 and ((p_slip.slip_ref is not null and x.slip_ref = p_slip.slip_ref) or (p_slip.img_hash <> '' and x.img_hash = p_slip.img_hash))) $$;

-- the result of the check: ok = paid straight away; a clear problem sends the booking back to "awaiting payment"
-- so the customer can send the right slip; anything unsure (service down, receiver not clear) waits for the team
-- p_retry: the checker was busy (or Bangkok Bank's first 5 minutes), so the slip is looked at again a few minutes later
drop function if exists slip_check_set(bigint, text, text, numeric, text);
create or replace function slip_check_set(p_slip_id bigint, p_result text, p_ref text, p_amount numeric, p_note text, p_retry boolean default false) returns jsonb
language plpgsql volatile security definer set search_path = public as $$
declare sl slips; b bookings; v_ref text := nullif(btrim(coalesce(p_ref, '')), '');
begin
  if v_ref is not null then perform pg_advisory_xact_lock(hashtext('slipref:' || v_ref)); end if;
  select * into sl from slips where id = p_slip_id for update;
  if not found then raise exception 'notfound'; end if;
  -- only the newest slip of the booking, and only once: a late answer changes nothing, but the bank reference it read
  -- is kept, so the same transfer can't pay for another booking later
  if sl.checked_at is not null or exists (select 1 from slips x where x.booking_id = sl.booking_id and (x.created_at, x.id) > (sl.created_at, sl.id)) then
    update slips set slip_ref = coalesce(slip_ref, v_ref), amount = coalesce(amount, p_amount), claimed_at = null where id = sl.id;
    if sl.used and v_ref is not null and exists (select 1 from slips x where x.used and x.id <> sl.id and x.booking_id <> sl.booking_id and x.slip_ref = v_ref) then
      insert into audit (who, ref, action) select 'ระบบ', k.ref, 'ตรวจพบภายหลัง: สลิปนี้เคยใช้ชำระการจองอื่นแล้ว เช็คยอดในแอปธนาคาร' from bookings k where k.id = sl.booking_id;
    end if;
    return jsonb_build_object('status', 'stale');
  end if;
  update slips set checked_at = now(), claimed_at = null, result = left(coalesce(p_note, ''), 300), amount = p_amount, slip_ref = v_ref,
         retry_at = case when p_result = 'manual' and coalesce(p_retry, false) and v_ref is null and tries < 4 then now() + interval '6 minutes' end
   where id = sl.id returning * into sl;
  select * into b from bookings where id = sl.booking_id for update;
  if b.pay <> 'review' or b.status <> 'confirmed' then
    -- the team already confirmed this transfer by hand: if the reference turns out to have paid another booking, say so in its history
    if sl.used and v_ref is not null and exists (select 1 from slips x where x.used and x.id <> sl.id and x.booking_id <> sl.booking_id and x.slip_ref = v_ref) then
      insert into audit (who, ref, action) values ('ระบบ', b.ref, 'ตรวจพบภายหลัง: สลิปนี้เคยใช้ชำระการจองอื่นแล้ว เช็คยอดในแอปธนาคาร');
    end if;
    return jsonb_build_object('status', b.pay);
  end if;
  if p_result = 'ok' and _slip_reused(sl) then
    p_result := 'bad'; p_note := 'สลิปนี้เคยใช้ชำระการจองอื่นไปแล้ว';
    update slips set result = p_note where id = sl.id;
  end if;
  if p_result = 'ok' then
    update slips set used = true where id = sl.id;
    update bookings set pay = 'paid', method = 'transfer', paid_at = now() where id = b.id;
    insert into audit (who, ref, action) values ('ระบบ', b.ref, 'ตรวจสลิปอัตโนมัติผ่าน ' || coalesce(p_amount::text, '') || ' บาท' || case when coalesce(p_note, '') <> '' then ' (' || p_note || ')' else '' end);
    return jsonb_build_object('status', 'paid');
  end if;
  if p_result = 'bad' then
    update bookings set pay = 'await', hold_until = greatest(coalesce(hold_until, now()), now() + make_interval(mins => 10)) where id = b.id;
    insert into audit (who, ref, action) values ('ระบบ', b.ref, 'สลิปไม่ผ่าน: ' || coalesce(p_note, ''));
    return jsonb_build_object('status', 'rejected', 'reason', p_note);
  end if;
  if sl.tries <= 1 or sl.retry_at is null then  -- one line in the history, not one per automatic retry
    insert into audit (who, ref, action) values ('ระบบ', b.ref, 'ตรวจสลิปอัตโนมัติไม่ได้ ' || case when sl.retry_at is not null then 'จะลองตรวจอีกครั้งใน 6 นาที' else 'รอทีมงานตรวจ' end || ': ' || coalesce(p_note, ''));
  end if;
  return jsonb_build_object('status', 'manual', 'reason', p_note);
end $$;

-- when the team confirms a transfer by hand, the newest slip is marked as used (and refused if it already paid another booking)
create or replace function _use_slip(p_booking bigint) returns void language plpgsql volatile set search_path = public as $$
declare sl slips;
begin
  select * into sl from slips where booking_id = p_booking order by created_at desc, id desc limit 1 for update;
  if not found then return; end if;
  if _slip_reused(sl) then raise exception 'slip_used'; end if;
  update slips set used = true, checked_at = coalesce(checked_at, now()) where id = sl.id;
end $$;

-- every new slip is checked on the server right away (the browser cannot skip it)
create or replace function _on_slip() returns trigger language plpgsql security definer set search_path = public as $$
declare b bookings;
begin
  select * into b from bookings where id = new.booking_id;
  begin
    perform net.http_post(url := 'https://tisybvfjejtdjtykowcb.supabase.co/functions/v1/pfd-slip',
                          body := jsonb_build_object('ref', b.ref, 'token', b.token), headers := '{"Content-Type":"application/json"}'::jsonb,
                          timeout_milliseconds := 20000);
  exception when others then null;  -- if the check can't start, the slip simply waits for the team
  end;
  return new;
end $$;
drop trigger if exists slips_check on slips;
create trigger slips_check after insert on slips for each row execute function _on_slip();

-- ---------- emails (sent by the pfd-mail edge function, which holds the Resend key) ----------
-- a paid web booking whose confirmation email has not gone out yet; taking it marks it sent, so two calls can't both send
drop function if exists mail_receipt_get(text);
create or replace function mail_receipt_get(p_ref text) returns jsonb
language plpgsql volatile security definer set search_path = public as $$
declare b bookings;
begin
  update bookings set receipt_sent_at = now()
   where ref = upper(p_ref) and pay = 'paid' and receipt_sent_at is null and email <> '' and status in ('confirmed','checked_in')
  returning * into b;
  if not found then return null; end if;
  return _booking_json(b) || jsonb_build_object('token', b.token, 'paid_at', b.paid_at, 'cfg', _cfg());
end $$;

-- p_ok = false: sending failed, so give it back for the next try
create or replace function mail_receipt_done(p_ref text, p_ok boolean default true) returns void
language plpgsql volatile security definer set search_path = public as $$
begin
  if p_ok then
    insert into audit (who, ref, action) values ('ระบบ', upper(p_ref), 'ส่งอีเมลยืนยันการชำระเงินแล้ว');
  else
    update bookings set receipt_sent_at = null where ref = upper(p_ref);
  end if;
end $$;
drop function if exists mail_receipt_done(text);

-- everything worth keeping, for the weekly backup email to the owner
create or replace function backup_data(p_token text) returns jsonb
language plpgsql stable security definer set search_path = public as $$
begin
  if p_token is null or p_token <> (select backup_token from app_private where id = 1) then return null; end if;
  return jsonb_build_object(
    'owners', coalesce((select jsonb_agg(email) from staff where role = 'owner'), '[]'::jsonb),
    'customers', coalesce((select jsonb_agg(to_jsonb(c) order by c.created_at) from customers c), '[]'::jsonb),
    'bookings', coalesce((select jsonb_agg(to_jsonb(b) - 'token' - 'wtoken' order by b.day, b.hm, b.created_at) from bookings b), '[]'::jsonb),
    'waivers', coalesce((select jsonb_agg(jsonb_build_object('first_name', w.first_name, 'last_name', w.last_name, 'email', w.email, 'phone', w.phone,
                           'came_with', w.came_with, 'marketing_consent', w.marketing_consent, 'services', w.services, 'ref', b.ref,
                           'signed_at', w.signed_at, 'checked_at', w.checked_at, 'checked_by', w.checked_by, 'version', w.version,
                           'removed_from', (select x.ref from bookings x where x.id = w.removed_from)) order by w.id)
                         from waivers w left join bookings b on b.id = w.booking_id), '[]'::jsonb),
    'slips', coalesce((select jsonb_agg(jsonb_build_object('ref', b.ref, 'slip_ref', s.slip_ref, 'amount', s.amount, 'result', s.result, 'created_at', s.created_at) order by s.id)
                       from slips s join bookings b on b.id = s.booking_id), '[]'::jsonb));
end $$;

-- when a web booking becomes paid (slip checked, or paid at the shop), ask pfd-mail to send the confirmation
create or replace function _on_paid() returns trigger language plpgsql security definer set search_path = public as $$
begin
  if new.pay = 'paid' and old.pay is distinct from 'paid' and new.email <> '' and new.receipt_sent_at is null then
    begin
      perform net.http_post(url := 'https://tisybvfjejtdjtykowcb.supabase.co/functions/v1/pfd-mail',
                            body := jsonb_build_object('ref', new.ref), headers := '{"Content-Type":"application/json"}'::jsonb);
    exception when others then null;  -- an email problem never blocks the payment
    end;
  end if;
  return new;
end $$;
drop trigger if exists bookings_paid on bookings;
create trigger bookings_paid after update of pay on bookings for each row execute function _on_paid();

-- the customer list for the "ลูกค้า" page (owner and managers): members, plus everyone who registered at the shop
-- (friends who came with a member, walk-ins), with how many times each person checked in
create or replace function staff_customers(p_q text) returns jsonb
language plpgsql stable security definer set search_path = public as $$
declare q text := lower(btrim(coalesce(p_q, ''))); digits text := regexp_replace(coalesce(p_q, ''), '\D', '', 'g');
begin
  perform _need('manager');
  if digits like '66%' and length(digits) = 11 then digits := '0' || substr(digits, 3); end if;
  return coalesce((select jsonb_agg(x order by x->>'sort' desc) from (
    select jsonb_build_object('type', 'member', 'email', c.email, 'phone', c.phone, 'first_name', c.first_name, 'last_name', c.last_name,
      'birth_date', c.birth_date, 'gender', c.gender, 'marketing_consent', c.marketing_consent, 'email_verified', c.email_verified_at is not null,
      'created_at', c.created_at, 'came_with', '',
      'bookings', (select count(*) from bookings b where b.user_id = c.user_id and b.status in ('confirmed','checked_in','no_show')),
      'visits', greatest((select count(*) from waivers w where w.checked_at is not null and lower(w.email) = lower(c.email)),
                         (select count(*) from bookings b where b.user_id = c.user_id and b.status = 'checked_in')),
      'spent', coalesce((select sum(total) from bookings b where b.user_id = c.user_id and b.pay = 'paid'), 0),
      'last_day', (select max(day) from bookings b where b.user_id = c.user_id and b.status in ('confirmed','checked_in')),
      'sort', c.created_at) x
    from customers c
    where q = '' or lower(c.first_name || ' ' || c.last_name) like '%' || q || '%' or lower(c.email) like '%' || q || '%'
       or (length(digits) >= 4 and c.phone like '%' || digits || '%')
    union all
    select jsonb_build_object('type', 'guest', 'email', g.email, 'phone', g.phone, 'first_name', g.first_name, 'last_name', g.last_name,
      'marketing_consent', g.marketing_consent, 'created_at', g.first_seen, 'came_with', g.came_with, 'bookings', g.registrations,
      'visits', g.visits, 'spent', 0, 'last_day', g.last_day, 'sort', g.first_seen)
    from (
      select lower(w.email) as em, (array_agg(w.email order by w.signed_at desc))[1] as email,
        (array_agg(w.first_name order by w.signed_at desc))[1] as first_name, (array_agg(w.last_name order by w.signed_at desc))[1] as last_name,
        (array_agg(w.phone order by w.signed_at desc))[1] as phone,
        coalesce((array_agg(w.came_with order by w.signed_at desc) filter (where w.came_with <> ''))[1], '') as came_with,
        (array_agg(w.marketing_consent order by w.signed_at desc))[1] as marketing_consent,
        min(w.signed_at) as first_seen, count(*) as registrations, count(*) filter (where w.checked_at is not null) as visits,
        (max(coalesce(w.checked_at, w.signed_at)) at time zone 'Asia/Bangkok')::date as last_day
      from waivers w
      where w.email <> '' and w.removed_from is null and not exists (select 1 from customers c where lower(c.email) = lower(w.email))
      group by lower(w.email), lower(w.first_name), lower(w.last_name)) g
    where q = '' or lower(g.first_name || ' ' || g.last_name) like '%' || q || '%' or g.em like '%' || q || '%'
       or (length(digits) >= 4 and g.phone like '%' || digits || '%')
    limit 3000) t), '[]'::jsonb);
end $$;

-- everyone registered on a booking, in the order they signed
create or replace function staff_people(p_ref text) returns jsonb
language plpgsql stable security definer set search_path = public as $$
begin
  perform _need('staff');
  return coalesce((select jsonb_agg(jsonb_build_object('id', w.id, 'name', w.name, 'email', w.email, 'phone', w.phone, 'came_with', w.came_with,
      'services', w.services, 'signature', w.signature, 'signed_at', w.signed_at, 'checked_at', w.checked_at, 'checked_by', w.checked_by,
      'visits', (select count(*) from waivers x where x.checked_at is not null and w.email <> '' and lower(x.email) = lower(w.email))) order by w.signed_at)
    from waivers w join bookings b on b.id = w.booking_id where b.ref = upper(btrim(p_ref))), '[]'::jsonb);
end $$;

-- the team confirms one person has arrived (or undoes a mis-tap); the booking counts as arrived with its first person
create or replace function staff_checkin_person(p_id bigint, p_on boolean) returns jsonb
language plpgsql volatile security definer set search_path = public as $$
declare w waivers; b bookings; who text;
begin
  perform _need('staff');
  who := coalesce((select email from staff where user_id = auth.uid()), '');
  select * into w from waivers where id = p_id for update;
  if not found then raise exception 'notfound'; end if;
  select * into b from bookings where id = w.booking_id for update;
  if p_on and b.id is not null then
    if b.day > (_now_bkk())::date then raise exception 'not_today'; end if;  -- a visit is counted on its own day, never ahead
    if b.status not in ('confirmed','checked_in') then raise exception 'change_closed'; end if;
  end if;
  update waivers set checked_at = case when p_on then coalesce(checked_at, now()) end, checked_by = case when p_on then who else '' end
   where id = p_id returning * into w;
  if b.id is not null then
    if p_on and b.status = 'confirmed' then
      update bookings set status = 'checked_in', checked_at = now(), pay = case when pay = 'await' then 'due' else pay end, hold_until = null where id = b.id;
    end if;
    -- undoing the last person who arrived puts the booking back to "booked"
    if not p_on and b.status = 'checked_in' and not exists (select 1 from waivers where booking_id = b.id and checked_at is not null) then
      update bookings set status = 'confirmed', checked_at = null where id = b.id;
    end if;
    perform _log(b.ref, case when p_on then 'เช็คอิน ' else 'ยกเลิกเช็คอิน ' end || w.name);
  end if;
  return jsonb_build_object('id', w.id, 'checked_at', w.checked_at);
end $$;

-- someone registered on the wrong booking (or twice): the team takes them off it, which frees the place for the right person.
-- The signed record is kept, just no longer attached to the booking.
create or replace function staff_unlink_person(p_id bigint) returns jsonb
language plpgsql volatile security definer set search_path = public as $$
declare w waivers; b bookings;
begin
  perform _need('staff');
  select * into w from waivers where id = p_id for update;
  if not found or w.booking_id is null then raise exception 'notfound'; end if;
  if w.checked_at is not null then raise exception 'person_in'; end if;
  select * into b from bookings where id = w.booking_id;
  update waivers set removed_from = booking_id, booking_id = null where id = w.id;
  perform _log(b.ref, 'เอาออกจากรายชื่อผู้ใช้บริการ: ' || w.name);
  return jsonb_build_object('id', w.id);
end $$;

-- the customers the shop already knows (members, people who registered, past bookings), for filling in a new booking
create or replace function staff_find_customer(p_q text) returns jsonb
language plpgsql stable security definer set search_path = public as $$
declare q text := lower(btrim(coalesce(p_q, ''))); d text := regexp_replace(coalesce(p_q, ''), '\D', '', 'g');
begin
  perform _need('staff');
  if d like '66%' and length(d) = 11 then d := '0' || substr(d, 3); end if;
  if length(d) < 4 then d := ''; end if;
  if length(q) < 2 then return '[]'::jsonb; end if;
  return coalesce((select jsonb_agg(jsonb_build_object('name', t.name, 'phone', t.phone, 'kind', t.kind, 'bookings', t.nb, 'visits', t.nv, 'last_day', t.last_day)
      order by t.exact desc, t.starts desc, t.last_day desc nulls last, t.name)
    from (
      select u.name, u.phone, u.kind, (d <> '' and u.phone = d) as exact, (lower(u.name) like q || '%') as starts,
        (select count(*) from bookings k where ((u.phone <> '' and k.phone = u.phone) or k.user_id = u.uid) and k.status <> 'cancelled') as nb,
        greatest((select count(*) from bookings k where ((u.phone <> '' and k.phone = u.phone) or k.user_id = u.uid) and k.status = 'checked_in'),
                 (select count(*) from waivers w where u.email <> '' and lower(w.email) = lower(u.email) and w.checked_at is not null)) as nv,
        (select max(k.day) from bookings k where ((u.phone <> '' and k.phone = u.phone) or k.user_id = u.uid) and k.status <> 'cancelled') as last_day
      from (
        -- one row per person: a member first, then someone who registered, then a past booking with the same phone
        select distinct on (s.key) s.* from (
          select coalesce(nullif(c.phone, ''), lower(c.email)) as key, 1 as pri, c.updated_at as at,
                 btrim(c.first_name || ' ' || c.last_name) as name, c.phone, c.email, 'member'::text as kind, c.user_id as uid
            from customers c
           where lower(c.first_name || ' ' || c.last_name) like '%' || q || '%' or (d <> '' and c.phone like '%' || d || '%')
          union all
          select coalesce(nullif(w.phone, ''), lower(w.email)), 2, w.signed_at, btrim(w.first_name || ' ' || w.last_name), w.phone, w.email, 'guest', null::uuid
            from waivers w
           where w.first_name <> '' and w.removed_from is null
             and (lower(w.first_name || ' ' || w.last_name) like '%' || q || '%' or (d <> '' and w.phone like '%' || d || '%'))
          union all
          select k.phone, 3, k.created_at, k.name, k.phone, '', 'booking', null::uuid
            from bookings k
           where k.phone <> '' and (lower(k.name) like '%' || q || '%' or (d <> '' and k.phone like '%' || d || '%'))
        ) s
        order by s.key, s.pri, s.at desc
      ) u
      order by (d <> '' and u.phone = d) desc, (lower(u.name) like q || '%') desc, u.at desc
      limit 8) t), '[]'::jsonb);
end $$;

-- people who registered before (members and friends), by name, phone or email, so they don't type it all again
create or replace function staff_people_search(p_q text) returns jsonb
language plpgsql stable security definer set search_path = public as $$
declare q text := lower(btrim(coalesce(p_q, ''))); digits text := regexp_replace(coalesce(p_q, ''), '\D', '', 'g');
begin
  perform _need('staff');
  if length(q) < 2 then return '[]'::jsonb; end if;
  if digits like '66%' and length(digits) = 11 then digits := '0' || substr(digits, 3); end if;
  return coalesce((select jsonb_agg(t.x order by t.at desc) from (
    (select distinct on (lower(w.email), lower(w.first_name), lower(w.last_name))
        jsonb_build_object('first_name', w.first_name, 'last_name', w.last_name, 'email', w.email, 'phone', w.phone, 'last', w.signed_at,
          'visits', (select count(*) from waivers z where z.checked_at is not null and lower(z.email) = lower(w.email))) as x, w.signed_at as at
      from waivers w
      where w.email <> '' and w.removed_from is null and (lower(w.first_name || ' ' || w.last_name) like '%' || q || '%' or lower(w.email) like '%' || q || '%'
         or (length(digits) >= 4 and w.phone like '%' || digits || '%'))
      order by lower(w.email), lower(w.first_name), lower(w.last_name), w.signed_at desc)
    union all
    (select jsonb_build_object('first_name', c.first_name, 'last_name', c.last_name, 'email', c.email, 'phone', c.phone, 'last', c.updated_at, 'visits', 0, 'member', true), c.updated_at
      from customers c
      where (lower(c.first_name || ' ' || c.last_name) like '%' || q || '%' or lower(c.email) like '%' || q || '%' or (length(digits) >= 4 and c.phone like '%' || digits || '%'))
        and not exists (select 1 from waivers w2 where lower(w2.email) = lower(c.email)))
    order by 2 desc limit 20) t), '[]'::jsonb);
end $$;

-- signatures for a booking, or everything signed on one day (walk-ins sign without a booking)
create or replace function staff_waivers(p_ref text, p_day date) returns jsonb
language plpgsql stable security definer set search_path = public as $$
begin
  perform _need('staff');
  return coalesce((select jsonb_agg(jsonb_build_object('id', w.id, 'name', w.name, 'services', w.services, 'signature', w.signature,
      'signed_at', w.signed_at, 'ref', b.ref, 'lang', w.lang) order by w.signed_at)
    from waivers w left join bookings b on b.id = w.booking_id
    where (coalesce(p_ref, '') <> '' and b.ref = upper(btrim(p_ref)))
       or (coalesce(p_ref, '') = '' and w.removed_from is null and (w.signed_at at time zone 'Asia/Bangkok')::date = p_day)), '[]'::jsonb);
end $$;

-- people booked per day, for the strip of days at the top of the queue page
create or replace function staff_days(p_from date, p_days int) returns jsonb
language plpgsql volatile security definer set search_path = public as $$
declare out jsonb := '[]'::jsonb; d date;
begin
  perform _need('staff');
  perform _expire_holds();
  for i in 0..least(greatest(coalesce(p_days, 14), 1), 62) - 1 loop
    d := p_from + i;
    out := out || jsonb_build_array(jsonb_build_object('day', d,
      'closed', not exists (select 1 from _slots(d)),
      'people', coalesce((select sum(people) from bookings where day = d and status in ('confirmed','checked_in','no_show')), 0)));
  end loop;
  return out;
end $$;

-- bookings waiting for the customer to choose a new day
create or replace function staff_waiting() returns jsonb
language plpgsql stable security definer set search_path = public as $$
begin
  perform _need('staff');
  return coalesce((select jsonb_agg(_staff_json(b) order by b.day, b.hm) from bookings b where b.status = 'waiting'), '[]'::jsonb);
end $$;

-- every booking in a date range, for the CSV download
create or replace function staff_export(p_from date, p_to date) returns jsonb
language plpgsql stable security definer set search_path = public as $$
begin
  perform _need('staff');
  if p_to - p_from > 400 then raise exception 'settings'; end if;
  return coalesce((select jsonb_agg(_staff_json(b) order by b.day, b.hm, b.created_at) from bookings b
                   where b.day between p_from and p_to), '[]'::jsonb);
end $$;

-- ---------- jobs the database runs by itself (pg_cron, below) ----------
-- slips the checker couldn't read yet (busy, or a Bangkok Bank slip in its first 5 minutes) are sent again, up to 4 tries;
-- a slip the team already approved by hand is still read once, so its bank reference is on record
create or replace function _slip_recheck() returns int language plpgsql volatile security definer set search_path = public as $$
declare r record; n int := 0;
begin
  for r in
    update slips s set checked_at = null, claimed_at = null, retry_at = null
     where s.retry_at < now() and s.tries < 4 and s.slip_ref is null and s.created_at > now() - interval '1 day'
       and not exists (select 1 from slips x where x.booking_id = s.booking_id and (x.created_at, x.id) > (s.created_at, s.id))
       and exists (select 1 from bookings k where k.id = s.booking_id and ((k.pay = 'review' and k.status = 'confirmed') or s.used))
    returning s.booking_id
  loop
    begin
      perform net.http_post(url := 'https://tisybvfjejtdjtykowcb.supabase.co/functions/v1/pfd-slip',
                            body := (select jsonb_build_object('ref', ref, 'token', token) from bookings where id = r.booking_id),
                            headers := '{"Content-Type":"application/json"}'::jsonb, timeout_milliseconds := 20000);
      n := n + 1;
    exception when others then null;
    end;
  end loop;
  return n;
end $$;

-- payment confirmations that did not go out are tried again (paid in the last two days); a send that stopped
-- half-way (taken but never finished) is handed back after 30 minutes
create or replace function _receipt_retry() returns int language plpgsql volatile security definer set search_path = public as $$
declare r record; n int := 0;
begin
  update bookings b set receipt_sent_at = null
   where b.pay = 'paid' and b.receipt_sent_at between now() - interval '2 days' and now() - interval '30 minutes'
     and not exists (select 1 from audit a where a.ref = b.ref and a.action = 'ส่งอีเมลยืนยันการชำระเงินแล้ว');
  for r in select b.ref from bookings b
            where b.pay = 'paid' and b.receipt_sent_at is null and b.email <> '' and b.status in ('confirmed','checked_in')
              and b.paid_at between now() - interval '2 days' and now() - interval '5 minutes'
            limit 20
  loop
    begin
      perform net.http_post(url := 'https://tisybvfjejtdjtykowcb.supabase.co/functions/v1/pfd-mail',
                            body := jsonb_build_object('ref', r.ref), headers := '{"Content-Type":"application/json"}'::jsonb);
      n := n + 1;
    exception when others then null;
    end;
  end loop;
  return n;
end $$;

-- ---------- who may call what ----------
-- (after every function is defined: Supabase gives new functions to anon by default, so take everything back first)
revoke execute on all functions in schema public from public, anon, authenticated;
grant execute on function public_config(), availability(date, int, int), get_booking(text, uuid),
  cancel_booking(text, uuid), move_booking(text, uuid, date, text), submit_slip(text, uuid, text), sign_waiver(jsonb), waiver_booking(text, uuid) to anon, authenticated;
grant execute on function create_booking(jsonb), my_bookings(), my_profile(), save_profile(jsonb), verify_email() to authenticated;
grant execute on function staff_me(text), staff_day(date), staff_search(text), staff_booking(text), staff_create_booking(jsonb),
  staff_checkin(text, text), staff_take_payment(text, text), staff_set_status(text, text), staff_move(text, date, text, boolean),
  staff_note(text, text), staff_set_slot(date, text, boolean, int), staff_set_closed_day(date, boolean, text), staff_set_holiday(date, boolean, text), staff_closed_days(),
  staff_save_settings(jsonb), staff_overview(date), staff_days(date, int), staff_waiting(), staff_restore(text, boolean), staff_cancelled(), staff_slip(text), staff_slips(), staff_export(date, date), staff_team(), staff_invite(text, text, text), staff_remove(text) to authenticated;
grant execute on function slip_check_get(text, uuid), slip_check_set(bigint, text, text, numeric, text, boolean),
  mail_receipt_get(text), mail_receipt_done(text, boolean), backup_data(text) to service_role;
grant execute on function staff_customers(text), staff_waivers(text, date), staff_people(text), staff_checkin_person(bigint, boolean),
  staff_people_search(text), staff_unlink_person(bigint), staff_find_customer(text) to authenticated;
-- helpers run inside the functions above (which run as the owner), so nobody needs to call them directly

-- ---------- background jobs (Supabase extensions; skipped quietly where they are not available) ----------
do $$ begin create extension if not exists pg_net; exception when others then raise notice 'pg_net not available'; end $$;
do $$ begin
  create extension if not exists pg_cron;
  -- every Monday 08:00 Bangkok (01:00 UTC): email the owner a backup of members and bookings
  perform cron.schedule('pfd-weekly-backup', '0 1 * * 1',
    $job$ select net.http_post(url := 'https://tisybvfjejtdjtykowcb.supabase.co/functions/v1/pfd-mail',
            body := jsonb_build_object('backup', (select backup_token from public.app_private where id = 1)),
            headers := '{"Content-Type":"application/json"}'::jsonb) $job$);
exception when others then raise notice 'pg_cron not available'; end $$;
do $$ begin
  -- every 15 minutes: try again to send payment confirmations that did not go out
  perform cron.schedule('pfd-receipt-retry', '*/15 * * * *', $job$ select public._receipt_retry() $job$);
  -- every 5 minutes: look again at slips the checker couldn't read yet
  perform cron.schedule('pfd-slip-recheck', '*/5 * * * *', $job$ select public._slip_recheck() $job$);
exception when others then raise notice 'pg_cron not available'; end $$;
