-- Corrections without losing history:
--  * a collection can be closed (archived) and reopened;
--  * a manual expense is voided, not deleted, so the audit trail keeps the mistake and its reversal;
--  * payments remember whether the treasurer booked them by hand ('manual') and a short description, so a
--    mistaken manual income can be reversed (status 'refunded') and still be explained in the history.
ALTER TABLE contributions ADD COLUMN closed_at TEXT;
ALTER TABLE expenses ADD COLUMN voided_at TEXT;
ALTER TABLE payments ADD COLUMN source TEXT NOT NULL DEFAULT 'online' CHECK (source IN ('online','manual'));
ALTER TABLE payments ADD COLUMN note TEXT;
