"""Opt-in presentation experiment; no model execution."""

import logging
import os

NODE_CLASS_MAPPINGS = {}
WEB_DIRECTORY = "./web"

# The local relay is absent unless an isolated runner supplies both values.
# Its bearer credential is entered into the browser by the operator and passed
# separately to the ordinary MCP fixture; it is never a tool argument/result.
_token = os.environ.get("OA_COMFY_BRIDGE_TOKEN")
_origin = os.environ.get("OA_COMFY_BRIDGE_ORIGIN")
_oa_mode = os.environ.get("OA_COMFY_OA_MODE")
if _oa_mode not in (None, "1"):
    raise RuntimeError("OA_COMFY_OA_MODE must be unset or 1")
if _token or _origin:
    if not (_token and _origin):
        raise RuntimeError("OA_COMFY_BRIDGE_TOKEN and OA_COMFY_BRIDGE_ORIGIN must be set together")
    from .server.bridge import Bridge, install_routes
    from server import PromptServer

    _bridge = Bridge(_token, _origin, require_context=bool(_oa_mode))
    install_routes(PromptServer.instance, _bridge)
    if _oa_mode:
        from .server.oa_presence import start_presence

        _presence, _presence_task = start_presence(PromptServer.instance, _bridge)

        def _presence_done(task):
            if task.cancelled():
                return
            error = task.exception()
            if error is not None:
                logging.error("OA ComfyUI presence stopped: %s", error)

        _presence_task.add_done_callback(_presence_done)
elif _oa_mode:
    raise RuntimeError("OA mode requires the configured presentation bridge")
