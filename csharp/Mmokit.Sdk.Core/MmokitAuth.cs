using System;
using System.Collections.Generic;
using System.Globalization;
using System.Net;
using System.Net.Http;
using System.Text;
using System.Threading;
using System.Threading.Tasks;

namespace Mmokit.Sdk.Core
{
    /// The transport credential a UDP client must hold before it can open a
    /// session: the key ID it names on ConnConfirm, and the 32-byte master key
    /// both sides derive their directional AEAD keys from.
    ///
    /// Issued by POST /auth/udp-key and short-lived (the server's default TTL
    /// is 5 minutes). It is a bearer secret for that window — do not log it,
    /// do not persist it, and fetch a fresh one per connect rather than caching
    /// it across sessions.
    public sealed class UdpKeyCredential
    {
        /// Names the key on the UDP handshake. Not secret: the server refuses
        /// any ConnConfirm whose cookie does not verify, so knowing an ID
        /// without the key buys nothing.
        public ulong KeyId { get; }

        /// The 32-byte master key. Secret.
        public byte[] Key { get; }

        /// Unix-epoch milliseconds at which the server will stop resolving
        /// KeyId. Handshakes attempted after this fail silently by design.
        public long ExpiresAtMs { get; }

        public UdpKeyCredential(ulong keyId, byte[] key, long expiresAtMs)
        {
            KeyId = keyId;
            Key = key ?? throw new ArgumentNullException(nameof(key));
            ExpiresAtMs = expiresAtMs;
        }
    }

    /// Thrown when an auth or key-issuance HTTP call fails. StatusCode is the
    /// HTTP status; ServerMessage is the server's own message when it sent a
    /// JSON error body, so callers can branch on the reason rather than parse
    /// a formatted string.
    public sealed class MmokitAuthException : Exception
    {
        public int StatusCode { get; }
        public string? ServerMessage { get; }

        public MmokitAuthException(string message, int statusCode, string? serverMessage = null)
            : base(message)
        {
            StatusCode = statusCode;
            ServerMessage = serverMessage;
        }
    }

    /// HTTPS half of the client handshake: authenticate over HTTP, then draw a
    /// UDP session key from POST /auth/udp-key and hand it to
    /// UdpTransport.Connect.
    ///
    /// This ordering is the point of CE-005b Tier 2. The v1 client built a UDP
    /// session first and authenticated afterwards over the op channel, which
    /// meant every byte before that op was forgeable on path and the token was
    /// itself the credential. Now the key is drawn over a channel that already
    /// has confidentiality and server authentication, and the UDP session is
    /// authenticated from its first datagram.
    ///
    /// Unity note: this uses System.Net.Http.HttpClient, which Mono and IL2CPP
    /// both implement on the platforms that can open a UDP socket at all.
    /// WebGL implements neither, and is out of scope for the UDP transport for
    /// that reason rather than this one.
    public static class MmokitAuth
    {
        /// The cookie the auth service sets. Matches auth.DefaultHTTPOpts()
        /// on the Go side; override when the server was configured with a
        /// different HTTPOpts.CookieName.
        public const string DefaultCookieName = "mmokit-session";

        /// Authenticate as `username` and return a UDP session key.
        ///
        /// baseUrl is the gateway's HTTP origin, e.g. "https://game.example:8080".
        /// Plain http:// works only against a server started with
        /// --dev-insecure-cookie; otherwise /auth/udp-key returns 403 rather
        /// than put a bearer secret on a plaintext listener.
        ///
        /// registerIfMissing defaults to FALSE, so connecting never creates an
        /// account as a side effect. Pass true for a bot or a demo that owns
        /// its own account namespace.
        public static async Task<UdpKeyCredential> FetchUdpKeyAsync(
            string baseUrl,
            string username,
            string password,
            bool registerIfMissing = false,
            string? email = null,
            string cookieName = DefaultCookieName,
            CancellationToken cancellationToken = default)
        {
            string session = await AuthenticateAsync(baseUrl, username, password, registerIfMissing, email, cookieName, cancellationToken)
                .ConfigureAwait(false);
            return await FetchUdpKeyWithSessionAsync(baseUrl, session, cookieName, cancellationToken)
                .ConfigureAwait(false);
        }

        /// Log in, optionally registering first, and return the session cookie
        /// VALUE (not the whole Set-Cookie header). Exposed separately so an
        /// application that runs its own login UI can reuse the session it
        /// already holds.
        ///
        /// Login is the ONLY request on the default path, and that is a
        /// deliberate cost decision as much as a semantic one. The server counts
        /// an IP failure on entry to both handlers and clears the bucket only on
        /// success (pkg/services/auth/handlers.go), with a default budget of ten
        /// failures per minute and a five-minute lockout. Attempting a doomed
        /// register before every login would double the counted entries per
        /// connect and halve the burst headroom — which a fleet of bots
        /// connecting from one address feels immediately.
        public static async Task<string> AuthenticateAsync(
            string baseUrl,
            string username,
            string password,
            bool registerIfMissing = false,
            string? email = null,
            string cookieName = DefaultCookieName,
            CancellationToken cancellationToken = default)
        {
            HttpClient http = Shared.Value;

            if (registerIfMissing)
            {
                var register = new Dictionary<string, string> { { "username", username }, { "password", password } };
                if (!string.IsNullOrEmpty(email)) register["email"] = email!;

                using HttpResponseMessage reg = await PostJsonAsync(http, baseUrl, "/auth/register", register, null, cookieName, cancellationToken).ConfigureAwait(false);
                if (reg.IsSuccessStatusCode)
                {
                    return RequireCookie(reg, cookieName, "/auth/register");
                }
                // 409 is the documented "username taken" — the expected result
                // on every run after the first, so it is a fallthrough, not an
                // error. Anything else is a real failure and must not be
                // masked by attempting a login that will also fail.
                if (reg.StatusCode != HttpStatusCode.Conflict)
                {
                    throw await ErrorAsync(reg, "register", cancellationToken).ConfigureAwait(false);
                }
            }

            var login = new Dictionary<string, string> { { "username", username }, { "password", password } };
            using (HttpResponseMessage res = await PostJsonAsync(http, baseUrl, "/auth/login", login, null, cookieName, cancellationToken).ConfigureAwait(false))
            {
                if (!res.IsSuccessStatusCode)
                {
                    throw await ErrorAsync(res, "login", cancellationToken).ConfigureAwait(false);
                }
                return RequireCookie(res, cookieName, "/auth/login");
            }
        }

        /// Draw a UDP session key using a session cookie value obtained
        /// elsewhere (AuthenticateAsync, or the application's own login).
        public static async Task<UdpKeyCredential> FetchUdpKeyWithSessionAsync(
            string baseUrl,
            string sessionCookieValue,
            string cookieName = DefaultCookieName,
            CancellationToken cancellationToken = default)
        {
            if (string.IsNullOrEmpty(sessionCookieValue))
            {
                throw new ArgumentException("session cookie value is required", nameof(sessionCookieValue));
            }

            HttpClient http = Shared.Value;
            using HttpResponseMessage res = await PostJsonAsync(
                http, baseUrl, "/auth/udp-key", null, sessionCookieValue, cookieName, cancellationToken).ConfigureAwait(false);

            if (!res.IsSuccessStatusCode)
            {
                throw await ErrorAsync(res, "udp-key", cancellationToken).ConfigureAwait(false);
            }

            string json = await res.Content.ReadAsStringAsync().ConfigureAwait(false);

            // NONE of the throws below may carry `json`. A 200 body from this
            // route contains the base64 master key, and ServerMessage is a
            // public property on a public exception — the usual fate of which
            // is a log line or a crash report. The status and a description of
            // what was wrong with the shape are enough to diagnose a malformed
            // response without exporting the secret alongside it.
            string? keyIdHex = Json.String(json, "keyId");
            string? keyB64 = Json.String(json, "key");
            if (keyIdHex == null || keyB64 == null)
            {
                throw new MmokitAuthException("udp-key: response missing keyId or key", (int)res.StatusCode);
            }

            // keyId is hex, not a JSON number, precisely because it is a uint64
            // and a JSON number loses precision above 2^53. keyId is not secret,
            // so echoing it is safe and useful.
            if (!ulong.TryParse(keyIdHex, NumberStyles.HexNumber, CultureInfo.InvariantCulture, out ulong keyId))
            {
                throw new MmokitAuthException($"udp-key: keyId is not hex: '{keyIdHex}'", (int)res.StatusCode);
            }

            byte[] key;
            try
            {
                key = Convert.FromBase64String(keyB64);
            }
            catch (FormatException)
            {
                // Not even the FormatException message: it quotes the input.
                throw new MmokitAuthException("udp-key: key is not valid base64", (int)res.StatusCode);
            }

            if (key.Length != UdpCrypto.KeySize)
            {
                throw new MmokitAuthException(
                    $"udp-key: key is {key.Length} bytes, expected {UdpCrypto.KeySize}", (int)res.StatusCode);
            }

            long expiresAtMs = Json.Long(json, "expiresAtMs") ?? 0;
            return new UdpKeyCredential(keyId, key, expiresAtMs);
        }

        // ----- HTTP plumbing -----

        /// One shared client, created on first use and never disposed — the
        /// documented .NET pattern. A client per call would leave a socket in
        /// TIME_WAIT per call, and the load-test case this SDK exists for
        /// starts hundreds of bots at once, two calls each.
        ///
        /// Sharing is safe here because the per-request state that would
        /// normally force separate clients — the cookie — is set per request
        /// rather than held on the handler. That is also why UseCookies is
        /// false: the default handler swallows Set-Cookie into an internal
        /// CookieContainer whose behaviour varies across Mono, IL2CPP and
        /// CoreCLR, and it honours the Secure attribute, which would silently
        /// drop the cookie on a plain-HTTP dev listener. Handling the one
        /// cookie explicitly is both shorter and identical on every runtime.
        static readonly Lazy<HttpClient> Shared = new Lazy<HttpClient>(() =>
            new HttpClient(new HttpClientHandler
            {
                UseCookies = false,
                // Unity's Mono backs HttpClientHandler with ServicePoint, whose
                // default is TWO concurrent connections per host. A bot fleet
                // starting together would serialise its logins behind that and
                // look like a server stall. CoreCLR already defaults to
                // unbounded, so this only matters where it matters.
                MaxConnectionsPerServer = 64,
            })
            {
                Timeout = TimeSpan.FromSeconds(15),
            });

        static async Task<HttpResponseMessage> PostJsonAsync(
            HttpClient http, string baseUrl, string path,
            Dictionary<string, string>? body, string? sessionCookieValue, string cookieName,
            CancellationToken cancellationToken)
        {
            var req = new HttpRequestMessage(HttpMethod.Post, JoinUrl(baseUrl, path));
            // /auth/udp-key takes no body; the cookie is the whole request.
            req.Content = new StringContent(body == null ? "{}" : Json.Object(body), Encoding.UTF8, "application/json");
            if (sessionCookieValue != null)
            {
                req.Headers.TryAddWithoutValidation("Cookie", cookieName + "=" + sessionCookieValue);
            }

            try
            {
                return await http.SendAsync(req, cancellationToken).ConfigureAwait(false);
            }
            catch (OperationCanceledException) when (!cancellationToken.IsCancellationRequested)
            {
                // An expired HttpClient.Timeout surfaces as TaskCanceledException,
                // NOT TimeoutException — a long-standing .NET wart. Left alone it
                // escapes every sensible catch clause a caller would write, so an
                // unreachable server reads as a crash rather than a timeout.
                // A cancellation the CALLER asked for is different and propagates
                // unchanged; that is what the filter distinguishes.
                throw new MmokitAuthException(
                    $"{path}: timed out after {http.Timeout.TotalSeconds:F0}s — is the server reachable at {baseUrl}?", 0);
            }
            catch (HttpRequestException ex)
            {
                // Connection refused, DNS failure, TLS failure. Same reasoning:
                // one exception type out of this class.
                throw new MmokitAuthException($"{path}: {ex.Message}", 0);
            }
        }

        static string JoinUrl(string baseUrl, string path)
        {
            if (string.IsNullOrEmpty(baseUrl)) throw new ArgumentException("baseUrl is required", nameof(baseUrl));
            return baseUrl.TrimEnd('/') + path;
        }

        /// Pull `cookieName` out of the response's Set-Cookie headers and
        /// return its value. Attributes (Path, HttpOnly, Max-Age…) are
        /// irrelevant to a non-browser client and are discarded.
        static string RequireCookie(HttpResponseMessage res, string cookieName, string what)
        {
            if (res.Headers.TryGetValues("Set-Cookie", out IEnumerable<string>? setCookies))
            {
                string prefix = cookieName + "=";
                foreach (string sc in setCookies)
                {
                    if (!sc.StartsWith(prefix, StringComparison.Ordinal)) continue;
                    int end = sc.IndexOf(';');
                    string value = end < 0 ? sc.Substring(prefix.Length) : sc.Substring(prefix.Length, end - prefix.Length);
                    if (value.Length > 0) return value;
                }
            }
            throw new MmokitAuthException(
                $"{what}: succeeded but set no '{cookieName}' cookie — is the server's HTTPOpts.CookieName different?",
                (int)res.StatusCode);
        }

        static async Task<MmokitAuthException> ErrorAsync(HttpResponseMessage res, string what, CancellationToken cancellationToken)
        {
            string body = "";
            try { body = await res.Content.ReadAsStringAsync().ConfigureAwait(false); }
            catch (Exception) { /* an unreadable body must not mask the status */ }

            string? msg = Json.String(body, "message");
            int status = (int)res.StatusCode;

            // The two statuses worth explaining, because both look like a
            // client bug and are actually a server-configuration answer.
            string hint = "";
            if (what == "udp-key" && status == 403)
            {
                hint = " — the server refuses to issue a UDP key over a plaintext listener; use https:// or start the server with --dev-insecure-cookie";
            }
            else if (what == "udp-key" && status == 404)
            {
                hint = " — the server has no AuthResolver, so it cannot issue UDP keys (register an auth service)";
            }

            return new MmokitAuthException(
                $"{what}: HTTP {status}{(msg == null ? "" : " " + msg)}{hint}", status, msg ?? (body.Length > 0 ? body : null));
        }
    }

    /// The smallest JSON reader that serves these three responses, and no more.
    ///
    /// netstandard2.1 has no System.Text.Json, and the SDK carries no package
    /// references so that it can be dropped into a Unity project as plain
    /// source. The bodies read here are flat objects emitted by Go's
    /// encoding/json, so a scanner for top-level string and integer fields is
    /// sufficient.
    ///
    /// It is deliberately NOT a general JSON parser. It reads fields of the
    /// ROOT object only; nested values are skipped rather than descended into,
    /// and a nested field of the same name reads as absent rather than being
    /// returned by mistake. Numbers are integers only — every numeric field
    /// here is a Go int64. Malformed input yields null, never an exception:
    /// this code runs on error paths, where throwing would replace the real
    /// failure with a parse failure.
    static class Json
    {
        /// Serialize a flat string map. Only used for request bodies we build
        /// ourselves, so the value set is known and small.
        internal static string Object(Dictionary<string, string> fields)
        {
            var sb = new StringBuilder("{");
            bool first = true;
            foreach (KeyValuePair<string, string> kv in fields)
            {
                if (!first) sb.Append(',');
                first = false;
                sb.Append('"').Append(Escape(kv.Key)).Append("\":\"").Append(Escape(kv.Value)).Append('"');
            }
            return sb.Append('}').ToString();
        }

        static string Escape(string s)
        {
            var sb = new StringBuilder(s.Length);
            foreach (char c in s)
            {
                switch (c)
                {
                    case '"': sb.Append("\\\""); break;
                    case '\\': sb.Append("\\\\"); break;
                    case '\n': sb.Append("\\n"); break;
                    case '\r': sb.Append("\\r"); break;
                    case '\t': sb.Append("\\t"); break;
                    default:
                        if (c < 0x20) sb.Append("\\u").Append(((int)c).ToString("x4", CultureInfo.InvariantCulture));
                        else sb.Append(c);
                        break;
                }
            }
            return sb.ToString();
        }

        /// Value of a top-level string field, or null when absent.
        internal static string? String(string json, string field)
        {
            int i = ValueStart(json, field);
            if (i < 0 || i >= json.Length || json[i] != '"') return null;
            i++;
            var sb = new StringBuilder();
            while (i < json.Length)
            {
                char c = json[i++];
                if (c == '"') return sb.ToString();
                if (c != '\\') { sb.Append(c); continue; }
                if (i >= json.Length) return null;
                char esc = json[i++];
                switch (esc)
                {
                    case 'n': sb.Append('\n'); break;
                    case 'r': sb.Append('\r'); break;
                    case 't': sb.Append('\t'); break;
                    case 'b': sb.Append('\b'); break;
                    case 'f': sb.Append('\f'); break;
                    case 'u':
                        // TryParse, not Parse: a malformed escape must make this
                        // reader return null so the caller reports the real HTTP
                        // failure, not throw a FormatException out of the middle
                        // of an error path and bury it.
                        if (i + 4 > json.Length) return null;
                        if (!ushort.TryParse(json.Substring(i, 4), NumberStyles.HexNumber, CultureInfo.InvariantCulture, out ushort cp))
                        {
                            return null;
                        }
                        sb.Append((char)cp);
                        i += 4;
                        break;
                    default: sb.Append(esc); break; // covers '"' and '\\'
                }
            }
            return null;
        }

        /// Value of a top-level integer field, or null when absent or not an
        /// integer. Fractional and exponent forms are not accepted: every
        /// field read here is a Go int64.
        internal static long? Long(string json, string field)
        {
            int i = ValueStart(json, field);
            if (i < 0) return null;
            int start = i;
            if (i < json.Length && (json[i] == '-' || json[i] == '+')) i++;
            int digits = i;
            while (i < json.Length && json[i] >= '0' && json[i] <= '9') i++;
            if (i == digits) return null;
            return long.TryParse(json.Substring(start, i - start), NumberStyles.Integer, CultureInfo.InvariantCulture, out long v)
                ? v
                : (long?)null;
        }

        /// Index of the first character of top-level `field`'s value, or -1.
        ///
        /// Depth-tracked on purpose. A plain substring search would match the
        /// same key nested inside another object and return ITS value — a wrong
        /// answer rather than no answer, which is the worse of the two failure
        /// modes. An auth error body is `{"code":…,"message":…}` today but
        /// nothing stops a future one nesting a "message", and silently reading
        /// the inner one would be invisible.
        ///
        /// Strings are skipped as units so braces and quoted key-lookalikes
        /// inside them cannot move the depth counter.
        static int ValueStart(string json, string field)
        {
            if (json == null) return -1;
            int depth = 0;
            for (int i = 0; i < json.Length; i++)
            {
                char c = json[i];
                if (c == '{' || c == '[') { depth++; continue; }
                if (c == '}' || c == ']') { depth--; continue; }
                if (c != '"') continue;

                int keyStart = i + 1;
                int end = EndOfString(json, i);
                if (end < 0) return -1;
                i = end;

                // Only a key at the top level of the root object counts.
                if (depth != 1) continue;
                int j = end + 1;
                while (j < json.Length && char.IsWhiteSpace(json[j])) j++;
                if (j >= json.Length || json[j] != ':') continue;

                if (string.CompareOrdinal(json, keyStart, field, 0, field.Length) == 0 &&
                    end - keyStart == field.Length)
                {
                    j++;
                    while (j < json.Length && char.IsWhiteSpace(json[j])) j++;
                    return j;
                }

                // Not our key: step over its value so a nested object's keys are
                // seen at the right depth rather than at this one.
                i = j;
            }
            return -1;
        }

        /// Index of the closing quote of the string starting at the opening
        /// quote `start`, honouring backslash escapes. -1 if unterminated.
        static int EndOfString(string json, int start)
        {
            for (int i = start + 1; i < json.Length; i++)
            {
                if (json[i] == '\\') { i++; continue; }
                if (json[i] == '"') return i;
            }
            return -1;
        }
    }
}
