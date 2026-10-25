"""Run isolated-schema tests against the configured development database."""
import os
from pathlib import Path
import subprocess

root = Path(__file__).resolve().parent.parent
values = {}
for line in (root / ".env").read_text().splitlines():
    line = line.strip()
    if line and not line.startswith("#") and "=" in line:
        key, value = line.split("=", 1)
        values[key.strip()] = value.strip().strip("\"'")
env = dict(os.environ)
env["TEST_DATABASE_URL"] = env.get("TEST_DATABASE_URL") or values["DATABASE_URL"]
raise SystemExit(subprocess.call(["go", "test", "-race", "./internal/...", "-count=1"], cwd=root, env=env))
