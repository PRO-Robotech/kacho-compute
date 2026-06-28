-- Copyright (c) PRO-Robotech
-- SPDX-License-Identifier: BUSL-1.1

-- +goose Up
-- Hypervisor — физический хост, на котором kacho-compute размещает инстансы.
-- INTERNAL-ONLY ресурс (placement/инвентарь железа — инфра-чувствительное; см.
-- workspace CLAUDE.md §«Инфра-чувствительные данные»). node_index — последовательный
-- стабильный индекс узла (0,1,2,…), основа /48-SRv6-локатора в kacho-vpc-implement;
-- аллоцируется при регистрации, возвращается во free-list при дерегистрации.
CREATE TABLE hypervisors (
  id                    TEXT        PRIMARY KEY,
  zone_id               TEXT        NOT NULL,
  node_index            integer     NOT NULL UNIQUE,
  fqdn                  TEXT        NOT NULL DEFAULT '',
  state                 TEXT        NOT NULL DEFAULT 'READY',  -- READY | CORDONED | DRAINING | DOWN
  capacity_vcpus        bigint      NOT NULL DEFAULT 0,
  capacity_memory_bytes bigint      NOT NULL DEFAULT 0,
  capacity_instances    bigint      NOT NULL DEFAULT 0,
  created_at            timestamptz NOT NULL DEFAULT now(),
  updated_at            timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX hypervisors_zone_idx ON hypervisors (zone_id);

CREATE SEQUENCE hypervisor_node_index_seq MINVALUE 0 START WITH 0 MAXVALUE 65535;
CREATE TABLE hypervisor_node_index_free (id integer PRIMARY KEY);

-- +goose Down
DROP TABLE hypervisor_node_index_free;
DROP SEQUENCE hypervisor_node_index_seq;
DROP TABLE hypervisors;
