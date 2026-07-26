using System;
using Mmokit.Sdk.Core;
using Xunit;

namespace Mmokit.Sdk.Core.Tests
{
    /// Cross-language golden tests for the two SDK cores that previously had
    /// none: AdaptivePlaybackController and PredictionBuffer, plus the
    /// FrameFlagInputAck trailer.
    ///
    /// Before this, TS/C# parity for these rested entirely on two independently
    /// hand-written suites that happened to agree. These replay the SAME
    /// Go-produced manifest that pkg/quantize/ts/playback-golden.test.ts
    /// replays, so a divergence now fails on exactly one side.
    public class PlaybackGoldenTests
    {
        // The manifest is produced by float64 arithmetic in Go and consumed by
        // float64 arithmetic here, so this is round-trip noise only.
        const int Precision = 9;

        [Fact]
        public void PlaybackMatchesGoldenTrace()
        {
            var golden = Golden.Load();
            var c = golden.Playback;
            Assert.NotEmpty(c.Steps);

            var controller = new AdaptivePlaybackController(new AdaptivePlaybackConfig
            {
                TickIntervalMs = c.TickIntervalMs,
                MinDelayMs = c.MinDelayMs,
                MaxDelayMs = c.MaxDelayMs,
                MinPlaybackRate = c.MinPlaybackRate,
                MaxPlaybackRate = c.MaxPlaybackRate,
                ConvergenceWindowMs = c.ConvergenceWindowMs,
                AttackFactor = c.AttackFactor,
                DecayFactor = c.DecayFactor,
                JitterFactor = c.JitterFactor,
            });

            foreach (var step in c.Steps)
            {
                controller.ObserveFrame(new FrameTimingObservation(
                    step.Seq,
                    step.FreshSnapshot,
                    step.ArrivalTimeMs,
                    step.HasProducedAt ? step.ProducedAtMs : (double?)null,
                    step.HasStreamChanged ? step.StreamChanged : (bool?)null));

                var m = controller.Metrics;
                Assert.Equal(step.ExpectedTargetDelayMs, m.TargetDelayMs, Precision);
                Assert.Equal(step.ExpectedJitterMs, m.JitterMs, Precision);
                Assert.Equal(step.ExpectedExcessDelayMs, m.ExcessDelayMs, Precision);
                Assert.Equal(step.ExpectedLossRate, m.LossRate, Precision);
                Assert.Equal(step.ExpectedReceivedFrames, m.ReceivedFrames);
                Assert.Equal(step.ExpectedLostFrames, m.LostFrames);
                Assert.Equal(step.ExpectedDuplicateFrames, m.DuplicateFrames);
                Assert.Equal(step.ExpectedOutOfOrderFrames, m.OutOfOrderFrames);

                if (!step.HasRender) continue;

                double? rendered = controller.RenderTime(step.RenderClientNowMs);
                if (step.ExpectedRenderNull)
                {
                    Assert.Null(rendered);
                    continue;
                }
                Assert.NotNull(rendered);
                Assert.Equal(step.ExpectedRenderTimeMs, rendered!.Value, Precision);

                var after = controller.Metrics;
                Assert.Equal(step.ExpectedPlaybackRate, after.PlaybackRate, Precision);
                Assert.Equal(step.ExpectedCurrentDelayMs, after.CurrentDelayMs, Precision);
            }
        }

        [Fact]
        public void PredictionMatchesGoldenTrace()
        {
            var golden = Golden.Load();
            var c = golden.Prediction;
            Assert.NotEmpty(c.Steps);

            var buffer = new PredictionBuffer<int>(c.MaxPending);

            foreach (var step in c.Steps)
            {
                switch (step.Op)
                {
                    case "push":
                    {
                        bool accepted = buffer.TryRecord(step.Seq, step.Input);
                        Assert.Equal(step.ExpectedAccepted, accepted);
                        break;
                    }
                    case "acknowledge":
                    {
                        int count = buffer.Acknowledge(step.Seq);
                        Assert.Equal(step.ExpectedAcknowledgedCount, count);
                        break;
                    }
                    case "reconcile":
                    {
                        int state = buffer.Reconcile(
                            step.Seq,
                            step.State,
                            (s, _, input) => s + input);
                        Assert.Equal(step.ExpectedState, state);
                        break;
                    }
                    case "reset":
                        // The C# core has no seeding Reset(ack) overload; a
                        // seeded reset is emulated by clearing then acking.
                        buffer.Reset();
                        if (step.HasSeq) buffer.Acknowledge(step.Seq);
                        break;
                    default:
                        throw new InvalidOperationException($"unknown prediction op {step.Op}");
                }

                Assert.Equal(step.ExpectedPendingCount, buffer.Count);
                Assert.Equal(step.ExpectedOverflowCount, buffer.DroppedInputCount);
                Assert.Equal(step.ExpectedHasLastAck, buffer.HasLastAcknowledgement);
                if (step.ExpectedHasLastAck)
                    Assert.Equal(step.ExpectedLastAck, buffer.LastAcknowledgedSequence);
            }
        }

        [Fact]
        public void DecodeInputAckMatchesGoldenTrailer()
        {
            var golden = Golden.Load();
            var c = golden.InputAckFrame;
            Assert.True(c.HasInputAck);

            byte[] raw = Golden.Hex(c.HexBytes);
            var (header, pos) = DeltaDecoderCore.DecodeFrameHeader(raw, 0);
            Assert.Equal(c.Tick, header.Tick);
            Assert.Equal(c.Seq, header.Seq);
            Assert.Equal(c.Flags, header.Flags);
            Assert.Equal(c.FullCount, header.FullCount);
            Assert.Equal(c.DeltaCount, header.DeltaCount);
            Assert.Equal(c.RemovedCount, header.RemovedCount);
            Assert.Equal(c.ExitedCount, header.ExitedCount);
            Assert.NotEqual(0u, header.Flags & DeltaDecoderCore.FrameFlagInputAck);

            for (int i = 0; i < header.FullCount; i++)
            {
                var (entry, next) = DeltaDecoderCore.DecodeFullEntry(raw, pos);
                Assert.Equal(c.Full[i].NetID, entry.NetID);
                Assert.Equal(c.Full[i].Epoch, entry.Epoch);
                Assert.Equal(c.Full[i].EntityType, entry.EntityType);
                Assert.Equal(c.Full[i].ProducedAtMs, entry.ProducedAtMs);
                pos = next;
            }
            for (int i = 0; i < header.DeltaCount; i++)
            {
                var (entry, next) = DeltaDecoderCore.DecodeDeltaEntry(raw, pos);
                Assert.Equal(c.Delta[i].NetID, entry.NetID);
                Assert.Equal(c.Delta[i].Epoch, entry.Epoch);
                pos = next;
            }
            for (int i = 0; i < header.RemovedCount; i++)
            {
                Assert.Equal(c.RemovedIDs[i], DeltaDecoderCore.ReadUint32(raw, pos));
                pos += 4;
            }
            for (int i = 0; i < header.ExitedCount; i++)
            {
                Assert.Equal(c.ExitedIDs[i], DeltaDecoderCore.ReadUint32(raw, pos));
                pos += 4;
            }

            var (ack, end) = DeltaDecoderCore.DecodeInputAck(raw, pos, header.Flags);
            Assert.NotNull(ack);
            Assert.Equal(c.ExpectedInputAck, ack!.Value);
            Assert.Equal(raw.Length, end);
        }
    }
}
