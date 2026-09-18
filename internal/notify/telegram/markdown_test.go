package telegram

import (
	"strings"
	"testing"
)

func TestMarkdownHTML(t *testing.T) {
	md := strings.Join([]string{
		"## Done: **tests** pass",
		"",
		"Changed `a<b>.go` and *two* *files*, see [the PR](https://github.com/x/y/pull/1?a=1&b=2).",
		"- first, with __init__ left alone",
		"  * nested ~~gone~~",
		"> quoted **bit**",
		"",
		"| a | b |",
		"|---|---|",
		"| 1 | 2 |",
		"---",
		"```go",
		"if a < b && *p {",
		"```",
		"2 * 3 * 4 and a lone *",
		"```",
		"cut short <here>",
	}, "\n")
	want := strings.Join([]string{
		"<b>Done: tests pass</b>",
		"",
		`Changed <code>a&lt;b&gt;.go</code> and <i>two</i> <i>files</i>, see <a href="https://github.com/x/y/pull/1?a=1&amp;b=2">the PR</a>.`,
		"• first, with __init__ left alone",
		"  • nested <s>gone</s>",
		"<blockquote>quoted <b>bit</b></blockquote>",
		"",
		"<pre>| a | b |\n|---|---|\n| 1 | 2 |</pre>",
		"⎯⎯⎯",
		`<pre><code class="language-go">if a &lt; b &amp;&amp; *p {</code></pre>`,
		"2 * 3 * 4 and a lone *",
		"<pre>cut short &lt;here&gt;</pre>",
	}, "\n")
	if got := markdownHTML(md); got != want {
		t.Errorf("got\n%s\n\nwant\n%s", got, want)
	}
}
