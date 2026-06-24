# Ctrl-f 改成「當前目錄的子目錄 → 在當前 session 開新 window」

日期：2026-06-18
狀態：approved → implementing

## 目標

把 `Ctrl-f` 從「掃 `$HOME` 兩層」改成「列出當前 tmux session 的 cwd 的子目錄
（廣度優先展開直到 30 個項目）→ 使用者 fuzzy 選 → 在當前 session 開新
window，cwd 為選定路徑」。

舊的 `findDirs` (掃 HOME) 移除。

## 設計決策

| 議題 | 決定 |
|------|------|
| 「當前目錄」來源 | `attachedSessionPath()` (re-queried at Ctrl-f time) |
| 搜尋演算法 | **廣度優先 (BFS)** |
| 深度 / 數量 | 累計 30 個項目為止 |
| 跳過隱藏目錄 | 是（以 `.` 開頭） |
| 沒 attached session | `m.errText = "no attached session"`，不切 source |
| 選擇後怎麼打開 | 走原 `connect()` 路徑：把路徑塞進 `m.result`，main.go → `pickPaneOrNewWindow(path)` |
| 舊 `findDirs` | 整個移除 |

## 改動清單

1. `sources.go`：刪 `findDirs`、加 `listSubdirs` (BFS)、改 `case srcFind:`
2. `help.go`：改 `Ctrl-f` 說明
3. `README.md`：改 `Ctrl-f` 那行
4. 新檔 `find_subdirs_test.go`：5 個 unit + 1 個整合
