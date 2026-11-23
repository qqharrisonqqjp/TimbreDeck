"""One synthesis per process, using the engine's own portable Python."""
import json
from pathlib import Path
import sys


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


def cosy(q, ref, output):
    import soundfile as sf
    from cosyvoice.cli.cosyvoice import CosyVoice3

    model = CosyVoice3(model_dir="pretrained_models/Fun-CosyVoice3-0.5B")
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


def main():
    job = json.load(sys.stdin)
    q = job["request"]
    root = Path.cwd()
    sys.path[:0] = [str(root), str(root / ("GPT_SoVITS" if q["engine"] == "gpt-sovits" else "third_party/Matcha-TTS"))]
    (gpt if q["engine"] == "gpt-sovits" else cosy)(q, job["ref"], job["output"])


if __name__ == "__main__":
    main()
