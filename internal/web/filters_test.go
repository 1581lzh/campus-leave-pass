package web

import (
	"bytes"
	"html/template"
	"strings"
	"testing"
)

// renderFilterBarMarkup executes the real embedded partial so these tests
// cover the same template the pages use.
func renderFilterBarMarkup(t *testing.T, bar FilterBar) string {
	t.Helper()
	tpl, err := template.New("filter_bar_test").ParseFS(assets, "templates/partials/filter_bar.html")
	if err != nil {
		t.Fatalf("parse filter_bar partial: %v", err)
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "filter_bar", bar); err != nil {
		t.Fatalf("execute filter_bar: %v", err)
	}
	return buf.String()
}

func TestFilterBarDefaults(t *testing.T) {
	out := renderFilterBarMarkup(t, FilterBar{
		Action: "/staff/leaves",
		Fields: []FilterField{TextFilter("q", "关键词", "", "")},
	})

	for _, want := range []string{
		`<form method="get" action="/staff/leaves" class="filters">`,
		`<button class="btn btn-primary" type="submit">筛选</button>`,
		`<input type="text" name="q" value="">`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("default markup missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, `href="/staff/leaves"`) {
		t.Fatalf("reset link rendered without ResetURL:\n%s", out)
	}
}

func TestFilterBarNormalizesMethodAndSubmitLabel(t *testing.T) {
	out := renderFilterBarMarkup(t, FilterBar{
		Action:      "/leaves",
		Method:      " POST ",
		SubmitLabel: "  ",
	})

	if !strings.Contains(out, `<form method="post" action="/leaves" class="filters">`) {
		t.Fatalf("method was not normalized:\n%s", out)
	}
	if !strings.Contains(out, `type="submit">筛选</button>`) {
		t.Fatalf("empty submit label should default to 筛选:\n%s", out)
	}
}

func TestFilterBarSelectedOptions(t *testing.T) {
	out := renderFilterBarMarkup(t, NewFilterBar("/leaves",
		SelectFilter("status", "状态", "approved",
			Option("pending", "待审批"),
			Option("approved", "已批准").WithSelected(true),
			OptionIf("rejected", "已驳回", false),
		),
	))

	for _, want := range []string{
		`<option value="pending">待审批</option>`,
		`<option value="approved" selected>已批准</option>`,
		`<option value="rejected">已驳回</option>`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("selected option markup missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, `<option value="pending" selected>`) ||
		strings.Contains(out, `<option value="rejected" selected>`) {
		t.Fatalf("unselected option received selected attribute:\n%s", out)
	}
}

func TestFilterBarEscapesUserControlledValues(t *testing.T) {
	out := renderFilterBarMarkup(t, FilterBar{
		Action:      "/x?a=1&b=2",
		SubmitLabel: `<script>alert(1)</script>`,
		ResetURL:    "/x?reset=1&ok=2",
		Hidden: []FilterHidden{
			{Name: `scope"`, Value: `"><script>alert(2)</script>`},
		},
		Fields: []FilterField{
			TextFilter(`q"`, `<b>学生</b>`, `"><script>alert(3)</script>`, `"quoted" & <name>`).
				WithHelp(`<img src=x onerror=alert(4)>`),
			SelectFilter("status", "状态", `<script>`,
				Option(`a"`, `<i>甲</i>`),
			),
		},
	})

	for _, raw := range []string{"<script>", "<b>", "<i>", "<img"} {
		if strings.Contains(out, raw) {
			t.Fatalf("unescaped %q found in rendered markup:\n%s", raw, out)
		}
	}
	for _, want := range []string{
		`&lt;script&gt;alert(1)&lt;/script&gt;`,
		`&lt;b&gt;学生&lt;/b&gt;`,
		`&lt;i&gt;甲&lt;/i&gt;`,
		`&lt;img src=x onerror=alert(4)&gt;`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("escaped markup missing %q:\n%s", want, out)
		}
	}
}

func TestFilterBarAttributesHiddenOrderAndReset(t *testing.T) {
	out := renderFilterBarMarkup(t, FilterBar{
		Action:   "/leaves",
		ResetURL: "/leaves",
		Hidden: []FilterHidden{
			{Name: "csrf", Value: "token"},
			{Name: "scope", Value: "all"},
		},
		Fields: []FilterField{
			TextFilter("student", "学生", "张三", "完整学号或姓名").
				WithSpan(2).
				WithMaxLength(32).
				WithInputMode("search").
				WithHelp("可留空"),
			DateFilter("from", "开始日期", "2026-03-01"),
		},
	})

	for _, want := range []string{
		`<input type="hidden" name="csrf" value="token">`,
		`<input type="hidden" name="scope" value="all">`,
		`<label class="field span-2">`,
		`placeholder="完整学号或姓名"`,
		`maxlength="32"`,
		`inputmode="search"`,
		`<small>可留空</small>`,
		`<input type="date" name="from" value="2026-03-01">`,
		`<a class="btn" href="/leaves">重置</a>`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("markup missing %q:\n%s", want, out)
		}
	}

	hiddenAt := strings.Index(out, `name="csrf"`)
	fieldAt := strings.Index(out, `name="student"`)
	if hiddenAt < 0 || fieldAt < 0 || hiddenAt > fieldAt {
		t.Fatalf("hidden inputs should render before fields:\n%s", out)
	}
}
