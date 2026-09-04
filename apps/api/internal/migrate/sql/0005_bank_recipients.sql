-- Recipients become bank accounts, and settlement becomes a real payout.
--
-- Until now a recipient was another Tender account picked from a dropdown,
-- which only worked because the list was seeded. Nobody sending money picks
-- their sister from a list of five. They type an account number, and they
-- expect to see the name on the account before they part with cash.
--
-- So a transfer now carries its destination: the account number, the bank, and
-- the name the bank returned for that account. Settlement ends in an actual
-- transfer to that account rather than an internal credit, which means the
-- recipient does not need Tender at all -- the far side of this product is
-- somebody's existing bank account.
--
-- Payouts are backed by a Fintava wallet. That wallet is the float: value
-- leaving the ledger and value leaving the wallet are the same movement, so
-- 'payable' exists to hold the gap between the two. Money sits in payable from
-- the moment a handover settles until the bank confirms the credit, which is
-- the only honest way to represent "we owe this, it has not landed yet".

BEGIN;
ALTER TYPE account_kind ADD VALUE IF NOT EXISTS 'payable';
COMMIT;

BEGIN;

ALTER TABLE accounts DROP CONSTRAINT user_kinds;
ALTER TABLE accounts ADD CONSTRAINT user_kinds CHECK (
    (user_id IS NOT NULL AND kind IN ('available','escrow','obligation'))
    OR
    (user_id IS NULL     AND kind IN ('float','revenue','loss_reserve','external','payable'))
);

-- ---------------------------------------------------------------- recipients

ALTER TABLE transfers ALTER COLUMN recipient_id DROP NOT NULL;

ALTER TABLE transfers
    ADD COLUMN recipient_account_number text,
    ADD COLUMN recipient_sort_code      text,
    ADD COLUMN recipient_account_name   text,
    ADD COLUMN recipient_bank_name      text;

-- The old rule was "sender and recipient differ". It has to allow a null
-- recipient now, and NULL <> x is NULL rather than false, which a CHECK
-- accepts -- so it is restated explicitly rather than relied upon.
ALTER TABLE transfers DROP CONSTRAINT transfers_check;
ALTER TABLE transfers ADD CONSTRAINT sender_is_not_recipient
    CHECK (recipient_id IS NULL OR sender_id <> recipient_id);

-- Exactly one kind of destination, never both and never neither.
ALTER TABLE transfers ADD CONSTRAINT one_destination CHECK (
    (recipient_id IS NOT NULL
        AND recipient_account_number IS NULL
        AND recipient_sort_code IS NULL)
    OR
    (recipient_id IS NULL
        AND recipient_account_number IS NOT NULL
        AND recipient_sort_code IS NOT NULL
        AND recipient_account_name IS NOT NULL)
);

-- ---------------------------------------------------------------- payouts

CREATE TYPE payout_state AS ENUM (
    'pending',    -- row created, nothing sent to the provider yet
    'submitting', -- a request is in flight; nothing else may send this one
    'unknown',    -- the request timed out: it may or may not have moved money
    'sent',       -- the provider accepted it, the bank has not confirmed
    'confirmed',  -- the money reached the account
    'failed'      -- the provider rejected it; no money moved
);

CREATE TABLE payouts (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),

    -- One payout per transfer, enforced here rather than in code. This single
    -- constraint is what stops a retry, a duplicate webhook, or two API
    -- machines racing from paying the same person twice.
    transfer_id    uuid NOT NULL UNIQUE REFERENCES transfers(id) ON DELETE CASCADE,

    account_number text   NOT NULL,
    sort_code      text   NOT NULL,
    account_name   text   NOT NULL,
    bank_name      text,
    amount_kobo    bigint NOT NULL CHECK (amount_kobo > 0),
    narration      text,

    state          payout_state NOT NULL DEFAULT 'pending',

    -- What the provider called it. Both are unique when present, so a replayed
    -- webhook cannot be mistaken for a second payout.
    provider_ref   text,
    provider_tx_id text,

    attempts       int  NOT NULL DEFAULT 0,
    last_error     text,

    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    submitted_at   timestamptz,
    settled_at     timestamptz
);

CREATE UNIQUE INDEX payouts_provider_ref   ON payouts (provider_ref)   WHERE provider_ref   IS NOT NULL;
CREATE UNIQUE INDEX payouts_provider_tx_id ON payouts (provider_tx_id) WHERE provider_tx_id IS NOT NULL;
CREATE INDEX payouts_needing_attention ON payouts (state, updated_at)
    WHERE state IN ('pending','submitting','unknown','sent');

-- ---------------------------------------------------------------- webhooks

-- Provider events are recorded before they are acted on, and the provider's own
-- id is unique, so a redelivery is recognised rather than applied twice.
-- Fintava retries for 72 hours, so redelivery is normal, not exceptional.
CREATE TABLE provider_events (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    provider      text NOT NULL DEFAULT 'fintava',
    event_type    text NOT NULL,
    provider_ref  text,
    payload       jsonb NOT NULL,
    received_at   timestamptz NOT NULL DEFAULT now(),
    processed_at  timestamptz,
    error         text,
    UNIQUE (provider, event_type, provider_ref)
);

CREATE INDEX provider_events_unprocessed ON provider_events (received_at)
    WHERE processed_at IS NULL;

COMMIT;

-- A transfer whose handover completed but whose bank payout bounced. The cash
-- is spent and the ledger has returned the value to the sender, so this is a
-- terminal state that still owes the sender an explanation, not a failure of
-- settlement.
BEGIN;
ALTER TYPE transfer_state ADD VALUE IF NOT EXISTS 'payout_failed';
COMMIT;
