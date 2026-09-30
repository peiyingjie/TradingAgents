package memory

import (
	"fmt"
	"golang.org/x/text/unicode/norm"
	"os"
	"regexp"
	"strings"
	"sync"
	"tradingagents/internal/dataflows"
)

const Separator = "\n\n<!-- ENTRY_END -->\n\n"

type Entry struct {
	Date       string `json:"date"`
	Ticker     string `json:"ticker"`
	Rating     string `json:"rating"`
	Pending    bool   `json:"pending"`
	Raw        string `json:"raw"`
	Alpha      string `json:"alpha"`
	Holding    string `json:"holding"`
	Resolved   string `json:"resolved"`
	Decision   string `json:"decision"`
	Reflection string `json:"reflection"`
}
type Outcome struct {
	Ticker, Date, Reflection, ResolutionDate string
	RawReturn, AlphaReturn                   float64
	HoldingDays                              int
}
type Log struct {
	Path       string
	MaxEntries *int
	mu         sync.Mutex
}

func Rating(text string) string {
	clean := norm.NFKC.String(text)
	label := regexp.MustCompile(`(?i)rating\b[^:\-‐-―]*[:\-‐-―][\s*]*(\w+)`)
	scale := regexp.MustCompile(`(?i)rating\s*(scale|options|legend)`)
	words := regexp.MustCompile(`(?i)\b(Buy|Overweight|Hold|Underweight|Sell)\b`)
	canonical := func(v string) string { v = strings.ToLower(v); return strings.ToUpper(v[:1]) + v[1:] }
	last := ""
	for _, line := range strings.Split(clean, "\n") {
		if scale.MatchString(line) {
			continue
		}
		m := label.FindStringSubmatch(line)
		if len(m) > 1 && words.MatchString(m[1]) {
			v := canonical(m[1])
			if v == "Buy" || v == "Overweight" || v == "Hold" || v == "Underweight" || v == "Sell" {
				last = v
			}
		}
	}
	if last != "" {
		return last
	}
	found := map[string]bool{}
	for _, m := range words.FindAllString(clean, -1) {
		found[canonical(m)] = true
	}
	if len(found) == 1 {
		for r := range found {
			return r
		}
	}
	return "REVIEW"
}
func Parse(raw string) *Entry {
	raw = strings.TrimSpace(raw)
	first, body, _ := strings.Cut(raw, "\n")
	if !strings.HasPrefix(first, "[") || !strings.HasSuffix(first, "]") {
		return nil
	}
	fields := strings.Split(first[1:len(first)-1], "|")
	for i := range fields {
		fields[i] = strings.TrimSpace(fields[i])
	}
	if len(fields) < 4 {
		return nil
	}
	e := &Entry{Date: fields[0], Ticker: fields[1], Rating: fields[2], Pending: fields[3] == "pending"}
	if !e.Pending {
		e.Raw = fields[3]
	}
	if len(fields) > 4 {
		e.Alpha = fields[4]
	}
	if len(fields) > 5 {
		e.Holding = fields[5]
	}
	for _, f := range fields[min(6, len(fields)):] {
		if strings.HasPrefix(f, "resolved:") {
			e.Resolved = strings.TrimSpace(strings.TrimPrefix(f, "resolved:"))
		}
	}
	if i := strings.Index(body, "DECISION:\n"); i >= 0 {
		decision := body[i+len("DECISION:\n"):]
		if j := strings.Index(decision, "\nREFLECTION:"); j >= 0 {
			decision = decision[:j]
		}
		e.Decision = strings.TrimSpace(decision)
	}
	if i := strings.Index(body, "REFLECTION:\n"); i >= 0 {
		e.Reflection = strings.TrimSpace(body[i+len("REFLECTION:\n"):])
	}
	return e
}
func (l *Log) read() (string, error) {
	if l.Path == "" {
		return "", nil
	}
	b, e := os.ReadFile(l.Path)
	if os.IsNotExist(e) {
		return "", nil
	}
	return strings.ReplaceAll(string(b), "\r\n", "\n"), e
}
func (l *Log) Entries() ([]Entry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	text, e := l.read()
	if e != nil {
		return nil, e
	}
	entries := []Entry{}
	for _, block := range strings.Split(text, Separator) {
		if entry := Parse(block); entry != nil {
			entries = append(entries, *entry)
		}
	}
	return entries, nil
}
func (l *Log) Store(ticker, date, decision string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.Path == "" {
		return nil
	}
	raw, e := l.read()
	if e != nil {
		return e
	}
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, "["+date+" | "+ticker+" |") && strings.HasSuffix(line, "]") {
			return nil
		}
	}
	entry := fmt.Sprintf("[%s | %s | %s | pending]\n\nDECISION:\n%s%s", date, ticker, Rating(decision), decision, Separator)
	return dataflows.AtomicWrite(l.Path, []byte(raw+entry))
}
func (l *Log) PastContext(ticker, asOf string) (string, error) {
	entries, e := l.Entries()
	if e != nil {
		return "", e
	}
	same, cross := []Entry{}, []Entry{}
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.Pending || asOf != "" && (e.Resolved == "" || e.Resolved > asOf) {
			continue
		}
		if e.Ticker == ticker && len(same) < 5 {
			same = append(same, e)
		} else if e.Ticker != ticker && len(cross) < 3 {
			cross = append(cross, e)
		}
	}
	fallback := func(s string) string {
		if s == "" {
			return "n/a"
		}
		return s
	}
	parts := []string{}
	if len(same) > 0 {
		parts = append(parts, "Past analyses of "+ticker+" (most recent first):")
		for _, e := range same {
			p := fmt.Sprintf("[%s | %s | %s | %s | %s | %s]\n\nDECISION:\n%s", e.Date, e.Ticker, e.Rating, fallback(e.Raw), fallback(e.Alpha), fallback(e.Holding), e.Decision)
			if e.Reflection != "" {
				p += "\n\nREFLECTION:\n" + e.Reflection
			}
			parts = append(parts, p)
		}
	}
	if len(cross) > 0 {
		parts = append(parts, "Recent cross-ticker lessons:")
		for _, e := range cross {
			body := e.Reflection
			if body == "" {
				r := []rune(e.Decision)
				body = e.Decision
				if len(r) > 300 {
					body = string(r[:300]) + "..."
				}
			}
			parts = append(parts, fmt.Sprintf("[%s | %s | %s | %s]\n%s", e.Date, e.Ticker, e.Rating, fallback(e.Raw), body))
		}
	}
	return strings.Join(parts, "\n\n"), nil
}
func (l *Log) Update(outcomes []Outcome) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.Path == "" || len(outcomes) == 0 {
		return nil
	}
	raw, e := l.read()
	if e != nil {
		return e
	}
	updates := map[string]Outcome{}
	for _, o := range outcomes {
		updates[o.Date+"|"+o.Ticker] = o
	}
	blocks := strings.Split(raw, Separator)
	for i, b := range blocks {
		entry := Parse(b)
		if entry == nil || !entry.Pending {
			continue
		}
		key := entry.Date + "|" + entry.Ticker
		o, ok := updates[key]
		if !ok {
			continue
		}
		resolved := ""
		if o.ResolutionDate != "" {
			resolved = " | resolved:" + o.ResolutionDate
		}
		_, rest, _ := strings.Cut(strings.TrimSpace(b), "\n")
		blocks[i] = fmt.Sprintf("[%s | %s | %s | %+.1f%% | %+.1f%% | %dd%s]\n\n%s\n\nREFLECTION:\n%s", entry.Date, entry.Ticker, entry.Rating, o.RawReturn*100, o.AlphaReturn*100, o.HoldingDays, resolved, strings.TrimLeft(rest, "\n \t"), o.Reflection)
		delete(updates, key)
	}
	if l.MaxEntries != nil && *l.MaxEntries > 0 {
		count := 0
		for _, b := range blocks {
			if e := Parse(b); e != nil && !e.Pending {
				count++
			}
		}
		drop := count - *l.MaxEntries
		kept := []string{}
		for _, b := range blocks {
			if e := Parse(b); e != nil && !e.Pending && drop > 0 {
				drop--
				continue
			}
			kept = append(kept, b)
		}
		blocks = kept
	}
	return dataflows.AtomicWrite(l.Path, []byte(strings.Join(blocks, Separator)))
}
