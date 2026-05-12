"""Case-set для InternalHypervisorService (kacho-compute) — INTERNAL-ONLY ресурс.

Hypervisor — физический хост, на который kacho-compute размещает инстансы (эпик KAC-2).
Инфра-чувствительное: НЕТ публичной/tenant REST-поверхности — только InternalHypervisorService
на cluster-internal порту (9091), проброшен через api-gateway internal mux.

`internal_hypervisor_service.proto` НЕ имеет `google.api.http`-аннотаций → grpc-gateway
маппит RPC на дефолтный путь `POST /<fully-qualified-service>/<method>` с телом = request-message:
  - POST /kacho.cloud.compute.v1.InternalHypervisorService/RegisterHypervisor
  - POST /kacho.cloud.compute.v1.InternalHypervisorService/GetHypervisor
  - POST /kacho.cloud.compute.v1.InternalHypervisorService/ListHypervisors
  - POST /kacho.cloud.compute.v1.InternalHypervisorService/UpdateHypervisorState
  - POST /kacho.cloud.compute.v1.InternalHypervisorService/DeregisterHypervisor
(см. ../kacho-api-gateway/internal/restmux/mux.go + kacho-proto gen/.../internal_hypervisor_service.pb.gw.go).

Синхронные RPC (не Operation) — это инфра-реестр, не tenant-facing ресурс.
Каждый case даёт хосту явный `id` с суффиксом `{{runId}}` (RegisterHypervisor идемпотентен по id)
и дерегистрирует его за собой. Зона — seeded `ru-central1-a`.
"""

CASES = []

HV_SVC = "/kacho.cloud.compute.v1.InternalHypervisorService"
HV_REG = f"{HV_SVC}/RegisterHypervisor"
HV_GET = f"{HV_SVC}/GetHypervisor"
HV_LIST = f"{HV_SVC}/ListHypervisors"
HV_STATE = f"{HV_SVC}/UpdateHypervisorState"
HV_DEREG = f"{HV_SVC}/DeregisterHypervisor"

_ZONE = "ru-central1-a"
_CAP = {"vcpus": 64, "memoryBytes": 274877906944, "instances": 40}


def _dereg_step(name="cleanup-dereg"):
    """Best-effort deregister of the case's hypervisor (id = hvId env var)."""
    return Step(name=name, method="POST", path=HV_DEREG, body={"hypervisorId": "{{hvId}}"}, test_script=[])


# ===========================================================================
# HV-REG / GET / LIST
# ===========================================================================

CASES.append(Case(
    id="HV-REG-OK",
    title="RegisterHypervisor(zone_id, fqdn, node_index via server, capacity) → 200, возвращает Hypervisor с id, READY, capacity",
    classes=["CRUD"], priority="P1",
    steps=[
        Step(name="register", method="POST", path=HV_REG,
             body={"id": "nm-hv-reg-{{runId}}", "zoneId": _ZONE, "fqdn": "nm-hv-reg-{{runId}}.kacho.local",
                   "capacity": _CAP},
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('id present', () => pm.expect(j.id, JSON.stringify(j)).to.be.a('string').and.length.greaterThan(0));",
                          "pm.test('id echoed', () => pm.expect(j.id).to.eql('nm-hv-reg-' + pm.environment.get('runId')));",
                          "pm.test('zoneId matches', () => pm.expect(j.zoneId).to.eql('ru-central1-a'));",
                          "pm.test('state READY', () => pm.expect(j.state).to.eql('READY'));",
                          "pm.test('nodeIndex is set (uint32)', () => pm.expect(Number(j.nodeIndex)).to.be.a('number').and.at.least(0));",
                          "pm.test('capacity vcpus echoed', () => pm.expect(String(j.capacity && j.capacity.vcpus)).to.eql('64'));",
                          "pm.test('createdAt set', () => pm.expect(j.createdAt).to.be.a('string'));",
                          "pm.environment.set('hvId', j.id);"]),
        _dereg_step(),
    ],
))

CASES.append(Case(
    id="HV-GET-OK",
    title="RegisterHypervisor → GetHypervisor(id) → 200, same id/zone, state READY",
    classes=["CRUD"], priority="P1",
    steps=[
        Step(name="register", method="POST", path=HV_REG,
             body={"id": "nm-hv-get-{{runId}}", "zoneId": _ZONE, "capacity": _CAP},
             test_script=[*assert_status(200), "pm.environment.set('hvId', pm.response.json().id);"]),
        Step(name="get", method="POST", path=HV_GET, body={"hypervisorId": "{{hvId}}"},
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('id matches', () => pm.expect(j.id).to.eql(pm.environment.get('hvId')));",
                          "pm.test('zoneId matches', () => pm.expect(j.zoneId).to.eql('ru-central1-a'));",
                          "pm.test('state READY', () => pm.expect(j.state).to.eql('READY'));"]),
        _dereg_step(),
    ],
))

CASES.append(Case(
    id="HV-LIST-OK",
    title="RegisterHypervisor → ListHypervisors → 200, hypervisors[] содержит зарегистрированный id",
    classes=["CRUD"], priority="P1",
    steps=[
        Step(name="register", method="POST", path=HV_REG,
             body={"id": "nm-hv-lst-{{runId}}", "zoneId": _ZONE, "capacity": _CAP},
             test_script=[*assert_status(200), "pm.environment.set('hvId', pm.response.json().id);"]),
        Step(name="list", method="POST", path=HV_LIST, body={},
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('hypervisors is array', () => pm.expect(j.hypervisors || []).to.be.an('array'));",
                          "const ids = (j.hypervisors || []).map(h => h.id);",
                          "pm.test('contains registered hv', () => pm.expect(ids).to.include(pm.environment.get('hvId')));"]),
        Step(name="list-by-zone", method="POST", path=HV_LIST, body={"zoneId": _ZONE},
             test_script=[*assert_status(200),
                          "pm.test('zone filter — all in zone', () => (pm.response.json().hypervisors || []).forEach(h => pm.expect(h.zoneId).to.eql('ru-central1-a')));"]),
        _dereg_step(),
    ],
))


# ===========================================================================
# HV-STATE — state transition READY → CORDONED
# ===========================================================================

CASES.append(Case(
    id="HV-STATE-OK",
    title="UpdateHypervisorState(READY → CORDONED) → GetHypervisor показывает CORDONED",
    classes=["CRUD", "STATE"], priority="P1",
    steps=[
        Step(name="register", method="POST", path=HV_REG,
             body={"id": "nm-hv-st-{{runId}}", "zoneId": _ZONE, "capacity": _CAP},
             test_script=[*assert_status(200), "pm.environment.set('hvId', pm.response.json().id);",
                          "pm.test('starts READY', () => pm.expect(pm.response.json().state).to.eql('READY'));"]),
        Step(name="cordon", method="POST", path=HV_STATE, body={"hypervisorId": "{{hvId}}", "state": "CORDONED"},
             test_script=[*assert_status(200),
                          "pm.test('state CORDONED', () => pm.expect(pm.response.json().state).to.eql('CORDONED'));"]),
        Step(name="get", method="POST", path=HV_GET, body={"hypervisorId": "{{hvId}}"},
             test_script=[*assert_status(200),
                          "pm.test('persisted CORDONED', () => pm.expect(pm.response.json().state).to.eql('CORDONED'));"]),
        _dereg_step(),
    ],
))


# ===========================================================================
# HV-DEREG — deregister → subsequent Get → NotFound
# ===========================================================================

CASES.append(Case(
    id="HV-DEREG-OK",
    title="DeregisterHypervisor → 200; затем GetHypervisor(id) → NotFound",
    classes=["CRUD"], priority="P1",
    steps=[
        Step(name="register", method="POST", path=HV_REG,
             body={"id": "nm-hv-dr-{{runId}}", "zoneId": _ZONE, "capacity": _CAP},
             test_script=[*assert_status(200), "pm.environment.set('hvId', pm.response.json().id);"]),
        Step(name="dereg", method="POST", path=HV_DEREG, body={"hypervisorId": "{{hvId}}"},
             test_script=[*assert_status(200)]),
        Step(name="get-after-dereg", method="POST", path=HV_GET, body={"hypervisorId": "{{hvId}}"},
             test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")]),
    ],
))


# ===========================================================================
# HV-REG-NEG — invalid input
# ===========================================================================

CASES.append(Case(
    id="HV-REG-NEG-EMPTY-ZONE",
    title="RegisterHypervisor с пустым zone_id → 400 INVALID_ARGUMENT",
    classes=["NEG", "VAL"], priority="P1",
    steps=[Step(name="reg-no-zone", method="POST", path=HV_REG,
                body={"id": "nm-hv-noz-{{runId}}", "capacity": _CAP},
                test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")])],
))

CASES.append(Case(
    id="HV-REG-NEG-BAD-ZONE",
    title="RegisterHypervisor с несуществующим zone_id → InvalidArgument/FailedPrecondition (3/9), без 500",
    classes=["NEG"], priority="P1",
    steps=[Step(name="reg-bad-zone", method="POST", path=HV_REG,
                body={"id": "nm-hv-badz-{{runId}}", "zoneId": "ru-central1-zzz", "capacity": _CAP},
                test_script=["pm.test('not 500', () => pm.expect(pm.response.code).to.not.eql(500));",
                             "pm.test('rejected 400 (or 200 if zone not validated — then cleanup)', () => pm.expect(pm.response.code).to.be.oneOf([400, 200]));",
                             "if (pm.response.code === 400) { const j = pm.response.json(); pm.test('grpc code 3 or 9', () => pm.expect(j.code, JSON.stringify(j)).to.be.oneOf([3, 9])); }",
                             "if (pm.response.code === 200) { pm.environment.set('hvId', pm.response.json().id); }"]),
           Step(name="cleanup-if-created", method="POST", path=HV_DEREG, body={"hypervisorId": "{{hvId}}"}, test_script=[])],
))

CASES.append(Case(
    id="HV-GET-NEG-NOTFOUND",
    title="GetHypervisor с несуществующим id → 404 NOT_FOUND",
    classes=["NEG"], priority="P1",
    steps=[Step(name="get-nx", method="POST", path=HV_GET, body={"hypervisorId": "nm-hv-nonexistent-zzz"},
                test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")])],
))


# ===========================================================================
# HV-NO-PUBLIC-PATH — Hypervisor НЕ имеет публичной/tenant REST-поверхности
# ===========================================================================
# Замечание: единственный REST-путь Hypervisor — internal-mux grpc-gateway-дефолт
# (POST /kacho.cloud.compute.v1.InternalHypervisorService/...), exposed только на
# cluster-internal listener (9091), НЕ на external TLS endpoint. REST-st'ового
# `/compute/v1/hypervisors` (как у Disk/Image/Instance) не существует — Hypervisor
# инфра-чувствительный (workspace CLAUDE.md §«Инфра-чувствительные данные»).

CASES.append(Case(
    id="HV-NO-PUBLIC-LIST-PATH",
    title="GET /compute/v1/hypervisors → 404 (нет публичного ListHypervisors — infra-sensitive)",
    classes=["NEG", "SEC"], priority="P1",
    steps=[Step(name="public-list", method="GET", path="/compute/v1/hypervisors",
                test_script=["pm.test('no public hypervisors path — 404', () => pm.expect(pm.response.code).to.eql(404));"])],
))

CASES.append(Case(
    id="HV-NO-PUBLIC-GET-PATH",
    title="GET /compute/v1/hypervisors/<id> → 404 (нет публичного GetHypervisor — infra-sensitive)",
    classes=["NEG", "SEC"], priority="P2",
    steps=[Step(name="public-get", method="GET", path="/compute/v1/hypervisors/nm-hv-any",
                test_script=["pm.test('no public hypervisor path — 404', () => pm.expect(pm.response.code).to.eql(404));"])],
))
