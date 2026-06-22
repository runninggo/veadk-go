package observability

import (
	"context"
	"testing"

	"github.com/volcengine/veadk-go/configs"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
	"google.golang.org/genai"
)

func TestBeforeRunCreatesInternalInvocationSpan(t *testing.T) {
	resetGlobalRegistryForTest(t)

	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	oldProvider := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
		otel.SetTracerProvider(oldProvider)
	})

	rootCtx, rootSpan := otel.Tracer("http-test").Start(
		context.Background(),
		"POST /api/runs",
		oteltrace.WithSpanKind(oteltrace.SpanKindServer),
	)

	state := newTestState()
	invocationCtx := &testInvocationContext{
		Context: rootCtx,
		session: &testSession{
			id:     "session-1",
			app:    "tls-copilot",
			user:   "user-1",
			state:  state,
			events: testEvents{},
		},
		invocationID: "invocation-1",
		userContent:  genai.NewContentFromText("check trace", genai.RoleUser),
	}
	plugin := &adkObservabilityPlugin{
		config: &configs.ObservabilityConfig{},
		tracer: otel.Tracer(InstrumentationName),
	}

	if _, err := plugin.BeforeRun(invocationCtx); err != nil {
		t.Fatalf("BeforeRun() error = %v", err)
	}
	rawSpan, err := state.Get(stateKeyInvocationSpan)
	if err != nil {
		t.Fatalf("invocation span not stored: %v", err)
	}
	invocationSpan, ok := rawSpan.(oteltrace.Span)
	if !ok {
		t.Fatalf("invocation span type = %T", rawSpan)
	}

	invocationSpan.End()
	rootSpan.End()

	spans := spansByName(recorder.Ended())
	invocation := spans[SpanInvocation]
	if invocation == nil {
		t.Fatalf("missing %q span; got %v", SpanInvocation, spanNames(recorder.Ended()))
	}
	root := spans["POST /api/runs"]
	if root == nil {
		t.Fatalf("missing HTTP root span; got %v", spanNames(recorder.Ended()))
	}
	if got := invocation.SpanKind(); got != oteltrace.SpanKindInternal {
		t.Fatalf("invocation span kind = %v, want %v", got, oteltrace.SpanKindInternal)
	}
	if got := invocation.Parent(); got.SpanID() != root.SpanContext().SpanID() || got.TraceID() != root.SpanContext().TraceID() {
		t.Fatalf("invocation parent = %s/%s, want HTTP root %s/%s", got.TraceID(), got.SpanID(), root.SpanContext().TraceID(), root.SpanContext().SpanID())
	}
}
