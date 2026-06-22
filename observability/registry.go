// Copyright (c) 2025 Beijing Volcano Engine Technology Co., Ltd. and/or its affiliates.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package observability

import (
	"sync"
	"time"

	"github.com/volcengine/veadk-go/log"
	"go.opentelemetry.io/otel/trace"
)

// TraceRegistry manages the mapping between ADK-go's spans and VeADK spans.
// It ensures thread-safe access and proper cleanup of resources.
type TraceRegistry struct {
	// managedLLMSpanMap tracks managed call_llm spans by the owner ADK trace and
	// native span id so raw generate_content spans can be re-parented precisely.
	managedLLMSpanMap sync.Map

	// toolCallMap tracks toolCallKey -> *toolCallInfo.
	toolCallMap sync.Map

	// toolCallFallbackMap tracks bare ToolCallID -> parents by ADK trace id.
	// It is used only when ADK emits a tool span under a separate internal
	// trace, and only while the bare ID is unambiguous across active traces.
	toolCallFallbackMap sync.Map

	// activeInvocationSpans tracks active VeADK invocation spans for shutdown flushing.
	activeInvocationSpans sync.Map

	// adkTraceToVeadkTraceMap tracks internal TraceID -> associated resources for cleanup.
	resourcesMu             sync.RWMutex
	adkTraceToVeadkTraceMap map[trace.TraceID]*traceInfos

	// cleanupQueue receives cleanup requests.
	cleanupQueue chan cleanupRequest

	// shutdownChan signals the cleanup loop to exit.
	shutdownChan chan struct{}
}

const (
	traceCleanupQueueSize = 512
	traceCleanupDelay     = 2 * time.Minute
	traceCleanupTick      = 10 * time.Second
)

type cleanupRequest struct {
	adkTraceID  trace.TraceID
	veadkSpanID trace.SpanID
	deadline    time.Time
}

type toolCallInfo struct {
	mu       sync.RWMutex
	parentSC trace.SpanContext
}

type toolCallKey struct {
	adkTraceID trace.TraceID
	toolCallID string
}

type toolCallFallbackInfo struct {
	mu      sync.RWMutex
	parents map[trace.TraceID]trace.SpanContext
}

type managedLLMSpanKey struct {
	ownerAdkTraceID trace.TraceID
	llmSpanID       trace.SpanID
}

type managedLLMSpanInfo struct {
	mu        sync.RWMutex
	spanSC    trace.SpanContext
	startTime time.Time
	endTime   time.Time
}

type traceInfos struct {
	veadkTraceID       trace.TraceID
	invocationSC       trace.SpanContext
	toolCallKeys       []toolCallKey
	toolCallIDs        []string
	managedLLMSpanKeys []managedLLMSpanKey
	linkedTraces       []trace.TraceID
}

var (
	// globalRegistry is the singleton instance of TraceRegistry.
	globalRegistry *TraceRegistry
	once           sync.Once
)

// GetRegistry returns the global TraceRegistry.
func GetRegistry() *TraceRegistry {
	once.Do(func() {
		globalRegistry = &TraceRegistry{
			adkTraceToVeadkTraceMap: make(map[trace.TraceID]*traceInfos),
			cleanupQueue:            make(chan cleanupRequest, traceCleanupQueueSize),
			shutdownChan:            make(chan struct{}),
		}
		go globalRegistry.cleanupLoop()
	})
	return globalRegistry
}

// Shutdown stops the cleanup loop and closes the shutdown channel.
func (r *TraceRegistry) Shutdown() {
	select {
	case <-r.shutdownChan:
	default:
		close(r.shutdownChan)
	}
}

func (r *TraceRegistry) cleanupLoop() {
	ticker := time.NewTicker(traceCleanupTick)
	defer ticker.Stop()

	var pendingRequests []cleanupRequest

	for {
		select {
		case <-r.shutdownChan:
			return
		case req := <-r.cleanupQueue:
			pendingRequests = append(pendingRequests, req)
		case <-ticker.C:
			pendingRequests = r.cleanupExpiredRequests(pendingRequests, time.Now())
		}
	}
}

func (r *TraceRegistry) cleanupExpiredRequests(pending []cleanupRequest, now time.Time) []cleanupRequest {
	activeRequests := pending[:0]
	for _, req := range pending {
		if now.After(req.deadline) {
			r.cleanupByTraceID(req.adkTraceID, req.veadkSpanID)
			continue
		}
		activeRequests = append(activeRequests, req)
	}
	return activeRequests
}

func (r *TraceRegistry) cleanupByTraceID(adkTraceID trace.TraceID, veadkSpanID trace.SpanID) {
	r.activeInvocationSpans.Delete(veadkSpanID)
	r.cleanupTraceResources(adkTraceID)
}

func (r *TraceRegistry) getOrCreateTraceInfos(adkTraceID trace.TraceID) *traceInfos {
	r.resourcesMu.Lock()
	defer r.resourcesMu.Unlock()

	if res, ok := r.adkTraceToVeadkTraceMap[adkTraceID]; ok {
		return res
	}
	res := &traceInfos{}
	r.adkTraceToVeadkTraceMap[adkTraceID] = res
	return res
}

// RegisterInvocationSpan tracks a live invocation span for shutdown flushing.
func (r *TraceRegistry) RegisterInvocationSpan(veadkSpan trace.Span) {
	if veadkSpan == nil || !veadkSpan.SpanContext().IsValid() {
		return
	}
	r.activeInvocationSpans.Store(veadkSpan.SpanContext().SpanID(), veadkSpan)
}

func (r *TraceRegistry) getOrCreateManagedLLMSpanInfo(key managedLLMSpanKey) *managedLLMSpanInfo {
	value, _ := r.managedLLMSpanMap.LoadOrStore(key, &managedLLMSpanInfo{})
	if info, ok := value.(*managedLLMSpanInfo); ok {
		return info
	}
	info := &managedLLMSpanInfo{}
	r.managedLLMSpanMap.Store(key, info)
	return info
}

// RegisterManagedLLMSpan records a managed call_llm span window for later
// generate_content parent resolution.
func (r *TraceRegistry) RegisterManagedLLMSpan(ownerAdkTraceID trace.TraceID, llmSC trace.SpanContext, startTime time.Time) {
	if !ownerAdkTraceID.IsValid() || !llmSC.IsValid() {
		return
	}
	key := managedLLMSpanKey{ownerAdkTraceID: ownerAdkTraceID, llmSpanID: llmSC.SpanID()}
	info := r.getOrCreateManagedLLMSpanInfo(key)
	info.mu.Lock()
	info.spanSC = llmSC
	info.startTime = startTime
	info.endTime = time.Time{}
	info.mu.Unlock()

	res := r.getOrCreateTraceInfos(ownerAdkTraceID)
	r.resourcesMu.Lock()
	res.managedLLMSpanKeys = append(res.managedLLMSpanKeys, key)
	r.resourcesMu.Unlock()
}

// FinishManagedLLMSpan closes the lifecycle window of a managed call_llm span.
func (r *TraceRegistry) FinishManagedLLMSpan(ownerAdkTraceID trace.TraceID, llmSpanID trace.SpanID, endTime time.Time) {
	if !ownerAdkTraceID.IsValid() || !llmSpanID.IsValid() {
		return
	}
	key := managedLLMSpanKey{ownerAdkTraceID: ownerAdkTraceID, llmSpanID: llmSpanID}
	value, ok := r.managedLLMSpanMap.Load(key)
	if !ok {
		return
	}
	info, ok := value.(*managedLLMSpanInfo)
	if !ok {
		return
	}
	info.mu.Lock()
	if info.endTime.IsZero() || info.endTime.Before(endTime) {
		info.endTime = endTime
	}
	info.mu.Unlock()
}

func (r *TraceRegistry) getOrCreateToolCallInfo(key toolCallKey) *toolCallInfo {
	value, _ := r.toolCallMap.LoadOrStore(key, &toolCallInfo{})
	if info, ok := value.(*toolCallInfo); ok {
		return info
	}
	info := &toolCallInfo{}
	r.toolCallMap.Store(key, info)
	return info
}

// RegisterToolCallMapping links a logical tool call ID to its parent LLM span context.
func (r *TraceRegistry) RegisterToolCallMapping(toolCallID string, adkTraceID trace.TraceID, veadkParentSC trace.SpanContext) {
	if toolCallID == "" || !adkTraceID.IsValid() || !veadkParentSC.IsValid() {
		return
	}
	key := toolCallKey{adkTraceID: adkTraceID, toolCallID: toolCallID}
	info := r.getOrCreateToolCallInfo(key)
	info.mu.Lock()
	info.parentSC = veadkParentSC
	info.mu.Unlock()

	res := r.getOrCreateTraceInfos(adkTraceID)
	r.resourcesMu.Lock()
	res.toolCallKeys = append(res.toolCallKeys, key)
	res.toolCallIDs = append(res.toolCallIDs, toolCallID)
	r.resourcesMu.Unlock()

	r.registerToolCallFallback(toolCallID, adkTraceID, veadkParentSC)
}

// ResolveToolCallParent returns the veadk parent LLM span and the owner ADK trace
// that should own any derived tool trace aliases.
func (r *TraceRegistry) ResolveToolCallParent(adkTraceID trace.TraceID, toolCallID string) (trace.SpanContext, trace.TraceID, bool) {
	if toolCallID == "" {
		return trace.SpanContext{}, trace.TraceID{}, false
	}
	if adkTraceID.IsValid() {
		if sc, ok := r.getToolCallParent(toolCallKey{adkTraceID: adkTraceID, toolCallID: toolCallID}); ok {
			return sc, adkTraceID, true
		}
	}
	return r.getUnambiguousToolCallFallback(toolCallID)
}

func (r *TraceRegistry) getToolCallParent(key toolCallKey) (trace.SpanContext, bool) {
	if val, ok := r.toolCallMap.Load(key); ok {
		info, ok := val.(*toolCallInfo)
		if !ok {
			return trace.SpanContext{}, false
		}
		info.mu.RLock()
		defer info.mu.RUnlock()
		if info.parentSC.IsValid() {
			return info.parentSC, true
		}
	}
	return trace.SpanContext{}, false
}

func (r *TraceRegistry) registerToolCallFallback(toolCallID string, adkTraceID trace.TraceID, parent trace.SpanContext) {
	if toolCallID == "" || !adkTraceID.IsValid() || !parent.IsValid() {
		return
	}
	raw, _ := r.toolCallFallbackMap.LoadOrStore(toolCallID, &toolCallFallbackInfo{parents: map[trace.TraceID]trace.SpanContext{}})
	info, ok := raw.(*toolCallFallbackInfo)
	if !ok {
		info = &toolCallFallbackInfo{parents: map[trace.TraceID]trace.SpanContext{}}
		r.toolCallFallbackMap.Store(toolCallID, info)
	}
	info.mu.Lock()
	info.parents[adkTraceID] = parent
	info.mu.Unlock()
}

func (r *TraceRegistry) unregisterToolCallFallback(toolCallID string, adkTraceID trace.TraceID) {
	if toolCallID == "" || !adkTraceID.IsValid() {
		return
	}
	raw, ok := r.toolCallFallbackMap.Load(toolCallID)
	if !ok {
		return
	}
	info, ok := raw.(*toolCallFallbackInfo)
	if !ok {
		return
	}
	info.mu.Lock()
	delete(info.parents, adkTraceID)
	empty := len(info.parents) == 0
	info.mu.Unlock()
	if empty {
		r.toolCallFallbackMap.Delete(toolCallID)
	}
}

func (r *TraceRegistry) getUnambiguousToolCallFallback(toolCallID string) (trace.SpanContext, trace.TraceID, bool) {
	raw, ok := r.toolCallFallbackMap.Load(toolCallID)
	if !ok {
		return trace.SpanContext{}, trace.TraceID{}, false
	}
	info, ok := raw.(*toolCallFallbackInfo)
	if !ok {
		return trace.SpanContext{}, trace.TraceID{}, false
	}
	info.mu.RLock()
	defer info.mu.RUnlock()
	if len(info.parents) != 1 {
		return trace.SpanContext{}, trace.TraceID{}, false
	}
	for ownerAdkTraceID, parent := range info.parents {
		if parent.IsValid() {
			return parent, ownerAdkTraceID, true
		}
	}
	return trace.SpanContext{}, trace.TraceID{}, false
}

// GetVeadkParentContextByToolCallID finds the veadk parent for a tool span by its logical ToolCallID.
func (r *TraceRegistry) GetVeadkParentContextByToolCallID(adkTraceID trace.TraceID, toolCallID string) (trace.SpanContext, bool) {
	parent, _, ok := r.ResolveToolCallParent(adkTraceID, toolCallID)
	return parent, ok
}

// ResolveManagedLLMParent finds the managed call_llm span whose lifecycle
// window encloses the raw generate_content span for the same ADK trace. When
// several candidates match, the most recent one wins.
func (r *TraceRegistry) ResolveManagedLLMParent(adkTraceID trace.TraceID, rawStart, rawEnd time.Time) (trace.SpanContext, bool) {
	if !adkTraceID.IsValid() || rawStart.IsZero() {
		return trace.SpanContext{}, false
	}

	r.resourcesMu.RLock()
	res, ok := r.adkTraceToVeadkTraceMap[adkTraceID]
	if !ok || len(res.managedLLMSpanKeys) == 0 {
		r.resourcesMu.RUnlock()
		return trace.SpanContext{}, false
	}
	keys := append([]managedLLMSpanKey(nil), res.managedLLMSpanKeys...)
	r.resourcesMu.RUnlock()

	bestStart := time.Time{}
	bestSC := trace.SpanContext{}
	for _, key := range keys {
		value, ok := r.managedLLMSpanMap.Load(key)
		if !ok {
			continue
		}
		info, ok := value.(*managedLLMSpanInfo)
		if !ok {
			continue
		}
		info.mu.RLock()
		spanSC := info.spanSC
		startTime := info.startTime
		endTime := info.endTime
		info.mu.RUnlock()

		if !spanSC.IsValid() || startTime.IsZero() || startTime.After(rawStart) {
			continue
		}
		if !endTime.IsZero() {
			if rawEnd.IsZero() {
				if endTime.Before(rawStart) {
					continue
				}
			} else if endTime.Before(rawEnd) {
				continue
			}
		}
		if bestSC.IsValid() && !bestStart.Before(startTime) {
			continue
		}
		bestStart = startTime
		bestSC = spanSC
	}
	return bestSC, bestSC.IsValid()
}

// RegisterTraceMapping records a mapping from an internal ADK TraceID to a VeADK TraceID.
func (r *TraceRegistry) RegisterTraceMapping(adkTraceID trace.TraceID, veadkTraceID trace.TraceID) {
	r.RegisterLinkedTraceMapping(adkTraceID, veadkTraceID, trace.TraceID{})
}

// RegisterLinkedTraceMapping records a TraceID alias and ties its lifecycle to
// the owner ADK trace. ADK may emit tool spans under a separate internal trace;
// linking keeps those translated spans aligned without leaving unmanaged
// registry entries behind.
func (r *TraceRegistry) RegisterLinkedTraceMapping(adkTraceID trace.TraceID, veadkTraceID trace.TraceID, ownerAdkTraceID trace.TraceID) {
	if !adkTraceID.IsValid() || !veadkTraceID.IsValid() {
		return
	}

	r.resourcesMu.Lock()
	defer r.resourcesMu.Unlock()

	res, ok := r.adkTraceToVeadkTraceMap[adkTraceID]
	if !ok {
		res = &traceInfos{}
		r.adkTraceToVeadkTraceMap[adkTraceID] = res
	}
	res.veadkTraceID = veadkTraceID

	if ownerAdkTraceID.IsValid() && ownerAdkTraceID != adkTraceID {
		if owner, ok := r.adkTraceToVeadkTraceMap[ownerAdkTraceID]; ok && !traceIDSliceContains(owner.linkedTraces, adkTraceID) {
			owner.linkedTraces = append(owner.linkedTraces, adkTraceID)
		}
	}
}

// GetVeadkTraceID finds the veadk TraceID for an internal TraceID.
func (r *TraceRegistry) GetVeadkTraceID(adkTraceID trace.TraceID) (trace.TraceID, bool) {
	r.resourcesMu.RLock()
	defer r.resourcesMu.RUnlock()

	if res, ok := r.adkTraceToVeadkTraceMap[adkTraceID]; ok {
		return res.veadkTraceID, res.veadkTraceID.IsValid()
	}
	return trace.TraceID{}, false
}

// RegisterInvocationSpanContext links an ADK TraceID to a VeADK invocation span context.
// This allows us to set the invoke_agent span's parent to our invocation span in the translator.
func (r *TraceRegistry) RegisterInvocationSpanContext(adkTraceID trace.TraceID, invocationSC trace.SpanContext) {
	if !adkTraceID.IsValid() || !invocationSC.IsValid() {
		return
	}
	res := r.getOrCreateTraceInfos(adkTraceID)
	r.resourcesMu.Lock()
	res.invocationSC = invocationSC
	r.resourcesMu.Unlock()
}

// GetInvocationSpanContext gets the VeADK invocation span context for an ADK TraceID.
func (r *TraceRegistry) GetInvocationSpanContext(adkTraceID trace.TraceID) (trace.SpanContext, bool) {
	r.resourcesMu.RLock()
	defer r.resourcesMu.RUnlock()
	if res, ok := r.adkTraceToVeadkTraceMap[adkTraceID]; ok && res.invocationSC.IsValid() {
		return res.invocationSC, true
	}
	return trace.SpanContext{}, false
}

// ScheduleCleanup schedules cleanup of all mappings related to an internal TraceID.
// This is typically called when the trace is considered complete.
func (r *TraceRegistry) ScheduleCleanup(adkTraceID trace.TraceID, veadkSpanID trace.SpanID) {
	select {
	case r.cleanupQueue <- cleanupRequest{
		adkTraceID:  adkTraceID,
		veadkSpanID: veadkSpanID,
		deadline:    time.Now().Add(traceCleanupDelay),
	}:
	default:
		log.Warn("trace cleanup queue is full")
	}
}

func (r *TraceRegistry) cleanupTraceResources(adkTraceID trace.TraceID) {
	r.resourcesMu.Lock()
	defer r.resourcesMu.Unlock()
	r.cleanupTraceResourcesLocked(adkTraceID)
}

func (r *TraceRegistry) cleanupTraceResourcesLocked(adkTraceID trace.TraceID) {
	res, ok := r.adkTraceToVeadkTraceMap[adkTraceID]
	if !ok {
		return
	}
	linkedTraces := append([]trace.TraceID(nil), res.linkedTraces...)
	for _, key := range res.managedLLMSpanKeys {
		r.managedLLMSpanMap.Delete(key)
	}
	for _, key := range res.toolCallKeys {
		r.toolCallMap.Delete(key)
	}
	for _, id := range res.toolCallIDs {
		r.unregisterToolCallFallback(id, adkTraceID)
	}
	delete(r.adkTraceToVeadkTraceMap, adkTraceID)
	for _, linkedTraceID := range linkedTraces {
		r.cleanupTraceResourcesLocked(linkedTraceID)
	}
}

func traceIDSliceContains(ids []trace.TraceID, target trace.TraceID) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}

// EndAllInvocationSpans ends all currently active invocation spans.
func (r *TraceRegistry) EndAllInvocationSpans() {
	r.activeInvocationSpans.Range(func(key, value any) bool {
		if span, ok := value.(trace.Span); ok && span.IsRecording() {
			span.End()
		}
		r.activeInvocationSpans.Delete(key)
		return true
	})
}
