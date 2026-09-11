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
- PairingState.availability 三種：`off`（節點沒開 `-discover`）、`on`、其他
  （讀不到）。三種文案不能合併（測試 `render-hostile-candidate.mjs`）。
- InboxView 有 loading、error、cleared、full、more 五個獨立狀態，加上空清單。
  loading 和空清單**不能長一樣**。

## 2. 後端綁定 → UI 入口（18 個，每個都要有）

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
| `OpenPairing(0)` | `btn-pairing-on` | **一定傳 0**（用節點預設時長） |
| `ClosePairing()` | `btn-pairing-off` | |
| `Inbox(sessionId)` | 每列的「收件匣」 | 開對話框，先畫 loading；序號守衛；清空按鈕只在答案回來後才對準這個 session |
| `ClearInbox(sessionId)` | 收件匣「清空收件匣…」 | `confirm` 後執行；結果（移除 N 則 / 失敗未變動）顯示在**對話框內**，不是 banner |
| `ServiceStatus()` | 每次 Overview 後 | 五種狀態文案（找不到 ah / 不支援 / 已裝執行中 / 已裝未執行 / 節點在跑但非服務 / 都沒有） |
| `InstallService(form)` | 服務表單「安裝為背景服務」 | 顯示 `$ command` + output；失敗把錯誤放進 output 區 |
| `UninstallService()` | `service-uninstall` | confirm；成功後 reload |
| `LocalAddresses()` | 開啟服務表單時 | 重建位址下拉；非私有網段自動帶入 `treatAsPrivate` 建議並說明 |
| `NodeURL()` / `SetNodeURL()` | 目前**沒有** UI 入口（靠 `AGENTHUB_URL`） | 保留為未接綁定 |

## 3. 畫面與元件清單（現況）

### 3.1 全域

- 標題列：連線點（ok/bad）、節點名稱 · 平台 · URL；視圖切換（本機／區網）；
  三個按鈕：預覽 heartbeat、重新掃描、重新整理。標題列是 Wails 拖曳區。
- Banner：一則，錯誤或成功（ok），成功會自動消失。
- 狀態列：左「顯示 N / total 個 session · 所有已配對 N · 指定節點 N · 不公開 N」，右本機節點 ID。
- `busy` 狀態：任何寫入進行中，所有寫入按鈕 disabled。

### 3.2 本機視圖

- 搜尋框：比對 `id` 與 `cwd`，大小寫不敏感。
- 8 個篩選 chip，三組（provider、status、audience），組內單選切換、組間 AND，每個 chip 帶全域計數。
- 選取列：全選目前篩選結果（含 indeterminate）、已選取 N 個、「設定公開對象…」「收回選取」。
- 表格 8 欄：勾選、SESSION（含收件匣按鈕）、PROVIDER、狀態、管理、公開對象、工作目錄、最後活動。
- 空狀態：「沒有符合條件的 session。」
- **無排序。**

### 3.3 區網視圖

左欄（`nodelist`）：
- 已配對節點列表：每列 presence 點 + 名稱 + presence 文字 + 平台 · 最後聯繫。空：「尚未配對任何節點。」
- 配對模式面板：headline、倒數（獨立元素，每秒只改這一個）、detail、開啟／停止按鈕、note（含 `broadcastWarning`，把本機名稱和它的來源說出來）。
- 正在廣播的機器：`candidate-full` 警告（在捲動區**外面**）、候選列（名稱、爭用/重複 pill、平台 · 位址、完整 nodeId、完整指紋、首次/最後看到、「用這一列開始配對…」）、`candidate-notice`（節點自己的免責文字）。
- 「配對新節點…」按鈕與說明。

右欄（`nodedetail`）：
- 節點詳情：名稱、完整指紋、核對說明、節點 ID／平台／配對時間／最後聯繫／可見的 session 數、「撤銷信任」+ 說明。
- 「這個節點公開給我的 session」：四種 presence 狀態 + `sessionsWithheld` + 空 + 表格（SESSION／節點／PROVIDER／狀態／最後活動）。

### 3.4 背景服務面板（目前嵌在主流程）

狀態行、重新讀取、安裝／重新安裝、移除；展開表單：資料庫路徑、對外位址下拉、允許區網、`-discover`、視為私有網段、自動喚醒；安裝說明；輸出區 `<pre>`。節點沒在跑且未安裝時表單自動展開一次。

### 3.5 對話框（4 個）

- 收件匣：警語（資料不是指令；「自稱」後是寄件者自選）、meta、訊息列（寄件者分兩半：驗證過的 node id 用 `fingerprint` 樣式，自選的 session 用 `claimed` 樣式，中間「自稱」）、清空。
- 配對新節點：說明（`ah node`、指紋逐組相符）、五個欄位、prefill note、本機指紋、送出。
- 設定公開對象：套用到 N 個；三種 mode radio；指定節點的 ID 輸入；四個旗標；套用。**每次開啟四個旗標一律重設為 off**（測試 `audience-dialog.mjs`）。
- Heartbeat 預覽：說明 + `<pre>`。

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
| availability=off | 「-discover」「沒有在看」；開啟按鈕 disabled | 「機器在廣播。」 |
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
- 模組只能註冊**兩個** `setInterval`：5 秒 pairing 輪詢、1 秒倒數。
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

另一個 session 在 `feat/112-copy-mcp-config` 分支上加了一個功能，重新設計實作時必須納入，
否則就是缺功能：

| 綁定 | 入口 | 行為 |
|---|---|---|
| `MCPConfig(sessionId)` | 每列的「MCP 設定」按鈕（目前放在 SESSION 欄，與「收件匣」並排） | 開 `mcp-modal`：`<pre id="mcp-text">` 顯示這一列的 `.mcp.json` 片段、`mcp-status` 顯示複製結果；文案說明 per-project 與 `--outbound` 的限制 |

- `MODAL_IDS` 多了 `mcp-modal`；新設計若把對話框改成抽屜，這個一起改。
- 設計稿的對應：Main 與 MainHacker 的「收件匣」動作欄應擴成兩個圖示（收件匣、MCP 設定），
  或改成一個「⋯」列選單。**目前設計稿尚未畫入，實作前補。**
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

新 UI 的「送出紀錄」「喚醒紀錄」視圖接這兩個端點。desktop 端還沒有對應的 Go 綁定，
實作時要在 `app.go` / `client.go` 加 `Outbound(limit, after)` 與 `Wakes(session, limit)`。

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

**#116 設定端點（尚未合併，先照此設計）**：一個 `GET /v1/node/settings` 回各欄位＋來源，
一個 `PUT` 部分更新回 `restartRequired: true`；欄位 `peerListen`、`allowLan`、`discover`、
`treatAsPrivate[]`、`autoWake`。設定頁的主按鈕由「重新安裝（改旗標）」改為「儲存並重啟服務」。

## 8. 新需求（2026-09-11，owner 指定）

**列動作「複製 resume 指令」。** 依 provider 產生指令並寫入剪貼簿：

| provider | 指令 | 依據 |
|---|---|---|
| claude | `claude --resume <providerSessionId>` | `adapter/claude.go` 讀 jsonl 的 `sessionId`，與 `claude --resume` 接受的 id 相同（已用本機檔案核對） |
| codex | `codex resume <providerSessionId>` | thread id |

- 剪貼簿寫入用 #112 的 `CopyText` 綁定，同樣受序號守衛：遲到的回應不得寫剪貼簿。
- 複製後的回饋要帶工作目錄提示：「在 <cwd> 執行」，cwd 為空則省略。
- 與「收件匣」「MCP 設定」並排為三個列動作；設計稿與實作都要有。

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
- 九個 node 測試由 Go 測試以 `node frontend/test/<file>.mjs` 執行，路徑與檔名不可改。

若新設計把 modal 改成抽屜，`.modal-card .warning` 這條選擇器仍要存在（可以是共用規則），
或同步修改 `frontend_test.go` 並在 PR 說明寫出理由。

### 7.6 #116 設定端點（#129 已合併，main `f35b327`；對方轉述，實作前對照 internal/api 原始碼）

- `GET /v1/node/settings` → `{settings:{peerListen, allowLan, discover, treatAsPrivate[], autoWake}, sources:{欄位→"flag"|"remembered"|"default"}, saved:{同 settings 形狀，DB 值}, restartRequired}`。
- `PUT /v1/node/settings` 收任意子集；`treatAsPrivate` 一律陣列，`[]` 代表撤回。回同形狀加 `message`，`restartRequired: true`（設定只在啟動時生效）。
  錯誤：`400 INVALID_REQUEST`（帶 node 的啟動訊息）、`409 SETTINGS_UNAVAILABLE`、`500 REGISTRY_ERROR`。
- UI 規則：(a) `PUT {allowLan:false}` 會同時把 `peerListen` 收回 `127.0.0.1:7463`，提交後**用回應刷新所有欄位**；
  (b) `sources.peerListen === "default"` 不代表「尚未設定」（撤回後是 default，下次啟動變 remembered）。
- `--listen` 不進設定，UI 不給欄位。重啟用 `ah service restart`，尚無 API；需要時開 issue。
- 設定頁改法：主按鈕「重新安裝（改旗標）」→「儲存並重啟服務」；desktop 端要加 `NodeSettings()` / `SaveNodeSettings()` 綁定與 `ah service restart` 的呼叫。
- `GET /v1/outbound?session=` 對方正在加；到位後 `loadOutbound` 的客端過濾與自動往前讀可以拿掉。

以上兩項是 PR #131 之後的接續 PR，不併入 #131。

### 7.7 `GET /v1/outbound?session=`（#132 已合併，main `c3234e7`）

- `?session=<id>&limit=1..200&after=<cursor>`；`session` 收 `codex:abc` 或 `<本機節點>/codex:abc`，都正規化成本機 session。
- 回應結構不變（`messages`／`next`，最新在前、無 body）；`next` 只在滿頁出現；**續頁要同時帶 `session` 與 `after`**。
- 錯誤：非本機節點位址 → `404 UNKNOWN_NODE`；格式錯 → `400 INVALID_REQUEST`；本機但沒送過 → 空清單，不是錯。
- 合併後接續 PR 要做：desktop `client.outbound` 加 `session` 參數、`App.Outbound(session, limit, after)`；`loadOutbound` 拿掉客端 `fromSession` 過濾與自動往前讀，空清單直接顯示「這個 session 還沒有送出過訊息」。`test/inbox-drawer.mjs` 第 1–3 段改成驗證參數有帶上、續頁帶 session。
- 邊角（#127 待修）：`session` 只給空白會被 trim 成空、退回全節點清單（`/v1/wakes` 同）；前端送參數前自己 trim，空就不要帶。
