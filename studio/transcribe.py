"""Suggest transcripts for saved clips using a local CTranslate2 model."""
import json
from pathlib import Path
import sys

from faster_whisper import WhisperModel


def main():
    job = json.load(sys.stdin)
    recognizer = WhisperModel(job["model"], device="cpu", compute_type="int8", local_files_only=True)
    transcripts = {}
    for clip in job["clips"]:
        language = clip["language"]
        if language == "yue" and "yue" not in recognizer.supported_languages:
            language = "zh"
        segments, _ = recognizer.transcribe(clip["path"], language=language)
        text = "".join(segment.text for segment in segments)
        transcripts[clip["id"]] = " ".join(text.split()).replace("|", "｜")[:10000]
    Path(job["output"]).write_text(json.dumps(transcripts, ensure_ascii=False), encoding="utf-8")


if __name__ == "__main__":
    main()
