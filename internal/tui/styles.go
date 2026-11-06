package tui

import "github.com/charmbracelet/lipgloss"

// Midnight-Commander-inspired palette: blue panels, cyan selection.
var (
	colFg    = lipgloss.Color("#d0d0d0")
	colSel   = lipgloss.Color("#00afaf")
	colTitle = lipgloss.Color("#ffff5f")
	colErr   = lipgloss.Color("#ff5f5f")
	colWarn  = lipgloss.Color("#ffaf00")
	colOK    = lipgloss.Color("#5fff5f")
	colDim   = lipgloss.Color("#8787af")
	colKeyBg = lipgloss.Color("#00afaf")
	colKeyFg = lipgloss.Color("#000000")

	stHeader = lipgloss.NewStyle().Background(colSel).Foreground(lipgloss.Color("#000000")).Bold(true)
	stPanel  = lipgloss.NewStyle().Border(lipgloss.DoubleBorder()).BorderForeground(colFg).
			Padding(0, 1)
	stPanelFocus = stPanel.BorderForeground(colTitle)
	stTitle      = lipgloss.NewStyle().Foreground(colTitle).Bold(true)
	stSel        = lipgloss.NewStyle().Background(colSel).Foreground(lipgloss.Color("#000000"))
	stDim        = lipgloss.NewStyle().Foreground(colDim)
	stErr        = lipgloss.NewStyle().Foreground(colErr)
	stWarn       = lipgloss.NewStyle().Foreground(colWarn)
	stOK         = lipgloss.NewStyle().Foreground(colOK)
	stKey        = lipgloss.NewStyle().Background(colKeyBg).Foreground(colKeyFg)
	stKeyLabel   = lipgloss.NewStyle().Foreground(colFg)
	stModal      = lipgloss.NewStyle().Border(lipgloss.ThickBorder()).BorderForeground(colWarn).Padding(1, 2)
)
