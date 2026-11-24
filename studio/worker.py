"""One synthesis per process, using the engine's own portable Python."""
import json
from pathlib import Path
import sys


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

    setattr(target, name, observed)


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


def gpt(q, ref, output):
    import soundfile as sf
    import torch
    from GPT_SoVITS.TTS_infer_pack.TTS import TTS, TTS_Config

    # The pinned GPT source sets this detector during import. Its large model
    # normally downloads on first use. Select the bundled compact model offline.
    if not Path("GPT_SoVITS/pretrained_models/fast_langdetect/lid.176.bin").is_file():
        import fast_langdetect
        small = Path(fast_langdetect.__file__).parent / "resources" / "lid.176.ftz"
        detector = fast_langdetect.LangDetectConfig(custom_model_path=str(small))
        fast_langdetect.infer._default_detector = fast_langdetect.LangDetector(detector)

    gpu = torch.cuda.is_available()
    weights = dict(TTS_Config.default_configs["v2Pro"], device="cuda" if gpu else "cpu", is_half=gpu)
    config = TTS_Config({"custom": weights})
    # Upstream saves its settings during model loading. Keep those job-local.
    config.configs_path = str(Path(output).with_name("inference.yaml"))
    synth = TTS(config)
    waveform_devices = set()
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
    modules["speaker_embedding"] = getattr(synth.sv_model, "embedding_model", None)
    return execution_details(torch, "v2Pro", modules, waveform_devices)


def load_cosy_cpu(model, llm_path, flow_path, hift_path):
    """Load the official float32 checkpoints without allocating duplicate tensors."""
    import torch

    for module, path in ((model.llm, llm_path), (model.flow, flow_path), (model.hift, hift_path)):
        state = torch.load(path, weights_only=True, map_location="cpu", mmap=True)
        if module is model.hift:
            state = {key.removeprefix("generator."): value for key, value in state.items()}
        module.load_state_dict(state, strict=True, assign=True)
        module.eval()


def cosy(q, ref, output):
    import soundfile as sf
    from cosyvoice.cli.cosyvoice import CosyVoice3

    import torch
    model_dir = "pretrained_models/Fun-CosyVoice3-0.5B"
    if torch.cuda.is_available():
        model = CosyVoice3(model_dir=model_dir)
    else:
        from unittest.mock import patch
        from cosyvoice.cli.model import CosyVoice3Model

        with patch.object(CosyVoice3Model, "load", load_cosy_cpu):
            model = CosyVoice3(model_dir=model_dir)
    waveform_devices = set()
    observe_output(model.model, "token2wav", waveform_devices)
    options = dict(stream=False, speed=q["speed"])
    prefix = "You are a helpful assistant."
    end = "<|endofprompt|>"
    if q["mode"] == "clone":
        result = model.inference_zero_shot(q["text"], prefix + end + q["transcript"], ref, **options)
    elif q["mode"] == "instruct":
        result = model.inference_instruct2(q["text"], prefix + " " + q["instruction"] + end, ref, **options)
    else:
        result = model.inference_cross_lingual(prefix + end + q["text"], ref, **options)
    with sf.SoundFile(output, "w", samplerate=model.sample_rate, channels=1, subtype="PCM_16") as wav:
        for chunk in result:
            wav.write(chunk["tts_speech"].detach().cpu().reshape(-1).numpy())
    modules = {name: getattr(model.model, name) for name in ("llm", "flow", "hift")}
    details = execution_details(torch, Path(model_dir).name, modules, waveform_devices)
    details["frontend_providers"] = {
        name: getattr(model.frontend, name).get_providers()
        for name in ("campplus_session", "speech_tokenizer_session")
        if getattr(model.frontend, name, None) is not None
    }
    return details


def main():
    job = json.load(sys.stdin)
    q = job["request"]
    root = Path.cwd()
    sys.path[:0] = [str(root), str(root / ("GPT_SoVITS" if q["engine"] == "gpt-sovits" else "third_party/Matcha-TTS"))]
    details = (gpt if q["engine"] == "gpt-sovits" else cosy)(q, job["ref"], job["output"])
    details.update(job=job["id"], engine=q["engine"], mode=q["mode"])
    Path(job["diagnostics"]).write_text(json.dumps(details) + "\n", encoding="utf-8")


if __name__ == "__main__":
    main()
