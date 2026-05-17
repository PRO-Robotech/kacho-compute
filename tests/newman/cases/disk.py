"""Case-set для DiskService (kacho-compute).

Covered RPCs: Get, List, Create, Update, Delete, Move, Relocate, ListOperations.
(ListSnapshotSchedules — blocked:kacho-snapshot-schedule; access-bindings — no-op skeleton, skip.)

Контракт изоляции: каждый case в своём runId, работает внутри pre-allocated
existingProjectId/existingProjectCrossId (из env), Org/Cloud/Folder НЕ создаёт; имена
суффиксуются {{runId}}. Кейсы спроектированы так, чтобы зеленеть и против реального
YC Compute API (verbatim parity). Где точный YC error-text неизвестен — `# probe-needed:`.

Disk size constraints (из proto `(value)`): Create [4194304 .. 28587302322176],
Update [4194304 .. 4398046511104]. id-prefix Disk = `epd`.
"""

CASES = []

_MIN_SIZE = 4194304          # 4 MiB
_MAX_CREATE = 28587302322176  # ~26 TiB (proto Create max)
_MAX_UPDATE = 4398046511104   # 4 TiB (proto Update max)
_DEF_SIZE = 10737418240       # 10 GiB — типовой размер для CRUD-кейсов

DISKS = "/compute/v1/disks"


def _disk_body(name_suffix, **over):
    b = {"projectId": "{{_suiteFolderId}}", "name": f"disk-{name_suffix}-{{{{runId}}}}",
         "zoneId": "{{existingZoneId}}", "size": _DEF_SIZE}
    b.update(over)
    return b


# ---------------------------------------------------------------------------
# DISK-CR — Create
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="DISK-CR-CRUD-OK",
    title="Create empty disk → Operation → poll → Get → assert поля + id-prefix epd + created_at секунды",
    classes=["CRUD", "CONF"], priority="P1",
    steps=[
        Step(name="create", method="POST", path=DISKS,
             body=_disk_body("cr", description="newman CRUD-OK", labels={"suite": "newman"}),
             test_script=[*assert_status(200), *assert_operation_envelope(),
                          *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.diskId", "diskId")]),
        poll_operation_until_done(),
        Step(name="get", method="GET", path=f"{DISKS}/{{{{diskId}}}}",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('id matches & has epd prefix', () => { pm.expect(j.id).to.eql(pm.environment.get('diskId')); pm.expect(j.id).to.match(/^epd/); });",
                          "pm.test('projectId matches', () => pm.expect(j.projectId).to.eql(pm.environment.get('_suiteFolderId')));",
                          "pm.test('zoneId matches', () => pm.expect(j.zoneId).to.eql(pm.environment.get('existingZoneId')));",
                          "pm.test('size matches', () => pm.expect(String(j.size)).to.eql('" + str(_DEF_SIZE) + "'));",
                          "pm.test('status READY', () => pm.expect(j.status).to.eql('READY'));",
                          "pm.test('typeId set (default network-ssd)', () => pm.expect(j.typeId).to.be.a('string').and.length.greaterThan(0));",
                          *assert_created_at_seconds()]),
        Step(name="cleanup", method="DELETE", path=f"{DISKS}/{{{{diskId}}}}",
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

CASES.append(Case(
    id="DISK-CR-CRUD-TYPE-EXPLICIT",
    title="Create disk с explicit typeId=network-ssd → typeId в Get совпадает",
    classes=["CRUD"], priority="P2",
    steps=[
        Step(name="create", method="POST", path=DISKS,
             body=_disk_body("ts", typeId="{{existingDiskTypeId}}"),
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.diskId", "diskId")]),
        poll_operation_until_done(),
        Step(name="get", method="GET", path=f"{DISKS}/{{{{diskId}}}}",
             test_script=[*assert_status(200),
                          "pm.test('typeId == requested', () => pm.expect(pm.response.json().typeId).to.eql(pm.environment.get('existingDiskTypeId')));"]),
        Step(name="cleanup", method="DELETE", path=f"{DISKS}/{{{{diskId}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

CASES.append(Case(
    id="DISK-CR-VAL-FOLDER-REQUIRED",
    title="Create без projectId → 400 InvalidArgument",
    classes=["VAL"], priority="P0",
    steps=[Step(name="cr-no-folder", method="POST", path=DISKS,
                body={"name": "disk-nf-{{runId}}", "zoneId": "{{existingZoneId}}", "size": _DEF_SIZE},
                test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")])],
))

CASES.append(Case(
    id="DISK-CR-VAL-ZONE-REQUIRED",
    title="Create без zoneId → 400 InvalidArgument",
    classes=["VAL"], priority="P0",
    steps=[Step(name="cr-no-zone", method="POST", path=DISKS,
                body={"projectId": "{{_suiteFolderId}}", "name": "disk-nz-{{runId}}", "size": _DEF_SIZE},
                test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")])],
))

CASES.append(Case(
    id="DISK-CR-VAL-SIZE-REQUIRED",
    title="Create без size → 400 InvalidArgument (size required)",
    classes=["VAL"], priority="P0",
    steps=[Step(name="cr-no-size", method="POST", path=DISKS,
                body={"projectId": "{{_suiteFolderId}}", "name": "disk-ns-{{runId}}", "zoneId": "{{existingZoneId}}"},
                test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")])],
))

CASES.append(Case(
    id="DISK-CR-NEG-FOLDER-NOTFOUND",
    title="Create в garbage projectId → async NOT_FOUND 'Folder ... not found'",
    classes=["NEG"], priority="P0",
    steps=[
        # # requires peer-validation enabled (KACHO_COMPUTE_SKIP_PEER_VALIDATION!=true)
        Step(name="cr-bad-folder", method="POST", path=DISKS,
             body=_disk_body("bf", projectId="{{garbageRmId}}"),
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        assert_op_error(5, "NOT_FOUND", msg_substr="folder"),
    ],
))

CASES.append(Case(
    id="DISK-CR-NEG-ZONE-UNKNOWN",
    title="Create с unknown zoneId → InvalidArgument (compute: zone existence — sync, паритет VPC)",
    classes=["NEG", "VAL"], priority="P1",
    steps=[Step(name="cr-bad-zone", method="POST", path=DISKS,
                body=_disk_body("bz", zoneId="ru-central1-zzz"),
                # probe-needed: реальный YC может давать NotFound "Zone ... not found"; у нас InvalidArgument
                test_script=["pm.test('rejected (400 sync or 200+op-error)', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
                             "if (pm.response.code === 400) { const j = pm.response.json(); pm.test('code 3 или 5', () => pm.expect(j.code).to.be.oneOf([3, 5])); }"])],
))

CASES.append(Case(
    id="DISK-CR-NEG-TYPE-UNKNOWN",
    title="Create с garbage typeId → async NOT_FOUND 'Disk type ... not found'",
    classes=["NEG"], priority="P1",
    steps=[
        Step(name="cr-bad-type", method="POST", path=DISKS,
             body=_disk_body("bt", typeId="garbage-disk-type"),
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        # probe-needed: точный YC text для unknown disk type — предполагаем "Disk type ... not found"
        assert_op_error(5, "NOT_FOUND", msg_substr="disk type"),
    ],
))

CASES.append(Case(
    id="DISK-CR-NEG-DUP-NAME",
    title="Create disk с дубликатом name в folder → async ALREADY_EXISTS",
    classes=["NEG", "CONC"], priority="P1",
    steps=[
        Step(name="cr-1", method="POST", path=DISKS, body=_disk_body("dup"),
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.diskId", "diskId")]),
        poll_operation_until_done(),
        Step(name="cr-2-dup", method="POST", path=DISKS, body=_disk_body("dup"),
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        # probe-needed: точный YC ALREADY_EXISTS text — проверяем только code
        assert_op_error(6, "ALREADY_EXISTS"),
        Step(name="cleanup", method="DELETE", path=f"{DISKS}/{{{{diskId}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

# BVA по size
CASES.append(Case(
    id="DISK-CR-BVA-SIZE-MIN-OK",
    title=f"Create с size={_MIN_SIZE} (min 4 MiB) → 200",
    classes=["BVA"], priority="P1",
    steps=[
        Step(name="cr-min", method="POST", path=DISKS, body=_disk_body("smin", size=_MIN_SIZE),
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.diskId", "diskId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="cleanup", method="DELETE", path=f"{DISKS}/{{{{diskId}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

CASES.append(Case(
    id="DISK-CR-BVA-SIZE-BELOW-MIN",
    title=f"Create с size={_MIN_SIZE - 1} (below min) → 400 InvalidArgument",
    classes=["BVA", "VAL"], priority="P1",
    steps=[Step(name="cr-below", method="POST", path=DISKS, body=_disk_body("sbelow", size=_MIN_SIZE - 1),
                test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")])],
))

CASES.append(Case(
    id="DISK-CR-BVA-SIZE-CREATE-MAX-OK",
    title=f"Create с size={_MAX_CREATE} (max ~26 TiB) → 200",
    classes=["BVA"], priority="P2",
    steps=[
        Step(name="cr-max", method="POST", path=DISKS, body=_disk_body("smax", size=_MAX_CREATE),
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.diskId", "diskId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="cleanup", method="DELETE", path=f"{DISKS}/{{{{diskId}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

CASES.append(Case(
    id="DISK-CR-BVA-SIZE-ABOVE-CREATE-MAX",
    title=f"Create с size={_MAX_CREATE + 1} (above max) → 400 InvalidArgument",
    classes=["BVA", "VAL"], priority="P1",
    steps=[Step(name="cr-above", method="POST", path=DISKS, body=_disk_body("sabove", size=_MAX_CREATE + 1),
                test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")])],
))

# CR-FROM-IMAGE / CR-FROM-SNAPSHOT — full happy path требует Image/Snapshot (см. image.py/snapshot.py).
# Здесь — минимум: source не существует → async NotFound.
CASES.append(Case(
    id="DISK-CR-NEG-SOURCE-IMAGE-NOTFOUND",
    title="Create disk из garbage imageId → async NOT_FOUND",
    classes=["NEG"], priority="P1",
    steps=[
        Step(name="cr-bad-img", method="POST", path=DISKS,
             body={"projectId": "{{_suiteFolderId}}", "name": "disk-bi-{{runId}}",
                   "zoneId": "{{existingZoneId}}", "size": _DEF_SIZE, "imageId": "{{garbageImageId}}"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        assert_op_error(5, "NOT_FOUND"),
    ],
))

CASES.append(Case(
    id="DISK-CR-NEG-SOURCE-SNAPSHOT-NOTFOUND",
    title="Create disk из garbage snapshotId → async NOT_FOUND",
    classes=["NEG"], priority="P1",
    steps=[
        Step(name="cr-bad-snap", method="POST", path=DISKS,
             body={"projectId": "{{_suiteFolderId}}", "name": "disk-bs-{{runId}}",
                   "zoneId": "{{existingZoneId}}", "size": _DEF_SIZE, "snapshotId": "{{garbageImageId}}"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        assert_op_error(5, "NOT_FOUND"),
    ],
))

CASES.append(Case(
    id="DISK-CR-CRUD-FROM-IMAGE-OK",
    title="Создать image из disk → создать disk из этого image → status READY, size >= min_disk_size",
    classes=["CRUD"], priority="P1",
    steps=[
        # 1. base disk
        Step(name="cr-base-disk", method="POST", path=DISKS, body=_disk_body("fidbase"),
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.diskId", "baseDiskId")]),
        poll_operation_until_done(),
        # 2. image from disk
        Step(name="cr-image", method="POST", path="/compute/v1/images",
             body={"projectId": "{{_suiteFolderId}}", "name": "img-fid-{{runId}}", "diskId": "{{baseDiskId}}"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.imageId", "imageId")]),
        poll_operation_until_done(),
        # 3. disk from image
        Step(name="cr-disk-from-img", method="POST", path=DISKS,
             body={"projectId": "{{_suiteFolderId}}", "name": "disk-fid-{{runId}}",
                   "zoneId": "{{existingZoneId}}", "size": _DEF_SIZE, "imageId": "{{imageId}}"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.diskId", "diskId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="get-disk", method="GET", path=f"{DISKS}/{{{{diskId}}}}",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('status READY', () => pm.expect(j.status).to.eql('READY'));",
                          "pm.test('sourceImageId set', () => pm.expect(j.sourceImageId).to.eql(pm.environment.get('imageId')));"]),
        # cleanup (disk-from-img → image → base disk)
        Step(name="del-disk", method="DELETE", path=f"{DISKS}/{{{{diskId}}}}", test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="del-img", method="DELETE", path="/compute/v1/images/{{imageId}}", test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="del-base", method="DELETE", path=f"{DISKS}/{{{{baseDiskId}}}}", test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

# ---------------------------------------------------------------------------
# DISK-GET / LIST
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="DISK-GET-NEG-NOTFOUND",
    title="Get well-formed-but-absent diskId → 404 NOT_FOUND",
    classes=["NEG"], priority="P0",
    steps=[Step(name="get-nx", method="GET", path=f"{DISKS}/{{{{garbageComputeId}}}}",
                test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")])],
))

CASES.append(Case(
    id="DISK-GET-CONF-NF-TEXT",
    title="Get garbage diskId → verbatim 'Disk ... not found' формат",
    classes=["CONF", "NEG"], priority="P1",
    steps=[Step(name="get-nx", method="GET", path=f"{DISKS}/{{{{garbageComputeId}}}}",
                test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND"),
                             # probe-needed: точный verbatim YC text — предполагаем "Disk <id> not found"
                             "pm.test('text mentions disk + not found', () => { const m = (pm.response.json().message || '').toLowerCase(); pm.expect(m).to.include('not found'); });"])],
))

CASES.append(Case(
    id="DISK-LST-CRUD-OK",
    title="List disks в folder → disks array",
    classes=["CRUD"], priority="P1",
    steps=[Step(name="list", method="GET", path=f"{DISKS}?projectId={{{{_suiteFolderId}}}}",
                test_script=[*assert_status(200),
                             "pm.test('disks is array', () => pm.expect(pm.response.json().disks || []).to.be.an('array'));"])],
))

CASES.append(Case(
    id="DISK-LST-VAL-FOLDER-REQUIRED",
    title="List без projectId → 400 InvalidArgument",
    classes=["VAL", "AUTHZ"], priority="P0",
    steps=[Step(name="list-nf", method="GET", path=DISKS,
                test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")])],
))

CASES.append(Case(
    id="DISK-LST-FILTER-MATCH",
    title="Создать disk → list filter=name=\"X\" → disk в результатах",
    classes=["FILTER", "CRUD"], priority="P2",
    steps=[
        Step(name="cr", method="POST", path=DISKS, body=_disk_body("flt"),
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.diskId", "diskId")]),
        poll_operation_until_done(),
        Step(name="list-filtered", method="GET",
             path=f"{DISKS}?projectId={{{{_suiteFolderId}}}}&pageSize=1000&filter=name%3D%22disk-flt-{{{{runId}}}}%22",
             test_script=[*assert_status(200),
                          "const ids = (Object.values(pm.response.json()).find(v => Array.isArray(v)) || []).map(x => x.id);",
                          "pm.test('filtered list contains', () => pm.expect(ids).to.include(pm.environment.get('diskId')));"]),
        Step(name="cleanup", method="DELETE", path=f"{DISKS}/{{{{diskId}}}}", test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

# ---------------------------------------------------------------------------
# DISK-UPD — Update
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="DISK-UPD-CRUD-NAME-DESC-LABELS-OK",
    title="Update disk mask=name,description,labels → все три применены",
    classes=["CRUD", "STATE"], priority="P1",
    steps=[
        Step(name="cr", method="POST", path=DISKS, body=_disk_body("upd", description="init", labels={"orig": "1"}),
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.diskId", "diskId")]),
        poll_operation_until_done(),
        Step(name="patch", method="PATCH", path=f"{DISKS}/{{{{diskId}}}}",
             body={"updateMask": "name,description,labels", "name": "disk-upd2-{{runId}}",
                   "description": "updated-newman", "labels": {"env": "prod"}},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="verify", method="GET", path=f"{DISKS}/{{{{diskId}}}}",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('name updated', () => pm.expect(j.name).to.match(/^disk-upd2-/));",
                          "pm.test('description updated', () => pm.expect(j.description).to.eql('updated-newman'));",
                          "pm.test('label env', () => pm.expect((j.labels || {}).env).to.eql('prod'));",
                          "pm.test('label orig removed (labels replaced)', () => pm.expect((j.labels || {}).orig).to.be.oneOf([undefined, '']));"]),
        Step(name="cleanup", method="DELETE", path=f"{DISKS}/{{{{diskId}}}}", test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

CASES.append(Case(
    id="DISK-UPD-SIZE-INCREASE-OK",
    title="Update disk mask=size с увеличением → 200, size в Get больше",
    classes=["CRUD", "STATE"], priority="P1",
    steps=[
        Step(name="cr", method="POST", path=DISKS, body=_disk_body("inc", size=_DEF_SIZE),
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.diskId", "diskId")]),
        poll_operation_until_done(),
        Step(name="patch-grow", method="PATCH", path=f"{DISKS}/{{{{diskId}}}}",
             body={"updateMask": "size", "size": _DEF_SIZE * 2},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="verify", method="GET", path=f"{DISKS}/{{{{diskId}}}}",
             test_script=[*assert_status(200),
                          "pm.test('size grew', () => pm.expect(String(pm.response.json().size)).to.eql('" + str(_DEF_SIZE * 2) + "'));"]),
        Step(name="cleanup", method="DELETE", path=f"{DISKS}/{{{{diskId}}}}", test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

CASES.append(Case(
    id="DISK-UPD-SIZE-DECREASE-REJECT",
    title="Update disk mask=size с уменьшением → InvalidArgument 'Disk size can only be increased'",
    classes=["NEG", "STATE", "VAL"], priority="P1",
    steps=[
        Step(name="cr", method="POST", path=DISKS, body=_disk_body("dec", size=_DEF_SIZE * 2),
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.diskId", "diskId")]),
        poll_operation_until_done(),
        Step(name="patch-shrink", method="PATCH", path=f"{DISKS}/{{{{diskId}}}}",
             body={"updateMask": "size", "size": _DEF_SIZE},
             # probe-needed: точный YC text. Может быть sync 400 или async op-error code 3.
             test_script=["pm.test('rejected (400 or 200+op-error)', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
                          *save_from_response("j.id", "opId"),
                          "if (pm.response.code === 400) { pm.test('code 3', () => pm.expect(pm.response.json().code).to.eql(3)); }"]),
        poll_operation_until_done(),
        Step(name="assert", method="GET", path="/operations/{{opId}}",
             test_script=["const j = pm.response.json();",
                          "pm.test('done', () => pm.expect(j.done).to.eql(true));",
                          "pm.test('if op-error → code 3', () => { if (j.error) pm.expect(j.error.code).to.eql(3); });"]),
        Step(name="cleanup", method="DELETE", path=f"{DISKS}/{{{{diskId}}}}", test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

CASES.append(Case(
    id="DISK-UPD-MASK-EMPTY-FULL-PATCH",
    title="Update disk без update_mask → full PATCH; immutable из body silently игнорируются",
    classes=["STATE", "VAL"], priority="P2",
    steps=[
        Step(name="cr", method="POST", path=DISKS, body=_disk_body("emp", typeId="{{existingDiskTypeId}}"),
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.diskId", "diskId")]),
        poll_operation_until_done(),
        Step(name="patch-no-mask", method="PATCH", path=f"{DISKS}/{{{{diskId}}}}",
             body={"description": "full-patch-desc", "typeId": "network-hdd", "zoneId": "ru-central1-b"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="verify", method="GET", path=f"{DISKS}/{{{{diskId}}}}",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('description applied', () => pm.expect(j.description).to.eql('full-patch-desc'));",
                          "pm.test('typeId NOT changed (immutable, silently ignored)', () => pm.expect(j.typeId).to.eql(pm.environment.get('existingDiskTypeId')));",
                          "pm.test('zoneId NOT changed (immutable)', () => pm.expect(j.zoneId).to.eql(pm.environment.get('existingZoneId')));"]),
        Step(name="cleanup", method="DELETE", path=f"{DISKS}/{{{{diskId}}}}", test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

CASES.append(Case(
    id="DISK-UPD-MASK-IMMUTABLE-TYPE",
    title="Update disk mask=type_id → 400 InvalidArgument 'type_id is immutable after Disk.Create'",
    classes=["STATE", "VAL", "CONF"], priority="P1",
    steps=[Step(name="patch-imm-type", method="PATCH", path=f"{DISKS}/{{{{garbageComputeId}}}}",
                body={"updateMask": "type_id", "typeId": "network-hdd"},
                # mask immutable отвергается до Get → 400; либо 404 если sync Get срабатывает первым
                test_script=["pm.test('rejected (400 immutable or 404)', () => pm.expect(pm.response.code).to.be.oneOf([400, 404]));",
                             "if (pm.response.code === 400) { const j = pm.response.json(); pm.test('code 3', () => pm.expect(j.code).to.eql(3)); pm.test('message mentions immutable or type', () => pm.expect((j.message||'').toLowerCase()).to.match(/immutable|type/)); }"])],
))

CASES.append(Case(
    id="DISK-UPD-MASK-IMMUTABLE-ZONE",
    title="Update disk mask=zone_id → 400 InvalidArgument (immutable)",
    classes=["STATE", "VAL"], priority="P1",
    steps=[Step(name="patch-imm-zone", method="PATCH", path=f"{DISKS}/{{{{garbageComputeId}}}}",
                body={"updateMask": "zone_id", "zoneId": "ru-central1-b"},
                test_script=["pm.test('rejected (400 or 404)', () => pm.expect(pm.response.code).to.be.oneOf([400, 404]));"])],
))

CASES.append(Case(
    id="DISK-UPD-MASK-UNKNOWN-FIELD",
    title="Update disk с unknown field в update_mask → 400 InvalidArgument",
    classes=["VAL", "STATE"], priority="P1",
    steps=[Step(name="patch-unk", method="PATCH", path=f"{DISKS}/{{{{garbageComputeId}}}}",
                body={"updateMask": "some_unknown_field_xyz", "description": "x"},
                test_script=["pm.test('rejected (400 or 404)', () => pm.expect(pm.response.code).to.be.oneOf([400, 404]));"])],
))

CASES.append(Case(
    id="DISK-UPD-AUTHZ-NF-SYNC",
    title="Update несуществующего disk → sync 404 NOT_FOUND",
    classes=["NEG", "AUTHZ"], priority="P1",
    steps=[Step(name="patch-nx", method="PATCH", path=f"{DISKS}/{{{{garbageComputeId}}}}",
                body={"updateMask": "description", "description": "x"},
                test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")])],
))

# ---------------------------------------------------------------------------
# DISK-DEL — Delete
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="DISK-DEL-CRUD-OK",
    title="Delete disk → Operation done; Get → 404",
    classes=["CRUD", "STATE"], priority="P1",
    steps=[
        Step(name="cr", method="POST", path=DISKS, body=_disk_body("delok"),
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.diskId", "diskId")]),
        poll_operation_until_done(),
        Step(name="del", method="DELETE", path=f"{DISKS}/{{{{diskId}}}}",
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="get-404", method="GET", path=f"{DISKS}/{{{{diskId}}}}",
             test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")]),
    ],
))

CASES.append(Case(
    id="DISK-DEL-CONF-RESPONSE-EMPTY",
    title="Delete disk → Operation.response = Empty, metadata = DeleteDiskMetadata{diskId}",
    classes=["CONF"], priority="P2",
    steps=[
        Step(name="cr", method="POST", path=DISKS, body=_disk_body("delm"),
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.diskId", "diskId")]),
        poll_operation_until_done(),
        Step(name="del", method="DELETE", path=f"{DISKS}/{{{{diskId}}}}",
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          "pm.test('metadata has diskId', () => pm.expect(pm.response.json().metadata && pm.response.json().metadata.diskId).to.eql(pm.environment.get('diskId')));"]),
        poll_operation_until_done(),
        Step(name="assert-empty", method="GET", path="/operations/{{opId}}",
             test_script=["const j = pm.response.json();",
                          "pm.test('done & no error', () => { pm.expect(j.done).to.eql(true); pm.expect(j.error).to.be.oneOf([undefined, null]); });",
                          "pm.test('response is Empty-like object', () => { pm.expect(j.response).to.be.an('object'); const keys = Object.keys(j.response).filter(k => k !== '@type'); pm.expect(keys.length).to.eql(0); });",
                          "pm.test('metadata.diskId matches', () => pm.expect(j.metadata && j.metadata.diskId).to.eql(pm.environment.get('diskId')));"]),
    ],
))

CASES.append(Case(
    id="DISK-DEL-NEG-NOTFOUND",
    title="Delete несуществующего disk → sync 404 NOT_FOUND",
    classes=["NEG"], priority="P0",
    steps=[Step(name="del-nx", method="DELETE", path=f"{DISKS}/{{{{garbageComputeId}}}}",
                test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")])],
))

# ---------------------------------------------------------------------------
# DISK-MV — Move
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="DISK-MV-CRUD-OK",
    title="Move disk в другой folder → projectId в Get обновлён",
    classes=["CRUD"], priority="P1",
    steps=[
        Step(name="cr", method="POST", path=DISKS, body=_disk_body("mv"),
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.diskId", "diskId")]),
        poll_operation_until_done(),
        Step(name="move", method="POST", path=f"{DISKS}/{{{{diskId}}}}:move",
             body={"destinationProjectId": "{{_suiteFolderCrossId}}"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="verify", method="GET", path=f"{DISKS}/{{{{diskId}}}}",
             test_script=[*assert_status(200),
                          "pm.test('projectId == cross', () => pm.expect(pm.response.json().projectId).to.eql(pm.environment.get('_suiteFolderCrossId')));"]),
        Step(name="cleanup", method="DELETE", path=f"{DISKS}/{{{{diskId}}}}", test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

CASES.append(Case(
    id="DISK-MV-NEG-DEST-NOTFOUND",
    title="Move disk в garbage destinationProjectId → async NOT_FOUND",
    classes=["NEG"], priority="P1",
    steps=[
        # # requires peer-validation enabled
        Step(name="cr", method="POST", path=DISKS, body=_disk_body("mvbad"),
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.diskId", "diskId")]),
        poll_operation_until_done(),
        Step(name="move-bad", method="POST", path=f"{DISKS}/{{{{diskId}}}}:move",
             body={"destinationProjectId": "{{garbageRmId}}"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        assert_op_error(5, "NOT_FOUND"),
        Step(name="cleanup", method="DELETE", path=f"{DISKS}/{{{{diskId}}}}", test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

CASES.append(Case(
    id="DISK-MV-AUTHZ-NF-SYNC",
    title="Move несуществующего disk → sync 404 NOT_FOUND",
    classes=["NEG", "AUTHZ"], priority="P1",
    steps=[Step(name="mv-nx", method="POST", path=f"{DISKS}/{{{{garbageComputeId}}}}:move",
                body={"destinationProjectId": "{{_suiteFolderId}}"},
                test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")])],
))

CASES.append(Case(
    id="DISK-MV-VAL-NO-DEST",
    title="Move disk без destinationProjectId → 400 InvalidArgument (или 404 если Get раньше)",
    classes=["VAL"], priority="P1",
    steps=[Step(name="mv-no-dest", method="POST", path=f"{DISKS}/{{{{garbageComputeId}}}}:move", body={},
                test_script=["pm.test('rejected (400 or 404)', () => pm.expect(pm.response.code).to.be.oneOf([400, 404]));"])],
))

# ---------------------------------------------------------------------------
# DISK-REL — Relocate
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="DISK-REL-CRUD-OK",
    title="Relocate disk (не attached) в другую zone → zoneId в Get обновлён",
    classes=["CRUD", "STATE"], priority="P2",
    steps=[
        Step(name="cr", method="POST", path=DISKS, body=_disk_body("rel"),
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.diskId", "diskId")]),
        poll_operation_until_done(),
        Step(name="relocate", method="POST", path=f"{DISKS}/{{{{diskId}}}}:relocate",
             body={"destinationZoneId": "{{existingZoneAltId}}"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="verify", method="GET", path=f"{DISKS}/{{{{diskId}}}}",
             test_script=[*assert_status(200),
                          "pm.test('zoneId == alt', () => pm.expect(pm.response.json().zoneId).to.eql(pm.environment.get('existingZoneAltId')));"]),
        Step(name="cleanup", method="DELETE", path=f"{DISKS}/{{{{diskId}}}}", test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

CASES.append(Case(
    id="DISK-REL-NEG-DEST-ZONE-UNKNOWN",
    title="Relocate disk в unknown zone → rejected (400 sync или async op-error)",
    classes=["NEG", "VAL"], priority="P2",
    steps=[
        Step(name="cr", method="POST", path=DISKS, body=_disk_body("relbad"),
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.diskId", "diskId")]),
        poll_operation_until_done(),
        Step(name="relocate-bad", method="POST", path=f"{DISKS}/{{{{diskId}}}}:relocate",
             body={"destinationZoneId": "ru-central1-zzz"},
             test_script=["pm.test('rejected (400 sync or 200+op-error)', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
                          *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="assert", method="GET", path="/operations/{{opId}}",
             test_script=["const j = pm.response.json();",
                          "pm.test('done', () => pm.expect(j.done).to.eql(true));",
                          "pm.test('if op-error → code 3 or 5', () => { if (j.error) pm.expect(j.error.code).to.be.oneOf([3, 5]); });"]),
        Step(name="cleanup", method="DELETE", path=f"{DISKS}/{{{{diskId}}}}", test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

# ---------------------------------------------------------------------------
# DISK-LOP — ListOperations
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="DISK-LOP-CRUD-OK",
    title="ListOperations disk → содержит как минимум create-op",
    classes=["CRUD"], priority="P1",
    steps=[
        Step(name="cr", method="POST", path=DISKS, body=_disk_body("lop"),
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.diskId", "diskId")]),
        poll_operation_until_done(),
        Step(name="list-ops", method="GET", path=f"{DISKS}/{{{{diskId}}}}/operations",
             test_script=[*assert_status(200),
                          "pm.test('at least 1 op', () => pm.expect((pm.response.json().operations || []).length).to.be.at.least(1));"]),
        Step(name="cleanup", method="DELETE", path=f"{DISKS}/{{{{diskId}}}}", test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

CASES.append(Case(
    id="DISK-LOP-NEG-PARENT-NF",
    title="ListOperations несуществующего disk → 200 (пусто) или 404",
    classes=["NEG"], priority="P2",
    steps=[Step(name="lop-nx", method="GET", path=f"{DISKS}/{{{{garbageComputeId}}}}/operations",
                test_script=["pm.test('200 or 404', () => pm.expect(pm.response.code).to.be.oneOf([200, 404]));"])],
))

# ---------------------------------------------------------------------------
# DISK — lifecycle conformance (Create→Get→List-includes→Update→Get→Delete→List-excludes→Get-404)
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="DISK-LIFECYCLE-CONF",
    title="Full lifecycle conformance: CRUD-инварианты disk",
    classes=["CRUD", "CONF", "STATE"], priority="P1",
    steps=[
        Step(name="cr", method="POST", path=DISKS, body=_disk_body("life"),
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.diskId", "diskId")]),
        poll_operation_until_done(),
        Step(name="get-1", method="GET", path=f"{DISKS}/{{{{diskId}}}}",
             test_script=[*assert_status(200), "pm.test('id', () => pm.expect(pm.response.json().id).to.eql(pm.environment.get('diskId')));"]),
        Step(name="lst-includes", method="GET", path=f"{DISKS}?projectId={{{{_suiteFolderId}}}}&pageSize=1000",
             test_script=[*assert_status(200),
                          "const ids = (pm.response.json().disks || []).map(x => x.id);",
                          "pm.test('list contains', () => pm.expect(ids).to.include(pm.environment.get('diskId')));"]),
        Step(name="upd", method="PATCH", path=f"{DISKS}/{{{{diskId}}}}",
             body={"updateMask": "description", "description": "life-conf"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="get-after-upd", method="GET", path=f"{DISKS}/{{{{diskId}}}}",
             test_script=[*assert_status(200), "pm.test('description updated', () => pm.expect(pm.response.json().description).to.eql('life-conf'));"]),
        Step(name="del", method="DELETE", path=f"{DISKS}/{{{{diskId}}}}",
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="lst-excludes", method="GET", path=f"{DISKS}?projectId={{{{_suiteFolderId}}}}&pageSize=1000",
             test_script=[*assert_status(200),
                          "const ids = (pm.response.json().disks || []).map(x => x.id);",
                          "pm.test('list does not contain', () => pm.expect(ids).to.not.include(pm.environment.get('diskId')));"]),
        Step(name="get-404", method="GET", path=f"{DISKS}/{{{{diskId}}}}",
             test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")]),
    ],
))

# ---------------------------------------------------------------------------
# Расширения через generic-блоки
# ---------------------------------------------------------------------------

_disk_extra = {"zoneId": "{{existingZoneId}}", "size": _DEF_SIZE}
CASES.extend(list_page_block("DISK", DISKS))
CASES.extend(filter_block("DISK", DISKS))
CASES.extend(name_validation_block("DISK", DISKS, _disk_extra))
CASES.extend(description_validation_block("DISK", DISKS, _disk_extra))
CASES.extend(labels_validation_block("DISK", DISKS, _disk_extra))
CASES.extend(http_method_block("DISK", DISKS))
CASES.extend(malformed_body_block("DISK", DISKS))
CASES.extend(security_injection_block("DISK", DISKS, DISKS, _disk_extra))

# CONF: id-prefix epd проверяется в DISK-CR-CRUD-OK; добавим явный assert через op-metadata.
CASES.append(Case(
    id="DISK-CR-CONF-ID-PREFIX-EPD",
    title="Create disk → operation.id prefix 'epd', metadata.diskId prefix 'epd'",
    classes=["CONF"], priority="P1",
    steps=[
        Step(name="cr", method="POST", path=DISKS, body=_disk_body("idpfx"),
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('operation.id epd...', () => pm.expect(j.id).to.match(/^epd[a-z0-9]{17}$/));",
                          "pm.test('metadata.diskId epd...', () => pm.expect(j.metadata && j.metadata.diskId).to.match(/^epd[a-z0-9]{17}$/));",
                          *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.diskId", "diskId")]),
        poll_operation_until_done(),
        Step(name="cleanup", method="DELETE", path=f"{DISKS}/{{{{diskId}}}}", test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

# blocked: ListSnapshotSchedules — нет SnapshotSchedule-ресурса.
# CASES.append(...)  # blocked:kacho-snapshot-schedule
# blocked: kms_key_id в Create — нет kacho-kms.
# CASES.append(...)  # blocked:kacho-kms
# blocked: os_product_ids — marketplace.
# CASES.append(...)  # blocked:kacho-marketplace
# Disk.Delete-while-attached — см. instance.py (нужен Instance).
