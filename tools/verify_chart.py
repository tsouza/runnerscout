#!/usr/bin/env python3
"""Offline chart contract checks using Helm and the real configuration parser."""
import copy
import json
from pathlib import Path
import subprocess
import tempfile
import tarfile
import unittest
import yaml

ROOT = Path(__file__).resolve().parents[1]
CHART = ROOT / "charts/runnerscout"
FIXTURE = json.loads((CHART / "tests/values.json").read_text())


def render(values, success=True, version="1.37.0"):
    with tempfile.TemporaryDirectory(prefix="runnerscout-chart-") as tmp:
        values_path = Path(tmp) / "values.json"
        values_path.write_text(json.dumps(values))
        result = subprocess.run(["helm", "template", "qualification", str(CHART),
            "--namespace", "runnerscout-test", "--kube-version", version,
            "-f", str(values_path)], capture_output=True, text=True)
    if not success:
        if result.returncode == 0:
            raise AssertionError("invalid chart configuration rendered successfully")
        return result.stderr
    if result.returncode:
        raise AssertionError(result.stderr)
    return [d for d in yaml.safe_load_all(result.stdout) if d]


def resource(docs, kind):
    matches = [d for d in docs if d["kind"] == kind]
    if len(matches) != 1:
        raise AssertionError(f"expected one {kind}, got {len(matches)}")
    return matches[0]


class ChartContracts(unittest.TestCase):
    def test_hook_pods_share_network_policy_without_matching_deployment_selector(self):
        for fixture in [FIXTURE, json.loads((CHART / "tests/crd-values.json").read_text())]:
            values = copy.deepcopy(fixture)
            values["networkPolicy"] = {"enabled": True, "egress": []}
            docs = render(values)
            deployment = resource(docs, "Deployment")
            selector = deployment["spec"]["selector"]["matchLabels"]
            policy = resource(docs, "NetworkPolicy")["spec"]["podSelector"]["matchLabels"]
            for doc in docs:
                if doc["kind"] not in {"Pod", "Job", "Deployment"}:
                    continue
                pod = doc if doc["kind"] == "Pod" else doc["spec"]["template"]
                labels = pod["metadata"].get("labels", {})
                if doc["kind"] != "Deployment":
                    self.assertFalse(all(labels.get(key) == value for key, value in selector.items()), "hook Pod can match the controller ReplicaSet")
                self.assertTrue(all(labels.get(key) == value for key, value in policy.items()), "chart Pod escaped its NetworkPolicy")

    def test_workload_identity_labels_cannot_override_controller_ownership(self):
        values = json.loads((CHART / "tests/crd-values.json").read_text())
        values["podLabels"] = {"azure.workload.identity/use": "true"}
        values["serviceAccount"] = {"annotations": {"azure.workload.identity/client-id": "fixture-client"}}
        docs = render(values)
        template = resource(docs, "Deployment")["spec"]["template"]
        self.assertEqual(template["metadata"]["labels"]["azure.workload.identity/use"], "true")
        for key in ["app.kubernetes.io/name", "app.kubernetes.io/instance", "runnerscout.io/instance"]:
            values["podLabels"] = {key: "another-controller"}
            render(values, success=False)

    def test_crd_mode_uses_named_api_configuration_and_secret_allowlist(self):
        values = json.loads((CHART / "tests/crd-values.json").read_text())
        docs = render(values)
        pod = resource(docs, "Deployment")["spec"]["template"]["spec"]
        self.assertEqual(pod["containers"][0]["args"], ["-scale-set=build", "-namespace=runnerscout-test"])
        self.assertEqual([v["name"] for v in pod["volumes"]], ["tmp"])
        self.assertFalse(any(d["kind"] == "ConfigMap" for d in docs))
        rules = [rule for doc in docs if doc["kind"] == "Role" for rule in doc["rules"]]
        secrets = [rule for rule in rules if "secrets" in rule["resources"]]
        self.assertEqual(len(secrets), 1)
        self.assertEqual(secrets[0]["verbs"], ["get"])
        self.assertEqual(secrets[0]["resourceNames"], values["crd"]["secretNames"])
        root = [rule for rule in rules if "runnerscalesets/status" in rule["resources"]]
        self.assertEqual(root[0]["resourceNames"], ["build"])
        hook = resource(docs, "Pod")["spec"]
        self.assertTrue(hook["automountServiceAccountToken"])
        self.assertIn("-check-crd", hook["containers"][0]["args"])

    def test_crd_uninstall_guard_has_only_read_access(self):
        values = json.loads((CHART / "tests/crd-values.json").read_text())
        values["fullnameOverride"] = "r" * 63
        docs = render(values)
        job = resource(docs, "Job")
        self.assertLessEqual(len(job["metadata"]["name"]), 63)
        self.assertEqual(job["metadata"]["annotations"]["helm.sh/hook"], "pre-delete")
        self.assertEqual(job["spec"]["backoffLimit"], 0)
        pod = job["spec"]["template"]["spec"]
        self.assertIn("-check-uninstall", pod["containers"][0]["args"])
        guard = next(d for d in docs if d["kind"] == "Role" and d["metadata"]["name"] == pod["serviceAccountName"])
        for rule in guard["rules"]:
            self.assertLessEqual(set(rule["verbs"]), {"get", "list"})
            self.assertLessEqual(set(rule["resources"]), {"configmaps", "runnerscalesets"})
        self.assertTrue(pod["containers"][0]["securityContext"]["readOnlyRootFilesystem"])
        values["rbac"] = {"create": False}
        values["serviceAccount"] = {"create": False, "name": "existing"}
        external = render(values)
        self.assertFalse(any(d["kind"] in {"Role", "RoleBinding", "ServiceAccount"} for d in external))

    def test_crd_values_reject_mixed_modes_and_unbounded_secret_access(self):
        fixture = json.loads((CHART / "tests/crd-values.json").read_text())
        for change in [
            {"crd": {"scaleSetName": "../other"}}, {"crd": {"secretNames": []}},
            {"crd": {"secretNames": ["github", "github"]}},
            {"crd": {"secretNames": ["../other"]}}, {"crd": {"scaleSetName": ""}},
            {"config": {"name": "mixed"}}, {"github": {"existingSecret": "mixed"}},
            {"github": {"appClientID": "mixed"}}, {"catalog": {"existingConfigMap": "mixed"}},
        ]:
            with self.subTest(change=change):
                values = copy.deepcopy(fixture)
                for key, value in change.items():
                    values.setdefault(key, {}).update(value)
                render(values, success=False)

    def test_packaged_crds_match_generated_schemas(self):
        schemas = sorted((ROOT / "config/crd/bases").glob("*.yaml"))
        self.assertEqual(len(schemas), 5)
        with tempfile.TemporaryDirectory() as tmp:
            subprocess.run(["helm", "package", str(CHART), "--destination", tmp], check=True, capture_output=True)
            with tarfile.open(next(Path(tmp).glob("*.tgz"))) as package:
                members = [name for name in package.getnames() if "/crds/" in name and name.endswith(".yaml")]
                self.assertEqual(len(members), 5)
                for schema in schemas:
                    self.assertEqual((CHART / "crds" / schema.name).read_bytes(), schema.read_bytes())
                    self.assertEqual(package.extractfile("runnerscout/crds/" + schema.name).read(), schema.read_bytes())

    def test_rendered_multicloud_config_accepted_by_controller(self):
        docs = render(FIXTURE)
        config = json.loads(resource(docs, "ConfigMap")["data"]["config.json"])
        self.assertEqual(config["namespace"], "runnerscout-test")
        self.assertEqual({p["kind"] for p in config["providers"].values()}, {"aws", "azure", "gcp"})
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "config.json"
            path.write_text(json.dumps(config))
            result = subprocess.run([str(ROOT / "bin/runnerscout"), "-config", str(path), "-validate"], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("no external operations", result.stdout)

    def test_packaged_chart_retains_test_hook(self):
        with tempfile.TemporaryDirectory() as tmp:
            subprocess.run(["helm", "package", str(CHART), "--destination", tmp], check=True, capture_output=True)
            package = next(Path(tmp).glob("*.tgz"))
            result = subprocess.run(["helm", "template", "qualification", str(package),
                "--namespace", "runnerscout-test", "--kube-version", "1.37.0",
                "-f", str(CHART / "tests/values.json")], check=True, capture_output=True, text=True)
            docs = [d for d in yaml.safe_load_all(result.stdout) if d]
            self.assertEqual(resource(docs, "Pod")["metadata"]["annotations"]["helm.sh/hook"], "test")

    def test_rbac_and_secret_boundary(self):
        docs = render(FIXTURE)
        self.assertFalse(any(d["kind"] in {"Secret", "ClusterRole", "ClusterRoleBinding"} for d in docs))
        role = resource(docs, "Role")
        self.assertEqual({r for rule in role["rules"] for r in rule["resources"]}, {"configmaps", "leases"})
        self.assertFalse(any("delete" in rule["verbs"] or "*" in rule["verbs"] for rule in role["rules"]))
        subject = resource(docs, "RoleBinding")["subjects"][0]
        self.assertEqual(subject["namespace"], "runnerscout-test")
        pod = resource(docs, "Deployment")["spec"]["template"]["spec"]
        controller = pod["containers"][0]
        self.assertTrue(controller["securityContext"]["readOnlyRootFilesystem"])
        self.assertFalse(controller["securityContext"]["allowPrivilegeEscalation"])
        self.assertTrue(pod["securityContext"]["runAsNonRoot"])
        self.assertIn("-github-token-file=/etc/runnerscout/github/token", controller["args"])
        secret = next(v["secret"] for v in pod["volumes"] if v["name"] == "github")
        self.assertEqual(secret["secretName"], "github-test")
        self.assertEqual(secret["defaultMode"], 0o440)
        hook = resource(docs, "Pod")
        self.assertFalse(hook["spec"]["automountServiceAccountToken"])
        self.assertEqual([v["name"] for v in hook["spec"]["volumes"]], ["config"])

    def test_app_auth_and_external_provider_credentials(self):
        values = copy.deepcopy(FIXTURE)
        values["github"].update(mode="app", appClientID="Iv1.example", appInstallationID=42)
        values["credentialSecrets"] = [{"name": "cloud-credentials"}]
        values["catalog"] = {"existingConfigMap": "fresh-catalog"}
        docs = render(values)
        pod = resource(docs, "Deployment")["spec"]["template"]["spec"]
        args = pod["containers"][0]["args"]
        self.assertIn("-github-app-installation-id=42", args)
        self.assertFalse(any("token-file" in arg for arg in args))
        self.assertIn("/etc/runnerscout/providers/cloud-credentials", [m["mountPath"] for m in pod["containers"][0]["volumeMounts"]])
        config = json.loads(resource(docs, "ConfigMap")["data"]["config.json"])
        self.assertEqual(config["catalogPath"], "/etc/runnerscout/catalog/catalog.json")

    def test_probes_use_distinct_runtime_endpoints(self):
        pod = resource(render(FIXTURE), "Deployment")["spec"]["template"]["spec"]
        controller = pod["containers"][0]
        self.assertEqual(controller["livenessProbe"]["httpGet"]["path"], "/healthz")
        self.assertEqual(controller["readinessProbe"]["httpGet"]["path"], "/readyz")
        self.assertEqual(controller["startupProbe"]["httpGet"]["port"], "health")
        self.assertEqual(controller["ports"], [{"name": "health", "containerPort": 8080}])

    def test_test_log_retention_is_explicit(self):
        default = resource(render(FIXTURE), "Pod")
        self.assertEqual(default["metadata"]["annotations"]["helm.sh/hook-delete-policy"], "before-hook-creation,hook-succeeded")
        values = copy.deepcopy(FIXTURE)
        values["tests"] = {"retainPod": True}
        retained = resource(render(values), "Pod")
        self.assertEqual(retained["metadata"]["annotations"]["helm.sh/hook-delete-policy"], "before-hook-creation")

    def test_existing_service_account_and_rbac(self):
        values = copy.deepcopy(FIXTURE)
        values.update(serviceAccount={"create": False, "name": "external"}, rbac={"create": False})
        docs = render(values)
        self.assertFalse(any(d["kind"] in {"ServiceAccount", "Role", "RoleBinding"} for d in docs))
        self.assertEqual(resource(docs, "Deployment")["spec"]["template"]["spec"]["serviceAccountName"], "external")

    def test_network_isolation_and_digest(self):
        values = copy.deepcopy(FIXTURE)
        values["networkPolicy"] = {"enabled": True, "egress": []}
        values["image"]["digest"] = "sha256:" + "a" * 64
        docs = render(values)
        policy = resource(docs, "NetworkPolicy")["spec"]
        self.assertEqual(policy["ingress"], [])
        self.assertEqual(policy["egress"], [])
        self.assertEqual(policy["podSelector"]["matchLabels"], {"runnerscout.io/instance": "qualification"})
        self.assertIn("@sha256:", resource(docs, "Deployment")["spec"]["template"]["spec"]["containers"][0]["image"])

    def test_invalid_values_fail_closed(self):
        for change in [
            {"image": {"repository": ""}},
            {"github": {"mode": "password"}},
            {"github": {"existingSecret": ""}},
            {"github": {"mode": "app", "appInstallationID": 0}},
            {"config": {"namespace": "foreign"}},
            {"serviceAccount": {"create": False, "name": ""}},
            {"credentialSecrets": [{"name": "../../escape"}]},
            {"unrecognizedOption": True},
        ]:
            with self.subTest(change=change):
                values = copy.deepcopy(FIXTURE)
                for key, value in change.items():
                    if isinstance(value, dict):
                        values.setdefault(key, {}).update(value)
                    else:
                        values[key] = value
                render(values, success=False)
        render(FIXTURE, success=False, version="1.31.0")

    def test_config_update_changes_checksum_without_changing_selector(self):
        before = resource(render(FIXTURE), "Deployment")
        values = copy.deepcopy(FIXTURE)
        values["config"]["maxRunners"] = 2
        after = resource(render(values), "Deployment")
        self.assertEqual(before["spec"]["selector"], after["spec"]["selector"])
        self.assertNotEqual(before["spec"]["template"]["metadata"]["annotations"]["checksum/config"], after["spec"]["template"]["metadata"]["annotations"]["checksum/config"])
        self.assertEqual(after["spec"]["strategy"]["type"], "Recreate")


if __name__ == "__main__":
    unittest.main(verbosity=2)
