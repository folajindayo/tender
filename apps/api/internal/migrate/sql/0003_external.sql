-- The ledger is a value-conservation ledger: every transaction sums to zero.
-- Value entering or leaving Tender (a bank deposit in, a withdrawal out) needs
-- a counter-account representing the world outside the system, or those
-- movements would have nothing to balance against.
--
-- A large negative 'external' balance is the expected steady state: it is the
-- mirror of all value currently held inside Tender.

BEGIN;
ALTER TYPE account_kind ADD VALUE IF NOT EXISTS 'external';
COMMIT;

BEGIN;
ALTER TABLE accounts DROP CONSTRAINT user_kinds;
ALTER TABLE accounts ADD CONSTRAINT user_kinds CHECK (
    (user_id IS NOT NULL AND kind IN ('available','escrow','obligation'))
    OR
    (user_id IS NULL     AND kind IN ('float','revenue','loss_reserve','external'))
);
COMMIT;
