import asyncio
from datetime import datetime
import json
import os
from pathlib import Path
import sys
import unittest
from unittest.mock import patch
from zoneinfo import ZoneInfo

import grpc
import httpx

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from planner import demo_plan, plan, validate_plan, model_plan, NeedInput
from server import Agent, pb, rpc


class PlannerTests(unittest.IsolatedAsyncioTestCase):
    def setUp(self):
        self.tz = ZoneInfo("Asia/Shanghai")
        self.now = datetime(2026, 10, 25, 10, 0, tzinfo=self.tz)

    def test_chinese_time_and_reminder(self):
        result = demo_plan("明天下午三点交周报，提前十分钟提醒我", self.now, self.tz)
        self.assertEqual(result["title"], "交周报")
        self.assertEqual(result["due_at"], "2026-10-26T15:00:00+08:00")
        self.assertEqual(result["remind_before_minutes"], 10)
        self.assertTrue(result["has_reminder"])

    def test_relative_time(self):
        result = demo_plan("三十分钟后提醒我喝水", self.now, self.tz)
        self.assertEqual(result["due_at"], "2026-10-25T10:30:00+08:00")
        self.assertEqual(result["title"], "喝水")

    def test_notes_stay_notes(self):
        result = demo_plan("记一下：明天想去的书店", self.now, self.tz)
        self.assertEqual(result["action"], "save_note")
        self.assertEqual(result["content"], "明天想去的书店")

    def test_event_requires_duration(self):
        with self.assertRaises(NeedInput):
            demo_plan("明天下午3点开会", self.now, self.tz)
        result = demo_plan("明天下午3点开会，持续1小时", self.now, self.tz)
        self.assertEqual(result["ends_at"], "2026-10-26T16:00:00+08:00")

    async def test_missing_time_is_clarified(self):
        with patch.dict(os.environ, {"AI_MODE": "demo"}):
            result = await plan("提醒我交周报", self.now.isoformat(), self.tz.key)
        self.assertEqual(result["action"], "clarification")

    def test_past_and_unsupported_dates_are_not_guessed(self):
        for text in ("今天上午9点提醒我喝水", "下周一下午3点提醒我喝水"):
            with self.subTest(text=text), self.assertRaises(NeedInput):
                demo_plan(text, self.now, self.tz)

    def test_malicious_and_malformed_actions_are_rejected(self):
        for value in ({"action": "run_sql", "sql": "DROP TABLE tasks"}, {"action": "create_task", "title": "x", "user_id": "other"}, {"action": "create_task", "title": "x", "due_at": "2026-10-26T15:00:00"}, {"action": "create_task", "title": "x", "has_reminder": "false"}, {"action": "create_task", "title": "x", "remind_before_minutes": -1}):
            with self.subTest(value=value), self.assertRaises(ValueError):
                validate_plan(value)

    def test_dst_gap_and_ambiguity_are_clarified(self):
        tz = ZoneInfo("America/New_York")
        for text in ("2026-03-08 2:30提醒我喝水", "2026-11-01 1:30提醒我喝水"):
            with self.subTest(text=text), self.assertRaises(NeedInput):
                demo_plan(text, datetime(2026, 1, 1, tzinfo=tz), tz)

    async def test_model_adapter_uses_schema_and_validates_response(self):
        def handle(request):
            data = json.loads(request.content)
            self.assertEqual(data["response_format"]["json_schema"]["strict"], True)
            self.assertEqual(request.headers["authorization"], "Bearer fake-key")
            result = {"action": "create_task", "title": "model task"}
            return httpx.Response(200, json={"choices": [{"finish_reason": "stop", "message": {"content": json.dumps(result)}}]})
        with patch.dict(os.environ, {"AI_API_KEY": "fake-key", "AI_MODEL": "test-model", "AI_BASE_URL": "https://model.invalid/v1"}):
            result = await model_plan("test", self.now, self.tz, 5, transport=httpx.MockTransport(handle))
        self.assertEqual(result["title"], "model task")


class GRPCTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.server = grpc.aio.server()
        rpc.add_AgentServiceServicer_to_server(Agent("test-service-credential"), self.server)
        port = self.server.add_insecure_port("127.0.0.1:0")
        await self.server.start()
        self.channel = grpc.aio.insecure_channel("127.0.0.1:" + str(port))
        self.stub = rpc.AgentServiceStub(self.channel)

    async def asyncTearDown(self):
        await self.channel.close()
        await self.server.stop(0)

    async def test_grpc_auth_and_planning(self):
        request = pb.PlanRequest(job_id="test", text="明天下午3点交周报，提前10分钟提醒我", timezone="Asia/Shanghai", now="2026-10-25T10:00:00+08:00")
        with self.assertRaises(grpc.aio.AioRpcError) as result:
            await self.stub.Plan(request, timeout=2)
        self.assertEqual(result.exception.code(), grpc.StatusCode.UNAUTHENTICATED)
        with patch.dict(os.environ, {"AI_MODE": "demo"}):
            response = await self.stub.Plan(request, metadata=[("authorization", "Bearer test-service-credential")], timeout=2)
        self.assertEqual(response.WhichOneof("result"), "create_task")
        self.assertEqual(response.create_task.title, "交周报")


if __name__ == "__main__":
    unittest.main()
