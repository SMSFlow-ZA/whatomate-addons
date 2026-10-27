using System.Text;

namespace ErGateway;

// These are literal contiguous source spans, not summaries or model-authored text.
internal static class ApprovedExcerpts
{
    internal const int MaximumCount = 128;
    internal const int MaximumBytes = 900;

    internal static IEnumerable<string> Split(string text)
    {
        int start = 0;
        while (start < text.Length)
        {
            while (start < text.Length && char.IsWhiteSpace(text[start])) start++;
            if (start == text.Length) yield break;
            int end = start, bytes = 0, whitespace = -1;
            bool reachedLimit = false;
            foreach (var rune in text.AsSpan(start).EnumerateRunes())
            {
                if (bytes + rune.Utf8SequenceLength > MaximumBytes) { reachedLimit = true; break; }
                bytes += rune.Utf8SequenceLength; end += rune.Utf16SequenceLength;
                if (Rune.IsWhiteSpace(rune)) whitespace = end;
                if ((rune.Value is '.' or '?' or '!') && (end == text.Length || char.IsWhiteSpace(text[end]))) break;
            }
            // Prefer a word boundary when a long sentence reaches the byte cap.
            if (reachedLimit && whitespace > start) end = whitespace;
            int trimmed = end;
            while (trimmed > start && char.IsWhiteSpace(text[trimmed - 1])) trimmed--;
            if (trimmed > start) yield return text[start..trimmed];
            start = end;
        }
    }
}
