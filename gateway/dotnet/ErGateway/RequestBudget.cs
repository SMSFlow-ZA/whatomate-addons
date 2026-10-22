using System.Security.Cryptography;
using System.Text;

namespace ErGateway;

// Minute/concurrency limits are per process. Configured monetary reservations
// persist separately; all admitted attempts retain them, including unknown outcomes.
public sealed class RequestBudget(GatewayOptions options) : IDisposable
{
    private readonly SpendingBudget? spending = options.SpendingLedgerPath.Length == 0 ? null : new(options);
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
            if (spending is not null && !spending.TryReserve(reservedTokens)) { concurrent.Release(); return null; }
            requests++; tokens += reservedTokens;
            return new Lease(concurrent);
        }
    }
    private sealed class Lease(SemaphoreSlim gate) : IDisposable { public void Dispose() => gate.Release(); }
    public static string Hash(string value) => Convert.ToHexString(SHA256.HashData(Encoding.UTF8.GetBytes(value)));
    public void Dispose() { spending?.Dispose(); concurrent.Dispose(); }
}
