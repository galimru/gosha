package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// followPoll is how often logs -f checks the file for new lines.
var followPoll = 500 * time.Millisecond

func (a *app) logsCmd() *cobra.Command {
	var follow bool
	var project string
	var lines int
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Print Gosha's log",
		Long: "Print the last lines of logs/gosha.log, one line per record. --project keeps only\n" +
			"one project's lines, and -f keeps printing new lines as they're written, across rotation.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := a.home.LogFile()
			f, err := os.Open(path)
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("no log at %s yet", path)
			}
			if err != nil {
				return err
			}
			defer func() { f.Close() }()
			out := cmd.OutOrStdout()
			keep := func(line string) (string, bool) { return formatLogLine(line, project) }
			r := bufio.NewReader(f)
			var tail []string
			for {
				line, err := r.ReadString('\n')
				if s, ok := keep(line); ok && strings.HasSuffix(line, "\n") {
					tail = append(tail, s)
					if lines > 0 && len(tail) > lines {
						tail = tail[1:]
					}
				}
				if err != nil {
					break
				}
			}
			for _, s := range tail {
				fmt.Fprintln(out, s)
			}
			if !follow {
				return nil
			}
			// What's left in r is a partial line; follow from the end of
			// the last full one.
			pos, err := f.Seek(0, io.SeekCurrent)
			if err != nil {
				return err
			}
			pos -= int64(r.Buffered())
			var partial string
			ctx := cmd.Context()
			for {
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(followPoll):
				}
				// Rotated (renamed away) or truncated: start on the new file.
				if fi, err := os.Stat(path); err == nil {
					cur, _ := f.Stat()
					if !os.SameFile(fi, cur) || fi.Size() < pos {
						nf, err := os.Open(path)
						if err != nil {
							continue
						}
						f.Close()
						f, pos, partial = nf, 0, ""
					}
				}
				if _, err := f.Seek(pos, io.SeekStart); err != nil {
					return err
				}
				data, err := io.ReadAll(f)
				if err != nil {
					return err
				}
				pos += int64(len(data))
				chunk := partial + string(data)
				parts := strings.Split(chunk, "\n")
				partial = parts[len(parts)-1]
				for _, line := range parts[:len(parts)-1] {
					if s, ok := keep(line); ok {
						fmt.Fprintln(out, s)
					}
				}
			}
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing new lines")
	cmd.Flags().StringVar(&project, "project", "", "only this project's lines")
	cmd.Flags().IntVarP(&lines, "lines", "n", 100, "how many lines to print first (0 = all)")
	return cmd
}

// formatLogLine renders one JSON log record like the stderr text format
// (time, level, message, then the other attributes), or reports false for
// a line of another project, or a line that isn't a record.
func formatLogLine(line, project string) (string, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", false
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		return line, project == ""
	}
	if project != "" && rec["project"] != project {
		return "", false
	}
	var b strings.Builder
	if ts, ok := rec["time"].(string); ok {
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			ts = t.Local().Format(time.DateTime)
		}
		b.WriteString(ts + " ")
	}
	fmt.Fprintf(&b, "%-5v %v", rec["level"], rec["msg"])
	keys := make([]string, 0, len(rec))
	for k := range rec {
		if k != "time" && k != "level" && k != "msg" {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	// project first, the rest alphabetically.
	if i := slices.Index(keys, "project"); i > 0 {
		keys = append([]string{"project"}, slices.Delete(keys, i, i+1)...)
	}
	for _, k := range keys {
		v := fmt.Sprint(rec[k])
		if strings.ContainsAny(v, " \t\n\"") {
			v = fmt.Sprintf("%q", v)
		}
		fmt.Fprintf(&b, " %s=%s", k, v)
	}
	return b.String(), true
}
