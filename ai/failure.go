package ai

import "errors"

// Stage errors are bounded internal classifications, never raw model data.
var errSupportingQuoteInvalid = errors.New("invalid supporting quote")

type supportingQuoteError struct{}

func (supportingQuoteError) Error() string   { return "AI evidence selection failed" }
func (supportingQuoteError) Unwrap() []error { return []error{ErrProvider, errSupportingQuoteInvalid} }
func FailureStage(err error) string {
	if errors.Is(err, errSupportingQuoteInvalid) {
		return "supporting_quote_invalid"
	}
	return ""
}
