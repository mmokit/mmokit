package main

import "math"

// Go reference implementations of AdaptivePlaybackController and
// PredictionBuffer, used ONLY to produce the cross-language golden manifest.
//
// This is deliberately a THIRD implementation of each algorithm, and that is a
// real drift risk. It is contained by mirroring pkg/quantize/ts/*.ts statement
// order literally — every block below is in the same order as the TypeScript
// it reproduces — and by never being used outside golden generation. The same
// discipline as clockSyncRef in main.go.

// ---------------------------------------------------------------------------
// Manifest DTOs
// ---------------------------------------------------------------------------

// PlaybackCase pins AdaptivePlaybackController across Go, TS and C#. Each step
// feeds one frame observation and optionally samples renderTime, recording
// every value both ports must reproduce.
type PlaybackCase struct {
	TickIntervalMs    float64        `json:"tickIntervalMs"`
	MinDelayMs        float64        `json:"minDelayMs"`
	MaxDelayMs        float64        `json:"maxDelayMs"`
	MinPlaybackRate   float64        `json:"minPlaybackRate"`
	MaxPlaybackRate   float64        `json:"maxPlaybackRate"`
	ConvergenceWindow float64        `json:"convergenceWindowMs"`
	AttackFactor      float64        `json:"attackFactor"`
	DecayFactor       float64        `json:"decayFactor"`
	JitterFactor      float64        `json:"jitterFactor"`
	Steps             []PlaybackStep `json:"steps"`
}

// PlaybackStep is one observeFrame call plus the metrics that must follow it.
// RenderAtMs, when HasRender is set, additionally samples renderTime.
type PlaybackStep struct {
	Note string `json:"note"`

	Seq             uint32  `json:"seq"`
	FreshSnapshot   bool    `json:"freshSnapshot"`
	HasStreamChange bool    `json:"hasStreamChanged"`
	StreamChanged   bool    `json:"streamChanged"`
	ArrivalTimeMs   float64 `json:"arrivalTimeMs"`
	HasProducedAt   bool    `json:"hasProducedAt"`
	ProducedAtMs    float64 `json:"producedAtMs"`

	ExpectedTargetDelayMs float64 `json:"expectedTargetDelayMs"`
	ExpectedJitterMs      float64 `json:"expectedJitterMs"`
	ExpectedExcessDelayMs float64 `json:"expectedExcessDelayMs"`
	ExpectedLossRate      float64 `json:"expectedLossRate"`
	ExpectedReceived      int     `json:"expectedReceivedFrames"`
	ExpectedLost          int     `json:"expectedLostFrames"`
	ExpectedDuplicate     int     `json:"expectedDuplicateFrames"`
	ExpectedOutOfOrder    int     `json:"expectedOutOfOrderFrames"`

	HasRender            bool    `json:"hasRender"`
	RenderClientNowMs    float64 `json:"renderClientNowMs"`
	ExpectedRenderNull   bool    `json:"expectedRenderNull"`
	ExpectedRenderTimeMs float64 `json:"expectedRenderTimeMs"`
	ExpectedPlaybackRate float64 `json:"expectedPlaybackRate"`
	ExpectedCurrentDelay float64 `json:"expectedCurrentDelayMs"`
}

// PredictionCase pins PredictionBuffer's push/acknowledge/reconcile semantics,
// including the uint32 wrap and capacity overflow.
type PredictionCase struct {
	MaxPending int              `json:"maxPending"`
	Steps      []PredictionStep `json:"steps"`
}

// PredictionStep is one buffer operation. Op is "push", "acknowledge",
// "reconcile" or "reset".
type PredictionStep struct {
	Note string `json:"note"`
	Op   string `json:"op"`

	Seq   uint32 `json:"seq"`
	Input int    `json:"input"`
	// HasSeq distinguishes reset() from reset(ack).
	HasSeq bool `json:"hasSeq"`
	// State is the authoritative state handed to reconcile; replay adds each
	// pending input value to it, which is enough to prove replay order.
	State int `json:"state"`

	ExpectedAccepted     bool   `json:"expectedAccepted"`
	ExpectedDropped      bool   `json:"expectedDropped"`
	ExpectedDroppedSeq   uint32 `json:"expectedDroppedSeq"`
	ExpectedAdvanced     bool   `json:"expectedAdvanced"`
	ExpectedAcknowledged int    `json:"expectedAcknowledgedCount"`
	ExpectedPending      int    `json:"expectedPendingCount"`
	ExpectedOverflow     int    `json:"expectedOverflowCount"`
	ExpectedState        int    `json:"expectedState"`
	ExpectedHasLastAck   bool   `json:"expectedHasLastAck"`
	ExpectedLastAck      uint32 `json:"expectedLastAck"`
}

// ---------------------------------------------------------------------------
// AdaptivePlaybackController reference
// ---------------------------------------------------------------------------

const uint32HalfRange = float64(0x80000000)

func clampF(v, lo, hi float64) float64 { return math.Min(hi, math.Max(lo, v)) }

// sequenceDistanceRef mirrors playback-controller.ts sequenceDistance.
func sequenceDistanceRef(from, to uint32) uint32 { return to - from }

type playbackRef struct {
	clock             clockSyncRef
	tickIntervalMs    float64
	minDelayMs        float64
	maxDelayMs        float64
	minPlaybackRate   float64
	maxPlaybackRate   float64
	convergenceWindow float64
	attackFactor      float64
	decayFactor       float64
	jitterFactor      float64

	targetDelay      float64
	currentDelay     float64
	jitter           float64
	excessDelay      float64
	prevRawExcess    float64
	hasPrevRawExcess bool
	lossPressureMs   float64

	lastSequence    uint32
	hasLastSequence bool
	received        int
	lost            int
	duplicate       int
	outOfOrder      int

	renderCursor        float64
	hasRenderCursor     bool
	lastRenderClient    float64
	hasLastRenderClient bool
	playbackRate        float64
	reanchorPending     bool
}

func newPlaybackRef() *playbackRef {
	p := &playbackRef{
		clock:             clockSyncRef{window: 40},
		tickIntervalMs:    50,
		minDelayMs:        100,
		maxDelayMs:        300,
		minPlaybackRate:   0.9,
		maxPlaybackRate:   1.1,
		convergenceWindow: 1000,
		attackFactor:      0.5,
		decayFactor:       0.05,
		jitterFactor:      2,
		playbackRate:      1,
	}
	p.targetDelay = p.minDelayMs
	p.currentDelay = p.minDelayMs
	return p
}

func (p *playbackRef) resetClock() {
	p.clock = clockSyncRef{window: p.clock.window}
}

// observeFrame mirrors AdaptivePlaybackController.observeFrame statement for
// statement: stream reset, sequence accounting, loss pressure, clock observe,
// jitter/excess EWMA, target EWMA.
func (p *playbackRef) observeFrame(seq uint32, freshSnapshot bool, hasStreamChanged, streamChanged bool,
	arrivalTimeMs float64, hasProducedAt bool, producedAtMs float64,
) {
	gap := uint32(0)
	hasProducerStamp := hasProducedAt

	reset := freshSnapshot
	if hasStreamChanged {
		reset = streamChanged
	}
	if reset {
		p.hasLastSequence = false
		p.reanchorPending = true
		if !hasProducerStamp {
			p.resetClock()
			p.hasPrevRawExcess = false
			p.reanchorPending = false
		}
	}

	if !p.hasLastSequence {
		p.lastSequence = seq
		p.hasLastSequence = true
		p.received++
	} else {
		distance := sequenceDistanceRef(p.lastSequence, seq)
		switch {
		case distance == 0:
			p.duplicate++
		case float64(distance) < uint32HalfRange:
			gap = distance - 1
			p.lost += int(gap)
			p.received++
			p.lastSequence = seq
		default:
			p.outOfOrder++
		}
	}

	if gap > 0 {
		p.lossPressureMs = math.Max(p.lossPressureMs,
			math.Min(p.maxDelayMs-p.minDelayMs, float64(gap)*p.tickIntervalMs))
	} else {
		p.lossPressureMs *= 0.85
	}

	if hasProducerStamp {
		if p.reanchorPending {
			p.resetClock()
			p.hasPrevRawExcess = false
			p.reanchorPending = false
		}
		p.clock.observe(producedAtMs, arrivalTimeMs)

		instant := producedAtMs - arrivalTimeMs
		rawExcess := math.Max(0, p.clock.offset-instant)
		if p.hasPrevRawExcess {
			variation := math.Abs(rawExcess - p.prevRawExcess)
			p.jitter += (variation - p.jitter) / 8
		}
		p.prevRawExcess = rawExcess
		p.hasPrevRawExcess = true

		excessFactor := p.decayFactor
		if rawExcess > p.excessDelay {
			excessFactor = p.attackFactor
		}
		p.excessDelay += excessFactor * (rawExcess - p.excessDelay)
	}

	rawTarget := clampF(
		p.minDelayMs+p.excessDelay+p.jitterFactor*p.jitter+p.lossPressureMs,
		p.minDelayMs, p.maxDelayMs)
	targetFactor := p.decayFactor
	if rawTarget > p.targetDelay {
		targetFactor = p.attackFactor
	}
	p.targetDelay = clampF(p.targetDelay+targetFactor*(rawTarget-p.targetDelay),
		p.minDelayMs, p.maxDelayMs)
}

// renderTime mirrors AdaptivePlaybackController.renderTime. ok=false is the
// TypeScript null return.
func (p *playbackRef) renderTime(clientNowMs float64) (float64, bool) {
	if !p.clock.initialized {
		return 0, false
	}
	serverNowMs := clientNowMs + p.clock.offset
	if !p.hasRenderCursor || !p.hasLastRenderClient {
		p.renderCursor = serverNowMs - p.targetDelay
		p.hasRenderCursor = true
		p.lastRenderClient = clientNowMs
		p.hasLastRenderClient = true
		p.currentDelay = p.targetDelay
		p.playbackRate = 1
		return p.renderCursor, true
	}

	elapsedMs := math.Max(0, clientNowMs-p.lastRenderClient)
	baselineDelay := serverNowMs - (p.renderCursor + elapsedMs)
	delayError := baselineDelay - p.targetDelay
	requestedRate := clampF(1+delayError/p.convergenceWindow, p.minPlaybackRate, p.maxPlaybackRate)

	requestedAdvance := elapsedMs * requestedRate
	availableAdvance := math.Max(0, serverNowMs-p.renderCursor)
	appliedAdvance := math.Min(requestedAdvance, availableAdvance)
	p.renderCursor += appliedAdvance
	if elapsedMs > 0 {
		p.playbackRate = appliedAdvance / elapsedMs
	} else {
		p.playbackRate = requestedRate
	}
	if clientNowMs > p.lastRenderClient {
		p.lastRenderClient = clientNowMs
	}
	p.currentDelay = math.Max(0, serverNowMs-p.renderCursor)
	return p.renderCursor, true
}

func (p *playbackRef) lossRate() float64 {
	delivered := p.received + p.lost
	if delivered == 0 {
		return 0
	}
	return float64(p.lost) / float64(delivered)
}

// buildPlaybackCase drives the reference through a fixed script covering:
// steady 20 Hz delivery, a delayed burst with a sequence gap, a duplicate, a
// reorder, a uint32 wrap, an empty (no producer stamp) stream switch, and a
// downward clock step. Each step records what both ports must reproduce.
func buildPlaybackCase() PlaybackCase {
	p := newPlaybackRef()
	c := PlaybackCase{
		TickIntervalMs:    p.tickIntervalMs,
		MinDelayMs:        p.minDelayMs,
		MaxDelayMs:        p.maxDelayMs,
		MinPlaybackRate:   p.minPlaybackRate,
		MaxPlaybackRate:   p.maxPlaybackRate,
		ConvergenceWindow: p.convergenceWindow,
		AttackFactor:      p.attackFactor,
		DecayFactor:       p.decayFactor,
		JitterFactor:      p.jitterFactor,
	}

	type scriptStep struct {
		note            string
		seq             uint32
		fresh           bool
		hasStreamChange bool
		streamChanged   bool
		arrival         float64
		hasProduced     bool
		produced        float64
		render          bool
		renderAt        float64
	}

	script := []scriptStep{
		{note: "first frame initializes the clock", seq: 1, fresh: true, arrival: 1000, hasProduced: true, produced: 6000, render: true, renderAt: 1000},
		{note: "steady 20Hz", seq: 2, arrival: 1050, hasProduced: true, produced: 6050, render: true, renderAt: 1050},
		{note: "steady 20Hz", seq: 3, arrival: 1100, hasProduced: true, produced: 6100, render: true, renderAt: 1100},
		{note: "delayed burst: arrival lags, producer keeps cadence", seq: 4, arrival: 1230, hasProduced: true, produced: 6150, render: true, renderAt: 1230},
		{note: "burst tail: same arrival, later stamp", seq: 5, arrival: 1230, hasProduced: true, produced: 6200, render: true, renderAt: 1235},
		{note: "sequence gap of 2 raises loss pressure", seq: 8, arrival: 1300, hasProduced: true, produced: 6350, render: true, renderAt: 1300},
		{note: "duplicate of the newest sequence", seq: 8, arrival: 1305, hasProduced: true, produced: 6350, render: true, renderAt: 1305},
		{note: "reordered older frame", seq: 6, arrival: 1310, hasProduced: true, produced: 6250, render: true, renderAt: 1310},
		{note: "recovery: healthy delivery decays loss pressure", seq: 9, arrival: 1350, hasProduced: true, produced: 6400, render: true, renderAt: 1350},
		{note: "recovery", seq: 10, arrival: 1400, hasProduced: true, produced: 6450, render: true, renderAt: 1400},
		{note: "ACK-only stream switch with no producer stamp resets the clock", seq: 100, hasStreamChange: true, streamChanged: true, arrival: 1450, render: true, renderAt: 1450},
		{note: "new stream's first stamped frame re-anchors", seq: 101, arrival: 1500, hasProduced: true, produced: 9000, render: true, renderAt: 1500},
		{note: "new stream steady", seq: 102, arrival: 1550, hasProduced: true, produced: 9050, render: true, renderAt: 1550},
		{note: "downward clock step: producer stamp jumps back", seq: 103, arrival: 1600, hasProduced: true, produced: 8800, render: true, renderAt: 1600},
		{note: "uint32 wrap: near-max sequence then zero", seq: 0xfffffffe, fresh: true, arrival: 1650, hasProduced: true, produced: 8850, render: true, renderAt: 1650},
		{note: "wrap forward across zero", seq: 0, arrival: 1700, hasProduced: true, produced: 8900, render: true, renderAt: 1700},
		{note: "post-wrap continuity", seq: 1, arrival: 1750, hasProduced: true, produced: 8950, render: true, renderAt: 1750},
	}

	for _, s := range script {
		p.observeFrame(s.seq, s.fresh, s.hasStreamChange, s.streamChanged, s.arrival, s.hasProduced, s.produced)
		step := PlaybackStep{
			Note:                  s.note,
			Seq:                   s.seq,
			FreshSnapshot:         s.fresh,
			HasStreamChange:       s.hasStreamChange,
			StreamChanged:         s.streamChanged,
			ArrivalTimeMs:         s.arrival,
			HasProducedAt:         s.hasProduced,
			ProducedAtMs:          s.produced,
			ExpectedTargetDelayMs: p.targetDelay,
			ExpectedJitterMs:      p.jitter,
			ExpectedExcessDelayMs: p.excessDelay,
			ExpectedLossRate:      p.lossRate(),
			ExpectedReceived:      p.received,
			ExpectedLost:          p.lost,
			ExpectedDuplicate:     p.duplicate,
			ExpectedOutOfOrder:    p.outOfOrder,
		}
		if s.render {
			step.HasRender = true
			step.RenderClientNowMs = s.renderAt
			rt, ok := p.renderTime(s.renderAt)
			step.ExpectedRenderNull = !ok
			step.ExpectedRenderTimeMs = rt
			step.ExpectedPlaybackRate = p.playbackRate
			step.ExpectedCurrentDelay = p.currentDelay
		}
		c.Steps = append(c.Steps, step)
	}
	return c
}

// ---------------------------------------------------------------------------
// PredictionBuffer reference
// ---------------------------------------------------------------------------

type pendingInputRef struct {
	seq   uint32
	input int
}

type predictionRef struct {
	maxPending int
	entries    []pendingInputRef
	newest     uint32
	hasNewest  bool
	lastAck    uint32
	hasLastAck bool
	overflow   int
}

func isForwardSequenceRef(from, to uint32) bool {
	d := sequenceDistanceRef(from, to)
	return d > 0 && float64(d) < uint32HalfRange
}

func isAcknowledgedByRef(sequence, cumulativeAck uint32) bool {
	d := sequenceDistanceRef(sequence, cumulativeAck)
	// Mirrors the TS predicate exactly: distance 0 OR a forward distance.
	return d == 0 || float64(d) < uint32HalfRange
}

// push mirrors PredictionBuffer.push; returns (accepted, dropped, hasDropped).
func (b *predictionRef) push(seq uint32, input int) (bool, pendingInputRef, bool) {
	if (b.hasLastAck && !isForwardSequenceRef(b.lastAck, seq)) ||
		(b.hasNewest && !isForwardSequenceRef(b.newest, seq)) {
		return false, pendingInputRef{}, false
	}
	b.entries = append(b.entries, pendingInputRef{seq: seq, input: input})
	b.newest = seq
	b.hasNewest = true
	if len(b.entries) <= b.maxPending {
		return true, pendingInputRef{}, false
	}
	dropped := b.entries[0]
	b.entries = b.entries[1:]
	b.overflow++
	return true, dropped, true
}

// acknowledge mirrors PredictionBuffer.acknowledge; returns (advanced, count).
func (b *predictionRef) acknowledge(ack uint32) (bool, int) {
	if b.hasLastAck && ack != b.lastAck && !isForwardSequenceRef(b.lastAck, ack) {
		return false, 0
	}
	if b.hasLastAck && ack == b.lastAck {
		return false, 0
	}
	b.lastAck = ack
	b.hasLastAck = true
	count := 0
	for len(b.entries) > 0 && isAcknowledgedByRef(b.entries[0].seq, ack) {
		b.entries = b.entries[1:]
		count++
	}
	return true, count
}

// reconcile mirrors PredictionBuffer.reconcile with replay(state, input) =
// state + input, which is enough to prove replay ORDER as well as membership.
func (b *predictionRef) reconcile(state int, ack uint32) (int, int) {
	_, acknowledged := b.acknowledge(ack)
	for _, e := range b.entries {
		state += e.input
	}
	return state, acknowledged
}

func (b *predictionRef) reset(ack uint32, hasAck bool) {
	b.entries = nil
	b.hasNewest = false
	b.lastAck = ack
	b.hasLastAck = hasAck
	b.overflow = 0
}

// buildPredictionCase scripts push/acknowledge/reconcile across a uint32 wrap
// and a capacity overflow.
func buildPredictionCase() PredictionCase {
	const maxPending = 4
	b := &predictionRef{maxPending: maxPending}
	c := PredictionCase{MaxPending: maxPending}

	record := func(s PredictionStep) {
		s.ExpectedPending = len(b.entries)
		s.ExpectedOverflow = b.overflow
		s.ExpectedHasLastAck = b.hasLastAck
		s.ExpectedLastAck = b.lastAck
		c.Steps = append(c.Steps, s)
	}

	doPush := func(note string, seq uint32, input int) {
		accepted, dropped, hasDropped := b.push(seq, input)
		record(PredictionStep{
			Note: note, Op: "push", Seq: seq, Input: input,
			ExpectedAccepted: accepted, ExpectedDropped: hasDropped,
			ExpectedDroppedSeq: dropped.seq,
		})
	}
	doAck := func(note string, ack uint32) {
		advanced, count := b.acknowledge(ack)
		record(PredictionStep{
			Note: note, Op: "acknowledge", Seq: ack,
			ExpectedAdvanced: advanced, ExpectedAcknowledged: count,
		})
	}
	doReconcile := func(note string, state int, ack uint32) {
		out, count := b.reconcile(state, ack)
		record(PredictionStep{
			Note: note, Op: "reconcile", Seq: ack, State: state,
			ExpectedAcknowledged: count, ExpectedState: out,
		})
	}
	doReset := func(note string, ack uint32, hasAck bool) {
		b.reset(ack, hasAck)
		record(PredictionStep{Note: note, Op: "reset", Seq: ack, HasSeq: hasAck})
	}

	doPush("first input", 1, 10)
	doPush("second input", 2, 20)
	doPush("duplicate sequence is rejected", 2, 99)
	doPush("stale sequence is rejected", 1, 98)
	doPush("third input", 3, 30)
	doReconcile("reconcile with ack=1 replays inputs 2 and 3", 1000, 1)
	doAck("re-acking the same sequence is a no-op", 1)
	doAck("stale ack is a no-op", 0)
	doAck("ack=3 drains the rest", 3)
	doReconcile("reconcile on an empty buffer returns the authoritative state", 2000, 3)

	doPush("fill to capacity", 4, 40)
	doPush("fill to capacity", 5, 50)
	doPush("fill to capacity", 6, 60)
	doPush("fill to capacity", 7, 70)
	doPush("overflow evicts the oldest unacknowledged input", 8, 80)
	doReconcile("replay order after eviction", 3000, 4)

	doReset("reset seeded with a cumulative ack", 0xfffffffe, true)
	doPush("push just before the wrap", 0xffffffff, 1)
	doPush("push across the wrap", 0, 2)
	doPush("post-wrap push", 1, 3)
	doAck("ack across the wrap drains both wrapped entries", 0)
	doReconcile("only the post-wrap input remains", 4000, 0)

	doReset("bare reset clears the ack frontier", 0, false)
	doPush("any sequence is accepted after a bare reset", 500, 5)
	return c
}
