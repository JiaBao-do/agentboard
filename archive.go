package agentboard

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/JiaBao-do/agentboard/model"
)

// Activity archive: when the live activity log grows past Options.MaxActivity
// the oldest entries move to <dir>/activity-YYYYMM.jsonl.gz (month of the
// entry, UTC), keeping the main data file small. Each archive is a series of
// gzip members (gzip members concatenate, so appending is a plain append);
// every member starts with a header line {"agentboard_archive":1} followed
// by one Activity JSON object per line. Archiving is at-least-once: after a
// crash the same entry can appear twice, so readers dedupe by ID
// (ReadArchive does).
const archiveVersion = 1

type archiveHeader struct {
	Version int `json:"agentboard_archive"`
}

// ArchiveFileName returns the archive file name for the month of an entry.
func archiveFileName(a model.Activity) string {
	return "activity-" + a.Time.UTC().Format("200601") + ".jsonl.gz"
}

// appendArchive writes entries to their monthly files as new gzip members.
func appendArchive(dir string, entries []model.Activity) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	byFile := map[string][]model.Activity{}
	var names []string
	for _, e := range entries {
		n := archiveFileName(e)
		if _, ok := byFile[n]; !ok {
			names = append(names, n)
		}
		byFile[n] = append(byFile[n], e)
	}
	sort.Strings(names)
	for _, name := range names {
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		zw := gzip.NewWriter(f)
		enc := json.NewEncoder(zw)
		werr := enc.Encode(archiveHeader{Version: archiveVersion})
		for _, e := range byFile[name] {
			if werr != nil {
				break
			}
			werr = enc.Encode(e)
		}
		if cerr := zw.Close(); werr == nil {
			werr = cerr
		}
		if serr := f.Sync(); werr == nil {
			werr = serr
		}
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return fmt.Errorf("agentboard: writing archive %s: %w", name, werr)
		}
	}
	return nil
}

// ReadArchive returns every archived activity entry under dir in ID order,
// with duplicates removed. A missing directory is an empty archive. Archives
// written by a newer version are refused.
func ReadArchive(dir string) ([]model.Activity, error) {
	files, err := filepath.Glob(filepath.Join(dir, "activity-*.jsonl.gz"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	seen := map[int64]bool{}
	var out []model.Activity
	for _, path := range files {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		zr, err := gzip.NewReader(f)
		if err != nil {
			f.Close()
			return nil, fmt.Errorf("%w: archive %s: %w", ErrCorrupt, filepath.Base(path), err)
		}
		sc := bufio.NewScanner(zr)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			line := sc.Bytes()
			if strings.HasPrefix(string(line), `{"agentboard_archive"`) {
				var h archiveHeader
				if err := json.Unmarshal(line, &h); err != nil || h.Version > archiveVersion {
					f.Close()
					return nil, fmt.Errorf("%w: archive %s has format version %d, this agentboard understands %d", ErrInvalid, filepath.Base(path), h.Version, archiveVersion)
				}
				continue
			}
			var a model.Activity
			if err := json.Unmarshal(line, &a); err != nil {
				f.Close()
				return nil, fmt.Errorf("%w: archive %s: %w", ErrCorrupt, filepath.Base(path), err)
			}
			if !seen[a.ID] {
				seen[a.ID] = true
				out = append(out, a)
			}
		}
		err = sc.Err()
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("%w: archive %s: %w", ErrCorrupt, filepath.Base(path), err)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// MergeActivity combines archived and live activity into one ID-ordered
// list without duplicates. Export with archives uses it.
func MergeActivity(archived, live []model.Activity) []model.Activity {
	seen := make(map[int64]bool, len(archived)+len(live))
	out := make([]model.Activity, 0, len(archived)+len(live))
	for _, list := range [][]model.Activity{archived, live} {
		for _, a := range list {
			if !seen[a.ID] {
				seen[a.ID] = true
				out = append(out, a)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
