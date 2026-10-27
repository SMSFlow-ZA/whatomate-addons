using System.Text;
using System.Text.RegularExpressions;

namespace ErGateway;

// These are literal contiguous source spans, not summaries or model-authored text.
internal static class ApprovedExcerpts
{
    internal const int MaximumCount = 128;
    internal const int MaximumBytes = 900;

    private static bool QuestionHeading(string line)
    {
        if (!line.EndsWith('?')) return false;
        string first = line.Split(' ', 2)[0];
        return new[] { "how", "what", "when", "where", "who", "which", "why", "can", "do", "does", "is", "are", "should" }
            .Contains(first, StringComparer.OrdinalIgnoreCase);
    }

    internal static IEnumerable<string> Split(string text)
    {
        var separators=Regex.Matches(text,@"\r?\n[\t ]*\r?\n",RegexOptions.CultureInvariant,TimeSpan.FromMilliseconds(50));
        if(separators.Count==0)
        {
            foreach(var span in Sentences(text))yield return span;
            yield break;
        }
        var blocks=new List<(int Start,int End)>();int cursor=0;
        foreach(Match gap in separators){blocks.Add((cursor,gap.Index));cursor=gap.Index+gap.Length;}
        blocks.Add((cursor,text.Length));
        bool Prose(string block)=>Regex.IsMatch(block,@"[.!?](?:\s|$)|(?m)^[\t ]*(?:[-*•]|[0-9]+[.)])[\t ]+\S",RegexOptions.CultureInvariant,TimeSpan.FromMilliseconds(50))
            || Encoding.UTF8.GetByteCount(block)>MaximumBytes;
        bool Intro(string block)=>new[]{"purpose","purpose and scope","scope","introduction","overview"}
            .Contains(block.Split('\n',2)[0].Trim(),StringComparer.OrdinalIgnoreCase);
        var introductions=new List<string>();
        for(int i=0;i<blocks.Count;i++)
        {
            int start=blocks[i].Start,end=blocks[i].End;
            while(start<end&&char.IsWhiteSpace(text[start]))start++;
            while(end>start&&char.IsWhiteSpace(text[end-1]))end--;
            if(start==end)continue;
            string block=text[start..end];
            // Separate layout metadata is not an answer. Keep actual answer
            // paragraphs together, including all their qualifications/steps.
            if(QuestionHeading(block)&&!block.Contains('\n'))
            {
                while(i+1<blocks.Count&&QuestionHeading(text[start..end].Trim()))end=blocks[++i].End;
                block=text[start..end].TrimEnd();end=start+block.Length;
                if(block.Split('\n',StringSplitOptions.RemoveEmptyEntries).All(line=>QuestionHeading(line.Trim())))continue;
            }
            if(!Prose(block))continue;
            if(Intro(block)){introductions.Add(block);continue;}
            if(Encoding.UTF8.GetByteCount(block)<=MaximumBytes)yield return block;
            else foreach(var span in Sentences(block))yield return span;
        }
        // Introductory context remains available for questions about purpose or
        // scope; practical passages come first without deleting approved facts.
        foreach(var block in introductions)
            if(Encoding.UTF8.GetByteCount(block)<=MaximumBytes)yield return block;
            else foreach(var span in Sentences(block))yield return span;
    }

    private static IEnumerable<string> Sentences(string text)
    {
        int start = 0;
        while (start < text.Length)
        {
            while (start < text.Length && char.IsWhiteSpace(text[start])) start++;
            if (start == text.Length) yield break;
            // A long standalone question heading cannot become a truncated
            // answer. Skip its complete line, keeping the following source text.
            int lineEnd = text.IndexOf('\n', start);
            if (lineEnd < 0) lineEnd = text.Length;
            if (QuestionHeading(text[start..lineEnd].Trim()) && Encoding.UTF8.GetByteCount(text[start..lineEnd]) >= MaximumBytes)
            { start = lineEnd; continue; }
            int end = start, bytes = 0, whitespace = -1, headingEnd = -1;
            bool reachedLimit = false;
            foreach (var rune in text.AsSpan(start).EnumerateRunes())
            {
                if (bytes + rune.Utf8SequenceLength > MaximumBytes) { reachedLimit = true; break; }
                bytes += rune.Utf8SequenceLength; end += rune.Utf16SequenceLength;
                if (Rune.IsWhiteSpace(rune)) whitespace = end;
                if ((rune.Value is '.' or '?' or '!') && (end == text.Length || char.IsWhiteSpace(text[end])))
                {
                    int lineStart = text.LastIndexOf('\n', end - 1) + 1;
                    // Keep a question heading and its answer contiguous. It is
                    // still one literal source substring, never an expansion or
                    // invented answer. Body questions within a sentence do not
                    // qualify as standalone headings.
                    bool endOfLine = end == text.Length || text[end..].TakeWhile(c => c != '\n').All(char.IsWhiteSpace);
                    if (rune.Value == '?' && lineStart >= start && endOfLine && QuestionHeading(text[lineStart..end].Trim()))
                    { headingEnd = end; continue; }
                    break;
                }
            }
            // Prefer a word boundary when a long sentence reaches the byte cap.
            if (reachedLimit && whitespace > start) end = whitespace;
            if (headingEnd >= 0 && end < headingEnd) end = headingEnd;
            int trimmed = end;
            while (trimmed > start && char.IsWhiteSpace(text[trimmed - 1])) trimmed--;
            // No body means no answer candidate. The cursor always advances,
            // including empty, malformed, Unicode and byte-limited headings.
            if (trimmed > start && (headingEnd < 0 || text[headingEnd..trimmed].Any(c => !char.IsWhiteSpace(c))))
                yield return text[start..trimmed];
            start = end;
        }
    }
}
