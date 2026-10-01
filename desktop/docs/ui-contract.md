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
| `Overview()` | 啟動、`btn-reload`、每次寫入後、待處理列「節點沒有回應」的「重試」、首次設定精靈第 1 步結束時（§3.2） | 更新 session/nodes/peers/counts、連線指示、footer；清掉已不存在的選取；讀不到節點時在**待處理列**（§3.1）說明（保留上次清單或從未載入過），不再用 toast |
| `Discover()` | `btn-discover`；首次設定精靈第 3 步沒有 session 時的「重新掃描」（同一個 `discoverSessions()`）；視窗第一次**連得到節點**而且 0 筆 session 時自動呼叫一次（`state.busy` 時不算，留給下一次不忙的讀取） | 掃描後 reload；toast 報 claude/codex/total/skipped 數 |
| `Heartbeat()` | `btn-heartbeat`（在設定頁的身分區 `settings-identity`） | 對話框顯示已簽章 envelope 純文字 |
| `SetAudience(ids, audience)` | 公開對象對話框「套用」；行內公開選單（列上的公開對象按鈕、批次列「公開 ▾」，§3.2）的三個選項與它 toast 上的「復原」；`btn-unpublish`；首次設定精靈第 3 步「分享 N 個 session」（走選單同一個 `applyAudienceChoice()`，帶 `withoutCwd`：一律 `exportCwd: false`，復原仍寫回原值） | 成功：清空選取（批次列與對話框；列上的選單不動選取）、關對話框、成功 toast；部分失敗：留著選取、錯誤 toast 顯示第一個錯誤（對話框開著時浮在對話框上方，§3.1）；行內選單與 `btn-unpublish` 的部分失敗 toast 也帶「復原」（至少一個成功時；寫回每個 session 的原值，對失敗的那些是同值重寫、冪等），全部失敗時不帶。選單與復原**依結果分組**：每個不同的 audience 呼叫一次，帶所有得到它的 session（`writeAudiences()`）；整次呼叫丟錯時該組全部算失敗，走同一條部分失敗規則 |
| `SetVisibility(ids, visibility)` | 目前**沒有** UI 入口 | 保留為未接綁定；不算缺功能 |
| `TrustNode(id, name, platform, key, fingerprint)` | 配對對話框「指紋一致，信任此節點」 | 關對話框、選中新節點、reload、警告 toast（「對方那台也要做一次」，不自動消失） |
| `RevokeNode(id)` | 節點詳情「撤銷信任」 | confirm 後執行；toast 說明同時移除授權 |
| `Pairing()` | 進入區網視圖時、每 5 秒（區網視圖，**或首次設定精靈第 2 步在畫面上**〔`firstRunPairingActive()`〕）、倒數歸零時（同樣兩種情況）、進入精靈第 2 步時 | 序號守衛：慢的回覆不能覆蓋快的 |
| `OpenPairing(0)` | 開啟配對抽屜時自動呼叫（傳 0；抽屜在節點回答前已被關掉就不呼叫；呼叫還在飛時抽屜被關掉，回來後照關抽屜的規則再判斷一次要不要 `ClosePairing`）；抽屜第 1 步的修復鈕存檔重啟節點後，抽屜仍開著就重讀並再開一次、`btn-pairing-on`（標籤「與另一台機器配對」）；**進入首次設定精靈第 2 步時**（同一個 `openPairingWindowIfNeeded()`，規則與抽屜相同：「抽屜開著」換成 `pairingWanted()`＝抽屜開著或精靈第 2 步在畫面上），以及第 2 步還在畫面上時視窗到期後（`keepFirstRunWindowOpen()`：每個第 2 步見過開著的視窗只重開一次，節點拒絕時不會每 5 秒報一次錯） | **一定傳 0**（用節點預設時長）；**不論有沒有 -discover 都可按**，節點已不再拒絕開不了廣播的視窗 |
| `ClosePairing()` | `btn-pairing-off`；關掉配對抽屜時；離開首次設定精靈第 2 步時（下一步、上一步、先跳過、稍後再設定、`goTo*()` 讓開；`releasePairingWindow()`，關抽屜走的也是它） | 關抽屜時先重讀 `PairRequests`，還有 pending／awaiting-confirm、讀取失敗或抽屜又被打開（或精靈又回到第 2 步）就**不關**（節點關視窗會作廢所有未決請求）。關掉 app 視窗時不處理（抽屜也沒有），留給節點自己的時限 |
| `PairRequests(all)` | 開啟配對抽屜時、抽屜開著時每 2 秒、每次決定之後；首次設定精靈第 2 步：進入時、在畫面上時每 2 秒（同一個 interval，`state.busy` 時同樣跳過）、離開時的重讀；第 2 步畫過的未決請求不是因為這個視窗的決定而從 `all=false` 的清單消失時，讀**一次** `PairRequests(true)` 找出它怎麼結束的（`readFirstRunPairEnding()`） | 序號守衛；抽屜開著時照舊（節點會替每個 pending outgoing 去對端輪詢）；`all` 由「顯示已結束」勾選框決定。**唯一的例外**（2026-09-29，待處理列）：15 秒背景 tick 在 `load()` 之後讀**一次** `PairRequests(false)`，而且只在：節點最後一次 `Pairing()` 回報配對開放中且倒數未歸零、配對抽屜**沒開**（開著時交給它自己的 2 秒輪詢）、tick 本身沒被 `interactionInProgress()` 擋下。收件的 pending 只可能在開放中的配對上等，所以關著時不讀；不新增 `setInterval`（`refreshIncomingPairRequests()`，測試 `notifications.mjs` §3d） |
| `StartPairRequest(address)` | 候選列的「送出配對請求」、位址表單的「送出配對請求」；首次設定精靈第 2 步的候選列與「找不到另一台？」的位址欄（都走同一個 `sendPairRequest()`） | 只送位址，不送金鑰/指紋/節點 ID；失敗走錯誤 toast，錯誤碼翻成中文（§4.3） |
| `ApprovePairRequest(id)` | 收到的請求列「指紋一致，核准」；首次設定精靈第 2 步比對畫面的「一樣，核准」（同一個 `decidePairRequest()`） | id 來自該列本身，不是欄位；成功後重讀請求清單與 `Overview()` |
| `ConfirmPairRequest(id)` | 送出的請求列（`awaiting-confirm`）「指紋一致，確認」；首次設定精靈第 2 步的「一樣，完成配對」 | 同上 |
| `RejectPairRequest(id)` | 任一未決請求列「拒絕」；首次設定精靈第 2 步等待畫面的「取消這次請求」與比對畫面的「不一樣，取消」 | 成功後把節點回的 `nextStep` 放進 toast——推不出去的拒絕只有這裡會說 |
| `Inbox(sessionId)` | 每列的「收件匣」（動作欄） | 開**抽屜**（`inbox-modal`，三個分頁：收件匣／送出紀錄／喚醒紀錄），先畫 loading；序號守衛；清空按鈕只在答案回來後才對準這個 session |
| `ClearInbox(sessionId)` | 收件匣「清空收件匣…」 | `askConfirm` 按「清空」後執行；結果（移除 N 則 / 失敗未變動）顯示在**對話框內**，不是 toast |
| `ServiceStatus()` | 每次 Overview 後 | 五種狀態文案（找不到 ah / 不支援 / 已裝執行中 / 已裝未執行 / 節點在跑但非服務 / 都沒有） |
| `InstallService(form)` | 服務表單「安裝為背景服務」；待處理列服務那一列與標題列 `service-pill` 的一鍵處理（支援但未安裝時，§3.1）；首次設定精靈第 1 步的一鍵（**走同一個一鍵處理** `runServiceQuickAction()`，§3.2） | 顯示 `$ command` + output；失敗把錯誤放進 output 區。一鍵處理**動手前先重讀** `ServiceStatus()`（`loadService()`，用讀到的狀態重算；畫面上的可能是好幾分鐘前的——有選取或對話框開著時 15 秒 tick 不讀——而 `ah service install` 會取代既有註冊，照舊狀態裝等於用預設資料庫換掉節點身分），重讀失敗就跳設定頁並用錯誤 toast 說明（`service.quickReadFailed`），什麼都不做；**節點在跑但不是服務**（`state.nodeReachable` 或狀態的 `nodeAnswering`，且未安裝）不直接裝，改去設定頁的服務表單（`goToService()` 會展開它）。其餘走**同一個** `installService()`：先 `openServiceForm()`（未安裝時資料庫欄位一定是空的＝節點預設），再照表單按鈕的路徑安裝，含它既有的 `askConfirm`（已安裝但讀不到／改了資料庫路徑，未安裝時兩者都不會問）。服務表單在「節點在跑但不是服務」時資料庫說明改為 `service.dbNoteRunningNotService`（它若是用 --db 啟動的就填那個路徑，這時留空＝預設資料庫＝對它而言是新身分、現有配對失效——後半句也在這個條件裡，用預設資料庫跑的節點留空不會換身分；測試 `service-panel.mjs`），欄位空白按安裝先 `askConfirm`（`service.runningNotServiceConfirm*`，danger、焦點在取消；有填路徑不問；測試 `inline-publish.mjs` §6） |
| `UninstallService()` | `service-uninstall` | `askConfirm`；成功後 reload |
| `LocalAddresses()` | 開啟服務表單時；首次設定精靈出現或重新打開時（`readForFirstRun()`，不需要節點在跑） | 重建位址下拉；非私有網段自動帶入 `treatAsPrivate` 建議並說明。精靈只列 `private` 的位址給使用者選 |
| `NodeURL()` / `SetNodeURL()` | 目前**沒有** UI 入口（靠 `AGENTHUB_URL`） | 保留為未接綁定 |

| `Outbound(session, limit, after)` | 收件匣抽屜的「送出紀錄」分頁 | 分頁載入；序號守衛；`next` 為空表示沒有更多 |
| `Wakes(session, limit)` | 收件匣抽屜的「喚醒紀錄」分頁 | 分頁載入；序號守衛；`limits` 顯示節點端的上限 |
| `NodeSettings()` | 進入設定頁的節點設定區 | 讀回 `settings`（執行中）與 `saved`（存下來的）兩份；規則見 §7.8 |
| `SaveNodeSettings(patch)` | 節點設定區的「儲存」；配對抽屜第 1 步與首次設定精靈第 1 步的修復鈕（都走 `applyPeerListenRepairFromCard()` → 表單的 `saveNodeSettings()`，§7.8） | patch 併到 **`saved`** 不是 `settings`；存完重讀並比對「有沒有真的寫進去」；沒生效要說出來 |
| `RestartService()` | 節點設定區的「重新啟動服務」；一鍵處理「已安裝但沒在跑」、首次設定精靈第 1 步遇到已安裝的服務而節點沒回應（`restartNode({ service: true })`：直接呼叫這個綁定，不經 `RestartNode` 在 Go 端再判一次，其餘——等節點回答、degraded 的說法——與設定頁同一份） | 走 `ah service restart`；三平台都支援——macOS `launchctl kickstart -k`、Linux `systemctl --user restart`、Windows `taskkill` 掉 node 再 `schtasks /Run` 排程工作 |
| `RestartNode()` | 服務區的「重新啟動／啟動」（`service-restart`）、首次設定精靈第 1 步在沒有服務管理員（不支援、找不到 ah）而節點沒回應時、節點設定儲存後 | 已安裝服務→`RestartService()`；否則 app 自己停掉再啟動節點（`desktop/nodeprocess.go`）。**帶著原本的資料庫重啟（#205）**：節點 API 不回報資料庫路徑，所以 Go 端讀正在跑的 agenthub-node 自己的命令列與環境變數（macOS `kern.procargs2`；Linux `/proc/<pid>/cmdline`、`/proc/<pid>/environ`；Windows 命令列走 WMI——PowerShell 用 `%SystemRoot%` 下的絕對路徑、只讀 stdout、關掉進度記錄——環境變數讀程序的 PEB，使用者比對程序 token 的 SID），命令列照節點的旗標表解析、Windows 照 Go `os.Args` 的切法（不是 `CommandLineToArgvW`），用同一組參數啟動新的——只拿掉節點記住的五個設定（它們正是這次重啟要套用的存檔值，帶回舊旗標會蓋掉存檔）。命令列沒寫的路徑（`--db`、`--claude-root`、`--codex-root`）是節點**用它自己的環境**算出來的（`defaultPaths`：macOS `HOME`、Linux `XDG_CONFIG_HOME` 再 `HOME`、Windows `APPDATA`／`USERPROFILE`），app 用節點的環境照同樣規則算（`desktop/nodeenv.go`），**明確**以 `--db <絕對路徑>` 等傳給新節點，不讓新節點用 app 的環境重算。**停之前**就判斷，下列情況拒絕且不碰節點：讀不到命令列、同時有兩個以上 agenthub-node、路徑旗標是相對路徑或由環境算出相對路徑、資料庫檔不存在、旗標表不認得的旗標、`-listen` 不是 app 的位址（`127.0.0.1:7462`）、有路徑沒寫而節點環境讀不到（macOS／Linux 一律拒絕；Windows 見下）、節點在回答但 `GET /v1/node` 讀不到身分、節點沒回答而資料庫只是 app 推斷的。停了之後等該 pid 從程序清單消失（殭屍程序不算在跑）才啟動，逾時就回錯誤不啟動——埠關了不代表程序已放開資料庫。啟動後比對 node id 與指紋，不同就回錯誤（兩個 id＋把舊節點帶回來的命令），不宣稱成功。復原命令分兩種：路徑都是從節點自己的命令列或環境讀來的，命令裡每個路徑都明確寫出，在哪個環境執行都指向同一個資料庫；有路徑是 app 用自己的環境**推算**的（Windows 讀不到 PEB 的 fallback），復原命令只給舊節點原本的命令列、不帶推算的路徑（新節點正是開在那些路徑上才變成別人），並寫明哪些旗標是推算的、要在舊節點原本啟動的環境（它的登入或啟動它的 shell）裡執行（測試 `TestRestartNodeDoesNotHandBackAnInferredPathAsTheWayBack`）；輸出與錯誤裡的命令（`commandLine`，`desktop/nodeargs.go`）照平台的 shell 加引號：Windows 是 PowerShell 形式（`& '…\agenthub-node.exe' --db '…'`，單引號內 `$`、`` ` `` 都是字面，`'` 與 PowerShell 視同單引號的彎引號重複一次；只有旗標名稱（`--db`、`-discover`）不加引號，其餘一律加——5.1 會把沒引號的 `-a.b` 在點切開，`--`、`--%` 是它自己的語法），macOS／Linux 是 sh／bash／zsh 的單引號形式；cmd.exe、fish 不在保證內。Windows PowerShell 5.1 傳給程式時會丟掉空字串參數、不跳脫參數內的 `"`（Windows 路徑兩者都不會有；PowerShell 7.3 起沒有這問題）。測試 `TestShellCommandLineQuotesForEachShell`、`TestCommandLineSurvivesTheShell`（把命令交給本機每個 `/bin` shell、Windows 上交給 Windows PowerShell 5.1，執行測試自己的 binary 比對收到的參數）；節點重啟前沒回答（沒有 id 可比）時照樣啟動，但輸出寫明沒有比對、用的是哪個資料庫、從哪裡讀來。沒有任何 agenthub-node 在跑＝首次啟動，照舊不帶參數。**保證範圍**：macOS／Linux 上，新節點開的資料庫路徑就是舊節點命令列或它自己環境所指的那一個（檔案須存在），加上事後 id／指紋比對。**Windows 殘餘風險**：讀 PEB 失敗（權限、位元數不同等）而有路徑沒寫時，改用 app 自己的 `APPDATA`／`USERPROFILE` 算出的路徑，條件是資料庫檔存在、節點程序 token 的使用者 SID 與 app 相同、且節點重啟前有回答身分（事後比對 id）——同一使用者但節點啟動時 `APPDATA` 被改過、而 app 那邊的預設資料庫恰好也存在時，新節點會開 app 那一個，這時只靠事後 id 比對發現（回錯誤＋復原命令，不刪任何東西），不是事前擋下。Windows 這一半（WMI、PEB、token）只在 CI 的 `desktop-windows-processes` job 實際執行，開發機上只編譯；macOS 這一半（`kern.procargs2`：環境後面直接接核心自己的 apple strings、中間不保證有空項目，讀到 buffer 結尾就是字串區結束；apple strings 留在環境清單尾端，查找取第一筆所以蓋不掉真的變數）另在 CI 的 `desktop-macos-processes` job 跑，真程序測試 `-count=20`。兩個 job 都要求清單裡每個測試在 log 有 `--- PASS:`，且只為該 OS 編譯的測試檔裡每個 `Test*` 都在清單上。列舉遇到 `EINVAL`／`ESRCH`（有個 agenthub-node 正在退出、還不是殭屍）時重試幾次（6×50ms）再決定，**不當成它已消失**，持續失敗就拒絕。前端**不另外問**：Go 端在上述範圍內確認資料庫才動手、確認不了就拒絕，所以 `nodeRunningNotAService` 時按重新啟動也不 `askConfirm`；拒絕的原文照 `restartNode()` 的失敗路徑放進 `service-output`，不出「已重新啟動」（測試 `service-panel.mjs`） |
| `SetNodeAddresses(id, addresses)` | 節點詳情的位址清單（首選＋備援，每列可改可移除，「新增備援」最多到 4 列）、「記錄位址」 | **整組取代**（`PUT /v1/nodes/{id}/addresses`）：移除的位址就從節點上消失，打錯的備援刪得掉；送出前 trim、丟掉空列，全空不送（錯誤 toast `network.addressEmpty`）；清單是草稿，`interactionInProgress()` 期間不被背景重畫蓋掉，增刪列保留其他列已打的字。舊節點（該路由 404/405、且不是節點自己的 JSON 錯誤）由綁定改走單一位址端點只記第一個，回 `olderNode: true`，視窗用 `network.addressSavedOlderNode` 說明備援在舊節點上無法編輯，不當成功顯示。測試：`app_test.go` 的 `TestSetNodeAddresses*`、`frontend/test/node-addresses.mjs` |
| `SetNodeAddress(...)` | 目前**沒有** UI 入口（節點詳情改用 `SetNodeAddresses`；它會把被取代的首選留成備援） | 保留為未接綁定 |
| `HostPlatform()` | 啟動一次 | 回 `runtime.GOOS`；`darwin` 時加 `body.mac` 讓標題列留出視窗按鈕的位置。問的是**主機**不是節點，兩者是不同的事實，而節點的那份正好在連不上時缺席 |
| `CopyText(text)` | MCP 設定、指紋、列上的「複製 ID」與工作目錄等所有「複製」 | 寫入剪貼簿；結果顯示在原地，不是 toast。列上的兩個複製（2026-10-01，擁有者指定；原本列上的 resume 沒有地方放結果而走 toast）：成功時按下的那個控制項本身顯示「已複製 ✓」1.5 秒（`flashCopied`，狀態存在列上，15 秒 tick 與切語言重畫時照樣保留）；失敗時在它旁邊開 `#copy-fallback`（`openCopyFallback`，與公開對象選單同一套定位，Esc、點外面、捲動、縮放關閉；Tab 只在欄位與「關閉」之間移動，移出框外就關閉並把焦點還給按鈕，框沒開時不攔任何 Tab），說出原因並把要複製的文字放在已選取的唯讀欄位裡讓使用者手動複製。兩者都不出 toast。序號守衛照舊：連按兩列時剪貼簿留最後一次，也只有最後一次回報（測試 `frontend/test/row-copy.mjs`） |

## 3. 畫面與元件清單（現況，2026-09-15 對照 main 的 index.html 與 src/ 重寫）

> 這一節在改版期間停在改版前的狀態，寫著「無排序」「表格 8 欄」「chip 帶全域計數」「安裝表單六個欄位」，
> 全部與程式不符，而且 §7.6 自己就寫著安裝表單只剩資料庫路徑。以下每一條都對照過原始碼。

> **2026-09-23 更新（分支 `feat/desktop-ux-simplify`）**：以下清單的畫面沒有增減，但有六處改了形狀——
> 面板用滿視窗寬度、按鈕列換行不裁切；配對收成一個三步抽屜（能不能被連到／對方在哪裡／比對指紋），
> 區網左欄只留主按鈕與一行狀態；本機表格去掉 MANAGED 欄、FLAGS 只在已公開的列顯示、Refresh 離開標題列；
> 公開對象對話框先問情境（三個 preset），四個旗標收進「進階」；背景服務與節點設定不再互相覆蓋
> （衝突在節點設定按儲存時問一次）；首次啟動清單剩三步。文案每個狀態只留一句，原本的解釋搬到
> `docs/desktop-window.md`，視窗裡用 tooltip 或 `<details>` 指過去；用詞見 §12。

### 3.1 全域

- 標題列：連線點（ok/bad）、`node-line`（節點名稱 · 平台 · URL）；三個分頁 `本機 session` / `區網` / `設定`
  （前兩個帶計數）。分頁是 `<nav id="view-switch" role="tablist">` 裡的 `<button role="tab" data-view>`（2026-09-29，
  原本是 `<span>`，鍵盤到不了）：`render()` 寫 `tab on`／`aria-selected`／`tabindex`（選中的 0、其餘 -1），
  左右方向鍵與 Home/End 移到相鄰分頁並切換（`viewSwitchKey`）；程式以 `#view-switch [data-view]` 找它們，
  `dom-shim.mjs` 從 index.html 解出這三個節點（測試 `inline-publish.mjs` §8）。
  右側三個控制項：**服務狀態 pill**（`service-pill`，**一鍵處理**：支援但未安裝且沒有節點在跑 → 安裝（節點在跑但不是服務 → 服務表單）、已安裝沒在跑 → `RestartService()`、
  其餘〔找不到 ah、平台不支援、狀態還沒讀到、已在跑〕→ 跳設定頁的服務區；`runServiceQuickAction()`，與待處理列同一份。
  綠色「背景服務執行中」只在服務已裝、在跑**而且節點有回答**時；服務在跑但節點沒回應（`state.nodeChecked` 且 `!state.nodeReachable`）
  是 amber 的 `service.pillRunningNoAnswer`「背景服務在跑，但節點沒回應」，按下同一個一鍵處理——它重讀狀態，已在跑就到設定頁的服務區，
  那裡有「重新啟動」；測試 `first-run-pairing.mjs` §14）、
  重新掃描（`btn-discover`）、「繼續設定」（`btn-resume-setup`，ghost 不是主按鈕；首次設定精靈收著而設定沒完成時才出現，§3.2）、通知紀錄的鈴鐺（`btn-bell`，見下面「通知三層」）。
  **首次設定精靈開著時**（`#app.firstrun-on`）標題列只留 app 名稱、連線點、鈴鐺與 toast：三個分頁、服務 pill、重新掃描、`node-line` 與底部狀態列都隱藏。`btn-reload` 保留在 DOM 但 `hidden`（15 秒背景輪詢取代它）；預覽 heartbeat
  移到設定頁的身分區（§2 `Heartbeat()`）。標題列是 Wails 拖曳區；macOS 下 `body.mac` 讓左側留出視窗按鈕的位置。
- **收件匣徽章（#146，2026-09-18）**：數字，不是「新」。
  - 定義是 **held，不是未讀**：節點沒有「已讀」這個概念，桌面端在抽屜裡讀也刻意不標示
    （抽屜那句「在這裡讀不會把訊息交給 agent，也不會標示已讀」仍然成立，也仍然是真的）。
    徽章數的是「還在收件匣裡、agent 還沒取走」的則數；只有 agent 取走或有人刪除才會減少。
  - 來源是一個批次端點 `GET /v1/inbox/counts`，在**每次成功的 `Overview()` 之後**跟著讀一次，
    用同一個 `overviewRequest` / `overviewApplied` 序號守衛（#114）。**不是每列打一次 API**——
    那正是改版時拿掉徽章的原因。
  - **0 與「不知道」是兩件事**：節點只回有訊息的 session，所以缺鍵＝0；讀取失敗則是「不知道」。
    兩者**都不顯示徽章**（一千列各掛一個「0」會把真正有東西的列蓋掉），但讀取失敗
    **不得被寫成 0**：`state.inboxCounts.ok` 轉為 false，上一輪的數字留在 `counts` 裡不清空，
    節點連不到時同樣是「不知道」。兩者在畫面上唯一分得出來的地方是收件匣按鈕的 title
    （已知為 0 → 「這個收件匣現在沒有訊息在等」；不知道 → 原本的按鈕說明），
    這條由 `frontend/test/inbox-badge.mjs` 以變異測試釘住。
  - `full` 另外標色（amber，`.inboxbadge.full`），因為滿了會把新訊息退回，是現在正在發生的事。
  - 三個位置：每列收件匣按鈕上的數字、`本機 session` 分頁上的全機總和（`#tab-local-inbox`，
    與既有的 session 計數 `#tab-local-n` 並列，數的是不同的東西）、以及 `document.title` 的
    `(N) ` 前綴。抽屜自己的 `held / capacity` 文案不變，不重複。
  - **分頁上那兩個數字必須看得出是兩個**：兩個裸數字並排會被讀成一個
    （「本機 session 1119 555」看起來是千分位，或是「1119 之 555」）。所以訊息總數自成一顆
    pill（`.tabinbox`）：`margin-left: 8px` 的間距 + 一個信封字符 `✉`（`aria-hidden`，
    語意由 pill 的 title `inbox.badge.tabTitle` 承擔），數字獨立放在 `#tab-local-inbox-n`。
    只要機器上有任何一個收件匣是滿的，這顆 pill 轉 amber——理由與列上的徽章相同，
    而且從別的視圖看過來，分頁是唯一看得到這件事的地方。
  - **元素識別**：表格改成以 session id 為 key 的原地更新（`sessionRow` / `updateSessionRow`，
    與 `updateCandidateRow` 同一套），15 秒的 tick 不再重建任何一列——徽章會自己變動，
    重建會把使用者正要按下去的收件匣按鈕換掉。`col.c-actions` 因為徽章從 176px 放寬到 200px。
  - **留存的列上，每一個會被翻譯的字串都必須寫在 update pass 裡，不能只寫在建立的時候。**
    這是上一條的直接代價，也是它引進的第一個 bug：列會被保留，所以在 `sessionRow` 裡寫死的字
    是用「這個 session 第一次出現時的語言」寫的，之後切語言不會重建列，那個字就永遠留在舊語言
    （收件匣按鈕的「收件匣」曾經整桌留在英文介面裡）。規則適用於 label、title/tooltip、
    `aria-label`、以及任何 `t()` / `plural()` 的產物；`sessionRow` 只負責建立空的骨架，
    按鈕的文字放在自己的 `<span class="label">` 裡（因為徽章是同一顆按鈕的子節點，
    直接寫 `textContent` 會把它刪掉）。切語言時 `setUILanguage()` → `render()` 重畫可見的列，
    `repaintFromState()` → `relabelSessionRows()` 再掃一次 `sessionRows` 裡的每一列——
    也就是別的分頁在畫面上、沒有 `render()` 跟在後面時仍被留住的那些（被篩選條件擋掉的列
    每次 `renderRows` 都會從 map 移除，重新出現時是用當下語言重建的）。由 `frontend/test/i18n.mjs` 釘住：
    zh 開兩列 → 切 en → 每列的 Inbox / Copy ID 標籤與 tooltip（含 `aria-label`）、工作目錄按鈕的 tooltip 都是英文、`#rows` 整段
    textContent 不得出現任何漢字 → 再切回 zh。
- **通知三層（2026-09-29，取代 `#banner`）**。原本的 banner 只有一則：成功 4 秒就消失、錯誤被下一則蓋掉，
  而且大多數寫入後面跟著的 `load()` 會把它藏起來，錯過就找不回來。現在：
  - **Toast**（`#toasts`，`aria-live="polite"`，在 index.html 所有抽屜與對話框**之後**）：右下角堆疊，最多 3 則，
    新的在下；超過時先擠掉最舊的**會自動消失**的（`ok`／`info`），沒有才擠掉最舊的警告或錯誤（都仍在通知紀錄裡）。
    **剛跳出的那一則永遠不是被擠掉的**：三則警告／錯誤之後來的 `ok` 是唯一會自動消失的一則，擠掉它等於連同「復原」一起丟掉
    （通知紀錄只存句子、不存按鈕），所以此時擠掉的是最舊的警告或錯誤（測試 `notifications.mjs`）。`notify(kind, title, {body, actions})`，kind 是
    `ok`／`info`／`warn`／`error`：`ok` 與 `info` 6 秒後自動收起，帶動作按鈕（復原、去公開 session 等）的延長為 15 秒並加 `long`（擁有者 2026-09-29 指定）（底部倒數條；滑鼠停在上面或鍵盤焦點在它裡面時暫停——倒數條停住、加 `held`——兩者都離開後重新給足原本的秒數，在它自己的按鈕之間移動焦點不算離開），`warn` 與 `error`
    **不自動消失**，要按 ✕；後兩種 `role="alert"`，前兩種 `role="status"`。`actions` 是 toast 上的按鈕，
    按了先收起再執行。舊的 `banner(message, ok)` 保留為 wrapper：`ok=true` → 成功，其餘 → 錯誤。
    配對抽屜等右側抽屜開著時整疊移到抽屜左邊；對話框（`.modal`）開著時只留最新一則、夾在卡片上方那條背景裡，
    不蓋住對話框底部的動作列。
  - **通知中心**：標題列的鈴鐺 `#btn-bell`，數字 `#bell-n` 是**未讀的錯誤＋警告**（0 時隱藏）。點開右側抽屜
    `#notify-modal`，列出本次開啟以來每一則 toast 與待處理列項目（嚴重度色條、標題、內文、HH:MM 與嚴重度文字），
    最新在上；開啟即全部標為已讀，開著時新來的也直接算已讀。只存在記憶體，上限 100 則。空時「目前沒有通知。」。
    它不在 `MODAL_IDS` 裡：唯讀清單，不擋 15 秒背景重讀。
  - **待處理列**（`#attention`，標題列正下方，每個視圖都看得到）：一列一件需要使用者處理的事，左側嚴重度色條、
    一句標題＋一行說明、一顆直接處理它的按鈕、一顆「稍後」。事情解決了該列自己消失；項目出現時記一筆進通知中心
    （alert 記為錯誤、warn 記為警告、info 記為資訊），同一件事持續期間不重複記。

    | 項目 | 條件 | 嚴重度 | 按鈕 |
    |---|---|---|---|
    | 節點沒有回應 | 至少一次 `Overview()` 已回答（`state.nodeChecked`）且最後一次 `reachable` 為 false；說明沿用 `app.notConnected`（含錯誤原文與「上次成功載入」／「還沒有載入過」） | alert | 「重試」＝前景 `load()`（走 `withBusy`） |
    | 背景服務 | `ServiceStatus().supported === true`、沒有 `toolError`、且不是「已安裝且執行中」 | alert | 未安裝「安裝為背景服務」（節點在跑但不是服務時「到設定頁安裝」，`attention.service.formAction`）、已安裝「啟動背景服務」，按了就做（`runServiceQuickAction()`，見下） |
    | 收件匣已滿 | `inboxCounts.ok` 且有 `full`（讀不到數字時不顯示，不拿舊數字說滿） | warn | 「開啟收件匣」開第一個滿的（依表格順序）；兩個以上時標題寫數量 |
    | 有機器想配對 | 節點回報配對開放中，且最近一次讀到的交換裡有 `incoming`＋`pending`（讀取規則見 §2 `PairRequests`） | info | 「比對並核准」＝`goToPairing()`。一件時標題是「自稱 {name} 的機器想和這台機器配對」：name 是對方自選的 `displayName`，放在 `class="claimed"` 的 span（同收件匣寄件者的自選那半），沒有名字（空白或只有空格）時另一句（`attention.pair.titleOneNoName`），**不拿 nodeId 當自稱名稱**（同候選列的「（未提供名稱）」，`candidateName()`；測試 `notifications.mjs` 3d） |

    **背景服務一鍵處理（2026-09-29）**：按下先重讀 `ServiceStatus()`，用讀到的算；讀失敗→設定頁＋錯誤 toast，不動手。`serviceQuickAction(status)` 只看狀態——沒有狀態、有 `toolError`、`supported !== true`、
    或已安裝且在跑 → `settings`；已安裝 → `restart`；其餘 → `install`。`install` 走設定頁按鈕的 `installService()`（資料庫路徑留空），
    `restart` 走 `restartNode({ service: true })`，`settings` 是 `goToService()`。重讀期間按下的那顆就轉圈並停用（`busy`＋`aria-busy`＋`disabled`；待處理列的按鈕帶 `busy` 時 render 不會把它解除停用）；
    整個一鍵處理由模組層旗標 `serviceQuickPending` 鎖住，pill 與待處理列同時按（或同一顆連按）只跑一次，另一下直接返回，
    重讀回來先放開再依結果決定；重讀期間另一個寫入開始了（`state.busy`）就什麼都不做、連表單都不開（測試 `inline-publish.mjs` §6）。之後都經 `withBusy` 讓按下的那顆轉圈；結果用 toast，
    失敗（丟錯、重啟後節點沒回答、degraded）的 toast 帶「開啟設定」（`withBusy` 的 `failureActions`；它走
    `goToService({ keepForm: true })`，不重開表單——重開會清掉資料庫欄位、藏起 ah 的錯誤原文）。成功後的 `load()` 重讀
    `ServiceStatus()`，這一列自己消失。三種情況不照按：另一個寫入進行中（`state.busy`，連表單都不動）；設定頁的服務表單開著、
    資料庫欄位有打字——空白是節點預設資料庫，等於新身分、所有配對作廢，所以改去表單，不替使用者清掉；
    節點在跑但不是服務（`nodeRunningNotAService()`：`install` 且 `state.nodeReachable` 或狀態的 `nodeAnswering`）——
    那是手動帶起來的節點，資料庫可能不是預設的，改去服務表單；表單的說明請使用者填那個節點的 `--db` 路徑，
    欄位空白按安裝會先問（danger、預設焦點在取消，見 §2 `InstallService`）。pill 的 `busy` 轉圈
    撐過中途落地的 `ServiceStatus()`（`renderServicePill` 保留 `busy`）。測試 `inline-publish.mjs` §6（三個分支、空資料庫路徑、RestartService 不是 RestartNode、
    失敗 toast 的動作、過期狀態重讀後不裝、節點在跑非服務不裝、重讀失敗）、`notifications.mjs` §3b（按鈕文字）。

    「稍後」以項目的 key 收起（key 帶身分：哪幾個收件匣、哪幾個請求 id），**直到它消失後再出現才會回來**；
    換成不同的一組（又多一個收件匣滿了）就是新的一件。列元素以項目種類為 key 原地更新（`keepChildren`），
    15 秒 tick 不會換掉游標下的按鈕。

    **首次設定精靈開著時**（2026-09-30，取代「首次啟動清單在時」的單列規則）：待處理列**整個隱藏**——精靈第 1 步就是
    節點與服務那兩列的同一件事，同一件事兩顆按鈕正是被取代的清單的問題。項目仍照常記進通知紀錄（鈴鐺）；精靈收起
    （稍後再設定、開始使用、或被一個 `goTo*()` 暫時讓開）就回來（`renderAttention()` 看 `firstRunVisible()`；
    測試 `first-run.mjs` §2、`notifications.mjs` 3b、`inline-publish.mjs` §6）。

    **超過 2 件時收合**：依嚴重度（alert > warn > info，同級照上表順序）只顯示前 2 件，第三列是一顆
    「還有 N 件」（`attention.more`），按了全部展開、按鈕變「收起」。展開狀態只在這個視窗的記憶體裡，不存；
    項目降到 2 件以下（沒東西可收）就重置為收合，之後再超過時從收合開始（測試 `notifications.mjs` 3f）。
    兩件（含）以下沒有這顆。收合是為了 900×760 下表格仍看得到至少 5 列。
    測試 `notifications.mjs` §3b、§3f。
- **首次設定精靈**（`#first-run`，見 §3.2）：開著時佔滿主內容區，三個視圖都藏在它下面；觸發條件橫跨三個視圖的狀態。
- 狀態列：左「顯示 N / total 個 session · 所有已配對 N · 指定節點 N · 不公開 N」，右本機節點 ID。
- `busy` 狀態：任何寫入進行中，所有寫入按鈕 disabled。**按下去的那一顆**另外轉圈並停用自己到呼叫回來為止
  （`withBusy(label, fn, { button })`，`button.busy` + `aria-busy`）：安裝／移除／重新啟動服務、送出配對請求、
  核准／確認／拒絕、套用公開對象與收回、撤銷信任、清空收件匣、重新掃描、儲存節點設定、記錄位址、開／關配對、
  手動信任、heartbeat、待處理列的重試。
- **尺寸（2026-09-29）**：`--control-h` 36px（原 28px）、主要按鈕 `--primary-h` 40px、表格列 48px；可點面積
  不小於 36×36——圖示按鈕（`.iconbtn`、列上的收件匣）是 36px 正方形，勾選框的整格（40px 寬）都是可點區
  （`td.col-check`／`#select-all-cell` 的 click 轉給裡面的勾選框），篩選 chip 與文字連結維持原視覺大小、
  用 `::after` 把可點區撐到 36px 高。鍵盤焦點用 2px `--focus` 外框。900×760 下（`dev/mock.html` 的
  `layoutCheck()`，中英文、含 `body.mac`）沒有水平捲動、沒有被裁掉的按鈕；標題列的節點那一行最多兩行，
  完整內容在它的 `title`。
- **停用要說原因**：會長時間停用的控制項，原因寫在旁邊看得到的 `.disabledwhy` 小字，不只放 `title`：
  候選列沒有位址時的「送出配對請求」（`candidate.noAddressWhy`）、背景照片關閉時的數字雨開關
  （`#motion-why`）。原本就有旁邊文字的（availability=off 的配對開啟鈕、`copy-pair-address`、Claude-only 的喚醒）不變。

### 3.2 本機視圖

- **首次設定精靈 `#first-run`**（2026-09-30，取代 `#onboarding` 清單卡片；核可的 mock 是
  `docs/ui-redesign/2026-09-30-first-run-mock.html`）。裝好 .dmg／.exe 第一次打開的人看到的就是它，
  所以英文文案的品質跟功能一樣重要。它是和三個視圖並列的 `<main class="view">`，開著時（`firstRunVisible()`）
  `render()` 把三個視圖都藏起來，`#app` 加 `firstrun-on`（標題列簡化，§3.1），待處理列整個隱藏（§3.1）。
  左邊 `#first-run-rail`：三步（`#first-run-steps`，號碼／完成打勾 `done`／目前步驟 `now` + `aria-current`）與底部
  「稍後再設定」（`#first-run-later`）；右邊 `#first-run-stage`：目前步驟，一次只有一顆主要按鈕，細節在 `<details class="why">`。
  - **出現條件**（沿用清單的四條，任一成立）：節點連不到；服務支援但沒安裝／沒在跑；`sessions` 是空的；`nodes` 是空的
    （`firstRunTriggered()`）。除了「節點連不到」以外，每一條都要 `state.loadedOnce`——讀不到節點時的空表格不是這台機器的
    事實（#114）；`loadedOnce` 與 `nodeReachable` 由 `load()` 一起設定。**新增**：全部要 `state.nodeChecked`（至少一次
    `Overview()` 已回答）——第一次讀取回來前 `nodeReachable` 是「還沒問」不是「連不到」，而精靈跟清單不同、出現後就留著。
    另外要 `UI_PREFS_KEY` 裡 `onboardingDismissed` 與 `firstRunFinished` 都不是 true（沿用清單的鍵名，所以清單時代關掉的
    設定照樣有效）。
  - **只在啟動時自己出現**：視窗對這台機器的第一個完整認識（第一次 `Overview()` 回答**而且** `ServiceStatus()` 也回來了；
    狀態一直讀不到就以第二次 `Overview()` 為準）形成之後（`settled`），觸發條件再成立也不自己出現——之後節點掉線、在終端機停掉服務、
    撤銷最後一台配對，都是待處理列的事，不能把整個精靈蓋在使用者正在用的設定頁上（fresh-context 審查 2026-09-30 找到的）。
  - **出現後就留著**（`syncFirstRun()`）：觸發條件正是第 1 步會關掉的東西，精靈不能在第一顆按鈕成功時自己消失。唯一例外：
    沒人碰過（`touched`）、不是手動打開（`forced`）、沒在跑、而觸發條件全都消失了——啟動時節點慢了一點的已設定機器——才自己收起。
  - **打開就到第一個未完成的步驟**（含啟動時自己出現，同「繼續設定」的 `firstIncompleteStep()`）：區網已開、還沒配對的機器開在第 2 步，
    不是開在一顆只剩「下一步」的第 1 步。啟動時判斷所需的讀取（`ServiceStatus()`、精靈自己的 `loadPairing()`）可能晚到，所以
    **沒人碰過、沒在跑**時每次 render 都再算一次，只往前、不往回；使用者按過任何東西（含「上一步」）之後步驟就是他的（`syncFirstRun()`，
    測試 `first-run.mjs` §1b、`first-run-pairing.mjs` §1）。
  - **每步的完成狀態每次 render 從 state 重算，不存**（`firstRunChecks()`、`firstRunStepComplete()`）：
    ①「AgentHub 在背景執行」＝`state.nodeReachable`；「開機後自動啟動」＝`ServiceStatus` 已安裝且在跑（狀態還沒讀到＝確認中；
    `toolError` 或 `supported !== true`＝不適用，說明但不算失敗、不擋下一步）；「區網裡的其他電腦連得到」＝`pairHereState(state.pairing.state).reachable`
    （節點說了就聽節點的，§3.3）。在終端機移除服務，那一行自己變回未完成。②＝`state.nodes` 非空。③＝`counts.all_paired + selected > 0`。
    只記在記憶體、不存的是使用者的選擇：只在這台用（`localOnly`）、略過第 2／3 步、選的位址、勾的 session。
  - **第 1 步的一顆主要按鈕**（`runFirstRunPrepare()`）依序做完未完成的，任何一步失敗就停在那一行：一句可讀的錯誤＋`<details>`
    裡的原始錯誤（這一步期間記進通知紀錄的錯誤／警告，`noticesSince()`）＋「重試」，不前進；全部完成才自動到第 2 步
    （只在這台用時到第 3 步）。順序是**節點與服務 → 區網設定（含重啟）→ 重讀**，不是「設定 → 安裝 → 重啟」：節點設定存在
    節點裡，節點沒在跑時寫不進去；服務先裝，單元裡就不會烘進任何設定（#116），而存檔自己的重啟會經過那個服務。
    - 節點與服務（`prepareFirstRunNode()`）：先重讀 `ServiceStatus()`。有服務管理員時：已安裝 → `restartNode({ service: true })`
      （`RestartService`）；未安裝 → **標題列 pill 同一個** `runServiceQuickAction()`（它再讀一次狀態、`nodeRunningNotAService()`
      時不裝而是 `goToService()` 開表單並問、資料庫欄位照 `openServiceForm()`、安裝走 `installService()` 含它的 `askConfirm`）。
      精靈自己在呼叫之前也先判一次 `nodeRunningNotAService()`，是的話直接 `goToService()` 並用 info toast `firstRun.toServiceForm`
      說為什麼到了設定頁——兩道守衛任一道都擋得下安裝（測試 `first-run.mjs` §5 各自變異過）。沒有服務管理員（不支援、找不到 ah）
      而節點沒回應 → `restartNode()`（`RestartNode`）。之後節點還沒回答就 `waitForNode()` + `load()`，並**再讀一次**
      `ServiceStatus()` 才判定，並且用**那次讀取自己的回答**（`load()` 發出狀態讀取但不等它；被更新的讀取超車的那次不會存進
    `state.service`，存著的可能還是安裝前的狀態）。服務那一步失敗記在「開機後自動啟動」那一行，節點那行只說還沒執行。
    - 區網（`prepareFirstRunLan()`）：節點設定表單有使用者沒存的修改（`unsavedNodeSettingsFields()`，配對抽屜同一條）就停，
      列出欄位、不替他存；否則 `loadNodeSettings()` 取新基準，再走 `applyPeerListenRepairFromCard({ peerListen, peerListens: [位址], allowLan: true })`
      ——配對抽屜第 1 步同一條路：表單填好按儲存、單元固定的旗標照 §7.8 問、存完 `RestartNode()`（已安裝服務時 Go 端走
      `RestartService`）、重讀比對有沒有真的寫進去（`didNotStick`）。之後 `loadPairing()` + `load()`，用節點回報的可達性判定成敗。
    - **快照規則（隱私）**：區網那一段只寫**按下那一刻畫面上說明句寫出的位址與埠**。第 1 步每顆會跑 `runFirstRunPrepare()` 的按鈕
      （主要按鈕、「重試」）在按下時取 `firstRunShownLan()`——最後一次畫出的說明句的 `{ address, port }`，說明句沒顯示時是 null——
      原樣傳給 `prepareFirstRunLan(shown)`，**不在節點與服務那段之後重算** `firstRunChosenAddress()`／`firstRunPort()`（節點存的位址與埠
      要節點跑起來才讀得到、位址清單可能在中間才回來或改變）。按下時沒有說明句（例如 `LocalAddresses()` 還沒回答、畫面是
      `firstRun.lan.noPrivate`）→ 只做節點與服務就停在第 1 步，讓使用者看到位址再按「開放區網並繼續」（測試 `first-run.mjs` §6b：
      位址晚到、清單在按下後改變、節點存的位址改變選擇、埠在按下後改變）。
    - 按鈕文字：節點或服務沒好 →「準備好這台電腦」；只差區網 →「開放區網並繼續」；全部完成 → 完成狀態＋「下一步」。
    - 進行中那幾行顯示「進行中…」轉圈（`phase`），主要按鈕 `busy`；`state.busy` 或進行中時每顆寫入按鈕 disabled。
  - **開放區網是隱私決定**：主要按鈕上方一行藍底說明，寫出實際會用的位址（`LocalAddresses()` 裡 `private` 的那些，
    埠照節點存的 `peerListen`）與影響（`firstRun.lan.consent`）。**兩個以上私有位址時列出單選**（`first-run-address`，
    預設節點已存的那個、否則第一個），說明句跟著選到的那個改；不替使用者決定。沒有私有位址時說明（`firstRun.lan.noPrivate`）
    並只提供「只在這台用」（此時它是主要按鈕）。
  - **「只在這台用，不開放區網」**：不寫任何節點設定（節點或服務沒好時仍會先做那一段，那不是節點設定）；第 2 步在步驟欄標
    「已選擇只在這台用」，直接到第 3 步，第 3 步只說之後再分享並只給「先跳過」。**節點與服務那段成功後才算數**（`localOnly`）；
    失敗時選擇只記成待定（`localOnlyPending`），區網那一行、說明句與兩個選擇都留著，「重試」重做同一個選擇、不開放區網
    （測試 `first-run.mjs` §6b 後段）。
  - 節點連不到時「AgentHub 在背景執行」那一行的主文是 `firstRun.node.down`，`dial tcp…` 原文只在它的 `<details>` 裡。
  - **第 2 步「連到另一台電腦」**（2026-09-30 第二段）：配對抽屜換了外觀與順序，**安全邏輯一條都是抽屜的**——
    送出走 `sendPairRequest()`（只帶位址）、決定走 `decidePairRequest()`（id 來自畫出那張卡的請求）、指紋區塊走 `writeFingerprintBlock()`
    （節點 `fingerprints` 陣列原樣，標籤走固定對照表，沒有陣列時照 §3.3 說去用 `ah pair pending`）、候選與請求都走 `reconcileRows()`
    （抽屜的候選列與請求列也改走它：空 key 與重複 key 一律新建，請求的指紋簽章變了就丟掉重建〔`fingerprintsChanged()`；抽屜這條由
    `pairing-exchange.mjs` §8d-ii 斷言：同一個 request id 指紋改變 → 新元素，之後不變 → 同一個元素〕）、
    notice 走 `candidateNoticeText()`、錯誤翻譯走 `pairErrorMessage()`。由上而下：
    - 標題；已配對時一句「已經和 N 台電腦配對。」，**「下一步」（主要按鈕）移到這句下面**，候選列的「送出配對請求」降為 ghost（一次一顆主要按鈕），仍可再配對一台。
    - 怎麼結束的（`frended`，見下）。
    - 虛線提示框：「**在另一台電腦也打開 AgentHub**，做到這一步。兩台都在這個畫面時，會互相出現在下面。」
    - **有未決請求（pending／awaiting-confirm）時**，每個請求一張卡（`frrequest`，以 request id 為 key），提示框與下面的尋找區隱藏：
      outgoing pending ＝等待畫面（轉圈、「等 X 按『核准』」、對方畫面會看到什麼、「對方核准之後，這裡會換成兩組指紋，你也要在這台比對一次」、
      「取消這次請求」→ `RejectPairRequest`，沒有指紋）；incoming pending 或 awaiting-confirm ＝比對畫面（標題「X（自稱）想和這台配對」／
      「和 X（自稱）比對指紋」、「看著另一台的螢幕…」、`PAIR_TEXT.compare` 警語＋說明〔指紋之上〕、放大的指紋區塊、
      **按鈕在指紋下面**：「一樣，核准」→ Approve／「一樣，完成配對」→ Confirm，「不一樣，取消」→ Reject）。等待→比對是**同一張卡、同一顆拒絕鈕**。
      成功 toast 同抽屜（`pairStepText()` 的句子＋「（節點回報：…）」），但**不帶「去公開 session」**（`fromWizard`：精靈的下一步就是分享，那顆會把人帶出精靈）。
    - 沒有未決請求時：「同一個網路上找到的電腦」（`frmachine`，以 nodeId 為 key）：平台圖示（固定對照表，不取自字串）、名稱（無名「（未提供名稱）」）＋ amber「自稱」、
      `contested`／`duplicate` pill、平台 · 最後看到；完整 nodeId、宣告的指紋、位址、首次／最後看到收進該列的 `<details>`
      （§4 的候選列斷言是對抽屜的 `candidate-rows`，在 DOM 裡就算數；精靈這份在 details 裡，也在 DOM 裡）；「送出配對請求」。
      清單狀態同抽屜：讀取中、沒在看（`firstRun.pair.notLooking`）、狀態讀不到、清單讀不到＋原文、清單已滿、空（「還沒找到…」）。
    - `<details class="frmanual">`「找不到另一台？」：位址欄＋「送出」（Enter 同按、trim、空的不送並給錯誤 toast——都是 `sendPairRequest()` 的）、
      這台的位址（`pairHereState()`，節點說了就聽節點的；多個開放位址時列出每個）＋「複製」（`copyPairAddress(address, 狀態列)`），
      不可達時不印位址、複製 disabled、說明並給「回到第 1 步」（不重複修復按鈕）、何時需要手動的一句、「手動輸入配對資料…」（`openPairModal()`）。
    - 底部：「先跳過，之後再配對」（`pairSkipped`，步驟欄標「已略過」；取代開發用的佔位鈕）、「上一步」（到第 1 步）；已配對時只有「下一步」與「上一步」。
    - **怎麼結束的**：第 2 步畫過的未決請求從清單消失、而不是這個視窗自己決定的（`pairDecided`，決定前記下、失敗時移除），讀一次 `PairRequests(true)`，
      用 `pairStepText()` 說（`fingerprint_mismatch` 是專屬句），節點的 `nextStep` 以「（節點回報：…）」跟在後面（已結束的列才顯示它，§3.3），「知道了」收起。
    - **生命週期**：第 2 步在畫面上（`firstRunPairingActive()`：精靈可見、`step === 2`、不是只在這台用）＝配對抽屜開著。`renderFirstRun()` 每次比對前後狀態
      （`syncFirstRunPairing()`），進入時 `enterFirstRunPairing()`（讀請求、`loadPairing()`、`openPairingWindowIfNeeded()`＝`OpenPairing(0)`），
      離開時（任何方式，含 `stepOutOfFirstRun()`）`releasePairingWindow()`。5 秒與 2 秒兩個既有 interval 的條件延伸到它（§4），1 秒倒數在它上面也跑，
      不顯示數字；歸零時問節點、第 2 步還在就重開（`keepFirstRunWindowOpen()`）。15 秒 tick 的 `refreshIncomingPairRequests()` 在它開著時不讀（同抽屜）。
      2 秒 tick 跟抽屜一樣之後接 `load({ background: true, exceptPairingDrawer: true })`（Overview，以及它帶出的 `ServiceStatus()`）——
      第 2 步靠它看到 `state.nodes` 多了一台。第 2 步讀的一律是 `PairRequests(false)`：抽屜的「顯示已結束」勾著也一樣。
      讀取失敗時不畫任何請求卡片（上一份清單也不算數，同抽屜的 `renderPairRequests()`），只說讀取失敗；有寫入在進行時「稍後再設定」disabled
      （那時離開，`releasePairingWindow()` 會因 `state.busy` 跳過而沒有人再回來關視窗）。
      寫入進行中進入第 2 步時 `openPairingWindowIfNeeded()` 不開視窗；這次開啟記成欠著（`pairWindowDeferred`），寫入結束後下一次
      5 秒 tick 仍在第 2 步、視窗沒開就開一次（仍傳 0；節點拒絕也只問這一次；開成後照「每個見過開著的視窗只重開一次」算）。
      設定 → 外觀的「顯示首次設定」在 `state.busy` 時 disabled（同「繼續設定」；測試 `first-run-pairing.mjs` §11b）。
      只在這台用時第 2 步是略過狀態（`firstRun.step2.localOnly`＋「下一步」），不開視窗、不輪詢。
    - 測試 `first-run-pairing.mjs`（§0 四個 interval、§1 進入與 `[0]`、§2 輪詢、§3 候選列、§4 等待與取消、§5 位址欄、§6 比對與三個動詞、§7 沒有陣列、
      §8 沒有自動決定、§9 element identity 與 `replaceChildren` 次數、§10 結束方式、§11 到期重開、§11b 寫入中進入、§12 離開的四種情況、§13 只在這台用、§14 pill）；
      dev mock `?onboarding=mixed&lan=open` 加 `&pair=none|outgoing|confirm|incoming|mismatch`（沒有 `&pair=` 時是兩台候選，送出後 6 秒對方「核准」）。
  - **第 3 步**：本機 session 依最後活動新到舊，先顯示 8 個、多的「顯示全部 N 個」；每列 checkbox＋標題（沒有就用 id）＋工作目錄
    （`<bdi>`、從左邊裁）＋provider badge，全部 `textContent`；預設全不勾。兩個情境卡片（`first-run-preset`：能留訊息／留訊息並喚醒，
    喚醒下是 `wake.caveat`）；勾的全是 Claude Code 時喚醒 disabled 並寫 `popover.wakeClaudeOnly`（與選單、對話框同一條）。
    主要按鈕「分享 N 個 session」（沒勾時 disabled，寫「先勾選要分享的 session」）走 `applyAudienceChoice(ids, preset, { withoutCwd: true })`：
    mode 規則同行內選單（未公開 → `all_paired`、已公開保留原本的 mode 與 nodes），`exportCwd` 一律 false（畫面上沒有任何地方說會帶工作目錄），
    成功 toast 與「復原」照舊。全部成功才到完成畫面。0 個 session：`firstRun.step3.noSessions`＋「重新掃描」（`discoverSessions()`）。「先跳過」。
  - **完成畫面**：一句結果（分享了幾個、給誰〔`describeTargets()`〕／只在這台用／還沒分享），「開始使用」＝寫入
    `onboardingDismissed` 與 `firstRunFinished`、回主視窗。主視窗「公開對象」欄的一次性提示**沒有做**（規格標為可選）。
  - **稍後再設定**：收起、寫入 `onboardingDismissed`、回主視窗、info toast 說「繼續設定」在哪。之後只要 `firstRunFinished` 不是 true
    且（精靈被暫時讓開，或使用者見過精靈〔這個視窗出現過，或 `onboardingDismissed`〕而觸發條件仍成立），標題列就有「繼續設定」
    ——沒見過精靈的已設定機器，節點掉線時不會冒出這顆：按了重新打開並跳到第一個未完成（也沒被略過）的步驟
    （`firstIncompleteStep()`）。設定 → 外觀的「顯示首次設定」（`settings-show-onboarding`）清掉兩個偏好、清掉略過的選擇，同樣打開。
  - **讓開**：`goToService()`、`goToNodeSettings()`、`goToPairing()`、`goToPublish()` 會先 `stepOutOfFirstRun()`——精靈蓋住三個視圖，
    它送使用者去的地方不能被它自己蓋住。只在這個視窗讓開（`suspended`），不寫偏好。
  - **元素識別**：每一步的面板、每一行、每顆按鈕、每個位址與 session 列都只建一次、之後就地改寫（`firstRunParts`、以位址／session id
    為 key 的 Map），15 秒的 `load()` tick 不換掉游標下的按鈕、不收起打開的 `<details>`（測試 `first-run.mjs` §9）。
  - 測試 `first-run.mjs`（§1 出現條件與 `nodeChecked`、§1b 開在第一個未完成的步驟、§2 待處理列與標題列、§3 三項推導、§4 順序與失敗停下、§5 身分保護、
    §6 位址與只在這台用、§6b 快照規則與失敗的只在這台用、§7 分享、§8 稍後／繼續／設定頁、§9 busy 與 tick、§10 英文）；dev mock `?onboarding=fresh|slow|unreachable|mixed`、
    `&addresses=two`、`mixed&service=none`。
- 搜尋框：比對 `id`、`cwd` 與管理方式，大小寫不敏感。
- **8 個篩選 chip，三組**（`provider` 2、`status` 3、`audience` 3），由 `sessions/filter.js` 的 `CHIPS` 產生。
  **組內可多選（OR），組間 AND**（`matchesGroups`）。每個 chip 帶的是 **facet 計數**——把**其他**組的篩選與
  搜尋都套用後這個 chip 會match到幾筆，所以開著「Codex」時「active」旁邊的數字跟表格一致。計數為 0 且未選取的 chip 加 `zero` 樣式。
- **浮動批次列**（`#selectionbar`，有選取才出現，表格卡片底部中間）：「已選取 N 個 session」、`btn-audience`「公開 ▾」
  （開**同一個**行內公開選單，對象是全部選取）、`btn-unpublish`「收回公開」（＝選單的「不公開」，同樣清掉 `exportCwd`、同樣帶復原）、`btn-deselect`「取消選取」。
  全選只在表頭（`#select-all`，含 indeterminate）；原本列上的 `select-all-visible` 已移除。批次列出現時 `body.selecting`
  把 toast 疊往上推 124px（18 + 56 的列、12 的卡片外距、26 的狀態列），toast 不蓋住它。批次套用成功後清空選取；部分失敗留著。
- **行內公開選單**（`#audience-popover`，`role="menu"`，2026-09-29）。表格的公開對象欄是一顆按鈕（`button.audbtn`，
  文字與 tooltip 同原本的 pill，▾ 由 CSS 畫；`aria-haspopup`／`aria-expanded`），點了在按鈕旁邊（下方放不下改上方，
  兩邊都放不下取大的那邊並捲動）打開，選了**立即套用**：
  - 選項：「不公開」（`mode: none`，`exportCwd` 與三個訊息旗標全 off——同 main 原本的收回公開；復原會寫回原值）、「能留訊息」（`acceptMessages`）、「留訊息並喚醒」
    （`acceptMessages`＋`allowOutbound`＋`autoWake`，下方 amber 小字是喚醒備註 `wake.caveat`）、分隔線、
    「指定機器、進階旗標…」（開 `audience-modal`；從列上來時那一列暫時成為選取，對話框關掉——套用或取消——就還原原本的選取）。
  - **對象不問**：未公開（`describeAudience().published` 為 false：`none`，或 `selected` 且 0 個節點）→ `all_paired, nodes: []`；
    已公開 → **保留原本的 mode 與 nodes**，只改旗標。兩個公開選項的 `exportCwd` 保留現值（`audienceForChoice()`）；
    「不公開」把它清掉，因為未公開的列不顯示旗標，留著的 `exportCwd` 會在下一次「能留訊息」時在使用者看不到的情況下公開工作目錄。
    頂端一行寫這次公開給誰（所有已配對機器／原本指定的 N 台／多選時各自或混合的說法）；按下公開會帶上 `exportCwd` 時
    （例如舊資料或進階對話框留下的、未公開卻勾著的）多一句「含工作目錄」（`popover.withCwd`，多選時只有部分帶上寫數量
    `popover.withCwdSome`）＋「選了立即套用」。
  - 打勾：這一列（或全部選取）目前正是哪一個情境就勾哪一個；已公開但旗標不是兩個 preset 之一（全 off 也是）→ 不勾＋「目前是自訂設定。」；
    多選且不一致 → 不勾＋「選取的 session 目前設定不一樣。」
  - 還沒有配對任何機器（`state.nodes` 空且 `nodesError` 為空）時照樣能用，頂端多一句「公開後配對的機器才看得到」＋「配對另一台機器」（`goToPairing()`）。
  - 只有 Claude Code 的選取，「留訊息並喚醒」disabled 並說明——與對話框的喚醒 preset 同一條規則（§3.5）。
  - 成功 toast 帶「復原」（部分失敗的錯誤 toast 也帶，§2）：把每個 session **原本的** audience 物件寫回（旗標、mode、nodes；不同的原值分批呼叫）。唯一不是原樣的：
    `selected` 且 0 個節點 `SetAudience` 會拒絕，寫回成 `none`＋同樣的旗標（兩者一樣沒人看得到）。套用喚醒時 toast 內文加喚醒備註。
  - 鍵盤：按鈕本身 Enter/Space 開、再按一次關；方向鍵上下（循環）、Home/End；Esc 與 Tab 關並把焦點還給按鈕；點外面關；
    頁面捲動或視窗縮放關。選了之後寫入期間按鈕 disabled，寫完焦點回到那顆按鈕。另一個寫入進行中時按「復原」不會被靜默吞掉：
    警告 toast 說這次沒有復原（`popover.undoBusy`）。開著時 `interactionInProgress()` 為真，15 秒 tick 不讀清單、不動列（它是畫在那一列旁邊的）。
  - 測試 `inline-publish.mjs` §1–§5。
- **旗標欄的喚醒 ⚠**：已公開且 `autoWake` 的列，醒 chip 旁邊一個 amber `⚠`（`.wakecaveat`），`title`／`aria-label` 是喚醒備註全文。
- **表格 8 欄**：勾選、SESSION（含 provider badge；`management` 進 badge 的 `title`）、狀態、公開對象（表格內 all_paired 用短標籤 `audience.cell.allPairedShort`：en「All paired」、zh「所有已配對」，tooltip 與篩選 chip 用完整說法；欄寬 108px——2026-09-29 它成了按鈕，加上 ▾ 與按鈕內距，量過「Not published」要 89px 內容；旗標欄 156→160px 放 ⚠；900px 下每個標籤都放得下，#194）、**旗標**（只在已公開的列顯示；mode `none` 與「指定：無」（`selected` 且 0 個節點）都算未公開，同 `describeAudience().published`，測試 `row-audience.mjs`）、工作目錄、最後活動、**動作**。MANAGED 欄已移除（2026-09-23）。
- **有排序**：5 個表頭可排序（`id`、`status`、`audience`、`cwd`、`lastSeenAt`；`SORT_KEYS` 仍接受舊偏好裡的 `management`／`provider`，但沒有表頭），
  預設 `lastSeenAt` 由新到舊。`status` 與 `audience` 用語意順序不是字母序（active→idle→inactive；
  all_paired→selected→none）。排序與篩選都寫進 localStorage。
- 列動作兩顆：`收件匣 ｜ 複製 ID`，靠右 sticky，`col.c-actions` 172px。MCP 入口已移除，見 §10。
  「複製 ID」（class `copyid`；en「Copy ID」）只複製 provider 自己的 session id，**不帶任何指令前綴**（§8）；
  tooltip 與 `aria-label` 以按鈕文字開頭並寫出用途與 ID 本身（「複製 ID：這個 session 的 ID，可用於 claude --resume。<id>」，
  Codex 列寫 `codex resume`）。按下後的「已複製 ✓」用 10.5px 字（13px 時按鈕從 60px 變 71px，
  收件匣帶三位數徽章的列要 180px，超過欄寬 172px；dev/mock.html 900×760 量的），按鈕寬度不因此變大。
- 工作目錄欄（2026-10-01）：有值時整格是一顆按鈕（`button.cwdcopy`，欄寬 × `--control-h` 高），按下複製完整路徑；
  hover／鍵盤焦點時顯示邊框與複製圖示（圖示佔最後 22px，tooltip 有完整路徑）。路徑照舊以 textContent 進 `<bdi>`、
  由右往左截斷。空值顯示純文字「—」（`span.cwdempty`），沒有按鈕。兩者在 `sessionRow` 建一次、`updateSessionRow` 切換，
  tick 不換掉按鈕。
- 空狀態：「沒有符合條件的 session。」

### 3.3 區網視圖

左欄（`nodelist`）：
- 已配對節點列表：每列 presence 點 + 名稱 + presence 文字 + 平台 · 最後聯繫。空：「尚未配對任何節點。」
- 配對模式面板：headline、倒數（獨立元素，每秒只改這一個）、detail、note（含 `broadcastWarning`，把本機名稱和它的來源說出來）。
  **這裡沒有自己的按鈕**（2026-09-17）：原本的 `#btn-open-pairing`「開啟配對面板」在 `#btn-pair` 改成開抽屜之後，
  和它變成同一個 handler（`openPairingDrawer`）、同一個視圖上的兩顆一模一樣的按鈕，只是文字不同。
  重複的入口只會讓人以為兩顆做的事不一樣，所以留下節點列表上那顆 primary 的 `#btn-pair`，把這顆刪掉。
- 正在廣播的機器：`candidate-full` 警告（在捲動區**外面**）、候選列（名稱、爭用/重複 pill、平台 · 位址、完整 nodeId、完整指紋、首次/最後看到、「送出配對請求」＋「改用手動填入…」）、`candidate-notice`（節點自己的免責文字；節點另回穩定代碼 `noticeCode`，目前只有 `candidates_unverified`，視窗認得就用 `candidate.notice.<code>` 以介面語言顯示，不認得或沒有代碼就顯示節點的英文 `notice`，#194）。
- **`#btn-pair`「配對另一台機器…」（節點列表的 primary 按鈕）開的是配對抽屜，不是手動表單**（2026-09-17）。
  它本來開 `pair-modal`——那是兩台機器互相連不到時的退路，要手動填五個欄位、還要自己把 base64 公鑰帶過去。
  結果這個視圖上最顯眼的按鈕把新手丟進退路，而真正會幫他找到對方機器的交換流程躲在次要連結後面。
  手動表單仍然只差一步：抽屜頁尾的 `#btn-pair-manual`「手動輸入配對資料…」，那裡本來就有一句話說明什麼時候該用它。
  （候選列的「改用手動填入…」＝ `prefillPairFrom()` 不變，它本來就是帶著資料進那個表單。）
  `pair-modal` 的標題因此改成「手動配對」。`frontend/test/pairing-completeness.mjs` 逐項斷言這件事。

**配對抽屜（`pairing-modal`）的順序，由上而下（#63）**：

0. **步驟條 `#pair-stepper`**（2026-09-29，標題列下、捲動區外）：「找到對方 → 送出請求 → 比對指紋」，**純顯示**，沒有任何可按的東西，
   不改變下面 1–6 的順序與 id。由請求列推導（`pairStepperPhase()`，只看 `pending`／`awaiting-confirm` 的列）：
   沒有未決列＝第 1 步進行中；有 `outgoing`＋`pending`＝前兩步完成、第 3 步「等對方核准」；有 `awaiting-confirm` 或
   `incoming`＋`pending`＝前兩步完成、第 3 步進行中。每次 `renderPairRequests()` 就地改寫三個 `<li>`，元素不重建
   （測試 `inline-publish.mjs` §7）。核准或確認成功的 toast 帶「去公開 session」（`goToPublish()`：照關閉鈕的規則收抽屜、切到本機視圖）。

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
   同時**視窗 headline 不得只說「配對開放中」**——那讀起來像「好了」，而實際上視窗開著卻沒有入口，
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
   - 按下核准／確認／拒絕之後的 toast 用**這個視窗自己的中文句子**
     （`pairStepText()`，退回 `PAIR_TEXT.decided`），節點的英文 `nextStep` 以
     「（節點回報：…）」跟在後面——既不能只丟英文，也不能把它吞掉。
   - 已結束（approved/rejected/expired，含 `reason: displaced`）不進預設清單，
     `pair-requests-all` 勾選 → `all=true`。已結束的列才顯示節點的 `nextStep`。
   - **任何地方都不得有自動核准或略過比對的入口。**
6. 底部 footer：手動 5 欄位配對，是兩台連不上彼此時的退路。

右欄（`nodedetail`）：
- 節點詳情：名稱、完整指紋、核對說明、節點 ID／平台／配對時間／最後聯繫／可見的 session 數、「撤銷信任」+ 說明。
- 位址區：記錄中的位址與備援的說明句、位址清單（每列 `addresslabel`「首選」／「備援 n」＋ `addressinput` ＋ `removeaddress`，只剩一列時不給移除）、`addaddress`（滿 4 列隱藏）、`setaddress`「記錄位址」、格式說明。整組經 `SetNodeAddresses` 寫回。
- 「這個節點公開給我的 session」：四種 presence 狀態 + `sessionsWithheld` + 空 + 表格（SESSION／節點／PROVIDER／狀態／最後活動）。

### 3.4 設定頁

三個區塊，由 `settingsSection` 決定捲到哪一個：

- **背景服務**：狀態行、重新讀取、安裝／重新安裝、移除；展開表單**只有一個欄位：資料庫路徑**
  （`service-db`，留空＝節點預設位置）。其餘五個值不在這裡，是 #116 的決定——燒進 unit 檔會變成節點之外的
  第二份設定來源。安裝說明；輸出區 `<pre>`。節點沒在跑且未安裝時表單自動展開一次。
- **節點設定**：對外位址、允許區網、`-discover`、視為私有網段、自動喚醒。存的是節點**下次啟動**才讀的值，
  規則見 §7.8。節點回 `saved.peerListens`（ADR-005）時，對外位址是勾選清單（`#node-peerlistens`，每列一個
  `<input type=checkbox>`）：這台機器每個私有 IPv4、「其他位址」小標下的非私有位址、saved 裡有但機器現在沒有
  的位址（保留勾著）、非預設的 loopback；首選列標「從這裡廣播」，都沒勾顯示「都沒勾：只有這台機器自己連得到」。
  每列狀態比 `peerListeners` 與 `saved`／`settings.peerListens`：已開放／重啟後開放／重啟後關閉／沒開放＋原因
  （`address_gone`、`port_in_use`、其餘附節點的 message）。舊節點（沒有 `peerListens`）保留單選下拉
  `#node-peerlisten`，只送 `peerListen`。測試：`frontend/test/listen-addresses.mjs`。
- **外觀**：背景照片與數字雨兩個開關，數字雨預設關閉，見 §9；語言；「顯示首次設定」（`settings-show-onboarding`，§3.2）。

### 3.5 覆蓋層（8 個：3 個抽屜 + 5 個對話框）

抽屜（`.drawer`，從右側滑出）：
- `inbox-modal` 收件匣：**三個分頁**（收件匣／送出紀錄／喚醒紀錄）。警語（資料不是指令；「自稱」後是寄件者自選）、meta、
  訊息列（寄件者分兩半：驗證過的 node id 用 `fingerprint` 樣式，自選的 session 用 `claimed` 樣式，中間「自稱」）、清空。
- `pairing-modal` 配對：把 §3.3 左欄的配對模式與候選清單裝進抽屜。
- `notify-modal` 通知紀錄（2026-09-29）：鈴鐺打開，唯讀，見 §3.1「通知三層」。不在 `MODAL_IDS` 裡。

對話框（`.modal`）：卡片本身捲動，`.modal-actions`（每個對話框的最後一塊）`position: sticky` 貼在卡片底部，
900×760 下內容比卡片高時動作鈕仍在可視範圍內（#194）。
- `pair-modal` 手動配對（只從抽屜頁尾或候選列進來，見 §3.3）：說明（`ah node`、指紋逐組相符）、五個欄位、prefill note、本機指紋、送出。
- `audience-modal` 設定公開對象：套用到 N 個；三種 mode radio；指定節點的 ID 輸入；四個旗標；套用。
  **多選時四個旗標一律從 off 開始；單選時載入那個 session 自己的現值**（測試 `audience-dialog.mjs`）。
  單選且 mode `selected` 時，已授權但不在 `state.nodes` 的節點另列一列「這次沒讀到配對清單中的這台機器」
  （`audience.unlistedNode`），預設勾著，取消勾選才會撤銷；否則套用會把它靜默撤掉（#194）。這一列不說
  「已不在配對」：`RevokeNode` 在同一交易刪授權、`SetAudience` 拒絕未配對節點，所以它實際只出現在 `Overview`
  的配對清單讀取失敗、`nodes` 回 `[]` 時（此時節點仍配對著）。可達的 `Overview` 帶 `error` 即表示這種失敗，
  存進 `state.nodesError`，對話框以 `audience.nodesReadFailed` 說明讀取失敗，不顯示「還沒有配對任何機器」。
  **兩個**情境 preset（2026-09-29 拿掉「只讓他們看見」：對方看得到卻不能寫訊息，等於沒給它任何事做），只寫三個訊息旗標
  （能留訊息：`acceptMessages`；留訊息並喚醒：`acceptMessages`＋`allowOutbound`＋`autoWake`），`exportCwd` 保留現值。
  「自訂」（`audienceFormIsCustom()`）：mode 不是 `none`，**而且**旗標不是兩個 preset 之一——
  所以已公開但全部 off 的 session 開啟即展開進階區並顯示「自訂」，而 mode `none` 一律不算自訂、不展開；換 mode radio 會重算這一句。
  **mode `none` 套用時四個旗標一律寫 false**（`readAudienceForm()`，含 `exportCwd`，與選單的「不公開」相同）：單選照樣載入現值，
  所以勾選框可能還勾著，此時 preset 下方多一句 `audience.noneClearsFlags`（套用時四個旗標一律關掉）；
  mode `none` 時兩個 preset radio 都不勾（`syncAudiencePreset()`：勾著的框湊巧符合某個 preset 也不勾——套用寫的是全關，
  不是那個 preset；測試 `audience-dialog.mjs`）。
  mode 不是 `none`、`audience-cwd` 勾著、進階區收合時，preset 下方另有選單同一句「含工作目錄」（`popover.withCwd`）——
  符合 preset 的 session 開啟時進階區是收合的，工作目錄的勾選框在裡面看不到；展開進階區（`toggle` 事件）這句就收起，
  再收合又出現（測試 `inline-publish.mjs`：未公開但帶 `exportCwd` 的 session 從對話框公開、已公開帶 `exportCwd` 的 session 在對話框改不公開）。喚醒的說明 `audience-autowake-note` 在進階區**外面**，除了只有
  Claude Code 的選取（那裡直接說叫不醒），第一行一律是喚醒備註 `wake.caveat`，節點沒帶 `-auto-wake`、Codex、Claude 的句子接在後面。
  喚醒備註另外出現在：行內選單的喚醒選項下、旗標欄的 ⚠、套用喚醒後的 toast、設定頁節點設定「允許訊息自動喚醒」的第二行說明
  （第一行「每個 session 仍要各自打開。」不變，兩句不重複）。
- `mcp-modal` MCP 設定：**列上已無入口**（§10），由 `openMCPConfig(sessionId)` 開啟，顯示該 session 的 `.mcp.json` 片段，文案說明 per-project 與 `--outbound` 的限制。
- `modal` Heartbeat 預覽：說明 + `<pre>`。
- `confirm-modal` 是非題（`askConfirm({title, body, confirmLabel, danger})`，回 Promise<boolean>）：
  所有「做了就回不去」的動作都在這裡問，**不用 `window.confirm`／`alert`／`prompt`**——macOS 版
  WKWebView 的 UIDelegate（Wails v2 `WailsContext.m`）只實作了 `runOpenPanelWithParameters`，
  沒有 `runJavaScriptConfirmPanelWithMessage`，WebKit 因此把每個 `confirm()` 當成按了取消、畫面上什麼也不出現。
  Esc、取消、點背景都回 false——點背景只算按下與點擊都在背景、按下晚於開啟 400ms、且不是連點第二下的那一次
  （對話框在第一下 click 裡就蓋滿視窗，雙擊的第二下必落在背景）；`danger` 時確認鈕是紅色且焦點預設在「取消」；body 以 `pre-line` 保留換行；
  z-index 高於抽屜（收件匣抽屜會從裡面問）。同時只有一題：開著時再問的第二題排在後面，第一題答完才出現（不會把第一題回 false）；
  開著時 Tab／Shift-Tab 只在「取消」與確認鈕之間循環，不會走到背景。

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
| availability=openNotAnnouncing | 視窗畫成**開著**（summary pill「配對開放中 · m:ss」）＋位址提示；候選區同 `off` 的說法 | 「未啟用」、「配對狀態讀不到」 |
| `state.peerAddress` 是 loopback / `0.0.0.0` / `::`，**或整個欄位不存在**（預設節點） | 「還沒有人連得進這台機器」＋「允許區網連線」＋跳設定按鈕；`copy-pair-address` disabled；headline 不得只說「配對開放中」 | 把 `127.0.0.1:7463` 當成對方要輸入的位址印出來；把整塊 `#pair-here` 藏起來 |
| `state.peerAddressReachable === false` | 補救那一段 ＋ 節點自己的 `peerAddressProblem` 當次要細節行 | 用前端自己的猜測蓋掉節點的判定 |
| `state.peerAddress` 非空且可達 | `#pair-here` 一律顯示，**有沒有廣播都顯示**；旁邊那句依 `state.notice` 有無而不同 | 只在沒廣播時才顯示 |
| 抽屜標題 `#pairing-sub` | 依 `announceableAddresses` 換句子 | 不廣播的節點上出現「開啟後同網段的人都會知道」（兩種寫法都算，有逗號沒逗號） |
| 拒絕／核准／確認的 toast | 中文句子；節點英文 `nextStep` 在括號裡 | 只有節點的英文 |
| `reason: fingerprint_mismatch` | 指紋不一致的專屬句子 | 跟一般拒絕同一句 |
| 送出請求被 `PEER_PAIRING_BUSY` 拒 | 節點原文（對端自己的理由＋補救）；**不得**出現錯誤碼 | 本地自己寫的一句話（它蓋掉的是兩種不同的 429） |
| 配對請求列（未決） | 兩組指紋、節點給的標籤、`PAIR_TEXT.compare`、按鈕在指紋**下面** | 只顯示一組指紋；`ah pair approve`（GUI 裡跟按鈕自相矛盾） |
| 配對請求列（已結束） | 一句結果 + 節點的 `nextStep` | 核准／確認按鈕 |
| 配對面板任何位置 | | 「自動核准」「略過比對」「全部核准」「不比對」 |
| availability 未知 | 「不可信」、錯誤原文 | 「-discover」「機器在廣播。」 |
| 配對開放中 | headline 含「開放中」；倒數含 `:` | 到期時「0:00」 |
| 配對到期 | 「已到期」，且觸發一次 `Pairing()` | |
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
- 模組只能註冊**四個** `setInterval`：5 秒 pairing 輪詢（僅區網視圖，或首次設定精靈第 2 步在畫面上）、2 秒配對請求輪詢
  （僅區網視圖**且配對抽屜開著**，或精靈第 2 步在畫面上；`state.busy` 時跳過——每次讀都會讓節點去對端輪詢）、1 秒倒數（同 5 秒的兩種情況；精靈裡不顯示數字，只處理歸零）、
  15 秒背景重讀清單（`interactionInProgress()` 為真時跳過；#114 曾經整個視窗停在 0 筆而節點正服務 1083 筆）。
  待處理列的配對請求讀取騎在這個 15 秒 tick 上（§2 `PairRequests`），**不是**第五個 interval；
  toast 的 6 秒自動收起是 `setTimeout`，不是 interval（`notifications.mjs` 斷言仍然只有四個）。
- `OpenPairing` 呼叫參數必須是 `[0]`。
- 首次設定精靈（§3.2）不新增 interval、不新增寫入綁定的呼叫路徑：它的每個寫入都是既有函式（`runServiceQuickAction`、`restartNode`、
  `applyPeerListenRepairFromCard`、`applyAudienceChoice`、`discoverSessions`，第 2 步的 `openPairingWindowIfNeeded`、`releasePairingWindow`、
  `sendPairRequest`、`decidePairRequest`、`openPairModal`）。第 2 步的候選列與請求卡片套上面候選列／請求列的同一條 element identity 規則與指紋例外
  （同一個 `reconcileRows()`），「任何地方都不得有自動核准或略過比對的入口」也包括它。開著時待處理列整個隱藏；`state.busy` 期間它的每顆寫入按鈕 disabled；
  它顯示的 session 標題、工作目錄、provider 與節點的原始錯誤都以 `textContent` 進 DOM。
- `inbox-clear` 在 `askConfirm` 回 false（取消／Esc／背景）時不呼叫 `ClearInbox`；回 true 時呼叫並把結果畫在抽屜內（測試 `confirm-dialog.mjs`）。app.js 不得呼叫 `window.confirm`／`alert`／`prompt`（同一個測試逐字檢查原始碼）。
- 公開對象對話框多選開啟時四個旗標為 false，且 `readAudienceForm()` 回傳四個 false；單選開啟時四個旗標是該 session 的現值。mode 為 `none` 時 `readAudienceForm()` 不論勾選框一律回傳四個 false（與選單「不公開」相同）。

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
   2026-09-29 拿掉 `banner`，加了 `toasts`、`attention`、`btn-bell`、`bell-n`、`notify-modal`、`notify-list`、
   `notify-close`、`select-all-cell`、`motion-why`。測試讀「最新一則 toast」與待處理列都經過
   `frontend/test/fixtures/toasts.mjs`（`latestToast()` 的 `textContent` 只取標題與第二行，不含 ✕ 與動作鈕文字）；
   `dom-shim.mjs` 多了 `remove()`、`setAttribute()`／`getAttribute()` 與父節點追蹤，因為 toast 是一則一則移除的
   （整疊重建會讓其他 toast 的倒數條重來）。
   2026-09-30 拿掉 `onboarding`、`onboarding-dismiss`、`onboarding-steps`、`onboarding-alldone`（清單卡片），加了 `first-run`、
   `first-run-rail`、`first-run-steps`、`first-run-later`、`first-run-stage`、`btn-resume-setup`；`settings-show-onboarding` 保留 id，
   意思變成「顯示首次設定」。
   2026-09-29 第二段拿掉 `select-all-visible`、`select-label`，加了 `audience-popover`、`pair-stepper`；`btn-audience`
   保留 id，意思變成「公開 ▾」（開行內選單，不再直接開對話框）。`dom-shim.mjs` 的 `querySelectorAll("#view-switch [data-view]")`
   從 index.html 解出三個分頁按鈕。

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

**2026-09-23 狀態（分支 `feat/desktop-ux-simplify`）**：第 10 條已處理（配對抽屜三步，左欄只剩主按鈕與一行狀態）；
第 9 條早已是現況（收件匣是抽屜、heartbeat 是對話框），這次沒動；第 8 條部分處理——FLAGS 只在已公開的列顯示，
沒有展開列；第 7 條的未讀數早已由 `held` 計數處理，這次只把沒有訊息時的按鈕縮成圖示。
第 1–6 條這次沒碰。另外這次處理了清單外的問題：每個狀態兩到四句解釋、node/machine 與 announce/broadcast 混用、
「配對視窗」當名詞。

**2026-09-29 狀態（分支 `feat/desktop-notify-inline-publish`，第二段：常用流程三次操作以內）**：第 5 條完成——選取列只在本機視圖、
有選取才浮出，而且變成批次列（公開 ▾／收回公開／取消選取）；第 8 條再推一步——公開對象欄本身就是設定入口（行內選單），
旗標欄多了喚醒 ⚠。清單外：背景服務從待處理列與標題列一鍵安裝／啟動；配對抽屜有步驟條；分頁是鍵盤到得了的 tab 按鈕；
公開對象對話框剩兩個 preset。點擊數（從起點到完成）：公開一個 session 2（列上的公開對象 → 選項）；批次公開 3 起
（勾選 N 列 → 公開 ▾ → 選項，勾選每多一列多一下，用表頭全選則固定 3）；發起配對 3（區網分頁 → 配對另一台機器… → 候選列的送出配對請求）；
核准對方請求 2（待處理列「比對並核准」→ 核准）；啟動背景服務 1（待處理列或標題列 pill）；讀收件匣 1（列上的收件匣）。

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

**清單版（ADR-005，節點回 `saved.peerListens` 時）。** 四條規則照舊，但以集合比較：
(1) 基準是 `saved.peerListens`，`samePeerListens()` 以集合比較（空清單＝`127.0.0.1:7463`），集合相同不送；
`checkedPeerListens()` 保留 saved 的順序、新勾的接在後面，所以取消再勾回首選不會換掉廣播位址。送出一律是
`peerListens`（都沒勾送 `["127.0.0.1:7463"]`），絕不送 `peerListen`。
(2) loopback 逐項看主機（`isLoopbackListen`），`127.0.0.1:9999` 保留一列、勾著、不觸發撤回警告；loopback 與網路位址
同時勾會先警告（節點會拒絕）。
(3) `didNotStick` 對 `peerListens` 做集合比較；重啟後 saved 裡有、`peerListeners` 沒說 bound 的網路位址，
在同一則 toast 另外用一句（`nodeSettings.savedNotOpen`）說出，toast 不再標成單純成功。`NODE_SETTING_FLAGS.peerListens`
是 `peer-listen`，所以固定該旗標的服務單元一樣會先 `askConfirm`，問句用清單自己的標籤「對外位址」／Listen addresses。
(4) 勾位址不勾允許區網、勾允許區網不勾位址、取消允許區網不取消位址（沿用 `warnWithdraw`）；非私有列勾選時
`suggestPrivateRange` 以最近勾選、仍勾著的非私有列為來源，說明多一句「對方機器也要宣告同一個網段」。
render 不寫任何表單欄位（`frontend/test/listen-addresses.mjs` 逐條反證）。

**規則 4 與配對抽屜的第一步。** 那一步要做的事正是「一鍵讓這台機器連得到」，而那一鍵在允許區網是
關著的時候一定會把它打開。規則 4 管的是**表單不因為另一個欄位的值自己動手**；它不禁止一顆使用者親手
按下、而且標籤上寫明會動到哪些開關的按鈕——程式碼裡本來就有這個模式，連理由都寫在
`peerListenRepairs()` 的註解裡：「只有在按下去會打開它的時候才在標籤裡指名那個開關。這個視窗不會背著
任何人勾那個框。」

（這一步原本是首次啟動清單的第三步；UX 簡化〔#193〕把它移進配對抽屜，清單剩三步，不再讀節點設定。2026-09-30 的首次設定精靈
第 1 步又用上它：按鈕上方那一行寫明位址與影響、使用者在兩個以上的私有位址之間自己選，按下去走同一個 `applyPeerListenRepairFromCard()`，
所以規則 1–4 與固定旗標的詢問都只有這一份實作，§3.2。）

所以抽屜第一步的按鈕**就是用 `peerListenRepairs()` 產生的**（`pairHereRepairs()`），餵給它一個合成的
`{reason: "loopback", address: <存下來的 peerListen>}` 加上節點回報的位址清單，按下去走
`applyPeerListenRepair(option)`。這樣有四件事是免費得到的：標籤照規則 4 指名每一個會被設定的旗標
（看的是節點**存下來的** `allowLan`，不是設定頁上還沒存的勾選框）；存檔走表單那條路，所以驗證、
重啟、以及規則 3 的「重啟後實際持有的值」比對都只有一份實作；「就先只在本機」這個選項在這裡被濾掉
（這一區存在的理由就是對方連不進來）；以及**光是 render 抽屜不會去寫 `#node-allow-lan`**
（`frontend/test/onboarding.mjs` 對抽屜逐項斷言）。前提是節點設定已經讀進來過——
`applyPeerListenRepair` 填的是真正的表單——所以 `ensurePairingNodeSettings()` 在抽屜打開且
`state.nodeSettings` 還是 null 時呼叫一次 `loadNodeSettings()`（一個視窗生命週期只問一次；讀取失敗、
沒拿到基準時才放開，下次開抽屜再問）。

清單版節點多三件事：可連線時 `peerListeners` 裡每個 bound 的網路位址一列（介面＋各自的「複製」），附「哪一個都能用；
對方輸入跟這台機器在同一個網路上的那一個」；saved 裡有但沒開放的位址一行 muted 點名＋「前往節點設定」；
不可連線且這台機器有 ≥2 個私有位址時，`pairHereRepairs()` 在單一位址按鈕（最多 3 個）前面放
「全部開放：{清單}[，並允許區網連線]」（最多 4 個位址，子句照規則 4 只在允許區網還關著時出現），
`applyPeerListenRepair()` 收 `option.peerListens` 整份清單，並照它的順序送（下一個 port 的修復保留原本的首選）。
舊節點不提供「全部開放」。抽屜開著時 `refreshPairListeners()` 另讀節點設定給第一步用（開抽屜時一次、之後隨配對輪詢
最多每 10 秒一次），只更新顯示用的 `pairListenView`，**不重畫設定表單**——節點每 30 秒重試綁定，視窗啟動時讀到的
狀態會過期，而重畫表單會蓋掉使用者還沒存的勾選。

**規則 3 與服務單元固定的旗標。** 服務單元每次啟動都帶的旗標（`ServiceStatus().pinnedSettings`）會
在重啟後蓋掉這次存的值。存檔時**只有這次改動碰到被固定的欄位**才問要不要用同一個資料庫重新登記服務；
使用者取消、或讀不到服務用的資料庫路徑（重新登記可能換掉節點身分）時，被固定的欄位不送、其餘照存，
另一則警告 toast 列出沒存的欄位，排在存檔結果之後（`frontend/test/service-recovery.mjs` §6b）。問句（`askConfirm`）列出單元**全部**固定的欄位
並標明這次改到哪些（「（這次改到）」／「(this save)」），因為重新登記只帶資料庫路徑、會一次解除全部；
`InstallService` 失敗時視同沒重新登記（回 false），被固定的欄位同樣不送（#194）。

## 8. 新需求（2026-09-11，owner 指定）

**列動作「複製 resume 指令」。** 依 provider 產生指令並寫入剪貼簿：

| provider | 指令 | 依據 |
|---|---|---|
| claude | `claude --resume <providerSessionId>` | `adapter/claude.go` 讀 jsonl 的 `sessionId`，與 `claude --resume` 接受的 id 相同（已用本機檔案核對） |
| codex | `codex resume <providerSessionId>` | thread id |

- 剪貼簿寫入用 #112 的 `CopyText` 綁定，同樣受序號守衛：遲到的回應不得寫剪貼簿。
- ~~複製後的回饋要帶工作目錄提示：「在 <cwd> 執行」，cwd 為空則省略。~~（2026-10-01 取代，見下）

**2026-10-01 更新（擁有者指定：「resume 複製時只給 session id 就好，前面指令不用，工作目錄也要可以複製」）。**
按鈕改名「複製 ID」／「Copy ID」，複製的是**純 ID**：`providerSessionId`；沒有這個欄位時退回 `id` 第一個冒號之後的部分——
兩者依構造相同：節點以 `model.SessionID(provider, providerSessionID)` 組出 `<provider>:<providerSessionId>`，
`registry.validateSessionFields` 拒絕 id 與此不符的 session，`model.ValidateProviderSessionID` 拒絕含冒號的 providerSessionId，所以 id 裡只有一個冒號。上表的指令只出現在 tooltip 裡說明用途。
工作目錄改由工作目錄欄自己複製（§3.2），所以回饋不再帶「在 <cwd> 執行」；回饋顯示在原地（§2 `CopyText`）。
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
- **ADR-005（多位址，PR #196）**：`settings`／`saved` 另帶 `peerListens`（清單，第一項＝`peerListen`，新節點
  至少一項）；新增 `peerListeners: [{address, state: bound|failed|pending, reason?, detail?, message?}]`（即時讀）；
  `peerListenProblem` 只在**全部**失敗、退回 loopback 時出現；`restartRequired` 比設定的清單、不比綁上的位址。
  PUT 收 `peerListens`；**只送 `peerListen` 會把整份清單換成一個**，所以視窗對新節點只送清單、對舊節點只送單值。
  owner API 拒絕未知欄位，視窗靠回應有沒有 `peerListens` 判斷。`desktop/client.go` 的 `NodeSettingValues.PeerListens`
  是 `omitempty`，舊節點經過 binding 後仍然是「沒有這個欄位」。已配對節點另有 `alternateAddresses`
  （`TrustedNode.Alternates`），節點詳情顯示成「備援位址」。
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

1. 列動作是兩顆，`frontend/test/mcp-config.mjs` §1 斷言 `mcp` class 的按鈕數為 **0**、`inbox` 與 `copyid`（原 `resume`，2026-10-01 改名，§8）各為 2。放回按鈕會讓測試失敗，所以那是個明確的決定而不是意外。
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

不進表格的還有一種：**節點說的話**。`nextStep`、`notice` 與節點的拒絕原文是資料，原樣顯示；拿節點的散文當 key，節點改一次措辭就靜靜對不上了。要翻譯節點的一句話，就讓節點另給一個穩定代碼（如候選清單的 `noticeCode`），以代碼當 key、散文當退路。`desktop/nodeprocess.go` 那四句原本是中文的輸出已經直接改寫成英文——它們跟 `ah` 自己的輸出並排進同一個 `<pre>`，而那邊本來就是英文；四句話不值得一層 Go 的 i18n。

## 12. 詞表（2026-09-23，分支 `feat/desktop-ux-simplify`）

視窗面向使用者的用詞，en 與 zh-Hant 各一組，兩張表一起守：

| 概念 | en | zh-Hant | 不再用 |
|---|---|---|---|
| 另一台已配對或要配對的電腦 | machine（the other machine, paired machines） | 機器（另一台機器、已配對機器） | node、節點（指對方時） |
| 這台電腦 | this machine | 這台機器 | this node、本節點（指這台電腦時） |
| 在網段上通告自己 | broadcast | 廣播 | announce、宣告 |
| 可配對的狀態 | Pairing open · mm:ss | 配對開放中 · mm:ss | pairing window、配對視窗（當名詞） |

**node 還留在哪裡。** node 是 `agenthub-node` 這個行程的名字，所以講那個行程本身的句子保留它：設定頁的
背景服務、節點設定（Node settings）與本機身分三區，「啟動／重新啟動節點」與它的狀態行，以及引用旗標的句子
（`-discover`、`-auto-wake`、`-display-name`）。技術欄位的值與標籤也保留：Node ID／節點 ID、`ah nodes`。i18n 的 key 名（`network.pairedNodes`、`audience.cell.nodeCount.*`）
不改：key 是程式與測試的介面，改名只會讓 diff 變大而使用者看不到。

**首次設定精靈的例外。** 精靈（§3.2）的中文沿用擁有者核可的 mock，用「這台電腦」「另一台電腦」而不是「這台機器」「另一台機器」；
英文照本表用 machine。這是刻意的，要統一的話改 `firstRun.*` 那幾句即可。

**文案規則。** 每個狀態一句主文；「為什麼」與操作細節收進可聚焦的 `<details class="why">`（摘要「說明」／Details，
app.js 的 `whyDetails(key)`，靜態頁面的三段在 index.html），內文是那一兩句本身。視窗裡**不指名 repo 檔案**
（安裝版沒有 `docs/`，#194；`test/i18n.mjs` 檢查兩張字表沒有 `docs/*.md`），也不把說明放在段落的 `title`
（鍵盤與螢幕報讀碰不到）。完整版仍留在 `docs/desktop-window.md` 給 repo 讀者。§4 的語意（資料不是指令、
四種 peer 狀態四句不同、四種 availability、inbox 的 loading 與空清單、contested/duplicate 旗標）只縮短、不刪。
