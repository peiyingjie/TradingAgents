package portfolio

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
)

type Position struct {
	Ticker       string   `json:"ticker"`
	Quantity     float64  `json:"quantity"`
	AveragePrice *float64 `json:"average_price"`
}
type Portfolio struct {
	Cash      *float64   `json:"cash"`
	Currency  *string    `json:"currency"`
	Positions []Position `json:"positions"`
}

func objectFields(b []byte) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, fmt.Errorf("expected an object")
	}
	return fields, nil
}
func numericField(b json.RawMessage, optional bool) (*float64, error) {
	if len(b) == 0 || bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		if optional {
			return nil, nil
		}
		return nil, fmt.Errorf("number is required")
	}
	var value any
	if err := json.Unmarshal(b, &value); err != nil {
		return nil, err
	}
	var number float64
	switch v := value.(type) {
	case float64:
		number = v
	case bool:
		if v {
			number = 1
		}
	case string:
		var err error
		number, err = strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("expected a number")
	}
	return &number, nil
}
func (p *Position) UnmarshalJSON(b []byte) error {
	fields, err := objectFields(b)
	if err != nil {
		return err
	}
	if len(fields["ticker"]) == 0 || bytes.Equal(bytes.TrimSpace(fields["ticker"]), []byte("null")) {
		return fmt.Errorf("position requires ticker")
	}
	var value Position
	if err = json.Unmarshal(fields["ticker"], &value.Ticker); err != nil {
		return err
	}
	quantity, err := numericField(fields["quantity"], false)
	if err != nil {
		return fmt.Errorf("position quantity: %w", err)
	}
	value.Quantity = *quantity
	value.AveragePrice, err = numericField(fields["average_price"], true)
	if err != nil {
		return fmt.Errorf("position average_price: %w", err)
	}
	*p = value
	return nil
}
func (p *Portfolio) UnmarshalJSON(b []byte) error {
	fields, err := objectFields(b)
	if err != nil {
		return err
	}
	value := Portfolio{Positions: []Position{}}
	value.Cash, err = numericField(fields["cash"], true)
	if err != nil {
		return fmt.Errorf("portfolio cash: %w", err)
	}
	if raw, ok := fields["currency"]; ok {
		if err = json.Unmarshal(raw, &value.Currency); err != nil {
			return err
		}
	}
	if raw, ok := fields["positions"]; ok {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("positions must be a list")
		}
		if err = json.Unmarshal(raw, &value.Positions); err != nil {
			return err
		}
	}
	*p = value
	return nil
}

func Load(path string) (*Portfolio, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, fmt.Errorf("portfolio file %s is not usable: %w", path, e)
	}
	p := &Portfolio{Positions: []Position{}}
	if e = json.Unmarshal(b, p); e != nil {
		return nil, fmt.Errorf("portfolio file %s is not usable: %w", path, e)
	}
	return p, nil
}
func grouped(n float64, precision int, format byte) string {
	if math.IsNaN(n) {
		return "nan"
	}
	if math.IsInf(n, 1) {
		return "inf"
	}
	if math.IsInf(n, -1) {
		return "-inf"
	}
	s := strconv.FormatFloat(n, format, precision, 64)
	if strings.ContainsAny(s, "eE") {
		return s
	}
	pieces := strings.SplitN(s, ".", 2)
	whole := pieces[0]
	for i := len(whole) - 3; i > 0; i -= 3 {
		if whole[i-1] == '-' {
			break
		}
		whole = whole[:i] + "," + whole[i:]
	}
	if len(pieces) > 1 {
		whole += "." + pieces[1]
	}
	return whole
}
func (p *Portfolio) Render(ticker string) string {
	symbol := strings.ToUpper(strings.TrimSpace(ticker))
	held := -1
	for i, pos := range p.Positions {
		if strings.EqualFold(pos.Ticker, symbol) {
			held = i
			break
		}
	}
	lines := []string{}
	if held < 0 {
		lines = append(lines, "- No current position in "+symbol)
	} else {
		pos := p.Positions[held]
		price := ""
		if pos.AveragePrice != nil {
			price = ", average price " + grouped(*pos.AveragePrice, 2, 'f')
		}
		lines = append(lines, fmt.Sprintf("- Current position in %s: %s units%s", symbol, grouped(pos.Quantity, 4, 'g'), price))
	}
	if p.Cash != nil {
		currency := ""
		if p.Currency != nil && *p.Currency != "" {
			currency = " " + *p.Currency
		}
		lines = append(lines, "- Cash available: "+grouped(*p.Cash, 2, 'f')+currency)
	}
	others := []string{}
	for i, pos := range p.Positions {
		if i != held {
			others = append(others, strings.ToUpper(pos.Ticker)+" "+grouped(pos.Quantity, 4, 'g'))
		}
	}
	if len(others) > 0 {
		lines = append(lines, "- Other positions: "+strings.Join(others, ", "))
	}
	return "Portfolio at the analysis date:\n" + strings.Join(lines, "\n")
}

type fingerprintNumber float64

func (v fingerprintNumber) MarshalJSON() ([]byte, error) {
	n := float64(v)
	if math.IsNaN(n) || math.IsInf(n, 0) {
		return []byte("null"), nil
	}
	format := byte('f')
	abs := math.Abs(n)
	if abs != 0 && (abs < 1e-5 || abs >= 1e16) {
		format = 'e'
	}
	s := strconv.FormatFloat(n, format, -1, 64)
	if i := strings.IndexByte(s, 'e'); i >= 0 {
		exponent, err := strconv.Atoi(s[i+1:])
		if err != nil {
			return nil, err
		}
		s = s[:i] + fmt.Sprintf("e%+d", exponent)
	} else if !strings.Contains(s, ".") {
		s += ".0"
	}
	return []byte(s), nil
}
func (p *Portfolio) Fingerprint() (string, error) {
	// Pydantic serializes integral float fields with a decimal suffix. Keep
	// that representation because existing checkpoint IDs hash these bytes.
	type position struct {
		Ticker       string             `json:"ticker"`
		Quantity     fingerprintNumber  `json:"quantity"`
		AveragePrice *fingerprintNumber `json:"average_price"`
	}
	optional := func(v *float64) *fingerprintNumber {
		if v == nil {
			return nil
		}
		n := fingerprintNumber(*v)
		return &n
	}
	wire := struct {
		Cash      *fingerprintNumber `json:"cash"`
		Currency  *string            `json:"currency"`
		Positions []position         `json:"positions"`
	}{optional(p.Cash), p.Currency, make([]position, 0, len(p.Positions))}
	for _, pos := range p.Positions {
		wire.Positions = append(wire.Positions, position{pos.Ticker, fingerprintNumber(pos.Quantity), optional(pos.AveragePrice)})
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	e := enc.Encode(wire)
	if e != nil {
		return "", e
	}
	b := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:12], nil
}
