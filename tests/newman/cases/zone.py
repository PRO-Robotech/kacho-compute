"""Case-set для ZoneService (kacho-compute) — read-only справочник.

Covered RPCs: Get, List. Seed зеркалит kacho-vpc geography: ru-central1-a / -b / -d
(см. internal/migrations/0001_initial.sql; в YC compute зон может быть больше — поэтому
asserts на «содержит» и «≥3», а не на точное множество). Кейсы спроектированы под verbatim YC.
"""

CASES = []

ZONES = "/compute/v1/zones"
_SEEDED = ["ru-central1-a", "ru-central1-b", "ru-central1-d"]


CASES.append(Case(
    id="ZONE-LST-CRUD-OK",
    title="List zones → ≥3 зон, содержит ru-central1-{a,b,d}, у каждой status UP и regionId set",
    classes=["CRUD"], priority="P1",
    steps=[Step(name="list", method="GET", path=ZONES,
                test_script=[*assert_status(200),
                             "const j = pm.response.json();",
                             "pm.test('zones is array', () => pm.expect(j.zones || []).to.be.an('array'));",
                             "const ids = (j.zones || []).map(z => z.id);",
                             "pm.test('at least 3 zones', () => pm.expect(ids.length).to.be.at.least(3));",
                             "pm.test('contains ru-central1-a/b/d', () => { ['ru-central1-a','ru-central1-b','ru-central1-d'].forEach(z => pm.expect(ids, z).to.include(z)); });",
                             "pm.test('seeded zones status UP', () => (j.zones || []).filter(z => z.id.startsWith('ru-central1-')).forEach(z => pm.expect(z.status, z.id).to.eql('UP')));",
                             "pm.test('each has regionId', () => (j.zones || []).forEach(z => pm.expect(z.regionId, z.id).to.be.a('string').and.length.greaterThan(0)));"])],
))

CASES.append(Case(
    id="ZONE-GET-CRUD-OK",
    title="Get ru-central1-a → id matches, status UP, regionId == ru-central1",
    classes=["CRUD"], priority="P1",
    steps=[Step(name="get", method="GET", path=f"{ZONES}/{{{{existingZoneId}}}}",
                test_script=[*assert_status(200),
                             "const j = pm.response.json();",
                             "pm.test('id matches', () => pm.expect(j.id).to.eql(pm.environment.get('existingZoneId')));",
                             "pm.test('status UP', () => pm.expect(j.status).to.eql('UP'));",
                             "pm.test('regionId set', () => pm.expect(j.regionId).to.be.a('string').and.length.greaterThan(0));"])],
))

CASES.append(Case(
    id="ZONE-GET-CRUD-ALT-OK",
    title="Get ru-central1-b (другая seeded зона) → id matches, status UP",
    classes=["CRUD"], priority="P2",
    steps=[Step(name="get-alt", method="GET", path=f"{ZONES}/{{{{existingZoneAltId}}}}",
                test_script=[*assert_status(200),
                             "const j = pm.response.json();",
                             "pm.test('id matches', () => pm.expect(j.id).to.eql(pm.environment.get('existingZoneAltId')));",
                             "pm.test('status UP', () => pm.expect(j.status).to.eql('UP'));"])],
))

CASES.append(Case(
    id="ZONE-GET-NEG-NOTFOUND",
    title="Get garbage zoneId → 404 NOT_FOUND",
    classes=["NEG"], priority="P0",
    steps=[Step(name="get-nx", method="GET", path=f"{ZONES}/ru-central1-zzz",
                test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")])],
))

CASES.append(Case(
    id="ZONE-GET-CONF-NF-TEXT",
    title="Get garbage zoneId → текст содержит 'not found'",
    classes=["CONF", "NEG"], priority="P1",
    steps=[Step(name="get-nx", method="GET", path=f"{ZONES}/ru-central1-zzz",
                test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND"),
                             # probe-needed: точный verbatim YC text — предполагаем "Zone <id> not found"
                             "pm.test('text mentions not found', () => pm.expect((pm.response.json().message || '').toLowerCase()).to.include('not found'));"])],
))

CASES.append(Case(
    id="ZONE-LST-BVA-PAGESIZE-1",
    title="List zones pageSize=1 → ≤1 item",
    classes=["BVA", "PAGE"], priority="P2",
    steps=[Step(name="ps1", method="GET", path=f"{ZONES}?pageSize=1",
                test_script=[*assert_status(200),
                             "pm.test('at most 1 item', () => pm.expect((pm.response.json().zones || []).length).to.be.at.most(1));"])],
))

CASES.append(Case(
    id="ZONE-LST-BVA-PAGESIZE-ZERO",
    title="List zones pageSize=0 → default applied (200)",
    classes=["BVA", "PAGE"], priority="P2",
    steps=[Step(name="ps0", method="GET", path=f"{ZONES}?pageSize=0",
                test_script=[*assert_status(200)])],
))

CASES.append(Case(
    id="ZONE-LST-BVA-PAGESIZE-OVER-1001",
    title="List zones pageSize=1001 → 400 InvalidArgument",
    classes=["BVA", "VAL"], priority="P1",
    steps=[Step(name="ps1001", method="GET", path=f"{ZONES}?pageSize=1001",
                test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")])],
))

CASES.append(Case(
    id="ZONE-LST-PAGE-ROUNDTRIP",
    title="List zones pageSize=1 → nextPageToken → page 2 с ним → 200",
    classes=["PAGE", "BVA"], priority="P2",
    steps=[
        Step(name="p1", method="GET", path=f"{ZONES}?pageSize=1",
             test_script=[*assert_status(200),
                          "const tok = pm.response.json().nextPageToken || '';",
                          "pm.environment.set('zoneNextToken', tok);",
                          "pm.test('token is string', () => pm.expect(tok).to.be.a('string'));"]),
        Step(name="p2", method="GET", path=f"{ZONES}?pageSize=1&pageToken={{{{zoneNextToken}}}}",
             test_script=[*assert_status(200)]),
    ],
))

CASES.append(Case(
    id="ZONE-CR-NEG-NOT-ALLOWED",
    title="POST /compute/v1/zones (Create) → справочник read-only → 404/405/501",
    classes=["VAL", "NEG"], priority="P3",
    steps=[Step(name="cr-zone", method="POST", path=ZONES, body={"id": "newman-fake-zone"},
                # api-gateway routes POST /compute/v1/zones to InternalZoneService.Create (internal mux).
                # Authz rejects unauthenticated call → 400 (INVALID_ARGUMENT) or 403/404/405/501.
                test_script=["pm.test('not allowed', () => pm.expect(pm.response.code).to.be.oneOf([400, 403, 404, 405, 501]));"])],
))
