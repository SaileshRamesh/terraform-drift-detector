package report

import (
	"bufio"
	"fmt"
	"io"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/saileshramesh/terraform-drift-detector/internal/model"
)

type ConsoleOptions struct {
	Color bool
	// ShowSkipped lists every unsupported resource instead of a per-type count.
	ShowSkipped bool
}

func WriteConsole(w io.Writer, r *model.Report, opts ConsoleOptions) error {
	p := painter(opts.Color)
	bw := bufio.NewWriter(w)

	fmt.Fprintf(bw, "%s  %s\n", p.bold("Terraform Drift Report"),
		p.dim(fmt.Sprintf("scan %s · %s", r.ScanID, time.Duration(r.DurationMs)*time.Millisecond)))
	for _, s := range r.Sources {
		fmt.Fprintf(bw, "  %s %s\n", p.dim("state:"), s)
	}

	s := r.Summary
	fmt.Fprintf(bw, "\n  %d resources · %s · %s · %s · %s · %s · %s · %s\n\n",
		s.Resources,
		p.green(fmt.Sprintf("%d in sync", s.InSync)),
		p.red(fmt.Sprintf("%d deleted", s.Deleted)),
		p.yellow(fmt.Sprintf("%d modified", s.Modified)),
		p.cyan(fmt.Sprintf("%d tags changed", s.TagsChanged)),
		p.blue(fmt.Sprintf("%d unmanaged", s.Unmanaged)),
		p.magenta(fmt.Sprintf("%d errors", s.Errors)),
		p.dim(fmt.Sprintf("%d skipped", s.Skipped)))

	skipped := map[string]int{}
	for _, d := range r.Drifts {
		if d.Kind == model.Skipped {
			skipped[d.Type]++
			if !opts.ShowSkipped {
				continue
			}
		}
		target := d.Address
		if target == "" {
			target = d.Type // unmanaged resources have no Terraform address
		}
		fmt.Fprintf(bw, "%s %s %s  %s\n",
			p.kind(d.Kind, fmt.Sprintf("%-12s", d.Kind)),
			fmt.Sprintf("%-6s", d.Severity),
			p.bold(target),
			p.dim(strings.TrimSpace(d.ID+" "+d.Region)))
		if d.Message != "" {
			fmt.Fprintf(bw, "    %s\n", p.dim(d.Message))
		}
		for _, c := range d.Changes {
			writeChange(bw, p, c)
		}
	}

	switch {
	case r.HasDrift():
		fmt.Fprintln(bw, p.dim("\n(values shown as state → cloud)"))
	case s.Errors == 0:
		fmt.Fprintln(bw, p.green("✔ No drift detected."))
	}
	if s.Errors > 0 {
		fmt.Fprintln(bw, p.magenta(fmt.Sprintf("⚠ %d resources could not be checked; drift status for them is unknown.", s.Errors)))
	}
	if len(skipped) > 0 && !opts.ShowSkipped {
		types := make([]string, 0, len(skipped))
		for t := range skipped {
			types = append(types, t)
		}
		sort.Strings(types)
		parts := make([]string, len(types))
		for i, t := range types {
			parts[i] = fmt.Sprintf("%s ×%d", t, skipped[t])
		}
		fmt.Fprintf(bw, "%s %s\n", p.dim("Not checked (no handler yet):"), strings.Join(parts, ", "))
	}
	return bw.Flush()
}

// writeChange shows list attributes (rules, security groups) as added and
// removed elements, and everything else as old → new.
func writeChange(w io.Writer, p painter, c model.Change) {
	e, eok := c.Expected.([]string)
	a, aok := c.Actual.([]string)
	if eok || aok {
		fmt.Fprintf(w, "    %s\n", c.Path)
		for _, v := range e {
			if !slices.Contains(a, v) {
				fmt.Fprintf(w, "      %s\n", p.red("- "+v))
			}
		}
		for _, v := range a {
			if !slices.Contains(e, v) {
				fmt.Fprintf(w, "      %s\n", p.green("+ "+v))
			}
		}
		return
	}
	fmt.Fprintf(w, "    %s: %s → %s\n", c.Path, formatValue(c.Expected), formatValue(c.Actual))
}

func formatValue(v any) string {
	switch t := v.(type) {
	case nil:
		return "(absent)"
	case string:
		return strconv.Quote(t)
	}
	return fmt.Sprint(v)
}

type painter bool

func (p painter) wrap(code, s string) string {
	if !p {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (p painter) bold(s string) string    { return p.wrap("1", s) }
func (p painter) dim(s string) string     { return p.wrap("2", s) }
func (p painter) red(s string) string     { return p.wrap("31", s) }
func (p painter) green(s string) string   { return p.wrap("32", s) }
func (p painter) yellow(s string) string  { return p.wrap("33", s) }
func (p painter) blue(s string) string    { return p.wrap("34", s) }
func (p painter) magenta(s string) string { return p.wrap("35", s) }
func (p painter) cyan(s string) string    { return p.wrap("36", s) }

func (p painter) kind(k model.DriftKind, s string) string {
	switch k {
	case model.Deleted:
		return p.red(s)
	case model.Modified:
		return p.yellow(s)
	case model.TagsChanged:
		return p.cyan(s)
	case model.Unmanaged:
		return p.blue(s)
	case model.Error:
		return p.magenta(s)
	}
	return p.dim(s)
}
