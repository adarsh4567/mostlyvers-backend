-- +goose Up
CREATE TABLE accounts (
  id uuid PRIMARY KEY,
  role text NOT NULL CHECK (role IN ('READER','OWNER')),
  email text NOT NULL,
  normalized_email text NOT NULL,
  password_hash text NOT NULL,
  status text NOT NULL DEFAULT 'ACTIVE' CHECK (status IN ('ACTIVE','DISABLED','DELETION_PENDING','DELETED')),
  email_verified_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (role, normalized_email)
);
CREATE TABLE reader_profiles (
  account_id uuid PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
  name text NOT NULL, age smallint NOT NULL CHECK (age BETWEEN 13 AND 120), gender text NOT NULL,
  phone text NOT NULL, profile_object_key text, deleted_at timestamptz
);
CREATE TABLE owner_profiles (
  account_id uuid PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
  name text NOT NULL, phone text NOT NULL, profile_object_key text
);
CREATE TABLE auth_sessions (
  id uuid PRIMARY KEY, account_id uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  token_hash bytea NOT NULL UNIQUE, family_id uuid NOT NULL, parent_id uuid,
  client_type text NOT NULL CHECK (client_type IN ('ANDROID','ADMIN')),
  expires_at timestamptz NOT NULL, rotated_at timestamptz, revoked_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(), last_used_at timestamptz NOT NULL DEFAULT now(),
  user_agent text, ip_hash bytea
);
CREATE INDEX auth_sessions_account_idx ON auth_sessions(account_id, expires_at);
CREATE TABLE one_time_tokens (
  id uuid PRIMARY KEY, account_id uuid REFERENCES accounts(id) ON DELETE CASCADE,
  purpose text NOT NULL CHECK (purpose IN ('PASSWORD_RESET','EMAIL_CHANGE','ACCOUNT_DELETION')),
  token_hash bytea NOT NULL UNIQUE, payload jsonb NOT NULL DEFAULT '{}', expires_at timestamptz NOT NULL,
  used_at timestamptz, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE rate_limits (
  bucket_key text PRIMARY KEY, attempts integer NOT NULL, window_started_at timestamptz NOT NULL, blocked_until timestamptz
);
CREATE TABLE devices (
  id uuid PRIMARY KEY, reader_id uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  installation_id text NOT NULL, display_name text NOT NULL, platform text NOT NULL DEFAULT 'ANDROID',
  os_version text NOT NULL, app_version text NOT NULL, package_name text NOT NULL,
  authorized_at timestamptz, revoked_at timestamptz, first_seen_at timestamptz NOT NULL DEFAULT now(), last_seen_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(reader_id, installation_id)
);
ALTER TABLE auth_sessions ADD COLUMN device_id uuid REFERENCES devices(id) ON DELETE SET NULL;
CREATE UNIQUE INDEX one_authorized_device_per_reader ON devices(reader_id) WHERE authorized_at IS NOT NULL AND revoked_at IS NULL;
CREATE TABLE integrity_verifications (
  id uuid PRIMARY KEY, reader_id uuid REFERENCES accounts(id) ON DELETE SET NULL, device_id uuid REFERENCES devices(id) ON DELETE SET NULL,
  request_hash text NOT NULL, verdict text NOT NULL, reason_code text, verified_at timestamptz NOT NULL DEFAULT now(), expires_at timestamptz NOT NULL
);
CREATE TABLE app_settings (
  id boolean PRIMARY KEY DEFAULT true CHECK (id), version integer NOT NULL DEFAULT 1,
  welcome_message text NOT NULL, thank_you_message text NOT NULL, about_app text NOT NULL,
  device_change_fee_minor bigint NOT NULL DEFAULT 7900 CHECK (device_change_fee_minor >= 0), currency char(3) NOT NULL DEFAULT 'INR',
  maximum_device_changes integer NOT NULL DEFAULT 5 CHECK (maximum_device_changes >= 0),
  default_prebook_discount integer NOT NULL DEFAULT 15 CHECK (default_prebook_discount BETWEEN 0 AND 90),
  latest_duration_months integer NOT NULL DEFAULT 2 CHECK (latest_duration_months BETWEEN 1 AND 120),
  updated_at timestamptz NOT NULL DEFAULT now(), updated_by uuid REFERENCES accounts(id)
);
CREATE TABLE author_profile (
  id boolean PRIMARY KEY DEFAULT true CHECK (id), version integer NOT NULL DEFAULT 1,
  name text NOT NULL DEFAULT '', short_bio text NOT NULL DEFAULT '', full_bio text NOT NULL DEFAULT '',
  photo_object_key text, social_links jsonb NOT NULL DEFAULT '[]', updated_at timestamptz NOT NULL DEFAULT now(), updated_by uuid REFERENCES accounts(id)
);
CREATE TABLE contact_settings (
  id boolean PRIMARY KEY DEFAULT true CHECK (id), version integer NOT NULL DEFAULT 1,
  instagram_username text NOT NULL DEFAULT '', instagram_url text NOT NULL DEFAULT '', youtube_name text NOT NULL DEFAULT '',
  youtube_url text NOT NULL DEFAULT '', official_email text NOT NULL DEFAULT '', support_email text,
  updated_at timestamptz NOT NULL DEFAULT now(), updated_by uuid REFERENCES accounts(id)
);
CREATE TABLE uploads (
  id uuid PRIMARY KEY, owner_id uuid NOT NULL REFERENCES accounts(id), kind text NOT NULL,
  file_name text NOT NULL, content_type text NOT NULL, size_bytes bigint NOT NULL CHECK(size_bytes >= 0), checksum_sha256 text NOT NULL,
  bucket text NOT NULL, object_key text NOT NULL, provider_upload_id text,
  status text NOT NULL DEFAULT 'CREATED' CHECK(status IN ('CREATED','UPLOADING','UPLOADED','PROCESSING','VALID','FAILED','ABORTED')),
  error_code text, created_at timestamptz NOT NULL DEFAULT now(), completed_at timestamptz, expires_at timestamptz NOT NULL
);
CREATE TABLE upload_parts (upload_id uuid NOT NULL REFERENCES uploads(id) ON DELETE CASCADE, part_number integer NOT NULL, etag text, PRIMARY KEY(upload_id, part_number));
CREATE TABLE books (
  id uuid PRIMARY KEY, version integer NOT NULL DEFAULT 1, title text NOT NULL, short_description text NOT NULL,
  cover_object_key text, publication_month smallint NOT NULL CHECK(publication_month BETWEEN 1 AND 12), publication_year smallint NOT NULL,
  price_minor bigint NOT NULL CHECK(price_minor >= 0), currency char(3) NOT NULL DEFAULT 'INR',
  status text NOT NULL DEFAULT 'DRAFT' CHECK(status IN ('DRAFT','UPCOMING','PUBLISHED','ARCHIVED')),
  prebook_enabled boolean NOT NULL DEFAULT false, prebook_discount integer NOT NULL DEFAULT 15 CHECK(prebook_discount BETWEEN 0 AND 90),
  purchase_product_id text, prebook_product_id text, catalog_status text NOT NULL DEFAULT 'NOT_CONFIGURED',
  current_content_version_id uuid, published_at timestamptz, latest_until timestamptz, unavailable_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), archived_at timestamptz
);
CREATE INDEX books_public_idx ON books(status, published_at DESC);
CREATE TABLE book_content_versions (
  id uuid PRIMARY KEY, book_id uuid NOT NULL REFERENCES books(id) ON DELETE CASCADE, version integer NOT NULL,
  source_upload_id uuid REFERENCES uploads(id), source_object_key text NOT NULL, sanitized_object_key text,
  encrypted_object_key text, encrypted_checksum_sha256 text, encrypted_content_length bigint, encrypted_nonce bytea,
  encrypted_content_key bytea, status text NOT NULL DEFAULT 'PROCESSING' CHECK(status IN ('PROCESSING','VALID','FAILED','OBSOLETE')),
  failure_code text, created_at timestamptz NOT NULL DEFAULT now(), validated_at timestamptz, UNIQUE(book_id, version)
);
ALTER TABLE books ADD CONSTRAINT books_current_content_fk FOREIGN KEY(current_content_version_id) REFERENCES book_content_versions(id);
CREATE TABLE book_resources (
  id uuid PRIMARY KEY, content_version_id uuid NOT NULL REFERENCES book_content_versions(id) ON DELETE CASCADE,
  resource_id text NOT NULL, href text NOT NULL, media_type text NOT NULL, object_key text NOT NULL, spine_position integer,
  checksum_sha256 text NOT NULL, content_length bigint NOT NULL, UNIQUE(content_version_id, resource_id), UNIQUE(content_version_id, href)
);
CREATE TABLE operations (
  id uuid PRIMARY KEY, kind text NOT NULL, status text NOT NULL DEFAULT 'PENDING' CHECK(status IN ('PENDING','RUNNING','COMPLETED','FAILED')),
  progress integer NOT NULL DEFAULT 0 CHECK(progress BETWEEN 0 AND 100), result jsonb, error_code text,
  created_by uuid REFERENCES accounts(id), created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE jobs (
  id uuid PRIMARY KEY, kind text NOT NULL, payload jsonb NOT NULL, status text NOT NULL DEFAULT 'PENDING' CHECK(status IN ('PENDING','RUNNING','COMPLETED','DEAD')),
  attempts integer NOT NULL DEFAULT 0, max_attempts integer NOT NULL DEFAULT 8, run_after timestamptz NOT NULL DEFAULT now(),
  locked_at timestamptz, locked_by text, last_error text, created_at timestamptz NOT NULL DEFAULT now(), completed_at timestamptz
);
CREATE INDEX jobs_claim_idx ON jobs(status, run_after) WHERE status = 'PENDING';
CREATE TABLE entitlements (
  id uuid PRIMARY KEY, reader_id uuid NOT NULL REFERENCES accounts(id), book_id uuid NOT NULL REFERENCES books(id),
  status text NOT NULL DEFAULT 'ACTIVE' CHECK(status IN ('ACTIVE','REVOKED')), source text NOT NULL CHECK(source IN ('PURCHASE','PREBOOK','DEVELOPMENT_SEED')),
  transaction_id uuid, granted_at timestamptz NOT NULL DEFAULT now(), revoked_at timestamptz, UNIQUE(reader_id, book_id)
);
CREATE TABLE checkouts (
  id uuid PRIMARY KEY, reader_id uuid NOT NULL REFERENCES accounts(id), book_id uuid REFERENCES books(id), device_id uuid REFERENCES devices(id),
  purpose text NOT NULL CHECK(purpose IN ('BOOK_PURCHASE','PREBOOK','DEVICE_TRANSFER')), provider text NOT NULL,
  status text NOT NULL DEFAULT 'CREATED' CHECK(status IN ('CREATED','PENDING','VERIFIED','FAILED','CANCELLED')),
  quoted_amount_minor bigint NOT NULL, currency char(3) NOT NULL, product_id text, provider_reference text,
  created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE transactions (
  id uuid PRIMARY KEY, reader_id uuid REFERENCES accounts(id), book_id uuid REFERENCES books(id), checkout_id uuid REFERENCES checkouts(id),
  type text NOT NULL CHECK(type IN ('BOOK_PURCHASE','PREBOOK','DEVICE_TRANSFER')), amount_minor bigint NOT NULL, currency char(3) NOT NULL,
  discount_percent integer, status text NOT NULL CHECK(status IN ('PENDING','COMPLETED','FAILED','REFUNDED')),
  provider text NOT NULL, provider_reference text, test_record boolean NOT NULL DEFAULT false, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE prebooks (
  id uuid PRIMARY KEY, reader_id uuid NOT NULL REFERENCES accounts(id), book_id uuid NOT NULL REFERENCES books(id), transaction_id uuid NOT NULL REFERENCES transactions(id),
  amount_minor bigint NOT NULL, currency char(3) NOT NULL, discount_percent integer NOT NULL, status text NOT NULL CHECK(status IN ('ACTIVE','CONVERTED','REFUNDED')),
  created_at timestamptz NOT NULL DEFAULT now(), converted_at timestamptz, UNIQUE(reader_id, book_id)
);
CREATE TABLE device_changes (
  id uuid PRIMARY KEY, reader_id uuid NOT NULL REFERENCES accounts(id), from_device_id uuid REFERENCES devices(id), to_device_id uuid NOT NULL REFERENCES devices(id),
  transaction_id uuid NOT NULL REFERENCES transactions(id), sequence_number integer NOT NULL, fee_minor bigint NOT NULL, currency char(3) NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(reader_id, sequence_number)
);
CREATE TABLE reading_progress (
  reader_id uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE, book_id uuid NOT NULL REFERENCES books(id), href text NOT NULL, cfi text,
  progression double precision NOT NULL CHECK(progression BETWEEN 0 AND 1), displayed_page integer, displayed_page_count integer,
  progress_percent numeric(6,2) NOT NULL CHECK(progress_percent BETWEEN 0 AND 100), version integer NOT NULL DEFAULT 1,
  updated_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(reader_id, book_id)
);
CREATE TABLE bookmarks (
  reader_id uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE, book_id uuid NOT NULL REFERENCES books(id), href text NOT NULL, cfi text,
  progression double precision NOT NULL CHECK(progression BETWEEN 0 AND 1), displayed_page integer, displayed_page_count integer,
  created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(reader_id, book_id)
);
CREATE TABLE reading_sessions (
  id uuid PRIMARY KEY, reader_id uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE, book_id uuid NOT NULL REFERENCES books(id),
  device_id uuid NOT NULL REFERENCES devices(id), content_version_id uuid NOT NULL REFERENCES book_content_versions(id), expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE offline_licenses (
  id uuid PRIMARY KEY, reader_id uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE, book_id uuid NOT NULL REFERENCES books(id),
  device_id uuid NOT NULL REFERENCES devices(id), content_version_id uuid NOT NULL REFERENCES book_content_versions(id),
  issued_at timestamptz NOT NULL DEFAULT now(), expires_at timestamptz NOT NULL, revoked_at timestamptz
);
CREATE INDEX offline_license_lookup ON offline_licenses(reader_id, book_id, device_id, expires_at DESC);
CREATE TABLE downloads (
  reader_id uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE, book_id uuid NOT NULL REFERENCES books(id), content_version_id uuid NOT NULL REFERENCES book_content_versions(id),
  license_id uuid NOT NULL REFERENCES offline_licenses(id), downloaded_at timestamptz NOT NULL DEFAULT now(), removed_at timestamptz,
  PRIMARY KEY(reader_id, book_id)
);
CREATE TABLE feedback (
  id uuid PRIMARY KEY, reader_id uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE, book_id uuid NOT NULL REFERENCES books(id),
  text text NOT NULL CHECK(char_length(text) BETWEEN 1 AND 5000), created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(reader_id, book_id)
);
CREATE TABLE youtube_assets (
  book_id uuid PRIMARY KEY REFERENCES books(id) ON DELETE CASCADE, song_name text NOT NULL, youtube_url text NOT NULL,
  status text NOT NULL DEFAULT 'ACTIVE' CHECK(status IN ('ACTIVE','PENDING','FAILED')), updated_at timestamptz NOT NULL DEFAULT now(), updated_by uuid REFERENCES accounts(id)
);
CREATE TABLE idempotency_records (
  actor_id uuid NOT NULL, scope text NOT NULL, key text NOT NULL, request_hash text NOT NULL, response_status integer, response_body jsonb,
  state text NOT NULL DEFAULT 'PROCESSING' CHECK(state IN ('PROCESSING','COMPLETED','FAILED')), expires_at timestamptz NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(actor_id, scope, key)
);
CREATE TABLE audit_events (
  id uuid PRIMARY KEY, actor_id uuid, actor_role text, action text NOT NULL, target_type text, target_id uuid,
  request_id text, metadata jsonb NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE account_deletion_requests (
  id uuid PRIMARY KEY, account_id uuid REFERENCES accounts(id), normalized_email text NOT NULL, token_hash bytea NOT NULL UNIQUE,
  status text NOT NULL DEFAULT 'PENDING' CHECK(status IN ('PENDING','CONFIRMED','PROCESSING','COMPLETED','EXPIRED')),
  expires_at timestamptz NOT NULL, confirmed_at timestamptz, completed_at timestamptz, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE storage_usage (id boolean PRIMARY KEY DEFAULT true CHECK(id), tracked_bytes bigint NOT NULL DEFAULT 0, reconciled_at timestamptz);

INSERT INTO app_settings(id,welcome_message,thank_you_message,about_app) VALUES(true,'Welcome to MOSTLYVERS','Thank you for being here, for supporting my stories, and for trusting MOSTLYVERS.','MOSTLYVERS offers individually purchased digital books without a monthly subscription.');
INSERT INTO author_profile(id) VALUES(true);
INSERT INTO contact_settings(id) VALUES(true);
INSERT INTO storage_usage(id) VALUES(true);

-- +goose Down
DROP TABLE IF EXISTS storage_usage, account_deletion_requests, audit_events, idempotency_records, youtube_assets, feedback, downloads, offline_licenses, reading_sessions, bookmarks, reading_progress, device_changes, prebooks, transactions, checkouts, entitlements, jobs, operations, book_resources, book_content_versions, books, upload_parts, uploads, contact_settings, author_profile, app_settings, integrity_verifications, devices, rate_limits, one_time_tokens, auth_sessions, owner_profiles, reader_profiles, accounts CASCADE;
