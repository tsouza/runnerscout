#!/usr/bin/env python3
"""Execute the runtime image under chart-equivalent restrictions, without networking."""
import argparse
import datetime
import json
from pathlib import Path
import subprocess
import tempfile
import uuid

ROOT = Path(__file__).resolve().parents[1]

def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--image", default="runnerscout:development")
    args = parser.parse_args()
    evidence = ROOT / "evidence" / ("runtime-" + str(uuid.uuid4()))
    evidence.mkdir(parents=True)
    manifest = {"scope": "local container runtime; no cloud or GitHub execution", "time": datetime.datetime.now(datetime.timezone.utc).isoformat(), "checks": {}, "cleanup_errors": [], "verdict": "fail"}
    try:
        metadata = json.loads(subprocess.check_output(["docker", "image", "inspect", args.image], text=True))[0]
        manifest.update(image_id=metadata["Id"], architecture=metadata["Architecture"], size_bytes=metadata["Size"])
        if metadata["Config"]["User"] != "10001:10001":
            raise RuntimeError("runtime image does not declare the chart user")
        with tempfile.TemporaryDirectory(prefix="runnerscout-image-") as tmp:
            path = Path(tmp) / "config.json"
            config = json.loads((ROOT / "charts/runnerscout/tests/values.json").read_text())["config"]
            config["namespace"] = "runtime-test"
            path.write_text(json.dumps(config))
            path.chmod(0o644)
            base = ["docker", "run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--memory", "1g", "--cpus", "2", "--tmpfs", "/tmp:rw,noexec,nosuid,size=268435456", "--mount", f"type=bind,src={path},dst=/config.json,readonly"]
            commands = {
                "identity": ["python3", "-c", "import os; assert os.getuid()==10001; assert not os.access('/usr/local',os.W_OK); print('non-root; read-only root')"],
                "controller_config": ["/usr/local/bin/runnerscout", "-config=/config.json", "-validate"],
                "aws_cli": ["aws", "--version"],
                "azure_python_dependency_absence": ["python", "-c", "import importlib.util; assert all(importlib.util.find_spec(name) is None for name in ('azure', 'msal', 'cryptography')); print('Azure uses native Go SDK')"],
                "gcp_cli": ["gcloud", "version", "--format=json"],
                "gcp_compute_commands": ["gcloud", "compute", "instances", "create", "--help"],
                "gcp_emulator_absence": ["python3", "-c", "from pathlib import Path; found=list(Path('/opt/google-cloud-sdk/platform').glob('*emulator*')); assert not found, 'unused cloud emulators are bundled'; print('no bundled cloud emulators')"],
            }
            for name, command in commands.items():
                container_name = "runnerscout-image-test-" + str(uuid.uuid4())
                try:
                    result = subprocess.run(base + ["--name", container_name, "--entrypoint", command[0], args.image] + command[1:], text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=120)
                finally:
                    cleanup = subprocess.run(["docker", "rm", "--force", container_name], capture_output=True, text=True, timeout=30)
                    if cleanup.returncode and "No such container" not in cleanup.stderr:
                        manifest["cleanup_errors"].append(cleanup.stderr.strip())
                (evidence / (name + ".log")).write_text(result.stdout)
                manifest["checks"][name] = "pass" if result.returncode == 0 else "fail"
                if result.returncode:
                    raise RuntimeError(f"{name} exited {result.returncode}; see retained log")
        if manifest["cleanup_errors"]:
            raise RuntimeError("test container cleanup incomplete")
        manifest["verdict"] = "pass"
    except Exception as error:
        manifest["reason"] = str(error)
    finally:
        (evidence / "manifest.json").write_text(json.dumps(manifest, indent=2) + "\n")
    print(json.dumps({**manifest, "evidence": str(evidence.relative_to(ROOT))}))
    return 0 if manifest["verdict"] == "pass" else 1

if __name__ == "__main__":
    raise SystemExit(main())
