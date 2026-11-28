"""Fetch only the pinned, public STS inference artifacts. No audio is uploaded."""
import argparse
import concurrent.futures
from contextlib import contextmanager
import hashlib
import json
import msvcrt
from pathlib import Path
import subprocess

MODELS = {
    "x-vc": {
        "chenxie95/X-VC": ("9e54747d8c4d1ef544b903e2300a4ba040dcc126", ["xvc.pt"]),
        "zai-org/glm-4-voice-tokenizer": ("a5f2404e63c84e92f5238908e1706316324ebafa",
                                         ["config.json", "preprocessor_config.json", "model.safetensors"]),
    },
    "seed-vc-tiny": {
        "Plachta/Seed-VC": ("257283f9f41585055e8f858fba4fd044e5caed6e", ["DiT_uvit_tat_xlsr_ema.pth"]),
        "facebook/wav2vec2-xls-r-300m": ("1a640f32ac3e39899438a2931f9924c02f080a54",
                                         ["config.json", "preprocessor_config.json", "pytorch_model.bin"]),
        "funasr/campplus": ("e4b6ede7ce16997aff4ae69fbca1f0175e2afede", ["campplus_cn_common.bin"]),
        "FunAudioLLM/CosyVoice-300M": ("24c40509c3c5ea6fe06b5f8790ff99e3714a6bee", ["hift.pt"]),
    },
    "meanvc2": {
        "ASLP-lab/MeanVC2": ("39cdd19522fe896c227da691314d9a0e3b995486",
                            ["fastu2pp_80ms.pt", "meanvc2_40ms_40ms.safetensors", "vocos.pt"]),
    },
    "tvtsyn1": {
        "warisqr007/TVTSyn": ("e37063244a413f834fe976c1ba356d2ef37ece6c", ["config.json", "model.safetensors"]),
        "speechbrain/spkrec-xvect-voxceleb": ("56895a2df401be4150a159f3a1c653f00051d477",
                                             ["hyperparams.yaml", "classifier.ckpt", "embedding_model.ckpt",
                                              "label_encoder.txt", "mean_var_norm_emb.ckpt"]),
        "speechbrain/spkrec-ecapa-voxceleb": ("0f99f2d0ebe89ac095bcc5903c4dd8f72b367286",
                                             ["hyperparams.yaml", "classifier.ckpt", "embedding_model.ckpt",
                                              "label_encoder.txt", "mean_var_norm_emb.ckpt"]),
    },
}


def curl(url, *args):
    return subprocess.run(["curl.exe", "-fLsS", "--connect-timeout", "20", "--retry", "2",
                           *args, url], check=True, stdout=subprocess.PIPE).stdout


@contextmanager
def weight_lock(assets):
    assets.mkdir(parents=True, exist_ok=True)
    with (assets / ".downloads.lock").open("a+b") as lock:
        lock.seek(0)
        try:
            msvcrt.locking(lock.fileno(), msvcrt.LK_NBLCK, 1)
        except OSError as e:
            raise RuntimeError("A weights download is already running in this asset directory") from e
        try:
            yield
        finally:
            msvcrt.locking(lock.fileno(), msvcrt.LK_UNLCK, 1)


def download(root, url, name, size, sha256=None, blob=None):
    p = root / name
    p.parent.mkdir(parents=True, exist_ok=True)

    def verified(f):
        if not f.is_file() or f.stat().st_size != size:
            return False
        h = hashlib.sha256() if sha256 else hashlib.sha1()
        if not sha256:
            h.update(f"blob {size}\0".encode())
        with f.open("rb") as file:
            for chunk in iter(lambda: file.read(8 << 20), b""):
                h.update(chunk)
        return h.hexdigest() == (sha256 or blob)

    if verified(p):
        print("VERIFIED", p, flush=True)
        return
    part = p.with_name(p.name + ".partial")
    if not verified(part):
        if part.exists() and part.stat().st_size >= size:
            part.unlink()
        curl(url, "-C", "-", "-o", str(part))
    if not verified(part):
        raise ValueError(f"Digest or size mismatch: {p}")
    part.replace(p)
    print("VERIFIED", p, flush=True)


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("assets", type=Path)
    ap.add_argument("models", nargs="*", metavar="MODEL",
                    help="One or more of " + ", ".join(MODELS) + "; defaults to all")
    a = ap.parse_args()
    if set(a.models) - MODELS.keys():
        ap.error("Unknown models: " + ", ".join(sorted(set(a.models) - MODELS.keys())))
    jobs = []
    for model in dict.fromkeys(a.models or MODELS):
        for repo, (revision, files) in MODELS[model].items():
            base = "https://huggingface.co"
            metadata = json.loads(curl(f"{base}/api/models/{repo}/revision/{revision}?blobs=true"))
            if metadata["sha"] != revision:
                raise ValueError(f"Unpinned revision for {repo}")
            entries = {x["rfilename"]: x for x in metadata["siblings"]}
            for name in files:
                x = entries[name]
                jobs.append((a.assets / "weights" / repo.replace("/", "--"),
                             f"{base}/{repo}/resolve/{revision}/{name}", name, x["size"],
                             x.get("lfs", {}).get("sha256"), x["blobId"]))
        if model == "x-vc":
            for name, size, sha in (
                ("pretrained_eres2net.ckpt", 26725867, "d8941f5952e31820173c8854562cb6d7897aaa58cd65c18f30d5a2e52d30847d"),
                ("configuration.json", 405, "bbd0c639f5c73325ad9f080d9f8866b613f739546216dc090eabe65ed0bbdd18"),
            ):
                jobs.append((a.assets / "weights" / "eres2net",
                             "https://modelscope.cn/models/iic/speech_eres2net_sv_en_voxceleb_16k/resolve/master/" + name,
                             name, size, sha))
    with weight_lock(a.assets), concurrent.futures.ThreadPoolExecutor(max_workers=5) as pool:
        futures = [pool.submit(download, *j) for j in jobs]
        for f in concurrent.futures.as_completed(futures):
            f.result()


if __name__ == "__main__":
    main()
