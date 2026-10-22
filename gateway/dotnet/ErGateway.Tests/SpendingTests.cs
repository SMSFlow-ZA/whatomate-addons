using ErGateway;

static class SpendingTests
{
    public static void Run()
    {
        static void Check(bool value, string message) { if (!value) throw new Exception(message); }
        static bool Refused(Action action) { try { action(); return false; } catch (Exception e) when (e is InvalidOperationException or IOException) { return true; } }
        var root = Path.Combine(Path.GetTempPath(), "gateway-budget-" + Guid.NewGuid().ToString("N"));
        Directory.CreateDirectory(root);
        var clock = new TestClock(new DateTimeOffset(2026, 10, 22, 10, 0, 0, TimeSpan.Zero));
        GatewayOptions Options(string path) => new() { InstanceId = "synthetic-budget", SpendingLedgerPath = path, SpendingCurrency = "ZAR", MonthlySpendLimitMinor = 100, DailySpendLimitMinor = 60, MaxCostMinorPerMillionTokens = 1_000_000, SpendingApprovalReference = "synthetic-test-approval" };
        var options = Options(Path.Combine(root, "reservations.jsonl"));
        try
        {
            Check(Refused(() => { using var _ = new SpendingBudget(options, clock); }), "missing ledger was silently reset");
            SpendingBudget.Initialize(options, clock);
            Check(Refused(() => SpendingBudget.Initialize(options, clock)), "ledger initialization overwrote history");
            using (var first = new SpendingBudget(options, clock))
            {
                Check(Refused(() => { using var _ = new SpendingBudget(options, clock); }), "two gateway owners admitted");
                Check(first.TryReserve(40), "first durable reservation failed");
                Check(!first.TryReserve(21), "daily monetary ceiling exceeded");
            }
            using (var restarted = new SpendingBudget(options, clock))
            {
                Check(!restarted.TryReserve(21), "restart reset spending allowance");
                clock.Now = clock.Now.AddDays(1);
                Check(restarted.TryReserve(60), "next-day remaining month budget unavailable");
                clock.Now = clock.Now.AddDays(1);
                Check(!restarted.TryReserve(1), "monthly monetary ceiling exceeded");
                clock.Now = new DateTimeOffset(2026, 11, 1, 0, 0, 0, TimeSpan.Zero);
                Check(restarted.TryReserve(1), "UTC month rollover failed");
                clock.Now = clock.Now.AddMinutes(-1);
                Check(!restarted.TryReserve(1), "clock rollback reopened allowance");
            }
            var wrong = Options(options.SpendingLedgerPath); wrong.InstanceId = "other-instance";
            Check(Refused(() => { using var _ = new SpendingBudget(wrong, clock); }), "ledger accepted another instance");
            File.AppendAllText(options.SpendingLedgerPath, "{\"partial\":");
            Check(Refused(() => { using var _ = new SpendingBudget(options, clock); }), "torn journal silently repaired/reset");

            var parallel = Options(Path.Combine(root, "parallel.jsonl")); parallel.DailySpendLimitMinor = 0;
            SpendingBudget.Initialize(parallel, clock);
            using (var budget = new SpendingBudget(parallel, clock))
            {
                int admitted = 0;
                Parallel.For(0, 50, _ => { if (budget.TryReserve(10)) Interlocked.Increment(ref admitted); });
                Check(admitted == 10, "concurrent spending exceeded monthly ceiling");
            }
            using (var reopened = new SpendingBudget(parallel, clock)) Check(!reopened.TryReserve(1), "unknown/failed reservations refunded on restart");
            Console.WriteLine("PASS: durable spending restart, exclusive owner, daily/monthly limits, UTC rollover, clock rollback, torn journal and concurrency");
        }
        finally { Directory.Delete(root, true); }
    }
    private sealed class TestClock(DateTimeOffset now) : TimeProvider
    {
        public DateTimeOffset Now = now;
        public override DateTimeOffset GetUtcNow() => Now;
    }
}
