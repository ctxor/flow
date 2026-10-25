"""Exercise a running local stack with a disposable account and real Go/Python gRPC."""
import http.cookiejar
import json
from pathlib import Path
import secrets
import sys
import time
import urllib.error
import urllib.request
import uuid

base = sys.argv[1] if len(sys.argv) > 1 else "http://127.0.0.1:8080"
opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))


def call(method, path, value=None, key=None):
    data = json.dumps(value).encode() if value is not None else None
    headers = {"Content-Type": "application/json"}
    if key:
        headers["Idempotency-Key"] = key
    req = urllib.request.Request(base + path, data=data, method=method, headers=headers)
    with opener.open(req, timeout=5) as response:
        return json.load(response)


email = "smoke-" + uuid.uuid4().hex[:8] + "@example.test"
password = "Local-test-" + secrets.token_hex(12)
call("POST", "/v1/auth/register", {"email": email, "password": password, "timezone": "Asia/Shanghai"})
workspace = call("GET", "/v1/workspaces")[0]["id"]
key = uuid.uuid4().hex
payload = {"workspace_id": workspace, "text": "8秒后提醒我完成本地验证"}
job = call("POST", "/v1/assistant/messages", payload, key)
assert call("POST", "/v1/assistant/messages", payload, key)["id"] == job["id"]
deadline = time.monotonic() + 45
while time.monotonic() < deadline:
    result = call("GET", "/v1/agent-jobs/" + job["id"])
    if result["status"] not in ("queued", "running"):
        break
    time.sleep(0.3)
assert result["status"] == "succeeded", result
tasks = call("GET", "/v1/tasks?workspace_id=" + workspace)
assert len(tasks) == 1 and tasks[0]["title"] == "完成本地验证", tasks
deadline = time.monotonic() + 20
notifications = []
while time.monotonic() < deadline:
    notifications = call("GET", "/v1/notifications")
    if any(n["kind"] == "reminder" for n in notifications):
        break
    time.sleep(0.3)
assert any(n["kind"] == "reminder" for n in notifications), notifications
note = call("POST", "/v1/notes", {"workspace_id": workspace, "title": "本地验证备忘", "content": "Go、Python gRPC、PostgreSQL 与 Redis 已联通。", "visibility": "private"})
assert note["created_at"] != "0001-01-01T00:00:00Z", note
report = {"ok": True, "job_status": result["status"], "task_count": len(tasks), "reminder_received": True, "base_url": base}
work = Path(__file__).resolve().parent.parent / "work"
work.mkdir(exist_ok=True)
(work / "smoke-report.json").write_text(json.dumps(report, indent=2))
account = work / "smoke-account.json"
account.write_text(json.dumps({"email": email, "password": password}))
account.chmod(0o600)
print(json.dumps(report, ensure_ascii=False))
