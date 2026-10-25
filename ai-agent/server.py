import asyncio
import hmac
import logging
import os
from pathlib import Path
import signal
import sys

import grpc

sys.path.insert(0, str(Path(__file__).parent / "generated"))
import agent_pb2 as pb
import agent_pb2_grpc as rpc
from planner import plan


def load_local_env():
    path = Path(__file__).resolve().parent.parent / ".env"
    if path.exists():
        for line in path.read_text().splitlines():
            line = line.strip()
            if line and not line.startswith("#") and "=" in line:
                key, value = line.split("=", 1)
                os.environ.setdefault(key.strip(), value.strip().strip("\"'"))


def to_response(result):
    action = result["action"]
    if action == "create_task":
        return pb.PlanResponse(create_task=pb.CreateTask(title=result["title"], description=result.get("description", ""), due_at=result.get("due_at", ""), has_reminder=result.get("has_reminder", False), remind_before_minutes=result.get("remind_before_minutes", 0)))
    if action == "save_note":
        return pb.PlanResponse(save_note=pb.SaveNote(title=result["title"], content=result["content"]))
    if action == "create_event":
        return pb.PlanResponse(create_event=pb.CreateEvent(title=result["title"], starts_at=result["starts_at"], ends_at=result["ends_at"], has_reminder=result.get("has_reminder", False), remind_before_minutes=result.get("remind_before_minutes", 0)))
    if action == "query_tasks":
        return pb.PlanResponse(query_tasks=pb.QueryTasks(today_only=result.get("today_only", False)))
    if action == "summarize_day":
        return pb.PlanResponse(summarize_day=pb.SummarizeDay())
    return pb.PlanResponse(clarification=pb.Clarification(question=result["question"]))


class Agent(rpc.AgentServiceServicer):
    def __init__(self, token):
        self.token = token
        self.slots = asyncio.Semaphore(4)

    async def Plan(self, request, context):
        authorization = dict(context.invocation_metadata()).get("authorization", "")
        if not hmac.compare_digest(authorization, "Bearer " + self.token):
            await context.abort(grpc.StatusCode.UNAUTHENTICATED, "invalid service credential")
        if not request.text.strip() or len(request.text) > 8000:
            await context.abort(grpc.StatusCode.INVALID_ARGUMENT, "text must contain 1-8000 characters")
        async def run():
            async with self.slots:
                remaining = min(32, context.time_remaining() or 32)
                result = await plan(request.text, request.now, request.timezone, timeout=max(0.1, remaining - 0.2))
                return to_response(result)
        try:
            return await asyncio.wait_for(run(), timeout=min(35, context.time_remaining() or 35))
        except asyncio.TimeoutError:
            await context.abort(grpc.StatusCode.DEADLINE_EXCEEDED, "planning deadline exceeded")
        except (ValueError, KeyError):
            await context.abort(grpc.StatusCode.INVALID_ARGUMENT, "invalid action or time arguments")
        except asyncio.CancelledError:
            raise
        except Exception:
            # Avoid logging prompts, API keys, or request URLs containing credentials.
            logging.error("AI planning failed; job_id=%s", request.job_id)
            await context.abort(grpc.StatusCode.UNAVAILABLE, "planning provider unavailable")


async def serve():
    load_local_env()
    token = os.getenv("AGENT_TOKEN", "")
    if len(token) < 16:
        raise RuntimeError("AGENT_TOKEN must contain at least 16 characters")
    if os.getenv("AI_MODE", "demo") == "openai" and (not os.getenv("AI_API_KEY") or not os.getenv("AI_MODEL")):
        raise RuntimeError("openai mode requires AI_API_KEY and AI_MODEL")
    server = grpc.aio.server(options=[("grpc.max_receive_message_length", 65536), ("grpc.max_send_message_length", 65536)], maximum_concurrent_rpcs=32)
    rpc.add_AgentServiceServicer_to_server(Agent(token), server)
    address = os.getenv("AGENT_BIND", "127.0.0.1:50051")
    if server.add_insecure_port(address) == 0:
        raise RuntimeError("could not bind agent listener")
    await server.start()
    logging.info("AI service listening on %s; mode=%s", address, os.getenv("AI_MODE", "demo"))
    event = asyncio.Event()
    for sig in (signal.SIGINT, signal.SIGTERM):
        asyncio.get_running_loop().add_signal_handler(sig, event.set)
    await event.wait()
    await server.stop(5)


if __name__ == "__main__":
    logging.basicConfig(level=logging.INFO, format="%(levelname)s %(message)s")
    asyncio.run(serve())
