package flags

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/qflag/qflag/internal/raft"
)

type Store interface {
	Get(key string) (string, bool)
	List(prefix string) map[string]string
	Propose(ctx context.Context, command raft.Command) (int, error)
}

type FeatureFlag struct {
	FlagName          string    `json:"flagName"`
	ProjectID         string    `json:"projectId"`
	Environment       string    `json:"environment"`
	Enabled           bool      `json:"enabled"`
	RolloutPercentage int       `json:"rolloutPercentage"`
	TargetUsers       []string  `json:"targetUsers"`
	Version           int       `json:"version"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

type AuditLog struct {
	FlagName  string          `json:"flagName"`
	Action    string          `json:"action"`
	OldValue  json.RawMessage `json:"oldValue,omitempty"`
	NewValue  json.RawMessage `json:"newValue,omitempty"`
	UpdatedBy string          `json:"updatedBy"`
	Timestamp time.Time       `json:"timestamp"`
	Version   int             `json:"version"`
}

type Evaluation struct {
	Flag    string `json:"flag"`
	Enabled bool   `json:"enabled"`
	Reason  string `json:"reason"`
	Version int    `json:"version"`
}

type WatchEvent struct {
	Type     string       `json:"type"`
	FlagName string       `json:"flagName"`
	Version  int          `json:"version"`
	Value    *FeatureFlag `json:"value,omitempty"`
}

type Service struct {
	store          Store
	mutationMu     sync.Mutex
	watchersMu     sync.Mutex
	watchers       map[chan WatchEvent]struct{}
	evaluationHits atomic.Int64
	updateHits     atomic.Int64
}

func NewService(store Store, events <-chan raft.ApplyEvent) *Service {
	s := &Service{
		store:    store,
		watchers: map[chan WatchEvent]struct{}{},
	}
	go s.consumeApplyEvents(events)
	return s
}

func (s *Service) CreateFlag(ctx context.Context, flag FeatureFlag, actor string) (FeatureFlag, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	if err := validateIdentity(flag.ProjectID, "projectId"); err != nil {
		return FeatureFlag{}, err
	}
	if err := validateIdentity(flag.Environment, "environment"); err != nil {
		return FeatureFlag{}, err
	}
	if err := validateIdentity(flag.FlagName, "flagName"); err != nil {
		return FeatureFlag{}, err
	}
	if err := validateRollout(flag.RolloutPercentage); err != nil {
		return FeatureFlag{}, err
	}
	now := time.Now().UTC()
	flag.CreatedAt = now
	flag.UpdatedAt = now
	flag.Version = 1
	flag.TargetUsers = normalizeUsers(flag.TargetUsers)

	key := flagKey(flag.ProjectID, flag.Environment, flag.FlagName)
	if _, ok := s.store.Get(key); ok {
		return FeatureFlag{}, fmt.Errorf("flag already exists")
	}

	if err := s.proposeMutation(ctx, []raft.Command{
		{Op: "put", Key: key, Value: string(mustJSON(flag))},
		s.auditCommand(flag, "CREATE_FLAG", nil, mustJSON(flag), actor),
	}); err != nil {
		return FeatureFlag{}, err
	}
	s.updateHits.Add(1)
	return flag, nil
}

func (s *Service) UpdateFlag(ctx context.Context, projectID, env, name string, patch map[string]any, actor string) (FeatureFlag, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	if err := validateRouteIdentity(projectID, env, name); err != nil {
		return FeatureFlag{}, err
	}
	for field := range patch {
		switch field {
		case "enabled", "rolloutPercentage", "targetUsers":
		default:
			return FeatureFlag{}, fmt.Errorf("unsupported field %q", field)
		}
	}
	oldFlag, err := s.GetFlag(projectID, env, name)
	if err != nil {
		return FeatureFlag{}, err
	}

	next := oldFlag
	if enabled, ok := patch["enabled"].(bool); ok {
		next.Enabled = enabled
	}
	if rollout, ok := numberToInt(patch["rolloutPercentage"]); ok {
		if err := validateRollout(rollout); err != nil {
			return FeatureFlag{}, err
		}
		next.RolloutPercentage = rollout
	}
	if users, ok := stringSlice(patch["targetUsers"]); ok {
		next.TargetUsers = normalizeUsers(users)
	}
	next.Version = oldFlag.Version + 1
	next.UpdatedAt = time.Now().UTC()

	if err := s.proposeMutation(ctx, []raft.Command{
		{Op: "put", Key: flagKey(projectID, env, name), Value: string(mustJSON(next))},
		{Op: "put", Key: versionKey(projectID, env, name, next.Version), Value: string(mustJSON(next))},
		s.auditCommand(next, "UPDATE_FLAG", mustJSON(oldFlag), mustJSON(next), actor),
	}); err != nil {
		return FeatureFlag{}, err
	}
	s.updateHits.Add(1)
	return next, nil
}

func (s *Service) DeleteFlag(ctx context.Context, projectID, env, name, actor string) error {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	if err := validateRouteIdentity(projectID, env, name); err != nil {
		return err
	}
	oldFlag, err := s.GetFlag(projectID, env, name)
	if err != nil {
		return err
	}
	if err := s.proposeMutation(ctx, []raft.Command{
		{Op: "delete", Key: flagKey(projectID, env, name), Value: string(mustJSON(oldFlag))},
		s.auditCommand(oldFlag, "DELETE_FLAG", mustJSON(oldFlag), nil, actor),
	}); err != nil {
		return err
	}
	s.updateHits.Add(1)
	return nil
}

func (s *Service) Rollback(ctx context.Context, projectID, env, name string, version int, actor string) (FeatureFlag, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	if err := validateRouteIdentity(projectID, env, name); err != nil {
		return FeatureFlag{}, err
	}
	oldFlag, err := s.GetFlag(projectID, env, name)
	if err != nil {
		return FeatureFlag{}, err
	}
	raw, ok := s.store.Get(versionKey(projectID, env, name, version))
	if !ok {
		return FeatureFlag{}, fmt.Errorf("version not found")
	}
	var restored FeatureFlag
	if err := json.Unmarshal([]byte(raw), &restored); err != nil {
		return FeatureFlag{}, err
	}
	restored.Version = oldFlag.Version + 1
	restored.UpdatedAt = time.Now().UTC()

	if err := s.proposeMutation(ctx, []raft.Command{
		{Op: "put", Key: flagKey(projectID, env, name), Value: string(mustJSON(restored))},
		{Op: "put", Key: versionKey(projectID, env, name, restored.Version), Value: string(mustJSON(restored))},
		s.auditCommand(restored, "ROLLBACK_FLAG", mustJSON(oldFlag), mustJSON(restored), actor),
	}); err != nil {
		return FeatureFlag{}, err
	}
	s.updateHits.Add(1)
	return restored, nil
}

func (s *Service) GetFlag(projectID, env, name string) (FeatureFlag, error) {
	raw, ok := s.store.Get(flagKey(projectID, env, name))
	if !ok {
		return FeatureFlag{}, fmt.Errorf("flag not found")
	}
	var flag FeatureFlag
	if err := json.Unmarshal([]byte(raw), &flag); err != nil {
		return FeatureFlag{}, err
	}
	return flag, nil
}

func (s *Service) ListFlags(projectID, env string) ([]FeatureFlag, error) {
	raw := s.store.List(flagsPrefix(projectID, env))
	flags := make([]FeatureFlag, 0, len(raw))
	for key, value := range raw {
		if strings.Contains(key, "/versions/") {
			continue
		}
		var flag FeatureFlag
		if err := json.Unmarshal([]byte(value), &flag); err == nil {
			flags = append(flags, flag)
		}
	}
	sort.Slice(flags, func(i, j int) bool { return flags[i].FlagName < flags[j].FlagName })
	return flags, nil
}

func (s *Service) ListAudit(projectID, env string) ([]AuditLog, error) {
	raw := s.store.List(auditPrefix(projectID, env))
	logs := make([]AuditLog, 0, len(raw))
	for _, value := range raw {
		var entry AuditLog
		if err := json.Unmarshal([]byte(value), &entry); err == nil {
			logs = append(logs, entry)
		}
	}
	sort.Slice(logs, func(i, j int) bool { return logs[i].Timestamp.After(logs[j].Timestamp) })
	return logs, nil
}

func (s *Service) Evaluate(projectID, env, name, userID string, defaultValue bool) (Evaluation, error) {
	s.evaluationHits.Add(1)
	flag, err := s.GetFlag(projectID, env, name)
	if err != nil {
		return Evaluation{Flag: name, Enabled: defaultValue, Reason: "default"}, err
	}

	for _, target := range flag.TargetUsers {
		if target == userID {
			return Evaluation{Flag: name, Enabled: true, Reason: "target_user", Version: flag.Version}, nil
		}
	}

	if !flag.Enabled {
		return Evaluation{Flag: name, Enabled: false, Reason: "flag_disabled", Version: flag.Version}, nil
	}

	bucket := rolloutBucket(userID, name)
	enabled := bucket < flag.RolloutPercentage
	reason := "percentage_rollout_miss"
	if enabled {
		reason = "percentage_rollout"
	}

	return Evaluation{Flag: name, Enabled: enabled, Reason: reason, Version: flag.Version}, nil
}

func (s *Service) Subscribe() (<-chan WatchEvent, func()) {
	ch := make(chan WatchEvent, 32)
	s.watchersMu.Lock()
	s.watchers[ch] = struct{}{}
	s.watchersMu.Unlock()

	cancel := func() {
		s.watchersMu.Lock()
		delete(s.watchers, ch)
		close(ch)
		s.watchersMu.Unlock()
	}
	return ch, cancel
}

func (s *Service) Metrics() map[string]raft.Metric {
	return map[string]raft.Metric{
		"flag_evaluation_total": {Type: "counter", Value: fmt.Sprintf("%d", s.evaluationHits.Load())},
		"flag_update_total":     {Type: "counter", Value: fmt.Sprintf("%d", s.updateHits.Load())},
	}
}

func (s *Service) proposeMutation(ctx context.Context, operations []raft.Command) error {
	_, err := s.store.Propose(ctx, raft.Command{Op: "batch", Operations: operations})
	return err
}

func (s *Service) auditCommand(flag FeatureFlag, action string, oldValue, newValue []byte, actor string) raft.Command {
	if actor == "" {
		actor = "local-admin"
	}
	entry := AuditLog{
		FlagName:  flag.FlagName,
		Action:    action,
		OldValue:  oldValue,
		NewValue:  newValue,
		UpdatedBy: actor,
		Timestamp: time.Now().UTC(),
		Version:   flag.Version,
	}
	return raft.Command{Op: "put", Key: fmt.Sprintf("%s/%s/%020d", auditPrefix(flag.ProjectID, flag.Environment), flag.FlagName, time.Now().UnixNano()), Value: string(mustJSON(entry))}
}

func (s *Service) consumeApplyEvents(events <-chan raft.ApplyEvent) {
	for event := range events {
		if !strings.Contains(event.Command.Key, "/flags/") || strings.Contains(event.Command.Key, "/versions/") {
			continue
		}
		watchEvent, ok := watchEventForCommand(event.Command)
		if !ok {
			continue
		}
		s.watchersMu.Lock()
		for watcher := range s.watchers {
			select {
			case watcher <- watchEvent:
			default:
			}
		}
		s.watchersMu.Unlock()
	}
}

func watchEventForCommand(command raft.Command) (WatchEvent, bool) {
	var flag FeatureFlag
	if err := json.Unmarshal([]byte(command.Value), &flag); err != nil {
		return WatchEvent{}, false
	}
	if command.Op == "delete" {
		return WatchEvent{Type: "FLAG_DELETED", FlagName: flag.FlagName, Version: flag.Version}, true
	}
	return WatchEvent{Type: "FLAG_UPDATED", FlagName: flag.FlagName, Version: flag.Version, Value: &flag}, true
}

func rolloutBucket(userID, flagName string) int {
	hash := uint32(2166136261)
	for _, char := range userID + ":" + flagName {
		hash ^= uint32(char)
		hash *= 16777619
	}
	return int(hash % 100)
}

func validateRollout(value int) error {
	if value < 0 || value > 100 {
		return fmt.Errorf("rolloutPercentage must be between 0 and 100")
	}
	return nil
}

func normalizeUsers(users []string) []string {
	if len(users) > 10000 {
		users = users[:10000]
	}
	out := make([]string, 0, len(users))
	seen := map[string]struct{}{}
	for _, user := range users {
		user = strings.TrimSpace(user)
		if user == "" {
			continue
		}
		if _, ok := seen[user]; ok {
			continue
		}
		seen[user] = struct{}{}
		out = append(out, user)
	}
	sort.Strings(out)
	return out
}

var identityPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func validateRouteIdentity(projectID, env, name string) error {
	for label, value := range map[string]string{"projectId": projectID, "environment": env, "flagName": name} {
		if err := validateIdentity(value, label); err != nil {
			return err
		}
	}
	return nil
}

func validateIdentity(value, label string) error {
	if !identityPattern.MatchString(value) {
		return fmt.Errorf("%s must contain 1-128 letters, numbers, dots, underscores, or hyphens and start with a letter or number", label)
	}
	return nil
}

func numberToInt(value any) (int, bool) {
	switch v := value.(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	default:
		return 0, false
	}
}

func stringSlice(value any) ([]string, bool) {
	items, ok := value.([]any)
	if !ok {
		if typed, typedOK := value.([]string); typedOK {
			return typed, true
		}
		return nil, false
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if ok {
			out = append(out, text)
		}
	}
	return out, true
}

func mustJSON(value any) []byte {
	if value == nil {
		return nil
	}
	data, _ := json.Marshal(value)
	return data
}

func flagsPrefix(projectID, env string) string {
	return fmt.Sprintf("/projects/%s/env/%s/flags/", projectID, env)
}

func flagKey(projectID, env, name string) string {
	return flagsPrefix(projectID, env) + name
}

func versionKey(projectID, env, name string, version int) string {
	return fmt.Sprintf("%s/versions/%020d", flagKey(projectID, env, name), version)
}

func auditPrefix(projectID, env string) string {
	return fmt.Sprintf("/audit/%s/%s", projectID, env)
}
