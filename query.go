package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var terms = regexp.MustCompile(`[\pL\pN_]+`)

func ftsQuery(s string) string {
	words := terms.FindAllString(s, -1)
	if len(words) > 16 {
		words = words[:16]
	}
	for i, w := range words {
		words[i] = `"` + strings.ReplaceAll(w, `"`, `""`) + `"*`
	}
	return strings.Join(words, " OR ")
}

type SearchOptions struct {
	All               bool
	Type, Status, Tag string
	Limit             int
}

func (s *Store) search(q string, o SearchOptions) ([]Entry, error) {
	if o.Limit <= 0 {
		o.Limit = 20
	}
	if o.Limit > 200 {
		o.Limit = 200
	}
	var args []any
	var where []string
	join := ""
	rank := ""
	if strings.TrimSpace(q) != "" {
		fq := ftsQuery(q)
		if fq == "" {
			return nil, nil
		}
		join = "JOIN entries_fts f ON f.id=e.id"
		where = append(where, "entries_fts MATCH ?")
		args = append(args, fq)
		rank = "bm25(entries_fts)"
	}
	if o.Type != "" {
		t, err := normalizeType(o.Type)
		if err != nil {
			return nil, err
		}
		where = append(where, "e.type=?")
		args = append(args, t)
	}
	if o.Status != "" {
		status := strings.ToUpper(o.Status)
		if !statuses[status] {
			return nil, fmt.Errorf("unknown status %q", o.Status)
		}
		where = append(where, "e.status=?")
		args = append(args, status)
	} else if !o.All {
		where = append(where, "e.status IN ('ACTIVE','UNVERIFIED')")
	}
	if o.Tag != "" {
		where = append(where, "EXISTS(SELECT 1 FROM tags t WHERE t.entry_id=e.id AND t.tag=?)")
		args = append(args, o.Tag)
	}
	sqlq := "SELECT e.id FROM entries e " + join
	if len(where) > 0 {
		sqlq += " WHERE " + strings.Join(where, " AND ")
	}
	// A constant integer in ORDER BY is a column index. An empty search has no
	// bm25 score, so leave that term out instead of substituting 0.
	sqlq += " ORDER BY CASE e.status WHEN 'ACTIVE' THEN 0 WHEN 'UNVERIFIED' THEN 1 ELSE 2 END, CASE e.importance WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 ELSE 3 END"
	if rank != "" {
		sqlq += ", " + rank
	}
	sqlq += ", COALESCE(e.confidence,0) DESC,e.updated_at DESC LIMIT ?"
	args = append(args, o.Limit)
	rows, err := s.DB.Query(sqlq, args...)
	if err != nil {
		return nil, err
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
		return nil, err
	}
	var out []Entry
	for _, id := range ids {
		e, er := s.get(id)
		if er != nil {
			return nil, er
		}
		out = append(out, e)
	}
	return out, nil
}
func (s *Store) history(q string, limit int) ([]Event, error) {
	events, err := readEvents(s.Dir + "/ledger.jsonl")
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	var out []Event
	for i := len(events) - 1; i >= 0 && len(out) < limit; i-- {
		ev := events[i]
		if q == "" || strings.Contains(strings.ToLower(eventText(ev)), strings.ToLower(q)) {
			out = append(out, ev)
		}
	}
	return out, nil
}
func eventText(ev Event) string {
	v := ev.Kind + " " + ev.Detail
	if ev.Entry != nil {
		v += " " + ev.Entry.ID + " " + ev.Entry.Title + " " + ev.Entry.Body + " " + strings.Join(ev.Entry.Tags, " ")
	}
	if ev.Relation != nil {
		v += " " + ev.Relation.From + " " + ev.Relation.To
	}
	return v
}
func (s *Store) unresolved() ([]Entry, error) {
	rows, err := s.DB.Query(`SELECT id FROM entries WHERE status='UNVERIFIED' OR (status='ACTIVE' AND (type IN ('HYPOTHESIS','TASK') OR (type='FACT' AND confidence<0.7))) ORDER BY updated_at DESC LIMIT 200`)
	if err != nil {
		return nil, err
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
		return nil, err
	}
	var out []Entry
	for _, id := range ids {
		e, er := s.get(id)
		if er != nil {
			return nil, er
		}
		out = append(out, e)
	}
	return out, nil
}
func (s *Store) blockers() ([]Entry, error) {
	rows, err := s.DB.Query("SELECT id FROM entries WHERE status='ACTIVE' AND type IN ('WARNING','FAILURE','TASK') ORDER BY CASE importance WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'normal' THEN 2 ELSE 3 END,updated_at DESC LIMIT 100")
	if err != nil {
		return nil, err
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
		return nil, err
	}
	var out []Entry
	for _, id := range ids {
		e, er := s.get(id)
		if er != nil {
			return nil, er
		}
		out = append(out, e)
	}
	return out, nil
}
func (s *Store) related(q string, limit int) ([]Entry, error) {
	candidates := limit * 4
	if candidates > 200 {
		candidates = 200
	}
	base, err := s.search(q, SearchOptions{Limit: candidates})
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []Entry
	for _, e := range base {
		if len(out) >= limit {
			break
		}
		if redundant(e, out) {
			continue
		}
		seen[e.ID] = true
		out = append(out, e)
	}
	for _, e := range base {
		for _, r := range e.Relations {
			other := r.To
			if other == e.ID {
				other = r.From
			}
			if seen[other] || len(out) >= limit {
				continue
			}
			v, er := s.get(other)
			if er == nil && (v.Status == "ACTIVE" || v.Status == "UNVERIFIED") && !redundant(v, out) {
				seen[other] = true
				out = append(out, v)
			}
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func titleTerms(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range terms.FindAllString(strings.ToLower(s), -1) {
		if len(w) > 2 {
			out[w] = true
		}
	}
	return out
}
func titleOverlap(a, b string) float64 {
	x, y := titleTerms(a), titleTerms(b)
	if len(x) == 0 || len(y) == 0 {
		return 0
	}
	common := 0
	for w := range x {
		if y[w] {
			common++
		}
	}
	denom := len(x)
	if len(y) < denom {
		denom = len(y)
	}
	return float64(common) / float64(denom)
}
func redundant(e Entry, selected []Entry) bool {
	for _, v := range selected {
		if e.Type == v.Type && ((e.Topic != "" && e.Topic == v.Topic) || titleOverlap(e.Title, v.Title) >= 0.8) {
			return true
		}
	}
	return false
}
func doctor(dir string) ([]string, error) {
	var issues []string
	for _, name := range []string{"ledger.db", "ledger.jsonl", "GOAL.md", "STATE.md", "DECISIONS.md", "AGENT-INSTRUCTIONS.md", "entities", "archive"} {
		if _, err := osStat(dir, name); err != nil {
			issues = append(issues, "missing "+name)
		}
	}
	events, err := readEvents(dir + "/ledger.jsonl")
	if err != nil {
		issues = append(issues, err.Error())
	}
	if _, err := os.Stat(filepath.Join(dir, "ledger.db")); err != nil {
		return issues, nil
	}
	db, er := sql.Open("sqlite", dir+"/ledger.db")
	if er != nil {
		return append(issues, er.Error()), nil
	}
	defer db.Close()
	var integrity string
	if er = db.QueryRow("PRAGMA integrity_check").Scan(&integrity); er != nil || integrity != "ok" {
		issues = append(issues, "SQLite integrity failed: "+integrity)
	}
	var n int64
	if er = db.QueryRow("SELECT COUNT(*) FROM entries").Scan(&n); er != nil {
		issues = append(issues, "database schema unavailable; rebuild from JSONL")
		return issues, nil
	}
	var version string
	db.QueryRow("SELECT value FROM meta WHERE key='schema_version'").Scan(&version)
	if version != fmt.Sprint(schemaVersion) {
		issues = append(issues, "unsupported schema version "+version)
	}
	var last int64
	db.QueryRow("SELECT COALESCE(MAX(seq),0) FROM events").Scan(&last)
	if last != int64(len(events)) {
		issues = append(issues, "event log and database differ; open Ledger to replay pending events or run rebuild")
	}
	for _, q := range []struct{ sql, msg string }{{"SELECT COUNT(*) FROM entries WHERE status='ACTIVE' AND (superseded_by IS NOT NULL AND superseded_by!='' OR invalidated_by IS NOT NULL AND invalidated_by!='' OR invalidation_reason!='')", "ACTIVE entries carry obsolete metadata"}, {"SELECT COUNT(*) FROM entries e WHERE superseded_by!='' AND NOT EXISTS(SELECT 1 FROM entries x WHERE x.id=e.superseded_by)", "missing superseding target"}, {"SELECT COUNT(*) FROM relations r WHERE NOT EXISTS(SELECT 1 FROM entries e WHERE e.id=r.from_id) OR NOT EXISTS(SELECT 1 FROM entries e WHERE e.id=r.to_id) OR r.from_id=r.to_id", "invalid relationship"}, {"SELECT ABS((SELECT COUNT(*) FROM entries)-(SELECT COUNT(*) FROM entries_fts))", "broken FTS index size"}} {
		var count int
		if er = db.QueryRow(q.sql).Scan(&count); er == nil && count > 0 {
			issues = append(issues, fmt.Sprintf("%s (%d)", q.msg, count))
		}
	}
	for _, check := range []struct{ query, msg string }{{"SELECT COUNT(*) FROM entries WHERE status='SUPERSEDED' AND (superseded_by IS NULL OR superseded_by='')", "superseded entry has no replacement"}, {"SELECT COUNT(*) FROM entries WHERE status='INVALIDATED' AND (invalidated_by IS NULL OR invalidated_by='') AND invalidation_reason=''", "invalidated entry has no reason"}, {"SELECT COUNT(*) FROM relations WHERE type NOT IN ('SUPERSEDES','INVALIDATES','EVIDENCE_FOR','RELATES_TO','DEPENDS_ON','BLOCKS')", "unknown relationship type"}} {
		var n int
		if er = db.QueryRow(check.query).Scan(&n); er == nil && n > 0 {
			issues = append(issues, fmt.Sprintf("%s (%d)", check.msg, n))
		}
	}
	rows, err := db.Query("SELECT topic,GROUP_CONCAT(id,', ') FROM entries WHERE type='DECISION' AND status='ACTIVE' AND topic!='' GROUP BY topic HAVING COUNT(*)>1")
	if err == nil {
		for rows.Next() {
			var topic, ids string
			if rows.Scan(&topic, &ids) == nil {
				issues = append(issues, "multiple ACTIVE decisions for topic "+topic+": "+ids)
			}
		}
		rows.Close()
	}
	rows, err = db.Query("SELECT ref FROM evidence")
	if err == nil {
		for rows.Next() {
			var ref string
			if rows.Scan(&ref) == nil && idPattern.MatchString(ref) {
				var n int
				if db.QueryRow("SELECT COUNT(*) FROM entries WHERE id=?", ref).Scan(&n) == nil && n == 0 {
					issues = append(issues, "dangling evidence reference "+ref)
				}
			}
		}
		rows.Close()
	}
	rows, err = db.Query("SELECT e.id,e.status,r.type FROM relations r JOIN entries e ON e.id=r.to_id WHERE r.type IN ('SUPERSEDES','INVALIDATES')")
	if err == nil {
		for rows.Next() {
			var id, status, kind string
			if rows.Scan(&id, &status, &kind) == nil && ((kind == "SUPERSEDES" && status != "SUPERSEDED") || (kind == "INVALIDATES" && status != "INVALIDATED")) {
				issues = append(issues, "relationship/status conflict for "+id)
			}
		}
		rows.Close()
	}
	if tx, txErr := db.Begin(); txErr == nil {
		_, er = tx.Exec("INSERT INTO entries_fts(entries_fts) VALUES('integrity-check')")
		tx.Rollback()
		if er != nil {
			issues = append(issues, "FTS integrity check failed: "+er.Error())
		}
	} else {
		issues = append(issues, "FTS integrity check unavailable: "+txErr.Error())
	}
	seen := map[string]bool{}
	for _, ev := range events {
		if ev.Kind == "entry_created" && ev.Entry != nil {
			if seen[ev.Entry.ID] {
				issues = append(issues, "duplicate ID "+ev.Entry.ID)
			}
			seen[ev.Entry.ID] = true
		}
	}
	sort.Strings(issues)
	return issues, nil
}
func osStat(dir, name string) (bool, error) {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil, err
}
func jsonOutput(v any) string { b, _ := json.MarshalIndent(v, "", "  "); return string(b) }
