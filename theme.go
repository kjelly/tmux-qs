package main

import (
	"os"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ─── 終端機主題偵測 ───────────────────────────────────────────────────────────
//
// tmux-qs 必須在 Bubble Tea 接管終端前決定背景，並在執行期間跟隨目前
// tmux client 的寬度變化。
// 這裡的策略：
//  1. TMUX_QS_THEME=light|dark 強制指定（最優先）。
//  2. tmux 內：精確比對目前 client_width 與全域 @eink-widths，並持續追蹤。
//  3. 其他環境或 tmux 查詢失敗：使用深色主題。

const defaultEinkWidths = "167,165"

func parseConfiguredEinkWidths(raw string) map[int]struct{} {
	widths := make(map[int]struct{})
	for _, field := range strings.Split(raw, ",") {
		width, err := strconv.Atoi(strings.TrimSpace(field))
		if err == nil && width > 0 {
			widths[width] = struct{}{}
		}
	}
	return widths
}

func parseEinkWidths(raw string) map[int]struct{} {
	widths := parseConfiguredEinkWidths(raw)
	if len(widths) == 0 {
		return parseConfiguredEinkWidths(defaultEinkWidths)
	}
	return widths
}

func isEinkWidth(width int, widths map[int]struct{}) bool {
	_, ok := widths[width]
	return ok
}

func themeIsDarkForWidth(width int, widths map[int]struct{}) bool {
	return !isEinkWidth(width, widths)
}

func loadEinkWidths() map[int]struct{} {
	raw, err := tmuxRunOut("show-options", "-gv", "@eink-widths")
	if err != nil {
		raw = ""
	}
	return parseEinkWidths(raw)
}

func currentTmuxClientWidth() (int, error) {
	args := []string{"display-message"}
	if client := strings.TrimSpace(os.Getenv("TMUX_QS_CLIENT")); client != "" {
		args = append(args, "-t", client)
	}
	args = append(args, "-p", "#{client_width}")
	raw, err := tmuxRunOut(args...)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(raw))
}

// isEinkClient is used only to preserve the existing grouped-session routing.
// Theme selection and routing share the same exact-width policy.
func isEinkClient() bool {
	width, err := currentTmuxClientWidth()
	return err == nil && isEinkWidth(width, loadEinkWidths())
}

// initTheme 在 Bubble Tea 啟動前決定深淺色，回傳執行期間是否需要持續追蹤
// （只有 tmux 內才追蹤得到；強制指定時則不追蹤）。
func initTheme() (watch bool) {
	switch os.Getenv("TMUX_QS_THEME") {
	case "light":
		lipgloss.SetHasDarkBackground(false)
	case "dark":
		lipgloss.SetHasDarkBackground(true)
	default:
		if os.Getenv("TMUX") != "" {
			dark, _ := tmuxHasDarkBackground()
			lipgloss.SetHasDarkBackground(dark)
			return true
		}
		lipgloss.SetHasDarkBackground(true)
	}
	return false
}

// tmuxHasDarkBackground 依目前 tmux client 寬度與 @eink-widths 判斷深淺。
func tmuxHasDarkBackground() (dark, ok bool) {
	if os.Getenv("TMUX") == "" {
		return true, false
	}
	width, err := currentTmuxClientWidth()
	if err != nil {
		return true, true
	}
	return themeIsDarkForWidth(width, loadEinkWidths()), true
}

type msgThemeChecked struct {
	dark     bool
	ok       bool
	fromTick bool
}

// watchTheme 每兩秒重查一次 client_width 與 @eink-widths（查詢在 timer goroutine 執行，
// 不會卡住 UI）。收到 msgThemeChecked 後由 Update 重新排程，形成循環。
func watchTheme() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg {
		dark, ok := tmuxHasDarkBackground()
		return msgThemeChecked{dark: dark, ok: ok, fromTick: true}
	})
}

// checkThemeNow 立刻重查一次。視窗大小變化（換到 e-ink 螢幕）是最即時的觸發點。
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
