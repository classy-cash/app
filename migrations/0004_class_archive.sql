-- A class can be archived at the end of the school year: it stays readable (history, reports) but nobody can
-- change it, and the administrator can restore it. Purely additive.
ALTER TABLE classes ADD COLUMN archived_at TEXT;
