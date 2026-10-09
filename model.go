package main

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var prefixes = map[string]string{"FACT": "FACT", "DECISION": "DEC", "EXPERIMENT": "EXP", "FAILURE": "FAIL", "HYPOTHESIS": "HYP", "WARNING": "WARN", "TASK": "TASK", "NOTE": "NOTE"}
var idPattern = regexp.MustCompile(`^(FACT|DEC|EXP|FAIL|HYP|WARN|TASK|NOTE)-[0-9]{4,}$`)
var statuses = map[string]bool{"ACTIVE": true, "SUPERSEDED": true, "INVALIDATED": true, "UNVERIFIED": true, "DEPRECATED": true, "RESOLVED": true}
var importanceValues = map[string]bool{"low": true, "normal": true, "high": true, "critical": true}

type Entry struct {
	ID                 string     `json:"id"`
	Type               string     `json:"type"`
	Title              string     `json:"title"`
	Body               string     `json:"body"`
	Status             string     `json:"status"`
	Confidence         *float64   `json:"confidence,omitempty"`
	Importance         string     `json:"importance,omitempty"`
	Topic              string     `json:"topic,omitempty"`
	LastVerified       string     `json:"last_verified,omitempty"`
	CreatedAt          string     `json:"created_at"`
	UpdatedAt          string     `json:"updated_at"`
	SupersededBy       string     `json:"superseded_by,omitempty"`
	InvalidatedBy      string     `json:"invalidated_by,omitempty"`
	InvalidationReason string     `json:"invalidation_reason,omitempty"`
	Tags               []string   `json:"tags,omitempty"`
	Evidence           []string   `json:"evidence,omitempty"`
	Files              []string   `json:"source_files,omitempty"`
	Entities           []string   `json:"entities,omitempty"`
	Relations          []Relation `json:"relationships,omitempty"`
}
type Relation struct {
	From string `json:"from"`
	To   string `json:"to"`
	Type string `json:"type"`
}
type Event struct {
	ID       string    `json:"event_id,omitempty"`
	Seq      int64     `json:"seq"`
	Time     string    `json:"time"`
	Kind     string    `json:"kind"`
	Entry    *Entry    `json:"entry,omitempty"`
	Relation *Relation `json:"relation,omitempty"`
	Detail   string    `json:"detail,omitempty"`
}

func importance(e Entry) string {
	if e.Importance == "" {
		return "normal"
	}
	return e.Importance
}

func normalizeType(s string) (string, error) {
	s = strings.ToUpper(s)
	if _, ok := prefixes[s]; !ok {
		return "", fmt.Errorf("unknown entry type %q", s)
	}
	return s, nil
}
func validEntry(e Entry) error {
	if !idPattern.MatchString(e.ID) {
		return fmt.Errorf("invalid ID %q", e.ID)
	}
	p, ok := prefixes[e.Type]
	if !ok || !strings.HasPrefix(e.ID, p+"-") {
		return errors.New("ID and type do not match")
	}
	if strings.TrimSpace(e.Body) == "" {
		return errors.New("entry body cannot be empty")
	}
	if !statuses[e.Status] {
		return fmt.Errorf("invalid status %q", e.Status)
	}
	if e.Confidence != nil && (*e.Confidence < 0 || *e.Confidence > 1) {
		return errors.New("confidence must be between 0 and 1")
	}
	if !importanceValues[importance(e)] {
		return fmt.Errorf("invalid importance %q", e.Importance)
	}
	if e.LastVerified != "" {
		if _, err := time.Parse("2006-01-02", e.LastVerified); err != nil {
			return fmt.Errorf("last_verified must be YYYY-MM-DD: %w", err)
		}
	}
	if _, err := time.Parse(time.RFC3339Nano, e.CreatedAt); err != nil {
		return fmt.Errorf("invalid creation time: %w", err)
	}
	if _, err := time.Parse(time.RFC3339Nano, e.UpdatedAt); err != nil {
		return fmt.Errorf("invalid update time: %w", err)
	}
	if e.Status == "ACTIVE" && (e.SupersededBy != "" || e.InvalidatedBy != "" || e.InvalidationReason != "") {
		return errors.New("active entry has invalidation or supersession metadata")
	}
	return nil
}
