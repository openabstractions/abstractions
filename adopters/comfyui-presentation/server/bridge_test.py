import asyncio
import unittest

from bridge import Bridge, UnresolvedCommand, parse_body


class BridgeTest(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.bridge = Bridge("x" * 32, "http://127.0.0.1:8188", wait_seconds=.1)

    @staticmethod
    def context(name="workflow-1", revision="7"):
        return {"instance": "page-1", "context": name,
                "revision": revision, "title": "Example workflow"}

    async def test_preview_round_trip_and_single_browser(self):
        session = await self.bridge.register()
        with self.assertRaises(RuntimeError):
            await self.bridge.register()
        submitted = asyncio.create_task(self.bridge.submit("preview", {"node_id": 70}))
        command = await self.bridge.next(session)
        self.assertEqual(command["kind"], "preview")
        await self.bridge.complete(session, command["id"], {"outcome": "proposed"})
        self.assertEqual(await submitted, {"outcome": "proposed"})

    async def test_wrong_origin_token_and_session_are_refused(self):
        with self.assertRaises(PermissionError):
            self.bridge.authorize("bad", self.bridge.origin)
        with self.assertRaises(PermissionError):
            self.bridge.authorize(self.bridge.token, "http://localhost:8188")
        session = await self.bridge.register()
        with self.assertRaises(PermissionError):
            await self.bridge.next(session + "x")

    async def test_disconnect_refuses_pending_without_effect(self):
        session = await self.bridge.register()
        submitted = asyncio.create_task(self.bridge.submit("read", {"operation": "op"}))
        await self.bridge.next(session)
        await self.bridge.unregister(session)
        with self.assertRaises(ConnectionError):
            await submitted

    async def test_one_in_flight_and_result_bound(self):
        session = await self.bridge.register()
        first = asyncio.create_task(self.bridge.submit("preview", {"node_id": 70}))
        command = await self.bridge.next(session)
        with self.assertRaises(RuntimeError):
            await self.bridge.submit("read", {"operation": "other"})
        with self.assertRaises(ValueError):
            await self.bridge.complete(session, command["id"], {"text": "x" * (16 * 1024)})
        await self.bridge.complete(session, command["id"], {"outcome": "proposed"})
        await first

    async def test_duplicate_result_is_rejected(self):
        session = await self.bridge.register()
        submitted = asyncio.create_task(self.bridge.submit("preview", {"node_id": 70}))
        command = await self.bridge.next(session)
        await self.bridge.complete(session, command["id"], {"outcome": "proposed"})
        with self.assertRaises(LookupError):
            await self.bridge.complete(session, command["id"], {"outcome": "proposed"})
        await submitted

    async def test_delivered_timeout_is_unresolved(self):
        session = await self.bridge.register()
        submitted = asyncio.create_task(self.bridge.submit("preview", {}, timeout=.01))
        await asyncio.sleep(0)
        await self.bridge.next(session)
        with self.assertRaises(UnresolvedCommand):
            await submitted

    async def test_oa_context_mapping_is_server_owned_and_reset_replaces_presence(self):
        bridge = Bridge("x" * 32, "http://127.0.0.1:8188", require_context=True)
        with self.assertRaises(ValueError):
            await bridge.register()
        session = await bridge.register(self.context())
        bridge.set_application_instance("oa-instance-1")
        version, context = bridge.presence_snapshot()
        self.assertEqual(context["context"], "workflow-1")

        submitted = asyncio.create_task(bridge.submit("context", {}))
        command = await bridge.next(session, self.context())
        await bridge.complete(session, command["id"], self.context())
        result = await submitted
        self.assertEqual(result["application_instance"], "oa-instance-1")
        self.assertEqual(result["instance"], "page-1")

        await bridge.update_context(session, self.context("workflow-2", "1"))
        changed_version, changed = bridge.presence_snapshot()
        self.assertGreater(changed_version, version)
        self.assertEqual((changed["context"], changed["revision"]), ("workflow-2", "1"))

    async def test_oa_preview_checks_and_strips_application_instance(self):
        bridge = Bridge("x" * 32, "http://127.0.0.1:8188", require_context=True)
        session = await bridge.register(self.context())
        with self.assertRaisesRegex(PermissionError, "presence is unavailable"):
            await bridge.submit("preview", {
                "application_instance": "oa-instance-1", "instance": "page-1",
                "context": "workflow-1", "revision": "7", "node_id": 70})
        bridge.set_application_instance("oa-instance-1")
        with self.assertRaises(PermissionError):
            await bridge.submit("preview", {"application_instance": "wrong"})
        binding = {"instance": "page-1", "context": "workflow-1", "revision": "7"}
        with self.assertRaises(PermissionError):
            await bridge.submit("preview", {
                "application_instance": "oa-instance-1", **binding, "revision": "old"})
        submitted = asyncio.create_task(bridge.submit("preview", {
            "application_instance": "oa-instance-1", **binding, "node_id": 70}))
        command = await bridge.next(session, self.context())
        self.assertNotIn("application_instance", command["arguments"])
        await bridge.complete(session, command["id"], {"outcome": "stale"})
        self.assertEqual(await submitted, {"outcome": "stale"})

        read = asyncio.create_task(bridge.submit("read", {
            "application_instance": "oa-instance-1", **binding, "operation": "op-1"}))
        command = await bridge.next(session, self.context())
        self.assertNotIn("application_instance", command["arguments"])
        await bridge.complete(session, command["id"], {"outcome": "unknown"})
        self.assertEqual(await read, {"outcome": "unknown"})

    async def test_browser_page_instance_cannot_change_within_session(self):
        bridge = Bridge("x" * 32, "http://127.0.0.1:8188", require_context=True)
        session = await bridge.register(self.context())
        changed = self.context()
        changed["instance"] = "page-2"
        with self.assertRaises(PermissionError):
            await bridge.update_context(session, changed)

    def test_json_body_requires_object(self):
        for raw in (b"{", b"null", b"[]", b"1"):
            with self.assertRaises(ValueError):
                parse_body(raw)
        self.assertEqual(parse_body(b'{"kind":"read"}'), {"kind": "read"})


if __name__ == "__main__":
    unittest.main()
