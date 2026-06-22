package observability

import (
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func TestTraceRegistryScopesToolCallMappingByADKTrace(t *testing.T) {
	registry := &TraceRegistry{
		adkTraceToVeadkTraceMap: make(map[trace.TraceID]*traceInfos),
	}
	adkTrace1 := mustTraceID(t, "11111111111111111111111111111111")
	adkTrace2 := mustTraceID(t, "22222222222222222222222222222222")
	parent1 := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: mustTraceID(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		SpanID:  mustSpanID(t, "aaaaaaaaaaaaaaaa"),
	})
	parent2 := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: mustTraceID(t, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
		SpanID:  mustSpanID(t, "bbbbbbbbbbbbbbbb"),
	})

	registry.RegisterToolCallMapping("call-1", adkTrace1, parent1)
	registry.RegisterToolCallMapping("call-1", adkTrace2, parent2)

	if got, ok := registry.GetVeadkParentContextByToolCallID(adkTrace1, "call-1"); !ok || got.SpanID() != parent1.SpanID() {
		t.Fatalf("trace1 mapping = %v ok=%v, want parent1", got, ok)
	}
	if got, ok := registry.GetVeadkParentContextByToolCallID(adkTrace2, "call-1"); !ok || got.SpanID() != parent2.SpanID() {
		t.Fatalf("trace2 mapping = %v ok=%v, want parent2", got, ok)
	}
	if got, ok := registry.GetVeadkParentContextByToolCallID(trace.TraceID{}, "call-1"); ok {
		t.Fatalf("invalid trace fallback returned %v, want no bare tool_call_id lookup", got)
	}
}

func TestTraceRegistryCleanupRemovesTraceScopedToolCalls(t *testing.T) {
	registry := &TraceRegistry{
		adkTraceToVeadkTraceMap: make(map[trace.TraceID]*traceInfos),
	}
	adkTrace := mustTraceID(t, "33333333333333333333333333333333")
	parent := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: mustTraceID(t, "cccccccccccccccccccccccccccccccc"),
		SpanID:  mustSpanID(t, "cccccccccccccccc"),
	})

	registry.RegisterToolCallMapping("call-1", adkTrace, parent)
	if _, ok := registry.GetVeadkParentContextByToolCallID(adkTrace, "call-1"); !ok {
		t.Fatal("mapping missing before cleanup")
	}

	registry.cleanupTraceResources(adkTrace)

	if got, ok := registry.GetVeadkParentContextByToolCallID(adkTrace, "call-1"); ok {
		t.Fatalf("mapping after cleanup = %v, want removed", got)
	}
}

func TestTraceRegistryLinkedToolTraceCleanedWithOwner(t *testing.T) {
	registry := &TraceRegistry{
		adkTraceToVeadkTraceMap: make(map[trace.TraceID]*traceInfos),
	}
	ownerTrace := mustTraceID(t, "44444444444444444444444444444444")
	toolTrace := mustTraceID(t, "55555555555555555555555555555555")
	parent := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: mustTraceID(t, "dddddddddddddddddddddddddddddddd"),
		SpanID:  mustSpanID(t, "dddddddddddddddd"),
	})

	registry.RegisterToolCallMapping("call-1", ownerTrace, parent)
	gotParent, gotOwner, ok := registry.ResolveToolCallParent(toolTrace, "call-1")
	if !ok || gotParent.SpanID() != parent.SpanID() || gotOwner != ownerTrace {
		t.Fatalf("ResolveToolCallParent = %v owner=%s ok=%v, want parent and owner trace", gotParent, gotOwner, ok)
	}

	registry.RegisterLinkedTraceMapping(toolTrace, parent.TraceID(), gotOwner)
	if got, ok := registry.GetVeadkTraceID(toolTrace); !ok || got != parent.TraceID() {
		t.Fatalf("linked trace mapping = %s ok=%v, want veadk trace", got, ok)
	}

	registry.cleanupTraceResources(ownerTrace)
	if got, ok := registry.GetVeadkTraceID(toolTrace); ok {
		t.Fatalf("linked trace mapping after owner cleanup = %s, want removed", got)
	}
}
