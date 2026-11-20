"""C08/C11 local-cache checks for an already built candidate; no publication."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


@unittest.skipUnless(os.environ.get('C2OPS_RELEASE_PACK_PACKAGE'),
                     'set C2OPS_RELEASE_PACK_PACKAGE to the built own candidate for isolated cache checks')
class ReleasePackCacheTests(unittest.TestCase):
    def test_c08_c11_download_only_and_missing_payload(self):
        package = os.environ['C2OPS_RELEASE_PACK_PACKAGE']
        self.assertTrue(Path(package, 'bin/release-pack').is_file())
        def nix(*args, check=True):
            return subprocess.run(['nix','--extra-experimental-features','nix-command flakes',*args],
                                  capture_output=True, text=True, check=check)
        with tempfile.TemporaryDirectory(prefix='release-pack-cache-') as directory:
            base = Path(directory)
            cache = base / 'cache'
            # Local file-cache creation is test data, not external publication.
            nix('copy','--to',cache.as_uri(),package)
            def realize(store, substituter, check=True):
                return nix('build','--no-link','--store',str(store),'--max-jobs','0','--builders','',
                    '--option','substituters',substituter.as_uri(),
                    '--option','extra-substituters','', '--option','require-sigs','false',
                    package, check=check)
            store = base / 'success-store'
            realize(store, cache)
            installed = store / package.lstrip('/')
            self.assertEqual(json.loads((installed / 'share/c2j/op.json').read_text()),
                             json.loads(Path(package, 'share/c2j/op.json').read_text()))
            closure = json.loads(nix('path-info','--json','--recursive',package).stdout)
            paths = [entry['path'] for entry in closure] if isinstance(closure,list) else list(closure)
            self.assertTrue(paths)
            for path in paths:
                self.assertTrue((store / path.lstrip('/')).exists(), path)
            # Fresh store cannot borrow the payload from the builder's store.
            missing = base / 'missing-cache'
            shutil.copytree(cache, missing)
            (missing / (Path(package).name[:32]+'.narinfo')).unlink()
            failed = realize(base / 'missing-store', missing, check=False)
            self.assertNotEqual(failed.returncode, 0)
            self.assertFalse((base / 'missing-store' / package.lstrip('/')).exists())
            self.assertNotIn('building ', failed.stderr.lower())
            print(f'release.pack: isolated cache-only closure import and missing-output rejection passed ({len(paths)} paths)')


if __name__ == '__main__':
    unittest.main()
