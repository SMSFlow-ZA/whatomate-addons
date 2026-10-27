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

static class ExcerptSelectionTests
{
    public static async Task Run()
    {
        var options = new GatewayOptions { InstanceId="synthetic-selection", ServiceToken=new string('s',40), AzureApiKey=new string('a',40), AzureEndpoint="https://synthetic.openai.azure.com/openai/v1/", NanoDeployment="nano", NanoModel="gpt-5-nano-2025-08-07", MiniDeployment="mini", MiniModel="gpt-5-mini-2025-08-07", EmbeddingDeployment="embed", EmbeddingModel="text-embedding-3-small", ChatAlias="er-chat", EmbeddingAlias="er-embed", DeploymentAttestation="synthetic" };
        var transport=new SelectionTransport();var builder=WebApplication.CreateBuilder();builder.Logging.ClearProviders();builder.WebHost.UseUrls("http://127.0.0.1:0");
        await using var app=GatewayApplication.Build(builder,options,transport);await app.StartAsync();
        using var client=new HttpClient{BaseAddress=new Uri(app.Services.GetRequiredService<IServer>().Features.Get<IServerAddressesFeature>()!.Addresses.Single())};client.DefaultRequestHeaders.Authorization=new("Bearer",options.ServiceToken);
        async Task<JsonElement> Send(string page)
        {
            var request=new{model=options.ChatAlias,max_tokens=1024,messages=new[]{new{role="system",content="wrapper"},new{role="user",content=JsonSerializer.Serialize(new{Question="What information should be recorded about repeated lateness?",Sources=new[]{new{Reference="private-original-reference",DocumentID="private-id",Version="1",ContentHash="hash",ManifestHash="manifest",Page=new{Number=1,Text=page,Method="text",ExtractorRevision="v1"}}}})}}};
            using var response=await client.PostAsync("/v1/chat/completions",new StringContent(JsonSerializer.Serialize(request),Encoding.UTF8,"application/json"));Check(response.IsSuccessStatusCode,"selection response failed");using var body=JsonDocument.Parse(await response.Content.ReadAsStringAsync());return body.RootElement.Clone();
        }
        static JsonElement Completion(JsonElement value)=>JsonDocument.Parse(value.GetProperty("choices")[0].GetProperty("message").GetProperty("content").GetString()!).RootElement.Clone();
        const string policy="SYNTHETIC PROCESS EXAMPLE. Record observed dates and times and the expected start time. Refer questions to the designated reviewer.";
        try
        {
            transport.Select=spans=>new { excerpt_id=spans[1].GetProperty("id").GetString(),grounded=true,inference=false };
            var result=Completion(await Send(policy));Check(result.GetProperty("answer").GetString()=="Record observed dates and times and the expected start time.","server did not emit exact selected source sentence");Check(result.GetProperty("citations")[0].GetString()=="private-original-reference","original citation lost");Check(!transport.LastBody.Contains("private-original-reference")&&!transport.LastBody.Contains("private-id"),"private source identities leaked");
            foreach(var selection in new object[]{new{excerpt_id="e999",grounded=true,inference=false},new{excerpt_id="none",grounded=true,inference=false},new{excerpt_id="e0",grounded=false,inference=false},new{excerpt_id="e0",grounded=true,inference=true},new{excerpt_id="e0",grounded=true,inference=false,answer="invented"},new{excerpt_id=42,grounded=true,inference=false}})
            {transport.Select=_=>selection;int before=transport.Calls;var denied=await Send(policy);Check(denied.GetProperty("gateway_metadata").GetProperty("handoff").GetBoolean(),"unsafe selection released");Check(transport.Calls==before+1,"selection retried provider");}
            foreach(var malformed in new[]{"{\"excerpt_id\":\"e0\",\"excerpt_id\":\"e1\",\"grounded\":true,\"inference\":false}","{\"excerpt_id\":[\"e0\",\"e1\"],\"grounded\":true,\"inference\":false}","{\"excerpt_id\":\"e0\",\"grounded\":\"true\",\"inference\":false}","null"})
            {using var value=JsonDocument.Parse(malformed);transport.Select=_=>value.RootElement;var denied=await Send(policy);Check(denied.GetProperty("gateway_metadata").GetProperty("handoff").GetBoolean(),"duplicate, combined or mistyped selection released");}
            var unicode=string.Concat(Enumerable.Repeat("🙂",600))+" End.";transport.Select=spans=>new{excerpt_id=spans[0].GetProperty("id").GetString(),grounded=true,inference=false};result=Completion(await Send(unicode));var quote=result.GetProperty("answer").GetString()!;Check(Encoding.UTF8.GetByteCount(quote)<=900&&unicode.Contains(quote,StringComparison.Ordinal)&&!quote.Contains('\uFFFD'),"unicode span split or source text changed");
            var boundary="Word "+new string('x',894)+".";result=Completion(await Send(boundary+" Next sentence."));Check(result.GetProperty("answer").GetString()==boundary,"exact900-byte sentence was shortened or joined");
            transport.Select=spans=>{Check(spans.EnumerateArray().All(x=>Encoding.UTF8.GetByteCount(x.GetProperty("text").GetString()!)<=900),"unbounded excerpt");Check(!spans.ToString().Contains("person@example.invalid"),"source PII leaked");return new{excerpt_id=spans[0].GetProperty("id").GetString(),grounded=true,inference=false};};
            result=Completion(await Send("Contact person@example.invalid. Record observed dates and times."));Check(result.GetProperty("answer").GetString()=="Record observed dates and times.","redacted source text presented as original");
            int maskedCalls=transport.Calls;var masked=await Send("Contact person@example.invalid.");Check(masked.GetProperty("gateway_metadata").GetProperty("handoff").GetBoolean()&&transport.Calls==maskedCalls,"entirely redacted context reached provider");
            int calls=transport.Calls;var tooMany=await Send(string.Concat(Enumerable.Repeat("A sentence. ",140)));Check(tooMany.GetProperty("gateway_metadata").GetProperty("handoff").GetBoolean()&&transport.Calls==calls,"excerpt count limit reached provider");
            Console.WriteLine("PASS: exact server-selected excerpts, original citations, no fabricated text, selection denials, unicode/PII/count boundaries and no retries");
        }
        finally{await app.StopAsync();}
    }
    static void Check(bool ok,string message){if(!ok)throw new Exception(message);}
    sealed class SelectionTransport:HttpMessageHandler
    {
        public Func<JsonElement,object> Select=_=>throw new Exception("selection fixture missing");public string LastBody="";public int Calls;
        protected override async Task<HttpResponseMessage> SendAsync(HttpRequestMessage request,CancellationToken token)
        {
            Calls++;LastBody=await request.Content!.ReadAsStringAsync(token);using var data=JsonDocument.Parse(LastBody);using var context=JsonDocument.Parse(data.RootElement.GetProperty("messages")[1].GetProperty("content").GetString()!);
            Check(context.RootElement.TryGetProperty("excerpts",out var spans),"bounded excerpt-selection protocol missing");Check(spans.GetArrayLength() is >0 and <=128,"excerpt count unbounded");
            var selected=Select(spans);var body=new{model="gpt-5-nano-2025-08-07",usage=new{total_tokens=100},choices=new[]{new{finish_reason="stop",message=new{content=JsonSerializer.Serialize(selected)}}}};
            return new(HttpStatusCode.OK){Content=new StringContent(JsonSerializer.Serialize(body),Encoding.UTF8,"application/json")};
        }
    }
}
