#!/usr/bin/env python3
"""TimbreDeck local automation CLI. Requires only Python 3.9+ standard library."""

import argparse
import contextlib
import hashlib
import http.client
import ipaddress
import json
import math
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import tempfile
import time
from urllib.parse import urlsplit
import zipfile


DEFAULT_DATA = Path("D:/TimbreDeck/acceptance-80ebf58/more/release/StudioData")
DEFAULT_OUTPUT = Path("D:/TimbreDeck/AI-Exports")
START_COMMAND = "D:/TimbreDeck/Start-TimbreDeck-AI.cmd"
AUTOSTART_EXE = Path("D:/TimbreDeck/TimbreDeck-AI.exe")
API_VERSION = 1
INSTANCE_HEADER = "X-TimbreDeck-Agent-Instance"
ENGINE_IDS = ("gpt-sovits", "cosyvoice3", "voxcpm2", "indextts2", "qwen3-tts")
STS_MODELS = ("cosyvoice3", "x-vc", "seed-vc-tiny", "meanvc2", "tvtsyn1")
LEGACY_CLEAN = ("denoise", "uvr", "both")
CLEAN_MODES = LEGACY_CLEAN + ("mossformer", "lavasr", "lavasr-denoise")
DATASET_CLEAN_MODES = tuple(mode for mode in CLEAN_MODES if mode not in ("uvr", "both"))
TRAINING_LANGUAGES = ("yue", "zh", "en", "ja", "ko")
ASR_LANGUAGES = ("auto",) + TRAINING_LANGUAGES + (
    "fr", "de", "es", "it", "pt", "nl", "ru", "ar", "hi", "th", "vi", "id",
)
ACTIVE = ("running", "cancelling")
TERMINAL = ("done", "failed", "cancelled")
MAX_WAV = 32 * 1024 * 1024
MAX_INPUT = 128 * 1024 * 1024
MAX_ARTIFACT = 128 * 1024 * 1024
MAX_JSON = 8 * 1024 * 1024
MAX_FILES = 24
TOKEN = re.compile(r"[A-Za-z0-9_-]{1,128}\Z")


class AgentError(Exception):
    def __init__(self, code, message, **details):
        super().__init__(message)
        self.code = code
        self.details = details


class AgentParser(argparse.ArgumentParser):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, allow_abbrev=False, **kwargs)

    def error(self, message):
        raise AgentError("usage", message)


def safe_id(value, n=32):
    return isinstance(value, str) and len(value) == n and re.fullmatch(r"[0-9a-f]+", value) is not None


def checked_id(value, field, n=32):
    if not safe_id(value, n):
        raise AgentError("invalid_id", f"{field} must be a {n}-character lowercase hexadecimal ID.")
    return value


def is_link(info):
    return stat.S_ISLNK(info.st_mode) or bool(
        getattr(info, "st_file_attributes", 0) & getattr(stat, "FILE_ATTRIBUTE_REPARSE_POINT", 0x400)
    )


def explicit_path(value):
    # abspath normalizes the caller's path lexically. Do not resolve implicit
    # home markers, alternate streams, network paths, symlinks, or junctions.
    raw = os.fspath(value)
    if raw.startswith(("\\\\", "//")) or "://" in raw or "\x00" in raw:
        raise AgentError("unsafe_path", "Only local filesystem paths are permitted.")
    absolute = Path(os.path.abspath(raw))
    # On Windows an alternate data stream can otherwise look like a file.
    if os.name == "nt" and any(":" in p or p.endswith((" ", ".")) for p in absolute.parts[1:]):
        raise AgentError("unsafe_path", f"Unsupported Windows path: {absolute}")
    return absolute


def inspect_path(path, directory=False, create=False):
    """Reject links/junctions in every component, even under an allowed root."""
    current = Path(path.anchor)
    for index, part in enumerate(path.parts[1:]):
        current /= part
        want_dir = directory or index < len(path.parts) - 2
        if create and want_dir:
            try:
                current.mkdir()
            except FileExistsError:
                pass
        info = current.lstat()
        if is_link(info) or (not stat.S_ISDIR(info.st_mode) if want_dir else not stat.S_ISREG(info.st_mode)):
            raise AgentError("unsafe_path", f"Path contains a link or is not a regular {'directory' if want_dir else 'file'}: {current}")
    return current.lstat()


def stable_file(path, limit):
    before = inspect_path(path)
    if not 0 < before.st_size <= limit:
        raise AgentError("size_limit", f"File must contain 1 to {limit} bytes: {path}")
    flags = os.O_RDONLY | getattr(os, "O_BINARY", 0) | getattr(os, "O_NOFOLLOW", 0)
    with os.fdopen(os.open(path, flags), "rb") as stream:
        actual = os.fstat(stream.fileno())
        if not os.path.samestat(before, actual) or not stat.S_ISREG(actual.st_mode):
            raise AgentError("input_changed", f"File changed while opening: {path}")
        data = stream.read(limit + 1)
        after = os.fstat(stream.fileno())
    final = inspect_path(path)
    if (not os.path.samestat(actual, final) or len(data) != before.st_size or
            actual.st_size != after.st_size or before.st_mtime_ns != after.st_mtime_ns or
            after.st_mtime_ns != final.st_mtime_ns):
        raise AgentError("input_changed", f"File changed while reading: {path}")
    return data


def wav_header(header):
    return len(header) >= 12 and header[:4] == b"RIFF" and header[8:12] == b"WAVE"


INPUT_EXTENSIONS = {".wav", ".mp3", ".mp4", ".m4a", ".aac", ".mov"}

def input_wavs(paths):
    if not 1 <= len(paths) <= MAX_FILES:
        raise AgentError("usage", f"Provide between 1 and {MAX_FILES} explicit WAV paths.")
    total, prepared = 0, []
    for filename in paths:
        path = explicit_path(filename)
        if path.suffix.lower() not in INPUT_EXTENSIONS:
            raise AgentError("invalid_wav", f"Expected WAV, MP3, MP4, M4A, AAC or MOV: {path}")
        try:
            data = stable_file(path, min(MAX_INPUT - total, MAX_WAV))
        except FileNotFoundError as exc:
            raise AgentError("missing_input", f"WAV file does not exist: {path}") from exc
        if path.suffix.lower()==".wav" and (not wav_header(data) or int.from_bytes(data[4:8], "little") + 8 != len(data)):
            raise AgentError("invalid_wav", f"File does not contain one complete RIFF WAV: {path}")
        total += len(data)
        prepared.append(data)
    return prepared


def loopback_endpoint(url):
    if (not isinstance(url, str) or not url or len(url) > 256 or
            any(c.isspace() or c in "\\?#%" for c in url)):
        raise AgentError("unsafe_url", "Use an HTTP loopback origin such as http://127.0.0.1:PORT.")
    try:
        parsed = urlsplit(url)
        port = parsed.port
        addr = ipaddress.ip_address(parsed.hostname)
        if (parsed.scheme != "http" or not addr.is_loopback or port is None or port == 0
                or parsed.username is not None or parsed.password is not None or parsed.path not in ("", "/")):
            raise ValueError("not an HTTP loopback origin")
    except (TypeError, ValueError) as exc:
        raise AgentError("unsafe_url", "Use an HTTP loopback origin such as http://127.0.0.1:PORT.") from exc
    canonical = f"[{addr}]" if addr.version == 6 else str(addr)
    return str(addr), port, f"http://{canonical}:{port}"


def offline(message, recoverable=False, **details):
    error = AgentError(
        "studio_offline",
        message + " Open TimbreDeck manually or run the optional AI background service, then retry.",
        status="offline", launchCommand=START_COMMAND, **details,
    )
    error.recoverable = recoverable
    return error


def discover(url):
    if url:
        return loopback_endpoint(url), None
    data = get_studio_data()
    path = data / "cache" / "agent-session.json"
    try:
        encoded = stable_file(path, 8192)
        manifest = json.loads(encoded.decode("utf-8-sig"))
    except FileNotFoundError as exc:
        raise offline("TimbreDeck is not running.", recoverable=True, sessionFile=str(path)) from exc
    except (UnicodeError, ValueError) as exc:
        raise offline("The local TimbreDeck session file is invalid.", sessionFile=str(path)) from exc
    if (not isinstance(manifest, dict) or not isinstance(manifest.get("instance"), str)
            or not TOKEN.fullmatch(manifest["instance"]) or type(manifest.get("pid")) is not int
            or manifest["pid"] <= 0):
        raise offline("The local TimbreDeck session file is incomplete.", sessionFile=str(path))
    return loopback_endpoint(manifest.get("url")), manifest["instance"]


def get_studio_data():
    return explicit_path(os.environ.get("TD_STUDIO_DATA") or DEFAULT_DATA)


def read_json(raw):
    def reject_constant(value):
        raise ValueError(f"Invalid JSON numeric constant: {value}")

    try:
        return json.loads(raw.decode("utf-8"), parse_constant=reject_constant)
    except (UnicodeError, ValueError) as exc:
        raise AgentError("invalid_response", "The local service returned invalid JSON.") from exc


class Client:
    def __init__(self, endpoint, expected, timeout):
        self.host, self.port, self.url = endpoint
        self.instance = None
        self.deadline = time.monotonic() + timeout
        try:
            self.info = self.json("GET", "/api/agent/info")
        except AgentError as exc:
            if exc.code == "connection":
                raise offline("The TimbreDeck service is not reachable.", recoverable=True, url=self.url) from exc
            if exc.details.get("httpStatus") == 404:
                raise AgentError("incompatible_api", "This TimbreDeck build does not support the agent API.", url=self.url) from exc
            raise
        if (not isinstance(self.info, dict) or self.info.get("product") != "TimbreDeck" or
                type(self.info.get("apiVersion")) is not int or self.info["apiVersion"] != API_VERSION):
            raise AgentError("incompatible_api", "The running service is not a compatible TimbreDeck agent API.", url=self.url)
        identity = self.info.get("instance")
        if not isinstance(identity, str) or not TOKEN.fullmatch(identity) or (expected is not None and identity != expected):
            raise AgentError("instance_mismatch", "The running TimbreDeck instance differs from the discovered session. Reopen the intended workspace.")
        self.instance = identity

    def remaining(self):
        remaining = self.deadline - time.monotonic()
        if remaining <= 0:
            raise AgentError("timeout", "TimbreDeck did not finish before the timeout. Existing work was not cancelled.")
        return remaining

    @contextlib.contextmanager
    def request(self, method, path, body=None, media=None, expected=(200,)):
        # HTTPConnection has no redirect handler, no proxy/environment adapter
        # and connects directly to the validated numeric loopback literal.
        conn = http.client.HTTPConnection(self.host, self.port, timeout=self.remaining())
        headers = {"X-TimbreDeck": "1", "Connection": "close"}
        if self.instance is not None:
            headers[INSTANCE_HEADER] = self.instance
        if media:
            headers["Content-Type"] = media
        try:
            conn.request(method, path, body=body, headers=headers)
            response = conn.getresponse()
            try:
                if 300 <= response.status < 400:
                    raise AgentError("redirect_blocked", "TimbreDeck returned a redirect; it was not followed.", httpStatus=response.status)
                if response.status not in expected:
                    message = f"Local TimbreDeck API returned HTTP {response.status}."
                    problem = response.read(8193)
                    if len(problem) <= 8192:
                        try:
                            value = read_json(problem)
                            if isinstance(value, dict) and isinstance(value.get("error"), str):
                                message = value["error"][:512]
                        except AgentError:
                            pass
                    if response.status == 409:
                        if "instance" in message.lower():
                            raise AgentError("instance_mismatch", message, httpStatus=response.status)
                        raise AgentError("conflict", message, httpStatus=response.status)
                    raise AgentError("http_error", message, httpStatus=response.status)
                yield response
            finally:
                response.close()
        except TimeoutError as exc:
            raise AgentError("timeout", "The local request timed out. Existing work was not cancelled.", outcome="unknown") from exc
        except (OSError, http.client.HTTPException) as exc:
            raise AgentError("connection", "Lost connection to the local TimbreDeck service.", url=self.url) from exc
        finally:
            conn.close()

    def json(self, method, path, value=None, data=None, expected=(200,)):
        media = None
        if value is not None:
            data = json.dumps(value, ensure_ascii=False, allow_nan=False).encode("utf-8")
            media = "application/json; charset=utf-8"
        elif data is not None:
            media = "audio/wav"
        with self.request(method, path, data, media, expected) as response:
            raw = response.read(MAX_JSON + 1)
            if len(raw) > MAX_JSON:
                raise AgentError("invalid_response", "Local JSON response exceeds the size limit.")
            return read_json(raw)

    def job(self, job_id=None):
        path = "/api/jobs/" + job_id if job_id and "jobs-by-id" in self.info.get("actions", []) else "/api/job"
        value = self.json("GET", path)
        if value is None:
            return None
        if (not isinstance(value, dict) or not safe_id(value.get("id")) or
                value.get("status") not in ACTIVE + TERMINAL):
            raise AgentError("invalid_response", "The local service returned an invalid job status.")
        return value

    def ensure_idle(self):
        current = self.job()
        if current is not None and current["status"] in ACTIVE:
            raise AgentError("busy", "TimbreDeck is busy; existing work was not changed or cancelled.", job=public_job(current))

    def upload(self, data):
        response = self.json("POST", "/api/reference", data=data, expected=(201,))
        expected = hashlib.sha256(data).hexdigest()
        duration = response.get("seconds") if isinstance(response, dict) else None
        key = response.get("id") if isinstance(response, dict) else None
        if (not isinstance(response, dict) or not isinstance(key,str) or not re.fullmatch("[a-f0-9]{64}",key) or
                response.get("sourceSha256",key) != expected or
                type(duration) not in (float, int) or not 0 < duration < 2**31):
            raise AgentError("invalid_response", "The service did not confirm the WAV's content hash and duration.")
        return {"id": key, "seconds": duration}

    def recognize(self, reference, language, model=None):
        request={"reference":reference,"language":language}
        if model:
            request["model"]=model
        value = self.json("POST", "/api/asr", request)
        if (not isinstance(value, dict) or not isinstance(value.get("text"), str)
                or not isinstance(value.get("provider"), str) or not isinstance(value.get("language"), str)):
            raise AgentError("invalid_response", "The service returned an incomplete transcript.")
        return {k: value[k] for k in ("text", "provider", "language", "requestedLanguage", "warning", "cached","model") if k in value}

    def submit(self, path, payload, allow_already_resident=False):
        # An HTTP conflict after the idle check is a race with a user job,
        # not permission to retry, assume ownership, or cancel that work.
        try:
            start = self.json("POST", path, payload, expected=(200, 202) if allow_already_resident else (202,))
        except AgentError as exc:
            if exc.code == "conflict":
                current = self.job()
                if current and current["status"] in ACTIVE:
                    raise AgentError("busy", "TimbreDeck is busy; existing work was not cancelled.", job=public_job(current)) from exc
            raise
        if (allow_already_resident and isinstance(start, dict) and start.get("kind") == "tts"
                and "status" not in start and safe_id(start.get("id"))):
            return None
        if not isinstance(start, dict) or not safe_id(start.get("id")) or start.get("status") not in ACTIVE + TERMINAL:
            raise AgentError("invalid_response", "The local service did not return a valid job ID.")
        job_id = start["id"]
        try:
            current = start
            while current["status"] in ACTIVE:
                time.sleep(min(0.5, self.remaining()))
                current = self.job(job_id)
                if current is None or current["id"] != job_id:
                    raise AgentError("job_changed", "The work slot changed before the requested job could be confirmed.")
            if current["status"] != "done":
                detail = current.get("detail")
                message = (detail[:512] if isinstance(detail, str) and detail else
                           f"The requested TimbreDeck job {current['status']}.")
                raise AgentError("job_" + current["status"], message, job=public_job(current))
        except AgentError as exc:
            exc.details.setdefault("job", public_job(start))
            raise
        return public_job(current)


def public_job(job):
    return {k: job[k] for k in ("id", "engine", "kind", "status") if k in job}


def has_action(client, name):
    return name in client.info.get("actions", [])


def clean_options(client):
    supported = client.info.get("cleanModes", LEGACY_CLEAN)
    return [mode for mode in CLEAN_MODES if mode in supported]


def ensure_clean_mode(client, mode):
    if mode not in clean_options(client):
        raise AgentError("unsupported", "The running Studio does not advertise this cleaning mode.")
    ready = client.info.get("readyCleanModes")
    if isinstance(ready, list) and mode not in ready:
        raise AgentError("model_unavailable", "The selected local cleaning model is not installed.")


def output_metadata(client, job):
    if not has_action(client, "output-metadata"):
        return {}
    try:
        value = client.json("GET", "/api/history/" + job["id"])
    except AgentError:
        return {}  # WAV/ZIP export can still succeed even if the optional metadata is unavailable.
    if not isinstance(value, dict) or value.get("id") != job["id"]:
        return {}
    result = {k: value[k] for k in ("model", "seconds", "sampleRate", "verifiedCuda", "parts") if k in value}
    if isinstance(value.get("timing"), dict):
        result["timing"] = {k: value["timing"][k] for k in ("coldStartSeconds", "generationSeconds", "totalSeconds", "resident") if k in value["timing"]}
    return result


def resident_state(client):
    value = client.json("GET", "/api/tts/resident")
    if not isinstance(value, dict) or type(value.get("busy")) is not bool:
        raise AgentError("invalid_response", "The service returned invalid resident model state.")
    if value.get("id") and (not safe_id(value["id"]) or value.get("kind") not in ("sts", "tts")):
        raise AgentError("invalid_response", "The service returned an invalid resident model ID.")
    return {k: value[k] for k in ("id", "busy", "kind", "engine", "variant", "modelProject") if k in value}


def manage_resident(client, args):
    if not has_action(client, "resident-control"):
        raise AgentError("unsupported", "This Studio build does not advertise resident model tools.")
    state = resident_state(client)
    if args.action == "resident-status":
        return {"resident": state}
    client.ensure_idle()
    if state["busy"]:
        raise AgentError("busy", "TimbreDeck is busy; its work was not changed.")
    if args.action == "stop-model":
        ident = checked_id(args.resident_id, "Resident ID")
        if ident != state.get("id"):
            raise AgentError("resident_changed", "Read resident-status and confirm the exact model to stop.")
        client.json("POST", "/api/" + state["kind"] + "/stop", {"id": ident})
        return {"resident": resident_state(client), "stopped": ident}
    engine, variant, project = args.engine, args.variant, optional_id(args.model_project, "Model project ID")
    if args.kind == "sts":
        if engine not in STS_MODELS or variant or project:
            raise AgentError("usage", "Choose a supported STS model ID without TTS variant/weights.")
        models = client.json("GET", "/api/sts/models")
        if not any(m.get("id") == engine and m.get("ready") for m in models):
            raise AgentError("model_unavailable", "The selected STS model is not installed.")
    else:
        data = engine_capabilities(client).get(engine, {})
        available = data.get("availableVariants", [])
        if not variant and len(available) == 1:
            variant = available[0]
        if variant not in available:
            raise AgentError("usage", "Specify one installed TTS variant from capabilities.")
        if project and (engine != "gpt-sovits" or variant != "v2Pro"):
            raise AgentError("usage", "Fine-tuned weights require GPT-SoVITS v2Pro.")
    if state.get("id"):
        if (state.get("kind"), state.get("engine"), state.get("variant"), state.get("modelProject")) == (
                args.kind, engine, variant, project):
            return {"resident": state, "alreadyResident": True}
        raise AgentError("resident_conflict", "A model is already pinned. Confirm its ID before explicitly stopping it.", resident=state)
    if args.kind == "sts":
        job = client.submit("/api/sts/warm?model=" + engine, {})
    else:
        payload = {"engine": engine, "variant": variant}
        if project:
            payload["modelProject"] = project
        job = client.submit("/api/tts/warm", payload, allow_already_resident=True)
    return {"resident": resident_state(client), **({"job": job} if job else {"alreadyResident": True})}


def engine_capabilities(client):
    engines = client.info.get("engines")
    if not isinstance(engines, dict):
        raise AgentError("invalid_response", "The local service returned no engine capabilities.")
    result = {}
    for key, entry in engines.items():
        if not isinstance(key,str) or not re.fullmatch(r"[a-z0-9-]{1,80}",key):
            continue
        if not isinstance(entry, dict):
            continue
        fields = {f: entry[f] for f in ("ready", "engineInstalled", "variants", "availableVariants",
                  "minReference", "maxReference", "languageOptions", "speakers", "speed") if f in entry}
        modes = entry.get("modes")
        if not isinstance(modes, list):
            modes = []
        mode_fields = ("id", "label", "reference", "transcript", "instruction", "languages", "variant", "speaker", "style", "emotion")
        fields["modes"] = [{f: mode[f] for f in mode_fields if f in mode}
                           for mode in modes if isinstance(mode, dict) and isinstance(mode.get("id"), str)]
        result[key] = fields
    return result


def safe_output_directory(value):
    path = explicit_path(value or os.environ.get("TD_AI_OUTPUT") or DEFAULT_OUTPUT)
    inspect_path(path, directory=True, create=True)
    return path


def export(client, job, api_path, directory, filename, media, kind):
    destination = directory / filename
    temp_path = None
    try:
        inspect_path(directory, directory=True)
        if os.path.lexists(destination):
            raise AgentError("output_exists", f"Existing artifact was not overwritten: {destination}")
        with client.request("GET", api_path) as response:
            response_type = (response.getheader("Content-Type") or "").partition(";")[0].strip().lower()
            type_options = ("audio/wav", "audio/x-wav", "audio/wave") if media == "audio/wav" else ("application/zip", "application/x-zip-compressed")
            if response_type not in type_options:
                raise AgentError("invalid_artifact", "The local service did not return the expected media type.")
            declared = response.getheader("Content-Length")
            if declared is not None and (not declared.isascii() or not declared.isdecimal() or
                                         len(declared) > 12 or not 0 < int(declared) <= MAX_ARTIFACT):
                raise AgentError("size_limit", "The returned artifact exceeds the allowed size.")
            with tempfile.NamedTemporaryFile(prefix=".td-agent-", suffix=".part", dir=directory, delete=False) as dest:
                temp_path = Path(dest.name)
                digest = hashlib.sha256()
                size = 0
                prefix = response.read(12)
                if media == "audio/wav":
                    if not wav_header(prefix) or not 44 <= int.from_bytes(prefix[4:8], "little") + 8 <= MAX_ARTIFACT:
                        raise AgentError("invalid_artifact", "The downloaded artifact is not a RIFF WAV.")
                elif not prefix.startswith((b"PK\x03\x04", b"PK\x05\x06", b"PK\x07\x08")):
                    raise AgentError("invalid_artifact", "The downloaded artifact is not a ZIP file.")
                block = prefix
                while block:
                    size += len(block)
                    if size > MAX_ARTIFACT:
                        raise AgentError("size_limit", "The returned artifact exceeds the allowed size.")
                    dest.write(block)
                    digest.update(block)
                    client.remaining()
                    block = response.read(1024 * 1024)
                if ((declared is not None and size != int(declared)) or
                        (media == "audio/wav" and size != int.from_bytes(prefix[4:8], "little") + 8) or
                        (media == "application/zip" and size < 22)):
                    raise AgentError("invalid_artifact", "The downloaded artifact is truncated or has an inconsistent length.")
                dest.flush()
                os.fsync(dest.fileno())
        inspect_path(directory, directory=True)
        if media == "application/zip" and not zipfile.is_zipfile(temp_path):
            raise AgentError("invalid_artifact", "The downloaded ZIP has no valid end-of-directory record.")
        try:
            # Windows rename is no-clobber. POSIX rename would overwrite;
            # an atomic hard-link from the same directory is used there.
            if os.name == "nt":
                os.rename(temp_path, destination)
            else:
                os.link(temp_path, destination)
        except FileExistsError as exc:
            raise AgentError("output_exists", f"Existing artifact was not overwritten: {destination}") from exc
        return {"path": str(destination), "type": media, "kind": kind,
                "sha256": digest.hexdigest(), "bytes": size, "job": job["id"]}
    except AgentError as exc:
        exc.details.setdefault("job", job)
        raise
    except OSError as exc:
        raise AgentError("export_failed", f"Could not safely write the artifact to {destination}.", job=job) from exc
    finally:
        if temp_path is not None:
            with contextlib.suppress(OSError):
                temp_path.unlink()


def optional_id(value, field):
    return checked_id(value, field) if value is not None else None


def synthesize(client, args):
    engine = engine_capabilities(client).get(args.engine)
    if not engine:
        raise AgentError("engine_unavailable", f"Engine {args.engine} is not available in the running Studio.")
    mode = next((item for item in engine["modes"] if item["id"] == args.mode), None)
    if not mode:
        raise AgentError("invalid_mode", f"Engine {args.engine} does not support mode {args.mode}.")
    if engine.get("ready") is False and not engine.get("availableVariants"):
        raise AgentError("engine_unavailable", f"Connect or install the {args.engine} engine and model in TimbreDeck first.")
    if bool(args.reference or args.voice_id) != bool(mode.get("reference")):
        raise AgentError("usage", "This mode requires --reference or --voice-id." if mode.get("reference") else "This mode does not use a reference voice.")
    if mode.get("instruction") and not (args.instruction or "").strip():
        raise AgentError("usage", "This mode requires --instruction.")
    if mode.get("speaker") and args.speaker not in engine.get("speakers", []):
        raise AgentError("usage", "This mode requires one of the speakers listed in capabilities.")
    if mode.get("languages") and args.language not in engine.get("languageOptions", []):
        raise AgentError("usage", "This language is not available for the selected engine.")
    if not (0.5 <= args.speed <= 2) or (not engine.get("speed") and args.speed != 1):
        raise AgentError("usage", "Speed must be 0.5 to 2 for supported engines, or 1 for native speed.")
    if not args.text.strip() or len(args.text.strip()) > 10000:
        raise AgentError("usage", "Text must contain 1 to 10,000 characters.")
    reference_language = args.reference_language
    if args.engine == "gpt-sovits" and reference_language not in (None, "auto") + TRAINING_LANGUAGES:
        raise AgentError("usage", "GPT-SoVITS reference language must be yue, zh, en, ja, ko, or auto.")
    emotion = mode.get("emotion")
    if (emotion == "audio") != bool(args.emotion_reference) or (emotion == "vector") != (args.emotion_weights is not None):
        raise AgentError("usage", "Use --emotion-reference only in emotion-audio mode and eight --emotion-weights only in emotion-vector mode.")
    if args.emotion_strength is not None and (not emotion or not 0 <= args.emotion_strength <= 1):
        raise AgentError("usage", "Emotion strength is only supported in emotion modes, in the range 0 to 1.")
    if args.emotion_weights is not None and not all(0 <= weight <= 1 for weight in args.emotion_weights):
        raise AgentError("usage", "All eight emotion weights must be in the range 0 to 1.")
    if args.transcript is not None and len(args.transcript.strip()) > 10000:
        raise AgentError("usage", "The reference transcript cannot exceed 10,000 characters.")
    if args.model_project is not None and args.engine != "gpt-sovits":
        raise AgentError("usage", "--model-project is supported only for GPT-SoVITS.")
    if args.model_project is not None and args.variant in ("v5turbo", "v5dev"):
        raise AgentError("usage", "GPT-SoVITS V5 uses base models only; fine-tuned v2Pro weights are incompatible.")
    voice_id = optional_id(args.voice_id, "Voice ID")
    project = optional_id(args.model_project, "Model project ID")
    if args.variant and ((mode.get("variant") and mode["variant"] != args.variant) or
                         (not mode.get("variant") and args.variant not in engine.get("variants", []))):
        raise AgentError("invalid_mode", "This variant is not available for the selected mode.")
    filenames = [p for p in (args.reference, args.emotion_reference) if p]
    data = input_wavs(filenames) if filenames else []
    client.ensure_idle()
    output_dir = safe_output_directory(args.output_dir)
    reference = ""
    transcript = (args.transcript or "").strip()
    if args.reference:
        item = client.upload(data[0])
        reference = item["id"]
        if (item["seconds"] < engine.get("minReference", 0) or
                (engine.get("maxReference") and item["seconds"] > engine["maxReference"])):
            raise AgentError("invalid_reference", "Reference WAV duration is outside this engine's supported range.")
    elif voice_id:
        profiles = client.json("GET", "/api/profiles")
        profile = next((entry for entry in profiles if isinstance(entry, dict) and entry.get("id") == voice_id), None) if isinstance(profiles, list) else None
        if not profile or not safe_id(profile.get("reference"), 64):
            raise AgentError("voice_not_found", "The requested voice profile was not found.")
        reference = profile["reference"]
        if mode.get("transcript") and not transcript and isinstance(profile.get("transcript"), str):
            transcript = profile["transcript"].strip()
        if reference_language is None and profile.get("language") in ("auto",) + TRAINING_LANGUAGES:
            reference_language = profile["language"]
    reference_language = reference_language or "auto"
    emotion_reference = client.upload(data[-1])["id"] if args.emotion_reference else None
    recognition = None
    if mode.get("transcript") and not transcript:
        recognition = client.recognize(reference, reference_language, args.asr_model)
        transcript = recognition["text"].strip()
        if not transcript:
            raise AgentError("empty_transcript", "No speech was recognized; supply an accurate --transcript or a clearer WAV.")
        if reference_language == "auto" and recognition["language"] in TRAINING_LANGUAGES:
            reference_language = recognition["language"]
    payload = {
        "engine": args.engine, "mode": args.mode, "text": args.text, "language": args.language,
        "refLang": reference_language, "transcript": transcript, "reference": reference,
        "instruction": args.instruction or "", "speed": args.speed, "preview": args.preview,
    }
    for k, value in (("voiceId", voice_id), ("variant", args.variant), ("modelProject", project),
                     ("speaker", args.speaker), ("emotionReference", emotion_reference),
                     ("emotionStrength", args.emotion_strength), ("emotionWeights", args.emotion_weights)):
        if value is not None:
            payload[k] = value
    if args.split_at:
        payload.update(splitAt=args.split_at,splitGapMs=args.split_gap_ms)
    if args.separate_parts:
        payload["splitSeparate"]=True
    job = client.submit("/api/synthesize", payload)
    artifact = export(client, job, "/api/audio/" + job["id"] + ("/parts" if args.separate_parts else ""), output_dir,
                      f"tts-{args.engine}-{job['id']}."+("zip" if args.separate_parts else "wav"),
                      "application/zip" if args.separate_parts else "audio/wav", "synthesis")
    result = {"job": job, "artifacts": [artifact]}
    if metadata := output_metadata(client, job):
        result["output"] = metadata
    if recognition is not None:
        result["recognition"] = recognition
    return result


def batch(client, args):
    if args.action == "clean":
        ensure_clean_mode(client, args.mode)
    else:
        if (args.clean_mode or args.no_split or args.no_transcribe or args.skip_unrecognized) and not has_action(client, "dataset-options"):
            raise AgentError("unsupported", "The running Studio does not advertise configurable dataset preparation.")
        if args.denoise and args.clean_mode not in (None, "denoise"):
            raise AgentError("usage", "Choose either --denoise (legacy) or --clean-mode.")
        if args.clean_mode:
            ensure_clean_mode(client, args.clean_mode)
    data = input_wavs(args.reference)
    if args.action == "clean" and not 0 <= args.gap_ms <= 3000:
        raise AgentError("usage", "--gap-ms must be 0 to 3000.")
    if args.action == "dataset" and (len(args.speaker.strip()) > 64 or
            "|" in args.speaker or any(ord(c) < 32 or 127 <= ord(c) < 160 for c in args.speaker)):
        raise AgentError("usage", "The speaker name may contain up to 64 characters, with no controls or pipes.")
    client.ensure_idle()
    output_dir = safe_output_directory(args.output_dir)
    ids = {hashlib.sha256(audio).hexdigest(): audio for audio in data}
    uploaded = {digest: client.upload(audio) for digest, audio in ids.items()}
    references = [uploaded[hashlib.sha256(audio).hexdigest()] for audio in data]
    duration = sum(item["seconds"] for item in references)
    if args.action == "clean":
        duration += args.gap_ms * (len(references) - 1) / 1000
    if duration > 180.001:
        raise AgentError("size_limit", "The selected WAVs and gaps exceed the 180-second limit.")
    payload = {"references": [item["id"] for item in references]}
    if args.action == "clean":
        payload.update(mode=args.mode, gapMs=args.gap_ms)
        job = client.submit("/api/clean", payload)
        artifact = export(client, job, "/api/clean/audio/" + job["id"], output_dir,
                          f"clean-{args.mode}-{job['id']}.wav", "audio/wav", "clean")
    else:
        payload.update(language=args.language, speaker=args.speaker, denoise=args.denoise)
        if args.clean_mode:
            payload["cleanMode"] = args.clean_mode
        if args.no_split:
            payload["split"] = False
        if args.no_transcribe:
            payload["transcribe"] = False
        if args.skip_unrecognized:
            payload["skipUnrecognized"] = True
        if args.asr_model:
            payload["asrModel"]=args.asr_model
        job = client.submit("/api/datasets/auto", payload)
        artifact = export(client, job, "/api/datasets/auto/" + job["id"] + "/download", output_dir,
                          f"dataset-{job['id']}.zip", "application/zip", "dataset")
    return {"job": job, "artifacts": [artifact]}


def connect(args):
    deadline = time.monotonic() + args.timeout
    try:
        endpoint, expected = discover(args.url)
        return Client(endpoint, expected, deadline - time.monotonic())
    except AgentError as first:
        if (not getattr(first, "recoverable", False) or args.url is not None
                or os.environ.get("TD_AGENT_AUTOSTART") != "1"
                or get_studio_data() != explicit_path(DEFAULT_DATA)):
            raise
        first.details["autostartAttempted"] = True
        try:
            executable = explicit_path(AUTOSTART_EXE)
            inspect_path(executable)
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise first
            launched = subprocess.run(
                [str(executable), "ensure-running"], cwd=str(executable.parent), shell=False,
                stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                creationflags=getattr(subprocess, "CREATE_NO_WINDOW", 0), timeout=min(20, remaining), check=False,
            )
        except (OSError, subprocess.SubprocessError) as start_failure:
            raise first from start_failure
        if launched.returncode != 0:
            first.details["autostartExitCode"] = launched.returncode
            raise first
        endpoint, expected = discover(None)
        return Client(endpoint, expected, deadline - time.monotonic())


def run(args):
    if not math.isfinite(args.timeout) or not 0.1 <= args.timeout <= 3600:
        raise AgentError("usage", "--timeout must be 0.1 to 3600 seconds.")
    client = connect(args)
    if args.action in ("status", "capabilities"):
        current = client.job()
        out = {
            "url": client.url, "instance": client.instance, "apiVersion": API_VERSION, "online": True,
            "status": "busy" if current and current["status"] in ACTIVE else "idle",
            "job": public_job(current) if current is not None else None,
        }
        build = client.info.get("build")
        if isinstance(build, dict) and isinstance(build.get("version"),str) and isinstance(build.get("id"),str):
            out["build"] = {"version":build["version"],"id":build["id"]}
        if args.action == "capabilities":
            out.update(engines=engine_capabilities(client), asrLanguages=list(ASR_LANGUAGES),
                       datasetLanguages=list(TRAINING_LANGUAGES), cleaningModes=clean_options(client),
                       inputFormats=sorted(x[1:] for x in INPUT_EXTENSIONS),
                       actions=["capabilities", "status", "asr", "synthesize", "dataset", "clean"])
            if isinstance(client.info.get("readyCleanModes"), list):
                out["readyCleaningModes"] = [m for m in clean_options(client) if m in client.info["readyCleanModes"]]
            if has_action(client, "dataset-options"):
                out["datasetOptions"] = {"cleanModes": [m for m in clean_options(client) if m in DATASET_CLEAN_MODES],
                                         "split": True, "transcribe": True, "skipUnrecognized": True, "training": False}
            if has_action(client, "resident-control"):
                out["actions"] += ["resident-status", "warm-model", "stop-model"]
            if has_action(client, "voice-metadata"):
                out["actions"].append("voices")
            if "sts" in client.info.get("actions",[]):
                out["actions"].append("sts")
                if "sts-models" in client.info.get("actions",[]):
                    out["stsModels"] = client.json("GET", "/api/sts/models")
        return out
    if args.action in ("resident-status", "warm-model", "stop-model"):
        return manage_resident(client, args)
    if args.action == "voices":
        if not has_action(client, "voice-metadata"):
            raise AgentError("unsupported", "The running Studio does not advertise voice metadata.")
        rows = client.json("GET", "/api/profiles")
        if not isinstance(rows, list):
            raise AgentError("invalid_response", "The service returned an invalid voice list.")
        return {"voices": [{k: p[k] for k in ("id", "name", "language") if k in p}
                           for p in rows if isinstance(p, dict) and safe_id(p.get("id"))]}
    if args.action == "sts":
        if "sts" not in client.info.get("actions",[]):
            raise AgentError("unsupported", "The running Studio does not advertise STS.")
        data=input_wavs([args.source,args.reference])
        if args.model and "sts-models" not in client.info.get("actions",[]):
            raise AgentError("unsupported", "This Studio version does not support selecting STS models.")
        client.ensure_idle()
        if args.model:
            models = client.json("GET", "/api/sts/models")
            if not any(m["id"] == args.model and m["ready"] for m in models):
                raise AgentError("unsupported", f"STS model {args.model} has not been installed and verified.")
        if args.streaming and (args.model != "seed-vc-tiny" or not any(m["id"] == args.model and m.get("mic") for m in models)):
            raise AgentError("unsupported", "Upstream streaming requires a compatible Seed-VC Tiny model.")
        output=safe_output_directory(args.output_dir)
        first,second=[client.upload(p) for p in data]
        payload = {"source":first["id"],"reference":second["id"]}
        if args.model:
            payload["model"] = args.model
        if args.streaming:
            payload["streaming"] = True
        job=client.submit("/api/sts",payload)
        artifact = export(client,job,"/api/audio/"+job["id"],output,
                          f"sts-{args.model or 'cosyvoice3'}-{job['id']}.wav","audio/wav","sts")
        metadata = output_metadata(client, job)
        return {"job":job,"artifacts":[artifact], **({"output": metadata} if metadata else {})}
    if args.action == "asr":
        data = input_wavs([args.reference])
        client.ensure_idle()
        reference = client.upload(data[0])
        return {"reference": reference["id"], **client.recognize(reference["id"], args.language,args.asr_model)}
    if args.action == "synthesize":
        return synthesize(client, args)
    return batch(client, args)


def create_parser():
    parser = AgentParser(description=__doc__)
    parser.add_argument("--url", help="A local HTTP origin; defaults to the discovered desktop workspace.")
    parser.add_argument("--timeout", type=float, default=600.0, help="Whole-command timeout in seconds (default 600).")
    sub = parser.add_subparsers(dest="action", required=True)
    sub.add_parser("status", help="Report service availability and the current job, without changing it.")
    sub.add_parser("capabilities", help="List engine, language, and model options.")
    sub.add_parser("voices", help="List IDs, names and languages for explicit synthesis only.")
    sub.add_parser("resident-status", help="Read the pinned TTS/STS model and busy state.")
    warm = sub.add_parser("warm-model", help="Preload an installed model without replacing a pinned model.")
    warm.add_argument("--kind", choices=("tts", "sts"), required=True)
    warm.add_argument("--engine", required=True)
    warm.add_argument("--variant")
    warm.add_argument("--model-project")
    stop = sub.add_parser("stop-model", help="Stop the exact confirmed resident model only when idle.")
    stop.add_argument("--resident-id", required=True)
    asr = sub.add_parser("asr", help="Transcribe an explicit WAV with local ASR.")
    asr.add_argument("--reference", required=True, help="Local WAV path.")
    asr.add_argument("--language", choices=ASR_LANGUAGES, default="auto")
    asr.add_argument("--asr-model")
    tts = sub.add_parser("synthesize", help="Synthesize speech and export a new WAV.")
    tts.add_argument("--engine", required=True, help="An engine ID from capabilities.")
    tts.add_argument("--mode", default="clone")
    tts.add_argument("--text", required=True)
    tts.add_argument("--language", default="auto")
    source = tts.add_mutually_exclusive_group()
    source.add_argument("--reference", help="Local WAV path (not a URL).")
    source.add_argument("--voice-id", help="Explicit saved voice profile ID.")
    tts.add_argument("--transcript", help="Verbatim reference words; mode-dependent ASR if omitted.")
    tts.add_argument("--reference-language", choices=ASR_LANGUAGES)
    tts.add_argument("--instruction")
    tts.add_argument("--variant")
    tts.add_argument("--speaker")
    tts.add_argument("--model-project")
    tts.add_argument("--speed", type=float, default=1.0)
    tts.add_argument("--emotion-reference")
    tts.add_argument("--emotion-strength", type=float)
    tts.add_argument("--emotion-weights", type=float, nargs=8)
    tts.add_argument("--preview", action="store_true")
    tts.add_argument("--split-at", type=int, default=0)
    tts.add_argument("--split-gap-ms", type=int, default=150)
    tts.add_argument("--separate-parts", action="store_true")
    tts.add_argument("--asr-model")
    tts.add_argument("--output-dir")
    sts = sub.add_parser("sts", help="Convert recorded speech to a reference voice; export WAV.")
    sts.add_argument("--source", required=True,help="Source speech whose words are retained; 1–25 seconds.")
    sts.add_argument("--reference", required=True,help="Target timbre reference; 2–20 seconds.")
    sts.add_argument("--model", choices=STS_MODELS,
                     help="Choose an installed STS model; default is CosyVoice 3.")
    sts.add_argument("--streaming", action="store_true", help="For Seed-VC Tiny, replay a file through the upstream realtime kernel.")
    sts.add_argument("--output-dir")
    clean = sub.add_parser("clean", help="Clean and combine WAVs in the supplied order.")
    clean.add_argument("--reference", action="append", required=True, help="Local WAV path; repeat for each input in order.")
    clean.add_argument("--mode", default="denoise", choices=CLEAN_MODES)
    clean.add_argument("--gap-ms", type=int, default=150)
    clean.add_argument("--output-dir")
    dataset = sub.add_parser("dataset", help="Build a reviewable training dataset ZIP from explicit WAVs.")
    dataset.add_argument("--reference", action="append", required=True, help="Local WAV path; repeat for each training input.")
    dataset.add_argument("--language", choices=TRAINING_LANGUAGES, required=True)
    dataset.add_argument("--speaker", "--name", dest="speaker", default="speaker-1")
    dataset.add_argument("--denoise", action="store_true")
    dataset.add_argument("--clean-mode", choices=DATASET_CLEAN_MODES)
    dataset.add_argument("--no-split", action="store_true")
    dataset.add_argument("--no-transcribe", action="store_true", help="Export unlabelled clips for manual review.")
    dataset.add_argument("--skip-unrecognized", action="store_true")
    dataset.add_argument("--asr-model")
    dataset.add_argument("--output-dir")
    return parser


def main(argv=None):
    action = None
    try:
        args = create_parser().parse_args(argv)
        action = args.action
        output = {"ok": True, "action": action, **run(args)}
        status = 0
    except AgentError as exc:
        output = {"ok": False, "action": action, "error": {"code": exc.code, "message": str(exc)}}
        if exc.details:
            output["error"]["details"] = exc.details
        status = 1
    except (OSError, ValueError) as exc:
        output = {"ok": False, "action": action, "error": {"code": "local_error", "message": str(exc)[:512]}}
        status = 1
    except KeyboardInterrupt:
        output = {"ok": False, "action": action, "error": {"code": "interrupted",
                  "message": "Stopped waiting; TimbreDeck's existing work was not cancelled."}}
        status = 130
    print(json.dumps(output, ensure_ascii=False, allow_nan=False, separators=(",", ":")))
    return status


if __name__ == "__main__":
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
    sys.stderr.reconfigure(encoding="utf-8", errors="replace")
    raise SystemExit(main())
