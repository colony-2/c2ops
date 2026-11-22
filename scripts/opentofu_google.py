"""Private helpers for the OpenTofu test harness; no production operation imports."""
import datetime as dt
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import tempfile
import urllib.parse
import urllib.request
import urllib.error

class SafeError(Exception):
    """Fixed public category; never wrap provider output or credential bytes."""

NAME = re.compile(r'[A-Za-z_][A-Za-z0-9_-]{0,255}\Z')
PROJECT = re.compile(r'[a-z][a-z0-9-]{4,28}[a-z0-9]\Z')
EMAIL = re.compile(r'[a-zA-Z0-9._-]+@[a-zA-Z0-9.-]+\.gserviceaccount\.com\Z')
BUCKET = re.compile(r'[a-z0-9][a-z0-9._-]{1,220}[a-z0-9]\Z')
CASES = ('token', 'direct_key', 'impersonated_key')


def now():
    return dt.datetime.now(dt.timezone.utc)


def expiry(value, budget):
    try:
        end = dt.datetime.fromisoformat(value.replace('Z', '+00:00'))
        if end.utcoffset() != dt.timedelta(0) or end < now() + dt.timedelta(seconds=budget + 420):
            raise ValueError()
        return end
    except (TypeError, ValueError, AttributeError):
        raise SafeError('insufficient_or_invalid_expiry') from None


def recipient(value):
    # age X25519 public recipients use Bech32; validate its checksum before resolution.
    alphabet = 'qpzry9x8gf2tvdw0s3jn54khce6mua7l'
    if not isinstance(value, str) or not value.startswith('age1') or len(value) != 62:
        return False
    try:
        data = [ord(c) >> 5 for c in 'age'] + [0] + [ord(c) & 31 for c in 'age'] + [alphabet.index(c) for c in value[4:]]
        check = 1
        for v in data:
            top = check >> 25
            check = ((check & 0x1ffffff) << 5) ^ v
            for i, g in enumerate((0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3)):
                if (top >> i) & 1:
                    check ^= g
        return check == 1
    except ValueError:
        return False


def profile(data):
    fields = {'version', 'project', 'bucket', 'negative_bucket', 'sentinel', 'source_email', 'target_email',
              'token_secret', 'key_secret', 'timeout_seconds', 'age_recipients', 'prefixes', 'negative_confirmed'}
    if not isinstance(data, dict) or set(data) != fields or type(data['version']) is not int or data['version'] != 1:
        raise SafeError('invalid_profile')
    for key, grammar in [('project', PROJECT), ('bucket', BUCKET), ('negative_bucket', BUCKET),
                         ('source_email', EMAIL), ('target_email', EMAIL), ('token_secret', NAME), ('key_secret', NAME)]:
        if not isinstance(data[key], str) or not grammar.fullmatch(data[key]):
            raise SafeError('invalid_profile')
    if (data['bucket'] == data['negative_bucket'] or data['source_email'] == data['target_email']
            or data['token_secret'] == data['key_secret'] or data['negative_confirmed'] is not True
            or not isinstance(data['sentinel'], str) or not re.fullmatch(r'[a-zA-Z0-9_/-]{1,200}', data['sentinel'])
            or type(data['timeout_seconds']) is not int or not 60 <= data['timeout_seconds'] <= 1800):
        raise SafeError('invalid_profile')
    if not isinstance(data['age_recipients'], list) or not data['age_recipients'] or not all(map(recipient, data['age_recipients'])):
        raise SafeError('invalid_recipients')
    prefixes = data['prefixes']
    if not isinstance(prefixes, dict) or set(prefixes) != set(CASES):
        raise SafeError('invalid_prefixes')
    values = list(prefixes.values())
    for v in values:
        if not isinstance(v, str) or not re.fullmatch(r'[a-zA-Z0-9_-]+(?:/[a-zA-Z0-9_-]+)*', v):
            raise SafeError('invalid_prefixes')
        if any(w != v and (w.startswith(v + '/') or v.startswith(w + '/')) for w in values) or values.count(v) != 1:
            raise SafeError('overlapping_prefixes')
    return data


def private_dir(path):
    path = Path(path).absolute()
    for parent in (path, *path.parents):
        if parent.is_symlink():
            raise SafeError('unsafe_private_directory')
    repo = Path(__file__).resolve().parents[1]
    if path == repo or repo in path.parents or any(p.name in ('inbox', 'outbox') for p in (path, *path.parents)):
        raise SafeError('private_directory_inside_delivery')
    path.mkdir(mode=0o700, parents=False, exist_ok=True)
    st = path.stat()
    if not stat.S_ISDIR(st.st_mode) or st.st_uid != os.getuid() or stat.S_IMODE(st.st_mode) != 0o700:
        raise SafeError('unsafe_private_directory')
    return path


def private_read(path):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd, 'rb') as f:
        st = os.fstat(f.fileno())
        if not stat.S_ISREG(st.st_mode) or st.st_uid != os.getuid() or stat.S_IMODE(st.st_mode) != 0o600 or st.st_size > 2**20:
            raise SafeError('unsafe_private_file')
        return json.load(f)


def private_write(path, data):
    path = Path(path)
    if path.exists() or path.is_symlink():
        private_read(path)  # Refuse links, wrong owner/mode and unrecognized file formats.
    fd, tmp = tempfile.mkstemp(prefix='.tofu-', dir=path.parent)
    try:
        with os.fdopen(fd, 'w') as f:
            json.dump(data, f)
            f.flush()
            os.fsync(f.fileno())
        os.replace(tmp, path)
    finally:
        if os.path.exists(tmp):
            os.unlink(tmp)


class Runner:
    def run(self, args, *, missing=False):
        try:
            r = subprocess.run(args, stdin=subprocess.DEVNULL, capture_output=True, timeout=180)
            if len(r.stdout) + len(r.stderr) > 2**21:
                raise SafeError('administrative_response_too_large')
            if r.returncode:
                if missing and b'NOT_FOUND' in r.stderr:
                    return None
                raise SafeError('administrative_command_failed')
            return r.stdout
        except (OSError, subprocess.TimeoutExpired):
            raise SafeError('administrative_command_unavailable') from None

    def cloud(self, *args, missing=False):
        b = self.run(['gcloud', *args, '--quiet', '--format=json'], missing=missing)
        if b is None:
            return None
        try:
            return json.loads(b or b'{}')
        except ValueError:
            raise SafeError('invalid_administrative_response') from None


def http_json(url, token, data=None, method=None):
    # Fixed Google destinations only, never inherit proxy or operator credentials.
    u = urllib.parse.urlparse(url)
    if u.scheme != 'https' or u.hostname not in ('iamcredentials.googleapis.com', 'storage.googleapis.com', 'oauth2.googleapis.com'):
        raise SafeError('unexpected_google_destination')
    headers = {'Authorization': 'Bearer ' + token}
    if data is not None:
        headers['Content-Type'] = 'application/json'
    req = urllib.request.Request(url, data=None if data is None else json.dumps(data).encode(), headers=headers, method=method)
    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, *args):
            raise SafeError('unexpected_google_redirect')
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    try:
        with opener.open(req, timeout=30) as r:
            b = r.read(2**20 + 1)
            if len(b) > 2**20:
                raise SafeError('google_response_too_large')
            return r.status, b
    except urllib.error.HTTPError as e:
        return e.code, b''
    except (OSError, ValueError):
        raise SafeError('google_transport_failed') from None
