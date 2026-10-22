using ErGateway;

var builder = WebApplication.CreateBuilder(args);
builder.WebHost.UseUrls(builder.Configuration["urls"] ?? "https://0.0.0.0:8443");
var options = new GatewayOptions();
builder.Configuration.GetSection("Gateway").Bind(options);
options.LoadSecrets();
GatewayApplication.ValidateTls(builder.Configuration);
var app = GatewayApplication.Build(builder, options);
await app.RunAsync();
