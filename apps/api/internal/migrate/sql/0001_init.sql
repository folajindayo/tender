-- Tender: cash settlement layer
-- All monetary amounts are integer kobo (1 naira = 100 kobo). Never floats.

BEGIN;

-- ---------------------------------------------------------------- users

CREATE TABLE users (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    phone           text NOT NULL UNIQUE,
    display_name    text NOT NULL,
    avatar_emoji    text NOT NULL DEFAULT '🙂',
    city            text NOT NULL DEFAULT 'Lagos',
    lat             double precision,
    lng             double precision,
    -- reliability, 0..100. Drives matching priority and credit eligibility.
    trust_score     int  NOT NULL DEFAULT 50 CHECK (trust_score BETWEEN 0 AND 100),
    settled_count   int  NOT NULL DEFAULT 0,
    defaulted_count int  NOT NULL DEFAULT 0,
    -- Tier 1 instant credit. Everyone starts at zero and earns up.
    credit_limit_kobo bigint NOT NULL DEFAULT 0 CHECK (credit_limit_kobo >= 0),
    sending_frozen  boolean NOT NULL DEFAULT false,
    created_at      timestamptz NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------- accounts

-- user accounts:   available | escrow | obligation
-- system accounts: float | revenue | loss_reserve   (user_id IS NULL)
CREATE TYPE account_kind AS ENUM (
    'available', 'escrow', 'obligation', 'float', 'revenue', 'loss_reserve'
);

CREATE TABLE accounts (
    id       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id  uuid REFERENCES users(id) ON DELETE CASCADE,
    kind     account_kind NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT user_kinds  CHECK (
        (user_id IS NOT NULL AND kind IN ('available','escrow','obligation'))
        OR
        (user_id IS NULL     AND kind IN ('float','revenue','loss_reserve'))
    ),
    UNIQUE NULLS NOT DISTINCT (user_id, kind)
);

-- ---------------------------------------------------------------- ledger

-- Double entry. Every transaction's entries must sum to exactly zero.
-- Sign convention: positive credits the account, negative debits it.
--   user available  : positive = spendable balance
--   user escrow     : positive = funds locked pending a handover
--   user obligation : NEGATIVE = amount the user owes the platform
--   float           : positive = settlement capital held
CREATE TABLE ledger_entries (
    id          bigserial PRIMARY KEY,
    tx_id       uuid NOT NULL,
    account_id  uuid NOT NULL REFERENCES accounts(id),
    amount_kobo bigint NOT NULL CHECK (amount_kobo <> 0),
    reason      text NOT NULL,
    transfer_id uuid,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX ledger_entries_tx      ON ledger_entries (tx_id);
CREATE INDEX ledger_entries_account ON ledger_entries (account_id);
CREATE INDEX ledger_entries_transfer ON ledger_entries (transfer_id);

-- The core invariant, enforced by the database rather than by convention.
-- Deferred so a multi-statement transaction can build up both legs.
CREATE OR REPLACE FUNCTION assert_ledger_balanced() RETURNS trigger AS $fn$
DECLARE
    offending uuid;
    delta     bigint;
BEGIN
    SELECT tx_id, SUM(amount_kobo) INTO offending, delta
      FROM ledger_entries
     WHERE tx_id = NEW.tx_id
     GROUP BY tx_id
    HAVING SUM(amount_kobo) <> 0;

    IF FOUND THEN
        RAISE EXCEPTION
            'unbalanced ledger transaction %: entries sum to % kobo, must be 0',
            offending, delta;
    END IF;
    RETURN NULL;
END;
$fn$ LANGUAGE plpgsql;

CREATE CONSTRAINT TRIGGER ledger_must_balance
    AFTER INSERT ON ledger_entries
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION assert_ledger_balanced();

CREATE VIEW account_balances AS
    SELECT a.id AS account_id, a.user_id, a.kind,
           COALESCE(SUM(e.amount_kobo), 0)::bigint AS balance_kobo
      FROM accounts a
      LEFT JOIN ledger_entries e ON e.account_id = a.id
     GROUP BY a.id, a.user_id, a.kind;

COMMIT;
