# Windows 實機驗收與重測

此候選版由 `80ebf58` 衍生。個人電腦已有五款引擎時，用本機交付目錄的 `Open-Studio.cmd`：它會開啟同一個持久工作室。單獨解壓程式包到其他電腦，只會得到程式、工具及源碼，須另外安裝或連接相容的引擎和模型。請勿同時用兩個啟動器開同一個資料目錄。

## 比較聲線與語言

1. 進入「聲音庫」，選取參考錄音；匯入新錄音時請填寫**錄音本身的語言與逐字稿**，並非打算生成的台詞。GPT-SoVITS 的參考 WAV 應為 3–10 秒。
2. 到「語音合成」，選引擎、模式及輸出設定。先生成與參考同語言的一句，再只改台詞及輸出語言。沒有提供獨立語言選項的引擎，請以實際發音作準。Qwen 的中文或自動設定不是獨立粵語模型。預設講者與聲線設計不使用參考音色。
3. 在「輸出歷史」選兩筆並排試聽。先核對文字與咬字，再比較停頓、語調及音色。自動轉錄只可輔助判讀，不能證明是地道粵語。
4. 新版會就每個成功作業核對波形解碼與指定主模型模組是否同在一張 CUDA 顯示卡，再顯示「已核對波形及主模型」。舊歷史若缺證據則顯示「未有完整 CUDA 作業證據」；不表示作業一定用了 CPU。

## 重跑單一模式或完整矩陣

關閉使用同一資料夾的工作室，在源碼目錄以 PowerShell 執行以下範例，並把路徑換成本機路徑。結果目錄必須是新目錄；引擎須已連接、模型須已安裝。不要對只想保留不動的工作室做重測，因為實際生成會新增錄音及歷史。

```powershell
python -B -X utf8 tests/acceptance_matrix.py `
  --launcher 'D:\桌面工作室\Start.exe' --data 'D:\桌面工作室\StudioData' `
  --reference 'D:\試聽\english.wav' --transcript 'At the top, the screen displays some stats.' `
  --text '今晚八點半喺旺角站等。' --ref-lang en --language yue --qwen-language zh `
  --mode clone --require-cuda --output 'D:\試聽\新一輪比較'
```

- **15 模式**：改為 `--mode all`，加上 `--emotion-reference 'D:\試聽\emotion.wav'`。需齊備 Qwen 的 Base、CustomVoice、VoiceDesign 三款模型。情緒示範可與音色參考來自不同錄音。
- **單款單模式**：例如 `--engine qwen3-tts --mode simple-clone`；同語言英文控制組可用 `--language en --qwen-language en`，並提供英文台詞。
- `comparison.json` 保存成功及失敗的作業、時間、模型、波形雜湊與 CUDA 證據；僅因系統裝有 CUDA、或 PyTorch 接受到 GPU，並不足以判作通過。
- 使用正確逐字稿的語音複製不等於拿錄音訓練；不要將只獲准作音色參考的檔案加入訓練素材。

## 資料保留

引擎、官方模型、微調模型、參考錄音與生成歷史是不同物件。卸載引擎不是清理錄音；要清理錄音請在設定內確認。搬移工作室前先關閉程式並備份整個資料夾，同時記錄外接引擎的路徑。
