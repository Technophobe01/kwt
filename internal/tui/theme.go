package tui

import "charm.land/lipgloss/v2"

type theme struct {
	header  lipgloss.Style
	dim     lipgloss.Style
	cursor  lipgloss.Style
	error   lipgloss.Style
	success lipgloss.Style
	live    lipgloss.Style
}

func newTheme() theme {
	return theme{
		header:  lipgloss.NewStyle().Bold(true),
		dim:     lipgloss.NewStyle().Faint(true),
		cursor:  lipgloss.NewStyle().Bold(true),
		error:   lipgloss.NewStyle().Foreground(lipgloss.Color("9")),
		success: lipgloss.NewStyle().Foreground(lipgloss.Color("10")),
		live:    lipgloss.NewStyle().Foreground(lipgloss.Color("10")),
	}
}
