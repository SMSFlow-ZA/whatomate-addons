using System.Reflection;
using System.Text;
using ErGateway;

static class HeadingExcerptTests
{
    public static void Run()
    {
        var method = typeof(GatewayService).Assembly.GetType("ErGateway.ApprovedExcerpts")!.GetMethod("Split", BindingFlags.Static | BindingFlags.NonPublic)!;
        string[] Split(string text) => ((IEnumerable<string>)method.Invoke(null, [text])!).ToArray();
        void Check(bool value, string reason) { if (!value) throw new Exception(reason); }
        const string question = "How do I follow up?";
        const string answer = "Keep your case reference and reply STATUS for the permitted state.";
        foreach (var gap in new[] { "\n", "\n\n", "\r\n\r\n" })
        {
            var text = question + gap + answer; var spans = Split(text);
            Check(spans.Length == 1 && spans[0] == text, "heading did not retain exact following answer");
        }
        Check(Split(question).Length == 0, "heading alone was offered as answer");
        Check(Split(question + "\n\n").Length == 0, "empty section was offered as answer");
        Check(Split("How " + new string('x', 1100) + "?\n" + answer).Single() == answer, "long heading prevented useful following body");
        Check(Split("How " + string.Concat(Enumerable.Repeat("🙂", 400)) + "?\n" + answer).Single() == answer, "Unicode heading split into false candidate");
        var inline = "Ask the reviewer which form is current? Then record the reference.";
        Check(Split(inline)[0] == "Ask the reviewer which form is current?", "ordinary in-body question was rewritten");
        var chain = question + "\nWhat happens next?\n" + answer;
        Check(Split(chain).Single() == chain, "adjacent headings lost exact source provenance");
        var exactBoundaryHeading = new string('x', 900 - question.Length - 1) + "\n" + question + "\n" + answer;
        Check(Split(exactBoundaryHeading).Single() == answer, "exact900-byte heading boundary did not advance safely to body");
        foreach (var text in new[] { "", " ", "?", "How", question, question + "\n" + new string('x', 2000), chain })
            foreach (var span in Split(text)) Check(span.Length > 0 && Encoding.UTF8.GetByteCount(span) <= 900 && text.Contains(span, StringComparison.Ordinal) && !span.Contains('\uFFFD'), "source or UTF8 limit changed");
        Console.WriteLine("PASS: FAQ headings retain exact explanatory body; heading-only/long/Unicode/empty boundaries progress safely");
    }
}
