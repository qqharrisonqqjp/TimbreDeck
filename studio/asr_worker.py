"""Local speech-to-text worker. It never downloads a model or changes a WAV."""

import contextlib
import hashlib
import json
import math
import os
from pathlib import Path
import re
import sys


RATE = 16000
MARKERS = re.compile(r"<\|[^|<>]{1,96}\|>")
MODEL_HASHES = {
    "faster-whisper-small/config.json": "b55496ac7940a7ae47d2c01eab40edfd8701feec1229d9cce3b40014383fb828",
    "faster-whisper-small/model.bin": "3e305921506d8872816023e4c273e75d2419fb89b24da97b4fe7bce14170d671",
    "faster-whisper-small/tokenizer.json": "fb7b63191e9bb045082c79fd742a3106a12c99513ab30df4a0d47fa6cb6fd0ab",
    "faster-whisper-small/vocabulary.txt": "34ce3fe1c5041027b3f8d42912270993f986dbc4bb34cf27f951e34a1e453913",
    "sensevoice-onnx/model.int8.onnx": "c71f0ce00bec95b07744e116345e33d8cbbe08cef896382cf907bf4b51a2cd51",
    "sensevoice-onnx/tokens.txt": "f449eb28dc567533d7fa59be34e2abca8784f771850c78a47fb731a31429a1dc",
}


def verified_model(assets, subdir):
    for name, expected in MODEL_HASHES.items():
        if name.split("/", 1)[0] != subdir:
            continue
        path = assets / name
        if not path.is_file():
            raise FileNotFoundError(f"Missing packaged ASR file: {name}")
        digest = hashlib.sha256()
        with path.open("rb") as stream:
            for chunk in iter(lambda: stream.read(1024 * 1024), b""):
                digest.update(chunk)
        if digest.hexdigest() != expected:
            raise ValueError(f"Packaged ASR file failed SHA-256 validation: {name}")
    return assets / subdir


def decode_wav(path):
    import numpy as np
    from scipy.signal import resample_poly
    import soundfile as sf

    samples, sr = sf.read(str(path), dtype="float32", always_2d=True)
    if samples.shape[0] == 0 or samples.shape[1] not in (1, 2) or not 8000 <= sr <= 384000:
        raise ValueError("Invalid WAV layout")
    if len(samples) > 180 * sr or not np.isfinite(samples).all():
        raise ValueError("WAV is too long or contains non-finite samples")
    mono = samples.mean(axis=1)
    if sr != RATE:
        gcd = math.gcd(sr, RATE)
        mono = resample_poly(mono, RATE // gcd, sr // gcd)
    return np.clip(mono, -1, 1)


def sense_chunks(wave):
    import numpy as np

    start = 0
    while len(wave) - start > 26 * RATE:
        # Place the cut at the quietest 100 ms in the final four seconds.
        low, high, block = start + 22 * RATE, start + 26 * RATE, RATE // 10
        energy = np.mean(wave[low:high].reshape(-1, block) ** 2, axis=1)
        end = low + int(np.argmin(energy)) * block + block // 2
        yield wave[start:end]
        start = end
    if start < len(wave):
        yield wave[start:]


def sensevoice(assets, language):
    import sherpa_onnx

    model_dir = verified_model(assets, "sensevoice-onnx")
    worker = sherpa_onnx.OfflineRecognizer.from_sense_voice(
        model=str(model_dir / "model.int8.onnx"), tokens=str(model_dir / "tokens.txt"),
        num_threads=min(4, os.cpu_count() or 1), sample_rate=RATE, provider="cpu",
        language=language, use_itn=True,
    )

    def transcribe(wave):
        all_text = []
        for samples in sense_chunks(wave):
            stream = worker.create_stream()
            stream.accept_waveform(RATE, samples)
            worker.decode_stream(stream)
            text = MARKERS.sub("", stream.result.text).strip()
            if text:
                all_text.append(text)
        return "".join(all_text), language

    return transcribe


def whisper(assets, language):
    from faster_whisper import WhisperModel

    directory = verified_model(assets, "faster-whisper-small")
    model = WhisperModel(str(directory), device="cpu", compute_type="int8",
                         cpu_threads=min(4, os.cpu_count() or 1), local_files_only=True)
    # The pinned pre-v3 multilingual model has a Chinese token but no
    # Cantonese token. Do not report a Chinese fallback as native Cantonese.
    effective = "zh" if language == "yue" else language

    def transcribe(wave):
        segments, info = model.transcribe(
            wave, task="transcribe", language=None if effective == "auto" else effective,
            beam_size=3, condition_on_previous_text=False, vad_filter=False,
        )
        return "".join(segment.text for segment in segments), info.language

    return transcribe


def process(request):
    import numpy as np

    assets = Path(request["assets"])
    language = request["language"]
    paths = request["paths"]
    output = Path(request["output"])
    if not assets.is_absolute() or not output.is_absolute() or not isinstance(paths, list) or not 1 <= len(paths) <= 256:
        raise ValueError("Malformed recognition request")
    packages = assets / "asr-python"
    if packages.is_dir():
        sys.path.insert(0, str(packages))
    models = {}
    failure = {}
    primary = "sensevoice" if language in ("zh", "yue") else "faster-whisper"
    output_rows = []
    for index, name in enumerate(paths, 1):
        path = Path(name)
        if not path.is_absolute():
            raise ValueError(f"Input {index} is not absolute")
        wave = decode_wav(path)
        silent = not len(wave) or float(np.max(np.abs(wave))) < 1.e-4
        providers = [primary] + (["faster-whisper"] if primary == "sensevoice" else [])
        for provider in providers:
            if provider in failure:
                continue
            if provider not in models:
                try:
                    models[provider] = (sensevoice if provider == "sensevoice" else whisper)(assets, language)
                except Exception as exc:
                    failure[provider] = repr(exc)
                    print(f"{provider} failed to initialize: {exc}", file=sys.stderr)
                    continue
            try:
                effective = "zh" if provider == "faster-whisper" and language == "yue" else language
                text, effective = ("", effective) if silent else models[provider](wave)
                warning = ""
                if provider != primary:
                    warning = ("粵語模型未能辨識，已改用 Faster Whisper 的中文模式，請核對粵語用字。"
                               if language == "yue" else "中文優先模型未能辨識，已改用 Faster Whisper。")
                output_rows.append({"text": " ".join(text.split())[:10000],
                                    "provider": provider, "language": effective,
                                    "requestedLanguage": language, "warning": warning})
                break
            except Exception as exc:
                print(f"{provider} failed on clip {index}: {exc}", file=sys.stderr)
        else:
            raise RuntimeError(f"Local recognition failed for clip {index}")
    with output.open("x", encoding="utf-8") as stream:
        json.dump(output_rows, stream, ensure_ascii=False)


def main():
    sys.dont_write_bytecode = True
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
    sys.stderr.reconfigure(encoding="utf-8", errors="replace")
    os.environ["HF_HUB_OFFLINE"] = "1"
    os.environ["TRANSFORMERS_OFFLINE"] = "1"
    try:
        request = json.loads(sys.stdin.buffer.read(256 * 1024 + 1).decode("utf-8-sig"))
        with contextlib.redirect_stdout(sys.stderr):
            process(request)
    except Exception as exc:
        print(f"ASR failed: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
