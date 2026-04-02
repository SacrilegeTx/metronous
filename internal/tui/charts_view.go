package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/kiosvantra/metronous/internal/store"
)

const chartsRefreshInterval = 2 * time.Second

type chartsTickMsg struct{ t time.Time }

type ChartsDataMsg struct {
	Rows []store.DailyCostByModelRow
	Err  error
}

// ChartsModel renders daily cost charts per model for a selected month.
type ChartsModel struct {
	es store.EventStore

	width  int
	height int

	monthStart time.Time // local midnight of the 1st day of selected month

	loading bool
	err     error

	dailyRows []store.DailyCostByModelRow
}

// NewChartsModel creates a ChartsModel wired to the given EventStore.
func NewChartsModel(es store.EventStore) ChartsModel {
	now := time.Now()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.Local)
	return ChartsModel{
		es:         es,
		monthStart: monthStart,
		loading:    true,
	}
}

func (m ChartsModel) Init() tea.Cmd {
	return tea.Batch(
		m.fetchDailyCost(),
		tea.Tick(chartsRefreshInterval, func(t time.Time) tea.Msg { return chartsTickMsg{t: t} }),
	)
}

func (m ChartsModel) Update(msg tea.Msg) (ChartsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case chartsTickMsg:
		return m, tea.Batch(
			tea.Tick(chartsRefreshInterval, func(t time.Time) tea.Msg { return chartsTickMsg{t: t} }),
			m.fetchDailyCost(),
		)

	case ChartsDataMsg:
		m.loading = false
		m.err = msg.Err
		if msg.Err == nil {
			m.dailyRows = msg.Rows
		}
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyLeft:
			m.monthStart = m.monthStart.AddDate(0, -1, 0)
			m.loading = true
			return m, m.fetchDailyCost()
		case tea.KeyRight:
			m.monthStart = m.monthStart.AddDate(0, 1, 0)
			m.loading = true
			return m, m.fetchDailyCost()
		}
	}

	return m, nil
}

func (m ChartsModel) fetchDailyCost() tea.Cmd {
	return func() tea.Msg {
		if m.es == nil {
			return ChartsDataMsg{Rows: nil, Err: nil}
		}
		startLocal := m.monthStart
		endLocal := startLocal.AddDate(0, 1, 0)
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		rows, err := m.es.QueryDailyCostByModel(ctx, startLocal.UTC(), endLocal.UTC())
		return ChartsDataMsg{Rows: rows, Err: err}
	}
}

func (m ChartsModel) View() string {
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("86")).Render("Charts")

	sub := fmt.Sprintf("Daily cost by model (stacked) — %s  (use ←/→)", m.monthStart.Format("January 2006"))

	palette := []lipgloss.Color{
		lipgloss.Color("82"),  // green
		lipgloss.Color("226"), // yellow/orange
		lipgloss.Color("196"), // red
		lipgloss.Color("33"),  // blue
		lipgloss.Color("93"),  // purple
		lipgloss.Color("214"), // pink
		lipgloss.Color("118"), // teal
		lipgloss.Color("45"),  // violet
	}

	// Build totals per model for legend ordering and segment capping.
	totals := make(map[string]float64)
	for _, r := range m.dailyRows {
		totals[r.Model] += r.TotalCostUSD
	}

	modelsByTotal := make([]struct {
		Model string
		Total float64
	}, 0, len(totals))
	for model, total := range totals {
		modelsByTotal = append(modelsByTotal, struct {
			Model string
			Total float64
		}{Model: model, Total: total})
	}
	sort.SliceStable(modelsByTotal, func(i, j int) bool {
		return modelsByTotal[i].Total > modelsByTotal[j].Total
	})

	maxSegments := len(palette)
	segmentModels := []string{}
	otherTotal := 0.0
	for i, mrow := range modelsByTotal {
		if i < maxSegments-1 {
			segmentModels = append(segmentModels, mrow.Model)
		} else {
			otherTotal += mrow.Total
		}
	}
	if len(modelsByTotal) > maxSegments-1 {
		segmentModels = append(segmentModels, "Other")
	} else if len(modelsByTotal) == 0 {
		segmentModels = []string{}
	}

	colors := make(map[string]lipgloss.Color)
	for i, model := range segmentModels {
		colors[model] = palette[i%len(palette)]
	}

	legend := ""
	if len(segmentModels) > 0 {
		parts := make([]string, 0, len(segmentModels))
		for _, model := range segmentModels {
			c := colors[model]
			block := lipgloss.NewStyle().Foreground(c).Render("█")
			parts = append(parts, fmt.Sprintf("%s %s", block, model))
		}
		legend = "Legend: " + strings.Join(parts, "  ")
	} else {
		legend = "Legend: (no data for selected month)"
	}

	// Prepare day buckets for the selected month.
	start := time.Date(m.monthStart.Year(), m.monthStart.Month(), 1, 0, 0, 0, 0, time.Local)
	end := start.AddDate(0, 1, 0)

	days := make([]time.Time, 0, 32)
	for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
		days = append(days, d)
	}

	costs := make(map[string]map[string]float64) // dayKey -> model -> cost
	for _, r := range m.dailyRows {
		dayKey := r.Day.Format("2006-01-02")
		bucket := costs[dayKey]
		if bucket == nil {
			bucket = make(map[string]float64)
			costs[dayKey] = bucket
		}
		// If model is not in segmentModels, route it into Other.
		if _, ok := colors[r.Model]; ok {
			bucket[r.Model] += r.TotalCostUSD
		} else {
			bucket["Other"] += r.TotalCostUSD
		}
	}

	lines := []string{}
	lines = append(lines, title+"\n"+sub)
	if m.loading {
		lines = append(lines, "Loading…")
	}
	lines = append(lines, legend)
	if m.err != nil {
		lines = append(lines, lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render("Error: "+m.err.Error()))
	}

	// Vertical stacked bar chart (one column per day).
	maxCols := m.width - 2
	if maxCols <= 0 {
		maxCols = 31
	}
	if maxCols < 6 {
		maxCols = 6
	}
	chunkSize := maxCols
	if chunkSize > len(days) {
		chunkSize = len(days)
	}

	barHeight := 10
	if m.height > 0 {
		barHeight = m.height - 10
		if barHeight < 6 {
			barHeight = 6
		}
		if barHeight > 18 {
			barHeight = 18
		}
	}

	blocks := make(map[string]string)
	for _, model := range segmentModels {
		blocks[model] = lipgloss.NewStyle().Foreground(colors[model]).Render("█")
	}

	dayTotals := make([]float64, 0, len(days))
	for _, d := range days {
		rowCosts := costs[d.Format("2006-01-02")]
		total := 0.0
		for _, model := range segmentModels {
			if rowCosts != nil {
				total += rowCosts[model]
			}
		}
		dayTotals = append(dayTotals, total)
	}

	if len(days) == 0 {
		lines = append(lines, "No data")
		return strings.Join(lines, "\n")
	}

	for chunkStart := 0; chunkStart < len(days); chunkStart += chunkSize {
		chunkEnd := chunkStart + chunkSize
		if chunkEnd > len(days) {
			chunkEnd = len(days)
		}
		chunkDays := days[chunkStart:chunkEnd]
		chunkTotals := dayTotals[chunkStart:chunkEnd]

		heightsPerDay := make([][]int, len(chunkDays))
		for i, d := range chunkDays {
			rowCosts := costs[d.Format("2006-01-02")]
			total := chunkTotals[i]
			if total <= 0 || len(segmentModels) == 0 {
				heightsPerDay[i] = make([]int, len(segmentModels))
				continue
			}

			floors := make([]int, len(segmentModels))
			fracs := make([]float64, len(segmentModels))
			sumFloors := 0
			for j, model := range segmentModels {
				c := 0.0
				if rowCosts != nil {
					c = rowCosts[model]
				}
				e := (float64(barHeight) * c) / total
				f := int(e)
				floors[j] = f
				fracs[j] = e - float64(f)
				sumFloors += f
			}

			rem := barHeight - sumFloors
			heights := make([]int, len(segmentModels))
			copy(heights, floors)
			if rem > 0 {
				idx := make([]int, len(segmentModels))
				for k := range idx {
					idx[k] = k
				}
				sort.SliceStable(idx, func(a, b int) bool {
					return fracs[idx[a]] > fracs[idx[b]]
				})
				for k := 0; k < rem && k < len(idx); k++ {
					heights[idx[k]]++
				}
			}
			heightsPerDay[i] = heights
		}

		// Render bar area.
		for y := barHeight; y >= 1; y-- {
			var row strings.Builder
			for i := range chunkDays {
				total := chunkTotals[i]
				if total <= 0 || len(segmentModels) == 0 {
					row.WriteByte(' ')
					continue
				}

				seg := -1
				cum := 0
				heights := heightsPerDay[i]
				for j := range segmentModels {
					cum += heights[j]
					if y <= cum {
						seg = j
						break
					}
				}
				if seg < 0 {
					row.WriteByte(' ')
					continue
				}
				row.WriteString(blocks[segmentModels[seg]])
			}
			lines = append(lines, row.String())
		}

		// X-axis labels (last digit) to keep width tight.
		var xlabels strings.Builder
		for _, d := range chunkDays {
			xlabels.WriteString(fmt.Sprintf("%d", d.Day()%10))
		}
		lines = append(lines, xlabels.String())

		if chunkEnd < len(days) {
			lines = append(lines, "")
		}
	}

	return strings.Join(lines, "\n")
}
