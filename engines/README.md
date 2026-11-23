# Engine sources · 引擎原始碼

每個 source.zip 是對應提交的原始碼封存，保留上游授權、設定與文字資源。省略文件插圖、影音示例與 PDF。Git LFS 指標保留原樣，執行環境及大型模型權重會獨立封裝。CosyVoice 內含上游鎖定的 Matcha-TTS 子模組。

Each archive preserves the upstream code, configuration and license files at the listed revision. Media examples, illustrations and PDFs are omitted. Large File Storage pointers remain pointers. The future release packs supply runtime dependencies and model weights.

| Archive | Source commit | SHA-256 |
|---|---|---|
| [gpt-sovits](gpt-sovits/source.zip) | [d7c2210da8](https://github.com/RVC-Boss/GPT-SoVITS/tree/d7c2210da8c013e81a94bfc7b811a477c99fd506) | `5d3f1eb6537abef0f922a8aeb029f3c258fdf4c0cea4eed041118226835624c7` |
| [cosyvoice3](cosyvoice3/source.zip) | [074ca6dc9e](https://github.com/QwenAudio/CosyVoice/tree/074ca6dc9e80a2f424f1f74b48bdd7d3fea531cc) | `fde16d36eb7f6336015ff56496711c38b2e36e13de6f891a4542fc7a58eff3c7` |
| [voxcpm2](voxcpm2/source.zip) | [300e4260b5](https://github.com/OpenBMB/VoxCPM/tree/300e4260b509066151d623dc9c0aa922cf82232b) | `98e262075f48f72d82cd22379597b093028234a7179775890971cbf68fa0170c` |
| [indextts2](indextts2/) | [830f6f8f94](https://github.com/index-tts/index-tts/tree/830f6f8f94a51fea23ab1d639027a86200075a4e) | `3a93a3aa1121a8d65f23fce13d7cb1d0adc5113a81a7988744adb5b561aaa037` |
| [qwen3-tts](qwen3-tts/source.zip) | [022e286b98](https://github.com/QwenLM/Qwen3-TTS/tree/022e286b98fbec7e1e916cb940cdf532cd9f488e) | `e8aa4e168d4f163a77f05d5faeab2759f192a85d5a3740a59129e856e3b5e346` |

Matcha-TTS commit: [dd9105b34b](https://github.com/shivammehta25/Matcha-TTS/tree/dd9105b34bf2be2230f4aa1e4769fb586a3c824e).

IndexTTS-2 is provided under its original bilibili Model Use License Agreement. Recipients using or distributing IndexTTS-2 must comply with the terms included in its source archive. Any modifications made to the original model in this Derivative Work are not endorsed, warranted, or guaranteed by the original right-holder of the original model, and the original right-holder disclaims all liability related to this Derivative Work.

IndexTTS-2 的原始 ZIP 在此以四個分卷保存。請在該目錄執行 Restore-Source.ps1 合併並核對 SHA-256，再解壓。For IndexTTS-2, run Restore-Source.ps1 in the engine directory to reconstruct and verify the original archive.
