"""Run the integration suite with a separate Go test deadline for each group."""
from pathlib import Path
import shlex
import subprocess

from verify import GHA_RUNTIME_PARTITIONS


def main():
    directory = Path(__file__).resolve().parents[1] / 'gha/integration'
    failed = False
    for flag, pattern in GHA_RUNTIME_PARTITIONS:
        command = ['go', 'test', '-count=1', '-v', '-timeout=10m', flag, pattern, './...']
        print(shlex.join(command), flush=True)
        result = subprocess.run(command, cwd=directory)
        if result.returncode:
            failed = True
    return int(failed)


if __name__ == '__main__':
    raise SystemExit(main())
