using System.Security.Cryptography;
using System.Text;
using System.Text.RegularExpressions;

namespace ErGateway;

public sealed class GatewayOptions
{
    public string InstanceId { get; set; } = "";
    public string ServiceToken { get; set; } = "";
    public string ServiceTokenFile { get; set; } = "";
    public string AzureApiKey { get; set; } = "";
    public string AzureApiKeyFile { get; set; } = "";
    public string AzureEndpoint { get; set; } = "";
    public string NanoDeployment { get; set; } = "";
    public string NanoModel { get; set; } = "";
    public string MiniDeployment { get; set; } = "";
    public string MiniModel { get; set; } = "";
    public string EmbeddingDeployment { get; set; } = "";
    public string EmbeddingModel { get; set; } = "";
    public string ChatAlias { get; set; } = "";
    public string EmbeddingAlias { get; set; } = "";
    public string ProcessingScope { get; set; } = "za_only";
    public string InternationalProcessingApproval { get; set; } = "";
    public string InternationalProcessingDisclosure { get; set; } = "";
    public string DeploymentAttestation { get; set; } = "";
    public int MaxConcurrentRequests { get; set; } = 2;
    public int MaxRequestsPerMinute { get; set; } = 30;
    public int MaxTokenBudgetPerMinute { get; set; } = 65536;
    public int MaxInputBytes { get; set; } = 32768;
    public int MaxCompletionTokens { get; set; } = 2048;
    public int TimeoutSeconds { get; set; } = 45;
    public int MaxResponseBytes { get; set; } = 1048576;
    public string SpendingLedgerPath { get; set; } = "";
    public string SpendingCurrency { get; set; } = "";
    public string SpendingApprovalReference { get; set; } = "";
    public long MonthlySpendLimitMinor { get; set; }
    public long DailySpendLimitMinor { get; set; }
    public long MaxCostMinorPerMillionTokens { get; set; }

    public void LoadSecrets()
    {
        ServiceToken = ReadSecret(ServiceToken, ServiceTokenFile);
        AzureApiKey = ReadSecret(AzureApiKey, AzureApiKeyFile);
    }
    private static string ReadSecret(string inline, string path)
    {
        if (path.Length == 0) return inline;
        if (inline.Length != 0 || !Path.IsPathFullyQualified(path) || new FileInfo(path).Length > 8192)
            throw new InvalidOperationException("Invalid exclusive secret file configuration");
        return File.ReadAllText(path).TrimEnd('\r', '\n');
    }
    public void Validate()
    {
        static bool Name(string value) => Regex.IsMatch(value, @"\A[A-Za-z0-9][A-Za-z0-9_.-]{0,127}\z", RegexOptions.CultureInvariant);
        if (!Name(InstanceId) || ServiceToken.Length < 32 || ServiceToken.Length > 4096 || AzureApiKey.Length < 16 || AzureApiKey.Length > 4096 || ServiceToken == AzureApiKey || ServiceToken.Any(char.IsWhiteSpace) || AzureApiKey.Any(char.IsWhiteSpace))
            throw new InvalidOperationException("Invalid per-instance service identity or upstream credential");
        if (!Uri.TryCreate(AzureEndpoint, UriKind.Absolute, out var endpoint) || endpoint.Scheme != "https" || !endpoint.IsDefaultPort || !Regex.IsMatch(endpoint.Host, @"\A[a-z0-9-]+\.openai\.azure\.com\z", RegexOptions.CultureInvariant) || endpoint.AbsolutePath != "/openai/v1/" || endpoint.Query.Length != 0 || endpoint.Fragment.Length != 0 || endpoint.UserInfo.Length != 0)
            throw new InvalidOperationException("A fixed Azure OpenAI HTTPS v1 endpoint is required");
        if (!Name(NanoDeployment) || !Name(MiniDeployment) || !Name(EmbeddingDeployment) || !Regex.IsMatch(NanoModel, @"\Agpt-5-nano-\d{4}-\d{2}-\d{2}\z") || !Regex.IsMatch(MiniModel, @"\Agpt-5-mini-\d{4}-\d{2}-\d{2}\z") || !Name(EmbeddingModel) || !Name(ChatAlias) || !Name(EmbeddingAlias) || ChatAlias == EmbeddingAlias)
            throw new InvalidOperationException("Explicit deployment names, actual model versions and distinct logical aliases are required");
        if (string.IsNullOrWhiteSpace(DeploymentAttestation) || (ProcessingScope != "za_only" && ProcessingScope != "global_standard") || (ProcessingScope == "global_standard" && (string.IsNullOrWhiteSpace(InternationalProcessingApproval) || string.IsNullOrWhiteSpace(InternationalProcessingDisclosure))))
            throw new InvalidOperationException("Reviewed processing evidence and explicit international approval/disclosure are required");
        if (MaxConcurrentRequests is < 1 or > 8 || MaxRequestsPerMinute is < 1 or > 600 || MaxTokenBudgetPerMinute is < 1024 or > 1000000 || MaxInputBytes is < 1024 or > 65536 || MaxCompletionTokens is < 128 or > 8192 || TimeoutSeconds is < 1 or > 55 || MaxResponseBytes is < 1024 or > 1048576)
            throw new InvalidOperationException("Gateway resource bounds are invalid");
    }
    public bool Authenticate(string authorization)
    {
        if (!authorization.StartsWith("Bearer ", StringComparison.Ordinal) || authorization.Length > 4103) return false;
        var actual = SHA256.HashData(Encoding.UTF8.GetBytes(authorization[7..]));
        var expected = SHA256.HashData(Encoding.UTF8.GetBytes(ServiceToken));
        return CryptographicOperations.FixedTimeEquals(actual, expected);
    }
}
