package server

import (
	"net/http"
	"time"

	"github.com/jtmckay/decree-go-rest/internal/config"
)

// HealthRoute is the path of the health endpoint (SPEC.md §7).
const HealthRoute = config.HealthPath

// DaemonState is the supervised daemon's state, as /healthz reports it.
type DaemonState struct {
	Running bool
	// PID is the daemon's process id while it runs, otherwise 0.
	PID      int
	Restarts int
	// Since is when the daemon last started or stopped; zero before it
	// first started.
	Since time.Time
}

// health is the body of /healthz.
type health struct {
	OK     bool         `json:"ok"`
	Daemon daemonHealth `json:"daemon"`
	Config configHealth `json:"config"`
}

type daemonHealth struct {
	Enabled  bool       `json:"enabled"`
	Running  bool       `json:"running"`
	PID      *int       `json:"pid"`
	Restarts int        `json:"restarts"`
	Since    *time.Time `json:"since"`
}

type configHealth struct {
	LoadedAt time.Time `json:"loaded_at"`
	Error    *string   `json:"error"`
}

// serveHealth is GET /healthz: no authentication, and exempt from the
// rate budgets. It is 503 when the daemon is enabled but not running, or
// the last config reload failed.
func (s *Server) serveHealth(w http.ResponseWriter, r *http.Request) {
	logged(r).route = HealthRoute
	if s.health == nil {
		s.notFound(w, r)
		return
	}
	h := s.health()
	status := http.StatusOK
	if !h.OK {
		status = http.StatusServiceUnavailable
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, h)
}

// health reports the daemon and the config. The daemon is the one of the
// startup config: daemon.* takes effect only on restart.
func (l *Live) health() health {
	var h health
	h.Daemon.Enabled = l.boot.Daemon.Enabled
	if h.Daemon.Enabled && l.Daemon != nil {
		st := l.Daemon()
		h.Daemon.Running = st.Running
		h.Daemon.Restarts = st.Restarts
		if st.Running {
			pid := st.PID
			h.Daemon.PID = &pid
		}
		if !st.Since.IsZero() {
			since := st.Since.UTC()
			h.Daemon.Since = &since
		}
	}
	loadedAt, err := l.ConfigState()
	h.Config.LoadedAt = loadedAt.UTC()
	if err != nil {
		msg := err.Error()
		h.Config.Error = &msg
	}
	h.OK = (!h.Daemon.Enabled || h.Daemon.Running) && err == nil
	return h
}
