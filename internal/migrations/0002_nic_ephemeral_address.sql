-- +goose Up
-- Track the VPC Address resource id that kacho-compute created to auto-allocate
-- a NIC's internal IPv4 (when the user didn't pass a manual address). On
-- Instance.Delete (and RemoveOneToOneNat) compute deletes these ephemeral
-- Address resources via the kacho-vpc API. NULL/'' = no ephemeral Address
-- (manual IP, or SKIP_PEER_VALIDATION synthetic IP). The external (NAT) Address
-- id + ephemeral flag are kept inside the primary_v4_nat JSONB blob.
ALTER TABLE instance_network_interfaces
  ADD COLUMN primary_v4_address_id TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE instance_network_interfaces
  DROP COLUMN primary_v4_address_id;
