-- Peaceful Day core schema. Money is stored in satang (THB x 100) as BIGINT.
-- All timestamps are timestamptz; business logic converts to Asia/Bangkok.

CREATE TABLE settings (
  key        TEXT PRIMARY KEY,
  value      JSONB NOT NULL,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE staff_users (
  id            BIGSERIAL PRIMARY KEY,
  email         TEXT NOT NULL UNIQUE,
  name          TEXT NOT NULL,
  password_hash TEXT NOT NULL,
  role          TEXT NOT NULL CHECK (role IN ('owner','manager','front_desk','marketing','finance')),
  active        BOOLEAN NOT NULL DEFAULT TRUE,
  failed_logins INT NOT NULL DEFAULT 0,
  locked_until  TIMESTAMPTZ,
  last_login_at TIMESTAMPTZ,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE staff_sessions (
  token_hash TEXT PRIMARY KEY,
  user_id    BIGINT NOT NULL REFERENCES staff_users(id) ON DELETE CASCADE,
  csrf       TEXT NOT NULL,
  expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE experiences (
  id           BIGSERIAL PRIMARY KEY,
  slug         TEXT NOT NULL UNIQUE,
  name_th      TEXT NOT NULL,
  name_en      TEXT NOT NULL,
  summary_th   TEXT NOT NULL DEFAULT '',
  summary_en   TEXT NOT NULL DEFAULT '',
  duration_min INT NOT NULL DEFAULT 60,
  status       TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','coming_soon','hidden')),
  sort         INT NOT NULL DEFAULT 0
);

-- Products: what customers buy.
--   ticket : booked against a session. price_mode per_person or flat (fixed group size).
--   pass   : credit pack (5 / 10 sessions, gift voucher, membership). Redeemed when booking.
CREATE TABLE products (
  id                BIGSERIAL PRIMARY KEY,
  kind              TEXT NOT NULL CHECK (kind IN ('ticket','pass')),
  category          TEXT NOT NULL DEFAULT 'single' CHECK (category IN ('single','group','package','promotion','gift','membership','complimentary')),
  name_th           TEXT NOT NULL,
  name_en           TEXT NOT NULL,
  desc_th           TEXT NOT NULL DEFAULT '',
  desc_en           TEXT NOT NULL DEFAULT '',
  price             BIGINT NOT NULL CHECK (price >= 0),
  price_mode        TEXT NOT NULL DEFAULT 'per_person' CHECK (price_mode IN ('per_person','flat')),
  min_participants  INT NOT NULL DEFAULT 1 CHECK (min_participants >= 1),
  max_participants  INT NOT NULL DEFAULT 10 CHECK (max_participants >= min_participants),
  credits           INT NOT NULL DEFAULT 0,          -- pass: sessions included
  validity_days     INT NOT NULL DEFAULT 0,          -- pass: 0 = no expiry
  experience_id     BIGINT REFERENCES experiences(id),
  allowed_weekdays  INT[] NOT NULL DEFAULT '{}',     -- empty = any; 0=Sun..6=Sat
  start_after       TEXT NOT NULL DEFAULT '',        -- 'HH:MM' earliest session start, '' = any
  available_from    DATE,
  available_until   DATE,
  public            BOOLEAN NOT NULL DEFAULT TRUE,   -- shown on website
  active            BOOLEAN NOT NULL DEFAULT TRUE,
  sort              INT NOT NULL DEFAULT 0,
  created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Weekly opening template per experience.
CREATE TABLE schedule_templates (
  id            BIGSERIAL PRIMARY KEY,
  experience_id BIGINT NOT NULL REFERENCES experiences(id) ON DELETE CASCADE,
  weekday       INT NOT NULL CHECK (weekday BETWEEN 0 AND 6),
  start_time    TEXT NOT NULL,   -- 'HH:MM'
  duration_min  INT NOT NULL DEFAULT 60,
  capacity      INT NOT NULL CHECK (capacity >= 0),
  UNIQUE (experience_id, weekday, start_time)
);

CREATE TABLE blackouts (
  id         BIGSERIAL PRIMARY KEY,
  start_date DATE NOT NULL,
  end_date   DATE NOT NULL CHECK (end_date >= start_date),
  reason     TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Concrete bookable sessions. Generated from templates; individually overridable.
CREATE TABLE slots (
  id            BIGSERIAL PRIMARY KEY,
  experience_id BIGINT NOT NULL REFERENCES experiences(id),
  starts_at     TIMESTAMPTZ NOT NULL,
  ends_at       TIMESTAMPTZ NOT NULL,
  capacity      INT NOT NULL CHECK (capacity >= 0),
  status        TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','blocked')),
  note          TEXT NOT NULL DEFAULT '',
  overridden    BOOLEAN NOT NULL DEFAULT FALSE,
  UNIQUE (experience_id, starts_at)
);
CREATE INDEX slots_starts_idx ON slots (starts_at);

CREATE TABLE customers (
  id                BIGSERIAL PRIMARY KEY,
  name              TEXT NOT NULL,
  phone             TEXT NOT NULL DEFAULT '',   -- digits, normalised (0XXXXXXXXX or +country)
  email             TEXT NOT NULL DEFAULT '',   -- lower-case
  lang              TEXT NOT NULL DEFAULT 'th',
  marketing_consent BOOLEAN NOT NULL DEFAULT FALSE,
  consent_at        TIMESTAMPTZ,
  notes             TEXT NOT NULL DEFAULT '',
  tags              TEXT[] NOT NULL DEFAULT '{}',
  referral_code     TEXT UNIQUE,
  created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX customers_phone_idx ON customers (phone) WHERE phone <> '';
CREATE INDEX customers_email_idx ON customers (email) WHERE email <> '';

CREATE TABLE customer_logins (
  token_hash  TEXT PRIMARY KEY,
  customer_id BIGINT NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
  kind        TEXT NOT NULL CHECK (kind IN ('magic','session')),
  expires_at  TIMESTAMPTZ NOT NULL,
  used_at     TIMESTAMPTZ,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE promo_codes (
  id                 BIGSERIAL PRIMARY KEY,
  code               TEXT NOT NULL UNIQUE,           -- upper-case
  description        TEXT NOT NULL DEFAULT '',
  kind               TEXT NOT NULL CHECK (kind IN ('percent','fixed')),
  value              BIGINT NOT NULL CHECK (value > 0), -- percent: 1-100, fixed: satang off the order
  max_uses           INT NOT NULL DEFAULT 0,          -- 0 = unlimited
  used_count         INT NOT NULL DEFAULT 0,
  per_customer_limit INT NOT NULL DEFAULT 1,          -- 0 = unlimited
  min_participants   INT NOT NULL DEFAULT 1,
  product_ids        BIGINT[] NOT NULL DEFAULT '{}',  -- empty = any ticket
  valid_from         TIMESTAMPTZ,
  valid_until        TIMESTAMPTZ,
  referrer_customer_id BIGINT REFERENCES customers(id),
  campaign           TEXT NOT NULL DEFAULT '',
  active             BOOLEAN NOT NULL DEFAULT TRUE,
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Credit wallets: packages, gift vouchers, memberships, complimentary passes.
CREATE TABLE passes (
  id             BIGSERIAL PRIMARY KEY,
  code           TEXT NOT NULL UNIQUE,
  product_id     BIGINT NOT NULL REFERENCES products(id),
  customer_id    BIGINT REFERENCES customers(id),
  credits_total  INT NOT NULL CHECK (credits_total >= 0),
  credits_used   INT NOT NULL DEFAULT 0,
  status         TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','active','void','expired')),
  expires_at     TIMESTAMPTZ,
  gift_to_name   TEXT NOT NULL DEFAULT '',
  gift_message   TEXT NOT NULL DEFAULT '',
  issued_by      BIGINT REFERENCES staff_users(id),
  note           TEXT NOT NULL DEFAULT '',
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (credits_used <= credits_total)
);

CREATE TABLE bookings (
  id               BIGSERIAL PRIMARY KEY,
  ref              TEXT NOT NULL UNIQUE,
  customer_id      BIGINT NOT NULL REFERENCES customers(id),
  slot_id          BIGINT NOT NULL REFERENCES slots(id),
  product_id       BIGINT NOT NULL REFERENCES products(id),
  participants     INT NOT NULL CHECK (participants >= 1),
  status           TEXT NOT NULL CHECK (status IN ('hold','confirmed','checked_in','cancelled','expired','no_show')),
  hold_expires_at  TIMESTAMPTZ,
  subtotal         BIGINT NOT NULL DEFAULT 0,
  discount         BIGINT NOT NULL DEFAULT 0,
  credits_value    BIGINT NOT NULL DEFAULT 0,
  credits_applied  INT NOT NULL DEFAULT 0,
  total            BIGINT NOT NULL DEFAULT 0,      -- amount charged online / due
  pay_method       TEXT NOT NULL CHECK (pay_method IN ('online','credits','pay_at_venue','complimentary')),
  payment_state    TEXT NOT NULL DEFAULT 'none' CHECK (payment_state IN ('none','pending','paid','failed','expired','refunded','partially_refunded','due_at_venue','paid_at_venue')),
  promo_code_id    BIGINT REFERENCES promo_codes(id),
  pass_id          BIGINT REFERENCES passes(id),
  manage_token_hash TEXT NOT NULL,
  idempotency_key  TEXT UNIQUE,
  channel          TEXT NOT NULL DEFAULT 'web' CHECK (channel IN ('web','admin','walk_in','phone','line')),
  created_by       BIGINT REFERENCES staff_users(id),
  reschedule_count INT NOT NULL DEFAULT 0,
  checked_in_at    TIMESTAMPTZ,
  cancelled_at     TIMESTAMPTZ,
  cancel_reason    TEXT NOT NULL DEFAULT '',
  needs_attention  TEXT NOT NULL DEFAULT '',
  customer_note    TEXT NOT NULL DEFAULT '',
  staff_note       TEXT NOT NULL DEFAULT '',
  utm_source       TEXT NOT NULL DEFAULT '',
  utm_medium       TEXT NOT NULL DEFAULT '',
  utm_campaign     TEXT NOT NULL DEFAULT '',
  referrer         TEXT NOT NULL DEFAULT '',
  landing_path     TEXT NOT NULL DEFAULT '',
  visitor_id       TEXT NOT NULL DEFAULT '',
  reminder_sent_at TIMESTAMPTZ,
  conversion_sent  BOOLEAN NOT NULL DEFAULT FALSE,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX bookings_slot_idx ON bookings (slot_id) WHERE status IN ('hold','confirmed','checked_in');
CREATE INDEX bookings_customer_idx ON bookings (customer_id);
CREATE INDEX bookings_created_idx ON bookings (created_at);
CREATE INDEX bookings_hold_idx ON bookings (hold_expires_at) WHERE status = 'hold';

CREATE TABLE pass_redemptions (
  id          BIGSERIAL PRIMARY KEY,
  pass_id     BIGINT NOT NULL REFERENCES passes(id),
  booking_id  BIGINT NOT NULL REFERENCES bookings(id),
  credits     INT NOT NULL CHECK (credits > 0),
  reversed_at TIMESTAMPTZ,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE promo_redemptions (
  id          BIGSERIAL PRIMARY KEY,
  promo_id    BIGINT NOT NULL REFERENCES promo_codes(id),
  booking_id  BIGINT NOT NULL REFERENCES bookings(id) UNIQUE,
  customer_id BIGINT NOT NULL REFERENCES customers(id),
  amount      BIGINT NOT NULL,
  reversed_at TIMESTAMPTZ,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Online purchase of a pass (package / gift voucher)
CREATE TABLE pass_orders (
  id              BIGSERIAL PRIMARY KEY,
  ref             TEXT NOT NULL UNIQUE,
  pass_id         BIGINT NOT NULL REFERENCES passes(id),
  customer_id     BIGINT NOT NULL REFERENCES customers(id),
  amount          BIGINT NOT NULL,
  status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','paid','failed','expired','refunded')),
  manage_token_hash TEXT NOT NULL,
  idempotency_key TEXT UNIQUE,
  utm_source      TEXT NOT NULL DEFAULT '',
  utm_medium      TEXT NOT NULL DEFAULT '',
  utm_campaign    TEXT NOT NULL DEFAULT '',
  visitor_id      TEXT NOT NULL DEFAULT '',
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE payments (
  id                 BIGSERIAL PRIMARY KEY,
  booking_id         BIGINT REFERENCES bookings(id),
  pass_order_id      BIGINT REFERENCES pass_orders(id),
  provider           TEXT NOT NULL,              -- 'stripe' | 'simulated' | 'venue'
  provider_session   TEXT UNIQUE,                -- checkout session id
  provider_payment   TEXT,                       -- payment intent / charge id
  method             TEXT NOT NULL DEFAULT '',   -- card | promptpay | cash | transfer
  status             TEXT NOT NULL CHECK (status IN ('pending','paid','failed','expired','refunded','partially_refunded')),
  amount             BIGINT NOT NULL,
  refunded_amount    BIGINT NOT NULL DEFAULT 0,
  currency           TEXT NOT NULL DEFAULT 'thb',
  checkout_url       TEXT NOT NULL DEFAULT '',
  receipt_url        TEXT NOT NULL DEFAULT '',
  paid_at            TIMESTAMPTZ,
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK ((booking_id IS NOT NULL) <> (pass_order_id IS NOT NULL))
);
CREATE INDEX payments_booking_idx ON payments (booking_id);

CREATE TABLE payment_events (
  id           BIGSERIAL PRIMARY KEY,
  provider     TEXT NOT NULL,
  event_id     TEXT NOT NULL,
  type         TEXT NOT NULL,
  payload      JSONB NOT NULL,
  processed_at TIMESTAMPTZ,
  error        TEXT NOT NULL DEFAULT '',
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (provider, event_id)
);

CREATE TABLE refunds (
  id                 BIGSERIAL PRIMARY KEY,
  payment_id         BIGINT NOT NULL REFERENCES payments(id),
  amount             BIGINT NOT NULL CHECK (amount > 0),
  status             TEXT NOT NULL CHECK (status IN ('pending','succeeded','failed')),
  provider_refund_id TEXT,
  reason             TEXT NOT NULL DEFAULT '',
  idempotency_key    TEXT NOT NULL UNIQUE,
  staff_id           BIGINT REFERENCES staff_users(id),
  error              TEXT NOT NULL DEFAULT '',
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE notifications (
  id              BIGSERIAL PRIMARY KEY,
  kind            TEXT NOT NULL,
  channel         TEXT NOT NULL CHECK (channel IN ('email','line','admin_email','meta_capi')),
  recipient       TEXT NOT NULL,
  subject         TEXT NOT NULL DEFAULT '',
  body            TEXT NOT NULL DEFAULT '',
  payload         JSONB,
  booking_id      BIGINT REFERENCES bookings(id),
  dedupe_key      TEXT UNIQUE,
  status          TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','sent','failed','skipped')),
  attempts        INT NOT NULL DEFAULT 0,
  next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_error      TEXT NOT NULL DEFAULT '',
  sent_at         TIMESTAMPTZ,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX notifications_due_idx ON notifications (next_attempt_at) WHERE status = 'queued';

CREATE TABLE audit_log (
  id         BIGSERIAL PRIMARY KEY,
  staff_id   BIGINT REFERENCES staff_users(id),
  actor      TEXT NOT NULL DEFAULT '',
  action     TEXT NOT NULL,
  entity     TEXT NOT NULL DEFAULT '',
  entity_id  TEXT NOT NULL DEFAULT '',
  detail     JSONB,
  ip         TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX audit_created_idx ON audit_log (created_at DESC);

-- First-party, cookie-consented funnel events (no personal data).
CREATE TABLE web_events (
  id           BIGSERIAL PRIMARY KEY,
  visitor_id   TEXT NOT NULL,
  type         TEXT NOT NULL CHECK (type IN ('page_view','booking_start','checkout_start','purchase')),
  path         TEXT NOT NULL DEFAULT '',
  utm_source   TEXT NOT NULL DEFAULT '',
  utm_medium   TEXT NOT NULL DEFAULT '',
  utm_campaign TEXT NOT NULL DEFAULT '',
  referrer_host TEXT NOT NULL DEFAULT '',
  value        BIGINT NOT NULL DEFAULT 0,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX web_events_created_idx ON web_events (created_at);

CREATE TABLE ad_spend (
  id         BIGSERIAL PRIMARY KEY,
  spend_date DATE NOT NULL,
  source     TEXT NOT NULL,
  campaign   TEXT NOT NULL DEFAULT '',
  amount     BIGINT NOT NULL CHECK (amount >= 0),
  note       TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE campaigns (
  id           BIGSERIAL PRIMARY KEY,
  name         TEXT NOT NULL,
  utm_campaign TEXT NOT NULL UNIQUE,
  starts_on    DATE,
  ends_on      DATE,
  notes        TEXT NOT NULL DEFAULT '',
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE landing_pages (
  id          BIGSERIAL PRIMARY KEY,
  slug        TEXT NOT NULL UNIQUE,
  title_th    TEXT NOT NULL,
  title_en    TEXT NOT NULL,
  body_th     TEXT NOT NULL DEFAULT '',
  body_en     TEXT NOT NULL DEFAULT '',
  cta_product BIGINT REFERENCES products(id),
  promo_code  TEXT NOT NULL DEFAULT '',
  published   BOOLEAN NOT NULL DEFAULT FALSE,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE page_meta (
  path     TEXT PRIMARY KEY,
  title_th TEXT NOT NULL DEFAULT '',
  title_en TEXT NOT NULL DEFAULT '',
  desc_th  TEXT NOT NULL DEFAULT '',
  desc_en  TEXT NOT NULL DEFAULT ''
);

CREATE TABLE gallery_images (
  id         BIGSERIAL PRIMARY KEY,
  data       BYTEA NOT NULL,
  mime       TEXT NOT NULL,
  alt_th     TEXT NOT NULL DEFAULT '',
  alt_en     TEXT NOT NULL DEFAULT '',
  placement  TEXT NOT NULL DEFAULT 'gallery' CHECK (placement IN ('gallery','hero','experience','story')),
  sort       INT NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Reviews are only shown when entered by staff with a source link (no invented testimonials).
CREATE TABLE reviews (
  id          BIGSERIAL PRIMARY KEY,
  author      TEXT NOT NULL,
  rating      INT NOT NULL CHECK (rating BETWEEN 1 AND 5),
  body        TEXT NOT NULL,
  source      TEXT NOT NULL,       -- e.g. 'Google'
  source_url  TEXT NOT NULL,
  review_date DATE,
  published   BOOLEAN NOT NULL DEFAULT TRUE,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
