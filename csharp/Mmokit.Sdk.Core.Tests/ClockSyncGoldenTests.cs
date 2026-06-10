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
    }
}
