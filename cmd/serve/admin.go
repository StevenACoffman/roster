package serve

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"path/filepath"
	"runtime/trace"
	"time"
)

// Metrics, pprof and trace snapshots are served on their own listener, never the
// public mux. pprof exposes heap contents and goroutine stacks, and
// /debug/pprof/profile burns CPU for as long as a caller asks; /metrics
// enumerates every RPC name and its traffic. A separate, operator-chosen address
// makes exposing any of that a deliberate act rather than an accident of sharing
// a port with the API.

const (
	flightRecorderMinAge   = 10 * time.Second
	adminReadHeaderTimeout = 5 * time.Second
	snapshotDirPerm        = 0o750
)

// adminServer bundles the admin listener and the resources it owns.
type adminServer struct {
	server   *http.Server
	listener net.Listener
	recorder *trace.FlightRecorder
	logger   *slog.Logger
}

// newAdminServer builds the admin listener.
//
// Setting snapshotDir starts a flight recorder: a rolling in-memory trace of the
// last flightRecorderMinAge that POST /debug/trace/snapshot dumps to a file. It
// costs a few percent of CPU continuously, and buys the ability to capture a
// latency spike after noticing it rather than having to reproduce it.
func newAdminServer(
	ctx context.Context,
	logger *slog.Logger,
	addr, snapshotDir string,
	metricsHandler http.Handler,
) (*adminServer, error) {
	mux := http.NewServeMux()

	// Nil when the metric pipeline failed to start: the route is then absent
	// rather than present and answering nothing.
	if metricsHandler != nil {
		mux.Handle("GET /metrics", metricsHandler)
	}

	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)

	admin := &adminServer{logger: logger}

	if snapshotDir != "" {
		recorder := trace.NewFlightRecorder(trace.FlightRecorderConfig{
			MinAge: flightRecorderMinAge,
		})
		if err := recorder.Start(); err != nil {
			return nil, fmt.Errorf("starting flight recorder: %w", err)
		}
		admin.recorder = recorder
		mux.Handle("POST /debug/trace/snapshot",
			handleTraceSnapshot(logger, recorder, snapshotDir))
	}

	mux.Handle("/", http.NotFoundHandler())

	var listenCfg net.ListenConfig
	listener, err := listenCfg.Listen(ctx, "tcp", addr)
	if err != nil {
		if admin.recorder != nil {
			admin.recorder.Stop()
		}
		return nil, fmt.Errorf("listening on admin address %s: %w", addr, err)
	}

	admin.listener = listener
	admin.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: adminReadHeaderTimeout,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
	}
	return admin, nil
}

// Addr reports the address the admin listener bound to.
func (a *adminServer) Addr() net.Addr { return a.listener.Addr() }

// Serve runs the admin listener until it is closed.
func (a *adminServer) Serve() error { return a.server.Serve(a.listener) }

// Close shuts the admin listener down and stops the flight recorder.
func (a *adminServer) Close(ctx context.Context) error {
	var shutdownErr error
	if a.server != nil {
		shutdownErr = a.server.Shutdown(ctx)
	}
	if a.recorder != nil {
		a.recorder.Stop()
	}
	return shutdownErr
}

// handleTraceSnapshot dumps the recorder's buffer to a file.
//
// POST because it has a side effect and is not free — a GET here would be
// fetched by any crawler or link-prefetcher that reached the admin port.
func handleTraceSnapshot(
	logger *slog.Logger, recorder *trace.FlightRecorder, dir string,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := os.MkdirAll(dir, snapshotDirPerm); err != nil {
			logger.ErrorContext(r.Context(), "creating trace snapshot directory",
				"dir", dir, "error", err)
			http.Error(w, "could not create snapshot directory", http.StatusInternalServerError)
			return
		}

		path := filepath.Join(dir, fmt.Sprintf("trace-%d.out", time.Now().UnixNano()))
		// dir is operator-supplied at startup and the basename is generated here,
		// so the path is not attacker-controlled.
		file, err := os.Create(path)
		if err != nil {
			logger.ErrorContext(r.Context(), "creating trace snapshot file",
				"path", path, "error", err)
			http.Error(w, "could not create snapshot file", http.StatusInternalServerError)
			return
		}
		defer func() { _ = file.Close() }()

		written, err := recorder.WriteTo(file)
		if err != nil {
			logger.ErrorContext(r.Context(), "writing trace snapshot",
				"path", path, "error", err)
			http.Error(w, "could not write snapshot", http.StatusInternalServerError)
			return
		}

		logger.InfoContext(r.Context(), "wrote execution trace snapshot",
			"path", path, "bytes", written)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"path":%q,"bytes":%d}`+"\n", path, written)
	})
}
