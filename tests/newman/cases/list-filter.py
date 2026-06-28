# Copyright (c) PRO-Robotech
# SPDX-License-Identifier: BUSL-1.1

"""Case-set для D-consumer (§11, D-40..D-47) — per-object filtered List в kacho-compute.

Источник истины — docs/specs/rbac-rules-model-2026-acceptance.md под-фаза D
(LST-1..6, D-40..D-47); workspace issue PRO-Robotech/kacho-workspace#111.

Что проверяем (black-box через api-gateway, реальный iam + OpenFGA в стенде):

  - D-40/D-45 read==enforce (happy): авторизованный субъект (jwtProjectAdminA1)
    видит СВОИ объекты в List. Это и есть RED→GREEN-пара D-consumer:
    до фикса compute слал в iam.ListObjects action="compute.instances.read"
    (verb "read"), который iam-сервер НЕ мапит на relation → отвечает
    InvalidArgument → compute оборачивает в Unavailable (503) → КАЖДЫЙ List
    ломается при list-filter.enabled=true. После фикса verb="list" → iam мапит
    на "viewer" (та же relation, что per-RPC Check для Get == read==enforce) →
    List возвращает 200 и доступные объекты.

  - D-44 no-leak (negative): well-formed-но-отсутствующий instanceId →
    Get == 404 NOT_FOUND (НЕ 403 PERMISSION_DENIED — existence не
    подтверждается) и объект отсутствует в List. read==enforce: List-видимость
    == Check-allow поверх тех же materialized tuples + scope_grant.

  - D-44 cross-account no-leak: jwtAccountAdminB НЕ видит instance проекта A1
    в своём scope (per-object фильтрация, не all-or-nothing leak).

Pre-conditions: tests/authz-fixtures/setup.sh (те же JWT/проекты, что authz-deny).
Требует list-filter.enabled=true на стенде (KACHO_COMPUTE_LIST_FILTER_ENABLED).
"""

CASES = []

INSTANCES = "/compute/v1/instances"


def _instance_body(name_suffix, project_var):
    # KAC-266: Instance создаётся без NIC (no auto-NIC).
    return {
        "projectId": f"{{{{{project_var}}}}}",
        "name": f"lf-inst-{name_suffix}-{{{{runId}}}}",
        "zoneId": "ru-central1-a", "platformId": "standard-v3",
        "resourcesSpec": {"memory": "1073741824", "cores": 2},
        "bootDiskSpec": {"diskSpec": {"size": "8589934592", "typeId": "network-ssd"}},
    }


# ---------------------------------------------------------------------------
# D-40/D-45 — read==enforce happy: owner sees own instance in (filtered) List.
# ---------------------------------------------------------------------------
CASES.append(Case(
    id="LF-INST-LST-READ-ENFORCE-OWNER-SEES-OWN",
    title="[D-40/D-45] PA1 создаёт instance в project-A1 и видит его в filtered List (read==enforce, verb→viewer)",
    classes=["AUTHZ", "POS", "LST"], priority="P0",
    steps=[
        Step(name="create-own", method="POST", path=INSTANCES,
             body=_instance_body("own", "projectA1Id"), auth="jwtProjectAdminA1",
             test_script=[*assert_status(200),
                          *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.instanceId", "lfInstanceId")]),
        poll_operation_until_done(), assert_op_success(),
        # filtered List as the SAME (authorized) subject → 200 + own instance visible.
        Step(name="list-own", method="GET",
             path=f"{INSTANCES}?projectId={{{{projectA1Id}}}}&pageSize=1000",
             auth="jwtProjectAdminA1",
             test_script=[*assert_status(200),
                          "const insts = pm.response.json().instances || [];",
                          "pm.test('[D-45] filtered List returns 200 (not 503/InvalidArgument from broken verb)', () => pm.expect(pm.response.code).to.eql(200));",
                          "const mine = insts.find(x => x.id === pm.environment.get('lfInstanceId'));",
                          "pm.test('[D-40] owner sees own instance in filtered List (read==enforce)', () => pm.expect(mine, JSON.stringify(insts.map(i=>i.id))).to.be.an('object'));"]),
        # cleanup.
        Step(name="del-own", method="DELETE", path=f"{INSTANCES}/{{{{lfInstanceId}}}}",
             auth="jwtProjectAdminA1",
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))


# ---------------------------------------------------------------------------
# D-44 — no-leak: well-formed-but-absent id → 404 (NOT 403), not in List.
# ---------------------------------------------------------------------------
CASES.append(Case(
    id="LF-INST-GET-NOLEAK-404-NOT-403",
    title="[D-44] PA1 Get well-formed-но-отсутствующего instanceId → 404 NOT_FOUND (no-leak, не 403)",
    classes=["AUTHZ", "NEG", "LST"], priority="P0",
    steps=[
        Step(name="get-absent", method="GET", path=f"{INSTANCES}/{{{{garbageComputeId}}}}",
             auth="jwtProjectAdminA1",
             test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND"),
                          "pm.test('[D-44] no-leak: NOT_FOUND, not PERMISSION_DENIED', () => pm.expect(pm.response.json().code).to.not.eql(7));"]),
    ],
))


# ---------------------------------------------------------------------------
# Over-show leak guard — subject-source: list-filter обязан брать subject из
# request Principal (x-kacho-principal-*), а НЕ из несуществующих x-kacho-subject*
# заголовков. До фикса subject="" → bypass-all → List возвращал ВСЕ объекты
# проекта мимо list-authz (existence+metadata leak).
#
# Проверка: jwtNoBindings — аутентифицированный субъект БЕЗ грантов в project-A1.
# Его List project-A1 обязан быть пустым (fail-closed), а instance, созданный
# PA1, не должен в нём появиться. RED при subject-source bug (bypass-all утекал
# instance), GREEN после фикса (principal-based subject → пустой allow-list).
# ---------------------------------------------------------------------------
CASES.append(Case(
    id="LF-INST-LST-OVERSHOW-LEAK-GUARD",
    title="[leak] jwtNoBindings List project-A1 → instance PA1 не виден (subject из principal, fail-closed)",
    classes=["AUTHZ", "NEG", "LST"], priority="P0",
    steps=[
        Step(name="create-a1-pa1", method="POST", path=INSTANCES,
             body=_instance_body("leak", "projectA1Id"), auth="jwtProjectAdminA1",
             test_script=[*assert_status(200),
                          *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.instanceId", "lfLeakInstanceId")]),
        poll_operation_until_done(), assert_op_success(),
        # Authenticated-but-not-granted subject lists project-A1. The handler
        # derives subject from the principal and consults the filter (empty
        # allow-list) → MUST NOT leak the PA1 instance. A non-empty result here
        # is the over-show leak.
        Step(name="list-a1-as-nobindings", method="GET",
             path=f"{INSTANCES}?projectId={{{{projectA1Id}}}}&pageSize=1000",
             auth="jwtNoBindings",
             test_script=[
                 "pm.test('[leak] response is not a server error (fail-closed, not 5xx)', () => pm.expect(pm.response.code).to.be.oneOf([200, 403]));",
                 "const insts = (pm.response.json().instances) || [];",
                 "pm.test('[leak] PA1 instance NOT leaked to a not-granted subject', () => pm.expect(insts.map(x=>x.id)).to.not.include(pm.environment.get('lfLeakInstanceId')));",
             ]),
        # cleanup as owner.
        Step(name="del-a1-leak", method="DELETE", path=f"{INSTANCES}/{{{{lfLeakInstanceId}}}}",
             auth="jwtProjectAdminA1",
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))


# ---------------------------------------------------------------------------
# D-44 — cross-account no-leak: AAB не видит instance проекта A1 в своём scope.
# (AAB List своего project-B1 → не содержит A1-объект; A1-проект для AAB — DENY,
#  поэтому per-object изоляция проверяется тем, что A1-instance не утекает в B1.)
# ---------------------------------------------------------------------------
CASES.append(Case(
    id="LF-INST-LST-CROSS-ACCOUNT-NO-LEAK",
    title="[D-44] AAB List instances project-B1 → instance проекта A1 не виден (per-object изоляция)",
    classes=["AUTHZ", "NEG", "LST"], priority="P1",
    steps=[
        # PA1 создаёт instance в A1.
        Step(name="create-a1", method="POST", path=INSTANCES,
             body=_instance_body("xacct", "projectA1Id"), auth="jwtProjectAdminA1",
             test_script=[*assert_status(200),
                          *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.instanceId", "lfXacctInstanceId")]),
        poll_operation_until_done(), assert_op_success(),
        # AAB листит СВОЙ project-B1 → A1-instance не должен присутствовать.
        Step(name="list-b1-as-aab", method="GET",
             path=f"{INSTANCES}?projectId={{{{projectB1Id}}}}&pageSize=1000",
             auth="jwtAccountAdminB",
             test_script=[*assert_status(200),
                          "const ids = (pm.response.json().instances || []).map(x => x.id);",
                          "pm.test('[D-44] cross-account: A1 instance not leaked into B1 List', () => pm.expect(ids).to.not.include(pm.environment.get('lfXacctInstanceId')));"]),
        # cleanup as owner.
        Step(name="del-a1", method="DELETE", path=f"{INSTANCES}/{{{{lfXacctInstanceId}}}}",
             auth="jwtProjectAdminA1",
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))
