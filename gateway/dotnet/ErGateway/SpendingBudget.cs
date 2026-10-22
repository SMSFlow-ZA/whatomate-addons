using System.Globalization;
using System.Text;
using System.Text.Json;
using System.Text.RegularExpressions;

namespace ErGateway;

// Single-writer append-only reservation journal. Reserve before network I/O;
// failures and unknown outcomes retain the complete reservation. Not an invoice.
public sealed class SpendingBudget : IDisposable
{
    private const long MaxLedgerBytes = 16 * 1024 * 1024;
    private readonly object sync = new();
    private readonly GatewayOptions options;
    private readonly TimeProvider clock;
    private readonly FileStream stream;
    private readonly List<Entry> entries = [];
    private string previousHash = "";
    private bool failed;
    private sealed record Header(int Schema, string Instance, string Currency, string CreatedUtc);
    private sealed record Entry(long Sequence, string Utc, long Minor, string Approval, string PreviousHash);

    public static void ValidateConfiguration(GatewayOptions options)
    {
        if (!Path.IsPathFullyQualified(options.SpendingLedgerPath) || !Regex.IsMatch(options.SpendingCurrency, "\\A[A-Z]{3}\\z") ||
            options.MonthlySpendLimitMinor < 1 || options.MonthlySpendLimitMinor > 1_000_000_000 ||
            options.DailySpendLimitMinor < 0 || options.DailySpendLimitMinor > options.MonthlySpendLimitMinor ||
            options.MaxCostMinorPerMillionTokens < 1 || options.MaxCostMinorPerMillionTokens > 1_000_000_000 ||
            string.IsNullOrWhiteSpace(options.InstanceId) || string.IsNullOrWhiteSpace(options.SpendingApprovalReference) || options.SpendingApprovalReference.Length > 256)
            throw new InvalidOperationException("Explicit durable spending limits and reviewed tariff/approval are required");
        // All ancestors must already exist and be controlled storage, never links.
        var parent = new DirectoryInfo(Path.GetDirectoryName(options.SpendingLedgerPath)!);
        while (parent is not null)
        {
            if (!parent.Exists || (parent.Attributes & FileAttributes.ReparsePoint) != 0)
                throw new InvalidOperationException("Spending storage must be an existing non-linked directory");
            parent = parent.Parent;
        }
        if (File.Exists(options.SpendingLedgerPath) && (File.GetAttributes(options.SpendingLedgerPath) & FileAttributes.ReparsePoint) != 0)
            throw new InvalidOperationException("Linked spending ledger forbidden");
    }

    // Explicit offline operator step only. Missing history never auto-initializes.
    public static void Initialize(GatewayOptions options, TimeProvider? clock = null)
    {
        ValidateConfiguration(options);
        using var file = new FileStream(options.SpendingLedgerPath, FileMode.CreateNew, FileAccess.Write, FileShare.None);
        if (!OperatingSystem.IsWindows()) File.SetUnixFileMode(options.SpendingLedgerPath, UnixFileMode.UserRead | UnixFileMode.UserWrite);
        var header = new Header(1, options.InstanceId, options.SpendingCurrency, Stamp((clock ?? TimeProvider.System).GetUtcNow()));
        file.Write(Encoding.UTF8.GetBytes(JsonSerializer.Serialize(header) + "\n"));
        file.Flush(true);
    }

    public SpendingBudget(GatewayOptions options, TimeProvider? clock = null)
    {
        ValidateConfiguration(options);
        this.options = options; this.clock = clock ?? TimeProvider.System;
        stream = new FileStream(options.SpendingLedgerPath, FileMode.Open, FileAccess.ReadWrite, FileShare.None);
        try
        {
            if (stream.Length < 1 || stream.Length > MaxLedgerBytes) throw new InvalidOperationException("Invalid spending history size");
            using var reader = new StreamReader(stream, new UTF8Encoding(false, true), leaveOpen: true);
            var text = reader.ReadToEnd();
            if (!text.EndsWith('\n')) throw new InvalidOperationException("Incomplete spending history");
            var lines = text.Split('\n');
            var header = JsonSerializer.Deserialize<Header>(lines[0]) ?? throw new InvalidOperationException("Missing spending identity");
            if (header.Schema != 1 || header.Instance != options.InstanceId || header.Currency != options.SpendingCurrency || !ValidStamp(header.CreatedUtc)) throw new InvalidOperationException("Spending identity mismatch");
            previousHash = RequestBudget.Hash(lines[0]);
            var lastTime = header.CreatedUtc;
            foreach (var line in lines.Skip(1).SkipLast(1))
            {
                var item = JsonSerializer.Deserialize<Entry>(line) ?? throw new InvalidOperationException("Invalid spending entry");
                if (item.Sequence != entries.Count + 1 || item.Minor < 1 || item.PreviousHash != previousHash || !ValidStamp(item.Utc) || string.CompareOrdinal(item.Utc, lastTime) < 0 || string.IsNullOrWhiteSpace(item.Approval))
                    throw new InvalidOperationException("Broken spending history");
                entries.Add(item); lastTime = item.Utc; previousHash = RequestBudget.Hash(line);
            }
            // Header creation itself is a monotonic clock floor.
            if (entries.Count == 0) createdUtc = header.CreatedUtc;
            stream.Position = stream.Length;
        }
        catch { stream.Dispose(); throw; }
    }
    private readonly string createdUtc = "";
    private static string Stamp(DateTimeOffset value) => value.UtcDateTime.ToString("yyyy-MM-dd'T'HH:mm:ss.fffffff'Z'", CultureInfo.InvariantCulture);
    private static bool ValidStamp(string value) => DateTimeOffset.TryParseExact(value, "yyyy-MM-dd'T'HH:mm:ss.fffffff'Z'", CultureInfo.InvariantCulture, DateTimeStyles.AssumeUniversal, out _);

    public bool TryReserve(int reservedTokens)
    {
        lock (sync)
        {
            if (failed || reservedTokens < 1) return false;
            try
            {
                ValidateConfiguration(options);
                if (!File.Exists(options.SpendingLedgerPath) || new FileInfo(options.SpendingLedgerPath).Length != stream.Length) { failed = true; return false; }
                var utc = Stamp(clock.GetUtcNow());
                var floor = entries.Count == 0 ? createdUtc : entries[^1].Utc;
                if (string.CompareOrdinal(utc, floor) < 0) return false;
                var minor = checked(((long)reservedTokens * options.MaxCostMinorPerMillionTokens + 999_999) / 1_000_000);
                var month = entries.Where(x => x.Utc[..7] == utc[..7]).Sum(x => x.Minor);
                var day = entries.Where(x => x.Utc[..10] == utc[..10]).Sum(x => x.Minor);
                if (minor > options.MonthlySpendLimitMinor - month || (options.DailySpendLimitMinor > 0 && minor > options.DailySpendLimitMinor - day)) return false;
                var item = new Entry(entries.Count + 1, utc, minor, options.SpendingApprovalReference, previousHash);
                var line = JsonSerializer.Serialize(item);
                var bytes = Encoding.UTF8.GetBytes(line + "\n");
                if (stream.Length + bytes.Length > MaxLedgerBytes) return false;
                stream.Write(bytes); stream.Flush(true);
                entries.Add(item); previousHash = RequestBudget.Hash(line);
                return true;
            }
            catch (Exception error) when (error is IOException or UnauthorizedAccessException or OverflowException or InvalidOperationException)
            { failed = true; return false; }
        }
    }
    public void Dispose() { lock (sync) { failed = true; stream.Dispose(); } }
}
