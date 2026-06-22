package observability

import (
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	oteltrace "go.opentelemetry.io/otel/trace"
)

func TestTranslatedGenerateContentSpanUsesMatchingManagedLLMSpan(t *testing.T) {
	registry := resetGlobalRegistryForTest(t)

	adkTraceID := mustTraceID(t, "dddddddddddddddddddddddddddddddd")
	rawParent := oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID: adkTraceID,
		SpanID:  mustSpanID(t, "1111111111111111"),
	})
	llm1 := oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID: adkTraceID,
		SpanID:  mustSpanID(t, "2222222222222222"),
	})
	llm2 := oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID: adkTraceID,
		SpanID:  mustSpanID(t, "3333333333333333"),
	})
	base := time.Date(2026, time.June, 22, 18, 0, 0, 0, time.UTC)
	registry.RegisterManagedLLMSpan(adkTraceID, llm1, base)
	registry.FinishManagedLLMSpan(adkTraceID, llm1.SpanID(), base.Add(100*time.Millisecond))
	registry.RegisterManagedLLMSpan(adkTraceID, llm2, base.Add(200*time.Millisecond))
	registry.FinishManagedLLMSpan(adkTraceID, llm2.SpanID(), base.Add(300*time.Millisecond))

	adkGenerate := tracetest.SpanStub{
		Name: "generate_content doubao-seed-1-6",
		SpanContext: oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
			TraceID: adkTraceID,
			SpanID:  mustSpanID(t, "4444444444444444"),
		}),
		Parent:               rawParent,
		StartTime:            base.Add(10 * time.Millisecond),
		EndTime:              base.Add(90 * time.Millisecond),
		InstrumentationScope: instrumentation.Scope{Name: ADKInstrumentationName},
	}.Snapshot()

	translated := &translatedSpan{ReadOnlySpan: adkGenerate}
	if got := translated.Parent(); got.SpanID() != llm1.SpanID() || got.TraceID() != llm1.TraceID() {
		t.Fatalf("generate_content parent = %s/%s, want matching call_llm %s/%s", got.TraceID(), got.SpanID(), llm1.TraceID(), llm1.SpanID())
	}
}

func TestTranslatedToolSpanPrefersToolCallIDOverRawTraceFallback(t *testing.T) {
	registry := resetGlobalRegistryForTest(t)

	adkTraceID := mustTraceID(t, "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
	rawParent := oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID: mustTraceID(t, "ffffffffffffffffffffffffffffffff"),
		SpanID:  mustSpanID(t, "aaaaaaaaaaaaaaaa"),
	})
	rightParent := oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID: adkTraceID,
		SpanID:  mustSpanID(t, "cccccccccccccccc"),
	})
	registry.RegisterToolCallMapping("call-1", adkTraceID, rightParent)

	adkTool := tracetest.SpanStub{
		Name: "execute_tool terminal_exec",
		SpanContext: oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
			TraceID: mustTraceID(t, "99999999999999999999999999999999"),
			SpanID:  mustSpanID(t, "dddddddddddddddd"),
		}),
		Parent:               rawParent,
		InstrumentationScope: instrumentation.Scope{Name: ADKInstrumentationName},
		Attributes: []attribute.KeyValue{
			attribute.String(AttrGenAIToolCallID, "call-1"),
			attribute.String(AttrGenAIToolName, "terminal_exec"),
		},
	}.Snapshot()

	translated := &translatedSpan{ReadOnlySpan: adkTool}
	if got := translated.Parent(); got.SpanID() != rightParent.SpanID() || got.TraceID() != rightParent.TraceID() {
		t.Fatalf("tool parent = %s/%s, want tool_call_id parent %s/%s", got.TraceID(), got.SpanID(), rightParent.TraceID(), rightParent.SpanID())
	}
}

func TestTranslatedExporterDropsRawADKCallLLMSpan(t *testing.T) {
	span := tracetest.SpanStub{
		Name:                 SpanCallLLM,
		SpanContext:          oteltrace.NewSpanContext(oteltrace.SpanContextConfig{TraceID: mustTraceID(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), SpanID: mustSpanID(t, "1111111111111111")}),
		InstrumentationScope: instrumentation.Scope{Name: ADKInstrumentationName},
	}.Snapshot()

	if isMatch(span) {
		t.Fatal("raw ADK call_llm span should be filtered out")
	}
}
