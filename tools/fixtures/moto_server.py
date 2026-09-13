"""Explicit compatibility extensions for the pinned Moto API emulator.

Moto stores client tokens but omits their discovery filter. RunInstances applies
instance/volume tags but omits tags requested for newly created interfaces. These
extensions use Moto's actual resource model; they do not synthesize responses or
ownership. HTTP qualification tests include token isolation and foreign tags.
This remains local emulation, not guest execution or live-cloud qualification.
"""
from functools import wraps
from moto.ec2.models import EC2Backend
from moto.ec2.utils import filter_dict_attribute_mapping
from moto.server import main

if "client-token" in filter_dict_attribute_mapping:
    raise RuntimeError("upstream supports client-token; remove and requalify extension")
filter_dict_attribute_mapping["client-token"] = "client_token"

_original_run_instances = EC2Backend.run_instances


@wraps(_original_run_instances)
def run_instances(self, *args, **kwargs):
    tags = kwargs.get("tags", {}).get("network-interface", {})
    existing = {nic.get("NetworkInterfaceId") for nic in kwargs.get("nics", [])}
    reservation = _original_run_instances(self, *args, **kwargs)
    for instance in reservation.instances:
        for interface in instance.nics.values():
            if interface.id not in existing:
                interface.add_tags(tags)
    return reservation


EC2Backend.run_instances = run_instances
main(["-H", "0.0.0.0", "-p", "5000"])
