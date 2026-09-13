#!/usr/bin/env python3
"""Exercise the compiled cpa-quota-warmup plugin through CPA's public C ABI.

This test does not wire up a real host (host_ctx/call are left NULL), so any
host.* callback the plugin makes (host.auth.list, host.log) fails immediately
inside the plugin's own cgo bridge. The plugin is expected to degrade
gracefully in that situation -- this is exactly what it does whenever the
management "status"/"run" routes are hit outside of a live CPA process too.
"""

import base64
import ctypes
import json
import pathlib
import sys


class Buffer(ctypes.Structure):
    _fields_ = [("ptr", ctypes.c_void_p), ("len", ctypes.c_size_t)]


PluginCall = ctypes.CFUNCTYPE(
    ctypes.c_int,
    ctypes.c_char_p,
    ctypes.POINTER(ctypes.c_uint8),
    ctypes.c_size_t,
    ctypes.POINTER(Buffer),
)
PluginFree = ctypes.CFUNCTYPE(None, ctypes.c_void_p, ctypes.c_size_t)
PluginShutdown = ctypes.CFUNCTYPE(None)


class HostAPI(ctypes.Structure):
    _fields_ = [
        ("abi_version", ctypes.c_uint32),
        ("host_ctx", ctypes.c_void_p),
        ("call", ctypes.c_void_p),
        ("free_buffer", ctypes.c_void_p),
    ]


class PluginAPI(ctypes.Structure):
    _fields_ = [
        ("abi_version", ctypes.c_uint32),
        ("call", PluginCall),
        ("free_buffer", PluginFree),
        ("shutdown", PluginShutdown),
    ]


def invoke(plugin: PluginAPI, method: str, request: dict) -> dict:
    encoded = json.dumps(request, separators=(",", ":")).encode()
    request_buffer = (ctypes.c_uint8 * max(1, len(encoded))).from_buffer_copy(
        encoded or b"\0"
    )
    response = Buffer()
    rc = plugin.call(
        method.encode(), request_buffer, len(encoded), ctypes.byref(response)
    )
    try:
        raw = ctypes.string_at(response.ptr, response.len) if response.ptr else b""
    finally:
        if response.ptr:
            plugin.free_buffer(response.ptr, response.len)
    decoded = json.loads(raw) if raw else {}
    if rc != 0 or not decoded.get("ok"):
        raise RuntimeError(f"{method} failed: rc={rc}, response={decoded}")
    return decoded["result"]


def register_request(yaml_text: str) -> dict:
    return {"config_yaml": base64.b64encode(yaml_text.encode()).decode()}


def management_request(path: str, query: dict | None = None) -> dict:
    return {
        "Method": "GET",
        "Path": path,
        "Headers": {},
        "Query": {key: [value] for key, value in (query or {}).items()},
        "Body": None,
    }


def decode_management(result: dict) -> tuple[int, dict, bytes]:
    body = base64.b64decode(result.get("Body") or b"")
    headers = {
        key.lower(): values[0] for key, values in (result.get("Headers") or {}).items()
    }
    return result.get("StatusCode", 0), headers, body


BASE_CONFIG = """
timezone: "UTC"
message: "hi"
providers:
  antigravity: { model: "gemini-3.7-flash-high" }
"""


def main() -> None:
    library_path = pathlib.Path(sys.argv[1]).resolve()
    library = ctypes.CDLL(str(library_path))
    init = library.cliproxy_plugin_init
    init.argtypes = [ctypes.POINTER(HostAPI), ctypes.POINTER(PluginAPI)]
    init.restype = ctypes.c_int

    # host_ctx/call are intentionally left NULL: every host.* callback this
    # plugin makes must fail safely rather than crash or hang.
    host = HostAPI(abi_version=1)
    plugin = PluginAPI()
    if init(ctypes.byref(host), ctypes.byref(plugin)) != 0:
        raise RuntimeError("cliproxy_plugin_init failed")

    try:
        registration = invoke(plugin, "plugin.register", register_request(BASE_CONFIG))
        capabilities = registration["capabilities"]
        if not capabilities.get("usage_plugin") or not capabilities.get("management_api"):
            raise AssertionError(f"unexpected capabilities: {capabilities}")
        for field in ("Name", "Version", "Author", "GitHubRepository"):
            if not registration["metadata"].get(field):
                raise AssertionError(f"required metadata field missing: {field}")
        plugin_id = registration["metadata"]["Name"]
        if plugin_id != "cpa-quota-warmup":
            raise AssertionError(f"unexpected plugin metadata name: {plugin_id}")
        if not registration["metadata"]["ConfigFields"]:
            raise AssertionError("no config fields advertised to the management UI")

        mgmt_routes = invoke(
            plugin,
            "management.register",
            {
                "Plugin": registration["metadata"],
                "BasePath": "/v0/management",
                "ResourceBasePath": f"/v0/resource/plugins/{plugin_id}",
            },
        )
        resources = mgmt_routes["Resources"]
        paths = [item["Path"] for item in resources]
        if paths != ["/panel", "/status", "/run"]:
            raise AssertionError(f"unexpected resource paths: {paths}")
        if not resources[0]["Menu"] or resources[1].get("Menu") or resources[2].get("Menu"):
            raise AssertionError("only the panel route may carry a menu label")

        # The panel page must render as a self-contained HTML document that
        # follows the Management Center's own language (and theme) choice.
        panel_status, panel_headers, panel_body = decode_management(
            invoke(
                plugin,
                "management.handle",
                management_request(f"/v0/resource/plugins/{plugin_id}/panel"),
            )
        )
        if panel_status != 200 or "text/html" not in panel_headers.get("content-type", ""):
            raise AssertionError(f"panel response: status={panel_status}, headers={panel_headers}")
        if b"cli-proxy-language" not in panel_body:
            raise AssertionError("panel HTML does not read the cli-proxy-language localStorage key")
        if b"cli-proxy-theme" not in panel_body:
            raise AssertionError("panel HTML does not follow the cli-proxy-theme localStorage key")
        if b"{{" in panel_body:
            raise AssertionError("panel HTML has an unreplaced {{...}} template placeholder")

        # usage.handle must never error, even for an arbitrary/unrelated record.
        usage_record = {
            "Provider": "antigravity",
            "Model": "gemini-3.7-flash-high",
            "SessionID": "header:cpa-quota-warmup-deadbeef",
            "AuthID": "auth-1",
            "RequestedAt": "2026-09-13T05:30:00Z",
            "Failed": False,
            "Detail": {"InputTokens": 5, "OutputTokens": 3},
        }
        invoke(plugin, "usage.handle", usage_record)

        status, headers, body = decode_management(
            invoke(
                plugin,
                "management.handle",
                management_request(f"/v0/resource/plugins/{plugin_id}/status", {"lang": "ru"}),
            )
        )
        if status != 200 or "application/json" not in headers.get("content-type", ""):
            raise AssertionError(f"status response: status={status}, headers={headers}")
        payload = json.loads(body)
        if payload["config"]["message"] != "hi":
            raise AssertionError(f"unexpected config summary: {payload['config']}")
        if payload.get("lang") != "ru":
            raise AssertionError(f"expected ?lang=ru to be honored, got lang={payload.get('lang')!r}")
        if not payload.get("auths_error"):
            # host.auth.list has no real host behind it in this test, so the
            # status route is expected to surface that as auths_error rather
            # than silently reporting zero auths.
            raise AssertionError(f"expected auths_error with no host wired up: {payload}")

        # The run route degrades the same way: no host.auth.list means no
        # accounts to trigger, surfaced as a 500 with an error body rather
        # than a crash or a hang.
        run_status, _, run_body = decode_management(
            invoke(
                plugin,
                "management.handle",
                management_request(f"/v0/resource/plugins/{plugin_id}/run", {"auth": "*", "lang": "zh-TW"}),
            )
        )
        if run_status != 500 or b"error" not in run_body:
            raise AssertionError(f"run response: status={run_status}, body={run_body!r}")
        run_payload = json.loads(run_body)
        if run_payload.get("lang") != "zh-TW":
            raise AssertionError(f"expected ?lang=zh-TW to be honored on /run, got {run_payload}")

        # plugin.reconfigure must hot-swap the config without erroring, and
        # the change must be visible through the status route immediately.
        reconfigured = invoke(
            plugin, "plugin.reconfigure", register_request(BASE_CONFIG.replace('"hi"', '"yo"'))
        )
        if reconfigured["metadata"]["Name"] != plugin_id:
            raise AssertionError("reconfigure returned unexpected metadata")
        status, _, body = decode_management(
            invoke(
                plugin,
                "management.handle",
                management_request(f"/v0/resource/plugins/{plugin_id}/status"),
            )
        )
        payload = json.loads(body)
        if payload["config"]["message"] != "yo":
            raise AssertionError(f"reconfigure did not take effect: {payload['config']}")

        invoke(plugin, "plugin.quiesce", {})

        invoke(plugin, "plugin.shutdown", {})
        status, _, body = decode_management(
            invoke(
                plugin,
                "management.handle",
                management_request(f"/v0/resource/plugins/{plugin_id}/status"),
            )
        )
        if status != 503:
            raise AssertionError(f"expected 503 after shutdown, got status={status} body={body!r}")

        print(
            "ABI integration passed: "
            f"schema={registration['schema_version']}, plugin={plugin_id} "
            f"v{registration['metadata']['Version']}"
        )
    finally:
        plugin.shutdown()


if __name__ == "__main__":
    main()
