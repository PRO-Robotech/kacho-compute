# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: BUSL-1.1

"""Case-set для ImageService (kacho-compute).

Covered RPCs: Get, GetLatestByFamily, List, Create, Update, Delete, ListOperations.
(access-bindings — no-op skeleton, skip; os_product_ids — blocked:kacho-marketplace.)

Create-источники: image_id / snapshot_id / disk_id / uri (exactly one). id-prefix Image = `fd8`,
operation prefix `epd`. created_at truncate до секунд. Кейсы спроектированы под verbatim YC.
"""

CASES = []

IMAGES = "/compute/v1/images"
DISKS = "/compute/v1/disks"
_DEF_DISK_SIZE = 10737418240  # 10 GiB
_SAMPLE_URI = "https://storage.example.net/presigned/image.qcow2"  # control-plane: download мгновенный → READY


def _img_body(name_suffix, **over):
    b = {"projectId": "{{_suiteFolderId}}", "name": f"img-{name_suffix}-{{{{runId}}}}"}
    b.update(over)
    return b


def _pre_disk(suffix="src"):
    """Создать base disk, сохранить baseDiskId; вернуть список шагов."""
    return [
        Step(name=f"pre-disk-{suffix}", method="POST", path=DISKS,
             body={"projectId": "{{_suiteFolderId}}", "name": f"disk-imgsrc-{suffix}-{{{{runId}}}}",
                   "zoneId": "{{existingZoneId}}", "size": _DEF_DISK_SIZE},
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
# IMG-CR — Create
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="IMG-CR-CRUD-OK",
    title="Create image из disk → Operation → poll → Get → status READY, family, min_disk_size, id-prefix fd8",
    classes=["CRUD", "CONF"], priority="P1",
    steps=[
        *_pre_disk("crok"),
        Step(name="create", method="POST", path=IMAGES,
             body={"projectId": "{{_suiteFolderId}}", "name": "img-cr-{{runId}}",
                   "family": "newman-fam-{{runId}}", "diskId": "{{baseDiskId}}",
                   "description": "newman CRUD-OK", "labels": {"suite": "newman"}},
             test_script=[*assert_status(200), *assert_operation_envelope(),
                          *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.imageId", "imageId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="get", method="GET", path=f"{IMAGES}/{{{{imageId}}}}",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('id matches & fd8 prefix', () => { pm.expect(j.id).to.eql(pm.environment.get('imageId')); pm.expect(j.id).to.match(/^fd8/); });",
                          "pm.test('projectId matches', () => pm.expect(j.projectId).to.eql(pm.environment.get('_suiteFolderId')));",
                          "pm.test('family matches', () => pm.expect(j.family).to.match(/^newman-fam-/));",
                          "pm.test('status READY', () => pm.expect(j.status).to.eql('READY'));",
                          "pm.test('minDiskSize numeric & >0', () => pm.expect(Number(j.minDiskSize)).to.be.above(0));",
                          *assert_created_at_seconds()]),
        Step(name="del-img", method="DELETE", path=f"{IMAGES}/{{{{imageId}}}}", test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_base_disk(),
    ],
))

CASES.append(Case(
    id="IMG-CR-CRUD-FROM-URI-OK",
    title="Create image из source uri → status READY (control-plane: download мгновенный)",
    classes=["CRUD"], priority="P2",
    steps=[
        Step(name="create", method="POST", path=IMAGES,
             body={"projectId": "{{_suiteFolderId}}", "name": "img-uri-{{runId}}", "uri": _SAMPLE_URI},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.imageId", "imageId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="get", method="GET", path=f"{IMAGES}/{{{{imageId}}}}",
             test_script=[*assert_status(200),
                          "pm.test('status READY', () => pm.expect(pm.response.json().status).to.eql('READY'));"]),
        Step(name="cleanup", method="DELETE", path=f"{IMAGES}/{{{{imageId}}}}", test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

CASES.append(Case(
    id="IMG-CR-CRUD-FROM-IMAGE-OK",
    title="Create image из другого image (image_id source) → status READY",
    classes=["CRUD"], priority="P2",
    steps=[
        # 1. base image из uri
        Step(name="cr-base-img", method="POST", path=IMAGES,
             body={"projectId": "{{_suiteFolderId}}", "name": "img-base-{{runId}}", "uri": _SAMPLE_URI},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.imageId", "baseImageId")]),
        poll_operation_until_done(),
        # 2. image из этого image
        Step(name="cr-img-from-img", method="POST", path=IMAGES,
             body={"projectId": "{{_suiteFolderId}}", "name": "img-fromimg-{{runId}}", "imageId": "{{baseImageId}}"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.imageId", "imageId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="get", method="GET", path=f"{IMAGES}/{{{{imageId}}}}",
             test_script=[*assert_status(200), "pm.test('status READY', () => pm.expect(pm.response.json().status).to.eql('READY'));"]),
        Step(name="del-img", method="DELETE", path=f"{IMAGES}/{{{{imageId}}}}", test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="del-base-img", method="DELETE", path=f"{IMAGES}/{{{{baseImageId}}}}", test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

CASES.append(Case(
    id="IMG-CR-CRUD-FROM-SNAPSHOT-OK",
    title="Create image из snapshot → status READY",
    classes=["CRUD"], priority="P2",
    steps=[
        *_pre_disk("snapsrc"),
        Step(name="cr-snapshot", method="POST", path="/compute/v1/snapshots",
             body={"projectId": "{{_suiteFolderId}}", "name": "snap-imgsrc-{{runId}}", "diskId": "{{baseDiskId}}"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.snapshotId", "snapshotId")]),
        poll_operation_until_done(),
        Step(name="cr-img-from-snap", method="POST", path=IMAGES,
             body={"projectId": "{{_suiteFolderId}}", "name": "img-fromsnap-{{runId}}", "snapshotId": "{{snapshotId}}"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.imageId", "imageId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="get", method="GET", path=f"{IMAGES}/{{{{imageId}}}}",
             test_script=[*assert_status(200), "pm.test('status READY', () => pm.expect(pm.response.json().status).to.eql('READY'));"]),
        Step(name="del-img", method="DELETE", path=f"{IMAGES}/{{{{imageId}}}}", test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="del-snap", method="DELETE", path="/compute/v1/snapshots/{{snapshotId}}", test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_cleanup_base_disk(),
    ],
))

CASES.append(Case(
    id="IMG-CR-VAL-FOLDER-REQUIRED",
    title="Create без projectId → 400 InvalidArgument",
    classes=["VAL"], priority="P0",
    steps=[Step(name="cr-nf", method="POST", path=IMAGES, body={"name": "img-nf-{{runId}}", "uri": _SAMPLE_URI},
                test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")])],
))

CASES.append(Case(
    id="IMG-CR-VAL-NO-SOURCE",
    title="Create без источника (нет image_id/snapshot_id/disk_id/uri) → 400 InvalidArgument 'exactly one of'",
    classes=["VAL", "NEG"], priority="P0",
    steps=[Step(name="cr-no-src", method="POST", path=IMAGES, body={"projectId": "{{_suiteFolderId}}", "name": "img-ns-{{runId}}"},
                # probe-needed: точный YC text — предполагаем mention "exactly one"/"source"
                test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")])],
))

CASES.append(Case(
    id="IMG-CR-VAL-MULTIPLE-SOURCE",
    title="Create с двумя источниками (image_id + uri) → 400 InvalidArgument",
    classes=["VAL", "NEG"], priority="P1",
    steps=[Step(name="cr-multi-src", method="POST", path=IMAGES,
                body={"projectId": "{{_suiteFolderId}}", "name": "img-ms-{{runId}}",
                      "imageId": "{{garbageImageId}}", "uri": _SAMPLE_URI},
                test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")])],
))

CASES.append(Case(
    id="IMG-CR-NEG-SOURCE-DISK-NOTFOUND",
    title="Create image из garbage diskId → async NOT_FOUND",
    classes=["NEG"], priority="P1",
    steps=[
        Step(name="cr-bad-disk", method="POST", path=IMAGES,
             body={"projectId": "{{_suiteFolderId}}", "name": "img-bd-{{runId}}", "diskId": "{{garbageComputeId}}"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        assert_op_error(5, "NOT_FOUND"),
    ],
))

CASES.append(Case(
    id="IMG-CR-NEG-SOURCE-IMAGE-NOTFOUND",
    title="Create image из garbage imageId → async NOT_FOUND",
    classes=["NEG"], priority="P1",
    steps=[
        Step(name="cr-bad-img", method="POST", path=IMAGES,
             body={"projectId": "{{_suiteFolderId}}", "name": "img-bi-{{runId}}", "imageId": "{{garbageImageId}}"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        assert_op_error(5, "NOT_FOUND"),
    ],
))

CASES.append(Case(
    id="IMG-CR-NEG-FOLDER-NOTFOUND",
    title="Create image в garbage projectId → async NOT_FOUND 'Folder ... not found'",
    classes=["NEG"], priority="P0",
    steps=[
        # # requires peer-validation enabled
        Step(name="cr-bad-folder", method="POST", path=IMAGES,
             body={"projectId": "{{garbageRmId}}", "name": "img-bf-{{runId}}", "uri": _SAMPLE_URI},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        assert_op_error(5, "NOT_FOUND", msg_substr="folder"),
    ],
))

CASES.append(Case(
    id="IMG-CR-NEG-DUP-NAME",
    title="Create image с дубликатом name в folder → async ALREADY_EXISTS",
    classes=["NEG", "CONC"], priority="P1",
    steps=[
        Step(name="cr-1", method="POST", path=IMAGES,
             body={"projectId": "{{_suiteFolderId}}", "name": "img-dup-{{runId}}", "uri": _SAMPLE_URI},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.imageId", "imageId")]),
        poll_operation_until_done(),
        Step(name="cr-2-dup", method="POST", path=IMAGES,
             body={"projectId": "{{_suiteFolderId}}", "name": "img-dup-{{runId}}", "uri": _SAMPLE_URI},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        assert_op_error(6, "ALREADY_EXISTS"),
        Step(name="cleanup", method="DELETE", path=f"{IMAGES}/{{{{imageId}}}}", test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

CASES.append(Case(
    id="IMG-CR-VAL-FAMILY-INVALID",
    title="Create image с family содержащим спец-символы → 400 (proto pattern '|[a-z][-a-z0-9]{1,61}[a-z0-9]')",
    classes=["VAL"], priority="P2",
    steps=[Step(name="cr-bad-fam", method="POST", path=IMAGES,
                body={"projectId": "{{_suiteFolderId}}", "name": "img-bf2-{{runId}}", "family": "Bad_Family!", "uri": _SAMPLE_URI},
                test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")])],
))

# ---------------------------------------------------------------------------
# IMG-GLF — GetLatestByFamily
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="IMG-GLF-CRUD-OK",
    title="GetLatestByFamily: создать 2 image одного family → GLF возвращает более новый",
    classes=["CRUD"], priority="P1",
    steps=[
        Step(name="cr-img-1", method="POST", path=IMAGES,
             body={"projectId": "{{_suiteFolderId}}", "name": "img-glf1-{{runId}}", "family": "glf-fam-{{runId}}", "uri": _SAMPLE_URI},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.imageId", "img1Id")]),
        poll_operation_until_done(),
        Step(name="cr-img-2", method="POST", path=IMAGES,
             body={"projectId": "{{_suiteFolderId}}", "name": "img-glf2-{{runId}}", "family": "glf-fam-{{runId}}", "uri": _SAMPLE_URI},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.imageId", "img2Id")]),
        poll_operation_until_done(),
        Step(name="glf", method="GET", path=f"{IMAGES}:latestByFamily?projectId={{{{_suiteFolderId}}}}&family=glf-fam-{{{{runId}}}}",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('family matches', () => pm.expect(j.family).to.match(/^glf-fam-/));",
                          "pm.test('returns the newer image (img2)', () => pm.expect(j.id).to.eql(pm.environment.get('img2Id')));"]),
        Step(name="del-img2", method="DELETE", path=f"{IMAGES}/{{{{img2Id}}}}", test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="del-img1", method="DELETE", path=f"{IMAGES}/{{{{img1Id}}}}", test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

CASES.append(Case(
    id="IMG-GLF-NEG-NOTFOUND",
    title="GetLatestByFamily для family без images → 404 NOT_FOUND",
    classes=["NEG"], priority="P1",
    steps=[Step(name="glf-nx", method="GET",
                path=f"{IMAGES}:latestByFamily?projectId={{{{_suiteFolderId}}}}&family=nonexistent-fam-{{{{runId}}}}",
                test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")])],
))

CASES.append(Case(
    id="IMG-GLF-VAL-FOLDER-REQUIRED",
    title="GetLatestByFamily без projectId → 400 InvalidArgument",
    classes=["VAL"], priority="P1",
    steps=[Step(name="glf-nf", method="GET", path=f"{IMAGES}:latestByFamily?family=anything",
                test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")])],
))

# ---------------------------------------------------------------------------
# IMG-GET / LIST
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="IMG-GET-NEG-NOTFOUND",
    title="Get well-formed-but-absent imageId → 404 NOT_FOUND",
    classes=["NEG"], priority="P0",
    steps=[Step(name="get-nx", method="GET", path=f"{IMAGES}/{{{{garbageImageId}}}}",
                test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")])],
))

CASES.append(Case(
    id="IMG-GET-CONF-NF-TEXT",
    title="Get garbage imageId → текст содержит 'not found'",
    classes=["CONF", "NEG"], priority="P1",
    steps=[Step(name="get-nx", method="GET", path=f"{IMAGES}/{{{{garbageImageId}}}}",
                test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND"),
                             "pm.test('text mentions not found', () => pm.expect((pm.response.json().message || '').toLowerCase()).to.include('not found'));"])],
))

CASES.append(Case(
    id="IMG-LST-CRUD-OK",
    title="List images в folder → images array",
    classes=["CRUD"], priority="P1",
    steps=[Step(name="list", method="GET", path=f"{IMAGES}?projectId={{{{_suiteFolderId}}}}",
                test_script=[*assert_status(200), "pm.test('images is array', () => pm.expect(pm.response.json().images || []).to.be.an('array'));"])],
))

CASES.append(Case(
    id="IMG-LST-VAL-FOLDER-REQUIRED",
    title="List без projectId → 400 InvalidArgument",
    classes=["VAL", "AUTHZ"], priority="P0",
    steps=[Step(name="list-nf", method="GET", path=IMAGES,
                test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")])],
))

# ---------------------------------------------------------------------------
# IMG-UPD — Update
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="IMG-UPD-CRUD-NAME-DESC-LABELS-OK",
    title="Update image mask=name,description,labels → все три применены",
    classes=["CRUD", "STATE"], priority="P1",
    steps=[
        Step(name="cr", method="POST", path=IMAGES,
             body={"projectId": "{{_suiteFolderId}}", "name": "img-upd-{{runId}}", "uri": _SAMPLE_URI,
                   "description": "init", "labels": {"orig": "1"}},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.imageId", "imageId")]),
        poll_operation_until_done(),
        Step(name="patch", method="PATCH", path=f"{IMAGES}/{{{{imageId}}}}",
             body={"updateMask": "name,description,labels", "name": "img-upd2-{{runId}}",
                   "description": "updated-newman", "labels": {"env": "prod"}},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="verify", method="GET", path=f"{IMAGES}/{{{{imageId}}}}",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('name updated', () => pm.expect(j.name).to.match(/^img-upd2-/));",
                          "pm.test('description updated', () => pm.expect(j.description).to.eql('updated-newman'));",
                          "pm.test('label env', () => pm.expect((j.labels || {}).env).to.eql('prod'));"]),
        Step(name="cleanup", method="DELETE", path=f"{IMAGES}/{{{{imageId}}}}", test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

CASES.append(Case(
    id="IMG-UPD-MASK-IMMUTABLE-FAMILY",
    title="Update image mask=family → 400 InvalidArgument (immutable) или 404",
    classes=["STATE", "VAL", "CONF"], priority="P1",
    steps=[Step(name="patch-imm-fam", method="PATCH", path=f"{IMAGES}/{{{{garbageImageId}}}}",
                body={"updateMask": "family", "family": "newfam"},
                test_script=["pm.test('rejected (400 immutable or 404)', () => pm.expect(pm.response.code).to.be.oneOf([400, 404]));",
                             "if (pm.response.code === 400) { const j = pm.response.json(); pm.test('code 3', () => pm.expect(j.code).to.eql(3)); }"])],
))

CASES.append(Case(
    id="IMG-UPD-MASK-UNKNOWN-FIELD",
    title="Update image с unknown field в update_mask → 400 InvalidArgument или 404",
    classes=["VAL", "STATE"], priority="P1",
    steps=[Step(name="patch-unk", method="PATCH", path=f"{IMAGES}/{{{{garbageImageId}}}}",
                body={"updateMask": "totally_unknown_xyz", "description": "x"},
                test_script=["pm.test('rejected (400 or 404)', () => pm.expect(pm.response.code).to.be.oneOf([400, 404]));"])],
))

CASES.append(Case(
    id="IMG-UPD-AUTHZ-NF-SYNC",
    title="Update несуществующего image → sync 404 NOT_FOUND",
    classes=["NEG", "AUTHZ"], priority="P1",
    steps=[Step(name="patch-nx", method="PATCH", path=f"{IMAGES}/{{{{garbageImageId}}}}",
                body={"updateMask": "description", "description": "x"},
                test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")])],
))

# ---------------------------------------------------------------------------
# IMG-DEL — Delete
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="IMG-DEL-CRUD-OK",
    title="Delete image → Operation done; Get → 404",
    classes=["CRUD", "STATE"], priority="P1",
    steps=[
        Step(name="cr", method="POST", path=IMAGES,
             body={"projectId": "{{_suiteFolderId}}", "name": "img-delok-{{runId}}", "uri": _SAMPLE_URI},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.imageId", "imageId")]),
        poll_operation_until_done(),
        Step(name="del", method="DELETE", path=f"{IMAGES}/{{{{imageId}}}}",
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="get-404", method="GET", path=f"{IMAGES}/{{{{imageId}}}}",
             test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")]),
    ],
))

CASES.append(Case(
    id="IMG-DEL-NEG-NOTFOUND",
    title="Delete несуществующего image → sync 404 NOT_FOUND",
    classes=["NEG"], priority="P0",
    steps=[Step(name="del-nx", method="DELETE", path=f"{IMAGES}/{{{{garbageImageId}}}}",
                test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")])],
))

# ---------------------------------------------------------------------------
# IMG-LOP — ListOperations
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="IMG-LOP-CRUD-OK",
    title="ListOperations image → содержит как минимум create-op",
    classes=["CRUD"], priority="P1",
    steps=[
        Step(name="cr", method="POST", path=IMAGES,
             body={"projectId": "{{_suiteFolderId}}", "name": "img-lop-{{runId}}", "uri": _SAMPLE_URI},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.imageId", "imageId")]),
        poll_operation_until_done(),
        Step(name="list-ops", method="GET", path=f"{IMAGES}/{{{{imageId}}}}/operations",
             test_script=[*assert_status(200), "pm.test('at least 1 op', () => pm.expect((pm.response.json().operations || []).length).to.be.at.least(1));"]),
        Step(name="cleanup", method="DELETE", path=f"{IMAGES}/{{{{imageId}}}}", test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

# ---------------------------------------------------------------------------
# IMG — lifecycle conformance
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="IMG-LIFECYCLE-CONF",
    title="Full lifecycle conformance: CRUD-инварианты image",
    classes=["CRUD", "CONF", "STATE"], priority="P1",
    steps=[
        Step(name="cr", method="POST", path=IMAGES,
             body={"projectId": "{{_suiteFolderId}}", "name": "img-life-{{runId}}", "uri": _SAMPLE_URI},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.imageId", "imageId")]),
        poll_operation_until_done(),
        Step(name="get-1", method="GET", path=f"{IMAGES}/{{{{imageId}}}}",
             test_script=[*assert_status(200), "pm.test('id', () => pm.expect(pm.response.json().id).to.eql(pm.environment.get('imageId')));"]),
        Step(name="lst-includes", method="GET", path=f"{IMAGES}?projectId={{{{_suiteFolderId}}}}&pageSize=1000",
             test_script=[*assert_status(200),
                          "const ids = (pm.response.json().images || []).map(x => x.id);",
                          "pm.test('list contains', () => pm.expect(ids).to.include(pm.environment.get('imageId')));"]),
        Step(name="upd", method="PATCH", path=f"{IMAGES}/{{{{imageId}}}}",
             body={"updateMask": "description", "description": "life-conf"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="get-after-upd", method="GET", path=f"{IMAGES}/{{{{imageId}}}}",
             test_script=[*assert_status(200), "pm.test('description updated', () => pm.expect(pm.response.json().description).to.eql('life-conf'));"]),
        Step(name="del", method="DELETE", path=f"{IMAGES}/{{{{imageId}}}}",
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="lst-excludes", method="GET", path=f"{IMAGES}?projectId={{{{_suiteFolderId}}}}&pageSize=1000",
             test_script=[*assert_status(200),
                          "const ids = (pm.response.json().images || []).map(x => x.id);",
                          "pm.test('list does not contain', () => pm.expect(ids).to.not.include(pm.environment.get('imageId')));"]),
        Step(name="get-404", method="GET", path=f"{IMAGES}/{{{{imageId}}}}",
             test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")]),
    ],
))

# ---------------------------------------------------------------------------
# Расширения через generic-блоки
# ---------------------------------------------------------------------------

# Для name/labels/desc-блоков нужен валидный source — используем uri.
_img_src = {"uri": _SAMPLE_URI}
CASES.extend(list_page_block("IMG", IMAGES))
CASES.extend(filter_block("IMG", IMAGES))
CASES.extend(name_validation_block("IMG", IMAGES, _img_src))
CASES.extend(description_validation_block("IMG", IMAGES, _img_src))
CASES.extend(labels_validation_block("IMG", IMAGES, _img_src))
CASES.extend(http_method_block("IMG", IMAGES))
CASES.extend(malformed_body_block("IMG", IMAGES))
CASES.extend(security_injection_block("IMG", IMAGES, IMAGES, _img_src))

CASES.append(Case(
    id="IMG-CR-CONF-ID-PREFIX-FD8",
    title="Create image → operation.id prefix 'epd', metadata.imageId prefix 'fd8'",
    classes=["CONF"], priority="P1",
    steps=[
        Step(name="cr", method="POST", path=IMAGES,
             body={"projectId": "{{_suiteFolderId}}", "name": "img-idpfx-{{runId}}", "uri": _SAMPLE_URI},
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('operation.id epd...', () => pm.expect(j.id).to.match(/^epd[a-z0-9]{17}$/));",
                          "pm.test('metadata.imageId fd8...', () => pm.expect(j.metadata && j.metadata.imageId).to.match(/^fd8[a-z0-9]{17}$/));",
                          *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.imageId", "imageId")]),
        poll_operation_until_done(),
        Step(name="cleanup", method="DELETE", path=f"{IMAGES}/{{{{imageId}}}}", test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

# blocked: os_product_ids в Create — нужен kacho-marketplace.
# CASES.append(...)  # blocked:kacho-marketplace
