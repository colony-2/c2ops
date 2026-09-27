import json
import os
import subprocess
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest.mock import patch

import jsonschema
import yaml

import main


class JevTests(unittest.TestCase):
    def setUp(self):
        self.requests = []
        self.status = 200
        self.response = {
            "model": "jev-1.13.0", "usage": {"input_tokens": 12, "output_tokens": 3},
            "answers": {
                "urgent": {"type": "noul", "noul": 0.98},
                "route": {"type": "choice", "choice": "billing", "confidence": 0.9,
                          "probabilities": {"billing": 0.95, "other": 0.05}},
                "quality": {"type": "score", "score": 1.8, "confidence": 0.8,
                            "legend": {"0": "poor", "1": "okay", "2": "good"},
                            "probabilities": {"0": 0.0, "1": 0.2, "2": 0.8}},
            },
        }
        owner = self

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                owner.requests.append((self.path, self.headers.get("Authorization"), body))
                data = json.dumps(owner.response).encode()
                self.send_response(owner.status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            def log_message(self, *args):
                pass

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)
        self.payload = {
            "state": {"ticket": "Please refund the duplicate charge today."},
            "questions": {
                "urgent": {"type": "noul", "instructions": "Is this urgent?"},
                "route": {"type": "choice", "instructions": {"task": "Route this ticket"},
                          "criteria": {"billing": None, "other": None}},
                "quality": {"type": "score", "instructions": "Rate this ticket",
                            "criteria": ["poor", "okay", "good"]},
            },
            "api_key": "test-secret", "base_url": f"http://127.0.0.1:{self.server.server_port}",
        }
        self.manifest = yaml.safe_load(Path("op.yaml").read_text())

    def invoke(self):
        return subprocess.run(self.manifest["command"], input=json.dumps(self.payload),
                              capture_output=True, text=True, timeout=60,
                              env={**os.environ, **self.manifest.get("env", {})})

    def test_manifest_roundtrip_all_primitives(self):
        jsonschema.validate(self.payload, self.manifest["input_schema"])
        result = self.invoke()
        self.assertEqual(result.returncode, 0, result.stderr)
        output = json.loads(result.stdout)["output"]
        jsonschema.validate(output, self.manifest["output_schema"])
        self.assertEqual(output, self.response)
        path, auth, body = self.requests[0]
        self.assertEqual(path, "/v1/systemone")
        self.assertEqual(auth, "Bearer test-secret")
        self.assertEqual(body["state"], self.payload["state"])
        self.assertEqual(body["questions"], self.payload["questions"])
        self.assertEqual(body["model"], "jev-latest")

    def test_model_and_environment_credentials(self):
        self.payload.pop("api_key")
        self.payload["model"] = "jev-1.13.0"
        with patch.dict(os.environ, {"TYPESAFE_API_KEY": "env-secret"}):
            main.run(self.payload)
        self.assertEqual(self.requests[0][1], "Bearer env-secret")
        self.assertEqual(self.requests[0][2]["model"], "jev-1.13.0")

    def test_auth_error_has_no_success_output_or_secret(self):
        self.status = 401
        self.response = {"error": {"message": "Invalid test-secret"}}
        result = self.invoke()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "")
        self.assertNotIn("test-secret", result.stderr)

    def test_missing_answers_fail(self):
        self.response["answers"].pop("urgent")
        with self.assertRaisesRegex(ValueError, "every requested answer"):
            main.run(self.payload)

    def test_invalid_inputs_fail_before_request(self):
        cases = [{}, {**self.payload, "state": 123}, {**self.payload, "questions": {}},
                 {**self.payload, "timeout_seconds": -1},
                 {**self.payload, "questions": {"q": {"type": "score", "instructions": "Rate", "criteria": ["one"]}}},
                 {**self.payload, "questions": {"q": {"type": "boolean", "instructions": "Yes?"}}}]
        for payload in cases:
            with self.subTest(payload=payload), self.assertRaises(ValueError):
                main.run(payload)
        self.assertEqual(self.requests, [])


if __name__ == "__main__":
    unittest.main()
