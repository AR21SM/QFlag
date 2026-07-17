package flags

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/qflag/qflag/internal/raft"
)

func TestRolloutBucketIsStable(t *testing.T) {
	first := rolloutBucket("user-42", "new-payment-flow")
	second := rolloutBucket("user-42", "new-payment-flow")

	if first != second {
		t.Fatalf("expected stable bucket, got %d and %d", first, second)
	}

	if first < 0 || first > 99 {
		t.Fatalf("bucket out of range: %d", first)
	}
}

func TestCreateFlagCommitsFlagAndAuditInOneRaftEntry(t *testing.T) {
	store := newMemoryStore()
	service := NewService(store, make(chan raft.ApplyEvent))

	flag, err := service.CreateFlag(context.Background(), FeatureFlag{
		FlagName:          "new-payment-flow",
		ProjectID:         "project-123",
		Environment:       "prod",
		Enabled:           true,
		RolloutPercentage: 5,
	}, "tester")
	if err != nil {
		t.Fatalf("CreateFlag() error = %v", err)
	}
	if flag.Version != 1 {
		t.Fatalf("version = %d, want 1", flag.Version)
	}

	commands := store.commands()
	if len(commands) != 1 || commands[0].Op != "batch" || len(commands[0].Operations) != 2 {
		t.Fatalf("expected one atomic flag-and-audit batch, got %#v", commands)
	}
	if !strings.Contains(commands[0].Operations[1].Key, "/audit/") {
		t.Fatalf("second batch operation is not an audit entry: %s", commands[0].Operations[1].Key)
	}
}

func TestConcurrentUpdatesKeepEveryVersion(t *testing.T) {
	store := newMemoryStore()
	initial := FeatureFlag{FlagName: "checkout", ProjectID: "project-123", Environment: "prod", Enabled: true, Version: 1}
	raw, _ := json.Marshal(initial)
	store.values[flagKey(initial.ProjectID, initial.Environment, initial.FlagName)] = string(raw)
	service := NewService(store, make(chan raft.ApplyEvent))

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, rollout := range []int{20, 40} {
		wg.Add(1)
		go func(value int) {
			defer wg.Done()
			_, err := service.UpdateFlag(context.Background(), "project-123", "prod", "checkout", map[string]any{"rolloutPercentage": value}, "tester")
			errs <- err
		}(rollout)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("UpdateFlag() error = %v", err)
		}
	}

	current, err := service.GetFlag("project-123", "prod", "checkout")
	if err != nil {
		t.Fatal(err)
	}
	if current.Version != 3 {
		t.Fatalf("version = %d, want 3", current.Version)
	}
	for _, version := range []int{2, 3} {
		if _, ok := store.Get(versionKey("project-123", "prod", "checkout", version)); !ok {
			t.Fatalf("version %d was not stored", version)
		}
	}
}

func TestCreateFlagRejectsUnsafeRouteIdentity(t *testing.T) {
	service := NewService(newMemoryStore(), make(chan raft.ApplyEvent))
	_, err := service.CreateFlag(context.Background(), FeatureFlag{
		FlagName: "unsafe/name", ProjectID: "project-123", Environment: "prod",
	}, "tester")
	if err == nil {
		t.Fatal("expected unsafe flag name to be rejected")
	}
}

type memoryStore struct {
	mu        sync.Mutex
	values    map[string]string
	proposals []raft.Command
}

func newMemoryStore() *memoryStore {
	return &memoryStore{values: map[string]string{}}
}

func (s *memoryStore) Get(key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[key]
	return value, ok
}

func (s *memoryStore) List(prefix string) map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	values := map[string]string{}
	for key, value := range s.values {
		if strings.HasPrefix(key, prefix) {
			values[key] = value
		}
	}
	return values
}

func (s *memoryStore) Propose(_ context.Context, command raft.Command) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.proposals = append(s.proposals, command)
	operations := command.Operations
	if command.Op != "batch" {
		operations = []raft.Command{command}
	}
	for _, operation := range operations {
		switch operation.Op {
		case "put":
			s.values[operation.Key] = operation.Value
		case "delete":
			delete(s.values, operation.Key)
		}
	}
	return len(s.proposals), nil
}

func (s *memoryStore) commands() []raft.Command {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]raft.Command(nil), s.proposals...)
}
