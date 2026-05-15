-- +goose Up
-- Упразднение Hypervisor (KAC-80/KAC-36): InternalHypervisorService удалён из proto,
-- inventory нод после kube-ovn перейдёт на k8s Node objects (управляется kube-ovn,
-- а не нашей таблицей `hypervisors`). Соответствующие repo/service/handler/domain
-- удалены в этом же PR.
DROP TABLE IF EXISTS hypervisor_node_index_free;
DROP SEQUENCE IF EXISTS hypervisor_node_index_seq;
DROP TABLE IF EXISTS hypervisors CASCADE;

-- +goose Down
-- Best-effort recreate — минимальная схема (zone_id + node_index UNIQUE) без
-- backfill: down-migration существует только для совместимости с goose down/redo
-- на dev-стендах. Боевые данные о гипервизорах после drop'а не восстанавливаются
-- (это by design, см. up-блок). После kube-ovn эта таблица не нужна — down здесь
-- НЕ источник истины и НЕ должен использоваться в production-ролл-бэке.
CREATE TABLE IF NOT EXISTS hypervisors (
  id                    TEXT        PRIMARY KEY,
  zone_id               TEXT        NOT NULL,
  node_index            integer     NOT NULL UNIQUE,
  fqdn                  TEXT        NOT NULL DEFAULT '',
  state                 TEXT        NOT NULL DEFAULT 'READY',
  capacity_vcpus        bigint      NOT NULL DEFAULT 0,
  capacity_memory_bytes bigint      NOT NULL DEFAULT 0,
  capacity_instances    bigint      NOT NULL DEFAULT 0,
  created_at            timestamptz NOT NULL DEFAULT now(),
  updated_at            timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS hypervisors_zone_idx ON hypervisors (zone_id);
CREATE SEQUENCE IF NOT EXISTS hypervisor_node_index_seq MINVALUE 0 START WITH 0 MAXVALUE 65535;
CREATE TABLE IF NOT EXISTS hypervisor_node_index_free (id integer PRIMARY KEY);
