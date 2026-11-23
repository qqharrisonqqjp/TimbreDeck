# TimbreDeck 聲台

繁體中文｜Windows 免安裝多引擎語音工作室。使用者先選引擎，再進入語音合成、聲線設計或模型訓練。規劃以本倉庫的 Releases 按需下載引擎、模型與執行環境。

English | A portable multi-engine speech studio for Windows. Select an engine, then choose synthesis, voice design, or model training where supported. The planned launcher downloads versioned engine packs, models and runtimes on demand from this repository’s Releases.

## 專案狀態 · Project status

本版收錄設計規格及鎖定版本的引擎原始碼，Windows 啟動器與引擎包待開發和實測。
This planning snapshot includes the specification and pinned engine sources. The launcher and Windows runtime packs are scheduled deliverables.

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
