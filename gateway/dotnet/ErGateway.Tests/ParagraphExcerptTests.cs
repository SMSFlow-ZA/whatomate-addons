using System.Reflection;
using System.Text;
using ErGateway;

static class ParagraphExcerptTests
{
    public static void Run()
    {
        var method=typeof(GatewayService).Assembly.GetType("ErGateway.ApprovedExcerpts")!.GetMethod("Split",BindingFlags.Static|BindingFlags.NonPublic)!;
        string[] Split(string text)=>((IEnumerable<string>)method.Invoke(null,[text])!).ToArray();
        void Check(bool value,string reason){if(!value)throw new Exception(reason);}
        const string steps="Minimum intake\nOffer the current concern form. Record brief facts, relevant dates and involved aliases. Avoid unrelated private details.";
        const string introductory="Purpose\nThis example describes a neutral review process. It does not determine an outcome.";
        var page="SYNTHETIC EXAMPLE ONLY\n\nExample procedure\nRevision 2 | staff audience\n\n"+introductory+"\n\n"+steps;
        var spans=Split(page);
        Check(spans.SequenceEqual(new[]{steps,introductory}),"procedure-first ordering lost complete instructions or approved purpose");
        Check(Split(introductory).Any(),"sole purpose source silently discarded");
        foreach(var list in new[]{"Steps\n- Record observed facts\n- Ask the reviewer for guidance","Steps\n1. Record observed facts\n2. Ask the reviewer for guidance","Steps\n• Record observed facts\n• Ask the reviewer for guidance"})
            Check(Split("EXAMPLE\n\n"+list).SequenceEqual(new[]{list}),"punctuation-free procedure list lost");
        const string question="How can a case be followed up?";
        const string body="Keep the reference. Send STATUS to receive the permitted state. This does not promise a response time.";
        Check(Split("EXAMPLE\n\n"+question+"\n\n"+body).SequenceEqual(new[]{question+"\n\n"+body}),"question and multi-sentence body fragmented");
        Check(Split("EXAMPLE\n\n"+question).Length==0,"structured heading-only page offered as answer");
        Check(Split("EXAMPLE\n\n"+question+"\n\nWhat happens next?").Length==0,"adjacent heading-only sections offered as answer");
        var longPage="EXAMPLE\n\nInstructions\n"+string.Concat(Enumerable.Repeat("Record a neutral observation. ",100));
        var longSpans=Split(longPage);
        Check(longSpans.Length>1,"oversized paragraph did not split within bound");
        foreach(var text in new[]{page,longPage,"EXAMPLE\n\n"+question+"\n\n"+body,"EXAMPLE\n\n"+string.Concat(Enumerable.Repeat("🙂",600))})
            foreach(var span in Split(text))Check(span.Length>0&&Encoding.UTF8.GetByteCount(span)<=900&&text.Contains(span,StringComparison.Ordinal)&&!span.Contains('\uFFFD'),"paragraph source provenance or Unicode bound changed");
        Console.WriteLine("PASS: complete structured procedure paragraphs, metadata/intro filtering, heading-only refusal and bounded exact Unicode spans");
    }
}
