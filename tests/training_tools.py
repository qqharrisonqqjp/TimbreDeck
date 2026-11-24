"""Exercise the pinned GPT-SoVITS command contracts with local worker fixtures."""

import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
from tempfile import TemporaryDirectory
import wave

from smoke import file


REPO = Path(__file__).resolve().parents[1]
SAMPLE = "a" * 32 + ".wav"
SPEAKER = "b" * 32
COMMON = """import os
from pathlib import Path
assert os.environ["HF_HUB_OFFLINE"] == "1"
out = Path(os.environ["opt_dir"])
def make(folder, name):
    (out/folder).mkdir(exist_ok=True)
    (out/folder/name).write_bytes(b"fixture")
"""


def main():
    with TemporaryDirectory() as name:
        root = Path(name).resolve() / "聲台 模組"
        engine = root / "engine"
        executable = engine / "runtime/python.exe"
        executable.parent.mkdir(parents=True)
        env = os.environ.copy()
        if os.name == "nt":
            shutil.copy(sys.executable, executable)
            env.update(PYTHONHOME=sys.base_prefix, PATH=sys.base_prefix + os.pathsep + env["PATH"])
        else:
            executable.symlink_to(sys.executable)
        gpt = engine / "GPT_SoVITS"
        config = gpt / "configs"
        file(config / "s2v2Pro.json", '{"train": {}, "model": {}, "data": {}}')
        file(config / "s1longer-v2.yaml", 'train: {}')
        stage = gpt / "prepare_datasets"
        file(stage / "1-get-text.py", COMMON +
             f'(out/"2-name2text-0.txt").write_text("{SAMPLE}\\tphones\\t1\\ttext\\n")')
        file(stage / "2-get-hubert-wav32k.py", COMMON +
             f'make("4-cnhubert", "{SAMPLE}.pt"); make("5-wav32k", "{SAMPLE}")')
        file(stage / "2-get-sv.py", COMMON + f'make("7-sv_cn", "{SAMPLE}.pt")')
        file(stage / "3-get-semantic.py", COMMON +
             f'(out/"6-name2semantic-0.tsv").write_text("{SAMPLE}\\t1 2 3\\n")')
        dataset = root / "dataset"
        (dataset / "wavs").mkdir(parents=True)
        file(dataset / "dataset.list", f'wavs/{SAMPLE}|{SPEAKER}|YUE|訓練句。\n')
        with wave.open(str(dataset / "wavs" / SAMPLE), "wb") as audio:
            audio.setparams((1, 2, 16000, 0, "NONE", "NONE"))
            audio.writeframes(b"\x00\x10" * 16000)

        features = root / "features"
        prep = [sys.executable, str(REPO / "tools/prepare_gpt_dataset.py"), str(engine), str(dataset)]
        subprocess.run([*prep, str(features)], env=env, check=True)
        assert (features / "6-name2semantic.tsv").read_text().splitlines()[0] == "item_name\tsemantic_audio"
        meta = json.loads((features / "preparation.json").read_text())
        assert SAMPLE in meta["wav_sha256"]
        file(stage / "2-get-sv.py", COMMON)
        failed = root / "missing-feature"
        result = subprocess.run([*prep, str(failed)], env=env, capture_output=True)
        assert result.returncode == 1 and b"missing feature: 7-sv_cn" in result.stderr
        assert not failed.exists()

        for checkpoint in ("s1v3.ckpt", "v2Pro/s2Gv2Pro.pth", "v2Pro/s2Dv2Pro.pth"):
            file(gpt / "pretrained_models" / checkpoint, "loaded")
        file(engine / "torch.py", "class cuda:\n    is_available = staticmethod(lambda: True)\n")
        file(engine / "yaml.py", "def safe_load(source): return {'train': {}}\n")
        file(gpt / "s1_train.py", """
import json,sys
from pathlib import Path
assert sys.argv[1] == '--config_file'
config = json.loads(Path(sys.argv[2]).read_text())
assert Path(config['train_semantic_path']).is_file()
assert Path(config['pretrained_s1']).name == 's1v3.ckpt'
output = Path(config['train']['half_weights_save_dir']) / 'trained.ckpt'
assert not output.exists(), 'completed S1 was run again'
output.write_bytes(b'S1')
""")
        stage_two = gpt / "s2_train.py"
        file(stage_two, "raise SystemExit(3)\n")
        run = root / "run"
        train = [sys.executable, str(REPO / "tools/train_gpt_model.py"), str(engine), str(features), str(run)]
        result = subprocess.run(train, env=env, capture_output=True)
        assert result.returncode == 1 and (run / "s1-complete.json").is_file()
        assert not (run / "model.json").exists()
        file(stage_two, """
import json,sys
from pathlib import Path
assert sys.argv[1] == '--config'
config = json.loads(Path(sys.argv[2]).read_text())
assert config['version'] == 'v2Pro'
assert Path(config['train']['pretrained_s2D']).name == 's2Dv2Pro.pth'
assert (Path(config['data']['exp_dir']) / '7-sv_cn').is_dir()
(Path(config['save_weight_dir']) / 'trained.pth').write_bytes(b'S2')
""")
        subprocess.run(train, env=env, check=True)
        model = json.loads((run / "model.json").read_text())
        assert (run / model["gpt"]).read_bytes() == b"S1"
        assert (run / model["sovits"]).read_bytes() == b"S2"

    print("GPT-SoVITS training tool fixtures passed: preparation, missing files, interrupted S2, resume and paired model.")


if __name__ == "__main__":
    main()
