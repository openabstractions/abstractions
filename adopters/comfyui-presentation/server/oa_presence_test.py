import asyncio
import dataclasses
from pathlib import Path
import types
import unittest

from oa_presence import OAPresence, PresenceRefused, runtime_options


@dataclasses.dataclass
class Interface:
    name: str
    protocol: str
    contract: str


@dataclasses.dataclass
class Context:
    name: str
    title: str
    revision: str


@dataclasses.dataclass
class Presence:
    application: str
    instance: str
    interfaces: list
    contexts: list
    lease_ms: int


WIRE = types.SimpleNamespace(
    ApplicationInterface=Interface,
    ApplicationContext=Context,
    ApplicationPresence=Presence,
)


class FakeApplications:
    def __init__(self, outcomes=("applied",)):
        self.outcomes = list(outcomes)
        self.announcements = []
        self.withdrawals = []

    def announce(self, presence):
        self.announcements.append(presence)
        outcome = self.outcomes.pop(0) if self.outcomes else "applied"
        instance = "oa-instance-2" if len(self.announcements) > 1 and not presence.instance else presence.instance or "oa-instance-1"
        return types.SimpleNamespace(outcome=outcome, instance=instance if outcome == "applied" else "")

    def withdraw(self, application, instance):
        self.withdrawals.append((application, instance))


class FakeBridge:
    def __init__(self):
        self.version = 0
        self.context = None
        self.application_instance = None
        self.changed = asyncio.Event()

    def presence_snapshot(self):
        return self.version, self.context

    def set_application_instance(self, instance):
        self.application_instance = instance

    async def wait_presence_change(self, version, timeout):
        if version != self.version:
            return
        self.changed.clear()
        if version != self.version:
            return
        await asyncio.wait_for(self.changed.wait(), timeout)

    def replace_context(self, context):
        self.context = context
        self.version += 1
        self.changed.set()


async def wait_until(predicate, seconds=.5):
    deadline = asyncio.get_running_loop().time() + seconds
    while not predicate():
        if asyncio.get_running_loop().time() >= deadline:
            raise AssertionError("condition did not become true")
        await asyncio.sleep(.005)


class PresenceTest(unittest.IsolatedAsyncioTestCase):
    async def test_waits_for_server_then_maps_browser_context_and_withdraws(self):
        applications = FakeApplications()
        bridge = FakeBridge()
        server = types.SimpleNamespace()
        manager = OAPresence(applications, bridge, WIRE, renew_seconds=.2)
        task = asyncio.create_task(manager.run(server))
        await asyncio.sleep(.02)
        self.assertEqual(applications.announcements, [])
        server.address, server.port = "127.0.0.1", 8188
        await wait_until(lambda: len(applications.announcements) == 1 and
                         bridge.application_instance == "oa-instance-1")
        self.assertEqual(applications.announcements[0].contexts, [])
        self.assertEqual(bridge.application_instance, "oa-instance-1")

        bridge.replace_context({
            "instance": "page-1", "context": "workflow-1",
            "revision": "7", "title": "Example workflow",
        })
        await wait_until(lambda: len(applications.announcements) == 2 and
                         bridge.application_instance == "oa-instance-1")
        renewed = applications.announcements[1]
        self.assertEqual(renewed.instance, "oa-instance-1")
        self.assertEqual(renewed.interfaces, [Interface(
            "presentation", "oa-local", "comfy.presentation@1")])
        self.assertEqual(renewed.contexts, [Context(
            "workflow-1", "Example workflow", "7")])

        task.cancel()
        with self.assertRaises(asyncio.CancelledError):
            await task
        self.assertEqual(applications.withdrawals, [("comfyui", "oa-instance-1")])
        self.assertIsNone(bridge.application_instance)

    async def test_stale_renewal_gets_one_new_service_handle(self):
        applications = FakeApplications(("applied", "stale", "applied"))
        resolutions = []
        def resolve():
            resolutions.append(True)
            return applications
        bridge = FakeBridge()
        server = types.SimpleNamespace(address="127.0.0.1", port=8188)
        manager = OAPresence(resolve, bridge, WIRE, renew_seconds=.02)
        task = asyncio.create_task(manager.run(server))
        await wait_until(lambda: len(applications.announcements) == 3 and
                         bridge.application_instance == "oa-instance-2")
        self.assertEqual(applications.announcements[1].instance, "oa-instance-1")
        self.assertEqual(applications.announcements[2].instance, "")
        self.assertEqual(bridge.application_instance, "oa-instance-2")
        self.assertGreaterEqual(len(resolutions), 3)
        task.cancel()
        with self.assertRaises(asyncio.CancelledError):
            await task

    async def test_refusal_publishes_no_mapping(self):
        applications = FakeApplications(("forbidden",))
        bridge = FakeBridge()
        server = types.SimpleNamespace(address="127.0.0.1", port=8188)
        manager = OAPresence(applications, bridge, WIRE, renew_seconds=.2)
        with self.assertRaises(PresenceRefused):
            await manager.run(server)
        self.assertIsNone(bridge.application_instance)
        self.assertEqual(applications.withdrawals, [])


class RuntimeOptionsTest(unittest.TestCase):
    def test_explicit_runtime_requires_paired_endpoint_and_verified_program(self):
        with self.assertRaisesRegex(RuntimeError, "must be set together"):
            runtime_options({"ABSTRACTION_RUNTIME_ENDPOINT": "pipe"}, lambda *parts: parts)
        with self.assertRaisesRegex(RuntimeError, "must be set together"):
            runtime_options({"ABSTRACTION_RUNTIME_PROGRAM": __file__}, lambda *parts: parts)
        self.assertEqual(runtime_options({}, lambda *parts: parts), {})

        options = runtime_options({
            "ABSTRACTION_RUNTIME_ENDPOINT": "pipe",
            "ABSTRACTION_RUNTIME_PROGRAM": __file__,
        }, lambda *parts: parts, principal=(1, "S-1-5-21-fixture"))
        self.assertEqual(options["endpoint"], "pipe")
        self.assertEqual(options["server"], (
            1, "S-1-5-21-fixture", str(Path(__file__).resolve())))


if __name__ == "__main__":
    unittest.main()
