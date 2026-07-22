# tmux-qs

`tmux-qs`（tmux quick switcher）是一個用 Go 寫的 tmux session 快速切換 TUI。
介面與操作模式對齊 `~/bin/workspace`（fzf popup 流程），但完全以 tmux 原生命令
直接實作，不依賴外部 fzf / sesh 工具，並在它的基礎上額外提供：

- 顯示與切換工作區的 **git branch**
- 偵測並標示 **「等待中的 AI agent」**（claude / opencode / aider / …）
- 將文字 prompt 直接送進指定的 session / pane（`Alt-Enter`）
- 依 pane 前景程式篩選 snippet，直接送出（`Space`／`Ctrl-s`）
- 多重 tmux server 掃描、SSH 主機連線、Command Palette、檔案搜尋、
  版面範本（Template）
- 完整 fuzzy 過濾、**frecency 排序**（頻率 × 最近度）、釘選、標籤 / 群組過濾、滑鼠操作
- **可設定的 keybindings**（`[keybindings]`）、**輸入框歷史**（`Ctrl-↑/↓` 叫回）
- **window 層級導航**（`Ctrl-v` 列出選定 session 的 window 直接跳）
- **砍掉 session 可復原**（`Alt-u` 重建剛砍掉的 session，含 layout）
- **可選把「有等待 agent」的 session 自動置頂**（`[waiting] float_to_top`）
- 進階 **Resurrect**：保存 window/pane layout、cwd 與執行中程式，並可週期自動存檔
- 離開時不開 TUI 的 CLI 捷徑（`--last` / `--back` / `--forward`）

依賴外部指令：`tmux`（硬依賴）、`zoxide`（可選，用於 `Ctrl-x` / `Alt-r` 來源）、
`git`（branch 偵測、自動偵測 tag）、`xdg-open`（`Alt-o` 開啟 git remote）。
Terminal 需支援 OSC 52（主流 terminal 都支援）以使用剪貼簿功能。

---

## 安裝

需求：

- Go 1.21+（僅 build 時需要）
- `tmux`（執行時唯一硬依賴）
- `zoxide`（可選；`Ctrl-x` 與 `Alt-r` 來源會用到）
- 支援 OSC 52 的 terminal（iTerm2、Alacritty、kitty、WezTerm、xterm…），用於剪貼簿
- 支援 `xdg-open` 或其他 URL 開啟工具，用於 `Alt-o` 開啟 git remote
- `git`（branch 偵測、自動偵測 tag、版本控制等功能會用到）

建置：

```sh
go build -o ~/bin/tmux-qs .
```

或用 Makefile：

```sh
make build      # 編出 ./tmux-qs
make install    # 編出並安裝到 ~/bin/
make test       # 跑全部單元測試
make version    # 印版本（-ldflags 注入）
make clean      # 清除 ./tmux-qs 與其他 build artifact
```

版本是 build-time 注入的：

```sh
go build -ldflags "-X main.version=v1.2.3" -o tmux-qs .
```

沒注入時 fallback 為 `dev`，所以 `tmux-qs --version` 對 local dev 也有意義。

### tmux 綁定

建議在 `~/.tmux.conf` 加入：

```tmux
# TUI 的色彩、滑鼠與 OSC 52 剪貼簿支援
set -g default-terminal "tmux-256color"
set -as terminal-features ",xterm*:RGB"
set -g mouse on
set -s set-clipboard on

# prefix + s：在 binding 當下保存 caller client/pane/cwd 後開 popup
bind-key s run-shell -b "TMUX_QS_CLIENT=#{client_name} TMUX_QS_CALLER_PANE=#{pane_id} TMUX_QS_CALLER_CWD=#{q:pane_current_path} tmux-qs --popup=center,80%,70%,border-native"
# 直接開啟目前 pane 的 snippet 清單
bind-key S run-shell -b "TMUX_QS_CLIENT=#{client_name} TMUX_QS_CALLER_PANE=#{pane_id} TMUX_QS_CALLER_CWD=#{q:pane_current_path} tmux-qs --snippets"
# 不需要 prefix 的 Alt-q：開啟；再次觸發則關閉並切回上一個 session
bind-key -n M-q run-shell -b "TMUX_QS_CLIENT=#{client_name} TMUX_QS_CALLER_PANE=#{pane_id} TMUX_QS_CALLER_CWD=#{q:pane_current_path} tmux-qs --toggle"
```

如果 terminal 不是 xterm 相容類型，請把 `xterm*` 改成實際的 `$TERM` pattern。
在 shell 直接打 `tmux-qs` 也會自動開 popup（偵測到 `TMUX` 環境變數時）。

也可以讓 tmux 直接建立 popup；這適合不需要 `--toggle` 的獨立按鍵。此時要把
caller identity 一起傳入，避免 snippet 或多 client 操作失去原始 pane：

```tmux
bind-key p display-popup -E -w 80% -h 70% \
  -d "#{pane_current_path}" -T " tmux-qs " \
  "TMUX_QS_POPUP=1 TMUX_QS_CLIENT=#{client_name} TMUX_QS_CALLER_PANE=#{pane_id} TMUX_QS_CALLER_CWD=#{q:pane_current_path} tmux-qs --no-popup"
```

### 快速連按 M-q（`--toggle`）在系統 lag 下的行為

`--toggle` 設計成「按一次開 picker、再按一次關掉並切回上一個 session」。但
`run-shell -b` 是非阻塞的，**快速按兩下**會 spawn 兩個 `tmux-qs` 行程同時跑。
在系統 lag 下這個 race window 會被拉大，會看到三種問題：

1. `pgrep -x tmux-qs` 抓到 parent + child 兩個行程，舊版會把 parent 也殺掉
2. 第二次按的行程讀 `~/.cache/tmux-qs/last-session` 時，第一次按的 popup child
   還沒把檔案寫好 → `no last session recorded` → 舊版會 `exit 1`
3. SIGTERM 風暴讓 tmux 的 `display-popup` 機制進入怪狀態

新版本加了三層保護：

| 保護 | 機制 | 解決的問題 |
|------|------|------------|
| 1. flock | `~/.cache/tmux-qs/toggle.lock` 序列化兩次 invocation | 第二次按的進程看到 lock 被持就 return，不會搶著跑 |
| 2. 精準殺 popup child（Linux） | 透過 `/proc/<pid>/environ` 同時比對 `TMUX_QS_POPUP=1` 與 `TMUX_QS_CLIENT` | 只關閉目前 tmux client 的 popup child，不影響其他 terminal/client |
| 3. graceful fallback | `lastSessionSwitch` 失敗時改走 picker、不 `exit 1` | 第二次按的使用者意圖本來就是「給我 picker」，exit 反而打斷流程 |

實作細節在 `toggle_lock.go`、`popup_child.go`、`main.go:runToggle`。Layer 1
的 `tryAcquireToggleLock` 用 `syscall.Flock + LOCK_EX | LOCK_NB`，沒拿到
lock 就 early return；Layer 3 在等 popup child 死亡最多 200ms 後 sleep 50ms
（給 OS 收 zombie 的時間），再嘗試 `lastSessionSwitch`。

macOS / BSD 沒有 `/proc`，Layer 2 退回舊行為（殺全部），但在那些平台上
fork-bomb-style 的系統 lag 極少見，不影響日常使用。

驗證：連按 M-q 兩下、三下、四下都不應該出現 `exit 1` 或 zombie popup。
如果想強制重現 lag 場景：`sudo tc qdisc add dev lo root netem delay 200ms`。

---

## 命令列介面

```
Usage: tmux-qs [options]

  --popup[=OPTS]      在 tmux popup 內開啟（預設行為，與 fzf 語法相容）
  --no-popup          在目前終端直接開 TUI（不開 popup）
  --toggle            切換：已開啟就關閉並切回上一個 session，否則照常開
  --last              不開 TUI，直接切回上一個 session（單鍵 Alt-Tab）
  --back              不開 TUI，回到 visit stack 上一個 session
  --forward           不開 TUI，前進到 visit stack 下一個 session
  --server=NAME       指定 tmux server（等同 tmux -L）
  --socket=PATH       指定 tmux socket（等同 tmux -S）
  --all-servers       掃描所有正在執行的 tmux server，合併顯示 sessions
                      （每個 session 前面會加上 "[server] " 前綴）
  -v, --version       印版本後離開
  --vim               啟用 vim 子模式（預設關閉）
  -h, --help          印說明後離開
```

### Popup 語法

像 `fzf --popup` 一樣：在 tmux 內執行時，程式會自動把自己包進
`tmux display-popup`，預設位置 `top,70%`。OPTS 語法與 fzf 完全相同：

```
--popup=[center|top|bottom|left|right][,SIZE[%]][,SIZE[%]][,border-native]
--no-popup    # 直接在目前終端執行，不開 popup
```

- 一個 SIZE：top/bottom 為高度，left/right 為寬度，center 為兩者
- 兩個 SIZE：固定為「寬,高」
- `border-native`：保留 tmux 原生邊框（`-B` 在 tmux 中代表「無邊框」，不可用來開啟邊框）
- 預設 `top,70%`

Popup 的輸入框固定在最上方，與 `--no-popup` 保持一致；篩選結果改變時
不會造成輸入框或清單垂直位移。

Popup 內執行 OSC 52 寫剪貼簿時，tmux 必須有 `set-clipboard on`，否則 escape
sequence 會被 tmux 攔截而送不到外層 terminal。

### 多 server 模式

`--all-servers` 會掃描 `/tmp/tmux-<uid>/` 下的所有 socket（同時納入 `$TMPDIR`
的對應目錄），合併所有有 session 的 server。`tmux-qs --all-servers` 內仍可以
用 `Ctrl-t`、`Ctrl-x` 等快捷鍵切換來源，但只能連回「目前已 attach 的 server」
的 session 之外的 session（會在背景暫時切換 server 完成 connect 後還原）。

預設行為：在 tmux 內執行時，會從 `$TMUX` 環境變數自動推導出對應的 server
的完整 socket path（`tmux -S <path>`），保證自訂 `-S` socket、popup 與外層都對話
到同一個 server。

### 守門檢查

- 偵測到目前 session 名稱為 `popup`（先前殘留的 popup session）→ 自動 `detach-client`
- Linux：偵測到目前 tmux client 已經有另一個 popup child → 自動離開；其他 client 不受影響
- 上述守門在 popup 子進程會跳過（由父進程做完，子進程只跑 UI）

---

## TUI 操作總覽

TUI 啟動時是一個由輸入框、提示列、列表組成的畫面。鍵盤輸入即時 fuzzy 過濾
列表。鍵盤 / 滑鼠可自由切換各種模式與來源；每一個鍵都會即時反映在畫面上。

### 模糊比對與排序

- **無輸入時**：列表依「目前來源」原始順序（`Ctrl-a` 與預設列表會把最近選
  過的 session 浮到最上面，**其它來源保留原順序**）
- **有輸入時**：依 fuzzy 分數排序，使用 fzf 的 `FuzzyMatchV2`（`path`
  scheme），所以 word boundary（尤其是 `/` 之後的路徑段第一個字）、連續字元
  會加分，字元跳躍（gap）扣分
  - 多詞查詢以空白分隔，每個詞都必須命中
  - **Smart-case**：查詢全小寫 → 忽略大小寫；查詢含大寫字母 → 該詞改為
    大小寫敏感（想用大寫縮小範圍時很有用）
  - 命中字元在 list 裡會以**反白高亮**顯示（多詞命中位置會去重）
- 路徑項目會以 `~` 取代 home prefix
- **效能**：比對重用 fzf 的 slab 並快取每個 entry 的字元表，所以連續打字時
  不會每個按鍵都對整個列表重新配置記憶體；列表很大（≥1500 筆）時比對會跨核心
  平行化

### Frecency 排序

每次按 `Enter` 連線時，session / path 名稱會寫入 `~/.cache/tmux-qs/recent.json`
（`$XDG_CACHE_HOME/tmux-qs/recent.json` 為主），記錄**選取次數**與**最後選取時間**。
`Ctrl-a` 與預設列表載入時依 **frecency** 分數排序——做法與 zoxide 相同：以選取
次數為基礎，乘上一個隨「上次選取多久以前」遞減的權重（<1h ×4、<1d ×2、
<1w ×0.5、更舊 ×0.25）；同一時間桶內再以最近選取時間打破平手。所以「常用但
不是剛用過」的 session 也會浮上來，而不是只看最後一次。其他來源（`Ctrl-t`、
`Ctrl-g`、`Ctrl-x`、…）保留原順序。

有輸入查詢時，fuzzy 分數仍是主排序鍵，frecency 只作為同分時的 tie-break。

### 模式（Mode）

TUI 內有多個畫面模式，由 `uiMode` 控制：

| 模式 | 進入 | 用途 |
|------|------|------|
| `modeList` | 預設 | 主列表 |
| `modeBranch` | `Ctrl-b` | 列出選定 repo 的 git branches |
| `modeHelp` | `?` | 按鍵說明覆蓋層 |
| `modeFiles` | `Alt-f` | 在選定目錄下 fuzzy 搜尋檔案 |
| `modeAgentSelect` | `Alt-v` / `Alt-t` | 選擇 AI agent / 套用 template |
| `modeTag` | `Ctrl-,` | 依標籤過濾 |
| `modeGroup` | `Ctrl-;` | 依群組過濾（config 內 `group` 欄位） |

加上 `--vim` 時，清單模式會啟用 vim 雙模態（`vimInsert` / `vimNormal`），由
`Esc` 切換。預設關閉；此時 `Esc` 會直接離開 TUI。

### Vim 子模式

以 `--vim` 啟動時，`modeList` 內可在 vim 風格下操作：

- `Esc` / `i` / `a` / `/`：insert ↔ normal 切換
  - `Esc`：normal 模式按 Esc 離開 TUI；insert 模式按 Esc 進入 normal
  - `/`：進入 normal 之後按 `/` 直接 focus 輸入框並切回 insert
- normal 模式支援：
  - `j` / `k`：下 / 上移動（可加數字 prefix，例如 `5j` 往下 5 行）
  - `gg` / `G`：跳到第一 / 最後一行
  - `dd`：刪除選取 session（同 `Ctrl-d` 的雙按確認流程）；若有標記則批次刪除
  - `yy`：複製選定 entry 目錄（同 `Ctrl-y`）
  - `S`：把輸入框文字批次送到所有標記的 sessions（同 Alt-Enter 的多目標版）
  - `u`：清除所有標記
  - `Space`：開啟選定 pane 的 snippet picker
  - `:q` / `Ctrl-c` / `Esc`：離開 TUI
- normal 模式中輸入框游標會被隱藏、輸入框會以「-- NORMAL --」指示取代
  （若已輸入 count prefix，會顯示 `-- NORMAL 5 --`）
- **normal 模式的快捷鍵 fall-through**：除了上面列的 vim 專屬鍵（`j` / `k` /
  `G` / `gg` / `dd` / `yy` / `S` / `u` / `Space` / `Enter` / `Esc` / `Ctrl-c` /
  `i` / `a` / `/`）以外，**所有 insert mode 的快捷鍵在 normal mode 也有效**——
  例如 `Ctrl-y`（剪貼簿）、`alt+up` / `alt+down`（preview 捲動）、`?`（help）、
  `tab`（cursor 下移）、所有 `Ctrl-` 開頭的 source 切換、以及 `alt+` 開頭的
  操作。設計原則：vim 專屬語意只在會跟 vim 衝突的鍵上保留，其它一律跟 insert
  模式共享同一個 handler（行為、錯誤訊息、history 寫入都一致）。

---

## 完整按鍵一覽

### 移動 / 輸入

| 按鍵 | 功能 |
|------|------|
| 任意輸入 | fuzzy 過濾；無輸入保留原順序，有輸入依分數排序 |
| `Tab` | 切換 session mode 與 window mode（每個 pane 一行） |
| `Shift-Tab` | 上一個（不循環） |
| `Ctrl-n` / `Ctrl-p` | 下 / 上 |
| `↑` / `↓` 或 `Ctrl-j` / `Ctrl-k` | 下 / 上 |
| `Alt-j` / `Alt-k` | **跳到下一個 / 上一個已存在的 tmux session**（循環） |
| `Alt-←` / `Alt-→` | visit stack 上一個 / 下一個 session |
| `Alt-↑` / `Alt-↓` | 預覽面板上 / 下捲一行 |
| `PgUp` / `PgDn` | 預覽面板翻頁 |
| `Esc` | 離開說明 / branch / tag / agent / files 模式；vim normal 模式按 Esc 離開 TUI |
| `Ctrl-c` | 離開 TUI |

### 連線 / 編輯 session

| 按鍵 | 功能 |
|------|------|
| `Enter` | 連線：tmux session 直接切換；目錄若有 session 則切換、否則新建（必要時跑 layout script） |
| `Alt-Enter` | 把輸入框文字以 `tmux send-keys` 送進選中 session；若該 session 有 waiting agent pane，自動送進那個 pane，然後清空輸入框 |
| `Space` | 開啟 snippet 清單，目標是**目前 session**的 active pane；不受游標所在 session 影響 |
| `Ctrl-s` | 開啟 snippet 清單，目標是游標選定的 session active pane，或 pane/window 清單的精確 pane |

在 snippet 清單中，`Enter` 送出後關閉 tmux-qs；`Space` 送出後保留清單，方便連續送出。
系統提供智慧畫面萃取（自動擷取 `[y/N]` 回應、`FAIL:` 錯誤修復與剪貼簿內容）與組合式 Prompt 積木（`Block: ` 搭配 `Space`／`Y` 手把鍵進行無鍵盤 Prompt 拼接）。

`tmux-qs --snippets` 會直接開啟**目前 session active pane**的 snippet 清單，適合綁定遊戲手把按鍵。
| `Alt-n` | 建立新的空白 tmux session（用輸入框文字當名稱，空的話自動命名 `qs-<timestamp>`）並切換 |
| `Alt-q` | 離開 TUI 並切換到上一個 session（`--toggle` 的第二段） |
| `Ctrl-r` | 用輸入框文字重新命名選中 session（清空輸入框） |
| `Ctrl-d` | 砍掉選中 tmux session（**需連按兩次確認**）；若有標記則批次刪除；`Alt-c` 清理模式下可一次砍掉所有過期 session |
| `Alt-u` | **復原上一次砍除**：重建剛被 `Ctrl-d` 砍掉的 session（含 window/pane layout、cwd 與白名單內的執行中程式）；批次 / 清理刪除也能一次復原全部 |
| `Ctrl-v` | 列出選定 session 的所有 **window** 並直接跳到選中的那個（跳到該 window 的 active pane） |
| `Ctrl-↑` / `Ctrl-↓` | 叫回**輸入框歷史**上一筆 / 下一筆（`Alt-Enter` 送出、`Ctrl-r` 改名、`Alt-n` 新建時輸入的文字） |

### 多重選取

| 按鍵 | 功能 |
|------|------|
| `Space` | 開啟選定 pane 的 snippet picker；多選標記不再有預設按鍵 |
| `Ctrl-d` | 批次砍掉所有標記的 sessions（同樣需雙按確認） |
| `S`（vim normal） | 把輸入框文字批次送進所有標記的 sessions |
| `u`（vim normal） | 清除所有標記 |
| `dd`（vim normal） | vim 版的「砍掉選中 session」 |

### 切換資料來源

每個來源都會切換列表內容，但都會保留 fuzzy 過濾、recency 排序、branch 標註
等共同行為。

| 按鍵 | 來源 |
|------|------|
| `Ctrl-a` | 全部（tmux sessions + config + zoxide），依 recency 排序；fuzzy 同時比對 entry 文字與 git branch（輸入 `main` 會命中所有 main branch 上的 entry） |
| `Ctrl-t` | tmux 全部 session 內的 **每個 window / pane** 一行（顯示該 pane 的 cwd 與 term title），依 session / win / pane index 排序；Enter 直接跳到該 pane |
| `Tab` | 在預設 session mode 與 `Ctrl-t` 的 window mode 間切換 |
| `Ctrl-g` | config 內定義的 `[[session]]` |
| `Ctrl-x` | zoxide 全部目錄（`zoxide query --list`） |
| `Alt-r` | zoxide 中目前 session root 下的子目錄 |
| `Ctrl-f` | 列出當前 tmux session `cwd` 的子目錄（廣度優先，最多 30 個），選了之後在當前 session 開新 window，cwd 為選定路徑 |
| `Ctrl-w` | 只列「有等待中 agent」的 sessions（取自 watcher 快照） |
| `Alt-c` | 清理模式：路徑已不存在 / 7 天以上未活動的 sessions |
| `Ctrl-e` | active panes 視圖：跨所有 session 列出所有 pane |
| `Ctrl-h` | SSH 主機視圖：解析自 `~/.ssh/config` 的 `Host` 項目 |
| `Ctrl-o` | Command Palette（管理指令） |

### 過濾 / 模式

| 按鍵 | 功能 |
|------|------|
| `Ctrl-b` | 進入 branch 模式（列出選定 repo 的 local / worktree / remote-only branches） |
| `Ctrl-,` | 進入 tag 過濾模式（依 config `tags` 與自動偵測 tag 篩選） |
| `Ctrl-;` | 進入 group 過濾模式（依 config `group` 欄位篩選） |
| `Alt-p` | 釘選 / 取消釘選選中項目（釘選的會置頂並加上 📌 前綴） |
| `Alt-v` | 選擇要啟動的 AI agent（從 config `commands` 篩選已安裝者） |
| `Alt-t` | 套用指定的 template（從 config `[[template]]` 列表） |
| `Alt-f` | 檔案模式：在選定目錄下 fuzzy 搜尋檔案並以 `$EDITOR` 開啟 |
| `Ctrl-y` | 複製 entry 的目錄路徑到剪貼簿（OSC 52） |
| `Alt-o` | 開啟選定專案的 git remote URL（`xdg-open`） |
| `Ctrl-Space` | 切換 per-pane 詳情面板（顯示哪個 pane 在等、tty 多久沒動） |
| `?` | 切換說明覆蓋層（`helpText`） |

### 滑鼠

TUI 啟動時即自動開啟 `tea.WithMouseCellMotion()`。

- **左鍵點選**：直接點某個 row 跳 cursor
- **滾輪上 / 下**：cursor 上 / 下捲一列
- **source tabs 左鍵點選**：切換 `Sessions`、`All ^a`、`Waiting ^w`、`Tmux ^t`、`Panes ^e`、`Config ^g`、`Files ^f`、`Commands ^o`；tab 仍保留鍵盤快捷鍵提示
- 中鍵不處理（terminal 普遍把中鍵當 paste，避免衝突）

---

## 連線行為（Enter / Alt-Enter / Alt-v）

`connect()` 是選擇後的核心邏輯，決策樹大致如下：

1. **目標是執行中的 tmux session**：
   - 若有 `paneID`（從 `Ctrl-e` 選 pane 而來），先用 `select-pane` 跳到該 pane
   - 在 tmux 內 → `switch-client`；不在 → `attach-session`
   - 同時把「前一個 session」寫入 `last-session`，並推進 visit stack
2. **目標是設定檔內的 `[[session]]` 名稱**：
   - 解析出對應的 `path`，等同下面的「目錄」分支
3. **目標是 `ssh <host>` 條目**（由 `Ctrl-h` 載入）：
   - 以 `ssh <host>` 為啟動命令建立新 session
4. **目標是檔案 / 目錄路徑**：
   - 若已有 session 落在此路徑 → 切到該 session
   - 否則建立新 session（名稱以目錄 base name 衍生，若衝突自動加 `-N`）
     - 若目錄裡有 config 指定名稱的 layout script（預設 `.tmux-qs.sh` /
       `.tmux.sh`）且 layout scripts 啟用，會把該 script 送到新 session
     - 否則套用第一個匹配的 `[[template]]`（依 `detect_files` 判斷）
     - 若是檔案，會以 `$EDITOR` 開啟（fallback `nvim`）
5. **目標是目錄但仍無 session 對應**：
   - 若目前已 attach session 的 cwd 就是目標路徑 → 在該 session 內找已
     在那個目錄的 shell / editor pane（依 `pane_shells`），或開新 window
   - 否則在 attached session 內同樣地「找現有 pane 或開新 window」

### `Alt-v`（AI agent 啟動）

1. 列出已安裝在系統上的所有 `commands` 設定（透過 `lookPath` 偵測）
2. 選定後：
   - 計算目標目錄（先看 config session 對應路徑，再看路徑，否則從 session path）
   - 若無對應 session → 建立新 session 並把 `agentCmd` 送進去
   - 若已有 → 開新 window（命名為 agent 名）並送 `agentCmd`
3. 切到該 session

### 排程腳本（Layout Script）

在選定目錄的根目錄若有名稱符合 `layout.script_names`（預設 `.tmux-qs.sh`、
`.tmux.sh`）的檔案：

- 若檔案有執行權限 → 直接 `./<script>`
- 否則用 `bash <script>`

可用 `[layout] enable_scripts = false` 關閉此行為。Template 與 layout script
互斥：若找到 layout script，就不會套用 template；否則會套用第一個 `detect_files`
全部命中的 template。

---

## 等待中的 Agent 偵測

TUI 開啟期間，背景會以 `poll_interval`（預設 5 秒）對所有 tmux pane 做一次掃
描，偵測「agent 正在等待使用者回應」的 session / pane，並在對應 entry 右側
inline 顯示提示：

```
>  work            3m   ⚠ ⏳ waiting  · claude[prompt] ×2 · opencode[stuck]
   blog            1d   ⚠ ⏳ waiting  · claude[idle]
   scratch               · claude
   notes
```

每個 process 後面用 `[signal]` 標示觸發原因。session 旁邊的 `3m` / `1d` 是
上次活動的時間（從 `tmux session_activity` 取得，沒有的話 fallback
`session_created`）。

### 偵測訊號

所有訊號都只對 `commands ∪ idle_shells` 中的 foreground command 觸發。回報的
`signal` 為以下其一：

| signal | 意義 |
|--------|------|
| `dead` | 前景 process 已結束（`pane_dead=1` 且結束時間在最近 2 分鐘內） |
| `prompt` | capture buffer（取最後 5 行）命中 agent prompt regex（`continue?` / `[Y/n]` / `human:` 等） |
| `stuck` | buffer 跨兩個 tick 完全一樣 **且** tty 超過 30 秒沒動（agent 卡住沒輸出） |
| `idle` | tty mtime 超過 `idle_threshold`（預設 30 秒）未更新 |

### 詳情面板（`Ctrl-Space`）

把 cursor 移到某個 waiting row，按 `Ctrl-Space` 展開 per-pane 詳情（窄
terminal 顯示在 row 下方，寬 terminal 顯示在右側預覽）：

```
>  work  3m   ⚠ ⏳ waiting  · claude ×2
    └ claude @ @1.0 · prompt · 3m
    └ claude @ @1.1 · stuck · 5m
```

顯示每個 waiting pane 的 window / pane index、觸發訊號、tty 多久沒動。

### 通知（waiting 狀態變化時）

當某 session **新進入** waiting 集合（上一個 tick 沒有、這 tick 有）時觸發：

1. 終端 bell（`\a`）— 所有 terminal 都支援
2. 嘗試 `notify-send`（Linux/Wayland）或 `osascript`（macOS）— 桌面通知

> 預設 `idle_shells = []`，所以 `nu` / `bash` / `nvim` 等常見 shell **不會**被
> 自動偵測——避免「正在跑 build 的 shell 誤報」。若想讓閒置的 shell 也被通知，
> 在 config 加入它們即可。

### 規則細節

- 同一個 session / path 內的同名 process 會去重，後面補 `×N`
- 字串超出該行剩餘寬度會被截斷，後面以 `+N` 或 `…` 表示還有更多
- 自己的 popup pane 不會被算進去（第一次 watcher tick 之後自動排除）
- `capture-pane` 只對 foreground command 在 `commands ∪ idle_shells` 內的
  pane 執行——其他 pane 的 buffer 不會被任何訊號用到，跳過可大幅減少每個
  tick 的 fork 數
- 偵測只在你打開 TUI 期間運作；關掉 TUI 偵測也跟著停
- 跨 process 的 waiting 結果有 5 秒 cache：快速關閉再打開 popup 時，
  會先讀 `~/.cache/tmux-qs/waiting.json`，避免等待 watcher 第一次 tick

### 失敗重試（Exponential Backoff）

當 `collectPanes` 失敗（NFS 停滯、tmux 卡住…）時，下一次 tick 會以指數退避
延長間隔，cap 為 60 秒，避免打爆 CPU 與 log。

---

## 剪貼簿（OSC 52）

`Ctrl-y`（或 vim normal `yy`）把選取項目的**目錄路徑**複製到系統剪貼簿：

- 目錄型 entry（`~/github/sesh`、`/tmp`）→ 直接複製 expand 後的絕對路徑
- tmux session 名稱（`visionai-deploy5`）→ 透過 `tmux list-sessions` 解析成
  session 的 cwd
- 找不到對應目錄 → 狀態列顯示 `✗ no directory for this entry`
- branch 模式下按 → 無動作（branch 名稱不是路徑）

**實作方式**：使用 [OSC 52](https://invisible-island.net/xterm/ctlseqs/ctlseqs.html#h3-Operating-System-Commands)
escape sequence（`ESC ] 52 ; c ; <base64-encoded-text> BEL`）直接寫到 terminal，
由 terminal 負責更新剪貼簿：

- **零外部相依**：不需要 `pbcopy` / `xclip` / `wl-copy` / `xsel` / `clip.exe`
- **跨平台**：iTerm2、Alacritty、kitty、WezTerm、xterm 等主流 terminal 都支援
- **tmux popup 內**：需要 `tmux.conf` 內有 `set-clipboard on`（否則 OSC 52
  會被 tmux 攔截但無法送到 outer terminal）
- **限制**：無法驗證 terminal 真的有收到；write-to-stdout 成功就當作成功

複製成功後狀態列短暫顯示 `✓ copied <path>`（1.5 秒後自動消失）；失敗則
顯示 `✗ copy failed`。

---

## Git Branch 功能

- 列表中凡是目錄項目（含 zoxide、config session、tmux session 對應目錄），
  會在右側以綠色顯示該目錄（含上層 repo）目前的 branch。實作讀 `.git/HEAD`
  直接解（支援 worktree 的 `gitdir:` 間接），不額外 fork process，速度快
- session name 項目也會透過 tmux 的 `#{session_path}` 找出對應資料夾並顯
  示其 branch；session 沒有對應資料夾（或不在 git repo 內）則跳過不標註
- working tree 有未提交的修改（unstaged / staged / untracked）會在 branch
  後加 `*` 警告
- `Ctrl-b` 進入 branch 模式（目錄與 session name 項目皆可用）：
  - 列出 local branches（標記 `*current`）、worktree 已 checkout 的 branch
    （標記 `⇒ 路徑`）、remote-only branches（標記 `(remote)`，自動剝離
    `origin/` 等前綴）
  - 選擇後：
    - branch 已在某個 worktree → 直接連到該 worktree 目錄
    - 是目前 branch → 直接連到 repo
    - 其他 → `git switch <branch>`（remote-only 會自動建立 tracking branch）
      後連到 repo
    - working tree 有衝突的本機修改時 git 會拒絕，錯誤訊息顯示在畫面底部，
      不會動到任何東西

---

## 預覽面板（Preview Pane）

當 terminal 寬度 ≥ 80 時，右側會出現預覽面板，內容依目前選中 entry 動態決
定：

- **tmux session**：抓**目前作用中 window 的 active pane**最近 20 行的
  capture buffer（其他 window / pane 不顯示；目的是「現在正在做什麼」的快
  速 snapshot，不是完整 session dump）
- **目錄**：列出 `.git/HEAD` 內容、`git status --short` 摘要、目錄裡
  重要的 metadata 檔（`package.json`、`Cargo.toml`、`go.mod` 等）
- **config session**：顯示展開後路徑、tags、group
- **waiting snapshot**：顯示等待中的 process 詳情

預覽有兩個保護機制：

- **Debounce 80ms**：cursor 停下後 80ms 才載入，避免快速滾動時對每個 row
  都 fork git / tmux
- **Cache TTL 3s**：同個 entry 在 3 秒內重新造訪會直接用 cache

預覽可用 `Alt-↑` / `Alt-↓` 捲動、`PgUp` / `PgDn` 翻頁；當內容可捲動時，
面板最底會顯示 `↓ 12/40` 之類的捲動位置指示。

---

## 自動偵測 Tag 與 Group

### Tag

- **config-defined**：`[[session]]` 內 `tags` 欄位
- **自動偵測**：根據目錄裡的標記檔推斷：

  | 檔案 | tag |
  |------|------|
  | `go.mod` | `go` |
  | `Cargo.toml` | `rust` |
  | `package.json` | `js` |
  | `pyproject.toml` / `requirements.txt` / `setup.py` | `python` |
  | `Makefile` | `make` |
  | `CMakeLists.txt` | `cmake` |
  | `Dockerfile` / `docker-compose.yml` / `docker-compose.yaml` | `docker` |
  | `terraform.tf` | `terraform` |
  | `.github` | `ci` |

`Ctrl-,` 進入 tag 過濾模式時，會列出「config-defined + 自動偵測」的聯集。

### Group

僅 config-defined session 可設定 `group` 欄位（例如 `"work"`、`"personal"`）。
`Ctrl-;` 進入 group 過濾模式時，會列出所有出現過的 group；清單中也會以
`≡ <group>` 標示每個 entry 屬於哪個 group。

---

## 釘選（Pinned）

`Alt-p` 切換釘選狀態。釘選清單存在：

```
$XDG_CONFIG_HOME/tmux-qs/pinned.txt
```

每行一個 entry。釘選的 entry 會在所有來源的清單中：

- 加上 📌 前綴
- 排到**最前面**（依插入順序）

---

## Command Palette（`Ctrl-o`）

命令面板列出可執行的管理指令：

- **內建**：
  - `Resurrect: Save Workspace State` — 把所有 session 的完整結構（每個
    window 的名稱與 layout、每個 pane 的 cwd 與前景程式）存到
    `~/.local/state/tmux-qs/resurrect.json`
  - `Resurrect: Restore Workspace State` — 讀回並對每個不在線上的 session
    重建：依序建立 windows、用 split 還原 pane 數量、套用儲存的 layout，並
    重新啟動白名單（`[resurrect] restore_programs`）內的前景程式。可在
    `[resurrect] auto_save_interval` 設定週期自動存檔（TUI 開著時靜默執行）。
    舊版只存 name→path 的 `resurrect.json` 仍可讀入（自動降級還原為空 session）
  - `Tmux-QS: Open Config File` — 用 `$EDITOR` 開 `~/.config/tmux-qs/config.toml`
  - `Tmux: Detach Client`
  - `Tmux: Reload Tmux Config` — `tmux source-file ~/.tmux.conf`
  - `Tmux: Kill Server (Danger)` — `tmux kill-server`
- **使用者自定**：在 config 用 `[[command]]` 定義，`{session}` 與 `{path}`
  會在執行時被替換成目前 attached session 的名稱與 cwd

---

## Active Panes 視圖（`Ctrl-e`）

跨所有 session 列出所有 pane，每行格式：

```
session:window.pane  [cmd]  ~/path
```

並在內部以 tab 分隔攜帶 session name 與 paneID，Enter 後會直接 `select-pane`
跳到該 pane。

## SSH 主機視圖（`Ctrl-h`）

解析 `~/.ssh/config` 的 `Host` 條目，**過濾掉含 `*` 或 `?` 的 pattern**（避免
誤列出動態 host），並去重。每行以 `ssh <host>` 形式顯示，Enter 後會建立新
session 並以 `ssh <host>` 為啟動命令。

## 清理模式（`Alt-c`）

列出「路徑已不存在」**或**「超過 7 天未活動」的 sessions。`Ctrl-d` 一次砍掉
所有列出項目（雙按確認）。

## 多重 Server 模式（`--all-servers`）

掃描 `/tmp/tmux-<uid>/` 與 `$TMPDIR/tmux-<uid>/` 兩個目錄（常見 tmux socket
擺放位置），對每個目錄底下的子目錄嘗試 `tmux -L <name> list-sessions`，成功
的就列入「正在跑的 server 集合」。所有這些 server 的 session 會合併顯示，
每個 session 前面加上 `[server-name] ` 前綴。

選定後會在背景暫時切換到該 session 所屬的 server（用 `-L`）完成 `connect`，
然後還原回原本的 server spec。

---

## 設定檔（Configuration）

### 搜尋順序

第一次啟動且完全找不到設定檔時，會自動產生範例檔至
`$XDG_CONFIG_HOME/tmux-qs/config.toml`（若 `XDG_CONFIG_HOME` 未設則
`~/.config/tmux-qs/config.toml`）。

搜尋順序（先找到的優先；都不存在才會在 `$HOME/.config/tmux-qs/config.toml`
寫入範例）：

1. `$TMUX_QS_CONFIG`（絕對路徑，環境變數）
2. `$XDG_CONFIG_HOME/tmux-qs/config.toml`
3. `~/.config/tmux-qs/config.toml`
4. `~/.tmux-qs.toml`

TUI 啟動後會週期性檢查 mtime 並在需要時重讀（hot-reload）；因此修改設定後
不需要重啟 tmux-qs，下次來源切換或 reload 行為就會套用新設定。

### 範例檔

```toml
# tmux-qs configuration
# 第一次啟動且完全找不到設定檔時，會自動寫入範例到 XDG 路徑。
# 後續的修改不會被覆蓋。

[waiting]
commands = [
  "claude", "opencode", "aider", "ollama", "codex",
  "cursor", "cody", "continue", "gpt", "copilot", "crush",
]

# 預設為空。加 ["nu", "bash", "zsh", "fish", "sh", "python", "nvim", "vim"]
# 等可讓閒置的 shell / REPL 在 tty 沒動一段時間後也被視為 waiting。
# 不設定的話，nu / bash / nvim 等閒置時不會被視為 waiting，避免「正在跑
# long-running build 的 shell 被誤判」的情況。
idle_shells = []

# 只放結構性的 prompt 訊號（continue?、yes.*no、[Y/n]、human: 等）
# 不放 process 名稱，避免 Claude Code 啟動 banner 含 "claude" 字串時誤判。
prompt_regex = [
  "continue\\?",
  "yes.*no",
  "\\[Y/n\\]|\\[y/N\\]",
  "human:|assistant:",
]

idle_threshold = "30s"
poll_interval  = "5s"

# Optional: override TUI 顏色（ANSI 16-color 數字；空字串 = 用預設）
[style]
# cursor   = "212"  # 游標所在 row 的標記
# selected = "212"  # 游標所在 entry 的文字
# branch   = "36"   # git branch 標註
# dim      = ""     # headers / 狀態列（淡灰）
# error    = "203"  # 錯誤訊息
# warn     = "214"  # waiting 警告
# success  = "42"   # 複製確認

# Optional: 在新 session 跑 layout script
[layout]
# enable_scripts = true
# script_names = [".tmux-qs.sh", ".tmux.sh"]
# pane_shells = ["nu", "nvim", "fish", "bash", "zsh"]
# agent = "claude"   # Alt-v 預設選的 agent（不選時的 fallback）

# Optional: 使用者自定命名 session（Ctrl-g）
# name = 顯示用的名字；path = 該 session 根目錄（"~" 會展開）
# tags = 標籤；group = 群組（用於 Ctrl-; 過濾）
#
# [[session]]
# name = "docs"
# path = "~/projects/docs"
# tags = ["go", "ci"]
# group = "work"
#
# [[session]]
# name = "scratch"
# path = "/tmp/scratch"
# group = "personal"

# Optional: templates 自動套用 split / 額外命令
# {session}、{path} 會被替換為 session 名稱與路徑
# [[template]]
# name = "node-template"
# detect_files = ["package.json"]
# windows = [
#   { name = "editor", command = "nvim ." },
#   { name = "shell", command = "", split = "horizontal" },
#   { name = "dev", command = "npm run dev", split = "vertical" },
# ]
# commands = [
#   "tmux send-keys -t {session}:dev 'npm run dev' Enter",
# ]

# Optional: Command Palette 使用者自定指令
# [[command]]
# name = "Git: Pull current session"
# cmd = "tmux send-keys -t {session} 'git pull' Enter"

# Optional: pane-aware snippets (Space for current session; Ctrl-s for selected target). commands matches the target pane's
# foreground command; omit it for a snippet available in every pane. The
# built-in defaults mirror fzf-send-keys.nu for nvim, codex, claude, crush,
# opencode, and ollama. `text` sends literal text; `keys` uses tmux notation.
# A preview is shown before anything is sent.
# [[snippet]]
# name = "Claude: continue"
# commands = ["claude", "codex"]
# text = "/continue"
# submit = true
# favorite = true  # place this action first for controller-friendly use

# Built-in agent actions (when no custom [[snippet]] entries replace the
# defaults) include Continue, Review changes, and Run tests. They are marked
# favorite and submit automatically, so a controller can select them with the
# stick and confirm with Enter without opening an on-screen keyboard.
#
# [[snippet]]
# name = "Interrupt"
# keys = ["C-c"]
```

### `commands` 與 `idle_shells` 差異

- `commands`：foreground command 命中任一即視為 AI agent
- `idle_shells`：foreground command 命中任一即視為「閒置的互動式 shell /
  REPL」

兩個列表的 command 都會觸發偵測；`isAllowed` 過濾會擋掉所有不在這兩個列表
內的 cmd。

> 預設 `idle_shells = []` 設計的用意是：避免「正在跑 long-running build 的
> shell 被誤判為 waiting」。把常見的互動式 shell / REPL（如 `nu` / `bash`
> / `nvim`）顯式加入 `idle_shells` 才會被偵測。

### `prompt_regex`

`regexp.MatchString` 對 pane 的 capture buffer 測試；壞 regex 會被略過並
印出警告。**只放結構性 prompt 訊號**，不放 process 名稱。

### `[resurrect]`

控制 Command Palette 的 Resurrect 存 / 還原：

- `restore_programs`：還原時允許重新啟動的前景程式白名單。不在名單內的
  pane 只會還原成空 shell——避免還原時誤跑當初剛好在跑的任意（可能危險的）
  指令。預設含常見編輯器 / 監看工具（`nvim`、`vim`、`htop`、`lazygit`、`ssh`…）
- `auto_save_interval`：設成非零的 Go duration（如 `"15m"`）後，TUI 開著時
  會以該間隔靜默存檔。留空（預設）為關閉

### `[keybindings]`

把 action 名稱映射到按鍵即可重新綁定。被改走的預設鍵會停止觸發原本的
action（兩個 action 互換鍵則都保留）。按鍵語法同 Bubble Tea（`ctrl+w`、
`alt+enter`、`ctrl+1`），也接受 `c-` / `m-` / `a-` / `s-` 前綴別名。

```toml
[keybindings]
waiting = "ctrl+1"   # 把「列出等待中 agent」從 Ctrl-w 改到 Ctrl-1
kill    = "ctrl+k"
```

可用 action：`all`、`tmux`、`configs`、`zoxide`、`zoxide-root`、`find`、
`panes`、`windows`、`ssh`、`commands`、`waiting`、`cleanup`、`copy`、`rename`、
`kill`、`branch`、`pin`、`agent`、`template`、`files`、`new-session`、
`open-remote`、`send`、`snippets`、`toggle-close`、`tag-filter`、`group-filter`、`detail`、
`jump-next`、`jump-prev`、`visit-back`、`visit-fwd`、`preview-up`、
`preview-down`、`undo`。核心導航（上 / 下 / Enter / Esc / 說明 / 離開）刻意不可
重綁，避免設錯讓 picker 無法操作。未知 action 名稱會印警告並忽略。

### `[waiting] float_to_top`

設為 `true` 時，目前「有等待中 agent」的 session 會在預設 / 全部列表中自動
排到最上面（在釘選項目下方）。預設 `false`。

### 設定檔解析失敗

會印出警告並沿用預設值。範例檔只會在「完全找不到任何設定檔」時寫入，後續
的修改不會被覆蓋。

---

## 資料持久化

TUI 會在 XDG 目錄下維護多份小型 cache / state：

| 檔案 | 用途 |
|------|------|
| `~/.cache/tmux-qs/recent.json` | frecency 排序資料（選取次數 + 最後選取時間，`recordTouch`） |
| `~/.cache/tmux-qs/input-history.txt` | 輸入框歷史（`Ctrl-↑/↓` 叫回），最多 200 筆 |
| `~/.cache/tmux-qs/last-view.json` | 60 秒內重開時還原上次的 TUI list 狀態（`src` / `mode` / 過濾 / `cursor` / `tag` / `group` / `vim mode` / preview scroll / `showDetail`）。`--last` / `--back` / `--forward` / `--toggle` / `--all-servers` / 顯式 `--server` / `--socket` 都會抑制還原。如果 60s 內重開沒還原，先確認 `make build && cp tmux-qs ~/bin/`（或你的 `$PATH` 安裝位置）— popup 模式下是子行程在跑，stale binary 會讓所有新邏輯失效。執行時 stdout 會印 `tmux-qs: restoring last view from ...` 或 `tmux-qs: no last view to restore ...` 來驗證讀寫 |
| `~/.cache/tmux-qs/last-session` | 上一個 attached session 名（`--last` 用） |
| `~/.cache/tmux-qs/visit-stack.json` | visit stack（`--back` / `--forward` 用），最多 20 筆 |
| `~/.cache/tmux-qs/waiting.json` | waiting snapshot 5 秒 cache |
| `~/.config/tmux-qs/config.toml` | 設定檔（搜尋順序見上） |
| `~/.config/tmux-qs/pinned.txt` | 釘選清單 |
| `~/.local/state/tmux-qs/resurrect.json` | Resurrect save/restore 狀態 |

所有 cache 寫入都是 best-effort，I/O 失敗會被忽略（不會打斷 TUI）。

---

## 架構簡介

主要 module 對應的 .go 檔：

| 檔案 | 內容 |
|------|------|
| `main.go` | CLI 解析、模式選擇（`--last` / `--back` / `--toggle`）、popup 啟動、TUI 進入 |
| `ui.go` | Bubble Tea model、Update、訊息處理、列表 / 預覽 / 輸入框狀態 |
| `view.go` | View 渲染（list、preview、detail、help overlay）、fuzzy match 高亮 |
| `keybindings.go` | 全部按鍵 dispatch（含 vim normal 模式）、fuzzy 評分 |
| `keymap.go` | `[keybindings]` 重綁：action→key 預設表、pressed→canonical 轉換 |
| `history.go` | 輸入框歷史的讀寫（`input-history.txt`） |
| `help.go` | `helpText` 說明覆蓋層內容（與 keybindings 同步） |
| `sources.go` | 所有 `loadSource` 種類、zoxide / find / panes / ssh / cleanup / commands 載入 |
| `connect.go` | `connect()` 決策樹、session 建立、layout script、template 套用、pane pick |
| `git.go` | branch 解析（讀 `.git/HEAD`，不 fork）、`switchBranch`、`isGitDirty` |
| `watch.go` | pane watcher、waiting 偵測、backoff、prevBuf 比較 |
| `cache.go` | waiting snapshot JSON cache |
| `recent.go` | frecency sort（頻率 × 最近度）與 `recent.json` |
| `pinned.go` | `pinned.txt` 讀寫 |
| `visit_stack.go` | last-session 與 visit stack JSON 持久化 |
| `commands.go` | Command Palette 內建指令、Resurrect save/restore |
| `clipboard.go` | OSC 52 escape sequence 寫剪貼簿 |
| `tmux_server.go` | tmux server spec（`-L` / `-S`）解析、`--all-servers` 掃描 |
| `config.go` | TOML 設定載入、mergeConfig、example config 寫入 |
| `session_meta.go` | session 的 activity / created 時間抓取 |
| `session_cmd.go` | new session / rename session 命令 |
| `exec.go` | 外部指令執行（含 5 秒 timeout）、`isCommandInstalled` |
| `notify.go` | 桌面通知（`notify-send` / `osascript`）與 bell |
| `popup.go` | `tmux display-popup` 啟動 |
| `styles.go` | lipgloss 樣式（顏色從 config 覆寫） |
| `xdg.go` | XDG 路徑解析 |

### 資料流

1. `main` 決定要 `--last` / `--back` / 開 popup / 直接開 TUI
2. TUI 啟動時 `newModel()` 會：
   - 載入 recency / pinned / config
   - 抓 `attached session` 的 self-pane id（`selfPaneCmd`）
   - 啟動第一個 `watchCmd` ticker
   - 觸發初始 source 載入（`loadSource(srcDefault)`）
3. 每個 source 載入都是 background goroutine，結果以 `itemsMsg` 送回 Update
4. Items 進來後會觸發：
   - branch 標註（`annotateCmd`，平行讀 `.git/HEAD`）
   - dirty 標註（`dirtyCmd`，平行 `git status --porcelain`）
5. `Enter` 觸發 `choose()` → 寫 recency → 依 entry 種類呼叫 `connect()`

### 為何不依賴 `fzf` / `sesh`

這個程式從早期 wrapper `~/bin/workspace` 演化而來，但完全以 tmux 原生命令直
接實作 connect / branch 偵測 / waiting 偵測 / recency，好處是：

- 沒有 `sesh connect <name>` 的 fork 開銷
- 不需要 `sesh.toml` 維護 session 與路徑對應（直接從 `tmux list-sessions`
  的 `#{session_path}` 抓）
- 能在 popup 內做更精細的 tmux 互動（`select-pane`、`send-keys`、template）
- waiting 偵測需要的 prompt / stuck / idle 訊號都得直接碰 tmux state
