"""Case-set authz-deny для kacho-compute (KAC-122).

Проверяет default-deny matrix для 6 субъектов на каждом публичном CRUD compute-ресурсов
+ catalog-read для DiskType (Region/Zone serving снят в Stage S7 — Geography в kacho-geo).
Источник истины матрицы — `docs/superpowers/specs/2026-05-19-authz-default-deny-matrix-newman-design.md`.

Pre-conditions: `tests/authz-fixtures/setup.sh`. Env-var'ы те же что vpc.
"""

CASES = []

SUBJECTS = [
    ("ANON", "anon",       "anonymous"),
    ("NOB",  "no-bind",    "jwtNoBindings"),
    ("PA1",  "proj-adm",   "jwtProjectAdminA1"),
    ("AAA",  "acct-adm-a", "jwtAccountAdminA"),
    ("AAB",  "acct-adm-b", "jwtAccountAdminB"),
    ("INV",  "invitee",    "jwtInvitee"),
]

EXPECT = {
    "project-A1":         {"ANON":"DENY","NOB":"DENY","PA1":"ALLOW","AAA":"ALLOW","AAB":"DENY", "INV":"ALLOW"},
    "project-B1":         {"ANON":"DENY","NOB":"DENY","PA1":"DENY", "AAA":"DENY", "AAB":"ALLOW","INV":"ALLOW"},
    "catalog-read":       {"ANON":"DENY","NOB":"ALLOW","PA1":"ALLOW","AAA":"ALLOW","AAB":"ALLOW","INV":"ALLOW"},
    "catalog-mutate":     {"ANON":"DENY","NOB":"DENY","PA1":"DENY", "AAA":"DENY", "AAB":"DENY", "INV":"DENY"},
}


def deny_asserts(case_id):
    return [
        f"pm.test('[{case_id}] DENY: status 403', () => pm.expect(pm.response.code, JSON.stringify(pm.response.text())).to.equal(403));",
        "let j; try { j = pm.response.json(); } catch(e) { j = null; }",
        f"pm.test('[{case_id}] DENY: grpc code 7 (PERMISSION_DENIED)', () => pm.expect(j && j.code, JSON.stringify(j)).to.equal(7));",
        f"pm.test('[{case_id}] DENY: message contains permission denied', () => pm.expect((j && j.message || '').toLowerCase()).to.contain('permission denied'));",
    ]


def allow_asserts(case_id):
    return [
        f"pm.test('[{case_id}] ALLOW: not 403', () => pm.expect(pm.response.code, 'unexpected 403: ' + pm.response.text()).to.not.equal(403));",
        "let _j; try { _j = pm.response.json(); } catch(e) { _j = null; }",
        f"pm.test('[{case_id}] ALLOW: not Unauthenticated (16)', () => pm.expect(_j && _j.code, JSON.stringify(_j)).to.not.equal(16));",
    ]


def unauth_asserts(case_id):
    # Anonymous (no credentials) → 401 + code 16 (UNAUTHENTICATED), not 403 + code 7
    # (PERMISSION_DENIED). gRPC/HTTP convention: missing credentials → UNAUTHENTICATED
    # (16) → HTTP 401; authenticated-but-denied → PERMISSION_DENIED (7) → HTTP 403.
    return [
        f"pm.test('[{case_id}] UNAUTH: status 401', () => pm.expect(pm.response.code, JSON.stringify(pm.response.text())).to.equal(401));",
        "let j; try { j = pm.response.json(); } catch(e) { j = null; }",
        f"pm.test('[{case_id}] UNAUTH: grpc code 16 (UNAUTHENTICATED)', () => pm.expect(j && j.code, JSON.stringify(j)).to.equal(16));",
    ]


def read_deny_asserts(case_id):
    # Hide-existence: a denied single-resource read (Get) on a verb-bearing compute
    # resource is surfaced as NotFound (404 / code 5), never PermissionDenied — no
    # enumeration / existence leak. Applies to authenticated-but-denied AND to a denied
    # read of a (well-formed) nonexistent id — both yield the same 404, so an attacker
    # cannot tell "exists but forbidden" from "does not exist".
    return [
        f"pm.test('[{case_id}] READ-DENY: status 404 (hide existence)', () => pm.expect(pm.response.code, JSON.stringify(pm.response.text())).to.equal(404));",
        "let j; try { j = pm.response.json(); } catch(e) { j = null; }",
        f"pm.test('[{case_id}] READ-DENY: grpc code 5 (NOT_FOUND, not 7)', () => pm.expect(j && j.code, JSON.stringify(j)).to.equal(5));",
        f"pm.test('[{case_id}] READ-DENY: no deny_reasons leak', () => pm.expect(JSON.stringify(j || {{}}).toLowerCase()).to.not.include('deny_reasons'));",
    ]


def _is_single_resource_get(path):
    # A single-resource Get targets one object: the path's last segment is a concrete id
    # — a `{{var}}` placeholder or a literal resource id (3-char prefix + ≥17 chars) —
    # with NO query string. A List (collection) carries a ?query (e.g. ?folderId=…) or
    # ends in the bare plural (`/instances`); those are NOT single reads and a denied List
    # stays PermissionDenied (403), not hidden as 404.
    if "?" in path:
        return False
    last = path.rstrip("/").rsplit("/", 1)[-1]
    if last.startswith("{{") and last.endswith("}}"):
        return True
    # Literal resource id: 3-char alpha prefix + ≥17 trailing alnum chars (matches the
    # GARBAGE_ID format), distinguishing it from the bare plural collection name.
    return len(last) >= 20 and last[:3].isalpha() and last[3:].isalnum()


def emit(case_id_prefix, title, scope, method, path, body, subject):
    code, label, auth = subject
    decision = EXPECT[scope][code]
    case_id = f"AUTHZ-{case_id_prefix}-{code}"
    if decision == "DENY":
        if method == "GET" and _is_single_resource_get(path):
            # Hide-existence: a denied single-resource Get on a verb-bearing compute
            # resource → NotFound (404 / code 5), not 403. ONLY a single-resource Get
            # (path ends in /{id}, no ?query) hides existence; a denied List stays
            # PermissionDenied (403). Anonymous (no token) → 401 (authN before authz).
            asserts = unauth_asserts(case_id) if code == "ANON" else read_deny_asserts(case_id)
        else:
            asserts = deny_asserts(case_id)
    else:
        asserts = allow_asserts(case_id)
    CASES.append(Case(
        id=case_id,
        title=f"[{decision}] {title} as {label} ({scope})",
        classes=["AUTHZ", "NEG" if decision == "DENY" else "POS"],
        priority="P1",
        steps=[Step(name=method.lower(), method=method, path=path, body=body, auth=auth, test_script=asserts)],
    ))


GARBAGE_ID = "epdnonexistent000001"   # compute resource id prefix


def define_resource_cases(resource_name, plural, create_body_extra=None, supports_update=True):
    create_body_extra = create_body_extra or {}
    plural_path = f"/compute/v1/{plural}"
    for subj in SUBJECTS:
        body_own = {"folderId": "{{projectA1Id}}", "name": f"authz-{resource_name}-{subj[0].lower()}-own-{{{{runId}}}}", **create_body_extra}
        emit(f"{resource_name.upper()}-CR-OWN", f"Create {resource_name} в project-A1",
             "project-A1", "POST", plural_path, body_own, subj)
        body_cross = {"folderId": "{{projectB1Id}}", "name": f"authz-{resource_name}-{subj[0].lower()}-cross-{{{{runId}}}}", **create_body_extra}
        emit(f"{resource_name.upper()}-CR-CROSS", f"Create {resource_name} в project-B1 (cross-account)",
             "project-B1", "POST", plural_path, body_cross, subj)
        emit(f"{resource_name.upper()}-LS-OWN", f"List {plural} в project-A1",
             "project-A1", "GET", f"{plural_path}?folderId={{{{projectA1Id}}}}", None, subj)
        emit(f"{resource_name.upper()}-LS-CROSS", f"List {plural} в project-B1 (cross-account)",
             "project-B1", "GET", f"{plural_path}?folderId={{{{projectB1Id}}}}", None, subj)
        emit(f"{resource_name.upper()}-GT", f"Get {resource_name} (garbage id)",
             "project-A1", "GET", f"{plural_path}/{GARBAGE_ID}", None, subj)
        if supports_update:
            emit(f"{resource_name.upper()}-UP", f"Update {resource_name} (garbage id)",
                 "project-A1", "PATCH", f"{plural_path}/{GARBAGE_ID}", {"name": "x"}, subj)
        emit(f"{resource_name.upper()}-DL", f"Delete {resource_name} (garbage id)",
             "project-A1", "DELETE", f"{plural_path}/{GARBAGE_ID}", None, subj)


# Project-scoped compute ресурсы.
define_resource_cases("instance", "instances", create_body_extra={
    "zoneId": "ru-central1-a", "platformId": "standard-v3",
    "resourcesSpec": {"memory": "1073741824", "cores": 2},
    "bootDiskSpec": {"diskSpec": {"size": "8589934592", "typeId": "network-ssd"}},
})
define_resource_cases("disk", "disks", create_body_extra={
    "zoneId": "ru-central1-a", "typeId": "network-ssd", "size": "8589934592"
})
define_resource_cases("image", "images", create_body_extra={"family": "authz-test"})
define_resource_cases("snapshot", "snapshots", create_body_extra={"diskId": "epdnonexistent000099"})


# ---------------------------------------------------------------------------
# Catalog resources (DiskType) — read-only публично; admin-mutate
# Region/Zone serving removed (Stage S7) — Geography is owned by kacho-geo.
# ---------------------------------------------------------------------------

CATALOG_READ_RESOURCES = [
    ("disktype", "/compute/v1/diskTypes", "/compute/v1/diskTypes/network-ssd"),
]

for name, list_path, get_path in CATALOG_READ_RESOURCES:
    for subj in SUBJECTS:
        emit(f"{name.upper()}-LS", f"List {name} (catalog)", "catalog-read",
             "GET", list_path, None, subj)
        emit(f"{name.upper()}-GT", f"Get {name} (catalog)", "catalog-read",
             "GET", get_path, None, subj)

# Catalog-mutate (admin-only — via Internal*Service на cluster-internal listener).
# Все 6 субъектов DENY: они либо не admin (anon/NOB/PA1/INV), либо account-level (AAA/AAB).
for name, list_path, _ in CATALOG_READ_RESOURCES:
    for subj in SUBJECTS:
        emit(f"{name.upper()}-CR", f"Create {name} (catalog admin)", "catalog-mutate",
             "POST", list_path, {"id": f"authz-{name}-{subj[0].lower()}", "name": "authz"}, subj)


# ---------------------------------------------------------------------------
# Cross-domain validation (KAC-122 §5.4 CD-*)
#
# KAC-266: the former CD-INST-XACCT-SUBNET case (cross-account subnet via an
# instance NIC spec) was removed — NIC binding is no longer part of the Instance
# lifecycle (no auto-NIC), so Instance.Create performs no cross-account subnet
# peer-validation. Generic instance create-deny (above) already covers the authz
# gate denial for the same subjects.
# ---------------------------------------------------------------------------
