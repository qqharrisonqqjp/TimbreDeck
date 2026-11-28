"""Resident, offline adapter for explicitly installed third-party STS models."""
import contextlib
import json
import os
from pathlib import Path
import struct
import sys
import tempfile
import time
import traceback
import wave


REPOS = {"x-vc": "x-vc", "seed-vc-tiny": "seed-vc", "meanvc2": "meanvc2", "tvtsyn1": "tvtsyn"}
PROCESS_STARTED = time.monotonic()


def read_audio(path, rate):
    import math
    import numpy as np
    import soundfile as sf
    from scipy.signal import resample_poly

    wave_, native = sf.read(path, dtype="float32", always_2d=True)
    audio = np.mean(wave_, axis=1)
    d = math.gcd(native, rate)
    return audio if native == rate else resample_poly(audio, rate // d, native // d)


def seed_converter(weights, checkout):
    from types import SimpleNamespace
    from unittest.mock import patch
    import inference
    import yaml

    config = yaml.safe_load((checkout / "configs/presets/config_dit_mel_seed_uvit_xlsr_tiny.yml").read_text())
    config["model_params"]["speech_tokenizer"]["name"] = str(weights / "facebook--wav2vec2-xls-r-300m")
    args = SimpleNamespace(fp16=True, f0_condition=False, auto_f0_adjust=False, semi_tone_shift=0,
                           length_adjust=1.0, diffusion_steps=10, inference_cfg_rate=0.7,
                           checkpoint=str(weights / "Plachta--Seed-VC/DiT_uvit_tat_xlsr_ema.pth"))
    with tempfile.TemporaryDirectory() as tmp:
        args.config = str(Path(tmp) / "seed.yml")
        Path(args.config).write_text(yaml.safe_dump(config))
        with patch.object(inference, "load_custom_model_from_hf",
                          lambda repo, name, config_filename=None: str(weights / repo.replace("/", "--") / name)):
            modules = inference.load_models(args)

    def run(source, ref):
        import torchaudio
        captured = []
        with tempfile.TemporaryDirectory() as tmp:
            args.source, args.target, args.output = source, ref, tmp
            with patch.object(inference, "load_models", return_value=modules), \
                 patch.object(torchaudio, "save", side_effect=lambda path, audio, rate: captured.append((audio.squeeze(0).numpy(), rate))):
                inference.main(args)
        yield from captured

    run.realtime = SeedRealtime(checkout, modules)
    return run, {"converter": modules[0].cfm, "decoder": modules[3], "speaker": modules[4]}


class SeedRealtime:
    """Use the pinned upstream Tiny realtime kernel, with headless SOLA state."""
    block_16k = 4800                         # upstream GUI default: 300 ms
    rate = 22050
    overlap_units = 4                        # 80 ms; units are 20 ms
    search_units = 1
    right_units = 1
    left_units = 125                         # 2.5 s for the content encoder
    converter_left_units = 25               # 0.5 s for the diffusion model
    latency_16k = 320 * (overlap_units + search_units + right_units)

    def __init__(self, checkout, modules):
        self.checkout = checkout
        self.modules = modules[:2] + modules[3:]
        self.kernel = None
        self.session = None
        self.ref = None
        self.prompt = None

    def reset(self, session=None):
        import torch

        if self.kernel is None:
            import inspect
            import runpy
            from unittest.mock import patch
            # The upstream GUI reads a .env at import; our isolated worker
            # already has explicit local settings and does not need it.
            with patch("dotenv.load_dotenv", return_value=False):
                namespace = runpy.run_path(str(self.checkout / "real-time-gui.py"))
            self.kernel = namespace["custom_infer"]
            inspect.unwrap(self.kernel).__globals__.update(device=torch.device("cuda:0"), fp16=True)
        self.session, self.seq = session, 0
        self.block = self.block_16k * self.rate // 16000
        unit = self.rate // 50
        self.overlap = self.overlap_units * unit
        self.search = self.search_units * unit
        self.buffer = torch.zeros(320 * (self.left_units + self.right_units +
                                         self.overlap_units + self.search_units) + self.block_16k, device="cuda:0")
        self.tail = torch.zeros(self.overlap, device="cuda:0")
        self.fade = torch.sin(torch.linspace(0, torch.pi / 2, self.overlap, device="cuda:0")).square()

    def blockify(self, samples, ref):
        import numpy as np
        import torch
        import torch.nn.functional as F

        if len(samples) != self.block_16k:
            raise ValueError("Seed-VC realtime requires one 300 ms block")
        if self.ref != ref:
            self.prompt = read_audio(ref, self.rate)
            self.ref = ref
        self.buffer[:-self.block_16k] = self.buffer[self.block_16k:].clone()
        self.buffer[-self.block_16k:] = torch.as_tensor(samples, device="cuda:0")
        # Same kernel and SOLA convention as the upstream realtime callback;
        # browser already gives us 16 kHz, so no device-rate round trip.
        if not np.any(np.abs(samples) > 0.001) and self.buffer[-self.block_16k - 320 * 6:].abs().amax().item() <= 0.001:
            self.tail.zero_()
            return np.zeros(self.block, dtype=np.float32)
        wave_ = self.kernel(self.modules, self.prompt, ref, self.buffer, self.block_16k,
                            self.left_units, self.right_units,
                            self.block_16k // 320 + self.overlap_units + self.search_units,
                            10, 0.7, 3.0, (self.left_units - self.converter_left_units) / 50)
        need = self.block + self.overlap + self.search
        if wave_.numel() < need:
            raise ValueError("Upstream realtime kernel returned a short block")
        corr = wave_[None, None, :self.overlap + self.search]
        score = F.conv1d(corr, self.tail[None, None]) / torch.sqrt(
            F.conv1d(corr.square(), torch.ones_like(self.tail)[None, None]) + 1e-8)
        offset = int(score.argmax().item())
        wave_ = wave_[offset:offset + self.block + self.overlap]
        wave_[:self.overlap] = wave_[:self.overlap] * self.fade + self.tail * (1 - self.fade)
        self.tail.copy_(wave_[self.block:])
        return wave_[:self.block].float().cpu().numpy()

    def live(self, source, ref, session, seq):
        if seq == 0:
            self.reset(session)
        if not session or session != self.session or seq != self.seq or (seq and self.ref != ref):
            raise ValueError("Seed-VC live session interrupted; restart the microphone")
        audio = self.blockify(read_audio(source, 16000), ref)
        self.seq += 1
        yield audio, self.rate

    def file(self, source, ref):
        import numpy as np

        self.reset()
        samples = read_audio(source, 16000)
        left = round(len(samples) * self.rate / 16000)
        skip = self.latency_16k * self.rate // 16000
        for offset in range(0, len(samples) + self.latency_16k, self.block_16k):
            chunk = samples[offset:offset + self.block_16k]
            if len(chunk) < self.block_16k:
                chunk = np.pad(chunk, (0, self.block_16k - len(chunk)))
            audio = self.blockify(chunk, ref)
            if skip:
                audio, skip = audio[skip:], max(0, skip - len(audio))
            audio = audio[:left]
            left -= len(audio)
            yield audio, self.rate


def xvc_converter(weights, checkout):
    import torch
    import torch.nn.functional as F
    from omegaconf import OmegaConf
    from bins import infer_utils as xvc
    from models.codec.sac.model import XVC

    config = OmegaConf.load(checkout / "configs/xvc.yaml")
    generator = config.model.generator
    for node in (generator.semantic_encoder.encoder.from_pretrained, generator.semantic_encoder.cfg):
        node.local_ckpt = str(weights / "zai-org--glm-4-voice-tokenizer")
    generator.speaker_encoder.pretrained_dir = str(weights / "eres2net")
    with tempfile.TemporaryDirectory() as tmp:
        p = str(Path(tmp) / "xvc.yml")
        OmegaConf.save(config, p)
        device = torch.device("cuda:0")
        m = XVC.load_from_checkpoint(p, weights / "chenxie95--X-VC/xvc.pt", device=device)
        m.remove_weight_norm()
    rate = int(config["sample_rate"])

    def run(source, ref):
        src, target, cond = xvc.load_pair_as_tensors(source, ref, config, device, 1280, False)
        speaker, frame = xvc.precompute_conditions(m, target, cond)
        # Upstream SeedTTS streaming preset: 2400/120/100/20 ms
        # (window/current/future/overlap). Forward each completed block now.
        window, step, future, overlap = [v * rate // 1000 for v in (2400, 120, 100, 20)]
        history = window - step - future - overlap
        fade = 0.5 * (1 - torch.cos(torch.pi * torch.linspace(0, 1, overlap, device=device)))
        tail = None
        end = min(src.size(-1), len(read_audio(source, rate)))
        for offset in range(0, end, step):
            left, right = offset - history, offset + step + future + overlap
            chunk = F.pad(src[..., max(left, 0):right], (max(0, -left), max(0, right - src.size(-1))))
            block = xvc.run_stream_chunk_forward(m, chunk, speaker, frame)
            audio = block[..., history:history + step]
            if tail is not None:
                audio = torch.cat([tail * (1 - fade) + audio[..., :overlap] * fade, audio[..., overlap:]], -1)
            tail = block[..., history + step:history + step + overlap]
            yield xvc.to_numpy_audio(audio)[:end - offset], rate

    return run, {"converter": m.acoustic_converter, "decoder": m.acoustic_decoder, "speaker": m.speaker_encoder}


def tvtsyn_converter(weights, checkout):
    sys.path[:0] = [str(checkout / "inference"), str(checkout / "src")]
    from speechbrain.inference.classifiers import EncoderClassifier
    from speechbrain.utils.fetching import FetchConfig, LocalStrategy
    from tvtsyn_inference import TVTSyn
    from tvtsyn_inference.speaker import SpeakerEncoder
    import torch

    m = TVTSyn.from_pretrained(weights / "warisqr007--TVTSyn", device="cuda:0", local_files_only=True)
    speaker = SpeakerEncoder.__new__(SpeakerEncoder)
    speaker.torch = torch
    speaker.device = "cuda:0"
    speaker.encoders = []
    for spec in m.config["speaker_models"]:
        path = weights / spec["repo_id"].replace("/", "--")
        speaker.encoders.append(EncoderClassifier.from_hparams(
            source=str(path), savedir=None, local_strategy=LocalStrategy.NO_LINK,
            overrides={"pretrained_path": path.as_posix()}, fetch_config=FetchConfig(allow_network=False),
            run_opts={"device": "cuda:0"}).eval())
    last = None
    embedding = None

    def run(source, ref):
        nonlocal last, embedding
        if ref != last:
            embedding = speaker.encode(ref)
            last = ref
        samples = read_audio(source, 16000)
        session = m.stream(embedding)
        try:
            for i in range(0, len(samples), 1600):
                yield session.push(samples[i:i + 1600]), 16000
            yield session.flush(), 16000
        finally:
            session.close()

    return run, {"converter": m.runtime.model, "decoder": m.runtime.streaming_step,
                 "speaker": speaker.encoders[0]}


def meanvc2_converter(weights, checkout):
    import numpy as np
    import torch

    sys.path.insert(0, str(checkout / "runtime"))
    import run_rt
    hf = weights / "ASLP-lab--MeanVC2"
    extra = weights / "meanvc2-extra"
    run_rt.MODEL_PATHS["40ms"].update(
        ckpt=str(hf / "meanvc2_40ms_40ms.safetensors"), asr_ckpt=str(hf / "fastu2pp_80ms.pt"))
    run_rt.VOCODER_PATH = str(hf / "vocos.pt")
    run_rt.SPEAKER_MODEL_PATH = str(extra / "wavlm_large_finetune.pth")
    run_rt.WAVLM_CONFIG_PATH = str(extra / "wavlm_large_cfg.pt")
    runner = None
    last = None
    evidence = {}

    def run(source, ref):
        nonlocal last, runner
        if runner is None:
            runner = run_rt.VCRunner(ref, device="cuda", model="40ms")
            torch.cuda.synchronize()
            run.ready_at = time.monotonic()
            evidence.update(converter=runner.vc, decoder=runner.vocoder, speaker=runner.spk_model,
                            content_encoder=runner.asr)
        elif ref != last:
            # Refresh speaker conditioning and reset session state, without
            # discarding the heavy model loaded during calibration.
            runner.vc_spk_emb = run_rt.extract_embedding(runner.spk_model, ref, device="cuda")
            runner.vc_gtm_kv = runner.vc.gtm(runner.vc_spk_emb)
        last = ref
        samples = read_audio(source, 16000)
        runner._init_cache()
        with torch.inference_mode():
            end = len(samples)
            emitted = 0
            # Feed enough lookahead to release the remaining real frames; trim
            # any samples generated only from that lookahead.
            samples = np.pad(samples, (0, runner.CHUNK))
            for i in range(0, len(samples), runner.CHUNK):
                block = runner.process_chunk(samples[i:i + runner.CHUNK])
                if block is not None and emitted < end:
                    result = block[:end - emitted]
                    emitted += len(result)
                    yield result, 16000

    return run, evidence


def write_frame(stream, kind, data=b""):
    stream.write(struct.pack("<cI", kind, len(data)))
    stream.write(data)
    stream.flush()


def process(job, converter, modules, stream, model_id, reused):
    import numpy as np
    import soxr
    import torch

    before = time.monotonic()
    sink = None
    first = None
    frames = 0
    header = False
    resampler = None
    native_rate = None
    preparing = job.get("prepare", False)
    reference = job["calibration"] if preparing else job["ref"]
    source = reference if preparing else job["sourceSpeech"]
    realtime = getattr(converter, "realtime", None)
    streaming = bool(job.get("liveSession") or job.get("request", {}).get("stsStreaming") or (preparing and realtime))
    if streaming and realtime is None:
        raise ValueError("Realtime route has no adapter")
    if streaming:
        if job.get("liveSession"):
            run = realtime.live(source, reference, job["liveSession"], job["liveSeq"])
        else:
            run = realtime.file(source, reference)
    else:
        if realtime:
            realtime.session = None
        run = converter(source, reference)
    blocks = 0
    try:
        if not preparing:
            sink = wave.open(job["output"], "wb")
            sink.setparams((1, 2, 24000, 0, "NONE", ""))

        def emit(y):
            nonlocal first, header, frames
            if not y.size:
                return
            if not np.isfinite(y).all():
                raise ValueError("STS produced non-finite audio")
            data = np.clip(np.rint(y * 32768), -32768, 32767).astype("<i2").tobytes()
            if first is None:
                first = time.monotonic() - before
            frames += len(y)
            if sink:
                sink.writeframesraw(data)
                write_frame(stream, b"P", (b"TD01" + struct.pack("<I", 24000) if not header else b"") + data)
                header = True

        with torch.inference_mode():
            for audio, rate in run:
                blocks += 1
                audio = np.asarray(audio, dtype=np.float32).reshape(-1)
                if native_rate is None:
                    native_rate = rate
                    if rate != 24000:
                        resampler = soxr.ResampleStream(rate, 24000, 1, dtype="float32", quality="HQ")
                if rate != native_rate:
                    raise ValueError("Unexpected audio sample-rate change")
                if len(audio):
                    emit(resampler.resample_chunk(audio) if resampler else audio)
            if resampler:
                emit(resampler.resample_chunk(np.empty(0, dtype=np.float32), last=True))
        torch.cuda.synchronize()
        if not frames:
            raise ValueError("No audio was produced")
        if sink:
            sink.close()
            sink = None
        finished = time.monotonic()
        ready = max(before, converter.ready_at) if not reused else before
        device_map = {name: sorted({str(p.device) for p in module.parameters()})
                      for name, module in modules.items()}
        data = dict(job=job.get("id"), engine=model_id, mode="voice-convert", model=model_id,
                    platform=sys.platform, module_devices=device_map, torch_version=torch.__version__,
                    cuda_version=torch.version.cuda, native_sample_rate=native_rate, output_samples=frames,
                    cuda_devices={"cuda:0": {"name": torch.cuda.get_device_name(0)}},
                    adapter_first_pcm_seconds=first, adapter_total_seconds=time.monotonic() - before,
                    peak_torch_bytes=torch.cuda.max_memory_allocated(), streaming=streaming,
                    blocks=blocks,
                    timings=dict(reused=reused, generate_seconds=finished-ready,
                                 load_seconds=None if reused else ready-PROCESS_STARTED))
        print(json.dumps(data), file=sys.stderr, flush=True)
        if not preparing:
            Path(job["diagnostics"]).write_text(json.dumps(data), encoding="utf-8")
    finally:
        if sink:
            sink.close()


def main():
    model_id, asset_dir = sys.argv[1:3]
    assets = Path(asset_dir)
    checkout = assets / "upstream" / REPOS[model_id]
    sys.path[:0] = [str(checkout), str(assets / "deps311")]
    if model_id == "x-vc":
        sys.path.insert(0, str(assets / "deps-x-vc"))
    os.chdir(checkout)
    # Reserve the original pipe for protocol frames even if a C extension logs
    # to stdout. All ordinary logs go to the studio's diagnostic file.
    protocol = os.fdopen(os.dup(sys.stdout.fileno()), "wb")
    os.dup2(sys.stderr.fileno(), sys.stdout.fileno())
    with protocol, contextlib.redirect_stdout(sys.stderr):
        import torch
        if not torch.cuda.is_available():
            raise RuntimeError("This STS adapter requires CUDA; CPU fallback is disabled")
        torch.cuda.set_device(0)
        torch.set_num_threads(2)
        load = {"x-vc": xvc_converter, "seed-vc-tiny": seed_converter,
                "meanvc2": meanvc2_converter, "tvtsyn1": tvtsyn_converter}[model_id]
        for line in sys.stdin:
            try:
                reused = "converter" in locals()
                if not reused:
                    converter, modules = load(assets / "weights", checkout)
                    torch.cuda.synchronize()
                    converter.ready_at = time.monotonic()
                process(json.loads(line), converter, modules, protocol, model_id, reused)
                write_frame(protocol, b"D")
            except Exception:
                traceback.print_exc(file=sys.stderr)
                write_frame(protocol, b"E")
                return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
