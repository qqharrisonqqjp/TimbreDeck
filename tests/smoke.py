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

from acceptance import read_execution, require_windows_cuda

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
    "voxcpm2": [
        "src/voxcpm/core.py",
        *("pretrained_models/VoxCPM2/" + name for name in
          ("config.json", "tokenizer.json", "model.safetensors", "audiovae.safetensors")),
    ],
    "indextts2": [
        *("checkpoints/" + name for name in
          ("config.yaml", "gpt.pth", "s2mel.pth", "bpe.model", "wav2vec2bert_stats.pt",
           "feat1.pt", "feat2.pt", "qwen0.6bemo4-merge/config.json", "qwen0.6bemo4-merge/model.safetensors")),
        *(f"checkpoints/hf_cache/models--{repo}/refs/main" for repo in
          ("facebook--w2v-bert-2.0", "amphion--MaskGCT", "funasr--campplus",
           "nvidia--bigvgan_v2_22khz_80band_256x")),
    ],
    "qwen3-tts": [
        "qwen_tts/inference/qwen3_tts_model.py",
        *(f"pretrained_models/Qwen3-TTS-12Hz-1.7B-{variant}/{name}"
          for variant in ("Base", "CustomVoice")
          for name in ("config.json", "model.safetensors", "generation_config.json", "tokenizer_config.json",
                       "speech_tokenizer/config.json", "speech_tokenizer/model.safetensors")),
    ],
}
DEVICE_FIXTURE = """
from types import SimpleNamespace

class Module:
    def __init__(self, device):
        self.device = device
    def parameters(self):
        return [SimpleNamespace(device=self.device)]
"""
GPT = DEVICE_FIXTURE + """
class TTS_Config:
    default_configs = {"v2Pro": {}}
    def __init__(self, config): assert not config["custom"]["is_half"]
class TTS:
    def __init__(self, config):
        self.t2s_model = Module("cpu")
        self.vits_model = Module("cpu")
        self.vits_model.decode = lambda: SimpleNamespace(device="cpu")
        self.sv_model = None
    def run(self, args):
        if args["text"] == "失敗。":
            yield 16000, bytes(32000)
            raise RuntimeError("upstream failure after fallback audio")
        assert args["text"] == "第一句。" and args["text_lang"] == "yue"
        assert args["prompt_text"] == "參考。" and args["speed_factor"] in (1, 1.25)
        self.vits_model.decode()
        yield 24000, bytes([1, 0, 2, 0]) * 1200
"""
COSY = DEVICE_FIXTURE + """
class Audio:
    device = "cuda:0"
    def detach(self): return self
    def cpu(self): return self
    def reshape(self, n): return self
    def numpy(self): return bytes(4800)
class CosyVoice3:
    sample_rate = 24000
    def __init__(self, model_dir):
        assert model_dir.endswith("0.5B")
        self.model = SimpleNamespace(llm=Module("cuda:0"), flow=Module("cuda:0"),
                                     hift=Module("cuda:0"), token2wav=Audio)
        self.frontend = SimpleNamespace()
    def result(self, ref, options):
        assert ref.endswith(".wav") and options == {"stream": False, "speed": 1.25}
        return [{"tts_speech": self.model.token2wav()} for _ in range(2)]
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
TORCH = """
from types import SimpleNamespace
__version__ = "fixture"
version = SimpleNamespace(cuda=None)
float32 = "float32"
class cuda:
    is_available = staticmethod(lambda: False)
"""
VOX = DEVICE_FIXTURE + """
class VoxCPM:
    @classmethod
    def from_pretrained(cls, path, **kwargs):
        assert path == "pretrained_models/VoxCPM2"
        assert kwargs == dict(load_denoiser=False, local_files_only=True, optimize=False)
        model = cls()
        model.tts_model = Module("cpu")
        model.tts_model.sample_rate = 48000
        model.tts_model.audio_vae = SimpleNamespace(decode=lambda: SimpleNamespace(device="cpu"))
        return model
    def generate(self, text, **kwargs):
        if text.startswith("(清朗)"):
            assert text == "(清朗)第一句。"
            assert not kwargs or kwargs["reference_wav_path"].endswith(".wav")
        else:
            assert kwargs["prompt_wav_path"] == kwargs["reference_wav_path"]
            assert kwargs["prompt_text"] == "參考。"
        self.tts_model.audio_vae.decode()
        return bytes([1, 0, 2, 0]) * 100
"""
INDEX = DEVICE_FIXTURE + """
class Samples(bytes):
    def reshape(self, size): return self
class IndexTTS2:
    def __init__(self, **kwargs):
        assert kwargs == dict(use_cuda_kernel=False, use_deepspeed=False)
        for name in ("gpt", "semantic_model", "semantic_codec", "s2mel", "bigvgan"):
            setattr(self, name, Module("cpu"))
        self.bigvgan.forward = lambda: SimpleNamespace(device="cpu")
    def infer(self, spk_audio_prompt, text, output_path, **kwargs):
        assert spk_audio_prompt.endswith(".wav") and output_path is None
        assert text == "第一句。"
        if kwargs:
            if "emo_audio_prompt" in kwargs:
                assert kwargs["emo_audio_prompt"] != spk_audio_prompt
                assert kwargs["emo_audio_prompt"].endswith(".wav")
                assert kwargs["emo_alpha"] == 0
            elif "emo_text" in kwargs:
                assert kwargs == dict(use_emo_text=True, emo_text="愉快", emo_alpha=1)
            else:
                assert kwargs == dict(emo_vector=[0.2] + [0] * 7, emo_alpha=1)
        self.bigvgan.forward()
        return 22050, Samples(bytes([1, 0, 2, 0]) * 100)
"""
QWEN = DEVICE_FIXTURE + """
class Qwen3TTSModel:
    @classmethod
    def from_pretrained(cls, path, **kwargs):
        assert kwargs == dict(device_map="cpu", dtype="float32",
                              attn_implementation="sdpa", local_files_only=True)
        model = cls()
        model.variant = path.rsplit("-", 1)[1]
        model.model = Module("cpu")
        return model
    def result(self, text, language):
        assert text == "第一句。" and language == "Chinese"
        return [bytes([1, 0, 2, 0]) * 100], 24000
    def generate_voice_clone(self, ref_audio, ref_text, x_vector_only_mode, **kwargs):
        assert self.variant == "Base" and ref_audio.endswith(".wav")
        assert ref_text == (None if x_vector_only_mode else "參考。")
        return self.result(**kwargs)
    def generate_custom_voice(self, speaker, instruct, **kwargs):
        assert self.variant == "CustomVoice" and speaker == "Vivian" and instruct == "節奏稍慢"
        return self.result(**kwargs)
    def generate_voice_design(self, instruct, **kwargs):
        assert self.variant == "VoiceDesign" and instruct == "清朗"
        return self.result(**kwargs)
"""
CUDA_TORCH = TORCH + """
version.cuda = "fixture"
cuda.is_available = staticmethod(lambda: True)
cuda.get_device_name = staticmethod(lambda device: "fixture GPU")
cuda.max_memory_allocated = staticmethod(lambda device: 4096)
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
            file(pkg / "torch.py", CUDA_TORCH)
            file(pkg / "cosyvoice/cli/cosyvoice.py", COSY)
        elif engine == "gpt-sovits":
            file(pkg / "GPT_SoVITS/pretrained_models/fast_langdetect/lid.176.bin", "test fixture")
            file(pkg / "torch.py", TORCH)
            file(pkg / "GPT_SoVITS/TTS_infer_pack/TTS.py", GPT)
        else:
            adapter_file, adapter = {
                "voxcpm2": ("src/voxcpm/__init__.py", VOX),
                "indextts2": ("indextts/infer_v2.py", INDEX),
                "qwen3-tts": ("qwen_tts/__init__.py", QWEN),
            }[engine]
            file(pkg / "torch.py", TORCH)
            file(pkg / adapter_file, adapter)


def expect_value_error(fn, *args):
    try:
        fn(*args)
    except ValueError:
        return
    raise AssertionError(f"{fn.__name__} accepted invalid evidence")


def check_execution(root, key, engine, mode):
    details = read_execution(root, key, engine, mode)
    assert details["platform"] == sys.platform
    expect_value_error(read_execution, root, key, "another-engine", mode)
    assert read_execution(root, "0" * 32, engine, mode) is None
    expect_value_error(require_windows_cuda, None)
    windows = dict(details, platform="win32")
    if engine == "gpt-sovits":
        assert details["waveform_devices"] == ["cpu"]
        expect_value_error(require_windows_cuda, windows)
    else:
        assert details["waveform_devices"] == ["cuda:0"]
        assert details["cuda_devices"]["cuda:0"]["peak_allocated_bytes"] == 4096
        require_windows_cuda(windows)
        expect_value_error(require_windows_cuda, dict(windows, platform="linux"))
        expect_value_error(require_windows_cuda, dict(windows, module_devices={}))
        expect_value_error(require_windows_cuda, dict(windows, waveform_devices=["cuda:0", "cpu"]))
    path = root / "outputs" / key / "execution.json"
    file(path, json.dumps(dict(details, job="another-job")))
    expect_value_error(read_execution, root, key, engine, mode)
    file(path, json.dumps(details))


def check_acceptance(binary, root, reference):
    ref_path = root / "reference.wav"
    ref_path.write_bytes(reference)
    for strict in (False, True):
        target = root / ("cuda-acceptance" if strict else "cpu-acceptance")
        command = [
            sys.executable, str(Path(__file__).with_name("acceptance.py")),
            str(binary.resolve()), str(ref_path), "參考。", str(target),
            "--gpt", str(root / "runtimes" / "gpt-sovits"),
            "--text", "第一句。", "--timeout", "20",
        ]
        if strict:
            command.append("--require-cuda")
        run = subprocess.run(command, capture_output=True, timeout=30)
        assert run.returncode == int(strict), (run.stdout, run.stderr)
        report = json.loads((target / "comparison.json").read_text(encoding="utf-8"))
        gpt = report["engines"]["gpt-sovits"]
        assert gpt["status"] == "generated" and gpt["execution"]["job"] == gpt["job"]
        assert gpt.get("cuda_status") == ("failed" if strict else None), gpt
        assert (target / "gpt-sovits.wav").is_file()


def expect_http_error(api, code, path, data):
    try:
        api(path, data)
    except urllib.error.HTTPError as err:
        assert err.code == code, err.read()
    else:
        raise AssertionError(f"{path} accepted an invalid request")


def check_new_engines(api, root, ref, emotion):
    profile = api("/api/profiles", dict(name="粵語角色", reference=ref, transcript="參考。", language="yue"))
    voice = profile["id"]
    assert api("/api/profiles") == [profile]
    common = dict(text="第一句。", speed=1, language="zh")
    cases = [
        ("voxcpm2", "clone", 48000, dict(voiceId=voice, instruction="清朗")),
        ("voxcpm2", "continue", 48000, dict(voiceId=voice)),
        ("voxcpm2", "design", 48000, dict(instruction="清朗")),
        ("indextts2", "clone", 22050, dict(reference=ref)),
        ("indextts2", "emotion-audio", 22050, dict(reference=ref, emotionReference=emotion, emotionStrength=0)),
        ("indextts2", "emotion-text", 22050, dict(reference=ref, instruction="愉快")),
        ("indextts2", "emotion-vector", 22050, dict(reference=ref, emotionWeights=[0.2] + [0] * 7)),
        ("qwen3-tts", "clone", 24000, dict(voiceId=voice)),
        ("qwen3-tts", "simple-clone", 24000, dict(reference=ref)),
        ("qwen3-tts", "preset", 24000, dict(speaker="Vivian", instruction="節奏稍慢")),
        ("qwen3-tts", "design", 24000, dict(instruction="清朗")),
    ]
    design = dict(common, engine="qwen3-tts", mode="design", instruction="清朗")
    expect_http_error(api, 409, "/api/synthesize", design)
    expect_http_error(api, 422, "/api/synthesize", dict(design, variant="Base"))
    expect_http_error(api, 422, "/api/synthesize", dict(design, instruction=""))
    expect_http_error(api, 422, "/api/synthesize", dict(common, engine="indextts2",
                      mode="emotion-vector", reference=ref, emotionWeights=[0.2]))
    for name in ("config.json", "model.safetensors", "generation_config.json", "tokenizer_config.json",
                 "speech_tokenizer/config.json", "speech_tokenizer/model.safetensors"):
        file(root / "runtimes/qwen3-tts/pretrained_models/Qwen3-TTS-12Hz-1.7B-VoiceDesign" / name, "fixture")
    for engine, mode, rate, options in cases:
        payload = dict(common, engine=engine, mode=mode, **options)
        key = api("/api/synthesize", payload)["id"]
        until = time.monotonic() + 20
        while time.monotonic() < until:
            job = api("/api/job")
            if job["status"] != "running":
                break
            time.sleep(.1)
        if job["status"] != "done":
            raise AssertionError((job, (root / "logs" / (key + ".log")).read_text(errors="replace")))
        record = api("/api/history")[0]
        assert record["id"] == key and record["sampleRate"] == rate
        if options.get("voiceId"):
            assert record["voiceName"] == profile["name"] and record["input"]["reference"] == ref
        with wave.open(io.BytesIO(api("/api/audio/" + key + "?download=1"))) as output:
            assert output.getframerate() == rate and output.getnframes() == 200
    api("/api/profiles/" + voice, dict(profile, name="新名稱"), method="PUT")
    assert api("/api/profiles")[0]["name"] == "新名稱"
    api("/api/profiles/" + voice, method="DELETE")
    assert not api("/api/profiles") and api("/api/reference/" + ref)
    assert any(x.get("voiceName") == "粵語角色" for x in api("/api/history"))


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
            gpt_modes = state["engines"]["gpt-sovits"]["modes"]
            assert [(m["id"], m["reference"], m["transcript"], m["languages"])
                    for m in gpt_modes] == [("clone", True, True, True)]
            cosy_modes = state["engines"]["cosyvoice3"]["modes"]
            assert [(m["id"], m["reference"], m["transcript"], m["instruction"])
                    for m in cosy_modes] == [
                ("clone", True, True, False), ("instruct", True, False, True),
                ("cross", True, False, False),
            ]
            assert state["engines"]["qwen3-tts"]["variants"] == ["Base", "CustomVoice", "VoiceDesign"]
            assert state["engines"]["qwen3-tts"]["availableVariants"] == ["Base", "CustomVoice"]
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
                required = "transcript" if mode == "clone" else "instruction" if mode == "instruct" else None
                if required:
                    try:
                        api("/api/synthesize", dict(request, **{required: ""}))
                    except urllib.error.HTTPError as err:
                        assert err.code == 422
                    else:
                        raise AssertionError(f"{engine}/{mode} accepted missing {required}")
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
                check_execution(root, key, engine, mode)
            failed = dict(request, engine="gpt-sovits", mode="clone", text="失敗。")
            api("/api/synthesize", failed)
            deadline = time.monotonic() + 20
            while api("/api/job")["status"] == "running" and time.monotonic() < deadline:
                time.sleep(.1)
            assert api("/api/job")["status"] == "failed"
            assert len(api("/api/history")) == 4
            emotion_audio = bytearray(wav.getvalue())
            emotion_audio[-1] = 1
            emotion = api("/api/reference", bytes(emotion_audio))["id"]
            check_new_engines(api, root, ref, emotion)
        finally:
            runtime.terminate()
            runtime.wait(timeout=15)
        check_acceptance(args.binary, root, wav.getvalue())
    print("Launcher HTTP, 15 synthesis modes, profiles, history, WAV export and CUDA acceptance checks passed.")


if __name__ == "__main__":
    main()
