package api

import (
	"encoding/json"
	"net/http"
)

// Launch profile: which of Dragonwilds' two dedicated-server builds the
// agent starts.
//
// This is the one server-shaped setting that isn't a setting at all — it
// decides what the server *is*. The native Linux build cannot load UE4SS,
// so it can never carry the dwbridge mod, which means no on-demand save, no
// commands, ever. The Windows build under Wine can. Everything else in the
// console reads capability downstream of this choice, which is why it is
// exposed rather than buried in agent environment variables.

func (s *Server) handleGetLaunch(w http.ResponseWriter, r *http.Request) {
	srv, ok := s.loadServer(w, r)
	if !ok {
		return
	}
	agent := s.agentFor(srv)
	if agent == nil {
		writeError(w, http.StatusBadRequest, "no agent configured for this server")
		return
	}
	health, err := agent.Health(r.Context())
	if err != nil {
		writeAgentError(w, err)
		return
	}
	if health.Launch == nil {
		// Companion or provisioner mode: nothing is being launched here, so
		// there is no build to choose. A 400 rather than an empty object,
		// matching how the other supervisor-only verbs answer.
		writeError(w, http.StatusBadRequest, "this agent does not run the game, so it has no launch profile")
		return
	}
	writeJSON(w, http.StatusOK, health.Launch)
}

func (s *Server) handleSetLaunch(w http.ResponseWriter, r *http.Request) {
	srv, ok := s.loadServer(w, r)
	if !ok {
		return
	}
	var req struct {
		Profile string `json:"profile"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	agent := s.agentFor(srv)
	if agent == nil {
		writeError(w, http.StatusBadRequest, "no agent configured for this server")
		return
	}
	status, err := agent.SetLaunchProfile(r.Context(), req.Profile)
	if err != nil {
		writeAgentError(w, err)
		return
	}
	// Worth auditing: it changes which build runs, and therefore whether
	// this server can be saved on demand at all.
	s.audit(r, srv.ID, "launch-profile", req.Profile)
	writeJSON(w, http.StatusOK, status)
}

// handleRecreateAgent moves a provisioned server's agent onto a different
// wkagent image — in practice, onto the Wine variant so it can run the
// modded build.
//
// This exists because provisioner-created containers belong to no
// orchestrator: they don't appear in a TrueNAS apps list or any compose
// file, so changing their image otherwise means hand-writing docker
// commands on the host. The provisioner made them and can rebuild them,
// which makes this a button instead of a runbook.
func (s *Server) handleRecreateAgent(w http.ResponseWriter, r *http.Request) {
	srv, ok := s.loadServer(w, r)
	if !ok {
		return
	}
	var req struct {
		ImageTag string `json:"imageTag"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if s.Provisioner == nil {
		writeError(w, http.StatusBadRequest,
			"no provisioner is configured, so Wildskeeper cannot rebuild this container — change its image where it was deployed")
		return
	}
	if srv.ContainerName == "" {
		writeError(w, http.StatusBadRequest, "this server has no container name recorded, so there is nothing to rebuild")
		return
	}
	result, err := s.Provisioner.RecreateAgent(r.Context(), srv.ContainerName, req.ImageTag)
	if err != nil {
		writeAgentError(w, err)
		return
	}
	s.audit(r, srv.ID, "agent-image", result.Image)
	writeJSON(w, http.StatusOK, result)
}
