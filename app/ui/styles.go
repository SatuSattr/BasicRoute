package ui

import "github.com/charmbracelet/lipgloss"

// Palet abu-abu / hitam / putih, plus hijau/merah untuk indikator status.
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
	// Header: "BasicRoute" bold #FEFEFE, "v1.0" lebih gelap.
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

	// Kartu provider: border redup, yang terpilih border terang + nama underline.
	styleCard       = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colLine).Padding(0, 1)
	styleCardSel    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colPaper).Padding(0, 1)
	styleCardName   = lipgloss.NewStyle().Bold(true).Foreground(colPaper)
	styleCardNameOD = lipgloss.NewStyle().Bold(true).Foreground(colDim)
	styleCardNameSl = lipgloss.NewStyle().Bold(true).Underline(true).Foreground(colPaper)
	styleCardLbl    = lipgloss.NewStyle().Foreground(colDim)
	styleCardVal    = lipgloss.NewStyle().Foreground(colText)
	styleCardURL    = lipgloss.NewStyle().Foreground(colDim)
	styleDotOn      = lipgloss.NewStyle().Bold(true).Foreground(colGreen)
	styleDotOff     = lipgloss.NewStyle().Foreground(colDim)
	styleDotErr     = lipgloss.NewStyle().Bold(true).Foreground(colRed)
	styleDotTest    = lipgloss.NewStyle().Bold(true).Foreground(colAmber)
	styleCardSel2   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colText).Padding(0, 1)

	// Detail provider: judul nama, kepala seksi, label vs nilai, pemisah.
	styleDetName = lipgloss.NewStyle().Bold(true).Foreground(colPaper)
	styleSecHead = lipgloss.NewStyle().Bold(true).Foreground(colText)
	styleDetLbl  = lipgloss.NewStyle().Foreground(colDim)
	styleDetVal  = lipgloss.NewStyle().Foreground(colText)
	styleDetHi   = lipgloss.NewStyle().Bold(true).Foreground(colPaper)
	styleSep     = lipgloss.NewStyle().Foreground(colFaint)
)
