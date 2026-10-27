-- An obligation can carry an individual amount (sibling discount, partial exemption). The flag keeps a later
-- edit of the collection's amount from overwriting it. Purely additive.
ALTER TABLE student_payments ADD COLUMN amount_override INTEGER NOT NULL DEFAULT 0 CHECK (amount_override IN (0,1));
