# 60s 內重開還原 TUI 狀態

日期：2026-06-18
狀態：approved → implementing

## 目標

`tmux-qs` 退出時，把「當下 TUI 看到的 list 狀態」序列化到 cache；下一次 60s 內重開時，從 cache 讀回並完整重現（src、mode、input 過濾、cursor、tag/group filter、vim mode、preview scroll、detail panel）。

超出 60s 就當 cache 過期，使用預設的 `srcDefault` 行為。

CLI fast-path flags（`--last` / `--back` / `--forward` / `--toggle` / `--all-servers`）以及顯式指定 `--server` / `--socket` 都會 **抑制 restore**，照原本邏輯走。

## 設計決策

| 議題 | 決定 |
|------|------|
| 保留範圍 | 完整：src、mode、input、cursor、tagFilter、groupFilter、vimMode、previewOffset、showDetail |
| TTL | 絕對 60s：寫入 timestamp → 重開時 `time.Since < 60s` 才 restore |
| 抑制條件 | 任一 fast-path flag 或顯式 `--server` / `--socket` → suppress restore |
| Save 觸發時機 | `p.Run()` 成功返回後立即寫 |
| 錯誤路徑不存 | `p.Run()` 失敗 / panic 不保存（讓使用者重來） |
| 檔案位置 | `$XDG_CACHE_HOME/tmux-qs/last-view.json` |
| Schema 版本控制 | `version` 欄位，未來變更可 bump |

## 持久化格式

```json
{
  "version": 1,
  "saved_at": "2026-06-18T03:55:12.123Z",
  "src": 2,
  "mode": 0,
  "input": "work",
  "cursor": 3,
  "tag_filter": "",
  "group_filter": "",
  "vim_mode": 1,
  "preview_offset": 0,
  "show_detail": false
}
```

## 邊界情況

| 情況 | 行為 |
|------|------|
| 60s 內重開、`srcTmux` 還在 | 載回 `srcTmux`，Init 跑 `loadCmd(srcTmux)` 拿到最新 pane list |
| 60s 內重開、`srcWaiting` 還在 | 載回 `srcWaiting` |
| 60s 內重開、輸入框有 "work" | 載回 input = "work"；refilter 跑 fuzzy |
| 60s 內重開、cursor 在第 3 row | 載回 cursor = 3，clamp 到 filtered 長度後生效 |
| 60s 內重開、`modeHelp` 還在 | 一進 TUI 就是 help overlay |
| 60s 內重開、`modeBranch` / `modeTag` / `modeGroup` / `modeFiles` / `modeAgentSelect` | fallback 到 `srcDefault`（這幾個 mode 依賴中間狀態如 `savedItems`、branches list、files list，無法安全 restore） |
| 跨 server 重開 | 顯式指定 `--server` / `--socket` 就 suppress restore |
| TTL 邊界 | 嚴格 `> 60s` 才過期 |

## newModel 簽名

保留向後相容的 variadic：

```go
func newModel(opts ...bool) model {
    themeWatch := false
    if len(opts) > 0 {
        themeWatch = opts[0]
    }
    restore := false
    if len(opts) > 1 {
        restore = opts[1]
    }
    // ...
}
```

`vim_normal_fallthrough_test.go` 等既有測試用 `newModel()` 不傳參數，仍可工作。

## 測試計劃

1. `TestSaveAndLoadLastViewRoundtrip` — 寫整個 model 欄位 → 讀回 → 比對
2. `TestLoadLastViewExpiresAfterTTL` — save、advance 超過 TTL、load 應 false
3. `TestLoadLastViewRejectsBadVersion` — version 改 0、load 應 false
4. `TestLoadLastViewRejectsCorruptedJSON` — 寫垃圾、load 應 false
5. `TestLoadLastViewRejectsZeroSavedAt` — 沒時間戳、load 應 false
6. `TestApplyLastView_NonListModeFallsBack` — save mode=modeBranch，apply 後 mode 應是 modeList
7. `TestNewModelAppliesLastViewWhenRestoreEnabled` — 寫 cache 後 `newModel(_, true)` 載入；`newModel(_, false)` 不載入
8. `TestNewModelWithoutCache` — 沒 cache 時不 panic

## 風險

- **隱私**：cache 包含使用者輸入 query（可能含密碼）—— 跟 `input-history.txt` 一致
- **跨進程 race**：同時退出時後寫的贏，正常
- **XDG 不可寫**：save 函式吞 error，行為退化為「永遠不 restore」

## 不在這次範圍

- 不存 `items` / `filtered` / `marked`
- 不存 `result*` 一次性出口狀態
- 不存 multi-server context
- 不支援非 modeList 的 restore
- 不做 cache 預熱
- 不做 config 開關
