package tui

import (
	"charm.land/bubbles/v2/key"
	"charm.land/lipgloss/v2"
)

// prefix keys start a two-stroke binding. There is no timeout: the next key
// press always resolves them.
const (
	prefixGoto   = "g"
	prefixSelect = "*"
)

type keyMap struct {
	Down     key.Binding
	Up       key.Binding
	Bottom   key.Binding
	HalfDown key.Binding
	HalfUp   key.Binding

	NextTab key.Binding
	PrevTab key.Binding

	Select     key.Binding
	Archive    key.Binding
	MarkRead   key.Binding
	MarkUnread key.Binding

	Open           key.Binding
	Refresh        key.Binding
	ToggleShowRead key.Binding
	Filter         key.Binding

	Escape key.Binding
	Help   key.Binding
	Quit   key.Binding

	AuthStart key.Binding
}

func defaultKeyMap() keyMap {
	return keyMap{
		Down:     key.NewBinding(key.WithKeys("j", "down"), key.WithHelp("j / ↓", "move down")),
		Up:       key.NewBinding(key.WithKeys("k", "up"), key.WithHelp("k / ↑", "move up")),
		Bottom:   key.NewBinding(key.WithKeys("G"), key.WithHelp("G", "go to last")),
		HalfDown: key.NewBinding(key.WithKeys("ctrl+d"), key.WithHelp("ctrl+d", "half page down")),
		HalfUp:   key.NewBinding(key.WithKeys("ctrl+u"), key.WithHelp("ctrl+u", "half page up")),

		NextTab: key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next tab")),
		PrevTab: key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "previous tab")),

		Select:     key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "toggle selection")),
		Archive:    key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "archive (done)")),
		MarkRead:   key.NewBinding(key.WithKeys("I"), key.WithHelp("I", "mark read")),
		MarkUnread: key.NewBinding(key.WithKeys("U"), key.WithHelp("U", "mark unread")),

		Open:           key.NewBinding(key.WithKeys("enter", "o"), key.WithHelp("enter / o", "open in browser")),
		Refresh:        key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "poll now")),
		ToggleShowRead: key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "unread only / all")),
		Filter:         key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),

		Escape: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "close help / clear filter / stop archiving")),
		Help:   key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:   key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),

		AuthStart: key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "sign in with GitHub")),
	}
}

// helpEntry is one row of the help overlay.
type helpEntry struct {
	keys string
	desc string
}

// legendEntry is one row of the marker legend. It carries a style because the
// legend has to show the colour the list uses, not just the shape.
type legendEntry struct {
	symbol string
	style  lipgloss.Style
	desc   string
}

// markerLegend explains the marker columns. The keys help lists what to press;
// this lists what the row is telling you.
func markerLegend() []legendEntry {
	return []legendEntry{
		{authorBar, styleAuthorBar, "you opened it"},
		{"x", styleSelected, "selected"},
		{"●", styleUnread, "unread"},
		{"○", styleRead, "read"},
		{"R", styleReview, "review requested"},
		{"M", styleMerged, "merged"},
		{"C", styleClosed, "closed without merging"},
	}
}

func (k keyMap) helpEntries() []helpEntry {
	bindings := []key.Binding{
		k.Down, k.Up, k.Bottom, k.HalfDown, k.HalfUp,
		k.NextTab, k.PrevTab,
		k.Select, k.Archive, k.MarkRead, k.MarkUnread,
		k.Open, k.Refresh, k.ToggleShowRead, k.Filter,
		k.Escape, k.Help, k.Quit,
	}

	entries := make([]helpEntry, 0, len(bindings)+3)
	entries = append(entries,
		helpEntry{"g g", "go to first"},
		helpEntry{"1 … 5", "switch to tab"},
	)
	for _, b := range bindings {
		entries = append(entries, helpEntry{b.Help().Key, b.Help().Desc})
	}
	entries = append(entries,
		helpEntry{"* a", "select everything shown"},
		helpEntry{"* n", "clear the selection"},
	)
	return entries
}
