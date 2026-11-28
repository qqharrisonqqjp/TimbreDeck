"""Exercise real model selection, warm, PCM export and unloading via an isolated Studio."""
import argparse
from contextlib import contextmanager
from http.client import HTTPConnection
import hashlib
import io
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import time
import wave


@contextmanager
def request(port, path, value=None):
    conn = HTTPConnection("127.0.0.1", port, timeout=200)
    data = value if isinstance(value, bytes) or value is None else json.dumps(value)
    try:
        conn.request("GET" if value is None else "POST", "/api/" + path, data,
                     {"X-TimbreDeck": "1", "Content-Type": "application/octet-stream" if isinstance(value, bytes)
                      else "application/json"})
        yield conn.getresponse()
    finally:
        conn.close()


def main():
    p = argparse.ArgumentParser(description=__doc__)
    for path in ("exe", "assets", "source", "reference", "out"):
        p.add_argument(path, type=Path)
    p.add_argument("models", nargs="*")
    p.add_argument("--engines-config", type=Path)
    a = p.parse_args()
    a.out.mkdir(parents=True, exist_ok=True)
    work = Path(tempfile.mkdtemp(prefix="studio-", dir=a.out))
    if a.engines_config:
        (work / "config").mkdir()
        shutil.copy2(a.engines_config, work / "config/engines.json")
    with (a.out / "studio.log").open("wb") as log:
        proc = subprocess.Popen([a.exe, "-no-browser", "-data", work], stderr=log, stdout=subprocess.PIPE,
                                env=dict(os.environ, TIMBREDECK_STS_ASSETS=str(a.assets.resolve())), encoding="utf-8")
        try:
            line = proc.stdout.readline().strip()
            url = re.search(r"http://127\.0\.0\.1:(\d+)$", line)
            assert url, line
            port = int(url[1])

            def api(path, value=None, status=200):
                with request(port, path, value) as r:
                    data = r.read()
                    assert r.status == status, (path, r.status, data)
                    return json.loads(data) if r.headers.get_content_type() == "application/json" else data

            def completed(key):
                for _ in range(1800):
                    job = api("job")
                    assert job["id"] == key, job
                    if job["status"] not in ("running", "cancelling"):
                        assert job["status"] == "done", job
                        return
                    time.sleep(.1)
                raise TimeoutError(key)

            entries = api("sts/models")
            choices = {row["id"]: row for row in entries}
            assert list(choices) == ["cosyvoice3", "x-vc", "seed-vc-tiny", "meanvc2", "tvtsyn1"]
            inputs = [p.read_bytes() for p in (a.source, a.reference)]
            ids = [api("reference", data, 201)["id"] for data in inputs]
            payload = dict(source=ids[0], reference=ids[1])
            api("sts", dict(payload, model="unknown"), 422)
            report = dict(sha256=hashlib.sha256(a.exe.read_bytes()).hexdigest(),
                          choices=entries, isolated_data=str(work), runs=[])
            for model in a.models or [key for key in choices if choices[key]["ready"]]:
                assert choices[model]["ready"], choices
                api("sts/stop", {})
                t0 = time.monotonic()
                ack = api(f"sts/warm?model={model}", {}, 202)
                completed(ack["id"])
                preheat = time.monotonic() - t0
                assert api("sts/worker") == {"resident": True, "pinned": True, "model": model}
                before = time.monotonic()
                ack = api("sts", dict(payload, model=model), 202)
                key = ack["id"]
                with request(port, f"sts/{key}/stream") as r:
                    assert r.status == 200 and r.headers["X-Audio-Format"] == "pcm-s16le-mono", (r.status, r.read())
                    assert r.headers["X-Sample-Rate"] == "24000"
                    first = r.read(2)
                    t1 = time.monotonic()
                    pcm = first + r.read()
                completed(key)
                elapsed = time.monotonic() - before
                response = api(f"audio/{key}?download=1")
                with wave.open(io.BytesIO(response), "rb") as f:
                    assert f.readframes(f.getnframes()) == pcm and pcm and any(pcm)
                assert all(api("reference/" + key) == x for key, x in zip(ids, inputs))
                data = json.loads((work / "outputs" / key / "execution.json").read_text("utf-8"))
                assert data["engine"] == model and data["mode"] == "voice-convert"
                if not choices[model]["mic"]:
                    api(f"sts/live?model={model}", b"", 422)
                api("sts/stop", {})
                assert api("sts/worker") == {"resident": False, "pinned": False, "model": ""}
                name = a.out / f"{model}-api.wav"
                name.write_bytes(response)
                row = dict(model=model, preheat_s=preheat, first_pcm_s=t1 - before, elapsed_s=elapsed,
                           seconds=len(pcm) / 48000, wav=str(name.resolve()), pinned_and_unloaded=True,
                           exact_pcm_match=True, originals_preserved=True, diagnostics=data)
                report["runs"].append(row)
                (a.out / "api-benchmark.json").write_text(json.dumps(report, indent=2), encoding="utf-8")
                print(json.dumps({k: v for k, v in row.items() if k != "diagnostics"}), flush=True)
        finally:
            proc.terminate()
            proc.wait(timeout=15)


if __name__ == "__main__":
    main()
