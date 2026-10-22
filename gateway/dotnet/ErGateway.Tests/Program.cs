using System.Net;
using System.Security.Cryptography;
using System.Security.Cryptography.X509Certificates;
using Microsoft.Extensions.Configuration;
using System.Net.Http.Headers;
using System.Text;
using System.Text.Json;
using ErGateway;
using Microsoft.AspNetCore.Builder;
using Microsoft.AspNetCore.Hosting;
using Microsoft.AspNetCore.Hosting.Server;
using Microsoft.AspNetCore.Hosting.Server.Features;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Logging;

static void Check(bool condition, string message) { if (!condition) throw new Exception(message); }
static GatewayOptions Options() => new()
{
    InstanceId = "synthetic-instance",
    ServiceToken = new string('s', 40),
    AzureApiKey = new string('a', 40),
    AzureEndpoint = "https://synthetic.openai.azure.com/openai/v1/",
    NanoDeployment = "nano-deploy",
    NanoModel = "gpt-5-nano-2025-08-07",
    MiniDeployment = "mini-deploy",
    MiniModel = "gpt-5-mini-2025-08-07",
    EmbeddingDeployment = "embed-deploy",
    EmbeddingModel = "text-embedding-3-small",
    ChatAlias = "er-chat-v1",
    EmbeddingAlias = "er-embed-v1",
    DeploymentAttestation = "reviewed-evidence"
};
static object Chat(string question = "What is the policy?", string page = "Approved policy", string model = "er-chat-v1") => new { model, max_tokens = 1024, temperature = 0, response_format = new { type = "json_object" }, messages = new[] { new { role = "system", content = "Go trusted policy wrapper" }, new { role = "user", content = JsonSerializer.Serialize(new { Question = question, Sources = new[] { new { Reference = "private-doc-ref", DocumentID = "not-for-upstream", Version = "1", ContentHash = "hash", ManifestHash = "approved", Page = new { Number = 1, Text = page, Method = "text", ExtractorRevision = "v1" } } } }) } } };
var defaults = Options(); defaults.Validate();
foreach (var mutate in new Action<GatewayOptions>[] {
 x=>x.AzureEndpoint="http://synthetic.openai.azure.com/openai/v1/",x=>x.AzureEndpoint="https://attacker.invalid/openai/v1/",x=>x.ServiceToken=x.AzureApiKey,x=>x.ProcessingScope="global_standard",x=>x.ProcessingScope="unknown",x=>x.MaxConcurrentRequests=0,x=>x.NanoModel="gpt-5-nano",x=>x.DeploymentAttestation="",x=>x.AzureEndpoint="https://synthetic.openai.azure.com/openai/v1/?key=bad",x=>x.AzureEndpoint="https://user@synthetic.openai.azure.com/openai/v1/"
}) { var o = Options(); mutate(o); bool rejected = false; try { o.Validate(); } catch (InvalidOperationException) { rejected = true; } Check(rejected, "unsafe config accepted"); }
var global = Options(); global.ProcessingScope = "global_standard"; global.InternationalProcessingApproval = "reviewed approval"; global.InternationalProcessingDisclosure = "International model processing; ZA stored data."; global.Validate();

var secretDirectory = Path.Combine(Path.GetTempPath(), "er-gateway-synthetic-" + Guid.NewGuid().ToString("N")); Directory.CreateDirectory(secretDirectory);
var serviceFile = Path.Combine(secretDirectory, "service-token"); var azureFile = Path.Combine(secretDirectory, "azure-key");
try
{
    File.WriteAllText(serviceFile, new string('s', 40) + "\n"); File.WriteAllText(azureFile, new string('a', 40) + "\n");
    var files = Options(); files.ServiceToken = ""; files.AzureApiKey = ""; files.ServiceTokenFile = serviceFile; files.AzureApiKeyFile = azureFile; files.LoadSecrets(); files.Validate();
    var exclusive = Options(); exclusive.ServiceTokenFile = serviceFile; bool rejected = false; try { exclusive.LoadSecrets(); } catch (InvalidOperationException) { rejected = true; }
    Check(rejected, "inline and file service secrets ambiguously accepted");
}
finally { File.Delete(serviceFile); File.Delete(azureFile); Directory.Delete(secretDirectory); }
var fake = new FakeAzure();
var builder = WebApplication.CreateBuilder(); builder.Logging.ClearProviders(); builder.WebHost.UseUrls("http://127.0.0.1:0");
await using var app = GatewayApplication.Build(builder, Options(), fake);
await app.StartAsync();
var address = app.Services.GetRequiredService<IServer>().Features.Get<IServerAddressesFeature>()!.Addresses.Single();
using var client = new HttpClient { BaseAddress = new Uri(address) };
async Task<HttpResponseMessage> Post(string path, object body, string? token = null) { var request = new HttpRequestMessage(HttpMethod.Post, path) { Content = new StringContent(JsonSerializer.Serialize(body), Encoding.UTF8, "application/json") }; if (token != null) request.Headers.Authorization = new AuthenticationHeaderValue("Bearer", token); return await client.SendAsync(request); }
var token = Options().ServiceToken;
Check((await Post("/v1/chat/completions", Chat())).StatusCode == HttpStatusCode.Unauthorized, "anonymous accepted");
Check((await Post("/v1/chat/completions", Chat(), new string('x', 40))).StatusCode == HttpStatusCode.Unauthorized, "cross-instance bearer accepted");
Check(fake.Calls == 0, "unauthorized call reached upstream");
var ok = await Post("/v1/chat/completions", Chat(), token); Check(ok.StatusCode == HttpStatusCode.OK, await ok.Content.ReadAsStringAsync());
using (var payload = JsonDocument.Parse(await ok.Content.ReadAsStringAsync())) { Check(payload.RootElement.GetProperty("model").GetString() == "er-chat-v1", "logical compatibility model missing"); Check(payload.RootElement.GetProperty("gateway_metadata").GetProperty("upstream_model").GetString() == Options().NanoModel, "actual model provenance lost"); Check(payload.RootElement.GetProperty("choices")[0].GetProperty("message").GetProperty("content").GetString()!.Contains("private-doc-ref"), "citation mapping lost"); }
Check(fake.LastBody.Contains("nano-deploy") && !fake.LastBody.Contains("not-for-upstream") && !fake.LastBody.Contains("private-doc-ref"), "private source identity leaked");
Check(fake.LastBody.Contains("max_completion_tokens") && !fake.LastBody.Contains("max_tokens") && !fake.LastBody.Contains("temperature"), "Azure reasoning parameters incompatible");
await Post("/v1/chat/completions", Chat("Compare the policy exceptions"), token); Check(fake.LastBody.Contains("mini-deploy"), "nuance not routed to mini");
int before = fake.Calls;
var blocked = await Post("/v1/chat/completions", Chat("Should I dismiss this employee for harassment?"), token); Check(blocked.IsSuccessStatusCode && fake.Calls == before, "consequential risk reached model");
var narrative = await Post("/v1/chat/completions", Chat("My manager threatened me yesterday"), token); Check(narrative.IsSuccessStatusCode && fake.Calls == before, "personal narrative reached model");
await Post("/v1/chat/completions", Chat("What is policy for person@example.invalid +27 82 123 4567?"), token); Check(!fake.LastBody.Contains("person@example.invalid") && !fake.LastBody.Contains("123 4567"), "common PII not minimized");
var spoof = JsonSerializer.SerializeToElement(new { model = "er-chat-v1", actor = "admin", messages = new[] { new { role = "user", content = "anything" } } }); Check((await Post("/v1/chat/completions", spoof, token)).StatusCode == HttpStatusCode.BadRequest, "body actor accepted");
var classification = new { model = "er-chat-v1", messages = new[] { new { role = "system", content = "classification" }, new { role = "user", content = "{\"text\":\"case narrative\",\"allowed_labels\":[\"Concern\"]}" } }, max_tokens = 384 };
Check((await Post("/v1/chat/completions", classification, token)).StatusCode == HttpStatusCode.UnprocessableEntity, "free narrative classification reached model");
var embed = await Post("/v1/embeddings", new { model = "er-embed-v1", input = new[] { "approved page" }, dimensions = 3, encoding_format = "float" }, token); Check(embed.IsSuccessStatusCode, "embedding failed");
fake.Mode = "model-mismatch"; Check((await Post("/v1/chat/completions", Chat(), token)).StatusCode == HttpStatusCode.BadGateway, "unpinned upstream model normalized");
fake.Mode = "numeric-model"; Check((await Post("/v1/chat/completions", Chat(), token)).StatusCode == HttpStatusCode.BadGateway, "malformed upstream model accepted");
fake.Mode = "incomplete"; var partial = await Post("/v1/chat/completions", Chat(), token); Check(partial.IsSuccessStatusCode && (await partial.Content.ReadAsStringAsync()).Contains("unsupported_output"), "incomplete reasoning output released");
fake.Mode = "429"; Check((await Post("/v1/chat/completions", Chat(), token)).StatusCode == HttpStatusCode.TooManyRequests, "upstream 429 lost");
fake.Mode = "error"; var error = await Post("/v1/chat/completions", Chat(), token); Check(error.StatusCode == HttpStatusCode.BadGateway && !(await error.Content.ReadAsStringAsync()).Contains("upstream-secret"), "upstream error leaked");
var topicRequest = new { model = "er-chat-v1", messages = new[] { new { role = "system", content = "classification" }, new { role = "user", content = "{\"text\":\"Alice person@example.invalid is late; employee 9999\",\"allowed_labels\":[\"Attendance concern\"]}" } }, max_tokens = 384 };
fake.Mode = "ok";
var topicResponse = await Post("/v1/chat/completions", topicRequest, token);
Check(topicResponse.IsSuccessStatusCode, "derived-topic classification failed");
Check(fake.LastBody.Contains("attendance") && !fake.LastBody.Contains("Alice") && !fake.LastBody.Contains("9999") && !fake.LastBody.Contains("example.invalid"), "classification raw narrative or identity leaked");
Check((await topicResponse.Content.ReadAsStringAsync()).Contains("derived_topic_only"), "derived-topic limitation missing");
var oversized = await Post("/v1/chat/completions", Chat(new string('x', 132000)), token); Check(oversized.StatusCode == HttpStatusCode.RequestEntityTooLarge, "request body bound missing");
await app.StopAsync();

async Task WithApp(GatewayOptions options, FakeAzure upstream, Func<HttpClient, Task> action)
{
    var b = WebApplication.CreateBuilder(); b.Logging.ClearProviders(); b.WebHost.UseUrls("http://127.0.0.1:0");
    await using var application = GatewayApplication.Build(b, options, upstream); await application.StartAsync();
    var url = application.Services.GetRequiredService<IServer>().Features.Get<IServerAddressesFeature>()!.Addresses.Single();
    using var c = new HttpClient { BaseAddress = new Uri(url) }; c.DefaultRequestHeaders.Authorization = new("Bearer", options.ServiceToken);
    try { await action(c); } finally { await application.StopAsync(); }
}
async Task<HttpResponseMessage> Send(HttpClient c) => await c.PostAsync("/v1/chat/completions", new StringContent(JsonSerializer.Serialize(Chat()), Encoding.UTF8, "application/json"));
await WithApp(global, new(), async c =>
{
    var response = await Send(c); var text = await response.Content.ReadAsStringAsync(); Check(response.IsSuccessStatusCode && text.Contains("global_standard") && text.Contains(global.InternationalProcessingDisclosure), "international processing disclosure missing from chat");
    var embeddings = await c.PostAsync("/v1/embeddings", new StringContent("{\"model\":\"er-embed-v1\",\"input\":[\"policy\"],\"dimensions\":3,\"encoding_format\":\"float\"}", Encoding.UTF8, "application/json")); Check(embeddings.IsSuccessStatusCode && (await embeddings.Content.ReadAsStringAsync()).Contains("global_standard"), "embedding scope approval/metadata missing");
});
var rate = Options(); rate.MaxRequestsPerMinute = 1;
await WithApp(rate, new(), async c => { Check((await Send(c)).IsSuccessStatusCode, "first bounded call failed"); Check((await Send(c)).StatusCode == HttpStatusCode.TooManyRequests, "request budget bypass"); });
var tokenBudget = Options(); tokenBudget.MaxTokenBudgetPerMinute = 1024;
await WithApp(tokenBudget, new(), async c => { Check((await Send(c)).StatusCode == HttpStatusCode.TooManyRequests, "token budget bypass"); });
var concurrency = Options(); concurrency.MaxConcurrentRequests = 1; var blocking = new FakeAzure { Mode = "blocking" };
await WithApp(concurrency, blocking, async c =>
{
    var first = Send(c); await blocking.Entered.Task.WaitAsync(TimeSpan.FromSeconds(5));
    try { Check((await Send(c)).StatusCode == HttpStatusCode.TooManyRequests, "concurrency budget bypass"); } finally { blocking.Release.TrySetResult(); }
    Check((await first).IsSuccessStatusCode, "admitted concurrent request failed");
});
var timeout = Options(); timeout.TimeoutSeconds = 1;
await WithApp(timeout, new() { Mode = "timeout" }, async c => { Check((await Send(c)).StatusCode == HttpStatusCode.GatewayTimeout, "upstream deadline not enforced"); });
var badConfig = new ConfigurationBuilder().AddInMemoryCollection(new Dictionary<string, string?> { { "urls", "http://127.0.0.1:8080" } }).Build();
bool tlsRejected = false; try { GatewayApplication.ValidateTls(badConfig); } catch (InvalidOperationException) { tlsRejected = true; }
Check(tlsRejected, "plaintext production listener accepted");
// Real TLS listener with an ephemeral fixture certificate and scoped trust.
using var rsa = RSA.Create(2048);
var certificateRequest = new CertificateRequest("CN=localhost", rsa, HashAlgorithmName.SHA256, RSASignaturePadding.Pkcs1);
var names = new SubjectAlternativeNameBuilder(); names.AddIpAddress(IPAddress.Loopback); certificateRequest.CertificateExtensions.Add(names.Build());
using var generatedCertificate = certificateRequest.CreateSelfSigned(DateTimeOffset.UtcNow.AddMinutes(-1), DateTimeOffset.UtcNow.AddHours(1));
using var certificate = X509CertificateLoader.LoadPkcs12(generatedCertificate.Export(X509ContentType.Pfx), null, X509KeyStorageFlags.Exportable);
var tlsBuilder = WebApplication.CreateBuilder(); tlsBuilder.Logging.ClearProviders(); tlsBuilder.WebHost.ConfigureKestrel(k => k.Listen(IPAddress.Loopback, 0, listen => listen.UseHttps(certificate)));
await using (var tlsApp = GatewayApplication.Build(tlsBuilder, Options(), new FakeAzure()))
{
    await tlsApp.StartAsync(); var tlsUrl = tlsApp.Services.GetRequiredService<IServer>().Features.Get<IServerAddressesFeature>()!.Addresses.Single();
    using var handler = new HttpClientHandler(); handler.ServerCertificateCustomValidationCallback = (_, presented, _, _) => presented?.Thumbprint == certificate.Thumbprint;
    using var tlsClient = new HttpClient(handler) { BaseAddress = new Uri(tlsUrl) }; tlsClient.DefaultRequestHeaders.Authorization = new("Bearer", token);
    Check((await Send(tlsClient)).IsSuccessStatusCode, "authenticated TLS provider journey failed"); await tlsApp.StopAsync();
}
Console.WriteLine("PASS: configuration, actual HTTP auth/instance binding, routing, PII minimization, human handoff, Go provider envelopes, model pinning, 429 and safe errors");

sealed class FakeAzure : HttpMessageHandler
{
    public int Calls; public string LastBody = ""; public string Mode = "ok";
    public TaskCompletionSource Entered { get; } = new(TaskCreationOptions.RunContinuationsAsynchronously); public TaskCompletionSource Release { get; } = new(TaskCreationOptions.RunContinuationsAsynchronously);
    protected override async Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken cancellationToken)
    {
        Calls++; LastBody = await request.Content!.ReadAsStringAsync(cancellationToken);
        CheckAuth(request);
        if (Mode == "timeout") await Task.Delay(5000, cancellationToken);
        if (Mode == "blocking") { Entered.TrySetResult(); await Release.Task.WaitAsync(cancellationToken); }
        if (Mode == "numeric-model") return new(HttpStatusCode.OK) { Content = new StringContent("{\"model\":7}") };
        if (Mode == "429") return new(HttpStatusCode.TooManyRequests);
        if (Mode == "error") return new(HttpStatusCode.InternalServerError) { Content = new StringContent("upstream-secret") };
        using var body = JsonDocument.Parse(LastBody); var deployment = body.RootElement.GetProperty("model").GetString();
        if (deployment != "embed-deploy" && body.RootElement.GetProperty("reasoning_effort").GetString() != (deployment == "nano-deploy" ? "minimal" : "low")) throw new Exception("reasoning budget route mismatch");
        var topic = LastBody.Contains("topic_tags", StringComparison.Ordinal);
        object response = deployment == "embed-deploy" ? new { model = "text-embedding-3-small", data = new[] { new { index = 0, embedding = new[] { 1.0, 0.0, 0.0 } } }, usage = new { prompt_tokens = 10, total_tokens = 10 } } : new { model = Mode == "model-mismatch" ? "unpinned-model" : deployment == "nano-deploy" ? "gpt-5-nano-2025-08-07" : "gpt-5-mini-2025-08-07", choices = new[] { new { finish_reason = Mode == "incomplete" ? "length" : "stop", message = new { role = "assistant", content = topic ? "{\"label\":\"Attendance concern\",\"rationale\":\"Attendance topic\"}" : "{\"answer\":\"Approved policy\",\"citations\":[\"s0\"],\"grounded\":true,\"inference\":false}" } } }, usage = new { prompt_tokens = 10, completion_tokens = 20, total_tokens = 30 } };
        return new(HttpStatusCode.OK) { Content = new StringContent(JsonSerializer.Serialize(response), Encoding.UTF8, "application/json") };
    }
    static void CheckAuth(HttpRequestMessage request) { if (request.Headers.GetValues("api-key").Single() != new string('a', 40) || !request.RequestUri!.AbsoluteUri.StartsWith("https://synthetic.openai.azure.com/openai/v1/")) throw new Exception("upstream key/endpoint contract violated"); }
}
