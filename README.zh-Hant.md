<img src="docs/icon.png" width="112" align="right" alt="">

# AgentHub

[English](README.md) | 繁體中文

**查看不同電腦上的 Claude Code 與 Codex session，也能讓它們互相留訊息。**

配對過的電腦之間直接點對點連線，只走你自己的網路：沒有中央的 AgentHub 伺服器，
不用帳號，也不回傳使用資料。別台電腦的 session，要對方分享給你才看得到；
訊息會留在收件匣，等 agent 自己來讀。免費開源（MIT）。目前還是早期版本，
請先看[目前狀態](#目前狀態)。

舉個例子：Linux 主機上的 Codex 跑完 migration，留了一則訊息給 Mac 上的 Claude Code：
「schema 改了，API 測試重跑一下」。Claude Code 下次檢查收件匣時就會看到。
只要兩台都把自己的 session 分享出去，從任何一台都看得到兩邊的 session，以及它們是 active 還是 idle。

![本機 session 表格：九個 Claude Code 與 Codex session，各自的狀態、誰看得到、收件匣數量（截圖為英文介面）](docs/screenshots/local-sessions.png)
*「本機 session」分頁。截圖裡的資料都是虛構的；介面有英文與繁體中文。*

## AgentHub 做什麼

- **找出你本來就在跑的 session。** Claude Code 和 Codex 照平常的方式開就好。
  AgentHub 從 agent 自己留在本機的檔案讀出既有的 session，不用透過 AgentHub 啟動，
  也不會寫進那些檔案。
- **顯示這台的 session，以及配對的電腦分享給你的 session。** 「本機 session」分頁是這台電腦的；
  到「區網」分頁選一台配對過的電腦，看得到它分享給你的 session。
- **顯示推測出來的狀態**：`active`、`idle`、`inactive` 或 `unknown`。
  判斷依據是 agent 的程式還在不在跑、檔案多久前更新過，所以分不出某個 session 是不是正在等你回覆。
- **讓 agent 跨電腦互相留訊息。** 透過四個 MCP 工具或 `ah` 指令，每個 session 要先設定好才能用。
  見[agent 之間互相留訊息](#agent-之間互相留訊息)。
- **可以用訊息喚醒 Codex session。** 要你自己打開，目前只在一台電腦上驗證過。

## AgentHub 不做什麼

- **不管理 Claude Code 或 Codex session 的生命週期。** 它不能啟動、停止、resume 或刪除 session，
  也不能派工作給它。「複製 ID」會給你 session ID，讓你自己去 resume。
- **不是即時的 agent 聊天室，也不是任務編排系統。** 訊息就是留在收件匣裡的一張紙條，
  沒有任務指派、追蹤，也不會等對方回覆。
- **不會自動把所有 session 集中起來。** 沒配對、對方沒分享，你就看不到那台的 session。
- **不會把你的對話送到任何地方。** 為了列出 session，它會從 agent 的檔案讀幾個欄位，例如 ID 和工作目錄；
  提示詞和對話紀錄一律不會離開這台電腦。

## 隱私與信任

- **點對點。** 配對過的電腦彼此直接連線。每台電腦跑一個小小的背景程式（節點）；
  中間沒有中央的 AgentHub 伺服器、沒有 AgentHub 雲端服務，也沒有中繼。
- **不用帳號，不回傳使用資料。**
- **配對由人來確認。** 兩台螢幕會顯示同樣的兩組指紋，也就是從各台金鑰算出來的短碼。
  兩邊各自和對方螢幕比對，在自己的電腦上核准。之後的連線就綁定在你們比對過的金鑰上。
- **分享要你主動選，以 session 為單位。** 每個 session 一開始都是「不公開」，配對本身不會分享任何東西。
  每個 session 給誰看由你決定：所有配對過的電腦，或只給你勾選的那幾台。
- **配對的電腦只拿到一段簡短描述**：你分享的每個 session 的 ID、是 Claude Code 還是 Codex、
  狀態、最後活動時間；工作目錄要你允許才會給。
- **配對視窗開著的時候**，同一個網路上的其他電腦看得到這台的名稱、位址、平台、指紋和節點 ID。
  其他時間它不會廣播自己。

每一點是怎麼做到的：[開發者文件](docs/developer.md#how-it-stays-private)（英文）。

![配對：兩組指紋要和另一台螢幕比對，下方是「一樣，核准」與「不一樣，拒絕」（截圖為英文介面）](docs/screenshots/network-pairing.png)
*兩邊的人在兩台螢幕上比對同一組短碼之後，兩台電腦才會配對。*

## 快速開始

一台就能先試。要連兩台的話，兩台都先裝好，因為配對時兩台要同時開著。

### 1. 安裝

**macOS 與 Linux：**

```sh
curl -fsSL https://raw.githubusercontent.com/SheldonChangL/agenthub/main/install.sh | sh
```

腳本會先用該版本的檢查碼核對下載的檔案，再裝好 app 和 `ah` 指令，並在背景啟動節點，
過程中不會要你輸入密碼。它也會在 `~/.claude/skills/agenthub-watch` 放一個 Claude Code skill，
教 agent 怎麼用 `ah`；不要的話，把最後的 `sh` 換成 `sh -s -- --no-skill`。

**Windows：**到 [Releases 頁面](https://github.com/SheldonChangL/agenthub/releases)
下載 `agenthub-desktop_<版本>_windows_amd64-installer.exe`。
Windows 版從沒在真的 Windows 電腦上跑過，只在 CI 建置和測試
（[#21](https://github.com/SheldonChangL/agenthub/issues/21)）。

下載檔沒有簽章。手動下載的檔案第一次開啟前，macOS 或 Windows 會跳警告；
[怎麼核對檔案、怎麼放行](docs/guide.zh-Hant.md#電腦跳出下載警告時)。

### 2. 打開 app，跟著設定走

打開 **agenthub-desktop**（macOS 裝完會自動打開）。全新安裝會先進入三步設定，兩台都要做。

![首次設定第 1 步：三項檢查、一格說明開放區網與在區網上搜尋會讓別人看到什麼，以及「準備好這台電腦」與「只在這台用，不開放區網」兩顆按鈕（截圖為英文介面）](docs/screenshots/first-run.png)

1. **準備好這台電腦。** 按一顆按鈕，就會啟動節點並設成登入時自動啟動、讓區網裡的其他電腦連得到它，
   並開始在區網上搜尋。按鈕上方有一格寫著這會讓別人看到什麼。只想在這台用，就按「只在這台用，不開放區網」。
2. **連到另一台。** 兩台都完成第 1 步，對方才會出現在清單裡。從其中一台送出請求，
   兩邊各自和對方螢幕比對兩組指紋後確認。一直找不到對方的話，改成輸入位址
   （見[疑難排解](docs/guide.zh-Hant.md#疑難排解)）。
   核准過的信任不會過期：你核准了、對方卻沒完成，就到「區網」分頁按「撤銷信任」移除。
3. **分享 session。** 勾選要讓另一台看到的 session，再選對方只能留訊息，還是也能喚醒 agent。

每個畫面的說明都在[使用手冊](docs/guide.zh-Hant.md)。

## agent 之間互相留訊息

留訊息是非同步的。訊息會放進 AgentHub 替收件的 session 準備的收件匣。讀過不會消失，
要有人刪掉才會移除：agent 自己刪（`ah inbox delete`，`agenthub-watch` skill 處理完會自動刪），
或你在視窗裡按「清空收件匣…」。AgentHub 不會把訊息寫進 Claude Code 或 Codex 的檔案；
被喚醒的 Codex 會像收到提示詞一樣收到它，那一輪會留在 Codex 自己的紀錄裡。

- **收發的是 agent，不是視窗。** 桌面視窗沒有傳訊息的按鈕。agent 用四個 MCP 工具
  （`agent_list`、`agent_status`、`agent_inbox`、`agent_send`）或 `ah` 指令收發；
  你也可以在終端機以自己某個 session 的身分送：`ah send --from <你的-session-id> <對方-session-id> -- "訊息"`。視窗裡看得到每個收件匣、送出了什麼，以及送到了還是被拒絕。
- **agent 要有管道。** MCP 工具每個 session 要一份小設定檔，寫明它代表哪個 session。
  `ah` 指令不用設定；在 macOS 與 Linux 上，安裝之後才開的 Claude Code session 會從 `agenthub-watch` skill 學會怎麼用
  （Windows 要在安裝程式裡勾選這個 skill）。
  [設定方式](docs/guide.zh-Hant.md#讓-agent-使用四個工具)。
- **每個 session 的公開對象要允許。** 只裝好 AgentHub，任何 agent 都還不能收發：每個 session 一開始都是關著的。
  收件的 session 要能收訊息（「能留訊息」）。要送出或回覆的 session 需要「允許這個 session 主動送出訊息」：
  Codex 選「留訊息並喚醒」就包含了；Claude Code 要到「指定機器、進階旗標…」裡勾，
  因為選「能留訊息」會把送出關掉。
- **Claude Code 不會被訊息喚醒。** 它要自己檢查收件匣：呼叫 `agent_inbox`，
  或在 session 裡啟動 `agenthub-watch` skill，讓它定時檢查。

![某一列的公開對象選單：不公開、能留訊息、留訊息並喚醒（Claude Code session 不能選）（截圖為英文介面）](docs/screenshots/inline-publish.png)
*每個 session 的公開對象選單。Claude Code session 沒有「留訊息並喚醒」可選。*

### 喚醒 Codex session

這台電腦和那個 session 的兩個開關都打開後，訊息可以在沒人坐在電腦前時，讓 Codex session 開始一輪工作。
目前只在一台電腦上實際看過成功。被喚醒的那一輪什麼都不會核准：它問的每個權限問題，答案都是否。
頻率限制，加上連續自動喚醒 4 次就停，可以避免兩台電腦無止盡地互相回覆。
打開喚醒，等於讓那台配對的電腦能在限制內，在你的電腦上啟動工作。見[喚醒 agent](docs/guide.zh-Hant.md#喚醒-agent)。

## 目前狀態

早期版本，一個人開發，每天在一台 Mac 和一台 Ubuntu 上使用。測試紀錄在
[docs/verification.md](docs/verification.md)（英文）。

| 項目 | 狀態 |
|---|---|
| Session 清單、狀態、分享 | 已在 macOS 與 Linux 驗證 |
| 配對與指紋比對 | 已在兩台實機之間用命令列驗證 |
| 跨電腦的訊息與收件匣 | 已在兩台實機之間驗證 |
| MCP 工具、跨電腦的 agent 對 agent | 已在兩台實機之間驗證 |
| 首次設定 | 有測試，區網搜尋也在兩台實機的測試節點上驗過；完整流程還沒在兩台實機的 app 上從頭跑過一次 |
| 喚醒 **Codex** session | 在一台電腦上驗證：實際觀察到一輪工作 |
| 喚醒 **Claude Code** session | **沒看過成功。** 節點會記錄訊息「已喚醒」，但從沒觀察到那一輪真的開始，所以視窗不提供這個選項。[細節](docs/channel-push-not-observed.md)（英文） |
| Windows | **從沒在真的 Windows 電腦上跑過**，只在 CI 建置和測試。節點只在登入時啟動，結束後不會自動重啟 |
| 在 Windows、macOS、Ubuntu 實機上的驗收 | 尚未完成：[#21](https://github.com/SheldonChangL/agenthub/issues/21) |

## 常見問題

**會不會把資料傳到網路上？** 不會。它只和你網路上的電腦溝通，平常就是你配對過的那幾台。
只有執行安裝腳本時，它才會連 GitHub。

**為什麼 macOS 或 Windows 會跳警告？** 下載檔沒有簽章，因為簽章憑證每年都要錢。
每個版本都附了 `SHA256SUMS` 可以核對。macOS 用一行指令安裝時會替你核對，所以不會跳警告。
[怎麼放行](docs/guide.zh-Hant.md#電腦跳出下載警告時)。

**另一台找不到怎麼辦？** 兩台都要完成第 1 步，而且在同一個網路上：只有一台在搜尋的話，兩台都看不到對方。
防火牆或訪客 Wi-Fi 也可能擋住，這時就把其中一台顯示的位址輸入到另一台。
見[疑難排解](docs/guide.zh-Hant.md#疑難排解)。

**配對後對方看得到什麼？** 看得到這台在線上，以及它的名稱、平台、指紋和位址。
你分享之前看不到任何 session；分享之後，對方看到的是上面那段簡短描述，看不到對話內容。

**怎麼移除？** 把安裝指令最後的 `sh` 換成 `sh -s -- --uninstall` 再執行一次。
除非加上 `--purge`，重新安裝後配對都還在。Windows 從「設定 → 應用程式」解除安裝。
見[升級與移除](docs/guide.zh-Hant.md#升級與移除)。

## 文件、參與與支持

- 使用手冊：[繁體中文](docs/guide.zh-Hant.md) · [English](docs/guide.md)
- [開發者文件](docs/developer.md)（英文）：建置、命令列、本機 API、隱私模型細節
- [SECURITY.md](SECURITY.md)（英文）：怎麼私下回報安全漏洞
- [CONTRIBUTING.md](CONTRIBUTING.md)（英文）：怎麼參與，issue 和 PR 用中文寫也可以

AgentHub 免費，以後也會一直免費。如果它幫你省了時間，可以到 [Ko-fi 頁面](https://ko-fi.com/sheldonchang)
支持一下；有沒有贊助，軟體都一樣。回報 bug 時順便說說你當時想做什麼，比錢更有幫助。

授權：MIT，見 [LICENSE](LICENSE)。
