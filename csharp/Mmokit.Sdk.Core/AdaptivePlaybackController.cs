using System;

namespace Mmokit.Sdk.Core
{
    /// One timing observation per decoded replication frame. Feed empty
    /// frames too so sequence gaps remain meaningful.
    public readonly struct FrameTimingObservation
    {
        public FrameTimingObservation(
            uint sequence,
            bool freshSnapshot,
            double arrivalTimeMs,
            double? producedAtMs = null,
            bool? streamChanged = null)
        {
            Sequence = sequence;
            FreshSnapshot = freshSnapshot;
            ArrivalTimeMs = arrivalTimeMs;
            ProducedAtMs = producedAtMs;
            StreamChanged = streamChanged;
        }

        public uint Sequence { get; }
        public bool FreshSnapshot { get; }
        /// When present, controls sequence reset independently of whether the
        /// frame is a same-stream full recovery snapshot.
        public bool? StreamChanged { get; }
        public double ArrivalTimeMs { get; }
        /// Newest producer stamp in the frame; null for an empty frame.
        public double? ProducedAtMs { get; }
    }

    public sealed class AdaptivePlaybackConfig
    {
        public ClockSync? Clock { get; set; }
        public double? TickIntervalMs { get; set; }
        public double? MinDelayMs { get; set; }
        public double? MaxDelayMs { get; set; }
        public double? MinPlaybackRate { get; set; }
        public double? MaxPlaybackRate { get; set; }
        /// Delay error that requests the full configured rate correction.
        public double? ConvergenceWindowMs { get; set; }
        /// EWMA coefficient used when the target must rise.
        public double? AttackFactor { get; set; }
        /// EWMA coefficient used when the target can fall.
        public double? DecayFactor { get; set; }
        public double? JitterFactor { get; set; }
    }

    public readonly struct PlaybackMetrics
    {
        public PlaybackMetrics(
            double currentDelayMs,
            double targetDelayMs,
            double jitterMs,
            double excessDelayMs,
            long receivedFrames,
            long lostFrames,
            long duplicateFrames,
            long outOfOrderFrames,
            double lossRate,
            double playbackRate,
            double? renderTimeMs)
        {
            CurrentDelayMs = currentDelayMs;
            TargetDelayMs = targetDelayMs;
            JitterMs = jitterMs;
            ExcessDelayMs = excessDelayMs;
            ReceivedFrames = receivedFrames;
            LostFrames = lostFrames;
            DuplicateFrames = duplicateFrames;
            OutOfOrderFrames = outOfOrderFrames;
            LossRate = lossRate;
            PlaybackRate = playbackRate;
            RenderTimeMs = renderTimeMs;
        }

        public double CurrentDelayMs { get; }
        public double TargetDelayMs { get; }
        public double JitterMs { get; }
        public double ExcessDelayMs { get; }
        public long ReceivedFrames { get; }
        public long LostFrames { get; }
        public long DuplicateFrames { get; }
        public long OutOfOrderFrames { get; }
        public double LossRate { get; }
        public double PlaybackRate { get; }
        public double? RenderTimeMs { get; }
    }

    /// <summary>
    /// Connection-level adaptive interpolation timeline.
    ///
    /// It combines producer stamps, arrival variation, and frame-sequence gaps
    /// into a bounded render-delay target. The render cursor converges by
    /// changing playback speed within tight bounds; it never rewinds when a
    /// burst of jitter raises the target. Every entity on a connection should
    /// sample against this shared producer-clock cursor.
    /// </summary>
    public sealed class AdaptivePlaybackController
    {
        const uint HalfUintRange = 0x80000000u;

        public const double DefaultServerTickMs = 50;
        public const double DefaultMinPlaybackDelayMs = 100;
        public const double DefaultMaxPlaybackDelayMs = 300;
        public const double DefaultMinPlaybackRate = 0.9;
        public const double DefaultMaxPlaybackRate = 1.1;

        // Stateful operations take this lock before the optional shared Clock
        // lock. ClockSync never calls back into a controller, so there is no
        // reverse lock order and sharing one clock cannot create a cycle.
        readonly object _sync = new();
        double _targetDelay;
        double _currentDelay;
        double _jitter;
        double _excessDelay;
        double? _previousRawExcess;
        double _lossPressureMs;

        uint _lastSequence;
        bool _hasLastSequence;
        long _receivedCount;
        long _lostCount;
        long _duplicateCount;
        long _outOfOrderCount;

        double? _renderCursor;
        double? _lastRenderClientTime;
        double _playbackRate = 1;
        bool _clockReanchorPending;

        public AdaptivePlaybackController(AdaptivePlaybackConfig? config = null)
        {
            config ??= new AdaptivePlaybackConfig();
            Clock = config.Clock ?? new ClockSync();
            TickIntervalMs = Math.Max(0.001, FiniteOr(config.TickIntervalMs, DefaultServerTickMs));
            MinDelayMs = Math.Max(0, FiniteOr(config.MinDelayMs, DefaultMinPlaybackDelayMs));
            MaxDelayMs = Math.Max(MinDelayMs, FiniteOr(config.MaxDelayMs, DefaultMaxPlaybackDelayMs));
            MinPlaybackRate = Clamp(FiniteOr(config.MinPlaybackRate, DefaultMinPlaybackRate), 0.01, 1);
            MaxPlaybackRate = Math.Max(1, FiniteOr(config.MaxPlaybackRate, DefaultMaxPlaybackRate));
            ConvergenceWindowMs = Math.Max(1, FiniteOr(config.ConvergenceWindowMs, 1000));
            AttackFactor = Clamp(FiniteOr(config.AttackFactor, 0.5), 0, 1);
            DecayFactor = Clamp(FiniteOr(config.DecayFactor, 0.05), 0, 1);
            JitterFactor = Math.Max(0, FiniteOr(config.JitterFactor, 2));
            _targetDelay = MinDelayMs;
            _currentDelay = MinDelayMs;
        }

        public ClockSync Clock { get; }
        public double TickIntervalMs { get; }
        public double MinDelayMs { get; }
        public double MaxDelayMs { get; }
        public double MinPlaybackRate { get; }
        public double MaxPlaybackRate { get; }
        public double ConvergenceWindowMs { get; }
        public double AttackFactor { get; }
        public double DecayFactor { get; }
        public double JitterFactor { get; }

        public double CurrentDelayMs
        {
            get
            {
                lock (_sync)
                    return _currentDelay;
            }
        }

        public double TargetDelayMs
        {
            get
            {
                lock (_sync)
                    return _targetDelay;
            }
        }

        /// Unsigned serial distance from fromSequence to toSequence.
        public static uint SequenceDistance(uint fromSequence, uint toSequence)
            => unchecked(toSequence - fromSequence);

        public static bool IsForwardSequence(uint fromSequence, uint toSequence)
        {
            uint distance = SequenceDistance(fromSequence, toSequence);
            return distance > 0 && distance < HalfUintRange;
        }

        /// Observe a frame. Duplicate/reordered frames still inform timing.
        public void ObserveFrame(FrameTimingObservation observation)
        {
            lock (_sync)
            {
                uint sequence = observation.Sequence;
                uint gap = 0;
                bool hasProducerStamp = observation.ProducedAtMs.HasValue &&
                    IsFinite(observation.ProducedAtMs.Value) &&
                    IsFinite(observation.ArrivalTimeMs);

                if (observation.StreamChanged ?? observation.FreshSnapshot)
                {
                    // A new stream is a sanctioned sequence discontinuity.
                    // Drop the superseded producer's clock samples while
                    // preserving the monotonic render cursor itself.
                    _hasLastSequence = false;
                    _clockReanchorPending = true;
                    if (!hasProducerStamp)
                    {
                        // Prevent an ACK-only successor frame from pairing a
                        // prediction seed against the old producer's clock.
                        Clock.Reset();
                        _previousRawExcess = null;
                        _clockReanchorPending = false;
                    }
                }

                if (!_hasLastSequence)
                {
                    _lastSequence = sequence;
                    _hasLastSequence = true;
                    _receivedCount++;
                }
                else
                {
                    uint distance = SequenceDistance(_lastSequence, sequence);
                    if (distance == 0)
                    {
                        _duplicateCount++;
                    }
                    else if (distance < HalfUintRange)
                    {
                        gap = distance - 1;
                        _lostCount += gap;
                        _receivedCount++;
                        _lastSequence = sequence;
                    }
                    else
                    {
                        _outOfOrderCount++;
                    }
                }

                if (gap > 0)
                {
                    _lossPressureMs = Math.Max(
                        _lossPressureMs,
                        Math.Min(MaxDelayMs - MinDelayMs, gap * TickIntervalMs));
                }
                else
                {
                    // Lifetime counters remain available while healthy delivery
                    // is allowed to lower render delay again.
                    _lossPressureMs *= 0.85;
                }

                if (hasProducerStamp)
                {
                    // hasProducerStamp proves the nullable contains a finite
                    // value; GetValueOrDefault keeps nullable analysis quiet.
                    double producedAtMs = observation.ProducedAtMs.GetValueOrDefault();
                    double offsetMs;
                    if (_clockReanchorPending)
                    {
                        offsetMs = Clock.ResetAndObserveServerTimeAndGetOffset(
                            producedAtMs,
                            observation.ArrivalTimeMs);
                        _previousRawExcess = null;
                        _clockReanchorPending = false;
                    }
                    else
                    {
                        offsetMs = Clock.ObserveServerTimeAndGetOffset(
                            producedAtMs,
                            observation.ArrivalTimeMs);
                    }

                    double instant = producedAtMs - observation.ArrivalTimeMs;
                    double rawExcess = Math.Max(0, offsetMs - instant);
                    if (_previousRawExcess.HasValue)
                    {
                        double variation = Math.Abs(rawExcess - _previousRawExcess.Value);
                        // RFC-style transit variation EWMA (1/8).
                        _jitter += (variation - _jitter) / 8;
                    }
                    _previousRawExcess = rawExcess;

                    double excessFactor = rawExcess > _excessDelay ? AttackFactor : DecayFactor;
                    _excessDelay += excessFactor * (rawExcess - _excessDelay);
                }

                double rawTarget = Clamp(
                    MinDelayMs + _excessDelay + JitterFactor * _jitter + _lossPressureMs,
                    MinDelayMs,
                    MaxDelayMs);
                double targetFactor = rawTarget > _targetDelay ? AttackFactor : DecayFactor;
                _targetDelay = Clamp(
                    _targetDelay + targetFactor * (rawTarget - _targetDelay),
                    MinDelayMs,
                    MaxDelayMs);
            }
        }

        /// Advance and return the shared producer-clock render cursor. Null is
        /// returned until a stamped frame initializes ClockSync.
        public double? RenderTime(double clientNowMs)
        {
            if (!IsFinite(clientNowMs))
                return null;

            lock (_sync)
            {
                if (!Clock.TryEstimatedServerNow(clientNowMs, out double serverNowMs))
                    return null;

                if (!_renderCursor.HasValue || !_lastRenderClientTime.HasValue)
                {
                    _renderCursor = serverNowMs - _targetDelay;
                    _lastRenderClientTime = clientNowMs;
                    _currentDelay = _targetDelay;
                    _playbackRate = 1;
                    return _renderCursor;
                }

                double elapsedMs = Math.Max(0, clientNowMs - _lastRenderClientTime.Value);
                // Compare the target with the delay we would have after advancing
                // at normal speed. Otherwise ordinary elapsed time appears as
                // artificial lag and biases the controller above 1x forever.
                double baselineDelay = serverNowMs - (_renderCursor.Value + elapsedMs);
                double delayError = baselineDelay - _targetDelay;
                double requestedPlaybackRate = Clamp(
                    1 + delayError / ConvergenceWindowMs,
                    MinPlaybackRate,
                    MaxPlaybackRate);

                // Only move forward. A larger target slows this cursor until the
                // server timeline pulls ahead instead of rewinding it. ClockSync
                // can move serverNow backwards when its previous maximum ages
                // out. Pause in that exceptional case instead of moving farther
                // ahead of the corrected producer clock; the applied rate may
                // temporarily be below MinPlaybackRate.
                double requestedAdvance = elapsedMs * requestedPlaybackRate;
                double availableAdvance = Math.Max(0, serverNowMs - _renderCursor.Value);
                double appliedAdvance = Math.Min(requestedAdvance, availableAdvance);
                _renderCursor += appliedAdvance;
                _playbackRate = elapsedMs > 0
                    ? appliedAdvance / elapsedMs
                    : requestedPlaybackRate;
                if (clientNowMs > _lastRenderClientTime.Value)
                    _lastRenderClientTime = clientNowMs;
                // A corrected clock may be behind an already-published monotonic
                // cursor. Zero is the meaningful effective delay until it catches
                // up; never expose a negative interpolation delay.
                _currentDelay = Math.Max(0, serverNowMs - _renderCursor.Value);
                return _renderCursor;
            }
        }

        public PlaybackMetrics Metrics
        {
            get
            {
                lock (_sync)
                {
                    long delivered = _receivedCount + _lostCount;
                    return new PlaybackMetrics(
                        _currentDelay,
                        _targetDelay,
                        _jitter,
                        _excessDelay,
                        _receivedCount,
                        _lostCount,
                        _duplicateCount,
                        _outOfOrderCount,
                        delivered == 0 ? 0 : (double)_lostCount / delivered,
                        _playbackRate,
                        _renderCursor);
                }
            }
        }

        static double Clamp(double value, double low, double high)
            => Math.Min(high, Math.Max(low, value));

        static double FiniteOr(double? value, double fallback)
            => value.HasValue && IsFinite(value.Value) ? value.Value : fallback;

        static bool IsFinite(double value)
            => !double.IsNaN(value) && !double.IsInfinity(value);
    }
}
