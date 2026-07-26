using System;
using Mmokit.Sdk.Core;
using Xunit;

namespace Mmokit.Sdk.Core.Tests
{
    /// Mirrors pkg/quantize/ts/reconciliation-gate.test.ts case for case, plus
    /// explicit eviction-order assertions. The C# port cannot rely on
    /// Dictionary ordering the way the TS reference relies on Map ordering, so
    /// eviction order is the one place a naive port silently diverges — and it
    /// only shows up under staging pressure during a handoff.
    public class ReconciliationGateTests
    {
        sealed class TestSeed
        {
            public uint StreamEpoch { get; init; } = 3;
            public uint Tick { get; init; } = 10;
            public uint ProcessedSequence { get; init; } = 4;
            public string Payload { get; init; } = "seed";
        }

        static ReconciliationGate<TestSeed> NewGate(int maxStaged = ReconciliationGate<TestSeed>.DefaultStagedPairs) =>
            new(s => new AckedFrame(s.StreamEpoch, s.Tick, s.ProcessedSequence), maxStaged);

        static AckedFrame Frame(uint epoch = 3, uint tick = 10, uint sequence = 4) =>
            new(epoch, tick, sequence);

        [Fact]
        public void PairsSeedThenFrameAndFrameThenSeed()
        {
            var seedFirst = NewGate();
            Assert.False(seedFirst.TryStageSeed(new TestSeed(), out _));
            Assert.True(seedFirst.TryStageFrame(Frame(), out var a));
            Assert.Equal("seed", a.Payload);

            var frameFirst = NewGate();
            Assert.False(frameFirst.TryStageFrame(Frame(), out _));
            Assert.True(frameFirst.TryStageSeed(new TestSeed(), out var b));
            Assert.Equal("seed", b.Payload);
        }

        [Fact]
        public void QuarantinesTickSequenceAndAuthorityMismatches()
        {
            var gate = NewGate();
            gate.TryStageSeed(new TestSeed(), out _);
            Assert.False(gate.TryStageFrame(Frame(tick: 11), out _));
            Assert.False(gate.TryStageFrame(Frame(sequence: 5), out _));
            Assert.False(gate.TryStageFrame(Frame(epoch: 4), out _));
        }

        [Fact]
        public void NewStreamDropsStaleRecordsButPreservesAnEarlyNewStreamSeed()
        {
            var gate = NewGate();
            gate.ObserveStream(3);
            gate.TryStageSeed(new TestSeed { Tick = 9 }, out _);
            var next = new TestSeed { StreamEpoch = 4, Tick = 1, ProcessedSequence = 8, Payload = "next" };
            gate.TryStageSeed(next, out _);

            Assert.True(gate.ObserveStream(4));
            Assert.True(gate.TryStageFrame(new AckedFrame(4, 1, 8), out var paired));
            Assert.Equal("next", paired.Payload);
            Assert.False(gate.TryStageFrame(new AckedFrame(3, 9, 4), out _));
        }

        [Fact]
        public void SameStreamFreshSnapshotKeepsOnlyItsExactEarlySeed()
        {
            var gate = NewGate();
            gate.ObserveStream(3);
            gate.TryStageSeed(new TestSeed { Tick = 9 }, out _);
            var exact = new TestSeed { Tick = 10, Payload = "exact" };
            gate.TryStageSeed(exact, out _);

            gate.ResetForFreshSnapshot(Frame());
            Assert.True(gate.TryStageFrame(Frame(), out var paired));
            Assert.Equal("exact", paired.Payload);
            Assert.False(gate.TryStageFrame(new AckedFrame(3, 9, 4), out _));
        }

        [Fact]
        public void RejectsALatePriorStreamSeedWhileAllowingAnEarlySuccessor()
        {
            var gate = NewGate();
            gate.ObserveStream(4);

            Assert.False(gate.AcceptsSeedStream(3));
            Assert.False(gate.TryStageSeed(new TestSeed { StreamEpoch = 3 }, out _));
            Assert.True(gate.AcceptsSeedStream(4));
            Assert.True(gate.AcceptsSeedStream(5));
            Assert.True(gate.CanApplySeedImmediately(4));
            Assert.False(gate.CanApplySeedImmediately(5));
        }

        [Fact]
        public void QuarantinesASuccessorSeedUntilItsExactFrameEstablishesTheStream()
        {
            var gate = NewGate();
            gate.ObserveStream(3);
            var successor = new TestSeed { StreamEpoch = 4, Tick = 1, ProcessedSequence = 8 };

            Assert.False(gate.CanApplySeedImmediately(successor.StreamEpoch));
            Assert.False(gate.TryStageSeed(successor, out _));
            Assert.True(gate.ObserveStream(4));
            Assert.True(gate.TryStageFrame(new AckedFrame(4, 1, 8), out _));
            Assert.True(gate.CanApplySeedImmediately(successor.StreamEpoch));
        }

        [Fact]
        public void StreamEpochOnlyAdvancesForward()
        {
            var gate = NewGate();
            Assert.False(gate.ObserveStream(10)); // first observation establishes
            Assert.False(gate.ObserveStream(10)); // same epoch is not a change
            Assert.False(gate.ObserveStream(9));  // backward is ignored
            Assert.True(gate.CanApplySeedImmediately(10));
            Assert.True(gate.ObserveStream(11));
        }

        [Fact]
        public void EvictsOldestFirstWhenStagingExceedsTheBound()
        {
            var gate = NewGate(3);
            for (uint tick = 1; tick <= 4; tick++)
                gate.TryStageSeed(new TestSeed { Tick = tick, Payload = $"seed-{tick}" }, out _);

            Assert.False(gate.TryStageFrame(Frame(tick: 1), out _));
            for (uint tick = 2; tick <= 4; tick++)
            {
                Assert.True(gate.TryStageFrame(Frame(tick: tick), out var paired));
                Assert.Equal($"seed-{tick}", paired.Payload);
            }
        }

        [Fact]
        public void ReStagingAnExistingKeyRefreshesItsPosition()
        {
            var gate = NewGate(2);
            gate.TryStageSeed(new TestSeed { Tick = 1, Payload = "a" }, out _);
            gate.TryStageSeed(new TestSeed { Tick = 2, Payload = "b" }, out _);
            // Re-stage tick 1: it moves to the back, so tick 2 becomes oldest.
            gate.TryStageSeed(new TestSeed { Tick = 1, Payload = "a2" }, out _);
            gate.TryStageSeed(new TestSeed { Tick = 3, Payload = "c" }, out _);

            Assert.False(gate.TryStageFrame(Frame(tick: 2), out _));
            Assert.True(gate.TryStageFrame(Frame(tick: 1), out var refreshed));
            Assert.Equal("a2", refreshed.Payload);
            Assert.True(gate.TryStageFrame(Frame(tick: 3), out var newest));
            Assert.Equal("c", newest.Payload);
        }

        [Fact]
        public void FramesAreBoundedTheSameWayAsSeeds()
        {
            var gate = NewGate(2);
            for (uint tick = 1; tick <= 3; tick++)
                gate.TryStageFrame(Frame(tick: tick), out _);

            Assert.False(gate.TryStageSeed(new TestSeed { Tick = 1 }, out _));
            Assert.True(gate.TryStageSeed(new TestSeed { Tick = 2 }, out _));
        }

        [Fact]
        public void ResetClearsTheStreamAndBothStagingMaps()
        {
            var gate = NewGate();
            gate.ObserveStream(5);
            gate.TryStageSeed(new TestSeed { StreamEpoch = 5 }, out _);
            gate.Reset();

            Assert.True(gate.CanApplySeedImmediately(1));
            Assert.False(gate.TryStageFrame(new AckedFrame(5, 10, 4), out _));
        }

        [Fact]
        public void RejectsANonPositiveBound()
        {
            Assert.Throws<ArgumentOutOfRangeException>(() => NewGate(0));
            Assert.Throws<ArgumentOutOfRangeException>(() => NewGate(-1));
            Assert.Throws<ArgumentNullException>(() =>
                new ReconciliationGate<TestSeed>(null!));
        }
    }
}
