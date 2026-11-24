"""Prepare GPT-SoVITS v2Pro features from an exported TimbreDeck dataset."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile


REVISION = "d7c2210da8c013e81a94bfc7b811a477c99fd506"
STAGES = (
    "1-get-text.py",
    "2-get-hubert-wav32k.py",
    "2-get-sv.py",
    "3-get-semantic.py",
)


def sha256_file(path):
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def read_dataset(root):
    paths = {}
    for line in (root / "dataset.list").read_text(encoding="utf-8").splitlines():
        path, speaker, language, transcript = line.split("|", 3)
        if (not re.fullmatch(r"wavs/[0-9a-f]{32}\.wav", path)
                or not re.fullmatch(r"[0-9a-f]{32}", speaker)
                or language not in {"YUE", "ZH", "EN", "JA", "KO"} or not transcript.strip()
                or "|" in transcript):
            raise ValueError(f"invalid training entry: {path}")
        source = root / path
        if not source.is_file() or source.resolve() != source or source.name in paths:
            raise ValueError(f"missing, linked or repeated WAV: {path}")
        paths[source.name] = sha256_file(source)
    if not paths:
        raise ValueError("dataset.list contains no training WAVs")
    return paths


def check_rows(path, expected, header=None):
    rows = path.read_text(encoding="utf-8").splitlines()
    if header and rows and rows[0] == header:
        rows = rows[1:]
    names = [row.split("\t", 1)[0] for row in rows if row.strip()]
    if set(names) != set(expected) or len(names) != len(expected):
        raise ValueError(f"{path.name} does not contain exactly the exported training entries")
    return rows


def prepare(engine, dataset, output, gpu, model_root=None):
    if output.exists():
        raise FileExistsError(output)
    paths = read_dataset(dataset)
    list_path = dataset / "dataset.list"
    list_hash = sha256_file(list_path)
    gpt = engine / "GPT_SoVITS"
    weights = (model_root or engine) / "GPT_SoVITS/pretrained_models"
    python = engine / "runtime" / "python.exe"
    config = gpt / "configs" / "s2v2Pro.json"
    scripts = [gpt / "prepare_datasets" / name for name in STAGES]
    if not all(p.is_file() for p in (python, config, *scripts)):
        raise ValueError("supply a complete portable GPT-SoVITS v2Pro installation with its training scripts")
    output.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix=".gpt-prepare-", dir=output.parent) as temporary:
        run = Path(temporary) / "features"
        run.mkdir()
        env = dict(os.environ, inp_text=str(list_path), inp_wav_dir=str(dataset / "wavs"),
                   exp_name="timbredeck", opt_dir=str(run), i_part="0", all_parts="1",
                   _CUDA_VISIBLE_DEVICES=gpu or "", CUDA_VISIBLE_DEVICES=gpu or "",
                   is_half="False", HF_HUB_OFFLINE="1", TRANSFORMERS_OFFLINE="1",
                   PYTHONUTF8="1", PYTHONNOUSERSITE="1",
                   bert_pretrained_dir=str(weights / "chinese-roberta-wwm-ext-large"),
                   cnhubert_base_dir=str(weights / "chinese-hubert-base"),
                   sv_path=str(weights / "sv" / "pretrained_eres2netv2w24s4ep4.ckpt"),
                   pretrained_s2G=str(weights / "v2Pro" / "s2Gv2Pro.pth"),
                   s2config_path=str(config),
                   PATH=os.pathsep.join((str(python.parent), str(engine), os.environ.get("PATH", ""))))
        for script in scripts:
            print(script.name, flush=True)
            subprocess.run([str(python), "-s", str(script)], cwd=engine, env=env, check=True)
        text = check_rows(run / "2-name2text-0.txt", paths)
        semantic_header = "item_name\tsemantic_audio"
        semantic = check_rows(run / "6-name2semantic-0.tsv", paths, semantic_header)
        for folder, suffix in (("4-cnhubert", ".pt"), ("5-wav32k", ""), ("7-sv_cn", ".pt")):
            for name in paths:
                item = run / folder / (name + suffix)
                if not item.is_file() or not item.stat().st_size:
                    raise ValueError(f"missing feature: {item.relative_to(run)}")
        if read_dataset(dataset) != paths or sha256_file(list_path) != list_hash:
            raise ValueError("training material changed during feature preparation")
        (run / "2-name2text.txt").write_text("\n".join(text) + "\n", encoding="utf-8")
        (run / "6-name2semantic.tsv").write_text("\n".join([semantic_header, *semantic]) + "\n", encoding="utf-8")
        (run / "preparation.json").write_text(json.dumps(dict(
            engine="GPT-SoVITS v2Pro", expected_source_revision=REVISION,
            dataset_sha256=list_hash, wav_sha256=paths, selected_gpu=gpu,
        ), indent=2) + "\n", encoding="utf-8")
        run.rename(output)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("engine", type=Path, help="complete GPT-SoVITS v2Pro root")
    parser.add_argument("dataset", type=Path, help="extracted TimbreDeck export")
    parser.add_argument("output", type=Path, help="new feature output directory")
    parser.add_argument("--gpu", help="one CUDA device index")
    parser.add_argument("--models", type=Path, help="separately installed v2Pro model pack root")
    args = parser.parse_args()
    if args.gpu is not None and not re.fullmatch(r"[0-9]+", args.gpu):
        parser.error("specify one nonnegative CUDA device index")
    try:
        prepare(args.engine.resolve(), args.dataset.resolve(), args.output.resolve(), args.gpu,
                args.models.resolve() if args.models else None)
    except (OSError, ValueError, subprocess.CalledProcessError) as exc:
        parser.exit(1, f"{exc}\n")


if __name__ == "__main__":
    main()
