-- Copyright (c) PRO-Robotech
-- SPDX-License-Identifier: BUSL-1.1

-- +goose Up
-- Link a compute Instance NIC to the backing kacho-vpc NetworkInterface
-- resource (vpc.NetworkInterface, epic KAC-2). On Instance.Create compute
-- creates (or attaches an existing) kacho-vpc NIC per NIC spec and stores its
-- id here; on Instance.Delete it detaches+deletes that NIC (which releases the
-- NIC's Address resources). '' = legacy NIC (pre-KAC-9) or SKIP_PEER_VALIDATION
-- synthetic NIC with no kacho-vpc resource. The other denormalised columns
-- (subnet_id, primary_v4_address, security_group_ids) mirror the kacho-vpc NIC.
ALTER TABLE instance_network_interfaces
  ADD COLUMN nic_id TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE instance_network_interfaces
  DROP COLUMN nic_id;
