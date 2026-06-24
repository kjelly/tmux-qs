# Insert-mode 快捷鍵在 normal-mode 也生效（set-difference 語意）

日期：2026-06-18
狀態：approved → implementing

## 目標

在 `modeList` 的 `vimNormal` 模式下，當前只接受 vim 風格的按鍵（`j`/`k`/`G`/`gg`/`dd`/`yy`/`S`/`u`/`Space`/`/`/`i`/`a`/`Enter`/`esc`/`ctrl+c`/`:q`/數字）。其它按鍵（含所有 `alt+` / `ctrl+` chords 以及 `?` 等）目前在 normal mode 是 inert。

**改動目標**：在 normal mode 下，凡是不在 normal-mode 綁定集內的按鍵，若 insert mode 對它有 handler，**就用 insert mode 同一個 handler**。效果、狀態變更、錯誤訊息、reload 行為、history 寫入都與 insert mode 完全相同。

## 設計決策

| 議題 | 決定 |
|------|------|
| 衝突定義 | 集合級 set-difference：normal-mode 鍵集合以外的 key 都可 fall through |
| Fall-through 行為 | 與 insert-mode 同一個 handler 執行（不做 re-semanticizing） |
| j/k vs ↑/↓ | 不動——兩者本來語意一致（都是 cursor move） |
| Keymap 重綁定 | 保留現狀：只有 insert-mode 綁定可被 `[keybindings]` 重綁 |
| 範圍 | 只在 `modeList && vimMode == vimNormal` |

## vimNormalOwnKeys 集合

normal mode 自身消費的 key：

```
esc ctrl+c :q j k G / i a enter S u y d g space
```

注意：
- 數字 0-9 **不列進去**：它們在 `handleVimNormal` 開頭就被當 count prefix 消費
- `d` `g` `y` 是 double-key sequence 的第一字元，會被既有 `vimLastKey` 邏輯處理；它們仍列在 own set 內是 defensive

## 實作

`handleVimNormal` 末段由 `return m, nil` 改為：

```go
if !vimNormalOwnKeys[key] {
    return m.dispatchAsInsert(msg)
}
return m, nil
```

`dispatchAsInsert` helper：

```go
func (m model) dispatchAsInsert(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
    saved := m.vimMode
    m.vimMode = vimInsert
    defer func() { m.vimMode = saved }()
    m.vimCount = "" // reset before fall-through to avoid stale count
    return m.handleKey(msg)
}
```

## 風險

- **count prefix 跟 fall-through 互動**：使用者按 `5` 然後按 `alt+up`，`5` 會被當 count prefix 累積。`alt+up` 進 normal handler 末段 → fall through → preview scroll。**5 沒被消費就**進了 `vimCount` 留到下次 motion。**已在 dispatchAsInsert 內 reset 解決。**
- **double-key 跨 fall-through**：`d` 進 double-key 偵測（`vimLastKey="d"`），然後按 `alt+up`，`alt+up` 進 normal handler 末段、fall through 之前 `vimLastKey` 已被 reset（line 911）。✓

## 測試計劃

### Fall-through 正向

1. `TestVimNormalAltUpScrollsPreview` — normal mode 按 `alt+up` 應 scroll preview
2. `TestVimNormalQuestionMarkTogglesHelp` — normal mode 按 `?` 切 `modeHelp`
3. `TestVimNormalCtrlASwitchesToAll` — normal mode 按 `ctrl+a` 切到 `srcAll`
4. `TestVimNormalTabMovesCursor` — normal mode 按 `tab` 等同 cursor move
5. `TestVimNormalCountPrefixResetsAfterFallthrough` — 5 然後 alt+up 不會汙染後續 j

### Regression guard

6. `TestVimNormalJKMovesCursor` — j/k 仍 cursor move
7. `TestVimNormalEnterStillChooses` — enter 仍走 `choose()`
8. `TestVimNormalEscStillQuits` — esc 仍 quit
9. `TestVimNormalDigitCountPrefix` — 5j 移動 5 行

## 不在這次範圍

- 不動 non-modeList modes
- 不動 keymap 重綁定
- 不新增任何 normal-mode 自己的 handler
- 不改 count prefix 機制本身，只在 fall-through 前 reset `vimCount`
