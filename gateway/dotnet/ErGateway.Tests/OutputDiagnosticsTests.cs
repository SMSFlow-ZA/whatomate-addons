using System.Net;
using System.Text;
using System.Text.Json;
using System.Text.Json.Nodes;
using ErGateway;
using Microsoft.AspNetCore.Builder;
using Microsoft.AspNetCore.Hosting;
using Microsoft.AspNetCore.Hosting.Server;
using Microsoft.AspNetCore.Hosting.Server.Features;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Logging;

static class OutputDiagnosticsTests
{
    public static async Task Run()
    {
        const string marker = "PRIVATE_PROVIDER_TEXT_do_not_log";
        var options = new GatewayOptions
        {
            InstanceId = "synthetic-output-diagnostics", ServiceToken = new string('s', 40), AzureApiKey = new string('a', 40),
            AzureEndpoint = "https://synthetic.openai.azure.com/openai/v1/", NanoDeployment = "nano-deploy", NanoModel = "gpt-5-nano-2025-08-07",
            MiniDeployment = "mini-deploy", MiniModel = "gpt-5-mini-2025-08-07", EmbeddingDeployment = "embed-deploy", EmbeddingModel = "text-embedding-3-small",
            ChatAlias = "er-chat-v1", EmbeddingAlias = "er-embed-v1", DeploymentAttestation = "synthetic-evidence"
        };
        var transport = new OutputTransport(); var logs = new OutputLogs();
        var builder = WebApplication.CreateBuilder(); builder.Logging.ClearProviders(); builder.Logging.AddProvider(logs); builder.WebHost.UseUrls("http://127.0.0.1:0");
        await using var app = GatewayApplication.Build(builder, options, transport); await app.StartAsync();
        using var client = new HttpClient { BaseAddress = new Uri(app.Services.GetRequiredService<IServer>().Features.Get<IServerAddressesFeature>()!.Addresses.Single()) };
        client.DefaultRequestHeaders.Authorization = new("Bearer", options.ServiceToken);
        object Request() => new { model = options.ChatAlias, max_tokens = 1024, messages = new[] {
            new { role = "system", content = "Synthetic wrapper" }, new { role = "user", content = JsonSerializer.Serialize(new { Question = "What is the policy?", Sources = new[] {
                new { Reference = "private-reference", DocumentID = "private-document", Version = "1", ContentHash = "hash", ManifestHash = "approved", Page = new { Number = 1, Text = "Approved policy", Method = "text", ExtractorRevision = "v1" } }
            } }) }
        } };
        static JsonObject Completion() => JsonNode.Parse("{\"answer\":\"Approved policy\",\"citations\":[\"s0\"],\"grounded\":true,\"inference\":false}")!.AsObject();
        static JsonObject Envelope() => new() { ["model"] = "gpt-5-nano-2025-08-07", ["usage"] = new JsonObject { ["total_tokens"] = 30 }, ["choices"] = new JsonArray(new JsonObject { ["finish_reason"] = "stop", ["message"] = new JsonObject { ["content"] = Completion().ToJsonString() } }) };
        static JsonObject Message(JsonObject body) => body["choices"]![0]!["message"]!.AsObject();
        var fixtures = new (string Stage, Action<JsonObject> Mutate)[] {
            ("ChoicesShape", b => b["choices"] = new JsonArray()),
            ("FinishReasonNotStop", b => b["choices"]![0]!["finish_reason"] = marker),
            ("MessageShape", b => b["choices"]![0]!["message"] = marker),
            ("ToolCalls", b => Message(b)["tool_calls"] = new JsonArray(marker)),
            ("FunctionCall", b => Message(b)["function_call"] = marker),
            ("Refusal", b => Message(b)["refusal"] = marker),
            ("EmptyContent", b => Message(b)["content"] = " "),
            ("NotGrounded", b => { var c = Completion(); c["grounded"] = false; Message(b)["content"] = c.ToJsonString(); }),
            ("Inference", b => { var c = Completion(); c["inference"] = true; Message(b)["content"] = c.ToJsonString(); }),
            ("AnswerTooLong", b => { var c = Completion(); c["answer"] = new string('x', 901); Message(b)["content"] = c.ToJsonString(); }),
            ("CitationsShape", b => { var c = Completion(); c["citations"] = new JsonArray(); Message(b)["content"] = c.ToJsonString(); }),
            ("UnknownCitation", b => { var c = Completion(); c["citations"] = new JsonArray(marker); Message(b)["content"] = c.ToJsonString(); }),
            ("NonVerbatim", b => { var c = Completion(); c["answer"] = marker; Message(b)["content"] = c.ToJsonString(); })
        };
        try
        {
            foreach (var fixture in fixtures)
            {
                logs.Events.Clear(); transport.Body = Envelope(); fixture.Mutate(transport.Body); var before = transport.Calls;
                using var response = await client.PostAsync("/v1/chat/completions", new StringContent(JsonSerializer.Serialize(Request()), Encoding.UTF8, "application/json"));
                var text = await response.Content.ReadAsStringAsync();
                Check(response.StatusCode == HttpStatusCode.OK && text.Contains("unsupported_output", StringComparison.Ordinal) && !text.Contains(marker, StringComparison.Ordinal), "handoff contract changed: " + fixture.Stage);
                Check(transport.Calls == before + 1, "diagnostic retried provider");
                Check(logs.Events.SequenceEqual(new[] { "AI output rejected Stage=" + fixture.Stage }), "missing or unsafe rejection stage: " + fixture.Stage);
                Check(!logs.Events.Any(x => x.Contains(marker, StringComparison.Ordinal) || x.Contains("private-", StringComparison.Ordinal) || x.Contains(options.InstanceId, StringComparison.Ordinal)), "private output escaped diagnostics");
            }
            logs.Events.Clear(); transport.Body = Envelope();
            using var success = await client.PostAsync("/v1/chat/completions", new StringContent(JsonSerializer.Serialize(Request()), Encoding.UTF8, "application/json"));
            Check(success.IsSuccessStatusCode && (await success.Content.ReadAsStringAsync()).Contains("Approved policy", StringComparison.Ordinal) && logs.Events.Count == 0, "valid output produced rejection noise");
            Console.WriteLine("PASS: 13 output rejection stages, unchanged handoffs, malicious output sanitization, no retries and quiet success");
        }
        finally { await app.StopAsync(); }
    }
    static void Check(bool condition, string message) { if (!condition) throw new Exception(message); }
    sealed class OutputTransport : HttpMessageHandler
    {
        public JsonObject Body = new(); public int Calls;
        protected override Task<HttpResponseMessage> SendAsync(HttpRequestMessage request, CancellationToken cancellationToken)
        {
            Calls++; return Task.FromResult(new HttpResponseMessage(HttpStatusCode.OK) { Content = new StringContent(Body.ToJsonString(), Encoding.UTF8, "application/json") });
        }
    }
    sealed class OutputLogs : ILoggerProvider, ILogger
    {
        public List<string> Events { get; } = [];
        public ILogger CreateLogger(string categoryName) => this;
        public void Dispose() { }
        public IDisposable? BeginScope<TState>(TState state) where TState : notnull => null;
        public bool IsEnabled(LogLevel logLevel) => true;
        public void Log<TState>(LogLevel logLevel, EventId eventId, TState state, Exception? exception, Func<TState, Exception?, string> formatter)
        {
            var message = formatter(state, exception);
            if (message.StartsWith("AI output rejected", StringComparison.Ordinal))
            {
                Check(logLevel == LogLevel.Warning && exception is null, "rejection log includes exception or wrong severity"); Events.Add(message);
            }
        }
    }
}
