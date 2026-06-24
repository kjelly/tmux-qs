# `--NORMAL--` 與使用者輸入之間加視覺分隔

日期：2026-06-18
狀態：approved → implementing

## 目標

vimNormal 模式下 `--NORMAL--` 指示器跟使用者輸入文字之間，目前只有一個
未樣式化的半形空格（`view.go:58`）。把它們用 dim 樣式的 `│` 符號
（與 preview pane 的 left/right divider 同視覺語言）明確分開；同時在
分隔符跟 input 之間再多加一個 dim 空格，強調「input 是另一個 block」。

## 改動

1. `view.go`：line 54-58
2. 新檔 `view_normal_test.go`：6 個 test

## 視覺

- Before: `-- NORMAL -- work`
- After:  `-- NORMAL -- │  work`
