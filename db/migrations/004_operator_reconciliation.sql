ALTER TABLE publications ADD COLUMN operator_reconcile_mode text NOT NULL DEFAULT ''
 CHECK(operator_reconcile_mode IN ('','complete','restore-applied'));
