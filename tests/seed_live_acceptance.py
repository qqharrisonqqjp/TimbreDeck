"""Real Windows CUDA Seed-VC Tiny: warmed file stream and paced mic HTTP replay."""
import argparse
from concurrent.futures import ThreadPoolExecutor
from hashlib import sha256
import io
import json
import math
import os
from pathlib import Path
import re
import subprocess
import tempfile
import time
import uuid
import wave

from sts_models_api import request


def mono_wav(pcm, rate):
    buf = io.BytesIO()
    with wave.open(buf, "wb") as f:
        f.setparams((1, 2, rate, 0, "NONE", ""))
        f.writeframes(pcm)
    return buf.getvalue()


def main():
    p = argparse.ArgumentParser(description=__doc__)
    for name in ("exe", "assets", "source", "reference", "output"):
        p.add_argument(name, type=Path)
    a = p.parse_args()
    a.output.mkdir(parents=True, exist_ok=True)
    work = Path(tempfile.mkdtemp(prefix="isolated-", dir=a.output))
    def digest(b):
        return sha256(b).hexdigest()
    original = [x.read_bytes() for x in (a.source, a.reference)]
    model = "seed-vc-tiny"
    with (a.output / "studio.log").open("w", encoding="utf-8") as log:
        server = subprocess.Popen([a.exe, "-no-browser", "-data", work], stdout=subprocess.PIPE, stderr=log,
                                  text=True, encoding="utf-8",
                                  env=dict(os.environ, TIMBREDECK_STS_ASSETS=str(a.assets.resolve())))
        try:
            port = int(re.search(r"http://127\.0\.0\.1:(\d+)$", server.stdout.readline().strip())[1])
            def api(path, data=None, status=200):
                with request(port, path, data) as r:
                    body = r.read()
                    assert r.status == status, (path, r.status, body)
                    return json.loads(body) if r.headers.get_content_type() == "application/json" else body

            def wait(ack):
                for _ in range(1800):
                    j = api("job")
                    assert ack["id"] == j["id"], j
                    if j["status"] not in ("running", "cancelling"):
                        assert j["status"] == "done", j
                        return
                    time.sleep(.1)
                raise TimeoutError(ack)

            assert next(x for x in api("sts/models") if x["id"] == model)["mic"]
            source, ref = [api("reference", b, 201)["id"] for b in original]
            begin = time.perf_counter()
            wait(api("sts/warm?model=" + model, {}, 202))
            warm = time.perf_counter() - begin
            worker = api("sts/worker")
            assert worker == {"resident": True, "pinned": True, "model": model}, worker
            print(f"PREHEAT {warm:.3f}s; pinned={worker['pinned']}", flush=True)
            begin = time.perf_counter()
            ack = api("sts", {"model": model, "source": source, "reference": ref, "streaming": True}, 202)
            with request(port, f"sts/{ack['id']}/stream") as r:
                assert r.status == 200, r.read()
                first = r.read(2)
                first_seconds = time.perf_counter() - begin
                assert api("job")["status"] == "running", "first packet arrived after work completed"
                pcm = first + r.read()
            wait(ack)
            total = time.perf_counter() - begin
            data = api(f"audio/{ack['id']}?download=1")
            with wave.open(io.BytesIO(data)) as f:
                seconds = f.getnframes() / f.getframerate()
                assert f.readframes(f.getnframes()) == pcm
            diag = json.loads((work / "outputs" / ack["id"] / "execution.json").read_text("utf-8"))
            assert diag["streaming"] and diag["blocks"] >= 75 and all(
                diag["module_devices"][k] == ["cuda:0"] for k in ("converter", "decoder", "speaker"))
            file = a.output / "Seed-VC-Tiny-2B-EN-streamed.wav"
            file.write_bytes(data)
            print(f"FILE {diag['blocks']} blocks; first={first_seconds:.3f}s; total={total:.3f}s", flush=True)

            src = subprocess.check_output(["ffmpeg", "-v", "error", "-i", str(a.source),
                                           "-ac", "1", "-ar", "16000", "-f", "s16le", "-"])
            assert abs(len(src) / 32000 - seconds) < .015
            block = 9600
            session = uuid.uuid4().hex
            url = f"sts/live?model={model}&reference={ref}&session={session}&seq="
            api(url + "0", mono_wav(bytes(8000), 16000), 422)
            # Simulates microphone arrival in real time, independent of inference.
            # A single sender preserves block order, like the browser.
            n = math.ceil((len(src) + 3840) / block)
            t0 = time.perf_counter()
            def send(i, released, audio):
                start = time.perf_counter()
                with request(port, url + str(i), mono_wav(audio.ljust(block, b"\0"), 16000)) as r:
                    assert r.status == 200 and r.headers["X-Audio-Format"] == "pcm-s16le-mono", (r.status, r.read())
                    payload = r.read()
                    delta = time.perf_counter() - start
                    assert len(payload) == 14400, len(payload)
                return payload, delta, time.perf_counter() - released

            with ThreadPoolExecutor(1) as pool:
                pending = []
                for i in range(n):
                    when = t0 + (i + 1) * .3
                    time.sleep(max(0, when - time.perf_counter()))
                    pending.append(pool.submit(send, i, when, src[i * block:(i + 1) * block]))
                packets = [f.result(timeout=10) for f in pending]
            assert any(b"\0" * len(x[0]) != x[0] for x in packets)
            latencies = sorted(x[2] for x in packets)
            p95 = latencies[math.ceil(n * .95) - 1]
            assert p95 <= .3, f"p95 {p95:.3f}s exceeded block duration"
            mic = a.output / "Seed-VC-Tiny-2B-EN-mic-replay.wav"
            # Remove the kernel's leading 120ms context and the replay padding.
            raw = b"".join(x[0] for x in packets)[5760:]
            mic.write_bytes(mono_wav(raw[:len(pcm)], 24000))
            assert [x.read_bytes() for x in (a.source, a.reference)] == original
            assert api("sts/worker") == worker
            api("sts/stop", {})
            assert not api("sts/worker")["resident"]
            report = dict(
                sha256=digest(a.exe.read_bytes()), inputs=[digest(x) for x in original],
                warm_s=warm, file_first_s=first_seconds, file_total_s=total,
                duration_s=seconds, file=str(file.resolve()), mic=str(mic.resolve()),
                file_sha256=digest(file.read_bytes()), mic_sha256=digest(mic.read_bytes()),
                mic_blocks=n, mic_p95_after_capture_s=p95, mic_max_after_capture_s=max(latencies),
                mic_first_after_capture_s=packets[0][2], released_and_unloaded=True,
                exact_file_pcm_match=True, isolated_work=str(work), diagnostics=diag)
            (a.output / "seed-live.json").write_text(json.dumps(report, indent=2), "utf-8")
            print(f"MIC {n} paced blocks; p95={p95:.3f}s; max={max(latencies):.3f}s; unloaded=true", flush=True)
        finally:
            server.terminate()
            server.wait(timeout=12)


if __name__ == "__main__":
    main()
