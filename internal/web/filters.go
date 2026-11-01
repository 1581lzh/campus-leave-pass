package web

import "strings"

// FilterOption is one choice in a select filter.
type FilterOption struct {
	Value    string
	Label    string
	Selected bool
}

// FilterField describes one control rendered by a FilterBar.
type FilterField struct {
	Kind        string // "text", "select", or "date"
	Name        string
	Label       string
	Value       string
	Placeholder string
	Help        string
	Options     []FilterOption
	Span        int // 2 spans two grid columns on wide screens
	MaxLength   int
	InputMode   string
}

// FilterHidden is a hidden input, typically a CSRF token or fixed scope value.
type FilterHidden struct {
	Name  string
	Value string
}

// FilterBar is the view model for templates/partials/filter_bar.html.
type FilterBar struct {
	Action      string
	Method      string // defaults to GET
	SubmitLabel string // defaults to 筛选
	ResetURL    string
	Fields      []FilterField
	Hidden      []FilterHidden

	// Collapsible renders the form inside a native <details> panel. The
	// summary is a normal button, so the panel also works without JavaScript.
	Collapsible  bool
	Open         bool
	SummaryLabel string // defaults to 筛选
	ActiveCount  int    // number of filters currently applied
}

// Normalized returns a copy with the template defaults applied. The partial
// calls this method so a zero-value Method or SubmitLabel is still safe.
func (b FilterBar) Normalized() FilterBar {
	b.Method = strings.ToLower(strings.TrimSpace(b.Method))
	if b.Method == "" {
		b.Method = "get"
	}
	b.SubmitLabel = strings.TrimSpace(b.SubmitLabel)
	if b.SubmitLabel == "" {
		b.SubmitLabel = "筛选"
	}
	b.SummaryLabel = strings.TrimSpace(b.SummaryLabel)
	if b.SummaryLabel == "" {
		b.SummaryLabel = "筛选"
	}
	return b
}

// NewFilterBar builds a filter bar with readable defaults.
func NewFilterBar(action string, fields ...FilterField) FilterBar {
	return FilterBar{Action: action, Fields: fields}.Normalized()
}

// TextFilter builds a text input field.
func TextFilter(name, label, value, placeholder string) FilterField {
	return FilterField{
		Kind: "text", Name: name, Label: label,
		Value: value, Placeholder: placeholder,
	}
}

// SelectFilter builds a select field with the supplied choices.
func SelectFilter(name, label, value string, options ...FilterOption) FilterField {
	return FilterField{
		Kind: "select", Name: name, Label: label,
		Value: value, Options: options,
	}
}

// DateFilter builds a date input field.
func DateFilter(name, label, value string) FilterField {
	return FilterField{Kind: "date", Name: name, Label: label, Value: value}
}

// Option builds a select choice.
func Option(value, label string) FilterOption {
	return FilterOption{Value: value, Label: label}
}

// OptionIf builds a select choice and marks it selected when selected is true.
func OptionIf(value, label string, selected bool) FilterOption {
	return FilterOption{Value: value, Label: label, Selected: selected}
}

// WithSelected returns the option with its selected state set.
func (o FilterOption) WithSelected(selected bool) FilterOption {
	o.Selected = selected
	return o
}

// WithHelp returns the field with helper text below the control.
func (f FilterField) WithHelp(help string) FilterField {
	f.Help = help
	return f
}

// WithSpan returns the field with its wide-screen column span set.
func (f FilterField) WithSpan(span int) FilterField {
	f.Span = span
	return f
}

// WithMaxLength returns the field with a maximum text length.
func (f FilterField) WithMaxLength(n int) FilterField {
	f.MaxLength = n
	return f
}

// WithInputMode returns the field with an inputmode hint.
func (f FilterField) WithInputMode(mode string) FilterField {
	f.InputMode = mode
	return f
}

// WithCollapsible renders the filter bar inside a collapsed details panel.
// activeCount is shown in the summary so users can see that filters are set
// even while the panel is closed.
func (b FilterBar) WithCollapsible(activeCount int) FilterBar {
	b.Collapsible = true
	b.ActiveCount = activeCount
	return b
}
