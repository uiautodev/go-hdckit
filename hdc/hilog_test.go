package hdc

import (
	"testing"
	"time"
)

const entry1 = "1699999999.123   1000  1001 I A00001/MyTag: hello world\n"
const entry2 = "1699999999.456   2000  2001 E C00ABC/Other: boom: kaboom\n"

func TestHilogParserAcrossChunks(t *testing.T) {
	var parser hilogParser

	if got := parser.feed(entry1); len(got) != 0 {
		t.Fatalf("expected no entries before a second start, got %d", len(got))
	}

	// Feed the second entry one byte at a time; the first entry should be
	// emitted exactly once.
	var entries []HilogEntry
	for i := 0; i < len(entry2); i++ {
		entries = append(entries, parser.feed(entry2[i:i+1])...)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	e := entries[0]
	if e.PID != 1000 || e.TID != 1001 {
		t.Errorf("pid/tid = %d/%d, want 1000/1001", e.PID, e.TID)
	}
	if e.Level != HilogLevelInfo {
		t.Errorf("level = %v, want I", e.Level)
	}
	if e.Type != HilogTypeApp {
		t.Errorf("type = %v, want A", e.Type)
	}
	if e.Domain != "00001" {
		t.Errorf("domain = %q, want 00001", e.Domain)
	}
	if e.Tag != "MyTag" {
		t.Errorf("tag = %q, want MyTag", e.Tag)
	}
	if e.Message != "hello world" {
		t.Errorf("message = %q, want %q", e.Message, "hello world")
	}
	want := time.Unix(1699999999, 123000000)
	if !e.Date.Equal(want) {
		t.Errorf("date = %v, want %v", e.Date, want)
	}
}

func TestParseHilogEntryErrorLevel(t *testing.T) {
	entry, ok := parseHilogEntry(entry2)
	if !ok {
		t.Fatal("expected entry to parse")
	}
	if entry.Level != HilogLevelError || entry.Type != HilogTypeCore {
		t.Errorf("level/type = %v/%v, want E/C", entry.Level, entry.Type)
	}
	if entry.Tag != "Other" || entry.Message != "boom: kaboom" {
		t.Errorf("tag/message = %q/%q", entry.Tag, entry.Message)
	}
}

func TestHilogEnums(t *testing.T) {
	if got := hilogLevel("F").String(); got != "F" {
		t.Errorf("fatal string = %q", got)
	}
	if got := hilogType("K").String(); got != "K" {
		t.Errorf("kernel type string = %q", got)
	}
}
