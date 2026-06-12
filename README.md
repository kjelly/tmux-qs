# tmux-qs

tmux session 快速切換器（Go TUI）。行為對齊 `~/bin/workspace`（fzf popup 流程），
並額外支援 ** 顯示目錄的 git branch**、**跳到指定 git branch**、**偵測等待中的 agent**。

依賴外部指令：`tmux`。
可選：`zoxide`（用於 `Ctrl-x` 與 `Alt-r` 來源）。terminal 需支援 OSC 52（主流 terminal 都支援）以使用剪貼簿功能。

## 安裝

```sh
go build -o ~/bin/tmux-qs .
```

## Popup

像 `fzf --popup` 一樣：在 tmux 內執行時，程式會自動把自己包進 `tmux display-popup`，
預設位置 `top,70%`（同原本 workspace 腳本）。OPTS 語法與 fzf 完全相同：

```
--popup[=[center|top|bottom|left|right][,SIZE[%]][,SIZE[%]]]
--no-popup    # 直接在目前終端執行，不開 popup
```

一個 SIZE：top/bottom 為高度、left/right 為寬度、center 為兩者。
兩個 SIZE：固定為「寬,高」。

Popup 開啟時，輸入框會自動盡量置中（保證至少 2 行上方留白）；
如果清單項目已經填滿 popup，輸入框則保持在最上方。

## tmux 綁定

在 `~/.tmux.conf` 加入（例如綁 `prefix + s`）：

```tmux
bind-key s run-shell -b "tmux-qs"
# 或自訂位置大小
bind-key s run-shell -b "tmux-qs --popup=center,80%,70%"
```

在 shell 直接打 `tmux-qs` 也會自動開 popup。

## 按鍵

| 按鍵 | 功能 |
|---|---|
| 輸入文字 | fuzzy 過濾。無查詢時保持原始排序（同 fzf `--no-sort`）；輸入後按匹配分數排序（word boundary、連續字元加分，跳躍扣分） |
| `Enter` | 連線：tmux session 直接 `tmux switch-client`/`attach-session`；目錄若已有 session 則切換，否則建立新 session（必要時跑 layout script）；zoxide 等其他子目錄會在當前 session 內找 pane 或開新 window |
| `Tab` / `Shift-Tab`、`Ctrl-n` / `Ctrl-p`、方向鍵 | 上下移動（碰到邊界不循環） |
| `Alt-j` / `Alt-k` | 跳到下一個/上一個**已存在的** tmux session（循環） |
| `Alt-n` | 建立新的空白 tmux session（自動命名 `qs-<timestamp>`）並切換 |
| `Alt-Enter` | 把輸入框文字送到選中 session 的 pane（`tmux send-keys`）後清空。若該 session 有等待中的 agent pane，直接送到那個 pane |
| `Ctrl-r` | 重新命名選中的 session（用輸入框文字當新名稱） |
| `Ctrl-a` | 全部 tmux sessions（**已選過的 session 會浮到最上面**） |
| `Ctrl-t` / `Ctrl-s` | tmux sessions |
| `Ctrl-g` | config 內定義的 sessions（`[[session]]` table） |
| `Ctrl-x` | zoxide 目錄（`zoxide query --list --score`） |
| `Alt-r` | zoxide 目錄、限目前 session root 之下 |
| `Ctrl-f` | 掃描 `~` 下兩層目錄（原生實作，等同 `fd -H -d 2 -t d -E .Trash . ~`） |
| `Ctrl-w` | 只列出**有等待中 agent** 的 sessions（取自 watcher 最近一次 tick 的快照） |
| `Ctrl-d` | 砍掉選取的 tmux session 並重新載入（**需連按兩次確認**；移動游標或改變過濾即取消） |
| `Ctrl-b` | **branch 模式**：列出選取目錄（repo）的 git branches |
| `Ctrl-y` | 複製選取項目的目錄路徑到剪貼簿（OSC 52；目錄型 entry 直接、session 名稱透過 `tmuxSessionPaths()` 解析） |
| `Ctrl-Space` | 切換 per-pane 詳情面板（顯示哪個 pane 等待、tty 多久沒動） |
| `?` | 切換說明覆蓋層（列出所有按鍵） |
| 滑鼠左鍵 | 點選某個 row 直接跳 cursor |
| 滾輪 | 上下捲動 cursor |
| `Esc` | 退出說明 / branch 模式；列表模式離開 |
| `Ctrl-c` | 離開 |

## Configuration

第一次啟動且完全找不到設定檔時，會自動產生範例檔至：

- `$XDG_CONFIG_HOME/tmux-qs/config.toml`（若 `XDG_CONFIG_HOME` 未設則 `~/.config/tmux-qs/config.toml`）

搜尋順序（先找到的優先；都不存在才會在 `$HOME/.config/tmux-qs/config.toml` 寫入範例）：

1. `$TMUX_QS_CONFIG`（絕對路徑，環境變數）
2. `$XDG_CONFIG_HOME/tmux-qs/config.toml`
3. `~/.config/tmux-qs/config.toml`
4. `~/.tmux-qs.toml`

可自訂欄位（未列出的欄位沿用內建預設）：

```toml
[waiting]
commands      = ["claude", "opencode", "aider", "ollama", "codex", "cursor", "cody", "continue", "gpt", "copilot", "crush"]
idle_shells   = []   # 加入 ["nu", "bash", "zsh", "fish", "sh", "python", "nvim", "vim"] 可讓閒置的 shell/REPL 也被視為 waiting
prompt_regex  = ["continue\\?", "yes.*no", "\\[Y/n\\]|\\[y/N\\]", "human:|assistant:"]
idle_threshold = "30s"
poll_interval  = "5s"

# Optional: override TUI colors (ANSI 16-color numbers; empty = default)
[style]
# cursor   = "212"  # selected row marker
# selected = "212"  # selected entry text
# branch   = "36"   # git branch annotation
# dim      = ""     # headers / status line (faint)
# error    = "203"  # error messages
# warn     = "214"  # waiting warnings
# success  = "42"   # copy confirmations
```

### `commands` 與 `idle_shells`

- `commands`：foreground command 命中任一即視為 AI agent。
- `idle_shells`：預設為空。加入常見互動式 shell/REPL（例如 `nu`、`bash`、`nvim`）可讓它們在 tty 長時間無輸出時也被視為 waiting。**不設定的話，`nu`/`bash`/`nvim` 等閒置時不會被視為 waiting**——這避免了「正在跑 long-running build 的 shell 被誤判」的情況。

兩個列表的 foreground command 都會觸發偵測；`isAllowed` 過濾會擋掉所有不在這兩個列表內的 cmd。

### `prompt_regex`

`regexp.MatchString` 對 pane 的 capture buffer 測試；壞 regex 會被略過並印出警告。
**只包含結構性 prompt 訊號**（`continue?` / `yes.*no` / `[Y/n]` / `human:` 等）——不包含 process 名稱，避免 Claude Code 啟動 banner 含有 "claude" 字串時誤判。

### 設定檔解析失敗

會印出警告並沿用預設值。範例檔只會在「完全找不到任何設定檔」時寫入，後續的修改不會被覆蓋。

## 剪貼簿

`Ctrl-y` 會把選取項目的**目錄路徑**複製到系統剪貼簿：

- 目錄型 entry（`~/github/sesh`、`/tmp`）→ 直接複製 expand 後的絕對路徑
- tmux session 名稱（`visionai-deploy5`）→ 透過 `tmux list-sessions` 解析成 session 的 cwd
- 找不到對應目錄 → 狀態列顯示 `✗ no directory for this entry`
- branch 模式下按 → 無動作（branch 名稱不是路徑）

**實作方式**：使用 [OSC 52](https://invisible-island.net/xterm/ctlseqs/ctlseqs.html#h3-Operating-System-Commands) escape sequence
（`ESC ] 52 ; c ; <base64> BEL`）直接寫到 terminal，由 terminal 負責更新剪貼簿。

- **零外部相依**：不需要 `pbcopy` / `xclip` / `wl-copy` / `xsel` / `clip.exe`
- **跨平台**：iTerm2、Alacritty、kitty、WezTerm、xterm 等主流 terminal 都支援
- **tmux popup 內**：需要 `tmux.conf` 內有 `set-clipboard on`（否則 OSC 52 會被 tmux 攔截但無法送到 outer terminal）
- **限制**：無法驗證 terminal 真的有收到；寫入 stdout 成功就當作成功

複製成功後狀態列短暫顯示 `✓ copied <path>`（1.5 秒後自動消失）；失敗則顯示 `✗ copy failed`。

## 等待中的 agent 提示

TUI 開啟期間，背景每 5 秒 poll 一次所有 tmux pane，偵測「agent 正在等待
使用者回答」的 session，並在對應清單項目右側 inline 顯示提示：

```
>  work            3m   ⚠ ⏳ waiting  · claude[prompt] ×2 · opencode[stuck]
   blog            1d   ⚠ ⏳ waiting  · claude[idle]
   scratch               · claude
   notes
```

每個 process 後面用 `[signal]` 標示觸發原因（`prompt` / `stuck` / `idle` / `dead`）。
session 旁邊的 `3m` / `1d` 是上次活動的時間（從 `tmux session_activity` 或 `session_created` 取得）。

### 偵測訊號

所有信號都只對 `commands` ∪ `idle_shells` 中的 foreground command 觸發。
回報的 `signal` 為以下其一：

| signal | 意義 |
|--------|------|
| `dead` | 前景 process 已結束（`pane_dead=1` 且結束時間在最近 2 分鐘內） |
| `prompt` | Capture buffer 命中 agent prompt regex（`continue?` / `[Y/n]` / `human:` 等） |
| `stuck` | Buffer 連續兩個 tick 完全一樣且 tty 超過 30 秒沒動（agent 卡住沒輸出） |
| `idle` | tty mtime 超過 `idle_threshold`（預設 30 秒）未更新 |

### 詳情面板（`Ctrl-Space`）

把 cursor 移到某個 waiting row，按 `Ctrl-Space` 展開 per-pane 詳情：

```
>  work  3m   ⚠ ⏳ waiting  · claude ×2
    └ claude @ @1.0 · prompt · 3m
    └ claude @ @1.1 · stuck · 5m
```

顯示每個 waiting pane 的 window/pane index、觸發訊號、tty 多久沒動。

### 通知（waiting 狀態變化時）

當某 session **新進入** waiting 集合時（即上一個 tick 沒有、這 tick 有），觸發：

1. 終端 bell（`\a`）— 所有 terminal 都支援
2. 嘗試 `notify-send`（Linux/Wayland）或 `osascript`（macOS）— 桌面通知

> 預設 `idle_shells = []`，所以 `nu` / `bash` / `nvim` 等常見 shell **不會**被自動偵測——避免「正在跑 build 的 shell 誤報」。若想讓閒置的 shell 也被通知，在 config 加入它們即可。

### 規則細節

- 同一個 session / path 內的同名 process 會去重，後面補 `×N`。
- 字串超過該行剩餘寬度會被截斷，後面顯示 `+N` 表示還有更多。
- 自己的 popup pane 不會被算進去（第一次 tick 之後自動排除）。
- `capture-pane` 只對 foreground command 在 `commands` ∪ `idle_shells` 內的 pane 執行——
  其他 pane 的 buffer 不會被任何訊號用到，跳過可大幅減少每個 tick 的 fork 數。
- 偵測只在你打開 TUI 期間運作；關掉 TUI 偵測也跟著停。
- 跨 process 的 waiting 結果有 5 秒 cache：關閉 popup 再立刻打開時，
  會先讀 `~/.cache/tmux-qs/waiting.json`，避免等待 watcher 第一次 tick。

## Recency 排序

`Ctrl-a` 與預設列表都會把「最近選過」的 session 浮到最上方。
時間戳存在 `~/.cache/tmux-qs/recent.json`（按 Enter 自動更新）。
其他 source（`Ctrl-t` tmux sessions、`Ctrl-g` configs 等）保留原始順序不動。

## Git branch 功能

- 列表中凡是目錄項目，會在右側以綠色顯示該目錄（含上層 repo）目前的 branch。
  讀取 `.git/HEAD`（支援 worktree 的 `gitdir:` 間接），不額外 fork process，速度快。
- session name 項目也會透過 tmux 的 `#{session_path}` 找出對應資料夾並顯示其 branch；
  session 沒有對應資料夾（或不在 git repo 內）則跳過不標註。
- `Ctrl-b` 進入 branch 模式（目錄與 session name 項目皆可用）：
  - 列出 local branches（標記 `*current`）、worktree 已 checkout 的 branch（標記 `⇒ 路徑`）、
    remote-only branches（標記 `(remote)`）。
  - 選擇後：
    - branch 已在某個 worktree → 直接連到該 worktree 目錄。
    - 是目前 branch → 直接連到 repo。
    - 其他 → `git switch <branch>`（remote-only 會自動建立 tracking branch）後連到 repo。
      working tree 有衝突的本機修改時 git 會拒絕，錯誤訊息顯示在畫面底部，不會動到任何東西。

## 滑鼠

- 左鍵：直接點選某個 row 把 cursor 跳過去
- 滾輪：上下捲動 cursor

滑鼠支援在 TUI 啟動時已自動開啟（`tea.WithMouseCellMotion()`）。
