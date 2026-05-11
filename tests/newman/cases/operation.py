"""Case-set для OperationService (kacho-compute) — Get / Cancel.

Все compute-операции имеют prefix `epd` (PrefixOperationCompute == PrefixInstance);
api-gateway OpsProxy маршрутизирует /operations/{id} по первым 3 символам id → backend `compute`.
Кейсы спроектированы под verbatim YC (Operation API).
"""

CASES = []

DISKS = "/compute/v1/disks"
_DISK_SIZE = 10737418240


CASES.append(Case(
    id="OP-GET-CRUD-OK",
    title="Get свежесозданной operation (после Disk.Create) → done=true, has response, metadata.diskId",
    classes=["CRUD"], priority="P1",
    steps=[
        Step(name="create-trigger", method="POST", path=DISKS,
             body={"folderId": "{{_suiteFolderId}}", "name": "disk-opget-{{runId}}",
                   "zoneId": "{{existingZoneId}}", "size": _DISK_SIZE},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.diskId", "diskId")]),
        poll_operation_until_done(),
        Step(name="get-op", method="GET", path="/operations/{{opId}}",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('id matches & epd prefix', () => { pm.expect(j.id).to.eql(pm.environment.get('opId')); pm.expect(j.id).to.match(/^epd/); });",
                          "pm.test('done=true', () => pm.expect(j.done).to.eql(true));",
                          "pm.test('has response (no error)', () => { pm.expect(j.response).to.be.an('object'); pm.expect(j.error).to.be.oneOf([undefined, null]); });",
                          "pm.test('metadata has diskId (epd...)', () => pm.expect(j.metadata && j.metadata.diskId).to.match(/^epd/));",
                          "pm.test('createdAt present', () => pm.expect(j.createdAt).to.be.a('string'));"]),
        Step(name="cleanup", method="DELETE", path=f"{DISKS}/{{{{diskId}}}}",
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

CASES.append(Case(
    id="OP-GET-CRUD-FAILED-OP",
    title="Get завершённой failed-operation (Disk.Create в garbage folder) → done=true, has error code 5",
    classes=["CRUD", "NEG"], priority="P1",
    steps=[
        # # requires peer-validation enabled
        Step(name="create-bad", method="POST", path=DISKS,
             body={"folderId": "{{garbageRmId}}", "name": "disk-opfail-{{runId}}",
                   "zoneId": "{{existingZoneId}}", "size": _DISK_SIZE},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="get-op", method="GET", path="/operations/{{opId}}",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('done=true', () => pm.expect(j.done).to.eql(true));",
                          "pm.test('has error (no response)', () => { pm.expect(j.error).to.be.an('object'); pm.expect(j.response).to.be.oneOf([undefined, null]); });",
                          "pm.test('error.code 5 NOT_FOUND', () => pm.expect(j.error.code).to.eql(5));",
                          "pm.test('error.message non-empty', () => pm.expect(j.error.message).to.be.a('string').and.length.greaterThan(0));"]),
    ],
))

CASES.append(Case(
    id="OP-GET-NEG-NOTFOUND-VALID-PREFIX",
    title="Get несуществующего opId с правильным epd-префиксом → 404 NOT_FOUND",
    classes=["NEG"], priority="P1",
    steps=[Step(name="get-nx", method="GET", path="/operations/{{garbageComputeId}}",
                test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")])],
))

CASES.append(Case(
    id="OP-GET-CONF-NF-TEXT",
    title="Get несуществующего epd-opId → текст содержит 'not found'",
    classes=["CONF", "NEG"], priority="P1",
    steps=[Step(name="get-nx", method="GET", path="/operations/{{garbageComputeId}}",
                test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND"),
                             # probe-needed: точный verbatim YC text — предполагаем "Operation <id> not found"
                             "pm.test('text mentions not found', () => pm.expect((pm.response.json().message || '').toLowerCase()).to.include('not found'));"])],
))

CASES.append(Case(
    id="OP-GET-NEG-UNKNOWN-PREFIX",
    title="Get opId без known 3-char prefix (OpsProxy: prefix не из {b1g,bpf,enp,epd}) → 400 InvalidArgument 'prefix'",
    classes=["NEG"], priority="P0",
    steps=[Step(name="get-garbage-prefix", method="GET", path="/operations/{{garbageId}}",
                # OpsProxy в api-gateway отвергает id без known 3-char prefix → 400 InvalidArgument
                test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT"),
                             "pm.test('mentions prefix', () => pm.expect((pm.response.json().message || '').toLowerCase()).to.include('prefix'));"])],
))

CASES.append(Case(
    id="OP-CANCEL-NEG-ALREADY-DONE",
    title="Cancel завершённой operation (Disk.Create уже done) → FailedPrecondition (или 200 idempotent)",
    classes=["NEG", "STATE"], priority="P1",
    steps=[
        Step(name="create-trigger", method="POST", path=DISKS,
             body={"folderId": "{{_suiteFolderId}}", "name": "disk-opcancel-{{runId}}",
                   "zoneId": "{{existingZoneId}}", "size": _DISK_SIZE},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.diskId", "diskId")]),
        poll_operation_until_done(),
        Step(name="cancel-done", method="POST", path="/operations/{{opId}}:cancel", body={},
             # probe-needed: YC поведение Cancel на done-op. Обычно FailedPrecondition; иногда idempotent 200 с уже-done op.
             test_script=["pm.test('FailedPrecondition (400+code9) or idempotent 200', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
                          "if (pm.response.code === 400) { pm.test('code 9 FAILED_PRECONDITION', () => pm.expect(pm.response.json().code).to.eql(9)); }",
                          "if (pm.response.code === 200) { pm.test('op still done', () => pm.expect(pm.response.json().done).to.eql(true)); }"]),
        Step(name="cleanup", method="DELETE", path=f"{DISKS}/{{{{diskId}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

CASES.append(Case(
    id="OP-CANCEL-NEG-NOTFOUND",
    title="Cancel несуществующего epd-opId → 404 NOT_FOUND",
    classes=["NEG"], priority="P1",
    steps=[Step(name="cancel-nx", method="POST", path="/operations/{{garbageComputeId}}:cancel", body={},
                test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")])],
))

CASES.append(Case(
    id="OP-CANCEL-NEG-UNKNOWN-PREFIX",
    title="Cancel opId без known prefix → 400 InvalidArgument 'prefix'",
    classes=["NEG"], priority="P2",
    steps=[Step(name="cancel-garbage", method="POST", path="/operations/{{garbageId}}:cancel", body={},
                test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")])],
))
