# TimbreDeck 聲台｜Windows 五引擎語音工作室

資料核對日期　2026 年 11 月 23 日  
範圍　GPT-SoVITS、CosyVoice 3、VoxCPM2、IndexTTS-2、Qwen3-TTS。第一版目標是 Windows 10／11 64 位元、本機生成、繁體中文操作、按鍵切換五個引擎。本文是規劃文件，所有硬件相容性及聽感仍待 Windows 實測。

## 1　官方倉庫與下載位置

「程式倉庫」放程式碼與使用說明。「模型權重」是讓電腦實際生成聲音的檔案。「整合包」則可能同時包含程式、執行環境和模型。下載頁內往往有多個版本，實際打包時應固定選定版本。

| 引擎 | 程式倉庫 | 模型與可用整合包 |
|---|---|---|
| GPT-SoVITS | [RVC-Boss/GPT-SoVITS](https://github.com/RVC-Boss/GPT-SoVITS) | [模型總庫](https://huggingface.co/lj1995/GPT-SoVITS) · [Windows 整合包列表](https://huggingface.co/lj1995/GPT-SoVITS-windows-package/tree/main) · [官方版本說明](https://github.com/RVC-Boss/GPT-SoVITS/releases)。首輪測試選有現成 Windows 包的 v2Pro 系列。官網分別提供一般 NVIDIA 版和 RTX 50 系列版。 |
| CosyVoice 3 | [QwenAudio/CosyVoice](https://github.com/QwenAudio/CosyVoice) | [Fun-CosyVoice3-0.5B-2512 模型與檔案](https://huggingface.co/FunAudioLLM/Fun-CosyVoice3-0.5B-2512/tree/main) · [官方使用範例](https://huggingface.co/FunAudioLLM/Fun-CosyVoice3-0.5B-2512)。下載管理器須選第 3 代的權重。 |
| VoxCPM2 | [OpenBMB/VoxCPM](https://github.com/OpenBMB/VoxCPM) | [openbmb/VoxCPM2 模型](https://huggingface.co/openbmb/VoxCPM2) · [檔案列表](https://huggingface.co/openbmb/VoxCPM2/tree/main)。程式倉庫與第二代模型的名稱有分別。 |
| IndexTTS-2 | [index-tts/index-tts](https://github.com/index-tts/index-tts) | [IndexTeam/IndexTTS-2 權重](https://huggingface.co/IndexTeam/IndexTTS-2/tree/main) · [2026 年第三方一鍵包](https://aigcpanel.com/zh/asset/IndexTTS2)。AIGCPanel 的 v1.2.1 是其套件版本，頁面日期為 2026-08-22。官方另有獨立模型 IndexTTS-2.5，首版按指定選 2。 |
| Qwen3-TTS | [QwenLM/Qwen3-TTS](https://github.com/QwenLM/Qwen3-TTS) | [官方模型全集](https://huggingface.co/collections/Qwen/qwen3-tts) · [Base（參考聲音複製）](https://huggingface.co/Qwen/Qwen3-TTS-12Hz-1.7B-Base) · [CustomVoice（預設聲線）](https://huggingface.co/Qwen/Qwen3-TTS-12Hz-1.7B-CustomVoice) · [VoiceDesign（文字設計聲線）](https://huggingface.co/Qwen/Qwen3-TTS-12Hz-1.7B-VoiceDesign)。以上三個直達連結均為 1.7B 版本。 |

## 2　使用者要提供甚麼

音色指「像誰在說」，例如聲線的厚薄與明亮程度。語氣指「怎樣說」，例如壓低聲音安慰別人。逐字稿指「參考錄音裏實際說了甚麼」，跟「這次想生成的台詞」是兩個不同欄位。

| 引擎 | 免訓練的最短流程 | 語氣怎樣控制 | 是否值得另設訓練頁 |
|---|---|---|---|
| GPT-SoVITS | 放一段乾淨單人錄音，核對參考逐字稿，輸入新台詞。官方以約 5 秒錄音示範。 | 可換用同一人不同語氣的錄音參考。首版提供錄音預聽、換帶感情的示範和語速設定。 | 最值得優先做。官方示範約 1 分鐘素材已可開始微調，素材品質與涵蓋的發音會影響成果。 |
| CosyVoice 3 | 標準複製用錄音加逐字稿。官方指令模式另接受「參考錄音＋講法指示」，適合做廣東話旁白。 | 用文字指定廣東話、情緒、語速、音量等。可做「自然旁白」「慢慢解釋」等可修改的按鈕。 | 官方另有訓練流程。日常複製及指令模式可直接使用。 |
| VoxCPM2 | 放一段錄音即可複製。加上精確逐字稿可用高相似度延續模式。也可以只用文字描述創作聲線。 | 可沿用錄音的音色，同時指定「語速稍慢、帶笑意」等說法。模型原生輸出 48 kHz。 | 官方支援 LoRA（只訓練少量附加參數）與完整微調。可作第二階段擴充。 |
| IndexTTS-2 | 放一段音色錄音，輸入新台詞。 | 可另放第二段情緒示範、填寫情緒描述，或調整八項情緒比例。使用情緒錄音時可另調強度。 | 第一版可把整頁用於角色演繹，無需訓練才能使用上述模式。 |
| Qwen3-TTS | Base 複製錄音，完整模式配逐字稿，精簡模式只抽取聲線特徵。CustomVoice 可直接選內建說話者。VoiceDesign 只需文字描述。 | 1.7B CustomVoice 支援對預設聲線下講法指令。VoiceDesign 依描述造聲。官方功能表只在這兩個版本標示文字指令控制。 | Base 可作微調起點。可先提供模型切換，後續才加入訓練工作流。 |

粵語方面，GPT-SoVITS 列出 yue，CosyVoice 3 有實際廣東話指令範例，VoxCPM2 明列粵語。IndexTTS-2 模型卡列中、英。Qwen3-TTS 本地模型列 Chinese，未獨立列粵語。因此先將前三款列入「粵語優先試聽」，後兩款待驗證。採用香港口語台詞測讀音、語尾助詞與中英夾雜。[GPT-SoVITS](https://github.com/RVC-Boss/GPT-SoVITS) · [CosyVoice 3](https://huggingface.co/FunAudioLLM/Fun-CosyVoice3-0.5B-2512) · [VoxCPM2](https://github.com/OpenBMB/VoxCPM) · [IndexTTS-2](https://huggingface.co/IndexTeam/IndexTTS-2) · [Qwen3-TTS](https://github.com/QwenLM/Qwen3-TTS)

## 3　介面及操作

左上角固定顯示 **TimbreDeck 聲台**。右上角固定放五個引擎切換鍵，作為主目錄。窄視窗收合成顯示目前引擎名稱的切換器，位置仍在右上角。選定引擎後更新次目錄、工作區、參數面板和主要動作；保留品牌位置與導覽層級。每個引擎分別記住上次的工作模式、模型和設定。

| 引擎 | 次目錄設計 | 選定模式後的畫面 |
|---|---|---|
| GPT-SoVITS | 語音合成、模型訓練、模型管理 | 合成先選基礎／角色模型，再選參考錄音和台詞。訓練切換為素材、切片校對、訓練設定、試聽對照。 |
| CosyVoice 3 | 語音合成、模型管理；訓練於接通後開放 | 合成模式先選錄音複製、跨語言或指令朗讀，再顯示對應參考資料及指令。 |
| VoxCPM2 | 語音合成、聲線設計、模型管理；微調於接通後開放 | 合成頁放錄音與講法，設計頁以聲線描述取代錄音。 |
| IndexTTS-2 | 語音合成、模型管理 | 角色音色常駐，按情緒來源切換第二段示範錄音、文字描述或八項比例，獨立預聽。 |
| Qwen3-TTS | 語音合成、聲線設計、模型管理；微調於接通後開放 | 合成頁選 Base 或 CustomVoice；聲線設計頁使用 VoiceDesign。所選模型決定顯示參考錄音、預設說話者或文字指令。 |

工作順序為 **引擎 → 工作模式 → 模型 → 素材／參數 → 試聽／生成或訓練**。保留「要讀出的文字」和「參考錄音原本說了甚麼？」兩個獨立欄位；音色示範與情緒示範可分開播放。切換時保留草稿和相容素材，每款引擎自行保存專屬設定。進行中的作業顯示狀態，切換畫面不會丟失工作。

- **聲音庫**　匯入一次單人錄音，儲存為角色。角色卡保留錄音、語言和可修正的逐字稿，另列各引擎已生成的專用資料、預設聲線或微調模型。原始錄音和逐字稿可以跨引擎共用，每款引擎分別建立自己的角色模型。
- **IndexTTS-2 示範**　先選「角色聲音」，再選「跟隨原錄音／文字描述／情緒錄音／手動調整」其中一項。揀「情緒錄音」才顯示第二個上載框、獨立播放鍵和強度滑桿。第二段可以是另一個人的演繹。介面例句可寫「保留角色 A 的聲線，參考 B 這句由平靜轉為委屈的講法」。
- **Qwen3-TTS 示範**　顯示「複製我的聲音」「選現成聲線」「描述一把新聲」。按所選模式下載對應子模型。選擇現成聲線並要求語氣控制時，使用 1.7B CustomVoice。輸入聲線描述與輸入朗讀台詞用兩個文字框。
- **GPT-SoVITS 訓練精靈**　匯入素材 → 自動切句和初步轉錄 → 使用者逐句試聽、修改或剔除 → 開始微調 → 用同一句比較原版與微調後的聲音 → 儲存角色。每一關均保存進度，保留原始素材。
- **歷史與匯出**　提供「試讀第一句」「生成全文」「匯出 WAV 音檔」「開啟輸出資料夾」。同一段台詞產生多個版本時，並排顯示引擎、聲音、語氣設定及播放鍵。預設保留引擎原始取樣率。

## 4　Windows 免安裝與資料夾

同一個 TimbreDeck GitHub 倉庫保存引擎來源版本、整合程式及繁體中文與英文說明。其 Releases（版本發佈頁）提供輕量主程式與獨立引擎包。使用者按「下載這個引擎」後，由本倉庫指定版本的發佈附件取得引擎、執行環境及配套模型。雙擊 Start.exe 開啟本機 HTML（網頁）介面。啟動器管理本機語音引擎，使用者無需自行安裝 Python、Git 或輸入指令。首次可只選想用的一款，日後按鍵添加其餘四款。下載頁應先列下載量、解壓所需空間和可用磁碟空間，並支援從其他電腦匯入已下載的引擎包。顯示卡驅動仍由 Windows 系統提供。

| 擬定位置 | 作用 |
|---|---|
| Start.exe、app/ | 啟動程式、HTML 介面和本機任務控制。 |
| engines/{引擎}/ | 每個引擎的程式及獨立執行環境，固定版本。五個子目錄為 gpt-sovits、cosyvoice3、voxcpm2、indextts2、qwen3-tts。 |
| models/{引擎}/{版本}/ | 各引擎的主模型及必需輔助模型。Qwen 再按 Base、CustomVoice、VoiceDesign 分開。 |
| voices/{角色}/ | 原始錄音、參考逐字稿、角色封面與各引擎專用的聲音資料。 |
| training/{引擎}/{角色}/ | 切片、訓練進度、設定及訓練產生的模型。 |
| outputs/{專案}/{日期}/ | 輸出音檔及相應台詞、引擎版本、生成設定，方便重做。 |
| config/、cache/、logs/、licenses/ | 使用者設定、可清理暫存、故障紀錄和每項軟件／模型的授權文件。 |

啟動器只在本機提供介面，模型載入和語音合成都在電腦內完成。預設一次載入一個引擎。生成期間要求切換，先完成當前工作或讓使用者取消，再釋放原本引擎的顯示記憶體。下載可續傳，完整性驗證通過才標為「可使用」。把所有必要模型（包括會在首次生成時自動下載的小模型）和快取指向程式資料夾，以便整個資料夾搬走。模型管理頁分開顯示「未下載」「可離線使用」「現有硬件待驗證」。

## 5　容量及發佈注意事項

| 上游提供的下載項目 | 頁面列出的大小 | 解讀 |
|---|---:|---|
| GPT-SoVITS v2Pro 一般 NVIDIA／RTX 50 系列整合包 | 8.19／8.84 GB | 兩個是可選壓縮包，選與顯示卡相符的一個。[來源](https://huggingface.co/lj1995/GPT-SoVITS-windows-package/tree/main) |
| CosyVoice 3 官方模型倉庫 | 9.75 GB | 整個倉庫大小。應按所需推理檔案核算實際下載量。[來源](https://huggingface.co/FunAudioLLM/Fun-CosyVoice3-0.5B-2512/tree/main) |
| VoxCPM2 官方模型倉庫 | 4.96 GB | 官方標準推理配置另列約 8 GB 顯示記憶體。磁碟容量和顯示記憶體是兩個數字。[檔案](https://huggingface.co/openbmb/VoxCPM2/tree/main) · [模型卡](https://huggingface.co/openbmb/VoxCPM2) |
| IndexTTS-2 官方模型倉庫 | 5.9 GB | 上游說明另有初次執行下載的輔助模型。[檔案](https://huggingface.co/IndexTeam/IndexTTS-2/tree/main) · [說明](https://github.com/index-tts/index-tts) |
| Qwen3-TTS Base 0.6B／1.7B | 2.52／4.54 GB | 各自一個子模型的倉庫大小。預設聲線和聲線設計需另計。[0.6B](https://huggingface.co/Qwen/Qwen3-TTS-12Hz-0.6B-Base/tree/main) · [1.7B](https://huggingface.co/Qwen/Qwen3-TTS-12Hz-1.7B-Base/tree/main) |

### 整體容量預算

採十進位 GB。以一般 NVIDIA 版、每款各裝一份、Qwen 採 1.7B 計算。表中 GPT 是已壓縮的完整包，其餘是上游模型倉庫檔案總量；合計用於安排磁碟空間，打包時按實際需要選檔案及扣除重複項目。

- 五個引擎、Qwen 只下載 Base：8.19 + 9.75 + 4.96 + 5.90 + 4.54 = **33.34 GB**。
- 加齊 Qwen 的 CustomVoice、VoiceDesign：33.34 + 4.52 + 4.52 = **42.38 GB**。改 RTX 50 系列 GPT 包再加 0.65 GB。[CustomVoice](https://huggingface.co/Qwen/Qwen3-TTS-12Hz-1.7B-CustomVoice/tree/main) · [VoiceDesign](https://huggingface.co/Qwen/Qwen3-TTS-12Hz-1.7B-VoiceDesign/tree/main)
- 全功能版下載暫按 **50–65 GB** 預算，假設上述基準外另需約 8–22 GB 壓縮執行環境及輔助資源。主程式獨立下載的設計目標為 **0.1 GB 以內**，沿用 Windows 上已可用的瀏覽器。
- 安裝後估算 **65–95 GB**：42.38 GB 基準，加 GPT 解壓新增預算 8–14 GB、其他四款各 3–7 GB 執行環境（共 12–28 GB）、其餘輔助檔及快取 2–8 GB，取整留餘。逐一更新時再預留 **15–25 GB** 暫存，故全裝建議開始時有 **100–120 GB 可用**。錄音、訓練檢查點及作品另計。全部數字屬規劃預算，將以 Windows 乾淨安裝測量取代。
- 每個引擎包使用固定版本和本倉庫的附件清單，記錄分卷順序、每卷與整包 SHA-256 校驗碼、下載量、展開大小、峰值空間、授權與來源提交。每個附件規劃最多 1.8 GB，低於 GitHub 單一附件 2 GiB 上限。按引擎逐一續傳、驗證、展開至暫存目錄，成功後切換；保留可用的舊版直至驗證完成。發佈前在全新 Windows 環境測試封鎖外部模型站後仍能下載、生成及重開。[GitHub Releases 規格](https://docs.github.com/en/repositories/releasing-projects-on-github/about-releases)

官方有列 Windows 整合包的 GPT-SoVITS 適合先驗證啟動體驗。其餘逐款建立可攜執行環境，尤其要測 IndexTTS-2 在 Windows 略去選配 DeepSpeed 加速時的基準路徑。第一輪先測 NVIDIA 顯示卡，CPU、AMD 和 Intel 支援按引擎逐項標示。授權也按元件存檔。GPT-SoVITS 主倉庫標示 MIT，CosyVoice 3、VoxCPM2、Qwen3-TTS 相關官方模型採 Apache-2.0，IndexTTS-2 權重另有 bilibili 模型使用許可協議，包含保留聲明及下游使用條件。[GPT-SoVITS](https://github.com/RVC-Boss/GPT-SoVITS) · [CosyVoice 3](https://huggingface.co/FunAudioLLM/Fun-CosyVoice3-0.5B-2512) · [VoxCPM2](https://huggingface.co/openbmb/VoxCPM2) · [Qwen3-TTS](https://huggingface.co/Qwen/Qwen3-TTS-12Hz-1.7B-Base) · [IndexTTS-2 權重隨附的中文許可協議](https://huggingface.co/IndexTeam/IndexTTS-2/blob/main/LICENSE_ZH.txt)

## 6　建議開發順序及驗收

| 階段 | 內容 | 完成標準 |
|---|---|---|
| 一　建立首條可用流程 | GPT-SoVITS ＋ CosyVoice 3，從本機啟動到香港粵語旁白輸出。 | 在乾淨的 Windows 使用者環境，以一般權限啟動。輸入同一句廣東話，用同一段參考聲音分別生成、試聽和匯出。 |
| 二　加入另外三款引擎 | VoxCPM2 的錄音配語氣指示、IndexTTS-2 的雙錄音、Qwen 的三個子模式。 | 切換五個引擎仍保留台詞。每種可見模式都能產生自己的音檔，切換或取消後釋放原有資源。 |
| 三　完善角色訓練 | GPT-SoVITS 引導式切片、轉錄、校對、微調、試聽和儲存。 | 使用者可由一批錄音製作角色，再用未參與訓練的台詞比較成效。 |
| 四　免安裝封裝驗收 | 斷網、搬移資料夾、中文及帶空格路徑、下載中斷與容量不足測試。 | 已備妥所需模型時可離線重開及生成，移到另一個磁碟後仍可讀取角色和歷史。只修復缺少的引擎，不影響已有專案。 |

試聽台詞可用「今晚八點半喺旺角站等，記得帶埋 USB 手指呀。」另以相同文稿生成平靜與焦急兩個版本。記錄香港地名、語尾助詞、英文縮寫、音色相似度和語氣差異，另記錄首段等候時間、生成速度及顯示記憶體峰值，以供發佈前列出各引擎的實測要求。
