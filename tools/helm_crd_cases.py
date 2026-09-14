"""CRD lifecycle cases for the task-owned Helm cluster and idle GitHub fixture."""

import json
import time
import yaml

REQUIRED = {
    "crd_schema_retention", "crd_named_secret_rbac", "crd_install_and_check",
    "crd_secret_rotation", "crd_upgrade_rollback", "crd_ownership_changes_rejected",
    "crd_uninstall_guard", "crd_uninstall_retains_state", "crd_namespace_cleanup",
    "network_policy_selector_stability",
}


def qualify(root, temp, kubeconfig, archive, postrenderer, image_tag, manifest, run):
    namespace = "runnerscout-test-crd"
    kube = ["kubectl", "--kubeconfig", str(kubeconfig)]
    namespaced = kube + ["-n", namespace]
    helm = ["helm", "--kubeconfig", str(kubeconfig), "--namespace", namespace]
    release = "crd-qualification"

    def get(resource, name):
        return json.loads(run("crd-get-" + name, namespaced + ["get", resource, name, "-o", "json"]).stdout)

    def until(name, predicate, timeout=90):
        end = time.monotonic() + timeout
        while time.monotonic() < end:
            if predicate():
                return
            time.sleep(1)
        raise RuntimeError(name + " did not complete")

    stats_code = "import ssl,urllib.request; c=ssl.create_default_context(cafile='/tls/tls.crt'); print(urllib.request.urlopen('https://localhost:8443/fixture/stats',context=c).read().decode())"

    def stats():
        return json.loads(run("crd-protocol-stats", kube + ["-n", "runnerscout-test", "exec", "pod/github-fixture", "--", "python3", "-c", stats_code]).stdout)

    def sessions():
        values = stats()
        if values.get("unexpected", 0):
            raise RuntimeError("CRD worker made an unexpected GitHub fixture request")
        return sum(value for key, value in values.items() if key.startswith("POST ") and "/sessions" in key)

    def schema_check():
        for schema in sorted((root / "charts/runnerscout/crds").glob("*.yaml")):
            expected = yaml.safe_load(schema.read_text())
            actual = json.loads(run("crd-schema-" + expected["metadata"]["name"], kube + ["get", "crd", expected["metadata"]["name"], "-o", "json"]).stdout)
            if actual["spec"]["versions"] != expected["spec"]["versions"]:
                raise RuntimeError("installed CRD schema does not match the packaged artifact")

    schema_check()
    run("crd-create-namespace", kube + ["create", "namespace", namespace])
    objects = [
        {"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "fixture-ca", "namespace": namespace}, "data": {"ca.crt": (temp / "tls.crt").read_text()}},
    ]
    fixture_secrets = {
        "github": {"privateKey": (temp / "app.key").read_text()},
        "aws-credentials": {"path": "/etc/runnerscout/providers/aws-credentials/credentials", "credentials": "[runnerscout]\naws_access_key_id=fixture\naws_secret_access_key=fixture\n"},
        "gcp-credentials": {"path": "/etc/runnerscout/providers/gcp-credentials/credentials.json", "credentials.json": json.dumps({"type": "authorized_user", "client_id": "fixture", "client_secret": "fixture", "refresh_token": "fixture"})},
        "azure-identity": {"client-id": "00000000-0000-0000-0000-000000000000", "tenant-id": "00000000-0000-0000-0000-000000000000", "token-file": "/etc/runnerscout/providers/azure-identity/token", "token": "fixture"},
    }
    for name, data in fixture_secrets.items():
        objects.append({"apiVersion": "v1", "kind": "Secret", "metadata": {"name": name, "namespace": namespace}, "stringData": data})
    for name in ["providers.yaml", "class.yaml", "catalog.yaml", "network.yaml", "budget.yaml"]:
        for obj in yaml.safe_load_all((root / "examples/multicloud" / name).read_text()):
            obj["metadata"]["namespace"] = namespace
            if obj["kind"] == "RunnerScaleSet":
                obj["metadata"]["name"] = "test-class"
                obj["spec"]["suspend"] = False
                obj["spec"]["github"].update(url="https://github.com/tsouza", scaleSetID=1)
                obj["spec"]["github"]["auth"].update(appClientID="Iv1.fixture", appInstallationID=42)
            objects.append(obj)
    run("crd-create-configuration", kube + ["apply", "--validate=strict", "-f", "-"], input=yaml.safe_dump_all(objects))

    values = yaml.safe_load((root / "examples/multicloud/values.yaml").read_text())
    values["image"] = {"repository": "runnerscout", "tag": image_tag, "pullPolicy": "Never"}
    # API/selector qualification only; kind's CNI is not an enforcement oracle.
    values["networkPolicy"] = {"enabled": True, "egress": [{}]}
    values["crd"]["scaleSetName"] = "test-class"
    values["credentialSecrets"].append({"name": "azure-identity"})
    values_path = temp / "crd-values.json"
    values_path.write_text(json.dumps(values))
    install = helm + ["upgrade", "--install", release, str(archive), "--values", str(values_path), "--post-renderer", str(postrenderer), "--wait", "--timeout", "120s"]
    run("crd-helm-install", install, timeout=150)
    run("crd-helm-test", helm + ["test", release, "--timeout", "90s"])
    root_object = get("runnerscaleset", "test-class")
    checkpoint = get("configmap", "test-class-configuration")
    fleet = get("configmap", "test-class-fleet")
    controller = get("deployment", "runnerscout")
    controller_policy = get("networkpolicy", "runnerscout")
    if controller_policy["spec"]["podSelector"]["matchLabels"] != controller["spec"]["selector"]["matchLabels"]:
        raise RuntimeError("controller NetworkPolicy abandoned its legacy selector")
    hook_policy = get("networkpolicy", "runnerscout-checks")
    if hook_policy["spec"]["podSelector"].get("matchExpressions") != [{"key": "app.kubernetes.io/component", "operator": "In", "values": ["configuration-check", "cleanup-check"]}]:
        raise RuntimeError("hook NetworkPolicy does not select both isolated hook types")
    if checkpoint["metadata"]["annotations"]["runnerscout.io/scale-set-uid"] != root_object["metadata"]["uid"]:
        raise RuntimeError("checkpoint did not retain the actual scale-set UID")
    if "PRIVATE KEY" in checkpoint["data"]["snapshot"] or "aws_secret_access_key" in checkpoint["data"]["snapshot"]:
        raise RuntimeError("credential contents leaked into checkpoint")
    manifest["checks"]["crd_install_and_check"] = "pass"

    for account, verb, resource, expected in [
        ("runnerscout", "get", "secret/github", "yes"),
        ("runnerscout", "get", "secret/unreferenced", "no"),
        ("runnerscout", "list", "secrets", "no"),
        ("runnerscout-guard", "get", "runnerscaleset/test-class", "yes"),
        ("runnerscout-guard", "get", "secret/github", "no"),
        ("runnerscout-guard", "update", "configmaps", "no"),
    ]:
        result = run("crd-rbac-" + account + "-" + verb, namespaced + ["auth", "can-i", verb, resource, "--as", "system:serviceaccount:" + namespace + ":" + account], check=False)
        if result.stdout.strip() != expected:
            raise RuntimeError("CRD Secret/guard RBAC boundary failed")
    manifest["checks"]["crd_named_secret_rbac"] = "pass"

    before_sessions = sessions()
    run("crd-rotated-key", ["openssl", "genrsa", "-out", str(temp / "rotated.key"), "2048"])
    secret = {"apiVersion": "v1", "kind": "Secret", "metadata": {"name": "github", "namespace": namespace}, "stringData": {"privateKey": (temp / "rotated.key").read_text()}}
    run("crd-secret-rotation", kube + ["apply", "-f", "-"], input=yaml.safe_dump(secret))
    until("Secret-driven session replacement", lambda: sessions() > before_sessions)
    run("crd-rotation-ready", namespaced + ["wait", "--for=condition=Ready", "runnerscaleset/test-class", "--timeout=60s"])
    manifest["checks"]["crd_secret_rotation"] = "pass"

    for name, changed in [
        ("deployment", {**values, "fullnameOverride": "replacement"}),
        ("selector", {**values, "nameOverride": "replacement"}),
        ("root", {**values, "crd": {**values["crd"], "scaleSetName": "another-root"}}),
        ("root-and-deployment", {**values, "fullnameOverride": "replacement", "crd": {**values["crd"], "scaleSetName": "another-root"}}),
    ]:
        values_path.write_text(json.dumps(changed))
        result = run("crd-reject-ownership-" + name, install, check=False)
        if result.returncode == 0 or "configuration mode and scale-set state name cannot change" not in result.stdout + result.stderr:
            raise RuntimeError("unsafe chart ownership change was not rejected before deployment")
    mounted = json.loads((root / "charts/runnerscout/tests/values.json").read_text())
    mounted.update(image=values["image"], fullnameOverride="runnerscout", crd={"scaleSetName": "", "secretNames": []})
    values_path.write_text(json.dumps(mounted))
    result = run("crd-reject-mode-change", install, check=False)
    if result.returncode == 0 or "configuration mode and scale-set state name cannot change" not in result.stdout + result.stderr:
        raise RuntimeError("CRD-to-mounted transition could orphan the old root")
    manifest["checks"]["crd_ownership_changes_rejected"] = "pass"

    values["podAnnotations"] = {"runnerscout.test/revision": "two"}
    values_path.write_text(json.dumps(values))
    run("crd-scale-set-update", namespaced + ["patch", "runnerscaleset", "test-class", "--type=merge", "-p", json.dumps({"spec": {"maxRunners": 3}})])
    run("crd-helm-upgrade", install, timeout=150)
    run("crd-helm-upgrade-test", helm + ["test", release, "--timeout", "90s"])
    run("crd-helm-rollback", helm + ["rollback", release, "1", "--wait", "--timeout", "120s"], timeout=150)
    run("crd-helm-rollback-test", helm + ["test", release, "--timeout", "90s"])
    final_policy = get("networkpolicy", "runnerscout")
    if final_policy["metadata"]["uid"] != controller_policy["metadata"]["uid"] or final_policy["spec"] != controller_policy["spec"]:
        raise RuntimeError("upgrade or rollback replaced the controller policy or changed its selector")
    manifest["checks"]["network_policy_selector_stability"] = "pass"
    manifest["network_policy_scope"] = "real API admission and selector/UID continuity; not CNI traffic-enforcement qualification"
    if get("runnerscaleset", "test-class")["spec"]["maxRunners"] != 3 or get("configmap", "test-class-fleet")["metadata"]["uid"] != fleet["metadata"]["uid"]:
        raise RuntimeError("chart rollback rewound external CRD settings or replaced durable state")
    manifest["checks"]["crd_upgrade_rollback"] = "pass"

    uninstall = helm + ["uninstall", release, "--wait", "--timeout", "60s"]
    blocked = run("crd-uninstall-live-root", uninstall, check=False, timeout=90)
    if blocked.returncode == 0:
        raise RuntimeError("uninstall removed a controller with an existing scale set")
    run("crd-controller-retained", namespaced + ["get", "deployment", "runnerscout"])
    guard_log = run("crd-live-root-guard-log", namespaced + ["logs", "job/runnerscout-uninstall"]).stdout
    if "delete the RunnerScaleSet" not in guard_log:
        raise RuntimeError("uninstall failed without exercising the root guard")

    run("crd-remove-github-credential", namespaced + ["delete", "secret", "github"])
    run("crd-delete-scale-set", namespaced + ["delete", "runnerscaleset", "test-class", "--wait=true", "--timeout=90s"], timeout=120)
    final_fleet = get("configmap", "test-class-fleet")
    modified = json.loads(final_fleet["data"]["fleet"])
    modified.setdefault("created", {})["rs-fixture-missing"] = "2026-01-01T00:00:00Z"
    run("crd-inject-missing-record", namespaced + ["patch", "configmap", "test-class-fleet", "--type=merge", "-p", json.dumps({"data": {"fleet": json.dumps(modified)}})])
    blocked = run("crd-uninstall-missing-record", uninstall, check=False, timeout=90)
    if blocked.returncode == 0:
        raise RuntimeError("uninstall discarded a missing allocation obligation")
    guard_log = run("crd-missing-record-guard-log", namespaced + ["logs", "job/runnerscout-uninstall"]).stdout
    if "cloud cleanup is unresolved" not in guard_log:
        raise RuntimeError("uninstall failed without exercising the durable-state guard")
    run("crd-controller-still-retained", namespaced + ["get", "deployment", "runnerscout"])
    manifest["checks"]["crd_uninstall_guard"] = "pass"
    # Restore only the exact fixture data saved before this negative control.
    # No VMs were admitted by the idle fixture and expired/incomplete catalog.
    run("crd-restore-fixture-record", namespaced + ["patch", "configmap", "test-class-fleet", "--type=merge", "-p", json.dumps({"data": final_fleet["data"]})])
    run("crd-helm-uninstall", uninstall, timeout=90)
    if run("crd-controller-absent", namespaced + ["get", "deployment", "runnerscout", "--ignore-not-found", "-o", "name"]).stdout.strip():
        raise RuntimeError("controller survived completed uninstall")
    if get("configmap", "test-class-fleet")["metadata"]["uid"] != fleet["metadata"]["uid"] or get("configmap", "test-class-configuration")["metadata"]["uid"] != checkpoint["metadata"]["uid"]:
        raise RuntimeError("uninstall removed durable ownership records")
    manifest["checks"]["crd_uninstall_retains_state"] = "pass"
    schema_check()
    manifest["checks"]["crd_schema_retention"] = "pass"
    run("crd-delete-namespace", kube + ["delete", "namespace", namespace, "--wait=true", "--timeout=90s"], timeout=120)
    manifest["checks"]["crd_namespace_cleanup"] = "pass"
