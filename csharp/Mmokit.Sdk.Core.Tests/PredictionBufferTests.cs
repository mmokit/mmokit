using System;
using System.Collections.Generic;
using System.Threading;
using System.Threading.Tasks;
using Mmokit.Sdk.Core;
using Xunit;

namespace Mmokit.Sdk.Core.Tests
{
    public class PredictionBufferTests
    {
        [Fact]
        public void AcknowledgeRemovesCoveredInputsAndIgnoresStaleAck()
        {
            var buffer = new PredictionBuffer<int>();
            Assert.True(buffer.TryRecord(10, 1));
            Assert.True(buffer.TryRecord(11, 2));
            Assert.True(buffer.TryRecord(12, 3));

            Assert.Equal(2, buffer.Acknowledge(11));
            Assert.Single(buffer.Pending);
            Assert.Equal(12u, buffer.Pending[0].Sequence);
            Assert.Equal(11u, buffer.LastAcknowledgedSequence);

            Assert.Equal(0, buffer.Acknowledge(11)); // duplicate
            Assert.Equal(0, buffer.Acknowledge(10)); // stale
            Assert.Equal(11u, buffer.LastAcknowledgedSequence);
            Assert.Single(buffer.Pending);
        }

        [Fact]
        public void SequenceAndAcknowledgementWorkAcrossWraparound()
        {
            var buffer = new PredictionBuffer<string>();
            Assert.True(buffer.TryRecord(uint.MaxValue - 1, "a"));
            Assert.True(buffer.TryRecord(uint.MaxValue, "b"));
            Assert.True(buffer.TryRecord(0, "c"));
            Assert.True(buffer.TryRecord(1, "d"));

            Assert.Equal(2, buffer.Acknowledge(uint.MaxValue));
            Assert.Equal(new uint[] { 0, 1 }, Sequences(buffer));

            Assert.Equal(1, buffer.Acknowledge(0));
            Assert.Equal(new uint[] { 1 }, Sequences(buffer));
            Assert.Equal(0u, buffer.LastAcknowledgedSequence);
            Assert.False(buffer.TryRecord(uint.MaxValue, "stale"));
        }

        [Fact]
        public void QueueIsBoundedAndRetainsNewestInputs()
        {
            var buffer = new PredictionBuffer<int>(capacity: 3);
            Assert.True(buffer.TryRecord(1, 10));
            Assert.True(buffer.TryRecord(2, 20));
            Assert.True(buffer.TryRecord(3, 30));
            Assert.True(buffer.TryRecord(4, 40));

            Assert.Equal(3, buffer.Count);
            Assert.Equal(new uint[] { 2, 3, 4 }, Sequences(buffer));
            Assert.Equal(1, buffer.DroppedInputCount);
        }

        [Fact]
        public void ReconcileReplaysOnlyUnacknowledgedInputsDeterministically()
        {
            var buffer = new PredictionBuffer<int>();
            buffer.TryRecord(100, 2);
            buffer.TryRecord(101, 3);
            buffer.TryRecord(102, 5);
            var replayed = new List<uint>();

            int state = buffer.Reconcile(
                100,
                authoritativeState: 20,
                (current, sequence, input) =>
                {
                    replayed.Add(sequence);
                    return current * input;
                });

            Assert.Equal(300, state); // ((20 * 3) * 5)
            Assert.Equal(new uint[] { 101, 102 }, replayed);

            replayed.Clear();
            int repeated = buffer.Reconcile(
                100, // duplicate acknowledgement must not alter history
                authoritativeState: 20,
                (current, sequence, input) =>
                {
                    replayed.Add(sequence);
                    return current * input;
                });
            Assert.Equal(300, repeated);
            Assert.Equal(new uint[] { 101, 102 }, replayed);
        }

        [Fact]
        public void RecordRejectsDuplicateOutOfOrderAndAlreadyAcknowledgedInput()
        {
            var buffer = new PredictionBuffer<int>();
            Assert.True(buffer.TryRecord(8, 1));
            Assert.False(buffer.TryRecord(8, 2));
            Assert.False(buffer.TryRecord(7, 3));
            Assert.Equal(1, buffer.Acknowledge(8));
            Assert.False(buffer.TryRecord(8, 4));
            Assert.False(buffer.TryRecord(7, 5));
            Assert.True(buffer.TryRecord(9, 6));
        }

        [Fact]
        public void RecordRejectsAmbiguousHalfRangeAfterAcknowledgement()
        {
            var buffer = new PredictionBuffer<int>();
            buffer.Acknowledge(10);

            Assert.False(buffer.TryRecord(unchecked(10u + 0x80000000u), 1));
            Assert.Empty(buffer.Pending);
        }

        [Fact]
        public void CorrectionHelpersDelegateMathAndClampBlendFactor()
        {
            Assert.Equal(3.0, PredictionCorrection.Measure(7.0, 10.0, (a, b) => Math.Abs(a - b)), 12);
            Assert.Equal(10.0, PredictionCorrection.Blend(0.0, 10.0, 2.0, (a, b, t) => a + (b - a) * t), 12);
            Assert.Equal(0.0, PredictionCorrection.Blend(0.0, 10.0, -1.0, (a, b, t) => a + (b - a) * t), 12);
        }

        [Fact]
        public async Task ReconcileUsesAtomicSnapshotAndReplaysWithoutHoldingBufferLock()
        {
            var buffer = new PredictionBuffer<int>();
            Assert.True(buffer.TryRecord(1, 10));
            Assert.True(buffer.TryRecord(2, 20));

            using var replayStarted = new ManualResetEventSlim();
            using var releaseReplay = new ManualResetEventSlim();
            Task<int> reconcile = Task.Run(() => buffer.Reconcile(
                1,
                authoritativeState: 0,
                (state, _, input) =>
                {
                    replayStarted.Set();
                    Assert.True(releaseReplay.Wait(TimeSpan.FromSeconds(10)));
                    return state + input;
                }));

            Assert.True(replayStarted.Wait(TimeSpan.FromSeconds(10)));
            Task<bool> record = Task.Run(() => buffer.TryRecord(3, 30));
            try
            {
                // A replay callback must not block a background network/update
                // operation on the buffer. Input 3 is recorded after the
                // reconcile snapshot and therefore belongs to the next replay.
                Task completed = await Task.WhenAny(record, Task.Delay(TimeSpan.FromSeconds(10)));
                Assert.Same(record, completed);
                Assert.True(await record);
            }
            finally
            {
                releaseReplay.Set();
            }

            Assert.Equal(20, await reconcile.WaitAsync(TimeSpan.FromSeconds(10)));
            Assert.Equal(new uint[] { 2, 3 }, Sequences(buffer));
        }

        [Fact]
        public async Task ConcurrentMutationAndSnapshotReadsRemainBoundedAndOrdered()
        {
            const int iterations = 20_000;
            var buffer = new PredictionBuffer<int>(capacity: 64);
            using var start = new ManualResetEventSlim();

            Task producer = Task.Run(() =>
            {
                start.Wait();
                for (uint sequence = 1; sequence <= iterations; sequence++)
                    buffer.TryRecord(sequence, (int)sequence);
            });
            Task acknowledger = Task.Run(() =>
            {
                start.Wait();
                for (uint sequence = 1; sequence <= iterations; sequence += 2)
                    buffer.Acknowledge(sequence);
            });
            Task reader = Task.Run(() =>
            {
                start.Wait();
                for (int iteration = 0; iteration < iterations; iteration++)
                {
                    IReadOnlyList<PendingInput<int>> pending = buffer.Pending;
                    Assert.InRange(pending.Count, 0, buffer.Capacity);
                    for (int i = 1; i < pending.Count; i++)
                        Assert.True(PredictionBuffer<int>.IsSequenceNewer(
                            pending[i].Sequence,
                            pending[i - 1].Sequence));
                    _ = buffer.Count;
                    _ = buffer.DroppedInputCount;
                    _ = buffer.HasLastAcknowledgement;
                }
            });

            start.Set();
            await Task.WhenAll(producer, acknowledger, reader).WaitAsync(TimeSpan.FromSeconds(20));
            Assert.InRange(buffer.Count, 0, buffer.Capacity);
        }

        static uint[] Sequences<TInput>(PredictionBuffer<TInput> buffer)
        {
            IReadOnlyList<PendingInput<TInput>> pending = buffer.Pending;
            var result = new uint[pending.Count];
            for (int i = 0; i < result.Length; i++)
                result[i] = pending[i].Sequence;
            return result;
        }
    }
}
