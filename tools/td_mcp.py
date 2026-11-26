"""Local stdio MCP adapter for the allowlisted TimbreDeck agent CLI."""

import argparse
import base64
import hashlib
import json
import math
from pathlib import Path
import re
import subprocess
import sys
import threading
from urllib.parse import urlsplit


SIBLING = Path(__file__).resolve().with_name("td_agent.py")
PROTOCOLS = ("2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05")
MAX_LINE = 128 * 1024
MAX_CLI_OUTPUT = 96 * 1024
MAX_TOOL_METADATA = 24 * 1024
MAX_WAV = 32 * 1024 * 1024
MAX_RESOURCE_BYTES = 8 * 1024 * 1024
MAX_RESOURCES = 128
RESOURCE_PAGE = 16
LANGUAGES = ["yue", "zh", "en", "ja", "ko"]
SYNTH_LANGUAGES = LANGUAGES + ["auto", "de", "fr", "ru", "pt", "es", "it"]
ASR_LANGUAGES = SYNTH_LANGUAGES + ["nl", "ar", "hi", "th", "vi", "id"]
ENGINES = ["gpt-sovits", "cosyvoice3", "voxcpm2", "indextts2", "qwen3-tts"]
MIMES = {".wav": "audio/wav", ".zip": "application/zip"}
ID_PATTERN = r"^[0-9a-f]{32}$"


def string(limit=160, **extra):
    return {"type": "string", "minLength": 1, "maxLength": limit, **extra}


def enum(values):
    return {"type": "string", "enum": values}


def number(lo, hi, type_="number"):
    return {"type": type_, "minimum": lo, "maximum": hi}


def array(items, minimum, maximum):
    return {"type": "array", "items": items, "minItems": minimum, "maxItems": maximum}


def tool(action, description, properties=None, required=(), read_only=False):
    return {
        "name": "timbredeck_" + action, "description": description,
        "inputSchema": {"type": "object", "properties": properties or {},
                        "required": list(required), "additionalProperties": False},
        "annotations": {"readOnlyHint": read_only, "destructiveHint": False, "openWorldHint": False},
    }


PATH = string(4096, description="An explicitly selected recording (WAV, MP3, MP4, M4A, AAC or MOV) in the configured input root.")
REFS = array(PATH, 1, 24)
BOOL = {"type": "boolean"}
TOOLS = [tool(*args, **kwargs) for args, kwargs in [
    (("capabilities", "Read local TimbreDeck engine and feature capabilities."), {"read_only": True}),
    (("status", "Read local TimbreDeck Studio status and any current job."), {"read_only": True}),
    (("asr", "Transcribe an explicitly selected local WAV; may import this WAV into the local Studio.",
      {"reference": PATH, "language": enum(ASR_LANGUAGES),"asr_model":string(80,pattern=r"^[a-z0-9-]+$")}, ("reference",)), {}),
    (("synthesize", "Generate and export a new WAV with a configured local voice engine.",
      {"engine": string(80,pattern=r"^[a-z0-9-]+$"), "text": string(10000), "language": enum(SYNTH_LANGUAGES),
       "mode": string(48, pattern=r"^[A-Za-z0-9_-]+$"), "reference": PATH,
       "transcript": string(10000), "reference_language": enum(ASR_LANGUAGES), "voice_id": string(32, pattern=ID_PATTERN),
       "variant": string(48, pattern=r"^[A-Za-z0-9_-]+$"), "speaker": string(160),
       "instruction": string(2000), "speed": number(0.5, 2),
       "emotion_reference": PATH, "emotion_strength": number(0, 1),
       "emotion_weights": array(number(0, 1), 8, 8),
       "model_project": string(32, pattern=ID_PATTERN), "preview": BOOL,"split_at":number(20,2000,"integer"),
       "split_gap_ms":number(0,5000,"integer"),"separate_parts":BOOL,"asr_model":string(80,pattern=r"^[a-z0-9-]+$")},
      ("engine", "text")), {}),
    (("clean", "Denoise or isolate speech from the explicitly ordered WAVs, then export a new WAV.",
      {"references": REFS, "mode": enum(["denoise", "uvr", "both"]), "gap_ms": number(0, 3000, "integer")},
      ("references",)), {}),
    (("dataset", "Split and transcribe explicitly selected WAVs for a GPT-SoVITS dataset; export a new ZIP.",
      {"references": REFS, "language": enum(LANGUAGES), "speaker": string(64), "denoise": BOOL,"asr_model":string(80,pattern=r"^[a-z0-9-]+$")},
      ("references", "language")), {}),
]]
BY_NAME = {item["name"]: item for item in TOOLS}


def fail_constant(value):
    raise ValueError(f"Unsupported JSON constant: {value}")


def load_json(data):
    return json.loads(data.decode("utf-8"), parse_constant=fail_constant)


def dump_json(data):
    return json.dumps(data, ensure_ascii=True, allow_nan=False, separators=(",", ":"))


def finite_number(value):
    return type(value) in (int, float) and abs(value) <= 10000 and math.isfinite(value)


def validate(value, schema, name):
    type_ = schema["type"]
    valid = True
    if type_ == "string":
        valid = (isinstance(value, str) and schema.get("minLength", 0) <= len(value) <= schema.get("maxLength", MAX_LINE)
                 and "\x00" not in value and all(not 0xD800 <= ord(c) <= 0xDFFF for c in value)
                 and ("enum" not in schema or value in schema["enum"])
                 and ("pattern" not in schema or re.fullmatch(schema["pattern"], value)))
    elif type_ in ("number", "integer"):
        valid = (finite_number(value) and (type_ != "integer" or type(value) is int)
                 and schema["minimum"] <= value <= schema["maximum"])
    elif type_ == "array":
        valid = isinstance(value, list) and schema["minItems"] <= len(value) <= schema["maxItems"]
        if valid:
            for item in value:
                validate(item, schema["items"], name)
    elif type_ == "boolean":
        valid = type(value) is bool
    if not valid:
        raise ValueError(f"Invalid tool argument: {name}")


def resolve_local_path(path, root):
    if path.drive.startswith("\\\\") or any(":" in part for part in path.parts[1 if path.anchor else 0:]):
        raise ValueError("Network shares and alternate data streams are not input or output files")
    path = (root / path).resolve(strict=True)
    if not path.is_relative_to(root) or not path.is_file():
        raise ValueError("File must be inside the configured root")
    return path


def local_wav(value, root):
    try:
        path = resolve_local_path(Path(value), root)
        if path.suffix.lower() not in {".wav",".mp3",".mp4",".m4a",".aac",".mov"} or not 12 <= path.stat().st_size <= MAX_WAV:
            raise ValueError("Recording must be 12 bytes to 32 MiB")
    except (OSError, ValueError, RuntimeError) as exc:
        raise ValueError("Select a valid WAV inside the configured input root (max 32 MiB)") from exc
    return str(path)


def cli_arguments(spec, args, input_root, output_root):
    if not isinstance(args, dict):
        raise ValueError("Tool arguments must be an object")
    schema = spec["inputSchema"]
    props = schema["properties"]
    if set(args) - props.keys() or set(schema["required"]) - args.keys():
        raise ValueError("Unknown or missing tool arguments")
    action = spec["name"].removeprefix("timbredeck_")
    command = [action]
    for key, value in args.items():
        validate(value, props[key], key)
        option = "--" + key.replace("_", "-")
        if key == "references":
            command.extend("--reference=" + local_wav(path, input_root) for path in value)
        elif key in ("reference", "emotion_reference"):
            command.append(option + "=" + local_wav(value, input_root))
        elif key == "emotion_weights":
            command.extend([option] + [format(x, ".10f") for x in value])
        elif type(value) is bool:
            if value:
                command.append(option)
        else:
            command.append(f"{option}={value}")
    if action in ("synthesize", "clean", "dataset"):
        command.append("--output-dir=" + str(output_root))
    return command


def run_cli(command, timeout):
    # Bound both pipes while draining concurrently (Windows pipes are not selectable).
    with subprocess.Popen(command, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                          stderr=subprocess.PIPE, shell=False) as process:
        captured = bytearray()
        oversized = threading.Event()

        def drain(stream, output=None):
            size = 0
            while chunk := stream.read1(8192):
                size += len(chunk)
                if size > MAX_CLI_OUTPUT:
                    oversized.set()
                    try:
                        process.kill()
                    except OSError:
                        pass
                    return
                if output is not None:
                    output.extend(chunk)

        threads = [threading.Thread(target=drain, args=pair, daemon=True) for pair in
                   [(process.stdout, captured), (process.stderr, None)]]
        for worker in threads:
            worker.start()
        try:
            code = process.wait(timeout=timeout + 15)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait()
            raise ValueError("TimbreDeck CLI timed out; check Studio status")
        for worker in threads:
            worker.join(timeout=3)
        if oversized.is_set() or any(worker.is_alive() for worker in threads):
            raise ValueError("TimbreDeck CLI output exceeded limits")
        try:
            result = load_json(bytes(captured))
        except (ValueError, RecursionError):
            raise ValueError(f"TimbreDeck CLI returned invalid JSON (exit {code})") from None
        if not isinstance(result, dict) or type(result.get("ok")) is not bool or (code == 0) != result["ok"]:
            raise ValueError(f"TimbreDeck CLI returned inconsistent results (exit {code})")
        return result


def local_artifacts(result, root):
    artifacts = result.get("artifacts", [])
    if not isinstance(artifacts, list) or len(artifacts) > 20:
        raise ValueError("Unexpected TimbreDeck artifact list")
    safe, links = [], []
    for item in artifacts:
        if not isinstance(item, dict) or not isinstance(item.get("path"), str):
            raise ValueError("Unexpected TimbreDeck artifact metadata")
        try:
            path = Path(item["path"])
            if not path.is_absolute():
                raise ValueError("Artifact path is not absolute")
            path = resolve_local_path(path, root)
            size = path.stat().st_size
            mime = MIMES.get(path.suffix.lower())
            if not mime or item.get("type") != mime or item.get("bytes") != size or size <= 0:
                raise ValueError("Artifact metadata does not match the saved file")
        except (OSError, ValueError, RuntimeError):
            raise ValueError("CLI did not produce a valid WAV or ZIP inside the configured output directory") from None
        uri = path.as_uri()
        digest = item.get("sha256")
        if not isinstance(digest, str) or not re.fullmatch(r"[0-9a-f]{64}", digest):
            raise ValueError("CLI did not provide the artifact SHA-256")
        info = {"path": str(path), "uri": uri, "type": mime, "bytes": size, "sha256": digest}
        for key, pattern in [("job", r"[0-9a-f]{32}"), ("kind", r"synthesis|clean|dataset")]:
            if isinstance(item.get(key), str) and re.fullmatch(pattern, item[key]):
                info[key] = item[key]
        safe.append(info)
        links.append({"type": "resource_link", "uri": uri, "name": path.name, "mimeType": mime, "size": size})
    if "artifacts" in result:
        result["artifacts"] = safe
    return links


def tool_result(result, protocol, links=()):
    data = dump_json(result)
    if len(data) > MAX_TOOL_METADATA:
        raise ValueError("TimbreDeck metadata exceeds the MCP response limit")
    reply = {"content": [{"type": "text", "text": data}], "isError": not result["ok"]}
    if protocol in PROTOCOLS[:2]:
        reply["structuredContent"] = result
        reply["content"].extend(links)
    return reply


class RPCError(Exception):
    def __init__(self, code, message):
        self.code, self.message = code, message


class Server:
    def __init__(self, config):
        self.config = config
        self.protocol = None
        self.initialized = False
        self.resources = {}

    def notification(self, method):
        if method == "notifications/initialized" and self.protocol:
            self.initialized = True

    def list_resources(self, cursor):
        start = 0
        if cursor not in (None, ""):
            if not isinstance(cursor, str) or not re.fullmatch(r"[0-9]{1,3}", cursor):
                raise RPCError(-32602, "Invalid cursor")
            start = int(cursor)
            if start < RESOURCE_PAGE or start % RESOURCE_PAGE or start >= len(self.resources):
                raise RPCError(-32602, "Invalid cursor")
        items = list(self.resources.values())
        finish = start + RESOURCE_PAGE
        result = {"resources": [
            {"uri": a["uri"], "name": Path(a["path"]).name, "mimeType": a["type"], "size": a["bytes"]}
            for a in items[start:finish]
        ]}
        if finish < len(items):
            result["nextCursor"] = str(finish)
        return result

    def read_resource(self, uri):
        if not isinstance(uri, str):
            raise RPCError(-32602, "A registered resource URI is required")
        artifact = self.resources.get(uri)
        if not artifact:
            raise RPCError(-32002, "Resource not found in this session")
        if artifact["bytes"] > MAX_RESOURCE_BYTES:
            raise RPCError(-32000, "Resource exceeds 8 MiB; open the returned local file or attach it in your client")
        try:
            path = resolve_local_path(Path(artifact["path"]), self.config.output_dir)
            if path.as_uri() != uri or path.stat().st_size != artifact["bytes"]:
                raise ValueError("Resource moved or changed")
            with path.open("rb") as stream:
                data = stream.read(MAX_RESOURCE_BYTES + 1)
            if len(data) != artifact["bytes"] or hashlib.sha256(data).hexdigest() != artifact["sha256"]:
                raise ValueError("Resource SHA-256 does not match")
        except (OSError, ValueError, RuntimeError):
            raise RPCError(-32002, "Resource is no longer available in its original form") from None
        return {"contents": [{"uri": uri, "mimeType": artifact["type"],
                              "blob": base64.b64encode(data).decode("ascii")}]}

    def request(self, method, params):
        if not isinstance(params, dict):
            raise RPCError(-32602, "Invalid params")
        if method == "ping":
            return {}
        if method == "initialize":
            version = params.get("protocolVersion")
            if self.protocol or not isinstance(version, str) or not version:
                raise RPCError(-32602, "Invalid or repeated initialize")
            self.protocol = version if version in PROTOCOLS else PROTOCOLS[0]
            return {"protocolVersion": self.protocol, "capabilities": {"tools": {"listChanged": False},
                                                                     "resources": {"subscribe": False, "listChanged": False}},
                    "serverInfo": {"name": "TimbreDeck", "version": "1.0.0"},
                    "instructions": "Use explicitly selected local WAVs. WAV/ZIP files stay on the server computer."}
        if not self.initialized:
            raise RPCError(-32002, "Server not initialized")
        if method == "tools/list":
            if params.get("cursor") not in (None, ""):
                raise RPCError(-32602, "Invalid cursor")
            return {"tools": TOOLS}
        if method == "resources/list":
            return self.list_resources(params.get("cursor"))
        if method == "resources/templates/list":
            if params.get("cursor") not in (None, ""):
                raise RPCError(-32602, "Invalid cursor")
            return {"resourceTemplates": []}
        if method == "resources/read":
            return self.read_resource(params.get("uri"))
        if method != "tools/call":
            raise RPCError(-32601, "Method not found")
        name = params.get("name")
        if not isinstance(name, str) or name not in BY_NAME:
            raise RPCError(-32602, "Unknown tool")
        try:
            command = cli_arguments(BY_NAME[name], params.get("arguments", {}), self.config.input_root,
                                    self.config.output_dir)
            base = [sys.executable, "-B", "-X", "utf8", str(SIBLING), "--timeout", str(self.config.timeout)]
            if self.config.url:
                base.extend(["--url", self.config.url])
            command = base + command
            if len(subprocess.list2cmdline(command).encode("utf-16-le")) > 62000:
                raise ValueError("Tool arguments exceed the local command-line limit")
            result = run_cli(command, self.config.timeout)
            # The desktop instance token belongs to the local CLI/Studio
            # handshake. It is never needed by an MCP client.
            result.pop("instance", None)
            if result["ok"]:
                links = local_artifacts(result, self.config.output_dir)
            else:
                result.pop("artifacts", None)
                links = []
            reply = tool_result(result, self.protocol, links)
            if result["ok"]:
                for artifact in result.get("artifacts", []):
                    self.resources[artifact["uri"]] = artifact
                    if len(self.resources) > MAX_RESOURCES:
                        del self.resources[next(iter(self.resources))]
            return reply
        except (ValueError, OSError, RuntimeError) as exc:
            return tool_result({"ok": False, "error": {"code": "mcp_adapter", "message": str(exc)[:1000]}},
                               self.protocol)

    def serve(self, stdin, stdout):
        def send(value):
            stdout.write(dump_json({"jsonrpc": "2.0", **value}).encode("utf-8") + b"\n")
            stdout.flush()

        while line := stdin.readline(MAX_LINE + 1):
            if len(line) > MAX_LINE:
                while line and not line.endswith(b"\n"):
                    line = stdin.readline(MAX_LINE + 1)
                send({"id": None, "error": {"code": -32700, "message": "Message exceeds length limit"}})
                continue
            try:
                message = load_json(line)
            except (ValueError, RecursionError):
                send({"id": None, "error": {"code": -32700, "message": "Parse error"}})
                continue
            key = message.get("id") if isinstance(message, dict) else None
            if (not isinstance(message, dict) or message.get("jsonrpc") != "2.0"
                    or ("id" in message and (type(key) not in (str, int)
                                             or isinstance(key, str) and len(key) > 256))
                    or not isinstance(message.get("method"), str)):
                send({"id": None, "error": {"code": -32600, "message": "Invalid request"}})
                continue
            method = message["method"]
            if "id" not in message:
                self.notification(method)
                continue
            try:
                reply = {"result": self.request(method, message.get("params", {}))}
            except RPCError as exc:
                reply = {"error": {"code": exc.code, "message": exc.message}}
            send({"id": key, **reply})


def configured_url(raw):
    try:
        url = urlsplit(raw)
        if (url.scheme == "http" and url.netloc == f"127.0.0.1:{url.port}" and url.port
                and url.path in ("", "/") and not url.query and not url.fragment):
            return f"http://{url.netloc}"
    except ValueError:
        pass
    raise argparse.ArgumentTypeError("expected http://127.0.0.1:<port>")


def directory(raw):
    try:
        root = Path(raw).resolve(strict=True)
        if root.drive.startswith("\\\\") or not root.is_dir() or root.parent == root:
            raise ValueError()
        return root
    except (OSError, ValueError, RuntimeError):
        raise argparse.ArgumentTypeError("expected an existing local directory other than a drive root") from None


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--url", type=configured_url, help="Optional local Studio HTTP address; default uses CLI discovery")
    parser.add_argument("--input-root", type=directory, required=True, help="WAV directory explicitly shared with MCP")
    parser.add_argument("--output-dir", type=directory, required=True, help="Existing local artifact directory")
    parser.add_argument("--timeout", type=int, choices=range(1, 3601), metavar="SECONDS", default=600)
    args = parser.parse_args()
    try:
        Server(args).serve(sys.stdin.buffer, sys.stdout.buffer)
    except (KeyboardInterrupt, BrokenPipeError):
        return 0
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
