"""Exercise real engine installations through Start.exe in an isolated workspace."""
import argparse
from array import array
import hashlib
import io
import json
from pathlib import Path
import subprocess
import sys
import time
import urllib.error
import urllib.request
import wave


def measure(audio):
    with wave.open(io.BytesIO(audio)) as wav:
        if wav.getsampwidth() != 2 or wav.getnchannels() != 1:
            raise ValueError("expected mono PCM16")
        rate = wav.getframerate()
        samples = array("h", wav.readframes(wav.getnframes()))
    if sys.byteorder != "little":
        samples.byteswap()
    if not samples or max(samples) == min(samples):
        raise ValueError("empty or constant output")
    return dict(rate_hz=rate, seconds=len(samples) / rate,
                peak=max(abs(v) for v in samples), sha256=hashlib.sha256(audio).hexdigest())


def main():
    cli = argparse.ArgumentParser(description=__doc__)
    cli.add_argument("launcher", type=Path)
    cli.add_argument("reference", type=Path, help="3–10 second Cantonese WAV")
    cli.add_argument("transcript", help="exact words in the reference")
    cli.add_argument("destination", type=Path, help="new directory for this run")
    cli.add_argument("--gpt", type=Path, help="complete GPT-SoVITS v2Pro root")
    cli.add_argument("--cosy", type=Path, help="complete CosyVoice 3 root")
    cli.add_argument("--text", default="今晚八點半喺旺角站等。")
    cli.add_argument("--timeout", type=int, default=1200, help="seconds per engine")
    args = cli.parse_args()
    engines = {k: p for k, p in (("gpt-sovits", args.gpt), ("cosyvoice3", args.cosy)) if p}
    if not engines or args.timeout <= 0:
        cli.error("select at least one engine and a positive timeout")
    audio = args.reference.read_bytes()
    target = args.destination.resolve()
    target.mkdir(parents=True, exist_ok=False)
    report = dict(reference_sha256=hashlib.sha256(audio).hexdigest(), text=args.text, engines={})
    launcher = subprocess.Popen([str(args.launcher.resolve()), "-no-browser", "-data", str(target)],
                                stdout=subprocess.PIPE, encoding="utf-8")
    try:
        url = launcher.stdout.readline().strip().split()[-1]
        def api(path, data=None):
            payload = data if data is None or isinstance(data, bytes) else json.dumps(data).encode()
            request = urllib.request.Request(url + "/api/" + path, data=payload,
                headers={"X-TimbreDeck": "1", "Content-Type": "application/octet-stream" if isinstance(data, bytes) else "application/json"})
            try:
                with urllib.request.urlopen(request, timeout=30) as res:
                    if res.headers.get_content_type() == "application/json":
                        return json.load(res)
                    return res.read()
            except urllib.error.HTTPError as exc:
                raise RuntimeError(exc.read().decode(errors="replace")) from exc

        ref = api("reference", audio)
        if len(engines) == 2 and not 3 <= ref["seconds"] <= 10:
            raise ValueError("the shared reference must be 3–10 seconds for GPT-SoVITS")
        for engine, root in engines.items():
            start = time.monotonic()
            entry = report["engines"][engine] = dict(status="failed")
            try:
                api("engine", dict(engine=engine, path=str(root.resolve())))
                req = dict(engine=engine, mode="clone", reference=ref["id"], preview=False,
                           text=args.text, transcript=args.transcript, speed=1,
                           refLang="yue", language="yue")
                key = api("synthesize", req)["id"]
                entry["job"] = key
                try:
                    while True:
                        job = api("job")
                        if job["id"] != key:
                            raise RuntimeError("job identity changed")
                        if job["status"] == "done":
                            break
                        if job["status"] != "running":
                            raise RuntimeError(job.get("detail", job["status"]))
                        if time.monotonic() - start >= args.timeout:
                            raise TimeoutError(engine)
                        time.sleep(1)
                finally:
                    current = api("job")
                    if current and current["id"] == key and current["status"] == "running":
                        api("cancel/" + key, {})
                        cancel_deadline = time.monotonic() + 30
                        while api("job")["status"] == "cancelling":
                            if time.monotonic() >= cancel_deadline:
                                raise TimeoutError("engine did not stop after cancellation")
                            time.sleep(.1)
                wav = api("audio/" + key + "?download=1")
                entry.update(measure(wav))
                if entry["sha256"] == report["reference_sha256"]:
                    raise ValueError("output is identical to the reference")
                (target / (engine + ".wav")).write_bytes(wav)
                entry["status"] = "generated"
            except Exception as exc:
                entry["error"] = str(exc)
            finally:
                entry["elapsed_seconds"] = round(time.monotonic() - start, 2)
                (target / "comparison.json").write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding="utf-8")
                print(engine, entry, flush=True)
    finally:
        launcher.terminate()
        launcher.wait(timeout=15)
    return int(any(e["status"] != "generated" for e in report["engines"].values()))


if __name__ == "__main__":
    sys.exit(main())
