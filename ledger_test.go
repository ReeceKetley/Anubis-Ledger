package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func run(t *testing.T, args ...string) string {
	t.Helper()
	var b bytes.Buffer
	if err := cli(args, &b); err != nil {
		t.Fatalf("ledger %v: %v", args, err)
	}
	return b.String()
}
func project(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(old) })
	run(t, "init")
	return dir
}
func TestLifecycleAndRebuild(t *testing.T) {
	root := project(t)
	if !strings.Contains(run(t, "add", "fact", "Renderer A handles transparency", "--tag", "renderer", "--confidence", "0.8"), "FACT-0001") {
		t.Fatal("first ID")
	}
	run(t, "add", "experiment", "Renderer A failed transparency trace", "--tag", "renderer")
	run(t, "add", "fact", "Renderer B handles transparency", "--tag", "renderer")
	if !strings.Contains(run(t, "search", "transparency"), "FACT-0001") {
		t.Fatal("search failed")
	}
	run(t, "invalidate", "FACT-0001", "--because", "EXP-0001")
	search := run(t, "search", "transparency")
	if strings.Contains(search, "FACT-0001") {
		t.Fatal("invalidated fact in current search")
	}
	if !strings.Contains(run(t, "search", "transparency", "--all"), "FACT-0001") {
		t.Fatal("history filter")
	}
	if !strings.Contains(run(t, "history", "FACT-0001"), "entry_invalidated") {
		t.Fatal("history")
	}
	run(t, "supersede", "FACT-0002", "EXP-0001") // replacement must be active
	if !strings.Contains(run(t, "context", "transparency"), "Renderer A failed") {
		t.Fatal("context")
	}
	if !strings.Contains(run(t, "show", "FACT-0001"), "Invalidated by: EXP-0001") {
		t.Fatal("show")
	}
	log, err := os.ReadFile(filepath.Join(root, ".ledger", "ledger.jsonl"))
	if err != nil || bytes.Count(log, []byte("\n")) < 4 {
		t.Fatal("JSONL persistence")
	}
	run(t, "rebuild")
	if !strings.Contains(run(t, "show", "FACT-0001"), "INVALIDATED") {
		t.Fatal("rebuild status")
	}
	if !strings.Contains(run(t, "doctor"), "healthy") {
		t.Fatal("doctor")
	}
}
func TestUpwardAndMissing(t *testing.T) {
	root := project(t)
	child := filepath.Join(root, "src", "pkg")
	if err := os.MkdirAll(child, 0700); err != nil {
		t.Fatal(err)
	}
	os.Chdir(child)
	run(t, "add", "task", "Test nested lookup")
	if !strings.Contains(run(t, "unresolved"), "TASK-0001") {
		t.Fatal("upward discovery")
	}
	os.Chdir(t.TempDir())
	var out bytes.Buffer
	if err := cli([]string{"status"}, &out); err == nil {
		t.Fatal("missing .ledger should fail")
	}
}
func TestInvalidRelationsAndJSON(t *testing.T) {
	project(t)
	run(t, "add", "fact", "Old answer")
	run(t, "add", "fact", "New answer")
	var b bytes.Buffer
	if err := cli([]string{"supersede", "FACT-0001", "FACT-0001"}, &b); err == nil {
		t.Fatal("self relation accepted")
	}
	run(t, "supersede", "FACT-0001", "FACT-0002")
	if err := cli([]string{"supersede", "FACT-0001", "FACT-0002"}, &b); err == nil {
		t.Fatal("duplicate supersession accepted")
	}
	var entries []Entry
	if err := json.Unmarshal([]byte(run(t, "search", "answer", "--json")), &entries); err != nil || len(entries) != 1 || entries[0].ID != "FACT-0002" {
		t.Fatalf("JSON search: %v %#v", err, entries)
	}
}
func TestDoctorAndSchemaVersion(t *testing.T) {
	root := project(t)
	s, err := openStore(filepath.Join(root, ".ledger"), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("UPDATE meta SET value='99' WHERE key='schema_version'"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	var b bytes.Buffer
	if err := cli([]string{"status"}, &b); err == nil || !strings.Contains(err.Error(), "schema version") {
		t.Fatalf("expected version error, got %v", err)
	}
	b.Reset()
	if err := cli([]string{"doctor"}, &b); err == nil || !strings.Contains(b.String(), "unsupported schema version") {
		t.Fatalf("doctor missed version: %v %s", err, b.String())
	}
	if err := os.WriteFile(filepath.Join(root, ".ledger", "ledger.jsonl"), []byte("broken\n"), 0600); err != nil {
		t.Fatal(err)
	}
	b.Reset()
	if err := cli([]string{"doctor"}, &b); err == nil || !strings.Contains(b.String(), "JSONL line") {
		t.Fatalf("doctor missed malformed JSONL: %v %s", err, b.String())
	}
}
func TestReplayPendingEvent(t *testing.T) {
	root := project(t)
	run(t, "add", "fact", "Pending replay example")
	s, err := openStore(filepath.Join(root, ".ledger"), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("DELETE FROM events"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("DELETE FROM entries_fts"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("DELETE FROM entries"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("UPDATE meta SET value='0' WHERE key='log_offset'"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if !strings.Contains(run(t, "search", "Pending"), "FACT-0001") {
		t.Fatal("pending event was not replayed")
	}
}
func TestInitIsSafeAndFilters(t *testing.T) {
	root := project(t)
	goal := filepath.Join(root, ".ledger", "GOAL.md")
	if err := os.WriteFile(goal, []byte("custom goal\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := cli([]string{"init"}, &b); err == nil {
		t.Fatal("second init succeeded")
	}
	contents, err := os.ReadFile(goal)
	if err != nil || string(contents) != "custom goal\n" {
		t.Fatal("second init changed memory")
	}
	run(t, "add", "fact", "Renderer uses queue", "--tag", "renderer")
	run(t, "add", "fact", "Audio uses queue", "--tag", "audio")
	run(t, "invalidate", "FACT-0001", "--reason", "New trace")
	if !strings.Contains(run(t, "search", "queue", "--status", "INVALIDATED"), "FACT-0001") {
		t.Fatal("explicit status filter")
	}
	tagged := run(t, "search", "queue", "--all", "--tag", "audio")
	if strings.Contains(tagged, "FACT-0001") || !strings.Contains(tagged, "FACT-0002") {
		t.Fatal("tag filter")
	}
}
func TestDoctorDanglingEvidence(t *testing.T) {
	root := project(t)
	run(t, "add", "fact", "Known statement")
	s, err := openStore(filepath.Join(root, ".ledger"), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec("INSERT INTO evidence(entry_id,ref) VALUES('FACT-0001','EXP-9999')"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	var b bytes.Buffer
	if err := cli([]string{"doctor"}, &b); err == nil || !strings.Contains(b.String(), "dangling evidence") {
		t.Fatalf("doctor missed evidence: %v %s", err, b.String())
	}
}
func TestOrientationWithoutQuery(t *testing.T) {
	project(t)
	run(t, "add", "warning", "Watch GPU residency")
	for _, cmd := range []string{"status", "brief", "blockers"} {
		if got := run(t, cmd); !strings.Contains(got, "Watch GPU residency") {
			t.Fatalf("%s omitted warning: %s", cmd, got)
		}
	}
}
