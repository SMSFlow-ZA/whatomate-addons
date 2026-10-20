package delivery

// MinimalSMSNotice intentionally excludes case references, narratives, employee
// names and category. The recipient must return to an approved protected channel.
const MinimalSMSNotice = "An employee relations update is available. Please use your approved employee relations channel for assistance."

// SMSFallbackConfig names an operator-approved route. Nil/empty configuration
// disables fallback. No failed, unknown or expired SMS attempt retries itself.
type SMSFallbackConfig struct{ Account string }
