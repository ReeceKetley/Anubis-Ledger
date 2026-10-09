package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func suggestRelated(s *Store, e Entry, out io.Writer) {
	query := e.Title
	if query == "" {
		query = e.Body
	}
	results, err := s.search(query, SearchOptions{Type: e.Type, Status: "ACTIVE", Limit: 12})
	if err != nil {
		return
	}
	var suggestions []Entry
	for _, v := range results {
		if v.ID == e.ID {
			continue
		}
		if (e.Topic != "" && e.Topic == v.Topic) || titleOverlap(e.Title, v.Title) >= 0.5 {
			suggestions = append(suggestions, v)
		}
		if len(suggestions) == 3 {
			break
		}
	}
	if len(suggestions) == 0 {
		return
	}
	fmt.Fprintln(out, "Possible related ACTIVE entries:")
	for _, v := range suggestions {
		fmt.Fprintf(out, "  %s  %s\n", v.ID, v.Title)
	}
	fmt.Fprintf(out, "Review them before deciding whether to run ledger supersede <OLD_ID> %s.\n", e.ID)
}

func cmdReference(s *Store, cmd string, args []string, out io.Writer) error {
	var all, js bool
	f, err := flags(cmd, args, func(f *flag.FlagSet) {
		f.BoolVar(&all, "all", false, "include historical entries")
		f.BoolVar(&js, "json", false, "JSON output")
	})
	if err != nil {
		return err
	}
	if len(f.Args()) != 1 {
		return fmt.Errorf("usage: ledger %s NAME [--all] [--json]", cmd)
	}
	name := f.Args()[0]
	table, col := "entry_entities", "name"
	if cmd == "file" {
		name = filepath.ToSlash(filepath.Clean(name))
		table, col = "entry_files", "path"
	}
	q := "SELECT e.id FROM entries e JOIN " + table + " x ON x.entry_id=e.id WHERE x." + col + "=?"
	if !all {
		q += " AND e.status IN ('ACTIVE','UNVERIFIED')"
	}
	q += " ORDER BY CASE e.importance WHEN 'critical' THEN 0 WHEN 'high' THEN 1 ELSE 2 END,e.updated_at DESC LIMIT 100"
	rows, err := s.DB.Query(q, name)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err != nil {
		return err
	}
	var es []Entry
	for _, id := range ids {
		e, er := s.get(id)
		if er != nil {
			return er
		}
		es = append(es, e)
	}
	writeEntries(out, es, js)
	return nil
}

var daysAgo = regexp.MustCompile(`^(\d+)\s+days?\s+ago$`)

func flexibleSince(raw string) (time.Time, error) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if m := daysAgo.FindStringSubmatch(raw); m != nil {
		n, _ := strconv.Atoi(m[1])
		return time.Now().AddDate(0, 0, -n), nil
	}
	if strings.HasSuffix(raw, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(raw, "d"))
		if err == nil && n >= 0 {
			return time.Now().AddDate(0, 0, -n), nil
		}
	}
	return parseSince(raw)
}
func eventLabel(ev Event) string {
	if ev.Entry != nil {
		return ev.Entry.ID + " " + ev.Entry.Title
	}
	if ev.Relation != nil {
		return ev.Relation.From + " -> " + ev.Relation.To
	}
	return ev.Detail
}
func cmdChanges(s *Store, cmd string, args []string, out io.Writer) error {
	var since, typ string
	var limit int
	var js bool
	f, err := flags(cmd, args, func(f *flag.FlagSet) {
		f.StringVar(&since, "since", "", "date or duration")
		f.StringVar(&typ, "type", "", "entry type")
		f.IntVar(&limit, "limit", 50, "maximum events")
		f.BoolVar(&js, "json", false, "JSON output")
	})
	if err != nil {
		return err
	}
	if cmd == "since" {
		if len(f.Args()) != 1 {
			return errors.New("usage: ledger since DATE|DURATION")
		}
		since = f.Args()[0]
	} else if len(f.Args()) != 0 {
		return errors.New("changelog takes no positional arguments")
	}
	if limit < 1 {
		return errors.New("limit must be positive")
	}
	if typ != "" {
		if typ, err = normalizeType(typ); err != nil {
			return err
		}
	}
	cutoff := time.Time{}
	if since != "" {
		cutoff, err = flexibleSince(since)
		if err != nil {
			return err
		}
	}
	events, err := readEvents(filepath.Join(s.Dir, "ledger.jsonl"))
	if err != nil {
		return err
	}
	var selected []Event
	for i := len(events) - 1; i >= 0 && len(selected) < limit; i-- {
		ev := events[i]
		when, er := time.Parse(time.RFC3339Nano, ev.Time)
		if er != nil || when.Before(cutoff) {
			continue
		}
		if ev.Entry == nil {
			continue
		}
		if typ != "" && ev.Entry.Type != typ {
			continue
		}
		if cmd == "changelog" && ev.Entry.Type == "NOTE" && importance(*ev.Entry) != "high" && importance(*ev.Entry) != "critical" {
			continue
		}
		selected = append(selected, ev)
	}
	if js {
		fmt.Fprintln(out, jsonOutput(selected))
		return nil
	}
	if len(selected) == 0 {
		fmt.Fprintln(out, "No changes found.")
		return nil
	}
	groups := []struct {
		title string
		kinds []string
	}{{"New knowledge", []string{"entry_created"}}, {"Changed knowledge", []string{"entry_updated", "entry_verified", "entry_superseded", "entry_invalidated", "entry_archived"}}, {"Other changes", []string{"checkpoint_created"}}}
	for _, g := range groups {
		printed := false
		for _, ev := range selected {
			match := false
			for _, k := range g.kinds {
				if ev.Kind == k {
					match = true
				}
			}
			if !match {
				continue
			}
			if !printed {
				fmt.Fprintln(out, g.title)
				printed = true
			}
			fmt.Fprintf(out, "- %s %s (%s)\n", ev.Time[:10], eventLabel(ev), ev.Kind)
		}
		if printed {
			fmt.Fprintln(out)
		}
	}
	return nil
}

func cmdHandoff(s *Store, args []string, out io.Writer) error {
	var since string
	f, err := flags("handoff", args, func(f *flag.FlagSet) { f.StringVar(&since, "since", "7d", "recent change window") })
	if err != nil {
		return err
	}
	if len(f.Args()) != 0 {
		return errors.New("handoff takes no positional arguments")
	}
	cutoff, err := flexibleSince(since)
	if err != nil {
		return err
	}
	state := readSummary(s, "STATE.md")
	fmt.Fprint(out, "# Session Handoff\n\n")
	for _, part := range []struct{ heading, source string }{{"Current milestone", firstPoint(section(readSummary(s, "GOAL.md"), "Current Milestone"))}, {"What works", firstPoint(section(state, "Working"))}, {"What is blocked", firstPoint(section(state, "Broken or Blocked"))}, {"Suggested next action", firstAction(section(state, "Next Work"))}} {
		if part.source != "" {
			fmt.Fprintf(out, "## %s\n\n%s\n\n", part.heading, part.source)
		}
	}
	events, err := readEvents(filepath.Join(s.Dir, "ledger.jsonl"))
	if err != nil {
		return err
	}
	n := 0
	seen := map[string]bool{}
	for i := len(events) - 1; i >= 0 && n < 8; i-- {
		ev := events[i]
		when, er := time.Parse(time.RFC3339Nano, ev.Time)
		if er != nil || when.Before(cutoff) || ev.Entry == nil || ev.Entry.Type == "NOTE" && importance(*ev.Entry) != "high" && importance(*ev.Entry) != "critical" {
			continue
		}
		if seen[ev.Entry.ID] {
			continue
		}
		seen[ev.Entry.ID] = true
		if n == 0 {
			fmt.Fprint(out, "## Recent memory changes\n\n")
		}
		fmt.Fprintf(out, "- %s: %s\n", ev.Kind, eventLabel(ev))
		n++
	}
	if n > 0 {
		fmt.Fprintln(out)
	}
	warnings, err := s.search("", SearchOptions{Type: "WARNING", Status: "ACTIVE", Limit: 5})
	if err != nil {
		return err
	}
	if len(warnings) > 0 {
		fmt.Fprint(out, "## Do not miss\n\n")
		for _, e := range warnings {
			fmt.Fprintf(out, "- %s %s\n", e.ID, e.Title)
		}
	}
	return nil
}

func cmdCompact(s *Store, args []string, out io.Writer) error {
	if len(args) > 0 {
		return errors.New("compact takes no arguments")
	}
	es, err := s.search("", SearchOptions{Limit: 200})
	if err != nil {
		return err
	}
	var pairs []string
	for i, a := range es {
		for _, b := range es[i+1:] {
			if a.Type == b.Type && ((a.Topic != "" && a.Topic == b.Topic) || titleOverlap(a.Title, b.Title) >= 0.8) {
				pairs = append(pairs, a.ID+" / "+b.ID)
			}
		}
	}
	fmt.Fprintln(out, "Advisory memory review (no changes made)")
	if len(pairs) == 0 {
		fmt.Fprintln(out, "No obvious duplicate current entries in the first 200.")
	} else {
		fmt.Fprintln(out, "Potential duplicates:")
		for _, p := range pairs {
			fmt.Fprintln(out, "-", p)
		}
	}
	return nil
}
func cmdStale(s *Store, args []string, out io.Writer) error {
	var days int
	var js bool
	f, err := flags("stale", args, func(f *flag.FlagSet) {
		f.IntVar(&days, "days", 90, "review age in days")
		f.BoolVar(&js, "json", false, "JSON output")
	})
	if err != nil {
		return err
	}
	if len(f.Args()) != 0 || days < 1 {
		return errors.New("usage: ledger stale [--days N] [--json]")
	}
	cutoff := time.Now().AddDate(0, 0, -days).Format("2006-01-02")
	rows, err := s.DB.Query("SELECT id FROM entries WHERE status IN ('ACTIVE','UNVERIFIED') AND last_verified!='' AND last_verified<? ORDER BY last_verified LIMIT 200", cutoff)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err != nil {
		return err
	}
	var es []Entry
	for _, id := range ids {
		e, er := s.get(id)
		if er != nil {
			return er
		}
		es = append(es, e)
	}
	writeEntries(out, es, js)
	return nil
}
func cmdVerify(s *Store, args []string, out io.Writer) error {
	var date string
	f, err := flags("verify", args, func(f *flag.FlagSet) {
		f.StringVar(&date, "date", time.Now().UTC().Format("2006-01-02"), "verification date")
	})
	if err != nil {
		return err
	}
	if len(f.Args()) != 1 {
		return errors.New("usage: ledger verify ID [--date YYYY-MM-DD]")
	}
	e, err := s.get(strings.ToUpper(f.Args()[0]))
	if err != nil {
		return err
	}
	if e.Status != "ACTIVE" && e.Status != "UNVERIFIED" {
		return errors.New("only current entries can be reverified")
	}
	e.LastVerified = date
	e.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err = validEntry(e); err != nil {
		return err
	}
	if err = s.record(Event{Kind: "entry_verified", Entry: &e}); err != nil {
		return err
	}
	fmt.Fprintln(out, e.ID, "verified", date)
	return nil
}
func cmdArchive(s *Store, args []string, out io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: ledger archive ID")
	}
	e, err := s.get(strings.ToUpper(args[0]))
	if err != nil {
		return err
	}
	if e.Status != "ACTIVE" && e.Status != "UNVERIFIED" {
		return errors.New("only current entries can be archived")
	}
	e.Status = "DEPRECATED"
	e.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err = s.record(Event{Kind: "entry_archived", Entry: &e}); err != nil {
		return err
	}
	fmt.Fprintln(out, e.ID, "archived")
	return nil
}

func copyDurable(src, dst string) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, err = io.Copy(io.MultiWriter(out, hash), in)
	if err == nil {
		err = out.Sync()
	}
	closeErr := out.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
func cmdBackup(s *Store, args []string, out io.Writer) error {
	if len(args) > 0 {
		return errors.New("backup takes no arguments")
	}
	dir := filepath.Join(s.Dir, "archive", "backup-"+time.Now().UTC().Format("20060102T150405.000000000"))
	if err := os.Mkdir(dir, 0700); err != nil {
		return err
	}
	manifest := map[string]string{}
	for _, name := range []string{"ledger.jsonl", "GOAL.md", "STATE.md", "DECISIONS.md", "AGENT-INSTRUCTIONS.md"} {
		hash, err := copyDurable(filepath.Join(s.Dir, name), filepath.Join(dir, name))
		if err != nil {
			return err
		}
		manifest[name] = hash
	}
	entityRoot := filepath.Join(s.Dir, "entities")
	if err := filepath.WalkDir(entityRoot, func(path string, item os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if item.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("entity symlink cannot be backed up safely: %s", path)
		}
		rel, err := filepath.Rel(entityRoot, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dir, "entities", rel)
		if item.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		if !item.Type().IsRegular() {
			return fmt.Errorf("unsupported entity file type: %s", path)
		}
		hash, err := copyDurable(path, target)
		if err != nil {
			return err
		}
		manifest[filepath.ToSlash(filepath.Join("entities", rel))] = hash
		return nil
	}); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "SHA256.json"), append(b, '\n'), 0600); err != nil {
		return err
	}
	fmt.Fprintln(out, dir)
	return nil
}
func cmdInfo(s *Store, args []string, out io.Writer) error {
	if len(args) > 0 {
		return errors.New("info takes no arguments")
	}
	var entries, active, events int
	s.DB.QueryRow("SELECT COUNT(*) FROM entries").Scan(&entries)
	s.DB.QueryRow("SELECT COUNT(*) FROM entries WHERE status IN ('ACTIVE','UNVERIFIED')").Scan(&active)
	s.DB.QueryRow("SELECT COUNT(*) FROM events").Scan(&events)
	st, err := os.Stat(filepath.Join(s.Dir, "ledger.db"))
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Schema version: %d\nEntries: %d\nCurrent entries: %d\nHistorical entries: %d\nEvents: %d\nDatabase size: %d bytes\n", schemaVersion, entries, active, entries-active, events, st.Size())
	return nil
}
