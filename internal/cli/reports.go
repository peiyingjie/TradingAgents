package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"tradingagents/internal/reporting"
	"tradingagents/internal/state"
)

func (t terminal) offerReport(s state.State, ticker, resultsDir string, now time.Time) error {
	choice, err := t.ask("Save report?", "Y")
	if err != nil {
		return err
	}
	if yes(choice) {
		def := filepath.Join(resultsDir, "reports", ticker+"_"+now.Format("20060102_150405"))
		path, err := t.ask("Save path (press Enter for default)", def)
		if err != nil {
			return err
		}
		report, err := reporting.Write(s, ticker, path, now)
		if err != nil {
			fmt.Fprintf(t.Out, "Error saving report: %v\n", err)
		} else {
			fmt.Fprintf(t.Out, "Report: %s\n", report)
		}
	}
	choice, err = t.ask("Display full report on screen?", "Y")
	if err != nil {
		return err
	}
	if yes(choice) {
		for _, section := range reportSections(s) {
			if section.text != "" {
				fmt.Fprintf(t.Out, "\n## %s\n\n%s\n", section.title, section.text)
			}
		}
	}
	return nil
}
func yes(s string) bool {
	s = strings.ToUpper(strings.TrimSpace(s))
	return s == "Y" || s == "YES" || s == ""
}
