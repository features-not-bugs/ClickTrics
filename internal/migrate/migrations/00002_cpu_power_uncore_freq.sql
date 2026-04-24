-- +goose Up
ALTER TABLE cpu_power
    ADD COLUMN IF NOT EXISTS uncore_freq_hz UInt64 CODEC(T64, ZSTD(1));

-- +goose Down
ALTER TABLE cpu_power
    DROP COLUMN IF EXISTS uncore_freq_hz;
