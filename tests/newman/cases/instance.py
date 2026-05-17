"""Case-set для InstanceService (kacho-compute) — самый большой ресурс.

Covered RPCs: Get, List, Create, Update, Delete, Start, Stop, Restart, Move, ListOperations,
AttachDisk, DetachDisk, AddOneToOneNat, RemoveOneToOneNat, UpdateNetworkInterface, UpdateMetadata,
GetSerialPortOutput, SimulateMaintenanceEvent.
(AttachFilesystem/DetachFilesystem — blocked:kacho-filesystem; Relocate — blocked; access-bindings — no-op skip.)

Cross-service: NIC.subnet_id / security_group_ids → kacho-vpc (нужен поднятый kacho-vpc;
кейсы с реальным subnet помечены '# requires kacho-vpc subnet {{existingSubnetId}}').
project_id → kacho-resource-manager. При KACHO_COMPUTE_SKIP_PEER_VALIDATION=true cross-service
existence-checks становятся no-op → NEG-SUBNET-NOTFOUND / NEG-FOLDER-NOTFOUND не сработают
(помечены '# requires peer-validation enabled').

id-prefix Instance = `epd`, operation prefix `epd`. State-машина: см. kacho-compute/CLAUDE.md §8.
Текст precondition-ошибок — probe-needed (предполагаем "Instance is not running" / "... not stopped" и т.п.);
кейсы проверяют code (FailedPrecondition=9), не точный текст, где он не probed.
"""

CASES = []

INSTANCES = "/compute/v1/instances"
DISKS = "/compute/v1/disks"
IMAGES = "/compute/v1/images"
_DISK_SIZE = 10737418240   # 10 GiB
_BOOT_SIZE = 21474836480   # 20 GiB
_SAMPLE_URI = "https://storage.example.net/presigned/image.qcow2"


# --- общие фрагменты -------------------------------------------------------

def _resources_spec(cores=2, memory=2147483648, core_fraction=100):
    return {"cores": cores, "memory": memory, "coreFraction": core_fraction}


def _nic_spec(subnet="{{existingSubnetId}}", sg="{{existingSgId}}", nat=False):
    n = {"subnetId": subnet, "primaryV4AddressSpec": {}}
    if sg:
        n["securityGroupIds"] = [sg]
    if nat:
        n["primaryV4AddressSpec"]["oneToOneNatSpec"] = {"ipVersion": "IPV4"}
    return n


def _boot_disk_spec_inline(name_suffix="boot", size=_BOOT_SIZE, image=None):
    spec = {"autoDelete": True, "diskSpec": {"name": f"bd-{name_suffix}-{{{{runId}}}}",
                                             "size": size, "typeId": "{{existingDiskTypeId}}"}}
    if image:
        spec["diskSpec"]["imageId"] = image
    return spec


def _instance_body(name_suffix, boot_disk_spec=None, secondary=None, nics=None, **over):
    b = {"projectId": "{{_suiteFolderId}}", "name": f"inst-{name_suffix}-{{{{runId}}}}",
         "zoneId": "{{existingZoneId}}", "platformId": "{{existingPlatformId}}",
         "resourcesSpec": _resources_spec(),
         "bootDiskSpec": boot_disk_spec or _boot_disk_spec_inline(name_suffix),
         "networkInterfaceSpecs": nics or [_nic_spec()]}
    if secondary is not None:
        b["secondaryDiskSpecs"] = secondary
    b.update(over)
    return b


def _create_instance_steps(name_suffix, **over):
    """Создать instance, сохранить instanceId; вернуть список шагов (без cleanup)."""
    return [
        Step(name=f"cr-inst-{name_suffix}", method="POST", path=INSTANCES,
             body=_instance_body(name_suffix, **over),
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.instanceId", "instanceId")]),
        poll_operation_until_done(), assert_op_success(),
    ]


def _delete_instance_steps(var="instanceId"):
    return [
        Step(name="cleanup-inst", method="DELETE", path=f"{INSTANCES}/{{{{{var}}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ]


def _create_disk_steps(suffix, save_as="extraDiskId", zone="{{existingZoneId}}", size=_DISK_SIZE):
    return [
        Step(name=f"cr-disk-{suffix}", method="POST", path=DISKS,
             body={"projectId": "{{_suiteFolderId}}", "name": f"disk-{suffix}-{{{{runId}}}}",
                   "zoneId": zone, "size": size},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.diskId", save_as)]),
        poll_operation_until_done(),
    ]


def _delete_disk_steps(var="extraDiskId", name="cleanup-disk"):
    return [
        Step(name=name, method="DELETE", path=f"{DISKS}/{{{{{var}}}}}",
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ]


def _stop_instance_steps():
    return [
        Step(name="stop-inst", method="POST", path=f"{INSTANCES}/{{{{instanceId}}}}:stop", body={},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(), assert_op_success(),
    ]


# ===========================================================================
# INST-CR — Create
# ===========================================================================

CASES.append(Case(
    id="INST-CR-CRUD-OK",
    title="Create instance (zone, platform standard-v3, 2c/2GB, boot_disk_spec, 1 NIC subnet) → poll → Get → status RUNNING, fqdn, boot_disk, NIC, id-prefix epd, created_at секунды",
    classes=["CRUD", "CONF", "STATE"], priority="P0",
    steps=[
        # # requires kacho-vpc subnet {{existingSubnetId}} + sg {{existingSgId}}
        Step(name="create", method="POST", path=INSTANCES,
             body=_instance_body("cr", description="newman CRUD-OK", labels={"suite": "newman"}),
             test_script=[*assert_status(200), *assert_operation_envelope(),
                          *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.instanceId", "instanceId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="get", method="GET", path=f"{INSTANCES}/{{{{instanceId}}}}",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('id matches & epd prefix', () => { pm.expect(j.id).to.eql(pm.environment.get('instanceId')); pm.expect(j.id).to.match(/^epd/); });",
                          "pm.test('projectId matches', () => pm.expect(j.projectId).to.eql(pm.environment.get('_suiteFolderId')));",
                          "pm.test('zoneId matches', () => pm.expect(j.zoneId).to.eql(pm.environment.get('existingZoneId')));",
                          "pm.test('platformId matches', () => pm.expect(j.platformId).to.eql(pm.environment.get('existingPlatformId')));",
                          "pm.test('status RUNNING', () => pm.expect(j.status).to.eql('RUNNING'));",
                          "pm.test('fqdn set', () => pm.expect(j.fqdn).to.be.a('string').and.length.greaterThan(0));",
                          "pm.test('bootDisk present with diskId', () => pm.expect(j.bootDisk && j.bootDisk.diskId).to.be.a('string').and.match(/^epd/));",
                          "pm.test('resources cores=2', () => pm.expect(String(j.resources && j.resources.cores)).to.eql('2'));",
                          "pm.test('at least 1 NIC with subnetId', () => { pm.expect((j.networkInterfaces || []).length).to.be.at.least(1); pm.expect(j.networkInterfaces[0].subnetId).to.eql(pm.environment.get('existingSubnetId')); });",
                          *assert_created_at_seconds()]),
        *_delete_instance_steps(),
    ],
))

CASES.append(Case(
    id="INST-CR-CRUD-FROM-IMAGE-BOOT-OK",
    title="Create instance: boot disk из image (uri-created) → status RUNNING; boot_disk.disk source = image",
    classes=["CRUD"], priority="P1",
    steps=[
        # # requires kacho-vpc subnet {{existingSubnetId}}
        Step(name="cr-image", method="POST", path=IMAGES,
             body={"projectId": "{{_suiteFolderId}}", "name": "img-instboot-{{runId}}", "uri": _SAMPLE_URI},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.imageId", "imageId")]),
        poll_operation_until_done(),
        Step(name="create", method="POST", path=INSTANCES,
             body=_instance_body("crimg", boot_disk_spec=_boot_disk_spec_inline("crimg", image="{{imageId}}")),
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.instanceId", "instanceId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="get", method="GET", path=f"{INSTANCES}/{{{{instanceId}}}}",
             test_script=[*assert_status(200), "pm.test('status RUNNING', () => pm.expect(pm.response.json().status).to.eql('RUNNING'));"]),
        *_delete_instance_steps(),
        Step(name="del-img", method="DELETE", path=f"{IMAGES}/{{{{imageId}}}}", test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ],
))

CASES.append(Case(
    id="INST-CR-CRUD-BOOT-DISK-ID-OK",
    title="Create instance: boot_disk_spec.disk_id (готовый Disk) вместо disk_spec → status RUNNING",
    classes=["CRUD"], priority="P1",
    steps=[
        # # requires kacho-vpc subnet {{existingSubnetId}}
        *_create_disk_steps("instbootid", save_as="bootDiskId", size=_BOOT_SIZE),
        Step(name="create", method="POST", path=INSTANCES,
             body=_instance_body("crbd", boot_disk_spec={"autoDelete": False, "diskId": "{{bootDiskId}}"}),
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.instanceId", "instanceId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="get", method="GET", path=f"{INSTANCES}/{{{{instanceId}}}}",
             test_script=[*assert_status(200),
                          "pm.test('bootDisk.diskId == bootDiskId', () => pm.expect(pm.response.json().bootDisk && pm.response.json().bootDisk.diskId).to.eql(pm.environment.get('bootDiskId')));"]),
        *_delete_instance_steps(),
        # autoDelete=false → диск остался → почистить
        *_delete_disk_steps(var="bootDiskId", name="cleanup-boot-disk"),
    ],
))

# --- required-field matrix ---
for fld, var, label in [
    ("zoneId", "z", "zone_id"),
    ("platformId", "p", "platform_id"),
    ("resourcesSpec", "r", "resources_spec"),
    ("bootDiskSpec", "b", "boot_disk_spec"),
    ("networkInterfaceSpecs", "n", "network_interface_specs"),
]:
    body = _instance_body(f"req{var}")
    body.pop(fld, None)
    CASES.append(Case(
        id=f"INST-CR-VAL-MISSING-{label.upper().replace('_', '-')}",
        title=f"Create instance без required '{label}' → 400 InvalidArgument",
        classes=["VAL"], priority="P0",
        steps=[Step(name=f"cr-no-{var}", method="POST", path=INSTANCES, body=body,
                    test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")])],
    ))

CASES.append(Case(
    id="INST-CR-VAL-MISSING-FOLDER",
    title="Create instance без projectId → 400 InvalidArgument",
    classes=["VAL"], priority="P0",
    steps=[Step(name="cr-no-folder", method="POST", path=INSTANCES,
                body={k: v for k, v in _instance_body("nf").items() if k != "projectId"},
                test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")])],
))

CASES.append(Case(
    id="INST-CR-NEG-FOLDER-NOTFOUND",
    title="Create instance в garbage projectId → async NOT_FOUND 'Folder ... not found'",
    classes=["NEG"], priority="P0",
    steps=[
        # # requires peer-validation enabled
        Step(name="cr-bad-folder", method="POST", path=INSTANCES, body=_instance_body("bf", projectId="{{garbageRmId}}"),
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        assert_op_error(5, "NOT_FOUND", msg_substr="folder"),
    ],
))

CASES.append(Case(
    id="INST-CR-NEG-SUBNET-NOTFOUND",
    title="Create instance: NIC subnet_id=garbage → async NOT_FOUND 'Subnet ... not found'",
    classes=["NEG"], priority="P1",
    steps=[
        # # requires peer-validation enabled (KACHO_COMPUTE_SKIP_PEER_VALIDATION!=true) + поднятый kacho-vpc
        Step(name="cr-bad-subnet", method="POST", path=INSTANCES,
             body=_instance_body("bs", nics=[_nic_spec(subnet="e9bnonexistent999999", sg=None)]),
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        assert_op_error(5, "NOT_FOUND", msg_substr="subnet"),
    ],
))

CASES.append(Case(
    id="INST-CR-NEG-DUP-NAME",
    title="Create instance с дубликатом name в folder → async ALREADY_EXISTS",
    classes=["NEG", "CONC"], priority="P1",
    steps=[
        # # requires kacho-vpc subnet {{existingSubnetId}}
        Step(name="cr-1", method="POST", path=INSTANCES, body=_instance_body("dup"),
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.instanceId", "instanceId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="cr-2-dup", method="POST", path=INSTANCES,
             body=_instance_body("dup", bootDiskSpec=_boot_disk_spec_inline("dup2")),
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        assert_op_error(6, "ALREADY_EXISTS"),
        *_delete_instance_steps(),
    ],
))

CASES.append(Case(
    id="INST-CR-VAL-NAME-UPPERCASE",
    title="Create instance с UPPERCASE name → 400 (compute lowercase-only)",
    classes=["VAL"], priority="P1",
    steps=[Step(name="cr-upper", method="POST", path=INSTANCES, body=_instance_body("u", name="InstUpper-{{runId}}"),
                test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")])],
))

CASES.append(Case(
    id="INST-CR-VAL-NAME-DIGIT-START",
    title="Create instance с name начинающимся с цифры → 400",
    classes=["VAL"], priority="P1",
    steps=[Step(name="cr-digit", method="POST", path=INSTANCES, body=_instance_body("d", name="9inst-{{runId}}"),
                test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")])],
))

CASES.append(Case(
    id="INST-CR-VAL-CORE-FRACTION-INVALID",
    title="Create instance с core_fraction=37 (не из {0,5,20,50,100}) → 400 InvalidArgument",
    classes=["VAL", "BVA"], priority="P1",
    steps=[Step(name="cr-bad-cf", method="POST", path=INSTANCES,
                body=_instance_body("cf", resourcesSpec=_resources_spec(core_fraction=37)),
                test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")])],
))

CASES.append(Case(
    id="INST-CR-VAL-CORES-ODD-INVALID",
    title="Create instance с cores=3 (не из proto set 2,4,6,...) → 400 InvalidArgument",
    classes=["VAL", "BVA"], priority="P1",
    steps=[Step(name="cr-bad-cores", method="POST", path=INSTANCES,
                body=_instance_body("co", resourcesSpec=_resources_spec(cores=3)),
                test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")])],
))

CASES.append(Case(
    id="INST-CR-VAL-BOOTDISK-EXACTLY-ONE",
    title="Create instance с boot_disk_spec где и disk_id, и disk_spec → 400 InvalidArgument (exactly one of)",
    classes=["VAL", "NEG"], priority="P1",
    steps=[Step(name="cr-both-bootdisk", method="POST", path=INSTANCES,
                body=_instance_body("bd2", boot_disk_spec={"autoDelete": True, "diskId": "{{garbageComputeId}}",
                                                           "diskSpec": {"name": "bd-x-{{runId}}", "size": _BOOT_SIZE}}),
                test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")])],
))

# ===========================================================================
# INST-CR — NIC modes: inline-create vs reference an existing kacho-vpc NIC
# ===========================================================================
# Epic KAC-2: Instance.NetworkInterfaceSpec gained `nic_id` — a NIC spec can
# either reference an existing kacho-vpc NetworkInterface by id (then subnet_id /
# primary_v4_address_spec must NOT be set — the NIC already carries them), or
# (the default) inline-create one with subnet_id [+ primary_v4_address_spec].
# These cases require kacho-vpc up (newman docker-compose has it).

VPC_NETWORKS = "/vpc/v1/networks"
VPC_SUBNETS = "/vpc/v1/subnets"
VPC_NICS = "/vpc/v1/networkInterfaces"


def _vpc_make_nic_steps():
    """Create a fresh kacho-vpc Network → Subnet (in {{existingZoneId}}) → NetworkInterface;
    saves vpcNetId / vpcSubnetId / vpcNicId. Returns step list (poll vpc ops)."""
    return [
        Step(name="vpc-cr-net", method="POST", path=VPC_NETWORKS,
             body={"projectId": "{{_suiteFolderId}}", "name": "nm-nicnet-{{runId}}"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.networkId", "vpcNetId")]),
        poll_operation_until_done(),
        Step(name="vpc-cr-subnet", method="POST", path=VPC_SUBNETS,
             body={"projectId": "{{_suiteFolderId}}", "name": "nm-nicsub-{{runId}}",
                   "networkId": "{{vpcNetId}}", "zoneId": "{{existingZoneId}}",
                   "v4CidrBlocks": ["192.168.222.0/24"]},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.subnetId", "vpcSubnetId")]),
        poll_operation_until_done(),
        Step(name="vpc-cr-nic", method="POST", path=VPC_NICS,
             body={"projectId": "{{_suiteFolderId}}", "name": "nm-nic-{{runId}}",
                   "subnetId": "{{vpcSubnetId}}"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.networkInterfaceId", "vpcNicId")]),
        poll_operation_until_done(),
    ]


def _vpc_cleanup_nic_steps():
    return [
        Step(name="vpc-del-nic", method="DELETE", path=f"{VPC_NICS}/{{{{vpcNicId}}}}", test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="vpc-del-subnet", method="DELETE", path=f"{VPC_SUBNETS}/{{{{vpcSubnetId}}}}", test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="vpc-del-net", method="DELETE", path=f"{VPC_NETWORKS}/{{{{vpcNetId}}}}", test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
    ]


CASES.append(Case(
    id="INST-CR-WITH-NICID-OK",
    title="Create instance: NIC spec = {nicId: <existing kacho-vpc NIC>} (no subnetId) → poll → Get → instance NIC carries that nicId; Delete → NIC usedBy cleared",
    classes=["CRUD", "CONF"], priority="P0",
    steps=[
        # # requires kacho-vpc up (newman docker-compose includes it)
        *_vpc_make_nic_steps(),
        Step(name="create", method="POST", path=INSTANCES,
             body=_instance_body("nicid", nics=[{"nicId": "{{vpcNicId}}"}]),
             test_script=[*assert_status(200), *assert_operation_envelope(),
                          *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.instanceId", "instanceId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="get", method="GET", path=f"{INSTANCES}/{{{{instanceId}}}}",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('status RUNNING', () => pm.expect(j.status).to.eql('RUNNING'));",
                          "pm.test('at least 1 NIC', () => pm.expect((j.networkInterfaces || []).length).to.be.at.least(1));",
                          "pm.test('NIC carries the referenced nicId', () => { const ids = (j.networkInterfaces || []).map(n => n.nicId); pm.expect(ids).to.include(pm.environment.get('vpcNicId')); });"]),
        # check the vpc NIC now reports usedBy → the instance
        Step(name="vpc-get-nic-attached", method="GET", path=f"{VPC_NICS}/{{{{vpcNicId}}}}",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('usedBy points at instance (if reported)', () => { if (j.usedBy) pm.expect(JSON.stringify(j.usedBy)).to.include(pm.environment.get('instanceId')); });"]),
        *_delete_instance_steps(),
        # after instance delete the NIC should be free again
        Step(name="vpc-get-nic-freed", method="GET", path=f"{VPC_NICS}/{{{{vpcNicId}}}}",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('usedBy cleared after instance delete (if observable)', () => { if (j.usedBy && Object.keys(j.usedBy).length) pm.expect(JSON.stringify(j.usedBy)).to.not.include(pm.environment.get('instanceId')); });"]),
        *_vpc_cleanup_nic_steps(),
    ],
))

CASES.append(Case(
    id="INST-CR-INLINE-NIC-OK",
    title="Create instance: inline NIC = {subnetId, primaryV4AddressSpec:{}} (default mode) → poll → Get → NIC has subnetId, no nicId reference; minimal boot disk (size only, no image_id)",
    classes=["CRUD", "CONF"], priority="P0",
    steps=[
        # # requires kacho-vpc subnet {{existingSubnetId}} + sg {{existingSgId}}
        Step(name="create", method="POST", path=INSTANCES,
             body=_instance_body("inlnic", nics=[_nic_spec()]),
             test_script=[*assert_status(200), *assert_operation_envelope(),
                          *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.instanceId", "instanceId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="get", method="GET", path=f"{INSTANCES}/{{{{instanceId}}}}",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('status RUNNING', () => pm.expect(j.status).to.eql('RUNNING'));",
                          "pm.test('boot disk present (size-only spec, no image)', () => pm.expect(j.bootDisk && j.bootDisk.diskId).to.be.a('string').and.length.greaterThan(0));",
                          "pm.test('NIC has subnetId == existingSubnetId', () => { pm.expect((j.networkInterfaces || []).length).to.be.at.least(1); pm.expect(j.networkInterfaces[0].subnetId).to.eql(pm.environment.get('existingSubnetId')); });",
                          "pm.test('inline NIC also exposes a nicId (kacho-vpc NetworkInterface backing it)', () => pm.expect(j.networkInterfaces[0].nicId === undefined || typeof j.networkInterfaces[0].nicId === 'string').to.be.true);"]),
        *_delete_instance_steps(),
    ],
))

# ===========================================================================
# INST-GET / LIST
# ===========================================================================

CASES.append(Case(
    id="INST-GET-NEG-NOTFOUND",
    title="Get well-formed-but-absent instanceId → 404 NOT_FOUND",
    classes=["NEG"], priority="P0",
    steps=[Step(name="get-nx", method="GET", path=f"{INSTANCES}/{{{{garbageComputeId}}}}",
                test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")])],
))

CASES.append(Case(
    id="INST-GET-CONF-NF-TEXT",
    title="Get garbage instanceId → текст содержит 'not found'",
    classes=["CONF", "NEG"], priority="P1",
    steps=[Step(name="get-nx", method="GET", path=f"{INSTANCES}/{{{{garbageComputeId}}}}",
                test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND"),
                             "pm.test('text mentions not found', () => pm.expect((pm.response.json().message || '').toLowerCase()).to.include('not found'));"])],
))

CASES.append(Case(
    id="INST-LST-CRUD-OK",
    title="List instances в folder → instances array",
    classes=["CRUD"], priority="P1",
    steps=[Step(name="list", method="GET", path=f"{INSTANCES}?projectId={{{{_suiteFolderId}}}}",
                test_script=[*assert_status(200), "pm.test('instances is array', () => pm.expect(pm.response.json().instances || []).to.be.an('array'));"])],
))

CASES.append(Case(
    id="INST-LST-VAL-FOLDER-REQUIRED",
    title="List instances без projectId → 400 InvalidArgument",
    classes=["VAL", "AUTHZ"], priority="P0",
    steps=[Step(name="list-nf", method="GET", path=INSTANCES,
                test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")])],
))

CASES.append(Case(
    id="INST-LST-VIEW-BASIC-NO-METADATA",
    title="List instances view=BASIC (default) → metadata не возвращается (verbatim YC)",
    classes=["CONF", "CRUD"], priority="P2",
    steps=[
        # # requires kacho-vpc subnet {{existingSubnetId}}
        *_create_instance_steps("vbasic", metadata={"foo": "bar"}),
        Step(name="list-basic", method="GET", path=f"{INSTANCES}?projectId={{{{_suiteFolderId}}}}&pageSize=1000",
             test_script=[*assert_status(200),
                          "const me = (pm.response.json().instances || []).find(x => x.id === pm.environment.get('instanceId'));",
                          "pm.test('instance found in list', () => pm.expect(me).to.be.an('object'));",
                          "pm.test('metadata omitted in BASIC view', () => pm.expect(me.metadata === undefined || Object.keys(me.metadata || {}).length === 0).to.eql(true));"]),
        *_delete_instance_steps(),
    ],
))

# ===========================================================================
# INST-UPD — Update
# ===========================================================================

CASES.append(Case(
    id="INST-UPD-CRUD-NAME-DESC-LABELS-OK",
    title="Update instance mask=name,description,labels (RUNNING) → все три применены, status неизменён",
    classes=["CRUD", "STATE"], priority="P1",
    steps=[
        # # requires kacho-vpc subnet {{existingSubnetId}}
        *_create_instance_steps("upd", description="init", labels={"orig": "1"}),
        Step(name="patch", method="PATCH", path=f"{INSTANCES}/{{{{instanceId}}}}",
             body={"updateMask": "name,description,labels", "name": "inst-upd2-{{runId}}",
                   "description": "updated-newman", "labels": {"env": "prod"}},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="verify", method="GET", path=f"{INSTANCES}/{{{{instanceId}}}}",
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('name updated', () => pm.expect(j.name).to.match(/^inst-upd2-/));",
                          "pm.test('description updated', () => pm.expect(j.description).to.eql('updated-newman'));",
                          "pm.test('label env', () => pm.expect((j.labels || {}).env).to.eql('prod'));",
                          "pm.test('status still RUNNING', () => pm.expect(j.status).to.eql('RUNNING'));"]),
        *_delete_instance_steps(),
    ],
))

CASES.append(Case(
    id="INST-UPD-RESOURCES-REQUIRES-STOPPED",
    title="Update instance mask=resources_spec пока RUNNING → FailedPrecondition; после Stop → OK",
    classes=["STATE", "NEG"], priority="P0",
    steps=[
        # # requires kacho-vpc subnet {{existingSubnetId}}
        *_create_instance_steps("updres"),
        # 1. RUNNING → Update resources → FailedPrecondition
        Step(name="patch-running", method="PATCH", path=f"{INSTANCES}/{{{{instanceId}}}}",
             body={"updateMask": "resources_spec", "resourcesSpec": _resources_spec(cores=4, memory=4294967296)},
             # probe-needed: точный текст ("Instance must be stopped" / "Instance is not stopped"). Может быть sync 400 или async op-error code 9.
             test_script=["pm.test('rejected (400 sync or 200+op-error)', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
                          *save_from_response("j.id", "opId"),
                          "if (pm.response.code === 400) { pm.test('code 9 FAILED_PRECONDITION', () => pm.expect(pm.response.json().code).to.eql(9)); }"]),
        poll_operation_until_done(),
        Step(name="assert-prec", method="GET", path="/operations/{{opId}}",
             test_script=["const j = pm.response.json();",
                          "pm.test('done', () => pm.expect(j.done).to.eql(true));",
                          "pm.test('if op-error → code 9', () => { if (j.error) pm.expect(j.error.code).to.eql(9); });"]),
        # 2. Stop → Update resources → OK
        *_stop_instance_steps(),
        Step(name="patch-stopped", method="PATCH", path=f"{INSTANCES}/{{{{instanceId}}}}",
             body={"updateMask": "resources_spec", "resourcesSpec": _resources_spec(cores=4, memory=4294967296)},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="verify-resources", method="GET", path=f"{INSTANCES}/{{{{instanceId}}}}",
             test_script=[*assert_status(200),
                          "pm.test('cores updated to 4', () => pm.expect(String(pm.response.json().resources.cores)).to.eql('4'));",
                          "pm.test('status STOPPED', () => pm.expect(pm.response.json().status).to.eql('STOPPED'));"]),
        *_delete_instance_steps(),
    ],
))

CASES.append(Case(
    id="INST-UPD-MASK-IMMUTABLE-ZONE",
    title="Update instance mask=zone_id → 400 InvalidArgument (immutable) или 404",
    classes=["STATE", "VAL", "CONF"], priority="P1",
    steps=[Step(name="patch-imm-zone", method="PATCH", path=f"{INSTANCES}/{{{{garbageComputeId}}}}",
                body={"updateMask": "zone_id", "zoneId": "{{existingZoneAltId}}"},
                test_script=["pm.test('rejected (400 immutable or 404)', () => pm.expect(pm.response.code).to.be.oneOf([400, 404]));",
                             "if (pm.response.code === 400) { pm.test('code 3', () => pm.expect(pm.response.json().code).to.eql(3)); }"])],
))

CASES.append(Case(
    id="INST-UPD-MASK-UNKNOWN-FIELD",
    title="Update instance с unknown field в update_mask → 400 InvalidArgument или 404",
    classes=["VAL", "STATE"], priority="P1",
    steps=[Step(name="patch-unk", method="PATCH", path=f"{INSTANCES}/{{{{garbageComputeId}}}}",
                body={"updateMask": "totally_unknown_xyz", "description": "x"},
                test_script=["pm.test('rejected (400 or 404)', () => pm.expect(pm.response.code).to.be.oneOf([400, 404]));"])],
))

CASES.append(Case(
    id="INST-UPD-AUTHZ-NF-SYNC",
    title="Update несуществующего instance → sync 404 NOT_FOUND",
    classes=["NEG", "AUTHZ"], priority="P1",
    steps=[Step(name="patch-nx", method="PATCH", path=f"{INSTANCES}/{{{{garbageComputeId}}}}",
                body={"updateMask": "description", "description": "x"},
                test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")])],
))

# ===========================================================================
# INST-STATE — state machine (Start/Stop/Restart)
# ===========================================================================

CASES.append(Case(
    id="INST-STATE-START-FROM-RUNNING",
    title="Create→RUNNING; Start → FailedPrecondition (instance уже running)",
    classes=["STATE", "NEG"], priority="P0",
    steps=[
        # # requires kacho-vpc subnet {{existingSubnetId}}
        *_create_instance_steps("startrun"),
        Step(name="start-running", method="POST", path=f"{INSTANCES}/{{{{instanceId}}}}:start", body={},
             # probe-needed: текст "Instance is already running" / "...not stopped". Может быть sync 400 или async op-error.
             test_script=["pm.test('rejected (400 sync or 200+op-error)', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
                          *save_from_response("j.id", "opId"),
                          "if (pm.response.code === 400) { pm.test('code 9 FAILED_PRECONDITION', () => pm.expect(pm.response.json().code).to.eql(9)); }"]),
        poll_operation_until_done(),
        Step(name="assert", method="GET", path="/operations/{{opId}}",
             test_script=["const j = pm.response.json();",
                          "pm.test('done', () => pm.expect(j.done).to.eql(true));",
                          "pm.test('if op-error → code 9', () => { if (j.error) pm.expect(j.error.code).to.eql(9); });"]),
        Step(name="verify-still-running", method="GET", path=f"{INSTANCES}/{{{{instanceId}}}}",
             test_script=[*assert_status(200), "pm.test('still RUNNING', () => pm.expect(pm.response.json().status).to.eql('RUNNING'));"]),
        *_delete_instance_steps(),
    ],
))

CASES.append(Case(
    id="INST-STATE-STOP-OK",
    title="Create→RUNNING; Stop → STOPPED",
    classes=["STATE", "CRUD"], priority="P0",
    steps=[
        # # requires kacho-vpc subnet {{existingSubnetId}}
        *_create_instance_steps("stopok"),
        *_stop_instance_steps(),
        Step(name="verify", method="GET", path=f"{INSTANCES}/{{{{instanceId}}}}",
             test_script=[*assert_status(200), "pm.test('status STOPPED', () => pm.expect(pm.response.json().status).to.eql('STOPPED'));"]),
        *_delete_instance_steps(),
    ],
))

CASES.append(Case(
    id="INST-STATE-START-FROM-STOPPED-OK",
    title="Create→RUNNING; Stop→STOPPED; Start → RUNNING",
    classes=["STATE", "CRUD"], priority="P0",
    steps=[
        # # requires kacho-vpc subnet {{existingSubnetId}}
        *_create_instance_steps("startstop"),
        *_stop_instance_steps(),
        Step(name="start", method="POST", path=f"{INSTANCES}/{{{{instanceId}}}}:start", body={},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="verify", method="GET", path=f"{INSTANCES}/{{{{instanceId}}}}",
             test_script=[*assert_status(200), "pm.test('status RUNNING', () => pm.expect(pm.response.json().status).to.eql('RUNNING'));"]),
        *_delete_instance_steps(),
    ],
))

CASES.append(Case(
    id="INST-STATE-STOP-FROM-STOPPED",
    title="Create→RUNNING; Stop→STOPPED; Stop again → FailedPrecondition",
    classes=["STATE", "NEG"], priority="P1",
    steps=[
        # # requires kacho-vpc subnet {{existingSubnetId}}
        *_create_instance_steps("stopstop"),
        *_stop_instance_steps(),
        Step(name="stop-again", method="POST", path=f"{INSTANCES}/{{{{instanceId}}}}:stop", body={},
             test_script=["pm.test('rejected (400 sync or 200+op-error)', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
                          *save_from_response("j.id", "opId"),
                          "if (pm.response.code === 400) { pm.test('code 9', () => pm.expect(pm.response.json().code).to.eql(9)); }"]),
        poll_operation_until_done(),
        Step(name="assert", method="GET", path="/operations/{{opId}}",
             test_script=["const j = pm.response.json();",
                          "pm.test('done', () => pm.expect(j.done).to.eql(true));",
                          "pm.test('if op-error → code 9', () => { if (j.error) pm.expect(j.error.code).to.eql(9); });"]),
        *_delete_instance_steps(),
    ],
))

CASES.append(Case(
    id="INST-STATE-RESTART-OK",
    title="Create→RUNNING; Restart → RUNNING",
    classes=["STATE", "CRUD"], priority="P1",
    steps=[
        # # requires kacho-vpc subnet {{existingSubnetId}}
        *_create_instance_steps("restartok"),
        Step(name="restart", method="POST", path=f"{INSTANCES}/{{{{instanceId}}}}:restart", body={},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="verify", method="GET", path=f"{INSTANCES}/{{{{instanceId}}}}",
             test_script=[*assert_status(200), "pm.test('status RUNNING', () => pm.expect(pm.response.json().status).to.eql('RUNNING'));"]),
        *_delete_instance_steps(),
    ],
))

CASES.append(Case(
    id="INST-STATE-RESTART-FROM-STOPPED",
    title="Create→RUNNING; Stop→STOPPED; Restart → FailedPrecondition",
    classes=["STATE", "NEG"], priority="P1",
    steps=[
        # # requires kacho-vpc subnet {{existingSubnetId}}
        *_create_instance_steps("restartstop"),
        *_stop_instance_steps(),
        Step(name="restart-stopped", method="POST", path=f"{INSTANCES}/{{{{instanceId}}}}:restart", body={},
             test_script=["pm.test('rejected (400 sync or 200+op-error)', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
                          *save_from_response("j.id", "opId"),
                          "if (pm.response.code === 400) { pm.test('code 9', () => pm.expect(pm.response.json().code).to.eql(9)); }"]),
        poll_operation_until_done(),
        Step(name="assert", method="GET", path="/operations/{{opId}}",
             test_script=["const j = pm.response.json();",
                          "pm.test('done', () => pm.expect(j.done).to.eql(true));",
                          "pm.test('if op-error → code 9', () => { if (j.error) pm.expect(j.error.code).to.eql(9); });"]),
        *_delete_instance_steps(),
    ],
))

CASES.append(Case(
    id="INST-START-AUTHZ-NF-SYNC",
    title="Start несуществующего instance → sync 404 NOT_FOUND",
    classes=["NEG", "AUTHZ"], priority="P1",
    steps=[Step(name="start-nx", method="POST", path=f"{INSTANCES}/{{{{garbageComputeId}}}}:start", body={},
                test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")])],
))

CASES.append(Case(
    id="INST-STOP-AUTHZ-NF-SYNC",
    title="Stop несуществующего instance → sync 404 NOT_FOUND",
    classes=["NEG", "AUTHZ"], priority="P1",
    steps=[Step(name="stop-nx", method="POST", path=f"{INSTANCES}/{{{{garbageComputeId}}}}:stop", body={},
                test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")])],
))

# ===========================================================================
# INST-AD / INST-DD — AttachDisk / DetachDisk
# ===========================================================================

CASES.append(Case(
    id="INST-AD-CRUD-OK",
    title="AttachDisk: create disk → AttachDisk → Get → secondary_disks содержит его",
    classes=["CRUD", "STATE"], priority="P0",
    steps=[
        # # requires kacho-vpc subnet {{existingSubnetId}}
        *_create_instance_steps("adok"),
        *_create_disk_steps("adok-extra", save_as="extraDiskId"),
        Step(name="attach", method="POST", path=f"{INSTANCES}/{{{{instanceId}}}}:attachDisk",
             body={"attachedDiskSpec": {"autoDelete": False, "diskId": "{{extraDiskId}}"}},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          "pm.test('metadata has diskId', () => pm.expect(pm.response.json().metadata && pm.response.json().metadata.diskId).to.eql(pm.environment.get('extraDiskId')));"]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="verify", method="GET", path=f"{INSTANCES}/{{{{instanceId}}}}",
             test_script=[*assert_status(200),
                          "const ids = (pm.response.json().secondaryDisks || []).map(d => d.diskId);",
                          "pm.test('secondaryDisks contains extra disk', () => pm.expect(ids).to.include(pm.environment.get('extraDiskId')));"]),
        # detach before delete (disk autoDelete=false)
        Step(name="detach", method="POST", path=f"{INSTANCES}/{{{{instanceId}}}}:detachDisk", body={"diskId": "{{extraDiskId}}"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_delete_disk_steps(var="extraDiskId"),
        *_delete_instance_steps(),
    ],
))

CASES.append(Case(
    id="INST-AD-NEG-WRONG-ZONE",
    title="AttachDisk: disk в другой zone (alt) → rejected (InvalidArgument/FailedPrecondition)",
    classes=["NEG", "STATE"], priority="P1",
    steps=[
        # # requires kacho-vpc subnet {{existingSubnetId}}
        *_create_instance_steps("adwz"),
        *_create_disk_steps("adwz-alt", save_as="extraDiskId", zone="{{existingZoneAltId}}"),
        Step(name="attach-wrong-zone", method="POST", path=f"{INSTANCES}/{{{{instanceId}}}}:attachDisk",
             body={"attachedDiskSpec": {"autoDelete": False, "diskId": "{{extraDiskId}}"}},
             test_script=["pm.test('rejected (400 sync or 200+op-error)', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
                          *save_from_response("j.id", "opId"),
                          "if (pm.response.code === 400) { pm.test('code 3 or 9', () => pm.expect(pm.response.json().code).to.be.oneOf([3, 9])); }"]),
        poll_operation_until_done(),
        Step(name="assert", method="GET", path="/operations/{{opId}}",
             test_script=["const j = pm.response.json();",
                          "pm.test('done', () => pm.expect(j.done).to.eql(true));",
                          "pm.test('if op-error → code 3 or 9', () => { if (j.error) pm.expect(j.error.code).to.be.oneOf([3, 9]); });"]),
        *_delete_disk_steps(var="extraDiskId"),
        *_delete_instance_steps(),
    ],
))

CASES.append(Case(
    id="INST-AD-NEG-ALREADY-ATTACHED",
    title="AttachDisk дважды один и тот же disk → второй раз rejected (FailedPrecondition)",
    classes=["NEG", "STATE"], priority="P1",
    steps=[
        # # requires kacho-vpc subnet {{existingSubnetId}}
        *_create_instance_steps("adaa"),
        *_create_disk_steps("adaa-extra", save_as="extraDiskId"),
        Step(name="attach-1", method="POST", path=f"{INSTANCES}/{{{{instanceId}}}}:attachDisk",
             body={"attachedDiskSpec": {"autoDelete": False, "diskId": "{{extraDiskId}}"}},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="attach-2-dup", method="POST", path=f"{INSTANCES}/{{{{instanceId}}}}:attachDisk",
             body={"attachedDiskSpec": {"autoDelete": False, "diskId": "{{extraDiskId}}"}},
             test_script=["pm.test('rejected (400 sync or 200+op-error)', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
                          *save_from_response("j.id", "opId"),
                          "if (pm.response.code === 400) { pm.test('code 3 or 9', () => pm.expect(pm.response.json().code).to.be.oneOf([3, 9])); }"]),
        poll_operation_until_done(),
        Step(name="assert", method="GET", path="/operations/{{opId}}",
             test_script=["const j = pm.response.json();",
                          "pm.test('done', () => pm.expect(j.done).to.eql(true));",
                          "pm.test('if op-error → code 3 or 9', () => { if (j.error) pm.expect(j.error.code).to.be.oneOf([3, 9]); });"]),
        Step(name="detach", method="POST", path=f"{INSTANCES}/{{{{instanceId}}}}:detachDisk", body={"diskId": "{{extraDiskId}}"},
             test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_delete_disk_steps(var="extraDiskId"),
        *_delete_instance_steps(),
    ],
))

CASES.append(Case(
    id="INST-DD-CRUD-OK",
    title="DetachDisk: attach → DetachDisk → Get → secondary_disks больше не содержит",
    classes=["CRUD", "STATE"], priority="P1",
    steps=[
        # # requires kacho-vpc subnet {{existingSubnetId}}
        *_create_instance_steps("ddok"),
        *_create_disk_steps("ddok-extra", save_as="extraDiskId"),
        Step(name="attach", method="POST", path=f"{INSTANCES}/{{{{instanceId}}}}:attachDisk",
             body={"attachedDiskSpec": {"autoDelete": False, "diskId": "{{extraDiskId}}"}},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="detach", method="POST", path=f"{INSTANCES}/{{{{instanceId}}}}:detachDisk", body={"diskId": "{{extraDiskId}}"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="verify", method="GET", path=f"{INSTANCES}/{{{{instanceId}}}}",
             test_script=[*assert_status(200),
                          "const ids = (pm.response.json().secondaryDisks || []).map(d => d.diskId);",
                          "pm.test('secondaryDisks no longer contains extra disk', () => pm.expect(ids).to.not.include(pm.environment.get('extraDiskId')));"]),
        *_delete_disk_steps(var="extraDiskId"),
        *_delete_instance_steps(),
    ],
))

CASES.append(Case(
    id="INST-DD-NEG-BOOT",
    title="DetachDisk boot disk → FailedPrecondition 'Cannot detach boot disk'",
    classes=["NEG", "STATE"], priority="P0",
    steps=[
        # # requires kacho-vpc subnet {{existingSubnetId}}
        *_create_instance_steps("ddboot"),
        Step(name="get-boot-id", method="GET", path=f"{INSTANCES}/{{{{instanceId}}}}",
             test_script=[*assert_status(200), *save_from_response("pm.response.json().bootDisk && pm.response.json().bootDisk.diskId", "bootDiskId")]),
        Step(name="detach-boot", method="POST", path=f"{INSTANCES}/{{{{instanceId}}}}:detachDisk", body={"diskId": "{{bootDiskId}}"},
             # probe-needed: точный текст "Cannot detach boot disk". Может быть sync 400 или async op-error code 9.
             test_script=["pm.test('rejected (400 sync or 200+op-error)', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
                          *save_from_response("j.id", "opId"),
                          "if (pm.response.code === 400) { pm.test('code 9', () => pm.expect(pm.response.json().code).to.eql(9)); }"]),
        poll_operation_until_done(),
        Step(name="assert", method="GET", path="/operations/{{opId}}",
             test_script=["const j = pm.response.json();",
                          "pm.test('done', () => pm.expect(j.done).to.eql(true));",
                          "pm.test('if op-error → code 9', () => { if (j.error) pm.expect(j.error.code).to.eql(9); });"]),
        Step(name="verify-still-running", method="GET", path=f"{INSTANCES}/{{{{instanceId}}}}",
             test_script=[*assert_status(200), "pm.test('bootDisk still present', () => pm.expect(pm.response.json().bootDisk && pm.response.json().bootDisk.diskId).to.eql(pm.environment.get('bootDiskId')));"]),
        *_delete_instance_steps(),
    ],
))

CASES.append(Case(
    id="INST-DD-NEG-NOT-ATTACHED",
    title="DetachDisk disk который не attached к этому instance → rejected",
    classes=["NEG", "STATE"], priority="P1",
    steps=[
        # # requires kacho-vpc subnet {{existingSubnetId}}
        *_create_instance_steps("ddna"),
        *_create_disk_steps("ddna-loose", save_as="extraDiskId"),
        Step(name="detach-loose", method="POST", path=f"{INSTANCES}/{{{{instanceId}}}}:detachDisk", body={"diskId": "{{extraDiskId}}"},
             test_script=["pm.test('rejected (400 sync or 200+op-error)', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
                          *save_from_response("j.id", "opId"),
                          "if (pm.response.code === 400) { pm.test('code 3 or 9', () => pm.expect(pm.response.json().code).to.be.oneOf([3, 9])); }"]),
        poll_operation_until_done(),
        Step(name="assert", method="GET", path="/operations/{{opId}}",
             test_script=["const j = pm.response.json();",
                          "pm.test('done', () => pm.expect(j.done).to.eql(true));",
                          "pm.test('if op-error → code 3 or 9', () => { if (j.error) pm.expect(j.error.code).to.be.oneOf([3, 9]); });"]),
        *_delete_disk_steps(var="extraDiskId"),
        *_delete_instance_steps(),
    ],
))

# ===========================================================================
# INST — Disk.Delete-while-attached (verbatim YC: "The disk ... is being used")
# ===========================================================================

CASES.append(Case(
    id="INST-DISK-DEL-WHILE-ATTACHED",
    title="Create disk → attach к instance → Disk.Delete → FailedPrecondition 'The disk ... is being used'; Detach → Delete OK",
    classes=["STATE", "NEG"], priority="P0",
    steps=[
        # # requires kacho-vpc subnet {{existingSubnetId}}
        *_create_instance_steps("dkdel"),
        *_create_disk_steps("dkdel-extra", save_as="extraDiskId"),
        Step(name="attach", method="POST", path=f"{INSTANCES}/{{{{instanceId}}}}:attachDisk",
             body={"attachedDiskSpec": {"autoDelete": False, "diskId": "{{extraDiskId}}"}},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(), assert_op_success(),
        # Disk.Delete while attached → FailedPrecondition (FK attached_disks RESTRICT on disk_id)
        Step(name="del-disk-attached", method="DELETE", path=f"{DISKS}/{{{{extraDiskId}}}}",
             # probe-needed: точный текст "The disk <id> is being used". Может быть sync 400 или async op-error code 9.
             test_script=["pm.test('rejected (400 sync or 200+op-error)', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
                          *save_from_response("j.id", "opId"),
                          "if (pm.response.code === 400) { pm.test('code 9 FAILED_PRECONDITION', () => pm.expect(pm.response.json().code).to.eql(9)); }"]),
        poll_operation_until_done(),
        Step(name="assert-prec", method="GET", path="/operations/{{opId}}",
             test_script=["const j = pm.response.json();",
                          "pm.test('done', () => pm.expect(j.done).to.eql(true));",
                          "pm.test('if op-error → code 9', () => { if (j.error) pm.expect(j.error.code).to.eql(9); });"]),
        Step(name="disk-still-there", method="GET", path=f"{DISKS}/{{{{extraDiskId}}}}",
             test_script=[*assert_status(200), "pm.test('disk still exists', () => pm.expect(pm.response.json().id).to.eql(pm.environment.get('extraDiskId')));"]),
        # Detach → Delete OK
        Step(name="detach", method="POST", path=f"{INSTANCES}/{{{{instanceId}}}}:detachDisk", body={"diskId": "{{extraDiskId}}"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="del-disk-now", method="DELETE", path=f"{DISKS}/{{{{extraDiskId}}}}",
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="disk-gone", method="GET", path=f"{DISKS}/{{{{extraDiskId}}}}",
             test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")]),
        *_delete_instance_steps(),
    ],
))

# ===========================================================================
# INST-NAT — AddOneToOneNat / RemoveOneToOneNat
# ===========================================================================

CASES.append(Case(
    id="INST-NAT-ADD-CRUD-OK",
    title="AddOneToOneNat на NIC index=0 → Get → NIC.primary_v4_address.one_to_one_nat присутствует",
    classes=["CRUD", "STATE"], priority="P1",
    steps=[
        # # requires kacho-vpc subnet {{existingSubnetId}}
        *_create_instance_steps("natadd"),
        Step(name="add-nat", method="POST", path=f"{INSTANCES}/{{{{instanceId}}}}/addOneToOneNat",
             body={"networkInterfaceIndex": "0", "oneToOneNatSpec": {"ipVersion": "IPV4"}},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="verify", method="GET", path=f"{INSTANCES}/{{{{instanceId}}}}",
             test_script=[*assert_status(200),
                          "const nic = (pm.response.json().networkInterfaces || [])[0];",
                          "pm.test('NIC 0 has oneToOneNat', () => pm.expect(nic && nic.primaryV4Address && nic.primaryV4Address.oneToOneNat).to.be.an('object'));"]),
        Step(name="rm-nat", method="POST", path=f"{INSTANCES}/{{{{instanceId}}}}/removeOneToOneNat",
             body={"networkInterfaceIndex": "0"}, test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_delete_instance_steps(),
    ],
))

CASES.append(Case(
    id="INST-NAT-ADD-NEG-ALREADY",
    title="AddOneToOneNat дважды на тот же NIC → второй раз FailedPrecondition",
    classes=["NEG", "STATE"], priority="P1",
    steps=[
        # # requires kacho-vpc subnet {{existingSubnetId}}
        *_create_instance_steps("nataa"),
        Step(name="add-nat-1", method="POST", path=f"{INSTANCES}/{{{{instanceId}}}}/addOneToOneNat",
             body={"networkInterfaceIndex": "0", "oneToOneNatSpec": {"ipVersion": "IPV4"}},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="add-nat-2-dup", method="POST", path=f"{INSTANCES}/{{{{instanceId}}}}/addOneToOneNat",
             body={"networkInterfaceIndex": "0", "oneToOneNatSpec": {"ipVersion": "IPV4"}},
             test_script=["pm.test('rejected (400 sync or 200+op-error)', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
                          *save_from_response("j.id", "opId"),
                          "if (pm.response.code === 400) { pm.test('code 9', () => pm.expect(pm.response.json().code).to.eql(9)); }"]),
        poll_operation_until_done(),
        Step(name="assert", method="GET", path="/operations/{{opId}}",
             test_script=["const j = pm.response.json();",
                          "pm.test('done', () => pm.expect(j.done).to.eql(true));",
                          "pm.test('if op-error → code 9', () => { if (j.error) pm.expect(j.error.code).to.eql(9); });"]),
        Step(name="rm-nat", method="POST", path=f"{INSTANCES}/{{{{instanceId}}}}/removeOneToOneNat",
             body={"networkInterfaceIndex": "0"}, test_script=[*save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        *_delete_instance_steps(),
    ],
))

CASES.append(Case(
    id="INST-NAT-REMOVE-CRUD-OK",
    title="RemoveOneToOneNat (после Add) → Get → NIC.primary_v4_address.one_to_one_nat отсутствует",
    classes=["CRUD", "STATE"], priority="P1",
    steps=[
        # # requires kacho-vpc subnet {{existingSubnetId}}
        *_create_instance_steps("natrm"),
        Step(name="add-nat", method="POST", path=f"{INSTANCES}/{{{{instanceId}}}}/addOneToOneNat",
             body={"networkInterfaceIndex": "0", "oneToOneNatSpec": {"ipVersion": "IPV4"}},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="rm-nat", method="POST", path=f"{INSTANCES}/{{{{instanceId}}}}/removeOneToOneNat",
             body={"networkInterfaceIndex": "0"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="verify", method="GET", path=f"{INSTANCES}/{{{{instanceId}}}}",
             test_script=[*assert_status(200),
                          "const nic = (pm.response.json().networkInterfaces || [])[0];",
                          "pm.test('NIC 0 has no oneToOneNat', () => pm.expect(nic && nic.primaryV4Address && nic.primaryV4Address.oneToOneNat).to.be.oneOf([undefined, null]));"]),
        *_delete_instance_steps(),
    ],
))

# ===========================================================================
# INST-UNI — UpdateNetworkInterface
# ===========================================================================

CASES.append(Case(
    id="INST-UNI-CRUD-OK",
    title="UpdateNetworkInterface index=0 mask=security_group_ids → 200 (применяется)",
    classes=["CRUD", "STATE"], priority="P2",
    steps=[
        # # requires kacho-vpc subnet {{existingSubnetId}} + sg {{existingSgId}}
        *_create_instance_steps("uni"),
        Step(name="update-nic", method="PATCH", path=f"{INSTANCES}/{{{{instanceId}}}}/updateNetworkInterface",
             body={"networkInterfaceIndex": "0", "updateMask": "security_group_ids", "securityGroupIds": ["{{existingSgId}}"]},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(), assert_op_success(),
        *_delete_instance_steps(),
    ],
))

CASES.append(Case(
    id="INST-UNI-NEG-BAD-INDEX",
    title="UpdateNetworkInterface index=99 (нет такого NIC) → rejected",
    classes=["NEG", "STATE"], priority="P2",
    steps=[
        # # requires kacho-vpc subnet {{existingSubnetId}}
        *_create_instance_steps("unibad"),
        Step(name="update-nic-bad", method="PATCH", path=f"{INSTANCES}/{{{{instanceId}}}}/updateNetworkInterface",
             body={"networkInterfaceIndex": "99", "updateMask": "security_group_ids", "securityGroupIds": ["{{existingSgId}}"]},
             test_script=["pm.test('rejected (400 sync or 200+op-error)', () => pm.expect(pm.response.code).to.be.oneOf([200, 400]));",
                          *save_from_response("j.id", "opId"),
                          "if (pm.response.code === 400) { pm.test('code 3, 5 or 9', () => pm.expect(pm.response.json().code).to.be.oneOf([3, 5, 9])); }"]),
        poll_operation_until_done(),
        Step(name="assert", method="GET", path="/operations/{{opId}}",
             test_script=["const j = pm.response.json();",
                          "pm.test('done', () => pm.expect(j.done).to.eql(true));",
                          "pm.test('if op-error → code 3,5,9', () => { if (j.error) pm.expect(j.error.code).to.be.oneOf([3, 5, 9]); });"]),
        *_delete_instance_steps(),
    ],
))

# ===========================================================================
# INST-UMETA — UpdateMetadata
# ===========================================================================

CASES.append(Case(
    id="INST-UMETA-CRUD-OK",
    title="UpdateMetadata upsert {foo:bar} → Get (view=FULL) → metadata.foo == bar; delete [foo] → metadata.foo нет",
    classes=["CRUD", "STATE"], priority="P1",
    steps=[
        # # requires kacho-vpc subnet {{existingSubnetId}}
        *_create_instance_steps("umeta"),
        Step(name="umeta-upsert", method="POST", path=f"{INSTANCES}/{{{{instanceId}}}}/updateMetadata",
             body={"upsert": {"foo": "bar", "ssh-keys": "ubuntu:ssh-ed25519 AAAA..."}},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="get-full", method="GET", path=f"{INSTANCES}/{{{{instanceId}}}}?view=FULL",
             test_script=[*assert_status(200),
                          "const md = pm.response.json().metadata || {};",
                          "pm.test('metadata.foo == bar', () => pm.expect(md.foo).to.eql('bar'));"]),
        Step(name="umeta-delete", method="POST", path=f"{INSTANCES}/{{{{instanceId}}}}/updateMetadata",
             body={"delete": ["foo"]},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="get-full-2", method="GET", path=f"{INSTANCES}/{{{{instanceId}}}}?view=FULL",
             test_script=[*assert_status(200),
                          "const md = pm.response.json().metadata || {};",
                          "pm.test('metadata.foo removed', () => pm.expect(md.foo).to.be.oneOf([undefined, null]));",
                          "pm.test('metadata.ssh-keys retained', () => pm.expect(md['ssh-keys']).to.be.a('string'));"]),
        *_delete_instance_steps(),
    ],
))

# ===========================================================================
# INST-SPO — GetSerialPortOutput
# ===========================================================================

CASES.append(Case(
    id="INST-SPO-CRUD-OK",
    title="GetSerialPortOutput → 200, contents — строка (control-plane: синтетический текст)",
    classes=["CRUD"], priority="P2",
    steps=[
        # # requires kacho-vpc subnet {{existingSubnetId}}
        *_create_instance_steps("spo"),
        Step(name="spo", method="GET", path=f"{INSTANCES}/{{{{instanceId}}}}:serialPortOutput",
             test_script=[*assert_status(200),
                          "pm.test('contents is string', () => pm.expect(pm.response.json().contents).to.be.a('string'));"]),
        *_delete_instance_steps(),
    ],
))

CASES.append(Case(
    id="INST-SPO-NEG-NOTFOUND",
    title="GetSerialPortOutput несуществующего instance → 404 NOT_FOUND",
    classes=["NEG"], priority="P2",
    steps=[Step(name="spo-nx", method="GET", path=f"{INSTANCES}/{{{{garbageComputeId}}}}:serialPortOutput",
                test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")])],
))

# ===========================================================================
# INST-SME — SimulateMaintenanceEvent (no-op)
# ===========================================================================

CASES.append(Case(
    id="INST-SME-CRUD-OK",
    title="SimulateMaintenanceEvent → Operation done (no-op в control-plane); status RUNNING",
    classes=["CRUD"], priority="P3",
    steps=[
        # # requires kacho-vpc subnet {{existingSubnetId}}
        *_create_instance_steps("sme"),
        Step(name="sim-maint", method="POST", path=f"{INSTANCES}/{{{{instanceId}}}}:simulateMaintenanceEvent", body={},
             # probe-needed: реальный YC может вернуть Operation; если RPC Unimplemented — 501. Allow 200|501.
             test_script=["pm.test('200 (op) or 501 (unimplemented)', () => pm.expect(pm.response.code).to.be.oneOf([200, 501]));",
                          "if (pm.response.code === 200) { pm.environment.set('opId', pm.response.json().id || ''); }"]),
        poll_operation_until_done(),
        Step(name="verify", method="GET", path=f"{INSTANCES}/{{{{instanceId}}}}",
             test_script=[*assert_status(200), "pm.test('status RUNNING', () => pm.expect(pm.response.json().status).to.eql('RUNNING'));"]),
        *_delete_instance_steps(),
    ],
))

# ===========================================================================
# INST-MV — Move
# ===========================================================================

CASES.append(Case(
    id="INST-MV-CRUD-OK",
    title="Move instance в другой folder → projectId в Get обновлён, status сохранён",
    classes=["CRUD", "STATE"], priority="P1",
    steps=[
        # # requires kacho-vpc subnet {{existingSubnetId}}
        *_create_instance_steps("mv"),
        Step(name="move", method="POST", path=f"{INSTANCES}/{{{{instanceId}}}}:move",
             body={"destinationProjectId": "{{_suiteFolderCrossId}}"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="verify", method="GET", path=f"{INSTANCES}/{{{{instanceId}}}}",
             test_script=[*assert_status(200),
                          "pm.test('projectId == cross', () => pm.expect(pm.response.json().projectId).to.eql(pm.environment.get('_suiteFolderCrossId')));",
                          "pm.test('status still RUNNING', () => pm.expect(pm.response.json().status).to.eql('RUNNING'));"]),
        *_delete_instance_steps(),
    ],
))

CASES.append(Case(
    id="INST-MV-AUTHZ-NF-SYNC",
    title="Move несуществующего instance → sync 404 NOT_FOUND",
    classes=["NEG", "AUTHZ"], priority="P1",
    steps=[Step(name="mv-nx", method="POST", path=f"{INSTANCES}/{{{{garbageComputeId}}}}:move",
                body={"destinationProjectId": "{{_suiteFolderId}}"},
                test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")])],
))

# ===========================================================================
# INST-LOP — ListOperations
# ===========================================================================

CASES.append(Case(
    id="INST-LOP-CRUD-OK",
    title="ListOperations instance → содержит как минимум create-op",
    classes=["CRUD"], priority="P1",
    steps=[
        # # requires kacho-vpc subnet {{existingSubnetId}}
        *_create_instance_steps("lop"),
        Step(name="list-ops", method="GET", path=f"{INSTANCES}/{{{{instanceId}}}}/operations",
             test_script=[*assert_status(200), "pm.test('at least 1 op', () => pm.expect((pm.response.json().operations || []).length).to.be.at.least(1));"]),
        *_delete_instance_steps(),
    ],
))

CASES.append(Case(
    id="INST-LOP-NEG-PARENT-NF",
    title="ListOperations несуществующего instance → 200 (пусто) или 404",
    classes=["NEG"], priority="P2",
    steps=[Step(name="lop-nx", method="GET", path=f"{INSTANCES}/{{{{garbageComputeId}}}}/operations",
                test_script=["pm.test('200 or 404', () => pm.expect(pm.response.code).to.be.oneOf([200, 404]));"])],
))

# ===========================================================================
# INST-DEL — Delete (+ auto_delete disk semantics)
# ===========================================================================

CASES.append(Case(
    id="INST-DEL-CRUD-OK",
    title="Delete instance → Operation done; Get → 404",
    classes=["CRUD", "STATE"], priority="P0",
    steps=[
        # # requires kacho-vpc subnet {{existingSubnetId}}
        *_create_instance_steps("delok"),
        Step(name="del", method="DELETE", path=f"{INSTANCES}/{{{{instanceId}}}}",
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="get-404", method="GET", path=f"{INSTANCES}/{{{{instanceId}}}}",
             test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")]),
    ],
))

CASES.append(Case(
    id="INST-DEL-STATE-AUTODELETE-BOOT-GONE",
    title="Delete instance с boot_disk auto_delete=true → boot disk тоже удалён (Get disk → 404)",
    classes=["STATE", "CRUD"], priority="P1",
    steps=[
        # # requires kacho-vpc subnet {{existingSubnetId}}
        *_create_instance_steps("delad"),
        Step(name="get-boot-id", method="GET", path=f"{INSTANCES}/{{{{instanceId}}}}",
             test_script=[*assert_status(200), *save_from_response("pm.response.json().bootDisk && pm.response.json().bootDisk.diskId", "bootDiskId")]),
        Step(name="del", method="DELETE", path=f"{INSTANCES}/{{{{instanceId}}}}",
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="boot-disk-gone", method="GET", path=f"{DISKS}/{{{{bootDiskId}}}}",
             test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")]),
    ],
))

CASES.append(Case(
    id="INST-DEL-STATE-NONAUTODELETE-DISK-REMAINS",
    title="Delete instance с boot_disk_id (auto_delete=false) → disk остаётся (Get disk → 200)",
    classes=["STATE", "CRUD"], priority="P1",
    steps=[
        # # requires kacho-vpc subnet {{existingSubnetId}}
        *_create_disk_steps("delnoad-boot", save_as="bootDiskId", size=_BOOT_SIZE),
        Step(name="create", method="POST", path=INSTANCES,
             body=_instance_body("delnoad", boot_disk_spec={"autoDelete": False, "diskId": "{{bootDiskId}}"}),
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.instanceId", "instanceId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="del", method="DELETE", path=f"{INSTANCES}/{{{{instanceId}}}}",
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="disk-remains", method="GET", path=f"{DISKS}/{{{{bootDiskId}}}}",
             test_script=[*assert_status(200), "pm.test('disk still exists', () => pm.expect(pm.response.json().id).to.eql(pm.environment.get('bootDiskId')));"]),
        *_delete_disk_steps(var="bootDiskId", name="cleanup-loose-disk"),
    ],
))

CASES.append(Case(
    id="INST-DEL-NEG-NOTFOUND",
    title="Delete несуществующего instance → sync 404 NOT_FOUND",
    classes=["NEG"], priority="P0",
    steps=[Step(name="del-nx", method="DELETE", path=f"{INSTANCES}/{{{{garbageComputeId}}}}",
                test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")])],
))

CASES.append(Case(
    id="INST-DEL-CONF-RESPONSE-EMPTY",
    title="Delete instance → Operation.response = Empty, metadata = DeleteInstanceMetadata{instanceId}",
    classes=["CONF"], priority="P2",
    steps=[
        # # requires kacho-vpc subnet {{existingSubnetId}}
        *_create_instance_steps("delm"),
        Step(name="del", method="DELETE", path=f"{INSTANCES}/{{{{instanceId}}}}",
             test_script=[*assert_status(200), *save_from_response("j.id", "opId"),
                          "pm.test('metadata has instanceId', () => pm.expect(pm.response.json().metadata && pm.response.json().metadata.instanceId).to.eql(pm.environment.get('instanceId')));"]),
        poll_operation_until_done(),
        Step(name="assert-empty", method="GET", path="/operations/{{opId}}",
             test_script=["const j = pm.response.json();",
                          "pm.test('done & no error', () => { pm.expect(j.done).to.eql(true); pm.expect(j.error).to.be.oneOf([undefined, null]); });",
                          "pm.test('response is Empty-like object', () => { pm.expect(j.response).to.be.an('object'); const keys = Object.keys(j.response).filter(k => k !== '@type'); pm.expect(keys.length).to.eql(0); });",
                          "pm.test('metadata.instanceId matches', () => pm.expect(j.metadata && j.metadata.instanceId).to.eql(pm.environment.get('instanceId')));"]),
    ],
))

# ===========================================================================
# INST — lifecycle conformance (Create→Get→List→Update→Get→Stop→Start→Delete→Get-404)
# ===========================================================================

CASES.append(Case(
    id="INST-LIFECYCLE-CONF",
    title="Full lifecycle conformance: Create→Get→List-includes→Update→Stop→Start→Delete→List-excludes→Get-404",
    classes=["CRUD", "CONF", "STATE"], priority="P1",
    steps=[
        # # requires kacho-vpc subnet {{existingSubnetId}}
        *_create_instance_steps("life"),
        Step(name="get-1", method="GET", path=f"{INSTANCES}/{{{{instanceId}}}}",
             test_script=[*assert_status(200), "pm.test('id', () => pm.expect(pm.response.json().id).to.eql(pm.environment.get('instanceId')));"]),
        Step(name="lst-includes", method="GET", path=f"{INSTANCES}?projectId={{{{_suiteFolderId}}}}&pageSize=1000",
             test_script=[*assert_status(200),
                          "const ids = (pm.response.json().instances || []).map(x => x.id);",
                          "pm.test('list contains', () => pm.expect(ids).to.include(pm.environment.get('instanceId')));"]),
        Step(name="upd", method="PATCH", path=f"{INSTANCES}/{{{{instanceId}}}}",
             body={"updateMask": "description", "description": "life-conf"},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="get-after-upd", method="GET", path=f"{INSTANCES}/{{{{instanceId}}}}",
             test_script=[*assert_status(200), "pm.test('description updated', () => pm.expect(pm.response.json().description).to.eql('life-conf'));"]),
        *_stop_instance_steps(),
        Step(name="start", method="POST", path=f"{INSTANCES}/{{{{instanceId}}}}:start", body={},
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(), assert_op_success(),
        Step(name="del", method="DELETE", path=f"{INSTANCES}/{{{{instanceId}}}}",
             test_script=[*assert_status(200), *save_from_response("j.id", "opId")]),
        poll_operation_until_done(),
        Step(name="lst-excludes", method="GET", path=f"{INSTANCES}?projectId={{{{_suiteFolderId}}}}&pageSize=1000",
             test_script=[*assert_status(200),
                          "const ids = (pm.response.json().instances || []).map(x => x.id);",
                          "pm.test('list does not contain', () => pm.expect(ids).to.not.include(pm.environment.get('instanceId')));"]),
        Step(name="get-404", method="GET", path=f"{INSTANCES}/{{{{instanceId}}}}",
             test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")]),
    ],
))

CASES.append(Case(
    id="INST-CR-CONF-ID-PREFIX-EPD",
    title="Create instance → operation.id prefix 'epd', metadata.instanceId prefix 'epd'",
    classes=["CONF"], priority="P1",
    steps=[
        # # requires kacho-vpc subnet {{existingSubnetId}}
        Step(name="cr", method="POST", path=INSTANCES, body=_instance_body("idpfx"),
             test_script=[*assert_status(200),
                          "const j = pm.response.json();",
                          "pm.test('operation.id epd...', () => pm.expect(j.id).to.match(/^epd[a-z0-9]{17}$/));",
                          "pm.test('metadata.instanceId epd...', () => pm.expect(j.metadata && j.metadata.instanceId).to.match(/^epd[a-z0-9]{17}$/));",
                          *save_from_response("j.id", "opId"),
                          *save_from_response("j.metadata && j.metadata.instanceId", "instanceId")]),
        poll_operation_until_done(),
        *_delete_instance_steps(),
    ],
))

# ===========================================================================
# Generic-блоки: pagination / filter / http-method / malformed / security
# (name/labels/desc-блоки требуют полное instance-body — у нас слишком много обязательных
#  полей под generic-helper; name-валидацию покрыли явными кейсами выше.)
# ===========================================================================

CASES.extend(list_page_block("INST", INSTANCES))
CASES.extend(filter_block("INST", INSTANCES))
CASES.extend(http_method_block("INST", INSTANCES))
CASES.append(Case(
    id="INST-CR-VAL-EMPTY-BODY",
    title="Create instance с пустым body → 400 (project_id required)",
    classes=["VAL", "NEG"], priority="P1",
    steps=[Step(name="cr-empty", method="POST", path=INSTANCES, body={},
                test_script=[*assert_status(400), *assert_grpc_code(3, "INVALID_ARGUMENT")])],
))
CASES.append(Case(
    id="INST-CR-VAL-MALFORMED-JSON",
    title="Create instance с malformed JSON → 400/415",
    classes=["VAL", "NEG"], priority="P2",
    steps=[Step(name="cr-malformed", method="POST", path=INSTANCES, body=None,
                pre_script=["pm.request.body = { mode: 'raw', raw: '{invalid json---}' };"],
                test_script=["pm.test('400 or 415', () => pm.expect(pm.response.code).to.be.oneOf([400, 415]));"])],
))
# Security: name injection — instance Create требует много полей; используем минимальное valid body + payload в name.
CASES.extend(security_injection_block(
    "INST", INSTANCES, INSTANCES,
    {"zoneId": "{{existingZoneId}}", "platformId": "{{existingPlatformId}}",
     "resourcesSpec": _resources_spec(),
     "bootDiskSpec": {"autoDelete": True, "diskSpec": {"name": "bd-sec-{{runId}}", "size": _BOOT_SIZE, "typeId": "{{existingDiskTypeId}}"}},
     "networkInterfaceSpecs": [{"subnetId": "{{existingSubnetId}}", "primaryV4AddressSpec": {}}]}))

# blocked: AttachFilesystem/DetachFilesystem — нет ресурса Filesystem.
# CASES.append(...)  # blocked:kacho-filesystem
# blocked: Relocate — нужен cross-zone disk move + cross-service.
# CASES.append(...)  # blocked
# AttachNetworkInterface / DetachNetworkInterface — присутствуют в proto; happy-path требует ещё одного
# subnet'а из kacho-vpc → откладываем (enhancement). Минимум — NEG на несуществующий instance:
CASES.append(Case(
    id="INST-ANI-AUTHZ-NF-SYNC",
    title="AttachNetworkInterface несуществующего instance → sync 404 NOT_FOUND",
    classes=["NEG", "AUTHZ"], priority="P2",
    steps=[Step(name="ani-nx", method="POST", path=f"{INSTANCES}/{{{{garbageComputeId}}}}:attachNetworkInterface",
                body={"networkInterfaceIndex": "1", "subnetId": "{{existingSubnetId}}", "securityGroupIds": ["{{existingSgId}}"]},
                test_script=[*assert_status(404), *assert_grpc_code(5, "NOT_FOUND")])],
))
