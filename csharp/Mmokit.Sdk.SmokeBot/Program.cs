using System;
using System.Threading;
using System.Threading.Tasks;
using Mmokit.Sdk; // generated: BasicClient, WorldDelta, MoveTargetMsg, …
using Mmokit.Sdk.Core; // MmokitAuthException

// Headless smoke-bot for the 4node-basic UDP SDK. Exercises the full live path —
// HTTPS auth → UDP key → authenticated handshake → server-pushed world state —
// and prints a verdict.
//
//   dotnet run --project csharp/Mmokit.Sdk.SmokeBot -- [host] [username] [password] [seconds] [baseUrl]
//   defaults: 127.0.0.1  smoke-<random>  4node-demo-password  12  http://<host>:8080
//
// There is no auth step after the handshake. The client authenticates over
// HTTPS first and draws a short-lived UDP session key; the server binds the
// player from that key as the session is created, so a connected session is
// already an authenticated one. The op-channel AuthRegister/AuthLogin flow this
// bot used to run is gone with CE-005b Tier 2 — it authenticated a session that
// had already been carrying traffic, which is the ordering the phase existed to
// reverse.
//
// Exit code: 0 = full round-trip (ServerConfig + WorldDeltas), 1 = auth/connect
// failure, 2 = connected but no world state arrived (spawn/delta path not flowing).

string host = args.Length > 0 ? args[0] : "127.0.0.1";
// Default to a fresh username each run so the account is created on the spot.
// Pass an explicit username to exercise the returning-player path instead.
string username = args.Length > 1 ? args[1] : $"smoke-{Guid.NewGuid():N}".Substring(0, 14);
string password = args.Length > 2 ? args[2] : "4node-demo-password";
int runSeconds = args.Length > 3 && int.TryParse(args[3], out int s) ? s : 12;
const int port = 9000;
// The gateway's HTTP origin. Plain http works because `just dev` and
// `just distributed` both pass --dev-insecure-cookie; against a real deployment
// pass an https:// URL here.
string baseUrl = args.Length > 4 ? args[4] : $"http://{host}:8080";

Console.WriteLine($"[smoke] auth {baseUrl} as '{username}' → udp {host}:{port} …");

var client = new BasicClient();

// Counters are touched from the receive-pump thread; we only read them at the
// end after Disconnect() stops the pump, so plain fields are fine here.
int serverConfigs = 0, assigns = 0, deltas = 0, totalEntered = 0, totalUpdated = 0;
uint tickRate = 0, myNetId = 0;

client.OnServerConfig(c =>
{
    serverConfigs++;
    tickRate = c.tickRate;
    Console.WriteLine($"[smoke] ServerConfig: tickRate={c.tickRate}");
});
client.OnPlayerEntityAssigned(p =>
{
    assigns++;
    myNetId = p.entityNetID;
    Console.WriteLine($"[smoke] PlayerEntityAssigned: netID={p.entityNetID} pos=({p.worldX:F1},{p.worldY:F1})");
});
client.OnWorldDelta(m =>
{
    var u = client.Decoder.Decode(m.body, m.streamEpoch);
    if (u is null) return;
    deltas++;
    totalEntered += u.Entered.Count;
    totalUpdated += u.Updated.Count;
    if (deltas <= 5 || deltas % 20 == 0)
        Console.WriteLine($"[smoke] WorldDelta #{deltas}: tick={u.Tick} fresh={u.FreshSnapshot} " +
                          $"entered={u.Entered.Count} updated={u.Updated.Count} removed={u.Removed.Count} exited={u.Exited.Count}");
});

// 1. HTTPS auth + UDP key + handshake, in one call. registerIfMissing:true
//    because a smoke run owns its own throwaway account: it creates one on the
//    first run and logs in on later runs (the server answers 409 for a taken
//    username, which MmokitAuth treats as "log in instead", not as a failure).
//    Ordinary clients leave it false so connecting never creates an account.
try
{
    await client.ConnectAsync(baseUrl, host, port, username, password, registerIfMissing: true);
}
catch (MmokitAuthException ex)
{
    Console.Error.WriteLine($"[smoke] AUTH FAILED: {ex.Message}");
    Console.Error.WriteLine($"[smoke]   • is a server up and serving HTTP on {baseUrl}?");
    Console.Error.WriteLine("[smoke]       cd examples/4node-basic && just dev      (or: just distributed)");
    if (ex.StatusCode == 403)
    {
        Console.Error.WriteLine("[smoke]   • 403 means the server refuses to hand a UDP key to a plaintext");
        Console.Error.WriteLine("[smoke]     listener. Both dev recipes pass --dev-insecure-cookie; a server");
        Console.Error.WriteLine("[smoke]     launched by hand does not. Pass it, or use an https:// baseUrl.");
    }
    if (ex.StatusCode == 404)
    {
        Console.Error.WriteLine("[smoke]   • 404 means the process has no auth service, so it cannot issue");
        Console.Error.WriteLine("[smoke]     UDP keys. In distributed mode auth runs on the GATEWAY process.");
    }
    return 1;
}
catch (TimeoutException ex)
{
    // The UDP half. MmokitAuth translates every HTTP-side failure — including
    // a timeout, which .NET otherwise raises as TaskCanceledException — into
    // MmokitAuthException above, so anything reaching here is the handshake.
    Console.Error.WriteLine($"[smoke] UDP HANDSHAKE FAILED: {ex.Message}");
    Console.Error.WriteLine($"[smoke]   • auth succeeded, so HTTP is fine on {baseUrl}; is UDP listening on {host}:{port}?");
    Console.Error.WriteLine("[smoke]       cd examples/4node-basic && just dev      (or: just distributed)");
    Console.Error.WriteLine("[smoke]     Both pass --udp-listen=:9000 for you. Launching the binary by");
    Console.Error.WriteLine("[smoke]     hand does NOT — the server default is off.");
    Console.Error.WriteLine("[smoke]     Look for 'udp: listening on :9000' in the log; 'udp: listener");
    Console.Error.WriteLine("[smoke]     disabled' means no flag reached it. In distributed mode only the");
    Console.Error.WriteLine("[smoke]     GATEWAY binds UDP — check the gateway pane, not a host pane.");
    Console.Error.WriteLine("[smoke]   • WSL2→Windows? try the WSL IP (`hostname -I`) instead of 127.0.0.1");
    return 1;
}
Console.WriteLine("[smoke] connected — authenticated UDP session established");

// 2. Drive movement and watch for server-pushed world state.
Console.WriteLine($"[smoke] running {runSeconds}s — sending MoveTargetMsg every 1s, watching for world deltas …");
var rng = new Random(0xBEEF);
for (int i = 0; i < runSeconds; i++)
{
    float x = 100f + (float)rng.NextDouble() * 800f;
    float y = 100f + (float)rng.NextDouble() * 800f;
    client.SendMoveTargetMsg(new MoveTargetMsg { x = x, y = y });
    await Task.Delay(1000);
}

client.Disconnect();

Console.WriteLine("──────────────────────────────────────────────");
Console.WriteLine($"[smoke] SUMMARY  ServerConfig={serverConfigs} (tickRate={tickRate})  " +
                  $"PlayerEntityAssigned={assigns} (netID={myNetId})  " +
                  $"WorldDeltas={deltas}  enteredTotal={totalEntered}  updatedTotal={totalUpdated}");

bool ok = serverConfigs > 0 && deltas > 0;
if (ok)
{
    Console.WriteLine("[smoke] ✅ PASS — full live round-trip: HTTPS auth + authenticated UDP + world state flowing.");
    return 0;
}
Console.WriteLine("[smoke] ⚠️ INCOMPLETE — session established but no ServerConfig/WorldDeltas arrived.");
Console.WriteLine("[smoke]   The UDP key authenticated the transport but no player was bound: check the");
Console.WriteLine("[smoke]   server log for 'udp: new authenticated connection … user=' followed by a");
Console.WriteLine("[smoke]   'gateway: conn=… user=… -> <cell>' line.");
return 2;
