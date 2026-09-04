-- Accounts people can actually sign into.
--
-- Until now a "user" was a seeded persona picked from a dropdown, which is why
-- the app showed a list of strangers to send money to. Real accounts need a
-- credential, so this adds email and a password hash, and a table of sessions.
--
-- Email is nullable because the seeded operator accounts -- the venues that
-- make matching work -- are not people who sign in. They are part of the
-- network, not customers, and forcing a credential onto them would mean
-- inventing one.
--
-- No email verification, by decision: nothing in Tender is gated on reaching an
-- inbox, and an unverified address is still a usable account handle.

BEGIN;

ALTER TABLE users
    ADD COLUMN email         text,
    ADD COLUMN password_hash text;

-- Case-insensitive uniqueness: nobody should be able to register the same
-- address twice by changing its capitalisation.
CREATE UNIQUE INDEX users_email_unique ON users (lower(email)) WHERE email IS NOT NULL;

-- An account either has both halves of a credential or neither. A row with an
-- email and no hash would be an account nobody can sign into and nobody else
-- can register.
ALTER TABLE users ADD CONSTRAINT credential_complete CHECK (
    (email IS NULL AND password_hash IS NULL)
    OR
    (email IS NOT NULL AND password_hash IS NOT NULL)
);

-- Sessions store a hash of the token, never the token itself. Anyone who reads
-- this table -- a backup, a log, a leaked dump -- gets nothing they can sign in
-- with, the same reason the password column holds a hash.
CREATE TABLE sessions (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash text NOT NULL UNIQUE,
    user_agent text,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz
);

CREATE INDEX sessions_live ON sessions (user_id) WHERE revoked_at IS NULL;

COMMIT;
