"""Foreground development launcher. Ctrl+C stops only children started here."""
import argparse
import os
from pathlib import Path
import shutil
import signal
import socket
import subprocess
import sys
import threading
import time
import urllib.request
from urllib.parse import urlparse

ROOT = Path(__file__).resolve().parent.parent
WORK = ROOT / "work"
LOGS = WORK / "dev-logs"
STOP = threading.Event()
CHILDREN = []


def say(message):
    print(message, flush=True)


def environment():
    path = Path(os.getenv("ENV_FILE", ROOT / ".env"))
    if not path.is_absolute():
        path = ROOT / path
    if not path.exists() and not os.getenv("ENV_FILE"):
        subprocess.run([sys.executable, str(ROOT / "scripts/init_env.py")], check=True)
    values = {}
    for line in path.read_text().splitlines():
        line = line.strip()
        if line and not line.startswith("#") and "=" in line:
            key, value = line.split("=", 1)
            values[key.strip()] = value.strip().strip("\"'")
    env = {**values, **os.environ}
    if len(env.get("AGENT_TOKEN", "")) < 16:
        raise RuntimeError("AGENT_TOKEN 无效，请检查项目根目录的 .env 或同名环境变量。")
    env["ENV_FILE"] = str(path)
    env.setdefault("GOCACHE", str(WORK / "cache"))
    env.setdefault("GOMODCACHE", str(WORK / "gomodcache"))
    env.setdefault("GOPATH", str(WORK / "gopath"))
    # Keep local dependency caches out of `go test ./...` package discovery.
    WORK.mkdir(exist_ok=True)
    marker = WORK / "go.mod"
    if not marker.exists():
        marker.write_text("module local-build-cache\n\ngo 1.27.3\n")
    return env


def address(value, default_port):
    parsed = urlparse(value if "://" in value else "//" + value)
    return parsed.hostname or "127.0.0.1", parsed.port or default_port


def reachable(host, port):
    try:
        with socket.create_connection((host, port), timeout=0.5):
            return True
    except OSError:
        return False


def ensure_children_alive():
    for name, child, path in CHILDREN:
        if child.poll() is not None:
            tail = "\n".join(path.read_text(errors="replace").splitlines()[-12:])
            raise RuntimeError(f"{name} 提前退出，日志：{path}\n{tail}")


def wait_port(name, host, port, seconds=30):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        if STOP.is_set():
            raise KeyboardInterrupt
        ensure_children_alive()
        if reachable(host, port):
            return
        STOP.wait(0.2)
    raise RuntimeError(f"{name} 未在 {host}:{port} 就绪，请查看 {LOGS}")


def start(name, command, env):
    path = LOGS / (name + ".log")
    with path.open("ab") as output:
        child = subprocess.Popen(command, cwd=ROOT, env=env, stdout=output,
                                 stderr=subprocess.STDOUT, start_new_session=True)
    CHILDREN.append((name, child, path))
    say(f"已启动 {name}，日志：{path}")


def prepare(env):
    host, port = address(env.get("DATABASE_URL", "postgres://127.0.0.1:55432/assistant"), 5432)
    if not reachable(host, port):
        if host not in ("127.0.0.1", "localhost") or port != 55432:
            raise RuntimeError("指定数据库不可达，请先启动 DATABASE_URL 对应的数据库。")
        if not shutil.which("docker"):
            raise RuntimeError("需要先启动 PostgreSQL，或安装并运行 Docker Desktop。")
        legacy = subprocess.run(["docker", "inspect", "flow-assistant-local-postgres"],
                                cwd=ROOT, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        if legacy.returncode == 0:
            subprocess.run(["docker", "start", "flow-assistant-local-postgres"], cwd=ROOT, env=env, check=True, timeout=30)
        else:
            subprocess.run(["docker", "compose", "up", "-d", "--wait", "postgres"], cwd=ROOT, env=env, check=True, timeout=120)
        wait_port("PostgreSQL", host, port)
    say(f"PostgreSQL 已就绪：{host}:{port}（复用现有数据）")

    redis_url = env.get("REDIS_URL", "redis://127.0.0.1:56379/0")
    host, port = address(redis_url, 6379)
    if not reachable(host, port):
        local = ROOT / "work/redis-7.4.6/src/redis-server"
        binary = shutil.which("redis-server") or (str(local) if local.is_file() and os.access(local, os.X_OK) else None)
        if binary and host in ("127.0.0.1", "localhost") and not urlparse(redis_url).password:
            data = WORK / "redis-data"
            data.mkdir(exist_ok=True)
            start("redis", [binary, "--bind", "127.0.0.1", "--port", str(port),
                            "--appendonly", "yes", "--appendfsync", "everysec", "--dir", str(data)], env)
        elif host in ("127.0.0.1", "localhost") and port == 56379 and not urlparse(redis_url).password:
            subprocess.run(["docker", "compose", "up", "-d", "--wait", "redis"], cwd=ROOT, env=env, check=True, timeout=120)
        else:
            raise RuntimeError("指定 Redis 不可达，请先启动 REDIS_URL 对应的服务。")
        wait_port("Redis", host, port)
    say(f"Redis 已就绪：{host}:{port}")

    host, port = address(env.get("AGENT_ADDR", "127.0.0.1:50051"), 50051)
    if not reachable(host, port):
        if host not in ("127.0.0.1", "localhost"):
            raise RuntimeError("指定 AI Agent 不可达，请先启动远程 Agent。")
        python = ROOT / "ai-agent/.venv/bin/python"
        if not python.is_file():
            raise RuntimeError("Python 依赖尚未安装，请先在项目根目录运行 make deps。")
        agent_env = {**env, "AGENT_BIND": f"{host}:{port}"}
        start("agent", [str(python), str(ROOT / "ai-agent/server.py")], agent_env)
        wait_port("Python Agent", host, port)
    say(f"Python Agent 已就绪：{host}:{port}")


def run_apps(env):
    host, port = address(env.get("HTTP_ADDR", "127.0.0.1:8080"), 8080)
    host = "127.0.0.1" if host in ("0.0.0.0", "::") else host
    if reachable(host, port):
        raise RuntimeError(f"API 端口 {port} 已被占用，请先停止现有 API；F5 调试时不要同时运行 make dev。")
    binaries = WORK / "bin"
    binaries.mkdir(exist_ok=True)
    for name in ("api", "worker"):
        target = binaries / ("dev-" + name)
        say(f"正在编译 {name}…")
        subprocess.run(["go", "build", "-o", str(target), "./cmd/" + name], cwd=ROOT, env=env, check=True)
    start("worker", [str(binaries / "dev-worker")], env)
    start("api", [str(binaries / "dev-api")], env)
    wait_port("HTTP API", host, port)
    with urllib.request.urlopen(f"http://{host}:{port}/readyz", timeout=3) as response:
        if response.status != 200:
            raise RuntimeError("API 依赖尚未就绪")
    say(f"应用地址：http://{host}:{port}")


def cleanup():
    for _, child, _ in reversed(CHILDREN):
        if child.poll() is None:
            child.terminate()
    for _, child, _ in reversed(CHILDREN):
        try:
            child.wait(timeout=10)
        except subprocess.TimeoutExpired:
            child.kill()
            child.wait()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("mode", choices=("run", "deps"), nargs="?", default="run")
    args = parser.parse_args()
    for sig in (signal.SIGINT, signal.SIGTERM):
        signal.signal(sig, lambda *_: STOP.set())
    LOGS.mkdir(parents=True, exist_ok=True)
    say("FLOW_START: 正在准备本地开发环境")
    try:
        env = environment()
        prepare(env)
        if args.mode == "run":
            run_apps(env)
        say("FLOW_READY: 依赖已就绪，可开始调试；Ctrl+C 停止本次启动的进程。")
        while not STOP.wait(0.5):
            ensure_children_alive()
        return 0
    except KeyboardInterrupt:
        return 0
    except Exception as exc:
        say(f"FLOW_ERROR: {exc}")
        return 1
    finally:
        cleanup()


if __name__ == "__main__":
    raise SystemExit(main())
