"""Case-set для SnapshotService (kacho-compute).

Covered RPCs: Get, List, Create (из Disk), Update, Delete, ListOperations.
(access-bindings — no-op skeleton, skip.)

Snapshot.Create требует disk_id (required). id-prefix Snapshot = `fd8`, operation prefix `epd`.
created_at truncate до секунд. disk_size в Snapshot == size исходного Disk на момент snapshot.
"""

CASES = []

SNAPS = "/compute/v1/snapshots"
DISKS = "/compute/v1/disks"
_DISK_SIZE = 16106127360  # 15 GiB — отличный от 10 GiB чтобы проверить disk_size в Snapshot


def _pre_disk(suffix="src"):
    return [
        Step(name=f"pre-disk-{suffix}", method="POST", path=DISKS,
             body={"projectId": "{{_suiteFolderId}}", "name": f"disk-snapsrc-{suffix}-{{{{runId}}}}",
                   "zoneId": "{{existingZoneId}}", "size": _DISK_SIZE},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.diskId", "baseDiskId")]),
        poll_operation_until_done(),
    ]


def _cleanup_base_disk():
    return [
        Step(name="cleanup-base-disk", method="DELETE", path=f"{DISKS}/{{{{baseDiskId}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ]


# ---------------------------------------------------------------------------
# SNAP-CR — Create
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="SNAP-CR-CRUD-OK",
    title="Create snapshot из disk → Operation → poll → Get → status READY, disk_size == disk.size, source_disk_id, id-prefix fd8",
    classes=["CRUD", "CONF"], priority="P1",
    steps=[
        *_pre_disk("crok"),
        Step(name="create", method="POST", path=SNAPS,
             body={"projectId": "{{_suiteFolderId}}", "name": "snap-cr-{{runId}}", "diskId": "{{baseDiskId}}",
                   "description": "newman CRUD-OK", "labels": {"suite": "newman"}},
             test_script=[*assert_status(200), *assert_operation_envelope(),
                          *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.snapshotId", "snapshotId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="get", method="GET", path=f"{SNAPS}/{{{{snapshotId}}}}",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('id matches & fd8 prefix', () => { pm.expect(j.id).to.eql(pm.environment.get('snapshotId')); pm.expect(j.id).to.match(/^fd8/); });",
                          "pm.test('projectId matches', () => pm.expect(j.projectId).to.eql(pm.environment.get('_suiteFolderId')));",
                          "pm.test('status READY', () => pm.expect(j.status).to.eql('READY'));",
                          "pm.test('sourceDiskId matches', () => pm.expect(j.sourceDiskId).to.eql(pm.environment.get('baseDiskId')));",
                          "pm.test('diskSize == disk.size', () => pm.expect(String(j.diskSize)).to.eql('" + str(_DISK_SIZE) + "'));",
                          *assert_created_at_seconds()]),
        Step(name="del-snap", method="DELETE", path=f"{SNAPS}/{{{{snapshotId}}}}", test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_base_disk(),
    ],
))

CASES.append(Case(
    id="SNAP-CR-VAL-FOLDER-REQUIRED",
    title="Create snapshot без projectId → 400 InvalidArgument",
    classes=["VAL"], priority="P0",
    steps=[Step(name="cr-nf", method="POST", path=SNAPS, body={"name": "snap-nf-{{runId}}", "diskId": "{{garbageComputeId}}"},
                test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")])],
))

CASES.append(Case(
    id="SNAP-CR-VAL-NO-DISK",
    title="Create snapshot без disk_id → 400 InvalidArgument (disk_id required)",
    classes=["VAL", "NEG"], priority="P0",
    steps=[Step(name="cr-no-disk", method="POST", path=SNAPS, body={"projectId": "{{_suiteFolderId}}", "name": "snap-nd-{{runId}}"},
                test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")])],
))

CASES.append(Case(
    id="SNAP-CR-NEG-DISK-NOTFOUND",
    title="Create snapshot из garbage disk_id → async NOT_FOUND",
    classes=["NEG"], priority="P1",
    steps=[
        Step(name="cr-bad-disk", method="POST", path=SNAPS,
             body={"projectId": "{{_suiteFolderId}}", "name": "snap-bd-{{runId}}", "diskId": "{{garbageComputeId}}"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        assert_op_error(5, "NOT_FOUND"),
    ],
))

CASES.append(Case(
    id="SNAP-CR-NEG-FOLDER-NOTFOUND",
    title="Create snapshot в garbage projectId → async NOT_FOUND 'Folder ... not found'",
    classes=["NEG"], priority="P0",
    steps=[
        # # requires peer-validation enabled
        *_pre_disk("bf"),
        Step(name="cr-bad-folder", method="POST", path=SNAPS,
             body={"projectId": "{{garbageRmId}}", "name": "snap-bf-{{runId}}", "diskId": "{{baseDiskId}}"},
             test_script=["pm.test('rejected sync (403) or accepted async (200)', () => pm.expect(pm.response.code).to.be.oneOf([200, 403]));",
                          "if (pm.response.code === 200) pm.environment.set('opId', pm.response.json().id);",
                          "else pm.environment.set('opId', '');"]),
        poll_operation_until_done(),
        assert_op_error(5, "NOT_FOUND", msg_substr="folder"),
        *_cleanup_base_disk(),
    ],
))

CASES.append(Case(
    id="SNAP-CR-NEG-DUP-NAME",
    title="Create snapshot с дубликатом name в folder → async ALREADY_EXISTS",
    classes=["NEG", "CONC"], priority="P1",
    steps=[
        *_pre_disk("dup"),
        Step(name="cr-1", method="POST", path=SNAPS,
             body={"projectId": "{{_suiteFolderId}}", "name": "snap-dup-{{runId}}", "diskId": "{{baseDiskId}}"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.snapshotId", "snapshotId")]),
        poll_operation_until_done(),
        Step(name="cr-2-dup", method="POST", path=SNAPS,
             body={"projectId": "{{_suiteFolderId}}", "name": "snap-dup-{{runId}}", "diskId": "{{baseDiskId}}"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        assert_op_error(6, "ALREADY_EXISTS"),
        Step(name="del-snap", method="DELETE", path=f"{SNAPS}/{{{{snapshotId}}}}", test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_base_disk(),
    ],
))

# ---------------------------------------------------------------------------
# SNAP-GET / LIST
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="SNAP-GET-NEG-NOTFOUND",
    title="Get well-formed-but-absent snapshotId → 404 NOT_FOUND",
    classes=["NEG"], priority="P0",
    steps=[Step(name="get-nx", method="GET", path=f"{SNAPS}/{{{{garbageImageId}}}}",
                test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")])],
))

CASES.append(Case(
    id="SNAP-GET-CONF-NF-TEXT",
    title="Get garbage snapshotId → текст содержит 'not found'",
    classes=["CONF", "NEG"], priority="P1",
    steps=[Step(name="get-nx", method="GET", path=f"{SNAPS}/{{{{garbageImageId}}}}",
                test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND"),
                             "pm.test('text mentions not found', () => pm.expect((pm.response.json().message || '').toLowerCase()).to.include('not found'));"])],
))

CASES.append(Case(
    id="SNAP-LST-CRUD-OK",
    title="List snapshots в folder → snapshots array",
    classes=["CRUD"], priority="P1",
    steps=[Step(name="list", method="GET", path=f"{SNAPS}?projectId={{{{_suiteFolderId}}}}",
                test_script=[*assert_status(200), "pm.test('snapshots is array', () => pm.expect(pm.response.json().snapshots || []).to.be.an('array'));"])],
))

CASES.append(Case(
    id="SNAP-LST-VAL-FOLDER-REQUIRED",
    title="List snapshots без projectId → 400 InvalidArgument",
    classes=["VAL", "AUTHZ"], priority="P0",
    steps=[Step(name="list-nf", method="GET", path=SNAPS,
                test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")])],
))

# ---------------------------------------------------------------------------
# SNAP-UPD — Update
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="SNAP-UPD-CRUD-NAME-DESC-LABELS-OK",
    title="Update snapshot mask=name,description,labels → все три применены",
    classes=["CRUD", "STATE"], priority="P1",
    steps=[
        *_pre_disk("upd"),
        Step(name="cr", method="POST", path=SNAPS,
             body={"projectId": "{{_suiteFolderId}}", "name": "snap-upd-{{runId}}", "diskId": "{{baseDiskId}}",
                   "description": "init", "labels": {"orig": "1"}},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.snapshotId", "snapshotId")]),
        poll_operation_until_done(),
        Step(name="patch", method="PATCH", path=f"{SNAPS}/{{{{snapshotId}}}}",
             body={"updateMask": "name,description,labels", "name": "snap-upd2-{{runId}}",
                   "description": "updated-newman", "labels": {"env": "prod"}},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="verify", method="GET", path=f"{SNAPS}/{{{{snapshotId}}}}",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('name updated', () => pm.expect(j.name).to.match(/^snap-upd2-/));",
                          "pm.test('description updated', () => pm.expect(j.description).to.eql('updated-newman'));",
                          "pm.test('label env', () => pm.expect((j.labels || {}).env).to.eql('prod'));"]),
        Step(name="del-snap", method="DELETE", path=f"{SNAPS}/{{{{snapshotId}}}}", test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_base_disk(),
    ],
))

CASES.append(Case(
    id="SNAP-UPD-MASK-IMMUTABLE-SOURCE-DISK",
    title="Update snapshot mask=source_disk_id → 400 InvalidArgument (immutable) или 404",
    classes=["STATE", "VAL", "CONF"], priority="P1",
    steps=[Step(name="patch-imm-src", method="PATCH", path=f"{SNAPS}/{{{{garbageImageId}}}}",
                body={"updateMask": "source_disk_id", "sourceDiskId": "{{garbageComputeId}}"},
                test_script=["pm.test('rejected (400 immutable or 404)', () => pm.expect(pm.response.code).to.be.oneOf([400, 404]));",
                             "if (pm.response.code === 400) { const j = pm.response.json(); pm.test('code 3', () => pm.expect(j.code).to.eql(3)); }"])],
))

CASES.append(Case(
    id="SNAP-UPD-MASK-UNKNOWN-FIELD",
    title="Update snapshot с unknown field в update_mask → 400 InvalidArgument или 404",
    classes=["VAL", "STATE"], priority="P1",
    steps=[Step(name="patch-unk", method="PATCH", path=f"{SNAPS}/{{{{garbageImageId}}}}",
                body={"updateMask": "totally_unknown_xyz", "description": "x"},
                test_script=["pm.test('rejected (400 or 404)', () => pm.expect(pm.response.code).to.be.oneOf([400, 404]));"])],
))

CASES.append(Case(
    id="SNAP-UPD-AUTHZ-NF-SYNC",
    title="Update несуществующего snapshot → sync 404 NOT_FOUND",
    classes=["NEG", "AUTHZ"], priority="P1",
    steps=[Step(name="patch-nx", method="PATCH", path=f"{SNAPS}/{{{{garbageImageId}}}}",
                body={"updateMask": "description", "description": "x"},
                test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")])],
))

# ---------------------------------------------------------------------------
# SNAP-DEL — Delete
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="SNAP-DEL-CRUD-OK",
    title="Delete snapshot → Operation done; Get → 404",
    classes=["CRUD", "STATE"], priority="P1",
    steps=[
        *_pre_disk("delok"),
        Step(name="cr", method="POST", path=SNAPS,
             body={"projectId": "{{_suiteFolderId}}", "name": "snap-delok-{{runId}}", "diskId": "{{baseDiskId}}"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.snapshotId", "snapshotId")]),
        poll_operation_until_done(),
        Step(name="del", method="DELETE", path=f"{SNAPS}/{{{{snapshotId}}}}",
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="get-404", method="GET", path=f"{SNAPS}/{{{{snapshotId}}}}",
             test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")]),
        *_cleanup_base_disk(),
    ],
))

CASES.append(Case(
    id="SNAP-DEL-NEG-NOTFOUND",
    title="Delete несуществующего snapshot → sync 404 NOT_FOUND",
    classes=["NEG"], priority="P0",
    steps=[Step(name="del-nx", method="DELETE", path=f"{SNAPS}/{{{{garbageImageId}}}}",
                test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")])],
))

CASES.append(Case(
    id="SNAP-DEL-STATE-DISK-DELETABLE-AFTER",
    title="Создать snapshot из disk → Delete disk до Delete snapshot → OK (Disk можно удалить, оставив Snapshot — verbatim YC)",
    classes=["STATE", "CRUD"], priority="P2",
    steps=[
        *_pre_disk("delafter"),
        Step(name="cr-snap", method="POST", path=SNAPS,
             body={"projectId": "{{_suiteFolderId}}", "name": "snap-delafter-{{runId}}", "diskId": "{{baseDiskId}}"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.snapshotId", "snapshotId")]),
        poll_operation_until_done(),
        # Delete the source disk — must succeed (snapshot retains source_disk_id but no FK)
        Step(name="del-disk", method="DELETE", path=f"{DISKS}/{{{{baseDiskId}}}}",
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="get-snap-still-there", method="GET", path=f"{SNAPS}/{{{{snapshotId}}}}",
             test_script=[*assert_status(200), "pm.test('snapshot still exists', () => pm.expect(pm.response.json().id).to.eql(pm.environment.get('snapshotId')));"]),
        Step(name="cleanup-snap", method="DELETE", path=f"{SNAPS}/{{{{snapshotId}}}}", test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

# ---------------------------------------------------------------------------
# SNAP-LOP — ListOperations
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="SNAP-LOP-CRUD-OK",
    title="ListOperations snapshot → содержит как минимум create-op",
    classes=["CRUD"], priority="P1",
    steps=[
        *_pre_disk("lop"),
        Step(name="cr", method="POST", path=SNAPS,
             body={"projectId": "{{_suiteFolderId}}", "name": "snap-lop-{{runId}}", "diskId": "{{baseDiskId}}"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.snapshotId", "snapshotId")]),
        poll_operation_until_done(),
        Step(name="list-ops", method="GET", path=f"{SNAPS}/{{{{snapshotId}}}}/operations",
             test_script=[*assert_status(200), "pm.test('at least 1 op', () => pm.expect((pm.response.json().operations || []).length).to.be.at.least(1));"]),
        Step(name="del-snap", method="DELETE", path=f"{SNAPS}/{{{{snapshotId}}}}", test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_base_disk(),
    ],
))

# ---------------------------------------------------------------------------
# SNAP — lifecycle conformance
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="SNAP-LIFECYCLE-CONF",
    title="Full lifecycle conformance: CRUD-инварианты snapshot",
    classes=["CRUD", "CONF", "STATE"], priority="P1",
    steps=[
        *_pre_disk("life"),
        Step(name="cr", method="POST", path=SNAPS,
             body={"projectId": "{{_suiteFolderId}}", "name": "snap-life-{{runId}}", "diskId": "{{baseDiskId}}"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.snapshotId", "snapshotId")]),
        poll_operation_until_done(),
        Step(name="get-1", method="GET", path=f"{SNAPS}/{{{{snapshotId}}}}",
             test_script=[*assert_status(200), "pm.test('id', () => pm.expect(pm.response.json().id).to.eql(pm.environment.get('snapshotId')));"]),
        Step(name="lst-includes", method="GET", path=f"{SNAPS}?projectId={{{{_suiteFolderId}}}}&pageSize=1000",
             test_script=[*assert_status(200),
                          "const ids = (pm.response.json().snapshots || []).map(x => x.id);",
                          "pm.test('list contains', () => pm.expect(ids).to.include(pm.environment.get('snapshotId')));"]),
        Step(name="upd", method="PATCH", path=f"{SNAPS}/{{{{snapshotId}}}}",
             body={"updateMask": "description", "description": "life-conf"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="get-after-upd", method="GET", path=f"{SNAPS}/{{{{snapshotId}}}}",
             test_script=[*assert_status(200), "pm.test('description updated', () => pm.expect(pm.response.json().description).to.eql('life-conf'));"]),
        Step(name="del", method="DELETE", path=f"{SNAPS}/{{{{snapshotId}}}}",
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="lst-excludes", method="GET", path=f"{SNAPS}?projectId={{{{_suiteFolderId}}}}&pageSize=1000",
             test_script=[*assert_status(200),
                          "const ids = (pm.response.json().snapshots || []).map(x => x.id);",
                          "pm.test('list does not contain', () => pm.expect(ids).to.not.include(pm.environment.get('snapshotId')));"]),
        Step(name="get-404", method="GET", path=f"{SNAPS}/{{{{snapshotId}}}}",
             test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")]),
        *_cleanup_base_disk(),
    ],
))

# ---------------------------------------------------------------------------
# Расширения через generic-блоки (name validation требует disk_id → используем pre-disk wrapper)
# ---------------------------------------------------------------------------

def _wrap_with_disk(case):
    """Обернуть кейс pre-disk шагами; внутри body должен ссылаться на {{baseDiskId}}."""
    uniq = case.id.lower().replace("-", "")[-12:]
    return Case(id=case.id, title=case.title, classes=case.classes, priority=case.priority,
                steps=[*_pre_disk(uniq), *case.steps, *_cleanup_base_disk()])


_snap_src = {"diskId": "{{baseDiskId}}"}
CASES.extend(list_page_block("SNAP", SNAPS))
CASES.extend(filter_block("SNAP", SNAPS))
# name/labels/desc 200-ожидающие кейсы — с pre-disk; 400-ожидающие — без (отказ синхронный).
CASES.extend(name_validation_block("SNAP", SNAPS, _snap_src, wrap=_wrap_with_disk))
CASES.extend(description_validation_block("SNAP", SNAPS, _snap_src, wrap=_wrap_with_disk))
CASES.extend(labels_validation_block("SNAP", SNAPS, _snap_src, wrap=_wrap_with_disk))
CASES.extend(http_method_block("SNAP", SNAPS))
CASES.extend(malformed_body_block("SNAP", SNAPS))
# security probes: name с garbage diskId — отказ синхронный (валидация name) или async NotFound (диск); не 500.
CASES.extend(security_injection_block("SNAP", SNAPS, SNAPS, {"diskId": "{{garbageComputeId}}"}))

CASES.append(Case(
    id="SNAP-CR-CONF-ID-PREFIX-FD8",
    title="Create snapshot → operation.id prefix 'epd', metadata.snapshotId prefix 'fd8'",
    classes=["CONF"], priority="P1",
    steps=[
        *_pre_disk("idpfx"),
        Step(name="cr", method="POST", path=SNAPS,
             body={"projectId": "{{_suiteFolderId}}", "name": "snap-idpfx-{{runId}}", "diskId": "{{baseDiskId}}"},
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('operation.id epd...', () => pm.expect(j.id).to.match(/^epd[a-z0-9]{17}$/));",
                          "pm.test('metadata.snapshotId fd8...', () => pm.expect(j.metadata && j.metadata.snapshotId).to.match(/^fd8[a-z0-9]{17}$/));",
                          *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.snapshotId", "snapshotId")]),
        poll_operation_until_done(),
        Step(name="cleanup", method="DELETE", path=f"{SNAPS}/{{{{snapshotId}}}}", test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_base_disk(),
    ],
))
