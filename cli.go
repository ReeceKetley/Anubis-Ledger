package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

const help = `Ledger — persistent project memory

Usage: ledger <command> [arguments] [options]

Commands:
  init                 Create .ledger in the current directory
  add TYPE BODY        Add a fact, decision, experiment, failure, hypothesis, warning, task, or note
  search QUERY         Search current knowledge (--all includes history)
  show ID              Show complete entry
  invalidate ID        Mark disproved (--because ID or --reason TEXT)
  supersede OLD NEW    Replace old knowledge with an active entry
  history QUERY        Show event history (query or ID)
  unresolved           Open hypotheses, tasks, uncertain facts
  context TASK         Concise task context
  brief                Compact session orientation
  status               Human project snapshot
  recent               Recent memory changes
  blockers             Active warnings, failures, and tasks
  handoff              Focused summary for the next agent
  since DATE           Changes grouped by kind since a date or duration
  changelog            Important changes (--since, --type)
  file PATH            Current memory about a repository file
  entity NAME          Current memory about a symbol or entity
  compact              Advisory duplicate and stale-memory report
  stale                Active entries due for review
  verify ID            Update an entry's last-verified date
  archive ID           Hide an old current entry without declaring it false
  backup               Save durable project memory in archive/
  info                 Ledger schema and storage summary
  goal|state|instructions  Print project Markdown
  doctor               Check consistency without changing data
  rebuild              Recreate SQLite from ledger.jsonl; back up old database

Run ledger <command> --help for command options.
`

type stringsFlag []string

func (s *stringsFlag) String() string     { return strings.Join(*s, ",") }
func (s *stringsFlag) Set(v string) error { *s = append(*s, v); return nil }
func flags(name string, args []string, configure func(*flag.FlagSet)) (*flag.FlagSet, error) {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(io.Discard)
	configure(f)
	var opts, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "--") || strings.HasPrefix(a, "-") {
			opts = append(opts, a)
			if !strings.Contains(a, "=") && a != "--help" && a != "-h" {
				if fl := f.Lookup(strings.TrimLeft(a, "-")); fl != nil {
					if _, ok := fl.Value.(interface{ IsBoolFlag() bool }); !ok && i+1 < len(args) {
						i++
						opts = append(opts, args[i])
					}
				}
			}
		} else {
			pos = append(pos, a)
		}
	}
	if err := f.Parse(append(opts, pos...)); err != nil {
		return nil, err
	}
	return f, nil
}
func cli(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(out, help)
		return nil
	}
	cmd := args[0]
	args = args[1:]
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprint(out, commandHelp(cmd))
		return nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if cmd == "init" {
		if len(args) > 0 {
			return errors.New("init takes no arguments")
		}
		if err = initProject(cwd); err != nil {
			return err
		}
		fmt.Fprintln(out, "Initialized .ledger")
		if _, err = os.Stat(filepath.Join(cwd, "AGENTS.md")); err == nil {
			fmt.Fprintln(out, "Consider adding to AGENTS.md:\n\nThis project uses Ledger for persistent project memory. Before significant work, read .ledger/AGENT-INSTRUCTIONS.md and run:\nledger context \"<current task>\"")
		}
		return nil
	}
	dir, err := discover(cwd)
	if err != nil {
		return err
	}
	if cmd == "doctor" {
		issues, err := doctor(dir)
		if err != nil {
			return err
		}
		if len(issues) == 0 {
			fmt.Fprintln(out, "Ledger healthy")
			return nil
		}
		for _, issue := range issues {
			fmt.Fprintln(out, "-", issue)
		}
		return errors.New("Ledger needs attention")
	}
	if cmd == "rebuild" {
		lock := flock.New(filepath.Join(dir, ".lock"))
		ok, err := lock.TryLock()
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("Ledger is busy")
		}
		defer lock.Unlock()
		if err = rebuild(dir); err != nil {
			return err
		}
		fmt.Fprintln(out, "Rebuilt ledger.db from ledger.jsonl; previous database saved in archive/")
		return nil
	}
	s, err := openStore(dir, true)
	if err != nil {
		return err
	}
	defer s.Close()
	switch cmd {
	case "goal", "state", "instructions":
		name := map[string]string{"goal": "GOAL.md", "state": "STATE.md", "instructions": "AGENT-INSTRUCTIONS.md"}[cmd]
		b, er := os.ReadFile(filepath.Join(dir, name))
		if er != nil {
			return er
		}
		fmt.Fprint(out, string(b))
	case "add":
		return cmdAdd(s, args, out)
	case "show":
		if len(args) != 1 {
			return errors.New("usage: ledger show ID")
		}
		e, er := s.get(strings.ToUpper(args[0]))
		if er != nil {
			return er
		}
		fmt.Fprint(out, renderEntry(e))
	case "invalidate":
		return cmdInvalidate(s, args, out)
	case "supersede":
		return cmdSupersede(s, args, out)
	case "search":
		return cmdSearch(s, args, out)
	case "history", "recent":
		return cmdHistory(s, cmd, args, out)
	case "unresolved":
		f, er := flags(cmd, args, func(f *flag.FlagSet) { f.Bool("json", false, "JSON output") })
		if er != nil {
			return er
		}
		es, er := s.unresolved()
		if er != nil {
			return er
		}
		writeEntries(out, es, f.Lookup("json").Value.String() == "true")
	case "blockers":
		es, er := s.blockers()
		if er != nil {
			return er
		}
		var filtered []Entry
		for _, e := range es {
			if e.Type == "WARNING" || e.Type == "FAILURE" || e.Type == "TASK" {
				filtered = append(filtered, e)
			}
		}
		writeEntries(out, filtered, false)
	case "handoff":
		return cmdHandoff(s, args, out)
	case "since", "changelog":
		return cmdChanges(s, cmd, args, out)
	case "file", "entity":
		return cmdReference(s, cmd, args, out)
	case "compact":
		return cmdCompact(s, args, out)
	case "stale":
		return cmdStale(s, args, out)
	case "verify":
		return cmdVerify(s, args, out)
	case "archive":
		return cmdArchive(s, args, out)
	case "backup":
		return cmdBackup(s, args, out)
	case "info":
		return cmdInfo(s, args, out)
	case "context":
		return cmdContext(s, args, out)
	case "brief":
		return cmdBrief(s, out)
	case "status":
		return cmdStatus(s, args, out)
	default:
		return fmt.Errorf("unknown command %q; run ledger --help", cmd)
	}
	return nil
}
func commandHelp(cmd string) string {
	m := map[string]string{
		"add":        "ledger add TYPE BODY [--title TEXT] [--tag TAG]... [--confidence 0..1] [--importance low|normal|high|critical] [--topic KEY] [--verified YYYY-MM-DD] [--evidence ID]... [--file PATH]... [--entity NAME]...\n",
		"search":     "ledger search QUERY [--all] [--type TYPE] [--status STATUS] [--tag TAG] [--limit N] [--json]\n",
		"invalidate": "ledger invalidate ID [--because ID] [--reason TEXT]\n",
		"context":    "ledger context TASK [--short|--normal|--deep] [--limit N] [--json]\n",
		"status":     "ledger status [--short] [--verbose] [--json]\n",
		"recent":     "ledger recent [--since RFC3339|YYYY-MM-DD|DURATION] [--limit N] [--json]\n",
		"handoff":    "ledger handoff [--since DATE|DURATION]\n",
		"since":      "ledger since DATE|DURATION [--type TYPE] [--limit N] [--json]\n",
		"changelog":  "ledger changelog [--since DATE|DURATION] [--type TYPE] [--limit N] [--json]\n",
		"file":       "ledger file PATH [--all] [--json]\n",
		"entity":     "ledger entity NAME [--all] [--json]\n",
		"stale":      "ledger stale [--days N] [--json]\n",
		"verify":     "ledger verify ID [--date YYYY-MM-DD]\n",
		"archive":    "ledger archive ID\n",
	}
	if x := m[cmd]; x != "" {
		return x
	}
	return help
}
func cmdAdd(s *Store, args []string, out io.Writer) error {
	var tags, evidence, files, entities stringsFlag
	var title, importanceLevel, topic, verified string
	var confidence float64 = -1
	f, err := flags("add", args, func(f *flag.FlagSet) {
		f.StringVar(&title, "title", "", "short title")
		f.Var(&tags, "tag", "tag (repeatable)")
		f.Float64Var(&confidence, "confidence", -1, "0..1")
		f.Var(&evidence, "evidence", "evidence entry ID (repeatable)")
		f.Var(&files, "file", "source file (repeatable)")
		f.Var(&entities, "entity", "symbol or entity (repeatable)")
		f.StringVar(&importanceLevel, "importance", "normal", "low, normal, high, or critical")
		f.StringVar(&topic, "topic", "", "stable topic key, useful for decisions")
		f.StringVar(&verified, "verified", "", "last verification date, YYYY-MM-DD")
	})
	if err != nil {
		return err
	}
	p := f.Args()
	if len(p) != 2 {
		return errors.New("usage: ledger add TYPE BODY [options]")
	}
	typ, err := normalizeType(p[0])
	if err != nil {
		return err
	}
	if strings.TrimSpace(p[1]) == "" {
		return errors.New("body cannot be empty")
	}
	if confidence > 1 || confidence < -1 {
		return errors.New("confidence must be between 0 and 1")
	}
	if confidence < 0 && confidence != -1 {
		return errors.New("confidence must be between 0 and 1")
	}
	for _, ref := range evidence {
		if idPattern.MatchString(ref) {
			if _, er := s.get(ref); er != nil {
				return fmt.Errorf("evidence %s does not exist", ref)
			}
		}
	}
	for i, file := range files {
		if filepath.IsAbs(file) || strings.HasPrefix(filepath.Clean(file), "..") {
			return fmt.Errorf("source file must be a relative path inside the project: %q", file)
		}
		files[i] = filepath.ToSlash(filepath.Clean(file))
	}
	var n int
	er := s.DB.QueryRow("SELECT COALESCE(MAX(CAST(SUBSTR(id,INSTR(id,'-')+1) AS INTEGER)),0)+1 FROM entries WHERE type=?", typ).Scan(&n)
	if er != nil {
		return er
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if title == "" {
		title = p[1]
		if len(title) > 100 {
			title = title[:100]
		}
	}
	e := Entry{ID: fmt.Sprintf("%s-%04d", prefixes[typ], n), Type: typ, Title: title, Body: p[1], Status: "ACTIVE", CreatedAt: now, UpdatedAt: now, Tags: tags, Evidence: evidence, Files: files, Entities: entities, Importance: strings.ToLower(importanceLevel), Topic: strings.TrimSpace(topic), LastVerified: verified}
	if confidence >= 0 {
		e.Confidence = &confidence
	}
	if err = validEntry(e); err != nil {
		return err
	}
	if err = s.record(Event{Kind: "entry_created", Entry: &e}); err != nil {
		return err
	}
	fmt.Fprintln(out, e.ID)
	if typ == "FACT" || typ == "DECISION" {
		suggestRelated(s, e, out)
	}
	return nil
}
func cmdInvalidate(s *Store, args []string, out io.Writer) error {
	var because, reason string
	f, err := flags("invalidate", args, func(f *flag.FlagSet) {
		f.StringVar(&because, "because", "", "evidence ID")
		f.StringVar(&reason, "reason", "", "text reason")
	})
	if err != nil {
		return err
	}
	if len(f.Args()) != 1 {
		return errors.New("usage: ledger invalidate ID --because ID|--reason TEXT")
	}
	if because == "" && reason == "" {
		return errors.New("give --because ID or --reason TEXT")
	}
	id := strings.ToUpper(f.Args()[0])
	e, err := s.get(id)
	if err != nil {
		return err
	}
	if e.Status == "INVALIDATED" {
		return errors.New("entry already invalidated")
	}
	if e.Status == "SUPERSEDED" {
		return errors.New("superseded entry cannot be invalidated")
	}
	if because != "" {
		because = strings.ToUpper(because)
		if because == id {
			return errors.New("entry cannot invalidate itself")
		}
		if _, err = s.get(because); err != nil {
			return fmt.Errorf("evidence %s does not exist", because)
		}
	}
	e.Status = "INVALIDATED"
	e.InvalidatedBy = because
	e.InvalidationReason = reason
	e.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	ev := Event{Kind: "entry_invalidated", Entry: &e, Detail: reason}
	if because != "" {
		r := Relation{From: because, To: id, Type: "INVALIDATES"}
		ev.Relation = &r
	}
	if err = s.record(ev); err != nil {
		return err
	}
	fmt.Fprintln(out, id, "INVALIDATED")
	return nil
}
func cmdSupersede(s *Store, args []string, out io.Writer) error {
	if len(args) != 2 {
		return errors.New("usage: ledger supersede OLD_ID NEW_ID")
	}
	oldID, newID := strings.ToUpper(args[0]), strings.ToUpper(args[1])
	if oldID == newID {
		return errors.New("entry cannot supersede itself")
	}
	old, err := s.get(oldID)
	if err != nil {
		return err
	}
	replacement, err := s.get(newID)
	if err != nil {
		return err
	}
	if old.Status == "SUPERSEDED" {
		return errors.New("entry already superseded")
	}
	if replacement.Status != "ACTIVE" {
		return errors.New("replacement must be ACTIVE")
	}
	old.Status = "SUPERSEDED"
	old.SupersededBy = newID
	old.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	r := Relation{From: newID, To: oldID, Type: "SUPERSEDES"}
	if err = s.record(Event{Kind: "entry_superseded", Entry: &old, Relation: &r}); err != nil {
		return err
	}
	fmt.Fprintln(out, oldID, "SUPERSEDED by", newID)
	return nil
}
func cmdSearch(s *Store, args []string, out io.Writer) error {
	var o SearchOptions
	var js bool
	f, err := flags("search", args, func(f *flag.FlagSet) {
		f.BoolVar(&o.All, "all", false, "include historical entries")
		f.StringVar(&o.Type, "type", "", "entry type")
		f.StringVar(&o.Status, "status", "", "entry status")
		f.StringVar(&o.Tag, "tag", "", "tag")
		f.IntVar(&o.Limit, "limit", 20, "maximum results")
		f.BoolVar(&js, "json", false, "JSON output")
	})
	if err != nil {
		return err
	}
	if len(f.Args()) != 1 {
		return errors.New("usage: ledger search QUERY [options]")
	}
	es, err := s.search(f.Args()[0], o)
	if err != nil {
		return err
	}
	writeEntries(out, es, js)
	return nil
}
func cmdHistory(s *Store, cmd string, args []string, out io.Writer) error {
	var since string
	var limit int
	var js bool
	f, err := flags(cmd, args, func(f *flag.FlagSet) {
		f.StringVar(&since, "since", "", "date or duration")
		f.IntVar(&limit, "limit", 30, "maximum events")
		f.BoolVar(&js, "json", false, "JSON output")
	})
	if err != nil {
		return err
	}
	if limit <= 0 {
		return errors.New("limit must be positive")
	}
	q := ""
	if cmd == "history" {
		if len(f.Args()) != 1 {
			return errors.New("usage: ledger history QUERY")
		}
		q = f.Args()[0]
	}
	historyLimit := limit
	if since != "" {
		historyLimit = int(^uint(0) >> 1)
	}
	events, err := s.history(q, historyLimit)
	if err != nil {
		return err
	}
	if since != "" {
		t, er := parseSince(since)
		if er != nil {
			return er
		}
		var filtered []Event
		for _, ev := range events {
			when, _ := time.Parse(time.RFC3339Nano, ev.Time)
			if !when.Before(t) {
				filtered = append(filtered, ev)
			}
		}
		events = filtered
	}
	if len(events) > limit {
		events = events[:limit]
	}
	if js {
		fmt.Fprintln(out, jsonOutput(events))
		return nil
	}
	for _, ev := range events {
		subject := ""
		if ev.Entry != nil {
			subject = ev.Entry.ID + " " + ev.Entry.Title + " [" + ev.Entry.Status + "]"
		}
		if ev.Relation != nil {
			subject = ev.Relation.From + " -> " + ev.Relation.To + " (" + ev.Relation.Type + ")"
		}
		fmt.Fprintf(out, "%s  %s  %s\n", ev.Time, ev.Kind, subject)
	}
	return nil
}
func parseSince(s string) (time.Time, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(-d), nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, errors.New("--since must be RFC3339, YYYY-MM-DD, or duration such as 72h")
}
func writeEntries(out io.Writer, es []Entry, js bool) {
	if js {
		fmt.Fprintln(out, jsonOutput(es))
		return
	}
	if len(es) == 0 {
		fmt.Fprintln(out, "No entries found.")
		return
	}
	for _, e := range es {
		fmt.Fprintf(out, "%s  [%s] %s — %s\n", e.ID, e.Status, e.Type, e.Title)
	}
}
func renderEntry(e Entry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s  [%s]\n%s\n\n%s\n", e.ID, e.Type, e.Status, e.Title, e.Body)
	if e.Confidence != nil {
		fmt.Fprintf(&b, "Confidence: %.2f\n", *e.Confidence)
	}
	fmt.Fprintf(&b, "Importance: %s\n", importance(e))
	if e.Topic != "" {
		fmt.Fprintf(&b, "Topic: %s\n", e.Topic)
	}
	if e.LastVerified != "" {
		fmt.Fprintf(&b, "Last verified: %s\n", e.LastVerified)
	}
	for _, x := range []struct {
		name string
		vals []string
	}{{"Tags", e.Tags}, {"Evidence", e.Evidence}, {"Files", e.Files}, {"Entities", e.Entities}} {
		if len(x.vals) > 0 {
			fmt.Fprintf(&b, "%s: %s\n", x.name, strings.Join(x.vals, ", "))
		}
	}
	if e.SupersededBy != "" {
		fmt.Fprintf(&b, "Superseded by: %s\n", e.SupersededBy)
	}
	if e.InvalidatedBy != "" {
		fmt.Fprintf(&b, "Invalidated by: %s\n", e.InvalidatedBy)
	}
	if e.InvalidationReason != "" {
		fmt.Fprintf(&b, "Invalidation reason: %s\n", e.InvalidationReason)
	}
	for _, r := range e.Relations {
		fmt.Fprintf(&b, "Relationship: %s %s %s\n", r.From, r.Type, r.To)
	}
	fmt.Fprintf(&b, "Created: %s\nUpdated: %s\n", e.CreatedAt, e.UpdatedAt)
	return b.String()
}
