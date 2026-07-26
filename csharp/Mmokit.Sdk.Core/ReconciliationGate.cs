using System;
using System.Collections.Generic;

namespace Mmokit.Sdk.Core
{
    /// The identity a server frame and a prediction seed must agree on to pair.
    public readonly struct AckedFrame
    {
        public AckedFrame(uint streamEpoch, uint tick, uint processedSequence)
        {
            StreamEpoch = streamEpoch;
            Tick = tick;
            ProcessedSequence = processedSequence;
        }

        public uint StreamEpoch { get; }
        public uint Tick { get; }
        public uint ProcessedSequence { get; }
    }

    /// Pairs server-authoritative prediction seeds with accepted delta frames.
    ///
    /// A seed is only usable once the frame it describes has been accepted by
    /// the decoder — otherwise a stale authority could retire commands the
    /// current one never processed. Matching on
    /// (StreamEpoch, Tick, ProcessedSequence) is what makes the pairing safe
    /// across a handoff. Both delivery orders are supported and staging is
    /// bounded so a client that never sees the matching half cannot grow
    /// without limit.
    ///
    /// Game-neutral by construction: it contains no movement or physics, only
    /// the pairing. TSeed is the game's own seed type; keySelector extracts the
    /// identity triple so generated seed classes need not implement anything.
    ///
    /// PORTING NOTE. The TypeScript reference (pkg/quantize/ts/reconciliation-
    /// gate.ts) relies on JS Map insertion order for oldest-first eviction and
    /// for re-insert-moves-to-back. Dictionary&lt;K,V&gt; guarantees NEITHER, so
    /// this port carries an explicit insertion-order list alongside each
    /// dictionary. Getting that wrong is silent and only surfaces under staging
    /// pressure during a handoff — the exact scenario this gate exists for.
    public sealed class ReconciliationGate<TSeed>
    {
        public const int DefaultStagedPairs = 8;

        readonly object _sync = new();
        readonly int _maxStaged;
        readonly Func<TSeed, AckedFrame> _keySelector;

        readonly Dictionary<string, TSeed> _seeds = new();
        readonly List<string> _seedOrder = new();
        readonly Dictionary<string, AckedFrame> _frames = new();
        readonly List<string> _frameOrder = new();

        uint _currentStreamEpoch;
        bool _hasStreamEpoch;

        public ReconciliationGate(Func<TSeed, AckedFrame> keySelector, int maxStaged = DefaultStagedPairs)
        {
            if (keySelector == null) throw new ArgumentNullException(nameof(keySelector));
            if (maxStaged < 1) throw new ArgumentOutOfRangeException(nameof(maxStaged), "maxStaged must be positive");
            _keySelector = keySelector;
            _maxStaged = maxStaged;
        }

        static string PairKey(uint epoch, uint tick, uint sequence) => $"{epoch}:{tick}:{sequence}";

        static string PairKey(in AckedFrame f) => PairKey(f.StreamEpoch, f.Tick, f.ProcessedSequence);

        /// Mirrors isForwardSequence in playback-controller.ts.
        static bool IsForwardSequence(uint from, uint to)
        {
            uint distance = unchecked(to - from);
            return distance > 0 && distance < 0x80000000u;
        }

        /// Insert-or-refresh with oldest-first eviction, reproducing JS Map
        /// insertion-order semantics: an existing key moves to the back.
        static void AddBounded<T>(Dictionary<string, T> map, List<string> order, string key, T value, int limit)
        {
            if (map.Remove(key)) order.Remove(key);
            map[key] = value;
            order.Add(key);
            while (order.Count > limit)
            {
                string oldest = order[0];
                order.RemoveAt(0);
                map.Remove(oldest);
            }
        }

        static bool RemoveKeyed<T>(Dictionary<string, T> map, List<string> order, string key)
        {
            if (!map.Remove(key)) return false;
            order.Remove(key);
            return true;
        }

        /// Returns true when an accepted frame establishes a fresh replication stream.
        public bool ObserveStream(uint streamEpoch)
        {
            lock (_sync)
            {
                if (!_hasStreamEpoch)
                {
                    _currentStreamEpoch = streamEpoch;
                    _hasStreamEpoch = true;
                    RetainEpoch(streamEpoch);
                    return false;
                }
                if (streamEpoch == _currentStreamEpoch) return false;
                if (!IsForwardSequence(_currentStreamEpoch, streamEpoch)) return false;
                _currentStreamEpoch = streamEpoch;
                RetainEpoch(streamEpoch);
                return true;
            }
        }

        /// Clear pre-reset pair records while preserving this frame's early seed.
        public void ResetForFreshSnapshot(AckedFrame? frame)
        {
            lock (_sync)
            {
                bool haveMatch = false;
                TSeed matching = default!;
                string key = "";
                if (frame.HasValue)
                {
                    key = PairKey(frame.Value);
                    haveMatch = _seeds.TryGetValue(key, out matching!);
                }
                _seeds.Clear();
                _seedOrder.Clear();
                _frames.Clear();
                _frameOrder.Clear();
                if (haveMatch)
                {
                    _seeds[key] = matching;
                    _seedOrder.Add(key);
                }
            }
        }

        /// Allow the current stream or an early seed from its forward successor.
        public bool AcceptsSeedStream(uint streamEpoch)
        {
            lock (_sync)
            {
                return !_hasStreamEpoch
                    || streamEpoch == _currentStreamEpoch
                    || IsForwardSequence(_currentStreamEpoch, streamEpoch);
            }
        }

        /// Whether a seed may affect prediction before its exact frame arrives.
        /// Successor-stream seeds stay quarantined until the delta decoder
        /// accepts that stream; an unestablished connection may use its first
        /// seed directly.
        public bool CanApplySeedImmediately(uint streamEpoch)
        {
            lock (_sync)
            {
                return !_hasStreamEpoch || streamEpoch == _currentStreamEpoch;
            }
        }

        void RetainEpoch(uint epoch)
        {
            string prefix = epoch.ToString() + ":";
            for (int i = _seedOrder.Count - 1; i >= 0; i--)
            {
                if (_seedOrder[i].StartsWith(prefix, StringComparison.Ordinal)) continue;
                _seeds.Remove(_seedOrder[i]);
                _seedOrder.RemoveAt(i);
            }
            for (int i = _frameOrder.Count - 1; i >= 0; i--)
            {
                if (_frameOrder[i].StartsWith(prefix, StringComparison.Ordinal)) continue;
                _frames.Remove(_frameOrder[i]);
                _frameOrder.RemoveAt(i);
            }
        }

        /// Stage a seed. Returns the seed when its frame had already arrived.
        public bool TryStageSeed(TSeed seed, out TSeed paired)
        {
            paired = default!;
            AckedFrame identity = _keySelector(seed);
            lock (_sync)
            {
                if (!(!_hasStreamEpoch
                      || identity.StreamEpoch == _currentStreamEpoch
                      || IsForwardSequence(_currentStreamEpoch, identity.StreamEpoch)))
                {
                    return false;
                }
                string key = PairKey(identity);
                if (RemoveKeyed(_frames, _frameOrder, key))
                {
                    paired = seed;
                    return true;
                }
                AddBounded(_seeds, _seedOrder, key, seed, _maxStaged);
                return false;
            }
        }

        /// Stage an accepted frame. Returns its seed when that had already arrived.
        public bool TryStageFrame(AckedFrame frame, out TSeed paired)
        {
            paired = default!;
            lock (_sync)
            {
                string key = PairKey(frame);
                if (_seeds.TryGetValue(key, out TSeed? seed))
                {
                    RemoveKeyed(_seeds, _seedOrder, key);
                    paired = seed!;
                    return true;
                }
                AddBounded(_frames, _frameOrder, key, frame, _maxStaged);
                return false;
            }
        }

        public void Reset()
        {
            lock (_sync)
            {
                _hasStreamEpoch = false;
                _currentStreamEpoch = 0;
                _seeds.Clear();
                _seedOrder.Clear();
                _frames.Clear();
                _frameOrder.Clear();
            }
        }
    }
}
