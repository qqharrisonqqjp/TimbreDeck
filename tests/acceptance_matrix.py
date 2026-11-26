"""Accept real, already-configured TimbreDeck engines; keep every submitted job and WAV."""
from argparse import ArgumentParser
from array import array
from contextlib import contextmanager
from datetime import datetime, timezone
from io import BytesIO
import hashlib
import json
import math
import os
from pathlib import Path
from queue import Empty, Queue
import re
import subprocess
import sys
from threading import Thread
import time
import urllib.error
import urllib.request
import wave


ENGINES = {
    "gpt-sovits": {"clone": "v2Pro"},
    "cosyvoice3": {mode: "Fun-CosyVoice3-0.5B" for mode in ("clone", "instruct", "cross")},
    "voxcpm2": {mode: "VoxCPM2" for mode in ("clone", "continue", "design")},
    "indextts2": {mode: "IndexTTS-2" for mode in ("clone", "emotion-audio", "emotion-text", "emotion-vector")},
    "qwen3-tts": {"clone": "Base", "simple-clone": "Base", "preset": "CustomVoice", "design": "VoiceDesign"},
}
CUDA_MODULES = {
    "gpt-sovits": ("t2s_model", "vits_model"),
    "cosyvoice3": ("llm", "flow", "hift"),
    "voxcpm2": ("tts",),
    "indextts2": ("gpt", "semantic_model", "semantic_codec", "s2mel", "bigvgan"),
    "qwen3-tts": ("tts", "tokenizer"),
}
TRANSCRIPT = {("gpt-sovits", "clone"), ("cosyvoice3", "clone"), ("voxcpm2", "continue"), ("qwen3-tts", "clone")}
WITHOUT_REFERENCE = {("voxcpm2", "design"), ("qwen3-tts", "preset"), ("qwen3-tts", "design")}
INSTRUCTIONS = {
    ("cosyvoice3", "instruct"): "Read with a natural, friendly tone.",
    ("voxcpm2", "design"): "A warm, composed female radio narrator with a measured pace.",
    ("indextts2", "emotion-text"): "Read with quiet confidence and warmth.",
    ("qwen3-tts", "design"): "A warm, composed female radio narrator with a measured pace.",
}
JOB_ID = re.compile(r"[0-9a-f]{32}\Z")
CUDA = re.compile(r"cuda:[0-9]+\Z")
LOOPBACK = re.compile(r"http://127\.0\.0\.1:([1-9][0-9]{0,4})\Z")
MAX_WAV = 128 << 20
TERMINAL = {"done", "failed", "cancelled"}


def select_cases(engines=None, modes=None):
    engines = list(dict.fromkeys(engines or ENGINES))
    modes = modes or ["all"]
    if any(e not in ENGINES for e in engines):
        raise ValueError("unknown engine")
    if "all" in modes:
        if len(modes) != 1:
            raise ValueError("use --mode all on its own")
        return [(e, m) for e in engines for m in ENGINES[e]]
    selected = set()
    for key in modes:
        if ":" in key:
            engine, mode = key.split(":", 1)
            pairs = [(engine, mode)] if engine in engines and mode in ENGINES[engine] else []
        else:
            pairs = [(e, key) for e in engines if key in ENGINES[e]]
        if not pairs:
            raise ValueError(f"no selected engine offers mode {key!r}")
        selected.update(pairs)
    return [(e, m) for e in engines for m in ENGINES[e] if (e, m) in selected]


def options(argv=None):
    p = ArgumentParser(description=__doc__)
    p.add_argument("--launcher", required=True, type=Path, help="new Start.exe file")
    p.add_argument("--data", required=True, type=Path, help="existing, configured Studio workspace; close it first")
    p.add_argument("--output", required=True, type=Path, help="new comparison directory (never overwrite an existing run)")
    p.add_argument("--reference", type=Path, help="local WAV; use 3–10 seconds when including GPT-SoVITS")
    p.add_argument("--transcript", help="exact transcript of --reference")
    p.add_argument("--ref-lang", choices=("auto", "yue", "zh", "en", "ja", "ko"), default="en")
    p.add_argument("--language", choices=("auto", "yue", "zh", "en", "ja", "ko"), default="en",
                   help="GPT-SoVITS output language; other engines may infer it")
    p.add_argument("--qwen-language", choices=("auto", "zh", "en", "ja", "ko", "de", "fr", "ru", "pt", "es", "it"),
                   default="auto", help="Qwen output language (no independent yue setting)")
    p.add_argument("--emotion-reference", type=Path, help="second local WAV, at most 15 seconds, for Index emotion-audio")
    p.add_argument("--text", default="Meet me at the station at half past eight tonight.")
    p.add_argument("--engine", action="append", choices=ENGINES,
                   help="repeat to select engines; default: all five")
    p.add_argument("--mode", action="append", help="all (default), a bare mode like clone, or ENGINE:MODE; repeat to combine")
    p.add_argument("--require-cuda", action="store_true", help="require Windows CUDA waveform, model, and Studio history evidence")
    p.add_argument("--timeout", type=int, default=600, help="job timeout in seconds (default: 600)")
    p.add_argument("--startup-timeout", type=int, default=30, help="launcher timeout in seconds (default: 30)")
    args = p.parse_args(argv)
    try:
        args.cases = select_cases(args.engine, args.mode)
    except ValueError as exc:
        p.error(str(exc))
    if not args.launcher.is_file() or not (args.data / "config").is_dir():
        p.error("--launcher must exist and --data must be an initialized Studio workspace with config/")
    if args.output.exists():
        p.error("--output must be a new directory")
    if not args.text.strip() or len(args.text.strip()) > 10000 or min(args.timeout, args.startup_timeout) <= 0:
        p.error("--text must contain 1–10000 characters and timeouts must be positive")
    if any(case not in WITHOUT_REFERENCE for case in args.cases) and not (args.reference and args.reference.is_file()):
        p.error("selected modes require --reference pointing to a local WAV")
    if any(case in TRANSCRIPT for case in args.cases) and not (args.transcript and args.transcript.strip()):
        p.error("selected modes require --transcript")
    if ("indextts2", "emotion-audio") in args.cases and not (args.emotion_reference and args.emotion_reference.is_file()):
        p.error("IndexTTS-2 emotion-audio requires --emotion-reference")
    return args


def payload(args, engine, mode, ref=None, emotion_ref=None):
    case = engine, mode
    request = {"engine": engine, "mode": mode, "variant": ENGINES[engine][mode],
               "text": args.text.strip(), "speed": 1, "preview": False}
    if case not in WITHOUT_REFERENCE:
        request["reference"] = ref
    if case in TRANSCRIPT:
        request["transcript"] = args.transcript.strip()
    if case in INSTRUCTIONS:
        request["instruction"] = INSTRUCTIONS[case]
    if engine == "gpt-sovits":
        request.update(language=args.language, refLang=args.ref_lang)
    elif engine == "qwen3-tts":
        request["language"] = args.qwen_language
        if mode == "preset":
            request["speaker"] = "Ryan"
    elif engine == "indextts2" and mode.startswith("emotion-"):
        request["emotionStrength"] = 0.7
        if mode == "emotion-audio":
            request["emotionReference"] = emotion_ref
        elif mode == "emotion-vector":
            request["emotionWeights"] = [0.7, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0]
    return request


def validate_loopback(url):
    match = LOOPBACK.fullmatch(url)
    if not match or int(match[1]) > 65535:
        raise ValueError(f"unexpected launcher URL: {url!r}")
    return url


class APIError(RuntimeError):
    def __init__(self, route, status, detail):
        super().__init__(f"{route}: HTTP {status}: {detail}")
        self.status = status


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class Loopback:
    def __init__(self, url):
        self.url = validate_loopback(url)
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())

    def __call__(self, route, payload=None, expect=200, binary=False, timeout=20):
        if not re.fullmatch(r"[a-z]+(?:/[0-9a-f]{32})?", route):
            raise ValueError("invalid API route")
        headers = {"Origin": self.url, "X-TimbreDeck": "1"}
        if isinstance(payload, bytes):
            headers["Content-Type"] = "audio/wav"
        elif payload is not None:
            headers["Content-Type"] = "application/json"
            payload = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        req = urllib.request.Request(f"{self.url}/api/{route}", data=payload, headers=headers)
        try:
            with self.opener.open(req, timeout=timeout) as res:
                if res.status != expect:
                    raise APIError(route, res.status, f"expected {expect}")
                if binary and res.headers.get_content_type() not in ("audio/wav", "audio/x-wav", "audio/wave"):
                    raise ValueError(f"{route}: expected WAV response, got {res.headers.get_content_type()}")
                limit = MAX_WAV if binary else 8 << 20
                data = res.read(limit + 1)
                if len(data) > limit:
                    raise ValueError(f"{route}: response exceeds {limit} bytes")
        except urllib.error.HTTPError as exc:
            with exc:
                raise APIError(route, exc.code, exc.read(4096).decode("utf-8", "replace")) from exc
        return data if binary else json.loads(data)


@contextmanager
def start_studio(args, output):
    env = dict(os.environ)
    env.update({"HTTP_PROXY": "http://127.0.0.1:9", "HTTPS_PROXY": "http://127.0.0.1:9",
                "ALL_PROXY": "http://127.0.0.1:9", "NO_PROXY": "127.0.0.1,localhost",
                "HF_HUB_OFFLINE": "1", "TRANSFORMERS_OFFLINE": "1", "PYTHONDONTWRITEBYTECODE": "1"})
    cmd = [str(args.launcher.resolve()), "-no-browser", "-data", str(args.data.resolve())]
    kwargs = {"creationflags": subprocess.CREATE_NO_WINDOW} if os.name == "nt" else {}
    with (output / "launcher.stderr.log").open("wb") as err, (output / "launcher.stdout.log").open("w", encoding="utf-8") as log:
        proc = subprocess.Popen(cmd, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=err,
                                env=env, encoding="utf-8", errors="replace", **kwargs)
        first = Queue(maxsize=1)

        def pump():
            line = proc.stdout.readline()
            first.put(line)
            if line:
                log.write(line)
                log.flush()
                for line in proc.stdout:
                    log.write(line)
                    log.flush()

        reader = Thread(target=pump, daemon=True)
        reader.start()
        try:
            try:
                first_line = first.get(timeout=args.startup_timeout).strip()
            except Empty as exc:
                raise TimeoutError("launcher did not announce a loopback endpoint") from exc
            if not first_line:
                raise RuntimeError("launcher closed stdout before announcing an endpoint; inspect launcher.stderr.log")
            yield Loopback(first_line.split()[-1])
        finally:
            if proc.poll() is None:
                proc.terminate()
                try:
                    proc.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    proc.kill()
                    proc.wait(timeout=10)
            reader.join(timeout=5)
            proc.stdout.close()


def import_reference(api, path, max_sec=None, min_sec=None):
    if path.stat().st_size > 32 << 20:
        raise ValueError(f"reference exceeds Studio's 32 MiB limit: {path}")
    audio = path.read_bytes()
    sha = hashlib.sha256(audio).hexdigest()
    info = api("reference", audio, expect=201)
    sec = info.get("seconds")
    if info.get("id") != sha or not isinstance(sec, (float, int)) or not math.isfinite(sec) or sec <= 0:
        raise ValueError("Studio did not return valid evidence for the exact local reference bytes")
    if (min_sec is not None and sec < min_sec) or (max_sec is not None and sec > max_sec):
        raise ValueError(f"reference duration {sec:.3f}s outside selected mode bounds: {min_sec}–{max_sec}s")
    return {"file": str(path.resolve()), "id": sha, "seconds": sec}


class JobOwnershipError(RuntimeError):
    """Stop the matrix rather than touch a job that this run did not create."""


class JobDeadline(TimeoutError):
    """The submitted job reached a known terminal state after its deadline."""


def check_job(row, engine, key=None):
    if (not isinstance(row, dict) or not JOB_ID.fullmatch(str(row.get("id", "")))
            or row.get("kind") != "synthesis" or row.get("engine") != engine
            or (key is not None and row["id"] != key)):
        raise JobOwnershipError(f"unexpected synthesis job identity: {row!r}")
    return row


def wait_job(api, engine, key, timeout, interval=0.5):
    deadline = time.monotonic() + timeout
    while True:
        row = check_job(api("job"), engine, key)
        state = row.get("status")
        if state in TERMINAL:
            return row
        if state not in ("running", "cancelling"):
            raise JobOwnershipError(f"unexpected job state: {state!r}")
        if time.monotonic() >= deadline:
            row = stop_owned(api, engine, key, row, interval)
            raise JobDeadline(f"job {key} exceeded {timeout}s; terminal state: {row['status']}")
        time.sleep(min(interval, max(0, deadline - time.monotonic())))


def stop_owned(api, engine, key, row=None, interval=0.5):
    deadline = time.monotonic() + 30
    try:
        if row is None:
            row = check_job(api("job", timeout=5), engine, key)
        if row.get("status") == "running":
            try:
                check_job(api("cancel/" + key, {}, timeout=5), engine, key)
            except APIError as exc:
                if exc.status != 409:
                    raise
        while row.get("status") not in TERMINAL:
            if row.get("status") not in ("running", "cancelling") or time.monotonic() >= deadline:
                raise JobOwnershipError(f"owned job {key} did not stop after cancellation")
            time.sleep(interval)
            row = check_job(api("job", timeout=5), engine, key)
        return row
    except (OSError, APIError, ValueError) as exc:
        raise JobOwnershipError(f"could not confirm that owned job {key} stopped") from exc


def measure(audio):
    with wave.open(BytesIO(audio)) as wav:
        if wav.getsampwidth() != 2 or wav.getnchannels() != 1:
            raise ValueError("expected mono PCM16 WAV")
        rate, frames = wav.getframerate(), wav.getnframes()
        raw = wav.readframes(frames)
    if rate < 8000 or rate > 384000 or len(raw) != frames * 2 or frames < 2:
        raise ValueError("invalid or truncated WAV data")
    samples = array("h", raw)
    if sys.byteorder != "little":
        samples.byteswap()
    low, high = min(samples), max(samples)
    if low == high:
        raise ValueError("empty, silent, or constant waveform")
    return {"rate_hz": rate, "channels": 1, "bits": 16, "frames": frames, "seconds": frames / rate,
            "peak_pcm16": max(abs(low), abs(high)), "rms_pcm16": math.sqrt(sum(x*x for x in samples) / frames),
            "sha256": hashlib.sha256(audio).hexdigest(), "bytes": len(audio)}


def verify_execution(details, engine, mode, key, require_cuda=False):
    if not isinstance(details, dict) or (details.get("job"), details.get("engine"), details.get("mode")) != (key, engine, mode):
        raise ValueError("execution sidecar does not match the completed job")
    if not isinstance(details.get("model"), str) or not details["model"]:
        raise ValueError("missing execution model identity")
    if not require_cuda:
        return None
    if details.get("platform") != "win32" or not details.get("torch_version") or not details.get("cuda_version"):
        raise ValueError("missing Windows PyTorch CUDA runtime evidence")
    devices = details.get("waveform_devices")
    if not isinstance(devices, list) or len(devices) != 1 or not CUDA.fullmatch(str(devices[0])):
        raise ValueError(f"expected one CUDA waveform device; got {devices!r}")
    device, = devices
    modules = details.get("module_devices") or {}
    for name in CUDA_MODULES[engine]:
        if modules.get(name) != devices:
            raise ValueError(f"{name} does not share the waveform's CUDA device")
    name = (details.get("cuda_devices") or {}).get(device, {}).get("name")
    if not isinstance(name, str) or not name:
        raise ValueError("missing CUDA device name")
    return f"{device} · {name}"


def ready_mode(state, engine, mode, variant):
    v = (state.get("engines") or {}).get(engine, {})
    if not v.get("engineInstalled") or not v.get("ready") or variant not in (v.get("availableVariants") or []):
        raise ValueError(f"{engine}/{variant} not ready; missing: {v.get('missing', [])}")
    caps = next((m for m in v.get("modes", []) if m.get("id") == mode), None)
    case = engine, mode
    if (not caps or (caps.get("variant") not in (None, "", variant))
            or bool(caps.get("reference")) != (case not in WITHOUT_REFERENCE)
            or bool(caps.get("transcript")) != (case in TRANSCRIPT)
            or bool(caps.get("instruction")) != (case in INSTRUCTIONS)
            or bool(caps.get("languages")) != (engine in ("gpt-sovits", "qwen3-tts"))
            or bool(caps.get("speaker")) != (case == ("qwen3-tts", "preset"))
            or (caps.get("emotion") or "") != (mode.removeprefix("emotion-") if mode.startswith("emotion-") else "")):
        raise ValueError("Studio mode contract does not match this acceptance matrix")


def save_report(out, report):
    path = out / "comparison.json"
    temp = path.with_suffix(".json.tmp")
    temp.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    temp.replace(path)


def run_case(api, args, out, state, report, row):
    engine, mode = row["engine"], row["mode"]
    ready_mode(state, engine, mode, row["variant"])
    ref, emo = report.get("reference"), report.get("emotion_reference")
    req = payload(args, engine, mode, ref and ref["id"], emo and emo["id"])
    row["input"] = req
    current = api("job")
    if current is not None and current.get("status") not in TERMINAL:
        raise JobOwnershipError("Studio already has an active job")
    try:
        accepted = check_job(api("synthesize", req, expect=202), engine)
    except APIError as exc:
        if exc.status == 409:
            raise JobOwnershipError(str(exc)) from exc
        raise
    key = accepted["id"]
    if any(x is not row and x.get("job") == key for x in report["results"]):
        raise JobOwnershipError("Studio reused a previous job ID")
    row.update(job=key, accepted=accepted, status="submitted")
    try:
        save_report(out, report)
        terminal = wait_job(api, engine, key, args.timeout)
    except (JobOwnershipError, JobDeadline):
        raise
    except (OSError, APIError, ValueError, KeyboardInterrupt):
        row["terminal"] = stop_owned(api, engine, key)
        raise
    row["terminal"] = terminal
    if terminal["status"] != "done":
        raise ValueError(terminal.get("detail") or terminal["status"])
    audio = api("audio/" + key, binary=True)
    row.update(measure(audio))
    if row["sha256"] in {item["id"] for item in (ref, emo) if item}:
        raise ValueError("returned audio is byte-identical to a reference")
    job_dir = args.data.resolve() / "outputs" / key
    if audio != (job_dir / "speech.wav").read_bytes():
        raise ValueError("HTTP WAV differs from the Studio job's stored WAV")
    dest = out / f"{engine}-{mode}.wav"
    with dest.open("xb") as f:
        f.write(audio)
    row["wav"] = str(dest)
    evidence_path = job_dir / "execution.json"
    sidecar = json.loads(evidence_path.read_text(encoding="utf-8"))
    row.update(execution=str(evidence_path), execution_data=sidecar)
    verified_cuda = verify_execution(sidecar, engine, mode, key, args.require_cuda)
    history = next((r for r in api("history") if r.get("id") == key), None)
    if (not history or history.get("model") != sidecar["model"] or not history.get("audioAvailable")
            or history.get("sampleRate") != row["rate_hz"] or history.get("seconds") != row["seconds"]
            or any(history.get("input", {}).get(k) != v for k, v in req.items())):
        raise ValueError("Studio history does not match the job input, execution model, and WAV")
    row["record"] = history
    if verified_cuda is not None and history.get("verifiedCuda") != verified_cuda:
        raise ValueError("Studio history did not independently report the same verified CUDA device")
    row.update(status="generated", cuda_status="passed" if args.require_cuda else "not-required")


def main(argv=None):
    args = options(argv)
    out = args.output.resolve()
    out.mkdir(parents=True, exist_ok=False)
    report = {"schema_version": 1, "utc": datetime.now(timezone.utc).isoformat(), "launcher": str(args.launcher.resolve()),
              "data": str(args.data.resolve()), "require_cuda": args.require_cuda, "text": args.text.strip(),
              "reference_language": args.ref_lang, "output_language": args.language, "qwen_language": args.qwen_language,
              "results": [{"engine": e, "mode": m, "variant": ENGINES[e][m], "status": "not-run"} for e, m in args.cases]}
    save_report(out, report)
    try:
        with start_studio(args, out) as api:
            state = api("state")
            current = api("job")
            if current is not None and current.get("status") not in TERMINAL:
                raise JobOwnershipError("Studio already has an active job")
            required = {e for e, m in args.cases if (e, m) not in WITHOUT_REFERENCE}
            if required:
                max_sec = min([x for e, x in (("gpt-sovits", 10), ("cosyvoice3", 30), ("indextts2", 15)) if e in required], default=None)
                report["reference"] = import_reference(api, args.reference, max_sec, 3 if "gpt-sovits" in required else None)
            if ("indextts2", "emotion-audio") in args.cases:
                report["emotion_reference"] = import_reference(api, args.emotion_reference, 15)
            for row in report["results"]:
                t = time.monotonic()
                try:
                    run_case(api, args, out, state, report, row)
                except JobOwnershipError as exc:
                    row.update(status="failed", error=str(exc))
                    raise
                except (OSError, ValueError, RuntimeError, TypeError, KeyError, AttributeError, wave.Error) as exc:
                    row.update(status="failed", error=str(exc))
                finally:
                    row["elapsed_seconds"] = round(time.monotonic() - t, 3)
                    save_report(out, report)
                    print(f"{row['engine']}:{row['mode']} {row['status']}: {row.get('error', row.get('job', ''))}", flush=True)
    except KeyboardInterrupt:
        report["run_error"] = "interrupted"
        save_report(out, report)
        return 130
    except (OSError, ValueError, RuntimeError, TypeError, AttributeError) as exc:
        report["run_error"] = str(exc)
        print(f"acceptance matrix: {exc}", file=sys.stderr)
    save_report(out, report)
    print(out / "comparison.json", flush=True)
    return int(bool(report.get("run_error")) or any(r["status"] != "generated" for r in report["results"]))


if __name__ == "__main__":
    sys.exit(main())

