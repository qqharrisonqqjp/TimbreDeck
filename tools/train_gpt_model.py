"""Fine-tune GPT-SoVITS v2Pro with previously prepared TimbreDeck features."""

import argparse
from contextlib import contextmanager
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile

from prepare_gpt_dataset import REVISION, sha256_file


@contextmanager
def lock_run(run):
    run.parent.mkdir(parents=True, exist_ok=True)
    with (run.parent / ("." + run.name + ".lock")).open("a+b") as lock:
        if os.name == "nt":
            import msvcrt

            if not lock.tell():
                lock.write(b"\0")
                lock.flush()
            lock.seek(0)
            msvcrt.locking(lock.fileno(), msvcrt.LK_NBLCK, 1)
        else:
            import fcntl

            fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        yield


def write_json(path, data):
    pending = path.with_suffix(".pending")
    pending.write_text(json.dumps(data, indent=2) + "\n", encoding="utf-8")
    pending.replace(path)


def latest_weights(folder, suffix):
    candidates = [p for p in folder.glob("*" + suffix) if p.is_file() and p.stat().st_size]
    if not candidates:
        raise ValueError(f"no {suffix} checkpoint was exported in {folder}")
    return max(candidates, key=lambda p: (p.stat().st_mtime_ns, p.name))


def train(engine, features, run, gpu, epochs, batch):
    if run.is_relative_to(features) or features.is_relative_to(run) or run.is_relative_to(engine):
        raise ValueError("choose a training run folder separate from the features and the engine")
    prepared = json.loads((features / "preparation.json").read_text(encoding="utf-8"))
    if prepared.get("expected_source_revision") != REVISION:
        raise ValueError("features must be prepared for the pinned v2Pro source")
    python = engine / "runtime" / "python.exe"
    gpt = engine / "GPT_SoVITS"
    weights = gpt / "pretrained_models"
    s1_base = weights / "s1v3.ckpt"
    s2_generator = weights / "v2Pro/s2Gv2Pro.pth"
    s2_discriminator = weights / "v2Pro/s2Dv2Pro.pth"
    stage_files = (gpt / "s1_train.py", gpt / "s2_train.py")
    for path in (python, s1_base, s2_generator, s2_discriminator, *stage_files):
        if not path.is_file() or not path.stat().st_size:
            raise ValueError(f"training requires {path}")
    env = dict(os.environ, PYTHONUTF8="1", PYTHONNOUSERSITE="1",
               HF_HUB_OFFLINE="1", TRANSFORMERS_OFFLINE="1", hz="25hz",
               CUDA_VISIBLE_DEVICES=gpu, _CUDA_VISIBLE_DEVICES=gpu,
               PATH=os.pathsep.join((str(python.parent), str(engine), os.environ.get("PATH", ""))))

    def command(*args, **kwargs):
        return subprocess.run([str(python), "-s", *map(str, args)], cwd=engine, env=env, check=True, **kwargs)

    command("-c", "import torch; raise SystemExit(0 if torch.cuda.is_available() else 'CUDA unavailable in training runtime')")
    inputs = ("preparation.json", "2-name2text.txt", "6-name2semantic.tsv")
    options = dict(source_revision=REVISION, gpu=gpu, epochs=epochs, batch_size=batch,
                   features={name: sha256_file(features / name) for name in inputs})
    if not run.exists():
        with tempfile.TemporaryDirectory(prefix=".gpt-train-", dir=run.parent) as temporary:
            stage = Path(temporary) / "run"
            stage.mkdir()
            shutil.copytree(features, stage / "data")
            write_json(stage / "run.json", options)
            stage.rename(run)
    if json.loads((run / "run.json").read_text(encoding="utf-8")) != options:
        raise ValueError("resume with the original GPU, options and prepared dataset")
    data = run / "data"
    if any(sha256_file(data / name) != options["features"][name] for name in inputs):
        raise ValueError("the training run's feature index changed")

    s1 = json.loads(command("-c", "import json,sys,yaml; print(json.dumps(yaml.safe_load(open(sys.argv[1], encoding='utf-8'))))",
                            gpt / "configs/s1longer-v2.yaml", capture_output=True, text=True, encoding="utf-8").stdout)
    s1.update(pretrained_s1=str(s1_base), train_semantic_path=str(data / "6-name2semantic.tsv"),
              train_phoneme_path=str(data / "2-name2text.txt"), output_dir=str(run / "logs_s1_v2Pro"))
    s1["train"].update(epochs=epochs, batch_size=batch, if_dpo=False,
                       if_save_every_weights=True, if_save_latest=True, save_every_n_epoch=1,
                       half_weights_save_dir=str(run / "s1-weights"), exp_name="timbredeck")
    config_path = run / "s1.json"
    write_json(config_path, s1)
    (run / "s1-weights").mkdir(exist_ok=True)

    s2 = json.loads((gpt / "configs/s2v2Pro.json").read_text(encoding="utf-8"))
    s2.update(s2_ckpt_dir=str(data), save_weight_dir=str(run / "s2-weights"),
              name="timbredeck", version="v2Pro")
    s2["model"]["version"] = "v2Pro"
    s2["data"]["exp_dir"] = str(data)
    s2["train"].update(epochs=epochs, batch_size=batch, save_every_epoch=1, gpu_numbers=gpu,
                       pretrained_s2G=str(s2_generator), pretrained_s2D=str(s2_discriminator),
                       if_save_every_weights=True, if_save_latest=True, lora_rank=0)
    write_json(run / "s2.json", s2)
    (run / "s2-weights").mkdir(exist_ok=True)
    (data / "logs_s2_v2Pro").mkdir(exist_ok=True)

    stages = (
        ("s1", gpt / "s1_train.py", "--config_file", ".ckpt"),
        ("s2", gpt / "s2_train.py", "--config", ".pth"),
    )
    exports = []
    for stage, script, flag, suffix in stages:
        marker = run / (stage + "-complete.json")
        if marker.is_file():
            filename = json.loads(marker.read_text(encoding="utf-8"))["file"]
            if filename != Path(filename).name:
                raise ValueError("invalid checkpoint marker")
            checkpoint = run / (stage + "-weights") / filename
            if not checkpoint.is_file() or not checkpoint.stat().st_size:
                raise ValueError(f"missing completed checkpoint: {checkpoint}")
        else:
            command(script, flag, run / (stage + ".json"))
            checkpoint = latest_weights(run / (stage + "-weights"), suffix)
            write_json(marker, {"file": checkpoint.name})
        exports.append(checkpoint)
    write_json(run / "model.json", dict(
        engine="gpt-sovits", model="v2Pro", source_revision=REVISION,
        gpt=str(exports[0].relative_to(run).as_posix()),
        sovits=str(exports[1].relative_to(run).as_posix()),
    ))
    print(run / "model.json")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("engine", type=Path, help="complete GPT-SoVITS v2Pro root")
    parser.add_argument("features", type=Path, help="output of prepare_gpt_dataset.py")
    parser.add_argument("run", type=Path, help="new training folder or earlier run to resume")
    parser.add_argument("--gpu", default="0", help="one CUDA device index")
    parser.add_argument("--epochs", type=int, default=8)
    parser.add_argument("--batch", type=int, default=2)
    args = parser.parse_args()
    if not re.fullmatch(r"[0-9]+", args.gpu) or args.epochs < 1 or args.batch < 1:
        parser.error("specify a valid CUDA device and positive training options")
    try:
        run = args.run.resolve()
        with lock_run(run):
            train(args.engine.resolve(), args.features.resolve(), run, args.gpu, args.epochs, args.batch)
    except (OSError, ValueError, subprocess.CalledProcessError) as exc:
        parser.exit(1, f"{exc}\n")


if __name__ == "__main__":
    main()
