# Desktop UI 功能契約

這份文件是 GUI 重新設計的驗收基準。它從三個來源抽出來：
`frontend/index.html` 的每個互動元素、`app.go` / `service.go` 對前端暴露的
每個綁定方法、`frontend/test/*.mjs` 斷言的每個行為與文案。重新設計後的
實作，每一條都要能對上一個 UI 入口或一個可觀察的行為；對不上就是缺功能。

文件內「必須」是契約，「建議」是這次重新設計要修的 UX 問題，兩者分開列。

## 1. 資料模型（節點回什麼，UI 就只能顯示什麼）

### Session（`client.go`）

| 欄位 | 型別 | 目前 UI 用法 |
|---|---|---|
| `id` | `provider:rest` | 表格第一欄只顯示 `rest`；搜尋比對整個 id |
| `provider` | `claude` / `codex` | 欄位 + 篩選 |
| `status` | `active` / `idle` / `inactive` / 其他 | pill；未知值 class 只能是 `pill` |
| `management` | string | 「管理」欄，muted |
| `audience.mode` | `none` / `all_paired` / `selected` | 「公開對象」欄 + 篩選 |
| `audience.nodes` | string[] | mode=selected 時顯示「N 個節點」，0 個顯示「指定節點（無）」且不算已公開 |
| `audience.exportCwd / acceptMessages / allowOutbound / autoWake` | bool | 目前**沒有顯示**，只在對話框設定 |
| `cwd` | string, 可空 | 「工作目錄」欄，空顯示 `—`，title 帶完整路徑 |
| `lastSeenAt` | ISO | 相對時間（秒/分/小時/天前） |
| `providerSessionId`, `visibility`, `statusSource`, `source`, `updatedAt` | | 目前未使用 |

`Overview.counts` 是全域計數：`total`、`claude`、`codex`、`active`、`idle`、
`inactive`、`all_paired`、`selected`、`none`。**它不隨篩選變動**（見 §5 問題 2）。

### TrustedNode、Peer、Candidate、PairingState、InboxView、ServiceStatus

欄位見 `client.go`、`app.go`、`service.go`。UI 上有意義的區分：

- Peer 有四種狀態，且**必須四種都長得不一樣**（測試 `render-hostile-peer.mjs`）：
  `presenceError`（本機讀不到）、從未收到心跳（無 `receivedAt`）、離線（有
  `receivedAt`、`online=false`，且**不得**顯示舊 session 清單）、線上。
- Candidate 每一個欄位都是對方自稱的。`contested` 與 `duplicate` 兩個旗標要在
  列上顯示，而且**跟著進配對對話框**（兩個都有時兩個都要說）。
- PairingState.availability 四種：`off`（沒開 `-discover`，視窗也沒開）、
  `openNotAnnouncing`（同一種節點但視窗開著：沒人找得到它，送到它位址的請求仍然會到）、
  `on`、其他（讀不到）。四種文案不能合併（測試 `render-hostile-candidate.mjs`、
  `pairing-exchange.mjs`）。把 `openNotAnnouncing` 畫成 `off` 等於在視窗開著並收著請求時
  說它是關的，會叫使用者去重開已經開著的東西。
- InboxView 有 loading、error、cleared、full、more 五個獨立狀態，加上空清單。
  loading 和空清單**不能長一樣**。

## 2. 後端綁定 → UI 入口（27 個，每個都要有）

| 綁定 | 現在的入口 | 觸發後必須發生的事 |
|---|---|---|
| `Overview()` | 啟動、`btn-reload`、每次寫入後 | 更新 session/nodes/peers/counts、連線指示、footer；清掉已不存在的選取 |
| `Discover()` | `btn-discover` | 掃描後 reload；banner 報 claude/codex/total/skipped 數 |
| `Heartbeat()` | `btn-heartbeat` | 對話框顯示已簽章 envelope 純文字 |
| `SetAudience(ids, audience)` | 公開對象對話框「套用」、`btn-unpublish` | 成功：清空選取、關對話框、banner；部分失敗：留著選取、banner 第一個錯誤 |
| `SetVisibility(ids, visibility)` | 目前**沒有** UI 入口 | 保留為未接綁定；不算缺功能 |
| `TrustNode(id, name, platform, key, fingerprint)` | 配對對話框「指紋一致，信任此節點」 | 關對話框、選中新節點、reload、banner |
| `RevokeNode(id)` | 節點詳情「撤銷信任」 | confirm 後執行；banner 說明同時移除授權 |
| `Pairing()` | 進入區網視圖時、每 5 秒（僅在區網視圖）、倒數歸零時 | 序號守衛：慢的回覆不能覆蓋快的 |
| `OpenPairing(0)` | `btn-pairing-on`（標籤「與另一台機器配對」） | **一定傳 0**（用節點預設時長）；**不論有沒有 -discover 都可按**，節點已不再拒絕開不了廣播的視窗 |
| `ClosePairing()` | `btn-pairing-off` | |
| `PairRequests(all)` | 開啟配對抽屜時、抽屜開著時每 2 秒、每次決定之後 | 序號守衛；**只在抽屜開著時讀**（節點會替每個 pending outgoing 去對端輪詢）；`all` 由「顯示已結束」勾選框決定 |
| `StartPairRequest(address)` | 候選列的「送出配對請求」、位址表單的「送出配對請求」 | 只送位址，不送金鑰/指紋/節點 ID；失敗走 banner，錯誤碼翻成中文（§4.3） |
| `ApprovePairRequest(id)` | 收到的請求列「指紋一致，核准」 | id 來自該列本身，不是欄位；成功後重讀請求清單與 `Overview()` |
| `ConfirmPairRequest(id)` | 送出的請求列（`awaiting-confirm`）「指紋一致，確認」 | 同上 |
| `RejectPairRequest(id)` | 任一未決請求列「拒絕」 | 成功後把節點回的 `nextStep` 放進 banner——推不出去的拒絕只有這裡會說 |
| `Inbox(sessionId)` | 每列的「收件匣」（動作欄） | 開**抽屜**（`inbox-modal`，三個分頁：收件匣／送出紀錄／喚醒紀錄），先畫 loading；序號守衛；清空按鈕只在答案回來後才對準這個 session |
| `ClearInbox(sessionId)` | 收件匣「清空收件匣…」 | `confirm` 後執行；結果（移除 N 則 / 失敗未變動）顯示在**對話框內**，不是 banner |
| `ServiceStatus()` | 每次 Overview 後 | 五種狀態文案（找不到 ah / 不支援 / 已裝執行中 / 已裝未執行 / 節點在跑但非服務 / 都沒有） |
| `InstallService(form)` | 服務表單「安裝為背景服務」 | 顯示 `$ command` + output；失敗把錯誤放進 output 區 |
| `UninstallService()` | `service-uninstall` | confirm；成功後 reload |
| `LocalAddresses()` | 開啟服務表單時 | 重建位址下拉；非私有網段自動帶入 `treatAsPrivate` 建議並說明 |
| `NodeURL()` / `SetNodeURL()` | 目前**沒有** UI 入口（靠 `AGENTHUB_URL`） | 保留為未接綁定 |

| `Outbound(session, limit, after)` | 收件匣抽屜的「送出紀錄」分頁 | 分頁載入；序號守衛；`next` 為空表示沒有更多 |
| `Wakes(session, limit)` | 收件匣抽屜的「喚醒紀錄」分頁 | 分頁載入；序號守衛；`limits` 顯示節點端的上限 |
| `NodeSettings()` | 進入設定頁的節點設定區 | 讀回 `settings`（執行中）與 `saved`（存下來的）兩份；規則見 §7.8 |
| `SaveNodeSettings(patch)` | 節點設定區的「儲存」 | patch 併到 **`saved`** 不是 `settings`；存完重讀並比對「有沒有真的寫進去」；沒生效要說出來 |
| `RestartService()` | 節點設定區的「重新啟動服務」 | 走 `ah service restart`；**Windows 上 `internal/service` 回 `ErrUnsupported`，這顆會失敗** |
| `SetNodeAddress(...)` | 節點詳情的位址欄 | 草稿欄位，`interactionInProgress()` 期間不被背景重畫蓋掉 |
| `SetNodeURL(url)` | 設定頁的節點 URL | 之後所有讀寫都對這個 URL |
| `HostPlatform()` | 啟動一次 | 回 `runtime.GOOS`；`darwin` 時加 `body.mac` 讓標題列留出視窗按鈕的位置。問的是**主機**不是節點，兩者是不同的事實，而節點的那份正好在連不上時缺席 |
| `CopyText(text)` | MCP 設定、resume 指令、指紋等所有「複製」 | 寫入剪貼簿；結果顯示在原地（對話框內或列上），不是 banner |

## 3. 畫面與元件清單（現況，2026-09-15 對照 main 的 index.html 與 src/ 重寫）

> 這一節在改版期間停在改版前的狀態，寫著「無排序」「表格 8 欄」「chip 帶全域計數」「安裝表單六個欄位」，
> 全部與程式不符，而且 §7.6 自己就寫著安裝表單只剩資料庫路徑。以下每一條都對照過原始碼。

### 3.1 全域

- 標題列：連線點（ok/bad）、`node-line`（節點名稱 · 平台 · URL）；三個分頁 `本機 session` / `區網` / `設定`
  （前兩個帶計數）；右側四個控制項：**服務狀態 pill**（`service-pill`，點了跳設定頁的服務區）、
  重新整理、重新掃描、預覽 heartbeat。標題列是 Wails 拖曳區；macOS 下 `body.mac` 讓左側留出視窗按鈕的位置。
- Banner：一則，錯誤或成功（ok），成功會自動消失。
- 狀態列：左「顯示 N / total 個 session · 所有已配對 N · 指定節點 N · 不公開 N」，右本機節點 ID。
- `busy` 狀態：任何寫入進行中，所有寫入按鈕 disabled。

### 3.2 本機視圖

- 搜尋框：比對 `id`、`cwd` 與管理方式，大小寫不敏感。
- **8 個篩選 chip，三組**（`provider` 2、`status` 3、`audience` 3），由 `sessions/filter.js` 的 `CHIPS` 產生。
  **組內可多選（OR），組間 AND**（`matchesGroups`）。每個 chip 帶的是 **facet 計數**——把**其他**組的篩選與
  搜尋都套用後這個 chip 會match到幾筆，所以開著「Codex」時「active」旁邊的數字跟表格一致。計數為 0 且未選取的 chip 加 `zero` 樣式。
- 選取列：全選目前篩選結果（含 indeterminate）、已選取 N 個、「設定公開對象…」「收回選取」。
- **表格 9 欄**：勾選、SESSION（含 provider badge）、狀態、管理、公開對象、**旗標**、工作目錄、最後活動、**動作**。
- **有排序**：6 個表頭可排序（`id`、`status`、`management`、`audience`、`cwd`、`lastSeenAt`），
  預設 `lastSeenAt` 由新到舊。`status` 與 `audience` 用語意順序不是字母序（active→idle→inactive；
  all_paired→selected→none）。排序與篩選都寫進 localStorage。
- 列動作兩顆：`收件匣 ｜ resume`，靠右 sticky，`col.c-actions` 176px。MCP 入口已移除，見 §10。
- 空狀態：「沒有符合條件的 session。」

### 3.3 區網視圖

左欄（`nodelist`）：
- 已配對節點列表：每列 presence 點 + 名稱 + presence 文字 + 平台 · 最後聯繫。空：「尚未配對任何節點。」
- 配對模式面板：headline、倒數（獨立元素，每秒只改這一個）、detail、開啟／停止按鈕、note（含 `broadcastWarning`，把本機名稱和它的來源說出來）。
- 正在廣播的機器：`candidate-full` 警告（在捲動區**外面**）、候選列（名稱、爭用/重複 pill、平台 · 位址、完整 nodeId、完整指紋、首次/最後看到、「送出配對請求」＋「改用手動填入…」）、`candidate-notice`（節點自己的免責文字）。
- 「配對新節點…」按鈕與說明。

**配對抽屜（`pairing-modal`）的順序，由上而下（#63）**：

1. **`btn-pairing-on`「與另一台機器配對」**：開視窗，**永遠可按**（`windowAvailable`；
   availability 為 `off` 或 `openNotAnnouncing` 都不影響）。只要節點給了 `state.peerAddress`，
   下面就出現 `#pair-here`：`#pair-local-address` 用 `.keyvalue` 大字顯示它，旁邊 `copy-pair-address`
   走 `CopyText`，複製失敗要說出來。**有廣播也要顯示**——mDNS 過不去跟 mDNS 沒開一樣安靜，
   差別只在旁邊那句話（`state.notice` 非空 → 這是唯一的路；否則 → 對方等不到時的退路）。

   **但位址不可達時不准把它當成「對方要輸入的位址」印出來，而「沒有位址」也是不可達的一種。**
   預設節點沒有 `-allow-lan`，peer listener 在 loopback 上，而 `announceablePeerAddress()`
   （`cmd/agenthub-node/main.go`）對 loopback／不可公告的位址回**空字串**——所以預設節點的
   `/v1/pairing` 根本**不帶 `peerAddress` 這個欄位**。`#pair-here` 以前遇到空位址是整塊隱藏，
   於是全新安裝的使用者只看到「配對視窗開著，但還沒有人連得進來。」，沒有原因也沒有按鈕。
   **現在只有 `windowAvailable` 為 false 才隱藏**；位址不可達（含完全沒有位址）一律顯示
   一句「還沒有人連得進這台機器」＋原因與補救（「允許區網連線」＋挑一個真的區網位址＋重啟節點）
   ＋一顆跳到「設定 → 節點設定」的按鈕（`goToNodeSettings()`，順手關掉抽屜），
   且 `copy-pair-address` 要 disabled。沒有位址時用 `PAIR_TEXT.hereNoAddressWhy`，
   有一個不可達的位址時用 `PAIR_TEXT.hereUnreachable`。

   **判定順序**：節點自己說了就聽節點的。`PairingState` 有兩個附加欄位（#172）：
   `peerAddressReachable`（bool）與 `peerAddressProblem`（string）。
   `pairHereState()` 的規則是——`peerAddressReachable` 是 boolean 就用它，並把
   `peerAddressProblem` 當**次要細節行**放在補救句下面（只在 reachable 為 false 時顯示）；
   欄位不存在（舊節點）才退回 `pairAddressReachable()` 自己判字串：空字串、loopback
   （`isLoopbackListen`）、未指定位址（`0.0.0.0`、`::`）都是**還不能用**。
   欄位缺席**不得**當成 false——那是替節點講它沒講過的話。Go 端因此用 `*bool`。
   同時**視窗 headline 不得只說「配對視窗開啟中」**——那讀起來像「好了」，而實際上視窗開著卻沒有入口，
   使用者會跑去另一台乾等。測試：`pairing-exchange.mjs` §8a（沒有 `peerAddress` 欄位的預設節點）、
   §8a-ii（節點自己說不可達＋原因）、§8a-iii（真的區網位址）、§8b（loopback 字串）。

   **抽屜標題底下那句是 render 出來的，不是寫死在 `index.html` 的**（`#pairing-sub`，
   `renderPairingSubtitle()`）：「開啟後同網段的人都會知道這台機器在跑 AgentHub。」只有在
   `announceableAddresses > 0` 時才成立，寫死在標記裡就是在一個不廣播的節點上開頭第一句就說謊。
2. **`#pair-waiting`**：等你決定的請求有幾個，一句話。請求面板在候選清單下面，短視窗時會在摺線以下。
3. **候選列的「送出配對請求」**：一鍵送出，只帶該列的 `address`。「改用手動填入…」是次要路徑。
4. **`#pair-address` + `btn-pair-send`**：手打對方畫面顯示的位址；Enter 等同按鈕；送出前 trim，空字串不送。
5. **`#pair-requests` 請求面板**：
   - 每列，由上而下：名稱 + 狀態 pill、平台 · 位址、完整 nodeId、request id、
     **`PAIR_TEXT.compare` 警語（在指紋區塊之上，一列只印一次）**、**指紋區塊**、
     一句中文指示、按鈕。警語在上面是因為節點自己的文案就是這樣假設的
     （「下面兩組指紋，上面是發起方…」）；放在下面會被讀成對「決定」的註解，而不是對「怎麼讀上面兩行」的指示。
   - **指紋區塊照節點給的 `fingerprints` 陣列原樣渲染**：順序是節點排的（發起方在上，兩台一致），
     標籤 `role`／`whose` 走固定對照表，值本身一個字都不動。前端**不得**自己排序、推導或只顯示一組。
     節點沒給這個陣列時不自己湊一組，改說去終端機用 `ah pair pending` 比對。
   - 按鈕**一定在指紋下面**：`incoming/pending` →「指紋一致，核准」＋「拒絕」；
     `awaiting-confirm` →「指紋一致，確認」＋「拒絕」；`outgoing/pending` 只有「拒絕」，
     但**文字要說出之後還要回到這台按確認**——只被告知「等對方核准」的發起方會卡在那裡。
   - `reason: fingerprint_mismatch` 的拒絕**要跟一般的「已拒絕」分開講**
     （`PAIR_TEXT.step["rejected-fingerprint-mismatch"]`）：那是唯一一種在講網路、
     不是在講某個人的決定的結束方式。
   - 按下核准／確認／拒絕之後的 banner 用**這個視窗自己的中文句子**
     （`pairStepText()`，退回 `PAIR_TEXT.decided`），節點的英文 `nextStep` 以
     「（節點回報：…）」跟在後面——既不能只丟英文，也不能把它吞掉。
   - 已結束（approved/rejected/expired，含 `reason: displaced`）不進預設清單，
     `pair-requests-all` 勾選 → `all=true`。已結束的列才顯示節點的 `nextStep`。
   - **任何地方都不得有自動核准或略過比對的入口。**
6. 底部 footer：手動 5 欄位配對，是兩台連不上彼此時的退路。

右欄（`nodedetail`）：
- 節點詳情：名稱、完整指紋、核對說明、節點 ID／平台／配對時間／最後聯繫／可見的 session 數、「撤銷信任」+ 說明。
- 「這個節點公開給我的 session」：四種 presence 狀態 + `sessionsWithheld` + 空 + 表格（SESSION／節點／PROVIDER／狀態／最後活動）。

### 3.4 設定頁

三個區塊，由 `settingsSection` 決定捲到哪一個：

- **背景服務**：狀態行、重新讀取、安裝／重新安裝、移除；展開表單**只有一個欄位：資料庫路徑**
  （`service-db`，留空＝節點預設位置）。其餘五個值不在這裡，是 #116 的決定——燒進 unit 檔會變成節點之外的
  第二份設定來源。安裝說明；輸出區 `<pre>`。節點沒在跑且未安裝時表單自動展開一次。
- **節點設定**：對外位址、允許區網、`-discover`、視為私有網段、自動喚醒。存的是節點**下次啟動**才讀的值，
  規則見 §7.8。
- **外觀**：背景照片與數字雨兩個開關，數字雨預設關閉，見 §9。

### 3.5 覆蓋層（6 個：2 個抽屜 + 4 個對話框）

抽屜（`.drawer`，從右側滑出）：
- `inbox-modal` 收件匣：**三個分頁**（收件匣／送出紀錄／喚醒紀錄）。警語（資料不是指令；「自稱」後是寄件者自選）、meta、
  訊息列（寄件者分兩半：驗證過的 node id 用 `fingerprint` 樣式，自選的 session 用 `claimed` 樣式，中間「自稱」）、清空。
- `pairing-modal` 配對：把 §3.3 左欄的配對模式與候選清單裝進抽屜。

對話框（`.modal`）：
- `pair-modal` 配對新節點：說明（`ah node`、指紋逐組相符）、五個欄位、prefill note、本機指紋、送出。
- `audience-modal` 設定公開對象：套用到 N 個；三種 mode radio；指定節點的 ID 輸入；四個旗標；套用。
  **每次開啟四個旗標一律重設為 off**（測試 `audience-dialog.mjs`）。
- `mcp-modal` MCP 設定：**列上已無入口**（§10），由 `openMCPConfig(sessionId)` 開啟，顯示該 session 的 `.mcp.json` 片段，文案說明 per-project 與 `--outbound` 的限制。
- `modal` Heartbeat 預覽：說明 + `<pre>`。

## 4. 安全與文案契約（測試逐字斷言的，不可改寫）

以下字串或行為被測試 `includes()` 直接比對，新設計要原樣保留：

| 位置 | 必須出現 | 不得出現 |
|---|---|---|
| 收件匣訊息列 | 寄件者 node id 在 `class="fingerprint"`；session 在 `class="claimed"`；「自稱」 | 任何 `on*=`、`href=`、`src=`、`style=` 屬性；寄件者資料進 class |
| 收件匣 loading | 「讀取」 | 「還沒有任何訊息」 |
| 收件匣失敗 | 錯誤原文 | 「還沒有任何訊息」 |
| 收件匣分頁 | showing 與 held 數字 | |
| 清空失敗 | 「沒有變動」+ 錯誤原文 | |
| 清空成功 | 「移除 N 則」 | |
| 候選列 | 完整指紋、完整 nodeId、平台、位址、首次與最後看到、「身分有爭用」「名稱或指紋重複」、無名時「（未提供名稱）」 | 候選資料進 class |
| availability=off（節點連 `/v1/pairing` 都拒絕，`windowAvailable` 為 false） | 「-discover」「沒有在看」；開啟按鈕 disabled | 「機器在廣播。」 |
| `windowAvailable` 為 true 但廣播不出去 | 「不會出現在對方的候選清單」、節點自己的 `lastError`；**開啟按鈕必須可按**；`#pair-here` 顯示位址 | 「開啟後，同網段的人都會知道」（沒東西送出去就不是取捨） |
| availability=openNotAnnouncing | 視窗畫成**開著**（summary pill「配對中 · 剩 m:ss」）＋位址提示；候選區同 `off` 的說法 | 「未啟用」、「配對狀態讀不到」 |
| `state.peerAddress` 是 loopback / `0.0.0.0` / `::`，**或整個欄位不存在**（預設節點） | 「還沒有人連得進這台機器」＋「允許區網連線」＋跳設定按鈕；`copy-pair-address` disabled；headline 不得只說「配對視窗開啟中」 | 把 `127.0.0.1:7463` 當成對方要輸入的位址印出來；把整塊 `#pair-here` 藏起來 |
| `state.peerAddressReachable === false` | 補救那一段 ＋ 節點自己的 `peerAddressProblem` 當次要細節行 | 用前端自己的猜測蓋掉節點的判定 |
| `state.peerAddress` 非空且可達 | `#pair-here` 一律顯示，**有沒有廣播都顯示**；旁邊那句依 `state.notice` 有無而不同 | 只在沒廣播時才顯示 |
| 抽屜標題 `#pairing-sub` | 依 `announceableAddresses` 換句子 | 不廣播的節點上出現「開啟後同網段的人都會知道」（兩種寫法都算，有逗號沒逗號） |
| 拒絕／核准／確認的 banner | 中文句子；節點英文 `nextStep` 在括號裡 | 只有節點的英文 |
| `reason: fingerprint_mismatch` | 指紋不一致的專屬句子 | 跟一般拒絕同一句 |
| 送出請求被 `PEER_PAIRING_BUSY` 拒 | 節點原文（對端自己的理由＋補救）；**不得**出現錯誤碼 | 本地自己寫的一句話（它蓋掉的是兩種不同的 429） |
| 配對請求列（未決） | 兩組指紋、節點給的標籤、`PAIR_TEXT.compare`、按鈕在指紋**下面** | 只顯示一組指紋；`ah pair approve`（GUI 裡跟按鈕自相矛盾） |
| 配對請求列（已結束） | 一句結果 + 節點的 `nextStep` | 核准／確認按鈕 |
| 配對面板任何位置 | | 「自動核准」「略過比對」「全部核准」「不比對」 |
| availability 未知 | 「不可信」、錯誤原文 | 「-discover」「機器在廣播。」 |
| 配對視窗開啟 | headline 含「開啟中」；倒數含 `:` | 到期時「0:00」 |
| 配對視窗到期 | 「已到期」，且觸發一次 `Pairing()` | |
| Peer 離線 | 「離線」 | 舊 session id |
| Peer 從未 | 「尚未收到」 | 「離線」、session id |
| Peer presenceError | | 「尚未收到」、session id、presence label class |
| 未知 status | pill 的 class 恰為 `pill` | |
| 所有 provider/peer/candidate/inbox 字串 | 以 textContent 進 DOM | `<script`、`onerror`、`<iframe` 等未跳脫 |

行為契約：
- 倒數 tick **不得**重建候選列元素（測試比對 element identity）。
- 候選列由 nodeId 當 key、可重複呼叫：同一台機器沿用同一個 row 元素，文字就地改寫，
  只有新增／消失／換位才動 `candidate-rows` 本身。這條對**每一次** render 都成立，
  不只倒數——抽屜那個 2 秒 tick 最後也會走完整 render，而 `replaceChildren` 就算把同一批
  元素放回去，瀏覽器仍會把每個 child 拆下再掛上：焦點掉了，橫跨 tick 的那一次按壓
  （mousedown 與 mouseup 分屬前後）也不會變成 click。測試除了比對 element identity，
  還會在候選內容沒變時計算 `candidate-rows.replaceChildren` 的呼叫次數，必須是 0。
- 配對請求列（`pair-requests`）套同一條規則，key 是 request id：同一個交換沿用同一個 row，
  指紋區塊只在節點答出不同的值時就地改寫，容器只有新增／消失／換位才動。
  **例外，且是安全性的例外**：一個沿用中的 row 若指紋簽章變了，那就不是擁有者一直在比對的那一列——
  就地改寫等於在游標已經停在上面時把兩個值換掉，而瞄準舊值的那次按壓仍然算數。
  這種情況要把 row 丟掉重建，讓瞄準前一個元素的 mousedown 不可能變成 click。
- 配對抽屜開著時，那個 2 秒 tick 除了讀請求，還要順手重讀一次 overview
  （`load({ background: true, exceptPairingDrawer: true })`）：抽屜是 modal，會把 15 秒的
  背景重讀擋住，於是在終端機跑 `ah revoke` 之後，抽屜後面那份已配對節點清單會一直停在舊的。
  其餘的守衛（`state.busy`、有選取的列、游標在輸入框裡）照舊生效。
- 模組只能註冊**四個** `setInterval`：5 秒 pairing 輪詢（僅區網視圖）、2 秒配對請求輪詢
  （僅區網視圖**且配對抽屜開著**，`state.busy` 時跳過——每次讀都會讓節點去對端輪詢）、1 秒倒數、
  15 秒背景重讀清單（`interactionInProgress()` 為真時跳過；#114 曾經整個視窗停在 0 筆而節點正服務 1083 筆）。
- `OpenPairing` 呼叫參數必須是 `[0]`。
- `inbox-clear` 在 `confirm` 回 false 時不呼叫 `ClearInbox`。
- 公開對象對話框每次開啟四個旗標為 false，且 `readAudienceForm()` 回傳四個 false。

### 4.1 測試架構的耦合

六個測試都是把 `main.js` 原始碼讀成字串、剝掉 import、用 `new Function`
執行，並用 `dom-shim.mjs` 的假 DOM 以 **element id** 定位。這表示：

1. 拆成多個模組會讓六個測試全部失效。重新設計時要一併把測試改成 `import` 模組。
2. 測試依賴的 id 清單：`rows`、`inbox-body`、`inbox-meta`、`inbox-close`、
   `inbox-clear`、`pairing-headline`、`pairing-countdown`、`pairing-detail`、
   `pairing-note`、`candidate-full`、`candidate-rows`、`btn-pairing-on`、
   `btn-pairing-off`、`audience-cwd`、`audience-messages`、`audience-outbound`、
   `audience-autowake`、以及 peer 測試自己插入的 `probe-*` 容器。
   改 id 就要同步改測試；改前先確認測試仍在測同一件事。
   #63 又加了一組：`pair-here`、`pair-local-address`、`copy-pair-address`、
   `copy-pair-address-status`、`pair-here-note`、`pair-waiting`、`pair-address`、
   `btn-pair-send`、`pair-address-note`、`pair-requests`、`pair-requests-all`、`pair-requests-note`、
   以及 `pairing-sub`（抽屜標題下那句，現在由 `renderPairingSubtitle()` 寫）。

### 4.3 配對交換的錯誤碼 → 中文句子（#63）

節點的錯誤是 `CODE: message`，message 是英文而且會叫人去跑 `ah` 子指令——那是寫給終端機的，
在一個按鈕就在旁邊的視窗裡是錯的建議。所以 `PAIR_TEXT.errors` 把下列碼各翻成**一句話 + 一個補救**，
其餘的碼**原樣保留節點的話**（沒人翻譯的拒絕仍然是答案，吞掉它才是把使用者留在原地）：

`PEER_TOO_OLD`、`PEER_PAIRING_CLOSED`、`PEER_PAIRING_DUPLICATE`、`PAIRING_BUSY`、
`PAIRING_DUPLICATE`、`PAIRING_STATE`、`PAIRING_EXCHANGE_DISABLED`、`PEER_UNREACHABLE`、
`PEER_KEY_MISMATCH`、`ADDRESS_NOT_ALLOWED`、`NOT_FOUND`。

`PEER_PAIRING_BUSY` **刻意不翻**：它的 body 帶著對端自己的理由與補救，而它轉述的 429 蓋著兩種
不同的拒絕（同一來源位址的上限，以及整份清單滿了），兩者要在不同地方解。在這裡寫死一句話一定會
挑其中一種、對另一種說錯話。比對錯誤碼是**從字串開頭錨定**的，不是子字串比對——
`PAIRING_BUSY` 曾經因此替 `PEER_PAIRING_BUSY` 回答，把「這台滿了」說成了對面那台的事。

所有新增的使用者可見字串都放在 `app.js` 的單一 `PAIR_TEXT` 物件裡，之後的英文化是換掉這個物件，
不是在四十個呼叫點裡找字串。

## 5. 這次要修的 UX 問題（建議，非契約）

1. **篩選群組不可見。** 8 個 chip 平鋪，使用者看不出三組、組內單選、組間 AND。
   → 三個有標題的分段群組；組內改多選 OR，組間 AND。
2. **chip 計數是全域值。** 篩了 Codex 之後 active 仍顯示全部。
   → facet 式計數：顯示套用「其他群組」篩選後的數量。
3. **沒有排序。** → 表頭可點排序，預設最後活動由新到舊；排序與篩選存 localStorage。
4. **搜尋範圍只有 id 和 cwd。** → 加 provider、management、audience 文字。
5. **選取列在區網視圖仍顯示。** → 只在本機視圖，且有選取才浮出。
6. **服務面板佔主流程。** → 移到獨立「設定」視圖或右側抽屜；標題列只留一行狀態 + 入口。
7. **收件匣按鈕藏在 ID 欄，無未讀數。** → 獨立動作欄；`Inbox` 目前每次只讀一個
   session，未讀數需要節點端新增計數欄位，否則只能顯示「有／無」。**這是後端變更，先標記。**
8. **audience 四個旗標在表格看不到。** → 公開對象欄加旗標圖示或展開列。
9. **四個 modal 同層級。** → heartbeat 與收件匣改側邊抽屜；配對與公開對象保持 modal（有不可逆動作）。
10. **區網視圖左欄塞了三件事**（節點清單、配對模式、候選）。→ 配對模式與候選合成一個「配對」分頁或抽屜，節點清單獨立。

## 6. 驗收清單（2026-09-11 實作分支 `feat/desktop-ui-redesign` 的狀態）

- [x] §2 的綁定每個都有入口：表格、列動作、對話框、抽屜、設定頁；另加 `Outbound` / `Wakes`（§7.5）。
      `SetVisibility`、`NodeURL`、`SetNodeURL` 依原狀未接。
- [x] §3 每個畫面元素都有對應：服務面板搬到設定分頁；配對面板與候選合成抽屜；收件匣改抽屜並加兩個分頁。
- [x] §4 全部保留：九個 node 測試改成 `import src/app.js`，斷言逐字未動；Go 靜態測試（§4.2）通過。
- [x] `npm test` 十個全綠（新增 `sessions-filter.mjs`）；`go test ./...` 通過；`vite build` 成功。
- [x] §5 的 1、2、3、5、6 已做；4（搜尋範圍）已做；8（旗標欄）已做；9（抽屜）已做；10（配對抽屜）已做。
- [x] §8 列動作「複製 resume 指令」已做。
- [ ] §5 的 7 未讀數：節點端沒有計數欄位，收件匣按鈕沒有徽章；待另開 issue。
- [ ] `wails build` 產出 .app 的實機操作驗證：本 session 只用 `frontend/dev/mock.html` 假資料看過版面，
      未接真節點跑過。

## 7. 並行中的變更（2026-09-11 記錄）

> **這一節是 2026-09-11 當下的快照，不是現況。**裡面的「未開 PR」「尚未動工」「尚未合併」
> 講的是那一天的狀態，之後全部完成了。要看現況請看 §1–§3 與 §9、§10；這裡留著是為了記住
> 當時的判斷與理由。已經明顯與現況牴觸、又不帶日期的句子已就地更正。

另一個 session 在 `feat/112-copy-mcp-config` 分支上加了一個功能，重新設計實作時必須納入，
否則就是缺功能：

| 綁定 | 入口 | 行為 |
|---|---|---|
| `MCPConfig(sessionId)` | **列上沒有入口**（2026-09-15 移除，見 §10）；綁定與 `mcp-modal` 都保留，由 `openMCPConfig(sessionId)` 呼叫 | 開 `mcp-modal`：`<pre id="mcp-text">` 顯示這一列的 `.mcp.json` 片段、`mcp-status` 顯示複製結果；文案說明 per-project 與 `--outbound` 的限制 |

- `MODAL_IDS` 多了 `mcp-modal`；新設計若把對話框改成抽屜，這個一起改。
- 設計稿的對應：這條已被 §10 取代——MCP 入口 2026-09-15 從列上移除，動作欄是「收件匣」與「resume」兩顆，
  不需要第三個圖示或「⋯」選單。
- 該分支合併前不要開始改 `frontend/`，否則同一份工作樹會互相覆蓋（見下）。

### 7.1 兩個 session 共用同一個工作樹

兩個 Claude session 都在 `~/Projects/others/agenthub` 原地工作，共用同一個 checkout 與分支。
這表示：一方 `git checkout` 會換掉另一方看到的檔案；一方 `git add -A` 會把另一方的未追蹤檔
一起提交。本文件與 `ui-redesign/` 目前是未追蹤檔，請實作 #112 的 session 提交時只 add 自己的檔案。

### 7.2 排隊中、會動到 frontend 的工作（2026-09-11 查 GitHub）

| 項目 | 狀態 | 動到的介面 | 對重新設計的意義 |
|---|---|---|---|
| #112 MCP 設定 | 本機分支，未開 PR | session 列動作、新 modal | 見 §7 |
| #110 / PR #123 | 已開 PR、可合併 | index.html +26、main.js +156、style.css +77、新測試 pairing-completeness.mjs | 配對對話框加本機公鑰、雙向提示、記錄 peer 位址；Pairing 與 Network 設計稿要補 |
| #111 訊息去向 | issue | 收件匣旁加「送出紀錄」「喚醒紀錄」兩個視圖；節點列位址缺失標紅 | 收件匣抽屜要預留分頁；節點列多一種警示狀態 |
| #116 啟動設定進 DB | issue，先要 node 端改 | 設定頁改成直接改設定＋重啟服務 | Settings 設計稿方向一致，欄位不變，按鈕從「重新安裝」變「儲存並重啟」 |
| #117 打包 ah 進 .app | issue | 無 | 無 |

結論：#112 與 #110 是小增量且已在路上，等它們合併；#111、#116 尚未動工，重新設計直接把它們的
需求畫進去並在新 UI 上實作，不必等。

### 7.3 #122 / #123 合併後，對方 session 補充的事實（2026-09-11，main `ea41a17`）

- MCP 設定片段**永遠帶 `-url <App 連的 node>`**；剪貼簿寫入是獨立綁定 `CopyText`，
  而且**遲到的回應不得寫剪貼簿**（序號守衛，與收件匣、配對同一套規則）。
- 配對面板：本機公鑰要**可複製**；配對成功後要**明說對方也要做一次**。
- 節點詳細頁的位址欄是**草稿欄位**：背景每 15 秒重畫，`interactionInProgress()` 現在把
  「任何文字欄位有焦點」算成互動而暫停重畫。新 UI 若換掉這套刷新機制，這幾個保護要一起帶過去。
- 這些在 main 上都有測試（`pairing-completeness.mjs` 等）；改測試架構時逐條保留斷言。

### 7.4 工作樹安排

重新設計在獨立 worktree `../agenthub-ui-redesign`、分支 `feat/desktop-ui-redesign`（自 `ea41a17` 開）。
本顆樹 `agenthub/` 留給另一個 session 的 agent 做每個 PR 的 checkout。

### 7.5 #111 端點形狀（#125 已合併，main `a742023`；欄位已對照原始碼核實）

新 UI 的「送出紀錄」「喚醒紀錄」視圖接這兩個端點。**已實作**：`Outbound(session, limit, after)` 與
`Wakes(session, limit)` 都在 `app.go` / `client.go`，畫在收件匣抽屜的第二、三個分頁（§3.5）。

**`GET /v1/outbound?limit=50[&after=<cursor>]`** — 本節點排給 peer 的訊息，最新在前。
`limit` 1–200，預設 50。回應 `{"messages":[…],"next":"<cursor>"}`；空清單無 `next`，滿頁才有。
每列（`registry.OutboundMessage`，清單**不含 `body`**）：

| 欄位 | 型別 | UI 用法 |
|---|---|---|
| `id` | string | 列鍵；點開可打 `GET /v1/outbound/{id}` 看 body |
| `to` | string | 目的 session（peer 端的 id） |
| `destinationNodeId` | string | 對應已配對節點名稱顯示 |
| `from` | string, 可空 | 本機來源 session |
| `state` | `pending` / `delivered` / `refused` | pill 三色；refused 為警示 |
| `attempts` | int | 次要資訊 |
| `createdAt` / `updatedAt` | ISO | 相對時間；預設以 `updatedAt` 排 |
| `lastError` | string, 可空 | **peer 提供、無長度上限（最壞 64KB）**；顯示前截到 ~200 字，完整內容放展開列 |
| `wakeHops` | int, 可空 | 有才顯示 |

**`GET /v1/wakes?limit=50[&session=<id>]`** — 誰喚醒了這台的 agent，最新在前。
`session` 過濾單一本機 session（非本機 id 會被拒，不是回空）。回應 `{"wakes":[…],"limits":{…}}`。
每筆（`registry.WakeEvent`）：

| 欄位 | 型別 | UI 用法 |
|---|---|---|
| `id`, `messageID` | string | 列鍵；`messageID` 可連到收件匣 |
| `sourceNodeId` | string, 可空 | 空＝本機來源 |
| `sourceSession` | string, 可空 | 寄件者自選，**用 `claimed` 樣式** |
| `destinationSession` | string | 被喚醒的 session |
| `hops` | int | |
| `outcome` | `woken` / `refused_hops` / `refused_pair_rate` / `refused_session_rate` / `refused_node_rate` / `failed` | woken 綠；refused_* 琥珀並在旁邊放對應 `limits` 值；failed 紅 |
| `detail` | string, 可空 | 原因文字 |
| `at` | ISO | 相對時間 |

`limits` 帶 `hops`、`pair`+`pairWindow`、`session`+`sessionWindow`、`node`+`nodeWindow`，
拒絕理由要跟產生它的規則並排顯示。對應 CLI：`ah outbound`、`ah wakes [session]`。

**#116 設定端點**（當時未合併，現已在 main，見本節標題的日期說明）：一個 `GET /v1/node/settings` 回各欄位＋來源，
一個 `PUT` 部分更新回 `restartRequired: true`；欄位 `peerListen`、`allowLan`、`discover`、
`treatAsPrivate[]`、`autoWake`。設定頁的主按鈕由「重新安裝（改旗標）」改為「儲存並重啟服務」。

## 7.8 節點設定頁：八輪審查釘出來的四條規則

實作 `feat/116-settings-page` 時，前五輪各出現一次 P1，全部源於誤解節點的語意。列在這裡，
因為每一條看起來都像細節，實際上都會讓使用者的設定悄悄消失或讓節點對外開放：

1. **表單編輯的是「下次啟動會用的設定」（`saved`），不是正在跑的值（`settings`）。**
   節點把寫入合併到 `saved` 再判斷（`internal/api/settings.go` 自己的註解就寫著）。用正在跑的值
   當基準，會讓一個無關欄位的存檔把記住的區網位址撤掉，而且那個開關永遠送不出去。
2. **`loopback` 看主機不看埠**，照 `nodeconfig.ValidateLoopback`。只比對 `127.0.0.1:7463` 會把
   `127.0.0.1:9999` 判成對外位址。
3. **設定有沒有生效要用觀察的，不能用推論的。** 節點每次啟動都把命令列給的值寫回自己的資料庫
   （`cmd/agenthub-node/main.go`），所以服務單元裡烘進去的旗標在重啟後同時是 running 也是 saved，
   從來源欄位看不出任何異常。唯一可靠的辦法是比對「使用者要求的值」與「重啟後節點實際持有的值」。
4. **表單不替使用者做選擇。** 早期版本會因為選了區網位址就自動打開允許區網，而且綁在那個開關
   自己的事件上，結果它根本關不掉——節點最主要的行為從視窗裡無法觸發。表單只預測節點會怎麼做，
   不改使用者控制的欄位。

## 8. 新需求（2026-09-11，owner 指定）

**列動作「複製 resume 指令」。** 依 provider 產生指令並寫入剪貼簿：

| provider | 指令 | 依據 |
|---|---|---|
| claude | `claude --resume <providerSessionId>` | `adapter/claude.go` 讀 jsonl 的 `sessionId`，與 `claude --resume` 接受的 id 相同（已用本機檔案核對） |
| codex | `codex resume <providerSessionId>` | thread id |

- 剪貼簿寫入用 #112 的 `CopyText` 綁定，同樣受序號守衛：遲到的回應不得寫剪貼簿。
- 複製後的回饋要帶工作目錄提示：「在 <cwd> 執行」，cwd 為空則省略。
- 與「收件匣」並排為兩個列動作（MCP 那顆已移除，§10）；設計稿與實作都要有。

### 4.2 Go 端靜態測試的硬性要求（`desktop/frontend_test.go`）

除了 node 測試，`go test ./desktop/...` 還直接讀前端原始碼。新 UI 必須滿足：

- **禁用 sink**：任何 `src/*.js` 不得出現 `innerHTML`、`outerHTML`、`insertAdjacentHTML`、`document.write`。
- **`statusPillClass`**：某個 `src/*.js` 要有 `function statusPillClass`，且內含
  `status === "active" || status === "idle"`（未知 status 只能落到 `pill`）。
- **每個 `el("...")` 字面查找的 id 都必須在 index.html 存在**（動態建立的元素用變數查找不算）。
- **style.css 必須有這些選擇器**（以換行開頭的完整規則）：`.stale {`、`.pill.bad {`、
  `.modal-card .warning {`、`.claimed {`、`#pairing-note .claimed {`、`.noaddress {`、`.keyvalue {`；
  且要提到 `#candidate-rows`。
- **可捲動容器要有對應屬性**：`.nodelist {` 含 `overflow-y`；`#candidate-rows {`、`#inbox-body {`、
  `.inboxrow .inboxbody {` 含 `max-height`。
- index.html 至少一個 `<p class="warning">`。
- **版面（#153／#155，實機才看得到）**：清單卡片**不設 `max-width`**（填滿視窗；原本的 1120px 讓背景只露一條、
  又壓縮了最長的欄）；工作目錄欄 `td.cwd` 用 `direction: rtl` 從左邊裁，路徑本身包在 `<bdi>` 裡隔離方向，
  因為那欄的答案在路徑尾端（裁右邊的話每一列都只剩 `/Us…`）；`col.c-actions` 寬度至少 160px（見 §10；原本三顆列動作時是 250px／量到 241px，
  儲存格 `overflow: hidden` 會把裝不下的裁掉，**任何視窗寬度都一樣**）；`table` 要有 `min-width`
  （沒有的話 `width: 100%` 讓它永遠等於容器寬，窄視窗只會壓縮欄位而不會捲動）；`.col-actions`
  要 `position: sticky`；`style.css` 要有 `body.mac .titlebar` 的左內縮，因為 `main.go` 用
  `mac.TitleBarHiddenInset()`，視窗按鈕會畫在頁面左上角。
- 九個 node 測試由 Go 測試以 `node frontend/test/<file>.mjs` 執行，路徑與檔名不可改。

若新設計把 modal 改成抽屜，`.modal-card .warning` 這條選擇器仍要存在（可以是共用規則），
或同步修改 `frontend_test.go` 並在 PR 說明寫出理由。

### 7.6 #116 設定端點（#129、#134、#138 已合併，main `1fc4ad5`；對方轉述，實作前對照 internal/api 原始碼）

- `GET /v1/node/settings` → `{settings:{peerListen, allowLan, discover, treatAsPrivate[], autoWake}, sources:{欄位→"flag"|"remembered"|"default"}, saved:{同 settings 形狀，DB 值}, restartRequired, peerListenWithdrawn?}`。
- `PUT /v1/node/settings` 收任意子集；`treatAsPrivate` 一律陣列，`[]` 代表撤回。回同形狀加 `message`，`restartRequired: true`（設定只在啟動時生效）。
  錯誤：`400 INVALID_REQUEST`、`409 SETTINGS_UNAVAILABLE`、`500 REGISTRY_ERROR`。
  400 的訊息是單句、無旗標用語，**可直接呈現給使用者**：
  「allowLan is off, so peerListen has to be a loopback address, and it was sent as \<addr\>;
  to serve that address, send allowLan true in the same write」。
- **UI 規則（三條，都會咬人）**：
  1. **提交後一律用回應刷新整個表單**，不要只更新送出的欄位。撤回判斷用的是「合併後生效」的 allowLan：
     stored 已是 `allowLan:false` 而 peerListen 仍是 LAN 位址時，**任何一次 PUT（即使只改 discover）**都會順手把
     peerListen 收回 `127.0.0.1:7463`，`message` 會講。
  2. `peerListenWithdrawn: true` 是可選欄位（`omitempty`）。**不出現就是沒有撤回**，不要當成 false 以外的意思。
     存續規則（#138 定案）：只要啟動時撤回過、且 `saved.peerListen` 仍是預設 `127.0.0.1:7463`，就一直帶著，
     **包含 owner 之後把 `allowLan` 開回 true**。那時 `message` 換成第二種措辭
     （「peerListen still reads as a default: the address this node had remembered was withdrawn at start-up
     and is not recoverable — send it again」），可直接顯示給使用者，這正是提醒重填位址的時機。
     owner 把 peerListen 改成任何別的 loopback 位址（含 `localhost:7463`）→ 旗標與句子都消失。
  3. `sources.peerListen === "default"` 不代表「尚未設定」（撤回後是 default，下次啟動變 remembered）。
- 已知待修（#135，不擋前端）：同一 process 內 owner 又把 LAN 寫回去時，GET 的 `message` 說明字串會過期。
  **顯示 `message` 時以 `saved` 欄位為準**，不要單看字串。
- `--listen` 不進設定，UI 不給欄位。重啟用 `ah service restart`，尚無 API；需要時開 issue。
- **已實作**（`feat/116-settings-page`）：`App.NodeSettings()` / `App.SaveNodeSettings(patch)` 接 GET/PUT，
  `App.RestartService()` 跑 `ah service restart`。設定頁新增「節點設定」區，主按鈕是「儲存並重啟服務」。
  安裝表單只剩資料庫路徑——把這五個值燒進 unit 檔會變成節點之外的第二份設定來源，正是 #116 要拿掉的。
  寫入只送改過的欄位；清空網段送空陣列（省略代表不動，永遠撤不掉）。節點不是背景服務時不假裝重啟過。
  測試：`desktop/settings_test.go`、`frontend/test/node-settings.mjs`（約 30 段、130 條以上斷言，
  逐條反轉都會讓測試失敗）、`frontend/test/dom-shim-select.mjs`（假 select 的行為，兩個 P1 曾靠它的
  失真而矇混過關）。八輪 fresh-context 審查，每一輪的修正都另外做過原始碼變異測試。

### 7.7 `GET /v1/outbound?session=`（#132 已合併，main `c3234e7`）

- `?session=<id>&limit=1..200&after=<cursor>`；`session` 收 `codex:abc` 或 `<本機節點>/codex:abc`，都正規化成本機 session。
- 回應結構不變（`messages`／`next`，最新在前、無 body）；`next` 只在滿頁出現；**續頁要同時帶 `session` 與 `after`**。
- 錯誤：非本機節點位址 → `404 UNKNOWN_NODE`；格式錯 → `400 INVALID_REQUEST`；本機但沒送過 → 空清單，不是錯。
- **已在 PR #131 實作**：`client.outbound(ctx, session, limit, after)` 送出前 trim，空就不帶；
  `App.Outbound(session, limit, after)`；前端 `loadOutbound` 帶 `state.inboxSessionAsked`，續頁重複帶 session，
  客端過濾與自動往前讀已移除，空清單直接顯示「這個 session 還沒有送出過訊息」。
- 邊角（#127 待修）：`session` 只給空白會被 trim 成空、退回全節點清單（`/v1/wakes` 同）；前端送參數前自己 trim，空就不要帶。
  `outbound_test.go` 的 `TestOutboundForwardsTheSessionFilterAndDropsABlankOne` 釘住這個行為。
- `session` 給兩次是 `400`（「session was given more than once」，#139）。本機 client 用 `url.Values.Set`，
  一個鍵只會有一個值，碰不到這個錯；改成 `Add` 才會。

## 9. 背景特效：數字雨是 opt-in（#156，2026-09-14 實測）

在 Ubuntu 測試機（HP ProBook，Intel HD 520，WebKitGTK 2.40，`wails build -tags webkit2_40`）上量到的：

| 狀態 | 驗證方式 | WebKitWebProcess | app 全樹總計 |
|---|---|---|---|
| 數字雨執行中 | 相隔 1 秒兩張截圖 md5 不同（畫面確實在動） | 100.2% | **101.7%** |
| 數字雨關閉、照片留著 | 截圖靜止、貓可見 | 2.0% | **2.4%** |

量測方法：`/proc/<pid>/stat` 的 utime+stime，對 app 進程與其子進程（WebKitWebProcess、WebKitNetworkProcess）取 30 秒差值；每次都確認視窗 pid 的 `/proc/pid/exe` 指向剛 build 出來的 binary。成本來源是 56 個各自動畫、帶雙層 text-shadow 發光的 column，在軟體合成路徑上逐幀重新光柵化。

契約：

1. `state.ui.motion` 預設 `false`。讀舊的 localStorage 時用 `ui.motion === true`（不是 `!== false`），否則所有既有安裝都會在升級後自動把雨打開。
2. 沒有任何自動降級。曾經有一版會量幀距、自己把雨關掉，並把結果寫進 localStorage；它會在使用者沒要求的情況下改變背景，而且一旦降級就永久記住。已整個移除，`autoTier` / `autoReason` / `calibrateBackdrop` / `measureFramePacing` 都不存在了。
3. 56 個 column 只在雨真的要畫時才建（`applyBackdrop()` 裡，在 `document.body` 判斷之前）。
4. 設定頁那句話兩種狀態都要講出成本，字串含「CPU」；關閉時另含「預設關閉」。`frontend/test/backdrop-switches.mjs` 逐字斷言。
5. `index.html` 的 `#toggle-motion` 不得帶 `checked`（`TestFrontendMotionToggleStartsUnchecked`）。
6. `style.css` 不得有任何 `backdrop-filter:`（`TestFrontendDoesNotBlurOverMovingPixels`）。注意：毛玻璃**沒有**被單獨量過，這條靠推論成立，不要對外宣稱它有數字。


## 10. 列動作只剩兩顆：MCP 入口已移除（2026-09-15，owner 指定）

`收件匣 ｜ MCP 設定 ｜ resume` 改成 `收件匣 ｜ resume`。

為什麼移除，而不是留著：MCP 那四個工具（`agent_list`、`agent_status`、`agent_send`、`agent_inbox`）在 `ah` 都有等價指令（`ah list`、`ah status`、`ah send`、`ah inbox`），而 `agenthub-watch` skill 本來就是走 `ah` 而不是 MCP。所以沒有設定 MCP 的新使用者，功能上不缺任何東西。一個 owner 可能一輩子用一次的設定，不該出現在一千列的每一列上。

保留了什麼：`MCPConfig` 綁定、`openMCPConfig()`、`mcp-modal` 的 markup 與全部文案。放回入口只要在 `renderRows` 加一行 `rowActionButton`。

契約：

1. 列動作是兩顆，`frontend/test/mcp-config.mjs` §1 斷言 `mcp` class 的按鈕數為 **0**、`inbox` 與 `resume` 各為 2。放回按鈕會讓測試失敗，所以那是個明確的決定而不是意外。
2. `openMCPConfig(sessionId)` 產生的設定必須帶**傳進去的那個** session。這條保護不能跟著入口一起拿掉：綁錯 session 之後從任何一側都看不出來（server 起得來、四個工具都回答，只是回答別人的 session）。2026-09-10 就發生過把另一台的 session id 手貼進設定。
3. `col.c-actions` 寬度下限從 250px 降到 **160px**，實際寬度 176px。
   154px 是**算**出來的不是量出來的：沿用三顆按鈕那次量到的單顆寬度（收件匣 63、resume 67），
   加一個 4px gap 與兩側各 10px padding。兩顆的版面沒有重新量過，所以這個數字只保證算術正確。
   `TestFrontendKeepsTheRowActionsReachable` 守這條。
4. `ah` **沒有**產生 `.mcp.json` 的指令，所以移除入口之後，UI 上不再有任何地方拿得到那份設定。要恢復可得性，選項是放回按鈕、移到設定頁、或補一個 `ah mcp-config <session>`。


## 11. 雙語：zh-Hant 與 en，非 zh locale 預設英文

視窗的每一句話都住在 `frontend/src/i18n/{zh-Hant,en}.js`，平面表格、dotted key、`{named}` 佔位符，沒有任何運算式或樣板字串（Go 端用 regex 讀它們）。`t(key, params)` 找不到就回傳 key 本身；`plural(n, key)` 讀 `<key>.one` / `<key>.other`。

語言的決定順序：存下來的覆寫 → `navigator.language` 開頭是 `zh` 就 zh-Hant → 其餘一律 en。覆寫存在既有的 `UI_PREFS_KEY` localStorage 物件裡（`state.ui.lang`），**不**經過節點：節點的設定是啟動組態（§7.8 規則 1），視窗的顯示語言不是。設定 → 外觀的 `<select id="settings-lang">` 改語言，`setUILanguage()` 會存檔並就地 `paintStatic()` + `render()`，不重新載入——重載會丟掉配對抽屜的狀態、篩選條件與打到一半的欄位。

`index.html` 只帶 key，不帶句子：`data-t`（textContent）、`data-t-placeholder`、`data-t-title`，由 `paintStatic()` 在 boot 與每次換語言時寫入。**帶 `data-t` 的元素不能有子元素**——`paintStatic` 寫的是 `textContent`，會把它們刪掉——所以句子裡有 `<code>` 或 `<b>` 時，要拆成一段文字一個 key。

契約（`TestFrontendKeepsItsWordsInOneTable` 逐條守）：

1. `frontend/src/` 底下、`src/i18n/` 以外的 `.js`，字串字面值裡不得出現漢字（註解先被剝掉：這個 codebase 的註解是刻意的中英混寫）。
2. `index.html` 完全不得出現漢字，註解也算——它原本的中文段落標記在這次一起翻掉了，所以這條可以是絕對的。
3. 兩張表的 key 必須一一對應；`en.js` 的值不得含漢字；同一個 key 兩邊值相同且含字母時視為漏翻。
4. `index.html` 裡每一個 `data-t*` 指到的 key，兩張表都要有。
5. 表格裡不得出現運算式（`${`）。名字（`Claude`、`Codex`、`active`）與純分隔符刻意不進表格：兩種語言一樣，放進去只會變成必須手動保持同步的重複。

`frontend/test/` 底下除了 `i18n.mjs`，全部跑在 zh-TW（`dom-shim.mjs` 的 `useLocale`），因為那些斷言是用中文寫的；英文那一半由 `i18n.mjs` 以 en-US 開機覆蓋，`TestFrontendSpeaksBothLanguages` 帶它跑。shim 的 `querySelectorAll("[data-t]")` 直接從 `index.html` 解出真的元素，並且 `i18n.mjs` 會把 shim 給的數量跟檔案裡的數量對起來——它以前回傳 `[]`，那會讓整段靜態文案的斷言全部落空。

不進表格的還有一種：**節點說的話**。`nextStep`、`notice` 與節點的拒絕原文是資料，原樣顯示；拿節點的散文當 key，節點改一次措辭就靜靜對不上了。`desktop/nodeprocess.go` 那四句原本是中文的輸出已經直接改寫成英文——它們跟 `ah` 自己的輸出並排進同一個 `<pre>`，而那邊本來就是英文；四句話不值得一層 Go 的 i18n。
