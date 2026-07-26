using System;
using System.Threading;
using System.Threading.Tasks;
using Mmokit.Sdk.Core;
using Xunit;

namespace Mmokit.Sdk.Core.Tests
{
    public class AdaptivePlaybackControllerTests
    {
        [Fact]
        public void SequenceAccountingHandlesGapsDuplicatesReorderingAndWrap()
        {
            var controller = new AdaptivePlaybackController();
            controller.ObserveFrame(new FrameTimingObservation(uint.MaxValue - 1, false, 0));
            controller.ObserveFrame(new FrameTimingObservation(1, false, 1)); // max, zero were lost
            controller.ObserveFrame(new FrameTimingObservation(1, false, 2)); // duplicate
            controller.ObserveFrame(new FrameTimingObservation(0, false, 3)); // reordered

            PlaybackMetrics metrics = controller.Metrics;
            Assert.Equal(2, metrics.ReceivedFrames);
            Assert.Equal(2, metrics.LostFrames);
            Assert.Equal(1, metrics.DuplicateFrames);
            Assert.Equal(1, metrics.OutOfOrderFrames);
            Assert.Equal(0.5, metrics.LossRate, 12);
            Assert.Equal(3u, AdaptivePlaybackController.SequenceDistance(uint.MaxValue - 1, 1));
            Assert.True(AdaptivePlaybackController.IsForwardSequence(uint.MaxValue, 0));
        }

        [Fact]
        public void LossAndArrivalVariationRaiseDelayWithinBounds()
        {
            var controller = new AdaptivePlaybackController(new AdaptivePlaybackConfig
            {
                MinDelayMs = 100,
                MaxDelayMs = 180,
                AttackFactor = 1,
                DecayFactor = 0.05,
            });

            controller.ObserveFrame(new FrameTimingObservation(10, false, 1000, 1100));
            Assert.Equal(100, controller.TargetDelayMs, 9);

            // Sequence gap plus a delayed arrival (lower instantaneous offset)
            // both push the adaptive target upward.
            controller.ObserveFrame(new FrameTimingObservation(13, false, 1150, 1150));
            Assert.True(controller.TargetDelayMs > 100);
            Assert.True(controller.TargetDelayMs <= 180);
            Assert.True(controller.Metrics.JitterMs > 0);
            Assert.Equal(2, controller.Metrics.LostFrames);
        }

        [Fact]
        public void RenderCursorNeverRewindsWhenTargetDelayRises()
        {
            var controller = new AdaptivePlaybackController(new AdaptivePlaybackConfig
            {
                MinDelayMs = 100,
                MaxDelayMs = 300,
                AttackFactor = 1,
                MinPlaybackRate = 0.9,
                MaxPlaybackRate = 1.1,
            });
            controller.ObserveFrame(new FrameTimingObservation(1, false, 1000, 1100));
            double first = controller.RenderTime(1000)!.Value;

            controller.ObserveFrame(new FrameTimingObservation(6, false, 1100, 1150));
            Assert.True(controller.TargetDelayMs > 100);
            double second = controller.RenderTime(1016)!.Value;
            double third = controller.RenderTime(1032)!.Value;

            Assert.True(second >= first);
            Assert.True(third >= second);
            Assert.InRange(controller.Metrics.PlaybackRate, 0.9, 1.1);
        }

        [Fact]
        public void NewStreamRestartsSequenceAndProducerClockWithoutRewindingCursor()
        {
            var controller = new AdaptivePlaybackController();
            controller.ObserveFrame(new FrameTimingObservation(
                100, true, 1000, 1100, streamChanged: true));
            double before = controller.RenderTime(1000)!.Value;

            controller.ObserveFrame(new FrameTimingObservation(
                5, true, 1010, 510, streamChanged: true));
            double after = controller.RenderTime(1010)!.Value;

            Assert.Equal(2, controller.Metrics.ReceivedFrames);
            Assert.Equal(0, controller.Metrics.LostFrames);
            Assert.True(controller.Clock.Initialized);
            Assert.Equal(-500, controller.Clock.OffsetMs);
            Assert.True(after >= before);
        }

        [Fact]
        public void SameStreamRecoverySnapshotsStillExposeSequenceLoss()
        {
            var controller = new AdaptivePlaybackController();
            controller.ObserveFrame(new FrameTimingObservation(
                10, true, 0, streamChanged: false));
            controller.ObserveFrame(new FrameTimingObservation(
                12, true, 50, streamChanged: false));

            Assert.Equal(2, controller.Metrics.ReceivedFrames);
            Assert.Equal(1, controller.Metrics.LostFrames);
        }

        [Fact]
        public void EmptyStreamSwitchInvalidatesOldClockUntilProducerStamp()
        {
            var controller = new AdaptivePlaybackController();
            controller.ObserveFrame(new FrameTimingObservation(
                1, true, 1000, 2000, streamChanged: true));
            Assert.Equal(1000, controller.Clock.OffsetMs);

            controller.ObserveFrame(new FrameTimingObservation(
                1, true, 1050, streamChanged: true));
            Assert.False(controller.Clock.Initialized);
            Assert.Null(controller.RenderTime(1050));

            controller.ObserveFrame(new FrameTimingObservation(
                2, false, 1100, 1900, streamChanged: false));
            Assert.True(controller.Clock.Initialized);
            Assert.Equal(800, controller.Clock.OffsetMs);
        }

        [Fact]
        public void SameStreamRecoveryKeepsExistingClockWindow()
        {
            var controller = new AdaptivePlaybackController();
            controller.ObserveFrame(new FrameTimingObservation(
                10, true, 1000, 2000, streamChanged: true));
            controller.ObserveFrame(new FrameTimingObservation(
                11, true, 1100, 1900, streamChanged: false));

            Assert.Equal(1000, controller.Clock.OffsetMs);
        }

        [Fact]
        public void RenderTimeWaitsForProducerStamp()
        {
            var controller = new AdaptivePlaybackController();
            controller.ObserveFrame(new FrameTimingObservation(1, false, 1000));
            Assert.Null(controller.RenderTime(1000));
            Assert.Null(controller.Metrics.RenderTimeMs);
        }

        [Fact]
        public void DownwardClockOffsetStepPausesWithoutRewindingOrNegativeDelay()
        {
            var controller = new AdaptivePlaybackController();
            controller.ObserveFrame(new FrameTimingObservation(
                0, true, 5000, 4000)); // offset -1000ms
            double cursor = controller.RenderTime(5000)!.Value;

            // Fill ClockSync's 40-sample window at -1600ms. Its old maximum
            // remains active until the final observation replaces it.
            for (uint i = 1; i < ClockSync.InstantWindow; i++)
            {
                double arrivalTimeMs = 5000 + i * 50;
                controller.ObserveFrame(new FrameTimingObservation(
                    i, false, arrivalTimeMs, arrivalTimeMs - 1600));
                double next = controller.RenderTime(arrivalTimeMs)!.Value;
                Assert.True(next >= cursor);
                Assert.True(controller.CurrentDelayMs >= 0);
                cursor = next;
            }
            Assert.Equal(-1000, controller.Clock.OffsetMs);

            const double correctedArrivalTimeMs = 7000;
            controller.ObserveFrame(new FrameTimingObservation(
                ClockSync.InstantWindow,
                false,
                correctedArrivalTimeMs,
                correctedArrivalTimeMs - 1600));
            Assert.Equal(-1600, controller.Clock.OffsetMs);

            double corrected = controller.RenderTime(correctedArrivalTimeMs)!.Value;
            Assert.Equal(cursor, corrected);
            Assert.Equal(0, controller.CurrentDelayMs);
            Assert.Equal(0, controller.Metrics.PlaybackRate);

            // Remain monotonic with a valid effective delay while the corrected
            // producer clock catches up to the already-published cursor.
            for (uint i = 41; i <= 55; i++)
            {
                double arrivalTimeMs = 5000 + i * 50;
                controller.ObserveFrame(new FrameTimingObservation(
                    i, false, arrivalTimeMs, arrivalTimeMs - 1600));
                double next = controller.RenderTime(arrivalTimeMs)!.Value;
                Assert.True(next >= cursor);
                Assert.True(controller.CurrentDelayMs >= 0);
                cursor = next;
            }
        }

        [Fact]
        public async Task SharedClockSupportsConcurrentObservationRenderingAndDirectUpdates()
        {
            const int iterations = 10_000;
            var clock = new ClockSync();
            var first = new AdaptivePlaybackController(new AdaptivePlaybackConfig { Clock = clock });
            var second = new AdaptivePlaybackController(new AdaptivePlaybackConfig { Clock = clock });
            using var start = new ManualResetEventSlim();

            Task firstObserver = Task.Run(() =>
            {
                start.Wait();
                for (uint i = 0; i < iterations; i++)
                {
                    double arrival = 10_000 + i;
                    first.ObserveFrame(new FrameTimingObservation(i, false, arrival, arrival + 100));
                }
            });
            Task secondObserver = Task.Run(() =>
            {
                start.Wait();
                for (uint i = 0; i < iterations; i++)
                {
                    double arrival = 20_000 + i;
                    second.ObserveFrame(new FrameTimingObservation(i, false, arrival, arrival + 120));
                }
            });
            Task directClockObserver = Task.Run(() =>
            {
                start.Wait();
                for (int i = 0; i < iterations; i++)
                    clock.ObserveServerTime(30_080 + i, 30_000 + i);
            });
            Task renderer = Task.Run(() =>
            {
                start.Wait();
                double? firstCursor = null;
                double? secondCursor = null;
                for (int i = 0; i < iterations; i++)
                {
                    double? nextFirst = first.RenderTime(40_000 + i);
                    double? nextSecond = second.RenderTime(40_000 + i);
                    if (nextFirst.HasValue && firstCursor.HasValue)
                        Assert.True(nextFirst.Value >= firstCursor.Value);
                    if (nextSecond.HasValue && secondCursor.HasValue)
                        Assert.True(nextSecond.Value >= secondCursor.Value);
                    firstCursor = nextFirst ?? firstCursor;
                    secondCursor = nextSecond ?? secondCursor;
                    _ = first.Metrics;
                    _ = second.Metrics;
                    _ = clock.OffsetMs;
                }
            });

            start.Set();
            await Task.WhenAll(firstObserver, secondObserver, directClockObserver, renderer)
                .WaitAsync(TimeSpan.FromSeconds(20));

            Assert.Equal(iterations, first.Metrics.ReceivedFrames);
            Assert.Equal(iterations, second.Metrics.ReceivedFrames);
            Assert.InRange(first.TargetDelayMs, first.MinDelayMs, first.MaxDelayMs);
            Assert.InRange(second.TargetDelayMs, second.MinDelayMs, second.MaxDelayMs);
            Assert.True(clock.Initialized);
        }
    }
}
