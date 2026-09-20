"""Renew one ComfyUI process presence through the native OA facade."""

import asyncio
import os
from pathlib import Path


APPLICATION = "comfyui"
LEASE_MS = 10_000
RENEW_SECONDS = 5


class PresenceRefused(RuntimeError):
    pass


def _windows_sid():
    import ctypes
    from ctypes import wintypes

    adv = ctypes.WinDLL("advapi32", use_last_error=True)
    kernel = ctypes.WinDLL("kernel32", use_last_error=True)
    adv.OpenProcessToken.argtypes = [
        wintypes.HANDLE, wintypes.DWORD, ctypes.POINTER(wintypes.HANDLE)]
    adv.OpenProcessToken.restype = wintypes.BOOL
    adv.GetTokenInformation.argtypes = [
        wintypes.HANDLE, ctypes.c_int, ctypes.c_void_p, wintypes.DWORD,
        ctypes.POINTER(wintypes.DWORD)]
    adv.GetTokenInformation.restype = wintypes.BOOL
    adv.ConvertSidToStringSidW.argtypes = [
        ctypes.c_void_p, ctypes.POINTER(ctypes.c_wchar_p)]
    adv.ConvertSidToStringSidW.restype = wintypes.BOOL
    kernel.GetCurrentProcess.restype = wintypes.HANDLE
    kernel.CloseHandle.argtypes = [wintypes.HANDLE]
    kernel.LocalFree.argtypes = [ctypes.c_void_p]

    token = wintypes.HANDLE()
    if not adv.OpenProcessToken(kernel.GetCurrentProcess(), 0x0008, ctypes.byref(token)):
        raise ctypes.WinError()
    try:
        needed = wintypes.DWORD()
        adv.GetTokenInformation(token, 1, None, 0, ctypes.byref(needed))
        if not needed.value:
            raise ctypes.WinError()
        data = ctypes.create_string_buffer(needed.value)
        if not adv.GetTokenInformation(
                token, 1, data, needed.value, ctypes.byref(needed)):
            raise ctypes.WinError()
        sid = ctypes.cast(data, ctypes.POINTER(ctypes.c_void_p))[0]
        text = ctypes.c_wchar_p()
        if not adv.ConvertSidToStringSidW(sid, ctypes.byref(text)):
            raise ctypes.WinError()
        try:
            if not text.value:
                raise RuntimeError("current Windows token has no user SID")
            return text.value
        finally:
            kernel.LocalFree(ctypes.cast(text, ctypes.c_void_p))
    finally:
        kernel.CloseHandle(token)


def _current_principal():
    if os.name == "nt":
        return 1, _windows_sid()
    if hasattr(os, "getuid"):
        return 2, str(os.getuid())
    raise RuntimeError("cannot establish the current OA runtime principal")


def runtime_options(environment, expectation_type, principal=None):
    endpoint = environment.get("ABSTRACTION_RUNTIME_ENDPOINT", "")
    program = environment.get("ABSTRACTION_RUNTIME_PROGRAM", "")
    if bool(endpoint) != bool(program):
        raise RuntimeError(
            "ABSTRACTION_RUNTIME_ENDPOINT and ABSTRACTION_RUNTIME_PROGRAM must be set together")
    if not endpoint:
        return {}
    if "\0" in endpoint or "\0" in program:
        raise RuntimeError("explicit OA runtime configuration is invalid")
    path = Path(program)
    if not path.is_absolute() or not path.is_file():
        raise RuntimeError("explicit OA runtime program must be an existing absolute file")
    kind, account = principal if principal is not None else _current_principal()
    return {
        "endpoint": endpoint,
        "server": expectation_type(kind, account, str(path.resolve(strict=True))),
    }


class OAPresence:
    def __init__(self, applications, bridge, wire, *, application=APPLICATION,
                 lease_ms=LEASE_MS, renew_seconds=RENEW_SECONDS):
        if application != APPLICATION:
            raise ValueError("the Comfy presentation profile requires application comfyui")
        if not 1_000 <= lease_ms <= 60_000 or not 0 < renew_seconds < lease_ms / 1000:
            raise ValueError("invalid presence lease or renewal interval")
        self.applications = applications
        self.bridge = bridge
        self.wire = wire
        self.application = application
        self.lease_ms = lease_ms
        self.renew_seconds = renew_seconds
        self.instance = ""

    async def run(self, prompt_server):
        await self._wait_server_ready(prompt_server)
        try:
            while True:
                version, context = self.bridge.presence_snapshot()
                await self._announce(context)
                try:
                    await self.bridge.wait_presence_change(version, self.renew_seconds)
                except asyncio.TimeoutError:
                    pass
        finally:
            instance = self.instance
            self.instance = ""
            self.bridge.set_application_instance(None)
            if instance:
                try:
                    await asyncio.wait_for(asyncio.to_thread(
                        self._withdraw, self.application, instance), 2)
                except (Exception, asyncio.CancelledError):
                    # Lease expiry is the crash/cancellation fallback.
                    pass

    async def _announce(self, context):
        presence = self._presence(context)
        change = await asyncio.to_thread(self._announce_sync, presence)
        if self._outcome(change) == "stale" and self.instance:
            self.instance = ""
            self.bridge.set_application_instance(None)
            presence = self._presence(context)
            change = await asyncio.to_thread(self._announce_sync, presence)
        if self._outcome(change) != "applied" or not change.instance:
            self.bridge.set_application_instance(None)
            raise PresenceRefused(
                f"OA application presence refused: {self._outcome(change) or 'unavailable'}")
        self.instance = change.instance
        self.bridge.set_application_instance(change.instance)

    def _presence(self, context):
        contexts = []
        if context is not None:
            contexts.append(self.wire.ApplicationContext(
                name=context["context"], title=context["title"], revision=context["revision"]))
        return self.wire.ApplicationPresence(
            application=self.application,
            instance=self.instance,
            interfaces=[self.wire.ApplicationInterface(
                name="presentation", protocol="oa-local", contract="comfy.presentation@1")],
            contexts=contexts,
            lease_ms=self.lease_ms)

    @staticmethod
    def _outcome(change):
        value = getattr(change, "outcome", "")
        return getattr(value, "value", value)

    def _client(self):
        return self.applications() if callable(self.applications) else self.applications

    def _announce_sync(self, presence):
        return self._client().announce(presence)

    def _withdraw(self, application, instance):
        return self._client().withdraw(application, instance)

    @staticmethod
    async def _wait_server_ready(prompt_server):
        while not (getattr(prompt_server, "address", None) and
                   getattr(prompt_server, "port", None)):
            await asyncio.sleep(.05)


def start_presence(prompt_server, bridge):
    """Resolve the native service now and schedule announcement after listen."""
    import abstraction.facade as wire
    from abstraction.facade.client import Machine
    from abstraction.ipc import ServerExpectation

    options = runtime_options(os.environ, ServerExpectation)

    def applications():
        return Machine(timeout=2, **options).resolve_applications(scope="local")

    applications()  # Fail this opt-in integration before scheduling any claim.
    manager = OAPresence(applications, bridge, wire)
    task = prompt_server.loop.create_task(manager.run(prompt_server))
    return manager, task
