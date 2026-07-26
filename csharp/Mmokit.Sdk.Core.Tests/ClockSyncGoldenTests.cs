using System;
using System.Threading;
using System.Threading.Tasks;
using Mmokit.Sdk.Core;
using Xunit;

namespace Mmokit.Sdk.Core.Tests
{
    public class ClockSyncGoldenTests
    {
        [Fact]
        public void ReproducesGoldenOffsets()
        {
            var golden = Golden.Load();
            Assert.Equal(ClockSync.InstantWindow, golden.ClockSync.Window);

            var c = new ClockSync();
            foreach (var o in golden.ClockSync.Observations)
            {
                c.ObserveServerTime(o.ServerMs, o.ClientNowMs);
                Assert.True(c.Initialized);
                Assert.Equal(o.ExpectedOffsetMs, c.OffsetMs, 6); // 6 dp tolerance
                Assert.Equal(o.ClientNowMs + o.ExpectedOffsetMs, c.EstimatedServerNow(o.ClientNowMs), 6);
            }
        }

        [Fact]
        public async Task ConcurrentObservationsAndReadsPreserveAValidWindow()
        {
            const int iterations = 20_000;
            var clock = new ClockSync();
            using var start = new ManualResetEventSlim();

            Task lowerObserver = Task.Run(() =>
            {
                start.Wait();
                for (int i = 0; i < iterations; i++)
                    clock.ObserveServerTime(10_100 + i, 10_000 + i);
            });
            Task upperObserver = Task.Run(() =>
            {
                start.Wait();
                for (int i = 0; i < iterations; i++)
                    clock.ObserveServerTime(20_200 + i, 20_000 + i);
            });
            Task reader = Task.Run(() =>
            {
                start.Wait();
                for (int i = 0; i < iterations; i++)
                {
                    _ = clock.Initialized;
                    Assert.InRange(clock.OffsetMs, 0, 200);
                    Assert.True(double.IsFinite(clock.EstimatedServerNow(i)));
                }
            });

            start.Set();
            await Task.WhenAll(lowerObserver, upperObserver, reader).WaitAsync(TimeSpan.FromSeconds(20));

            // Deterministically replace the entire concurrent observation
            // window, proving that no racing writer corrupted its index/count.
            for (int i = 0; i < ClockSync.InstantWindow; i++)
                clock.ObserveServerTime(30_000 + i - 50, 30_000 + i);
            Assert.Equal(-50, clock.OffsetMs);
            Assert.Equal(950, clock.EstimatedServerNow(1000));
        }
    }
}
