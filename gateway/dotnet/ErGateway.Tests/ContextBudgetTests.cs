using System.Net;
using System.Text;
using System.Text.Json;
using ErGateway;
using Microsoft.AspNetCore.Builder;
using Microsoft.AspNetCore.Hosting;
using Microsoft.AspNetCore.Hosting.Server;
using Microsoft.AspNetCore.Hosting.Server.Features;
using Microsoft.Extensions.DependencyInjection;
using Microsoft.Extensions.Logging;

static class ContextBudgetTests
{
    public static async Task Run()
    {
        var options = new GatewayOptions { InstanceId="synthetic-context-budget",ServiceToken=new string('s',40),AzureApiKey=new string('a',40),AzureEndpoint="https://synthetic.openai.azure.com/openai/v1/",NanoDeployment="nano",NanoModel="gpt-5-nano-2025-08-07",MiniDeployment="mini",MiniModel="gpt-5-mini-2025-08-07",EmbeddingDeployment="embed",EmbeddingModel="text-embedding-3-small",ChatAlias="er-chat",EmbeddingAlias="er-embed",DeploymentAttestation="synthetic",MaxInputBytes=4096,MaxRequestsPerMinute=100 };
        var transport=new Capture();var builder=WebApplication.CreateBuilder();builder.Logging.ClearProviders();builder.WebHost.UseUrls("http://127.0.0.1:0");
        await using var app=GatewayApplication.Build(builder,options,transport);await app.StartAsync();
        using var client=new HttpClient {BaseAddress=new Uri(app.Services.GetRequiredService<IServer>().Features.Get<IServerAddressesFeature>()!.Addresses.Single())};client.DefaultRequestHeaders.Authorization=new("Bearer",options.ServiceToken);
        async Task<(HttpStatusCode,JsonElement)> Send(object context) {
            var request=new{model="er-chat",max_tokens=1024,messages=new[]{new{role="system",content="wrapper"},new{role="user",content=JsonSerializer.Serialize(context)}}};
            using var response=await client.PostAsync("/v1/chat/completions",new StringContent(JsonSerializer.Serialize(request),Encoding.UTF8,"application/json"));
            using var body=JsonDocument.Parse(await response.Content.ReadAsStringAsync());return(response.StatusCode,body.RootElement.Clone());
        }
        static object Source(int index,string text)=>new{Reference="ref-"+index,DocumentID="synthetic-"+index,Version="1",ContentHash="hash",ManifestHash="manifest",Page=new{Number=1,Text=text,Method="text",ExtractorRevision="fixture"}};
        const string first="Record the observed dates and times and the expected start time.";
        try {
            var pages=new[]{Source(0,first),Source(1,string.Concat(Enumerable.Repeat("A lower ranked illustrative process sentence with sufficient detail. ",32))),Source(2,"This lower page must not leapfrog the excluded page.")};
            var (status,result)=await Send(new{Question="What should a manager record when an employee is repeatedly late?",Sources=pages});
            Check(status==HttpStatusCode.OK&&transport.Calls==1,"bounded ranked prefix did not reach one upstream attempt");
            Check(transport.Bytes<=options.MaxInputBytes&&transport.Excerpts.SequenceEqual(new[]{first}),"whole page/rank boundary not retained");
            using var completion=JsonDocument.Parse(result.GetProperty("choices")[0].GetProperty("message").GetProperty("content").GetString()!);
            Check(completion.RootElement.GetProperty("answer").GetString()==first&&completion.RootElement.GetProperty("citations")[0].GetString()=="ref-0","quote/provenance changed");
            int before=transport.Calls;
            (status,_)=await Send(new{Question="What is the process?",Sources=new[]{pages[1],pages[0]}});
            Check(status==HttpStatusCode.RequestEntityTooLarge&&transport.Calls==before,"oversized first page skipped or sent");
            (status,_)=await Send(new{Question="What is the process?",Sources=new[]{pages[0],Source(1,"My manager sent unrelated private details yesterday.")}});
            Check(transport.Calls==before,"sensitive candidate bypassed by context fitting");
            transport.Classify=true;
            (status,result)=await Send(new{text="Synthetic example: an employee has arrived after the scheduled start time on several occasions. Suggest a category for an ER reviewer, without making a finding.",allowed_labels=new[]{"Attendance concern","Workplace concern","Conflict of interest","Escalation request"}});
            Check(status==HttpStatusCode.OK&&transport.Calls==before+1,"neutral attendance paraphrase rejected");
            Check(!transport.LastBody.Contains("employee")&&!transport.LastBody.Contains("arrived")&&transport.LastBody.Contains("attendance"),"classification narrative leaked");
            before=transport.Calls;
            foreach(var text in new[]{"A scheduling question about next year.","The employee arrived after the scheduled start time and declares a conflict of interest.","Choose a disciplinary sanction for arriving after the scheduled start time."}) {
                (status,_)=await Send(new{text,allowed_labels=new[]{"Attendance concern","Conflict of interest"}});
                Check(status==HttpStatusCode.UnprocessableEntity&&transport.Calls==before,"unknown, ambiguous or consequential classification reached upstream");
            }
            Console.WriteLine("PASS: whole ranked page context budget, first-page refusal, source protection and derived-topic paraphrase boundaries (mock upstream only)");
        } finally {await app.StopAsync();}
    }
    static void Check(bool value,string message){if(!value)throw new Exception(message);}
    sealed class Capture:HttpMessageHandler {
        public int Calls,Bytes; public string LastBody="";public string[] Excerpts=[];public bool Classify;
        protected override async Task<HttpResponseMessage> SendAsync(HttpRequestMessage request,CancellationToken token) {
            Calls++;LastBody=await request.Content!.ReadAsStringAsync(token);Bytes=Encoding.UTF8.GetByteCount(LastBody);
            using var payload=JsonDocument.Parse(LastBody);using var context=JsonDocument.Parse(payload.RootElement.GetProperty("messages")[1].GetProperty("content").GetString()!);
            string content;
            if(Classify) content="{\"label\":\"Attendance concern\",\"rationale\":\"Provisional attendance topic\"}";
            else {Excerpts=context.RootElement.GetProperty("excerpts").EnumerateArray().Select(x=>x.GetProperty("text").GetString()!).ToArray();content="{\"excerpt_id\":\"e0\",\"grounded\":true,\"inference\":false}";}
            return new(HttpStatusCode.OK){Content=new StringContent(JsonSerializer.Serialize(new{model="gpt-5-nano-2025-08-07",usage=new{total_tokens=100},choices=new[]{new{finish_reason="stop",message=new{content}}}}),Encoding.UTF8,"application/json")};
        }
    }
}
