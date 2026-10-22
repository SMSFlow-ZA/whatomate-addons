using System.Security.Cryptography;
using System.Text;

namespace ErGateway;

// Budgets are conservative per running process. Every admitted attempt retains
// its full reservation, including unknown outcomes; no retries or refunds.
public sealed class RequestBudget(GatewayOptions options)
{
    private readonly object sync = new();
    private readonly SemaphoreSlim concurrent = new(options.MaxConcurrentRequests);
    private long window = Environment.TickCount64 / 60000;
    private int requests;
    private int tokens;
    public IDisposable? TryAcquire(int reservedTokens)
    {
        lock (sync)
        {
            var current = Environment.TickCount64 / 60000;
            if (current != window) { window = current; requests = 0; tokens = 0; }
            if (reservedTokens < 1 || requests >= options.MaxRequestsPerMinute || reservedTokens > options.MaxTokenBudgetPerMinute - tokens || !concurrent.Wait(0)) return null;
            requests++; tokens += reservedTokens;
            return new Lease(concurrent);
        }
    }
    private sealed class Lease(SemaphoreSlim gate) : IDisposable { public void Dispose() => gate.Release(); }
    public static string Hash(string value) => Convert.ToHexString(SHA256.HashData(Encoding.UTF8.GetBytes(value)));
}
