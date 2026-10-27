-- Class council ("rada klasowa"): at most one chairperson (przewodniczący) and one deputy
-- (wiceprzewodniczący) per class, alongside the single treasurer from 0001.
CREATE UNIQUE INDEX uniq_president_per_class ON memberships(class_id) WHERE role = 'president';
CREATE UNIQUE INDEX uniq_vice_president_per_class ON memberships(class_id) WHERE role = 'vice_president';
