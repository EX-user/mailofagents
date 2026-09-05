package server

// Worker heartbeat upload (boss directive, Efira shape): workers POST
// waiting/working state signals; the server keeps only the latest beat per
// account. Authenticated writes only — a worker can never overwrite another
// account's signal. Not in /api/boards/info self-description per the same
// directive; old servers 404 the path and clients degrade silently.

import (
	"net/http"
	"time"
)

// workerBeatStates are the valid heartbeat states; anything else is 400.
var workerBeatStates = map[string]bool{"waiting": true, "working": true}

// workerBeatDetailMaxRunes caps the free-form detail tail.
const workerBeatDetailMaxRunes = 500

// handleWorkerHeartbeat POSTs (upload) or GETs (read own latest) the
// caller's heartbeat.
//
//	POST /api/worker/heartbeat  {state, detail?, ts?}  (account)
//	GET  /api/worker/heartbeat                          (account)
func (s *Server) handleWorkerHeartbeat(w http.ResponseWriter, r *http.Request) {
	address := accountFrom(r.Context())
	switch r.Method {
	case http.MethodPost:
		var req struct {
			State  string `json:"state"`
			Detail string `json:"detail"`
			Ts     int64  `json:"ts"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
		if err := decodeJSON(r, &req); err != nil {
			badRequest(w, "invalid body: "+err.Error())
			return
		}
		if !workerBeatStates[req.State] {
			badRequest(w, "state must be waiting or working")
			return
		}
		if len([]rune(req.Detail)) > workerBeatDetailMaxRunes {
			badRequest(w, "detail too long")
			return
		}
		now := time.Now()
		if err := s.store.SetWorkerBeat(address, req.State, req.Detail, req.Ts, now); err != nil {
			internalError(w, "store heartbeat: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ts": now.Unix()})
	case http.MethodGet:
		rec, ok, err := s.store.WorkerBeatByAddress(address)
		if err != nil {
			internalError(w, "read heartbeat: "+err.Error())
			return
		}
		if !ok {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "beat": nil})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "beat": rec})
	default:
		methodNotAllowed(w)
	}
}
