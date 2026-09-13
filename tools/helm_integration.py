#!/usr/bin/env python3
"""Real isolated Kubernetes Helm lifecycle; idle GitHub protocol fixture only."""
import argparse
import datetime
import hashlib
import json
import ipaddress
import os
from pathlib import Path
import subprocess
import tempfile
import time
import uuid
import yaml
import helm_crd_cases

ROOT = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--image-tag", default="development")
    args = parser.parse_args()
    image = "runnerscout:" + args.image_tag
    identity = "runnerscout-helm-" + uuid.uuid4().hex[:8]
    evidence = ROOT / "evidence" / identity
    evidence.mkdir(parents=True)
    manifest = {"scope": "real Kubernetes and Helm lifecycle with an idle HTTPS GitHub fixture; no live GitHub job or cloud VM", "started_at": datetime.datetime.now(datetime.timezone.utc).isoformat(), "checks": {}, "cleanup_errors": [], "verdict": "fail"}
    manifest["harness_sha256"] = hashlib.sha256(b"\0".join((ROOT / "tools" / name).read_bytes() for name in ("helm_integration.py", "helm_crd_cases.py"))).hexdigest()
    manifest["example_sha256"] = hashlib.sha256(b"\0".join((ROOT / "examples/multicloud" / name).read_bytes() for name in ("providers.yaml", "class.yaml", "catalog.yaml", "network.yaml", "values.yaml"))).hexdigest()
    deadline = time.monotonic() + 1500
    sequence = 0
    diagnostic_kubeconfig = None
    network_created = False
    cluster_attempted = False
    env = os.environ.copy()
    env["KIND_EXPERIMENTAL_DOCKER_NETWORK"] = identity
    kind = ["go", "run", "sigs.k8s.io/kind@v0.33.0"]

    def diagnose():
        commands = [["docker", "logs", "--tail", "120", identity + "-control-plane"]]
        if diagnostic_kubeconfig and diagnostic_kubeconfig.exists():
            base = ["kubectl", "--kubeconfig", str(diagnostic_kubeconfig), "-n", "runnerscout-test"]
            commands += [base + ["get", "pods", "-o", "wide"], base + ["get", "events", "--sort-by=.lastTimestamp"], base + ["logs", "deployment/runnerscout", "--all-containers", "--tail=100"], base + ["logs", "pod/github-fixture", "--tail=100"]]
            crd_base = ["kubectl", "--kubeconfig", str(diagnostic_kubeconfig), "-n", "runnerscout-test-crd"]
            commands += [crd_base + ["get", "pods", "-o", "wide"], crd_base + ["get", "events", "--sort-by=.lastTimestamp"], crd_base + ["get", "runnerscalesets", "-o", "yaml"], crd_base + ["logs", "deployment/runnerscout", "--all-containers", "--tail=100"], crd_base + ["logs", "job/runnerscout-uninstall", "--tail=100"]]
        for index, command in enumerate(commands):
            try:
                result = subprocess.run(command, cwd=ROOT, env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=20)
                (evidence / f"diagnostic-{index}.log").write_text(result.stdout)
            except Exception as error:
                (evidence / f"diagnostic-{index}.log").write_text(str(error))

    def run(name, args, input=None, timeout=120, check=True, cleanup=False):
        nonlocal sequence
        sequence += 1
        if not cleanup:
            timeout = min(timeout, max(1, deadline - time.monotonic()))
            if time.monotonic() >= deadline:
                raise RuntimeError("Helm qualification campaign deadline reached")
        try:
            result = subprocess.run(args, cwd=ROOT, env=env, input=input, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout)
        except subprocess.TimeoutExpired as error:
            output = error.stdout or ""
            if isinstance(output, bytes):
                output = output.decode(errors="replace")
            (evidence / f"{sequence:02d}-{name}.log").write_text(output)
            if not cleanup:
                diagnose()
            raise
        (evidence / f"{sequence:02d}-{name}.log").write_text("private ephemeral kubeconfig omitted\n" if name == "internal-kubeconfig" else result.stdout + result.stderr)
        if check and result.returncode:
            if not cleanup:
                diagnose()
            raise RuntimeError(f"{name} exited {result.returncode}; see retained log")
        return result

    try:
        metadata = json.loads(run("image", ["docker", "image", "inspect", image]).stdout)[0]
        manifest["image_id"] = metadata["Id"]
        with tempfile.TemporaryDirectory(prefix=identity + "-") as tmp:
            temp = Path(tmp)
            kubeconfig = temp / "kubeconfig"
            diagnostic_kubeconfig = kubeconfig
            kubectl = ["kubectl", "--kubeconfig", str(kubeconfig)]
            ns = "runnerscout-test"
            helm = ["helm", "--kubeconfig", str(kubeconfig), "--namespace", ns]
            run("create-network", ["docker", "network", "create", "--internal", "--label", "runnerscout.test=" + identity, identity])
            network_created = True
            network = json.loads(run("network-isolation", ["docker", "network", "inspect", identity]).stdout)[0]
            if not network["Internal"]:
                raise RuntimeError("test Docker network is not internal")
            manifest["checks"]["internal_network"] = "pass"
            # kind's entrypoint needs a Docker DNS gateway even on an internal
            # network without a default route. Supply that gateway explicitly
            # through a test-owned hosts mount, retaining network isolation.
            gateway = network["IPAM"]["Config"][0]["Gateway"]
            node_ip = str(ipaddress.ip_address(gateway) + 1)
            hosts = temp / "node-hosts"
            hosts.write_text("127.0.0.1 localhost\n" + gateway + " host.docker.internal\n" + node_ip + " " + identity + "-control-plane\n")
            kind_config = temp / "kind.yaml"
            kind_config.write_text(yaml.safe_dump({"kind": "Cluster", "apiVersion": "kind.x-k8s.io/v1alpha4", "nodes": [{"role": "control-plane", "extraMounts": [{"hostPath": str(hosts), "containerPath": "/etc/hosts", "readOnly": True}]}]}))
            cluster_attempted = True
            created = run("create-cluster", kind + ["create", "cluster", "--name", identity, "--image", "kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5", "--kubeconfig", str(kubeconfig), "--config", str(kind_config), "--wait", "120s", "--retain"], timeout=240, check=False)
            # Docker intentionally does not publish ports on an internal network.
            # kind may finish bootstrap but fail to export an external endpoint.
            # Accept only that specific export failure; independently require a
            # TLS-validated API and Ready node through its internal address.
            if created.returncode and "failed to get api server port" not in created.stdout + created.stderr:
                diagnose()
                raise RuntimeError("kind bootstrap failed before kubeconfig export")
            internal = yaml.safe_load(run("internal-kubeconfig", kind + ["get", "kubeconfig", "--internal", "--name", identity]).stdout)
            cluster = internal["clusters"][0]["cluster"]
            if not cluster.get("certificate-authority-data") or cluster.get("insecure-skip-tls-verify"):
                raise RuntimeError("internal kubeconfig lacks verified TLS")
            cluster["server"] = "https://" + node_ip + ":6443"
            kubeconfig.write_text(yaml.safe_dump(internal))
            kubeconfig.chmod(0o600)
            run("node-ready", kubectl + ["wait", "--for=condition=Ready", "nodes", "--all", "--timeout=120s"], timeout=150)
            node = json.loads(run("node-network", ["docker", "inspect", identity + "-control-plane"]).stdout)[0]
            if node["NetworkSettings"]["Networks"][identity]["IPAddress"] != node_ip:
                raise RuntimeError("test node IP differs from explicit internal-network hosts mapping")
            run("load-image", kind + ["load", "docker-image", image, "--name", identity], timeout=240)
            version = json.loads(run("cluster-version", kubectl + ["version", "-o", "json"]).stdout)
            if not version["serverVersion"]["gitVersion"].startswith("v1.37."):
                raise RuntimeError("unexpected Kubernetes qualification version")
            manifest["kubernetes"] = version["serverVersion"]["gitVersion"]
            run("fixture-cert", ["openssl", "req", "-x509", "-nodes", "-newkey", "rsa:2048", "-days", "1", "-keyout", str(temp / "tls.key"), "-out", str(temp / "tls.crt"), "-subj", "/CN=RunnerScout test fixture", "-addext", "subjectAltName=DNS:api.github.com,DNS:github.com"])
            run("app-key", ["openssl", "genrsa", "-out", str(temp / "app.key"), "2048"])
            labels = {"app": "github-fixture"}
            objects = [
                {"apiVersion": "v1", "kind": "Namespace", "metadata": {"name": ns}},
                {"apiVersion": "v1", "kind": "Secret", "metadata": {"name": "fixture-tls", "namespace": ns}, "type": "kubernetes.io/tls", "stringData": {"tls.crt": (temp / "tls.crt").read_text(), "tls.key": (temp / "tls.key").read_text()}},
                {"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "fixture-ca", "namespace": ns}, "data": {"ca.crt": (temp / "tls.crt").read_text()}},
                {"apiVersion": "v1", "kind": "Secret", "metadata": {"name": "github-test", "namespace": ns}, "stringData": {"token": "fixture-token", "privateKey": (temp / "app.key").read_text()}},
                {"apiVersion": "v1", "kind": "ConfigMap", "metadata": {"name": "fixture-script", "namespace": ns}, "data": {"github_server.py": (ROOT / "tools/fixtures/github_server.py").read_text()}},
                {"apiVersion": "v1", "kind": "Service", "metadata": {"name": "github-fixture", "namespace": ns}, "spec": {"selector": labels, "ports": [{"port": 443, "targetPort": 8443}]}},
                {"apiVersion": "v1", "kind": "Pod", "metadata": {"name": "github-fixture", "namespace": ns, "labels": labels}, "spec": {"automountServiceAccountToken": False, "restartPolicy": "Never", "securityContext": {"runAsNonRoot": True, "runAsUser": 10001, "runAsGroup": 10001, "fsGroup": 10001, "seccompProfile": {"type": "RuntimeDefault"}}, "containers": [{"name": "fixture", "image": image, "imagePullPolicy": "Never", "command": ["python3", "/fixture/github_server.py"], "ports": [{"containerPort": 8443}], "readinessProbe": {"tcpSocket": {"port": 8443}, "periodSeconds": 1}, "securityContext": {"allowPrivilegeEscalation": False, "readOnlyRootFilesystem": True, "capabilities": {"drop": ["ALL"]}}, "resources": {"requests": {"cpu": "50m", "memory": "64Mi"}, "limits": {"memory": "256Mi"}}, "volumeMounts": [{"name": "script", "mountPath": "/fixture", "readOnly": True}, {"name": "tls", "mountPath": "/tls", "readOnly": True}]}], "volumes": [{"name": "script", "configMap": {"name": "fixture-script"}}, {"name": "tls", "secret": {"secretName": "fixture-tls", "defaultMode": 288}}]}},
            ]
            run("fixture-apply", kubectl + ["apply", "-f", "-"], input=yaml.safe_dump_all(objects))
            run("fixture-ready", kubectl + ["-n", ns, "wait", "--for=condition=Ready", "pod/github-fixture", "--timeout=90s"])
            service = json.loads(run("fixture-service", kubectl + ["-n", ns, "get", "service", "github-fixture", "-o", "json"]).stdout)
            fixture_ip = service["spec"]["clusterIP"]
            # Test-only transport substitution. The production chart has no
            # insecure endpoint flag; TLS validation remains enabled.
            postrenderer = temp / "postrender"
            postrenderer.write_text("#!/usr/bin/env python3\nimport sys,yaml\ndocs=list(yaml.safe_load_all(sys.stdin))\nfor d in docs:\n if d and d.get('kind')=='Deployment':\n  p=d['spec']['template']['spec']\n  p['hostAliases']=[{'ip':" + repr(fixture_ip) + ",'hostnames':['api.github.com','github.com']}]\n  p['volumes'].append({'name':'fixture-ca','configMap':{'name':'fixture-ca'}})\n  p['containers'][0]['volumeMounts'].append({'name':'fixture-ca','mountPath':'/fixture-ca','readOnly':True})\n  p['containers'][0]['env'].append({'name':'SSL_CERT_FILE','value':'/fixture-ca/ca.crt'})\nyaml.safe_dump_all(docs,sys.stdout)\n")
            postrenderer.chmod(0o755)
            values = json.loads((ROOT / "charts/runnerscout/tests/values.json").read_text())
            values["image"] = {"repository": "runnerscout", "tag": args.image_tag, "pullPolicy": "Never"}
            values["fullnameOverride"] = "runnerscout"
            values_path = temp / "values.json"
            values_path.write_text(json.dumps(values))
            run("package-chart", ["helm", "package", str(ROOT / "charts/runnerscout"), "--destination", str(temp)])
            archives = list(temp.glob("runnerscout-*.tgz"))
            if len(archives) != 1:
                raise RuntimeError("expected one packaged chart artifact")
            manifest["chart_archive_sha256"] = hashlib.sha256(archives[0].read_bytes()).hexdigest()
            manifest["fixture_sha256"] = hashlib.sha256((ROOT / "tools/fixtures/github_server.py").read_bytes()).hexdigest()
            install = helm + ["upgrade", "--install", "qualification", str(archives[0]), "--values", str(values_path), "--post-renderer", str(postrenderer), "--wait", "--timeout", "120s"]
            run("helm-install", install, timeout=150)
            manifest["checks"]["install_ready"] = "pass"
            manifest["checks"]["packaged_chart_install"] = "pass"
            run("helm-test", helm + ["test", "qualification", "--timeout", "90s"])
            manifest["checks"]["helm_test"] = "pass"
            fleet = json.loads(run("fleet-before", kubectl + ["-n", ns, "get", "configmap", "test-class-fleet", "-o", "json"]).stdout)
            fleet_uid = fleet["metadata"]["uid"]
            pod = json.loads(run("pod-before", kubectl + ["-n", ns, "get", "pods", "-l", "app.kubernetes.io/instance=qualification", "-o", "json"]).stdout)["items"][0]
            pod_uid = pod["metadata"]["uid"]
            sa = "system:serviceaccount:" + ns + ":runnerscout"
            for verb, resource, expected in [("get", "configmaps", "yes"), ("update", "leases.coordination.k8s.io", "yes"), ("get", "secrets", "no"), ("delete", "configmaps", "no")]:
                result = run("rbac-" + verb + "-" + resource, kubectl + ["auth", "can-i", verb, resource, "--as", sa, "-n", ns], check=False)
                if result.stdout.strip() != expected:
                    raise RuntimeError("unexpected effective RBAC for " + verb + " " + resource)
            manifest["checks"]["effective_rbac"] = "pass"
            values["github"].update(mode="app", appClientID="Iv1.fixture", appInstallationID=42)
            values["config"]["maxRunners"] = 2
            values["tests"] = {"retainPod": True}
            values_path.write_text(json.dumps(values))
            run("helm-upgrade", install, timeout=150)
            current = json.loads(run("config-upgraded", kubectl + ["-n", ns, "get", "configmap", "runnerscout-config", "-o", "json"]).stdout)
            if json.loads(current["data"]["config.json"])["maxRunners"] != 2:
                raise RuntimeError("upgrade did not apply controller configuration")
            pods = json.loads(run("pods-upgraded", kubectl + ["-n", ns, "get", "pods", "-l", "app.kubernetes.io/instance=qualification", "-o", "json"]).stdout)["items"]
            if len(pods) != 1 or pods[0]["metadata"]["uid"] == pod_uid:
                raise RuntimeError("upgrade did not replace the controller pod")
            test_result = run("helm-test-retained-logs", helm + ["test", "qualification", "--logs", "--timeout", "90s"])
            if "configuration valid; no external operations performed" not in test_result.stdout:
                raise RuntimeError("Helm test did not demonstrate the actual configuration parser")
            manifest["checks"]["retained_test_logs"] = "pass"
            manifest["checks"]["upgrade_ready"] = "pass"
            run("helm-rollback", helm + ["rollback", "qualification", "1", "--wait", "--timeout", "120s"], timeout=150)
            current = json.loads(run("config-rolled-back", kubectl + ["-n", ns, "get", "configmap", "runnerscout-config", "-o", "json"]).stdout)
            if json.loads(current["data"]["config.json"])["maxRunners"] != 1:
                raise RuntimeError("rollback did not restore controller configuration")
            run("helm-test-rollback", helm + ["test", "qualification", "--timeout", "90s"])
            test_pod = run("successful-test-cleanup", kubectl + ["-n", ns, "get", "pod", "runnerscout-config-test", "--ignore-not-found", "-o", "json"]).stdout
            if test_pod.strip():
                raise RuntimeError("successful test pod was not removed after restoring default cleanup")
            manifest["checks"]["rollback_ready"] = "pass"
            # Query through the controller, whose test-only hosts and CA map to
            # the fixture. This also proves that its actual runtime trusts TLS.
            stats_code = "import ssl,urllib.request; c=ssl.create_default_context(cafile='/fixture-ca/ca.crt'); print(urllib.request.urlopen('https://api.github.com/fixture/stats',context=c).read().decode())"
            stats = json.loads(run("protocol-stats", kubectl + ["-n", ns, "exec", "deployment/runnerscout", "--", "python3", "-c", stats_code]).stdout)
            if stats.get("unexpected", 0) or not any("/sessions" in key and key.startswith("POST") and value >= 3 for key, value in stats.items()) or not stats.get("GET /fixture/messages", 0):
                raise RuntimeError("fixture did not observe expected controller session lifecycle")
            if not stats.get("POST /app/installations/42/access_tokens", 0):
                raise RuntimeError("GitHub App installation-token exchange was not exercised")
            manifest["checks"]["app_authentication"] = "pass"
            manifest["checks"]["idle_scaleset_protocol"] = "pass"
            manifest["fixture_requests"] = stats
            run("helm-uninstall", helm + ["uninstall", "qualification", "--wait", "--timeout", "90s"])
            remaining = json.loads(run("deployment-absent", kubectl + ["-n", ns, "get", "deployment", "runnerscout", "--ignore-not-found", "-o", "json"]).stdout or "null")
            if remaining is not None:
                raise RuntimeError("deployment still present after uninstall")
            fleet = json.loads(run("fleet-retained", kubectl + ["-n", ns, "get", "configmap", "test-class-fleet", "-o", "json"]).stdout)
            if fleet["metadata"]["uid"] != fleet_uid:
                raise RuntimeError("durable fleet state replaced or lost")
            manifest["checks"]["uninstall_retains_state"] = "pass"
            helm_crd_cases.qualify(ROOT, temp, kubeconfig, archives[0], postrenderer, args.image_tag, manifest, run)
            run("delete-namespace", kubectl + ["delete", "namespace", ns, "--wait=true", "--timeout=90s"])
            manifest["checks"]["namespace_cleanup"] = "pass"
            required = {"packaged_chart_install", "app_authentication", "internal_network", "install_ready", "helm_test", "effective_rbac", "upgrade_ready", "retained_test_logs", "rollback_ready", "idle_scaleset_protocol", "uninstall_retains_state", "namespace_cleanup"}
            required |= helm_crd_cases.REQUIRED
            if any(manifest["checks"].get(check) != "pass" for check in required):
                raise RuntimeError("required lifecycle evidence missing")
            manifest["verdict"] = "pass"
    except Exception as error:
        manifest["reason"] = str(error)
        # Preserve failure state while the temporary kubeconfig still exists if
        # possible. Never fall back to the user's default cluster.
    finally:
        if cluster_attempted:
            try:
                run("cluster-cleanup", kind + ["delete", "cluster", "--name", identity], timeout=120, cleanup=True)
                result = run("cluster-absence", ["docker", "ps", "-aq", "--filter", "label=io.x-k8s.kind.cluster=" + identity], cleanup=True)
                if result.stdout.strip():
                    raise RuntimeError("test cluster containers remain")
            except Exception as error:
                manifest["cleanup_errors"].append(str(error))
        if network_created:
            try:
                run("network-cleanup", ["docker", "network", "rm", identity], cleanup=True)
            except Exception as error:
                manifest["cleanup_errors"].append(str(error))
        if manifest["cleanup_errors"]:
            manifest["verdict"] = "fail"
        (evidence / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    print(json.dumps({**manifest, "evidence": str(evidence.relative_to(ROOT))}))
    return 0 if manifest["verdict"] == "pass" else 1

if __name__ == "__main__":
    raise SystemExit(main())
