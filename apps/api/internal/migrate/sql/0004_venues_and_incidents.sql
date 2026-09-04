-- Settlement moves from "meet a stranger wherever they say" to "collect at a
-- registered agent's fixed, public premises".
--
-- Escrow is a financial control: it can guarantee nobody loses money, and it is
-- powerless against someone who takes the cash and simply never confirms. Before
-- this change that attack was not merely possible but free -- the match expired,
-- the attacker's escrow was returned, and they kept the notes. Two things close
-- it: the counterparty must be an accountable operator at a known shopfront, and
-- a reported incident freezes the funds instead of releasing them.

-- New enum values cannot be used in the transaction that creates them.
BEGIN;
ALTER TYPE match_state    ADD VALUE IF NOT EXISTS 'disputed';
ALTER TYPE transfer_state ADD VALUE IF NOT EXISTS 'disputed';
COMMIT;

BEGIN;

-- ---------------------------------------------------------------- venues

CREATE TYPE venue_kind AS ENUM ('agent', 'bank', 'filling_station', 'market_office');

-- A place a handover may happen: fixed, publicly known, and answerable to
-- somebody. Never a coordinate a user typed in for one transaction.
CREATE TABLE venues (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text NOT NULL,
    kind        venue_kind NOT NULL DEFAULT 'agent',
    address     text NOT NULL,
    lat         double precision NOT NULL,
    lng         double precision NOT NULL,
    -- The operator is accountable for what happens on these premises.
    operator_id uuid NOT NULL REFERENCES users(id),
    opens_at    time NOT NULL DEFAULT '08:00',
    closes_at   time NOT NULL DEFAULT '18:00',
    verified    boolean NOT NULL DEFAULT false,
    active      boolean NOT NULL DEFAULT true,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX venues_operator ON venues (operator_id);
CREATE INDEX venues_open ON venues (active, verified) WHERE active AND verified;

-- ---------------------------------------------------------------- cash-out requests

-- Requests are now anchored to a venue rather than to a point and a free-text
-- label the requester chose. Existing rows predate venues and cannot be
-- migrated, so they are dropped rather than guessed at.
DELETE FROM cashout_requests;

ALTER TABLE cashout_requests
    DROP COLUMN lat,
    DROP COLUMN lng,
    DROP COLUMN label,
    ADD COLUMN venue_id uuid NOT NULL REFERENCES venues(id);

CREATE INDEX cashout_venue ON cashout_requests (venue_id);

-- ---------------------------------------------------------------- incidents

CREATE TYPE incident_kind AS ENUM (
    'cash_taken',    -- handed over, counterparty never confirmed
    'wrong_amount',  -- the count did not match at the meeting
    'threatened',    -- a safety incident
    'no_show'        -- the other party never arrived
);

CREATE TYPE incident_status AS ENUM ('open', 'resolved', 'dismissed');

CREATE TABLE incidents (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    transfer_id  uuid NOT NULL REFERENCES transfers(id) ON DELETE CASCADE,
    match_id     uuid REFERENCES matches(id),
    reporter_id  uuid NOT NULL REFERENCES users(id),
    accused_id   uuid NOT NULL REFERENCES users(id),
    kind         incident_kind NOT NULL,
    detail       text NOT NULL DEFAULT '',
    -- Whether this report caused the counterparty's escrow to be held.
    froze_escrow boolean NOT NULL DEFAULT false,
    status       incident_status NOT NULL DEFAULT 'open',
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX incidents_transfer ON incidents (transfer_id);
CREATE INDEX incidents_accused  ON incidents (accused_id, status);
-- One live report per party per transfer; repeat presses must not stack.
CREATE UNIQUE INDEX incidents_one_open
    ON incidents (transfer_id, reporter_id) WHERE status = 'open';

-- ---------------------------------------------------------------- accountability

ALTER TABLE users
    ADD COLUMN suspended     boolean NOT NULL DEFAULT false,
    ADD COLUMN incident_count int    NOT NULL DEFAULT 0;

COMMIT;
