-- +goose Up
ALTER TABLE entitlements DROP CONSTRAINT entitlements_source_check;
ALTER TABLE entitlements ADD CONSTRAINT entitlements_source_check
  CHECK(source IN ('PURCHASE','PREBOOK','DEVELOPMENT_SEED','FREE_LAUNCH'));

ALTER TABLE prebooks ALTER COLUMN transaction_id DROP NOT NULL;
ALTER TABLE device_changes ALTER COLUMN transaction_id DROP NOT NULL;

CREATE TABLE email_otp_challenges (
  id uuid PRIMARY KEY,
  normalized_email text NOT NULL,
  code_hash bytea NOT NULL,
  purpose text NOT NULL CHECK(purpose IN ('SIGNUP')),
  attempts integer NOT NULL DEFAULT 0,
  expires_at timestamptz NOT NULL,
  consumed_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX email_otp_active_idx ON email_otp_challenges(normalized_email, purpose, expires_at DESC);

CREATE TABLE push_tokens (
  id uuid PRIMARY KEY,
  reader_id uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  device_id uuid NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  token text NOT NULL UNIQUE,
  provider text NOT NULL CHECK(provider IN ('EXPO','FCM')),
  enabled boolean NOT NULL DEFAULT true,
  last_error text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX push_tokens_reader_idx ON push_tokens(reader_id, enabled);

CREATE TABLE notifications (
  id uuid PRIMARY KEY,
  campaign_id uuid NOT NULL,
  reader_id uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  title text NOT NULL CHECK(char_length(title) BETWEEN 1 AND 120),
  body text NOT NULL CHECK(char_length(body) BETWEEN 1 AND 500),
  data jsonb NOT NULL DEFAULT '{}',
  read_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX notifications_reader_idx ON notifications(reader_id, created_at DESC);
CREATE INDEX notifications_campaign_idx ON notifications(campaign_id);

ALTER TABLE uploads ADD COLUMN provider text NOT NULL DEFAULT 'R2'
  CHECK(provider IN ('R2','CLOUDINARY'));

-- +goose Down
ALTER TABLE uploads DROP COLUMN provider;
DROP TABLE notifications;
DROP TABLE push_tokens;
DROP TABLE email_otp_challenges;
ALTER TABLE device_changes ALTER COLUMN transaction_id SET NOT NULL;
ALTER TABLE prebooks ALTER COLUMN transaction_id SET NOT NULL;
ALTER TABLE entitlements DROP CONSTRAINT entitlements_source_check;
ALTER TABLE entitlements ADD CONSTRAINT entitlements_source_check
  CHECK(source IN ('PURCHASE','PREBOOK','DEVELOPMENT_SEED'));
