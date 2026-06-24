# Ctrl-t 擴充：列出所有 window/pane（cwd + term title）

日期：2026-06-18
狀態：approved → implementing

## 目標

擴充 `Ctrl-t`（`srcTmux`）目前「只列 session 名稱」的行為，改成**每個 pane 一列**
的 flat 列表；每列帶：

- session 名稱
- window index / pane index
- foreground command
- pane 的 `cwd`（不是 session 的 cwd）
- pane 的 `term title`（`#{pane_title}`）
- session 的 git branch（沿用現有 `annotateCmd` 機制，由 session 路徑推出）

Enter 在任何 row 上 = 切到該 session 並 `select-pane` 到該 pane，與現有
`srcPanes`（Ctrl-e）行為一致。

## 設計決策摘要

| 議題 | 決定 |
|------|------|
| 粒度 | 純 flat：每個 pane 一行；pane 前面補上所屬 session |
| Enter 語意 | 進 session + select-pane 到該 pane（與 srcPanes 相同） |
| 顯示格式 | `session:win.pane [cmd] ~cwd 「title」` |
| 排序 | 保留現有 srcTmux 順序（tmux `list-sessions` 預設字母序；session 內依 win/pane index） |
| Fuzzy | 同時對 session name / cmd / cwd / title 比對；session name 加權 |
| 後設 metadata | 沿用 `annotateCmd`（git branch 由 session 路徑推） |
| 多重 server | 與現有 srcTmux 相同：只列當前 server（`--all-servers` 是另一個 view） |

## 顯示範例

```
> work:1.0   [nu]         ~ ~/projects/work            「~/projects/work (nu)」
  work:1.1   [nvim]       ~ ~/projects/work/src        「src/init.nu」
  work:2.0   [claude]     ~ ~/projects/work            「Claude Code — work」
  blog:1.0   [bash]       ~ ~/blog                     「~/blog」
  scratch:1.0 [nu]       ~ /tmp/scratch                「」
```

- `> ` 為現有 cursor 標記
- `📌 ` 為現有釘選標記
- 連字符不夠時 cwd / title 會被截斷
- title 為空時整個 `「」` 區段消失（不留空殼）
- `~` prefix 沿用 `loadPanes` 既有 shortener

## 改動清單

### 1. `sources.go`

**`loadSource` 的 `srcTmux` 分支改為：**

```go
case srcTmux:
    return loadTmuxPanes()
```

新增 `loadTmuxPanes()`：

- 先跑 `list-sessions -F #{session_name}` 拿原本的 session 順序
- 再跑 `list-panes -a -F` 拿所有 pane，format string 帶齊：
  `#{session_name}\t#{window_index}\t#{pane_index}\t#{pane_id}\t#{pane_current_command}\t#{pane_current_path}\t#{pane_title}`
- 在 Go 端 group → sort → 格式化
- 對每個 pane 組出 display string + `display\t<session>\t<paneID>` envelope

### 2. `ui.go`

- `entryText`（line 1083-1092）加入 `srcTmux`
- `choose`（line 909）加入 `srcTmux` 分支
- `choose` 末段的「waiting pane → resultPaneID」邏輯對 `srcTmux` 不跑

### 3. `view.go`

- `renderList`（line 250-253）加入 `srcTmux` 條件

### 4. Fuzzy 加權（`keybindings.go`）

- 新增 `sessionNameBonus(srcTmux 限定)`：query 任一 term 命中 session name 加 50 分

### 5. `connect()` 行為

不需改動。`paneID != ""` 時 `tmux select-pane -t <paneID>` 後 `switchOrAttach`，正是新行為要的效果。

## 測試計劃

1. `TestLoadTmuxPanes_Ordering` — 多 session，驗證維持 `list-sessions` 順序 + 內部 `(win, pane)` 排序
2. `TestLoadTmuxPanes_DisplayFormat` — 驗證 `session:win.pane [cmd] ~cwd 「title」`，title 為空 / ==cmd 時整段消失
3. `TestLoadTmuxPanes_EnvelopeForPaneID` — 驗證 `\t<session>\t<paneID>` envelope 可解析
4. `TestCtrlT_ChooseSelectsPane` — 整合測試：srcTmux 模式按 Enter → `m.result == sessionName`、`m.resultPaneID == <正確 pane>`
5. `TestCtrlT_FuzzySessionNameBonus` — 命中 session name 的分數 > 命中 cwd / title

## 風險

- **大 session 體感**：若某 session 有 20+ panes，會把其他 session 擠到後面
- **效能**：fuzzy match 已有 ≥1500 row 平行化；100 row 仍在 sequential 路徑
- **回退相容**：現有 srcTmux row 是 session name（無 envelope），新版有 envelope

## 不在這次範圍

- 不動 `srcAll` / `srcDefault` / `srcPanes`（Ctrl-e）/ `srcWindows`（Ctrl-v）
- 不動 `--all-servers` 的 pane 列舉
- 不動 `connect()` 內部
- 不加新 keybinding
- 不改 prompt 圖示（保留 `🪟`）
