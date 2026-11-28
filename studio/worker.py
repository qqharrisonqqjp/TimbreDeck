"""Synthesis worker using the engine's own portable Python."""
import json
import contextlib
import os
from pathlib import Path
import sys
import struct
import time

boot_time = time.perf_counter()
observers = []


def remember_method(target, name, wrapper):
    observers.append((target, name, target.__dict__.get(name), name in target.__dict__))
    setattr(target, name, wrapper)


def resident_model(q, load):
    cache = q.get("_cache")
    if cache is None:
        model = load()
        q["_model_ready"]()
        return model
    if "model" not in cache:
        cache["model"] = load()
    q["_model_ready"]()
    return cache["model"]


def observe_output(target, name, devices):
    """Capture the waveform device before the engine copies audio to the CPU."""
    method = getattr(target, name, None)
    if method is None:
        return

    def observed(*args, **kwargs):
        samples = method(*args, **kwargs)
        device = getattr(samples, "device", None)
        devices.add(str(device) if device is not None else "unknown")
        return samples

    remember_method(target, name, observed)


def execution_details(torch, model, modules, waveform_devices):
    details = dict(
        platform=sys.platform, model=model, torch_version=str(torch.__version__),
        cuda_version=torch.version.cuda, waveform_devices=sorted(waveform_devices),
        module_devices={name: sorted({str(p.device) for p in module.parameters()})
                        for name, module in modules.items() if module is not None},
    )
    devices = {}
    for device in sorted(waveform_devices):
        if device.startswith("cuda:"):
            devices[device] = dict(name=torch.cuda.get_device_name(device))
            try:
                devices[device]["peak_allocated_bytes"] = torch.cuda.max_memory_allocated(device)
            except (AttributeError, RuntimeError) as exc:
                print(f"PyTorch peak memory unavailable: {exc}", file=sys.stderr)
    details["cuda_devices"] = devices
    return details


def observe_tokenizer_decode(tokenizer_model, devices):
    """Observe the real decoded waveform(s) before Qwen converts them to NumPy."""
    method = getattr(tokenizer_model, "decode", None)
    if method is None:
        return

    def observed(*args, **kwargs):
        result = method(*args, **kwargs)
        value = getattr(result, "audio_values", None)
        waves = value if isinstance(value, (list, tuple)) else (value,)
        for wave in waves:
            device = getattr(wave, "device", None)
            devices.add(str(device) if device is not None else "unknown")
        return result

    remember_method(tokenizer_model, "decode", observed)


def gpt(q, ref, output, _emotion_ref):
    variant = q.get("variant") or "v2Pro"
    v5 = variant in ("v5turbo", "v5dev")
    if v5:
        from tools.portable_runtime import activate
        activate()
    import soundfile as sf
    import torch
    from GPT_SoVITS.TTS_infer_pack.TTS import TTS, TTS_Config

    # The pinned GPT source sets this detector during import. Its large model
    # normally downloads on first use. Select the bundled compact model offline.
    base = Path(q["modelRoot"]) / "GPT_SoVITS/pretrained_models"
    detector_model = base / "fast_langdetect/lid.176.bin"
    if q["modelRoot"] or not detector_model.is_file():
        import fast_langdetect
        small = Path(fast_langdetect.__file__).parent / "resources" / "lid.176.ftz"
        detector = fast_langdetect.LangDetectConfig(custom_model_path=str(detector_model if detector_model.is_file() else small))
        fast_langdetect.infer._default_detector = fast_langdetect.LangDetector(detector)

    gpu = torch.cuda.is_available()
    if v5 and not gpu:
        raise RuntimeError("GPT-SoVITS V5 requires an NVIDIA CUDA runtime in this Windows build")
    weights = dict(TTS_Config.default_configs[variant], device="cuda" if gpu else "cpu", is_half=gpu)
    pipeline = TTS
    if q["modelRoot"]:
        if v5:
            class SplitV5(TTS):
                def init_vocoder(self, version):
                    # The pinned upstream loader resolves the shared vocoder via its
                    # engine directory. Adapt only this call for standalone model packs.
                    scope = TTS.init_vocoder.__globals__
                    previous = scope["now_dir"]
                    try:
                        scope["now_dir"] = q["modelRoot"]
                        return super().init_vocoder(version)
                    finally:
                        scope["now_dir"] = previous
            pipeline = SplitV5
        else:
            import sv
            sv.sv_path = str(base / "sv/pretrained_eres2netv2w24s4ep4.ckpt")
        weights.update(t2s_weights_path=str(base / "s1v3.ckpt"),
                       vits_weights_path=str(base / (f"gsv-v5-pretrained/s2G{variant}.pth" if v5 else "v2Pro/s2Gv2Pro.pth")),
                       cnhuhbert_base_path=str(base / "chinese-hubert-base"),
                       bert_base_path=str(base / "chinese-roberta-wwm-ext-large"))
    trained = q.get("trained")
    if trained:
        if v5:
            raise ValueError("v2Pro fine-tuned weights are incompatible with GPT-SoVITS V5")
        if not all(Path(trained[key]).is_file() for key in ("gpt", "sovits")):
            raise ValueError("missing fine-tuned model file")
        weights.update(t2s_weights_path=trained["gpt"], vits_weights_path=trained["sovits"])
    config = TTS_Config({"custom": weights})
    # Upstream saves its settings during model loading. Keep those job-local.
    config.configs_path = str(Path(output).with_name("inference.yaml"))
    synth = resident_model(q, lambda: pipeline(config))
    if v5 and (synth.configs.version != variant or synth.vocoder is None):
        raise ValueError("the loaded GPT-SoVITS model is not the requested V5 variant")
    if q.get("_prepare"):
        return
    waveform_devices = set()
    if v5:
        observe_output(synth.vocoder, "forward", waveform_devices)
    else:
        observe_output(synth.vits_model, "decode", waveform_devices)
    # In non-streaming mode upstream produces one batch. Exhaust it so an
    # exception raised after a fallback silence buffer reaches the launcher.
    (rate, samples), = synth.run(dict(
        text=q["text"], text_lang=q["language"], ref_audio_path=ref,
        prompt_lang=q["refLang"], prompt_text=q["transcript"],
        speed_factor=q["speed"], text_split_method="cut5", return_fragment=False,
    ))
    if not any(memoryview(samples).cast("B")):
        raise ValueError("GPT-SoVITS returned silence")
    sf.write(output, samples, rate, subtype="PCM_16")
    modules = {name: getattr(synth, name, None) for name in
               ("t2s_model", "vits_model", "bert_model", "cnhuhbert_model")}
    if v5:
        modules["vocoder"] = synth.vocoder
    else:
        modules["speaker_embedding"] = getattr(synth.sv_model, "embedding_model", None)
    return execution_details(torch, "v2Pro (fine-tuned)" if trained else variant, modules, waveform_devices)


def load_cosy_cpu(model, llm_path, flow_path, hift_path):
    """Load the official float32 checkpoints without allocating duplicate tensors."""
    import torch

    for module, path in ((model.llm, llm_path), (model.flow, flow_path), (model.hift, hift_path)):
        state = torch.load(path, weights_only=True, map_location="cpu", mmap=True)
        if module is model.hift:
            state = {key.removeprefix("generator."): value for key, value in state.items()}
        module.load_state_dict(state, strict=True, assign=True)
        module.eval()


def cosy(q, ref, output, _emotion_ref):
    started = time.perf_counter()
    import soundfile as sf
    from cosyvoice.cli.cosyvoice import CosyVoice3

    import torch
    imports_seconds = time.perf_counter() - started
    model_dir = str(Path(q["modelRoot"]) / "pretrained_models/Fun-CosyVoice3-0.5B")
    cache=q.get("_cache")
    warm=cache is not None and cache.get("root")==model_dir
    if warm:
        model=cache["model"]
    elif torch.cuda.is_available():
        model = CosyVoice3(model_dir=model_dir)
    else:
        from unittest.mock import patch
        from cosyvoice.cli.model import CosyVoice3Model

        with patch.object(CosyVoice3Model, "load", load_cosy_cpu):
            model = CosyVoice3(model_dir=model_dir)
    if cache is not None:
        if not warm:
            cache.update(root=model_dir,model=model,token2wav=model.model.token2wav,initial_hop=model.model.token_hop_len)
        model.model.token_hop_len=cache["initial_hop"]
        model.model.token2wav=cache["token2wav"]
    if "_model_ready" in q:
        q["_model_ready"]()
    if q.get("_prepare") and q.get("mode", "voice-convert") != "voice-convert":
        return
    if q.get("_prepare"):
        if not cache.get("calibrated"):
            wav=q["_calibration"]
            for chunk in model.inference_vc(wav,wav,stream=True,speed=1.0):
                if not chunk["tts_speech"].numel():
                    raise ValueError("Empty STS calibration audio")
            if torch.cuda.is_available():
                torch.cuda.synchronize()
            cache["calibrated"]=True
        return
    waveform_devices = set()
    observe_output(model.model, "token2wav", waveform_devices)
    loaded_seconds = time.perf_counter() - started
    options = dict(stream=False, speed=q["speed"])
    prefix = "You are a helpful assistant."
    end = "<|endofprompt|>"
    stream = q["mode"] == "voice-convert"
    if stream:
        options.update(stream=True, speed=1.0)
        result = model.inference_vc(q["_source"], ref, **options)
    elif q["mode"] == "clone":
        result = model.inference_zero_shot(q["text"], prefix + end + q["transcript"], ref, **options)
    elif q["mode"] == "instruct":
        result = model.inference_instruct2(q["text"], prefix + " " + q["instruction"] + end, ref, **options)
    else:
        result = model.inference_cross_lingual(prefix + end + q["text"], ref, **options)
    if stream:
        import numpy as np
        wire=q["_wire"]
        wire.write(struct.pack("<4sI",b"TD01",model.sample_rate))
        wire.flush()
    durations=[]
    first_audio=None
    with sf.SoundFile(output, "w", samplerate=model.sample_rate, channels=1, subtype="PCM_16") as wav:
        for chunk in result:
            samples=chunk["tts_speech"].detach().cpu().reshape(-1).numpy()
            if stream:
                if not len(samples):
                    continue
                if not np.isfinite(samples).all():
                    raise ValueError("STS produced invalid samples")
                pcm=np.clip(np.rint(samples*32768),-32768,32767).astype("<i2")
                wire.write(pcm.tobytes());wire.flush()
                elapsed=time.perf_counter()-started
                if first_audio is None:
                    first_audio=elapsed
                durations.append({"at_seconds":round(elapsed,4),"samples":int(pcm.size)})
                wav.write(pcm)
            else:
                wav.write(samples)
    if stream and not durations:
        raise ValueError("STS did not produce audio")
    modules = {name: getattr(model.model, name) for name in (("flow", "hift") if stream else ("llm", "flow", "hift"))}
    details = execution_details(torch, Path(model_dir).name, modules, waveform_devices)
    details["frontend_providers"] = {
        name: getattr(model.frontend, name).get_providers()
        for name in ("campplus_session", "speech_tokenizer_session")
        if getattr(model.frontend, name, None) is not None
    }
    if stream:
        details["stream"]=dict(imports_seconds=round(imports_seconds,3),ready_seconds=round(loaded_seconds,3),
            first_audio_seconds=round(first_audio,3),total_seconds=round(time.perf_counter()-started,3),
            chunks=durations,input="recorded_audio",path="native_voice_conversion",asr=False,text_generation=False,warm=warm)
    return details


def vox(q, ref, output, _emotion_ref):
    import soundfile as sf
    import torch
    from voxcpm import VoxCPM

    model = resident_model(q, lambda: VoxCPM.from_pretrained(
        str(Path(q["modelRoot"]) / "pretrained_models/VoxCPM2"),
        load_denoiser=False, local_files_only=True, optimize=False,
    ))
    if q.get("_prepare"):
        return
    waveform_devices = set()
    observe_output(model.tts_model.audio_vae, "decode", waveform_devices)
    options = {"reference_wav_path": ref} if ref else {}
    text = q["text"]
    if q["mode"] == "continue":
        options.update(prompt_wav_path=ref, prompt_text=q["transcript"])
    elif q["instruction"]:
        text = f"({q['instruction']}){text}"
    samples = model.generate(text=text, **options)
    sf.write(output, samples, model.tts_model.sample_rate, subtype="PCM_16")
    return execution_details(torch, "VoxCPM2", {"tts": model.tts_model}, waveform_devices)


def index(q, ref, output, emotion_ref):
    import soundfile as sf
    import torch
    paths = {}
    if q["modelRoot"]:
        checkpoint_dir = Path(q["modelRoot"]) / "checkpoints"
        cache = str(checkpoint_dir / "hf_cache")
        os.environ["HF_HUB_CACHE"] = cache
        os.environ["TRANSFORMERS_CACHE"] = cache
        # The pinned adapter sets HF_HUB_CACHE at import time. Initialize the
        # shared cache constants first, then restore the job's cache environment.
        import huggingface_hub.constants
        import transformers.utils.hub
        paths = dict(cfg_path=str(checkpoint_dir / "config.yaml"), model_dir=str(checkpoint_dir))
    from indextts.infer_v2 import IndexTTS2
    if paths:
        os.environ["HF_HUB_CACHE"] = cache

    model = resident_model(q, lambda: IndexTTS2(use_cuda_kernel=False, use_deepspeed=False, **paths))
    if q.get("_prepare"):
        return
    waveform_devices = set()
    observe_output(model.bigvgan, "forward", waveform_devices)
    options = {}
    if q["mode"] == "emotion-audio":
        options["emo_audio_prompt"] = emotion_ref
    elif q["mode"] == "emotion-text":
        options.update(use_emo_text=True, emo_text=q["instruction"])
    elif q["mode"] == "emotion-vector":
        options["emo_vector"] = q["emotionWeights"]
    if options:
        options["emo_alpha"] = q.get("emotionStrength", 1.0)
    rate, samples = model.infer(spk_audio_prompt=ref, text=q["text"], output_path=None, **options)
    sf.write(output, samples.reshape(-1), rate, subtype="PCM_16")
    modules = {name: getattr(model, name) for name in ("gpt", "semantic_model", "semantic_codec", "s2mel", "bigvgan")}
    return execution_details(torch, "IndexTTS-2", modules, waveform_devices)


QWEN_LANGUAGES = {
    "auto": "Auto", "zh": "Chinese", "en": "English", "ja": "Japanese",
    "ko": "Korean", "de": "German", "fr": "French", "ru": "Russian",
    "pt": "Portuguese", "es": "Spanish", "it": "Italian",
}


def qwen(q, ref, output, _emotion_ref):
    import soundfile as sf
    import torch
    from qwen_tts import Qwen3TTSModel

    name = "Qwen3-TTS-12Hz-1.7B-" + q["variant"]
    device = "cuda:0" if torch.cuda.is_available() else "cpu"
    dtype = torch.bfloat16 if device != "cpu" and torch.cuda.is_bf16_supported() else torch.float32
    model = resident_model(q, lambda: Qwen3TTSModel.from_pretrained(
        str(Path(q["modelRoot"]) / "pretrained_models" / name), device_map=device, dtype=dtype,
        attn_implementation="sdpa", local_files_only=True,
    ))
    if q.get("_prepare"):
        return
    waveform_devices = set()
    tokenizer = getattr(getattr(model.model, "speech_tokenizer", None), "model", None)
    observe_tokenizer_decode(tokenizer, waveform_devices)
    options = dict(text=q["text"], language=QWEN_LANGUAGES[q["language"]])
    if q["mode"] == "design":
        wavs, rate = model.generate_voice_design(instruct=q["instruction"], **options)
    elif q["mode"] == "preset":
        wavs, rate = model.generate_custom_voice(speaker=q["speaker"], instruct=q["instruction"] or None, **options)
    else:
        simple = q["mode"] == "simple-clone"
        wavs, rate = model.generate_voice_clone(
            ref_audio=ref, ref_text=None if simple else q["transcript"], x_vector_only_mode=simple, **options,
        )
    samples, = wavs
    sf.write(output, samples, rate, subtype="PCM_16")
    return execution_details(torch, name, {"tts": model.model, "tokenizer": tokenizer}, waveform_devices)


ADAPTERS = {
    "gpt-sovits": (gpt, "GPT_SoVITS"),
    "cosyvoice3": (cosy, "third_party/Matcha-TTS"),
    "voxcpm2": (vox, "src"),
    "indextts2": (index, ""),
    "qwen3-tts": (qwen, ""),
}


def run_job(job, wire=None, cache=None):
    q = job["request"]
    q["trained"] = job.get("trained")
    q["modelRoot"] = job.get("modelRoot", "")
    root = Path.cwd()
    adapter, imports = ADAPTERS[q["engine"]]
    if str(root) not in sys.path:
        sys.path.insert(0, str(root))
    if imports and str(root / imports) not in sys.path:
        sys.path.insert(1, str(root / imports))
    q["_cache"] = cache
    q["_prepare"] = job.get("prepareTTS", False)
    reused = cache is not None and "model" in cache
    entered = time.perf_counter() if reused else boot_time
    ready = None
    def model_ready():
        nonlocal ready
        ready = time.perf_counter()
    q["_model_ready"] = model_ready
    try:
        if q["mode"]=="voice-convert" and q["engine"]=="cosyvoice3":
            q["_source"]=job["sourceSpeech"]
            q["_wire"]=wire or sys.stdout.buffer
            with contextlib.redirect_stdout(sys.stderr):
                details=adapter(q,job["ref"],job["output"],job.get("emotionRef"))
        else:
            details = adapter(q, job.get("ref"), job["output"], job.get("emotionRef"))
    finally:
        for target, name, old, present in reversed(observers):
            if present:
                setattr(target, name, old)
            else:
                delattr(target, name)
        observers.clear()
    if q["_prepare"]:
        import torch
        if torch.cuda.is_available():
            torch.cuda.synchronize()
        return
    if ready is not None:
        details["timings"] = dict(
            load_seconds=None if reused else round(ready-entered, 6),
            generate_seconds=round(time.perf_counter()-ready, 6),
            reused=reused,
        )
    if q["mode"] != "voice-convert":
        details["resident_reused"] = reused
    details.update(job=job["id"], engine=q["engine"], mode=q["mode"])
    Path(job["diagnostics"]).write_text(json.dumps(details) + "\n", encoding="utf-8")

def sts_resident():
    import traceback
    wire=sys.stdout.buffer
    buffer={}
    sys.path.insert(0,str(Path.cwd()))
    sys.path.insert(1,str(Path.cwd()/"third_party/Matcha-TTS"))
    class Framed:
        def frame(self,tag,data=b""):
            wire.write(struct.pack("<cI",tag,len(data)));wire.write(data);wire.flush()
        def write(self,data):self.frame(b"P",data)
        def flush(self):pass
    sender=Framed()
    for line in sys.stdin:
        try:
            job=json.loads(line)
            if job.get("prepare") is True:
                with contextlib.redirect_stdout(sys.stderr):
                    cosy({"modelRoot":job["modelRoot"],"_cache":buffer,"_prepare":True,"_calibration":job["calibration"]},None,None,None)
                sender.frame(b"D")
                continue
            if job["request"]["mode"]!="voice-convert" or job["request"]["engine"]!="cosyvoice3":
                raise ValueError("Resident worker accepts STS only")
            run_job(job,sender,buffer)
            sender.frame(b"D")
        except Exception:
            traceback.print_exc(file=sys.stderr)
            sender.frame(b"E")
            break


def tts_resident():
    import traceback
    wire = os.fdopen(os.dup(sys.stdout.fileno()), "wb")
    os.dup2(sys.stderr.fileno(), sys.stdout.fileno())
    cache = {}
    identity = None
    with wire, contextlib.redirect_stdout(sys.stderr):
        for line in sys.stdin:
            try:
                job = json.loads(line)
                q = job["request"]
                weights = job.get("trained") or {}
                key = (q["engine"], q["variant"], job.get("modelRoot"), weights.get("gpt"), weights.get("sovits"))
                if q["mode"] == "voice-convert" or (identity is not None and identity != key):
                    raise ValueError("TTS model identity changed in a resident process")
                identity = key
                run_job(job, cache=cache)
                wire.write(b"D\x00\x00\x00\x00")
                wire.flush()
            except Exception:
                traceback.print_exc(file=sys.stderr)
                wire.write(b"E\x00\x00\x00\x00")
                wire.flush()
                return 1
    return 0


if __name__ == "__main__":
    if "--sts-resident" in sys.argv:
        sts_resident()
    elif "--tts-resident" in sys.argv:
        sys.exit(tts_resident())
    else:
        run_job(json.load(sys.stdin))
