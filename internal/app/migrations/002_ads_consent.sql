-- Advertising measurement (Meta Conversions API) is only sent for bookings made by visitors
-- who accepted advertising cookies.
ALTER TABLE bookings ADD COLUMN IF NOT EXISTS ads_consent BOOLEAN NOT NULL DEFAULT FALSE;
