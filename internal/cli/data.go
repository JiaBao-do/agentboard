package cli

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/JiaBao-do/agentboard"
)

// export writes the whole board as indented, versioned JSON. Online (the
// default) it asks the running server; with -data it reads the data
// directory directly, which is refused while a server holds the lock.
func (a *app) export(args []string, dump bool) error {
	name := "export"
	if dump {
		name = "dump"
	}
	fs := a.newFlags(name)
	c := a.addClientFlags(fs)
	var (
		data        = fs.String("data", "", "read this data `directory` directly (offline) instead of asking a server")
		out         = fs.String("o", "", "write to this `file` instead of stdout")
		gz          = fs.Bool("gzip", false, "gzip the output")
		withArchive = fs.Bool("with-archive", false, "offline: include archived activity entries")
	)
	if _, err := parse(fs, args); err != nil {
		return err
	}
	var st *agentboard.State
	if *data != "" {
		lock, err := agentboard.AcquireLock(*data)
		if err != nil {
			return offlineRefusal(err)
		}
		defer func() { _ = lock.Release() }()
		st, err = agentboard.NewFileStore(filepath.Join(*data, boardFile)).Load()
		if err != nil {
			return err
		}
		if *withArchive {
			arch, err := agentboard.ReadArchive(filepath.Join(*data, "archive"))
			if err != nil {
				return err
			}
			st.Activity = agentboard.MergeActivity(arch, st.Activity)
		}
	} else {
		if *withArchive {
			return usagef("-with-archive needs -data (archives live in the data directory)")
		}
		var err error
		if st, err = c.client().Export(a.ctx); err != nil {
			return err
		}
	}
	var buf bytes.Buffer
	if err := agentboard.EncodeState(&buf, st); err != nil {
		return err
	}
	payload := buf.Bytes()
	if *gz {
		var zb bytes.Buffer
		zw := gzip.NewWriter(&zb)
		if _, err := zw.Write(payload); err != nil {
			return err
		}
		if err := zw.Close(); err != nil {
			return err
		}
		payload = zb.Bytes()
	}
	if *out == "" {
		_, err := a.out.Write(payload)
		return err
	}
	if err := os.WriteFile(*out, payload, 0o600); err != nil {
		return err
	}
	fmt.Fprintf(a.errw, "wrote %s (%d bytes, %d tasks)\n", *out, len(payload), len(st.Tasks))
	return nil
}

// offlineRefusal turns a lock error into advice about using the API instead.
func offlineRefusal(err error) error {
	if errors.Is(err, agentboard.ErrLocked) {
		return fmt.Errorf("%w\nthe server owns this data directory; talk to it instead: agentboard export -url http://HOST:PORT (import needs the server stopped first)", err)
	}
	return err
}

// importCmd replaces the board in a data directory from an export (plain or
// gzip JSON) or from another data file. The server must not be running. The
// previous data file is kept as board.json.before-import-<time>.
func (a *app) importCmd(args []string) error {
	fs := a.newFlags("import")
	data := fs.String("data", a.getenv("AGENTBOARD_DATA", defaultData), "data `directory` to import into [AGENTBOARD_DATA]")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("usage: agentboard import FILE [-data DIR]")
	}
	raw, err := os.ReadFile(pos[0])
	if err != nil {
		return err
	}
	if len(raw) > 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return err
		}
		if raw, err = io.ReadAll(io.LimitReader(zr, 1<<30)); err != nil {
			return err
		}
	}
	st, _, err := agentboard.DecodeFile(raw) // plain JSON, or a data file; validated
	if err != nil {
		return fmt.Errorf("%s is not a valid board: %w", pos[0], err)
	}
	lock, err := agentboard.AcquireLock(*data)
	if err != nil {
		return offlineRefusal(err)
	}
	defer func() { _ = lock.Release() }()

	path := filepath.Join(*data, boardFile)
	if old, err := os.ReadFile(path); err == nil {
		keep := path + ".before-import-" + time.Now().UTC().Format("20060102T150405Z")
		if err := os.WriteFile(keep, old, 0o600); err != nil {
			return err
		}
		fmt.Fprintf(a.errw, "previous data kept as %s\n", filepath.Base(keep))
	}
	if err := agentboard.NewFileStore(path).Save(st); err != nil {
		return err
	}
	fmt.Fprintf(a.out, "imported %d tasks, %d agents, %d activity entries\n", len(st.Tasks), len(st.Agents), len(st.Activity))
	return nil
}
