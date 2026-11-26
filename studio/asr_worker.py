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
    nanoFiles={"encoder_adaptor.int8.onnx","llm.int8.onnx","embedding.int8.onnx",
        "Qwen3-0.6B/tokenizer.json","Qwen3-0.6B/tokenizer_config.json","Qwen3-0.6B/merges.txt","Qwen3-0.6B/vocab.json","Qwen3-0.6B/config.json","LICENSE","NOTICE"}
    if subdir in ("faster-whisper-large-v3", "faster-whisper-large-v3-turbo","qwen3-asr-0.6b","qwen3-asr-1.7b","fun-asr-nano","fun-asr-mlt-nano"):
        root = assets / subdir
        info = json.loads((root / "timbredeck-asr.json").read_text("utf8"))
        files = info["files"]
        qwen = subdir.startswith("qwen3-asr-")
        nano = subdir.startswith("fun-asr-")
        valid = ({"encoder_adaptor.int8.onnx","llm.int8.onnx","embedding.int8.onnx","Qwen3-0.6B/tokenizer.json"}<=files.keys()
            if nano else ("config.json" in files and any(n.endswith(".safetensors") for n in files)) if qwen else {"model.bin", "config.json", "tokenizer.json"} <= files.keys())
        if not valid or not re.fullmatch(r"[0-9a-f]{40}", info["revision"]):
            raise ValueError("Incomplete verified model")
        for name, meta in files.items():
            extra={"tokenizer_config.json","generation_config.json","vocab.json","merges.txt","model.safetensors.index.json","chat_template.json","chat_template.jinja","added_tokens.json","special_tokens_map.json","LICENSE"}
            if name not in {"model.bin","config.json","tokenizer.json","preprocessor_config.json","vocabulary.json","vocabulary.txt"} and not (nano and name in nanoFiles) and not (qwen and (name in extra or re.fullmatch(r"model(?:-[0-9]+-of-[0-9]+)?\.safetensors",name))):
                raise ValueError("Unknown model file")
            h = hashlib.sha256()
            with (root/name).open("rb") as stream:
                for chunk in iter(lambda: stream.read(1024*1024), b""):
                    h.update(chunk)
                if stream.tell()!=meta["bytes"] or h.hexdigest()!=meta["sha256"]:
                    raise ValueError(f"ASR model hash mismatch: {name}")
        return root
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


def sense_chunks(wave, size=26):
    import numpy as np

    start = 0
    while len(wave) - start > size * RATE:
        # Place the cut at the quietest 100 ms in the final four seconds.
        low, high, block = start + (size-4) * RATE, start + size * RATE, RATE // 10
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


def whisper(assets, language, model_id="faster-whisper-small"):
    from faster_whisper import WhisperModel

    directory = verified_model(assets, model_id)
    model = WhisperModel(str(directory), device="cpu", compute_type="int8",
                         cpu_threads=min(4, os.cpu_count() or 1), local_files_only=True)
    # The pinned pre-v3 multilingual model has a Chinese token but no
    # Cantonese token. Do not report a Chinese fallback as native Cantonese.
    effective = "zh" if language == "yue" and "yue" not in model.supported_languages else language

    def transcribe(wave):
        segments, info = model.transcribe(
            wave, task="transcribe", language=None if effective == "auto" else effective,
            beam_size=3, condition_on_previous_text=False, vad_filter=False,
        )
        return "".join(segment.text for segment in segments), info.language

    return transcribe

def fun_nano(assets,language,selected):
    import sherpa_onnx
    root=verified_model(assets,selected)
    names={"zh":"中文","yue":"粤语","en":"英文","ja":"日语","ko":"韩语","vi":"越南语","id":"印尼语",
        "th":"泰语","ar":"阿拉伯语","hi":"印地语","pl":"波兰语","pt":"葡萄牙语"}
    forced=names.get(language,"") if selected=="fun-asr-mlt-nano" or language in ("en","ja","zh","yue") else ""
    recognizer=sherpa_onnx.OfflineRecognizer.from_funasr_nano(
        encoder_adaptor=str(root/"encoder_adaptor.int8.onnx"),llm=str(root/"llm.int8.onnx"),
        embedding=str(root/"embedding.int8.onnx"),tokenizer=str(root/"Qwen3-0.6B"),
        num_threads=4, provider="cpu", sample_rate=RATE,language=forced,itn=True,max_new_tokens=300)
    def transcribe(wave):
        texts=[]
        for samples in sense_chunks(wave,26 if selected=="fun-asr-mlt-nano" else 16):
            stream=recognizer.create_stream()
            stream.accept_waveform(RATE,samples)
            recognizer.decode_stream(stream)
            texts.append(MARKERS.sub("",stream.result.text).strip())
        return " ".join(texts),language if forced else "auto"
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
    choice = request.get("model")
    if choice not in (None,"","sensevoice-onnx","faster-whisper-small","faster-whisper-large-v3","faster-whisper-large-v3-turbo","qwen3-asr-0.6b","qwen3-asr-1.7b","fun-asr-nano","fun-asr-mlt-nano"):
        raise ValueError("Unknown ASR model")
    primary = choice or ("sensevoice-onnx" if language in ("zh","yue") else "faster-whisper-small")
    if primary.startswith("qwen3-asr-"):
        import numpy as np
        # This uses an independent Python 3.11 environment, not the GPT overlay.
        if str(packages) in sys.path:
            sys.path.remove(str(packages))
        sys.path.insert(0,str(assets/"qwen-asr-python"))
        import torch
        from qwen_asr import Qwen3ASRModel
        torch.set_num_threads(min(6,os.cpu_count() or 1))
        model=Qwen3ASRModel.from_pretrained(str(verified_model(assets,primary)),dtype=torch.float32,
            device_map="cpu", attn_implementation="sdpa", max_inference_batch_size=1,max_new_tokens=384,local_files_only=True)
        from qwen_asr.inference.utils import SUPPORTED_LANGUAGES
        language_names={"yue":"Cantonese","zh":"Chinese","en":"English","ja":"Japanese","ko":"Korean",
          "fr":"French","de":"German","es":"Spanish","it":"Italian","pt":"Portuguese","nl":"Dutch",
          "ru":"Russian","ar":"Arabic","hi":"Hindi","th":"Thai","vi":"Vietnamese","id":"Indonesian"}
        codes={v:k for k,v in language_names.items()}
        forced=language_names.get(language)
        if forced not in SUPPORTED_LANGUAGES:forced=None
        output_rows=[]
        for name in paths:
            path=Path(name)
            if not path.is_absolute():raise ValueError("Input path is not absolute")
            wav=decode_wav(path)
            silent=not len(wav) or float(np.max(np.abs(wav)))<1.e-4
            results=[] if silent else model.transcribe(audio=(wav,RATE),language=forced)
            text=" ".join(str(r.text).strip() for r in results)
            effective=codes.get(str(results[0].language),language) if results else language
            output_rows.append({"text":" ".join(text.split())[:10000],"provider":"qwen3-asr","language":effective,
              "requestedLanguage":language,"warning":"" if forced or language=="auto" else "此模型改用自動語言辨識。","model":primary})
        with output.open("x",encoding="utf8") as stream:json.dump(output_rows,stream,ensure_ascii=False)
        return
    output_rows = []
    for index, name in enumerate(paths, 1):
        path = Path(name)
        if not path.is_absolute():
            raise ValueError(f"Input {index} is not absolute")
        wave = decode_wav(path)
        silent = not len(wave) or float(np.max(np.abs(wave))) < 1.e-4
        candidates = [primary] + (["faster-whisper-small"] if primary != "faster-whisper-small" else [])
        for chosen in candidates:
            provider = "sensevoice" if chosen=="sensevoice-onnx" else "fun-asr" if chosen.startswith("fun-asr-") else "faster-whisper"
            if chosen in failure:
                continue
            if chosen not in models:
                try:
                    models[chosen] = sensevoice(assets,language) if provider=="sensevoice" else fun_nano(assets,language,chosen) if provider=="fun-asr" else whisper(assets,language,chosen)
                except Exception as exc:
                    failure[chosen] = repr(exc)
                    print(f"{provider} failed to initialize: {exc}", file=sys.stderr)
                    continue
            try:
                effective = "zh" if provider == "faster-whisper" and language == "yue" else language
                text, effective = ("", effective) if silent else models[chosen](wave)
                warning = ""
                if language=="yue" and provider=="faster-whisper" and effective!="yue":
                    warning = "此模型使用中文模式處理粵語，請核對粵語用字。"
                    if chosen!=primary: warning="原模型未能辨識，已用 Faster Whisper Small 後備。"+warning
                elif chosen!=primary:
                    warning = "所選模型未能辨識，已改用 Faster Whisper Small。"
                output_rows.append({"text": " ".join(text.split())[:10000],
                                    "provider": provider, "language": effective,
                                    "requestedLanguage": language, "warning": warning,"model":chosen})
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
