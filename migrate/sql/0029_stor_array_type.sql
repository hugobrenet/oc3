-- 2026-11-05: the type of an array, as its driver reports it (freenas, pure,
-- symmetrix...): the worker parsing the array feed refuses a report of an
-- array whose name another type holds, rather than mixing the rows of two
-- arrays. Empty for an array the historical collector inserted.
ALTER TABLE stor_array ADD COLUMN IF NOT EXISTS array_type varchar(32) DEFAULT NULL;
