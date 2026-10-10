import json
import os
import subprocess
import sys
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest.mock import patch

import jsonschema

import main


class KimiTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.worktree = self.root / "worktree"
        self.worktree.mkdir()
        self.payload = {"prompt": "Remember the word apricot.", "worktree_path": str(self.worktree),
                        "workdir_path": str(self.root), "artifact_outbox_path": str(self.root / "outbox")}
        self.manifest = json.loads(Path("op.json").read_text())
        self.command = [sys.executable, str(Path(__file__).resolve().parents[1] / "scripts/op_test.py"),
                        "run", "nix:github:colony-2/c2ops/main#kimi"]

    def test_cli_call_matches_declared_package(self):
        with patch.object(main, "execute", return_value=subprocess.CompletedProcess([], 1, "", "test")) as execute:
            main.run(self.payload)
        args = execute.call_args.args[0]
        self.assertEqual(args[:4], ["pnpm", "--package=" + main.KIMI_PACKAGE, "dlx", "kimi"])
        self.assertIn("pnpm:" + main.KIMI_PACKAGE, self.manifest["dependencies"])

    def test_real_cli_session_and_artifacts(self):
        requests = []

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                requests.append((self.path, body))
                text = "stored apricot" if len(requests) == 2 else "apricot"
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                chunks = [({"role": "assistant", "content": text}, None), ({}, "stop")]
                if len(requests) == 1:
                    chunks = [({"role": "assistant", "tool_calls": [{"index": 0, "id": "call-1",
                        "type": "function", "function": {"name": "Bash", "arguments": json.dumps({
                            "command": "printf apricot > kimi-marker.txt", "description": "Write test marker"})}}]}, None),
                        ({}, "tool_calls")]
                for delta, finish in chunks:
                    event = {"id": "chatcmpl-test", "object": "chat.completion.chunk", "created": 1,
                             "model": "mock-model", "choices": [{"index": 0, "delta": delta, "finish_reason": finish}]}
                    self.wfile.write(("data: " + json.dumps(event) + "\n\n").encode())
                self.wfile.write(b"data: [DONE]\n\n")
                self.wfile.flush()

            def log_message(self, *args):
                pass

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        self.payload["env"] = {
            "KIMI_MODEL_NAME": "mock-model", "KIMI_MODEL_PROVIDER_TYPE": "openai",
            "KIMI_MODEL_API_KEY": "dummy", "KIMI_MODEL_BASE_URL": f"http://127.0.0.1:{server.server_port}/v1",
            "KIMI_DISABLE_TELEMETRY": "1",
        }
        jsonschema.validate(self.payload, self.manifest["input_schema"])
        poison = self.root / "bin"
        poison.mkdir()
        (poison / "kimi").write_text("#!/bin/sh\necho 'unexpected bare kimi invocation' >&2\nexit 127\n")
        (poison / "kimi").chmod(0o755)
        for index in range(2):
            # The runner prepares declared tools; a conflicting host CLI must not win.
            proc = subprocess.run(self.command, input=json.dumps(self.payload),
                                  capture_output=True, text=True, timeout=90,
                                  env={**os.environ, **self.manifest.get("env", {}),
                                       "PATH": str(poison) + os.pathsep + os.environ["PATH"]})
            self.assertEqual(proc.returncode, 0, proc.stderr + proc.stdout)
            output = json.loads(proc.stdout)["output"]
            jsonschema.validate(output, self.manifest["output_schema"])
            self.assertEqual(output["status"], "completed")
            self.assertIn("apricot", output["assistantSummary"])
            self.assertTrue(output["sessionId"])
            if index:
                self.assertEqual(output["sessionId"], self.payload["sessionId"])
            self.payload.update(sessionId=output["sessionId"], prompt="What word did I ask you to remember?")
        self.assertEqual(len(requests), 3)
        self.assertEqual(requests[0][0], "/v1/chat/completions")
        self.assertIn("Remember the word apricot.", json.dumps(requests[2][1]["messages"]))
        self.assertEqual((self.worktree / "kimi-marker.txt").read_text(), "apricot")
        events = (self.root / "outbox/stdout.jsonl").read_text()
        self.assertIn("session.resume_hint", events)
        self.assertTrue((self.root / "outbox/stderr.txt").is_file())

    def test_failure_and_malformed_output(self):
        for code, stdout, stderr in [(1, "", "auth failed"), (0, "not json", ""), (0, "", "")]:
            with self.subTest(code=code, stdout=stdout):
                with patch.object(main, "execute", return_value=subprocess.CompletedProcess([], code, stdout, stderr)):
                    output, result_code = main.run(self.payload)
                self.assertNotEqual(result_code, 0)
                self.assertEqual(output["status"], "error")
                self.assertTrue(output["incompleteReason"])
                self.assertTrue((self.root / "outbox/stderr.txt").read_text())

    def test_timeout_preserves_partial_artifact(self):
        with patch.object(main, "execute", side_effect=subprocess.TimeoutExpired("kimi", 1, output=b'{"role":"assistant","content":"partial"}\n')):
            output, code = main.run(self.payload)
        self.assertEqual(code, 124)
        self.assertEqual(output["assistantSummary"], "partial")
        self.assertIn("timed out", output["incompleteReason"])

    def test_invalid_input(self):
        for change in [{"prompt": ""}, {"worktree_path": ""}, {"sessionId": "../escape"}, {"timeout_seconds": 0}]:
            with self.subTest(change=change), self.assertRaises(ValueError):
                main.run({**self.payload, **change})


if __name__ == "__main__":
    unittest.main()
