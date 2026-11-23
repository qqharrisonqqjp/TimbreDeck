# TimbreDeck 聲台

繁體中文｜Windows 免安裝多引擎語音工作室。使用者先選引擎，再進入語音合成、聲線設計或模型訓練。規劃以本倉庫的 Releases 按需下載引擎、模型與執行環境。

English | A portable multi-engine speech studio for Windows. Select an engine, then choose synthesis, voice design, or model training where supported. The planned launcher downloads versioned engine packs, models and runtimes on demand from this repository’s Releases.

## 專案狀態 · Project status

Windows x64 啟動器預覽版已加入。本機工作室可匯入 WAV、分開保存各引擎的設定、試讀第一句、生成全文、取消作業，以及從輸出歷史試聽或匯出。GPT-SoVITS v2Pro 與 CosyVoice 3 已有合成介接程式，使用時需要完整本機環境。其餘三款引擎及訓練仍在規劃中。這一版的自動測試以測試替身驗證流程，真實模型的 Windows 顯示卡推理尚待驗收。

**[下載 Windows 預覽版 / Windows previews](https://github.com/qqharrisonqqjp/TimbreDeck/releases)** · [啟動及引擎設定 / Getting started](docs/PREVIEW.md)

The preview includes a standalone Windows x64 launcher and a local studio for drafting, reference WAV import, synthesis jobs, playback and export. The GPT-SoVITS and CosyVoice adapters require complete local engine installations. Windows GPU inference with real weights is pending. The other engines and training are planned.

| Engine | 預定工作流程 / Planned workflow |
|---|---|
| GPT-SoVITS v2Pro | 錄音複製、角色微調 / Reference cloning and voice fine-tuning |
| CosyVoice 3 | 粵語、指令朗讀 / Cantonese and instruction-guided synthesis |
| VoxCPM2 | 聲線設計、可控複製 / Voice design and controllable cloning |
| IndexTTS-2 | 分開提供音色與情緒錄音 / Independent voice and emotion references |
| Qwen3-TTS | Base、CustomVoice、VoiceDesign / Cloning, preset voices and voice design |

## 介面與發佈 · Interface and distribution

左上角顯示 TimbreDeck 聲台，右上角切換五款引擎。次目錄按引擎提供可用的工作模式。逐字稿與輸出台詞各自有獨立欄位。完整五引擎安裝暫估 65–95 GB，建議預留 100–120 GB 可用空間作安裝與更新。

The product name stays at the top left. The engine switcher stays at the top right. Each engine has its own workspace, secondary navigation and controls. Reference transcripts and output scripts have separate fields. A complete installation is budgeted at 65–95 GB, with 100–120 GB of free space recommended for setup and updates. Final sizes require Windows measurements.

[繁體中文開發規格與容量計算](docs/PLAN.zh-Hant.md) · [引擎版本與授權 / Upstream revisions and licenses](engines/README.md)
