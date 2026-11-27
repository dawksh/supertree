package terminal

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"golang.org/x/sys/unix"
)

type receipt struct {
	Launch      Launch   `json:"launch"`
	Fingerprint [32]byte `json:"fingerprint"`
	Info        Info     `json:"info"`
}
type journal struct {
	Version  int                `json:"version"`
	Epoch    string             `json:"epoch"`
	Receipts map[string]receipt `json:"receipts"`
}

type host struct {
	mu        sync.Mutex // journal/launch serialization; never used by PTY readers.
	dir       string
	journal   journal
	current   map[string]string // terminal ID -> most recent request ID
	processes map[string]*process
	stopping  bool
	workers   sync.WaitGroup
	// Private failure-injection seam: production always uses atomicJournal.
	save func(string, journal) error
}

func loadHost(dir string) (*host, error) {
	h := &host{dir: dir, current: map[string]string{}, processes: map[string]*process{}, save: atomicJournal}
	h.journal = journal{Version: 1, Epoch: token(), Receipts: map[string]receipt{}}
	f, err := openPrivate(filepath.Join(dir, "receipts.json"), unix.O_RDONLY)
	if err == nil {
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, MaxMessage+1))
		if err != nil {
			return nil, err
		}
		if len(b) > MaxMessage {
			return nil, failure("invalid_state", "terminal receipt journal exceeds bound")
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.DisallowUnknownFields()
		var old journal
		if dec.Decode(&old) != nil || dec.Decode(new(any)) != io.EOF || old.Version != 1 || !identifier(old.Epoch) || len(old.Receipts) > MaxReceipts || old.Receipts == nil {
			return nil, failure("invalid_state", "terminal receipt journal is invalid; preserve it for explicit recovery")
		}
		for id, r := range old.Receipts {
			if !validLaunch(r.Launch) || r.Launch.RequestID != id || r.Info.TerminalID != r.Launch.TerminalID || r.Info.Generation != r.Launch.Generation || r.Info.RequestID != id || r.Launch.Definition.Validate() != nil || fingerprint(r.Launch) != r.Fingerprint {
				return nil, failure("invalid_state", "terminal receipt identity/definition is invalid")
			}
			switch r.Info.State {
			case "running", "starting":
				r.Info.State = "lost"
				r.Info.Error = "previous host was lost; inspect before an explicit restart"
			case "exited", "failed-to-start", "lost":
			default:
				return nil, failure("invalid_state", "terminal receipt state is invalid")
			}
			r.Info.PID = 0 // never adopt/signal a historical PID.
			h.journal.Receipts[id] = r
			previous, ok := h.current[r.Info.TerminalID]
			if !ok || h.journal.Receipts[previous].Info.Generation < r.Info.Generation {
				h.current[r.Info.TerminalID] = id
			} else if h.journal.Receipts[previous].Info.Generation == r.Info.Generation {
				return nil, failure("invalid_state", "duplicate terminal generation in journal")
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if len(h.current) > MaxTerminals {
		return nil, failure("invalid_state", "terminal inventory exceeds namespace bound")
	}
	if err := h.save(dir, h.journal); err != nil {
		return nil, err
	}
	return h, nil
}

func atomicJournal(dir string, j journal) error {
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	if len(b) > MaxMessage {
		return failure("resource_limit", "terminal receipts exceed journal bound")
	}
	f, err := os.CreateTemp(dir, ".receipt-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	defer f.Close()
	if _, err = f.Write(b); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(name, filepath.Join(dir, "receipts.json")); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func fingerprint(l Launch) [32]byte { b, _ := json.Marshal(l); return sha256.Sum256(b) }
func validLaunch(l Launch) bool {
	return identifier(l.RequestID) && identifier(l.TerminalID) && l.Generation > 0
}
func (h *host) info(r receipt) Info {
	if p := h.processes[r.Info.TerminalID]; p != nil && h.current[r.Info.TerminalID] == r.Info.RequestID {
		return p.info(r.Info)
	}
	return r.Info
}
func (h *host) inventory() Status {
	result := Status{Version: ProtocolVersion, Namespace: namespace(h.dir), Epoch: h.journal.Epoch, PID: os.Getpid(), Capabilities: []string{"launch-receipts-v1", "screen-inspect-v1", "legacy-xterm-256color", "bounded-graphemes-256"}, Terminals: []Info{}, ReservedBytes: int64(len(h.processes)) * ReservationBytes}
	result.Directory = h.dir
	for _, id := range h.current {
		result.Terminals = append(result.Terminals, h.info(h.journal.Receipts[id]))
	}
	sort.Slice(result.Terminals, func(i, j int) bool { return result.Terminals[i].TerminalID < result.Terminals[j].TerminalID })
	return result
}

func (h *host) persist(r receipt) error {
	h.journal.Receipts[r.Launch.RequestID] = r
	if err := h.save(h.dir, h.journal); err != nil {
		return failure("receipt_uncertain", "could not persist terminal outcome; inspect inventory before retrying")
	}
	return nil
}

func (h *host) ensure(ctx context.Context, l Launch, restart bool) (Info, error) {
	if !validLaunch(l) {
		return Info{}, failure("invalid_arguments", "valid request ID, terminal ID and nonzero generation are required")
	}
	if err := l.Definition.Validate(); err != nil {
		return Info{}, err
	}
	if old, ok := h.journal.Receipts[l.RequestID]; ok {
		if old.Fingerprint != fingerprint(l) {
			return Info{}, failure("conflict", "request ID was already used with another launch")
		}
		return h.info(old), nil
	}
	if len(h.journal.Receipts) >= MaxReceipts {
		return Info{}, failure("resource_limit", "terminal receipt limit reached; use an explicitly new namespace")
	}
	var retired *process
	if previous, ok := h.current[l.TerminalID]; ok {
		r := h.journal.Receipts[previous]
		if !restart || r.Info.Generation == math.MaxInt64 || l.Generation != r.Info.Generation+1 {
			return Info{}, failure("conflict", "terminal already exists; restart requires its next generation")
		}
		// Restart must reuse a known definition. It is not an implicit stop or
		// an opportunity to change ownership, cwd, argv, or private environment.
		a, _ := json.Marshal(r.Launch.Definition)
		b, _ := json.Marshal(l.Definition)
		if !bytes.Equal(a, b) {
			return Info{}, failure("conflict", "restart must preserve the known launch definition")
		}
		if p := h.processes[l.TerminalID]; p != nil {
			members, err := p.members()
			if err != nil {
				return Info{}, failure("stop_failed", "cannot verify terminal session")
			}
			if len(members) > 0 {
				return Info{}, failure("conflict", "stop the existing terminal session before restarting")
			}
			if err := p.stop(false); err != nil {
				return Info{}, err
			}
			r.Info = p.info(r.Info)
			// The exit watcher may still be waiting for this lock. Preserve the
			// superseded receipt's final outcome before advancing current, so an
			// old request can never report an already-restarted process as live.
			if err := h.persist(r); err != nil {
				return Info{}, err
			}
			retired = p
		}
	} else if restart || l.Generation != 1 {
		return Info{}, failure("conflict", "new terminals start at generation 1")
	} else if len(h.current) >= MaxTerminals {
		return Info{}, failure("resource_limit", "terminal namespace reservation budget reached")
	}
	if err := ctx.Err(); err != nil {
		return Info{}, err
	}
	r := receipt{Launch: l, Fingerprint: fingerprint(l), Info: Info{TerminalID: l.TerminalID, Generation: l.Generation, WorkspaceID: l.Definition.WorkspaceID, RequestID: l.RequestID, HostEpoch: h.journal.Epoch, State: "starting", EnvironmentKeys: envKeys(l.Definition)}}
	// Before spawn, durable starting means a new host cannot infer permission
	// to execute this request again after a crash at any later instruction.
	if err := h.persist(r); err != nil {
		delete(h.journal.Receipts, l.RequestID)
		return Info{}, err
	}
	h.current[l.TerminalID] = l.RequestID
	if retired != nil {
		retired.dispose()
		delete(h.processes, l.TerminalID)
	}
	p, err := startProcess(l.Definition)
	if err != nil {
		r.Info.State = "failed-to-start"
		r.Info.Error = "executable failed to start"
		if e := h.persist(r); e != nil {
			return r.Info, e
		}
		return r.Info, nil
	}
	h.processes[l.TerminalID] = p
	r.Info.State = "running"
	r.Info.PID = p.cmd.Process.Pid
	h.workers.Add(1)
	go func() {
		defer h.workers.Done()
		<-p.exited
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.current[l.TerminalID] == l.RequestID {
			latest := h.journal.Receipts[l.RequestID]
			latest.Info = p.info(latest.Info)
			_ = h.persist(latest)
		}
	}()
	if err := h.persist(r); err != nil {
		return r.Info, err
	}
	return p.info(r.Info), nil
}

func (h *host) target(t *Target) (receipt, *process, error) {
	if t == nil || !identifier(t.TerminalID) || t.Generation <= 0 {
		return receipt{}, nil, failure("invalid_arguments", "terminal ID and generation are required")
	}
	id, ok := h.current[t.TerminalID]
	if !ok {
		return receipt{}, nil, failure("not_found", "terminal is not in this host inventory")
	}
	r := h.journal.Receipts[id]
	if r.Info.Generation != t.Generation {
		return receipt{}, nil, failure("stale_generation", "terminal generation changed; inspect before retrying")
	}
	return r, h.processes[t.TerminalID], nil
}

func (h *host) handle(ctx context.Context, req Request) (Response, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopping {
		return responseError(failure("host_stopping", "terminal host is stopping")), false
	}
	if req.Op == "hello" || req.Op == "inventory" {
		status := h.inventory()
		return Response{Status: &status}, false
	}
	if req.Epoch != h.journal.Epoch {
		return responseError(failure("stale_epoch", "host epoch changed; reconcile inventory without replaying input or launches")), false
	}
	switch req.Op {
	case "ensure", "restart":
		if req.Launch == nil {
			return responseError(failure("invalid_arguments", "launch is required")), false
		}
		info, err := h.ensure(ctx, *req.Launch, req.Op == "restart")
		if err != nil {
			return responseError(err), false
		}
		return Response{Info: &info}, false
	case "inspect", "screen", "stop":
		r, p, err := h.target(req.Target)
		if err != nil {
			return responseError(err), false
		}
		if req.Op == "screen" {
			if p == nil {
				return responseError(failure("terminal_lost", "this host has no live screen for that receipt")), false
			}
			screen, err := p.screen(r.Info, req.Row, req.Rows)
			if err != nil {
				return responseError(err), false
			}
			return Response{Screen: &screen}, false
		}
		if req.Op == "stop" && p != nil {
			if err = p.stop(req.Force); err != nil {
				return responseError(err), false
			}
			r.Info = p.info(r.Info)
			if err = h.persist(r); err != nil {
				return responseError(err), false
			}
		}
		info := h.info(r)
		return Response{Info: &info}, false
	case "shutdown":
		if !req.Force {
			for _, p := range h.processes {
				members, err := p.members()
				if err != nil || len(members) > 0 {
					return responseError(failure("conflict", "host has running jobs; stop them or explicitly force host shutdown")), false
				}
			}
		}
		h.stopping = true
		return Response{}, true
	default:
		return responseError(failure("unknown_command", "unsupported terminal host operation")), false
	}
}

func (h *host) close() {
	h.mu.Lock()
	h.stopping = true
	for id, p := range h.processes {
		if err := p.stop(true); err != nil {
			continue
		} // do not claim an exit we cannot verify.
		r := h.journal.Receipts[h.current[id]]
		r.Info = p.info(r.Info)
		_ = h.persist(r)
		p.dispose()
	}
	h.mu.Unlock()
	h.workers.Wait()
}
