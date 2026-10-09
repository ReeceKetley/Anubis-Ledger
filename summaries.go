package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func section(body, name string) string {
	lines := strings.Split(body, "\n")
	var out []string
	found := false
	for _, line := range lines {
		if strings.EqualFold(strings.TrimSpace(line), "## "+name) {
			found = true
			continue
		}
		if found && strings.HasPrefix(line, "## ") {
			break
		}
		if found && strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
func readSummary(s *Store, name string) string {
	b, err := os.ReadFile(filepath.Join(s.Dir, name))
	if err != nil {
		return ""
	}
	return string(b)
}
func meaningful(v string) string {
	if strings.HasPrefix(v, "Describe ") || strings.HasPrefix(v, "List important ") || strings.HasPrefix(v, "List things ") {
		return "Not filled in yet."
	}
	return v
}

var numberedPoint = regexp.MustCompile(`^\d+[.)]\s+`)

func firstPoint(v string) string {
	var parts []string
	for _, raw := range strings.Split(v, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if len(parts) > 0 && (strings.HasPrefix(line, "- ") || numberedPoint.MatchString(line)) {
			break
		}
		parts = append(parts, line)
	}
	point := strings.TrimPrefix(strings.Join(parts, " "), "- ")
	point = numberedPoint.ReplaceAllString(point, "")
	return truncateRunes(point, 500)
}
func firstAction(v string) string {
	lines := strings.Split(v, "\n")
	for i, line := range lines {
		if numberedPoint.MatchString(strings.TrimSpace(line)) {
			return firstPoint(strings.Join(lines[i:], "\n"))
		}
	}
	return firstPoint(v)
}
func cmdContext(s *Store, args []string, out io.Writer) error {
	var limit int
	var js, short, normal, deep bool
	f, err := flags("context", args, func(f *flag.FlagSet) {
		f.IntVar(&limit, "limit", 20, "entry limit")
		f.BoolVar(&js, "json", false, "JSON output")
		f.BoolVar(&short, "short", false, "compact context")
		f.BoolVar(&normal, "normal", false, "standard context")
		f.BoolVar(&deep, "deep", false, "expanded context")
	})
	if err != nil {
		return err
	}
	if len(f.Args()) != 1 {
		return errors.New("usage: ledger context TASK [--limit N] [--json]")
	}
	if boolCount(short, normal, deep) > 1 {
		return errors.New("choose only one context depth")
	}
	if short && f.Lookup("limit").Value.String() == "20" {
		limit = 8
	}
	if deep && f.Lookup("limit").Value.String() == "20" {
		limit = 40
	}
	if limit < 1 || limit > 200 {
		return errors.New("limit must be between 1 and 200")
	}
	entries, err := s.related(f.Args()[0], limit)
	if err != nil {
		return err
	}
	goal, state := readSummary(s, "GOAL.md"), readSummary(s, "STATE.md")
	critical, err := s.search("", SearchOptions{Type: "WARNING", Status: "ACTIVE", Limit: 30})
	if err != nil {
		return err
	}
	for _, e := range critical {
		if e.Importance != "critical" {
			continue
		}
		found := false
		for _, v := range entries {
			if e.ID == v.ID {
				found = true
			}
		}
		if !found {
			entries = append([]Entry{e}, entries...)
			if len(entries) > limit {
				entries = entries[:limit]
			}
		}
	}
	if js {
		fmt.Fprintln(out, jsonOutput(map[string]any{"task": f.Args()[0], "goal": goal, "state": state, "entries": entries}))
		return nil
	}
	fmt.Fprint(out, "# Ledger Context\n\n")
	fmt.Fprintln(out, "## Project Goal\n", meaningful(section(goal, "Primary Objective")))
	stateText := strings.TrimSpace(state)
	if !deep {
		parts := []string{}
		for _, name := range []string{"Working", "Broken or Blocked", "Next Work"} {
			point := firstPoint(section(state, name))
			if name == "Next Work" {
				point = firstAction(section(state, name))
			}
			if v := point; v != "" {
				parts = append(parts, name+": "+v)
			}
		}
		stateText = strings.Join(parts, "\n")
	}
	fmt.Fprintln(out, "\n## Current State\n", stateText)
	budget, perEntry := 10000, 650
	if short {
		budget, perEntry = 3500, 300
	}
	if deep {
		budget, perEntry = 26000, 1500
	}
	used := 0
	groups := []struct {
		title string
		types []string
	}{{"Relevant Facts", []string{"FACT"}}, {"Relevant Decisions", []string{"DECISION"}}, {"Previous Experiments", []string{"EXPERIMENT"}}, {"Known Failures", []string{"FAILURE"}}, {"Warnings", []string{"WARNING"}}, {"Open Questions", []string{"HYPOTHESIS", "TASK"}}, {"Other Notes", []string{"NOTE"}}}
	for _, group := range groups {
		var selected []Entry
		for _, e := range entries {
			for _, typ := range group.types {
				if e.Type == typ {
					selected = append(selected, e)
				}
			}
		}
		if len(selected) == 0 {
			continue
		}
		fmt.Fprintln(out, "\n## "+group.title)
		for _, e := range selected {
			if used >= budget {
				break
			}
			body := truncateRunes(e.Body, perEntry)
			used += len(body) + len(e.Title) + 60
			fmt.Fprintf(out, "\n- **%s** [%s; %s] %s: %s", e.ID, e.Status, importance(e), e.Title, body)
			if len(e.Evidence) > 0 {
				fmt.Fprintf(out, " (evidence: %s)", strings.Join(e.Evidence, ", "))
			}
			fmt.Fprintln(out)
		}
	}
	var files []string
	seen := map[string]bool{}
	for _, e := range entries {
		for _, f := range append(e.Files, e.Entities...) {
			if !seen[f] {
				seen[f] = true
				files = append(files, f)
			}
		}
	}
	if len(files) > 0 {
		fmt.Fprintln(out, "\n## Relevant Files / Entities")
		for _, file := range files {
			fmt.Fprintln(out, "-", file)
		}
	}
	return nil
}
func boolCount(values ...bool) int {
	n := 0
	for _, v := range values {
		if v {
			n++
		}
	}
	return n
}
func truncateRunes(s string, limit int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= limit {
		return string(r)
	}
	return string(r[:limit]) + "…"
}
func cmdBrief(s *Store, out io.Writer) error {
	goal, state := readSummary(s, "GOAL.md"), readSummary(s, "STATE.md")
	fmt.Fprint(out, "# Ledger Brief\n\n")
	fmt.Fprintln(out, "Goal:", meaningful(section(goal, "Primary Objective")))
	fmt.Fprintln(out, "Milestone:", meaningful(section(goal, "Current Milestone")))
	for _, name := range []string{"Working", "Broken or Blocked", "Next Work"} {
		v := firstPoint(section(state, name))
		if name == "Next Work" {
			v = firstAction(section(state, name))
		}
		if v != "" {
			fmt.Fprintln(out, name+":", v)
		}
	}
	es, err := s.blockers()
	if err != nil {
		return err
	}
	n := 0
	for _, e := range es {
		if e.Type == "WARNING" && n < 3 {
			fmt.Fprintf(out, "Warning: %s %s\n", e.ID, e.Title)
			n++
		}
	}
	return nil
}
func cmdStatus(s *Store, args []string, out io.Writer) error {
	var short, verbose, js bool
	f, err := flags("status", args, func(f *flag.FlagSet) {
		f.BoolVar(&short, "short", false, "compact output")
		f.BoolVar(&verbose, "verbose", false, "include entries")
		f.BoolVar(&js, "json", false, "JSON output")
	})
	if err != nil {
		return err
	}
	if len(f.Args()) != 0 {
		return errors.New("status takes no positional arguments")
	}
	goal, state := readSummary(s, "GOAL.md"), readSummary(s, "STATE.md")
	es, err := s.search("", SearchOptions{Limit: 200})
	if err != nil {
		return err
	}
	counts := map[string]int{}
	countRows, err := s.DB.Query("SELECT type,COUNT(*) FROM entries WHERE status IN ('ACTIVE','UNVERIFIED') GROUP BY type")
	if err != nil {
		return err
	}
	for countRows.Next() {
		var typ string
		var n int
		if err = countRows.Scan(&typ, &n); err != nil {
			break
		}
		counts[typ] = n
	}
	countRows.Close()
	if err != nil {
		return err
	}
	if js {
		fmt.Fprintln(out, jsonOutput(map[string]any{"goal": goal, "state": state, "active_entry_counts": counts, "entries": es}))
		return nil
	}
	project := filepath.Base(filepath.Dir(s.Dir))
	fmt.Fprintf(out, "Project: %s\n\nGoal\n----\n%s\n\nCurrent Milestone\n-----------------\n%s\n", project, meaningful(section(goal, "Primary Objective")), meaningful(section(goal, "Current Milestone")))
	if short {
		total := 0
		for _, n := range counts {
			total += n
		}
		fmt.Fprintf(out, "\nCurrent memory: %d entries\n", total)
		return nil
	}
	fmt.Fprintln(out, "\nCurrent State\n-------------")
	fmt.Fprintln(out, strings.TrimSpace(state))
	if events, er := s.history("", 100); er == nil {
		n := 0
		for _, ev := range events {
			if ev.Entry == nil || ev.Entry.Type == "NOTE" || ev.Kind != "entry_created" && ev.Kind != "entry_updated" {
				continue
			}
			if n == 0 {
				fmt.Fprintln(out, "\nRecent Progress\n---------------")
			}
			fmt.Fprintf(out, "- %s %s\n", ev.Entry.ID, ev.Entry.Title)
			n++
			if n >= 5 {
				break
			}
		}
	}
	for _, group := range []struct{ heading, typ string }{{"Current Warnings", "WARNING"}, {"Open Tasks", "TASK"}, {"Open Questions", "HYPOTHESIS"}} {
		var selected []Entry
		for _, e := range es {
			if e.Type == group.typ {
				selected = append(selected, e)
			}
		}
		if len(selected) > 0 {
			fmt.Fprintln(out, "\n"+group.heading+"\n"+strings.Repeat("-", len(group.heading)))
			for _, e := range selected {
				fmt.Fprintf(out, "- %s %s\n", e.ID, e.Title)
			}
		}
	}
	if verbose {
		fmt.Fprintln(out, "\nActive Entries\n--------------")
		writeEntries(out, es, false)
	}
	return nil
}
