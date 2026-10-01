<img src="docs/icon.png" width="112" align="right" alt="">

# AgentHub

[English](README.md) | 繁體中文

**一個視窗，看遍你每台電腦上所有的 Claude Code 與 Codex session。**

AgentHub 會列出你各台電腦上正在跑的 coding agent session，告訴你哪些在等你回應，
還能讓一台電腦上的 agent 寄訊息給另一台電腦上的 agent。一切都留在你自己的網路裡：
不用帳號、沒有雲端伺服器、不回傳使用資料。免費開源，採用 MIT 授權。

![本機 session 表格：九個 Claude Code 與 Codex session，各自的狀態、誰看得到、收件匣數量（截圖為英文介面）](docs/screenshots/local-sessions.png)
*這台的每個 session：狀態、誰看得到、有幾則訊息在等。*

![首次設定第 1 步：三項檢查、一格說明開放區網與在區網上搜尋會讓別人看到什麼，以及「準備好這台電腦」與「只在這台用，不開放區網」兩顆按鈕（截圖為英文介面）](docs/screenshots/first-run.png)
*全新安裝會先打開三步設定。你沒選之前，什麼都不會分享出去。*

![配對：兩組指紋要和另一台螢幕比對，下方是「一樣，核准」與「不一樣，拒絕」（截圖為英文介面）](docs/screenshots/network-pairing.png)
*兩邊的人在兩台螢幕上比對同一組短碼之後，兩台電腦才會配對。*

視窗有英文與繁體中文兩種介面。截圖裡的資料都是虛構的。

## 開始使用

一台就能先試。要連兩台的話，兩台都先裝好：設定的第 2 步需要兩台同時開在那一步。

### 1. 安裝

**macOS 與 Linux**：在終端機貼上這一行。

```sh
curl -fsSL https://raw.githubusercontent.com/SheldonChangL/agenthub/main/install.sh | sh
```

它會先用該版本的檢查碼核對下載的檔案，再裝好 app 和 `ah` 指令，並讓 AgentHub 在背景執行。
它也會在 `~/.claude/skills/agenthub-watch` 放一個 Claude Code skill，讓這台的 agent 知道怎麼用 `ah`；
不要的話，把最後的 `sh` 換成 `sh -s -- --no-skill`。過程中不會要你輸入密碼。

**Windows**：到 [Releases 頁面](https://github.com/SheldonChangL/agenthub/releases)
下載 `agenthub-desktop_<版本>_windows_amd64-installer.exe` 並執行。
AgentHub 從未在真的 Windows 電腦上跑過，Windows 版只在 CI 建置與測試
（[#21](https://github.com/SheldonChangL/agenthub/issues/21)）。

這些檔案沒有簽章，所以手動下載的話，macOS 和 Windows 第一次開啟前會跳警告。
手冊裡寫了[怎麼放行、怎麼核對檔案](docs/guide.zh-Hant.md#電腦跳出下載警告時)。

### 2. 打開 app

- **macOS**：安裝完會自動打開。之後從「應用程式」打開 agenthub-desktop。
- **Linux**：應用程式選單裡的 AgentHub，或在新開的終端機執行 `agenthub-desktop`。
- **Windows**：開始功能表裡的 agenthub-desktop。

### 3. 跟著設定走，兩台都要做

1. **準備這台電腦**：按一顆按鈕，AgentHub 就會在背景執行、登入時自動啟動、讓區網裡的其他電腦連得到它，
   並開始在區網上搜尋。按鈕上方的說明寫著這會讓人看到什麼：配對期間，同一個網路上的其他電腦看得到這台的名稱與位址
   （以及平台、指紋與節點 ID）。只想在這台用，就按**只在這台用，不開放區網**。
2. **連到另一台**：兩台都完成第 1 步之後，對方才會出現在清單裡。清單說這台沒在搜尋時，按**開始在區網上搜尋**。
   還是找不到，就打開**找不到另一台？**，輸入另一台螢幕上顯示的位址。從其中一台送出請求；
   被請求的那台和另一台螢幕比對兩組短碼後按**一樣，核准**，送出請求的那台再比對一次，按**一樣，完成配對**。
3. **分享 session**：勾選要讓另一台看到的 session，再選對方只能留訊息，還是也能叫醒 agent。

你核准了、對方卻一直沒完成的話，這台仍然信任對方，而且不會過期。到「區網」分頁按**撤銷信任**移除它。
[使用手冊](docs/guide.zh-Hant.md)把每個畫面都寫清楚了。

## 可以做什麼

- **看每個 session 和它的狀態。** Claude Code 和 Codex，不管在 Mac、Linux 還是 Windows 上，
  都在同一張清單裡。每個 session 會標示 `active`、`idle`、`inactive` 或 `unknown`，
  從 agent 自己留在磁碟上的檔案讀出來，完全不碰 agent 本身。
- **分享 session，一列一列設或一次設很多個。** 每個 session 的公開對象按鈕有三個選項：
  **不公開**、**能留訊息**、**留訊息並喚醒**。指定機器等其他設定在完整對話框裡。
  見[分享 session](docs/guide.zh-Hant.md#分享-session)。
- **讀收件匣。** 其他電腦寄來的訊息，會放在 AgentHub 替每個 session 準備的收件匣裡等著。
  見[收件匣與訊息](docs/guide.zh-Hant.md#收件匣與訊息)。
- **讓 agent 彼此溝通。** 四個 MCP 工具（`agent_list`、`agent_status`、`agent_inbox`、`agent_send`）
  讓 agent 能問另一台電腦上的 agent 在做什麼，也能寫訊息給它。
  [設定方式](docs/guide.zh-Hant.md#讓-agent-使用四個工具)。
- **用訊息叫醒 agent。** 兩個開關都打開時，訊息可以在沒人坐在電腦前時，讓 Codex session 開始一輪工作。
  這只在一台電腦上實際看過成功，Claude Code 目前還叫不醒。
  見[喚醒 agent](docs/guide.zh-Hant.md#喚醒-agent)。

![某一列的公開對象選單：不公開、能留訊息、留訊息並喚醒（Claude Code session 不能選）（截圖為英文介面）](docs/screenshots/inline-publish.png)
*分享就是每一列上的一個選單，選了立即生效，也能按「復原」。*

## 白話講隱私

- **不上雲端。** 你的電腦之間直接透過你自己的網路溝通。沒有 AgentHub 伺服器、不用註冊帳號，也不回傳使用資料。
- **你沒分享，就什麼都不會出去。** 每個 session 一開始都是**不公開**。兩台電腦配對本身不會分享任何東西。
- **配對要兩個人都同意。** 兩台螢幕會顯示同樣的兩組指紋，也就是從各台金鑰算出來的短碼。
  你們比對之後，各自在自己的螢幕上核准。只要有一組不一樣，就拒絕。
- **配對的電腦只看得到一段簡短描述。** 它拿到的是每個分享 session 的 ID、是 Claude Code 還是 Codex、
  狀態與狀態從哪裡判斷來的、是不是由 AgentHub 管理、最後活動時間；工作目錄要你允許才會給。你的提示詞和對話紀錄一律不會送出去。
- **區網搜尋大多時候只是在聽。** 搜尋開著時，這台會一直收聽區網上其他電腦的通告（只收不送）；
  只有配對視窗開著時才會廣播自己。停在設定第 2 步時，視窗到期會自動重開。
- **訊息只進 AgentHub 自己的收件匣。** 不會寫進 Claude Code 或 Codex 的檔案。
  只有 agent 自己去讀，或你打開了喚醒，訊息才會到 agent 手上。

細節和設計紀錄在[開發者文件](docs/developer.md#how-it-stays-private)（英文）。

## 目前狀態

這是一個人做的專案，每天在一台 Mac 和一台 Ubuntu 上使用，到現在只有作者本人用過。
在兩台實機上測過的項目記錄在 [docs/verification.md](docs/verification.md)（英文）。
下面列出的缺口都是真的，其中一項是功能回報成功、實際上可能什麼都沒做。

| 項目 | 狀態 |
|---|---|
| Session 清單、狀態、分享 | 已在 macOS 與 Linux 驗證 |
| 配對與指紋比對 | 已在兩台實機之間用命令列驗證 |
| 兩台之間的心跳（每台每 15 秒送給已配對電腦的簽章更新）、訊息、收件匣 | 已在兩台實機之間驗證 |
| 首次設定（v0.1.9 新增） | 在 dev mock 與測試裡驗過。區網搜尋在兩台實機的測試節點上驗過（2026-10-01）。完整的首次設定流程還沒有在兩台實機上從頭跑過一次。 |
| MCP 工具、跨機器的 agent 對 agent | 已在兩台實機之間驗證 |
| 喚醒 **Codex** session | 在一台電腦上驗證：實際觀察到一輪工作 |
| 喚醒 **Claude Code** session | **未驗證。** 節點回報訊息已喚醒，但從沒觀察到那一輪真的開始。[細節](docs/channel-push-not-observed.md)（英文） |
| Windows | **從未在真的 Windows 電腦上跑過**，只在 CI 建置與測試。背景節點只在登入時啟動，結束後不會自動重啟 |
| 在 Windows、macOS、Ubuntu 實機上的驗收 | 尚未完成：[#21](https://github.com/SheldonChangL/agenthub/issues/21) |
| 簽章的下載檔 | 沒有做，在值得買憑證之前也不打算做 |

## 常見問題

**會不會把資料傳到網路上？** 不會。AgentHub 只和你配對過的電腦直接透過區網溝通。
只有你執行安裝腳本時，它才會連 GitHub 下載版本檔案。

**為什麼 macOS 或 Windows 會跳警告？** 下載檔沒有簽章，因為簽章憑證每年都要花錢。
每個版本都附了檢查碼可以核對。macOS 用一行指令安裝時會替你核對，所以不會跳警告。
[怎麼放行](docs/guide.zh-Hant.md#電腦跳出下載警告時)。

**另一台找不到怎麼辦？** 兩台都要完成第 1 步（它會打開區網搜尋），而且在同一個網路上：
只有一台在搜尋時，兩台都看不到對方。第 2 步說這台沒在搜尋時，按**開始在區網上搜尋**。
防火牆或訪客 Wi-Fi 還是可能擋住，這時打開**找不到另一台？**，輸入另一台螢幕上顯示的位址。
見[疑難排解](docs/guide.zh-Hant.md#疑難排解)。

**配對後對方看得到什麼？** 你分享 session 之前，什麼都看不到。分享之後，對方看到的是上面說的那段簡短描述，看不到對話內容。

**怎麼移除？** 把安裝指令最後的 `sh` 換成 `sh -s -- --uninstall` 再執行一次。
除非加上 `--purge`，否則重新安裝後配對都還在。Windows 則從「設定 → 應用程式」解除安裝。
見[升級與移除](docs/guide.zh-Hant.md#升級與移除)。

**訊息叫得醒 Claude Code 嗎？** 目前還不行。訊息會進收件匣，agent 可以用 `agent_inbox` 讀，
但從來沒看過 Claude Code session 被成功叫醒。Codex 的喚醒則在一台電腦上看過成功。

## 更多資料

- 使用手冊：[繁體中文](docs/guide.zh-Hant.md) · [English](docs/guide.md)
- [開發者文件](docs/developer.md)（英文）：建置、命令列、本機 API、隱私模型細節
- [SECURITY.md](SECURITY.md)（英文）：怎麼回報安全漏洞
- [CONTRIBUTING.md](CONTRIBUTING.md)（英文）：怎麼參與，issue 和 PR 用中文寫也可以

## 支持這個專案

AgentHub 免費，以後也一直免費。如果它幫你省了時間，你想表達一下，
可以到 [Ko-fi 頁面](https://ko-fi.com/sheldonchang)。不管有沒有贊助，軟體都不會有任何差別：
沒有付費版，也沒有會注意到這件事的使用資料回傳。

回報 bug，順便說說你當時想做什麼，比錢更有價值；粗糙的地方只能靠這樣被找出來。

授權：MIT，見 [LICENSE](LICENSE)。
