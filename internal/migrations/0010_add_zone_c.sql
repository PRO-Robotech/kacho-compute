-- +goose Up
-- Add ru-central1-c zone (used in VPC test suites for AddressPool/Subnet scope isolation).
INSERT INTO zones (id, region_id, status, name) VALUES ('ru-central1-c', 'ru-central1', 'UP', 'Russia Central 1 C')
ON CONFLICT (id) DO NOTHING;

-- +goose Down
DELETE FROM zones WHERE id = 'ru-central1-c';
