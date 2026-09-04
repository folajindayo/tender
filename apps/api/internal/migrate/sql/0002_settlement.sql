-- Transfers, cash pledges, the note registry, and the matching book.

BEGIN;

-- ---------------------------------------------------------------- transfers

CREATE TYPE transfer_mode AS ENUM ('escrow', 'credit');

CREATE TYPE transfer_state AS ENUM (
    'draft',             -- created, no cash pledged yet
    'pledged',           -- cash photographed and accepted, awaiting a match
    'credited',          -- tier 1 only: recipient already paid from float
    'matched',           -- counterparty found, their funds locked in escrow
    'handover_pending',  -- one side has confirmed the physical handover
    'settled',           -- both confirmed; value delivered
    'expired',           -- nobody showed up; escrow released, nobody lost money
    'rejected',          -- counterparty refused the notes at handover
    'voided',            -- cancelled before matching
    'defaulted'          -- tier 1 only: credit extended, never settled
);

CREATE TABLE transfers (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    ref           bigserial NOT NULL UNIQUE,          -- human-facing "#4471"
    sender_id     uuid NOT NULL REFERENCES users(id),
    recipient_id  uuid NOT NULL REFERENCES users(id),
    amount_kobo   bigint NOT NULL CHECK (amount_kobo > 0),
    fee_kobo      bigint NOT NULL DEFAULT 0 CHECK (fee_kobo >= 0),
    mode          transfer_mode  NOT NULL DEFAULT 'escrow',
    state         transfer_state NOT NULL DEFAULT 'draft',
    note          text,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz,
    settled_at    timestamptz,
    CHECK (sender_id <> recipient_id),
    CHECK (fee_kobo < amount_kobo)
);

CREATE INDEX transfers_sender    ON transfers (sender_id, created_at DESC);
CREATE INDEX transfers_recipient ON transfers (recipient_id, created_at DESC);
CREATE INDEX transfers_open      ON transfers (state, expires_at)
    WHERE state IN ('pledged','credited','matched','handover_pending');

-- ---------------------------------------------------------------- pledges

-- One photograph of physical cash, and what the vision layer made of it.
-- This is evidence and a fraud signal. It is NOT collateral and never secures value.
CREATE TABLE pledges (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    transfer_id        uuid NOT NULL REFERENCES transfers(id) ON DELETE CASCADE,
    image_sha256       text NOT NULL,
    declared_kobo      bigint NOT NULL,
    detected_kobo      bigint NOT NULL,
    confidence         real   NOT NULL DEFAULT 0,
    screen_replay      boolean NOT NULL DEFAULT false,
    photocopy_suspected boolean NOT NULL DEFAULT false,
    accepted           boolean NOT NULL DEFAULT false,
    rejection_reason   text,
    vision_mode        text NOT NULL,
    vision_raw         jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at         timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX pledges_transfer ON pledges (transfer_id);
-- The same photograph can never open two pledges at once.
CREATE UNIQUE INDEX pledges_image_active ON pledges (image_sha256) WHERE accepted;

-- ---------------------------------------------------------------- note registry

-- Individual banknotes claimed by a pledge. While a note is 'active' it cannot
-- appear in any other pledge -- this single constraint is the double-spend guard.
CREATE TYPE note_status AS ENUM ('active', 'released');

CREATE TABLE pledged_notes (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    pledge_id     uuid NOT NULL REFERENCES pledges(id) ON DELETE CASCADE,
    denomination_kobo bigint NOT NULL CHECK (denomination_kobo > 0),
    serial        text,          -- null when unreadable in the photo
    serial_confidence real NOT NULL DEFAULT 0,
    note_phash    text NOT NULL, -- perceptual hash, the fallback identifier
    status        note_status NOT NULL DEFAULT 'active',
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX pledged_notes_pledge ON pledged_notes (pledge_id);

-- A legible serial is the strong identifier...
CREATE UNIQUE INDEX pledged_notes_serial_active
    ON pledged_notes (serial)
    WHERE status = 'active' AND serial IS NOT NULL;

-- ...and the perceptual hash catches replays where the serial was unreadable.
CREATE UNIQUE INDEX pledged_notes_phash_active
    ON pledged_notes (note_phash)
    WHERE status = 'active';

-- ---------------------------------------------------------------- demand book

CREATE TYPE cashout_state AS ENUM ('open', 'matched', 'fulfilled', 'cancelled');

-- People holding digital naira who want physical notes. This is the supply of
-- settlement capacity, and the reason the platform never has to touch cash.
CREATE TABLE cashout_requests (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      uuid NOT NULL REFERENCES users(id),
    amount_kobo  bigint NOT NULL CHECK (amount_kobo > 0),
    -- how much flexibility they have on the amount, in kobo
    tolerance_kobo bigint NOT NULL DEFAULT 0 CHECK (tolerance_kobo >= 0),
    lat          double precision NOT NULL,
    lng          double precision NOT NULL,
    label        text NOT NULL DEFAULT '',
    state        cashout_state NOT NULL DEFAULT 'open',
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX cashout_open ON cashout_requests (state, amount_kobo) WHERE state = 'open';

-- ---------------------------------------------------------------- matches

CREATE TYPE match_state AS ENUM (
    'proposed', 'sender_confirmed', 'counterparty_confirmed',
    'completed', 'expired', 'rejected'
);

CREATE TABLE matches (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    transfer_id         uuid NOT NULL REFERENCES transfers(id) ON DELETE CASCADE,
    cashout_request_id  uuid NOT NULL REFERENCES cashout_requests(id),
    counterparty_id     uuid NOT NULL REFERENCES users(id),
    amount_kobo         bigint NOT NULL CHECK (amount_kobo > 0),
    handover_code       text NOT NULL,
    distance_m          int  NOT NULL DEFAULT 0,
    state               match_state NOT NULL DEFAULT 'proposed',
    sender_confirmed_at       timestamptz,
    counterparty_confirmed_at timestamptz,
    rejection_reason    text,
    expires_at          timestamptz NOT NULL,
    created_at          timestamptz NOT NULL DEFAULT now()
);

-- A transfer can only have one live match at a time.
CREATE UNIQUE INDEX matches_transfer_live ON matches (transfer_id)
    WHERE state IN ('proposed','sender_confirmed','counterparty_confirmed');

CREATE INDEX matches_counterparty ON matches (counterparty_id, state);
CREATE INDEX matches_expiry ON matches (expires_at)
    WHERE state IN ('proposed','sender_confirmed','counterparty_confirmed');

COMMIT;
