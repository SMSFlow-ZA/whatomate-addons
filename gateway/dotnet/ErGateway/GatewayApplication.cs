using System.Net;

namespace ErGateway;

public static class GatewayApplication
{
    public static WebApplication Build(WebApplicationBuilder builder, GatewayOptions options, HttpMessageHandler? upstreamTransport = null)
    {
        options.Validate();
        // No HTTP body/header tracing, developer exceptions or request query logs.
        builder.Logging.AddFilter("Microsoft.AspNetCore", LogLevel.Warning);
        builder.WebHost.ConfigureKestrel(server =>
        {
            server.Limits.MaxRequestBodySize = 131072;
            server.Limits.MaxConcurrentConnections = 32;
            server.Limits.RequestHeadersTimeout = TimeSpan.FromSeconds(10);
        });
        builder.Services.AddSingleton(options);
        var transport = upstreamTransport ?? new SocketsHttpHandler
        {
            AllowAutoRedirect = false,
            UseProxy = false,
            AutomaticDecompression = DecompressionMethods.None,
            MaxConnectionsPerServer = options.MaxConcurrentRequests,
            ConnectTimeout = TimeSpan.FromSeconds(10),
            PooledConnectionLifetime = TimeSpan.FromMinutes(5)
        };
        builder.Services.AddSingleton(provider => new GatewayService(options, transport, provider.GetRequiredService<ILogger<GatewayService>>()));
        var app = builder.Build();
        app.Use(async (context, next) =>
        {
            context.Response.Headers.CacheControl = "no-store";
            if (context.Request.Path == "/healthz" && context.Request.Method == "GET") { await next(context); return; }
            if (context.Request.Headers.Authorization.Count != 1 || !options.Authenticate(context.Request.Headers.Authorization.ToString()))
            {
                context.Response.StatusCode = 401; return;
            }
            // Instance identity is bound by this service configuration. Request
            // bodies or headers cannot select another tenant or add grants.
            await next(context);
        });
        app.MapGet("/healthz", () => Results.Json(new { status = "ok" }));
        app.MapPost("/v1/chat/completions", (HttpContext context, GatewayService service) => service.Handle(context, false));
        app.MapPost("/v1/embeddings", (HttpContext context, GatewayService service) => service.Handle(context, true));
        return app;
    }

    public static void ValidateTls(IConfiguration config)
    {
        var urls = config["urls"] ?? config["ASPNETCORE_URLS"] ?? "https://+:8443";
        if (urls.Split(';', StringSplitOptions.RemoveEmptyEntries).Any(url => !url.StartsWith("https://", StringComparison.OrdinalIgnoreCase))) throw new InvalidOperationException("Gateway listener must use HTTPS");
        foreach (var endpoint in config.GetSection("Kestrel:Endpoints").GetChildren())
            if (!endpoint["Url"]?.StartsWith("https://", StringComparison.OrdinalIgnoreCase) ?? true) throw new InvalidOperationException("Gateway endpoints must use HTTPS");
        var certificate = config["Kestrel:Certificates:Default:Path"];
        var key = config["Kestrel:Certificates:Default:KeyPath"];
        if (certificate is null || key is null || !Path.IsPathFullyQualified(certificate) || !Path.IsPathFullyQualified(key) || !File.Exists(certificate) || !File.Exists(key)) throw new InvalidOperationException("Mounted TLS certificate and private key are required");
    }
}
