# Git repo subdir → 以 repo 根建立 session、子目錄開 window

日期：2026-06-24
狀態：approved → implementing

## 目標

把 `tmux-qs` 對「位於 git repo 內的子目錄」entry 的連線行為，從「以子目錄為 cwd
直接建 session」改成「以該 repo 根目錄建立（並切換到）session，再於其中以子目錄
為 cwd 開新 window」。子目錄若在所屬 repo session 內已存在 pane，則直接跳進
該 pane；不另外開 window。

## 動機

使用者常從 zoxide / Ctrl-f / config session 選到一個 repo 的子目錄（例：
`~/go/src/github.com/foo/bar/cmd/cli`、`~/work/blog/_drafts`）。目前的行為是
「以子目錄為 cwd 建一個新 session」。實務上的問題：

- 同一個 repo 內若有多個子目錄被加到 zoxide，就會出現 N 個以 subdir 為 cwd 的
  session，全部是同一個 repo 的子視窗——使用者期待的是「一個 repo 一個 session」。
- `Alt-v` 啟動 AI agent 選到 subdir 也是同樣情況。
- Repo 內的 layout script / template 應該以 repo 根為中心，不是以每個 subdir。

## 設計決策

| 議題 | 決定 |
|------|------|
| 觸發情境 | Picker 選到「目錄型 entry」且該路徑在 git repo 內且**不是** repo 根時（任何來源：default / tmux / configs / zoxide / find / files / ssh-host 皆走 `connect()`） |
| 不觸發 | entry 已是 repo 根、entry 不在任何 git repo 內、entry 是檔案（檔案走原本的「以 dirPath 建 session 並 `$EDITOR` 開啟」分支——檔案路徑的 dir 通常也在 repo 內，但因有「編輯單一檔案」語意，不在本規則範圍） |
| 觸發處理 | `connect()` 內插入 git-root 分支：以 repo root 建 / 找 session，再開 subdir window |
| 既有 session 命中 | 跳進去；若其內已有任何 pane cwd == subdir，select-window + select-pane，**不**開新 window |
| 既有 session 未命中 | `tmux new-window -t <session> -c <subdir>`，window 不命名 |
| 沒有對應 session | 走原本 session 建立流程（`deriveSessionName(root)`、template / layout script 套 root），再 new-window 到 subdir |
| AI agent (`Alt-v` / `Alt-m`) | 跟 Enter 一樣：root session + subdir window；agent command 送進 subdir window |
| paneID 傳入 | 在 git-root 分支忽略（subdir window 不沿用原 paneID） |
| config 開關 | 無；永遠開啟 |
| picker 顯示 | 不動；subdir 仍以 subdir 字串顯示在列表，不 fold 到 root 群組 |

## 架構

```
connect(target, paneID, openWithAgent, agentCmd, hintPath)
   │
   ├─ openWithAgent 分支維持原樣（但內部 dir 改用 root 建立 session，再走 subdir window）
   │
   ├─ 計算 finalPath = hintPath > resolvedPath > target → expand → filepath.Clean
   │
   ├─ if isDir(finalPath):
   │     root := walkUpToGit(finalPath)
   │     if root != "" && filepath.Clean(root) != filepath.Clean(finalPath):
   │         return connectGitSubdir(root, finalPath, openWithAgent, agentCmd)
   │
   ├─ 既有的 directory 分支（已是 root / 不在 repo / 檔案）── 行為不變
   ├─ 既有的 ssh 分支 ── 行為不變
   └─ 既有的 fallback pickPaneOrNewWindow 分支 ── 行為不變
```

`connectGitSubdir(root, subdir, agentCmd)`：

1. 找 `tmuxSessionPaths()` 中 `cwd == Clean(root)` 的 session，命名為 `existing`。
2. 若 `existing` 存在：
   - 跑 `tmux list-panes -s -t <existing> -F "#{window_id}\t#{pane_id}\t#{pane_current_path}"`。
   - 若有 `pane_current_path == Clean(subdir)`：先 `select-window -t <window_id>` 再
     `select-pane -t <pane_id>`，`switchOrAttach(existing)`，結束。
     （已存在的 pane 不再注入 `agentCmd`——這是「跳回使用者的現有工作」語意，
     與新開 window 時「注入 agent」語意分開。）
   - 否則 `tmux new-window -t <existing> -c <subdir>`，`switchOrAttach(existing)`。
   - 若有 `agentCmd`：`send-keys -t <new window> <agentCmd> Enter`（或已存在的 pane id）。
3. 若 `existing` 不存在：
   - 跑既有 `deriveSessionName(root)` 拿到 session 名稱。
   - 跑 `tmux new-session -d -s <name> -c <root>`。
   - 套 `applyAutoTemplate(name, root)` 或 layout script（沿用既有流程）。
   - 接著跑 (2) 的「new-window to subdir + switchOrAttach + 送 agent」。
4. `paneID` 參數在 git-root 分支完全忽略（subdir window 是新位置，原 paneID
   屬於別的 session，跳過去沒意義；呼叫端在走進這個分支前已經知道 target 是
   path entry，paneID 來自 pane-mode 是 pathless 的特例）。

## 改動清單

### 1. `git.go`

- 新增 `walkUpToGit(dir string) string`：從 `dir` 往上找 `.git`，回傳含 `.git` 的
  目錄絕對路徑；已是 repo 根回傳自身；不在 repo 內回傳 `""`。支援 `.git` 為
  指向真正 gitdir 的檔案（worktree / submodule）。
- 把 `branchOfDir()` 改寫為呼叫 `walkUpToGit()` 後讀 `HEAD`（保留既有簽章、
  既有 cache 行為）。
- `resolveBranches` / `resolveDirty` 內呼叫 `branchOfDir` 處不需改動。

### 2. `connect.go`

- `connect()`：在「計算 finalPath」之後、「走原本 isDir / isFile 分支」之前插入
  git-root 判斷，命中則呼叫 `connectGitSubdir` 並回傳。
- `connectGitSubdir(root, subdir string, openWithAgent bool, selectedAgent string)`
  按上面「架構」段落實作。
- 把 `openWithAgent` 分支改成：先算 `root := walkUpToGit(dir)`，若有 root 且不是
  dir 自身，走 `connectGitSubdir(root, dir, openWithAgent=true, selectedAgent)`；
  否則維持原本「以 dir 建 session 後送 agent」邏輯。

### 3. 既有函式不動

- `applyAutoTemplate` / `applyTemplate` / `applyTemplateByName`
- `pickPaneOrNewWindow`（fallback 路徑使用）
- `switchOrAttach`
- `deriveSessionName`
- 任何 UI / picker 渲染 / source loading

### 4. 文檔

- `README.md` 在「連線行為（Enter / Alt-Enter / Alt-v）」小節新增「**Git repo
  子目錄**」段落，說明新行為與三種 case（已有 / 沒有 / 已有 pane）。
- `docs/superpowers/specs/2026-06-18-ctrl-f-subdirs-design.md` 補一句：Ctrl-f 列
  出的子目錄若在 repo 內，Enter 後走 git-root 分支（具體行為見本 spec）。

## 測試計劃

### Unit（不需要真 tmux server）

放在新檔 `git_root_session_test.go`：

1. `TestWalkUpToGit_RepoRoot`：tempDir 內 `git init`，回傳 tempDir 自身
2. `TestWalkUpToGit_SubdirReturnsRoot`：tempDir/src, tempDir/docs 都回傳 tempDir
3. `TestWalkUpToGit_NotRepo`：空目錄回傳 `""`
4. `TestWalkUpToGit_NestedSubdir`：tempDir/a/b/c 回傳 tempDir
5. `TestWalkUpToGit_AlreadyCleaned`：input 為 `tempDir/.`，應回傳 tempDir（不卡在 `.`）
6. `TestBranchOfDir_StillWorksAfterRefactor`：既有 `branchOfDir` 行為不變（smoke test）

### Integration（fake tmux server via tmux -L）

放在既有 `connect_test.go` 延伸：

1. `TestConnect_GitSubdir_NewSession`：
   - 在 tempDir git init，建立 `pkg/` 子目錄
   - 呼叫 `connect(tempDir/pkg, "", false, "", "")`
   - 預期：`tmux list-sessions` 出現 session，`#{session_path} == tempDir`
   - 預期：`list-panes -t <session>` 至少有一個 pane，`pane_current_path == tempDir/pkg`
2. `TestConnect_GitSubdir_ReuseSession`：
   - 先 `tmux new-session -d -s name -c tempDir`，掛進去
   - 呼叫 `connect(tempDir/pkg, "", false, "", "")`
   - 預期：沒有新增 session；`list-panes -t name` 出現 cwd == tempDir/pkg 的 pane
3. `TestConnect_GitSubdir_ReusePane`：
   - 先 `tmux new-session -d -s name -c tempDir`，再 `new-window -t name -c tempDir/pkg`
   - 呼叫 `connect(tempDir/pkg, "", false, "", "")`
   - 預期：pane 數量不變（沒 new-window）
4. `TestConnect_RepoRoot_NoChange`：
   - 在 tempDir git init；`connect(tempDir, ...)`
   - 預期：session cwd == tempDir（無 subdir window 額外開）
5. `TestConnect_NonRepo_NoChange`：
   - 普通目錄；`connect(tempDir, ...)`
   - 預期：session cwd == tempDir（與原本一致）
6. `TestConnect_GitSubdir_WithAgent`：
   - 呼叫 `connect(tempDir/pkg, "", true, "claude", "")`
   - 預期：session cwd == tempDir、subdir pane 出現、pane capture 看到 `claude`
7. `TestConnect_FileInsideRepo_StaysInFileBranch`：
   - 在 tempDir 內建檔 `tempDir/pkg/main.go`
   - 呼叫 `connect(tempDir/pkg/main.go, "", false, "", "")`
   - 預期：session cwd == tempDir/pkg（檔案 dir），與既有「editor + file」語意一致

### 手動驗證

README 加一段驗證步驟：

```sh
# 1. 找一個含子目錄的 nested repo，例如：
git clone https://github.com/charmbracelet/bubbletea /tmp/bubbletea
# 2. 在 repo 內任何目錄下打 tmux-qs，輸入 /tmp/bubbletea/examples
# 3. Enter → 看到 session 名稱 = bubbletea（或既有同名 session）
# 4. tmux 內：window 0 = /tmp/bubbletea（套 template），window 1 = /tmp/bubbletea/examples
```

## 風險

| 風險 | 影響 | 緩解 |
|------|------|------|
| `walkUpToGit` 在 NFS 慢 | connect() 每次多一次 `.git` stat（最多 walk 到 repo root 的層數） | 只在 directory 分支才呼叫；其他分支短路；不加 cache（呼叫頻率低，誤判的代價小） |
| Worktree subdir | walk-up 找到的是 worktree 路徑（不是 main repo） | 符合 git 標準語意：worktree 視為獨立 checkout |
| 已 attach session 在 subdir 但無 root session | 仍會建 root session + new-window；使用者「已在的 session」被忽略 | 在 README 明列；屬於 corner case；解法是先以 root 建一次 session，後續選擇都會走 reuse 分支 |
| `Alt-v` 同時套用 | agent 送進 subdir window 而非 root | 與使用者直覺一致（agent 跑在當下選的目錄） |
| `walkUpToGit` 對 `.git` 是檔案（指向 gitdir）的處理 | worktree / submodule 場景需正確回傳 repo 根 | unit test 涵蓋 |

## 不在這次範圍

- 不改 picker 顯示（subdir 仍以 subdir 字串顯示）
- 不新增 keybinding（所有觸發來自既有 Enter / Alt-v）
- 不動 Resurrect / recordVisit / last-session
- 不動 session 命名策略（root session 名稱仍走 `deriveSessionName(root)`；同一
  repo 的所有 subdir 共用同一個 root session 名稱，這是期望行為）
- 不加 config 開關（依使用者決定）

## 對既有 spec 的影響

- `2026-06-18-ctrl-f-subdirs-design.md`：不衝突；Ctrl-f 仍只負責「列出當前 session
  cwd 的子目錄」，連線階段走本 spec 的 git-root 分支
- `2026-06-18-ctrl-t-pane-flatlist-design.md`：不衝突；paneID 路徑走既有的 pane-jump 分支
- `2026-06-18-last-view-restore-design.md`：不衝突
- `2026-06-18-vim-normal-insert-fallthrough-design.md`：不衝突
- `2026-06-18-ctrl-a-branch-fuzzy-design.md`：不衝突
