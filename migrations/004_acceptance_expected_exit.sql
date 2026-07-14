ALTER TABLE benchmark_acceptance_checks
  ADD COLUMN expected_exit_code INTEGER NOT NULL DEFAULT 0;
