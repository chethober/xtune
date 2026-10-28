package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The dashboard is a full-screen overview of a deployment: the server's configs with their
// state (active, quiet/probably blocked, idle), and the users with quota bars and who is online.
// It reads the state and usage the status and users menus already show, but on one live screen
// that refreshes, instead of a static dump. It is read-only: changes stay in the menus.

// dashData is a snapshot of what the dashboard draws. It is built by snapshotDash, which takes no
// terminal and no server, so it can be tested on its own.
type dashData struct {
	Host      string
	IPBlocked string // "" unless the IP looks blocked, else why
	Configs   []dashConfig
	Users     []dashUser
	NUsers    int
	NActive   int
	NOnline   int
}

type dashConfig struct {
	ID, Kind string
	Port     int
	Proto    string
	State    string // active | quiet | idle | follower
	Server   string // set for a follower's config
	Bytes    int64
	Since    time.Time // when the state last changed, zero if unknown
}

type dashUser struct {
	Name     string
	Status   string // active | disabled | expired | over quota
	Up, Down int64
	Quota    int64
	Percent  int // of quota, -1 when unlimited
	Online   int
	Devices  int // limit, 0 = unlimited
	Expires  int64
}

// snapshotDash gathers the dashboard's data from a deployment. now is passed in so tests fix it.
func snapshotDash(st *State, us *Usage, now time.Time) dashData {
	d := dashData{Host: st.Host}
	if b, why := ipBlocked(st, us, now); b {
		d.IPBlocked = why
	}
	quiet := quietEntries(st, us, now)
	for _, srv := range st.servers() {
		for _, e := range srv.Entries {
			c := dashConfig{ID: e.ID, Kind: e.Kind, Port: e.Port, Proto: e.Proto(), State: "idle"}
			eu := us.Entries[entryKey(e)]
			switch {
			case srv != st:
				c.State, c.Server = "follower", srv.Host
			case quiet[e.ID] > 0:
				c.State, c.Since = "quiet", now.Add(-quiet[e.ID])
			case eu != nil && eu.Active > 0:
				c.State, c.Bytes, c.Since = "active", eu.Bytes, time.Unix(eu.Active, 0)
			}
			d.Configs = append(d.Configs, c)
		}
	}
	for _, u := range st.Users {
		uu := us.Users[u.Name]
		if uu == nil {
			uu = &UserUsage{}
		}
		du := dashUser{Name: u.Name, Status: userStatus(u, uu, now), Up: uu.Up, Down: uu.Down,
			Quota: u.Quota, Percent: -1, Online: len(uu.Online), Devices: u.Devices, Expires: u.Expires}
		if u.Quota > 0 {
			du.Percent = int(min(100, (uu.Up+uu.Down)*100/u.Quota))
		}
		d.Users = append(d.Users, du)
		d.NUsers++
		if du.Status == "active" {
			d.NActive++
		}
		d.NOnline += du.Online
	}
	// busiest users first, so a crowded server shows who matters at the top
	sort.SliceStable(d.Users, func(i, j int) bool {
		return d.Users[i].Up+d.Users[i].Down > d.Users[j].Up+d.Users[j].Down
	})
	return d
}

// ---------------------------------------------------------------- rendering

var (
	dashHead  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	dashCol   = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	dashBad   = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	dashWarn  = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	dashGood  = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	dashFaint = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)

// stateMark is the coloured dot and label for a config's state.
func stateMark(state string) string {
	switch state {
	case "active":
		return dashGood.Render("● active")
	case "quiet":
		return dashBad.Render("● quiet (blocked?)")
	case "follower":
		return dashCol.Render("● follower")
	}
	return dashFaint.Render("○ idle")
}

// statusMark is the coloured label for a user's status.
func statusMark(status string) string {
	switch status {
	case "active":
		return dashGood.Render(status)
	case "disabled":
		return dashFaint.Render(status)
	}
	return dashBad.Render(status)
}

// bar is a quota meter: pct of width filled, coloured by how full it is. -1 (unlimited) is shown
// as a dim rule.
func bar(pct, width int) string {
	if pct < 0 {
		return dashFaint.Render(strings.Repeat("─", width))
	}
	fill := pct * width / 100
	st := dashGood
	switch {
	case pct >= 100:
		st = dashBad
	case pct >= 80:
		st = dashWarn
	}
	return st.Render(strings.Repeat("█", fill)) + dashFaint.Render(strings.Repeat("░", width-fill))
}

// ago is a short "5m", "3h", "2d" form of how long ago t was, or "-" for the zero time.
func ago(t, now time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// render draws the dashboard. width is the terminal width (0 for a default); only the user table
// is truncated to it, so a narrow terminal still shows every config.
func (d dashData) render(now time.Time, width int) string {
	if width < 40 {
		width = 80
	}
	var b strings.Builder
	title := "xtune  ·  " + d.Host
	if d.IPBlocked != "" {
		title += "  " + dashBad.Render("⚠ IP looks blocked: "+d.IPBlocked)
	}
	b.WriteString(dashHead.Render(title) + "\n")
	b.WriteString(dashCol.Render(fmt.Sprintf("%d users · %d active · %d online now", d.NUsers, d.NActive, d.NOnline)) + "\n\n")

	b.WriteString(dashHead.Render("Configs") + "\n")
	for _, c := range d.Configs {
		line := fmt.Sprintf("  %-16s %-14s %5d/%-4s  %-20s", c.ID, c.Kind, c.Port, c.Proto, stateMark(c.State))
		switch {
		case c.State == "active":
			line += dashCol.Render(fmt.Sprintf("  %s, last %s", fmtBytes(c.Bytes), ago(c.Since, now)))
		case c.State == "quiet":
			line += dashCol.Render("  since " + ago(c.Since, now))
		case c.State == "follower" && c.Server != "":
			line += dashCol.Render("  " + c.Server)
		}
		b.WriteString(line + "\n")
	}
	if len(d.Configs) == 0 {
		b.WriteString(dashFaint.Render("  nothing deployed yet") + "\n")
	}

	b.WriteString("\n" + dashHead.Render("Users") + "\n")
	if len(d.Users) == 0 {
		b.WriteString(dashFaint.Render("  no users yet") + "\n")
	}
	bw := 16
	for _, u := range d.Users {
		quota := "unlimited"
		if u.Quota > 0 {
			quota = fmt.Sprintf("%s / %s", fmtBytes(u.Up+u.Down), fmtBytes(u.Quota))
		}
		online := ""
		if u.Online > 0 {
			d := ""
			if u.Devices > 0 {
				d = fmt.Sprintf("/%d", u.Devices)
			}
			online = dashGood.Render(fmt.Sprintf("  ↑%d%s online", u.Online, d))
		}
		b.WriteString(fmt.Sprintf("  %-16s %-9s %s %-22s%s\n",
			trunc(u.Name, 16), statusMark(u.Status), bar(u.Percent, bw), quota, online))
	}
	b.WriteString("\n" + dashFaint.Render("r refresh · q back") + "\n")
	return b.String()
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

// ---------------------------------------------------------------- bubble tea

// dashModel is the live dashboard: it reloads the snapshot on r and on a timer.
type dashModel struct {
	r      *Server
	data   dashData
	err    error
	width  int
	height int
}

type dashTick time.Time

func dashReload(r *Server) tea.Cmd {
	return func() tea.Msg {
		st, err := LoadState(r)
		if err != nil {
			return err
		}
		if st == nil {
			return dashData{Host: r.Conn.Host}
		}
		us, err := LoadUsage(r)
		if err != nil {
			return err
		}
		return snapshotDash(st, us, time.Now())
	}
}

func dashTicker() tea.Cmd {
	return tea.Tick(5*time.Second, func(t time.Time) tea.Msg { return dashTick(t) })
}

func (m dashModel) Init() tea.Cmd { return tea.Batch(dashReload(m.r), dashTicker()) }

func (m dashModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		case "r":
			return m, dashReload(m.r)
		}
	case dashTick:
		return m, tea.Batch(dashReload(m.r), dashTicker())
	case dashData:
		m.data, m.err = msg, nil
	case error:
		m.err = msg
	}
	return m, nil
}

func (m dashModel) View() string {
	if m.err != nil {
		return errStyle.Render("error: ") + m.err.Error() + "\n\n" + dashFaint.Render("r retry · q back") + "\n"
	}
	return m.data.render(time.Now(), m.width)
}

// dashboard runs the live dashboard until the user presses q.
func (t *tui) dashboard() error {
	_, err := tea.NewProgram(dashModel{r: t.r}, tea.WithAltScreen()).Run()
	return err
}
