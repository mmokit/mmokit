using System;
using System.Threading;
using System.Threading.Tasks;
using Mmokit.Sdk; // generated: BasicClient, AuthLoginRequest, WorldDelta, MoveTargetMsg, …

// Headless smoke-bot for the 4node-basic UDP SDK. Exercises the full live path —
// handshake → op-channel auth → server-pushed world state — and prints a verdict.
//
//   dotnet run --project csharp/Mmokit.Sdk.SmokeBot -- [host] [username] [password] [seconds]
//   defaults: 127.0.0.1  smokebot  4node-demo-password  12
//
// Exit code: 0 = full round-trip (ServerConfig + WorldDeltas), 1 = connect/auth
// failure, 2 = authed but no world state arrived (spawn/delta path not flowing).

string host = args.Length > 0 ? args[0] : "127.0.0.1";
// Default to a fresh username each run so AuthRegister always succeeds and the
// bot exercises the full working path. Pass an explicit username to test the
// AuthLogin (reconnect) path instead.
string username = args.Length > 1 ? args[1] : $"smoke-{Guid.NewGuid():N}".Substring(0, 14);
string password = args.Length > 2 ? args[2] : "4node-demo-password";
int runSeconds = args.Length > 3 && int.TryParse(args[3], out int s) ? s : 12;
const int port = 9000;
var opTimeout = TimeSpan.FromSeconds(5);

Console.WriteLine($"[smoke] connecting to {host}:{port} as '{username}' …");

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

// 1. Handshake (loop-until-ConnAccept lives in UdpTransport.Connect).
try
{
    client.Connect(host, port);
}
catch (Exception ex)
{
    Console.Error.WriteLine($"[smoke] CONNECT FAILED: {ex.Message}");
    Console.Error.WriteLine("[smoke]   • is the server up and serving UDP? (cd examples/4node-basic && just dev)");
    Console.Error.WriteLine("[smoke]   • WSL2→Windows? try the WSL IP (`hostname -I`) instead of 127.0.0.1");
    return 1;
}
Console.WriteLine("[smoke] handshake OK (ConnAccept received)");

// 2. Auth over the op channel. Exactly one SUCCESSFUL auth op — a successful
//    register/login authenticates the connection (the gateway then dispatches
//    PlayerAssignment → spawn), so a second auth op on the same connection is
//    redundant. Try register first; a duplicate username comes back as a hard
//    OperationError op-response (surfaced as an exception), so on any register
//    rejection we fall through to login. Mirrors the web demo's register-then-
//    login-on-conflict order.
bool authed = false;
try
{
    var reg = await client.AuthRegister(
        new AuthRegisterRequest { username = username, password = password, email = $"{username}@smoke.local" })
        .WaitAsync(opTimeout);
    if (reg.errorCode == 0)
    {
        Console.WriteLine($"[smoke] registered '{reg.username}' (token len={reg.sessionToken.Length}) — authenticated");
        authed = true;
    }
    else
    {
        Console.WriteLine($"[smoke] register soft-error {reg.errorCode} ({reg.errorMessage}) — logging in");
    }
}
catch (TimeoutException)
{
    Console.Error.WriteLine("[smoke] REGISTER TIMED OUT — op-channel reply never arrived (UDP 0x01 round-trip stuck).");
    client.Disconnect();
    return 1;
}
catch (Exception ex)
{
    Console.WriteLine($"[smoke] register rejected ({ex.Message}) — account likely exists, logging in");
}

if (!authed)
{
    try
    {
        var login = await client.AuthLogin(
            new AuthLoginRequest { username = username, password = password, mfaCode = "" })
            .WaitAsync(opTimeout);
        if (login.errorCode != 0)
        {
            Console.Error.WriteLine($"[smoke] LOGIN FAILED: errorCode={login.errorCode} {login.errorMessage}");
            client.Disconnect();
            return 1;
        }
        Console.WriteLine($"[smoke] login OK: user='{login.username}' token len={login.sessionToken.Length}");
    }
    catch (TimeoutException)
    {
        Console.Error.WriteLine("[smoke] LOGIN TIMED OUT — op-channel reply never arrived (UDP 0x01 round-trip stuck).");
        client.Disconnect();
        return 1;
    }
    catch (Exception ex)
    {
        Console.Error.WriteLine($"[smoke] LOGIN REJECTED: {ex.Message}");
        client.Disconnect();
        return 1;
    }
}

// 3. Drive movement and watch for server-pushed world state.
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
    Console.WriteLine("[smoke] ✅ PASS — full live round-trip: handshake + auth + world state flowing.");
    return 0;
}
Console.WriteLine("[smoke] ⚠️ INCOMPLETE — auth succeeded but no ServerConfig/WorldDeltas arrived.");
Console.WriteLine("[smoke]   Likely the op-channel login isn't driving PlayerAssignment/spawn on the gateway.");
return 2;
