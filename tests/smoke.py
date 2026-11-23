"""Exercise the built launcher via HTTP. Engine fixtures validate calls without weights."""
import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
import wave

MANDATORY = {
    "gpt-sovits": [
        "GPT_SoVITS/pretrained_models/v2Pro/s2Gv2Pro.pth",
        "GPT_SoVITS/pretrained_models/s1v3.ckpt",
        "GPT_SoVITS/pretrained_models/sv/pretrained_eres2netv2w24s4ep4.ckpt",
        "GPT_SoVITS/pretrained_models/chinese-roberta-wwm-ext-large/config.json",
        "GPT_SoVITS/pretrained_models/chinese-hubert-base/config.json",
    ],
    "cosyvoice3": [
        "third_party/Matcha-TTS/matcha/models/components/decoder.py",
        *("pretrained_models/Fun-CosyVoice3-0.5B/" + name for name in
          ("cosyvoice3.yaml", "llm.pt", "flow.pt", "hift.pt", "campplus.onnx",
           "speech_tokenizer_v3.onnx", "CosyVoice-BlankEN/config.json")),
    ],
}
GPT = """
class TTS_Config:
    default_configs = {"v2Pro": {}}
    def __init__(self, config): assert not config["custom"]["is_half"]
class TTS:
    def __init__(self, config): pass
    def run(self, args):
        if args["text"] == "失敗。":
            yield 16000, bytes(32000)
            raise RuntimeError("upstream failure after fallback audio")
        assert args["text"] == "第一句。" and args["text_lang"] == "yue"
        assert args["prompt_text"] == "參考。" and args["speed_factor"] == 1.25
        yield 24000, bytes([1, 0]) * 2400
"""
COSY = """
class Audio:
    def detach(self): return self
    def cpu(self): return self
    def reshape(self, n): return self
    def numpy(self): return bytes(4800)
class CosyVoice3:
    sample_rate = 24000
    def __init__(self, model_dir): assert model_dir.endswith("0.5B")
    def result(self, ref, options):
        assert ref.endswith(".wav") and options == {"stream": False, "speed": 1.25}
        return [{"tts_speech": Audio()}] * 2
    def inference_zero_shot(self, text, prompt, ref, **options):
        assert prompt == "You are a helpful assistant.<|endofprompt|>參考。"
        return self.result(ref, options)
    def inference_instruct2(self, text, prompt, ref, **options):
        assert prompt == "You are a helpful assistant. 請用廣東話。<|endofprompt|>"
        return self.result(ref, options)
    def inference_cross_lingual(self, text, ref, **options):
        assert text.startswith("You are a helpful assistant.<|endofprompt|>")
        return self.result(ref, options)
"""
SOUNDFILE = """
import wave
class SoundFile:
    def __init__(self, path, mode, samplerate, channels, subtype):
        self.f = wave.open(path, "wb")
        self.f.setparams((channels, 2, samplerate, 0, "NONE", "NONE"))
    def __enter__(self): return self
    def __exit__(self, *args): self.f.close()
    def write(self, data): self.f.writeframes(data)
def write(path, data, rate, subtype):
    with SoundFile(path, "w", rate, 1, subtype) as f: f.write(data)
"""


def file(path, data):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(data, encoding="utf-8")


def fixtures(root):
    for engine, files in MANDATORY.items():
        pkg = root / "runtimes" / engine
        for name in files:
            file(pkg / name, "fixture")
        target = pkg / "runtime" / "python.exe"
        target.parent.mkdir()
        if os.name == "nt":
            # The copied Windows interpreter needs the base stdlib and DLLs.
            shutil.copy(sys.executable, target)
            os.environ["PYTHONHOME"] = sys.base_prefix
            os.environ["PATH"] = sys.base_prefix + os.pathsep + os.environ["PATH"]
        else:
            target.symlink_to(sys.executable)
        file(pkg / "soundfile.py", SOUNDFILE)
        if engine == "cosyvoice3":
            file(pkg / "cosyvoice/cli/cosyvoice.py", COSY)
        else:
            file(pkg / "GPT_SoVITS/pretrained_models/fast_langdetect/lid.176.bin", "test fixture")
            file(pkg / "torch.py", "class cuda:\n    is_available = staticmethod(lambda: False)\n")
            file(pkg / "GPT_SoVITS/TTS_infer_pack/TTS.py", GPT)


def main():
    cli = argparse.ArgumentParser()
    cli.add_argument("binary", type=Path)
    args = cli.parse_args()
    with tempfile.TemporaryDirectory() as tmp:
        root = Path(tmp) / "聲台 demo"
        fixtures(root)
        runtime = subprocess.Popen([str(args.binary.resolve()), "-no-browser", "-data", str(root)],
                                   stdout=subprocess.PIPE, encoding="utf-8")
        try:
            base = runtime.stdout.readline().strip().split()[-1]
            def api(path, body=None, method=None):
                binary = isinstance(body, bytes)
                data = body if binary or body is None else json.dumps(body).encode()
                headers = {"X-TimbreDeck": "1", "Content-Type": "audio/wav" if binary else "application/json"}
                req = urllib.request.Request(base + path, data=data, headers=headers, method=method)
                with urllib.request.urlopen(req, timeout=10) as res:
                    return json.load(res) if res.headers.get_content_type() == "application/json" else res.read()
            assert "TimbreDeck" in api("/").decode()
            state = api("/api/state")
            assert len(state["engines"]) == 5
            assert all(state["engines"][k]["ready"] for k in MANDATORY)
            wav = io.BytesIO()
            with wave.open(wav, "wb") as out:
                out.setparams((1, 2, 16000, 0, "NONE", "NONE"))
                out.writeframes(bytes(96000))
            ref = api("/api/reference", wav.getvalue())["id"]
            assert ref == hashlib.sha256(wav.getvalue()).hexdigest()
            for engine, mode in [("gpt-sovits", "clone"), ("cosyvoice3", "clone"),
                                 ("cosyvoice3", "instruct"), ("cosyvoice3", "cross")]:
                request = dict(engine=engine, mode=mode, reference=ref, preview=True, text="第一句。第二句。",
                               transcript="參考。", instruction="請用廣東話。", speed=1.25, refLang="yue", language="yue")
                key = api("/api/synthesize", request)["id"]
                until = time.monotonic() + 20
                while time.monotonic() < until:
                    job = api("/api/job")
                    if job["status"] != "running":
                        break
                    time.sleep(.1)
                if job["status"] != "done":
                    log = root / "logs" / (key + ".log")
                    raise AssertionError((job, log.read_text(errors="replace") if log.exists() else ""))
                record = api("/api/history")[0]
                assert record["id"] == key and record["input"]["text"] == "第一句。"
                audio = api("/api/audio/" + key + "?download=1")
                with wave.open(io.BytesIO(audio)) as result:
                    assert result.getframerate() == 24000
                    assert result.getnframes() == (2400 if engine == "gpt-sovits" else 4800)
            failed = dict(request, engine="gpt-sovits", mode="clone", text="失敗。")
            api("/api/synthesize", failed)
            deadline = time.monotonic() + 20
            while api("/api/job")["status"] == "running" and time.monotonic() < deadline:
                time.sleep(.1)
            assert api("/api/job")["status"] == "failed"
            assert len(api("/api/history")) == 4
        finally:
            runtime.terminate()
            runtime.wait(timeout=15)
    print("Launcher HTTP, four adapter contracts, history and WAV export passed.")


if __name__ == "__main__":
    main()
