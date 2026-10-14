package ui

import "github.com/charmbracelet/lipgloss"

// Gray/black/white palette, plus green/red status indicators.
var (
	colPaper = lipgloss.Color("#FEFEFE") // teks utama / judul
	colText  = lipgloss.Color("#D6D6D6") // isi
	colDim   = lipgloss.Color("#8E8E8E") // label sekunder
	colFaint = lipgloss.Color("#585858") // versi, hint, pemisah
	colLine  = lipgloss.Color("#2C2C2C") // border window
	colSelBg = lipgloss.Color("#1F1F1F") // latar pilihan aktif
	colGreen = lipgloss.Color("#4ADE80") // indikator enabled / sehat
	colRed   = lipgloss.Color("#F87171") // indikator error
	colAmber = lipgloss.Color("#FACC15") // indikator sedang di-test (kuning)
)

var (
	// Header: bold "BasicRoute", dimmer version.
	styleTitle   = lipgloss.NewStyle().Bold(true).Foreground(colPaper)
	styleVersion = lipgloss.NewStyle().Foreground(colFaint)

	styleURL     = lipgloss.NewStyle().Foreground(colDim)
	styleStatNum = lipgloss.NewStyle().Bold(true).Foreground(colPaper)
	styleStatLbl = lipgloss.NewStyle().Foreground(colDim)
	styleStatSep = lipgloss.NewStyle().Foreground(colFaint)

	styleMenuHead  = lipgloss.NewStyle().Bold(true).Underline(true).Foreground(colText)
	styleBullet    = lipgloss.NewStyle().Foreground(colFaint)
	styleBulletSel = lipgloss.NewStyle().Bold(true).Foreground(colPaper)
	styleItem      = lipgloss.NewStyle().Foreground(colDim)
	styleItemSel   = lipgloss.NewStyle().Bold(true).Foreground(colPaper).Background(colSelBg)

	stylePageTitle = lipgloss.NewStyle().Bold(true).Underline(true).Foreground(colPaper)
	styleWin       = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colLine).Padding(0, 2)
	styleBody      = lipgloss.NewStyle().Foreground(colText)

	styleLogInfo = lipgloss.NewStyle().Foreground(colDim)
	styleLogWarn = lipgloss.NewStyle().Foreground(colText)
	styleLogErr  = lipgloss.NewStyle().Bold(true).Foreground(colPaper)

	styleFooter  = lipgloss.NewStyle().Foreground(colFaint)
	styleFootKey = lipgloss.NewStyle().Bold(true).Foreground(colText)
	stylePrompt  = lipgloss.NewStyle().Bold(true).Foreground(colPaper)
	styleInputPh = lipgloss.NewStyle().Foreground(colFaint)
	styleStatus  = lipgloss.NewStyle().Foreground(colText)
	styleErr     = lipgloss.NewStyle().Bold(true).Underline(true).Foreground(colPaper)

	// Provider table: type column uses bright white.
	styleProvType = lipgloss.NewStyle().Bold(true).Foreground(colPaper)
	styleDotOn    = lipgloss.NewStyle().Bold(true).Foreground(colGreen)
	styleDotOff   = lipgloss.NewStyle().Foreground(colDim)
	styleDotErr   = lipgloss.NewStyle().Bold(true).Foreground(colRed)
	styleDotTest  = lipgloss.NewStyle().Bold(true).Foreground(colAmber)

	// Provider detail: name title, section heads, label vs value, separator.
	styleDetName = lipgloss.NewStyle().Bold(true).Foreground(colPaper)
	styleSecHead = lipgloss.NewStyle().Bold(true).Foreground(colText)
	styleDetLbl  = lipgloss.NewStyle().Foreground(colDim)
	styleDetVal  = lipgloss.NewStyle().Foreground(colText)
	styleDetHi   = lipgloss.NewStyle().Bold(true).Foreground(colPaper)
	styleSep     = lipgloss.NewStyle().Foreground(colFaint)
)
