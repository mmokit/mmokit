using System;
using System.Collections.Generic;
using System.IO;
using System.Net;
using System.Text;
using System.Threading;
using System.Threading.Tasks;
using Mmokit.Sdk.Core;
using Xunit;

namespace Mmokit.Sdk.Core.Tests
{
    /// <summary>
    /// The HTTPS half of the client handshake (CE-005b Tier 2, unit 6).
    ///
    /// These run against a real loopback HttpListener rather than a mocked
    /// handler, because every bug this code can have is in the HTTP details:
    /// which status means "log in instead", whether the cookie survives the
    /// round trip, and whether a uint64 key ID survives being carried as hex.
    /// A mock that returns what the client expects would test none of them.
    ///
    /// The server's shapes are pinned to the Go side: /auth/register and
    /// /auth/login answer with Set-Cookie: mmokit-session=… (pkg/services/auth
    /// cookie.go), 409 means the username is taken (http.go's status map), and
    /// /auth/udp-key answers {"keyId","key","expiresAtMs"} with keyId in hex
    /// (pkg/universe/udp_key_http.go).
    /// </summary>
    public class MmokitAuthTests
    {
        const string Token = "s3cr3t-session-token-value";
        const string KeyIdHex = "0011223344556677";
        const ulong KeyIdValue = 0x0011223344556677UL;

        static byte[] TestKey()
        {
            var k = new byte[UdpCrypto.KeySize];
            for (int i = 0; i < k.Length; i++) k[i] = (byte)(0x5a ^ i);
            return k;
        }

        [Fact]
        public async Task Register_Succeeds_And_Draws_Key()
        {
            using var srv = new FakeAuthServer { RegisterStatus = 200 };
            srv.Start();

            UdpKeyCredential cred = await MmokitAuth.FetchUdpKeyAsync(srv.BaseUrl, "alice", "hunter2hunter2", registerIfMissing: true);

            Assert.Equal(KeyIdValue, cred.KeyId);
            Assert.Equal(TestKey(), cred.Key);
            Assert.Equal(1_700_000_000_000L, cred.ExpiresAtMs);
            // Registered, so no login call was needed.
            Assert.Equal(new[] { "/auth/register", "/auth/udp-key" }, srv.Paths);
            // The key request must have carried the cookie the register set.
            Assert.Equal("mmokit-session=" + Token, srv.UdpKeyCookieHeader);
        }

        // 409 is the expected answer on every run after the first. It must fall
        // through to login, not surface as a failure.
        [Fact]
        public async Task UsernameTaken_Falls_Through_To_Login()
        {
            using var srv = new FakeAuthServer { RegisterStatus = 409 };
            srv.Start();

            UdpKeyCredential cred = await MmokitAuth.FetchUdpKeyAsync(srv.BaseUrl, "alice", "hunter2hunter2", registerIfMissing: true);

            Assert.Equal(KeyIdValue, cred.KeyId);
            Assert.Equal(new[] { "/auth/register", "/auth/login", "/auth/udp-key" }, srv.Paths);
            Assert.Equal("mmokit-session=" + Token, srv.UdpKeyCookieHeader);
        }

        // A register rejection that is NOT 409 — a too-weak password, say — is a
        // real failure. Falling through to login would mask it behind a second,
        // more confusing error.
        [Fact]
        public async Task NonConflictRegisterFailure_Does_Not_Attempt_Login()
        {
            using var srv = new FakeAuthServer { RegisterStatus = 400, ErrorMessage = "password too weak" };
            srv.Start();

            var ex = await Assert.ThrowsAsync<MmokitAuthException>(
                () => MmokitAuth.FetchUdpKeyAsync(srv.BaseUrl, "alice", "short", registerIfMissing: true));

            Assert.Equal(400, ex.StatusCode);
            Assert.Equal("password too weak", ex.ServerMessage);
            Assert.Equal(new[] { "/auth/register" }, srv.Paths);
        }

        /// The trap this client exists to avoid. When the server is started with
        /// --cors-origins, CrossSiteHTTPOpts forces Secure=true on the session
        /// cookie even on a plaintext dev listener — which is exactly what
        /// `just distributed` does. A CookieContainer-based client silently
        /// drops a Secure cookie received over http:// and then gets a bare 401
        /// from /auth/udp-key with nothing to explain it. Reading Set-Cookie
        /// directly is scheme-agnostic, and this pins that.
        [Fact]
        public async Task SecureCookie_Over_PlainHttp_Is_Still_Sent()
        {
            using var srv = new FakeAuthServer { RegisterStatus = 200, SecureCookie = true };
            srv.Start();

            Assert.StartsWith("http://", srv.BaseUrl);
            UdpKeyCredential cred = await MmokitAuth.FetchUdpKeyAsync(srv.BaseUrl, "alice", "hunter2hunter2", registerIfMissing: true);

            Assert.Equal(KeyIdValue, cred.KeyId);
            Assert.Equal("mmokit-session=" + Token, srv.UdpKeyCookieHeader);
        }

        // 403 is the server refusing to put a bearer secret on a plaintext
        // listener. The message has to say so, or the reader goes hunting in
        // the client for a bug that is a server flag.
        [Fact]
        public async Task PlaintextRefusal_Explains_Itself()
        {
            using var srv = new FakeAuthServer { RegisterStatus = 200, UdpKeyStatus = 403 };
            srv.Start();

            var ex = await Assert.ThrowsAsync<MmokitAuthException>(
                () => MmokitAuth.FetchUdpKeyAsync(srv.BaseUrl, "alice", "hunter2hunter2", registerIfMissing: true));

            Assert.Equal(403, ex.StatusCode);
            Assert.Contains("--dev-insecure-cookie", ex.Message);
        }

        [Fact]
        public async Task MissingAuthService_Explains_Itself()
        {
            using var srv = new FakeAuthServer { RegisterStatus = 200, UdpKeyStatus = 404 };
            srv.Start();

            var ex = await Assert.ThrowsAsync<MmokitAuthException>(
                () => MmokitAuth.FetchUdpKeyAsync(srv.BaseUrl, "alice", "hunter2hunter2", registerIfMissing: true));

            Assert.Equal(404, ex.StatusCode);
            Assert.Contains("auth service", ex.Message);
        }

        // A key of the wrong length would fail later inside HKDF with a message
        // about a master key, far from the cause. Catch it at the boundary.
        [Fact]
        public async Task WrongLengthKey_Is_Rejected_At_The_Boundary()
        {
            using var srv = new FakeAuthServer { RegisterStatus = 200, KeyOverride = new byte[16] };
            srv.Start();

            var ex = await Assert.ThrowsAsync<MmokitAuthException>(
                () => MmokitAuth.FetchUdpKeyAsync(srv.BaseUrl, "alice", "hunter2hunter2", registerIfMissing: true));

            Assert.Contains("16 bytes", ex.Message);
            Assert.Contains("32", ex.Message);
        }

        // keyId rides as hex precisely because a JSON number loses precision
        // above 2^53. A value in that range must survive intact.
        [Fact]
        public async Task LargeKeyId_Survives_The_Hex_Round_Trip()
        {
            const ulong big = 0xFEDCBA9876543210UL;
            using var srv = new FakeAuthServer { RegisterStatus = 200, KeyIdHexOverride = "fedcba9876543210" };
            srv.Start();

            UdpKeyCredential cred = await MmokitAuth.FetchUdpKeyAsync(srv.BaseUrl, "alice", "hunter2hunter2", registerIfMissing: true);

            Assert.Equal(big, cred.KeyId);
            Assert.True(cred.KeyId > (1UL << 53), "test value must exceed the JSON-number-safe range to be meaningful");
        }

        // The default path must NOT register. Connecting is not account creation,
        // and a doomed register before every login would also double the per-IP
        // failures the server counts — a fleet of bots on one address would
        // rate-limit itself in half the time.
        [Fact]
        public async Task Default_Path_Logs_In_Without_Registering()
        {
            using var srv = new FakeAuthServer { RegisterStatus = 200 };
            srv.Start();

            UdpKeyCredential cred = await MmokitAuth.FetchUdpKeyAsync(srv.BaseUrl, "alice", "hunter2hunter2");

            Assert.Equal(KeyIdValue, cred.KeyId);
            Assert.Equal(new[] { "/auth/login", "/auth/udp-key" }, srv.Paths);
        }

        // An application with its own login UI holds a session already and must
        // not be forced back through register/login to get a key.
        [Fact]
        public async Task ExistingSession_Skips_Register_And_Login()
        {
            using var srv = new FakeAuthServer { RegisterStatus = 200 };
            srv.Start();

            UdpKeyCredential cred = await MmokitAuth.FetchUdpKeyWithSessionAsync(srv.BaseUrl, Token);

            Assert.Equal(KeyIdValue, cred.KeyId);
            Assert.Equal(new[] { "/auth/udp-key" }, srv.Paths);
        }

        // A nested field of the same name must read as absent, not be returned
        // in place of the real one. Returning the wrong value is worse than
        // returning none, because nothing downstream can tell.
        [Fact]
        public async Task NestedFieldOfSameName_Does_Not_Shadow_The_Real_One()
        {
            using var srv = new FakeAuthServer
            {
                RegisterStatus = 400,
                RawErrorBody = "{\"detail\":{\"message\":\"INNER\"},\"message\":\"OUTER\"}",
            };
            srv.Start();

            var ex = await Assert.ThrowsAsync<MmokitAuthException>(
                () => MmokitAuth.FetchUdpKeyAsync(srv.BaseUrl, "alice", "hunter2hunter2", registerIfMissing: true));

            Assert.Equal("OUTER", ex.ServerMessage);
        }

        // A malformed \u escape must not throw out of the error path — that
        // would replace the real HTTP failure with a parse failure.
        [Fact]
        public async Task MalformedUnicodeEscape_Does_Not_Throw_Over_The_Real_Error()
        {
            using var srv = new FakeAuthServer
            {
                RegisterStatus = 400,
                RawErrorBody = "{\"message\":\"bad \\uZZZZ escape\"}",
            };
            srv.Start();

            var ex = await Assert.ThrowsAsync<MmokitAuthException>(
                () => MmokitAuth.FetchUdpKeyAsync(srv.BaseUrl, "alice", "hunter2hunter2", registerIfMissing: true));

            Assert.Equal(400, ex.StatusCode);
        }

        // A 200 body from /auth/udp-key carries the master key. A malformed one
        // must not put that body into a public exception property, which is
        // exactly the thing that ends up in a log or a crash report.
        [Fact]
        public async Task MalformedKeyResponse_Does_Not_Leak_The_Key_Into_The_Exception()
        {
            const string secret = "c2VjcmV0LW1hc3Rlci1rZXktbWF0ZXJpYWwtaGVyZQ==";
            using var srv = new FakeAuthServer
            {
                RegisterStatus = 200,
                RawUdpKeyBody = "{\"keyId\":\"nothex\",\"key\":\"" + secret + "\",\"expiresAtMs\":1}",
            };
            srv.Start();

            var ex = await Assert.ThrowsAsync<MmokitAuthException>(
                () => MmokitAuth.FetchUdpKeyAsync(srv.BaseUrl, "alice", "hunter2hunter2", registerIfMissing: true));

            Assert.DoesNotContain(secret, ex.Message);
            Assert.DoesNotContain(secret, ex.ServerMessage ?? "");
        }

        // An unreachable server must produce MmokitAuthException, not the raw
        // TaskCanceledException .NET raises when HttpClient.Timeout expires —
        // that type escapes every catch clause a caller would sensibly write.
        [Fact]
        public async Task UnreachableServer_Surfaces_As_MmokitAuthException()
        {
            // Port 1 on loopback: refused immediately, so this does not wait out
            // the 15s client timeout. Exercises the HttpRequestException arm;
            // the timeout arm shares the same translation.
            var ex = await Assert.ThrowsAsync<MmokitAuthException>(
                () => MmokitAuth.FetchUdpKeyAsync("http://127.0.0.1:1", "alice", "hunter2hunter2"));

            Assert.Equal(0, ex.StatusCode);
            Assert.Contains("/auth/login", ex.Message);
        }

        /// Minimal stand-in for the gateway's client-facing listener, serving
        /// the three routes the client uses with the Go side's shapes.
        sealed class FakeAuthServer : IDisposable
        {
            readonly HttpListener _listener = new HttpListener();
            readonly List<string> _paths = new List<string>();
            readonly object _lock = new object();
            CancellationTokenSource? _cts;

            public int RegisterStatus { get; set; } = 200;
            public int UdpKeyStatus { get; set; } = 200;
            public bool SecureCookie { get; set; }
            public string? ErrorMessage { get; set; }
            public byte[]? KeyOverride { get; set; }
            public string? KeyIdHexOverride { get; set; }
            public string? RawErrorBody { get; set; }
            public string? RawUdpKeyBody { get; set; }

            public string BaseUrl { get; private set; } = "";
            public string? UdpKeyCookieHeader { get; private set; }

            public string[] Paths
            {
                get { lock (_lock) { return _paths.ToArray(); } }
            }

            public void Start()
            {
                // Port 0 is not available to HttpListener, so probe upward for
                // a free one rather than risk a collision with a parallel test.
                var rng = new Random();
                for (int attempt = 0; attempt < 20; attempt++)
                {
                    int port = rng.Next(20000, 60000);
                    string prefix = $"http://127.0.0.1:{port}/";
                    try
                    {
                        _listener.Prefixes.Clear();
                        _listener.Prefixes.Add(prefix);
                        _listener.Start();
                        BaseUrl = prefix.TrimEnd('/');
                        break;
                    }
                    catch (HttpListenerException)
                    {
                        // Port taken — try another.
                    }
                }
                if (!_listener.IsListening) throw new InvalidOperationException("could not bind a loopback port");

                _cts = new CancellationTokenSource();
                CancellationToken ct = _cts.Token;
                _ = Task.Run(async () =>
                {
                    while (!ct.IsCancellationRequested)
                    {
                        HttpListenerContext ctx;
                        try { ctx = await _listener.GetContextAsync().ConfigureAwait(false); }
                        catch (Exception) { return; } // listener stopped
                        try { Handle(ctx); } catch (Exception) { /* keep serving */ }
                    }
                });
            }

            void Handle(HttpListenerContext ctx)
            {
                string path = ctx.Request.Url!.AbsolutePath;
                lock (_lock) { _paths.Add(path); }

                switch (path)
                {
                    case "/auth/register":
                        Respond(ctx, RegisterStatus, RegisterStatus == 200);
                        return;
                    case "/auth/login":
                        Respond(ctx, 200, true);
                        return;
                    case "/auth/udp-key":
                        UdpKeyCookieHeader = ctx.Request.Headers["Cookie"];
                        if (UdpKeyStatus != 200)
                        {
                            RespondPlainText(ctx, UdpKeyStatus, UdpKeyStatus switch
                            {
                                404 => "udp key issuance unavailable",
                                403 => "udp key issuance requires TLS",
                                _ => "unauthenticated",
                            });
                            return;
                        }
                        if (RawUdpKeyBody != null) { WriteBody(ctx, 200, RawUdpKeyBody); return; }
                        WriteBody(ctx, 200,
                            "{\"keyId\":\"" + (KeyIdHexOverride ?? KeyIdHex) + "\"," +
                            "\"key\":\"" + Convert.ToBase64String(KeyOverride ?? TestKey()) + "\"," +
                            "\"expiresAtMs\":1700000000000}");
                        return;
                    default:
                        Respond(ctx, 404, false);
                        return;
                }
            }

            void Respond(HttpListenerContext ctx, int status, bool setCookie)
            {
                if (setCookie)
                {
                    string attrs = "Path=/; HttpOnly; SameSite=Strict" + (SecureCookie ? "; Secure" : "");
                    ctx.Response.Headers.Add("Set-Cookie", $"mmokit-session={Token}; {attrs}; Max-Age=2592000");
                    WriteBody(ctx, status, "{\"user_id\":\"11111111-2222-3333-4444-555555555555\",\"username\":\"alice\",\"expires_at_ms\":1700000000000}");
                    return;
                }
                WriteBody(ctx, status, RawErrorBody ?? ("{\"code\":7,\"message\":\"" + (ErrorMessage ?? "denied") + "\"}"));
            }

            // /auth/udp-key rejects with http.Error, which is text/plain with no
            // JSON envelope — unlike the /auth/* handlers. Reproduced exactly so
            // the client's error path is exercised against a body it cannot
            // parse a "message" out of, which is what it will actually meet.
            static void RespondPlainText(HttpListenerContext ctx, int status, string text)
            {
                byte[] buf = Encoding.UTF8.GetBytes(text + "\n");
                ctx.Response.StatusCode = status;
                ctx.Response.ContentType = "text/plain; charset=utf-8";
                ctx.Response.ContentLength64 = buf.Length;
                using Stream os = ctx.Response.OutputStream;
                os.Write(buf, 0, buf.Length);
            }

            static void WriteBody(HttpListenerContext ctx, int status, string body)
            {
                byte[] buf = Encoding.UTF8.GetBytes(body);
                ctx.Response.StatusCode = status;
                ctx.Response.ContentType = "application/json";
                ctx.Response.ContentLength64 = buf.Length;
                using Stream os = ctx.Response.OutputStream;
                os.Write(buf, 0, buf.Length);
            }

            public void Dispose()
            {
                _cts?.Cancel();
                try { _listener.Stop(); } catch (Exception) { }
                try { _listener.Close(); } catch (Exception) { }
            }
        }
    }
}
