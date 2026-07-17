package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/qflag/qflag/internal/flags"
	"github.com/qflag/qflag/internal/raft"
)

type api struct {
	node           *raft.Node
	flags          *flags.Service
	apiToken       string
	allowedOrigins map[string]struct{}
}

func main() {
	nodeID := env("NODE_ID", "node-1")
	httpAddr := env("HTTP_ADDR", ":8080")
	dataDir := env("DATA_DIR", "./data/"+nodeID)
	publicURL := env("PUBLIC_URL", "http://localhost"+strings.TrimPrefix(httpAddr, ":"))
	apiToken := mustEnv("API_TOKEN")
	raftToken := mustEnv("RAFT_TOKEN")

	peers := parsePeers(os.Getenv("PEERS"))
	node, err := raft.NewNode(nodeID, publicURL, dataDir, peers, raftToken)
	if err != nil {
		log.Fatal(err)
	}

	flagService := flags.NewService(node, node.ApplyEvents())
	server := &api{node: node, flags: flagService, apiToken: apiToken, allowedOrigins: parseOrigins(env("CORS_ALLOWED_ORIGINS", "http://localhost:3000"))}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	node.Start(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", server.health)
	mux.HandleFunc("/metrics", server.metrics)
	mux.HandleFunc("/raft/request-vote", server.requireRaftToken(raftToken, server.requestVote))
	mux.HandleFunc("/raft/append-entries", server.requireRaftToken(raftToken, server.appendEntries))
	mux.HandleFunc("/api/v1/cluster/status", server.requireAPIToken(server.clusterStatus))
	mux.HandleFunc("/api/v1/audit", server.requireAPIToken(server.audit))
	mux.HandleFunc("/api/v1/projects/", server.requireAPIToken(server.projectRoutes))

	httpServer := &http.Server{
		Addr:              httpAddr,
		Handler:           server.cors(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	log.Printf("qflag node %s listening on %s", nodeID, httpAddr)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func (a *api) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"nodeId": a.node.ID(),
		"role":   a.node.Role(),
	})
}

func (a *api) requestVote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req raft.RequestVoteRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a.node.HandleRequestVote(req))
}

func (a *api) appendEntries(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req raft.AppendEntriesRequest
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a.node.HandleAppendEntries(req))
}

func (a *api) clusterStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.node.Status())
}

func (a *api) audit(w http.ResponseWriter, r *http.Request) {
	if !a.requireLeader(w) {
		return
	}
	projectID := queryDefault(r, "projectId", "project_123")
	envName := queryDefault(r, "env", "prod")
	logs, err := a.flags.ListAudit(projectID, envName)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, logs)
}

func (a *api) metrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")

	all := map[string]raft.Metric{}
	for key, value := range a.node.Metrics() {
		all[key] = value
	}
	for key, value := range a.flags.Metrics() {
		all[key] = value
	}

	for key, metric := range all {
		_, _ = fmt.Fprintf(w, "# TYPE %s %s\n%s %s\n", key, metric.Type, key, metric.Value)
	}
}

func (a *api) projectRoutes(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 7 || parts[0] != "api" || parts[1] != "v1" || parts[2] != "projects" || parts[4] != "env" {
		writeError(w, http.StatusNotFound, "route not found")
		return
	}

	projectID := parts[3]
	envName := parts[5]
	resource := parts[6]

	if resource == "watch" {
		a.watch(w, r)
		return
	}

	if resource != "flags" {
		writeError(w, http.StatusNotFound, "route not found")
		return
	}
	if !a.requireLeader(w) {
		return
	}

	if len(parts) == 7 {
		switch r.Method {
		case http.MethodGet:
			a.listFlags(w, r, projectID, envName)
		case http.MethodPost:
			a.createFlag(w, r, projectID, envName)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}

	flagName := parts[7]
	if len(parts) == 8 {
		switch r.Method {
		case http.MethodGet:
			a.getFlag(w, r, projectID, envName, flagName)
		case http.MethodPatch:
			a.updateFlag(w, r, projectID, envName, flagName)
		case http.MethodDelete:
			a.deleteFlag(w, r, projectID, envName, flagName)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}

	if len(parts) == 9 && parts[8] == "evaluate" {
		a.evaluate(w, r, projectID, envName, flagName)
		return
	}

	if len(parts) == 9 && parts[8] == "rollback" {
		a.rollback(w, r, projectID, envName, flagName)
		return
	}

	writeError(w, http.StatusNotFound, "route not found")
}

func (a *api) listFlags(w http.ResponseWriter, r *http.Request, projectID, envName string) {
	list, err := a.flags.ListFlags(projectID, envName)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (a *api) createFlag(w http.ResponseWriter, r *http.Request, projectID, envName string) {
	var req struct {
		FlagName          string   `json:"flagName"`
		Enabled           bool     `json:"enabled"`
		RolloutPercentage int      `json:"rolloutPercentage"`
		TargetUsers       []string `json:"targetUsers"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	flag, err := a.flags.CreateFlag(r.Context(), flags.FeatureFlag{
		FlagName:          req.FlagName,
		ProjectID:         projectID,
		Environment:       envName,
		Enabled:           req.Enabled,
		RolloutPercentage: req.RolloutPercentage,
		TargetUsers:       req.TargetUsers,
	}, r.Header.Get("X-Actor"))
	if err != nil {
		writeRaftError(w, a.node, err)
		return
	}
	writeJSON(w, http.StatusCreated, flag)
}

func (a *api) getFlag(w http.ResponseWriter, r *http.Request, projectID, envName, flagName string) {
	flag, err := a.flags.GetFlag(projectID, envName, flagName)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, flag)
}

func (a *api) updateFlag(w http.ResponseWriter, r *http.Request, projectID, envName, flagName string) {
	var patch map[string]any
	if err := decodeJSON(w, r, &patch); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	flag, err := a.flags.UpdateFlag(r.Context(), projectID, envName, flagName, patch, r.Header.Get("X-Actor"))
	if err != nil {
		writeRaftError(w, a.node, err)
		return
	}
	writeJSON(w, http.StatusOK, flag)
}

func (a *api) deleteFlag(w http.ResponseWriter, r *http.Request, projectID, envName, flagName string) {
	if err := a.flags.DeleteFlag(r.Context(), projectID, envName, flagName, r.Header.Get("X-Actor")); err != nil {
		writeRaftError(w, a.node, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) rollback(w http.ResponseWriter, r *http.Request, projectID, envName, flagName string) {
	var req struct {
		Version int `json:"version"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	flag, err := a.flags.Rollback(r.Context(), projectID, envName, flagName, req.Version, r.Header.Get("X-Actor"))
	if err != nil {
		writeRaftError(w, a.node, err)
		return
	}
	writeJSON(w, http.StatusOK, flag)
}

func (a *api) evaluate(w http.ResponseWriter, r *http.Request, projectID, envName, flagName string) {
	defaultValue := r.URL.Query().Get("default") == "true"
	result, err := a.flags.Evaluate(projectID, envName, flagName, r.URL.Query().Get("userId"), defaultValue)
	if err != nil {
		writeJSON(w, http.StatusOK, result)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *api) watch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !a.requireLeader(w) {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	events, cancel := a.flags.Subscribe()
	defer cancel()

	for {
		select {
		case <-r.Context().Done():
			return
		case event := <-events:
			data, _ := json.Marshal(event)
			_, _ = fmt.Fprintf(w, "event: flag\n")
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-time.After(20 * time.Second):
			_, _ = fmt.Fprintf(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}

func writeRaftError(w http.ResponseWriter, node *raft.Node, err error) {
	if errors.Is(err, raft.ErrNotLeader) {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":     "not_leader",
			"leaderId":  node.LeaderID(),
			"leaderUrl": node.LeaderURL(),
		})
		return
	}
	writeError(w, http.StatusBadRequest, err.Error())
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": message})
}

func (a *api) requireLeader(w http.ResponseWriter) bool {
	if a.node.Role() == raft.RoleLeader {
		return true
	}
	writeRaftError(w, a.node, raft.ErrNotLeader)
	return false
}

func (a *api) requireAPIToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validToken(r.Header.Get("Authorization"), "Bearer ", a.apiToken) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="qflag"`)
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		next(w, r)
	}
}

func (a *api) requireRaftToken(expected string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validToken(r.Header.Get("X-Raft-Token"), "", expected) {
			writeError(w, http.StatusUnauthorized, "invalid raft token")
			return
		}
		next(w, r)
	}
}

func validToken(value, prefix, expected string) bool {
	if prefix != "" {
		if !strings.HasPrefix(value, prefix) {
			return false
		}
		value = strings.TrimPrefix(value, prefix)
	}
	if expected == "" || len(value) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(value), []byte(expected)) == 1
}

func (a *api) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			if _, ok := a.allowedOrigins[origin]; !ok {
				writeError(w, http.StatusForbidden, "origin not allowed")
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Actor")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain one JSON value")
		}
		return err
	}
	return nil
}

func parsePeers(value string) map[string]string {
	peers := map[string]string{}
	if value == "" {
		return peers
	}
	for _, item := range strings.Split(value, ",") {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) == 2 {
			peers[strings.TrimSpace(parts[0])] = strings.TrimRight(strings.TrimSpace(parts[1]), "/")
		}
	}
	return peers
}

func queryDefault(r *http.Request, key, fallback string) string {
	if value := r.URL.Query().Get(key); value != "" {
		return value
	}
	return fallback
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func mustEnv(key string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		log.Fatalf("%s is required", key)
	}
	return value
}

func parseOrigins(value string) map[string]struct{} {
	origins := map[string]struct{}{}
	for _, origin := range strings.Split(value, ",") {
		origin = strings.TrimRight(strings.TrimSpace(origin), "/")
		if origin != "" {
			origins[origin] = struct{}{}
		}
	}
	return origins
}
