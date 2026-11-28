"""Offline TimbreDeck reference-audio worker. Request: one UTF-8 JSON object."""

import contextlib
import hashlib
import json
import math
import os
from pathlib import Path
import sys
import tempfile


OUTPUT_RATE = 32000
UVR_RATE = 44100
DENOISE_RATE = 16000
LIMIT_SECONDS = 180
WEIGHTS = {
    "HP2_all_vocals.pth": "39796caa5db18d7f9382d8ac997ac967bfd85f7761014bb807d2543cc844ef05",
    "speech_frcrn_ans_cirm_16k/configuration.json": "bbf0257214a6a5171a0e20b978fcc1d3646ecfb0e3b50b8062b5b6502b641d70",
    "speech_frcrn_ans_cirm_16k/pytorch_model.bin": "477a2f82019b0a2b5bdd51541405d7e585a1f0355e162f3b46bf5c20d79fd612",
}


def verified_asset(assets, name):
    path = assets / name
    if not path.is_file():
        raise ValueError(f"Missing packaged model file: {name}")
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    if digest.hexdigest() != WEIGHTS[name]:
        raise ValueError(f"Packaged model file failed SHA-256 validation: {name}")
    return path


def absolute_path(value, key):
    if not isinstance(value, str) or not Path(value).is_absolute():
        raise ValueError(f"{key} must be an absolute path")
    return Path(value)


def read_request():
    request = json.loads(sys.stdin.buffer.read(64 * 1024 + 1).decode("utf-8-sig"))
    if not isinstance(request, dict):
        raise ValueError("Expected a JSON request object")
    mode = request.get("mode")
    inputs = request.get("inputs")
    gap_ms = request.get("gapMs")
    if mode not in ("denoise", "uvr", "both", "mossformer", "lavasr", "lavasr-denoise"):
        raise ValueError("Unknown cleaning mode")
    if not isinstance(inputs, list) or not 1 <= len(inputs) <= 24:
        raise ValueError("Expected 1–24 input WAVs")
    if type(gap_ms) is not int or not 0 <= gap_ms <= 3000:
        raise ValueError("Gap must be an integer between 0 and 3000 ms")
    files = [absolute_path(value, "input") for value in inputs]
    output = absolute_path(request.get("output"), "output")
    engine = absolute_path(request.get("engineRoot"), "engineRoot")
    assets = absolute_path(request.get("assets"), "assets")
    if os.path.lexists(output) or not output.parent.is_dir() or output.suffix.lower() != ".wav":
        raise ValueError("Expected a new WAV output in an existing folder")
    return mode, files, output, gap_ms, engine, assets


def resample(data, source_rate, target_rate):
    if source_rate == target_rate:
        return data
    divisor = math.gcd(source_rate, target_rate)
    return resample_poly(data, target_rate // divisor, source_rate // divisor, axis=0)


def frames_at_rate(frames, source_rate, target_rate):
    return (2 * frames * target_rate + source_rate) // (2 * source_rate)


def fit_frames(data, length):
    if len(data) < length:
        padding = [(0, length - len(data))] + [(0, 0)] * (data.ndim - 1)
        return np.pad(data, padding)
    return data[:length]


def finite_samples(data, stage):
    if data.size == 0 or not np.isfinite(data).all():
        raise ValueError(f"{stage} returned empty or invalid sample data")
    return data


def windowed(data, process, size, overlap, stage):
    """Join model windows using a short linear crossfade; retain exact input length."""
    output = np.empty(len(data), dtype=np.float32)
    step = size - overlap
    fade = np.linspace(0, 1, overlap, dtype=np.float32)
    for start in range(0, len(data), step):
        end = min(start + size, len(data))
        result = finite_samples(process(data[start:end]), stage)
        if result.shape != (end - start,):
            raise ValueError(f"{stage} returned the wrong frame count")
        if start:
            end_overlap = start + overlap
            output[start:end_overlap] *= 1 - fade
            output[start:end_overlap] += result[:overlap] * fade
            output[end_overlap:end] = result[overlap:]
        else:
            output[:end] = result
        if end == len(data):
            break
    return output


def load_denoiser(assets, device):
    from modelscope.models.audio.ans.frcrn import FRCRN

    config_path = verified_asset(assets, "speech_frcrn_ans_cirm_16k/configuration.json")
    weights_path = verified_asset(assets, "speech_frcrn_ans_cirm_16k/pytorch_model.bin")
    config = json.loads(config_path.read_text(encoding="utf-8"))["model"]
    del config["type"]
    model = FRCRN(**config)
    model.load_state_dict(torch.load(str(weights_path), map_location="cpu", weights_only=True))
    model.eval().to(device)

    def denoise(samples):
        # Match the ModelScope pipeline's active-level normalization, then restore
        # each input's gain so relative loudness between clips is preserved.
        power = samples.astype(np.float64) ** 2
        active = power[power > power.mean()]
        rms = math.sqrt(float(active.mean() if active.size else power.mean()))
        if not rms:
            return samples
        gain = (10 ** (-25 / 20)) / rms

        def infer(segment):
            # FRCRN's overlap-add geometry requires a 320-sample multiple.
            # At least one second and an extra analysis hop cover short clips.
            padded = max(16000, (len(segment) + 639) // 320 * 320)
            normalized = fit_frames(np.ascontiguousarray(segment * gain, dtype=np.float32), padded)
            tensor = torch.from_numpy(normalized[None, :]).to(device)
            with torch.inference_mode():
                enhanced = model(tensor)[4][0].detach().cpu().numpy()
            return enhanced[: len(segment)] / gain

        return windowed(samples, infer, 4 * DENOISE_RATE, DENOISE_RATE // 4, "FRCRN")

    return denoise


def load_uvr(assets, engine, device):
    uvr_dir = engine / "tools" / "uvr5"
    sys.path.insert(0, str(uvr_dir))
    from lib.lib_v5 import nets_61968KB, spec_utils
    from lib.lib_v5.model_param_init import ModelParameters
    from lib.utils import inference

    weights_path = verified_asset(assets, "HP2_all_vocals.pth")
    params = ModelParameters(str(uvr_dir / "lib/lib_v5/modelparams/4band_v2.json"))
    model = nets_61968KB.CascadedASPPNet(params.param["bins"] * 2)
    model.load_state_dict(torch.load(str(weights_path), map_location="cpu", weights_only=True))
    model.eval().to(device)
    bands = params.param["band"]
    band_count = len(bands)
    settings = {"window_size": 512, "tta": False}
    aggressiveness = {"value": 0.1, "split_bin": bands[1]["crop_stop"]}

    def reconstruct(vocal, keep, top):
        # UVR5's inverse allocates np.ndarray and writes only cropped frequency
        # bins. Leftover uninitialized bins can contain earlier allocations (or
        # NaNs, even after multiplying by zero). Silence omitted bins explicitly.
        wave = None
        offset = 0
        for number in range(1, band_count + 1):
            band = bands[number]
            nyquist = band["n_fft"] // 2
            spread = np.zeros((2, nyquist + 1, vocal.shape[2]), dtype=vocal.dtype)
            low, high = band["crop_start"], band["crop_stop"]
            spread[:, low:high, :] = vocal[:, offset:offset + high - low, :]
            offset += high - low
            if number == band_count:
                spread[:, nyquist - keep:nyquist, :] = top[:, :keep, :]
            if "hpf_start" in band:
                spec_utils.fft_hp_filter(spread, band["hpf_start"], band["hpf_stop"] - 1)
            if "lpf_start" in band:
                spec_utils.fft_lp_filter(spread, band["lpf_start"], band["lpf_stop"])
            piece = spec_utils.spectrogram_to_wave(
                spread, band["hl"], params.param["mid_side"], params.param["mid_side_b2"],
                params.param["reverse"],
            ).T
            wave = piece if wave is None else wave + piece
            if number < band_count:
                wave = resample(wave, band["sr"], bands[number + 1]["sr"])
        return wave

    def separate(segment):
        if not np.any(segment):
            return np.zeros(len(segment), dtype=np.float32)
        # Add analysis padding at the tail: the multiband STFT trims to a
        # common hop boundary. Remove the padding after reconstruction.
        wave = np.pad(segment.T, ((0, 0), (0, 1920)))
        if wave.shape[0] == 1:
            wave = np.repeat(wave, 2, axis=0)
        specs = {}
        previous_rate = UVR_RATE
        for number in range(band_count, 0, -1):
            band = bands[number]
            wave = np.asfortranarray(resample(wave.T, previous_rate, band["sr"]).T)
            previous_rate = band["sr"]
            specs[number] = spec_utils.wave_to_spectrogram(
                wave, band["hl"], band["n_fft"], params.param["mid_side"],
                params.param["mid_side_b2"], params.param["reverse"],
            )
        mixture = spec_utils.combine_spectrograms(specs, params)
        if not np.any(mixture):
            return np.zeros(len(segment), dtype=np.float32)
        predicted, _, phase = inference(mixture, device, model, aggressiveness, settings)
        # HP2 predicts the accompaniment; vocals are the complementary stem.
        vocal = mixture - predicted * phase
        top_band = bands[band_count]
        top_bin = top_band["n_fft"] // 2
        keep = top_bin - top_band["crop_stop"] + params.param["pre_filter_stop"] - params.param["pre_filter_start"]
        top = spec_utils.mirroring("mirroring", vocal, specs[band_count][:, top_bin - keep : top_bin, :], params)
        reconstructed = reconstruct(vocal, keep, top)
        return np.asarray(reconstructed[: len(segment)], dtype=np.float32).mean(axis=1)

    def uvr(samples):
        return windowed(samples, separate, UVR_RATE * 20, UVR_RATE // 2, "UVR5")

    return uvr


def load_enhancer(mode, engine, device):
    if mode == "mossformer":
        from clearvoice import ClearVoice
        model = ClearVoice(task="speech_enhancement", model_names=["MossFormer2_SE_48K"])
        def process(samples, rate):
            data = resample(samples, rate, 48000)
            return windowed(data, lambda x: np.asarray(model(x[None, :])).reshape(-1),
                            48000*8, 4800, "ClearerVoice"), 48000
        return process
    from LavaSR.model import LavaEnhance2
    from LavaSR.enhancer.linkwitz_merge import FastLRMerge
    model = LavaEnhance2(str(engine / "lavasr"), str(device))
    def process(samples, rate):
        model.bwe_model.lr_refiner = FastLRMerge(device=str(device), cutoff=min(rate, 16000)//2, transition_bins=1024)
        data = resample(samples, rate, 48000)
        def infer(x):
            # Vocos emits whole hops and the denoiser can trim its tail.
            # Supply lead-out padding; crop only after the model returns enough audio.
            audio = torch.as_tensor(resample(np.pad(x, (0, 6144)), 48000, 16000), device=device).unsqueeze(0)
            with torch.inference_mode():
                wave = model.enhance(audio, denoise=mode=="lavasr-denoise", batch=False)
            return wave.detach().cpu().numpy().reshape(-1)[:len(x)]
        return windowed(data, infer, 48000*8, 4800, "LavaSR"), 48000
    return process


def process_request(mode, files, output, gap_ms, engine, assets):
    global np, sf, resample_poly, torch
    import numpy as np
    import soundfile as sf
    from scipy.signal import resample_poly
    import torch

    entries = []
    for index, file in enumerate(files, 1):
        try:
            info = sf.info(str(file))
            if info.format not in ("WAV", "WAVEX", "RF64") or not 1 <= info.channels <= 2:
                raise ValueError("Expected mono or stereo WAV")
            if info.frames <= 0 or not 8000 <= info.samplerate <= 192000:
                raise ValueError("WAV is empty or uses an unsupported sample rate")
        except Exception as exc:
            raise ValueError(f"Input {index} could not be read: {exc}") from exc
        entries.append((file, info, frames_at_rate(info.frames, info.samplerate, OUTPUT_RATE)))
    gap_frames = gap_ms * OUTPUT_RATE // 1000
    expected = sum(entry[2] for entry in entries) + gap_frames * (len(entries) - 1)
    if expected > LIMIT_SECONDS * OUTPUT_RATE:
        raise ValueError(f"Joined reference is longer than {LIMIT_SECONDS} seconds")

    device_name = os.environ.get("TIMBREDECK_CLEAN_DEVICE", "auto")
    if device_name not in ("auto", "cpu", "cuda"):
        raise ValueError("Unsupported cleaning device")
    if device_name == "auto":
        device_name = "cuda" if torch.cuda.is_available() else "cpu"
    device = torch.device(device_name)
    torch.set_num_threads(min(4, os.cpu_count() or 1))
    if device.type == "cuda" and not torch.cuda.is_available():
        raise ValueError("CUDA was requested but is not available")
    uvr = load_uvr(assets, engine, device) if mode in ("uvr", "both") else None
    denoise = load_denoiser(assets, device) if mode in ("denoise", "both") else None

    enhance = load_enhancer(mode, engine, device) if mode in ("mossformer", "lavasr", "lavasr-denoise") else None

    handle, staging = tempfile.mkstemp(prefix=".clean-", suffix=".wav", dir=str(output.parent))
    os.close(handle)
    try:
        with sf.SoundFile(staging, "w", samplerate=OUTPUT_RATE, channels=1, format="WAV", subtype="PCM_16") as destination:
            for index, (file, info, length) in enumerate(entries, 1):
                try:
                    samples, rate = sf.read(str(file), dtype="float32", always_2d=True)
                    if samples.shape != (info.frames, info.channels) or rate != info.samplerate:
                        raise ValueError("WAV changed during processing")
                    finite_samples(samples, "WAV decoder")
                    if uvr:
                        samples = uvr(resample(samples, rate, UVR_RATE))
                        rate = UVR_RATE
                    else:
                        samples = samples.mean(axis=1)
                    if denoise:
                        samples = denoise(resample(samples, rate, DENOISE_RATE))
                        rate = DENOISE_RATE
                    if enhance:
                        samples, rate = enhance(samples, rate)
                    samples = finite_samples(resample(samples, rate, OUTPUT_RATE), "Sample converter")
                    if index > 1 and gap_frames:
                        destination.write(np.zeros(gap_frames, dtype=np.float32))
                    destination.write(np.clip(fit_frames(samples, length), -1.0, 32767 / 32768))
                except Exception as exc:
                    raise ValueError(f"Input {index} processing failed: {exc}") from exc
        # Windows rename (unlike POSIX rename) fails when the destination exists.
        # A same-directory hard link gives equivalent no-clobber semantics on POSIX.
        if os.name == "nt":
            os.rename(staging, output)
        else:
            os.link(staging, output)
    finally:
        if os.path.exists(staging):
            os.unlink(staging)
    return {"mode": mode, "device": device_name, "clips": len(files), "frames": expected, "sampleRate": OUTPUT_RATE}


def main():
    sys.dont_write_bytecode = True
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
    sys.stderr.reconfigure(encoding="utf-8", errors="replace")
    os.environ["HF_HUB_OFFLINE"] = "1"
    os.environ["TRANSFORMERS_OFFLINE"] = "1"
    try:
        request = read_request()
        with contextlib.redirect_stdout(sys.stderr):
            result = process_request(*request)
    except Exception as exc:
        print(f"Audio cleaning failed: {exc}", file=sys.stderr)
        return 1
    print(json.dumps(result))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
