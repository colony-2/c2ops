"""Exercise the download gate with real Nix stores and a signed fixture cache."""

import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import threading
import unittest
from unittest.mock import patch

import publication


@unittest.skipUnless(os.environ.get("C2OPS_TEST_NIX_PUBLICATION") == "1",
                     "set C2OPS_TEST_NIX_PUBLICATION=1 to run real Nix cache tests")
class NixPublicationTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temporary = tempfile.TemporaryDirectory(prefix="c2ops-cache-tests-")
        cls.addClassCleanup(cls.temporary.cleanup)
        cls.directory = Path(cls.temporary.name)
        # Input-addressed outputs require signatures. The dependent output is
        # deliberately tiny, and /bin/sh is used only to construct test data.
        expression = '''let
          mk = name: script: extra: builtins.derivation ({
            inherit name; system = builtins.currentSystem; builder = "/bin/sh";
            args = [ "-c" script ];
          } // extra);
          dependency = mk "c2ops-cache-test-dependency" "echo dependency > $out" {};
        in mk "c2ops-cache-test-root" "echo $dependency > $out" { inherit dependency; }
        '''
        output = cls.nix("build", "--impure", "--no-link", "--json",
                         "--option", "sandbox", "false", "--option", "pre-build-hook", "",
                         "--max-jobs", "1", "--expr", expression)
        cls.root = json.loads(output)[0]["outputs"]["out"]
        cls.dependency = Path(cls.root).read_text().strip()
        secret = cls.directory / "secret"
        public = cls.directory / "public"
        subprocess.run(["nix-store", "--generate-binary-cache-key", "c2ops-test", str(secret), str(public)],
                       check=True, capture_output=True)
        cls.public_key = public.read_text().strip()
        cls.nix("store", "sign", "--recursive", "--key-file", str(secret), cls.root)
        cls.cache = cls.directory / "cache"
        cls.nix("copy", "--to", cls.cache.as_uri(), cls.root)

    @staticmethod
    def nix(*args):
        return subprocess.run(["nix", "--extra-experimental-features", "nix-command flakes", *args],
                              check=True, text=True, capture_output=True).stdout

    def verify_from(self, cache, store):
        original = publication.run

        def fixture_cache(*args, **kwargs):
            args = list(args)
            if "substituters" in args:
                args[args.index("substituters") + 1] = cache.as_uri()
            args[1:1] = ["--option", "trusted-public-keys", self.public_key,
                         "--option", "extra-trusted-public-keys", ""]
            return original(*args, capture_output=True, **kwargs)

        data = {"revision": "a" * 40, "system": "test",
                "packages": {"fixture": {"store_path": self.root}}}
        with patch("publication.run", side_effect=fixture_cache):
            publication.verify(data, store, "colony2")

    def test_downloads_full_signed_closure_into_empty_store(self):
        store = self.directory / "success"
        self.verify_from(self.cache, store)
        for path in (self.root, self.dependency):
            self.assertTrue((store / path.lstrip("/")).is_file())

    def test_missing_dependency_fails_even_when_present_in_builders_store(self):
        cache = self.directory / "incomplete"
        shutil.copytree(self.cache, cache)
        (cache / (Path(self.dependency).name[:32] + ".narinfo")).unlink()
        self.assertTrue(Path(self.root).exists())
        self.assertTrue(Path(self.dependency).exists())
        with self.assertRaises(subprocess.CalledProcessError):
            self.verify_from(cache, self.directory / "missing")

    def test_unsigned_output_fails(self):
        cache = self.directory / "unsigned"
        shutil.copytree(self.cache, cache)
        narinfo = cache / (Path(self.root).name[:32] + ".narinfo")
        narinfo.write_text("\n".join(line for line in narinfo.read_text().splitlines()
                                     if not line.startswith("Sig:")) + "\n")
        with self.assertRaises(subprocess.CalledProcessError):
            self.verify_from(cache, self.directory / "untrusted")

    def test_pin_uses_verified_store(self):
        requests = []

        class Handler(BaseHTTPRequestHandler):
            def do_POST(self):
                requests.append(json.loads(self.rfile.read(int(self.headers["Content-Length"]))))
                self.send_response(200)
                self.end_headers()

            def log_message(self, *args):
                pass

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(server.server_close)
        self.addCleanup(server.shutdown)
        store = self.directory / "pin-store"
        self.verify_from(self.cache, store)
        catalog = self.directory / "catalog"
        (catalog / "nix").mkdir(parents=True)
        (catalog / "nix/ops.json").write_text('{"fixture": {}}')
        data = {"revision": "a" * 40, "system": "test",
                "packages": {"fixture": {"store_path": self.root}}}
        original = publication.run

        def local_api(*args, **kwargs):
            kwargs["env"]["CACHIX_AUTH_TOKEN"] = "local-test-token"
            return original(args[0], "--host", f"http://127.0.0.1:{server.server_port}",
                            *args[1:], capture_output=True, **kwargs)

        with patch("publication.ROOT", catalog), patch("publication.run", side_effect=local_api):
            # The root exists in the builder's store; an empty selected store
            # must still fail, proving Cachix actually honors NIX_REMOTE.
            with self.assertRaises(subprocess.CalledProcessError):
                publication.pin(data, self.directory / "wrong-pin-store", "colony2")
            publication.pin(data, store, "colony2")
        self.assertEqual(len(requests), 1)
        self.assertEqual(requests[0]["storePath"], self.root)
