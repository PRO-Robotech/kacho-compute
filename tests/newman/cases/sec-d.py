"""Case-set для SEC-D (kacho-compute): FGA owner-tuple через kacho-iam
(transactional-outbox) + opt-in mTLS.

SEC-D устраняет прямой доступ compute к OpenFGA: на каждый resource Create/Delete
intent owner-tuple пишется строкой в compute_fga_register_outbox В ТОЙ ЖЕ
writer-tx, что и Insert/Delete ресурса; register-drainer применяет его через
InternalIAMService.RegisterResource/UnregisterResource. Публичный контракт
ресурсов НЕ меняется (эпик #8) — эти кейсы гоняют существующие публичные RPC и
проверяют, что after-create per-resource Get резолвится (owner-tuple применён
eventual через IAM), а Delete → Get 404.

Контракт изоляции: каждый case в своём runId, работает внутри pre-allocated
existingProjectId (_suiteFolderId из env); имена суффиксуются {{runId}}.
id-prefix Disk = `epd`.

mTLS-mismatch (SEC-D-21) и cross-service-owner-down (SEC-D-23) — отдельные
негативы, требующие управления инфраструктурой стенда (peer down / per-edge
TLS-flag); помечены `# requires`-аннотацией и гоняются в dedicated профиле, не в
обычном regression-проходе.
"""

CASES = []

DISKS = "/compute/v1/disks"
_DEF_SIZE = 10737418240  # 10 GiB


# ---------------------------------------------------------------------------
# SEC-D-15 — happy: Create → Operation done → Get показывает ресурс (owner-tuple
# применён eventual через IAM register-drainer) → Delete → Get 404.
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="SECD-CR-GET-AFTER-TUPLE-OK",
    title="SEC-D-15: Create disk → Operation done → Get показывает ресурс (per-resource Check резолвится, owner-tuple применён через IAM) → Delete → Get 404",
    classes=["CONF", "IDM"], priority="P1",
    steps=[
        Step(name="create", method="POST", path=DISKS,
             body={"projectId": "{{_suiteFolderId}}", "name": f"secd-disk-{{{{runId}}}}",
                   "zoneId": "{{existingZoneId}}", "size": _DEF_SIZE,
                   "labels": {"suite": "sec-d"}},
             test_script=[*assert_status(200), *assert_operation_envelope(),
                          *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.diskId", "diskId")]),
        poll_operation_until_done(),
        assert_op_success(),
        # per-resource Get: резолвится → owner-tuple зарегистрирован в IAM (раньше
        # best-effort dual-write мог потерять tuple → DENY навсегда; теперь intent
        # durable + retried, окно DENY конечно).
        Step(name="get", method="GET", path=f"{DISKS}/{{{{diskId}}}}",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('id matches & epd prefix', () => { pm.expect(j.id).to.eql(pm.environment.get('diskId')); pm.expect(j.id).to.match(/^epd/); });",
                          "pm.test('projectId matches', () => pm.expect(j.projectId).to.eql(pm.environment.get('_suiteFolderId')));",
                          "pm.test('status READY', () => pm.expect(j.status).to.eql('READY'));",
                          *assert_created_at_seconds()]),
        Step(name="delete", method="DELETE", path=f"{DISKS}/{{{{diskId}}}}",
             test_script=[*assert_status(200), *assert_operation_envelope(),
                          *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        assert_op_success(),
        # после Delete — Get → 404 (unregister-intent тоже записан в writer-tx).
        Step(name="get-after-delete", method="GET", path=f"{DISKS}/{{{{diskId}}}}",
             test_script=[*assert_grpc_code(404, "NOT_FOUND")]),
    ],
))


# ---------------------------------------------------------------------------
# SEC-D negative (deterministic, без управления инфраструктурой): Delete
# несуществующего ресурса → async Operation завершается error NOT_FOUND. Это
# тот же async-мутация-путь (Operation), что и happy-case, но через него видно,
# что мутация корректно фейлится, а unregister-intent для отсутствующего ресурса
# не пишется (нет orphan-intent).
# ---------------------------------------------------------------------------

CASES.append(Case(
    id="SECD-DEL-NEG-NOT-FOUND",
    title="SEC-D: Delete несуществующего disk → Operation error NOT_FOUND (async-мутация-путь корректно фейлится, orphan unregister-intent не пишется)",
    classes=["NEG"], priority="P2",
    steps=[
        Step(name="delete-missing", method="DELETE", path=f"{DISKS}/epd00000000000000000",
             test_script=[*assert_status(200), *assert_operation_envelope(),
                          *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        assert_op_error(5, "NOT_FOUND", msg_substr="not found"),
    ],
))


# requires: kacho-vpc peer down — SEC-D-23 (cross-service NIC IPAM мутация при
# недоступном owner → Operation error UNAVAILABLE). Это синхронная cross-service
# ref-validation на request-path (НЕ FGA-tuple-path, который асинхронный через
# outbox). Гоняется в dedicated chaos-профиле, не в обычном regression-проходе.

# requires: per-edge mTLS mismatch — SEC-D-21 (vpc-client mTLS-on, iam-listener
# mTLS-off → register-drainer вызов завершается UNAVAILABLE, register-intent
# остаётся durable). Требует TLS-flag-управления стендом (SEC-F PKI); dedicated
# mTLS-профиль.
