package daemon

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"

	"github.com/lanewayhq/laneway/internal/api"
)

// routes is the single place API routes are registered. Handlers decode the
// request, call the daemon and encode the result; behaviour lives elsewhere.
func (d *Daemon) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/projects", d.handleListProjects)
	mux.HandleFunc("GET /v1/events", d.handleEvents)
	mux.HandleFunc("POST /v1/projects", d.handleLink)
	mux.HandleFunc("DELETE /v1/projects", d.handleUnlink)
	mux.HandleFunc("GET /v1/worktree", d.handleWorktree)
	mux.HandleFunc("GET /v1/info", d.handleInfo)
	mux.HandleFunc("GET /v1/daemon", d.handleDaemonInfo)
	mux.HandleFunc("GET /v1/logs", d.handleLogs)
	mux.HandleFunc("POST /v1/worktree/ready", d.handleWorktreeReady)
	mux.HandleFunc("POST /v1/worktree/{action}", d.handleControl)
	mux.HandleFunc("POST /v1/aliases", d.handleAddAlias)
	mux.HandleFunc("DELETE /v1/aliases/{name}", d.handleRemoveAlias)
	mux.HandleFunc("GET /v1/services", d.handleListServices)
	mux.HandleFunc("POST /v1/services/{action}", d.handleControlServices)
	mux.HandleFunc("GET /v1/orphans", d.handleListOrphans)
	mux.HandleFunc("POST /v1/gc", d.handleGC)
	mux.HandleFunc("GET /v1/plugins", d.handleListPlugins)
	mux.HandleFunc("POST /v1/plugins", d.handleAddPlugin)
	mux.HandleFunc("POST /v1/plugins/{plugin}/commands/{command}", d.handleRunPluginCommand)
	return mux
}

func (d *Daemon) handleListProjects(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, d.projectList())
}

func (d *Daemon) handleLink(w http.ResponseWriter, r *http.Request) {
	var req api.LinkRequest
	if !readJSON(w, r, &req) {
		return
	}
	res, err := d.link(r.Context(), req)
	if err != nil {
		d.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (d *Daemon) handleUnlink(w http.ResponseWriter, r *http.Request) {
	dir, err := d.queryDir(r)
	if err != nil {
		d.writeError(w, err)
		return
	}
	res, err := d.unlink(r.Context(), dir)
	if err != nil {
		d.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (d *Daemon) handleWorktree(w http.ResponseWriter, r *http.Request) {
	dir, err := d.queryDir(r)
	if err != nil {
		d.writeError(w, err)
		return
	}
	info, err := d.worktreeInfo(r.Context(), dir)
	if err != nil {
		d.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (d *Daemon) handleInfo(w http.ResponseWriter, r *http.Request) {
	dir, err := d.queryDir(r)
	if err != nil {
		d.writeError(w, err)
		return
	}
	info, err := d.info(r.Context(), dir)
	if err != nil {
		d.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (d *Daemon) handleWorktreeReady(w http.ResponseWriter, r *http.Request) {
	var req api.ReadyRequest
	if !readJSON(w, r, &req) {
		return
	}
	dir, err := d.worktreeDir(r.Context(), req.Dir, req.Worktree)
	if err != nil {
		d.writeError(w, err)
		return
	}
	req.Dir, req.Worktree = dir, ""
	info, err := d.worktreeReady(r.Context(), req)
	if err != nil {
		d.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (d *Daemon) handleControl(w http.ResponseWriter, r *http.Request) {
	var req api.ControlRequest
	if !readJSON(w, r, &req) {
		return
	}
	res, err := d.control(r.Context(), r.PathValue("action"), req)
	if err != nil {
		d.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (d *Daemon) handleAddAlias(w http.ResponseWriter, r *http.Request) {
	var req api.AliasRequest
	if !readJSON(w, r, &req) {
		return
	}
	dir, err := d.worktreeDir(r.Context(), req.Dir, req.Worktree)
	if err != nil {
		d.writeError(w, err)
		return
	}
	res, err := d.addAlias(r.Context(), dir, req.Name)
	if err != nil {
		d.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (d *Daemon) handleRemoveAlias(w http.ResponseWriter, r *http.Request) {
	dir, err := d.queryDir(r)
	if err != nil {
		d.writeError(w, err)
		return
	}
	res, err := d.removeAlias(r.Context(), dir, r.PathValue("name"))
	if err != nil {
		d.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (d *Daemon) handleListServices(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, d.listServices())
}

func (d *Daemon) handleControlServices(w http.ResponseWriter, r *http.Request) {
	var req api.ServiceRequest
	if !readJSON(w, r, &req) {
		return
	}
	res, err := d.controlServices(r.Context(), r.PathValue("action"), req)
	if err != nil {
		d.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (d *Daemon) handleListOrphans(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, d.listOrphans())
}

func (d *Daemon) handleGC(w http.ResponseWriter, r *http.Request) {
	var req api.GCRequest
	if !readJSON(w, r, &req) {
		return
	}
	writeJSON(w, http.StatusOK, d.gc(r.Context(), req))
}

func (d *Daemon) handleDaemonInfo(w http.ResponseWriter, r *http.Request) {
	info := api.DaemonInfo{PID: os.Getpid(), ProxyAddr: d.proxy.Addr().String()}
	if addr := d.HTTPAddr(); addr != nil {
		info.HTTPAddr = addr.String()
	}
	writeJSON(w, http.StatusOK, info)
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, api.ErrorResponse{Error: api.Error{
			Code: api.CodeBadRequest, Message: "invalid request body: " + err.Error(),
		}})
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// statusFor maps API error codes to HTTP statuses.
var statusFor = map[string]int{
	api.CodeBadRequest:      http.StatusBadRequest,
	api.CodeNameTaken:       http.StatusConflict,
	api.CodeNotGitRepo:      http.StatusUnprocessableEntity,
	api.CodeNotLinked:       http.StatusNotFound,
	api.CodeUnknownProcess:  http.StatusNotFound,
	api.CodeUnknownWorktree: http.StatusNotFound,
	api.CodeStartFailed:     http.StatusBadGateway,
	api.CodeInvalidAlias:    http.StatusUnprocessableEntity,
	api.CodeAliasTaken:      http.StatusConflict,
	api.CodeUnknownAlias:    http.StatusNotFound,

	api.CodeInvalidConfig: http.StatusUnprocessableEntity,

	api.CodeShuttingDown: http.StatusServiceUnavailable,

	api.CodeUnknownService: http.StatusNotFound,
	api.CodeServiceFailed:  http.StatusBadGateway,

	api.CodeInvalidPlugin:  http.StatusUnprocessableEntity,
	api.CodeUnknownPlugin:  http.StatusNotFound,
	api.CodeUnknownCommand: http.StatusNotFound,

	api.CodePluginsUnavailable: http.StatusBadGateway,
}

// writeError sends err to the client. An *api.Error is passed through as is;
// anything else is logged and reported as an internal error.
func (d *Daemon) writeError(w http.ResponseWriter, err error) {
	var apiErr *api.Error
	if !errors.As(err, &apiErr) {
		d.log.Error("request failed", "err", err)
		apiErr = &api.Error{Code: api.CodeInternal, Message: err.Error()}
	}
	status, ok := statusFor[apiErr.Code]
	if !ok {
		status = http.StatusInternalServerError
	}
	writeJSON(w, status, api.ErrorResponse{Error: *apiErr})
}

// queryDir is the directory of the Worktree a GET or DELETE request acts on,
// from its dir and worktree query parameters (see worktreeDir).
func (d *Daemon) queryDir(r *http.Request) (string, error) {
	q := r.URL.Query()
	return d.worktreeDir(r.Context(), q.Get("dir"), q.Get("worktree"))
}

func (d *Daemon) handleListPlugins(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, d.listPlugins())
}

func (d *Daemon) handleAddPlugin(w http.ResponseWriter, r *http.Request) {
	var req api.AddPluginRequest
	if !readJSON(w, r, &req) {
		return
	}
	res, err := d.addPlugin(req)
	if err != nil {
		d.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (d *Daemon) handleRunPluginCommand(w http.ResponseWriter, r *http.Request) {
	var req api.PluginCommandRequest
	if !readJSON(w, r, &req) {
		return
	}
	res, err := d.runPluginCommand(r.Context(), r.PathValue("plugin"), r.PathValue("command"), req)
	if err != nil {
		d.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
