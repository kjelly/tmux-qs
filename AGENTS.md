# tmux-qs 操作指引

這個專案同時服務「通用遊戲手把」與鍵盤使用者。修改操作流程時，必須保留兩種操作方式，不要要求使用者更換作業系統層的手把映射。

## 遊戲手把對應

手把是通用滑鼠替代品；`tmux-qs` 只接收它送出的標準鍵盤／滑鼠事件。

| 手把操作 | 送出的事件 | `tmux-qs` 中的用途 |
| --- | --- | --- |
| 左搖桿上／下 | `Up`／`Down` | 移動清單游標 |
| 左搖桿左／右 | `Left`／`Right` | 送出左右方向鍵；在目標 pane 或其他終端程式中使用 |
| 右搖桿移動 | 滑鼠游標移動 | 移動終端滑鼠游標 |
| 滑鼠左鍵 | Mouse left press | 選取清單列 |
| 滑鼠滾輪上／下 | Wheel up／down | 上下移動清單 |
| X | `Backspace` | 刪除輸入框前一個字元 |
| Y | `Space` | 開啟 snippet；在 snippet 清單中送出並保持清單開啟 |
| A | `Enter` | 連線、確認或送出 snippet 並關閉清單 |
| B | `Esc` | 返回上一層或離開 TUI |
| 額外按鍵 | `Tab` | 在 session 清單與 window／pane 清單間切換 |

右搖桿與滑鼠按鍵不能被新的功能重新解讀；它們必須維持通用滑鼠操作。新增手把流程應優先使用既有的 `Space`、`Enter`、`Esc`、`Tab` 事件。

## 鍵盤友善操作

基本流程：

- `↑`／`↓` 或 `Ctrl-n`／`Ctrl-p`：移動游標。
- `Tab`：切換 session／window／pane 視圖。
- `Enter`：連線到選取項目；在 snippet 清單中送出並關閉。
- `Esc`：返回或離開。
- `Space`：開啟目前工作區的 snippets；在 snippet 清單中送出並保留清單。
- `Ctrl-s`：對選取的 session／pane 開啟 snippets。
- `Alt-Enter`：把輸入框文字送到選取 pane。
- `Alt-←`／`Alt-→`：在 session visit stack 中後退／前進。
- `Alt-↑`／`Alt-↓`、`PgUp`／`PgDn`：捲動預覽面板。
- 輸入框下方的 source tabs 可用滑鼠左鍵直接切換，並保留快捷鍵提示：`Sessions`、`All ^a`、`Waiting ^w`、`Tmux ^t`、`Panes ^e`、`Config ^g`、`Files ^f`、`Commands ^o`。

## Agent 與 snippet 操作

snippet 清單是手把操作 Agent 的主要入口：

- Favorite 項目優先顯示，其次依輸入歷史中的使用次數遞減排序；同頻率維持設定檔順序。
- 內建 Agent 動作包括 `Continue`、`Review changes`、`Run tests`、`Explain status`、`Inspect error`。
- 這些 prompt 會自動送出 Enter，無需螢幕鍵盤。
- 最近使用且不重複的 prompt 會顯示為 `Recent: ...`，可直接重送。
- snippet 標記 `favorite = true` 可固定到清單前段：

```toml
[[snippet]]
name = "Continue"
commands = ["claude", "codex"]
text = "continue"
submit = true
favorite = true
```

等待中的 Agent 預設會在 session／all 清單中優先顯示（`waiting.float_to_top = true`）。等待偵測仍受 `waiting.pinned_only` 設定限制；需要監控所有 session 時明確設定 `pinned_only = false`。

## 修改與驗證

- 修改操作行為時，同時更新 `help.go`、`README.md` 與相關測試。
- 格式化：`rtk gofmt -w <修改的 Go 檔案>`。
- 單元測試：`rtk go test ./... -skip 'PTY|Teatest|VimNormalTabMovesCursor'`。
- 修改前後不要重設或覆蓋工作區中其他未提交變更。
