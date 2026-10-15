package hdc

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// HilogLevel is the severity level of a hilog entry.
type HilogLevel int

const (
	HilogLevelVerbose HilogLevel = 2
	HilogLevelDebug   HilogLevel = 3
	HilogLevelInfo    HilogLevel = 4
	HilogLevelWarn    HilogLevel = 5
	HilogLevelError   HilogLevel = 6
	HilogLevelFatal   HilogLevel = 7
)

func (l HilogLevel) String() string {
	switch l {
	case HilogLevelVerbose:
		return "V"
	case HilogLevelDebug:
		return "D"
	case HilogLevelInfo:
		return "I"
	case HilogLevelWarn:
		return "W"
	case HilogLevelError:
		return "E"
	case HilogLevelFatal:
		return "F"
	default:
		return "?"
	}
}

// HilogType is the log type of a hilog entry.
type HilogType int

const (
	HilogTypeApp HilogType = iota
	HilogTypeInit
	HilogTypeCore
	HilogTypeKernel
	HilogTypePrivate
)

var hilogTypeLetters = [...]string{"A", "I", "C", "K", "P"}

func (t HilogType) String() string {
	if int(t) < 0 || int(t) >= len(hilogTypeLetters) {
		return "?"
	}
	return hilogTypeLetters[t]
}

// HilogEntry is a single parsed hilog record.
type HilogEntry struct {
	Date    time.Time
	PID     int
	TID     int
	Level   HilogLevel
	Type    HilogType
	Domain  string
	Tag     string
	Message string
}

// Hilog is a live hilog stream. Entries are delivered on the channel returned
// by Entries until the stream is closed or errors out.
type Hilog struct {
	conn   *Connection
	ctx    context.Context
	cancel context.CancelFunc

	entries chan HilogEntry
	errs    chan error

	closeOnce sync.Once
}

// OpenHilog starts streaming device logs. When clear is true the existing log
// buffer is cleared first.
func (t *Target) OpenHilog(ctx context.Context, clear bool) (*Hilog, error) {
	if clear {
		if _, err := t.RunShell(ctx, "hilog -r"); err != nil {
			return nil, err
		}
	}

	conn, err := t.transport(ctx)
	if err != nil {
		return nil, err
	}
	if err := conn.send(ctx, []byte("shell hilog -v wrap -v epoch")); err != nil {
		conn.Close()
		return nil, err
	}

	hctx, cancel := context.WithCancel(ctx)
	h := &Hilog{
		conn:    conn,
		ctx:     hctx,
		cancel:  cancel,
		entries: make(chan HilogEntry),
		errs:    make(chan error, 1),
	}
	go h.loop()
	return h, nil
}

// Entries returns the channel of parsed log entries.
func (h *Hilog) Entries() <-chan HilogEntry { return h.entries }

// Errors returns a channel yielding a non-nil error if the stream stops
// unexpectedly.
func (h *Hilog) Errors() <-chan error { return h.errs }

// Close stops the stream and releases the underlying connection.
func (h *Hilog) Close() {
	h.closeOnce.Do(h.cancel)
}

func (h *Hilog) loop() {
	defer close(h.entries)
	defer close(h.errs)
	defer h.conn.Close()

	var parser hilogParser
	for {
		data, err := h.conn.readValue(h.ctx)
		if err != nil {
			if h.ctx.Err() == nil {
				select {
				case h.errs <- err:
				default:
				}
			}
			return
		}
		for _, entry := range parser.feed(string(data)) {
			select {
			case h.entries <- entry:
			case <-h.ctx.Done():
				return
			}
		}
	}
}

var (
	reHilogEntryStart = regexp.MustCompile(`\d{10}\.\d{3}\s+\d+\s+\d+`)
	reHilogEntry      = regexp.MustCompile(`^(\d{10}\.\d{3})\s+(\d+)\s+(\d+)\s+([DIWEF])\s+([AICKP])(.{5})\/([^:]*):(.*)`)
)

// hilogParser incrementally parses hilog output that may be split across
// arbitrary chunk boundaries.
type hilogParser struct {
	buf string
}

func (p *hilogParser) feed(chunk string) []HilogEntry {
	p.buf += chunk

	starts := reHilogEntryStart.FindAllStringIndex(p.buf, -1)
	entries := []HilogEntry{}
	for i := 0; i+1 < len(starts); i++ {
		raw := p.buf[starts[i][0]:starts[i+1][0]]
		if entry, ok := parseHilogEntry(raw); ok {
			entries = append(entries, entry)
		}
	}

	if len(starts) > 0 {
		p.buf = p.buf[starts[len(starts)-1][0]:]
	} else {
		p.buf = ""
	}
	return entries
}

func parseHilogEntry(raw string) (HilogEntry, bool) {
	m := reHilogEntry.FindStringSubmatch(raw)
	if m == nil {
		return HilogEntry{}, false
	}
	pid, _ := strconv.Atoi(m[2])
	tid, _ := strconv.Atoi(m[3])
	return HilogEntry{
		Date:    parseHilogTime(m[1]),
		PID:     pid,
		TID:     tid,
		Level:   hilogLevel(m[4]),
		Type:    hilogType(m[5]),
		Domain:  m[6],
		Tag:     m[7],
		Message: strings.TrimSpace(m[8]),
	}, true
}

func parseHilogTime(s string) time.Time {
	secPart, frac, _ := strings.Cut(s, ".")
	secs, _ := strconv.ParseInt(secPart, 10, 64)
	for len(frac) < 9 {
		frac += "0"
	}
	nsec, _ := strconv.ParseInt(frac[:9], 10, 64)
	return time.Unix(secs, nsec)
}

func hilogLevel(letter string) HilogLevel {
	switch letter {
	case "V":
		return HilogLevelVerbose
	case "D":
		return HilogLevelDebug
	case "I":
		return HilogLevelInfo
	case "W":
		return HilogLevelWarn
	case "E":
		return HilogLevelError
	case "F":
		return HilogLevelFatal
	default:
		return 0
	}
}

func hilogType(letter string) HilogType {
	for i, l := range hilogTypeLetters {
		if l == letter {
			return HilogType(i)
		}
	}
	return 0
}
