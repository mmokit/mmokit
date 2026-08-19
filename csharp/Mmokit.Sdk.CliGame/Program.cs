using System;
using System.Collections.Generic;
using System.Globalization;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
using Mmokit.Sdk;      // generated: BasicClient, PlayerEntity, BotEntity, MoveTargetMsg, …
using Mmokit.Sdk.Core; // MmokitAuthException

// A playable terminal client for examples/4node-basic, over the UDP transport.
//
//   dotnet run --project csharp/Mmokit.Sdk.CliGame -- [host] [username] [password] [baseUrl]
//   defaults: 127.0.0.1  runner-<random>  4node-demo-password  http://<host>:8080
//
// Why this exists alongside the smoke-bot: the smoke-bot proves the transport
// works and exits. This one is driven by a person, so it exercises the parts a
// scripted round-trip never touches — sustained input, entity churn in and out
// of AoI, and what the world actually looks like when the server is the only
// authority on it. It is deliberately a game rather than a viewer: you can only
// find out whether movement feels right by moving.
//
// It is also the smallest complete example of the C# SDK. The Unity client is
// the SDK's real target and needs Unity to run; this needs a terminal.
//
// CONTROLS
//   W A S D / arrows  set a move target one step from where you are
//   SPACE             stop (target = current position)
//   Q or Ctrl-C       quit
//
// There is no auth step after the handshake: the client authenticates over
// HTTPS, draws a short-lived UDP session key, and the server binds the player
// from that key as the session is created. A connected session is already an
// authenticated one.

const int UdpPort = 9000;
const float StepDistance = 220f;   // world units per keypress
const int RenderHz = 12;

string host = args.Length > 0 ? args[0] : "127.0.0.1";
string username = args.Length > 1 ? args[1] : $"runner-{Guid.NewGuid():N}".Substring(0, 13);
string password = args.Length > 2 ? args[2] : "4node-demo-password";
string baseUrl = args.Length > 3 ? args[3] : $"http://{host}:8080";

var world = new WorldView();
var client = new BasicClient();

client.OnServerConfig(c => world.TickRate = c.tickRate);
client.OnPlayerEntityAssigned(p =>
{
    world.MyNetID = p.entityNetID;
    world.MyX = p.worldX;
    world.MyY = p.worldY;
    world.Note($"spawned at ({p.worldX:F0}, {p.worldY:F0})");
});
client.OnPong(p => world.LatencyMs = DateTimeOffset.UtcNow.ToUnixTimeMilliseconds() - p.clientTime);
client.OnWorldDelta(m =>
{
    var update = client.Decoder.Decode(m.body, m.streamEpoch);
    if (update is null) return;
    world.Apply(update);
});

Console.WriteLine($"[cli] auth {baseUrl} as '{username}' → udp {host}:{UdpPort} …");
try
{
    // registerIfMissing: this client owns its throwaway account. The server
    // answers 409 for a taken username, which MmokitAuth treats as "log in
    // instead" rather than as a failure, so re-running with the same name
    // exercises the returning-player path.
    await client.ConnectAsync(baseUrl, host, UdpPort, username, password, registerIfMissing: true);
}
catch (MmokitAuthException ex)
{
    Console.Error.WriteLine($"[cli] AUTH FAILED: {ex.Message}");
    Console.Error.WriteLine($"[cli]   • is a server up and serving HTTP on {baseUrl}?");
    Console.Error.WriteLine("[cli]       cd examples/4node-basic && just dev      (or: just distributed)");
    if (ex.StatusCode == 403)
    {
        Console.Error.WriteLine("[cli]   • 403: the server refuses to hand a UDP key to a plaintext listener.");
        Console.Error.WriteLine("[cli]     Both dev recipes pass --dev-insecure-cookie; a hand-launched server");
        Console.Error.WriteLine("[cli]     does not. Pass it, or use an https:// baseUrl.");
    }
    if (ex.StatusCode == 404)
    {
        Console.Error.WriteLine("[cli]   • 404: this process has no auth service, so it cannot issue UDP keys.");
        Console.Error.WriteLine("[cli]     In distributed mode auth runs on the GATEWAY process.");
    }
    if (ex.StatusCode == 409)
    {
        Console.Error.WriteLine($"[cli]   • 409: schema fingerprint mismatch. This SDK was generated from a");
        Console.Error.WriteLine($"[cli]     different build than the server is running ({BasicProtocol.SchemaFingerprint}).");
        Console.Error.WriteLine("[cli]     Regenerate: just csharp-sdk   (and rebuild this client)");
    }
    return 1;
}
catch (TimeoutException ex)
{
    Console.Error.WriteLine($"[cli] UDP HANDSHAKE FAILED: {ex.Message}");
    Console.Error.WriteLine($"[cli]   • auth succeeded, so HTTP is fine; is UDP listening on {host}:{UdpPort}?");
    Console.Error.WriteLine("[cli]     Both dev recipes pass --udp-listen=:9000; the server default is OFF.");
    Console.Error.WriteLine("[cli]     In distributed mode only the GATEWAY binds UDP.");
    Console.Error.WriteLine("[cli]   • WSL2→Windows? try the WSL IP (`hostname -I`) instead of 127.0.0.1");
    return 1;
}

world.Note("connected — authenticated UDP session");

using var quit = new CancellationTokenSource();
Console.CancelKeyPress += (_, e) => { e.Cancel = true; quit.Cancel(); };

// Keep the server's view of us alive and measure round-trip time. Ping is an
// engine-default client input; every game gets the handler for free.
var pinger = Task.Run(async () =>
{
    while (!quit.IsCancellationRequested)
    {
        client.SendPing(new Ping { clientTime = DateTimeOffset.UtcNow.ToUnixTimeMilliseconds() });
        try { await Task.Delay(2000, quit.Token); } catch (OperationCanceledException) { return; }
    }
});

// Input runs on its own thread because Console.ReadKey blocks. Setting a move
// TARGET rather than a velocity is the server's model: ClickToMoveSystem walks
// the entity toward it, so a dropped input costs a little distance, never a
// desynced position. That is what makes this safe to send over an unreliable
// channel.
var input = new Thread(() =>
{
    // Console.KeyAvailable throws when stdin is redirected — a pipe, or a CI
    // runner. Degrade to a read-only view rather than taking the process down
    // with it; the render loop is still worth watching.
    if (Console.IsInputRedirected)
    {
        world.Note("stdin is not a terminal — input disabled, view only");
        return;
    }
    while (!quit.IsCancellationRequested)
    {
        if (!Console.KeyAvailable) { Thread.Sleep(15); continue; }
        var key = Console.ReadKey(intercept: true).Key;
        if (key == ConsoleKey.Q) { quit.Cancel(); return; }
        // Every move target is computed from where the server last said we are,
        // so pressing a key before PlayerEntityAssigned arrives would aim from
        // (0,0) and walk us to the world origin. A person never wins that race;
        // a piped keystroke wins it every time.
        if (world.MyNetID == 0) { world.Note("not spawned yet — input ignored"); continue; }
        float dx = 0, dy = 0;
        switch (key)
        {
            case ConsoleKey.W: case ConsoleKey.UpArrow:    dy = -1; break;
            case ConsoleKey.S: case ConsoleKey.DownArrow:  dy = +1; break;
            case ConsoleKey.A: case ConsoleKey.LeftArrow:  dx = -1; break;
            case ConsoleKey.D: case ConsoleKey.RightArrow: dx = +1; break;
            case ConsoleKey.Spacebar: break;               // stop where we are
            default: continue;
        }
        // Reliable: each keypress is a discrete intent, not a sample of a
        // continuous stick. The generated API lets high-rate replaceable input
        // opt out instead — that is the case unreliable delivery is for.
        client.SendMoveTargetMsg(new MoveTargetMsg
        {
            x = world.MyX + dx * StepDistance,
            y = world.MyY + dy * StepDistance,
        });
    }
}) { IsBackground = true };
input.Start();

Console.CursorVisible = false;
Console.Clear();
try
{
    while (!quit.IsCancellationRequested)
    {
        Console.Write(world.Render(username));
        try { await Task.Delay(1000 / RenderHz, quit.Token); } catch (OperationCanceledException) { break; }
    }
}
finally
{
    Console.CursorVisible = true;
    Console.Write("\x1b[0m");
    Console.Clear();
}

client.Disconnect();
await pinger;
Console.WriteLine($"[cli] disconnected after {world.Deltas} world deltas");
return 0;

// ---------------------------------------------------------------------------

/// The client's whole model of the world: whatever the server last said.
///
/// Entities are keyed by NetID and only ever mutated from a decoded delta —
/// there is no client-side simulation here, deliberately. A terminal client
/// that predicted movement would hide exactly the thing this example is useful
/// for showing, which is what the authoritative stream actually looks like.
sealed class WorldView
{
    public uint MyNetID;
    public float MyX, MyY;
    public uint TickRate;
    public uint Tick;
    public long LatencyMs = -1;
    public int Deltas;

    readonly Dictionary<uint, Actor> _actors = new();
    readonly List<string> _log = new();

    public void Note(string line)
    {
        lock (_log)
        {
            _log.Add(line);
            if (_log.Count > 4) _log.RemoveAt(0);
        }
    }

    public void Apply(DeltaWorldUpdate u)
    {
        lock (_actors)
        {
            Deltas++;
            Tick = u.Tick;
            foreach (var e in u.Entered) Upsert(e);
            foreach (var e in u.Updated) Upsert(e);
            foreach (var id in u.Removed) _actors.Remove(id);
            // Exited means "left my area of interest", not "destroyed". Dropping
            // it is right for a viewer: the entity is no longer being told to us,
            // so continuing to draw it would be drawing a memory.
            foreach (var id in u.Exited) _actors.Remove(id);
        }
    }

    void Upsert(EntityBase e)
    {
        Actor a = e switch
        {
            PlayerEntity p => new Actor(p.NetID, p.worldX, p.worldY, p.name, true, p.r, p.g, p.b),
            BotEntity b => new Actor(b.NetID, b.worldX, b.worldY, "", false, 160, 160, 160),
            _ => default,
        };
        if (a.NetID == 0) return;
        _actors[a.NetID] = a;
        if (a.NetID == MyNetID) { MyX = a.X; MyY = a.Y; }
    }

    public string Render(string username)
    {
        const int w = 72, h = 22;
        // World units per cell, chosen so a 2000-unit 4node cell is a bit wider
        // than the viewport — you can see a cell boundary approach.
        const float scale = 60f;

        var grid = new char[h, w];
        var colour = new string[h, w];
        for (int y = 0; y < h; y++)
            for (int x = 0; x < w; x++) grid[y, x] = ' ';

        List<Actor> snapshot;
        lock (_actors) snapshot = new List<Actor>(_actors.Values);

        foreach (var a in snapshot)
        {
            // Screen-centre is always us, so the view is what the player's own
            // AoI covers rather than a fixed window on the world.
            int sx = (int)MathF.Round((a.X - MyX) / scale) + w / 2;
            int sy = (int)MathF.Round((a.Y - MyY) / (scale * 2)) + h / 2;
            if (sx < 0 || sx >= w || sy < 0 || sy >= h) continue;

            char glyph;
            if (a.NetID == MyNetID) glyph = '@';
            else if (!a.IsPlayer) glyph = 'o';
            else glyph = a.Name.Length > 0 ? char.ToUpperInvariant(a.Name[0]) : 'P';

            grid[sy, sx] = glyph;
            colour[sy, sx] = a.NetID == MyNetID ? "\x1b[1;97m" : Ansi(a.R, a.G, a.B);
        }

        var sb = new StringBuilder(8192);
        sb.Append("\x1b[H"); // home, then overwrite — no scrollback churn
        sb.Append("\x1b[0m┌").Append('─', w).Append("┐\n");
        for (int y = 0; y < h; y++)
        {
            sb.Append('│');
            for (int x = 0; x < w; x++)
            {
                if (colour[y, x] is string c) sb.Append(c).Append(grid[y, x]).Append("\x1b[0m");
                else sb.Append(grid[y, x]);
            }
            sb.Append("│\n");
        }
        sb.Append('└').Append('─', w).Append("┘\n");

        string lat = LatencyMs < 0 ? "  — " : $"{LatencyMs,3}ms";
        int players = 0, bots = 0;
        foreach (var a in snapshot) { if (a.IsPlayer) players++; else bots++; }

        sb.Append($" {username}  pos ({MyX,7:F0},{MyY,7:F0})  tick {Tick,-8} {TickRate}Hz  rtt {lat}\n");
        sb.Append($" players {players,-3} bots {bots,-3} deltas {Deltas,-6}  [WASD] move  [space] stop  [q] quit\n");

        lock (_log)
            for (int i = 0; i < 4; i++)
                sb.Append(" \x1b[2m").Append((i < _log.Count ? _log[i] : "").PadRight(w)).Append("\x1b[0m\n");

        return sb.ToString();
    }

    static string Ansi(byte r, byte g, byte b) =>
        string.Create(CultureInfo.InvariantCulture, $"\x1b[38;2;{r};{g};{b}m");

    readonly record struct Actor(uint NetID, float X, float Y, string Name, bool IsPlayer, byte R, byte G, byte B);
}
