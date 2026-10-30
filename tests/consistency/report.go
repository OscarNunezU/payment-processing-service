package main

import (
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"text/tabwriter"
	"time"
)

// report prints what was checked, and every violation, up to 20 per
// invariant.
func report(w io.Writer, s Snapshot, settled time.Duration, results []Result) {
	statuses := map[string]int{}
	for _, p := range s.Payments {
		statuses[p.Status]++
	}
	var parts []string
	for _, status := range slices.Sorted(maps.Keys(statuses)) {
		parts = append(parts, fmt.Sprintf("%d %s", statuses[status], status))
	}
	fmt.Fprintf(w, "%d payments (%s), %d keys in the acquirer's book, %d webhook events. Settled after %s.\n\n",
		len(s.Payments), strings.Join(parts, ", "), len(s.Book), len(s.Deliveries), settled.Round(time.Second))

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "Invariant\tRule\tChecked\tViolations")
	for _, r := range results {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\n", r.Invariant, r.Rule, r.Checked, len(r.Violations))
	}
	_ = tw.Flush()

	for _, r := range results {
		v := sorted(r.Violations)
		if len(v) == 0 {
			continue
		}
		fmt.Fprintf(w, "\n%s, %s:\n", r.Invariant, r.Rule)
		for i, line := range v {
			if i == 20 {
				fmt.Fprintf(w, "  … and %d more\n", len(v)-20)
				break
			}
			fmt.Fprintf(w, "  %s\n", line)
		}
	}
	if n := Violations(results); n > 0 {
		fmt.Fprintf(w, "\n%d violations.\n", n)
		return
	}
	fmt.Fprintln(w, "\nAll invariants hold.")
}
