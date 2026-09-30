package dataflows

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Article struct {
	Title, Summary, Publisher, Link string
	Published                       *time.Time
}

func timestamp(v any) *time.Time {
	if n, ok := v.(float64); ok {
		t := time.Unix(int64(n), 0).UTC()
		return &t
	}
	if s, ok := v.(string); ok {
		for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
			if t, e := time.Parse(layout, s); e == nil {
				return &t
			}
		}
	}
	return nil
}
func article(v any) Article {
	p := obj(v)
	a := Article{Title: "No title", Publisher: "Unknown"}
	if nested, ok := p["content"]; ok {
		p = obj(nested)
		a.Publisher = text(obj(p["provider"])["displayName"])
		a.Published = timestamp(p["pubDate"])
		link := obj(p["canonicalUrl"])
		if len(link) == 0 {
			link = obj(p["clickThroughUrl"])
		}
		a.Link = text(link["url"])
	} else {
		a.Publisher = text(p["publisher"])
		a.Published = timestamp(p["providerPublishTime"])
		a.Link = text(p["link"])
	}
	if title, ok := p["title"]; ok {
		a.Title = text(title)
	}
	if a.Publisher == "" {
		a.Publisher = "Unknown"
	}
	a.Summary = text(p["summary"])
	return a
}
func renderArticles(articles []Article) string {
	var out strings.Builder
	for _, a := range articles {
		fmt.Fprintf(&out, "### %s (source: %s)\n", a.Title, a.Publisher)
		if a.Summary != "" {
			out.WriteString(a.Summary + "\n")
		}
		if a.Link != "" {
			out.WriteString("Link: " + a.Link + "\n")
		}
		out.WriteString("\n")
	}
	return out.String()
}
func (s *Service) yahooNews(ctx context.Context, r Request) (string, error) {
	canonical := s.Normalize(r.Ticker)
	resolved := ""
	if canonical != r.Ticker {
		resolved = " (resolved to " + canonical + ")"
	}
	start, e := ParseDate(r.StartDate)
	if e != nil {
		return "", e
	}
	end, e := ParseDate(r.EndDate)
	if e != nil {
		return "", e
	}
	endpoint := s.Endpoints["yahoo_site"]
	if endpoint == "" {
		endpoint = "https://finance.yahoo.com"
	}
	raw, e := s.request(ctx, "POST", endpoint+"/xhr/ncp", url.Values{"queryRef": {"latestNews"}, "serviceKey": {"ncp_fin"}}, map[string]any{"serviceConfig": map[string]any{"snippetCount": s.Config.NewsArticleLimit, "s": []string{canonical}}}, nil, 30*time.Second)
	if e != nil {
		return "", &NoDataError{r.Ticker, r.Ticker, "news unavailable: " + e.Error()}
	}
	p, e := jsonObject(raw)
	if e != nil {
		return "", e
	}
	kept := []Article{}
	dates := []time.Time{}
	for _, v := range arr(obj(obj(p["data"])["tickerStream"])["stream"]) {
		if len(arr(obj(v)["ad"])) > 0 {
			continue
		}
		a := article(v)
		if a.Published != nil {
			dates = append(dates, *a.Published)
		}
		if InWindow(a.Published, start, end, s.Now()) {
			kept = append(kept, a)
		}
	}
	if len(kept) == 0 {
		gap := CoverageGap(dates, r.StartDate, r.EndDate, "Yahoo Finance news", "news for "+r.Ticker+resolved, s.Now())
		if gap != "" {
			return gap, nil
		}
		return fmt.Sprintf("No news found for %s%s between %s and %s", r.Ticker, resolved, r.StartDate, r.EndDate), nil
	}
	return fmt.Sprintf("## %s%s News, from %s to %s:\n\n%s", r.Ticker, resolved, r.StartDate, r.EndDate, renderArticles(kept)), nil
}
func (s *Service) yahooGlobalNews(ctx context.Context, r Request) (string, error) {
	end, e := ParseDate(r.CurrentDate)
	if e != nil {
		return "", e
	}
	start := end.AddDate(0, 0, -value(r.LookBackDays, s.Config.GlobalNewsLookbackDays))
	limit := value(r.Limit, s.Config.GlobalNewsArticleLimit)
	kept := []Article{}
	seen := map[string]bool{}
	for _, query := range s.Config.GlobalNewsQueries {
		b, e := s.yahooGet(ctx, "/v1/finance/search", url.Values{"q": {query}, "newsCount": {strconv.Itoa(limit)}, "quotesCount": {"8"}, "enableFuzzyQuery": {"true"}, "quotesQueryId": {"tss_match_phrase_query"}, "newsQueryId": {"news_cie_vespa"}, "listsCount": {"8"}, "enableCb": {"true"}, "enableNavLinks": {"true"}, "enableResearchReports": {"true"}, "enableCulturalAssets": {"false"}, "enableLogoUrl": {"false"}, "enableLists": {"true"}})
		if e != nil {
			return "", &NoDataError{"global news", "global news", "unavailable: " + e.Error()}
		}
		p, e := jsonObject(b)
		if e != nil {
			return "", e
		}
		for _, v := range arr(p["news"]) {
			a := article(v)
			if InWindow(a.Published, start, end, s.Now()) && a.Title != "" && !seen[a.Title] {
				seen[a.Title] = true
				kept = append(kept, a)
			}
		}
		if len(kept) >= limit {
			break
		}
	}
	if len(kept) == 0 {
		gap := CoverageGap(nil, start.Format(dateLayout), r.CurrentDate, "Yahoo Finance global news", "market news", s.Now())
		if gap != "" {
			return gap, nil
		}
		return fmt.Sprintf("No global news found between %s and %s", start.Format(dateLayout), r.CurrentDate), nil
	}
	return fmt.Sprintf("## Global Market News, from %s to %s:\n\n%s", start.Format(dateLayout), r.CurrentDate, renderArticles(kept[:max(0, min(len(kept), limit))])), nil
}
func shorten(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
func (s *Service) StockTwits(ctx context.Context, ticker, start, end string) (string, error) {
	symbol := strings.ToUpper(strings.TrimSpace(ticker))
	if base := s.CryptoBase(ticker); base != "" {
		symbol = base + ".X"
	}
	raw, e := s.request(ctx, "GET", s.Endpoints["stocktwits"]+"/api/2/streams/symbol/"+url.PathEscape(symbol)+".json", nil, nil, map[string]string{"User-Agent": "tradingagents/0.2 (+https://github.com/TauricResearch/TradingAgents)", "Accept": "application/json"}, 10*time.Second)
	if e != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "<stocktwits unavailable: HTTPError>", nil
	}
	p, e := jsonObject(raw)
	if e != nil {
		return "<stocktwits unavailable: JSONDecodeError>", nil
	}
	a, e := ParseDate(start)
	if e != nil {
		return "", e
	}
	b, e := ParseDate(end)
	if e != nil {
		return "", e
	}
	dates := []time.Time{}
	kept := []map[string]any{}
	for _, v := range arr(p["messages"]) {
		m := obj(v)
		t := timestamp(m["created_at"])
		if t != nil {
			dates = append(dates, *t)
		}
		if InWindow(t, a, b, s.Now()) {
			kept = append(kept, m)
		}
	}
	if len(kept) == 0 {
		gap := CoverageGap(dates, start, end, "StockTwits", "messages about $"+strings.ToUpper(ticker), s.Now())
		if gap != "" {
			return gap, nil
		}
		return fmt.Sprintf("<no StockTwits messages for $%s within %s..%s>", strings.ToUpper(ticker), start, end), nil
	}
	lines := []string{}
	bull, bear, unlabeled := 0, 0, 0
	for _, m := range kept[:min(30, len(kept))] {
		tag := text(obj(obj(m["entities"])["sentiment"])["basic"])
		switch tag {
		case "Bullish":
			bull++
		case "Bearish":
			bear++
		default:
			unlabeled++
			tag = "no-label"
		}
		user := text(obj(m["user"])["username"])
		if user == "" {
			user = "?"
		}
		body := shorten(strings.TrimSpace(strings.ReplaceAll(text(m["body"]), "\n", " ")), 280)
		lines = append(lines, fmt.Sprintf("[%s · @%s · %s] %s", text(m["created_at"]), user, tag, body))
	}
	total := bull + bear + unlabeled
	return fmt.Sprintf("Bullish: %d (%.0f%%) · Bearish: %d (%.0f%%) · Unlabeled: %d · Total: %d most-recent messages\n\n%s", bull, math.RoundToEven(100*float64(bull)/float64(total)), bear, math.RoundToEven(100*float64(bear)/float64(total)), unlabeled, total, strings.Join(lines, "\n")), nil
}
func (s *Service) Reddit(ctx context.Context, ticker, start, end string) (string, error) {
	if base := s.CryptoBase(ticker); base != "" {
		ticker = base
	}
	label := "r/wallstreetbets, r/stocks, r/investing"
	q := url.Values{"q": {ticker}, "restrict_sr": {"on"}, "sort": {"new"}, "t": {"week"}, "limit": {"100"}}
	var raw []byte
	var e error
	for attempt := 0; attempt < 2; attempt++ {
		raw, e = s.request(ctx, "GET", s.Endpoints["reddit"]+"/r/wallstreetbets+stocks+investing/search.rss", q, nil, map[string]string{"User-Agent": "tradingagents/0.2 (+https://github.com/TauricResearch/TradingAgents)"}, 10*time.Second)
		if e == nil {
			break
		}
		var he *HTTPError
		if !errors.As(e, &he) || he.Status != 429 || attempt > 0 {
			break
		}
		delay := 60 * time.Second
		if sec, err := strconv.ParseFloat(he.RetryAfter, 64); err == nil {
			delay = time.Duration(sec * float64(time.Second))
		} else if when, err := time.Parse(time.RFC1123, he.RetryAfter); err == nil {
			delay = max(0, when.Sub(s.Now()))
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
	}
	if e != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "<Reddit unavailable: fetch failed (" + label + "); this is not an absence of discussion>", nil
	}
	if len(raw) > 5*1024*1024 {
		return "<Reddit unavailable: fetch failed (" + label + "); this is not an absence of discussion>", nil
	}
	var feed struct {
		Entries []struct {
			Title     string `xml:"title"`
			Content   string `xml:"content"`
			Published string `xml:"published"`
			Category  struct {
				Term  string `xml:"term,attr"`
				Label string `xml:"label,attr"`
			} `xml:"category"`
			Link struct {
				Href string `xml:"href,attr"`
			} `xml:"link"`
		} `xml:"entry"`
	}
	if e = xml.Unmarshal(raw, &feed); e != nil {
		return "<Reddit unavailable: fetch failed (" + label + "); this is not an absence of discussion>", nil
	}
	a, e := ParseDate(start)
	if e != nil {
		return "", e
	}
	b, e := ParseDate(end)
	if e != nil {
		return "", e
	}
	type post struct{ Title, Body, Date string }
	group := map[string][]post{}
	order := []string{"wallstreetbets", "stocks", "investing"}
	dates := []time.Time{s.Now().AddDate(0, 0, -7)}
	kept := 0
	for _, entry := range feed.Entries {
		pub := timestamp(entry.Published)
		if pub != nil {
			dates = append(dates, *pub)
		}
		if !InWindow(pub, a, b, s.Now()) {
			continue
		}
		kept++
		sub := entry.Category.Term
		if sub == "" {
			sub = "unknown"
		}
		exists := false
		for _, k := range order {
			exists = exists || k == sub
		}
		if !exists {
			order = append(order, sub)
		}
		body := entry.Content
		if begin := strings.Index(body, "<!-- SC_OFF -->"); begin >= 0 {
			body = body[begin+len("<!-- SC_OFF -->"):]
			if stop := strings.Index(body, "<!-- SC_ON -->"); stop >= 0 {
				body = body[:stop]
			}
		}
		body = html.UnescapeString(regexp.MustCompile(`<[^>]+>`).ReplaceAllString(body, " "))
		body = strings.Join(strings.Fields(body), " ")
		date := "?"
		if pub != nil {
			date = pub.UTC().Format(dateLayout)
		}
		group[sub] = append(group[sub], post{strings.TrimSpace(strings.ReplaceAll(entry.Title, "\n", " ")), shorten(body, 240), date})
	}
	if kept == 0 {
		gap := CoverageGap(dates, start, end, "Reddit search", "discussion of "+strings.ToUpper(ticker), s.Now())
		if gap != "" {
			return gap, nil
		}
		return fmt.Sprintf("<no Reddit posts found mentioning %s across %s within %s..%s>", strings.ToUpper(ticker), label, start, end), nil
	}
	blocks := []string{}
	for _, sub := range order {
		posts := group[sub]
		if len(posts) == 0 {
			if len(feed.Entries) >= 100 {
				blocks = append(blocks, fmt.Sprintf("r/%s: <not among the newest 100 matches across %s>", sub, label))
			} else {
				blocks = append(blocks, fmt.Sprintf("r/%s: <no posts found mentioning %s>", sub, strings.ToUpper(ticker)))
			}
			continue
		}
		posts = posts[:min(5, len(posts))]
		lines := []string{fmt.Sprintf("r/%s — %d recent posts mentioning %s:", sub, len(posts), strings.ToUpper(ticker))}
		for _, p := range posts {
			line := fmt.Sprintf("  [%s] %s", p.Date, p.Title)
			if p.Body != "" {
				line += "\n    body excerpt: " + p.Body
			}
			lines = append(lines, line)
		}
		blocks = append(blocks, strings.Join(lines, "\n"))
	}
	return strings.Join(blocks, "\n\n"), nil
}
