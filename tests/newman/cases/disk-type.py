# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: BUSL-1.1

"""Case-set для DiskTypeService (kacho-compute) — read-only справочник.

Covered RPCs: Get, List. Seed: network-hdd, network-ssd, network-ssd-nonreplicated,
network-ssd-io-m3 (см. internal/migrations/0001_initial.sql). Кейсы спроектированы под verbatim YC.
"""

CASES = []

DT = "/compute/v1/diskTypes"
_SEEDED = ["network-hdd", "network-ssd", "network-ssd-nonreplicated", "network-ssd-io-m3"]


CASES.append(Case(
    id="DT-LST-CRUD-OK",
    title="List diskTypes → ≥4 типов, содержит network-ssd / network-hdd; у каждого zoneIds непустой",
    classes=["CRUD"], priority="P1",
    steps=[Step(name="list", method="GET", path=DT,
                test_script=[*assert_status(200),
                             "const j = pm.response.json();",
                             "pm.test('diskTypes is array', () => pm.expect(j.diskTypes || []).to.be.an('array'));",
                             "const ids = (j.diskTypes || []).map(x => x.id);",
                             "pm.test('at least 4 disk types', () => pm.expect(ids.length).to.be.at.least(4));",
                             "pm.test('contains network-ssd', () => pm.expect(ids).to.include('network-ssd'));",
                             "pm.test('contains network-hdd', () => pm.expect(ids).to.include('network-hdd'));",
                             "pm.test('each has non-empty zoneIds', () => (j.diskTypes || []).forEach(t => pm.expect((t.zoneIds || []).length, t.id).to.be.at.least(1)));"])],
))

CASES.append(Case(
    id="DT-GET-CRUD-OK",
    title="Get network-ssd → id == network-ssd, zoneIds содержит ru-central1-a",
    classes=["CRUD"], priority="P1",
    steps=[Step(name="get", method="GET", path=f"{DT}/{{{{existingDiskTypeId}}}}",
                test_script=[*assert_status(200),
                             "const j = pm.response.json();",
                             "pm.test('id matches', () => pm.expect(j.id).to.eql(pm.environment.get('existingDiskTypeId')));",
                             "pm.test('zoneIds non-empty', () => pm.expect((j.zoneIds || []).length).to.be.at.least(1));",
                             "pm.test('zoneIds contains existingZoneId', () => pm.expect(j.zoneIds || []).to.include(pm.environment.get('existingZoneId')));"])],
))

CASES.append(Case(
    id="DT-GET-CRUD-HDD-OK",
    title="Get network-hdd (другой seeded тип) → id matches",
    classes=["CRUD"], priority="P2",
    steps=[Step(name="get-hdd", method="GET", path=f"{DT}/network-hdd",
                test_script=[*assert_status(200),
                             "pm.test('id == network-hdd', () => pm.expect(pm.response.json().id).to.eql('network-hdd'));"])],
))

CASES.append(Case(
    id="DT-GET-NEG-NOTFOUND",
    title="Get garbage diskTypeId → 404 NOT_FOUND",
    classes=["NEG"], priority="P0",
    steps=[Step(name="get-nx", method="GET", path=f"{DT}/garbage-disk-type-xyz",
                test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")])],
))

CASES.append(Case(
    id="DT-GET-CONF-NF-TEXT",
    title="Get garbage diskTypeId → текст содержит 'not found'",
    classes=["CONF", "NEG"], priority="P1",
    steps=[Step(name="get-nx", method="GET", path=f"{DT}/garbage-disk-type-xyz",
                test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND"),
                             # probe-needed: точный verbatim YC text — предполагаем "Disk type <id> not found"
                             "pm.test('text mentions not found', () => pm.expect((pm.response.json().message || '').toLowerCase()).to.include('not found'));"])],
))

CASES.append(Case(
    id="DT-LST-BVA-PAGESIZE-1",
    title="List diskTypes pageSize=1 → ≤1 item",
    classes=["BVA", "PAGE"], priority="P2",
    steps=[Step(name="ps1", method="GET", path=f"{DT}?pageSize=1",
                test_script=[*assert_status(200),
                             "pm.test('at most 1 item', () => pm.expect((pm.response.json().diskTypes || []).length).to.be.at.most(1));"])],
))

CASES.append(Case(
    id="DT-LST-BVA-PAGESIZE-ZERO",
    title="List diskTypes pageSize=0 → default applied (200)",
    classes=["BVA", "PAGE"], priority="P2",
    steps=[Step(name="ps0", method="GET", path=f"{DT}?pageSize=0",
                test_script=[*assert_status(200)])],
))

CASES.append(Case(
    id="DT-LST-BVA-PAGESIZE-OVER-1001",
    title="List diskTypes pageSize=1001 → 400 InvalidArgument",
    classes=["BVA", "VAL"], priority="P1",
    steps=[Step(name="ps1001", method="GET", path=f"{DT}?pageSize=1001",
                test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")])],
))

CASES.append(Case(
    id="DT-LST-PAGE-TOKEN-GARBAGE",
    title="List diskTypes с garbage pageToken → 400 InvalidArgument или 200 (справочник мал)",
    classes=["PAGE", "VAL"], priority="P2",
    steps=[Step(name="bad-token", method="GET", path=f"{DT}?pageSize=2&pageToken=not-a-real-token",
                # probe-needed: возможно справочник игнорирует pageToken; allow 200|400
                test_script=["pm.test('200 or 400', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));"])],
))

CASES.append(Case(
    id="DT-CR-NEG-NOT-ALLOWED",
    title="POST /compute/v1/diskTypes (Create) → справочник read-only → 404/405/501",
    classes=["VAL", "NEG"], priority="P3",
    steps=[Step(name="cr-dt", method="POST", path=DT, body={"id": "newman-fake-type"},
                test_script=["pm.test('not allowed', () => pm.expect(pm.response.code).to.be.oneOf([404, 405, 501]));"])],
))
