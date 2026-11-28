"""Measure one resident model on the supplied synthetic or user-selected files."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import struct
import subprocess
import time
import wave


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("model")
    p.add_argument("assets", type=Path)
    p.add_argument("source", type=Path)
    p.add_argument("reference", type=Path)
    p.add_argument("out", type=Path)
    p.add_argument("--second-source", type=Path)
    p.add_argument("--runs", type=int, default=3)
    a = p.parse_args()
    a.out.mkdir(parents=True, exist_ok=True)
    offline = dict(os.environ, HF_HUB_OFFLINE="1", TRANSFORMERS_OFFLINE="1",
                   PYTHONDONTWRITEBYTECODE="1", PYTHONUTF8="1")
    worker = Path(__file__).resolve().parents[1] / "studio" / "sts_model_worker.py"
    command = [os.fspath(Path(os.sys.executable)), "-u", os.fspath(worker), a.model, os.fspath(a.assets.resolve())]
    results = []
    with (a.out / "worker.log").open("wb") as log:
        proc = subprocess.Popen(command, stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=log, env=offline)
        try:
            for index, sample in enumerate([a.source] * a.runs + ([a.second_source] if a.second_source else [])):
                path = a.out.resolve() / f"run-{index + 1}"
                req = dict(id=str(index + 1), sourceSpeech=str(sample.resolve()), ref=str(a.reference.resolve()),
                           output=str(path) + ".wav", diagnostics=str(path) + ".json")
                before = time.monotonic()
                proc.stdin.write((json.dumps(req) + "\n").encode())
                proc.stdin.flush()
                first, pcm = None, bytearray()
                while True:
                    header = proc.stdout.read(5)
                    if len(header) != 5:
                        raise RuntimeError("Worker ended; see worker.log")
                    tag, size = struct.unpack("<cI", header)
                    if tag == b"D":
                        break
                    if tag == b"E":
                        raise RuntimeError("Worker failed; see worker.log")
                    assert tag == b"P" and 0 < size <= (4 << 20)
                    block = proc.stdout.read(size)
                    assert len(block) == size
                    if first is None:
                        first = time.monotonic() - before
                        assert struct.unpack("<4sI", block[:8]) == (b"TD01", 24000)
                        block = block[8:]
                    pcm.extend(block)
                elapsed = time.monotonic() - before
                with wave.open(req["output"], "rb") as f:
                    assert (f.getframerate(), f.getnchannels(), f.getsampwidth()) == (24000, 1, 2)
                    seconds = f.getnframes() / f.getframerate()
                    assert pcm and f.readframes(f.getnframes()) == pcm
                assert any(pcm)
                assert hashlib.sha256(sample.read_bytes()).digest() != hashlib.sha256(Path(req["output"]).read_bytes()).digest()
                data = json.loads(Path(req["diagnostics"]).read_text(encoding="utf-8"))
                assert all(data["module_devices"].get(k) == ["cuda:0"] for k in ("converter", "decoder", "speaker"))
                result = dict(input=str(sample.resolve()), reference=req["ref"], first_pcm_s=first, elapsed_s=elapsed,
                              seconds=seconds, rtf=elapsed / seconds, output=req["output"],
                              sha256=hashlib.sha256(Path(req["output"]).read_bytes()).hexdigest(), diagnostics=data)
                results.append(result)
                (a.out / "benchmark.json").write_text(json.dumps(results, indent=2), encoding="utf-8")
                print(json.dumps(result), flush=True)
        finally:
            proc.stdin.close()
            try:
                code = proc.wait(timeout=20)
            except subprocess.TimeoutExpired:
                proc.kill()
                raise
            if code != 0:
                print(f"Worker exit {code}; see {a.out / 'worker.log'}")


if __name__ == "__main__":
    main()
