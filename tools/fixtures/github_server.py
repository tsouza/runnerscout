#!/usr/bin/env python3
"""Isolated chart-test fixture for idle scale-set sessions. Never a live GitHub oracle."""
import base64
from collections import Counter
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
import ssl
import threading
import time
from urllib.parse import urlsplit
import uuid

COUNTS = Counter()
LOCK = threading.Lock()
CLASS = os.environ.get("FIXTURE_CLASS_NAME", "test-class")

def encoded(value):
    return base64.urlsafe_b64encode(json.dumps(value).encode()).decode().rstrip("=")

TOKEN = encoded({"alg": "HS256"}) + "." + encoded({"exp": int(time.time()) + 3600}) + ".c2ln"

class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def reply(self, status, body=None):
        data = b"" if body is None else json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def handle_request(self):
        path = urlsplit(self.path).path
        length = int(self.headers.get("Content-Length", "0"))
        if length > 1048576:
            self.reply(413)
            return
        if length:
            self.rfile.read(length)
        with LOCK:
            COUNTS[self.command + " " + path] += 1
        if self.command == "GET" and path == "/fixture/stats":
            with LOCK:
                self.reply(200, dict(COUNTS))
        elif self.command == "POST" and path.endswith("/runners/registration-token"):
            self.reply(201, {"token": "fixture-registration-token"})
        elif self.command == "POST" and path.endswith("/actions/runner-registration"):
            self.reply(200, {"url": "https://api.github.com/tenant/123/", "token": TOKEN})
        elif self.command == "GET" and path.endswith("/runnerscalesets/1"):
            self.reply(200, {"id": 1, "name": CLASS})
        elif self.command == "POST" and path.endswith("/runnerscalesets/1/sessions"):
            self.reply(200, {"sessionId": str(uuid.uuid4()), "ownerName": "chart-fixture", "runnerScaleSet": {"id": 1, "name": CLASS}, "messageQueueUrl": "https://api.github.com/fixture/messages", "messageQueueAccessToken": TOKEN, "statistics": {"totalAvailableJobs": 0, "totalAcquiredJobs": 0, "totalAssignedJobs": 0, "totalRunningJobs": 0, "totalRegisteredRunners": 0, "totalBusyRunners": 0, "totalIdleRunners": 0}})
        elif self.command == "GET" and path == "/fixture/messages":
            time.sleep(0.2)
            self.reply(202)
        elif self.command == "DELETE" and "/runnerscalesets/1/sessions/" in path:
            self.reply(204)
        else:
            with LOCK:
                COUNTS["unexpected"] += 1
            self.reply(404, {"error": "unexpected fixture request"})

    do_GET = handle_request
    do_POST = handle_request
    do_DELETE = handle_request

if __name__ == "__main__":
    server = ThreadingHTTPServer(("0.0.0.0", 8443), Handler)
    tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    tls.minimum_version = ssl.TLSVersion.TLSv1_2
    tls.load_cert_chain("/tls/tls.crt", "/tls/tls.key")
    server.socket = tls.wrap_socket(server.socket, server_side=True)
    server.serve_forever()
