#!/usr/bin/env python3
"""Bounded CRD/API qualification on a task-owned internal-network kind cluster."""
import hashlib
import ipaddress
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time
import uuid
import yaml

ROOT = Path(__file__).resolve().parents[1]
NODE = "kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5"


def main():
    identity = "runnerscout-crd-" + uuid.uuid4().hex[:8]
    evidence = ROOT / "evidence" / identity
    evidence.mkdir(parents=True)
    result = {"scope": "real isolated Kubernetes CRD validation and configuration snapshot; no runner or cloud execution", "verdict": "fail", "checks": {}, "cleanup_errors": [], "node_image": NODE, "kind": "v0.33.0"}
    result["schemas"] = {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted((ROOT / "config/crd/bases").glob("*.yaml"))}
    deadline = time.monotonic() + 600
    env = os.environ.copy()
    env["KIND_EXPERIMENTAL_DOCKER_NETWORK"] = identity
    kind = ["go", "run", "sigs.k8s.io/kind@v0.33.0"]
    network_created = False
    cluster_attempted = False
    sequence = 0

    def run(name, args, check=True, timeout=120, cleanup=False):
        nonlocal sequence
        sequence += 1
        if not cleanup:
            timeout = min(timeout, max(1, deadline - time.monotonic()))
            if time.monotonic() >= deadline:
                raise TimeoutError("CRD qualification deadline reached")
        process = subprocess.run(args, cwd=ROOT, env=env, text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout)
        (evidence / f"{sequence:02d}-{name}.log").write_text("private ephemeral kubeconfig omitted\n" if name == "kubeconfig" else process.stdout + process.stderr)
        if check and process.returncode:
            raise RuntimeError(f"{name} failed ({process.returncode}); see retained log")
        return process

    try:
        if len(result["schemas"]) != 5:
            raise RuntimeError("complete five-CRD schema set required")
        with tempfile.TemporaryDirectory(prefix=identity + "-") as tmp:
            temp = Path(tmp)
            run("network-create", ["docker", "network", "create", "--internal", "--label", "runnerscout.test=" + identity, identity])
            network_created = True
            network = json.loads(run("network", ["docker", "network", "inspect", identity]).stdout)[0]
            if not network["Internal"]:
                raise RuntimeError("test network is not internal")
            result["checks"]["internal_network"] = "pass"
            gateway = network["IPAM"]["Config"][0]["Gateway"]
            node_ip = str(ipaddress.ip_address(gateway) + 1)
            hosts = temp / "hosts"
            hosts.write_text("127.0.0.1 localhost\n" + gateway + " host.docker.internal\n" + node_ip + " " + identity + "-control-plane\n")
            config = temp / "kind.yaml"
            config.write_text(yaml.safe_dump({"kind": "Cluster", "apiVersion": "kind.x-k8s.io/v1alpha4", "nodes": [{"role": "control-plane", "extraMounts": [{"hostPath": str(hosts), "containerPath": "/etc/hosts", "readOnly": True}]}]}))
            kubeconfig = temp / "kubeconfig"
            cluster_attempted = True
            created = run("cluster-create", kind + ["create", "cluster", "--name", identity, "--image", NODE, "--config", str(config), "--kubeconfig", str(kubeconfig), "--wait", "120s", "--retain"], check=False, timeout=240)
            if created.returncode and "failed to get api server port" not in created.stdout + created.stderr:
                raise RuntimeError("kind bootstrap failed")
            document = yaml.safe_load(run("kubeconfig", kind + ["get", "kubeconfig", "--internal", "--name", identity]).stdout)
            cluster = document["clusters"][0]["cluster"]
            if not cluster.get("certificate-authority-data") or cluster.get("insecure-skip-tls-verify"):
                raise RuntimeError("verified Kubernetes TLS is required")
            cluster["server"] = "https://" + node_ip + ":6443"
            kubeconfig.write_text(yaml.safe_dump(document))
            kubeconfig.chmod(0o600)
            node = json.loads(run("node-network", ["docker", "inspect", identity + "-control-plane"]).stdout)[0]
            if node["NetworkSettings"]["Networks"][identity]["IPAddress"] != node_ip:
                raise RuntimeError("unexpected node IP")
            run("node-ready", ["kubectl", "--kubeconfig", str(kubeconfig), "wait", "--for=condition=Ready", "nodes", "--all", "--timeout=120s"], timeout=150)
            env["RUNNERSCOUT_TEST_KUBECONFIG"] = str(kubeconfig)
            tested = run("integration-tests", ["make", "integration"], check=False, timeout=540)
            if tested.returncode:
                raise RuntimeError("required Kubernetes integration failed, skipped or incomplete")
            result["checks"]["persistence_and_cas"] = "pass"
            result["checks"]["schemas_and_snapshot"] = "pass"
            result["checks"]["namespace_and_crd_cleanup"] = "pass"
            result["verdict"] = "pass"
    except Exception as error:
        result["reason"] = str(error)
    finally:
        if cluster_attempted:
            try:
                run("cluster-cleanup", kind + ["delete", "cluster", "--name", identity], cleanup=True)
                if run("cluster-absence", ["docker", "ps", "-aq", "--filter", "label=io.x-k8s.kind.cluster=" + identity], cleanup=True).stdout.strip():
                    raise RuntimeError("test cluster still present")
            except Exception as error:
                result["cleanup_errors"].append(str(error))
        if network_created:
            try:
                run("network-cleanup", ["docker", "network", "rm", identity], cleanup=True)
            except Exception as error:
                result["cleanup_errors"].append(str(error))
        if result["cleanup_errors"]:
            result["verdict"] = "fail"
        (evidence / "manifest.json").write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps({**result, "evidence": str(evidence.relative_to(ROOT))}))
    return 0 if result["verdict"] == "pass" else 1


if __name__ == "__main__":
    raise SystemExit(main())
