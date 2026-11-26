# ASR 模型比較與實測方式

磁碟大小只可比較同一種權重格式。下表的「實裝」是這台 Windows 電腦的檔案大小與能實測的內容；FP16 權重在 CPU INT8 計算，不會令磁碟檔自動變小。顯存／記憶體、速度及準確度不採用跨機器的固定宣稱。選模型時，工作室在選單和「比較模型」顯示用途及就緒狀態。

| 模型 | 本版狀態／磁碟 | 語言或用法重點 |
|---|---|---|
| Faster Whisper Small | 實裝 486.2 MB；CPU INT8 | 多語、預設後備；以中文模式處理粵語會提示校對。 |
| SenseVoice-Small | 實裝 239.5 MB，ONNX INT8 | 粵語、普通話、英、日、韓；本工作室只使用轉錄，不將情緒及背景音標籤填入逐字稿。 |
| Faster Whisper Large v3 Turbo | 實裝 1621.7 MB；CPU INT8 | 少於完整 v3 的解碼層，適合試快速通用轉錄。 |
| Faster Whisper Large v3 | 實裝 3090.8 MB；CPU INT8 | 完整解碼器；已實測可指定原生粵語 token，個別用字仍需校對。本介面只作轉錄。 |
| Qwen3-ASR 0.6B | 實裝 1880.6 MB；CPU | 較輕量的多語及方言選項；用獨立粵語合成樣本覆核。 |
| Qwen3-ASR 1.7B | 實裝 4703.1 MB；CPU | 同系列較大型權重；不保證在每個語言或噪音條件都較準。 |
| Fun-ASR-Nano | 實裝 1009.6 MB；Sherpa ONNX INT8 | 上游標準版集中中、英、日及多種中文方言。VAD、說話人分離另需管線；未整合說話人分離。 |
| Fun-ASR-MLT-Nano | 實裝 1009.8 MB；社群 ONNX INT8 | 上游模型卡標示 31 語種；本機英文短句已確認，未逐語言驗收。 |
| hon9kon9ize 粵語 Whisper | 待確認指定版本的權重連結及部署格式 | 同名的 [whisper-large-v3-cantonese](https://huggingface.co/khleeloo/whisper-large-v3-cantonese) 由另一个作者發佈且需要接受存取條件，沒有自行替換。可先用已驗證的 SenseVoice 或 Qwen3-ASR 0.6B。 |

## 同料比較

用「ASR 擂臺」選同一源音、指定語言，至少勾兩款。可分為英文控制、口語粵語、粵英混說及背景音四組。各組以人工逐字稿為準；中文可算 CER，英文可算 WER，專名與數字另作人工檢查。只比較未快取的首次時間，並另外記錄常駐後速度。拿到非空文字不等於通過準確率檢驗。

上游：[SenseVoice](https://github.com/QwenAudio/SenseVoice) · [Faster Whisper](https://github.com/SYSTRAN/faster-whisper) · [Large v3](https://huggingface.co/Systran/faster-whisper-large-v3) · [Turbo CTranslate2](https://huggingface.co/dropbox-dash/faster-whisper-large-v3-turbo) · [Qwen3-ASR](https://github.com/QwenLM/Qwen3-ASR) · [Fun-ASR-Nano](https://huggingface.co/FunAudioLLM/Fun-ASR-Nano-2512) · [Fun-ASR-MLT-Nano](https://huggingface.co/FunAudioLLM/Fun-ASR-MLT-Nano-2512)

實際權重：[Nano Sherpa 轉換](https://k2-fsa.github.io/sherpa/onnx/funasr-nano/pretrained.html) · [MLT Nano 社群轉換](https://huggingface.co/luigi000/fun-asr-mlt-nano-onnx-int8-1024)。部署前使用 `tools/fetch_asr_models.py` 查各權重的不可變版本並核對 Hugging Face 檔案校驗。Qwen 使用既有 Qwen3-TTS 的獨立 Python 3.11、範例額外依賴套件 `qwen-asr==0.0.6`、`transformers==4.57.6`、`nagisa==0.2.11`、`DyNet38==2.2`、`six==1.17.0`、`soynlp==0.0.493`、`qwen-omni-utils==0.0.9` 和 `sox==1.5.0` 安裝在 ASR 資產的 `qwen-asr-python`，不混入 TTS。
