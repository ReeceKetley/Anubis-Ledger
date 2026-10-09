package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/flock"
	_ "modernc.org/sqlite"
)

const schemaVersion = 2
const schema = `
PRAGMA foreign_keys=ON;
CREATE TABLE IF NOT EXISTS meta(key TEXT PRIMARY KEY,value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS entries(id TEXT PRIMARY KEY,type TEXT NOT NULL,title TEXT NOT NULL,body TEXT NOT NULL,status TEXT NOT NULL,confidence REAL,created_at TEXT NOT NULL,updated_at TEXT NOT NULL,superseded_by TEXT,invalidated_by TEXT,invalidation_reason TEXT,source_files TEXT NOT NULL,entities TEXT NOT NULL,importance TEXT NOT NULL DEFAULT 'normal',topic TEXT NOT NULL DEFAULT '',last_verified TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS entries_status_type ON entries(status,type,updated_at DESC);
CREATE TABLE IF NOT EXISTS tags(entry_id TEXT NOT NULL REFERENCES entries(id),tag TEXT NOT NULL,PRIMARY KEY(entry_id,tag));
CREATE INDEX IF NOT EXISTS tags_tag ON tags(tag);
CREATE TABLE IF NOT EXISTS evidence(entry_id TEXT NOT NULL REFERENCES entries(id),ref TEXT NOT NULL,PRIMARY KEY(entry_id,ref));
CREATE TABLE IF NOT EXISTS entry_files(entry_id TEXT NOT NULL REFERENCES entries(id),path TEXT NOT NULL,PRIMARY KEY(entry_id,path));
CREATE INDEX IF NOT EXISTS entry_files_path ON entry_files(path);
CREATE TABLE IF NOT EXISTS entry_entities(entry_id TEXT NOT NULL REFERENCES entries(id),name TEXT NOT NULL,PRIMARY KEY(entry_id,name));
CREATE INDEX IF NOT EXISTS entry_entities_name ON entry_entities(name);
CREATE TABLE IF NOT EXISTS relations(from_id TEXT NOT NULL REFERENCES entries(id),to_id TEXT NOT NULL REFERENCES entries(id),type TEXT NOT NULL,PRIMARY KEY(from_id,to_id,type));
CREATE TABLE IF NOT EXISTS events(seq INTEGER PRIMARY KEY,time TEXT NOT NULL,kind TEXT NOT NULL,entry_id TEXT,detail TEXT NOT NULL,event_id TEXT NOT NULL UNIQUE);
CREATE VIRTUAL TABLE IF NOT EXISTS entries_fts USING fts5(id UNINDEXED,title,body,tags,files,entities);
`

type Store struct {
	Dir  string
	DB   *sql.DB
	lock *flock.Flock
}

func discover(start string) (string, error) {
	p, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for {
		d := filepath.Join(p, ".ledger")
		if st, e := os.Stat(d); e == nil && st.IsDir() {
			return d, nil
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", errors.New("no .ledger directory found; run ledger init at the project root")
		}
		p = parent
	}
}
func openStore(dir string, repair bool) (*Store, error) {
	s := &Store{Dir: dir, lock: flock.New(filepath.Join(dir, ".lock"))}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ok, err := s.lock.TryLockContext(ctx, 50*time.Millisecond)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("Ledger is busy; retry shortly")
	}
	dbPath := filepath.Join(dir, "ledger.db")
	if _, err = os.Stat(dbPath); err != nil {
		s.Close()
		return nil, fmt.Errorf("database missing; run ledger rebuild: %w", err)
	}
	s.DB, err = sql.Open("sqlite", dbPath)
	if err != nil {
		s.Close()
		return nil, err
	}
	s.DB.SetMaxOpenConns(1)
	if _, err = s.DB.Exec("PRAGMA foreign_keys=ON"); err != nil {
		s.Close()
		return nil, err
	}
	var v string
	err = s.DB.QueryRow("SELECT value FROM meta WHERE key='schema_version'").Scan(&v)
	if err != nil {
		s.Close()
		return nil, fmt.Errorf("unsupported database schema; run ledger doctor: %w", err)
	}
	if v == "1" {
		if err = s.migrateV1(); err != nil {
			s.Close()
			return nil, fmt.Errorf("schema migration: %w", err)
		}
		v = "2"
	}
	if v != strconv.Itoa(schemaVersion) {
		s.Close()
		return nil, fmt.Errorf("database schema version %s is unsupported (expected %d)", v, schemaVersion)
	}
	if repair {
		if err = s.replayPending(); err != nil {
			s.Close()
			return nil, err
		}
	}
	return s, nil
}
func (s *Store) migrateV1() error {
	backup := filepath.Join(s.Dir, "archive", "ledger-schema-v1-"+time.Now().UTC().Format("20060102T150405.000000000")+".db")
	if _, err := s.DB.Exec("VACUUM INTO ?", backup); err != nil {
		return fmt.Errorf("could not back up v1 database: %w", err)
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		"ALTER TABLE entries ADD COLUMN importance TEXT NOT NULL DEFAULT 'normal'",
		"ALTER TABLE entries ADD COLUMN topic TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE entries ADD COLUMN last_verified TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE events ADD COLUMN event_id TEXT NOT NULL DEFAULT ''",
		"UPDATE events SET event_id=printf('EVT-%08d',seq)",
		"CREATE UNIQUE INDEX events_event_id ON events(event_id)",
		"CREATE TABLE entry_files(entry_id TEXT NOT NULL REFERENCES entries(id),path TEXT NOT NULL,PRIMARY KEY(entry_id,path))",
		"CREATE INDEX entry_files_path ON entry_files(path)",
		"CREATE TABLE entry_entities(entry_id TEXT NOT NULL REFERENCES entries(id),name TEXT NOT NULL,PRIMARY KEY(entry_id,name))",
		"CREATE INDEX entry_entities_name ON entry_entities(name)",
		"INSERT INTO entry_files(entry_id,path) SELECT e.id,j.value FROM entries e,json_each(e.source_files) j WHERE j.value IS NOT NULL",
		"INSERT INTO entry_entities(entry_id,name) SELECT e.id,j.value FROM entries e,json_each(e.entities) j WHERE j.value IS NOT NULL",
		"UPDATE meta SET value='2' WHERE key='schema_version'",
	} {
		if _, err = tx.Exec(q); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) Close() {
	if s.DB != nil {
		s.DB.Close()
	}
	if s.lock != nil {
		s.lock.Unlock()
	}
}
func createDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	if _, err = db.Exec("INSERT OR IGNORE INTO meta(key,value) VALUES('schema_version',?)", strconv.Itoa(schemaVersion)); err != nil {
		db.Close()
		return nil, err
	}
	if _, err = db.Exec("INSERT OR IGNORE INTO meta(key,value) VALUES('log_offset','0')"); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func readEvents(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	var out []Event
	var n int64
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			n++
			if line[len(line)-1] != '\n' {
				return nil, fmt.Errorf("JSONL line %d is incomplete", n)
			}
			var ev Event
			if e := json.Unmarshal(line, &ev); e != nil {
				return nil, fmt.Errorf("JSONL line %d: %w", n, e)
			}
			if ev.Seq != n {
				return nil, fmt.Errorf("JSONL line %d has sequence %d", n, ev.Seq)
			}
			if ev.ID == "" {
				ev.ID = fmt.Sprintf("EVT-%08d", ev.Seq)
			}
			if ev.ID != fmt.Sprintf("EVT-%08d", ev.Seq) {
				return nil, fmt.Errorf("JSONL line %d has invalid event ID %q", n, ev.ID)
			}
			out = append(out, ev)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
func (s *Store) replayPending() error {
	var offset, last int64
	if err := s.DB.QueryRow("SELECT value FROM meta WHERE key='log_offset'").Scan(&offset); err != nil {
		return err
	}
	if err := s.DB.QueryRow("SELECT COALESCE(MAX(seq),0) FROM events").Scan(&last); err != nil {
		return err
	}
	f, err := os.Open(filepath.Join(s.Dir, "ledger.jsonl"))
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.Size() < offset {
		return errors.New("event log is shorter than indexed offset; run ledger doctor")
	}
	if st.Size() == offset {
		return nil
	}
	if _, err = f.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	r := bufio.NewReader(f)
	for {
		line, er := r.ReadBytes('\n')
		if len(line) > 0 {
			if line[len(line)-1] != '\n' {
				return errors.New("incomplete trailing JSONL event")
			}
			var ev Event
			if err = json.Unmarshal(line, &ev); err != nil {
				return err
			}
			if ev.Seq != last+1 {
				return fmt.Errorf("unexpected event sequence %d after %d", ev.Seq, last)
			}
			if ev.ID == "" {
				ev.ID = fmt.Sprintf("EVT-%08d", ev.Seq)
			}
			if ev.ID != fmt.Sprintf("EVT-%08d", ev.Seq) {
				return fmt.Errorf("invalid event ID %q", ev.ID)
			}
			offset += int64(len(line))
			if err = s.apply(ev, offset); err != nil {
				return err
			}
			last++
		}
		if er == io.EOF {
			break
		}
		if er != nil {
			return er
		}
	}
	return nil
}
func (s *Store) appendEvent(ev Event) (int64, error) {
	b, err := json.Marshal(ev)
	if err != nil {
		return 0, err
	}
	f, err := os.OpenFile(filepath.Join(s.Dir, "ledger.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	if _, err = f.Write(append(b, '\n')); err != nil {
		return 0, err
	}
	if err = f.Sync(); err != nil {
		return 0, err
	}
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}
func (s *Store) nextSeq() (int64, error) {
	var n int64
	err := s.DB.QueryRow("SELECT COALESCE(MAX(seq),0)+1 FROM events").Scan(&n)
	return n, err
}
func (s *Store) record(ev Event) error {
	if ev.Entry != nil {
		if err := validEntry(*ev.Entry); err != nil {
			return err
		}
		if ev.Kind == "entry_created" {
			var n int
			if err := s.DB.QueryRow("SELECT COUNT(*) FROM entries WHERE id=?", ev.Entry.ID).Scan(&n); err != nil {
				return err
			}
			if n != 0 {
				return fmt.Errorf("entry ID %s already exists", ev.Entry.ID)
			}
		}
	}
	if ev.Relation != nil {
		r := ev.Relation
		if r.From == r.To || !idPattern.MatchString(r.From) || !idPattern.MatchString(r.To) {
			return errors.New("invalid relationship IDs")
		}
		allowed := map[string]bool{"SUPERSEDES": true, "INVALIDATES": true, "EVIDENCE_FOR": true, "RELATES_TO": true, "DEPENDS_ON": true, "BLOCKS": true}
		if !allowed[r.Type] {
			return fmt.Errorf("invalid relationship type %s", r.Type)
		}
		for _, id := range []string{r.From, r.To} {
			if ev.Entry != nil && ev.Entry.ID == id {
				continue
			}
			var n int
			if err := s.DB.QueryRow("SELECT COUNT(*) FROM entries WHERE id=?", id).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return fmt.Errorf("relationship target %s does not exist", id)
			}
		}
		var n int
		if err := s.DB.QueryRow("SELECT COUNT(*) FROM relations WHERE from_id=? AND to_id=? AND type=?", r.From, r.To, r.Type).Scan(&n); err != nil {
			return err
		}
		if n != 0 {
			return errors.New("relationship already exists")
		}
	}
	n, err := s.nextSeq()
	if err != nil {
		return err
	}
	ev.Seq = n
	ev.ID = fmt.Sprintf("EVT-%08d", n)
	ev.Time = time.Now().UTC().Format(time.RFC3339Nano)
	offset, err := s.appendEvent(ev)
	if err != nil {
		return err
	}
	return s.apply(ev, offset)
}
func (s *Store) apply(ev Event, offset int64) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if ev.Entry != nil {
		e := ev.Entry
		if err = validEntry(*e); err != nil {
			return err
		}
		files, _ := json.Marshal(e.Files)
		entities, _ := json.Marshal(e.Entities)
		_, err = tx.Exec(`INSERT INTO entries(id,type,title,body,status,confidence,created_at,updated_at,superseded_by,invalidated_by,invalidation_reason,source_files,entities,importance,topic,last_verified) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET title=excluded.title,body=excluded.body,status=excluded.status,confidence=excluded.confidence,updated_at=excluded.updated_at,superseded_by=excluded.superseded_by,invalidated_by=excluded.invalidated_by,invalidation_reason=excluded.invalidation_reason,source_files=excluded.source_files,entities=excluded.entities,importance=excluded.importance,topic=excluded.topic,last_verified=excluded.last_verified`, e.ID, e.Type, e.Title, e.Body, e.Status, e.Confidence, e.CreatedAt, e.UpdatedAt, e.SupersededBy, e.InvalidatedBy, e.InvalidationReason, string(files), string(entities), importance(*e), e.Topic, e.LastVerified)
		if err != nil {
			return err
		}
		for _, q := range []string{"DELETE FROM tags WHERE entry_id=?", "DELETE FROM evidence WHERE entry_id=?", "DELETE FROM entry_files WHERE entry_id=?", "DELETE FROM entry_entities WHERE entry_id=?", "DELETE FROM entries_fts WHERE id=?"} {
			if _, err = tx.Exec(q, e.ID); err != nil {
				return err
			}
		}
		for _, tag := range e.Tags {
			if _, err = tx.Exec("INSERT INTO tags VALUES(?,?)", e.ID, tag); err != nil {
				return err
			}
		}
		for _, ref := range e.Evidence {
			if _, err = tx.Exec("INSERT INTO evidence VALUES(?,?)", e.ID, ref); err != nil {
				return err
			}
		}
		for _, file := range e.Files {
			if _, err = tx.Exec("INSERT INTO entry_files VALUES(?,?)", e.ID, file); err != nil {
				return err
			}
		}
		for _, entity := range e.Entities {
			if _, err = tx.Exec("INSERT INTO entry_entities VALUES(?,?)", e.ID, entity); err != nil {
				return err
			}
		}
		if _, err = tx.Exec("INSERT INTO entries_fts(id,title,body,tags,files,entities) VALUES(?,?,?,?,?,?)", e.ID, e.Title, e.Body, strings.Join(e.Tags, " "), strings.Join(e.Files, " "), strings.Join(e.Entities, " ")); err != nil {
			return err
		}
	}
	if ev.Relation != nil {
		r := ev.Relation
		_, err = tx.Exec("INSERT INTO relations(from_id,to_id,type) VALUES(?,?,?)", r.From, r.To, r.Type)
		if err != nil {
			return err
		}
	}
	id := ""
	if ev.Entry != nil {
		id = ev.Entry.ID
	}
	if ev.ID == "" {
		ev.ID = fmt.Sprintf("EVT-%08d", ev.Seq)
	}
	_, err = tx.Exec("INSERT INTO events(seq,time,kind,entry_id,detail,event_id) VALUES(?,?,?,?,?,?)", ev.Seq, ev.Time, ev.Kind, id, ev.Detail, ev.ID)
	if err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO meta(key,value) VALUES('log_offset',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", offset); err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) get(id string) (Entry, error) {
	var e Entry
	var confidence sql.NullFloat64
	var files, entities string
	err := s.DB.QueryRow(`SELECT id,type,title,body,status,confidence,created_at,updated_at,COALESCE(superseded_by,''),COALESCE(invalidated_by,''),COALESCE(invalidation_reason,''),source_files,entities,importance,topic,last_verified FROM entries WHERE id=?`, id).Scan(&e.ID, &e.Type, &e.Title, &e.Body, &e.Status, &confidence, &e.CreatedAt, &e.UpdatedAt, &e.SupersededBy, &e.InvalidatedBy, &e.InvalidationReason, &files, &entities, &e.Importance, &e.Topic, &e.LastVerified)
	if err != nil {
		return e, err
	}
	if confidence.Valid {
		e.Confidence = &confidence.Float64
	}
	json.Unmarshal([]byte(files), &e.Files)
	json.Unmarshal([]byte(entities), &e.Entities)
	for _, spec := range []struct {
		q   string
		dst *[]string
	}{{"SELECT tag FROM tags WHERE entry_id=? ORDER BY tag", &e.Tags}, {"SELECT ref FROM evidence WHERE entry_id=? ORDER BY ref", &e.Evidence}} {
		rows, er := s.DB.Query(spec.q, id)
		if er != nil {
			return e, er
		}
		for rows.Next() {
			var v string
			if er = rows.Scan(&v); er != nil {
				break
			}
			*spec.dst = append(*spec.dst, v)
		}
		rows.Close()
		if er != nil {
			return e, er
		}
	}
	rows, err := s.DB.Query("SELECT from_id,to_id,type FROM relations WHERE from_id=? OR to_id=? ORDER BY type,from_id,to_id", id, id)
	if err != nil {
		return e, err
	}
	defer rows.Close()
	for rows.Next() {
		var r Relation
		if err = rows.Scan(&r.From, &r.To, &r.Type); err != nil {
			return e, err
		}
		e.Relations = append(e.Relations, r)
	}
	return e, rows.Err()
}
func initProject(root string) error {
	dir := filepath.Join(root, ".ledger")
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("%s already exists; existing memory was not changed", dir)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return err
	}
	for _, name := range []string{"entities", "archive"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0700); err != nil {
			return err
		}
	}
	for name, body := range templates {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "ledger.jsonl"), nil, 0600); err != nil {
		return err
	}
	db, err := createDB(filepath.Join(dir, "ledger.db"))
	if err != nil {
		return err
	}
	return db.Close()
}
func rebuild(dir string) error {
	events, err := readEvents(filepath.Join(dir, "ledger.jsonl"))
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "ledger.db.rebuild")
	if _, err = os.Stat(tmp); err == nil {
		return fmt.Errorf("%s already exists; inspect it before rebuilding", tmp)
	}
	db, err := createDB(tmp)
	if err != nil {
		return err
	}
	s := &Store{Dir: dir, DB: db}
	for _, ev := range events {
		if err = s.apply(ev, 0); err != nil {
			db.Close()
			return fmt.Errorf("event %d: %w", ev.Seq, err)
		}
	}
	st, err := os.Stat(filepath.Join(dir, "ledger.jsonl"))
	if err != nil {
		db.Close()
		return err
	}
	if _, err = db.Exec("UPDATE meta SET value=? WHERE key='log_offset'", st.Size()); err != nil {
		db.Close()
		return err
	}
	db.Close()
	old := filepath.Join(dir, "ledger.db")
	backup := filepath.Join(dir, "archive", "ledger-"+time.Now().UTC().Format("20060102T150405.000000000")+".db")
	if _, err = os.Stat(old); err == nil {
		if err = os.Rename(old, backup); err != nil {
			return err
		}
	}
	if err = os.Rename(tmp, old); err != nil {
		os.Rename(backup, old)
		return err
	}
	return nil
}
