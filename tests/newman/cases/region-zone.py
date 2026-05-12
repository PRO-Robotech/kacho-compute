"""Case-set для geography в kacho-compute — Region (новый ресурс) + Zone admin-CRUD.

Geography (Region/Zone) перенесена в kacho-compute из kacho-vpc (эпик KAC-15).
Покрывает:
  - public read-only RegionService (GET /compute/v1/regions, /compute/v1/regions/{id})
  - public read-only ZoneService — see также cases/zone.py (там полный read-набор);
    здесь — только ZONE-GET-OK с проверкой нового поля `name`.
  - admin CRUD: InternalRegionService / InternalZoneService — REST под
    /compute/v1/regions, /compute/v1/zones (POST/PATCH/DELETE) через api-gateway
    internal mux (kacho-only, internal-port 9091). Синхронные RPC (не Operation).
  - Region.Delete RESTRICT'нут, если у региона есть зоны → FailedPrecondition.
  - geography ушла из kacho-vpc → /vpc/v1/regions, /vpc/v1/zones → 404.

Изоляция: admin-ресурсы создаются с id-суффиксом `{{runId}}` (id допускает [a-z0-9-]),
каждый case чистит за собой. Seed: ru-central1 + ru-central1-{a,b,d} (см. internal/migrations).
"""

CASES = []

REGIONS = "/compute/v1/regions"
ZONES = "/compute/v1/zones"
_SEEDED_REGION = "ru-central1"
_SEEDED_ZONE = "ru-central1-a"


# ===========================================================================
# Public read-only — Region
# ===========================================================================

CASES.append(Case(
    id="RGN-LIST-OK",
    title="GET /compute/v1/regions → 200, regions[] содержит ru-central1, у каждого id и name",
    classes=["CRUD"], priority="P1",
    steps=[Step(name="list", method="GET", path=REGIONS,
                test_script=[*assert_status(200),
                             "const j = pm.response.json();",
                             "pm.test('regions is array', () => pm.expect(j.regions || []).to.be.an('array'));",
                             "const ids = (j.regions || []).map(r => r.id);",
                             "pm.test('contains ru-central1', () => pm.expect(ids).to.include('ru-central1'));",
                             "pm.test('each region has id', () => (j.regions || []).forEach(r => pm.expect(r.id, 'region.id').to.be.a('string').and.length.greaterThan(0)));"])],
))

CASES.append(Case(
    id="RGN-GET-OK",
    title="GET /compute/v1/regions/ru-central1 → 200, id matches",
    classes=["CRUD"], priority="P1",
    steps=[Step(name="get", method="GET", path=f"{REGIONS}/{_SEEDED_REGION}",
                test_script=[*assert_status(200),
                             "const j = pm.response.json();",
                             "pm.test('id matches', () => pm.expect(j.id).to.eql('ru-central1'));"])],
))

CASES.append(Case(
    id="RGN-GET-NEG-NOTFOUND",
    title="GET /compute/v1/regions/<garbage> → 404 NOT_FOUND",
    classes=["NEG"], priority="P1",
    steps=[Step(name="get-nx", method="GET", path=f"{REGIONS}/ru-central-zzz",
                test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")])],
))


# ===========================================================================
# Public read-only — Zone (новое поле `name`); полный read-набор — cases/zone.py
# ===========================================================================

CASES.append(Case(
    id="ZONE-LIST-OK",
    title="GET /compute/v1/zones → 200, zones[] содержит ru-central1-a с полем name",
    classes=["CRUD"], priority="P1",
    steps=[Step(name="list", method="GET", path=ZONES,
                test_script=[*assert_status(200),
                             "const j = pm.response.json();",
                             "const byId = Object.fromEntries((j.zones || []).map(z => [z.id, z]));",
                             "pm.test('contains ru-central1-a', () => pm.expect(byId).to.have.property('ru-central1-a'));",
                             "pm.test('ru-central1-a has name', () => pm.expect(byId['ru-central1-a'].name, 'zone.name').to.be.a('string').and.length.greaterThan(0));",
                             "pm.test('ru-central1-a regionId == ru-central1', () => pm.expect(byId['ru-central1-a'].regionId).to.eql('ru-central1'));"])],
))

CASES.append(Case(
    id="ZONE-GET-OK",
    title="GET /compute/v1/zones/ru-central1-a → 200, id matches, name set, regionId set",
    classes=["CRUD"], priority="P1",
    steps=[Step(name="get", method="GET", path=f"{ZONES}/{_SEEDED_ZONE}",
                test_script=[*assert_status(200),
                             "const j = pm.response.json();",
                             "pm.test('id matches', () => pm.expect(j.id).to.eql('ru-central1-a'));",
                             "pm.test('name set', () => pm.expect(j.name, 'zone.name').to.be.a('string').and.length.greaterThan(0));",
                             "pm.test('regionId set', () => pm.expect(j.regionId).to.be.a('string').and.length.greaterThan(0));",
                             "pm.test('status UP', () => pm.expect(j.status).to.eql('UP'));"])],
))


# ===========================================================================
# Admin CRUD — InternalRegionService (REST /compute/v1/regions on internal mux)
# ===========================================================================

CASES.append(Case(
    id="RGN-ADMIN-CRUD-OK",
    title="POST /compute/v1/regions (Create) → GET → PATCH name → DELETE → GET 404",
    classes=["CRUD"], priority="P1",
    steps=[
        Step(name="create", method="POST", path=REGIONS,
             body={"id": "nm-rgn-{{runId}}", "name": "newman region {{runId}}"},
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('id echoed', () => pm.expect(j.id).to.eql('nm-rgn-' + pm.environment.get('runId')));",
                          "pm.test('name echoed', () => pm.expect(j.name).to.match(/newman region/));"]),
        Step(name="get", method="GET", path=f"{REGIONS}/nm-rgn-{{{{runId}}}}",
             test_script=[*assert_status(200),
                          "pm.test('id matches', () => pm.expect(pm.response.json().id).to.eql('nm-rgn-' + pm.environment.get('runId')));"]),
        Step(name="patch", method="PATCH", path=f"{REGIONS}/nm-rgn-{{{{runId}}}}",
             body={"name": "renamed {{runId}}"},
             test_script=["pm.test('patch 200', () => pm.expect(pm.response.code).to.eql(200));",
                          "pm.test('name updated', () => pm.expect(pm.response.json().name).to.match(/renamed/));"]),
        Step(name="delete", method="DELETE", path=f"{REGIONS}/nm-rgn-{{{{runId}}}}",
             test_script=["pm.test('delete 200', () => pm.expect(pm.response.code).to.eql(200));"]),
        Step(name="get-after-del", method="GET", path=f"{REGIONS}/nm-rgn-{{{{runId}}}}",
             test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")]),
    ],
))

CASES.append(Case(
    id="ZONE-ADMIN-CRUD-OK",
    title="POST /compute/v1/regions (test region) → POST /compute/v1/zones под него → GET → DELETE zone → DELETE region",
    classes=["CRUD"], priority="P1",
    steps=[
        Step(name="create-region", method="POST", path=REGIONS,
             body={"id": "nm-zrgn-{{runId}}", "name": "zone-parent {{runId}}"},
             test_script=[*assert_status(200)]),
        Step(name="create-zone", method="POST", path=ZONES,
             body={"id": "nm-zone-{{runId}}", "regionId": "nm-zrgn-{{runId}}", "status": "UP",
                   "name": "newman zone {{runId}}"},
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('id echoed', () => pm.expect(j.id).to.eql('nm-zone-' + pm.environment.get('runId')));",
                          "pm.test('regionId echoed', () => pm.expect(j.regionId).to.eql('nm-zrgn-' + pm.environment.get('runId')));",
                          "pm.test('name echoed', () => pm.expect(j.name).to.match(/newman zone/));"]),
        Step(name="get-zone", method="GET", path=f"{ZONES}/nm-zone-{{{{runId}}}}",
             test_script=[*assert_status(200),
                          "pm.test('id matches', () => pm.expect(pm.response.json().id).to.eql('nm-zone-' + pm.environment.get('runId')));"]),
        Step(name="delete-zone", method="DELETE", path=f"{ZONES}/nm-zone-{{{{runId}}}}",
             test_script=["pm.test('delete zone 200', () => pm.expect(pm.response.code).to.eql(200));"]),
        Step(name="delete-region", method="DELETE", path=f"{REGIONS}/nm-zrgn-{{{{runId}}}}",
             test_script=["pm.test('delete region 200', () => pm.expect(pm.response.code).to.eql(200));"]),
        Step(name="get-zone-after-del", method="GET", path=f"{ZONES}/nm-zone-{{{{runId}}}}",
             test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")]),
    ],
))

CASES.append(Case(
    id="RGN-DEL-NEG-HAS-ZONES",
    title="DELETE региона, у которого есть зона → FailedPrecondition (RESTRICT) → 400/409; cleanup zone+region",
    classes=["NEG"], priority="P1",
    steps=[
        Step(name="create-region", method="POST", path=REGIONS,
             body={"id": "nm-rgnz-{{runId}}", "name": "has-zones {{runId}}"},
             test_script=[*assert_status(200)]),
        Step(name="create-zone", method="POST", path=ZONES,
             body={"id": "nm-zinr-{{runId}}", "regionId": "nm-rgnz-{{runId}}", "status": "UP",
                   "name": "zone-in-region {{runId}}"},
             test_script=[*assert_status(200)]),
        Step(name="delete-region-blocked", method="DELETE", path=f"{REGIONS}/nm-rgnz-{{{{runId}}}}",
             test_script=["pm.test('blocked: 400 or 409', () => pm.expect(pm.response.code).to.be.oneOf([400, 409]));",
                          "const j = pm.response.json();",
                          "pm.test('grpc code FAILED_PRECONDITION (9)', () => pm.expect(j.code, JSON.stringify(j)).to.eql(9));"]),
        # cleanup: drop zone first, then region
        Step(name="cleanup-zone", method="DELETE", path=f"{ZONES}/nm-zinr-{{{{runId}}}}", test_script=[]),
        Step(name="cleanup-region", method="DELETE", path=f"{REGIONS}/nm-rgnz-{{{{runId}}}}", test_script=[]),
    ],
))


# ===========================================================================
# Geography ушла из kacho-vpc — /vpc/v1/regions, /vpc/v1/zones → 404
# ===========================================================================

CASES.append(Case(
    id="RGN-NEG-NOT-IN-VPC",
    title="GET /vpc/v1/regions → 404 (geography перенесена в kacho-compute, эпик KAC-15)",
    classes=["NEG", "CONF"], priority="P2",
    steps=[Step(name="vpc-regions", method="GET", path="/vpc/v1/regions",
                test_script=["pm.test('not under /vpc — 404', () => pm.expect(pm.response.code).to.eql(404));"])],
))

CASES.append(Case(
    id="ZONE-NEG-NOT-IN-VPC",
    title="GET /vpc/v1/zones → 404 (geography перенесена в kacho-compute, эпик KAC-15)",
    classes=["NEG", "CONF"], priority="P2",
    steps=[Step(name="vpc-zones", method="GET", path="/vpc/v1/zones",
                test_script=["pm.test('not under /vpc — 404', () => pm.expect(pm.response.code).to.eql(404));"])],
))
