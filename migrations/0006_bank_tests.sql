-- Diagnostic references for verifying the real bank-notification pipeline end to end. A row is NOT a payment: a
-- notification whose title carries the marker is intercepted before any transfer, payment or ledger logic runs, so
-- it can never settle an obligation or move a balance. Rows are kept after expiry or cancellation on purpose, so
-- a late or replayed notification is still recognised and swallowed instead of becoming an ordinary transfer.
-- Purely additive.
CREATE TABLE bank_tests (
    id              INTEGER PRIMARY KEY,
    class_id        INTEGER NOT NULL REFERENCES classes(id),
    marker          TEXT NOT NULL UNIQUE,
    expected_minor  INTEGER NOT NULL CHECK (expected_minor > 0),
    status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','success','cancelled')),
    created_by      INTEGER REFERENCES users(id),
    created_at      TEXT NOT NULL,
    expires_at      TEXT NOT NULL,
    cancelled_at    TEXT,
    -- best observation of a notification carrying the marker: the first one, upgraded once by the first parsed one
    event_id        TEXT,
    observed_at     TEXT,
    observed_minor  INTEGER,
    parsed          INTEGER NOT NULL DEFAULT 0 CHECK (parsed IN (0,1)),
    hits            INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_bank_tests_class ON bank_tests(class_id, created_at);
