"""Bounded, authenticated relay between one ComfyUI browser and a local client."""

import asyncio
import json
import secrets

MAX_BODY = 16 * 1024


class UnresolvedCommand(RuntimeError):
    """A browser received the command but its result missed the caller deadline."""


def parse_body(raw):
    try:
        value = json.loads(raw or b"{}")
    except (json.JSONDecodeError, UnicodeDecodeError) as exc:
        raise ValueError("malformed JSON object") from exc
    if not isinstance(value, dict):
        raise ValueError("JSON body must be an object")
    return value


class Bridge:
    def __init__(self, token, origin, wait_seconds=15, lease_seconds=30, require_context=False):
        if not token or len(token) < 32:
            raise ValueError("OA_COMFY_BRIDGE_TOKEN must contain at least 32 characters")
        if not origin:
            raise ValueError("OA_COMFY_BRIDGE_ORIGIN is required")
        self.token = token
        self.origin = origin.rstrip("/")
        self.wait_seconds = wait_seconds
        self.lease_seconds = lease_seconds
        self.require_context = require_context
        self.session = None
        self.session_deadline = None
        self.browser_context = None
        self.application_instance = None
        self.presence_version = 0
        self._presence_event = asyncio.Event()
        self.pending = None
        self._lock = asyncio.Lock()

    def authorize(self, token, origin=None):
        if not secrets.compare_digest(token or "", self.token):
            raise PermissionError("invalid bridge credential")
        if origin is not None and origin.rstrip("/") != self.origin:
            raise PermissionError("unexpected browser origin")

    async def register(self, context=None):
        async with self._lock:
            self._expire_session()
            if self.session is not None:
                raise RuntimeError("a browser is already connected")
            if self.require_context and context is None:
                raise ValueError("browser context is required")
            checked = self._check_context(context) if context is not None else None
            self.session = secrets.token_urlsafe(24)
            self.session_deadline = asyncio.get_running_loop().time() + self.lease_seconds
            self._replace_context(checked)
            return self.session

    async def update_context(self, session, context):
        async with self._lock:
            self._require_session(session)
            checked = self._check_context(context)
            if self.browser_context and checked["instance"] != self.browser_context["instance"]:
                raise PermissionError("browser page instance cannot change within a session")
            self._replace_context(checked)

    async def unregister(self, session):
        async with self._lock:
            self._require_session(session)
            self.session = None
            self.session_deadline = None
            self._replace_context(None)
            if self.pending:
                self.pending["future"].set_exception(ConnectionError("browser disconnected"))
                self.pending = None

    async def submit(self, kind, arguments, timeout=20):
        if kind not in ("context", "preview", "read"):
            raise ValueError("unsupported command")
        encoded = json.dumps(arguments, separators=(",", ":")).encode()
        if len(encoded) > MAX_BODY:
            raise ValueError("command exceeds bridge limit")
        async with self._lock:
            self._expire_session()
            if self.session is None:
                raise ConnectionError("no authorized browser connected")
            if (kind in ("preview", "read") and self.require_context
                    and self.application_instance is None):
                raise PermissionError("OA application presence is unavailable")
            if kind in ("preview", "read") and self.application_instance is not None:
                if arguments.get("application_instance") != self.application_instance:
                    raise PermissionError("application instance binding mismatch")
                if self.browser_context is None or any(
                        arguments.get(name) != self.browser_context[name]
                        for name in ("instance", "context", "revision")):
                    raise PermissionError("browser context binding mismatch")
                arguments = dict(arguments)
                arguments.pop("application_instance", None)
            if self.pending is not None:
                raise RuntimeError("bridge already has an in-flight command")
            future = asyncio.get_running_loop().create_future()
            self.pending = {
                "id": secrets.token_urlsafe(18), "kind": kind,
                "arguments": arguments, "delivered": False, "future": future,
            }
        try:
            return await asyncio.wait_for(asyncio.shield(future), timeout)
        except asyncio.TimeoutError as exc:
            if self.pending and self.pending["future"] is future and self.pending["delivered"]:
                raise UnresolvedCommand("delivered command expired; result is unresolved") from exc
            raise
        finally:
            async with self._lock:
                if self.pending and self.pending["future"] is future:
                    self.pending = None

    async def next(self, session, context=None):
        deadline = asyncio.get_running_loop().time() + self.wait_seconds
        while True:
            async with self._lock:
                self._require_session(session)
                if context is not None:
                    checked = self._check_context(context)
                    if self.browser_context and checked["instance"] != self.browser_context["instance"]:
                        raise PermissionError("browser page instance cannot change within a session")
                    self._replace_context(checked)
                self.session_deadline = asyncio.get_running_loop().time() + self.lease_seconds
                if self.pending and not self.pending["delivered"]:
                    self.pending["delivered"] = True
                    return {k: self.pending[k] for k in ("id", "kind", "arguments")}
            remaining = deadline - asyncio.get_running_loop().time()
            if remaining <= 0:
                return None
            await asyncio.sleep(min(.05, remaining))

    async def complete(self, session, command_id, result):
        encoded = json.dumps(result, separators=(",", ":")).encode()
        if len(encoded) > MAX_BODY:
            raise ValueError("result exceeds bridge limit")
        async with self._lock:
            self._require_session(session)
            if not self.pending or self.pending["id"] != command_id:
                raise LookupError("unknown or expired command")
            if self.pending["kind"] == "context":
                checked = self._check_context(result)
                if self.browser_context and checked["instance"] != self.browser_context["instance"]:
                    raise PermissionError("browser page instance cannot change within a session")
                self._replace_context(checked)
                result = dict(checked)
                if self.application_instance is not None:
                    result["application_instance"] = self.application_instance
            future = self.pending["future"]
            if future.done():
                raise LookupError("command already completed")
            future.set_result(result)

    def _require_session(self, session):
        self._expire_session()
        if self.session is None or not secrets.compare_digest(session or "", self.session):
            raise PermissionError("invalid browser session")

    def _expire_session(self):
        if self.session_deadline is None or asyncio.get_running_loop().time() <= self.session_deadline:
            return
        self.session = None
        self.session_deadline = None
        self._replace_context(None)
        if self.pending:
            self.pending["future"].set_exception(ConnectionError("browser lease expired"))
            self.pending = None

    def set_application_instance(self, instance):
        self.application_instance = instance or None

    def presence_snapshot(self):
        context = dict(self.browser_context) if self.browser_context is not None else None
        return self.presence_version, context

    async def wait_presence_change(self, version, timeout):
        if self.presence_version != version:
            return
        self._presence_event.clear()
        if self.presence_version != version:
            return
        await asyncio.wait_for(self._presence_event.wait(), timeout)

    @staticmethod
    def _check_context(context):
        if not isinstance(context, dict):
            raise ValueError("browser context must be an object")
        limits = {"instance": 128, "context": 64, "revision": 256, "title": 256}
        checked = {}
        for name, limit in limits.items():
            value = context.get(name)
            if not isinstance(value, str) or not value or len(value.encode("utf-8")) > limit:
                raise ValueError(f"browser context {name} is invalid")
            checked[name] = value
        return checked

    def _replace_context(self, context):
        if context == self.browser_context:
            return
        self.browser_context = context
        self.presence_version += 1
        self._presence_event.set()


def install_routes(prompt_server, bridge):
    """Install aiohttp handlers on an isolated ComfyUI PromptServer."""
    from aiohttp import web

    async def body(request):
        if request.content_length is not None and request.content_length > MAX_BODY:
            raise web.HTTPRequestEntityTooLarge(max_size=MAX_BODY, actual_size=request.content_length)
        raw = await request.content.read(MAX_BODY + 1)
        if len(raw) > MAX_BODY:
            raise web.HTTPRequestEntityTooLarge(max_size=MAX_BODY, actual_size=len(raw))
        try:
            return parse_body(raw)
        except ValueError as exc:
            raise web.HTTPBadRequest(text=str(exc)) from exc

    def credential(request, browser=False):
        prefix = "Bearer "
        value = request.headers.get("Authorization", "")
        if not value.startswith(prefix):
            raise web.HTTPUnauthorized()
        origin = request.headers.get("Origin") if browser else None
        if browser and origin is None:
            raise web.HTTPForbidden(text="browser Origin is required")
        try:
            bridge.authorize(value[len(prefix):], origin)
        except PermissionError as exc:
            raise web.HTTPForbidden(text=str(exc)) from exc

    @prompt_server.routes.post("/oa/presentation/v1/browser/register")
    async def register(request):
        credential(request, True)
        try:
            data = await body(request)
            return web.json_response({"session": await bridge.register(data.get("context"))})
        except (RuntimeError, ValueError) as exc:
            raise web.HTTPConflict(text=str(exc)) from exc

    @prompt_server.routes.post("/oa/presentation/v1/browser/next")
    async def next_command(request):
        credential(request, True)
        data = await body(request)
        try:
            return web.json_response({"command": await bridge.next(data.get("session"), data.get("context"))})
        except (PermissionError, ValueError) as exc:
            raise web.HTTPForbidden(text=str(exc)) from exc

    @prompt_server.routes.post("/oa/presentation/v1/browser/context")
    async def update_context(request):
        credential(request, True)
        data = await body(request)
        try:
            await bridge.update_context(data.get("session"), data.get("context"))
            return web.json_response({"updated": True})
        except (PermissionError, ValueError) as exc:
            raise web.HTTPConflict(text=str(exc)) from exc

    @prompt_server.routes.post("/oa/presentation/v1/browser/result")
    async def result(request):
        credential(request, True)
        data = await body(request)
        try:
            await bridge.complete(data.get("session"), data.get("id"), data.get("result"))
            return web.json_response({"accepted": True})
        except (PermissionError, LookupError, ValueError) as exc:
            raise web.HTTPConflict(text=str(exc)) from exc

    @prompt_server.routes.post("/oa/presentation/v1/browser/unregister")
    async def unregister(request):
        credential(request, True)
        data = await body(request)
        try:
            await bridge.unregister(data.get("session"))
            return web.json_response({"disconnected": True})
        except PermissionError as exc:
            raise web.HTTPForbidden(text=str(exc)) from exc

    @prompt_server.routes.post("/oa/presentation/v1/command")
    async def command(request):
        credential(request)
        data = await body(request)
        try:
            result = await bridge.submit(data.get("kind"), data.get("arguments", {}))
            return web.json_response({"result": result})
        except (ConnectionError, PermissionError, RuntimeError, UnresolvedCommand) as exc:
            raise web.HTTPConflict(text=str(exc)) from exc
        except (ValueError, asyncio.TimeoutError) as exc:
            raise web.HTTPBadRequest(text=str(exc)) from exc
