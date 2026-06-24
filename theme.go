package main

import (
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ─── 終端機主題偵測 ───────────────────────────────────────────────────────────
//
// Lipgloss 的背景偵測在 Bubble Tea 接管 stdin 後就無法運作，而且在 tmux 底下
// termenv 根本不會發 OSC 查詢，會默默退回深色，導致淺色主題下文字看不見。
// 這裡的策略：
//  1. TMUX_QS_THEME=light|dark 強制指定（最優先）。
//  2. tmux 內：讀 window-style 的 bg 色（~/bin/tmux-set-background 切換的就是
//     這個選項），並在執行期間持續追蹤，主題切換時即時換色重繪。
//  3. 其他環境：啟動時（接管終端機之前）做一次 OSC 查詢。

var tmuxBgRe = regexp.MustCompile(`bg=#([0-9a-fA-F]{6})`)

// initTheme 在 Bubble Tea 啟動前決定深淺色，回傳執行期間是否需要持續追蹤
// （只有 tmux 內才追蹤得到；強制指定時則不追蹤）。
func initTheme() (watch bool) {
	switch os.Getenv("TMUX_QS_THEME") {
	case "light":
		lipgloss.SetHasDarkBackground(false)
	case "dark":
		lipgloss.SetHasDarkBackground(true)
	default:
		if dark, ok := tmuxHasDarkBackground(); ok {
			lipgloss.SetHasDarkBackground(dark)
			return true
		}
		lipgloss.SetHasDarkBackground(lipgloss.HasDarkBackground())
		// 在 tmux 內但 window-style 尚未設定 bg：之後設定了也要能跟上
		return os.Getenv("TMUX") != ""
	}
	return false
}

// tmuxHasDarkBackground 讀取 tmux window-style 目前的背景色並判斷深淺。
// window-style 沒設定 bg（或不在 tmux 內）時 ok 為 false。
func tmuxHasDarkBackground() (dark, ok bool) {
	if os.Getenv("TMUX") == "" {
		return false, false
	}
	out, err := exec.Command("tmux", "show", "-gv", "window-style").Output()
	if err != nil {
		return false, false
	}
	m := tmuxBgRe.FindStringSubmatch(string(out))
	if m == nil {
		return false, false
	}
	v, err := strconv.ParseUint(m[1], 16, 32)
	if err != nil {
		return false, false
	}
	r := float64((v >> 16) & 0xFF)
	g := float64((v >> 8) & 0xFF)
	b := float64(v & 0xFF)
	// ITU-R BT.601 亮度，過半視為淺色背景
	return 0.299*r+0.587*g+0.114*b < 128, true
}

type msgThemeChecked struct {
	dark     bool
	ok       bool
	fromTick bool
}

// watchTheme 每兩秒重查一次 tmux 背景色（查詢在 timer goroutine 執行，
// 不會卡住 UI）。收到 msgThemeChecked 後由 Update 重新排程，形成循環。
func watchTheme() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg {
		dark, ok := tmuxHasDarkBackground()
		return msgThemeChecked{dark: dark, ok: ok, fromTick: true}
	})
}

// checkThemeNow 立刻重查一次。tmux-set-background 是依 client 寬度切換主題，
// 所以視窗大小變化（換到 e-ink 螢幕）是最即時的觸發點。
func checkThemeNow() tea.Cmd {
	return func() tea.Msg {
		dark, ok := tmuxHasDarkBackground()
		return msgThemeChecked{dark: dark, ok: ok}
	}
}

// applyTheme 套用偵測結果；主題確實變了才需要整頁重繪。
func applyTheme(msg msgThemeChecked) (changed bool) {
	if !msg.ok || msg.dark == lipgloss.HasDarkBackground() {
		return false
	}
	lipgloss.SetHasDarkBackground(msg.dark)
	return true
}
