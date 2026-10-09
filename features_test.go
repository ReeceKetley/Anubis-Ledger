package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFeaturesAndRebuild(t *testing.T) {
	root := project(t)
	if err := os.WriteFile(filepath.Join(root, ".ledger", "entities", "renderer.md"), []byte("# Renderer\n"), 0600); err != nil {
		t.Fatal(err)
	}
	run(t, "add", "warning", "Do not regenerate protocol IDs", "--importance", "critical", "--topic", "protocol-ids", "--file", "src/protocol.go", "--entity", "BuildProtocolIDs", "--verified", "2026-01-01")
	run(t, "add", "decision", "Use Vulkan renderer", "--topic", "renderer-backend")
	second := run(t, "add", "decision", "Use Vulkan for rendering", "--topic", "renderer-backend")
	if !strings.Contains(second, "Possible related ACTIVE entries") || !strings.Contains(second, "DEC-0001") {
		t.Fatalf("missing supersession assistance: %s", second)
	}
	if !strings.Contains(run(t, "file", "src/protocol.go"), "WARN-0001") {
		t.Fatal("file lookup")
	}
	if !strings.Contains(run(t, "entity", "BuildProtocolIDs"), "WARN-0001") {
		t.Fatal("entity lookup")
	}
	if !strings.Contains(run(t, "stale", "--days", "30"), "WARN-0001") {
		t.Fatal("stale lookup")
	}
	run(t, "verify", "WARN-0001")
	if strings.Contains(run(t, "stale", "--days", "30"), "WARN-0001") {
		t.Fatal("verification did not clear stale result")
	}
	if !strings.Contains(run(t, "context", "protocol IDs", "--short"), "WARN-0001") {
		t.Fatal("short context missed critical warning")
	}
	if !strings.Contains(run(t, "handoff"), "Session Handoff") {
		t.Fatal("handoff")
	}
	if !strings.Contains(run(t, "since", "1 day ago"), "New knowledge") {
		t.Fatal("since")
	}
	if !strings.Contains(run(t, "changelog", "--type", "decision"), "DEC-0002") {
		t.Fatal("changelog")
	}
	if compact := run(t, "compact"); !strings.Contains(compact, "DEC-0001") || !strings.Contains(compact, "DEC-0002") {
		t.Fatalf("compact: %s", compact)
	}
	var events []Event
	if err := json.Unmarshal([]byte(run(t, "recent", "--json")), &events); err != nil || len(events) == 0 || events[0].ID == "" {
		t.Fatalf("event ID: %v %+v", err, events)
	}
	backup := strings.TrimSpace(run(t, "backup"))
	if _, err := os.Stat(filepath.Join(backup, "SHA256.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(backup, "entities", "renderer.md")); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := cli([]string{"doctor"}, &out); err == nil || !strings.Contains(out.String(), "multiple ACTIVE decisions") {
		t.Fatalf("doctor missed conflict: %v %s", err, out.String())
	}
	run(t, "supersede", "DEC-0001", "DEC-0002")
	run(t, "archive", "DEC-0002")
	if strings.Contains(run(t, "search", "Vulkan"), "DEC-0002") {
		t.Fatal("archived decision remained current")
	}
	run(t, "rebuild")
	if !strings.Contains(run(t, "doctor"), "healthy") {
		t.Fatal("doctor after resolution")
	}
	if _, err := os.Stat(filepath.Join(root, ".ledger", "ledger.db")); err != nil {
		t.Fatal(err)
	}
}

func TestMigratesV1Database(t *testing.T) {
	root := project(t)
	run(t, "add", "fact", "Legacy fact")
	dir := filepath.Join(root, ".ledger")
	dbPath := filepath.Join(dir, "ledger.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"DROP TABLE entry_files", "DROP TABLE entry_entities", "ALTER TABLE entries DROP COLUMN importance", "ALTER TABLE entries DROP COLUMN topic", "ALTER TABLE entries DROP COLUMN last_verified", "CREATE TABLE events_old(seq INTEGER PRIMARY KEY,time TEXT NOT NULL,kind TEXT NOT NULL,entry_id TEXT,detail TEXT NOT NULL)", "INSERT INTO events_old SELECT seq,time,kind,entry_id,detail FROM events", "DROP TABLE events", "ALTER TABLE events_old RENAME TO events", "UPDATE meta SET value='1' WHERE key='schema_version'"} {
		if _, err = db.Exec(q); err != nil {
			t.Fatalf("make v1 fixture: %s: %v", q, err)
		}
	}
	db.Close()
	if !strings.Contains(run(t, "search", "Legacy"), "FACT-0001") {
		t.Fatal("migrated search")
	}
	if !strings.Contains(run(t, "info"), "Schema version: 2") {
		t.Fatal("schema not migrated")
	}
	files, err := filepath.Glob(filepath.Join(dir, "archive", "ledger-schema-v1-*.db"))
	if err != nil || len(files) != 1 {
		t.Fatalf("migration backup: %v %v", files, err)
	}
	if !strings.Contains(run(t, "doctor"), "healthy") {
		t.Fatal("doctor after migration")
	}
}
func TestAtomicStatusRelationshipEvents(t *testing.T) {
	root := project(t)
	run(t, "add", "fact", "Old fact")
	run(t, "add", "fact", "New fact")
	run(t, "supersede", "FACT-0001", "FACT-0002")
	events, err := readEvents(filepath.Join(root, ".ledger", "ledger.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[2].Entry == nil || events[2].Relation == nil || events[2].Kind != "entry_superseded" {
		t.Fatalf("supersession must be one event: %+v", events)
	}
	run(t, "rebuild")
	if !strings.Contains(run(t, "show", "FACT-0001"), "SUPERSEDED") {
		t.Fatal("rebuild lost status")
	}
}
func TestFirstPointKeepsWrappedSentence(t *testing.T) {
	got := firstPoint("- First sentence wraps\n  onto another line.\n- Second point.")
	if got != "First sentence wraps onto another line." {
		t.Fatalf("first point: %q", got)
	}
}
func TestFirstActionSkipsIntroduction(t *testing.T) {
	got := firstAction("Two options:\n1. First task wraps\n   onto another line.\n2. Second task.")
	if got != "First task wraps onto another line." {
		t.Fatalf("first action: %q", got)
	}
}
