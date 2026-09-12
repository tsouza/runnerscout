#!/usr/bin/env python3
"""Offline chart contract checks using Helm and the real configuration parser."""
import copy
import json
from pathlib import Path
import subprocess
import tempfile
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
        self.assertEqual(policy["podSelector"]["matchLabels"], resource(docs, "Deployment")["spec"]["selector"]["matchLabels"])
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
