package observability

import (
	"context"
	"iter"
	"sync"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

func resetGlobalRegistryForTest(t *testing.T) *TraceRegistry {
	t.Helper()
	oldRegistry := globalRegistry
	oldOnce := once
	registry := &TraceRegistry{
		adkTraceToVeadkTraceMap: make(map[oteltrace.TraceID]*traceInfos),
		cleanupQueue:            make(chan cleanupRequest, traceCleanupQueueSize),
		shutdownChan:            make(chan struct{}),
	}
	globalRegistry = registry
	once = sync.Once{}
	once.Do(func() {})
	t.Cleanup(func() {
		registry.Shutdown()
		globalRegistry = oldRegistry
		once = oldOnce
	})
	return registry
}

func mustTraceID(t *testing.T, raw string) oteltrace.TraceID {
	t.Helper()
	id, err := oteltrace.TraceIDFromHex(raw)
	if err != nil {
		t.Fatalf("TraceIDFromHex(%q): %v", raw, err)
	}
	return id
}

func mustSpanID(t *testing.T, raw string) oteltrace.SpanID {
	t.Helper()
	id, err := oteltrace.SpanIDFromHex(raw)
	if err != nil {
		t.Fatalf("SpanIDFromHex(%q): %v", raw, err)
	}
	return id
}

func spansByName(spans []sdktrace.ReadOnlySpan) map[string]sdktrace.ReadOnlySpan {
	out := make(map[string]sdktrace.ReadOnlySpan, len(spans))
	for _, span := range spans {
		out[span.Name()] = span
	}
	return out
}

func spanNames(spans []sdktrace.ReadOnlySpan) []string {
	names := make([]string, 0, len(spans))
	for _, span := range spans {
		names = append(names, span.Name())
	}
	return names
}

type testState struct {
	mu     sync.Mutex
	values map[string]any
}

func newTestState() *testState {
	return &testState{values: make(map[string]any)}
}

func (s *testState) Get(key string) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[key]
	if !ok {
		return nil, session.ErrStateKeyNotExist
	}
	return value, nil
}

func (s *testState) Set(key string, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values[key] = value
	return nil
}

func (s *testState) All() iter.Seq2[string, any] {
	return func(yield func(string, any) bool) {
		s.mu.Lock()
		defer s.mu.Unlock()
		for key, value := range s.values {
			if !yield(key, value) {
				return
			}
		}
	}
}

type testSession struct {
	id     string
	app    string
	user   string
	state  session.State
	events session.Events
}

func (s *testSession) ID() string                { return s.id }
func (s *testSession) AppName() string           { return s.app }
func (s *testSession) UserID() string            { return s.user }
func (s *testSession) State() session.State      { return s.state }
func (s *testSession) Events() session.Events    { return s.events }
func (s *testSession) LastUpdateTime() time.Time { return time.Time{} }

type testEvents struct{}

func (testEvents) All() iter.Seq[*session.Event] { return func(func(*session.Event) bool) {} }
func (testEvents) Len() int                      { return 0 }
func (testEvents) At(int) *session.Event         { return nil }

type testInvocationContext struct {
	context.Context
	session      session.Session
	invocationID string
	userContent  *genai.Content
	ended        bool
}

func (c *testInvocationContext) Agent() agent.Agent          { return nil }
func (c *testInvocationContext) Artifacts() agent.Artifacts  { return nil }
func (c *testInvocationContext) Memory() agent.Memory        { return nil }
func (c *testInvocationContext) Session() session.Session    { return c.session }
func (c *testInvocationContext) InvocationID() string        { return c.invocationID }
func (c *testInvocationContext) Branch() string              { return "" }
func (c *testInvocationContext) UserContent() *genai.Content { return c.userContent }
func (c *testInvocationContext) RunConfig() *agent.RunConfig { return nil }
func (c *testInvocationContext) EndInvocation()              { c.ended = true }
func (c *testInvocationContext) Ended() bool                 { return c.ended }
func (c *testInvocationContext) WithContext(ctx context.Context) agent.InvocationContext {
	cloned := *c
	cloned.Context = ctx
	return &cloned
}
