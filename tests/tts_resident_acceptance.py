"""Run real Windows TTS warm/switch/stop and verify reuse in an isolated workspace."""
from hashlib import sha256
import io
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import time
import wave

from sts_models_api import request


def main():
    exe, configuration, reference, out = map(Path, sys.argv[1:5])
    selected = sys.argv[5:]
    out.mkdir(parents=True, exist_ok=True)
    data = Path(tempfile.mkdtemp(prefix="tts-", dir=out))
    (data / "config").mkdir()
    shutil.copy2(configuration, data / "config/engines.json")
    # Selected packs live outside engines.json. Use the existing immutable
    # fixture weights without a multi-GB copy; this test never edits packs.
    library = os.getenv("TD_TEST_MODEL_LIBRARY")
    if library:
        for engine in ("qwen3-tts", "voxcpm2"):
            source = Path(library) / engine
            if source.is_dir():
                shutil.copytree(source, data / "models" / engine, copy_function=os.link)
    r = reference.read_bytes()
    with (out / "studio.log").open("wb") as logs:
        process = subprocess.Popen([exe, "-no-browser", "-data", data], stdout=subprocess.PIPE, stderr=logs,
                                   encoding="utf-8")
        try:
            url = process.stdout.readline().strip()
            port = int(re.search(r"http://127\.0\.0\.1:(\d+)$", url)[1])

            def api(path, value=None, code=200):
                with request(port, path, value) as resp:
                    b = resp.read()
                    assert resp.status == code, (path, resp.status, b)
                    return json.loads(b) if resp.headers.get_content_type() == "application/json" else b

            def wait(ack):
                for _ in range(2400):
                    j = api("job")
                    assert j["id"] == ack["id"], j
                    if j["status"] not in ("running", "cancelling"):
                        assert j["status"] == "done", j
                        return
                    time.sleep(.1)
                raise TimeoutError(ack)

            fixtures = [
                ("gpt-sovits", "v2Pro", "clone"),
                ("cosyvoice3", "Fun-CosyVoice3-0.5B", "cross"),
                ("qwen3-tts", "Base", "simple-clone"),
                ("voxcpm2", "VoxCPM2", "clone"),
                ("indextts2", "IndexTTS-2", "clone"),
            ]
            ref = api("reference", r, 201)["id"]
            assert api("tts/resident") == {"busy": False}
            api("tts/warm", {"engine": "unknown"}, 422)
            resident = {}
            rows = []
            meta = dict(exe_sha256=sha256(exe.read_bytes()).hexdigest(), workspace=str(data), runs=rows)
            for engine, variant, mode in fixtures:
                if selected and engine not in selected:
                    continue
                args = dict(engine=engine, variant=variant)
                if resident:
                    api("tts/warm", args, 409)
                    assert api("tts/resident") == resident
                    api("tts/warm", dict(args, replace="old-id"), 409)
                    assert api("tts/resident") == resident
                    args["replace"] = resident["id"]
                before = time.perf_counter()
                j = api("tts/warm", args, 202)
                wait(j)
                loaded = time.perf_counter() - before
                active = api("tts/resident")
                assert active["kind"] == "tts" and active["engine"] == engine and active["variant"] == variant
                assert active["id"] != resident.get("id")
                resident = active
                api("tts/stop", {"id": "invalid"}, 409)
                api("sts", {}, 409)
                run = dict(engine=engine, variant=variant, load_s=loaded, outputs=[])
                for n in range(2 if engine == "gpt-sovits" else 1):
                    payload = dict(engine=engine, variant=variant, mode=mode, reference=ref, language="en",
                                   refLang="en", transcript="This local reference voice is a synthetic test fixture.",
                                   text="May the Force be with you." if n == 0 else "This is a second synthesis test.",
                                   speed=1.0)
                    t = time.perf_counter()
                    job = api("synthesize", payload, 202)
                    wait(job)
                    wav = api(f"audio/{job['id']}?download=1")
                    with wave.open(io.BytesIO(wav), "rb") as f:
                        assert f.getnframes() and any(f.readframes(f.getnframes()))
                    diag = json.loads((data / "outputs" / job["id"] / "execution.json").read_text("utf-8"))
                    assert diag["resident_reused"] is True and "cuda:0" in diag["waveform_devices"], diag
                    assert api("tts/resident") == resident
                    path = out / f"{engine}-warm-{n+1}.wav"
                    path.write_bytes(wav)
                    run["outputs"].append(dict(file=str(path.resolve()), seconds=time.perf_counter()-t,
                                               sha256=sha256(wav).hexdigest(), execution=diag))
                rows.append(run)
                (out / "tts-resident.json").write_text(json.dumps(meta, indent=2), "utf-8")
                print(engine, "PASS", f"load {loaded:.3f}s", [round(x["seconds"], 3) for x in run["outputs"]], flush=True)
            assert rows
            api("tts/stop", {"id": resident["id"]})
            assert api("tts/resident") == {"busy": False}
            meta.update(unloaded=True, unchanged_input=reference.read_bytes()==r)
            (out / "tts-resident.json").write_text(json.dumps(meta, indent=2), "utf-8")
            print("STOP PASS", flush=True)
        finally:
            process.terminate()
            process.wait(timeout=15)


if __name__ == "__main__":
    main()
