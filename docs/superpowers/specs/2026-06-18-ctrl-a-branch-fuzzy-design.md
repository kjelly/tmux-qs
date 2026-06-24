# srcAll 的 fuzzy 比對加上 branch

日期：2026-06-18
狀態：approved → implementing

## 目標

在 `srcAll` (Ctrl-a) / `srcDefault` (alias) 模式下，使用者的 fuzzy input
不只比對 entry 文字，也比對該 entry 的 git branch。branch 命中區段
會在畫面上 highlight 出來。

## 設計決策

| 議題 | 決定 |
|------|------|
| 比對語意 | fzf 風格：query 是單一字串，用 fzf 的 subsequence fuzzy 跑一次 |
| 涵蓋 source | 只有 `srcAll` / `srcDefault` |
| Highlight 改法 | composite 文字（item + 兩個空白 + branch）拿去 fuzzy；indices 是相對 composite 的 offset；render 時把 offset map 回 rawItem / branch 兩段分開 highlight |
| 不涵蓋 | 其他所有 source 維持只比對 item |
| modeBranch | 不變（branch picker 整個 view 都是 branch） |
| m.annots 還沒到 | 沒 branch 的 entry 退回只比對 item |

## 改動清單

1. `ui.go`：新增 `compositeEntryText`，refilter 改用
2. `view.go`：新增 `highlightComposite`，renderEntry 改用
3. 新檔 `branch_fuzzy_test.go`：8 個 test
4. `help.go` / `README.md`：一句話更新
