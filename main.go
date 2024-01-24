package main

import (
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"os/user"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	. "strings"
	"syscall"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/antonmedv/clipboard"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/sahilm/fuzzy"
)

var Version = "v1.7.0"

const separator = "    " // Separator between columns.

var (
	warning       = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).PaddingLeft(1).PaddingRight(1)
	preview       = lipgloss.NewStyle().PaddingLeft(2)
	cursor        = lipgloss.NewStyle().Background(lipgloss.Color("#825DF2")).Foreground(lipgloss.Color("#FFFFFF"))
	bar           = lipgloss.NewStyle().Background(lipgloss.Color("#5C5C5C")).Foreground(lipgloss.Color("#FFFFFF"))
	search        = lipgloss.NewStyle().Background(lipgloss.Color("#499F1C")).Foreground(lipgloss.Color("#FFFFFF"))
	danger        = lipgloss.NewStyle().Background(lipgloss.Color("#FF0000")).Foreground(lipgloss.Color("#FFFFFF"))
	fileSeparator = string(filepath.Separator)
	showIcons     = false
)

var (
	keyForceQuit = key.NewBinding(key.WithKeys("ctrl+c"))
	keyCDQuit    = key.NewBinding(key.WithKeys("shift+enter"))
	keyQuit      = key.NewBinding(key.WithKeys("esc"))
	keyQuitQ     = key.NewBinding(key.WithKeys("q"))
	keyOpen      = key.NewBinding(key.WithKeys("enter"))
	keyBack      = key.NewBinding(key.WithKeys("backspace"))
	keyUp        = key.NewBinding(key.WithKeys("up"))
	keyDown      = key.NewBinding(key.WithKeys("down"))
	keyLeft      = key.NewBinding(key.WithKeys("left"))
	keyRight     = key.NewBinding(key.WithKeys("right"))
	keyTop       = key.NewBinding(key.WithKeys("shift+up"))
	keyBottom    = key.NewBinding(key.WithKeys("shift+down"))
	keyLeftmost  = key.NewBinding(key.WithKeys("shift+left"))
	keyRightmost = key.NewBinding(key.WithKeys("shift+right"))
	keyPageUp    = key.NewBinding(key.WithKeys("pgup"))
	keyPageDown  = key.NewBinding(key.WithKeys("pgdown"))
	keyHome      = key.NewBinding(key.WithKeys("home"))
	keyEnd       = key.NewBinding(key.WithKeys("end"))
	keyVimUp     = key.NewBinding(key.WithKeys("k"))
	keyVimDown   = key.NewBinding(key.WithKeys("j"))
	keyVimLeft   = key.NewBinding(key.WithKeys("h"))
	keyVimRight  = key.NewBinding(key.WithKeys("l"))
	keyVimTop    = key.NewBinding(key.WithKeys("g"))
	keyVimBottom = key.NewBinding(key.WithKeys("G"))
	keySearch    = key.NewBinding(key.WithKeys("/"))
	keyPreview   = key.NewBinding(key.WithKeys(" "))
	keyDelete    = key.NewBinding(key.WithKeys("d"))
	keyUndo      = key.NewBinding(key.WithKeys("u"))
	keyYank      = key.NewBinding(key.WithKeys("y"))
	keyMode      = key.NewBinding(key.WithKeys("m"))
	keyShowRaw   = key.NewBinding(key.WithKeys("v"))
)

func main() {
	startPath, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	for i := 1; i < len(os.Args); i++ {
		if os.Args[i] == "--help" || os.Args[1] == "-h" {
			usage()
		}
		if os.Args[i] == "--version" || os.Args[1] == "-v" {
			version()
		}
		if os.Args[i] == "--icons" {
			showIcons = true
			parseIcons()
			continue
		}
		startPath, err = filepath.Abs(os.Args[1])
		if err != nil {
			panic(err)
		}
	}

	output := termenv.NewOutput(os.Stderr)
	lipgloss.SetColorProfile(output.ColorProfile())

	m := &model{
		path:      startPath,
		width:     80,
		height:    60,
		positions: make(map[string]position),
	}
	m.list()

	p := tea.NewProgram(m, tea.WithOutput(os.Stderr))
	if _, err := p.Run(); err != nil {
		panic(err)
	}
	os.Exit(m.exitCode)
}

type navigationMode int

const (
	navigationModeDefault navigationMode = iota
	navigationModeDetail
	navigationModeMax
)

type model struct {
	path              string              // Current dir path we are looking at.
	files             []fs.DirEntry       // Files we are looking at.
	err               error               // Error while listing files.
	c, r              int                 // Selector position in columns and rows.
	columns, rows     int                 // Displayed amount of rows and columns.
	width, height     int                 // Terminal size.
	offset            int                 // Scroll position.
	positions         map[string]position // Map of cursor positions per path.
	search            string              // Type to select files with this value.
	searchMode        bool                // Whether type-to-select is active.
	searchId          int                 // Search id to indicate what search we are currently on.
	matchedIndexes    []int               // List of char found indexes.
	prevName          string              // Base name of previous directory before "up".
	findPrevName      bool                // On View(), set c&r to point to prevName.
	exitCode          int                 // Exit code.
	previewMode       bool                // Whether preview is active.
	previewContent    string              // Content of preview.
	deleteCurrentFile bool                // Whether to delete current file.
	toBeDeleted       []toDelete          // Map of files to be deleted.
	yankSuccess       bool                // Show yank info
	navMode           navigationMode      // current navigationMode
	showRawValue      bool                // Show raw value
}

type position struct {
	c, r   int
	offset int
}

type toDelete struct {
	path string
	at   time.Time
}

type (
	clearSearchMsg int
	toBeDeletedMsg int
)

func (m *model) Init() tea.Cmd {
	walkMode, err := strconv.Atoi(lookup([]string{"WALK_MODE"}, "0"))
	if err == nil && walkMode < int(navigationModeMax) {
		m.navMode = navigationMode(walkMode)
	}

	return nil
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// Reset position history as c&r changes.
		m.positions = make(map[string]position)
		// Keep cursor at same place.
		fileName, ok := m.fileName()
		if ok {
			m.prevName = fileName
			m.findPrevName = true
		}
		// Also, m.c&r no longer point to the correct indexes.
		m.c = 0
		m.r = 0
		return m, nil

	case tea.KeyMsg:
		if m.searchMode {
			if key.Matches(msg, keySearch) {
				m.searchMode = false
				return m, nil
			} else if key.Matches(msg, keyBack) {
				if len(m.search) > 0 {
					m.search = m.search[:len(m.search)-1]
					return m, nil
				}
			} else if msg.Type == tea.KeyRunes {
				m.search += string(msg.Runes)
				names := make([]string, len(m.files))
				for i, fi := range m.files {
					names[i] = fi.Name()
				}
				matches := fuzzy.Find(m.search, names)
				if len(matches) > 0 {
					m.matchedIndexes = matches[0].MatchedIndexes
					index := matches[0].Index
					m.c = index / m.rows
					m.r = index % m.rows
				}
				m.updateOffset()
				m.saveCursorPosition()
				// Save search id to clear only current search after delay.
				// User may have already started typing next search.
				searchId := m.searchId
				return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg {
					return clearSearchMsg(searchId)
				})
			}
		}

		switch {
		case key.Matches(msg, keyForceQuit):
			_, _ = fmt.Fprintln(os.Stderr) // Keep last item visible after prompt.
			m.exitCode = 2
			m.dontDoPendingDeletions()
			return m, tea.Quit

		case key.Matches(msg, keyQuit, keyQuitQ):
			_, _ = fmt.Fprintln(os.Stderr) // Keep last item visible after prompt.
			quitNoCd := ToLower(lookup([]string{"WALK_QUIT_NO_CD"}, ""))
			if quitNoCd != "1" && quitNoCd != "true" && quitNoCd != "yes" {
				fmt.Println(m.path) // Write to cd.
			}
			m.exitCode = 0
			m.performPendingDeletions()
			return m, tea.Quit

		case key.Matches(msg, keyCDQuit):
			_, _ = fmt.Fprintln(os.Stderr) // Keep last item visible after prompt.
			fmt.Println(m.path)            // Write to cd.
			m.exitCode = 0
			m.performPendingDeletions()
			return m, tea.Quit

		case key.Matches(msg, keyOpen):
			m.searchMode = false
			filePath, ok := m.filePath()
			if !ok {
				return m, nil
			}
			if fi := fileInfo(filePath); fi.IsDir() {
				// Enter subdirectory.
				m.path = filePath
				if p, ok := m.positions[m.path]; ok {
					m.c = p.c
					m.r = p.r
					m.offset = p.offset
				} else {
					m.c = 0
					m.r = 0
					m.offset = 0
				}
				m.list()
			} else {
				// Open file. This will block until complete.
				return m, m.openEditor()
			}

		case key.Matches(msg, keyBack):
			m.searchMode = false
			m.prevName = filepath.Base(m.path)
			m.path = filepath.Join(m.path, "..")
			if p, ok := m.positions[m.path]; ok {
				m.c = p.c
				m.r = p.r
				m.offset = p.offset
			} else {
				m.findPrevName = true
			}
			m.list()
			return m, nil

		case key.Matches(msg, keyUp):
			m.moveUp()

		case key.Matches(msg, keyTop, keyPageUp, keyVimTop):
			m.moveTop()

		case key.Matches(msg, keyBottom, keyPageDown, keyVimBottom):
			m.moveBottom()

		case key.Matches(msg, keyLeftmost):
			m.moveLeftmost()

		case key.Matches(msg, keyRightmost):
			m.moveRightmost()

		case key.Matches(msg, keyHome):
			m.moveStart()

		case key.Matches(msg, keyEnd):
			m.moveEnd()

		case key.Matches(msg, keyVimUp):
			if !m.searchMode {
				m.moveUp()
			}

		case key.Matches(msg, keyDown):
			m.moveDown()

		case key.Matches(msg, keyVimDown):
			if !m.searchMode {
				m.moveDown()
			}

		case key.Matches(msg, keyLeft):
			m.moveLeft()

		case key.Matches(msg, keyVimLeft):
			if !m.searchMode {
				m.moveLeft()
			}

		case key.Matches(msg, keyRight):
			m.moveRight()

		case key.Matches(msg, keyVimRight):
			if !m.searchMode {
				m.moveRight()
			}

		case key.Matches(msg, keySearch):
			m.searchMode = true
			m.searchId++
			m.search = ""

		case key.Matches(msg, keyPreview):
			m.previewMode = !m.previewMode
			// Reset position history as c&r changes.
			m.positions = make(map[string]position)
			// Keep cursor at same place.
			fileName, ok := m.fileName()
			if !ok {
				return m, nil
			}
			m.prevName = fileName
			m.findPrevName = true

			if m.previewMode {
				return m, tea.EnterAltScreen
			} else {
				m.previewContent = ""
				return m, tea.ExitAltScreen
			}

		case key.Matches(msg, keyDelete):
			filePathToDelete, ok := m.filePath()
			if ok {
				if m.deleteCurrentFile {
					m.deleteCurrentFile = false
					m.toBeDeleted = append(m.toBeDeleted, toDelete{
						path: filePathToDelete,
						at:   time.Now().Add(6 * time.Second),
					})
					m.list()
					m.previewContent = ""
					return m, tea.Tick(time.Second, func(time.Time) tea.Msg {
						return toBeDeletedMsg(0)
					})
				} else {
					m.deleteCurrentFile = true
				}
			}
			return m, nil

		case key.Matches(msg, keyUndo):
			if len(m.toBeDeleted) > 0 {
				m.toBeDeleted = m.toBeDeleted[:len(m.toBeDeleted)-1]
				m.list()
				m.previewContent = ""
				return m, nil
			}

		case key.Matches(msg, keyYank):
			// copy path to clipboard
			clipboard.WriteAll(m.path)
			m.yankSuccess = true
			return m, nil
		case key.Matches(msg, keyMode):
			// TODO: convert current file pointer
			m.navMode = m.navMode + 1
			if m.navMode >= navigationModeMax {
				m.navMode = navigationModeDefault
			}
		case key.Matches(msg, keyShowRaw):
			m.showRawValue = !m.showRawValue
		} // End of switch statement for key presses.

		m.deleteCurrentFile = false
		m.yankSuccess = false
		m.updateOffset()
		m.saveCursorPosition()

	case clearSearchMsg:
		if m.searchId == int(msg) {
			m.searchMode = false
		}

	case toBeDeletedMsg:
		toBeDeleted := make([]toDelete, 0)
		for _, td := range m.toBeDeleted {
			if td.at.After(time.Now()) {
				toBeDeleted = append(toBeDeleted, td)
			} else {
				_ = os.RemoveAll(td.path)
			}
		}
		m.toBeDeleted = toBeDeleted
		if len(m.toBeDeleted) > 0 {
			return m, tea.Tick(time.Second, func(time.Time) tea.Msg {
				return toBeDeletedMsg(0)
			})
		}
	}

	return m, nil
}

func (m *model) BuildDefaultView() string {
	var main string
	return main
}

func (m *model) View() string {
	width := m.width
	if m.previewMode {
		width = m.width / 2
	}
	height := m.listHeight()

	var names [][]string
	if m.navMode == navigationModeDetail {
		names, m.rows, m.columns = buildDetailFileList(m.showRawValue, m.files, width, height, func(name string, i, j int) {
			if m.findPrevName && m.prevName == name {
				m.c = i
				m.r = j
			}
		})
	} else { // navigationModeDetailDefault
		names, m.rows, m.columns = buildFilesList(m.files, width, height, func(name string, i, j int) {
			if m.findPrevName && m.prevName == name {
				m.c = i
				m.r = j
			}
		})
	}

	// If we need to select previous directory on "up".
	if m.findPrevName {
		m.findPrevName = false
		m.updateOffset()
		m.saveCursorPosition()
	}

	// After we have updated offset and saved cursor position, we can
	// preview currently selected file.
	m.preview()

	// Get output rows width before coloring.
	outputWidth := len(path.Base(m.path)) // Use current dir name as default.
	if m.previewMode {
		row := make([]string, m.columns)
		for i := 0; i < m.columns; i++ {
			if len(names[i]) > 0 {
				row[i] = names[i][0]
			} else {
				outputWidth = width
			}
		}
		outputWidth = max(outputWidth, len(Join(row, separator)))
	} else {
		outputWidth = width
	}

	// Let's add colors to file names.
	output := make([]string, m.rows)
	for j := 0; j < m.rows; j++ {
		row := make([]string, m.columns)
		for i := 0; i < m.columns; i++ {
			if i == m.c && j == m.r {
				if m.deleteCurrentFile {
					row[i] = danger.Render(names[i][j])
				} else {
					row[i] = cursor.Render(names[i][j])
				}
			} else {
				row[i] = names[i][j]
			}
		}
		output[j] = Join(row, separator)
	}

	if len(output) >= m.offset+height {
		output = output[m.offset : m.offset+height]
	}

	// Preview pane.
	fileName, _ := m.fileName()
	previewPane := bar.Render(fileName) + "\n"
	previewPane += m.previewContent

	// Location bar (grey).
	location := m.path
	if userHomeDir, err := os.UserHomeDir(); err == nil {
		location = Replace(m.path, userHomeDir, "~", 1)
	}
	if runtime.GOOS == "windows" {
		location = ReplaceAll(Replace(location, "\\/", fileSeparator, 1), "/", fileSeparator)
	}

	// Filter bar (green).
	filter := ""
	if m.searchMode {
		location = TrimSuffix(location, fileSeparator)
		filter = fileSeparator + m.search
	}
	barLen := len(location) + len(filter)
	if barLen > outputWidth {
		location = location[min(barLen-outputWidth, len(location)):]
	}
	barStr := bar.Render(location) + search.Render(filter)

	main := barStr + "\n" + Join(output, "\n")

	if m.err != nil {
		main = barStr + "\n" + warning.Render(m.err.Error())
	} else if len(m.files) == 0 {
		main = barStr + "\n" + warning.Render("No files")
	}

	// Delete bar.
	if len(m.toBeDeleted) > 0 {
		toDelete := m.toBeDeleted[len(m.toBeDeleted)-1]
		timeLeft := int(time.Until(toDelete.at).Seconds())
		deleteBar := fmt.Sprintf("%v deleted. (u)ndo %v", path.Base(toDelete.path), timeLeft)
		main += "\n" + danger.Render(deleteBar)
	}

	// Yank success.
	if m.yankSuccess {
		yankBar := fmt.Sprintf("yanked path to clipboard: %v", m.path)
		main += "\n" + bar.Render(yankBar)
	}

	if m.previewMode {
		return lipgloss.JoinHorizontal(
			lipgloss.Top,
			main,
			preview.
				MaxHeight(m.height).
				Render(previewPane),
		)
	} else {
		return main
	}
}

func (m *model) moveUp() {
	m.r--
	if m.r < 0 {
		m.r = m.rows - 1
		m.c--
	}
	if m.c < 0 {
		m.r = m.rows - 1 - (m.columns*m.rows - len(m.files))
		m.c = m.columns - 1
	}
}

func (m *model) moveDown() {
	m.r++
	if m.r >= m.rows {
		m.r = 0
		m.c++
	}
	if m.c >= m.columns {
		m.c = 0
	}
	if m.c == m.columns-1 && (m.columns-1)*m.rows+m.r >= len(m.files) {
		m.r = 0
		m.c = 0
	}
}

func (m *model) moveLeft() {
	m.c--
	if m.c < 0 {
		m.c = m.columns - 1
	}
	if m.c == m.columns-1 && (m.columns-1)*m.rows+m.r >= len(m.files) {
		m.r = m.rows - 1 - (m.columns*m.rows - len(m.files))
		m.c = m.columns - 1
	}
}

func (m *model) moveRight() {
	m.c++
	if m.c >= m.columns {
		m.c = 0
	}
	if m.c == m.columns-1 && (m.columns-1)*m.rows+m.r >= len(m.files) {
		m.r = m.rows - 1 - (m.columns*m.rows - len(m.files))
		m.c = m.columns - 1
	}
}

func (m *model) moveTop() {
	m.r = 0
}

func (m *model) moveBottom() {
	m.r = m.rows - 1
	if m.c == m.columns-1 && (m.columns-1)*m.rows+m.r >= len(m.files) {
		m.r = m.rows - 1 - (m.columns*m.rows - len(m.files))
	}
}

func (m *model) moveLeftmost() {
	m.c = 0
}

func (m *model) moveRightmost() {
	m.c = m.columns - 1
	if m.c == m.columns-1 && (m.columns-1)*m.rows+m.r >= len(m.files) {
		m.r = m.rows - 1 - (m.columns*m.rows - len(m.files))
		m.c = m.columns - 1
	}
}

func (m *model) moveStart() {
	m.moveLeftmost()
	m.moveTop()
}

func (m *model) moveEnd() {
	m.moveRightmost()
	m.moveBottom()
}

func (m *model) list() {
	var err error
	m.files = nil

	// ReadDir already returns files and dirs sorted by filename.
	files, err := os.ReadDir(m.path)
	if err != nil {
		m.err = err
		return
	} else {
		m.err = nil
	}

files:
	for _, file := range files {
		for _, toDelete := range m.toBeDeleted {
			if path.Join(m.path, file.Name()) == toDelete.path {
				continue files
			}
		}
		m.files = append(m.files, file)
	}
}

func (m *model) listHeight() int {
	h := m.height - 1 // Subtract 1 for location bar.
	if len(m.toBeDeleted) > 0 {
		h-- // Subtract 1 for delete bar.
	}
	return h
}

func (m *model) updateOffset() {
	height := m.listHeight()
	// Scrolling down.
	if m.r >= m.offset+height {
		m.offset = m.r - height + 1
	}
	// Scrolling up.
	if m.r < m.offset {
		m.offset = m.r
	}
	// Don't scroll more than there are rows.
	if m.offset > m.rows-height && m.rows > height {
		m.offset = m.rows - height
	}
}

func (m *model) saveCursorPosition() {
	m.positions[m.path] = position{
		c:      m.c,
		r:      m.r,
		offset: m.offset,
	}
}

func (m *model) fileName() (string, bool) {
	i := m.c*m.rows + m.r
	if i >= len(m.files) || i < 0 {
		return "", false
	}
	return m.files[i].Name(), true
}

func (m *model) filePath() (string, bool) {
	fileName, ok := m.fileName()
	if !ok {
		return fileName, false
	}
	return path.Join(m.path, fileName), true
}

func (m *model) openEditor() tea.Cmd {
	filePath, ok := m.filePath()
	if !ok {
		return nil
	}

	cmdline := Split(lookup([]string{"WALK_EDITOR", "EDITOR"}, "less"), " ")
	cmdline = append(cmdline, filePath)

	execCmd := exec.Command(cmdline[0], cmdline[1:]...)
	return tea.ExecProcess(execCmd, func(err error) tea.Msg {
		// Note: we could return a message here indicating that editing is
		// finished and altering our application about any errors. For now,
		// however, that's not necessary.
		return nil
	})
}

func (m *model) preview() {
	if !m.previewMode {
		return
	}
	filePath, ok := m.filePath()
	if !ok {
		return
	}

	fileInfo, err := os.Stat(filePath)
	if err != nil {
		return
	}

	width := m.width / 2
	height := m.height - 1 // Subtract 1 for name bar.

	if fileInfo.IsDir() {
		files, err := os.ReadDir(filePath)
		if err != nil {
			m.previewContent = err.Error()
		}

		var names [][]string
		var rows, columns int
		if m.navMode == navigationModeDetail {
			names, rows, columns = buildDetailFileList(m.showRawValue, files, width, height, nil)
		} else {
			names, rows, columns = buildFilesList(files, width, height, nil)
		}

		output := make([]string, rows)
		for j := 0; j < rows; j++ {
			row := make([]string, columns)
			for i := 0; i < columns; i++ {
				row[i] = names[i][j]
			}
			output[j] = Join(row, separator)
		}
		if len(output) >= height {
			output = output[0:height]
		}
		m.previewContent = Join(output, "\n")
		return
	}

	if isImageExt(filePath) {
		img, err := drawImage(filePath, width, height)
		if err != nil {
			m.previewContent = warning.Render("No image preview available")
			return
		}
		m.previewContent = img
		return
	}

	var content []byte
	// If file is too big (> 100kb), read only first 100kb.
	if fileInfo.Size() > 100*1024 {
		file, err := os.Open(filePath)
		if err != nil {
			m.previewContent = err.Error()
			return
		}
		defer file.Close()
		content = make([]byte, 100*1024)
		_, err = file.Read(content)
		if err != nil {
			m.previewContent = err.Error()
			return
		}
	} else {
		content, err = os.ReadFile(filePath)
		if err != nil {
			m.previewContent = err.Error()
			return
		}
	}

	switch {
	case utf8.Valid(content):
		m.previewContent = leaveOnlyAscii(content)
	default:
		m.previewContent = warning.Render("No preview available")
	}
}

func leaveOnlyAscii(content []byte) string {
	var result []byte

	for _, b := range content {
		if b == '\t' {
			result = append(result, ' ', ' ', ' ', ' ')
		} else if b == '\r' {
			continue
		} else if (b >= 32 && b <= 127) || b == '\n' { // '\n' is kept if newline needs to be retained
			result = append(result, b)
		}
	}

	return string(result)
}

func padRight(s string, n int) string {
	if n == 0 || len(s) == n {
		return s
	} else if n < len(s) {
		return s
	}

	return s + Repeat(" ", n-len(s))
}

func padLeft(s string, n int) string {
	if n == 0 || len(s) == n {
		return s
	} else if n < len(s) {
		return s
	}

	return Repeat(" ", n-len(s)) + s
}

func formatSizeOf(value float64, suffix string, factor float64) string {
	isIntegral := value == float64(int64(value))
	if isIntegral && math.Abs(value) < 999.5 {
		return fmt.Sprintf("%d%s", int64(value), suffix)
	}

	unit := []string{"", "k", "M", "G", "T", "P", "E", "Z"}
	for _, u := range unit {
		if math.Abs(value) < 999.5 {
			if math.Abs(value) < 99.95 {
				if math.Abs(value) < 9.995 {
					return fmt.Sprintf("%1.2f%s%s", value, u, suffix)
				}
				return fmt.Sprintf("%2.1f%s%s", value, u, suffix)
			}
			return fmt.Sprintf("%3.0f%s%s", value, u, suffix)
		}
		value = value / factor
	}
	return fmt.Sprintf("%3.1fY%s", value, suffix)
}

func buildDetailFileList(showRaw bool, files []os.DirEntry, width int, height int, callback func(name string, i, j int)) ([][]string, int, int) {
	names := make([]string, len(files))

	// should use different format on Windows

	uidLen := 0
	gidLen := 0
	sizeLen := 0

	userNames := map[uint32]string{}
	groupNames := map[uint32]string{}

	errorName := "<ERROR>"

	for n := 0; n < len(files); n++ {
		info, err := files[n].Info()
		if err != nil {
			continue
		}

		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			if _, ok := userNames[stat.Uid]; !ok {
				uid := strconv.FormatUint(uint64(stat.Uid), 10)
				if showRaw {
					uidLen = max(uidLen, len(uid))
					userNames[stat.Uid] = uid
				} else {
					u, err := user.LookupId(uid)
					if err == nil {
						uidLen = max(uidLen, len(u.Username))
						userNames[stat.Uid] = u.Username
					} else {
						uidLen = max(uidLen, len(errorName))
						userNames[stat.Uid] = errorName
					}
				}
			}

			if _, ok := groupNames[stat.Gid]; !ok {
				gid := strconv.FormatUint(uint64(stat.Gid), 10)
				if showRaw {
					gidLen = max(gidLen, len(gid))
					groupNames[stat.Gid] = gid
				} else {
					g, err := user.LookupGroupId(gid)
					if err == nil {
						gidLen = max(gidLen, len(g.Name))
						groupNames[stat.Gid] = g.Name
					} else {
						gidLen = max(gidLen, len(errorName))
						groupNames[stat.Gid] = errorName
					}
				}
			}
		}

		if showRaw {
			sizeStr := strconv.FormatInt(info.Size(), 10)
			sizeLen = max(sizeLen, len(sizeStr))
		} else {
			sizeStr := formatSizeOf(float64(info.Size()), "B", 1024)
			sizeLen = max(sizeLen, len(sizeStr))
		}
	}

	nameLen := 0
	for i := 0; i < len(files); i++ {
		info, err := files[i].Info()
		if err != nil {
			names[i] = fmt.Sprintf("[ERR] %s", files[i].Name())
			continue
		}

		cols := make([]string, 0, 5)

		mode := info.Mode()
		if showRaw {
			// x0777
			cols = append(cols, fmt.Sprintf("%s 0%03o", mode.String()[:1], mode.Perm()))
		} else {
			// drwxr-xr-x
			cols = append(cols, mode.String())
		}

		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			cols = append(
				cols,
				fmt.Sprintf("%3d", stat.Nlink),
				padRight(userNames[stat.Uid], uidLen),
				padRight(groupNames[stat.Gid], gidLen),
			)
		}

		if showRaw {
			cols = append(cols, padLeft(strconv.FormatInt(info.Size(), 10), sizeLen))
		} else {
			cols = append(cols, padLeft(formatSizeOf(float64(info.Size()), "B", 1024), sizeLen))
		}

		if showRaw {
			cols = append(cols, strconv.FormatInt(info.ModTime().Unix(), 10))
		} else {
			cols = append(cols, info.ModTime().Format(time.DateTime))
		}

		name := ""
		if showIcons {
			if err == nil {
				icon := icons.getIcon(info)
				if icon != "" {
					name += icon + " "
				}
			}
		}

		name += info.Name()
		if callback != nil {
			callback(files[i].Name(), 0, i)
		}

		if files[i].IsDir() {
			// Dirs should have a slash at the end.
			name += fileSeparator
		}

		cols = append(cols, name)

		row := Join(cols, " ")
		nameLen = max(nameLen, len(row))

		names[i] = row
	}

	for i := 0; i < len(names); i++ {
		names[i] += Repeat(" ", nameLen-len(names[i]))
	}

	return [][]string{names}, len(files), 1
}

func buildFilesList(files []os.DirEntry, width int, height int, callback func(name string, i, j int)) ([][]string, int, int) {
	// If it's possible to fit all files in one column on a third of the screen,
	// just use one column. Otherwise, let's squeeze listing in half of screen.
	columns := len(files) / (height / 3)
	if columns <= 0 {
		columns = 1
	}

start:
	// Let's try to fit everything in terminal width with this many columns.
	// If we are not able to do it, decrease column number and goto start.
	rows := int(math.Ceil(float64(len(files)) / float64(columns)))
	names := make([][]string, columns)
	n := 0

	for i := 0; i < columns; i++ {
		names[i] = make([]string, rows)
		// Columns size is going to be of max file name size.
		max := 0
		for j := 0; j < rows; j++ {
			name := ""
			if n < len(files) {
				if showIcons {
					info, err := files[n].Info()
					if err == nil {
						icon := icons.getIcon(info)
						if icon != "" {
							name += icon + " "
						}
					}
				}
				name += files[n].Name()
				if callback != nil {
					callback(files[n].Name(), i, j)
				}
				if files[n].IsDir() {
					// Dirs should have a slash at the end.
					name += fileSeparator
				}
				n++
			}
			if max < len(name) {
				max = len(name)
			}
			names[i][j] = name
		}
		// Append spaces to make all names in one column of same size.
		for j := 0; j < rows; j++ {
			names[i][j] += Repeat(" ", max-len(names[i][j]))
		}
	}
	for j := 0; j < rows; j++ {
		row := make([]string, columns)
		for i := 0; i < columns; i++ {
			row[i] = names[i][j]
		}
		if len(Join(row, separator)) > width && columns > 1 {
			// Yep. No luck, let's decrease number of columns and try one more time.
			columns--
			goto start
		}
	}
	return names, rows, columns
}

func (m *model) dontDoPendingDeletions() {
	for _, toDelete := range m.toBeDeleted {
		fmt.Fprintf(os.Stderr, "Was not deleted: %v\n", toDelete.path)
	}
}

func (m *model) performPendingDeletions() {
	for _, toDelete := range m.toBeDeleted {
		_ = os.RemoveAll(toDelete.path)
	}
	m.toBeDeleted = nil
}

func fileInfo(path string) os.FileInfo {
	fi, err := os.Stat(path)
	if err != nil {
		panic(err)
	}
	return fi
}

func lookup(names []string, val string) string {
	for _, name := range names {
		val, ok := os.LookupEnv(name)
		if ok && val != "" {
			return val
		}
	}
	return val
}

func usage() {
	_, _ = fmt.Fprintf(os.Stderr, "\n  "+cursor.Render(" walk ")+"\n\n  Usage: walk [path]\n\n")
	w := tabwriter.NewWriter(os.Stderr, 0, 8, 2, ' ', 0)
	put := func(s string) {
		_, _ = fmt.Fprintln(w, s)
	}
	put("    Arrows, hjkl\tMove cursor")
	put("    Enter\tEnter directory")
	put("    Backspace\tExit directory")
	put("    Space\tToggle preview")
	put("    Esc, q\tExit with cd")
	put("    Ctrl+c\tExit without cd")
	put("    /\tFuzzy search")
	put("    dd\tDelete file or dir")
	put("    y\tYank current directory path to clipboard")
	put("\n  Flags:\n")
	put("    --icons\tdisplay icons")
	_ = w.Flush()
	_, _ = fmt.Fprintf(os.Stderr, "\n")
	os.Exit(1)
}

func version() {
	fmt.Printf("\n  %s %s\n\n", cursor.Render(" walk "), Version)
	os.Exit(0)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
