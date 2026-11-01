// Golden-vector generator for the NuGet version, range and ID rules of trove (R-NUGET-003,
// R-NUGET-004). It prints one JSON document on stdout; generate.sh stores it as
// internal/formats/nuget/version/testdata/vectors.json. The Go packages
// internal/formats/nuget/version and internal/formats/nuget/id must agree with every entry.
//
// Accept or reject, the normalized forms, the ordering and the range semantics come from the
// pinned NuGet.Versioning library. Two things are trove's own rule written down here, because the
// library has no such notion: is_semver2 (the rule nuget.org applies) and the ID rules (the
// regular expressions of R-NUGET-003 under .NET's definition of \w).
using System.Text;
using System.Text.Json;
using System.Text.RegularExpressions;
using NuGet.Versioning;

var comparer = VersionComparer.Default;

// ---- versions -------------------------------------------------------------------------------

var versionInputs = new List<string>
{
    // plain forms, missing parts, zero fourth part
    "1", "1.0", "1.0.0", "1.0.0.0", "1.2", "1.2.3", "1.2.3.4", "1.2.3.0", "1.2.0.4", "0", "0.0", "0.0.0", "0.0.0.0",
    "0.0.1", "0.1.0", "1.0.0.1", "10.0.0", "2.0.0", "2.5", "2.5.1.3", "2.5.1.0", "9.9.9.9",
    // leading zeros
    "01", "01.02", "01.02.03", "01.02.03.0", "01.2.0.0", "001.002.003.004", "00", "0.00", "1.01", "1.0.00.1",
    // limits of the numeric parts (Int32)
    "2147483647", "2147483647.2147483647.2147483647.2147483647", "2147483648", "1.2147483648", "1.0.0.2147483648",
    "99999999999999999999", "4294967296", "1.0.0.4294967295",
    // not parts
    "", " ", "a", "v1.0", "V1.0.0", "1.a", "1.0.0.a", "1..0", ".1", "1.", "1.0.", "1.0.0.0.0", "1.0.0.0.1", "-1", "1.-1",
    "+1", "1,0", "1 0", "1. 0", "1 .0", "1.0 .0", "١.٠", "1.0.0\u0000", "1.0.0\n", "1.0.0\r\n",
    // white space around
    " 1.0.0", "1.0.0 ", "  1.0.0-beta  ", "\t1.0\t", "\n1.0.0\n", " 1.0.0", "1.0.0 ", " 1.0.0", "\u001f1.0.0",
    // pre-release labels
    "1.0.0-beta", "1.0.0-Beta", "1.0.0-BETA", "1.0.0-beta.1", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-beta.10",
    "1.0.0-beta1", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-rc.1", "1.0.0-RC.1", "1.0.0-rc.2",
    "1.0.0-rc.10", "1.0.0-0", "1.0.0-1", "1.0.0-01", "1.0.0-001", "1.0.0-0.0", "1.0.0-0.1", "1.0.0-1.0", "1.0.0-a.0",
    "1.0.0-a.00", "1.0.0-a.1.b", "1.0.0-a-b", "1.0.0--", "1.0.0---", "1.0.0--a", "1.0.0-a-", "1.0.0-a--b", "1.0.0-1a",
    "1.0.0-a1", "1.0.0-A1", "1.0.0-1-", "1.0.0-0-0", "1.0.0-x.7.z.92", "1.0.0-x-y-z.--", "1.0.0-ci.5",
    "1.0.0-12345678901234567890", "1.0.0-12345678901234567891", "1.0.0-2147483647", "1.0.0-2147483648",
    "1.0.0-4294967296", "1.0.0-4294967297", "1.0.0-99999999999999999999.1", "1.0.0-9999999999999999999",
    "1.0.0-9999999999999999998", "1.0.0-alpha.99999999999999999999",
    "1.0.0--5", "1.0.0--1", "1.0.0--0", "1.0.0--5.1", "1.0.0-a.-5", "1.0.0-1.-5", "1.0.0--2147483648", "1.0.0--2147483649", "1.0.0--99999999999",
    "1.0.0-0-0", "1.0.0-5-", "1.0.0-5-5", "1.0.0--a", "1.0.0---1", "1.0.0-00000000000000000001", "1.0.0-10", "1.0.0-9", "1.0.0-2147483646",
    "1.0.0-99999999999", "1.0.0-100000000000", "1.0.0-3000000000", "1.0.0-3000000000.1", "1.0.0-10000000000.1", "1.0.0-9999999999",
    "1.0.0-", "1.0.0-.", "1.0.0-.a", "1.0.0-a.", "1.0.0-a..b", "1.0.0-a_b", "1.0.0-a b", "1.0.0-é", "1.0.0-ä.1",
    "1.0.0-a+b", "1.0.0-a.b+c.d", "1.0.0 -beta", "1.0.0- beta", "1.0.0-beta ", "1.0.0-be ta", "1.0.0-be\ta",
    "1.0.0-a-b-c", "1.0.0-z", "1.0.0-Z", "1.0.0-a.b.c.d.e.f.g.h", "1.0.0-a.b.c.d.e.f.g.i", "1.0.0-aa", "1.0.0-a.a",
    "1.0.0-alpha.0", "1.0.0-alpha.00", "1.0.0-alpha.000", "1.0.0-alpha.1a", "1.0.0-alpha.a1",
    "1.0.0.1-beta", "1.0.0.0-beta", "1.2.3.0-RC", "1.2-rc", "1-rc", "1-rc.1",
    // build metadata
    "1.0.0+a", "1.0.0+A", "1.0.0+a1", "1.0.0+sha.9f2c", "1.0.0+a.b", "1.0.0+001", "1.0.0+0", "1.0.0+-", "1.0.0+a-b",
    "3.0.0+a1", "3.0.0+b2", "2.5.1.0+sha.9f2c", "2.6.1.0+sha.9f2c", "1.0.0-beta+meta", "1.0.0-beta.1+meta.1",
    "01.2.0.0-RC.1+sha.9f2c", "1.2.0-RC.1+sha.9f2c", "1.0+meta", "1+meta", "1.2.3.4+meta", "1.0.0-rc.1+build.1",
    "1.0.0+", "1.0.0+.", "1.0.0+a.", "1.0.0+.a", "1.0.0+a..b", "1.0.0+a+b", "1.0.0++", "1.0.0+a_b", "1.0.0+é",
    "1.0.0+a b", "1.0.0 +a", "1.0.0+ a", "1.0.0+a-", "1.0.0-a+b-c", "1.0.0-beta+beta", "1.0.0-+a", "1.0.0-+",
    "+", "-", "+a", "-a", "1+", "1-", "1.0.0+--", "1.0.0+0.0",
    // a long one
    "1.0.0-" + new string('a', 200), "1.0.0-" + new string('a', 300), "1.0.0-" + string.Join(".", Enumerable.Repeat("1", 60)),
    "1.0.0-" + new string('9', 100), "1.0.0-" + new string('9', 300),
};

// A deterministic cross product widens the coverage of the hand-written list.
var numbers = new[] { "0", "1", "01", "10", "2", "2147483647", "2147483648", "a", "" };
var releases = new[] { "", "-", "-a", "-A", "-1", "-01", "-a.1", "-a.01", "-1.a", "-a.b.c", "-a-b", "-.a" };
var metas = new[] { "", "+", "+m", "+m.1", "+01" };
for (var parts = 1; parts <= 5; parts++)
{
    foreach (var n in numbers)
    {
        foreach (var rel in releases)
        {
            foreach (var meta in metas)
            {
                var core = string.Join(".", Enumerable.Repeat(n, parts));
                versionInputs.Add(core + rel + meta);
            }
        }
    }
}
// Mixed parts (a different value in each position).
foreach (var core in new[] { "1.2", "1.2.3", "1.2.3.4", "0.2.0", "0.0.3", "0.0.0.4", "1.0.3", "1.2.0.4", "7.0.0.0", "7.0.0.1" })
{
    foreach (var rel in new[] { "", "-rc.1", "-RC.1", "-rc.2", "-rc.10", "-alpha", "-beta", "-0", "-1" })
    {
        versionInputs.Add(core + rel);
    }
}

versionInputs = versionInputs.Distinct(StringComparer.Ordinal).ToList();

var parsed = new List<(string Input, NuGetVersion? Version)>();
foreach (var input in versionInputs)
{
    parsed.Add((input, NuGetVersion.TryParse(input, out var v) ? v : null));
}

// Dense rank of the distinct valid versions under the library's comparer.
var ordered = parsed.Where(p => p.Version != null).Select(p => p.Version!).ToList();
ordered.Sort(comparer);
var rankOf = new List<NuGetVersion>();
foreach (var v in ordered)
{
    if (rankOf.Count == 0 || comparer.Compare(rankOf[^1], v) != 0)
    {
        rankOf.Add(v);
    }
}

int Rank(NuGetVersion v) => rankOf.FindIndex(r => comparer.Compare(r, v) == 0);

// The rule nuget.org applies (R-NUGET-004): a dot in the pre-release label, or build metadata.
bool IsSemVer2(NuGetVersion v) => (v.IsPrerelease && v.Release.Contains('.')) || v.HasMetadata;

var versionEntries = new List<object>();
foreach (var (input, v) in parsed)
{
    if (v == null)
    {
        versionEntries.Add(new { input, valid = false });
        continue;
    }
    var normalized = v.ToNormalizedString();
    versionEntries.Add(new
    {
        input,
        valid = true,
        normalized,
        full = v.ToFullString(),
        url = normalized.ToLowerInvariant(),
        metadata = v.Metadata,
        prerelease = v.IsPrerelease,
        semver2 = IsSemVer2(v),
        rank = Rank(v),
    });
}

// ---- ranges ---------------------------------------------------------------------------------

var probes = new[]
{
    "0.0.0", "0.9.0", "0.9.9", "1.0.0-alpha", "1.0.0-beta.1", "1.0.0", "1.0.0.1", "1.0.1", "1.5.0", "1.9.9",
    "2.0.0-rc.1", "2.0.0", "2.0.1", "2.5.0", "3.0.0-beta", "3.0.0", "3.0.0+meta", "4.0.0", "10.0.0", "1.0.0+sha.1",
};

var rangeInputs = new List<string>
{
    "1.0", "1.0.0", "1", "2.0", "2.5.1.3", "0.0", "0", "1.0.0-beta", "1.0.0-beta.1", "1.0.0+meta", "1.0.0-beta.1+meta",
    "[1.0]", "[1.0.0]", "[1]", "[1.0.0.0]", "[1.0.0-beta]", "[1.0.0+meta]", "[1.0.0.1]", "[ 1.0 ]", "[1.0 ]", "[ 1.0]",
    "[1.0,2.0)", "[1.0,2.0]", "(1.0,2.0)", "(1.0,2.0]", "[1.0, 2.0)", "[ 1.0 , 2.0 )", "[1.0 , 2.0]",
    "[1.0,)", "(1.0,)", "(,1.0]", "(,1.0)", "[,1.0]", "[,1.0)", "(,)", "[,]", "(,]", "[,)", "[1.0,]", "[1.0,",
    "[1.0,1.0]", "[1.0,1.0)", "(1.0,1.0)", "(1.0,1.0]", "(2.0,1.0)", "[2.0,1.0)", "(2.0,1.0]", "(1.0,2.0)", "(1.0.0-a,1.0.0-A)", "[1.0.0-a,1.0.0-A]", "(1.0.0+a,1.0.0+b)", "[1.0,1.0.0.0]", "[1.0.0,1.0.0+x]", "[2.0,1.0]", "(2.0,1.0)", "[2.0,1.0)",
    "[1.0,2.0", "1.0,2.0)", "[1.0;2.0]", "[1.0 2.0]", "[1.0,2.0,3.0]", "[1.0,2.0)x", "x[1.0,2.0)", "[1.0,2.0))", "[[1.0,2.0)",
    "(1.0)", "(1.0]", "[1.0)", "()", "[]", "[", "(", ")", "]", ",", "[,", ",]", "",
    " ", "  1.0  ", "\t[1.0,2.0)\t", " [1.0,2.0) ", "[1.0,2.0) ", "[a]", "[1.0,a]", "[a,1.0]", "a", "~1.0", ">=1.0", "^1.0",
    "[1.0.*]", "1.0.*", "*", "1.*", "[1.*,)", "[*,)", "(1.0.0-*,)", "1.0.0-*", "1.0.0-beta*", "[1.0,2.*]", "1.0.0-*+meta",
    "[1.0.0-beta.1,2.0.0-rc.1]", "[1.0.0-beta+x,2.0.0+y)", "(,1.0.0-beta.1]", "(1.0.0-a.b,)", "[1.0.0-a,1.0.0-b]",
    "[01.0,02.0)", "[1.0.0.0,2.0.0.0)", "(1.0.0.0,)", "[2147483648,)", "[1.0,2147483648)", "[1.0,2147483647]",
    "(,2.0.0+meta)", "[1.0.0+meta,)", "[1.0.0-ci.5,)", "(1.0.0,2.0.0-beta.2+sha)",
    "[1.0, 2.0)", "[1.0,\t2.0)", "[1.0,\n2.0)", "(  ,  1.0  ]", "[  1.0  ,  )", "[1.0 ,2.0)",
    "1.0 ", " 1.0", "1.0\n", "1. 0", "1 .0", "1.0 [", "[1.0]]", "[1.0]x", "[[1.0]", "[1.0,,2.0]", "[,,]", "[1.0,2.0]+m",
};
rangeInputs = rangeInputs.Distinct(StringComparer.Ordinal).ToList();

// Every pair of a handful of bounds, with all four bracket combinations.
var bounds = new[] { "", "1.0", "1.0.0-beta", "1.0.0-beta.1", "2.0", "2.0.0+m", "3.0.0-rc.1" };
foreach (var lo in new[] { "[", "(" })
{
    foreach (var hi in new[] { "]", ")" })
    {
        foreach (var a in bounds)
        {
            foreach (var b in bounds)
            {
                rangeInputs.Add(lo + a + "," + b + hi);
            }
        }
    }
}
rangeInputs = rangeInputs.Distinct(StringComparer.Ordinal).ToList();

var rangeEntries = new List<object>();
foreach (var input in rangeInputs)
{
    // Dependency ranges are never floating: allowFloating is false (Q-NUGET-20).
    if (!VersionRange.TryParse(input, false, out var r) || r.IsFloating)
    {
        rangeEntries.Add(new { input, valid = false });
        continue;
    }
    var satisfies = probes.Where(p => r.Satisfies(NuGetVersion.Parse(p))).ToArray();
    var semver2 = (r.MinVersion != null && IsSemVer2(r.MinVersion)) || (r.MaxVersion != null && IsSemVer2(r.MaxVersion));
    rangeEntries.Add(new
    {
        input,
        valid = true,
        normalized = r.ToNormalizedString(),
        min = r.MinVersion?.ToFullString(),
        minInclusive = r.IsMinInclusive,
        max = r.MaxVersion?.ToFullString(),
        maxInclusive = r.IsMaxInclusive,
        semver2,
        satisfies,
    });
}

// ---- IDs ------------------------------------------------------------------------------------

// R-NUGET-003. \z instead of $: the trailing line feed that $ would let through is refused.
var localId = new Regex(@"^[A-Za-z0-9_]+([.-][A-Za-z0-9_]+)*\z", RegexOptions.CultureInvariant);
var upstreamId = new Regex(@"^\w+([.-]\w+)*\z", RegexOptions.CultureInvariant);

var idInputs = new List<string>
{
    "Acme.Widgets", "acme.widgets", "ACME.WIDGETS", "A", "a", "_", "_a", "a_", "a_b", "__", "0", "0a", "9.9", "a-b", "a.b-c.d",
    "Newtonsoft.Json", "System.Text.Json", "Microsoft.Extensions.Logging.Abstractions", "xunit.runner.visualstudio", "A.B.C.D.E.F",
    "", " ", " a", "a ", "a b", "a\tb", "a\n", "a\r\n", "\na", "a..b", ".a", "a.", "-a", "a-", "a.-b", "a-.b", "a--b", "a.-", "-", ".", "..",
    "a/b", "a\\b", "a@b", "a:b", "a+b", "a,b", "a;b", "a*b", "a?b", "a%b", "a#b", "a$b", "a!b", "a'b", "a\"b", "a<b", "a=b", "a~b", "a`b", "a|b", "a^b", "a&b", "a(b)", "a[b]", "a{b}",
    "Müller.Logging", "müller.logging", "MÜLLER.LOGGING", "Ünï", "ß", "ẞ", "straße", "STRASSE", "İstanbul", "ıstanbul", "Kelvin", "Kelvin", "ǅ", "ǆ", "Ǆ",
    "日本語", "日本.語", "Привет", "ПРИВЕТ", "عربى", "ελληνικά", "हिन्दी", "हिंदी", "ก็", "é", "́e", "á", "à.b",
    "aः", "a·b", "aⅠ", "Ⅷ", "a²", "a٣", "٣a", "a٣", "a‿", "a⁀b", "‿", "a⸗b", "a・b", "a‍b", "a‌b", "a﻿b", "a­b",
    "a b", "a b", "a　b", "a€b", "a™", "a©", "a\u0000", "a\u0007", "a\u007f", "a\u0085",
    "\U00010400", "a\U00010400", "\U0001d4d0", "a\U0001f600", "𝒜",
    new string('a', 99), new string('a', 100), new string('a', 101), new string('a', 200),
    string.Join(".", Enumerable.Repeat("ab", 33)), string.Join(".", Enumerable.Repeat("ab", 34)),
    new string('ä', 100), new string('ä', 101),
};
idInputs = idInputs.Distinct(StringComparer.Ordinal).ToList();

var idEntries = idInputs.Select(id => new
{
    input = id,
    local = id.Length <= 100 && localId.IsMatch(id),
    upstream = id.Length <= 100 && upstreamId.IsMatch(id),
    // The ASCII-only lowercase of R-NUGET-003; .NET's invariant lowercase differs only outside ASCII.
    lower = new string(id.Select(c => c is >= 'A' and <= 'Z' ? (char)(c + 32) : c).ToArray()),
}).ToList();

// ---- output ---------------------------------------------------------------------------------

var doc = new
{
    generator = new
    {
        library = "NuGet.Versioning",
        version = typeof(NuGetVersion).Assembly.GetName().Version?.ToString(),
        informational = typeof(NuGetVersion).Assembly
            .GetCustomAttributes(typeof(System.Reflection.AssemblyInformationalVersionAttribute), false)
            .Cast<System.Reflection.AssemblyInformationalVersionAttribute>()
            .Select(a => a.InformationalVersion.Split('+')[0])
            .FirstOrDefault(),
    },
    probes,
    versions = versionEntries,
    ranges = rangeEntries,
    ids = idEntries,
};

var options = new JsonSerializerOptions
{
    WriteIndented = false,
    Encoder = System.Text.Encodings.Web.JavaScriptEncoder.UnsafeRelaxedJsonEscaping,
};

// One entry per line keeps the committed file reviewable and a regeneration diff readable.
var sb = new StringBuilder();
sb.Append("{\n");
sb.Append("\"generator\": ").Append(JsonSerializer.Serialize(doc.generator, options)).Append(",\n");
sb.Append("\"probes\": ").Append(JsonSerializer.Serialize(doc.probes, options)).Append(",\n");
void Section(string name, IEnumerable<object> entries, bool last)
{
    sb.Append('"').Append(name).Append("\": [\n");
    var list = entries.ToList();
    for (var i = 0; i < list.Count; i++)
    {
        sb.Append(JsonSerializer.Serialize(list[i], options)).Append(i + 1 < list.Count ? ",\n" : "\n");
    }
    sb.Append(last ? "]\n" : "],\n");
}
Section("versions", versionEntries, false);
Section("ranges", rangeEntries, false);
Section("ids", idEntries.Cast<object>(), true);
sb.Append("}\n");

var stdout = Console.OpenStandardOutput();
var bytes = new UTF8Encoding(false).GetBytes(sb.ToString());
stdout.Write(bytes, 0, bytes.Length);
