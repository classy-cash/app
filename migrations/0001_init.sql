-- Classy Cash schema v1. Money is stored as integer grosze (minor units) in PLN.
-- Timestamps are UTC, RFC3339 text. Ids are INTEGER; every class-scoped query filters by class_id.

CREATE TABLE users (
    id            INTEGER PRIMARY KEY,
    email         TEXT NOT NULL UNIQUE COLLATE NOCASE,
    name          TEXT NOT NULL,
    phone         TEXT NOT NULL DEFAULT '',
    password_hash TEXT NOT NULL DEFAULT '',
    is_admin      INTEGER NOT NULL DEFAULT 0,
    session_epoch INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL,
    updated_at    TEXT,
    confirmed_at  TEXT
);

CREATE TABLE classes (
    id               INTEGER PRIMARY KEY,
    name             TEXT NOT NULL,
    school_name      TEXT NOT NULL DEFAULT '',
    blik_phone       TEXT NOT NULL DEFAULT '',
    transfer_account TEXT NOT NULL DEFAULT '',
    created_at       TEXT NOT NULL
);

CREATE TABLE memberships (
    id       INTEGER PRIMARY KEY,
    user_id  INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    class_id INTEGER NOT NULL REFERENCES classes(id) ON DELETE CASCADE,
    role     TEXT NOT NULL CHECK (role IN ('treasurer','president','vice_president','parent')),
    UNIQUE (user_id, class_id)
);
CREATE UNIQUE INDEX uniq_treasurer_per_class ON memberships(class_id) WHERE role = 'treasurer';

CREATE TABLE students (
    id         INTEGER PRIMARY KEY,
    class_id   INTEGER NOT NULL REFERENCES classes(id) ON DELETE CASCADE,
    first_name TEXT NOT NULL,
    last_name  TEXT NOT NULL,
    deleted_at TEXT
);
CREATE INDEX idx_students_class ON students(class_id, last_name, first_name);

CREATE TABLE student_parents (
    student_id INTEGER NOT NULL REFERENCES students(id) ON DELETE CASCADE,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (student_id, user_id)
);
CREATE INDEX idx_student_parents_user ON student_parents(user_id);

CREATE TABLE contributions (
    id           INTEGER PRIMARY KEY,
    class_id     INTEGER NOT NULL REFERENCES classes(id) ON DELETE CASCADE,
    title        TEXT NOT NULL,
    amount_minor INTEGER NOT NULL CHECK (amount_minor >= 0),
    due_at       TEXT,
    created_at   TEXT NOT NULL
);
CREATE INDEX idx_contributions_class ON contributions(class_id, due_at);

CREATE TABLE contribution_students (
    contribution_id INTEGER NOT NULL REFERENCES contributions(id) ON DELETE CASCADE,
    student_id      INTEGER NOT NULL REFERENCES students(id) ON DELETE CASCADE,
    PRIMARY KEY (contribution_id, student_id)
);

-- A payment is what a parent sends the treasurer: a payment code plus the expected amount.
-- status: pending | paid | failed | refunded | expired. The code is cleared once paid.
CREATE TABLE payments (
    id           INTEGER PRIMARY KEY,
    class_id     INTEGER NOT NULL REFERENCES classes(id) ON DELETE CASCADE,
    user_id      INTEGER NOT NULL REFERENCES users(id),
    amount_minor INTEGER NOT NULL CHECK (amount_minor >= 0),
    status       TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','paid','failed','refunded','expired')),
    code         TEXT,
    created_at   TEXT NOT NULL,
    paid_at      TEXT
);
CREATE UNIQUE INDEX uniq_payment_code ON payments(code) WHERE code IS NOT NULL;
CREATE INDEX idx_payments_class_status ON payments(class_id, status, created_at);
CREATE INDEX idx_payments_user ON payments(user_id);

-- A student's obligation. status: pending | partial | paid.
CREATE TABLE student_payments (
    id              INTEGER PRIMARY KEY,
    class_id        INTEGER NOT NULL REFERENCES classes(id) ON DELETE CASCADE,
    student_id      INTEGER NOT NULL REFERENCES students(id) ON DELETE CASCADE,
    payment_id      INTEGER REFERENCES payments(id) ON DELETE SET NULL,
    contribution_id INTEGER REFERENCES contributions(id) ON DELETE SET NULL,
    label           TEXT NOT NULL,
    amount_minor    INTEGER NOT NULL CHECK (amount_minor >= 0),
    status          TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','partial','paid')),
    created_at      TEXT NOT NULL,
    due_at          TEXT,
    paid_at         TEXT
);
CREATE INDEX idx_sp_class_status ON student_payments(class_id, status);
CREATE INDEX idx_sp_student ON student_payments(student_id);
CREATE INDEX idx_sp_payment ON student_payments(payment_id);
CREATE INDEX idx_sp_contribution ON student_payments(contribution_id);

CREATE TABLE expenses (
    id              INTEGER PRIMARY KEY,
    class_id        INTEGER NOT NULL REFERENCES classes(id) ON DELETE CASCADE,
    label           TEXT NOT NULL,
    description     TEXT,
    amount_minor    INTEGER NOT NULL CHECK (amount_minor >= 0),
    spent_at        TEXT NOT NULL,
    attachment_path TEXT,
    created_at      TEXT NOT NULL
);
CREATE INDEX idx_expenses_class ON expenses(class_id, spent_at);

-- A bank transfer as announced by the bank's notification. amount_minor is what the bank reported.
-- review_reason is set when the transfer must not be auto-matched (possible duplicate).
CREATE TABLE transfers (
    id               INTEGER PRIMARY KEY,
    class_id         INTEGER REFERENCES classes(id) ON DELETE CASCADE,
    event_id         TEXT,
    account_number   TEXT NOT NULL,
    sender           TEXT NOT NULL,
    title            TEXT NOT NULL,
    amount_minor     INTEGER NOT NULL,
    transferred_at   TEXT NOT NULL,
    payment_id       INTEGER REFERENCES payments(id) ON DELETE SET NULL,
    review_reason    TEXT,
    notified_at      TEXT,
    deleted_at       TEXT,
    created_at       TEXT NOT NULL
);
CREATE INDEX idx_transfers_payment ON transfers(payment_id);
CREATE INDEX idx_transfers_when ON transfers(transferred_at);
CREATE INDEX idx_transfers_history ON transfers(account_number, sender);

-- Inbox of authenticated webhook deliveries: transport-level idempotency and the quarantine view.
-- status: parsed | unrecognized | duplicate_review | rejected_auth
CREATE TABLE bank_events (
    id          TEXT PRIMARY KEY,
    received_at TEXT NOT NULL,
    recipient   TEXT NOT NULL,
    mail_from   TEXT NOT NULL,
    subject     TEXT NOT NULL,
    body        TEXT NOT NULL,
    status      TEXT NOT NULL,
    reason      TEXT NOT NULL DEFAULT '',
    transfer_id INTEGER REFERENCES transfers(id) ON DELETE SET NULL,
    created_at  TEXT NOT NULL
);
CREATE INDEX idx_bank_events_status ON bank_events(status, received_at);

-- Cash ledger: one row per money movement with a running balance, rebuilt deterministically.
CREATE TABLE ledger (
    id                   INTEGER PRIMARY KEY,
    class_id             INTEGER NOT NULL REFERENCES classes(id) ON DELETE CASCADE,
    tx_date              TEXT NOT NULL,
    kind                 TEXT NOT NULL CHECK (kind IN ('income','expense')),
    amount_minor         INTEGER NOT NULL,
    balance_after_minor  INTEGER NOT NULL,
    payment_id           INTEGER REFERENCES payments(id) ON DELETE CASCADE,
    expense_id           INTEGER REFERENCES expenses(id) ON DELETE CASCADE,
    created_at           TEXT NOT NULL
);
CREATE INDEX idx_ledger_class_date ON ledger(class_id, tx_date, id);

CREATE TABLE sessions (
    token_hash TEXT PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    epoch      INTEGER NOT NULL,
    class_id   INTEGER,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
);
CREATE INDEX idx_sessions_user ON sessions(user_id);

-- Single-use tokens: kind = login | reset.
CREATE TABLE auth_tokens (
    token_hash TEXT PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind       TEXT NOT NULL CHECK (kind IN ('login','reset')),
    expires_at TEXT NOT NULL,
    used_at    TEXT
);

-- Durable outgoing mail. idempotency_key is forwarded to the relay so a retry never sends twice.
-- status: pending | sent | failed | captured
CREATE TABLE outbox (
    id              INTEGER PRIMARY KEY,
    idempotency_key TEXT NOT NULL UNIQUE,
    to_email        TEXT NOT NULL,
    to_name         TEXT NOT NULL DEFAULT '',
    reply_to        TEXT NOT NULL DEFAULT '',
    subject         TEXT NOT NULL,
    html_body       TEXT NOT NULL,
    text_body       TEXT NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending',
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TEXT NOT NULL,
    last_error      TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL,
    sent_at         TEXT
);
CREATE INDEX idx_outbox_due ON outbox(status, next_attempt_at);

CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
