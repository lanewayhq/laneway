package daemon

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/mrsafalpiya/portby.dev/internal/api"
)

// routes is the single place API routes are registered. Handlers decode the
// request, call the daemon and encode the result; behaviour lives elsewhere.
func (d *Daemon) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/projects", d.handleListProjects)
	mux.HandleFunc("POST /v1/projects", d.handleLink)
	return mux
}

func (d *Daemon) handleListProjects(w http.ResponseWriter, r *http.Request) {
	list := api.ProjectList{Projects: []api.Project{}}
	for _, p := range d.registry.Projects() {
		list.Projects = append(list.Projects, d.apiProject(p))
	}
	writeJSON(w, http.StatusOK, list)
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
	api.CodeBadRequest: http.StatusBadRequest,
	api.CodeNameTaken:  http.StatusConflict,
	api.CodeNotGitRepo: http.StatusUnprocessableEntity,

	api.CodeInvalidConfig: http.StatusUnprocessableEntity,
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
