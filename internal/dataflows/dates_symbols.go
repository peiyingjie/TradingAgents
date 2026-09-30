package dataflows

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const dateLayout = "2006-01-02"

func ParseDate(s string) (time.Time, error) {
	t, e := time.Parse(dateLayout, s)
	if e != nil || t.Format(dateLayout) != s {
		return t, fmt.Errorf("date must be in YYYY-MM-DD format, got %q", s)
	}
	return t, nil
}
func AsOf(requested, tradeDate string) string {
	if tradeDate == "" {
		return requested
	}
	r, e := ParseDate(requested)
	d, de := ParseDate(tradeDate)
	if e == nil && de == nil && !r.After(d) {
		return requested
	}
	return tradeDate
}
func AsOfWindow(start, end, tradeDate string) (string, string) {
	clamped := AsOf(end, tradeDate)
	s, se := ParseDate(start)
	e, ee := ParseDate(end)
	c, ce := ParseDate(clamped)
	if clamped == end || se != nil || ce != nil || !s.After(c) {
		return start, clamped
	}
	span := time.Duration(0)
	if ee == nil && !e.Before(s) {
		span = e.Sub(s)
	}
	return c.Add(-span).Format(dateLayout), clamped
}
func InWindow(pub *time.Time, start, end, now time.Time) bool {
	if pub == nil {
		return !end.Before(now.UTC().Add(-24 * time.Hour))
	}
	return !pub.Before(start) && pub.Before(end.AddDate(0, 0, 1))
}
func CoverageGap(dates []time.Time, start, end, source, subject string, now time.Time) string {
	oldest := now.UTC()
	for _, d := range dates {
		if d.Before(oldest) {
			oldest = d.UTC()
		}
	}
	reason := ""
	if end > now.UTC().Format(dateLayout) {
		reason = "the window extends past today"
	} else if oldest.Format(dateLayout) > start {
		reason = "it only serves recent items (coverage starts " + oldest.Format(dateLayout) + ")"
	}
	if reason == "" {
		return ""
	}
	return fmt.Sprintf("<%s unavailable for %s..%s: %s, so this is not an absence of %s>", source, start, end, reason, subject)
}
func WithholdProfile(date, label, today string) string {
	if date == "" || date >= today {
		return ""
	}
	return fmt.Sprintf("# Company Fundamentals for %s\n# Point-in-time as of: %s\n\nProfile fundamentals are withheld for this date. This vendor serves only present-day values (%s) with no historical vintage: market cap, valuation multiples, the 52-week range and TTM income move with today's quote, and even the name, sector and industry reflect today rather than %s (companies rename and get reclassified). Serving them would put post-decision information into a %s analysis. Point-in-time fundamentals for %s are available from the balance sheet, income statement, and cash flow tools.", label, date, today, date, date, date)
}
func (s *Service) CryptoBase(raw string) string {
	compact := strings.ReplaceAll(strings.TrimRight(strings.ToUpper(strings.TrimSpace(raw)), "+"), "-", "")
	for _, q := range []string{"USDT", "USDC", "USD"} {
		if strings.HasSuffix(compact, q) {
			base := strings.TrimSuffix(compact, q)
			if slices.Contains(s.Meta.Crypto, base) {
				return base
			}
			return ""
		}
	}
	return ""
}
func (s *Service) Normalize(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return raw
	}
	v := strings.TrimRight(strings.ToUpper(strings.TrimSpace(raw)), "+")
	if alias, ok := s.Meta.Aliases[v]; ok {
		return alias
	}
	if b := s.CryptoBase(v); b != "" {
		return b + "-USD"
	}
	if len(v) == 6 && slices.Contains(s.Meta.Forex, v[:3]) && slices.Contains(s.Meta.Forex, v[3:]) {
		return v + "=X"
	}
	if m := regexp.MustCompile(`^(\d{1,5})\.HK$`).FindStringSubmatch(v); m != nil {
		n, _ := strconv.Atoi(m[1])
		return fmt.Sprintf("%04d.HK", n)
	}
	if regexp.MustCompile(`^\d{6}\.SH$`).MatchString(v) {
		return strings.TrimSuffix(v, ".SH") + ".SS"
	}
	return v
}
